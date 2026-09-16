package gocommerce

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// What "how much did we sell" means, written as tests rather than as prose.
// Each one pins a way the number can be quietly wrong: a definition of a sale
// that drops a state, a bucket cut on a different midnight from the window that
// contains it, a total that stops matching the buckets under it, and money from
// two currencies added together.

// ------------------------------------------------------------------ fixtures

// reportOrder is one imported order: a line, a time, a currency and the two
// statuses. Imported rather than checked out because the window and the buckets
// are the thing under test, and only the importer lets a test say when an order
// was placed. It goes through the service, so no test writes a core table.
type reportOrder struct {
	number    string
	sku       string
	qty       int
	unit      int64
	status    string
	payment   string
	currency  string
	createdAt time.Time
	shipping  int64
	discount  int64
}

func importReportOrders(t *testing.T, app *App, orders ...reportOrder) {
	t.Helper()

	var b strings.Builder
	b.WriteString("number,email,sku,title,quantity,unit_price_minor,status,payment_status," +
		"currency,created_at,shipping_minor,discount_minor,total_minor\n")
	for i, o := range orders {
		if o.number == "" {
			o.number = fmt.Sprintf("IMP-%03d", i+1)
		}
		if o.sku == "" {
			o.sku = "REP-SKU"
		}
		if o.qty == 0 {
			o.qty = 1
		}
		if o.unit == 0 {
			o.unit = 1000
		}
		if o.status == "" {
			o.status = OrderConfirmed
		}
		if o.payment == "" {
			o.payment = PaymentPaid
		}
		if o.currency == "" {
			o.currency = app.Config().Currency
		}
		if o.createdAt.IsZero() {
			o.createdAt = time.Now().UTC()
		}
		total := o.unit*int64(o.qty) + o.shipping - o.discount
		fmt.Fprintf(&b, "%s,buyer@example.com,%s,%s,%d,%d,%s,%s,%s,%s,%d,%d,%d\n",
			o.number, o.sku, "Test "+o.sku, o.qty, o.unit, o.status, o.payment,
			o.currency, o.createdAt.Format(time.RFC3339), o.shipping, o.discount, total)
	}

	result, err := app.Data().ImportOrders(context.Background(), strings.NewReader(b.String()), ImportOptions{})
	if err != nil {
		t.Fatalf("import orders: %v", err)
	}
	if len(result.Errors) > 0 {
		t.Fatalf("import orders: %+v", result.Errors)
	}
	if result.Created != len(orders) {
		t.Fatalf("imported %d orders, wanted %d", result.Created, len(orders))
	}
}

func day(y int, m time.Month, d, hour int) time.Time {
	return time.Date(y, m, d, hour, 0, 0, 0, time.UTC)
}

func salesReport(t *testing.T, app *App, q SalesQuery) *SalesReport {
	t.Helper()
	report, err := app.Reports().Sales(context.Background(), q)
	if err != nil {
		t.Fatalf("sales report: %v", err)
	}
	return report
}

// currencyBlock finds one currency's block, failing with what was there instead
// — an empty block list is the shape most likely to be wrong.
func currencyBlock(t *testing.T, report *SalesReport, currency string) *CurrencySales {
	t.Helper()
	for _, block := range report.Currencies {
		if block.Currency == currency {
			return block
		}
	}
	have := []string{}
	for _, block := range report.Currencies {
		have = append(have, block.Currency)
	}
	t.Fatalf("no %s block in the report; it carries %v", currency, have)
	return nil
}

func topProducts(t *testing.T, app *App, q TopProductsQuery) ([]*ProductSales, int) {
	t.Helper()
	rows, total, err := app.Reports().TopProducts(context.Background(), q)
	if err != nil {
		t.Fatalf("top products: %v", err)
	}
	return rows, total
}

// bounds is the window every fixture-based test asks for: civil dates, so the
// zone under test is the only thing deciding where the edges fall.
func bounds(from, to string) (ReportBound, ReportBound) {
	return ReportBound{Date: from}, ReportBound{Date: to}
}

// ------------------------------------------------------- the definition of a sale

// The report's SQL predicate and the state machine's own stockCommitted are two
// spellings of one idea, so they are checked against each other over the whole
// status enum. This is the test that fails the day a seventh status is added
// and only one of the two learns about it.
func TestReportSalePredicateMatchesStockCommitted(t *testing.T) {
	every := []string{OrderPending, OrderConfirmed, OrderPartial, OrderShipped,
		OrderDelivered, OrderCancelled}

	for _, status := range every {
		inReport := false
		for _, s := range saleStatuses {
			if s == status {
				inReport = true
			}
		}
		if inReport != stockCommitted(status) {
			t.Errorf("status %q: the report says sale=%v, stockCommitted says %v",
				status, inReport, stockCommitted(status))
		}
	}
}

