package flutterwave

import (
	"context"
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

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

const (
	testKey  = "FLWSECK_TEST-stand-in-X"
	liveKey  = "FLWSECK-stand-in-X"
	testHash = "stand-in-hash"
)

// Flutterwave speaks major units and the engine minor ones. The conversion is
// string work, never a float (rule 6), and honours each currency's decimals: a
// hundredfold slip on a zero-decimal franc is the bug this exists to catch.
func TestMajorAndMinor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		minor    int64
		currency string
		want     string
	}{
		{750000, "NGN", "7500"}, {2550, "USD", "25.50"}, {2505, "USD", "25.05"}, {5, "GHS", "0.05"},
		{7500, "UGX", "7500"}, {7500, "XOF", "7500"}, {7500, "RWF", "7500"},
	} {
		if got := major(tc.minor, tc.currency); string(got) != tc.want {
			t.Errorf("major(%d %s) = %s, want %s", tc.minor, tc.currency, got, tc.want)
		}
		if got, err := minor(json.Number(tc.want), tc.currency); err != nil || got != tc.minor {
			t.Errorf("minor(%s %s) = %d, %v; want %d", tc.want, tc.currency, got, err, tc.minor)
		}
	}
	if got, err := minor("25.500", "USD"); err != nil || got != 2550 {
		t.Errorf("minor(25.500) = %d, %v", got, err)
	}
	for _, in := range []struct{ n, currency string }{
		{"", "USD"}, {"25.555", "USD"}, {"-1", "USD"}, {"1e3", "USD"}, {"abc", "USD"}, {"10.5", "UGX"},
	} {
		if got, err := minor(json.Number(in.n), in.currency); err == nil {
			t.Errorf("minor(%q %s) = %d, want an error", in.n, in.currency, got)
		}
	}
}

// ---------------------------------------------------------------- the stub

// stub stands in for Flutterwave's v3 API, in the shapes its reference documents,
// and remembers what it was sent. Transactions are held per key, because a test
// key cannot see a live transaction or the other way round.
type stub struct {
	mu       sync.Mutex
	key      string
	created  []map[string]any
	refunds  []map[string]any
	refundTo []string
	byID     map[string]string
	byRef    map[string]string
	verifies atomic.Int32
	calls    atomic.Int32
	down     bool
}

