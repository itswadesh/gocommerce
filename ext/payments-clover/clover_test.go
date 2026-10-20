package clover

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

const (
	testSecret   = "clover-signing-secret"
	testMerchant = "MERCHANT1"
	testToken    = "clover-private-token"
	testReturn   = "https://shop.example/thanks"
)

// TestVerifySignature is pure logic and needs no database: it is the one piece
// of this module standing between the internet and marking orders paid.
func TestVerifySignature(t *testing.T) {
	t.Parallel()

	body := []byte(`{"Type":"PAYMENT","Status":"APPROVED","Id":"PAY1","Data":"S1"}`)
	now := time.Unix(1_700_000_000, 0)
	good := signHeader(now.Unix(), body, testSecret)

	for _, tc := range []struct {
		name    string
		header  string
		body    []byte
		at      time.Time
		wantErr bool
	}{
		{"a good signature passes", good, body, now, false},
		{"the parts in either order pass", "v1=" + hexSig(now.Unix(), body, testSecret) + ",t=" + strconv.FormatInt(now.Unix(), 10), body, now, false},
		{"upper-case hex passes", "t=" + strconv.FormatInt(now.Unix(), 10) + ",v1=" + strings.ToUpper(hexSig(now.Unix(), body, testSecret)), body, now, false},
		{
			// More than one v1 while a secret is being changed: any match is a match.
			"one good signature among several passes",
			"t=" + strconv.FormatInt(now.Unix(), 10) + ",v1=00ff,v1=" + hexSig(now.Unix(), body, testSecret), body, now, false,
		},
		{"no header is refused", "", body, now, true},
		{"no timestamp is refused", "v1=" + hexSig(now.Unix(), body, testSecret), body, now, true},
		{"no signature is refused", "t=" + strconv.FormatInt(now.Unix(), 10), body, now, true},
		{"a signature made with another secret is refused", signHeader(now.Unix(), body, "another secret"), body, now, true},
		{"a tampered body is refused", good, []byte(`{"Type":"PAYMENT","Status":"APPROVED","Id":"PAY2","Data":"S1"}`), now, true},
		{
			// The timestamp is signed, so an attacker cannot move it, which is
			// what makes the staleness bound worth having.
			"a stale delivery is refused", good, body, now.Add(2 * signatureTolerance), true,
		},
		{"a delivery from the future is refused", good, body, now.Add(-2 * signatureTolerance), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifySignature(tc.body, tc.header, []byte(testSecret), tc.at)
			if tc.wantErr && err == nil {
				t.Error("expected an error, got none")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// The sandbox switch picks Clover's sandbox host, and a base URL typed in wins
// over both.
func TestBaseURL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		cfg  Config
		want string
	}{
		{Config{}, "https://api.clover.com"},
		{Config{Sandbox: true}, "https://apisandbox.dev.clover.com"},
		{Config{Sandbox: true, BaseURL: "http://127.0.0.1:9/"}, "http://127.0.0.1:9"},
	} {
		c := tc.cfg
		(&Module{}).finish(&c)
		if c.BaseURL != tc.want {
			t.Errorf("%+v: base URL = %q, want %q", tc.cfg, c.BaseURL, tc.want)
		}
	}
}

// cloverStub stands in for Clover's API and remembers what it was sent.
type cloverStub struct {
	mu       sync.Mutex
	sessions int
	checkout map[string]any
	headers  http.Header
	refund   map[string]any
	answer   string // what POST /v1/refunds answers
}

func (s *cloverStub) lastCheckout() (map[string]any, http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkout, s.headers
}

func (s *cloverStub) made() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions
}

func (s *cloverStub) answerRefund(body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answer = body
}

func (s *cloverStub) lastRefund() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refund
}

func newApp(t *testing.T, change ...func(*Config)) (*gocommerce.App, *cloverStub) {
	t.Helper()
	stub := &cloverStub{answer: `{"id":"REF1","amount":2500,"charge":"PAY1","currency":"usd","status":"succeeded"}`}
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		// Clover refuses a call without its token and merchant; so does this
		// stub, because a module that forgot either would otherwise pass.
		if r.Header.Get("Authorization") != "Bearer "+testToken || r.Header.Get("X-Clover-Merchant-Id") != testMerchant {
			http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		stub.mu.Lock()
		defer stub.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/invoicingcheckoutservice/v1/checkouts":
			stub.sessions++
			stub.checkout, stub.headers = in, r.Header.Clone()
			id := fmt.Sprintf("SESSION%d", stub.sessions)
			fmt.Fprintf(w, `{"href":"https://sandbox.dev.clover.com/checkout/%s?mode=checkout","checkoutSessionId":"%s","expirationTime":%d}`,
				id, id, time.Now().Add(15*time.Minute).UnixMilli())
		case r.Method == http.MethodPost && r.URL.Path == "/v1/refunds":
			stub.refund = in
			fmt.Fprint(w, stub.answer)
		default:
			http.NotFound(w, r)
		}
	})
	cfg := Config{
		MerchantID:    testMerchant,
		PrivateToken:  testToken,
		WebhookSecret: testSecret,
		ReturnURL:     testReturn,
		BaseURL:       server.URL,
	}
	for _, c := range change {
		c(&cfg)
	}
	return gctest.New(t, New(cfg)), stub
}

