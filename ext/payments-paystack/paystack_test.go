package paystack

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

const (
	testKey = "sk_test_stand_in"
	liveKey = "sk_live_stand_in"
)

// ---------------------------------------------------------------- the stub

// stub stands in for Paystack's API, in the shapes its reference documents, and
// remembers what it was sent.
type stub struct {
	mu          sync.Mutex
	key         string
	initialized []map[string]any
	refunds     []map[string]any
	// what GET /transaction/verify/{reference} answers, by reference; 400 when
	// absent, which is how Paystack answers a reference it does not have.
	transactions map[string]string
	verifies     atomic.Int32
	calls        atomic.Int32
	verifyDown   bool
}

func newStub(t *testing.T, key string) (*stub, *httptest.Server) {
	t.Helper()
	s := &stub{key: key, transactions: map[string]string{}}
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+s.key {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"status":false,"message":"Invalid key"}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/transaction/initialize":
			s.initialized = append(s.initialized, in)
			fmt.Fprintf(w, `{"status":true,"message":"Authorization URL created","data":{"authorization_url":"https://checkout.paystack.com/%d","access_code":"code","reference":%q}}`,
				len(s.initialized), in["reference"])
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/transaction/verify/"):
			s.verifies.Add(1)
			if s.verifyDown {
				w.WriteHeader(http.StatusBadGateway)
				fmt.Fprint(w, `{"status":false,"message":"unavailable"}`)
				return
			}
			tx, ok := s.transactions[strings.TrimPrefix(r.URL.Path, "/transaction/verify/")]
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"status":false,"message":"Transaction reference not found"}`)
				return
			}
			fmt.Fprintf(w, `{"status":true,"message":"Verification successful","data":%s}`, tx)
		case r.Method == http.MethodPost && r.URL.Path == "/refund":
			s.refunds = append(s.refunds, in)
			fmt.Fprint(w, `{"status":true,"message":"Refund has been queued for processing","data":{"id":3018284,"status":"pending","currency":"USD"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"status":false,"message":"not found"}`)
		}
	})
	return s, server
}

func (s *stub) set(reference, tx string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transactions[reference] = tx
}

func (s *stub) lastInit() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.initialized) == 0 {
		return nil
	}
	return s.initialized[len(s.initialized)-1]
}

// tx is the part of Paystack's verify answer this module reads.
func tx(domain, status, reference string, amount int64, currency string) string {
	return fmt.Sprintf(`{"id":2009945086,"domain":%q,"status":%q,"reference":%q,"amount":%d,"currency":%q,"gateway_response":"Successful","channel":"card","fees":100}`,
		domain, status, reference, amount, currency)
}

func newApp(t *testing.T, key string) (*gocommerce.App, *stub) {
	t.Helper()
	s, server := newStub(t, key)
	return gctest.NewWithConfig(t, gocommerce.Config{AllowPrivateURLs: true, Currency: "USD"},
		New(Config{SecretKey: key, BaseURL: server.URL})), s
}

func checkoutInput(t *testing.T, app *gocommerce.App, sku string) gocommerce.CheckoutInput {
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
		ReturnURL: "https://shop.example/thanks",
		Address:   gocommerce.Address{Line1: "1 Marina", City: "Lagos", State: "LA", PostalCode: "101001", Country: "NG", Phone: "+2348000000000"},
	}
}

func place(t *testing.T, app *gocommerce.App) *gocommerce.CheckoutResult {
	t.Helper()
	result, err := app.Order().Checkout(context.Background(), "paystack", checkoutInput(t, app, "GCTEST-ps"), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	return result
}

func sign(body, key string) string {
	mac := hmac.New(sha512.New, []byte(key))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

// success is a delivery claiming success. Its amount is deliberately nonsense:
// nothing in it should matter but the reference.
func success(reference string) string {
	return fmt.Sprintf(`{"event":"charge.success","data":{"reference":%q,"status":"success","amount":1,"currency":"XXX"}}`, reference)
}

func deliver(app *gocommerce.App, body, signature string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/checkout/paystack/webhook", strings.NewReader(body))
	if signature != "" {
		req.Header.Set("x-paystack-signature", signature)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

func post(t *testing.T, app *gocommerce.App, body, key string) {
	t.Helper()
	if rec := deliver(app, body, sign(body, key)); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}
}

func getOrder(t *testing.T, app *gocommerce.App, id int64) *gocommerce.Order {
	t.Helper()
	order, err := app.Order().Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	return order
}

func paidEvents(t *testing.T, app *gocommerce.App, orderID int64) int {
	t.Helper()
	var n int
	if err := app.DB().QueryRow(`SELECT count(*) FROM outbox_events WHERE event_name = 'order.paid' AND aggregate_id = $1`, orderID).Scan(&n); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	return n
}

// ---------------------------------------------------------------- checkout

// The module's reason to exist: a transaction for the order's total in hundredths,
// a redirect to Paystack, settled by a webhook only after Paystack is asked.
func TestCheckoutAndWebhook(t *testing.T) {
	app, s := newApp(t, testKey)
	result := place(t, app)
	if result.Payment.Kind != gocommerce.IntentRedirect {
		t.Fatalf("payment kind = %q, want redirect", result.Payment.Kind)
	}
	ref := result.Payment.Reference
	if !strings.HasPrefix(ref, result.Order.Number+"-") {
		t.Errorf("reference = %q, want the order number and a suffix", ref)
	}
	data := result.Payment.ClientData
	if data["url"] != "https://checkout.paystack.com/1" || data["reference"] != ref || data["mode"] != "test" {
		t.Errorf("client data = %v", data)
	}
	sent := s.lastInit()
	if fmt.Sprint(sent["amount"]) != "2500" || sent["currency"] != "USD" || sent["email"] != "gctest@example.com" ||
		sent["reference"] != ref || sent["callback_url"] != "https://shop.example/thanks" {
		t.Errorf("initialize sent %v", sent)
	}

	s.set(ref, tx("test", "success", ref, 2500, "USD"))
	post(t, app, success(ref), testKey)
	order := getOrder(t, app, result.Order.ID)
	if order.PaymentStatus != gocommerce.PaymentPaid || order.Status != gocommerce.OrderConfirmed || order.PaymentReference != ref {
		t.Errorf("payment %q, order %q, reference %q; want paid, confirmed, %s", order.PaymentStatus, order.Status, order.PaymentReference, ref)
	}
}

// A store selling in something Paystack lacks never offers it, and an order in
// one is refused before anything is sent: passing the number through would
// charge some other amount.
func TestUnsupportedCurrency(t *testing.T) {
	s, server := newStub(t, testKey)
	mod := New(Config{SecretKey: testKey, BaseURL: server.URL})
	app := gctest.NewWithConfig(t, gocommerce.Config{AllowPrivateURLs: true, Currency: "INR"}, mod)
	for _, code := range app.Pay().Methods() {
		if code == "paystack" {
			t.Error("a rupee store offers Paystack")
		}
	}
	order := gctest.PlaceOrder(t, app, gocommerce.CodeCOD).Order
	_, err := mod.Initiate(context.Background(), order, gocommerce.PayOptions{ReturnURL: "https://shop.example"})
	if err == nil || !strings.Contains(err.Error(), "INR") {
		t.Errorf("initiate = %v, want a refusal naming INR", err)
	}
	if s.calls.Load() != 0 {
		t.Error("Paystack was called")
	}
}

func TestReturnURLIsRequired(t *testing.T) {
	app, s := newApp(t, testKey)
	in := checkoutInput(t, app, "GCTEST-noret")
	in.ReturnURL = ""
	if _, err := app.Order().Checkout(context.Background(), "paystack", in, ""); err == nil {
		t.Error("a checkout with nowhere to come back to was accepted")
	}
	if s.lastInit() != nil {
		t.Error("a transaction was opened")
	}
}

// ----------------------------------------------------------------- webhook

func TestWebhookRejectsBadSignature(t *testing.T) {
	app, s := newApp(t, testKey)
	result := place(t, app)
	ref := result.Payment.Reference
	s.set(ref, tx("test", "success", ref, 2500, "USD"))
	body := success(ref)
	for name, sig := range map[string]string{
		"no":          "",
		"a junk":      "zz",
		"another key": sign(body, "sk_test_other"),
		"a tampered":  sign(body+" ", testKey),
	} {
		if rec := deliver(app, body, sig); rec.Code != http.StatusBadRequest {
			t.Errorf("%s signature = %d, want 400", name, rec.Code)
		}
	}
	if s.verifies.Load() != 0 {
		t.Error("an unauthenticated delivery reached Paystack")
	}
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPending {
		t.Errorf("payment status = %q: a forged webhook changed the order", order.PaymentStatus)
	}
}

// Paystack's word is what counts. A correctly signed delivery does not settle an
// order whose payment is for another amount or currency, and says so where
// somebody will see it; retrying would not change the answer.
func TestWrongAmountOrCurrencyIsRefused(t *testing.T) {
	for name, answer := range map[string]struct {
		amount   int64
		currency string
	}{
		"another amount":   {2400, "USD"},
		"more than asked":  {2600, "USD"},
		"another currency": {2500, "NGN"},
	} {
		t.Run(name, func(t *testing.T) {
			app, s := newApp(t, testKey)
			result := place(t, app)
			ref := result.Payment.Reference
			s.set(ref, tx("test", "success", ref, answer.amount, answer.currency))
			post(t, app, success(ref), testKey)
			if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPending {
				t.Errorf("payment status = %q, want the order untouched", order.PaymentStatus)
			}
			check, err := app.Pay().CheckPayment(context.Background(), result.Order.ID)
			if err != nil {
				t.Fatalf("check: %v", err)
			}
			if check.Gateway.Paid || check.Recorded {
				t.Errorf("check-payment settled it: %+v", check.Gateway)
			}
		})
	}
}

// A delivery can say what it likes; if Paystack does not call it a success the
// order is left alone and the retry finds work to do.
func TestDeliveryPaystackDoesNotBearOut(t *testing.T) {
	app, s := newApp(t, testKey)
	result := place(t, app)
	ref := result.Payment.Reference
	s.set(ref, tx("test", "abandoned", ref, 2500, "USD"))
	body := success(ref)
	if rec := deliver(app, body, sign(body, testKey)); rec.Code != http.StatusBadGateway {
		t.Errorf("webhook = %d, want 502 so Paystack retries", rec.Code)
	}
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPending {
		t.Fatalf("payment status = %q", order.PaymentStatus)
	}
	s.set(ref, tx("test", "success", ref, 2500, "USD"))
	post(t, app, body, testKey)
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPaid {
		t.Errorf("after the retry: payment status = %q, want paid", order.PaymentStatus)
	}
}

// Paystack down when a delivery comes: nothing is recorded and the redelivery
// settles the order.
func TestPaystackDownThenRetried(t *testing.T) {
	app, s := newApp(t, testKey)
	result := place(t, app)
	ref := result.Payment.Reference
	s.set(ref, tx("test", "success", ref, 2500, "USD"))
	s.mu.Lock()
	s.verifyDown = true
	s.mu.Unlock()
	body := success(ref)
	if rec := deliver(app, body, sign(body, testKey)); rec.Code != http.StatusBadGateway {
		t.Errorf("webhook = %d, want 502", rec.Code)
	}
	s.mu.Lock()
	s.verifyDown = false
	s.mu.Unlock()
	post(t, app, body, testKey)
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPaid {
		t.Errorf("payment status = %q, want paid", order.PaymentStatus)
	}
}

func TestDuplicateWebhookIsHarmless(t *testing.T) {
	app, s := newApp(t, testKey)
	result := place(t, app)
	ref := result.Payment.Reference
	s.set(ref, tx("test", "success", ref, 2500, "USD"))
	for range 3 {
		post(t, app, success(ref), testKey)
	}
	if n := paidEvents(t, app, result.Order.ID); n != 1 {
		t.Errorf("order.paid events = %d, want 1", n)
	}
	if n := s.verifies.Load(); n != 1 {
		t.Errorf("Paystack asked %d times, want 1", n)
	}
}

// A reference this store did not open, and events that are not about a payment,
// are acknowledged so Paystack stops retrying them for days.
func TestWebhookForSomethingElseIsLeftAlone(t *testing.T) {
	app, s := newApp(t, testKey)
	post(t, app, success("SOMEONE-ELSES"), testKey)
	post(t, app, `{"event":"transfer.success","data":{"reference":"x"}}`, testKey)
	if s.verifies.Load() != 0 {
		t.Error("Paystack was asked about something that is not this store's")
	}
}

// Money for an order cancelled meanwhile does not revive it; a person refunds it.
func TestPaymentForACancelledOrder(t *testing.T) {
	app, s := newApp(t, testKey)
	ctx := context.Background()
	result := place(t, app)
	ref := result.Payment.Reference
	if _, err := app.Order().Cancel(ctx, result.Order.ID, "test"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	s.set(ref, tx("test", "success", ref, 2500, "USD"))
	post(t, app, success(ref), testKey)
	order := getOrder(t, app, result.Order.ID)
	if order.Status != gocommerce.OrderCancelled || order.PaymentStatus == gocommerce.PaymentPaid {
		t.Errorf("order %q, payment %q; want it cancelled and unpaid", order.Status, order.PaymentStatus)
	}
}

// ----------------------------------------------------------------- the keys

// The key says the mode, the checkout says it to the storefront, and a payment
// from the other mode settles nothing: test money on a live store is the mistake
// this exists to stop.
func TestTestAndLiveKeys(t *testing.T) {
	for key, want := range map[string]string{testKey: "test", liveKey: "live", "other": ""} {
		if got := keyMode(key); got != want {
			t.Errorf("keyMode(%q) = %q, want %q", key, got, want)
		}
	}

	t.Run("a live store takes a live payment", func(t *testing.T) {
		app, s := newApp(t, liveKey)
		result := place(t, app)
		if result.Payment.ClientData["mode"] != "live" {
			t.Errorf("mode = %q", result.Payment.ClientData["mode"])
		}
		ref := result.Payment.Reference
		s.set(ref, tx("live", "success", ref, 2500, "USD"))
		post(t, app, success(ref), liveKey)
		if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPaid {
			t.Errorf("payment status = %q, want paid", order.PaymentStatus)
		}
	})

	t.Run("a live store refuses a delivery signed with the test key", func(t *testing.T) {
		app, s := newApp(t, liveKey)
		result := place(t, app)
		ref := result.Payment.Reference
		body := success(ref)
		s.set(ref, tx("test", "success", ref, 2500, "USD"))
		if rec := deliver(app, body, sign(body, testKey)); rec.Code != http.StatusBadRequest {
			t.Errorf("webhook = %d, want 400", rec.Code)
		}
	})

	t.Run("a live store does not settle on a test payment", func(t *testing.T) {
		app, s := newApp(t, liveKey)
		result := place(t, app)
		ref := result.Payment.Reference
		s.set(ref, tx("test", "success", ref, 2500, "USD"))
		post(t, app, success(ref), liveKey)
		check, err := app.Pay().CheckPayment(context.Background(), result.Order.ID)
		if err != nil {
			t.Fatalf("check: %v", err)
		}
		if check.Gateway.Paid || check.Order.PaymentStatus != gocommerce.PaymentPending {
			t.Errorf("gateway %+v, payment %q; want it unsettled", check.Gateway, check.Order.PaymentStatus)
		}
	})

	t.Run("a test store does not settle on a live payment", func(t *testing.T) {
		app, s := newApp(t, testKey)
		result := place(t, app)
		ref := result.Payment.Reference
		s.set(ref, tx("live", "success", ref, 2500, "USD"))
		post(t, app, success(ref), testKey)
		if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPending {
			t.Errorf("payment status = %q, want the order untouched", order.PaymentStatus)
		}
	})

	t.Run("the public key is not a secret key", func(t *testing.T) {
		app := gctest.NewWithConfig(t, gocommerce.Config{Currency: "USD"}, New(Config{SecretKey: "pk_test_public"}))
		for _, code := range app.Pay().Methods() {
			if code == "paystack" {
				t.Error("offered with only a public key")
			}
		}
	})
}

// ------------------------------------------------------------------ status

func TestCheckPaymentAndVerifyRoute(t *testing.T) {
	app, s := newApp(t, testKey)
	ctx := context.Background()
	result := place(t, app)
	ref := result.Payment.Reference

	s.set(ref, tx("test", "failed", ref, 2500, "USD"))
	check, err := app.Pay().CheckPayment(ctx, result.Order.ID)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !check.Gateway.Failed || check.Recorded || check.Order.PaymentStatus != gocommerce.PaymentPending {
		t.Errorf("after a decline: %+v, payment %q", check.Gateway, check.Order.PaymentStatus)
	}

	s.set(ref, tx("test", "success", ref, 2500, "USD"))
	req := httptest.NewRequest(http.MethodPost, "/x/payments-paystack/transactions/"+ref+"/verify", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"payment_status":"paid"`) {
		t.Fatalf("verify = %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "gctest@example.com") {
		t.Error("the public route gave away the shopper's email")
	}
	asked := s.verifies.Load()
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x/payments-paystack/transactions/"+ref+"/verify", nil))
	if s.verifies.Load() != asked {
		t.Error("a settled order went back to Paystack")
	}
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x/payments-paystack/transactions/nobody/verify", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown reference = %d, want 404", rec.Code)
	}
}