func newStub(t *testing.T, key string) (*stub, *httptest.Server) {
	t.Helper()
	s := &stub{key: key, byID: map[string]string{}, byRef: map[string]string{}}
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+s.key {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"status":"error","message":"Invalid authorization key","data":null}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		s.mu.Lock()
		defer s.mu.Unlock()
		missing := func() {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"status":"error","message":"No transaction was found for this id","data":null}`)
		}
		answer := func(tx string, ok bool) {
			s.verifies.Add(1)
			switch {
			case s.down:
				w.WriteHeader(http.StatusBadGateway)
				fmt.Fprint(w, `{"status":"error","message":"unavailable","data":null}`)
			case !ok:
				missing()
			default:
				fmt.Fprintf(w, `{"status":"success","message":"Transaction fetched successfully","data":%s}`, tx)
			}
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/payments":
			s.created = append(s.created, in)
			fmt.Fprintf(w, `{"status":"success","message":"Hosted Link","data":{"link":"https://checkout.flutterwave.com/v3/hosted/pay/%d"}}`, len(s.created))
		case r.Method == http.MethodGet && r.URL.Path == "/v3/transactions/verify_by_reference":
			tx, ok := s.byRef[r.URL.Query().Get("tx_ref")]
			answer(tx, ok)
		case r.Method == http.MethodGet && len(parts) == 4 && parts[3] == "verify":
			tx, ok := s.byID[parts[2]]
			answer(tx, ok)
		case r.Method == http.MethodPost && len(parts) == 4 && parts[3] == "refund":
			s.refunds = append(s.refunds, in)
			s.refundTo = append(s.refundTo, parts[2])
			fmt.Fprint(w, `{"status":"success","message":"Transaction refund initiated","data":{"id":8612,"status":"completed","amount_refunded":10}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"status":"error","message":"not found","data":null}`)
		}
	})
	return s, server
}

// tx is the part of Flutterwave's verify answer this module reads, recorded under
// both of the ways it can be asked for.
func (s *stub) tx(id int64, txRef, status, amount, charged, currency string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body := fmt.Sprintf(`{"id":%d,"tx_ref":%q,"flw_ref":"FLW1","amount":%s,"currency":%q,"charged_amount":%s,"app_fee":1.4,"status":%q,"payment_type":"card","processor_response":"Approved"}`,
		id, txRef, amount, currency, charged, status)
	s.byID[strconv.FormatInt(id, 10)] = body
	s.byRef[txRef] = body
}

func (s *stub) lastCreated() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.created) == 0 {
		return nil
	}
	return s.created[len(s.created)-1]
}

func newApp(t *testing.T, key string) (*gocommerce.App, *stub) {
	t.Helper()
	s, server := newStub(t, key)
	return gctest.NewWithConfig(t, gocommerce.Config{AllowPrivateURLs: true, Currency: "USD"},
		New(Config{SecretKey: key, SecretHash: testHash, BaseURL: server.URL})), s
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
	result, err := app.Order().Checkout(context.Background(), "flutterwave", checkoutInput(t, app, "GCTEST-flw"), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	return result
}

// completed is a delivery. Its amount is deliberately nonsense: nothing in it
// should matter but the transaction id and the tx_ref.
func completed(id int64, txRef, status string) string {
	return fmt.Sprintf(`{"event":"charge.completed","data":{"id":%d,"tx_ref":%q,"status":%q,"amount":1,"currency":"XXX"}}`, id, txRef, status)
}

func deliver(app *gocommerce.App, body, hash string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/checkout/flutterwave/webhook", strings.NewReader(body))
	if hash != "" {
		req.Header.Set("verif-hash", hash)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

func post(t *testing.T, app *gocommerce.App, body string) {
	t.Helper()
	if rec := deliver(app, body, testHash); rec.Code != http.StatusOK {
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

// The module's reason to exist: a checkout for the order's total in major units,
// a redirect to Flutterwave, settled by a webhook only after Flutterwave is
// asked, with the transaction id kept because a refund needs it.
func TestCheckoutAndWebhook(t *testing.T) {
	app, s := newApp(t, testKey)
	result := place(t, app)
	if result.Payment.Kind != gocommerce.IntentRedirect {
		t.Fatalf("payment kind = %q, want redirect", result.Payment.Kind)
	}
	ref := result.Payment.Reference
	if !strings.HasPrefix(ref, result.Order.Number+"_") {
		t.Errorf("tx_ref = %q, want the order number and a suffix", ref)
	}
	data := result.Payment.ClientData
	if data["url"] != "https://checkout.flutterwave.com/v3/hosted/pay/1" || data["tx_ref"] != ref || data["mode"] != "test" {
		t.Errorf("client data = %v", data)
	}
	sent := s.lastCreated()
	customer, _ := sent["customer"].(map[string]any)
	if fmt.Sprint(sent["amount"]) != "25" || sent["currency"] != "USD" || sent["tx_ref"] != ref ||
		sent["redirect_url"] != "https://shop.example/thanks" || customer["email"] != "gctest@example.com" {
		t.Errorf("checkout sent %v", sent)
	}

	s.tx(77, ref, "successful", "25", "25.35", "USD")
	post(t, app, completed(77, ref, "successful"))
	order := getOrder(t, app, result.Order.ID)
	if order.PaymentStatus != gocommerce.PaymentPaid || order.Status != gocommerce.OrderConfirmed || order.PaymentReference != "77" {
		t.Errorf("payment %q, order %q, reference %q; want paid, confirmed, 77", order.PaymentStatus, order.Status, order.PaymentReference)
	}
}

// A store selling in something Flutterwave lacks never offers it, and an order in
// one is refused before anything is sent.
func TestUnsupportedCurrency(t *testing.T) {
	s, server := newStub(t, testKey)
	mod := New(Config{SecretKey: testKey, SecretHash: testHash, BaseURL: server.URL})
	app := gctest.NewWithConfig(t, gocommerce.Config{AllowPrivateURLs: true, Currency: "JPY"}, mod)
	for _, code := range app.Pay().Methods() {
		if code == "flutterwave" {
			t.Error("a yen store offers Flutterwave")
		}
	}
	order := gctest.PlaceOrder(t, app, gocommerce.CodeCOD).Order
	_, err := mod.Initiate(context.Background(), order, gocommerce.PayOptions{ReturnURL: "https://shop.example"})
	if err == nil || !strings.Contains(err.Error(), "JPY") {
		t.Errorf("initiate = %v, want a refusal naming JPY", err)
	}
	if s.calls.Load() != 0 {
		t.Error("Flutterwave was called")
	}
}

func TestReturnURLIsRequired(t *testing.T) {
	app, s := newApp(t, testKey)
	in := checkoutInput(t, app, "GCTEST-noret")
	in.ReturnURL = ""
	if _, err := app.Order().Checkout(context.Background(), "flutterwave", in, ""); err == nil {
		t.Error("a checkout with nowhere to come back to was accepted")
	}
	if s.lastCreated() != nil {
		t.Error("a checkout was opened")
	}
}

// ----------------------------------------------------------------- webhook

func TestWebhookRejectsAWrongHash(t *testing.T) {
	app, s := newApp(t, testKey)
	result := place(t, app)
	ref := result.Payment.Reference
	s.tx(77, ref, "successful", "25", "25", "USD")
	body := completed(77, ref, "successful")
	for name, hash := range map[string]string{"no": "", "a wrong": "not-the-hash", "a prefix of the": testHash[:4]} {
		if rec := deliver(app, body, hash); rec.Code != http.StatusBadRequest {
			t.Errorf("%s hash = %d, want 400", name, rec.Code)
		}
	}
	if s.verifies.Load() != 0 {
		t.Error("an unauthenticated delivery reached Flutterwave")
	}
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPending {
		t.Errorf("payment status = %q: a forged webhook changed the order", order.PaymentStatus)
	}
}

// Flutterwave's word is what counts. Its own guide would accept paying more; the
// engine records one figure, so this leaves that for a person too.
func TestWrongAmountOrCurrencyIsRefused(t *testing.T) {
	for name, a := range map[string]struct{ amount, charged, currency string }{
		"another amount":     {"24", "24", "USD"},
		"more than asked":    {"26", "26", "USD"},
		"charged less":       {"25", "24", "USD"},
		"another currency":   {"25", "25", "NGN"},
		"an unreadable one":  {`"25.555"`, "25", "USD"},
		"minor units by bug": {"2500", "2500", "USD"},
	} {
		t.Run(name, func(t *testing.T) {
			app, s := newApp(t, testKey)
			result := place(t, app)
			ref := result.Payment.Reference
			s.tx(77, ref, "successful", a.amount, a.charged, a.currency)
			post(t, app, completed(77, ref, "successful"))
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

// With the hash in hand, somebody names a real transaction of another shopper's
// against this order's tx_ref. It was not made for this order, so it pays nothing.
func TestAnotherTransactionCannotPayThisOrder(t *testing.T) {
	app, s := newApp(t, testKey)
	result := place(t, app)
	s.tx(99, "someone-elses", "successful", "25", "25", "USD")
	post(t, app, completed(99, result.Payment.Reference, "successful"))
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPending {
		t.Errorf("payment status = %q, want the order untouched", order.PaymentStatus)
	}
}

// A body saying "successful" counts for nothing against what Flutterwave says.
func TestDeliveryFlutterwaveDoesNotBearOut(t *testing.T) {
	app, s := newApp(t, testKey)
	result := place(t, app)
	ref := result.Payment.Reference
	s.tx(77, ref, "pending", "25", "25", "USD")
	body := completed(77, ref, "successful")
	if rec := deliver(app, body, testHash); rec.Code != http.StatusBadGateway {
		t.Errorf("webhook = %d, want 502 so Flutterwave retries", rec.Code)
	}
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPending {
		t.Fatalf("payment status = %q", order.PaymentStatus)
	}
	s.tx(77, ref, "successful", "25", "25", "USD")
	post(t, app, body)
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPaid {
		t.Errorf("after the retry: payment status = %q, want paid", order.PaymentStatus)
	}
}

func TestFlutterwaveDownThenRetried(t *testing.T) {
	app, s := newApp(t, testKey)
	result := place(t, app)
	ref := result.Payment.Reference
	s.tx(77, ref, "successful", "25", "25", "USD")
	s.mu.Lock()
	s.down = true
	s.mu.Unlock()
	body := completed(77, ref, "successful")
	if rec := deliver(app, body, testHash); rec.Code != http.StatusBadGateway {
		t.Errorf("webhook = %d, want 502", rec.Code)
	}
	s.mu.Lock()
	s.down = false
	s.mu.Unlock()
	post(t, app, body)
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPaid {
		t.Errorf("payment status = %q, want paid", order.PaymentStatus)
	}
}

func TestDuplicateWebhookIsHarmless(t *testing.T) {
	app, s := newApp(t, testKey)
	result := place(t, app)
	ref := result.Payment.Reference
	s.tx(77, ref, "successful", "25", "25", "USD")
	for range 3 {
		post(t, app, completed(77, ref, "successful"))
	}
	if n := paidEvents(t, app, result.Order.ID); n != 1 {
		t.Errorf("order.paid events = %d, want 1", n)
	}
	if n := s.verifies.Load(); n != 1 {
		t.Errorf("Flutterwave asked %d times, want 1", n)
	}
}

// A declined attempt leaves the order payable, a success settles it, and the
// failure redelivered after that changes nothing.
func TestFailureThenSuccessThenLateFailure(t *testing.T) {
	app, s := newApp(t, testKey)
	result := place(t, app)
	ref := result.Payment.Reference
	s.tx(76, ref, "failed", "25", "25", "USD")
	post(t, app, completed(76, ref, "failed"))
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentFailed || order.Status != gocommerce.OrderPending {
		t.Errorf("after a failure: payment %q, order %q", order.PaymentStatus, order.Status)
	}
	s.tx(77, ref, "successful", "25", "25", "USD")
	post(t, app, completed(77, ref, "successful"))
	s.tx(78, ref, "failed", "25", "25", "USD")
	post(t, app, completed(78, ref, "failed"))
	if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPaid || order.PaymentReference != "77" {
		t.Errorf("payment %q by %q; want still paid by 77", order.PaymentStatus, order.PaymentReference)
	}
}

func TestWebhookForSomethingElseIsLeftAlone(t *testing.T) {
	app, s := newApp(t, testKey)
	post(t, app, completed(55, "SOMEONE-ELSES", "successful"))
	post(t, app, `{"event":"transfer.completed","data":{"id":5}}`)
	if s.verifies.Load() != 0 {
		t.Error("Flutterwave was asked about something that is not this store's")
	}
}

func TestPaymentForACancelledOrder(t *testing.T) {
	app, s := newApp(t, testKey)
	result := place(t, app)
	ref := result.Payment.Reference
	if _, err := app.Order().Cancel(context.Background(), result.Order.ID, "test"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	s.tx(77, ref, "successful", "25", "25", "USD")
	post(t, app, completed(77, ref, "successful"))
	order := getOrder(t, app, result.Order.ID)
	if order.Status != gocommerce.OrderCancelled || order.PaymentStatus == gocommerce.PaymentPaid {
		t.Errorf("order %q, payment %q; want it cancelled and unpaid", order.Status, order.PaymentStatus)
	}
}

// ----------------------------------------------------------------- the keys

// The key says the mode, and the checkout says it to the storefront. A key of one
// mode cannot see the other's transactions, which is what stops a test payment
// settling a live store: the delivery is refused for good, not retried.
func TestTestAndLiveKeys(t *testing.T) {
	for key, want := range map[string]string{testKey: "test", liveKey: "live"} {
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
		s.tx(77, result.Payment.Reference, "successful", "25", "25", "USD")
		post(t, app, completed(77, result.Payment.Reference, "successful"))
		if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPaid {
			t.Errorf("payment status = %q, want paid", order.PaymentStatus)
		}
	})

	t.Run("a live store does not settle on a test transaction", func(t *testing.T) {
		app, _ := newApp(t, liveKey)
		result := place(t, app)
		body := completed(77, result.Payment.Reference, "successful")
		if rec := deliver(app, body, testHash); rec.Code != http.StatusBadRequest {
			t.Errorf("webhook = %d, want 400", rec.Code)
		}
		if order := getOrder(t, app, result.Order.ID); order.PaymentStatus != gocommerce.PaymentPending {
			t.Errorf("payment status = %q, want the order untouched", order.PaymentStatus)
		}
	})

	t.Run("the public key is not a secret key", func(t *testing.T) {
		app := gctest.NewWithConfig(t, gocommerce.Config{Currency: "USD"}, New(Config{SecretKey: "FLWPUBK_TEST-x", SecretHash: testHash}))
		for _, code := range app.Pay().Methods() {
			if code == "flutterwave" {
				t.Error("offered with only a public key")
			}
		}
	})

	t.Run("no secret hash, no method", func(t *testing.T) {
		app := gctest.NewWithConfig(t, gocommerce.Config{Currency: "USD"}, New(Config{SecretKey: testKey}))
		for _, code := range app.Pay().Methods() {
			if code == "flutterwave" {
				t.Error("offered with no way to tell a real delivery")
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

	check, err := app.Pay().CheckPayment(ctx, result.Order.ID)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if check.Gateway.Paid || check.Gateway.Failed || !strings.Contains(check.Gateway.Detail, "no payment") {
		t.Errorf("before any attempt: %+v", check.Gateway)
	}

	s.tx(76, ref, "failed", "25", "25", "USD")
	check, err = app.Pay().CheckPayment(ctx, result.Order.ID)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !check.Gateway.Failed || check.Recorded || check.Order.PaymentStatus != gocommerce.PaymentPending {
		t.Errorf("after a decline: %+v, payment %q", check.Gateway, check.Order.PaymentStatus)
	}

	s.tx(77, ref, "successful", "25", "25", "USD")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x/payments-flutterwave/transactions/"+ref+"/verify", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"payment_status":"paid"`) {
		t.Fatalf("verify = %d: %s", rec.Code, rec.Body)
	}
	if got := getOrder(t, app, result.Order.ID).PaymentReference; got != "77" {
		t.Errorf("reference = %q, want the transaction id", got)
	}
	asked := s.verifies.Load()
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x/payments-flutterwave/transactions/"+ref+"/verify", nil))
	if s.verifies.Load() != asked {
		t.Error("a settled order went back to Flutterwave")
	}
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x/payments-flutterwave/transactions/nobody/verify", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown tx_ref = %d, want 404", rec.Code)
	}
}

