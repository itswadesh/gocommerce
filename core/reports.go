package gocommerce

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"sync"
	"time"
)

// What the store sold.
//
// This is a reading of the orders, the way Customers is: it owns no table, it
// writes nothing, and it emits no event because it changes no state (rules 3
// and 4 have nothing to commit alongside). Each answer is one statement, so no
// transaction is opened either — a read needs none, and every statement is its
// own snapshot.
//
// A rollup table was declined for the reason M17 already records about storing
// a stock sum: a stored copy of a sum is a number that can be wrong, and this
// one would have to be rebuilt on EditLines, MarkPaid, MarkUnpaid, Cancel,
// Refund and RecalculateOrderDiscount. The cost of that honesty is that a
// report is a current reading of history rather than an immutable statement —
// marking an old order paid changes a past bucket, and that is said out loud in
// skills/reports.md rather than hidden.

const (
	GrainDay   = "day"
	GrainWeek  = "week" // ISO: Monday-based, because that is date_trunc's definition
	GrainMonth = "month"
)

// A report is not a paginated list, so the bound is on the window rather than
// on a page. 400 is a little over a year of days and thirty years of months;
// past that the refusal names a coarser grain instead of drawing a chart
// nobody can read.
const maxReportBuckets = 400

// The SHORTEST a bucket of each grain can be. Dividing the span by the shortest
// bucket over-counts, so the guard refuses a little early rather than admitting
// more buckets than the contract promises — using the average month (31 days)
// would let ~408 month buckets through a cap that says 400.
var grainMinSpan = map[string]time.Duration{
	GrainDay:   23 * time.Hour,
	GrainWeek:  7*24*time.Hour - time.Hour,
	GrainMonth: 28 * 24 * time.Hour,
}

// saleStatuses is what counts as a sale, and it is stockCommitted's set — the
// order's inventory has left the shelf, as opposed to merely being reserved.
// Written here as data and handed to SQL as a parameter so the two spellings
// cannot drift; TestReportSalePredicateMatchesStockCommitted asserts they agree
// status by status.
//
// Not `payment_status = 'paid'`: cash on delivery is the only built-in method,
// it confirms the order at checkout while payment stays pending, so a paid-only
// report shows a stock GoCommerce store zero revenue beside a non-zero order
// count. Not `status <> 'cancelled'` either: that counts the in-flight pending
// checkouts that orders_unpaid_idx exists to find and SweepUnpaid cancels an
// OrderTTL later, so today's takings would inflate and tomorrow's deflate with
// nobody touching anything.
//
// `partial` is in the set and its absence would be a silent money bug: a partly
// shipped order reached that state from confirmed, every one of its units is
// off the shelf, and dropping it would make an order vanish from revenue at the
// moment the first parcel went out.
var saleStatuses = []string{OrderConfirmed, OrderPartial, OrderShipped, OrderDelivered}

// ReportBound is an instant or a civil date. A date means nothing until a zone
// is chosen, so it is resolved in the report's zone rather than in UTC — which
// is why this is not parseDate's *time.Time.
type ReportBound struct {
	At   *time.Time
	Date string
}

func (b ReportBound) args() (any, any) {
	var at, date any
	if b.At != nil {
		at = *b.At
	}
	if b.Date != "" {
		date = b.Date
	}
	return at, date
}

// SalesQuery is one reading of the order book.
type SalesQuery struct {
	From, To ReportBound
	GroupBy  string
	TimeZone string
}

// TopProductsQuery ranks the lines of the same sale set.
type TopProductsQuery struct {
	From, To      ReportBound
	TimeZone      string
	Currency      string
	By            string // "product" (default) or "variant"
	Sort          string // "revenue" (default) or "units"
	Limit, Offset int
}

// SalesSubset is one slice of the sale set, cut by payment status.
type SalesSubset struct {
	Orders int   `json:"orders"`
	Total  Money `json:"total"`
}

