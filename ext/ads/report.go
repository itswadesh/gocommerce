package ads

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Row is one source, medium and campaign in one currency.
//
// Revenue is what the orders came to, Refunded what has gone back, and Net
// the difference. GrossProfit is Net less the tax inside it and less what the
// goods that stayed sold cost the shop, at the variant's cost now: the engine
// keeps no cost on an order line. A unit with no cost recorded is counted in
// UncostedUnits and costs nothing here, so a row with any is a ceiling.
// Shipping charges stay in, because what delivery cost the shop is not known.
type Row struct {
	Source             string           `json:"source"`
	Medium             string           `json:"medium"`
	Campaign           string           `json:"campaign"`
	Orders             int              `json:"orders"`
	Revenue            gocommerce.Money `json:"revenue"`
	AverageOrder       gocommerce.Money `json:"average_order"`
	Refunded           gocommerce.Money `json:"refunded"`
	Net                gocommerce.Money `json:"net"`
	Returns            int              `json:"returns"`
	ProductCost        gocommerce.Money `json:"product_cost"`
	GrossProfit        gocommerce.Money `json:"gross_profit"`
	UncostedUnits      int              `json:"uncosted_units"`
	NewCustomers       int              `json:"new_customers"`
	ReturningCustomers int              `json:"returning_customers"`
	// Spend and what follows from it are null where no spend was entered:
	// zero would say the ads were free.
	Spend        *gocommerce.Money `json:"spend"`
	ROAS         *float64          `json:"roas"`
	POAS         *float64          `json:"poas"`
	CostPerOrder *gocommerce.Money `json:"cost_per_order"`
}

// CurrencyReport is one currency's rows. Nothing is added across currencies.
type CurrencyReport struct {
	Currency string `json:"currency"`
	Totals   Row    `json:"totals"`
	Rows     []Row  `json:"rows"`
}

// Report is the ads report for a window.
type Report struct {
	From       string            `json:"from"`
	To         string            `json:"to"`
	TimeZone   string            `json:"time_zone"`
	Touch      string            `json:"touch"`
	Currencies []*CurrencyReport `json:"currencies"`
}

// OrderRow is one order behind a report row.
type OrderRow struct {
	ID            int64            `json:"id"`
	Number        string           `json:"number"`
	CreatedAt     time.Time        `json:"created_at"`
	Total         gocommerce.Money `json:"total"`
	Refunded      gocommerce.Money `json:"refunded"`
	GrossProfit   gocommerce.Money `json:"gross_profit"`
	UncostedUnits int              `json:"uncosted_units"`
	Returned      bool             `json:"returned"`
	// Customer is "new" for the shopper's first sale, "returning" otherwise.
	Customer string `json:"customer"`
}

type orderFact struct {
	ID        int64
	Number    string
	At        time.Time
	Currency  string
	Total     int64
	Tax       int64
	Refunded  int64
	Cost      int64
	Uncosted  int
	Returned  bool
	Returning bool
	Attr      *Attribution
}

// profit is what the shop keeps of the order, less the tax in it and what
// the goods cost. Tax is taken off in proportion to what was kept, because a
// refund gives back its share of the tax too.
func (o orderFact) profit() int64 {
	kept := o.Total - o.Refunded
	tax := int64(0)
	if o.Total > 0 {
		tax = o.Tax * kept / o.Total
	}
	if kept <= 0 {
		return 0
	}
	return kept - tax - o.Cost
}

type spendFact struct {
	channel  channel
	Currency string
	Amount   int64
}

type window struct {
	from, to string // civil dates, both inclusive
	tz       string
	lo, hi   time.Time
	touch    string
}