// ----------------------------------------------------------------- refunds

func TestRefund(t *testing.T) {
	app, s := newApp(t, testKey)
	ctx := context.Background()
	result := place(t, app)
	ref := result.Payment.Reference
	s.tx(77, ref, "successful", "25", "25", "USD")
	post(t, app, completed(77, ref, "successful"))

	order, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{AmountMinor: 1050}, nil)
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if order.Refunded.AmountMinor != 1050 || order.PaymentStatus != gocommerce.PaymentPaid {
		t.Errorf("refunded %d, payment %q", order.Refunded.AmountMinor, order.PaymentStatus)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.refunds) != 1 || s.refundTo[0] != "77" || fmt.Sprint(s.refunds[0]["amount"]) != "10.5" {
		t.Errorf("refunds sent = %v to %v", s.refunds, s.refundTo)
	}
}

// A reference typed by hand, even a real transaction id, is read back first and
// names nothing of this order's, so no money moves.
func TestRefundOfAHandTypedReference(t *testing.T) {
	for name, typed := range map[string]string{"not an id": "cash-1", "another shopper's": "99"} {
		t.Run(name, func(t *testing.T) {
			app, s := newApp(t, testKey)
			ctx := context.Background()
			result := place(t, app)
			s.tx(99, "someone-elses", "successful", "25", "25", "USD")
			if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, typed); err != nil {
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
		})
	}
}

