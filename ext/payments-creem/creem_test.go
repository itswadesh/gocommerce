package creem

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
	"strings"
	"testing"

	gocommerce "github.com/misiki/gocommerce/core"
	"github.com/misiki/gocommerce/gctest"
)

const testSecret = "whsec_creem_test"

// The one piece of this module standing between the internet and marking
// orders paid.
//
// Creem signs the raw body with HMAC-SHA256 and nothing else — no timestamp,
// exactly like Lemon Squeezy. A captured delivery therefore stays valid for
// that body for ever, which is what makes the idempotency claim in the handler
// load-bearing rather than tidy.
func TestVerifySignature(t *testing.T) {
	t.Parallel()

	body := []byte(`{"eventType":"checkout.completed"}`)

	for _, tc := range []struct {
		name    string
		header  string
		body    []byte
		wantErr bool
	}{
		{"a good signature passes", sign(string(body), testSecret), body, false},
		{"no header is refused", "", body, true},
		{"junk is refused", "not-a-signature", body, true},
		{"the wrong secret is refused", sign(string(body), "nope"), body, true},
		{
			// The signature covers the body, so a body swapped after signing
			// must fail — the attack the HMAC exists for.
			"a tampered body is refused",
			sign(string(body), testSecret),
			[]byte(`{"eventType":"checkout.completed","extra":true}`),
			true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifySignature(tc.body, tc.header, testSecret)
			if tc.wantErr && err == nil {
				t.Error("expected an error, got none")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// Creem refuses a custom price outside 100..99,999,999 cents. Caught here so
// the operator reads what is wrong with the order rather than a 400 from a
// gateway naming a field they never filled in.
func TestTheAmountCreemWillAcceptIsCheckedFirst(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		minor   int64
		wantErr bool
	}{
		{99, true},        // under the floor
		{100, false},      // exactly the floor
		{1500, false},     // ordinary
		{99999999, false}, // exactly the ceiling
		{100000000, true}, // over it
		{0, true},
		{-1, true},
	} {
		err := checkAmount(tc.minor)
		if tc.wantErr && err == nil {
			t.Errorf("%d cents was accepted", tc.minor)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%d cents was refused: %v", tc.minor, err)
		}
	}
}

// stubCreem stands in for the API.
func stubCreem(t *testing.T) *httptest.Server {
	t.Helper()
	return gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/checkouts":
			// The key travels in a header of Creem's own, not as a bearer
			// token; a module sending Authorization would be refused.
			if got := r.Header.Get("x-api-key"); got != "creem_test" {
				http.Error(w, `{"message":"bad key"}`, http.StatusUnauthorized)
				return
			}
			body, _ := io.ReadAll(r.Body)
			var in struct {
				ProductID   string            `json:"product_id"`
				CustomPrice int64             `json:"custom_price"`
				RequestID   string            `json:"request_id"`
				Metadata    map[string]string `json:"metadata"`
				Customer    struct {
					Email string `json:"email"`
				} `json:"customer"`
			}
			_ = json.Unmarshal(body, &in)
			// Echoed back so the test can prove the order's own total, in minor
			// units, is what reached the gateway — and which product it was
			// created against.
			fmt.Fprintf(w,
				`{"id":"ch_test_1","status":"pending","checkout_url":"https://checkout.creem.test/ch_test_1?p=%d&prod=%s&req=%s"}`,
				in.CustomPrice, in.ProductID, in.RequestID)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/refunds":
			body, _ := io.ReadAll(r.Body)
			var in struct {
				TransactionID string `json:"transaction_id"`
			}
			_ = json.Unmarshal(body, &in)
			// Creem refunds a transaction, so a module sending an order id
			// would get a 404 at the first real refund. Echoed back so the
			// test can prove which id was sent.
			if !strings.HasPrefix(in.TransactionID, "tran_") {
				http.Error(w, `{"message":"transaction not found"}`, http.StatusNotFound)
				return
			}
			fmt.Fprintf(w, `{"id":"ref_for_%s","status":"pending"}`, in.TransactionID)
		default:
			http.NotFound(w, r)
		}
	})
}

func newApp(t *testing.T) *gocommerce.App {
	t.Helper()
	server := stubCreem(t)
	return gctest.New(t, New(Config{
		APIKey:        "creem_test",
		WebhookSecret: testSecret,
		ProductID:     "prod_placeholder",
		BaseURL:       server.URL,
	}))
}

