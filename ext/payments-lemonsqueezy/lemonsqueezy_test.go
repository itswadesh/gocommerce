package lemonsqueezy

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

const testSecret = "ls_whsec_test"

// TestVerifySignature is pure logic and needs no database: it is the one piece
// of this module standing between the internet and marking orders paid.
//
// Note what is missing compared with Stripe and Paddle — there is no timestamp
// case to test, because Lemon Squeezy does not sign one. That absence is the
// reason the idempotency claim in the handler carries the weight it does.
func TestVerifySignature(t *testing.T) {
	t.Parallel()

	body := []byte(`{"meta":{"event_name":"order_created"}}`)

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
			[]byte(`{"meta":{"event_name":"order_created"},"extra":true}`),
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

// stubLemonSqueezy stands in for the API.
func stubLemonSqueezy(t *testing.T) *httptest.Server {
	t.Helper()
	return gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", mediaType)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/checkouts":
			body, _ := io.ReadAll(r.Body)
			var in struct {
				Data struct {
					Attributes struct {
						CustomPrice int64 `json:"custom_price"`
					} `json:"attributes"`
				} `json:"data"`
			}
			_ = json.Unmarshal(body, &in)
			// Echo the price back so the test can prove the order total, in
			// minor units, is what reached the gateway.
			fmt.Fprintf(w, `{"data":{"id":"co_test_1","attributes":{"url":"https://pay.example.test/co_test_1?p=%d"}}}`,
				in.Data.Attributes.CustomPrice)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/refund"):
			fmt.Fprint(w, `{"data":{"id":"ord_test_9","attributes":{"status":"refunded"}}}`)
		default:
			http.NotFound(w, r)
		}
	})
}

func newApp(t *testing.T) *gocommerce.App {
	t.Helper()
	server := stubLemonSqueezy(t)
	return gctest.New(t, New(Config{
		APIKey:        "ls_test",
		WebhookSecret: testSecret,
		StoreID:       "1",
		VariantID:     "2",
		BaseURL:       server.URL,
	}))
}

// The module's reason to exist, end to end: a hosted checkout that settles when
// Lemon Squeezy says the money arrived.
func TestCheckoutAndWebhook(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, "lemonsqueezy")
	if result.Payment.Kind != gocommerce.IntentRedirect {
		t.Fatalf("payment kind = %q, want redirect", result.Payment.Kind)
	}
	// The order total in minor units is what was sent, not a converted float.
	want := fmt.Sprintf("p=%d", result.Order.Total.AmountMinor)
	if got := result.Payment.ClientData["url"]; !strings.Contains(got, want) {
		t.Errorf("checkout url = %q, want it to carry %s", got, want)
	}
	if result.Order.Status != gocommerce.OrderPending {
		t.Errorf("status = %q, want pending until Lemon Squeezy confirms", result.Order.Status)
	}

	body := fmt.Sprintf(
		`{"meta":{"event_name":"order_created","custom_data":{"order_id":"%d"}},"data":{"id":"ord_test_9","attributes":{"status":"paid"}}}`,
		result.Order.ID)
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
	// The reference stored is Lemon Squeezy's ORDER id from the webhook, not
	// the checkout id Initiate returned — a refund is issued against the former.
	if order.PaymentReference != "ord_test_9" {
		t.Errorf("payment reference = %q, want the order id from the webhook", order.PaymentReference)
	}
}

// An order_created that has not settled must not mark the order paid.
func TestUnpaidOrderCreatedDoesNotSettle(t *testing.T) {
	app := newApp(t)
	result := gctest.PlaceOrder(t, app, "lemonsqueezy")

	body := fmt.Sprintf(
		`{"meta":{"event_name":"order_created","custom_data":{"order_id":"%d"}},"data":{"id":"ord_pending","attributes":{"status":"pending"}}}`,
		result.Order.ID)
	if rec := postWebhook(t, app, body, sign(body, testSecret)); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}

	order, err := app.Order().Get(context.Background(), result.Order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if order.PaymentStatus != gocommerce.PaymentPending {
		t.Errorf("payment status = %q — an unsettled order_created marked it paid", order.PaymentStatus)
	}
}