// ------------------------------------------------------------------ resume

func TestRetriedCheckout(t *testing.T) {
	retry := func(t *testing.T, status string) (*gocommerce.App, *stub, *gocommerce.CheckoutResult, *gocommerce.CheckoutResult) {
		t.Helper()
		app, s := newApp(t, testKey)
		ctx := context.Background()
		in := checkoutInput(t, app, "GCTEST-retry")
		first, err := app.Order().Checkout(ctx, "flutterwave", in, "key-1")
		if err != nil {
			t.Fatalf("first checkout: %v", err)
		}
		if status != "" {
			s.tx(77, first.Payment.Reference, status, "25", "25", "USD")
		}
		again, err := app.Order().Checkout(ctx, "flutterwave", in, "key-1")
		if err != nil {
			t.Fatalf("retry: %v", err)
		}
		return app, s, first, again
	}
	count := func(s *stub) int {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.created)
	}

	for _, status := range []string{"", "pending"} {
		_, s, first, again := retry(t, status)
		if again.Payment.Reference != first.Payment.Reference || count(s) != 1 {
			t.Errorf("status %q: retry opened %q after %q, %d checkouts; want the same one", status, again.Payment.Reference, first.Payment.Reference, count(s))
		}
	}

	_, s, first, again := retry(t, "failed")
	if again.Payment.Reference == first.Payment.Reference || count(s) != 2 {
		t.Errorf("after a failure: retry reopened %q, %d checkouts; want a new one", again.Payment.Reference, count(s))
	}

	app, s, first, again := retry(t, "successful")
	if again.Order.PaymentStatus != gocommerce.PaymentPaid || count(s) != 1 {
		t.Errorf("payment %q, %d checkouts; want paid on the first", again.Order.PaymentStatus, count(s))
	}
	if got := getOrder(t, app, first.Order.ID).PaymentReference; got != "77" {
		t.Errorf("reference = %q, want the transaction id", got)
	}
}

