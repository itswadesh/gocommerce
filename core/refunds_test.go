package gocommerce

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// What a refund is, proved against the thing it replaced: a single flip of
// payment_status that recorded no amount, emitted no event, and refused every
// refund after the first.

// ------------------------------------------------------------------ fixtures

// referencedProvider refunds and says what the gateway called it, so the
// optional ReferencedRefunder port is exercised alongside the plain one.
type referencedProvider struct{ ref string }

func (referencedProvider) Code() string { return "referenced" }
func (referencedProvider) Initiate(context.Context, *Order, PayOptions) (PaymentIntent, error) {
	return PaymentIntent{Kind: IntentNone, Provider: "referenced"}, nil
}
func (referencedProvider) Refund(context.Context, *Order, int64) error { return nil }
func (p referencedProvider) RefundWithReference(context.Context, *Order, int64) (string, error) {
	return p.ref, nil
}

// decliningProvider is a gateway that says no.
type decliningProvider struct{}

func (decliningProvider) Code() string { return "declining" }
func (decliningProvider) Initiate(context.Context, *Order, PayOptions) (PaymentIntent, error) {
	return PaymentIntent{Kind: IntentNone, Provider: "declining"}, nil
}
func (decliningProvider) Refund(context.Context, *Order, int64) error {
	return errors.New("the card network refused this refund")
}

// countingProvider blocks until it is released, and counts how many refunds
// actually reached the gateway. Both halves are what a concurrency test needs:
// one to hold two callers inside the provider call at once, the other to prove
// the money only ever left once.
type countingProvider struct {
	mu      sync.Mutex
	entered int
	gate    chan struct{}
}

func (*countingProvider) Code() string { return "counting" }
func (*countingProvider) Initiate(context.Context, *Order, PayOptions) (PaymentIntent, error) {
	return PaymentIntent{Kind: IntentNone, Provider: "counting"}, nil
}
func (p *countingProvider) Refund(context.Context, *Order, int64) error {
	p.mu.Lock()
	p.entered++
	p.mu.Unlock()
	if p.gate != nil {
		<-p.gate
	}
	return nil
}
func (p *countingProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.entered
}

// payModule installs whatever provider a test needs, so the same fixtures serve
// a working, a failing, a slow and a reference-returning gateway.
type payModule struct{ provider PaymentProvider }

func (payModule) Name() string            { return "payments-under-test" }
func (payModule) Migrations() []Migration { return nil }
func (m payModule) Register(app *App) error {
	app.RegisterPayment(m.provider)
	return nil
}

// refundableOrder places an order through a method that can refund and settles
// it, which is where every test here starts.
func refundableOrder(t *testing.T, app *App, method, sku string, priceMinor int64) *Order {
	t.Helper()
	ctx := context.Background()
	product := simpleProduct(t, app, sku, priceMinor, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(ctx, method, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	order, err := app.Pay().MarkPaid(ctx, result.Order.ID, "charge-ref")
	if err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	return order
}

func countRefundRows(t *testing.T, app *App, orderID int64) int {
	t.Helper()
	var n int
	if err := app.DB().QueryRowContext(context.Background(),
		`SELECT count(*) FROM order_refunds WHERE order_id = $1`, orderID).Scan(&n); err != nil {
		t.Fatalf("count refund rows: %v", err)
	}
	return n
}

func refundedMinor(t *testing.T, app *App, orderID int64) int64 {
	t.Helper()
	var n int64
	if err := app.DB().QueryRowContext(context.Background(),
		`SELECT refunded_minor FROM orders WHERE id = $1`, orderID).Scan(&n); err != nil {
		t.Fatalf("read refunded_minor: %v", err)
	}
	return n
}

// orderEvents reads the outbox directly, because what a consumer will be handed
// is the stored payload and not whatever the service had in memory.
func orderEvents(t *testing.T, app *App, orderID int64, name string) []OrderEvent {
	t.Helper()
	rows, err := app.DB().QueryContext(context.Background(), `
		SELECT payload FROM outbox_events
		WHERE aggregate_type = $1 AND aggregate_id = $2 AND event_name = $3
		ORDER BY id`, AggregateOrder, orderID, name)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	defer rows.Close()
	var out []OrderEvent
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("scan payload: %v", err)
		}
		var ev OrderEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		out = append(out, ev)
	}
	return out
}

// ------------------------------------------------------------------ the gap

