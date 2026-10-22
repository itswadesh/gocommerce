package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Asking a gateway what became of a payment, for the order whose webhook never
// arrived. The engine records only what the gateway says, and only through
// MarkPaid.

// statusProvider is a gateway that answers whatever the test tells it to, and
// remembers whether a transaction on the order was open while it was asked.
type statusProvider struct {
	mu       sync.Mutex
	state    PaymentState
	err      error
	asked    int
	app      *App
	lockFree []bool         // per call: could the order row be locked from outside?
	during   func(o *Order) // runs while the gateway is being asked
}

func (*statusProvider) Code() string { return "checkable" }
func (*statusProvider) Initiate(context.Context, *Order, PayOptions) (PaymentIntent, error) {
	return PaymentIntent{Kind: IntentRedirect, Provider: "checkable", Reference: "session-1"}, nil
}
func (p *statusProvider) Status(ctx context.Context, o *Order) (PaymentState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked++
	// Rule 5: the gateway is asked with nothing held. If the engine had the
	// order row locked across this call, NOWAIT would fail here.
	free := false
	if p.app != nil {
		free = InTx(ctx, p.app.db, func(tx *sql.Tx) error {
			var id int64
			return tx.QueryRowContext(ctx,
				`SELECT id FROM orders WHERE id = $1 FOR UPDATE NOWAIT`, o.ID).Scan(&id)
		}) == nil
	}
	p.lockFree = append(p.lockFree, free)
	if p.during != nil {
		p.during(o)
	}
	return p.state, p.err
}

func (p *statusProvider) answer(s PaymentState, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state, p.err = s, err
}

func (p *statusProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.asked
}