// The module's reason to exist, end to end: a Hosted Checkout session for the
// order's total, which settles when Clover's signed webhook says it was paid.
func TestCheckoutAndWebhook(t *testing.T) {
	app, stub := newApp(t)
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, "clover")
	if result.Payment.Kind != gocommerce.IntentRedirect {
		t.Fatalf("payment kind = %q, want redirect to Clover's page", result.Payment.Kind)
	}
	if got := result.Payment.ClientData["url"]; got != "https://sandbox.dev.clover.com/checkout/SESSION1?mode=checkout" {
		t.Errorf("redirect url = %q", got)
	}
	if result.Payment.Reference != "SESSION1" {
		t.Errorf("reference = %q, want the checkout session", result.Payment.Reference)
	}

	sent, headers := stub.lastCheckout()
	if ua := headers.Get("User-Agent"); !strings.Contains(ua, "gocommerce") {
		t.Errorf("User-Agent = %q; Clover asks every call to name its application", ua)
	}
	cart, _ := sent["shoppingCart"].(map[string]any)
	lines, _ := cart["lineItems"].([]any)
	if len(lines) != 1 {
		t.Fatalf("line items = %v, want the whole order as one line", cart["lineItems"])
	}
	line, _ := lines[0].(map[string]any)
	// Cents, as the engine holds them: 2500 is $25.00, not $2,500.
	if line["price"] != float64(result.Order.Total.AmountMinor) || line["unitQty"] != float64(1) {
		t.Errorf("line = %v, want one unit at %d", line, result.Order.Total.AmountMinor)
	}
	if !strings.Contains(fmt.Sprint(line["name"]), result.Order.Number) {
		t.Errorf("line name = %v, want the order number in it", line["name"])
	}
	if tips, _ := sent["tips"].(map[string]any); tips["enabled"] != false {
		t.Errorf("tips = %v, want them off: a tip would make the payment differ from the order", sent["tips"])
	}
	redirects, _ := sent["redirectUrls"].(map[string]any)
	for _, k := range []string{"success", "failure", "cancel"} {
		if redirects[k] != testReturn {
			t.Errorf("redirectUrls.%s = %v, want %s", k, redirects[k], testReturn)
		}
	}
	if customer, _ := sent["customer"].(map[string]any); customer["email"] != "gctest@example.com" {
		t.Errorf("customer = %v, want the shopper's email", sent["customer"])
	}

	// Clover's own casing, as its documentation writes the fields.
	body := `{"Type":"PAYMENT","Status":"APPROVED","Message":"Approved for 2500","Id":"PAY1","MerchantId":"MERCHANT1","Data":"SESSION1"}`
	if rec := postWebhook(t, app, body); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}
	order, err := app.Order().Get(ctx, result.Order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if order.PaymentStatus != gocommerce.PaymentPaid || order.Status != gocommerce.OrderConfirmed {
		t.Errorf("payment %q, order %q; want paid and confirmed", order.PaymentStatus, order.Status)
	}
	// The payment id, which is what a refund is issued against, not the session.
	if order.PaymentReference != "PAY1" {
		t.Errorf("payment reference = %q, want the payment id", order.PaymentReference)
	}
}

// Lower-case keys settle too: Clover documents the fields without saying how
// they are written.
func TestWebhookKeysInAnyCase(t *testing.T) {
	app, _ := newApp(t)
	result := gctest.PlaceOrder(t, app, "clover")
	body := `{"type":"PAYMENT","status":"APPROVED","id":"PAY9","merchantId":"MERCHANT1","data":"SESSION1"}`
	if rec := postWebhook(t, app, body); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPaid {
		t.Errorf("payment status = %q, want paid", order.PaymentStatus)
	}
}