// TestRefundRecordsWhatWentBack is the whole gap in one test: before this, the
// amount was stored nowhere and no event was written.
func TestRefundRecordsWhatWentBack(t *testing.T) {
	app := newTestApp(t, refundableModule{})
	ctx := context.Background()

	order := refundableOrder(t, app, "refundable", "REF-FULL", 2000)
	refunded, err := app.Pay().Refund(ctx, order.ID,
		RefundRequest{Reason: "arrived broken"}, nil)
	if err != nil {
		t.Fatalf("refund: %v", err)
	}

	if refunded.PaymentStatus != PaymentRefunded {
		t.Errorf("payment status = %q, want refunded", refunded.PaymentStatus)
	}
	if refunded.Refunded.AmountMinor != 2000 || refunded.Refunded.Currency == "" {
		t.Errorf("refunded = %+v, want 2000 with a currency", refunded.Refunded)
	}
	if len(refunded.Refunds) != 1 {
		t.Fatalf("refund records = %d, want 1", len(refunded.Refunds))
	}
	r := refunded.Refunds[0]
	if r.Status != RefundSucceeded || r.Amount.AmountMinor != 2000 ||
		r.Reason != "arrived broken" || r.Provider != "refundable" {
		t.Errorf("refund record = %+v, want a succeeded 2000 through refundable with the reason", r)
	}

	events := orderEvents(t, app, order.ID, EventOrderRefunded)
	if len(events) != 1 {
		t.Fatalf("order.refunded events = %d, want 1", len(events))
	}
	ev := events[0]
	if ev.Refund == nil {
		t.Fatal("the event carries no refund block, so a consumer cannot tell what came back")
	}
	if ev.Refund.AmountMinor != 2000 || ev.Refund.RefundedMinor != 2000 || ev.Refund.RemainingMinor != 0 {
		t.Errorf("refund payload = %+v, want 2000 / 2000 / 0", *ev.Refund)
	}
	if ev.RefundedMinor != 2000 {
		t.Errorf("refunded_minor on the event = %d, want 2000", ev.RefundedMinor)
	}
}

// TestPartialRefundLeavesTheOrderPaid proves D36: partiality is a number, not a
// status — and the store's own record stops being a lie.
func TestPartialRefundLeavesTheOrderPaid(t *testing.T) {
	app := newTestApp(t, refundableModule{})
	ctx := context.Background()

	order := refundableOrder(t, app, "refundable", "REF-PART", 2000)
	got, err := app.Pay().Refund(ctx, order.ID, RefundRequest{AmountMinor: 600}, nil)
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if got.PaymentStatus != PaymentPaid {
		t.Errorf("payment status = %q, want paid: the store still holds 1400 of it", got.PaymentStatus)
	}
	if got.Refunded.AmountMinor != 600 {
		t.Errorf("refunded = %d, want 600", got.Refunded.AmountMinor)
	}
	if len(got.Refunds) != 1 || got.Refunds[0].Amount.AmountMinor != 600 ||
		got.Refunds[0].Amount.Currency == "" {
		t.Errorf("refunds = %+v, want one 600 with a currency", got.Refunds)
	}
}

// TestSecondPartialRefundIsAllowedUpToTheRemainder is the gap's second
// consequence: the old not-paid guard locked the operator out after the first
// partial refund.
func TestSecondPartialRefundIsAllowedUpToTheRemainder(t *testing.T) {
	app := newTestApp(t, refundableModule{})
	ctx := context.Background()

	order := refundableOrder(t, app, "refundable", "REF-TWICE", 2000)
	if _, err := app.Pay().Refund(ctx, order.ID, RefundRequest{AmountMinor: 600}, nil); err != nil {
		t.Fatalf("first refund: %v", err)
	}
	second, err := app.Pay().Refund(ctx, order.ID, RefundRequest{AmountMinor: 1400}, nil)
	if err != nil {
		t.Fatalf("second refund: %v", err)
	}
	if second.PaymentStatus != PaymentRefunded {
		t.Errorf("payment status = %q, want refunded once the parts add up", second.PaymentStatus)
	}
	if second.Refunded.AmountMinor != 2000 || len(second.Refunds) != 2 {
		t.Errorf("after two refunds: refunded %d over %d records, want 2000 over 2",
			second.Refunded.AmountMinor, len(second.Refunds))
	}

	_, err = app.Pay().Refund(ctx, order.ID, RefundRequest{AmountMinor: 1}, nil)
	if err == nil {
		t.Fatal("a third refund on a fully refunded order should be refused")
	}
	var apiErr *APIError
	if !asAPIError(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Errorf("error = %v, want a 409", err)
	}
}