// A sale is an order whose stock has left the shelf. A cancelled order and an
// in-flight checkout are reported beside the totals, never inside them.
func TestSalesCountsCommittedOrdersOnly(t *testing.T) {
	app := newTestApp(t, &gatewayModule{})
	ctx := context.Background()

	// Confirmed at checkout, cash on delivery, payment still pending.
	sold := confirmedOrder(t, app, orderSpec{"SALE-1", 2})

	cancelled := confirmedOrder(t, app, orderSpec{"SALE-2", 1})
	if _, err := app.Order().Cancel(ctx, cancelled.ID, "changed their mind"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// A gateway that needs a client action leaves the order pending — exactly
	// the set SweepUnpaid may cancel an OrderTTL later.
	open := simpleProduct(t, app, "SALE-3", 2500, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, open.DefaultVariant().ID, 1)
	pending, err := app.Order().Checkout(ctx, "testgateway", checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout through the gateway: %v", err)
	}
	if pending.Order.Status != OrderPending {
		t.Fatalf("the gateway order is %s, not pending", pending.Order.Status)
	}

	block := currencyBlock(t, salesReport(t, app, SalesQuery{}), app.Config().Currency)
	if block.Totals.Orders != 1 {
		t.Errorf("orders = %d, want 1 (only the committed one)", block.Totals.Orders)
	}
	if block.Totals.Total.AmountMinor != sold.Total.AmountMinor {
		t.Errorf("total = %d, want %d", block.Totals.Total.AmountMinor, sold.Total.AmountMinor)
	}
	if block.Totals.Excluded.Cancelled.Orders != 1 {
		t.Errorf("excluded.cancelled.orders = %d, want 1", block.Totals.Excluded.Cancelled.Orders)
	}
	if block.Totals.Excluded.Pending.Orders != 1 {
		t.Errorf("excluded.pending.orders = %d, want 1", block.Totals.Excluded.Pending.Orders)
	}
	if got := block.Totals.Excluded.Pending.Total.AmountMinor; got != pending.Order.Total.AmountMinor {
		t.Errorf("excluded.pending.total = %d, want %d", got, pending.Order.Total.AmountMinor)
	}
}

// The regression guard against redefining a sale as payment_status = 'paid',
// which would report the store shape this engine ships with as zero revenue.
func TestSalesCountsAnUnpaidCODOrderAsASale(t *testing.T) {
	app := newTestApp(t)

	order := confirmedOrder(t, app, orderSpec{"COD-1", 1})
	if order.PaymentStatus != PaymentPending {
		t.Fatalf("a cash-on-delivery order came back %s, not pending", order.PaymentStatus)
	}

	totals := currencyBlock(t, salesReport(t, app, SalesQuery{}), app.Config().Currency).Totals
	if totals.Orders != 1 || totals.Net.AmountMinor <= 0 {
		t.Fatalf("orders = %d, net = %d; an unpaid COD order is still a sale",
			totals.Orders, totals.Net.AmountMinor)
	}
	if totals.Paid.Orders != 0 {
		t.Errorf("paid.orders = %d, want 0", totals.Paid.Orders)
	}
	if totals.Outstanding.Orders != 1 {
		t.Errorf("outstanding.orders = %d, want 1", totals.Outstanding.Orders)
	}
}

// A partly shipped order is a sale. Its absence from the predicate would make
// an order vanish from revenue at the moment the first parcel went out, which
// is why `partial` landed before this report did.
func TestSalesCountsAPartlyShippedOrder(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	order := confirmedOrder(t, app, orderSpec{"PARTSALE-1", 3})
	line := lineBySKU(t, order, "PARTSALE-1")
	shipped, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{
		Tracking: "TRACK-PART",
		Lines:    []ShipLine{{OrderLineID: line.ID, Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("ship one unit: %v", err)
	}
	if shipped.Status != OrderPartial {
		t.Fatalf("status = %q, want partial", shipped.Status)
	}

	totals := currencyBlock(t, salesReport(t, app, SalesQuery{}), app.Config().Currency).Totals
	if totals.Orders != 1 {
		t.Errorf("orders = %d, want 1 — a partly shipped order is a sale", totals.Orders)
	}
	if totals.Total.AmountMinor != shipped.Total.AmountMinor {
		t.Errorf("total = %d, want the whole order's %d",
			totals.Total.AmountMinor, shipped.Total.AmountMinor)
	}
	if totals.Units != 3 {
		t.Errorf("units = %d, want 3 — the order sold three, one of them has gone out", totals.Units)
	}
}

// ------------------------------------------------------------- the arithmetic

// total = net + tax + shipping, under both tax modes, per bucket and in the
// totals. This is the identity the response promises, and the tax_inclusive
// branch is what makes it a per-row decision rather than a store-wide one.
func TestSalesTotalEqualsNetPlusTaxPlusShipping(t *testing.T) {
	for _, inclusive := range []bool{false, true} {
		t.Run(fmt.Sprintf("inclusive=%v", inclusive), func(t *testing.T) {
			app := newTaxApp(t, func(c *Config) {
				c.PricesIncludeTax = inclusive
				c.FlatShippingMinor = 500
			})
			newTaxRate(t, app, TaxRateInput{Name: "VAT", Country: "US", RateBP: 2000})

			product := simpleProduct(t, app, "TAXID-1", 2000, 5)
			order, err := checkoutTo(t, app, product.DefaultVariant().ID, 2, "US", "")
			if err != nil {
				t.Fatalf("checkout: %v", err)
			}
			if order.Tax.AmountMinor == 0 {
				t.Fatalf("the fixture charged no tax, so it proves nothing")
			}

			block := currencyBlock(t, salesReport(t, app, SalesQuery{}), app.Config().Currency)
			for i, b := range block.Buckets {
				if got, want := b.Total.AmountMinor, b.Net.AmountMinor+b.Tax.AmountMinor+b.Shipping.AmountMinor; got != want {
					t.Errorf("bucket %d: total %d != net+tax+shipping %d", i, got, want)
				}
			}
			totals := block.Totals
			if got, want := totals.Total.AmountMinor, totals.Net.AmountMinor+totals.Tax.AmountMinor+totals.Shipping.AmountMinor; got != want {
				t.Errorf("totals: total %d != net+tax+shipping %d", got, want)
			}
			if totals.Total.AmountMinor != order.Total.AmountMinor {
				t.Errorf("total %d does not match the order's own %d",
					totals.Total.AmountMinor, order.Total.AmountMinor)
			}

			// Net is written off the source columns, so it differs between the
			// two modes by exactly the tax the inclusive price contained.
			wantNet := order.Subtotal.AmountMinor - order.Discount.AmountMinor
			if inclusive {
				wantNet -= order.Tax.AmountMinor
			}
			if totals.Net.AmountMinor != wantNet {
				t.Errorf("net = %d, want %d", totals.Net.AmountMinor, wantNet)
			}
		})
	}
}

// payment_status is CHECK-constrained to exactly four values, so the three
// slices partition the sale set with nothing left over. That is what makes the
// split safe to draw as a stacked bar.
func TestSalesPaymentSplitPartitionsTheTotal(t *testing.T) {
	app := newTestApp(t)
	at := day(2026, time.March, 10, 9)

	importReportOrders(t, app,
		reportOrder{number: "SPLIT-1", payment: PaymentPaid, createdAt: at},
		reportOrder{number: "SPLIT-2", payment: PaymentPaid, createdAt: at},
		reportOrder{number: "SPLIT-3", payment: PaymentPending, createdAt: at},
		reportOrder{number: "SPLIT-4", payment: PaymentFailed, createdAt: at},
		reportOrder{number: "SPLIT-5", payment: PaymentRefunded, createdAt: at},
	)

	from, to := bounds("2026-03-01", "2026-04-01")
	report := salesReport(t, app, SalesQuery{From: from, To: to})
	block := currencyBlock(t, report, app.Config().Currency)

	check := func(what string, total SalesTotals) {
		t.Helper()
		sum := total.Paid.Total.AmountMinor + total.Outstanding.Total.AmountMinor +
			total.Refunded.Total.AmountMinor
		if sum != total.Total.AmountMinor {
			t.Errorf("%s: paid+outstanding+refunded = %d, total = %d", what, sum, total.Total.AmountMinor)
		}
		if n := total.Paid.Orders + total.Outstanding.Orders + total.Refunded.Orders; n != total.Orders {
			t.Errorf("%s: the three slices hold %d orders, total says %d", what, n, total.Orders)
		}
	}

	check("totals", block.Totals)
	for i, b := range block.Buckets {
		check(fmt.Sprintf("bucket %d", i), SalesTotals{
			Orders: b.Orders, Total: b.Total, Paid: b.Paid,
			Outstanding: b.Outstanding, Refunded: b.Refunded,
		})
	}

	if block.Totals.Paid.Orders != 2 {
		t.Errorf("paid.orders = %d, want 2", block.Totals.Paid.Orders)
	}
	if block.Totals.Outstanding.Orders != 2 {
		t.Errorf("outstanding.orders = %d, want 2 (pending and failed)", block.Totals.Outstanding.Orders)
	}
	if block.Totals.Refunded.Orders != 1 {
		t.Errorf("refunded.orders = %d, want 1", block.Totals.Refunded.Orders)
	}
}

// A partial refund leaves the order `paid` (D36), so the money that went back
// is reported as its own figure beside the total rather than netted off it.
// Reading `paid.total` as money kept is the bug this pins.
func TestSalesReportsRefundedMoneyWithoutNettingItOff(t *testing.T) {
	app := newTestApp(t, payModule{provider: referencedProvider{ref: "re_1"}})
	ctx := context.Background()

	order := refundableOrder(t, app, "referenced", "REF-REP-1", 4000)
	if _, err := app.Pay().Refund(ctx, order.ID, RefundRequest{AmountMinor: 1500}, nil); err != nil {
		t.Fatalf("refund: %v", err)
	}

	totals := currencyBlock(t, salesReport(t, app, SalesQuery{}), app.Config().Currency).Totals
	if totals.Orders != 1 {
		t.Fatalf("orders = %d, want 1 — a refunded sale is still a sale", totals.Orders)
	}
	if totals.Net.AmountMinor != order.Subtotal.AmountMinor-order.Discount.AmountMinor {
		t.Errorf("net = %d; refunds are reported, never netted off net", totals.Net.AmountMinor)
	}
	if totals.Paid.Orders != 1 {
		t.Errorf("paid.orders = %d, want 1 — a partly refunded order is still paid", totals.Paid.Orders)
	}
	if totals.Paid.Refunded.AmountMinor != 1500 {
		t.Errorf("paid.refunded = %d, want 1500", totals.Paid.Refunded.AmountMinor)
	}
	if got, want := totals.Paid.NetCollected.AmountMinor, order.Total.AmountMinor-1500; got != want {
		t.Errorf("paid.net_collected = %d, want %d", got, want)
	}
	if totals.Paid.NetCollected.Currency != app.Config().Currency {
		t.Errorf("net_collected carries %q", totals.Paid.NetCollected.Currency)
	}
}

// Un-recording a payment is not un-selling: the order keeps its status, so it
// stays a sale and only moves between the payment slices.
func TestSalesMarkUnpaidLeavesTheSaleStanding(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	order := confirmedOrder(t, app, orderSpec{"UNPAID-1", 1})
	if _, err := app.Pay().MarkPaid(ctx, order.ID, "cash"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	if totals := currencyBlock(t, salesReport(t, app, SalesQuery{}), app.Config().Currency).Totals; totals.Paid.Orders != 1 {
		t.Fatalf("paid.orders = %d after MarkPaid, want 1", totals.Paid.Orders)
	}

	if _, err := app.Pay().MarkUnpaid(ctx, order.ID); err != nil {
		t.Fatalf("mark unpaid: %v", err)
	}
	totals := currencyBlock(t, salesReport(t, app, SalesQuery{}), app.Config().Currency).Totals
	if totals.Orders != 1 {
		t.Errorf("orders = %d, want 1 — the order is still confirmed", totals.Orders)
	}
	if totals.Paid.Orders != 0 || totals.Outstanding.Orders != 1 {
		t.Errorf("paid.orders = %d, outstanding.orders = %d; want 0 and 1",
			totals.Paid.Orders, totals.Outstanding.Orders)
	}
}

// The totals are added up from the buckets rather than queried a second time,
// so a stat card and the chart beneath it can never disagree.
func TestSalesTotalsAreTheSumOfTheBuckets(t *testing.T) {
	app := newTestApp(t)
	importReportOrders(t, app,
		reportOrder{number: "SUM-1", unit: 1000, qty: 2, shipping: 300, createdAt: day(2026, time.March, 2, 10)},
		reportOrder{number: "SUM-2", unit: 2500, qty: 1, discount: 500, createdAt: day(2026, time.March, 4, 10)},
		reportOrder{number: "SUM-3", unit: 700, qty: 3, createdAt: day(2026, time.March, 4, 18)},
	)

	from, to := bounds("2026-03-01", "2026-03-08")
	block := currencyBlock(t, salesReport(t, app, SalesQuery{From: from, To: to}), app.Config().Currency)

	var orders, units int
	var items, discount, shipping, tax, net, total int64
	for _, b := range block.Buckets {
		orders += b.Orders
		units += b.Units
		items += b.Items.AmountMinor
		discount += b.Discount.AmountMinor
		shipping += b.Shipping.AmountMinor
		tax += b.Tax.AmountMinor
		net += b.Net.AmountMinor
		total += b.Total.AmountMinor
	}
	tt := block.Totals
	for _, c := range []struct {
		name      string
		got, want int64
	}{
		{"orders", int64(tt.Orders), int64(orders)},
		{"units", int64(tt.Units), int64(units)},
		{"items", tt.Items.AmountMinor, items},
		{"discount", tt.Discount.AmountMinor, discount},
		{"shipping", tt.Shipping.AmountMinor, shipping},
		{"tax", tt.Tax.AmountMinor, tax},
		{"net", tt.Net.AmountMinor, net},
		{"total", tt.Total.AmountMinor, total},
	} {
		if c.got != c.want {
			t.Errorf("totals.%s = %d, the buckets add up to %d", c.name, c.got, c.want)
		}
	}

	if want := (total + int64(orders)/2) / int64(orders); tt.AverageOrder.AmountMinor != want {
		t.Errorf("average_order = %d, want %d (half up)", tt.AverageOrder.AmountMinor, want)
	}
	if tt.AverageOrder.Currency != app.Config().Currency {
		t.Errorf("average_order carries %q", tt.AverageOrder.Currency)
	}
}

// An empty window still answers, with the store's own currency and a zero
// average rather than a division by zero.
func TestSalesOnAStoreThatSoldNothing(t *testing.T) {
	app := newTestApp(t)

	from, to := bounds("2026-03-01", "2026-03-04")
	report := salesReport(t, app, SalesQuery{From: from, To: to})
	block := currencyBlock(t, report, app.Config().Currency)

	if len(block.Buckets) != 3 {
		t.Errorf("got %d buckets, want 3 — an empty window is still three days", len(block.Buckets))
	}
	if block.Totals.Orders != 0 || block.Totals.Total.AmountMinor != 0 {
		t.Errorf("totals are not zero: %+v", block.Totals)
	}
	if block.Totals.AverageOrder.AmountMinor != 0 ||
		block.Totals.AverageOrder.Currency != app.Config().Currency {
		t.Errorf("average_order = %+v, want zero in the store's currency", block.Totals.AverageOrder)
	}
}

// --------------------------------------------------------------- the buckets

// The bucket boundaries are cut in the report's zone, not the database
// session's. Without the AT TIME ZONE this test passes or fails depending on a
// PostgreSQL setting nobody in this repository set.
func TestSalesBucketsFollowTheRequestedZone(t *testing.T) {
	app := newTestApp(t)
	importReportOrders(t, app,
		reportOrder{number: "TZ-1", createdAt: day(2026, time.March, 1, 19)},
		reportOrder{number: "TZ-2", createdAt: day(2026, time.March, 1, 20)},
	)

	from, to := bounds("2026-03-01", "2026-03-04")

	utc := currencyBlock(t, salesReport(t, app,
		SalesQuery{From: from, To: to, TimeZone: "UTC"}), app.Config().Currency)
	if utc.Buckets[0].Orders != 2 {
		t.Errorf("in UTC both orders are on the 1st; bucket 0 has %d", utc.Buckets[0].Orders)
	}

	// 19:00 and 20:00 UTC are both after midnight in Kolkata (UTC+5:30).
	ist := currencyBlock(t, salesReport(t, app,
		SalesQuery{From: from, To: to, TimeZone: "asia/kolkata"}), app.Config().Currency)
	if ist.Buckets[0].Orders != 0 {
		t.Errorf("in Kolkata nothing was sold on the 1st; bucket 0 has %d", ist.Buckets[0].Orders)
	}
	if ist.Buckets[1].Orders != 2 {
		t.Errorf("in Kolkata both sales are on the 2nd; bucket 1 has %d", ist.Buckets[1].Orders)
	}
}

// A civil bound and a bucket boundary are the same midnight. Measured against
// different ones the first and last buckets are quietly partial and nobody can
// tell, which is the reason the window is resolved in the report's zone too.
func TestSalesWindowEdgesUseTheSameZoneAsTheBuckets(t *testing.T) {
	app := newTestApp(t)

	from, to := bounds("2026-03-02", "2026-03-03")
	report := salesReport(t, app, SalesQuery{From: from, To: to, TimeZone: "Asia/Kolkata"})
	if report.TimeZone != "Asia/Kolkata" {
		t.Errorf("time_zone = %q", report.TimeZone)
	}

	// Midnight on the 2nd in Kolkata is 18:30 UTC on the 1st.
	wantFrom := time.Date(2026, time.March, 1, 18, 30, 0, 0, time.UTC)
	if !report.From.Equal(wantFrom) {
		t.Errorf("from = %s, want %s", report.From.UTC(), wantFrom)
	}

	block := currencyBlock(t, report, app.Config().Currency)
	if len(block.Buckets) != 1 {
		t.Fatalf("got %d buckets, want 1", len(block.Buckets))
	}
	if !block.Buckets[0].Start.Equal(report.From) {
		t.Errorf("bucket 0 starts at %s, the window at %s",
			block.Buckets[0].Start.UTC(), report.From.UTC())
	}
	if block.Buckets[0].Partial {
		t.Error("a bucket that exactly fills the window is not partial")
	}
}

// A bar chart with missing bars is a lie about shape, so the grid drives the
// response rather than the GROUP BY.
func TestSalesFillsEveryBucketInTheWindow(t *testing.T) {
	app := newTestApp(t)
	importReportOrders(t, app, reportOrder{number: "GAP-1", createdAt: day(2026, time.March, 3, 12)})

	from, to := bounds("2026-03-01", "2026-03-06")
	block := currencyBlock(t, salesReport(t, app, SalesQuery{From: from, To: to}), app.Config().Currency)

	if len(block.Buckets) != 5 {
		t.Fatalf("got %d buckets, want 5", len(block.Buckets))
	}
	for i, b := range block.Buckets {
		if i > 0 && !b.Start.Equal(block.Buckets[i-1].End) {
			t.Errorf("bucket %d starts at %s, bucket %d ended at %s",
				i, b.Start.UTC(), i-1, block.Buckets[i-1].End.UTC())
		}
		want := 0
		if i == 2 {
			want = 1
		}
		if b.Orders != want {
			t.Errorf("bucket %d has %d orders, want %d", i, b.Orders, want)
		}
	}
}

// Generated in naive local time with a calendar step: over a spring-forward the
// day is 23 hours long and the buckets still meet exactly. An implementation
// using generate_series over timestamptz with interval '1 day' ships this bug.
func TestSalesBucketsSurviveADaylightSavingChange(t *testing.T) {
	app := newTestApp(t)

	from, to := bounds("2026-03-28", "2026-03-31")
	block := currencyBlock(t, salesReport(t, app,
		SalesQuery{From: from, To: to, TimeZone: "Europe/London"}), app.Config().Currency)

	if len(block.Buckets) != 3 {
		t.Fatalf("got %d buckets, want 3", len(block.Buckets))
	}
	for i, b := range block.Buckets {
		if i > 0 && !b.Start.Equal(block.Buckets[i-1].End) {
			t.Errorf("bucket %d does not start where bucket %d ended", i, i-1)
		}
	}
	// 29 March 2026 is the spring-forward in London.
	if got := block.Buckets[1].End.Sub(block.Buckets[1].Start); got != 23*time.Hour {
		t.Errorf("the DST day is %s long, want 23h", got)
	}
}

// A window from midday to midday clips its first and last buckets, and says so.
func TestSalesFlagsPartialBuckets(t *testing.T) {
	app := newTestApp(t)

	fromAt := day(2026, time.March, 1, 12)
	toAt := day(2026, time.March, 4, 12)
	block := currencyBlock(t, salesReport(t, app, SalesQuery{
		From: ReportBound{At: &fromAt}, To: ReportBound{At: &toAt},
	}), app.Config().Currency)

	if len(block.Buckets) != 4 {
		t.Fatalf("got %d buckets, want 4", len(block.Buckets))
	}
	for i, b := range block.Buckets {
		want := i == 0 || i == len(block.Buckets)-1
		if b.Partial != want {
			t.Errorf("bucket %d partial = %v, want %v", i, b.Partial, want)
		}
	}
}

// Weeks are ISO, which is date_trunc's definition: Monday starts one.
func TestSalesWeeksStartOnMonday(t *testing.T) {
	app := newTestApp(t)

	from, to := bounds("2026-03-01", "2026-03-15") // 1 March 2026 is a Sunday
	block := currencyBlock(t, salesReport(t, app,
		SalesQuery{From: from, To: to, GroupBy: GrainWeek}), app.Config().Currency)

	if len(block.Buckets) != 3 {
		t.Fatalf("got %d buckets, want 3 (a Sunday, then two whole weeks)", len(block.Buckets))
	}
	if got := block.Buckets[1].Start.UTC(); !got.Equal(day(2026, time.March, 2, 0)) {
		t.Errorf("the second week starts at %s, want Monday 2 March", got)
	}
	if !block.Buckets[0].Partial {
		t.Error("the window opens mid-week, so the first bucket is partial")
	}
}

// ------------------------------------------------------------------ currency

// Minor units of two currencies do not add up to money, so they never meet.
func TestSalesNeverSumsAcrossCurrencies(t *testing.T) {
	app := newTestApp(t)
	at := day(2026, time.March, 3, 11)
	importReportOrders(t, app,
		reportOrder{number: "CUR-1", currency: "USD", unit: 1000, createdAt: at},
		reportOrder{number: "CUR-2", currency: "EUR", unit: 2000, createdAt: at},
	)

	from, to := bounds("2026-03-01", "2026-03-06")
	report := salesReport(t, app, SalesQuery{From: from, To: to})

	usd := currencyBlock(t, report, "USD")
	eur := currencyBlock(t, report, "EUR")
	if usd.Totals.Total.AmountMinor != 1000 || usd.Totals.Total.Currency != "USD" {
		t.Errorf("USD totals = %+v", usd.Totals.Total)
	}
	if eur.Totals.Total.AmountMinor != 2000 || eur.Totals.Total.Currency != "EUR" {
		t.Errorf("EUR totals = %+v", eur.Totals.Total)
	}
	for _, b := range eur.Buckets {
		if b.Total.Currency != "EUR" || b.Paid.NetCollected.Currency != "EUR" {
			t.Errorf("a EUR bucket carries %q", b.Total.Currency)
		}
	}
}

// A store with no sales in the window still has a currency, so the block is
// there and zero-filled rather than absent — an empty response looks broken.
func TestSalesAlwaysIncludesTheStoresOwnCurrency(t *testing.T) {
	app := newTestApp(t)
	importReportOrders(t, app,
		reportOrder{number: "ONLY-EUR", currency: "EUR", createdAt: day(2026, time.March, 3, 11)})

	from, to := bounds("2026-03-01", "2026-03-06")
	report := salesReport(t, app, SalesQuery{From: from, To: to})

	block := currencyBlock(t, report, app.Config().Currency)
	if block.Totals.Orders != 0 {
		t.Errorf("the store's own currency sold nothing, but reports %d orders", block.Totals.Orders)
	}
	if len(block.Buckets) != 5 {
		t.Errorf("the zero-filled block has %d buckets, want 5", len(block.Buckets))
	}
}

// ---------------------------------------------------------------- validation

func TestSalesValidation(t *testing.T) {
	app := newTestApp(t)

	cases := []struct {
		name   string
		target string
		want   string
	}{
		{"an unknown zone", "?tz=Mars/Olympus", "not a time zone"},
		{"an hourly grain", "?group_by=hour", "group_by must be"},
		{"from after to", "?from=2026-03-10&to=2026-03-01", "from must be before to"},
		{"an unparseable date", "?from=last-tuesday", "is not a date"},
		{"five years of days", "?from=2020-01-01&to=2026-01-01", "coarser group_by"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, app, "GET", "/api/admin/reports/sales"+tc.target, withAdmin)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("body %s does not mention %q", rec.Body.String(), tc.want)
			}
		})
	}

	// A script calling the service gets the same refusal the HTTP caller does.
	if _, err := app.Reports().Sales(context.Background(), SalesQuery{GroupBy: "hour"}); err == nil {
		t.Error("Sales accepted group_by=hour")
	}
	if _, _, err := app.Reports().TopProducts(context.Background(),
		TopProductsQuery{By: "supplier"}); err == nil {
		t.Error("TopProducts accepted by=supplier")
	}
	if _, _, err := app.Reports().TopProducts(context.Background(),
		TopProductsQuery{Sort: "alphabetical"}); err == nil {
		t.Error("TopProducts accepted sort=alphabetical")
	}
}

// A window of a year of days is under the cap and must not be refused: a guard
// that refuses the ordinary case is worse than no guard.
func TestSalesAcceptsAYearOfDays(t *testing.T) {
	app := newTestApp(t)
	from, to := bounds("2025-03-01", "2026-03-01")
	report := salesReport(t, app, SalesQuery{From: from, To: to})
	if n := len(currencyBlock(t, report, app.Config().Currency).Buckets); n != 365 {
		t.Errorf("got %d buckets, want 365", n)
	}
}

// ------------------------------------------------------------- top products

func TestTopProductsRanksWithinOneCurrency(t *testing.T) {
	app := newTestApp(t)
	at := day(2026, time.March, 5, 10)
	importReportOrders(t, app,
		reportOrder{number: "TP-1", sku: "CHEAP", qty: 10, unit: 100, createdAt: at},
		reportOrder{number: "TP-2", sku: "CHEAP", qty: 5, unit: 100, createdAt: at},
		reportOrder{number: "TP-3", sku: "PRICEY", qty: 2, unit: 5000, createdAt: at},
		reportOrder{number: "TP-4", sku: "EURO", qty: 9, unit: 900, currency: "EUR", createdAt: at},
		// Still in checkout: its lines are not sales.
		reportOrder{number: "TP-5", sku: "GHOST", qty: 99, unit: 100, status: OrderPending, createdAt: at},
	)

	from, to := bounds("2026-03-01", "2026-03-10")
	rows, total := topProducts(t, app, TopProductsQuery{From: from, To: to, Currency: "USD"})
	if total != 2 {
		t.Fatalf("meta total = %d, want 2 (the USD groups only)", total)
	}
	if rows[0].SKU != "PRICEY" {
		t.Errorf("by revenue the first row is %s, want PRICEY", rows[0].SKU)
	}
	if rows[0].Revenue.AmountMinor != 10000 {
		t.Errorf("PRICEY revenue = %d, want 10000", rows[0].Revenue.AmountMinor)
	}

	byUnits, _ := topProducts(t, app, TopProductsQuery{
		From: from, To: to, Currency: "USD", Sort: "units"})
	if byUnits[0].SKU != "CHEAP" {
		t.Errorf("by units the first row is %s, want CHEAP", byUnits[0].SKU)
	}
	if byUnits[0].Units != 15 || byUnits[0].Orders != 2 {
		t.Errorf("CHEAP sold %d units across %d orders, want 15 and 2",
			byUnits[0].Units, byUnits[0].Orders)
	}
	for _, row := range byUnits {
		if row.SKU == "GHOST" {
			t.Error("a pending order's lines were ranked")
		}
		if row.Revenue.Currency != "USD" {
			t.Errorf("row %s carries %q", row.SKU, row.Revenue.Currency)
		}
	}
}

// order_lines.total_minor is unit_price * quantity, and under inclusive pricing
// that price contains the tax — so the two modes must report the same
// merchandise revenue for the same sale.
func TestTopProductsRevenueExcludesTaxInBothModes(t *testing.T) {
	revenues := map[bool]int64{}
	taxes := map[bool]int64{}

	for _, inclusive := range []bool{false, true} {
		app := newTaxApp(t, func(c *Config) { c.PricesIncludeTax = inclusive })
		newTaxRate(t, app, TaxRateInput{Name: "VAT", Country: "US", RateBP: 2000})

		product := simpleProduct(t, app, "TPTAX", 2400, 5)
		if _, err := checkoutTo(t, app, product.DefaultVariant().ID, 1, "US", ""); err != nil {
			t.Fatalf("checkout: %v", err)
		}

		rows, _ := topProducts(t, app, TopProductsQuery{})
		if len(rows) != 1 {
			t.Fatalf("got %d rows, want 1", len(rows))
		}
		revenues[inclusive] = rows[0].Revenue.AmountMinor
		taxes[inclusive] = rows[0].Tax.AmountMinor
	}

	if revenues[false] != 2400 {
		t.Errorf("exclusive revenue = %d, want the line total 2400", revenues[false])
	}
	if revenues[true] != 2400-taxes[true] {
		t.Errorf("inclusive revenue = %d, want 2400 less the %d of tax inside it",
			revenues[true], taxes[true])
	}
	if taxes[true] == 0 {
		t.Fatal("the inclusive fixture charged no tax, so it proves nothing")
	}
}

// A discount is recorded once per order and never allocated to lines (D28), so
// these rows deliberately exceed the sales report's net by exactly the
// discounts in the window. Pinned rather than left to be discovered.
func TestTopProductsRevenueIgnoresOrderLevelDiscounts(t *testing.T) {
	app := newTestApp(t)
	at := day(2026, time.March, 6, 10)
	importReportOrders(t, app,
		reportOrder{number: "DISC-1", sku: "D-A", qty: 2, unit: 1000, discount: 400, createdAt: at})

	from, to := bounds("2026-03-01", "2026-03-10")
	rows, _ := topProducts(t, app, TopProductsQuery{From: from, To: to})
	if len(rows) != 1 || rows[0].Revenue.AmountMinor != 2000 {
		t.Fatalf("revenue = %+v, want the line value 2000", rows)
	}

	totals := currencyBlock(t, salesReport(t, app, SalesQuery{From: from, To: to}),
		app.Config().Currency).Totals
	if got := rows[0].Revenue.AmountMinor - totals.Net.AmountMinor; got != 400 {
		t.Errorf("the rows exceed net by %d, want exactly the %d of discount", got, 400)
	}
}

// product_id is ON DELETE SET NULL because an order line is a snapshot. Two
// deleted products must stay two rows, which is what the 'sku:' fallback in the
// group key is for.
func TestTopProductsSurvivesDeletedProducts(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	first := simpleProduct(t, app, "GONE-1", 1000, 5)
	second := simpleProduct(t, app, "GONE-2", 2000, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, first.DefaultVariant().ID, 1)
	addToCart(t, app, cart.Token, second.DefaultVariant().ID, 1)
	if _, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), ""); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	for _, p := range []*Product{first, second} {
		if err := app.Products().DeleteProduct(ctx, p.ID); err != nil {
			t.Fatalf("delete %s: %v", p.Title, err)
		}
	}

	rows, total := topProducts(t, app, TopProductsQuery{})
	if total != 2 || len(rows) != 2 {
		t.Fatalf("got %d rows (total %d), want 2 — deleted products must not collapse into one",
			len(rows), total)
	}
	for _, row := range rows {
		if row.ProductID != nil {
			t.Errorf("row %s still carries a product id", row.SKU)
		}
		if row.Title == "" || row.SKU == "" {
			t.Errorf("row %+v lost its snapshot", row)
		}
	}
}