// A declined card is recorded, and the order stays payable: Clover's page lets
// the shopper try another card, and that one can still settle it.
func TestDeclineThenApproval(t *testing.T) {
	app, _ := newApp(t)
	result := gctest.PlaceOrder(t, app, "clover")

	if rec := postWebhook(t, app, `{"Type":"PAYMENT","Status":"DECLINED","Id":"PAYD","MerchantId":"MERCHANT1","Data":"SESSION1"}`); rec.Code != http.StatusOK {
		t.Fatalf("decline webhook = %d: %s", rec.Code, rec.Body)
	}
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentFailed || order.Status != gocommerce.OrderPending {
		t.Errorf("after a decline: payment %q, order %q; want failed and still pending", order.PaymentStatus, order.Status)
	}
	if rec := postWebhook(t, app, `{"Type":"PAYMENT","Status":"APPROVED","Id":"PAYA","MerchantId":"MERCHANT1","Data":"SESSION1"}`); rec.Code != http.StatusOK {
		t.Fatalf("approval webhook = %d: %s", rec.Code, rec.Body)
	}
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPaid || order.PaymentReference != "PAYA" {
		t.Errorf("after the approval: payment %q, reference %q; want paid by PAYA", order.PaymentStatus, order.PaymentReference)
	}
}

func TestWebhookReplayIsIdempotent(t *testing.T) {
	app, _ := newApp(t)
	gctest.PlaceOrder(t, app, "clover")
	body := `{"Type":"PAYMENT","Status":"APPROVED","Id":"PAY4","MerchantId":"MERCHANT1","Data":"SESSION1"}`
	for i := range 2 {
		if rec := postWebhook(t, app, body); rec.Code != http.StatusOK {
			t.Fatalf("delivery %d = %d: %s", i+1, rec.Code, rec.Body)
		}
	}
	var claims int
	if err := app.DB().QueryRow(`SELECT count(*) FROM payments_clover_events WHERE event_id = 'PAYMENT:PAY4:APPROVED'`).Scan(&claims); err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if claims != 1 {
		t.Errorf("event rows = %d, want exactly 1", claims)
	}
}

// Clover redelivering an old decline after the order was paid is answered from
// the claim, not acted on again: marking a paid order failed is refused, and
// a refusal would be answered 500 and retried for ever.
func TestLateRedeliveryOfADecline(t *testing.T) {
	app, _ := newApp(t)
	result := gctest.PlaceOrder(t, app, "clover")
	declined := `{"Type":"PAYMENT","Status":"DECLINED","Id":"PAYD","MerchantId":"MERCHANT1","Data":"SESSION1"}`
	for _, body := range []string{
		declined,
		`{"Type":"PAYMENT","Status":"APPROVED","Id":"PAYA","MerchantId":"MERCHANT1","Data":"SESSION1"}`,
		declined,
	} {
		if rec := postWebhook(t, app, body); rec.Code != http.StatusOK {
			t.Fatalf("webhook %s = %d: %s", body, rec.Code, rec.Body)
		}
	}
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPaid {
		t.Errorf("payment status = %q, want still paid", order.PaymentStatus)
	}
}

func TestWebhookRejectsBadSignature(t *testing.T) {
	app, _ := newApp(t)
	result := gctest.PlaceOrder(t, app, "clover")
	body := `{"Type":"PAYMENT","Status":"APPROVED","Id":"PAY5","MerchantId":"MERCHANT1","Data":"SESSION1"}`
	now := time.Now().Unix()
	for name, header := range map[string]string{
		"no":          "",
		"a junk":      "t=1,v1=nonsense",
		"a forged":    signHeader(now, []byte(body), "not the secret"),
		"a stale":     signHeader(now-int64(2*signatureTolerance/time.Second), []byte(body), testSecret),
		"an unsigned": "t=" + strconv.FormatInt(now, 10),
	} {
		rec := deliver(app, body, header)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status for %s signature = %d, want 400", name, rec.Code)
		}
	}
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPending {
		t.Errorf("payment status = %q: a forged webhook changed the order", order.PaymentStatus)
	}
}