// TestRefundOfZeroMeansTheRemainder pins the changed meaning of an omitted
// amount: everything not yet refunded, which is the only reading that stays
// true on the second call.
func TestRefundOfZeroMeansTheRemainder(t *testing.T) {
	app := newTestApp(t, refundableModule{})
	ctx := context.Background()

	order := refundableOrder(t, app, "refundable", "REF-REST", 1000)
	if _, err := app.Pay().Refund(ctx, order.ID, RefundRequest{AmountMinor: 400}, nil); err != nil {
		t.Fatalf("first refund: %v", err)
	}
	rest, err := app.Pay().Refund(ctx, order.ID, RefundRequest{}, nil)
	if err != nil {
		t.Fatalf("refund the rest: %v", err)
	}
	if rest.Refunded.AmountMinor != 1000 {
		t.Errorf("refunded = %d, want exactly the total", rest.Refunded.AmountMinor)
	}
	if n := len(rest.Refunds); n != 2 || rest.Refunds[1].Amount.AmountMinor != 600 {
		t.Errorf("second refund = %+v, want 600 and not the whole total", rest.Refunds)
	}
}

// TestRefundRefusesMoreThanRemains — the check has to happen before the money
// moves, so the gateway is never asked for an amount the order cannot afford.
func TestRefundRefusesMoreThanRemains(t *testing.T) {
	counter := &countingProvider{}
	app := newTestApp(t, payModule{provider: counter})
	ctx := context.Background()

	order := refundableOrder(t, app, "counting", "REF-OVER", 1000)
	if _, err := app.Pay().Refund(ctx, order.ID, RefundRequest{AmountMinor: 600}, nil); err != nil {
		t.Fatalf("first refund: %v", err)
	}
	_, err := app.Pay().Refund(ctx, order.ID, RefundRequest{AmountMinor: 500}, nil)
	if err == nil {
		t.Fatal("refunding more than remains should be refused")
	}
	var apiErr *APIError
	if !asAPIError(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("error = %v, want a 400", err)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("message = %q, want it to name what is still refundable", err.Error())
	}
	if n := countRefundRows(t, app, order.ID); n != 1 {
		t.Errorf("refund rows = %d, want the refused one not to have been written", n)
	}
	if got := refundedMinor(t, app, order.ID); got != 600 {
		t.Errorf("refunded_minor = %d, want 600", got)
	}
	if counter.count() != 1 {
		t.Errorf("the provider was entered %d times, want 1: the refusal must precede the money",
			counter.count())
	}
}

// TestConcurrentRefundsCannotExceedTheTotal is the double-spend the committed
// pending row exists for, and the test a check-then-call design cannot pass:
// rule 5 forbids the transaction that would otherwise hold it.
func TestConcurrentRefundsCannotExceedTheTotal(t *testing.T) {
	counter := &countingProvider{gate: make(chan struct{})}
	app := newTestApp(t, payModule{provider: counter})
	ctx := context.Background()

	order := refundableOrder(t, app, "counting", "REF-RACE", 1000)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = app.Pay().Refund(ctx, order.ID, RefundRequest{AmountMinor: 1000}, nil)
		}(i)
	}
	// Both callers are past the reservation by the time either is let through
	// the gateway, which is exactly the window the old shape lost money in.
	close(counter.gate)
	wg.Wait()

	var ok, refused int
	for _, err := range errs {
		if err == nil {
			ok++
			continue
		}
		refused++
		var apiErr *APIError
		if !asAPIError(err, &apiErr) ||
			(apiErr.Status != http.StatusConflict && apiErr.Status != http.StatusBadRequest) {
			t.Errorf("the losing refund failed with %v, want a 409 or a 400", err)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("%d refunds succeeded and %d were refused, want exactly one of each", ok, refused)
	}
	if counter.count() != 1 {
		t.Errorf("the provider was entered %d times, want 1", counter.count())
	}

	var succeeded int64
	if err := app.DB().QueryRowContext(ctx, `
		SELECT coalesce(sum(amount_minor), 0) FROM order_refunds
		WHERE order_id = $1 AND status = 'succeeded'`, order.ID).Scan(&succeeded); err != nil {
		t.Fatalf("sum the ledger: %v", err)
	}
	if got := refundedMinor(t, app, order.ID); succeeded != got || got != 1000 {
		t.Errorf("ledger %d, refunded_minor %d, total 1000 — all three must agree", succeeded, got)
	}
}