// ----------------------------------------------------------------- refunds

func TestRefund(t *testing.T) {
	app, s := newApp(t, testKey)
	ctx := context.Background()
	result := place(t, app)
	ref := result.Payment.Reference
	s.set(ref, tx("test", "success", ref, 2500, "USD"))
	post(t, app, success(ref), testKey)

	order, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{AmountMinor: 1000}, nil)
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if order.Refunded.AmountMinor != 1000 || order.PaymentStatus != gocommerce.PaymentPaid {
		t.Errorf("refunded %d, payment %q", order.Refunded.AmountMinor, order.PaymentStatus)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.refunds) != 1 || s.refunds[0]["transaction"] != ref || fmt.Sprint(s.refunds[0]["amount"]) != "1000" || s.refunds[0]["currency"] != "USD" {
		t.Errorf("refunds sent = %v", s.refunds)
	}
}

// A reference typed by hand names nothing this store opened, so no money moves.
func TestRefundOfAHandTypedReference(t *testing.T) {
	app, s := newApp(t, testKey)
	ctx := context.Background()
	result := place(t, app)
	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "typed-by-hand"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	if _, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{}, nil); err == nil {
		t.Error("the refund went through")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.refunds) != 0 {
		t.Errorf("refunds sent = %v", s.refunds)
	}
}