// A payment taken some other way (a session this store did not make, or a
// payment for another merchant) is acknowledged and left alone, rather than
// retried for ever.
func TestWebhookForSomethingElseIsLeftAlone(t *testing.T) {
	app, _ := newApp(t)
	result := gctest.PlaceOrder(t, app, "clover")
	for name, body := range map[string]string{
		"an unknown session": `{"Type":"PAYMENT","Status":"APPROVED","Id":"PAY6","MerchantId":"MERCHANT1","Data":"NOT-OURS"}`,
		"another merchant":   `{"Type":"PAYMENT","Status":"APPROVED","Id":"PAY7","MerchantId":"SOMEONE-ELSE","Data":"SESSION1"}`,
		"another event type": `{"Type":"REFUND","Status":"APPROVED","Id":"PAY8","MerchantId":"MERCHANT1","Data":"SESSION1"}`,
	} {
		if rec := postWebhook(t, app, body); rec.Code != http.StatusOK {
			t.Errorf("%s: webhook = %d, want 200 so Clover stops sending it", name, rec.Code)
		}
	}
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPending {
		t.Errorf("payment status = %q, want the order untouched", order.PaymentStatus)
	}
}

// The webhook carries no amount, so the module keeps what each session asked
// for. A session whose amount is not the order's settles nothing.
func TestSessionForAnotherAmountDoesNotSettle(t *testing.T) {
	app, _ := newApp(t)
	result := gctest.PlaceOrder(t, app, "clover")
	if _, err := app.DB().Exec(`UPDATE payments_clover_sessions SET amount_minor = 100 WHERE session_id = 'SESSION1'`); err != nil {
		t.Fatalf("change the session: %v", err)
	}
	if rec := postWebhook(t, app, `{"Type":"PAYMENT","Status":"APPROVED","Id":"PAY10","MerchantId":"MERCHANT1","Data":"SESSION1"}`); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus == gocommerce.PaymentPaid {
		t.Error("a payment for $1.00 settled a $25.00 order")
	}
}

// Hosted Checkout takes no currency: it charges in the merchant account's own.
// An order in any other currency is refused before a session is made, rather
// than charged the same number of the account's cents.
func TestOrderInAnotherCurrencyIsRefused(t *testing.T) {
	app, stub := newApp(t, func(c *Config) { c.Currency = "CAD" })
	if _, err := app.Order().Checkout(context.Background(), "clover", checkoutInput(t, app), ""); err == nil || !strings.Contains(err.Error(), "CAD") {
		t.Errorf("checkout of a USD order on a CAD account = %v, want a refusal naming CAD", err)
	}
	if n := stub.made(); n != 0 {
		t.Errorf("sessions made = %d, want none", n)
	}
}

// Clover sends the shopper back only to an https address.
func TestReturnURLMustBeHTTPS(t *testing.T) {
	for name, url := range map[string]string{"none": "", "plain http": "http://shop.example/thanks"} {
		app, stub := newApp(t, func(c *Config) { c.ReturnURL = url })
		if _, err := app.Order().Checkout(context.Background(), "clover", checkoutInput(t, app), ""); err == nil || !strings.Contains(err.Error(), "https") {
			t.Errorf("%s: checkout = %v, want a refusal asking for an https return URL", name, err)
		}
		if n := stub.made(); n != 0 {
			t.Errorf("%s: sessions made = %d, want none", name, n)
		}
	}
}

func TestRefund(t *testing.T) {
	app, stub := newApp(t)
	ctx := context.Background()
	result := gctest.PlaceOrder(t, app, "clover")
	if rec := postWebhook(t, app, `{"Type":"PAYMENT","Status":"APPROVED","Id":"PAY1","MerchantId":"MERCHANT1","Data":"SESSION1"}`); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}

	// Part first: the order is one line with no tax or tip, which is what
	// Clover needs before it will refund part of a payment.
	stub.answerRefund(`{"id":"REF1","amount":1000,"charge":"PAY1","currency":"usd","status":"succeeded"}`)
	order, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{AmountMinor: 1000}, nil)
	if err != nil {
		t.Fatalf("partial refund: %v", err)
	}
	if got := stub.lastRefund(); got["charge"] != "PAY1" || got["amount"] != float64(1000) {
		t.Errorf("refund sent %v, want charge PAY1 for 1000", got)
	}
	if order.PaymentStatus != gocommerce.PaymentPaid || len(order.Refunds) != 1 || order.Refunds[0].ProviderReference != "REF1" {
		t.Errorf("after a partial refund: payment %q, refunds %+v", order.PaymentStatus, order.Refunds)
	}

	stub.answerRefund(`{"id":"REF2","amount":1500,"charge":"PAY1","currency":"usd","status":"succeeded"}`)
	order, err = app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{}, nil)
	if err != nil {
		t.Fatalf("refund the rest: %v", err)
	}
	if got := stub.lastRefund(); got["amount"] != float64(1500) {
		t.Errorf("second refund sent %v, want the 1500 left", got)
	}
	if order.PaymentStatus != gocommerce.PaymentRefunded {
		t.Errorf("payment status = %q, want refunded", order.PaymentStatus)
	}
}