// The module's reason to exist, end to end: a hosted checkout that settles when
// Creem says the money arrived.
func TestCheckoutAndWebhook(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, "creem")
	if result.Payment.Kind != gocommerce.IntentRedirect {
		t.Fatalf("payment kind = %q, want redirect", result.Payment.Kind)
	}
	url := result.Payment.ClientData["url"]
	// The order total in minor units is what was sent, not a converted float.
	if want := fmt.Sprintf("p=%d", result.Order.Total.AmountMinor); !strings.Contains(url, want) {
		t.Errorf("checkout url = %q, want it to carry %s", url, want)
	}
	if !strings.Contains(url, "prod=prod_placeholder") {
		t.Errorf("checkout url = %q, want it created against the placeholder product", url)
	}
	// request_id carries the order, so a Creem dashboard row can be tied back
	// to this store without opening the metadata.
	if want := fmt.Sprintf("req=%d", result.Order.ID); !strings.Contains(url, want) {
		t.Errorf("checkout url = %q, want it to carry %s", url, want)
	}
	if result.Order.Status != gocommerce.OrderPending {
		t.Errorf("status = %q, want pending until Creem confirms", result.Order.Status)
	}

	body := completed(result.Order.ID, "evt_1", "ord_test_9", "paid")
	if rec := postWebhook(t, app, body, sign(body, testSecret)); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}

	order, err := app.Order().Get(ctx, result.Order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if order.PaymentStatus != gocommerce.PaymentPaid {
		t.Errorf("payment status = %q, want paid", order.PaymentStatus)
	}
	if order.Status != gocommerce.OrderConfirmed {
		t.Errorf("status = %q, want confirmed", order.Status)
	}
	// Creem's ORDER id from the webhook, not the checkout id Initiate returned:
	// it is what a refund in Creem's dashboard is issued against, so it is what
	// somebody reconciling the two needs.
	if order.PaymentReference != "ord_test_9" {
		t.Errorf("payment reference = %q, want the order id from the webhook", order.PaymentReference)
	}
}

// A checkout.completed whose order has not settled must not mark ours paid.
func TestAnUnsettledCheckoutDoesNotSettle(t *testing.T) {
	app := newApp(t)
	result := gctest.PlaceOrder(t, app, "creem")

	body := completed(result.Order.ID, "evt_2", "ord_pending", "pending")
	if rec := postWebhook(t, app, body, sign(body, testSecret)); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}

	order, err := app.Order().Get(context.Background(), result.Order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if order.PaymentStatus != gocommerce.PaymentPending {
		t.Errorf("payment status = %q — an unsettled checkout marked it paid", order.PaymentStatus)
	}
}

// Creem retries, and with no timestamp in the signature a captured delivery
// stays valid for ever — so this claim is the only thing stopping a replay
// settling the order twice.
func TestWebhookReplayIsIdempotent(t *testing.T) {
	app := newApp(t)
	result := gctest.PlaceOrder(t, app, "creem")

	body := completed(result.Order.ID, "evt_dup", "ord_dup", "paid")
	signature := sign(body, testSecret)

	for i := range 2 {
		if rec := postWebhook(t, app, body, signature); rec.Code != http.StatusOK {
			t.Fatalf("delivery %d = %d: %s", i+1, rec.Code, rec.Body)
		}
	}

	var claims int
	if err := app.DB().QueryRow(
		`SELECT count(*) FROM payments_creem_events WHERE id = 'evt_dup'`,
	).Scan(&claims); err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if claims != 1 {
		t.Errorf("event rows = %d, want exactly 1", claims)
	}
}

func TestWebhookRejectsBadSignature(t *testing.T) {
	app := newApp(t)
	result := gctest.PlaceOrder(t, app, "creem")

	body := completed(result.Order.ID, "evt_forged", "ord_forged", "paid")
	for _, header := range []string{"", "deadbeef", sign(body, "the-wrong-secret")} {
		if rec := postWebhook(t, app, body, header); rec.Code != http.StatusBadRequest {
			t.Errorf("status for header %q = %d, want 400", header, rec.Code)
		}
	}

	order, err := app.Order().Get(context.Background(), result.Order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if order.PaymentStatus != gocommerce.PaymentPending {
		t.Errorf("payment status = %q — a forged webhook changed the order", order.PaymentStatus)
	}
}

// An event about somebody else's sale — a subscription renewal, a payment link
// used outside this store — is acknowledged rather than retried forever.
func TestAnEventForNoOrderOfOursIsAcknowledged(t *testing.T) {
	app := newApp(t)
	_ = gctest.PlaceOrder(t, app, "creem")

	body := `{"id":"evt_foreign","eventType":"checkout.completed","object":{"id":"ch_x","order":{"id":"ord_x","status":"paid"},"metadata":{}}}`
	if rec := postWebhook(t, app, body, sign(body, testSecret)); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}
}

// A full refund goes through, against the transaction id the webhook carried
// — not the order id, which is what Creem's refund endpoint would refuse.
func TestFullRefund(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, "creem")
	// The checkout.completed event is what supplies the transaction id, so the
	// refund path only exists after it has arrived. Settling through the
	// webhook rather than calling MarkPaid directly is the point.
	body := completedWithTransaction(result.Order.ID, "evt_paid", "ord_test_9", "tran_abc", "paid")
	if rec := postWebhook(t, app, body, sign(body, testSecret)); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}

	order, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{}, nil)
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if order.PaymentStatus != gocommerce.PaymentRefunded {
		t.Errorf("payment status = %q, want refunded", order.PaymentStatus)
	}
	if len(order.Refunds) != 1 || order.Refunds[0].ProviderReference != "ref_for_tran_abc" {
		t.Errorf("refunds = %+v, want one carrying the id Creem answered with", order.Refunds)
	}
}