// The trailing key in the ORDER BY is what makes LIMIT/OFFSET paging total: on
// a tie without it, pages repeat and drop rows.
func TestTopProductsPagesStably(t *testing.T) {
	app := newTestApp(t)
	at := day(2026, time.March, 7, 10)

	var fixtures []reportOrder
	for i := 0; i < 11; i++ {
		fixtures = append(fixtures, reportOrder{
			number:    fmt.Sprintf("TIE-%02d", i),
			sku:       fmt.Sprintf("TIE-SKU-%02d", i),
			qty:       2,
			unit:      1000,
			createdAt: at,
		})
	}
	importReportOrders(t, app, fixtures...)

	from, to := bounds("2026-03-01", "2026-03-10")
	seen := map[string]bool{}
	for offset := 0; offset < 15; offset += 5 {
		rows, total := topProducts(t, app, TopProductsQuery{
			From: from, To: to, Limit: 5, Offset: offset})
		if total != 11 {
			t.Fatalf("total = %d, want 11", total)
		}
		for _, row := range rows {
			if seen[row.SKU] {
				t.Errorf("%s appeared on two pages", row.SKU)
			}
			seen[row.SKU] = true
		}
	}
	if len(seen) != 11 {
		t.Errorf("paging showed %d of 11 products", len(seen))
	}
}