// TestFailedRefundIsRecordedAndDoesNotCount — a declined refund neither hides
// itself nor blocks the retry.
func TestFailedRefundIsRecordedAndDoesNotCount(t *testing.T) {
	rec := &recordingNotifier{}
	app := newTestApp(t, payModule{provider: decliningProvider{}}, &notifyModule{rec: rec})
	ctx := context.Background()

	order := refundableOrder(t, app, "declining", "REF-NO", 1000)
	_, err := app.Pay().Refund(ctx, order.ID, RefundRequest{AmountMinor: 400}, nil)
	if err == nil {
		t.Fatal("a declined refund should be reported")
	}
	if !strings.Contains(err.Error(), "nothing was recorded as having moved") {
		t.Errorf("error = %q, want it to say what is and is not known", err.Error())
	}

	var status, message string
	if err := app.DB().QueryRowContext(ctx,
		`SELECT status, error FROM order_refunds WHERE order_id = $1`, order.ID).
		Scan(&status, &message); err != nil {
		t.Fatalf("read the refund row: %v", err)
	}
	if status != RefundFailed || !strings.Contains(message, "refused") {
		t.Errorf("row = (%q, %q), want a failed row carrying what the gateway said", status, message)
	}
	if got := refundedMinor(t, app, order.ID); got != 0 {
		t.Errorf("refunded_minor = %d, want 0: nothing moved", got)
	}
	if _, err := app.DrainOutbox(ctx); err != nil {
		t.Fatalf("drain outbox: %v", err)
	}
	if n := rec.count(EventOrderRefunded); n != 0 {
		t.Errorf("order.refunded notifications = %d, want 0", n)
	}
}

// TestRefundEmitsOrderRefunded: one event per refund, and it reaches a template.
func TestRefundEmitsOrderRefunded(t *testing.T) {
	rec := &recordingNotifier{}
	app := newTestApp(t, refundableModule{}, &notifyModule{rec: rec})
	ctx := context.Background()

	order := refundableOrder(t, app, "refundable", "REF-EVENT", 2000)
	if _, err := app.Pay().Refund(ctx, order.ID,
		RefundRequest{AmountMinor: 800, Reason: "one item returned"}, nil); err != nil {
		t.Fatalf("first refund: %v", err)
	}
	if _, err := app.Pay().Refund(ctx, order.ID, RefundRequest{AmountMinor: 1200}, nil); err != nil {
		t.Fatalf("second refund: %v", err)
	}

	events := orderEvents(t, app, order.ID, EventOrderRefunded)
	if len(events) != 2 {
		t.Fatalf("order.refunded events = %d, want one per refund", len(events))
	}
	want := []struct {
		amount, refunded, remaining int64
		status                      string
	}{
		{800, 800, 1200, PaymentPaid},
		{1200, 2000, 0, PaymentRefunded},
	}
	for i, w := range want {
		ev := events[i]
		if ev.Refund == nil {
			t.Fatalf("event %d carries no refund block", i)
		}
		if ev.Refund.AmountMinor != w.amount || ev.Refund.RefundedMinor != w.refunded ||
			ev.Refund.RemainingMinor != w.remaining {
			t.Errorf("event %d refund = %+v, want %d / %d / %d",
				i, *ev.Refund, w.amount, w.refunded, w.remaining)
		}
		if ev.PaymentStatus != w.status {
			t.Errorf("event %d payment_status = %q, want %q", i, ev.PaymentStatus, w.status)
		}
	}
	if events[0].Reason != "one item returned" {
		t.Errorf("reason on the event = %q, want the one the operator typed", events[0].Reason)
	}

	if _, err := app.DrainOutbox(ctx); err != nil {
		t.Fatalf("drain outbox: %v", err)
	}
	if n := rec.count(EventOrderRefunded); n != 2 {
		t.Fatalf("order.refunded notifications = %d, want 2", n)
	}
	// What proves the event reaches a template, which is the third consequence
	// of the gap: until now a notifier heard nothing at all.
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, note := range rec.sent {
		if note.Event != EventOrderRefunded {
			continue
		}
		if note.Data["refund_amount_minor"] == "" || note.Data["refund_remaining_minor"] == "" {
			t.Errorf("notification data = %v, want the refund figures", note.Data)
		}
	}
}