// -------------------------------------------------------------- the install

// Installed idle, switched on from the panel, and neither secret is handed back.
func TestConfiguredFromThePanel(t *testing.T) {
	_, server := newStub(t, testKey)
	app := gctest.NewWithConfig(t, gocommerce.Config{AllowPrivateURLs: true, Currency: "NGN"}, New(Config{BaseURL: server.URL}))
	offered := func() bool {
		for _, code := range app.Pay().Methods() {
			if code == "flutterwave" {
				return true
			}
		}
		return false
	}
	if offered() {
		t.Fatal("offered with no key")
	}
	on := true
	plugin, err := app.Plugins().Update(context.Background(), PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{"secret_key": testKey, "secret_hash": testHash}})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if !offered() {
		t.Error("configured from the panel, the gateway should be offered")
	}
	if fmt.Sprint(plugin.Settings["secret_key"]) == testKey || fmt.Sprint(plugin.Settings["secret_hash"]) == testHash {
		t.Error("the settings read handed a secret back")
	}
}

// The module's own client is the install's guarded one (D109).
func TestAPrivateAddressIsNotCalled(t *testing.T) {
	s, server := newStub(t, testKey)
	mod := New(Config{SecretKey: testKey, SecretHash: testHash, BaseURL: server.URL})
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