func (m *Module) parseWindow(ctx context.Context, q map[string]string) (window, error) {
	w := window{from: q["from"], to: q["to"], tz: q["tz"], touch: q["touch"]}
	if w.tz == "" {
		w.tz = "UTC"
	}
	if w.touch == "" {
		w.touch = "last"
	}
	if w.touch != "first" && w.touch != "last" {
		return w, gocommerce.Validationf("touch is first or last")
	}
	if w.to == "" {
		w.to = time.Now().UTC().Format(time.DateOnly)
	}
	to, err := time.Parse(time.DateOnly, w.to)
	if err != nil {
		return w, gocommerce.Validationf("to is a date, YYYY-MM-DD")
	}
	if w.from == "" {
		w.from = to.AddDate(0, 0, -29).Format(time.DateOnly)
	}
	from, err := time.Parse(time.DateOnly, w.from)
	if err != nil {
		return w, gocommerce.Validationf("from is a date, YYYY-MM-DD")
	}
	if from.After(to) {
		return w, gocommerce.Validationf("from must not be after to")
	}
	if to.Sub(from) > 731*24*time.Hour {
		return w, gocommerce.Validationf("a report covers at most two years")
	}
	// The window's edges are midnights in the report's zone; the database
	// knows the zones.
	err = m.app.DB().QueryRowContext(ctx,
		`SELECT ($1::date)::timestamp AT TIME ZONE $3, ($2::date + 1)::timestamp AT TIME ZONE $3`,
		w.from, w.to, w.tz).Scan(&w.lo, &w.hi)
	if err != nil {
		return w, gocommerce.Validationf("%q is not a time zone this database knows", w.tz)
	}
	return w, nil
}

// facts reads the sales placed in the window, one statement per question.
// ponytail: every order in the window is read into memory; a store selling
// hundreds of thousands a month wants this grouped in SQL.
func (m *Module) facts(ctx context.Context, w window) ([]orderFact, error) {
	db := m.app.DB()
	sales := gocommerce.SaleStatuses()
	rows, err := db.QueryContext(ctx, `
		SELECT o.id, o.number, o.created_at, o.currency, o.total_minor, o.tax_minor, o.refunded_minor,
		       lower(o.email), o.metadata->'attribution',
		       EXISTS (SELECT 1 FROM order_returns r WHERE r.order_id = o.id AND r.status = 'received')
		  FROM orders o
		 WHERE o.created_at >= $1 AND o.created_at < $2 AND o.status = ANY($3)
		 ORDER BY o.created_at, o.id`, w.lo, w.hi, sales)
	if err != nil {
		return nil, gocommerce.Internalf(err, "read the orders")
	}
	defer rows.Close()
	var out []orderFact
	var emails []string
	email := map[int64]string{}
	index := map[int64]int{}
	for rows.Next() {
		var f orderFact
		var e string
		var attr []byte
		if err := rows.Scan(&f.ID, &f.Number, &f.At, &f.Currency, &f.Total, &f.Tax, &f.Refunded, &e, &attr, &f.Returned); err != nil {
			return nil, gocommerce.Internalf(err, "read the orders")
		}
		if len(attr) > 0 {
			var a Attribution
			// Written by the shopper's checkout: a shape that does not read
			// is an order with no attribution, not a broken report.
			if json.Unmarshal(attr, &a) == nil {
				f.Attr = &a
			}
		}
		index[f.ID] = len(out)
		email[f.ID] = e
		emails = append(emails, e)
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, gocommerce.Internalf(err, "read the orders")
	}
	if len(out) == 0 {
		return out, nil
	}
	ids := make([]int64, len(out))
	for i, f := range out {
		ids[i] = f.ID
	}

	// What the goods that stayed sold cost: each line less what came back.
	cost, err := db.QueryContext(ctx, `
		SELECT l.order_id,
		       coalesce(sum((l.quantity - coalesce(back.qty, 0)) * v.cost_minor) FILTER (WHERE v.cost_minor IS NOT NULL), 0),
		       coalesce(sum(l.quantity - coalesce(back.qty, 0)) FILTER (WHERE v.cost_minor IS NULL), 0)
		  FROM order_lines l
		  LEFT JOIN variants v ON v.id = l.variant_id
		  LEFT JOIN (SELECT rl.order_line_id, sum(rl.quantity) AS qty
		               FROM order_return_lines rl
		               JOIN order_returns r ON r.id = rl.return_id AND r.status = 'received'
		              GROUP BY rl.order_line_id) back ON back.order_line_id = l.id
		 WHERE l.order_id = ANY($1)
		 GROUP BY l.order_id`, ids)
	if err != nil {
		return nil, gocommerce.Internalf(err, "read what the goods cost")
	}
	defer cost.Close()
	for cost.Next() {
		var id, c int64
		var n int
		if err := cost.Scan(&id, &c, &n); err != nil {
			return nil, gocommerce.Internalf(err, "read what the goods cost")
		}
		out[index[id]].Cost, out[index[id]].Uncosted = c, n
	}
	if err := cost.Err(); err != nil {
		return nil, gocommerce.Internalf(err, "read what the goods cost")
	}

	// A shopper is returning when they bought before the window, or earlier
	// in it. Shoppers are their email address, as on the Customers screen.
	before := map[string]bool{}
	prior, err := db.QueryContext(ctx, `
		SELECT DISTINCT lower(email) FROM orders
		 WHERE lower(email) = ANY($1) AND created_at < $2 AND status = ANY($3)`, emails, w.lo, sales)
	if err != nil {
		return nil, gocommerce.Internalf(err, "read earlier orders")
	}
	defer prior.Close()
	for prior.Next() {
		var e string
		if err := prior.Scan(&e); err != nil {
			return nil, gocommerce.Internalf(err, "read earlier orders")
		}
		before[e] = true
	}
	if err := prior.Err(); err != nil {
		return nil, gocommerce.Internalf(err, "read earlier orders")
	}
	for i := range out {
		e := email[out[i].ID]
		out[i].Returning = before[e]
		before[e] = true
	}
	return out, nil
}