// TestReferencedRefunderIsPreferred: the optional port is detected, and a module
// that has not adopted it is not downgraded.
func TestReferencedRefunderIsPreferred(t *testing.T) {
	ctx := context.Background()

	app := newTestApp(t, payModule{provider: referencedProvider{ref: "re_3QabcXYZ"}})
	order := refundableOrder(t, app, "referenced", "REF-ID", 1000)
	got, err := app.Pay().Refund(ctx, order.ID, RefundRequest{}, nil)
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if len(got.Refunds) != 1 || got.Refunds[0].ProviderReference != "re_3QabcXYZ" {
		t.Errorf("refunds = %+v, want the gateway's own id recorded", got.Refunds)
	}

	plain := newTestApp(t, refundableModule{})
	plainOrder := refundableOrder(t, plain, "refundable", "REF-NOID", 1000)
	unreferenced, err := plain.Pay().Refund(ctx, plainOrder.ID, RefundRequest{}, nil)
	if err != nil {
		t.Fatalf("refund through a plain Refunder: %v", err)
	}
	if len(unreferenced.Refunds) != 1 || unreferenced.Refunds[0].ProviderReference != "" {
		t.Errorf("refunds = %+v, want an empty reference and a successful refund",
			unreferenced.Refunds)
	}
}

// ---------------------------------------------------------- stranded refunds

// ageRefund backdates a pending row past refundStaleAfter, which is the state a
// process death between the gateway and the settling transaction leaves behind.
func ageRefund(t *testing.T, app *App, refundID int64) {
	t.Helper()
	if _, err := app.DB().ExecContext(context.Background(),
		`UPDATE order_refunds SET created_at = now() - interval '1 hour' WHERE id = $1`,
		refundID); err != nil {
		t.Fatalf("age the refund: %v", err)
	}
}

// TestSettleRefundResolvesAStrandedPending proves the recovery path exists in
// the service, so clearing a stranded row is never SQL against a core table.
func TestSettleRefundResolvesAStrandedPending(t *testing.T) {
	app := newTestApp(t, refundableModule{})
	ctx := context.Background()

	order := refundableOrder(t, app, "refundable", "REF-STRAND", 1000)
	refundID, amount, err := app.Pay().reserveRefund(ctx, order.ID, 400, "goodwill", nil)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if amount != 400 {
		t.Fatalf("reserved %d, want 400", amount)
	}

	// Too young to settle: it may still be in flight.
	_, err = app.Pay().SettleRefund(ctx, order.ID, refundID,
		RefundSettlement{Outcome: RefundSucceeded}, nil)
	if err == nil {
		t.Error("settling a refund that may still be running should be refused")
	}

	ageRefund(t, app, refundID)
	settled, err := app.Pay().SettleRefund(ctx, order.ID, refundID,
		RefundSettlement{Outcome: RefundSucceeded, ProviderReference: "re_by_hand"}, nil)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if settled.Refunded.AmountMinor != 400 || settled.PaymentStatus != PaymentPaid {
		t.Errorf("after settling: refunded %d, status %q — want 400 and paid",
			settled.Refunded.AmountMinor, settled.PaymentStatus)
	}
	if len(settled.Refunds) != 1 || settled.Refunds[0].ProviderReference != "re_by_hand" {
		t.Errorf("refunds = %+v, want the reference the operator quoted", settled.Refunds)
	}
	if n := len(orderEvents(t, app, order.ID, EventOrderRefunded)); n != 1 {
		t.Errorf("order.refunded events = %d, want 1: settling announces it exactly as a refund does", n)
	}
	// Settling it a second time is a conflict, not a second movement of money.
	if _, err := app.Pay().SettleRefund(ctx, order.ID, refundID,
		RefundSettlement{Outcome: RefundSucceeded}, nil); err == nil {
		t.Error("settling an already-settled refund should be refused")
	}
}

// TestSettleRefundCanRecordThatNothingMoved — the other half: the gateway has no
// such refund, so the row is closed and the order is untouched.
func TestSettleRefundCanRecordThatNothingMoved(t *testing.T) {
	app := newTestApp(t, refundableModule{})
	ctx := context.Background()

	order := refundableOrder(t, app, "refundable", "REF-NOMOVE", 1000)
	refundID, _, err := app.Pay().reserveRefund(ctx, order.ID, 400, "", nil)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	ageRefund(t, app, refundID)

	if _, err := app.Pay().SettleRefund(ctx, order.ID, refundID,
		RefundSettlement{Outcome: "maybe"}, nil); err == nil {
		t.Error("an outcome that is neither succeeded nor failed should be refused")
	}
	settled, err := app.Pay().SettleRefund(ctx, order.ID, refundID,
		RefundSettlement{Outcome: RefundFailed, Note: "the gateway has no such refund"}, nil)
	if err != nil {
		t.Fatalf("settle as failed: %v", err)
	}
	if settled.Refunded.AmountMinor != 0 || settled.PaymentStatus != PaymentPaid {
		t.Errorf("after a failed settlement: refunded %d, status %q — want 0 and paid",
			settled.Refunded.AmountMinor, settled.PaymentStatus)
	}
	if n := len(orderEvents(t, app, order.ID, EventOrderRefunded)); n != 0 {
		t.Errorf("order.refunded events = %d, want 0: nothing about the order changed", n)
	}
	// And the amount is released, so the refund can be tried again.
	if _, err := app.Pay().Refund(ctx, order.ID, RefundRequest{}, nil); err != nil {
		t.Fatalf("refund after a stranded row was closed: %v", err)
	}
	if got := refundedMinor(t, app, order.ID); got != 1000 {
		t.Errorf("refunded_minor = %d, want the whole total", got)
	}
}