func checkableOrder(t *testing.T, app *App, sku string) *Order {
	t.Helper()
	product := simpleProduct(t, app, sku, 2500, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(context.Background(), "checkable", checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	return result.Order
}

func checkPayment(t *testing.T, app *App, id int64) (*PaymentCheck, int, string) {
	t.Helper()
	rec := do(t, app, http.MethodPost, "/api/admin/orders/"+strconv.FormatInt(id, 10)+"/check-payment", withAdmin)
	if rec.Code != http.StatusOK {
		return nil, rec.Code, rec.Body.String()
	}
	var out PaymentCheck
	decodeData(t, rec, &out)
	return &out, rec.Code, rec.Body.String()
}

// The reason it exists: the gateway says paid, and the order is settled
// through MarkPaid with the gateway's reference — confirmed, stock committed,
// order.paid written — exactly as the lost webhook would have done.
func TestCheckPaymentRecordsWhatTheGatewayReports(t *testing.T) {
	gw := &statusProvider{}
	app := newTestApp(t, payModule{provider: gw})
	gw.app = app
	order := checkableOrder(t, app, "CHK-1")

	gw.answer(PaymentState{Paid: true, Reference: "pay_123", Detail: "SUCCESS"}, nil)
	check, code, body := checkPayment(t, app, order.ID)
	if code != http.StatusOK {
		t.Fatalf("check-payment = %d: %s", code, body)
	}
	if !check.Gateway.Paid || check.Gateway.Reference != "pay_123" || check.Gateway.Detail != "SUCCESS" {
		t.Errorf("gateway = %+v, want what the gateway said", check.Gateway)
	}
	if !check.Recorded || check.Provider != "checkable" {
		t.Errorf("recorded = %v, provider = %q; want recorded through checkable", check.Recorded, check.Provider)
	}
	if check.Order == nil || check.Order.PaymentStatus != PaymentPaid || check.Order.Status != OrderConfirmed ||
		check.Order.PaymentReference != "pay_123" {
		t.Fatalf("order = %+v, want paid, confirmed, with the gateway's reference", check.Order)
	}
	if n := len(orderEvents(t, app, order.ID, EventOrderPaid)); n != 1 {
		t.Errorf("order.paid events = %d, want 1", n)
	}
	for i, free := range gw.lockFree {
		if !free {
			t.Errorf("call %d: the order row was locked while the gateway was asked (rule 5)", i+1)
		}
	}

	// Asked again, the gateway still says paid; nothing more is recorded.
	again, code, body := checkPayment(t, app, order.ID)
	if code != http.StatusOK {
		t.Fatalf("second check = %d: %s", code, body)
	}
	if again.Recorded {
		t.Error("a second check recorded the payment again")
	}
	if n := len(orderEvents(t, app, order.ID, EventOrderPaid)); n != 1 {
		t.Errorf("order.paid events after a second check = %d, want still 1", n)
	}
}

// Nothing the gateway did not say is recorded: no payment yet leaves the order
// pending, and a reported failure is answered, not written — a failed attempt
// is not the end of an order whose shopper can still try again.
func TestCheckPaymentNeverSetsAStatusTheGatewayDidNotReport(t *testing.T) {
	gw := &statusProvider{}
	app := newTestApp(t, payModule{provider: gw})
	order := checkableOrder(t, app, "CHK-2")

	for _, state := range []PaymentState{
		{Detail: "ACTIVE"},
		{Failed: true, Detail: "FAILED: card declined"},
	} {
		gw.answer(state, nil)
		check, code, body := checkPayment(t, app, order.ID)
		if code != http.StatusOK {
			t.Fatalf("check-payment = %d: %s", code, body)
		}
		if check.Recorded {
			t.Errorf("%+v: recorded = true", state)
		}
		if check.Gateway != state {
			t.Errorf("gateway = %+v, want %+v back as the gateway said it", check.Gateway, state)
		}
		if check.Order.PaymentStatus != PaymentPending || check.Order.Status != OrderPending {
			t.Errorf("%+v: order is %s / %s, want untouched", state, check.Order.Status, check.Order.PaymentStatus)
		}
	}
	if n := len(orderEvents(t, app, order.ID, EventOrderPaid)); n != 0 {
		t.Errorf("order.paid events = %d, want none", n)
	}
}

// A gateway that cannot be reached is an error, and the order is left alone.
// Its words go to the log and the store's error page, as every 5xx's do (D86).
func TestCheckPaymentGatewayError(t *testing.T) {
	gw := &statusProvider{}
	app := newTestApp(t, payModule{provider: gw})
	order := checkableOrder(t, app, "CHK-3")
	gw.answer(PaymentState{}, errors.New("connection refused"))

	_, code, body := checkPayment(t, app, order.ID)
	if code != http.StatusInternalServerError {
		t.Errorf("check-payment = %d: %s; want 500", code, body)
	}
	if strings.Contains(body, "connection refused") {
		t.Errorf("body = %s: the cause reached the client", body)
	}
	got, err := app.Order().Get(context.Background(), order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PaymentStatus != PaymentPending {
		t.Errorf("payment status = %q, want still pending", got.PaymentStatus)
	}
}

// A method that cannot be asked says so plainly, before anything is called.
func TestCheckPaymentNeedsAMethodThatCanBeAsked(t *testing.T) {
	app := newTestApp(t)
	product := simpleProduct(t, app, "CHK-4", 2500, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(context.Background(), CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatal(err)
	}
	_, code, body := checkPayment(t, app, result.Order.ID)
	if code != http.StatusConflict || !strings.Contains(body, "cannot be asked") {
		t.Errorf("check-payment on cash on delivery = %d: %s; want 409 saying it cannot be asked", code, body)
	}

	if _, code, _ := checkPayment(t, app, 999999); code != http.StatusNotFound {
		t.Errorf("check-payment on a missing order = %d, want 404", code)
	}
}

// Money the gateway took for an order that was cancelled meanwhile is a
// refund problem, and MarkPaid's refusal reaches the operator.
func TestCheckPaymentOnACancelledOrder(t *testing.T) {
	gw := &statusProvider{}
	app := newTestApp(t, payModule{provider: gw})
	order := checkableOrder(t, app, "CHK-5")
	if _, err := app.Order().Cancel(context.Background(), order.ID, "test"); err != nil {
		t.Fatal(err)
	}
	gw.answer(PaymentState{Paid: true, Reference: "pay_late"}, nil)
	_, code, body := checkPayment(t, app, order.ID)
	if code != http.StatusConflict || !strings.Contains(body, "refund") {
		t.Errorf("check-payment on a cancelled order = %d: %s; want 409 about a refund", code, body)
	}
}

// The settings say which methods can be asked, so the panel offers the action
// only where it can work; and the route is orders.write, like Mark paid.
func TestCheckPaymentIsAdvertisedAndGated(t *testing.T) {
	gw := &statusProvider{}
	app := newTestApp(t, payModule{provider: gw})
	for _, p := range app.Settings().PaymentMethods {
		switch p.Code {
		case "checkable":
			if !p.ChecksPayments {
				t.Error("checkable: checks_payments = false, want true")
			}
		case CodeCOD:
			if p.ChecksPayments {
				t.Error("cod: checks_payments = true, want false")
			}
		}
	}
	raw, err := json.Marshal(app.Settings().PaymentMethods)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"checks_payments":true`) {
		t.Errorf("settings JSON = %s, want checks_payments on the gateway", raw)
	}

	var rights []Right
	for _, r := range app.Routes() {
		if r.Pattern == "POST /api/admin/orders/{id}/check-payment" {
			rights = r.Rights
		}
	}
	if len(rights) != 1 || rights[0] != RightOrdersWrite {
		t.Errorf("rights = %v, want orders.write", rights)
	}
}

// A webhook that settles the order while the gateway is being asked has
// recorded the payment; the check did not, and says so.
func TestCheckPaymentRecordedOnlyWhenThisCheckSettled(t *testing.T) {
	gw := &statusProvider{}
	app := newTestApp(t, payModule{provider: gw})
	order := checkableOrder(t, app, "CHK-6")
	gw.during = func(o *Order) {
		if _, err := app.Pay().MarkPaid(context.Background(), o.ID, "pay_from_webhook"); err != nil {
			t.Errorf("webhook mark paid: %v", err)
		}
	}
	gw.answer(PaymentState{Paid: true, Reference: "pay_from_webhook", Amount: &Money{AmountMinor: 2500, Currency: order.Total.Currency}}, nil)
	check, code, body := checkPayment(t, app, order.ID)
	if code != http.StatusOK {
		t.Fatalf("check-payment = %d: %s", code, body)
	}
	if check.Recorded {
		t.Error("recorded = true for a payment the webhook recorded while the check was in flight")
	}
	if check.Order.PaymentStatus != PaymentPaid {
		t.Errorf("order payment = %q, want paid", check.Order.PaymentStatus)
	}
	if n := len(orderEvents(t, app, order.ID, EventOrderPaid)); n != 1 {
		t.Errorf("order.paid events = %d, want 1", n)
	}
}

// The gateway's word is about the amount it took. An order whose total is not
// that amount by the time the payment is recorded — edited while the gateway
// was asked — is refused under the order's lock, not settled.
func TestCheckPaymentRefusesAnOrderWhoseTotalMoved(t *testing.T) {
	gw := &statusProvider{}
	app := newTestApp(t, payModule{provider: gw})
	order := checkableOrder(t, app, "CHK-7")
	gw.answer(PaymentState{Paid: true, Reference: "pay_old_total", Amount: &Money{AmountMinor: 1900, Currency: order.Total.Currency}}, nil)
	_, code, body := checkPayment(t, app, order.ID)
	if code != http.StatusConflict || !strings.Contains(body, "1900") {
		t.Errorf("check-payment = %d: %s; want 409 naming the amount the gateway took", code, body)
	}
	got, err := app.Order().Get(context.Background(), order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PaymentStatus != PaymentPending {
		t.Errorf("payment = %q, want still pending", got.PaymentStatus)
	}
}