// ------------------------------------------------------------------ resume

func TestRetriedCheckout(t *testing.T) {
	retry := func(t *testing.T, status string) (*gocommerce.App, *stub, *gocommerce.CheckoutResult, *gocommerce.CheckoutResult) {
		t.Helper()
		app, s := newApp(t, testKey)
		ctx := context.Background()
		in := checkoutInput(t, app, "GCTEST-retry")
		first, err := app.Order().Checkout(ctx, "paystack", in, "key-1")
		if err != nil {
			t.Fatalf("first checkout: %v", err)
		}
		if status != "" {
			s.set(first.Payment.Reference, tx("test", status, first.Payment.Reference, 2500, "USD"))
		}
		again, err := app.Order().Checkout(ctx, "paystack", in, "key-1")
		if err != nil {
			t.Fatalf("retry: %v", err)
		}
		return app, s, first, again
	}
	count := func(s *stub) int {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.initialized)
	}

	for _, status := range []string{"", "abandoned"} {
		_, s, first, again := retry(t, status)
		if again.Payment.Reference != first.Payment.Reference || count(s) != 1 {
			t.Errorf("status %q: retry opened %q after %q, %d transactions; want the same one", status, again.Payment.Reference, first.Payment.Reference, count(s))
		}
	}

	_, s, first, again := retry(t, "failed")
	if again.Payment.Reference == first.Payment.Reference || count(s) != 2 {
		t.Errorf("after a failure: retry reopened %q, %d transactions; want a new one", again.Payment.Reference, count(s))
	}

	app, s, first, again := retry(t, "success")
	if again.Order.PaymentStatus != gocommerce.PaymentPaid || count(s) != 1 {
		t.Errorf("payment %q, %d transactions; want paid on the first", again.Order.PaymentStatus, count(s))
	}
	if got := getOrder(t, app, first.Order.ID).PaymentReference; got != first.Payment.Reference {
		t.Errorf("reference = %q, want %q", got, first.Payment.Reference)
	}
}