// --------------------------------------------------------------- the guards

// TestEditLinesRefusesAnOrderWithARefund: without the widened guard this passes
// silently, because a partly refunded order is still "paid". It is also what
// proves orders_refunded_within_total can never be reached from the API.
func TestEditLinesRefusesAnOrderWithARefund(t *testing.T) {
	app := newTestApp(t, refundableModule{})
	ctx := context.Background()

	order := refundableOrder(t, app, "refundable", "REF-EDIT", 2000)
	if _, err := app.Pay().Refund(ctx, order.ID, RefundRequest{AmountMinor: 500}, nil); err != nil {
		t.Fatalf("refund: %v", err)
	}
	before, err := app.Order().Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	_, _, err = app.Order().EditLines(ctx, order.ID, OrderEdit{
		Lines: []OrderLineEdit{{ID: before.Lines[0].ID, Quantity: 1}},
	})
	if err == nil {
		t.Fatal("editing the lines of a partly refunded order should be refused")
	}
	if !strings.Contains(err.Error(), "refunded") {
		t.Errorf("error = %q, want it to name the refund", err.Error())
	}
	after, err := app.Order().Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Total.AmountMinor != before.Total.AmountMinor {
		t.Errorf("total moved from %d to %d", before.Total.AmountMinor, after.Total.AmountMinor)
	}
}

// TestMarkUnpaidRefusesAPartiallyRefundedOrder is the regression the running
// total would otherwise open: the switch catches the full case only because the
// status flipped.
func TestMarkUnpaidRefusesAPartiallyRefundedOrder(t *testing.T) {
	app := newTestApp(t, refundableModule{})
	ctx := context.Background()

	order := refundableOrder(t, app, "refundable", "REF-UNPAY", 2000)
	if _, err := app.Pay().Refund(ctx, order.ID, RefundRequest{AmountMinor: 500}, nil); err != nil {
		t.Fatalf("refund: %v", err)
	}
	if _, err := app.Pay().MarkUnpaid(ctx, order.ID); err == nil {
		t.Fatal("a partly refunded order was allowed to become unpaid")
	}
	if _, err := app.Pay().MarkFailed(ctx, order.ID, "declined"); err == nil {
		t.Fatal("a partly refunded order was allowed to be marked failed")
	}
	got, err := app.Order().Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PaymentStatus != PaymentPaid || got.Refunded.AmountMinor != 500 {
		t.Errorf("order = (%q, %d), want the payment and the refund both untouched",
			got.PaymentStatus, got.Refunded.AmountMinor)
	}
}

// TestUpdateRefusesAProviderChangeAfterAPartialRefund: the money went out
// through the method on the order, and rewriting it would point the record at a
// gateway that never saw it.
func TestUpdateRefusesAProviderChangeAfterAPartialRefund(t *testing.T) {
	app := newTestApp(t, refundableModule{})
	ctx := context.Background()

	order := refundableOrder(t, app, "refundable", "REF-PROV", 2000)
	if _, err := app.Pay().Refund(ctx, order.ID, RefundRequest{AmountMinor: 500}, nil); err != nil {
		t.Fatalf("refund: %v", err)
	}
	method := CodeCOD
	if _, err := app.Order().Update(ctx, order.ID, OrderPatch{PaymentProvider: &method}); err == nil {
		t.Fatal("the payment method was changed after money had gone back through it")
	}
}

// TestCustomerSpendSubtractsRefunds guards the consequence of keeping the order
// at "paid": lifetime spend has to mean what the customer actually kept.
func TestCustomerSpendSubtractsRefunds(t *testing.T) {
	app := newTestApp(t, refundableModule{})
	ctx := context.Background()

	order := refundableOrder(t, app, "refundable", "REF-SPEND", 1000)
	if _, err := app.Pay().Refund(ctx, order.ID, RefundRequest{AmountMinor: 400}, nil); err != nil {
		t.Fatalf("refund: %v", err)
	}
	customers, _, err := app.Order().Customers(ctx, CustomerQuery{Search: "shopper@example.com"})
	if err != nil {
		t.Fatalf("customers: %v", err)
	}
	if len(customers) != 1 {
		t.Fatalf("customers = %d, want 1", len(customers))
	}
	if customers[0].Spent.AmountMinor != 600 {
		t.Errorf("spent = %d, want 600", customers[0].Spent.AmountMinor)
	}
}