// Lemon Squeezy retries, and with no timestamp in the signature a captured
// delivery stays valid for ever — so this claim is the only thing stopping a
// replay settling the order twice.
func TestWebhookReplayIsIdempotent(t *testing.T) {
	app := newApp(t)
	result := gctest.PlaceOrder(t, app, "lemonsqueezy")

	body := fmt.Sprintf(
		`{"meta":{"event_name":"order_created","custom_data":{"order_id":"%d"}},"data":{"id":"ord_dup","attributes":{"status":"paid"}}}`,
		result.Order.ID)
	signature := sign(body, testSecret)

	for i := range 2 {
		if rec := postWebhook(t, app, body, signature); rec.Code != http.StatusOK {
			t.Fatalf("delivery %d = %d: %s", i+1, rec.Code, rec.Body)
		}
	}

	var claims int
	if err := app.DB().QueryRow(
		`SELECT count(*) FROM payments_lemonsqueezy_events WHERE id = 'order_created:ord_dup'`,
	).Scan(&claims); err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if claims != 1 {
		t.Errorf("event rows = %d, want exactly 1", claims)
	}
}

func TestWebhookRejectsBadSignature(t *testing.T) {
	app := newApp(t)
	result := gctest.PlaceOrder(t, app, "lemonsqueezy")

	body := fmt.Sprintf(
		`{"meta":{"event_name":"order_created","custom_data":{"order_id":"%d"}},"data":{"id":"ord_forged","attributes":{"status":"paid"}}}`,
		result.Order.ID)

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

func TestRefund(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, "lemonsqueezy")
	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "ord_test_9"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	order, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{}, nil)
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if order.PaymentStatus != gocommerce.PaymentRefunded {
		t.Errorf("payment status = %q, want refunded", order.PaymentStatus)
	}
	if len(order.Refunds) != 1 || order.Refunds[0].ProviderReference != "ord_test_9" {
		t.Errorf("refunds = %+v, want one carrying the id Lemon Squeezy answered with", order.Refunds)
	}
}

// ------------------------------------------------------------------ helpers

func sign(body, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func postWebhook(t *testing.T, app *gocommerce.App, body, signature string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/checkout/lemonsqueezy/webhook", strings.NewReader(body))
	if signature != "" {
		req.Header.Set("X-Signature", signature)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

// TestInstalledIdleUntilConfigured: with nothing in Config the module still
// registers — the panel lists it — but the settings say it is not set up and
// nothing can be sent through it, until its fields are typed into the plugin.
func TestInstalledIdleUntilConfigured(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ctx := context.Background()

	var info *gocommerce.ProviderInfo
	for _, p := range app.Settings().PaymentMethods {
		if p.Code == "lemonsqueezy" {
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
		if code == "lemonsqueezy" {
			t.Fatal("an idle gateway must not be offered at checkout")
		}
	}
	if _, err := app.Order().Checkout(ctx, "lemonsqueezy", gocommerce.CheckoutInput{}, ""); err == nil || !strings.Contains(err.Error(), "no payment method") {
		t.Fatalf("checkout through an idle gateway = %v, want it refused as unknown", err)
	}

	on := true
	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{"api_key": "k-1", "webhook_secret": "w-1", "store_id": "1", "variant_id": "2"}}); err != nil {
		t.Fatalf("configure from the panel: %v", err)
	}
	for _, p := range app.Settings().PaymentMethods {
		if p.Code == "lemonsqueezy" && !p.Configured {
			t.Fatal("configured from the panel, the settings should say so")
		}
	}
	offered := false
	for _, code := range app.Pay().Methods() {
		offered = offered || code == "lemonsqueezy"
	}
	if !offered {
		t.Fatal("configured from the panel, the gateway should be offered")
	}
}