// -------------------------------------------------------------- the install

// Installed idle, switched on with a key from the panel, and the key stays a
// secret: a read of the settings never hands it back.
func TestConfiguredFromThePanel(t *testing.T) {
	_, server := newStub(t, testKey)
	app := gctest.NewWithConfig(t, gocommerce.Config{AllowPrivateURLs: true, Currency: "NGN"}, New(Config{BaseURL: server.URL}))
	ctx := context.Background()
	offered := func() bool {
		for _, code := range app.Pay().Methods() {
			if code == "paystack" {
				return true
			}
		}
		return false
	}
	if offered() {
		t.Fatal("offered with no key")
	}
	on := true
	plugin, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{"secret_key": testKey}})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if !offered() {
		t.Error("configured from the panel, the gateway should be offered")
	}
	if fmt.Sprint(plugin.Settings["secret_key"]) == testKey {
		t.Error("the settings read handed the key back")
	}
}

// The module's own client is the install's guarded one (D109).
func TestAPrivateAddressIsNotCalled(t *testing.T) {
	s, server := newStub(t, testKey)
	mod := New(Config{SecretKey: testKey, BaseURL: server.URL})
	app := gctest.NewWithConfig(t, gocommerce.Config{Currency: "USD"}, mod)
	result := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	_, err := mod.Initiate(context.Background(), result.Order, gocommerce.PayOptions{ReturnURL: "https://shop.example"})
	if err == nil || !strings.Contains(err.Error(), "is not a public address") {
		t.Fatalf("initiate against a loopback address = %v, want the refusal", err)
	}
	if s.calls.Load() != 0 {
		t.Error("the private address was reached")
	}
}