// TestDoctorReconcilesRefunds: OK when the two agree, FAIL when somebody has
// written orders by hand, WARN on a refund left in flight.
func TestDoctorReconcilesRefunds(t *testing.T) {
	app := newTestApp(t, refundableModule{})
	ctx := context.Background()

	order := refundableOrder(t, app, "refundable", "REF-DOCTOR", 1000)
	if _, err := app.Pay().Refund(ctx, order.ID, RefundRequest{AmountMinor: 400}, nil); err != nil {
		t.Fatalf("refund: %v", err)
	}
	if d := findCheck(t, app.Diagnose(ctx), "refunds"); d.Status != StatusOK {
		t.Errorf("check = %+v, want ok", d)
	}

	stranded, _, err := app.Pay().reserveRefund(ctx, order.ID, 100, "", nil)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	ageRefund(t, app, stranded)
	if d := findCheck(t, app.Diagnose(ctx), "refunds"); d.Status != StatusWarn {
		t.Errorf("check = %+v, want a warning about a refund in flight", d)
	}
	if _, err := app.Pay().SettleRefund(ctx, order.ID, stranded,
		RefundSettlement{Outcome: RefundFailed}, nil); err != nil {
		t.Fatalf("settle: %v", err)
	}

	// The one thing the column can be wrong about, done the one way it can
	// happen: a write to orders that went nowhere near the service.
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE orders SET refunded_minor = 900 WHERE id = $1`, order.ID); err != nil {
		t.Fatalf("desynchronise: %v", err)
	}
	d := findCheck(t, app.Diagnose(ctx), "refunds")
	if d.Status != StatusFail {
		t.Errorf("check = %+v, want a failure", d)
	}
	if !strings.Contains(d.Hint, "by hand") {
		t.Errorf("hint = %q, want it to say where the drift came from", d.Hint)
	}
}

func findCheck(t *testing.T, rep Report, name string) Diagnostic {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q check in the report", name)
	return Diagnostic{}
}

// ------------------------------------------------------------------- routes

// TestRefundRouteAcceptsAnAmountAndAReason walks the HTTP surface, including the
// bodyless POST the panel has always been able to send.
func TestRefundRouteAcceptsAnAmountAndAReason(t *testing.T) {
	app := newTestApp(t, refundableModule{})

	order := refundableOrder(t, app, "refundable", "REF-HTTP", 2000)
	path := fmt.Sprintf("/api/admin/orders/%d/refund", order.ID)

	if rec := doBody(t, app, "POST", path, `{"amount_minor":800,"reason":"one item returned"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated refund = %d, want 401", rec.Code)
	}

	rec := doBody(t, app, "POST", path,
		`{"amount_minor":800,"reason":"one item returned"}`, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("refund = %d: %s", rec.Code, rec.Body)
	}
	var got Order
	decodeData(t, rec, &got)
	if got.Refunded.AmountMinor != 800 || got.PaymentStatus != PaymentPaid {
		t.Errorf("after a partial refund: refunded %d, status %q — want 800 and paid",
			got.Refunded.AmountMinor, got.PaymentStatus)
	}
	if len(got.Refunds) != 1 || got.Refunds[0].Reason != "one item returned" {
		t.Errorf("refunds = %+v, want the reason recorded", got.Refunds)
	}
	// A script holding the static token is the system, not a person: attributed
	// to nobody, and not refused for it.
	if got.Refunds[0].By != "" {
		t.Errorf("by = %q, want it empty for the static admin token", got.Refunds[0].By)
	}

	rest := doBody(t, app, "POST", path, "", withAdmin)
	if rest.Code != http.StatusOK {
		t.Fatalf("bodyless refund = %d: %s", rest.Code, rest.Body)
	}
	decodeData(t, rest, &got)
	if got.Refunded.AmountMinor != 2000 || got.PaymentStatus != PaymentRefunded {
		t.Errorf("after the rest: refunded %d, status %q — want 2000 and refunded",
			got.Refunded.AmountMinor, got.PaymentStatus)
	}
}