func TestTopProductsByVariant(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	stock := 10
	product, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Variant tee", Status: ProductActive,
		Options: []OptionInput{{Name: "Size", Values: []string{"S", "M"}}},
		Variants: []VariantInput{
			{SKU: "VAR-S", PriceMinor: 1000, StockOnHand: &stock, Options: []string{"S"}},
			{SKU: "VAR-M", PriceMinor: 1000, StockOnHand: &stock, Options: []string{"M"}},
		},
	})
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	small, medium := product.Variants[0], product.Variants[1]

	cart := newCart(t, app)
	addToCart(t, app, cart.Token, small.ID, 1)
	addToCart(t, app, cart.Token, medium.ID, 3)
	if _, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), ""); err != nil {
		t.Fatalf("checkout: %v", err)
	}

	byProduct, total := topProducts(t, app, TopProductsQuery{})
	if total != 1 || byProduct[0].Units != 4 {
		t.Errorf("by product: %d rows, %d units; want 1 row of 4", total, byProduct[0].Units)
	}

	byVariant, total := topProducts(t, app, TopProductsQuery{By: "variant", Sort: "units"})
	if total != 2 {
		t.Fatalf("by variant: %d rows, want 2", total)
	}
	if byVariant[0].Units != 3 || byVariant[0].VariantID == nil || *byVariant[0].VariantID != medium.ID {
		t.Errorf("the first variant row is %+v, want the medium's three", byVariant[0])
	}
	if byVariant[0].VariantLabel == "" {
		t.Error("a variant row carries no label")
	}
}