// Clover answering that a refund failed is a failed refund, not a success.
func TestRefundDeclinedByClover(t *testing.T) {
	app, stub := newApp(t)
	result := gctest.PlaceOrder(t, app, "clover")
	if rec := postWebhook(t, app, `{"Type":"PAYMENT","Status":"APPROVED","Id":"PAY1","MerchantId":"MERCHANT1","Data":"SESSION1"}`); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}
	stub.answerRefund(`{"id":"REF3","amount":2500,"charge":"PAY1","status":"failed","failure_reason":"insufficient_funds"}`)
	if _, err := app.Pay().Refund(context.Background(), result.Order.ID, gocommerce.RefundRequest{}, nil); err == nil || !strings.Contains(err.Error(), "insufficient_funds") {
		t.Errorf("refund = %v, want the failure Clover gave", err)
	}
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPaid {
		t.Errorf("payment status = %q, want still paid", order.PaymentStatus)
	}
}

// An order marked paid by hand still carries the checkout session as its
// reference. There is no Clover payment to refund against, and the module says
// so rather than sending the session to /v1/refunds.
func TestRefundNeedsAPayment(t *testing.T) {
	app, stub := newApp(t)
	ctx := context.Background()
	result := gctest.PlaceOrder(t, app, "clover")
	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "SESSION1"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	if _, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{}, nil); err == nil {
		t.Error("refunding against a checkout session succeeded")
	}
	if stub.lastRefund() != nil {
		t.Errorf("a refund was sent: %v", stub.lastRefund())
	}
}

// TestInstalledIdleUntilConfigured: with nothing in Config the module still
// registers, so the panel lists it, but it is not offered at checkout until
// its fields are typed into the plugin.
func TestInstalledIdleUntilConfigured(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ctx := context.Background()

	listed := false
	for _, p := range app.Settings().PaymentMethods {
		if p.Code == "clover" {
			listed = true
			if p.Configured {
				t.Error("configured before anything was typed in")
			}
		}
	}
	if !listed {
		t.Fatal("an idle module should still be listed in the settings")
	}
	for _, code := range app.Pay().Methods() {
		if code == "clover" {
			t.Fatal("an idle gateway must not be offered at checkout")
		}
	}

	on := true
	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{
		"merchant_id": testMerchant, "private_token": testToken, "webhook_secret": testSecret,
	}}); err != nil {
		t.Fatalf("configure from the panel: %v", err)
	}
	offered := false
	for _, code := range app.Pay().Methods() {
		offered = offered || code == "clover"
	}
	if !offered {
		t.Fatal("configured from the panel, the gateway should be offered")
	}
}

// ------------------------------------------------------------------ helpers

func hexSig(ts int64, body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10) + "."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func signHeader(ts int64, body []byte, secret string) string {
	return "t=" + strconv.FormatInt(ts, 10) + ",v1=" + hexSig(ts, body, secret)
}

func deliver(app *gocommerce.App, body, header string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/checkout/clover/webhook", strings.NewReader(body))
	if header != "" {
		req.Header.Set("Clover-Signature", header)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

func postWebhook(t *testing.T, app *gocommerce.App, body string) *httptest.ResponseRecorder {
	t.Helper()
	return deliver(app, body, signHeader(time.Now().Unix(), []byte(body), testSecret))
}

func getOrder(t *testing.T, app *gocommerce.App, id int64) *gocommerce.Order {
	t.Helper()
	order, err := app.Order().Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	return order
}

// checkoutInput is a cart ready to check out, for the tests that expect the
// payment step to refuse and so cannot use gctest.PlaceOrder.
func checkoutInput(t *testing.T, app *gocommerce.App) gocommerce.CheckoutInput {
	t.Helper()
	ctx := context.Background()
	product := gctest.CreateProduct(t, app, "GCTEST-clover", 2500, 10)
	cart, err := app.Cart().Create(ctx, "")
	if err != nil {
		t.Fatalf("create cart: %v", err)
	}
	if _, err := app.Cart().AddLine(ctx, cart.Token, product.DefaultVariant().ID, 1); err != nil {
		t.Fatalf("add to cart: %v", err)
	}
	return gocommerce.CheckoutInput{
		CartID: cart.Token, Email: "gctest@example.com", Name: "GC Test",
		Address: gocommerce.Address{Line1: "1 Test Street", City: "Testville", State: "CA", PostalCode: "94117", Country: "US", Phone: "+15555550123"},
	}
}
