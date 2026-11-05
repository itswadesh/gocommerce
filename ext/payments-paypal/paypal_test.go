package paypal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

const webhookID = "WH-TEST"

// paypalStub answers the calls this module makes, in the shapes PayPal
// documents, and remembers what it was asked.
type paypalStub struct {
	mu        sync.Mutex
	status    string // the PayPal order's status
	customID  string
	total     string
	captured  int
	created   map[string]any
	requestID string
	verified  []byte
	sigOK     bool
	refunds   []string
	tokens    int
	mod       *Module
}

func (s *paypalStub) order() string {
	captures := ""
	if s.status == "COMPLETED" {
		captures = fmt.Sprintf(`,"payments":{"captures":[{"id":"CAP1","status":"COMPLETED","custom_id":%q,"amount":{"currency_code":"USD","value":%q}}]}`, s.customID, s.total)
	}
	return fmt.Sprintf(`{"id":"PP1","status":%q,"purchase_units":[{"custom_id":%q%s}],"links":[{"rel":"payer-action","href":"https://paypal.test/approve/PP1"}]}`,
		s.status, s.customID, captures)
}

func newApp(t *testing.T, currency string) (*gocommerce.App, *paypalStub) {
	t.Helper()
	stub := &paypalStub{status: "CREATED", total: "25.00", sigOK: true}
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/v1/oauth2/token" {
			if user, pass, ok := r.BasicAuth(); !ok || user != "cid" || pass != "secret" {
				http.Error(w, `{"name":"invalid_client","message":"Client Authentication failed"}`, http.StatusUnauthorized)
				return
			}
			stub.tokens++
			fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, `{"name":"AUTHENTICATION_FAILURE","message":"no token"}`, http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/v1/notifications/verify-webhook-signature":
			stub.verified = body
			result := "FAILURE"
			if stub.sigOK {
				result = "SUCCESS"
			}
			fmt.Fprintf(w, `{"verification_status":%q}`, result)
		case r.Method == http.MethodPost && r.URL.Path == "/v2/checkout/orders":
			_ = json.Unmarshal(body, &stub.created)
			stub.requestID = r.Header.Get("PayPal-Request-Id")
			unit := stub.created["purchase_units"].([]any)[0].(map[string]any)
			stub.customID = unit["custom_id"].(string)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, stub.order())
		case r.Method == http.MethodPost && r.URL.Path == "/v2/checkout/orders/PP1/capture":
			stub.captured++
			stub.status = "COMPLETED"
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, stub.order())
		case r.Method == http.MethodGet && r.URL.Path == "/v2/checkout/orders/PP1":
			fmt.Fprint(w, stub.order())
		case r.Method == http.MethodGet && r.URL.Path == "/v2/payments/captures/CAP1":
			fmt.Fprintf(w, `{"id":"CAP1","status":"COMPLETED","custom_id":%q,"amount":{"currency_code":"USD","value":%q}}`, stub.customID, stub.total)
		case r.Method == http.MethodPost && r.URL.Path == "/v2/payments/captures/CAP1/refund":
			var in struct {
				Amount amount `json:"amount"`
			}
			_ = json.Unmarshal(body, &in)
			stub.refunds = append(stub.refunds, in.Amount.Value)
			fmt.Fprint(w, `{"id":"REF1","status":"COMPLETED"}`)
		default:
			http.Error(w, `{"name":"RESOURCE_NOT_FOUND","message":"not found"}`, http.StatusNotFound)
		}
	})
	mod := New(Config{ClientID: "cid", ClientSecret: "secret", WebhookID: webhookID, BaseURL: server.URL})
	stub.mod = mod
	return gctest.NewWithConfig(t, gocommerce.Config{AllowPrivateURLs: true, Currency: currency}, mod), stub
}