// ---------------------------------------------------------------- the routes

func TestReportRoutesNeedOrdersRead(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	paths := []string{"/api/admin/reports/sales", "/api/admin/reports/top-products"}

	for _, path := range paths {
		if rec := do(t, app, "GET", path); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s unauthenticated = %d, want 401", path, rec.Code)
		}
		rec := do(t, app, "GET", path, withAdmin)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s with the admin token = %d (%s)", path, rec.Code, rec.Body.String())
		}
		var body struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Data) == 0 {
			t.Errorf("%s did not answer in the data envelope: %s", path, rec.Body.String())
		}
	}

	staff := signInAs(t, app, "staff@example.com", RoleStaff)
	for _, path := range paths {
		if rec := do(t, app, "GET", path, bearer(staff)); rec.Code != http.StatusOK {
			t.Errorf("%s as staff = %d, want 200 — staff carry orders.read", path, rec.Code)
		}
	}

	// Re-cut staff to drop orders.read, which is the store's own lever (D24).
	kept := []Right{}
	for _, r := range DefaultRightsOf(RoleStaff) {
		// Reports and payouts each have their own right now: reports is a
		// shape without the orders behind it, payouts is the reconciliation.
		if r != RightReportsRead && r != RightPayoutsRead {
			kept = append(kept, r)
		}
	}
	if _, err := app.Roles().Set(ctx, RoleStaff, kept, nil); err != nil {
		t.Fatalf("re-cut staff: %v", err)
	}
	for _, path := range paths {
		rec := do(t, app, "GET", path, bearer(staff))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s after the re-cut = %d, want 403", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), ".read") {
			t.Errorf("the refusal does not name the right: %s", rec.Body.String())
		}
	}
}