// PaidSales is the settled slice, which needs two more figures than the others.
//
// D36 made partiality a number rather than a status: an order that has had some
// of its money sent back is still `paid`, and its whole total would otherwise
// sit in this subset as if the store still held it. Refunded is refunded_minor
// summed over exactly these rows and NetCollected is what is left — the only
// figure here that answers "how much of this is still ours".
type PaidSales struct {
	Orders       int   `json:"orders"`
	Total        Money `json:"total"`
	Refunded     Money `json:"refunded"`
	NetCollected Money `json:"net_collected"`
}

// ExcludedSales is what was placed in the window and is not a sale: orders
// still in checkout, and orders that were cancelled. Reported beside the
// totals, never inside them, so an operator comparing the report with the
// Orders list can see where the difference went.
type ExcludedSales struct {
	Pending   SalesSubset `json:"pending"`
	Cancelled SalesSubset `json:"cancelled"`
}

// SalesBucket is one calendar day, ISO week or month of the window.
type SalesBucket struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	// Partial marks a first or last bucket the window clips, which is what
	// stops "sales are down today" being read off three hours of trading.
	Partial     bool          `json:"partial,omitempty"`
	Orders      int           `json:"orders"`
	Units       int           `json:"units"`
	Items       Money         `json:"items"`
	Discount    Money         `json:"discount"`
	Shipping    Money         `json:"shipping"`
	Tax         Money         `json:"tax"`
	Net         Money         `json:"net"`
	Total       Money         `json:"total"`
	Paid        PaidSales     `json:"paid"`
	Outstanding SalesSubset   `json:"outstanding"`
	Refunded    SalesSubset   `json:"refunded"`
	Excluded    ExcludedSales `json:"excluded"`
}

// SalesTotals is the window, added up from its own buckets.
type SalesTotals struct {
	Orders       int           `json:"orders"`
	Units        int           `json:"units"`
	Items        Money         `json:"items"`
	Discount     Money         `json:"discount"`
	Shipping     Money         `json:"shipping"`
	Tax          Money         `json:"tax"`
	Net          Money         `json:"net"`
	Total        Money         `json:"total"`
	AverageOrder Money         `json:"average_order"`
	Paid         PaidSales     `json:"paid"`
	Outstanding  SalesSubset   `json:"outstanding"`
	Refunded     SalesSubset   `json:"refunded"`
	Excluded     ExcludedSales `json:"excluded"`
}

// CurrencySales is one currency's block. Nothing is ever summed across two of
// them: minor units of different currencies add up to a number that is not
// money.
type CurrencySales struct {
	Currency string        `json:"currency"`
	Totals   SalesTotals   `json:"totals"`
	Buckets  []SalesBucket `json:"buckets"`
}

// SalesReport is the whole answer. The timestamps are instants; format them in
// TimeZone, which is the zone the buckets were cut in.
type SalesReport struct {
	From       time.Time        `json:"from"`
	To         time.Time        `json:"to"`
	GroupBy    string           `json:"group_by"`
	TimeZone   string           `json:"time_zone"`
	Currencies []*CurrencySales `json:"currencies"`
}

// ProductSales is one row of the best-seller ranking.
type ProductSales struct {
	ProductID    *int64 `json:"product_id"`
	VariantID    *int64 `json:"variant_id,omitempty"`
	SKU          string `json:"sku"`
	Title        string `json:"title"`
	VariantLabel string `json:"variant_label,omitempty"`
	Units        int    `json:"units"`
	Orders       int    `json:"orders"`
	Revenue      Money  `json:"revenue"`
	Tax          Money  `json:"tax"`
}

// Reports reads the orders. It owns no table, writes nothing and holds no
// transaction; the only state it carries is the zone catalogue, cached because
// the tz database cannot change while this process runs.
type Reports struct {
	app   *App
	mu    sync.Mutex
	zones map[string]string // lower(name) -> canonical name; nil until loaded
}

// Reports answers what the store sold.
func (a *App) Reports() *Reports { return a.reports }