// Creem takes no amount and refunds "the full remaining refundable amount", so
// a partial request must be refused rather than sent. Sending it would return
// the whole order to the shopper while this store's books recorded a fraction,
// which is the most expensive way this module could be wrong.
func TestAPartialRefundIsRefusedRatherThanSent(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, "creem")
	body := completedWithTransaction(result.Order.ID, "evt_part", "ord_part", "tran_part", "paid")
	if rec := postWebhook(t, app, body, sign(body, testSecret)); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}

	part := result.Order.Total.AmountMinor / 2
	_, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{AmountMinor: part}, nil)
	if err == nil {
		t.Fatal("a partial refund was accepted; Creem would have returned the whole order")
	}
	if !strings.Contains(err.Error(), "whole order") {
		t.Errorf("error = %v, want it to say why", err)
	}

	// And nothing was recorded as having moved.
	order, err := app.Order().Get(ctx, result.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if order.PaymentStatus == gocommerce.PaymentRefunded {
		t.Error("the order was marked refunded by a refusal")
	}
}

// A refund before the checkout.completed event has nothing to refund against,
// and says so rather than posting an order id to an endpoint expecting a
// transaction.
func TestARefundBeforeTheWebhookIsRefused(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, "creem")
	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "ord_test_9"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	_, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{}, nil)
	if err == nil {
		t.Fatal("a refund with no transaction recorded was attempted")
	}
	if !strings.Contains(err.Error(), "transaction") {
		t.Errorf("error = %v, want it to name what is missing", err)
	}
}

// A refund made in Creem's dashboard reaches the store as an event, so the
// operator is not the last to know.
func TestARefundEventIsAccepted(t *testing.T) {
	app := newApp(t)
	result := gctest.PlaceOrder(t, app, "creem")

	body := fmt.Sprintf(
		`{"id":"evt_ref","eventType":"refund.created","object":{"id":"ref_1","refund_amount":500,"refund_currency":"EUR","status":"succeeded","order":{"id":"ord_test_9","status":"paid"},"metadata":{"order_id":"%d"}}}`,
		result.Order.ID)
	if rec := postWebhook(t, app, body, sign(body, testSecret)); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}
}

// ------------------------------------------------------------------ helpers

func completedWithTransaction(orderID int64, eventID, creemOrder, transaction, status string) string {
	return fmt.Sprintf(
		`{"id":"%s","eventType":"checkout.completed","object":{"id":"ch_test_1","status":"completed","order":{"id":"%s","amount":1500,"currency":"EUR","status":"%s","transaction":"%s"},"metadata":{"order_id":"%d"}}}`,
		eventID, creemOrder, status, transaction, orderID)
}

func completed(orderID int64, eventID, creemOrder, status string) string {
	return fmt.Sprintf(
		`{"id":"%s","eventType":"checkout.completed","object":{"id":"ch_test_1","status":"completed","order":{"id":"%s","amount":1500,"currency":"EUR","status":"%s"},"metadata":{"order_id":"%d"}}}`,
		eventID, creemOrder, status, orderID)
}

func sign(body, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func postWebhook(t *testing.T, app *gocommerce.App, body, signature string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/checkout/creem/webhook", strings.NewReader(body))
	if signature != "" {
		req.Header.Set("creem-signature", signature)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

// With nothing in Config the module still registers — the panel lists it — but
// the settings say it is not set up and nothing can be sent through it, until
// its fields are typed into the plugin.
func TestInstalledIdleUntilConfigured(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ctx := context.Background()

	var info *gocommerce.ProviderInfo
	for _, p := range app.Settings().PaymentMethods {
		if p.Code == "creem" {
			p := p
			info = &p
		}
	}
	if info == nil {
		t.Fatal("an idle module should still be listed in the settings")
	}
	if info.Configured {
		t.Fatalf("configured = %v before anything was typed in", info.Configured)
	}
	for _, code := range app.Pay().Methods() {
		if code == "creem" {
			t.Fatal("an idle gateway must not be offered at checkout")
		}
	}
	if _, err := app.Order().Checkout(ctx, "creem", gocommerce.CheckoutInput{}, ""); err == nil || !strings.Contains(err.Error(), "no payment method") {
		t.Fatalf("checkout through an idle gateway = %v, want it refused as unknown", err)
	}

	on := true
	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{
		Enabled: &on,
		Settings: map[string]any{
			"api_key": "k-1", "webhook_secret": "w-1", "product_id": "prod_1",
		},
	}); err != nil {
		t.Fatalf("configure from the panel: %v", err)
	}
	for _, p := range app.Settings().PaymentMethods {
		if p.Code == "creem" && !p.Configured {
			t.Fatal("configured from the panel, the settings should say so")
		}
	}
	offered := false
	for _, code := range app.Pay().Methods() {
		offered = offered || code == "creem"
	}
	if !offered {
		t.Fatal("configured from the panel, the gateway should be offered")
	}
}