// `to` is exclusive, as it is on GET /api/admin/orders. One date rule per API.
func TestReportsRespectTheDateWindow(t *testing.T) {
	app := newTestApp(t)
	importReportOrders(t, app,
		reportOrder{number: "WIN-1", sku: "IN", createdAt: day(2026, time.March, 2, 12)},
		reportOrder{number: "WIN-2", sku: "BEFORE", createdAt: day(2026, time.February, 27, 12)},
		reportOrder{number: "WIN-3", sku: "ON-TO", createdAt: day(2026, time.March, 4, 0)},
	)

	from, to := bounds("2026-03-01", "2026-03-04")
	totals := currencyBlock(t, salesReport(t, app, SalesQuery{From: from, To: to}),
		app.Config().Currency).Totals
	if totals.Orders != 1 {
		t.Errorf("orders = %d, want 1: `to` is exclusive and `from` inclusive", totals.Orders)
	}

	rows, total := topProducts(t, app, TopProductsQuery{From: from, To: to})
	if total != 1 || rows[0].SKU != "IN" {
		t.Errorf("top products returned %d rows: %+v", total, rows)
	}
}

// Reporting changes no state, so there is nothing for rule 4 to commit
// alongside. This is the guard against someone later "improving" it with an
// audit event written outside a transaction.
func TestReportsEmitNoEvents(t *testing.T) {
	app := newTestApp(t)
	confirmedOrder(t, app, orderSpec{"QUIET-1", 1})

	before, beforeAudits := countOutboxRows(t, app), countAuditRows(t, app)
	salesReport(t, app, SalesQuery{})
	topProducts(t, app, TopProductsQuery{})

	if after := countOutboxRows(t, app); after != before {
		t.Errorf("reading a report wrote %d outbox rows", after-before)
	}
	if after := countAuditRows(t, app); after != beforeAudits {
		t.Errorf("reading a report wrote %d audit rows", after-beforeAudits)
	}
}

func countAuditRows(t *testing.T, app *App) int {
	t.Helper()
	var n int
	if err := app.DB().QueryRowContext(context.Background(),
		`SELECT count(*) FROM admin_audit`).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return n
}

func countOutboxRows(t *testing.T, app *App) int {
	t.Helper()
	var n int
	if err := app.DB().QueryRowContext(context.Background(),
		`SELECT count(*) FROM outbox_events`).Scan(&n); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	return n
}