// parseReportBound reads a civil date or an instant, and keeps them apart.
func parseReportBound(s string) (ReportBound, error) {
	if s == "" {
		return ReportBound{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return ReportBound{At: &t}, nil
	}
	if _, err := time.Parse("2006-01-02", s); err == nil {
		// Kept as text rather than as a time: midnight on this date is a
		// different instant in every zone, and the zone is not known here.
		return ReportBound{Date: s}, nil
	}
	return ReportBound{}, Validationf("%q is not a date (use YYYY-MM-DD or RFC 3339)", s)
}

// zone canonicalises an IANA zone name against the database's own catalogue.
//
// The tz database lives in PostgreSQL, which already carries it and already
// keeps it current. Go's time/tzdata would be a second, drifting copy — either
// ~450KB embedded in the binary or a host zoneinfo directory the container may
// not have — and rule 1 declines a second copy of something we already have.
//
// Loaded once because the names cannot change while this process runs, and
// under a mutex rather than a sync.Once so a transient failure retries instead
// of poisoning the process.
func (r *Reports) zone(ctx context.Context, name string) (string, error) {
	if name == "" {
		return "UTC", nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.zones == nil {
		rows, err := r.app.db.QueryContext(ctx, `SELECT name FROM pg_timezone_names`)
		if err != nil {
			return "", Internalf(err, "read the time zone catalogue")
		}
		defer rows.Close()
		loaded := map[string]string{}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				return "", Internalf(err, "read the time zone catalogue")
			}
			loaded[strings.ToLower(n)] = n
		}
		if err := rows.Err(); err != nil {
			return "", Internalf(err, "read the time zone catalogue")
		}
		r.zones = loaded
	}

	canonical, ok := r.zones[strings.ToLower(name)]
	if !ok {
		return "", Validationf("%q is not a time zone this database knows", name)
	}
	return canonical, nil
}

// window resolves both edges of the range in the report's own zone.
//
// One statement, touching no table: it is constant expressions and now().
// Resolving the edges here rather than in the aggregate is what lets the bucket
// guard see the span before 1,800 buckets are built, and a civil bound and a
// bucket boundary have to be the same midnight — measured against different
// ones the first and last buckets are quietly partial and nobody can tell.
func (r *Reports) window(ctx context.Context, tz string, from, to ReportBound) (time.Time, time.Time, error) {
	toAt, toDate := to.args()
	fromAt, fromDate := from.args()

	var lo, hi time.Time
	// The 30-day default is subtracted in the report's zone, not the session's,
	// for the same reason the bounds are resolved there.
	err := r.app.db.QueryRowContext(ctx, `
		WITH hi AS (
		    SELECT coalesce($1::timestamptz,
		                    ($2::date)::timestamp AT TIME ZONE $5::text,
		                    now()) AS at
		)
		SELECT coalesce($3::timestamptz,
		                ($4::date)::timestamp AT TIME ZONE $5::text,
		                ((hi.at AT TIME ZONE $5::text) - interval '30 days') AT TIME ZONE $5::text),
		       hi.at
		  FROM hi`,
		toAt, toDate, fromAt, fromDate, tz).Scan(&lo, &hi)
	if err != nil {
		return time.Time{}, time.Time{}, Internalf(err, "resolve the report window")
	}
	if !lo.Before(hi) {
		return time.Time{}, time.Time{}, Validationf("from must be before to")
	}
	return lo, hi, nil
}

func tooManyBuckets(lo, hi time.Time, grain string) bool {
	shortest, ok := grainMinSpan[grain]
	// Every caller validates the grain first; refusing rather than dividing by a
	// zero duration is what keeps a future one from panicking instead.
	if !ok {
		return true
	}
	return int(hi.Sub(lo)/shortest)+1 > maxReportBuckets
}

// averageMinor divides money by a count, half up.
//
// Integer arithmetic, because no float goes near money (rule 6). No negative
// case: every money column carries a CHECK >= 0 and net cannot go negative
// either, since the discount is capped at the subtotal in both the apply and
// the recalculate path.
func averageMinor(total int64, orders int) int64 {
	if orders <= 0 {
		return 0
	}
	return (total + int64(orders)/2) / int64(orders)
}