func place(t *testing.T, app *gocommerce.App) *gocommerce.CheckoutResult {
	t.Helper()
	ctx := context.Background()
	product := gctest.CreateProduct(t, app, "PP-1", 2500, 10)
	cart, err := app.Cart().Create(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Cart().AddLine(ctx, cart.Token, product.DefaultVariant().ID, 1); err != nil {
		t.Fatal(err)
	}
	result, err := app.Order().Checkout(ctx, "paypal", gocommerce.CheckoutInput{
		CartID: cart.Token, Email: "a@example.com", Name: "A", ReturnURL: "https://shop.test/return",
		Address: gocommerce.Address{Line1: "1 Test St", City: "T", State: "CA", PostalCode: "94117", Country: "US", Phone: "+15555550123"},
	}, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	return result
}

func webhook(t *testing.T, app *gocommerce.App, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/checkout/paypal/webhook", strings.NewReader(body))
	req.Header.Set("Paypal-Transmission-Sig", "sig")
	req.Header.Set("Paypal-Transmission-Id", "tid")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

func captureEvent(id, kind string, orderID int64, value string) string {
	return fmt.Sprintf(`{"id":%q,"event_type":%q,"resource":{"id":"CAP1","custom_id":"%d","amount":{"currency_code":"USD","value":%q},"supplementary_data":{"related_ids":{"order_id":"PP1"}}}}`,
		id, kind, orderID, value)
}

func paymentStatus(t *testing.T, app *gocommerce.App, orderID int64) string {
	t.Helper()
	o, err := app.Order().Get(context.Background(), orderID)
	if err != nil {
		t.Fatal(err)
	}
	return o.PaymentStatus
}

func TestValueIsAnExactDecimal(t *testing.T) {
	for _, c := range []struct {
		minor    int64
		currency string
		want     string
		bad      bool
	}{
		{2500, "USD", "25.00", false},
		{5, "usd", "0.05", false},
		{1999, "EUR", "19.99", false},
		{2500, "JPY", "2500", false},
		{2500, "HUF", "25", false},
		{2550, "HUF", "", true},
		{2500, "INR", "", true},
	} {
		got, err := value(c.minor, c.currency)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("value(%d, %s) = %q, %v; want %q, bad=%v", c.minor, c.currency, got, err, c.want, c.bad)
		}
	}
}

func TestMoneyReadsPayPalAmountsBack(t *testing.T) {
	for _, c := range []struct {
		in   amount
		want int64
		bad  bool
	}{
		{amount{"USD", "25.00"}, 2500, false},
		{amount{"USD", "25.5"}, 2550, false},
		{amount{"USD", "25"}, 2500, false},
		{amount{"JPY", "2500"}, 2500, false},
		{amount{"USD", "25.001"}, 0, true},
		{amount{"USD", "abc"}, 0, true},
	} {
		got, err := money(c.in)
		if (err != nil) != c.bad || got.AmountMinor != c.want {
			t.Errorf("money(%v) = %d, %v; want %d, bad=%v", c.in, got.AmountMinor, err, c.want, c.bad)
		}
	}
}

// PayPal has no rupees, so a rupee store must not offer a method that can only
// refuse the shopper at the last step.
func TestARupeeStoreDoesNotOfferPayPal(t *testing.T) {
	app, _ := newApp(t, "INR")
	rec := gctest.Request(t, app, http.MethodGet, "/api/checkout", nil)
	if strings.Contains(rec.Body.String(), `"paypal"`) {
		t.Fatalf("a rupee store offered PayPal: %s", rec.Body)
	}
}

func TestADollarStoreOffersPayPalAndRedirects(t *testing.T) {
	app, stub := newApp(t, "USD")
	if rec := gctest.Request(t, app, http.MethodGet, "/api/checkout", nil); !strings.Contains(rec.Body.String(), `"paypal"`) {
		t.Fatalf("a dollar store did not offer PayPal: %s", rec.Body)
	}
	result := place(t, app)
	if result.Payment.Kind != gocommerce.IntentRedirect || result.Payment.ClientData["url"] != "https://paypal.test/approve/PP1" {
		t.Fatalf("intent = %+v", result.Payment)
	}
	if result.Payment.Reference != "PP1" {
		t.Fatalf("reference = %q", result.Payment.Reference)
	}
	unit := stub.created["purchase_units"].([]any)[0].(map[string]any)
	if got := unit["amount"].(map[string]any); got["value"] != "25.00" || got["currency_code"] != "USD" {
		t.Fatalf("amount sent = %v", got)
	}
	if stub.requestID == "" {
		t.Fatal("no idempotency key on the order, so a checkout retry could make a second one")
	}
}

func TestReturnPageCapturesAnApprovedOrderOnce(t *testing.T) {
	app, stub := newApp(t, "USD")
	result := place(t, app)
	path := "/x/payments-paypal/orders/PP1/capture"

	// Not approved yet: nothing is captured and the order stays unpaid.
	gctest.Request(t, app, http.MethodPost, path, nil)
	if stub.captured != 0 || paymentStatus(t, app, result.Order.ID) != gocommerce.PaymentPending {
		t.Fatalf("captured %d before approval", stub.captured)
	}

	stub.status = "APPROVED"
	stub.mod.asked.Clear() // the quiet period is covered on its own below
	rec := gctest.Request(t, app, http.MethodPost, path, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"paid"`) {
		t.Fatalf("capture answered %d %s", rec.Code, rec.Body)
	}
	if stub.captured != 1 {
		t.Fatalf("captured %d times", stub.captured)
	}
	o, _ := app.Order().Get(context.Background(), result.Order.ID)
	if o.PaymentReference != "CAP1" {
		t.Fatalf("reference = %q, want the capture so a refund can find it", o.PaymentReference)
	}

	// A paid order is answered from the store, without another capture.
	gctest.Request(t, app, http.MethodPost, path, nil)
	if stub.captured != 1 {
		t.Fatalf("captured again: %d", stub.captured)
	}
}

func TestCaptureRouteIsRateLimitedPerOrder(t *testing.T) {
	app, stub := newApp(t, "USD")
	place(t, app)
	path := "/x/payments-paypal/orders/PP1/capture"
	gctest.Request(t, app, http.MethodPost, path, nil)
	stub.status = "APPROVED"
	gctest.Request(t, app, http.MethodPost, path, nil)
	if stub.captured != 0 {
		t.Fatal("a second call inside the quiet period reached PayPal")
	}
}

func TestCaptureRouteRefusesAnUnknownOrder(t *testing.T) {
	app, _ := newApp(t, "USD")
	if rec := gctest.Request(t, app, http.MethodPost, "/x/payments-paypal/orders/NOPE/capture", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}

// A shopper who closes the tab after approving is why the webhook also captures.
func TestApprovedWebhookCaptures(t *testing.T) {
	app, stub := newApp(t, "USD")
	result := place(t, app)
	stub.status = "APPROVED"
	body := fmt.Sprintf(`{"id":"WH1","event_type":"CHECKOUT.ORDER.APPROVED","resource":{"id":"PP1","purchase_units":[{"custom_id":"%d"}]}}`, result.Order.ID)
	if rec := webhook(t, app, body); rec.Code != http.StatusOK {
		t.Fatalf("webhook answered %d %s", rec.Code, rec.Body)
	}
	if stub.captured != 1 || paymentStatus(t, app, result.Order.ID) != gocommerce.PaymentPaid {
		t.Fatalf("captured %d, status %s", stub.captured, paymentStatus(t, app, result.Order.ID))
	}
}

func TestInvalidSignatureChangesNothing(t *testing.T) {
	app, stub := newApp(t, "USD")
	result := place(t, app)
	stub.sigOK = false
	rec := webhook(t, app, captureEvent("WH2", "PAYMENT.CAPTURE.COMPLETED", result.Order.ID, "25.00"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rec.Code)
	}
	if got := paymentStatus(t, app, result.Order.ID); got != gocommerce.PaymentPending {
		t.Fatalf("a forged event moved the order to %s", got)
	}
}

func TestMissingSignatureNeverReachesPayPal(t *testing.T) {
	app, stub := newApp(t, "USD")
	req := httptest.NewRequest(http.MethodPost, "/api/checkout/paypal/webhook", strings.NewReader(`{"id":"X"}`))
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || stub.verified != nil {
		t.Fatalf("got %d, verified %q", rec.Code, stub.verified)
	}
}

// The signature covers the bytes PayPal sent, so they go back unchanged.
func TestVerificationSendsTheEventVerbatim(t *testing.T) {
	app, stub := newApp(t, "USD")
	result := place(t, app)
	event := captureEvent("WH3", "PAYMENT.CAPTURE.COMPLETED", result.Order.ID, "25.00")
	event = strings.Replace(event, `{"id"`, `{ "id"`, 1)
	webhook(t, app, event)
	if !strings.Contains(string(stub.verified), `"webhook_event":`+event+`}`) {
		t.Fatalf("event was re-encoded: %s", stub.verified)
	}
	if !strings.Contains(string(stub.verified), `"webhook_id":"`+webhookID+`"`) {
		t.Fatalf("webhook id missing: %s", stub.verified)
	}
	if !json.Valid(stub.verified) {
		t.Fatalf("not JSON: %s", stub.verified)
	}
}

func TestCompletedCaptureSettlesOnceAcrossRedelivery(t *testing.T) {
	app, _ := newApp(t, "USD")
	result := place(t, app)
	event := captureEvent("WH4", "PAYMENT.CAPTURE.COMPLETED", result.Order.ID, "25.00")
	for range 2 {
		if rec := webhook(t, app, event); rec.Code != http.StatusOK {
			t.Fatalf("got %d %s", rec.Code, rec.Body)
		}
	}
	if got := paymentStatus(t, app, result.Order.ID); got != gocommerce.PaymentPaid {
		t.Fatalf("status %s", got)
	}
}

func TestACaptureForTheWrongAmountDoesNotSettle(t *testing.T) {
	app, _ := newApp(t, "USD")
	result := place(t, app)
	if rec := webhook(t, app, captureEvent("WH5", "PAYMENT.CAPTURE.COMPLETED", result.Order.ID, "1.00")); rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	if got := paymentStatus(t, app, result.Order.ID); got != gocommerce.PaymentPending {
		t.Fatalf("₹1 settled an order for 25: %s", got)
	}
}

func TestAnEventForAnotherStoresOrderIsLeftAlone(t *testing.T) {
	app, _ := newApp(t, "USD")
	if rec := webhook(t, app, captureEvent("WH6", "PAYMENT.CAPTURE.COMPLETED", 987654, "25.00")); rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestDeniedCaptureFailsAnUnpaidOrderOnly(t *testing.T) {
	app, _ := newApp(t, "USD")
	result := place(t, app)
	webhook(t, app, captureEvent("WH7", "PAYMENT.CAPTURE.DENIED", result.Order.ID, "25.00"))
	if got := paymentStatus(t, app, result.Order.ID); got != gocommerce.PaymentFailed {
		t.Fatalf("status %s", got)
	}
	webhook(t, app, captureEvent("WH8", "PAYMENT.CAPTURE.COMPLETED", result.Order.ID, "25.00"))
	webhook(t, app, captureEvent("WH9", "PAYMENT.CAPTURE.DENIED", result.Order.ID, "25.00"))
	if got := paymentStatus(t, app, result.Order.ID); got != gocommerce.PaymentPaid {
		t.Fatalf("a late denial changed a paid order to %s", got)
	}
}

func TestRefundGoesToTheCapture(t *testing.T) {
	app, stub := newApp(t, "USD")
	result := place(t, app)
	webhook(t, app, captureEvent("WH10", "PAYMENT.CAPTURE.COMPLETED", result.Order.ID, "25.00"))
	rec := gctest.AdminRequest(t, app, http.MethodPost, fmt.Sprintf("/api/admin/orders/%d/refund", result.Order.ID),
		map[string]any{"amount_minor": 1000, "reason": "one returned"})
	if rec.Code != http.StatusOK {
		t.Fatalf("refund answered %d %s", rec.Code, rec.Body)
	}
	if len(stub.refunds) != 1 || stub.refunds[0] != "10.00" {
		t.Fatalf("refunds = %v", stub.refunds)
	}
}

func TestCheckPaymentRecordsACaptureNoWebhookBrought(t *testing.T) {
	app, stub := newApp(t, "USD")
	result := place(t, app)
	stub.status = "COMPLETED"
	check, err := app.Pay().CheckPayment(context.Background(), result.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !check.Recorded || check.Order.PaymentReference != "CAP1" {
		t.Fatalf("check = %+v", check)
	}
}

func TestCheckPaymentRefusesAPayPalOrderMadeForAnotherOrder(t *testing.T) {
	app, stub := newApp(t, "USD")
	result := place(t, app)
	stub.status, stub.customID = "COMPLETED", "999999"
	check, err := app.Pay().CheckPayment(context.Background(), result.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if check.Recorded || check.Gateway.Paid {
		t.Fatalf("settled on another order's payment: %+v", check)
	}
}

// A reference can be typed by hand, so a refund first checks that the capture
// really is this order's.
func TestRefundRefusesAnotherOrdersCapture(t *testing.T) {
	app, stub := newApp(t, "USD")
	result := place(t, app)
	webhook(t, app, captureEvent("WH11", "PAYMENT.CAPTURE.COMPLETED", result.Order.ID, "25.00"))
	stub.customID = "999999"
	rec := gctest.AdminRequest(t, app, http.MethodPost, fmt.Sprintf("/api/admin/orders/%d/refund", result.Order.ID),
		map[string]any{"amount_minor": 1000, "reason": "typed reference"})
	if rec.Code == http.StatusOK || len(stub.refunds) != 0 {
		t.Fatalf("refunded another order's capture: %d %s, refunds %v", rec.Code, rec.Body, stub.refunds)
	}
}