// TestRefundAttributesTheOperator — who authorised it is a fact about the past,
// snapshotted so it outlives the account.
func TestRefundAttributesTheOperator(t *testing.T) {
	app := newTestApp(t, refundableModule{})

	token := signInAs(t, app, "refunder@example.com", RoleOwner)
	order := refundableOrder(t, app, "refundable", "REF-WHO", 1000)

	rec := doBody(t, app, "POST", fmt.Sprintf("/api/admin/orders/%d/refund", order.ID),
		`{"amount_minor":250}`, bearer(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("refund = %d: %s", rec.Code, rec.Body)
	}
	var got Order
	decodeData(t, rec, &got)
	if len(got.Refunds) != 1 || got.Refunds[0].By != "refunder@example.com" {
		t.Fatalf("refunds = %+v, want the operator's email", got.Refunds)
	}

	var superuserID *int64
	if err := app.DB().QueryRowContext(context.Background(),
		`SELECT superuser_id FROM order_refunds WHERE order_id = $1`, order.ID).
		Scan(&superuserID); err != nil {
		t.Fatalf("read superuser_id: %v", err)
	}
	if superuserID == nil {
		t.Error("superuser_id is null, so the refund points at nobody")
	}
}

// TestSettleRefundRoute is the same recovery path over HTTP, where an operator
// will actually reach it.
func TestSettleRefundRoute(t *testing.T) {
	app := newTestApp(t, refundableModule{})
	ctx := context.Background()

	order := refundableOrder(t, app, "refundable", "REF-SETTLE-HTTP", 1000)
	refundID, _, err := app.Pay().reserveRefund(ctx, order.ID, 300, "", nil)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	ageRefund(t, app, refundID)

	path := fmt.Sprintf("/api/admin/orders/%d/refunds/%d/settle", order.ID, refundID)
	if rec := doBody(t, app, "POST", path, `{"outcome":"nonsense"}`, withAdmin); rec.Code != http.StatusBadRequest {
		t.Errorf("a nonsense outcome = %d, want 400", rec.Code)
	}
	missing := fmt.Sprintf("/api/admin/orders/%d/refunds/%d/settle", order.ID, refundID+999)
	if rec := doBody(t, app, "POST", missing, `{"outcome":"failed"}`, withAdmin); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown refund = %d, want 404", rec.Code)
	}
	rec := doBody(t, app, "POST", path, `{"outcome":"succeeded","provider_reference":"re_hand"}`, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("settle = %d: %s", rec.Code, rec.Body)
	}
	var got Order
	decodeData(t, rec, &got)
	if got.Refunded.AmountMinor != 300 {
		t.Errorf("refunded = %d, want 300", got.Refunded.AmountMinor)
	}
}

// TestGuestSeesOnlyMoneyThatMoved proves the guest path did not quietly acquire
// staff identities and internal notes (AGENTS rule 8).
func TestGuestSeesOnlyMoneyThatMoved(t *testing.T) {
	app := newTestApp(t, refundableModule{})
	ctx := context.Background()

	product := simpleProduct(t, app, "REF-GUEST", 2000, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(ctx, "refundable", checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	order := result.Order
	if _, err := app.Pay().MarkPaid(ctx, order.ID, "charge"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}

	if _, err := app.Pay().Refund(ctx, order.ID,
		RefundRequest{AmountMinor: 400, Reason: "goodwill, do not repeat"}, nil); err != nil {
		t.Fatalf("refund: %v", err)
	}
	// One in flight and one that failed, so all three statuses are present.
	if _, _, err := app.Pay().reserveRefund(ctx, order.ID, 100, "still asking", nil); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	failed, _, err := app.Pay().reserveRefund(ctx, order.ID, 100, "did not work", nil)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	app.Pay().failRefund(ctx, failed, "the gateway said no")

	admin, err := app.Order().Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("admin read: %v", err)
	}
	if len(admin.Refunds) != 3 {
		t.Fatalf("admin sees %d refunds, want all three", len(admin.Refunds))
	}

	guest, err := app.Order().GetForGuest(ctx, order.Number, order.AccessToken)
	if err != nil {
		t.Fatalf("guest read: %v", err)
	}
	if len(guest.Refunds) != 1 {
		t.Fatalf("guest sees %d refunds, want only the one that moved money", len(guest.Refunds))
	}
	r := guest.Refunds[0]
	if r.Reason != "" || r.By != "" || r.Error != "" {
		t.Errorf("guest refund = %+v, want the operator's note, name and error stripped", r)
	}
	if r.Amount.AmountMinor != 400 || guest.Refunded.AmountMinor != 400 {
		t.Errorf("guest sees %d of %d refunded, want 400 both ways",
			r.Amount.AmountMinor, guest.Refunded.AmountMinor)
	}
}
