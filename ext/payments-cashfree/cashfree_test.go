package cashfree

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

const (
	testAppID  = "TEST1234567890"
	testSecret = "cfsk_ma_test_secret"
)

// TestVerifySignature is pure logic and needs no database: it is the one piece
// of this module standing between the internet and marking orders paid.
func TestVerifySignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"data":{"order":{"order_id":"GC-1001_ab12cd34"}},"type":"PAYMENT_SUCCESS_WEBHOOK"}`)
	now := time.UnixMilli(1_700_000_000_123)
	ms := strconv.FormatInt(now.UnixMilli(), 10)

	for _, tc := range []struct {
		name      string
		signature string
		timestamp string
		body      []byte
		at        time.Time
		wantErr   bool
	}{
		{"a good signature passes", sign(ms, body, testSecret), ms, body, now, false},
		{
			// Cashfree's samples stamp in milliseconds; a stamp in seconds is
			// read as seconds rather than as 1970.
			"a stamp in seconds passes", sign("1700000000", body, testSecret), "1700000000", body, now, false,
		},
		{"no signature is refused", "", ms, body, now, true},
		{"no timestamp is refused", sign(ms, body, testSecret), "", body, now, true},
		{"a signature made with another secret is refused", sign(ms, body, "another secret"), ms, body, now, true},
		{"a tampered body is refused", sign(ms, body, testSecret), ms, []byte(`{"type":"PAYMENT_SUCCESS_WEBHOOK"}`), now, true},
		{"a signature over another timestamp is refused", sign(ms, body, testSecret), strconv.FormatInt(now.UnixMilli()+1, 10), body, now, true},
		{"hex where base64 belongs is refused", hexSign(ms, body, testSecret), ms, body, now, true},
		{
			// The timestamp is inside the signed string, so it cannot be moved,
			// which is what makes the staleness bound worth having.
			"a stale delivery is refused", sign(ms, body, testSecret), ms, body, now.Add(2 * signatureTolerance), true,
		},
		{"a delivery from the future is refused", sign(ms, body, testSecret), ms, body, now.Add(-2 * signatureTolerance), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifySignature(tc.body, tc.signature, tc.timestamp, []byte(testSecret), tc.at)
			if tc.wantErr && err == nil {
				t.Error("expected an error, got none")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// The environment picks Cashfree's host, fixed in code, and a base URL typed
// in wins over both.
func TestBaseURL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		cfg  Config
		want string
	}{
		{Config{}, "https://api.cashfree.com/pg"},
		{Config{Environment: "production"}, "https://api.cashfree.com/pg"},
		{Config{Environment: "Sandbox"}, "https://sandbox.cashfree.com/pg"},
		{Config{Environment: "sandbox", BaseURL: "http://127.0.0.1:9/pg/"}, "http://127.0.0.1:9/pg"},
	} {
		c := tc.cfg
		(&Module{}).finish(&c)
		if c.BaseURL != tc.want {
			t.Errorf("%+v: base URL = %q, want %q", tc.cfg, c.BaseURL, tc.want)
		}
	}
}

// Cashfree speaks rupees with two decimals; the engine speaks paise. The
// conversion is string work on integers, never a float (rule 6).
func TestRupees(t *testing.T) {
	t.Parallel()
	for minor, want := range map[int64]string{2500: "25.00", 5: "0.05", 100: "1.00", 123456: "1234.56", 0: "0.00"} {
		if got := rupees(minor); string(got) != want {
			t.Errorf("rupees(%d) = %s, want %s", minor, got, want)
		}
	}
	for in, want := range map[string]int64{"25": 2500, "25.5": 2550, "25.50": 2550, "0.05": 5, "1924": 192400, "10.01": 1001, "25.500": 2550} {
		got, err := paise(json.Number(in))
		if err != nil || got != want {
			t.Errorf("paise(%s) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "25.555", "-1", "1e3", "abc", "1.2.3"} {
		if got, err := paise(json.Number(in)); err == nil {
			t.Errorf("paise(%q) = %d, want an error", in, got)
		}
	}
}

// ---------------------------------------------------------------- the stub

// cashfreeStub stands in for Cashfree's PG API, in the shapes its reference
// documents, and remembers what it was sent.
type cashfreeStub struct {
	mu       sync.Mutex
	created  []map[string]any
	headers  http.Header
	payments map[string]string // cf order id → the JSON array GET .../payments answers
	refunds  []map[string]any
	refundTo []string
	answer   string // the refund_status POST .../refunds answers with
	calls    atomic.Int32
}

func (s *cashfreeStub) lastOrder() (map[string]any, http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.created) == 0 {
		return nil, nil
	}
	return s.created[len(s.created)-1], s.headers
}

func (s *cashfreeStub) setPayments(cfOrderID, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payments[cfOrderID] = body
}

func (s *cashfreeStub) answerRefund(status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answer = status
}

func (s *cashfreeStub) lastRefund() (map[string]any, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.refunds) == 0 {
		return nil, ""
	}
	return s.refunds[len(s.refunds)-1], s.refundTo[len(s.refundTo)-1]
}

func newStub(t *testing.T) (*cashfreeStub, *httptest.Server) {
	t.Helper()
	stub := &cashfreeStub{payments: map[string]string{}, answer: "SUCCESS"}
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		stub.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// Cashfree refuses a call without its credentials and a version; so
		// does this stub, because a module that forgot one would otherwise pass.
		if r.Header.Get("x-client-id") != testAppID || r.Header.Get("x-client-secret") != testSecret {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"authentication Failed","code":"request_failed","type":"authentication_error"}`)
			return
		}
		if r.Header.Get("x-api-version") == "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"message":"x-api-version : is missing in the request","code":"request_failed","type":"invalid_request_error"}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var in map[string]any
		dec := json.NewDecoder(strings.NewReader(string(body)))
		dec.UseNumber()
		_ = dec.Decode(&in)

		stub.mu.Lock()
		defer stub.mu.Unlock()
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/orders":
			stub.created = append(stub.created, in)
			stub.headers = r.Header.Clone()
			fmt.Fprintf(w, `{"cf_order_id":"2149460581","created_at":"2026-10-22T10:00:00+05:30","customer_details":{"customer_id":%q,"customer_phone":"9999999999"},"entity":"order","order_amount":%s,"order_currency":"INR","order_id":%q,"order_status":"ACTIVE","payment_session_id":"session_test_%d","order_tags":null}`,
				in["customer_details"].(map[string]any)["customer_id"], in["order_amount"], in["order_id"], len(stub.created))
		case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "orders" && parts[2] == "payments":
			list, ok := stub.payments[parts[1]]
			if !ok {
				list = "[]"
			}
			fmt.Fprint(w, list)
		case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "orders" && parts[2] == "refunds":
			stub.refunds = append(stub.refunds, in)
			stub.refundTo = append(stub.refundTo, parts[1])
			fmt.Fprintf(w, `{"cf_payment_id":"5114917126974","cf_refund_id":"1553338","refund_id":%q,"order_id":%q,"entity":"refund","refund_amount":%s,"refund_currency":"INR","refund_status":%q,"refund_type":"MERCHANT_INITIATED","status_description":"In Progress"}`,
				in["refund_id"], parts[1], in["refund_amount"], stub.answer)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"not found","code":"request_failed"}`)
		}
	})
	return stub, server
}

func newApp(t *testing.T, change ...func(*Config, *gocommerce.Config)) (*gocommerce.App, *cashfreeStub) {
	t.Helper()
	app, stub, _ := newAppModule(t, change...)
	return app, stub
}

func newAppModule(t *testing.T, change ...func(*Config, *gocommerce.Config)) (*gocommerce.App, *cashfreeStub, *Module) {
	t.Helper()
	stub, server := newStub(t)
	cfg := Config{AppID: testAppID, SecretKey: testSecret, Environment: "sandbox", BaseURL: server.URL}
	engine := gocommerce.Config{AllowPrivateURLs: true, Currency: "INR"}
	for _, c := range change {
		c(&cfg, &engine)
	}
	mod := New(cfg)
	return gctest.NewWithConfig(t, engine, mod), stub, mod
}

// ---------------------------------------------------------------- checkout

// The module's reason to exist, end to end: a Cashfree order for the order's
// total, handed to the storefront's drop-in, settled by the signed webhook.
func TestCheckoutAndWebhook(t *testing.T) {
	app, stub := newApp(t)

	result := gctest.PlaceOrder(t, app, "cashfree")
	if result.Payment.Kind != gocommerce.IntentClientAction {
		t.Fatalf("payment kind = %q, want client_action for the drop-in", result.Payment.Kind)
	}
	cfOrder := result.Payment.Reference
	if !strings.HasPrefix(cfOrder, result.Order.Number+"_") {
		t.Errorf("Cashfree order id = %q, want the order number and a suffix", cfOrder)
	}
	data := result.Payment.ClientData
	if data["payment_session_id"] != "session_test_1" || data["order_id"] != cfOrder || data["environment"] != "sandbox" {
		t.Errorf("client data = %v, want the session, the Cashfree order and the environment", data)
	}

	sent, headers := stub.lastOrder()
	if headers.Get("x-api-version") != apiVersion {
		t.Errorf("x-api-version = %q, want %q", headers.Get("x-api-version"), apiVersion)
	}
	// Rupees, as Cashfree takes them, written from paise without a float:
	// 2500 paise is ₹25.00, not ₹2,500.
	if fmt.Sprint(sent["order_amount"]) != "25.00" || sent["order_currency"] != "INR" || sent["order_id"] != cfOrder {
		t.Errorf("order sent = %v, want 25.00 INR as %s", sent, cfOrder)
	}
	customer, _ := sent["customer_details"].(map[string]any)
	if customer["customer_email"] != "gctest@example.com" || customer["customer_phone"] != "+15555550123" {
		t.Errorf("customer = %v, want the shopper's email and phone", customer)
	}
	if id, _ := customer["customer_id"].(string); len(id) < 3 || strings.ContainsAny(id, "@.+ ") {
		t.Errorf("customer_id = %q, want 3 or more alphanumerics", id)
	}
	tags, _ := sent["order_tags"].(map[string]any)
	if tags["order_number"] != result.Order.Number {
		t.Errorf("order tags = %v, want the order number for the Cashfree dashboard", tags)
	}

	if rec := postWebhook(t, app, success(cfOrder, "5114917126974", "25.00")); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}
	order := getOrder(t, app, result.Order.ID)
	if order.PaymentStatus != gocommerce.PaymentPaid || order.Status != gocommerce.OrderConfirmed {
		t.Errorf("payment %q, order %q; want paid and confirmed", order.PaymentStatus, order.Status)
	}
	if order.PaymentReference != "5114917126974" {
		t.Errorf("payment reference = %q, want Cashfree's payment id", order.PaymentReference)
	}
}

// A return URL goes to Cashfree with its placeholder for the order, so the
// storefront's page knows which Cashfree order to verify.
func TestReturnURLCarriesTheCashfreeOrder(t *testing.T) {
	app, stub := newApp(t)
	in := checkoutInput(t, app, "GCTEST-return", "+919876543210")
	in.ReturnURL = "https://shop.example/thanks?step=done"
	if _, err := app.Order().Checkout(context.Background(), "cashfree", in, ""); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	sent, _ := stub.lastOrder()
	meta, _ := sent["order_meta"].(map[string]any)
	if meta["return_url"] != "https://shop.example/thanks?step=done&cf_order_id={order_id}" {
		t.Errorf("return_url = %v", meta["return_url"])
	}
	// An Indian number goes as Cashfree's ten digits.
	if customer, _ := sent["customer_details"].(map[string]any); customer["customer_phone"] != "9876543210" {
		t.Errorf("customer_phone = %v, want 9876543210", customer["customer_phone"])
	}
}

// Cashfree takes rupees. An order in any other currency is refused before a
// Cashfree order is made, rather than charged the same number of paise.
func TestOrderInAnotherCurrencyIsRefused(t *testing.T) {
	app, stub := newApp(t, func(_ *Config, e *gocommerce.Config) { e.Currency = "USD" })
	_, err := app.Order().Checkout(context.Background(), "cashfree", checkoutInput(t, app, "GCTEST-usd", "+15555550123"), "")
	if err == nil || !strings.Contains(err.Error(), "INR") {
		t.Errorf("checkout of a USD order = %v, want a refusal naming INR", err)
	}
	if n, _ := stub.lastOrder(); n != nil {
		t.Errorf("a Cashfree order was made: %v", n)
	}
}

// Cashfree will not open an order without the shopper's phone.
func TestPhoneIsRequired(t *testing.T) {
	app, stub := newApp(t)
	_, err := app.Order().Checkout(context.Background(), "cashfree", checkoutInput(t, app, "GCTEST-nophone", ""), "")
	if err == nil || !strings.Contains(err.Error(), "phone") {
		t.Errorf("checkout without a phone = %v, want a refusal asking for one", err)
	}
	if n, _ := stub.lastOrder(); n != nil {
		t.Errorf("a Cashfree order was made: %v", n)
	}
}

// ----------------------------------------------------------------- webhook

func TestWebhookReplayIsIdempotent(t *testing.T) {
	app, _ := newApp(t)
	result := gctest.PlaceOrder(t, app, "cashfree")
	body := success(result.Payment.Reference, "777", "25.00")
	for i := range 3 {
		if rec := postWebhook(t, app, body); rec.Code != http.StatusOK {
			t.Fatalf("delivery %d = %d: %s", i+1, rec.Code, rec.Body)
		}
	}
	var claims int
	if err := app.DB().QueryRow(`SELECT count(*) FROM payments_cashfree_events WHERE event_id = 'PAYMENT_SUCCESS_WEBHOOK:777'`).Scan(&claims); err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if claims != 1 {
		t.Errorf("event rows = %d, want exactly 1", claims)
	}
	if n := len(paidEvents(t, app, result.Order.ID)); n != 1 {
		t.Errorf("order.paid events = %d, want 1", n)
	}
}

func TestWebhookRejectsBadSignature(t *testing.T) {
	app, _ := newApp(t)
	result := gctest.PlaceOrder(t, app, "cashfree")
	body := success(result.Payment.Reference, "778", "25.00")
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	stale := strconv.FormatInt(time.Now().Add(-2*signatureTolerance).UnixMilli(), 10)
	for name, h := range map[string][2]string{
		"no":           {"", now},
		"a junk":       {"bm9uc2Vuc2U=", now},
		"a forged":     {sign(now, []byte(body), "not the secret"), now},
		"a stale":      {sign(stale, []byte(body), testSecret), stale},
		"an unstamped": {sign(now, []byte(body), testSecret), ""},
	} {
		if rec := deliver(app, body, h[0], h[1]); rec.Code != http.StatusBadRequest {
			t.Errorf("status for %s signature = %d, want 400", name, rec.Code)
		}
	}
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPending {
		t.Errorf("payment status = %q: a forged webhook changed the order", order.PaymentStatus)
	}
}

// A failed attempt is recorded and the order stays payable; a later success
// settles it; the failure redelivered after that changes nothing and is not
// answered 500 for Cashfree to retry for ever.
func TestFailureThenSuccessThenLateFailure(t *testing.T) {
	app, _ := newApp(t)
	result := gctest.PlaceOrder(t, app, "cashfree")
	cf := result.Payment.Reference

	if rec := postWebhook(t, app, failed(cf, "901")); rec.Code != http.StatusOK {
		t.Fatalf("failure webhook = %d: %s", rec.Code, rec.Body)
	}
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentFailed || order.Status != gocommerce.OrderPending {
		t.Errorf("after a failure: payment %q, order %q; want failed and still pending", order.PaymentStatus, order.Status)
	}
	if rec := postWebhook(t, app, success(cf, "902", "25.00")); rec.Code != http.StatusOK {
		t.Fatalf("success webhook = %d: %s", rec.Code, rec.Body)
	}
	// Another attempt's failure, delivered late.
	if rec := postWebhook(t, app, failed(cf, "903")); rec.Code != http.StatusOK {
		t.Fatalf("late failure webhook = %d: %s", rec.Code, rec.Body)
	}
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPaid || order.PaymentReference != "902" {
		t.Errorf("payment %q by %q; want still paid by 902", order.PaymentStatus, order.PaymentReference)
	}
}

// Something that is not this store's order, or not for its amount, or not a
// payment at all, is acknowledged and left alone rather than retried for ever.
func TestWebhookForSomethingElseIsLeftAlone(t *testing.T) {
	app, _ := newApp(t)
	result := gctest.PlaceOrder(t, app, "cashfree")
	cf := result.Payment.Reference
	for name, body := range map[string]string{
		"an unknown Cashfree order": success("SOMEONE-ELSES_1", "801", "25.00"),
		"another amount":            success(cf, "802", "1.00"),
		"a user who dropped out":    strings.Replace(failed(cf, "803"), "PAYMENT_FAILED_WEBHOOK", "PAYMENT_USER_DROPPED_WEBHOOK", 1),
		"a charges report":          strings.Replace(success(cf, "804", "25.00"), "PAYMENT_SUCCESS_WEBHOOK", "PAYMENT_CHARGES_WEBHOOK", 1),
	} {
		if rec := postWebhook(t, app, body); rec.Code != http.StatusOK {
			t.Errorf("%s: webhook = %d, want 200 so Cashfree stops sending it", name, rec.Code)
		}
	}
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPending {
		t.Errorf("payment status = %q, want the order untouched", order.PaymentStatus)
	}
}

// An offer can make the payment less than the order; what Cashfree was asked
// to collect is the order's amount, and that is what is checked.
func TestOfferPaidOrderSettles(t *testing.T) {
	app, _ := newApp(t)
	result := gctest.PlaceOrder(t, app, "cashfree")
	body := strings.Replace(success(result.Payment.Reference, "805", "25.00"), `"payment_amount":25.00`, `"payment_amount":20.00`, 1)
	if rec := postWebhook(t, app, body); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPaid {
		t.Errorf("payment status = %q, want paid", order.PaymentStatus)
	}
}

// ------------------------------------------------------------------ status

// Asking Cashfree: a successful payment on the order's Cashfree order is
// found and recorded through the engine's check-payment.
func TestCheckPaymentAsksCashfree(t *testing.T) {
	app, stub := newApp(t)
	ctx := context.Background()
	result := gctest.PlaceOrder(t, app, "cashfree")
	cf := result.Payment.Reference

	stub.setPayments(cf, `[]`)
	check, err := app.Pay().CheckPayment(ctx, result.Order.ID)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if check.Recorded || check.Gateway.Paid || check.Gateway.Failed || !strings.Contains(check.Gateway.Detail, "no payment") {
		t.Errorf("before any attempt: %+v", check.Gateway)
	}

	stub.setPayments(cf, `[`+paymentJSON("611", "FAILED", "25.00", "Payment was declined")+`]`)
	check, err = app.Pay().CheckPayment(ctx, result.Order.ID)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !check.Gateway.Failed || check.Recorded || !strings.Contains(check.Gateway.Detail, "declined") ||
		check.Order.PaymentStatus != gocommerce.PaymentPending {
		t.Errorf("after a decline: %+v, order payment %q; want failed, answered, not recorded", check.Gateway, check.Order.PaymentStatus)
	}

	stub.setPayments(cf, `[`+paymentJSON("612", "SUCCESS", "25.00", "Transaction successful")+`,`+
		paymentJSON("611", "FAILED", "25.00", "Payment was declined")+`]`)
	check, err = app.Pay().CheckPayment(ctx, result.Order.ID)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !check.Recorded || check.Gateway.Reference != "612" || check.Order.PaymentStatus != gocommerce.PaymentPaid {
		t.Errorf("after a success: %+v, order payment %q; want 612 recorded", check.Gateway, check.Order.PaymentStatus)
	}
}

// A success for an amount other than the order's is reported, not recorded.
func TestStatusChecksTheAmount(t *testing.T) {
	app, stub := newApp(t)
	result := gctest.PlaceOrder(t, app, "cashfree")
	stub.setPayments(result.Payment.Reference, `[`+paymentJSON("613", "SUCCESS", "1.00", "Transaction successful")+`]`)
	check, err := app.Pay().CheckPayment(context.Background(), result.Order.ID)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if check.Gateway.Paid || check.Recorded || !strings.Contains(check.Gateway.Detail, "100") {
		t.Errorf("gateway = %+v, want an unmatched amount reported and nothing recorded", check.Gateway)
	}
}

// The storefront's return page asks the engine to verify, naming the Cashfree
// order it came back with; the payment Cashfree reports is recorded.
func TestVerifyOnReturn(t *testing.T) {
	app, stub := newApp(t)
	result := gctest.PlaceOrder(t, app, "cashfree")
	cf := result.Payment.Reference

	rec := gctest.Request(t, app, http.MethodPost, "/x/payments-cashfree/orders/"+cf+"/verify", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify = %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Order         string `json:"order"`
		PaymentStatus string `json:"payment_status"`
	}
	gctest.DecodeData(t, rec, &out)
	if out.Order != result.Order.Number || out.PaymentStatus != gocommerce.PaymentPending {
		t.Errorf("before paying: %+v", out)
	}

	stub.setPayments(cf, `[`+paymentJSON("614", "SUCCESS", "25.00", "Transaction successful")+`]`)
	rec = gctest.Request(t, app, http.MethodPost, "/x/payments-cashfree/orders/"+cf+"/verify", nil)
	gctest.DecodeData(t, rec, &out)
	if out.PaymentStatus != gocommerce.PaymentPaid {
		t.Errorf("after paying: %+v, want paid", out)
	}
	if order := getOrder(t, app, result.Order.ID); order.PaymentReference != "614" {
		t.Errorf("payment reference = %q, want 614", order.PaymentReference)
	}

	// Paid, the answer comes from the store and Cashfree is not asked again.
	before := stub.calls.Load()
	gctest.Request(t, app, http.MethodPost, "/x/payments-cashfree/orders/"+cf+"/verify", nil)
	if stub.calls.Load() != before {
		t.Error("a paid order asked Cashfree again")
	}

	if rec := gctest.Request(t, app, http.MethodPost, "/x/payments-cashfree/orders/NOT-OURS_1/verify", nil); rec.Code != http.StatusNotFound {
		t.Errorf("verify of an unknown Cashfree order = %d, want 404", rec.Code)
	}
}

func TestSpecCoversRoutes(t *testing.T) {
	app, _ := newApp(t)
	gctest.AssertSpecCoversModuleRoutes(t, app, "payments-cashfree")
	for _, p := range app.Settings().PaymentMethods {
		if p.Code == "cashfree" && (!p.ChecksPayments || p.Name != "Cashfree") {
			t.Errorf("settings = %+v, want Cashfree, checkable", p)
		}
	}
}

// ----------------------------------------------------------------- refunds

func TestRefund(t *testing.T) {
	app, stub := newApp(t)
	ctx := context.Background()
	result := gctest.PlaceOrder(t, app, "cashfree")
	cf := result.Payment.Reference
	if rec := postWebhook(t, app, success(cf, "5114917126974", "25.00")); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}

	order, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{AmountMinor: 1050, Reason: "one item"}, nil)
	if err != nil {
		t.Fatalf("partial refund: %v", err)
	}
	sent, to := stub.lastRefund()
	if to != cf || fmt.Sprint(sent["refund_amount"]) != "10.50" {
		t.Errorf("refund sent %v to %s, want 10.50 against %s", sent, to, cf)
	}
	if id, _ := sent["refund_id"].(string); len(id) < 3 || len(id) > 40 {
		t.Errorf("refund_id = %q, want 3 to 40 characters", id)
	}
	if order.PaymentStatus != gocommerce.PaymentPaid || len(order.Refunds) != 1 || order.Refunds[0].ProviderReference != "1553338" {
		t.Errorf("after a partial refund: payment %q, refunds %+v", order.PaymentStatus, order.Refunds)
	}

	order, err = app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{}, nil)
	if err != nil {
		t.Fatalf("refund the rest: %v", err)
	}
	if sent, _ := stub.lastRefund(); fmt.Sprint(sent["refund_amount"]) != "14.50" {
		t.Errorf("second refund sent %v, want the 14.50 left", sent)
	}
	if order.PaymentStatus != gocommerce.PaymentRefunded {
		t.Errorf("payment status = %q, want refunded", order.PaymentStatus)
	}
}

// Cashfree answering that a refund was cancelled or rejected is a failed
// refund, not a success.
func TestRefundRefusedByCashfree(t *testing.T) {
	app, stub := newApp(t)
	result := gctest.PlaceOrder(t, app, "cashfree")
	if rec := postWebhook(t, app, success(result.Payment.Reference, "79", "25.00")); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}
	stub.answerRefund("CANCELLED")
	if _, err := app.Pay().Refund(context.Background(), result.Order.ID, gocommerce.RefundRequest{}, nil); err == nil {
		t.Error("a cancelled refund was recorded as sent")
	}
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPaid {
		t.Errorf("payment status = %q, want still paid", order.PaymentStatus)
	}
}

// When checkout was retried, the order has two Cashfree orders. The refund
// goes against the one Cashfree took the money on.
func TestRefundFindsThePaidCashfreeOrder(t *testing.T) {
	app, stub, mod := newAppModule(t)
	ctx := context.Background()
	result := gctest.PlaceOrder(t, app, "cashfree")
	first := result.Payment.Reference
	second, err := mod.Initiate(ctx, result.Order, gocommerce.PayOptions{})
	if err != nil {
		t.Fatalf("second initiate: %v", err)
	}
	if second.Reference == first {
		t.Fatal("a retried checkout reused the Cashfree order id")
	}
	stub.setPayments(first, `[`+paymentJSON("71", "USER_DROPPED", "25.00", "dropped")+`]`)
	stub.setPayments(second.Reference, `[`+paymentJSON("72", "SUCCESS", "25.00", "ok")+`]`)
	if rec := postWebhook(t, app, success(second.Reference, "72", "25.00")); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}
	if _, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{}, nil); err != nil {
		t.Fatalf("refund: %v", err)
	}
	if _, to := stub.lastRefund(); to != second.Reference {
		t.Errorf("refund went to %s, want the paid Cashfree order %s", to, second.Reference)
	}
}

// -------------------------------------------------------------- set up idle

// With nothing in Config the module still registers, so the panel lists it,
// but it is not offered at checkout until its fields are typed into the plugin.
func TestInstalledIdleUntilConfigured(t *testing.T) {
	app := gctest.NewWithConfig(t, gocommerce.Config{AllowPrivateURLs: true, Currency: "INR"}, New(Config{}))
	ctx := context.Background()

	listed := false
	for _, p := range app.Settings().PaymentMethods {
		if p.Code == "cashfree" {
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
		if code == "cashfree" {
			t.Fatal("an idle gateway must not be offered at checkout")
		}
	}
	// An unconfigured webhook cannot be verified, so it is refused.
	if rec := deliver(app, `{}`, "x", "1"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("webhook to an idle module = %d, want 503", rec.Code)
	}

	on := true
	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{
		"app_id": testAppID, "secret_key": testSecret, "environment": "sandbox",
	}}); err != nil {
		t.Fatalf("configure from the panel: %v", err)
	}
	offered := false
	for _, code := range app.Pay().Methods() {
		offered = offered || code == "cashfree"
	}
	if !offered {
		t.Fatal("configured from the panel, the gateway should be offered")
	}
}

// The API base URL is a store setting, so the module's own client is the
// install's guarded one: pointed at a private address, a payment fails
// without that address being reached (D109).
func TestAPrivateBaseURLIsNotCalled(t *testing.T) {
	stub, server := newStub(t)
	mod := New(Config{AppID: testAppID, SecretKey: testSecret, BaseURL: server.URL})
	app := gctest.NewWithConfig(t, gocommerce.Config{Currency: "INR"}, mod)
	result := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	_, err := mod.Initiate(context.Background(), result.Order, gocommerce.PayOptions{})
	if err == nil || !strings.Contains(err.Error(), "is not a public address") {
		t.Fatalf("initiate against a loopback base URL = %v, want the refusal", err)
	}
	if n := stub.calls.Load(); n != 0 {
		t.Errorf("the private address was reached %d times", n)
	}
}

// ------------------------------------------------------------------ helpers

func sign(timestamp string, body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func hexSign(timestamp string, body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write(body)
	return fmt.Sprintf("%x", mac.Sum(nil))
}

// success is a PAYMENT_SUCCESS_WEBHOOK as Cashfree's 2023-08-01 reference
// prints it, for a Cashfree order of the given amount.
func success(cfOrder, cfPayment, amount string) string {
	return fmt.Sprintf(`{"data":{"order":{"order_id":%q,"order_amount":%s,"order_currency":"INR","order_tags":null},"payment":{"cf_payment_id":%s,"payment_status":"SUCCESS","payment_amount":%s,"payment_currency":"INR","payment_message":"00::Transaction success","payment_time":"2026-10-22T12:20:29+05:30","bank_reference":"234928698581","auth_id":null,"payment_method":{"upi":{"channel":"collect","upi_id":"shopper@ybl"}},"payment_group":"upi"},"customer_details":{"customer_name":null,"customer_id":"gc1","customer_email":"gctest@example.com","customer_phone":"9908734801"},"payment_gateway_details":{"gateway_name":"CASHFREE","gateway_order_id":"1634766330","gateway_payment_id":"1504280029"}},"event_time":"2026-10-22T12:20:30+05:30","type":"PAYMENT_SUCCESS_WEBHOOK"}`,
		cfOrder, amount, strconv.Quote(cfPayment), amount)
}

// failed is a PAYMENT_FAILED_WEBHOOK, with cf_payment_id as a number, the way
// the reference's own failed sample writes it.
func failed(cfOrder, cfPayment string) string {
	return fmt.Sprintf(`{"data":{"order":{"order_id":%q,"order_amount":25.00,"order_currency":"INR","order_tags":null},"payment":{"cf_payment_id":%s,"payment_status":"FAILED","payment_amount":25.00,"payment_currency":"INR","payment_message":"Internal Server Error","payment_time":"2026-10-22T14:57:38+05:30","bank_reference":null,"auth_id":null,"payment_method":{"card":{"channel":null}},"payment_group":"card"},"customer_details":{"customer_phone":"+919611199227"},"error_details":{"error_code":"TRANSACTION_DECLINED","error_description":"payment has been declined","error_reason":"debit_failed","error_source":"bank"}},"event_time":"2026-10-22T14:57:38+05:30","type":"PAYMENT_FAILED_WEBHOOK"}`,
		cfOrder, cfPayment)
}

// paymentJSON is one entry of GET /orders/{order_id}/payments.
func paymentJSON(cfPayment, status, amount, message string) string {
	return fmt.Sprintf(`{"cf_payment_id":%q,"order_id":"x","entity":"payment","payment_currency":"INR","order_amount":%s,"order_currency":"INR","payment_amount":%s,"payment_status":%q,"payment_time":"2026-10-22T12:15:06+05:30","payment_message":%q,"is_captured":true,"payment_group":"upi"}`,
		cfPayment, amount, amount, status, message)
}

func deliver(app *gocommerce.App, body, signature, timestamp string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/checkout/cashfree/webhook", strings.NewReader(body))
	if signature != "" {
		req.Header.Set("x-webhook-signature", signature)
	}
	if timestamp != "" {
		req.Header.Set("x-webhook-timestamp", timestamp)
	}
	req.Header.Set("x-webhook-version", apiVersion)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

func postWebhook(t *testing.T, app *gocommerce.App, body string) *httptest.ResponseRecorder {
	t.Helper()
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	return deliver(app, body, sign(ts, []byte(body), testSecret), ts)
}

func getOrder(t *testing.T, app *gocommerce.App, id int64) *gocommerce.Order {
	t.Helper()
	order, err := app.Order().Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	return order
}

func paidEvents(t *testing.T, app *gocommerce.App, orderID int64) []int64 {
	t.Helper()
	rows, err := app.DB().Query(`SELECT id FROM outbox_events WHERE event_name = 'order.paid' AND aggregate_id = $1`, orderID)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// checkoutInput is a cart ready to check out, for the tests that need their
// own phone or return URL, or expect the payment step to refuse.
func checkoutInput(t *testing.T, app *gocommerce.App, sku, phone string) gocommerce.CheckoutInput {
	t.Helper()
	ctx := context.Background()
	product := gctest.CreateProduct(t, app, sku, 2500, 10)
	cart, err := app.Cart().Create(ctx, "")
	if err != nil {
		t.Fatalf("create cart: %v", err)
	}
	if _, err := app.Cart().AddLine(ctx, cart.Token, product.DefaultVariant().ID, 1); err != nil {
		t.Fatalf("add to cart: %v", err)
	}
	return gocommerce.CheckoutInput{
		CartID: cart.Token, Email: "gctest@example.com", Name: "GC Test",
		Address: gocommerce.Address{Line1: "1 MG Road", City: "Bengaluru", State: "KA", PostalCode: "560001", Country: "IN", Phone: phone},
	}
}