// Sales adds up the order book, bucketed, in one statement per currency block.
func (r *Reports) Sales(ctx context.Context, q SalesQuery) (*SalesReport, error) {
	grain := q.GroupBy
	if grain == "" {
		grain = GrainDay
	}
	if _, ok := grainMinSpan[grain]; !ok {
		return nil, Validationf("group_by must be %s, %s or %s", GrainDay, GrainWeek, GrainMonth)
	}
	tz, err := r.zone(ctx, q.TimeZone)
	if err != nil {
		return nil, err
	}
	lo, hi, err := r.window(ctx, tz, q.From, q.To)
	if err != nil {
		return nil, err
	}
	if tooManyBuckets(lo, hi, grain) {
		return nil, Validationf(
			"that window is more than %d %s buckets; ask for a coarser group_by",
			maxReportBuckets, grain)
	}

	// Three things in here are not obvious:
	//
	//   AT TIME ZONE $4 on every truncation, because date_trunc over a
	//   timestamptz follows the SERVER's zone — the buckets would otherwise
	//   change meaning with a PostgreSQL setting nobody in this repository set.
	//
	//   generate_series runs in naive local time with a calendar step, because
	//   adding interval '1 day' to a timestamptz adds exactly 24 hours and
	//   drifts an hour across a DST boundary, and a month is its own length.
	//   Converting each boundary back with AT TIME ZONE is what makes
	//   bucket[n].End == bucket[n+1].Start true for every n.
	//
	//   `seen` unions the store's configured currency, because a store with no
	//   sales in the window still has one, and an empty response looks broken
	//   rather than quiet.
	//
	// The grain is a whitelisted constant passed as a parameter, so the SQL
	// text is fixed and nothing is interpolated from caller text.
	rows, err := r.app.db.QueryContext(ctx, `
		WITH placed AS (
		    SELECT o.id, o.currency,
		           date_trunc($3::text, o.created_at AT TIME ZONE $4::text) AS bucket,
		           o.status, o.payment_status, o.tax_inclusive,
		           o.subtotal_minor, o.discount_minor, o.shipping_minor,
		           o.tax_minor, o.total_minor, o.refunded_minor,
		           o.status = ANY($6::text[]) AS sale
		      FROM orders o
		     WHERE o.created_at >= $1 AND o.created_at < $2
		),
		agg AS (
		    SELECT currency, bucket,
		           count(*)                     FILTER (WHERE sale) AS orders,
		           coalesce(sum(subtotal_minor) FILTER (WHERE sale), 0) AS items,
		           coalesce(sum(discount_minor) FILTER (WHERE sale), 0) AS discount,
		           coalesce(sum(shipping_minor) FILTER (WHERE sale), 0) AS shipping,
		           coalesce(sum(tax_minor)      FILTER (WHERE sale), 0) AS tax,
		           coalesce(sum(subtotal_minor - discount_minor
		                        - CASE WHEN tax_inclusive THEN tax_minor ELSE 0 END)
		                                        FILTER (WHERE sale), 0) AS net,
		           coalesce(sum(total_minor)    FILTER (WHERE sale), 0) AS total,
		           count(*)                     FILTER (WHERE sale AND payment_status = 'paid') AS paid_orders,
		           coalesce(sum(total_minor)    FILTER (WHERE sale AND payment_status = 'paid'), 0) AS paid_total,
		           coalesce(sum(refunded_minor) FILTER (WHERE sale AND payment_status = 'paid'), 0) AS paid_refunded,
		           count(*)                     FILTER (WHERE sale AND payment_status IN ('pending','failed')) AS owed_orders,
		           coalesce(sum(total_minor)    FILTER (WHERE sale AND payment_status IN ('pending','failed')), 0) AS owed_total,
		           count(*)                     FILTER (WHERE sale AND payment_status = 'refunded') AS refunded_orders,
		           coalesce(sum(total_minor)    FILTER (WHERE sale AND payment_status = 'refunded'), 0) AS refunded_total,
		           count(*)                     FILTER (WHERE status = 'pending') AS open_orders,
		           coalesce(sum(total_minor)    FILTER (WHERE status = 'pending'), 0) AS open_total,
		           count(*)                     FILTER (WHERE status = 'cancelled') AS cancelled_orders,
		           coalesce(sum(total_minor)    FILTER (WHERE status = 'cancelled'), 0) AS cancelled_total
		      FROM placed
		     GROUP BY currency, bucket
		),
		units AS (
		    SELECT p.currency, p.bucket, sum(l.quantity) AS units
		      FROM placed p
		      JOIN order_lines l ON l.order_id = p.id
		     WHERE p.sale
		     GROUP BY p.currency, p.bucket
		),
		seen AS (
		    SELECT DISTINCT currency FROM placed
		    UNION
		    SELECT $5::text
		),
		grid AS (
		    SELECT s.currency, g.bucket
		      FROM seen s
		      CROSS JOIN generate_series(
		          date_trunc($3::text, $1::timestamptz AT TIME ZONE $4::text),
		          date_trunc($3::text, ($2::timestamptz - interval '1 microsecond') AT TIME ZONE $4::text),
		          ('1 ' || $3::text)::interval) AS g(bucket)
		)
		SELECT g.currency,
		       g.bucket AT TIME ZONE $4::text,
		       (g.bucket + ('1 ' || $3::text)::interval) AT TIME ZONE $4::text,
		       coalesce(a.orders, 0), coalesce(u.units, 0),
		       coalesce(a.items, 0), coalesce(a.discount, 0), coalesce(a.shipping, 0),
		       coalesce(a.tax, 0), coalesce(a.net, 0), coalesce(a.total, 0),
		       coalesce(a.paid_orders, 0), coalesce(a.paid_total, 0), coalesce(a.paid_refunded, 0),
		       coalesce(a.owed_orders, 0), coalesce(a.owed_total, 0),
		       coalesce(a.refunded_orders, 0), coalesce(a.refunded_total, 0),
		       coalesce(a.open_orders, 0), coalesce(a.open_total, 0),
		       coalesce(a.cancelled_orders, 0), coalesce(a.cancelled_total, 0)
		  FROM grid g
		  LEFT JOIN agg   a ON a.currency = g.currency AND a.bucket = g.bucket
		  LEFT JOIN units u ON u.currency = g.currency AND u.bucket = g.bucket
		 ORDER BY g.currency, g.bucket`,
		lo, hi, grain, tz, r.app.cfg.Currency, stringArray(saleStatuses))
	if err != nil {
		return nil, Internalf(err, "read the sales report")
	}
	defer rows.Close()

	report := &SalesReport{From: lo, To: hi, GroupBy: grain, TimeZone: tz}
	byCurrency := map[string]*CurrencySales{}
	for rows.Next() {
		var (
			currency                       string
			start, end                     time.Time
			orders, units                  int
			items, discount, shipping, tax int64
			net, total                     int64
			paidOrders                     int
			paidTotal, paidRefunded        int64
			owedOrders                     int
			owedTotal                      int64
			refundedOrders                 int
			refundedTotal                  int64
			openOrders                     int
			openTotal                      int64
			cancelledOrders                int
			cancelledTotal                 int64
		)
		if err := rows.Scan(&currency, &start, &end, &orders, &units,
			&items, &discount, &shipping, &tax, &net, &total,
			&paidOrders, &paidTotal, &paidRefunded,
			&owedOrders, &owedTotal, &refundedOrders, &refundedTotal,
			&openOrders, &openTotal, &cancelledOrders, &cancelledTotal); err != nil {
			return nil, Internalf(err, "read the sales report")
		}

		block := byCurrency[currency]
		if block == nil {
			block = &CurrencySales{Currency: currency}
			byCurrency[currency] = block
			report.Currencies = append(report.Currencies, block)
		}
		block.Buckets = append(block.Buckets, SalesBucket{
			Start:    start,
			End:      end,
			Partial:  start.Before(lo) || end.After(hi),
			Orders:   orders,
			Units:    units,
			Items:    money(items, currency),
			Discount: money(discount, currency),
			Shipping: money(shipping, currency),
			Tax:      money(tax, currency),
			Net:      money(net, currency),
			Total:    money(total, currency),
			Paid: PaidSales{
				Orders:       paidOrders,
				Total:        money(paidTotal, currency),
				Refunded:     money(paidRefunded, currency),
				NetCollected: money(paidTotal-paidRefunded, currency),
			},
			Outstanding: SalesSubset{Orders: owedOrders, Total: money(owedTotal, currency)},
			Refunded:    SalesSubset{Orders: refundedOrders, Total: money(refundedTotal, currency)},
			Excluded: ExcludedSales{
				Pending:   SalesSubset{Orders: openOrders, Total: money(openTotal, currency)},
				Cancelled: SalesSubset{Orders: cancelledOrders, Total: money(cancelledTotal, currency)},
			},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, Internalf(err, "read the sales report")
	}

	// The totals are added up from the buckets rather than queried again: a
	// stat card and the chart under it can then never disagree, whatever
	// committed between two statements.
	for _, block := range report.Currencies {
		block.Totals = sumBuckets(block.Currency, block.Buckets)
	}
	// The store's own currency first, then the rest alphabetically. An
	// operator who has never heard of multi-currency must not find their
	// figures behind a tab, and the dashboard reads the first block as the
	// store's own.
	own := r.app.cfg.Currency
	sort.Slice(report.Currencies, func(i, j int) bool {
		a, b := report.Currencies[i].Currency, report.Currencies[j].Currency
		if (a == own) != (b == own) {
			return a == own
		}
		return a < b
	})
	return report, nil
}

func sumBuckets(currency string, buckets []SalesBucket) SalesTotals {
	var t SalesTotals
	var items, discount, shipping, tax, net, total int64
	var paidTotal, paidRefunded, owedTotal, refundedTotal, openTotal, cancelledTotal int64
	for _, b := range buckets {
		t.Orders += b.Orders
		t.Units += b.Units
		items += b.Items.AmountMinor
		discount += b.Discount.AmountMinor
		shipping += b.Shipping.AmountMinor
		tax += b.Tax.AmountMinor
		net += b.Net.AmountMinor
		total += b.Total.AmountMinor
		t.Paid.Orders += b.Paid.Orders
		paidTotal += b.Paid.Total.AmountMinor
		paidRefunded += b.Paid.Refunded.AmountMinor
		t.Outstanding.Orders += b.Outstanding.Orders
		owedTotal += b.Outstanding.Total.AmountMinor
		t.Refunded.Orders += b.Refunded.Orders
		refundedTotal += b.Refunded.Total.AmountMinor
		t.Excluded.Pending.Orders += b.Excluded.Pending.Orders
		openTotal += b.Excluded.Pending.Total.AmountMinor
		t.Excluded.Cancelled.Orders += b.Excluded.Cancelled.Orders
		cancelledTotal += b.Excluded.Cancelled.Total.AmountMinor
	}
	t.Items = money(items, currency)
	t.Discount = money(discount, currency)
	t.Shipping = money(shipping, currency)
	t.Tax = money(tax, currency)
	t.Net = money(net, currency)
	t.Total = money(total, currency)
	t.AverageOrder = money(averageMinor(total, t.Orders), currency)
	t.Paid.Total = money(paidTotal, currency)
	t.Paid.Refunded = money(paidRefunded, currency)
	t.Paid.NetCollected = money(paidTotal-paidRefunded, currency)
	t.Outstanding.Total = money(owedTotal, currency)
	t.Refunded.Total = money(refundedTotal, currency)
	t.Excluded.Pending.Total = money(openTotal, currency)
	t.Excluded.Cancelled.Total = money(cancelledTotal, currency)
	return t
}

// TopProducts ranks the lines of the same sale set, within one currency.
func (r *Reports) TopProducts(ctx context.Context, q TopProductsQuery) ([]*ProductSales, int, error) {
	by := q.By
	if by == "" {
		by = "product"
	}
	if by != "product" && by != "variant" {
		return nil, 0, Validationf("by must be product or variant")
	}
	order := q.Sort
	if order == "" {
		order = "revenue"
	}
	if order != "revenue" && order != "units" {
		return nil, 0, Validationf("sort must be revenue or units")
	}
	currency := q.Currency
	if currency == "" {
		currency = r.app.cfg.Currency
	}
	tz, err := r.zone(ctx, q.TimeZone)
	if err != nil {
		return nil, 0, err
	}
	lo, hi, err := r.window(ctx, tz, q.From, q.To)
	if err != nil {
		return nil, 0, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}

	// The three fragments below are chosen in Go from a closed set and never
	// interpolated from caller text.
	//
	// The group key falls back to 'sku:' because product_id and variant_id are
	// both ON DELETE SET NULL by design — an order line is a snapshot and must
	// stay readable after the product it came from is deleted — so grouping on
	// the id alone would collapse every deleted product into one anonymous row
	// and invent a bestseller that never existed.
	//
	// The trailing `key` in the ORDER BY is what makes the order total: without
	// it LIMIT/OFFSET paging repeats and drops rows whenever two products tie.
	key, variantID, label := `coalesce(l.product_id::text, 'sku:' || l.sku)`, `NULL::bigint`, `''::text`
	if by == "variant" {
		key = `coalesce(l.variant_id::text, 'sku:' || l.sku)`
		variantID = `(array_agg(l.variant_id ORDER BY l.id DESC))[1]`
		label = `(array_agg(l.variant_label ORDER BY l.id DESC))[1]`
	}
	ranking := `revenue DESC, units DESC`
	if order == "units" {
		ranking = `units DESC, revenue DESC`
	}

	// Revenue is line value excluding tax under BOTH tax modes. The FILTER is
	// not cosmetic: order_lines.total_minor is unit_price * quantity, and under
	// inclusive pricing that price CONTAINS the tax, so the unadjusted sum
	// would report tax-inclusive revenue beside a tax column reporting the same
	// tax again — and the two modes would report different revenue for the same
	// sale. It is also BEFORE order-level discounts, which are recorded once per
	// order (D28) and never allocated to lines: apportioning them here would be
	// an invention, so these rows deliberately do not add up to the sales
	// report's net.
	//
	// count(*) OVER () keeps the total and the rows on one snapshot; a separate
	// count query can straddle a checkout commit and tell the panel 12 while
	// showing 11.
	rows, err := r.app.db.QueryContext(ctx, `
		WITH sold AS (
		    SELECT o.id, o.tax_inclusive
		      FROM orders o
		     WHERE o.created_at >= $1 AND o.created_at < $2
		       AND o.status = ANY($6::text[])
		       AND o.currency = $3
		),
		ranked AS (
		    SELECT `+key+` AS key,
		           (array_agg(l.product_id ORDER BY l.id DESC))[1] AS product_id,
		           `+variantID+` AS variant_id,
		           (array_agg(l.sku   ORDER BY l.id DESC))[1] AS sku,
		           (array_agg(l.title ORDER BY l.id DESC))[1] AS title,
		           `+label+` AS variant_label,
		           sum(l.quantity)            AS units,
		           count(DISTINCT l.order_id) AS orders,
		           sum(l.total_minor)
		             - coalesce(sum(l.tax_minor) FILTER (WHERE s.tax_inclusive), 0) AS revenue,
		           sum(l.tax_minor) AS tax
		      FROM sold s
		      JOIN order_lines l ON l.order_id = s.id
		     GROUP BY 1
		)
		SELECT key, product_id, variant_id, sku, title, variant_label,
		       units, orders, revenue, tax, count(*) OVER () AS matched
		  FROM ranked
		 ORDER BY `+ranking+`, key
		 LIMIT $4 OFFSET $5`,
		lo, hi, currency, limit, q.Offset, stringArray(saleStatuses))
	if err != nil {
		return nil, 0, Internalf(err, "read the best sellers")
	}
	defer rows.Close()

	list := []*ProductSales{}
	total := 0
	for rows.Next() {
		var (
			key                  string
			productID, variantID sql.NullInt64
			row                  ProductSales
			revenue, tax         int64
		)
		if err := rows.Scan(&key, &productID, &variantID, &row.SKU, &row.Title,
			&row.VariantLabel, &row.Units, &row.Orders, &revenue, &tax, &total); err != nil {
			return nil, 0, Internalf(err, "read the best sellers")
		}
		if productID.Valid {
			row.ProductID = &productID.Int64
		}
		if variantID.Valid {
			row.VariantID = &variantID.Int64
		}
		row.Revenue = money(revenue, currency)
		row.Tax = money(tax, currency)
		list = append(list, &row)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, Internalf(err, "read the best sellers")
	}
	return list, total, nil
}