func (m *Module) spendFacts(ctx context.Context, w window) ([]spendFact, error) {
	rows, err := m.app.DB().QueryContext(ctx, `
		SELECT source, medium, min(campaign), currency, sum(amount_minor)
		  FROM ads_spend WHERE day BETWEEN $1 AND $2
		 GROUP BY source, medium, lower(campaign), currency`, w.from, w.to)
	if err != nil {
		return nil, gocommerce.Internalf(err, "read the ad spend")
	}
	defer rows.Close()
	var out []spendFact
	for rows.Next() {
		var s spendFact
		if err := rows.Scan(&s.channel.Source, &s.channel.Medium, &s.channel.Campaign, &s.Currency, &s.Amount); err != nil {
			return nil, gocommerce.Internalf(err, "read the ad spend")
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// tally is a row being added up.
type tally struct {
	Row
	revenue, refunded, cost, profit, spend int64
	hasSpend                               bool
}

func (t *tally) add(o orderFact) {
	t.Orders++
	t.revenue += o.Total
	t.refunded += o.Refunded
	t.cost += o.Cost
	t.profit += o.profit()
	t.UncostedUnits += o.Uncosted
	if o.Returned {
		t.Returns++
	}
	if o.Returning {
		t.ReturningCustomers++
	} else {
		t.NewCustomers++
	}
}

func (t *tally) row(currency string) Row {
	r := t.Row
	m := func(v int64) gocommerce.Money { return gocommerce.Money{AmountMinor: v, Currency: currency} }
	r.Revenue, r.Refunded, r.Net = m(t.revenue), m(t.refunded), m(t.revenue-t.refunded)
	r.ProductCost, r.GrossProfit = m(t.cost), m(t.profit)
	r.AverageOrder = m(average(t.revenue, t.Orders))
	if t.hasSpend {
		s := m(t.spend)
		r.Spend = &s
		r.ROAS, r.POAS = ratio(t.revenue, t.spend), ratio(t.profit, t.spend)
		if t.Orders > 0 {
			c := m(average(t.spend, t.Orders))
			r.CostPerOrder = &c
		}
	}
	return r
}

// average divides money by a count, half up, in integers (rule 6).
func average(total int64, n int) int64 {
	if n <= 0 {
		return 0
	}
	if total < 0 {
		return -average(-total, n)
	}
	return (total + int64(n)/2) / int64(n)
}

// ratio is a ratio of two sums of money, to two decimals. It is not money.
func ratio(a, b int64) *float64 {
	if b <= 0 {
		return nil
	}
	r := math.Round(float64(a)/float64(b)*100) / 100
	return &r
}

// aggregate adds the orders up by where they came from, and matches spend to
// the rows by source, medium and campaign, ignoring case, in the same
// currency. Spend with no orders is a row of its own: it is the money that
// bought nothing.
func aggregate(orders []orderFact, spend []spendFact, touch string) []*CurrencyReport {
	type id struct {
		currency string
		ch       channel
	}
	rows := map[id]*tally{}
	get := func(currency string, ch channel) *tally {
		k := id{currency, ch.key()}
		t := rows[k]
		if t == nil {
			t = &tally{Row: Row{Source: ch.Source, Medium: ch.Medium, Campaign: ch.Campaign}}
			rows[k] = t
		}
		return t
	}
	for _, o := range orders {
		get(o.Currency, o.Attr.touch(touch).channel()).add(o)
	}
	for _, s := range spend {
		t := get(s.Currency, s.channel)
		t.spend += s.Amount
		t.hasSpend = true
	}

	byCurrency := map[string]*CurrencyReport{}
	totals := map[string]*tally{}
	for k, t := range rows {
		c := byCurrency[k.currency]
		if c == nil {
			c = &CurrencyReport{Currency: k.currency, Rows: []Row{}}
			byCurrency[k.currency] = c
			totals[k.currency] = &tally{}
		}
		c.Rows = append(c.Rows, t.row(k.currency))
		all := totals[k.currency]
		all.Orders += t.Orders
		all.revenue += t.revenue
		all.refunded += t.refunded
		all.cost += t.cost
		all.profit += t.profit
		all.UncostedUnits += t.UncostedUnits
		all.Returns += t.Returns
		all.NewCustomers += t.NewCustomers
		all.ReturningCustomers += t.ReturningCustomers
		all.spend += t.spend
		all.hasSpend = all.hasSpend || t.hasSpend
	}
	out := make([]*CurrencyReport, 0, len(byCurrency))
	for cur, c := range byCurrency {
		c.Totals = totals[cur].row(cur)
		sort.SliceStable(c.Rows, func(i, j int) bool {
			a, b := c.Rows[i], c.Rows[j]
			if sa, sb := spent(a), spent(b); sa != sb {
				return sa > sb
			}
			if a.Revenue.AmountMinor != b.Revenue.AmountMinor {
				return a.Revenue.AmountMinor > b.Revenue.AmountMinor
			}
			return strings.ToLower(a.Source+a.Medium+a.Campaign) < strings.ToLower(b.Source+b.Medium+b.Campaign)
		})
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b *CurrencyReport) int { return strings.Compare(a.Currency, b.Currency) })
	return out
}

func spent(r Row) int64 {
	if r.Spend == nil {
		return -1
	}
	return r.Spend.AmountMinor
}

func query(r *http.Request) map[string]string {
	out := map[string]string{}
	for k := range r.URL.Query() {
		out[k] = strings.TrimSpace(r.URL.Query().Get(k))
	}
	return out
}

func (m *Module) report(ctx context.Context, w window) (*Report, []orderFact, error) {
	orders, err := m.facts(ctx, w)
	if err != nil {
		return nil, nil, err
	}
	spend, err := m.spendFacts(ctx, w)
	if err != nil {
		return nil, nil, err
	}
	return &Report{From: w.from, To: w.to, TimeZone: w.tz, Touch: w.touch, Currencies: aggregate(orders, spend, w.touch)}, orders, nil
}

func (m *Module) handleReport(rw http.ResponseWriter, r *http.Request) {
	w, err := m.parseWindow(r.Context(), query(r))
	if err != nil {
		gocommerce.RespondError(rw, r, err)
		return
	}
	rep, _, err := m.report(r.Context(), w)
	if err != nil {
		gocommerce.RespondError(rw, r, err)
		return
	}
	gocommerce.Respond(rw, http.StatusOK, rep)
}

// handleOrders lists the orders behind one row, newest first.
func (m *Module) handleOrders(rw http.ResponseWriter, r *http.Request) {
	q := query(r)
	w, err := m.parseWindow(r.Context(), q)
	if err != nil {
		gocommerce.RespondError(rw, r, err)
		return
	}
	orders, err := m.facts(r.Context(), w)
	if err != nil {
		gocommerce.RespondError(rw, r, err)
		return
	}
	want := channel{q["source"], q["medium"], q["campaign"]}.key()
	out := []OrderRow{}
	for i := len(orders) - 1; i >= 0; i-- {
		o := orders[i]
		if o.Currency != q["currency"] || o.Attr.touch(w.touch).channel().key() != want {
			continue
		}
		m := func(v int64) gocommerce.Money { return gocommerce.Money{AmountMinor: v, Currency: o.Currency} }
		customer := "new"
		if o.Returning {
			customer = "returning"
		}
		out = append(out, OrderRow{ID: o.ID, Number: o.Number, CreatedAt: o.At, Total: m(o.Total),
			Refunded: m(o.Refunded), GrossProfit: m(o.profit()), UncostedUnits: o.Uncosted,
			Returned: o.Returned, Customer: customer})
	}
	gocommerce.Respond(rw, http.StatusOK, out)
}
