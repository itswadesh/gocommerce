package hyperswitch

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
	"testing"

	gocommerce "github.com/misiki/gocommerce/core"
	"github.com/misiki/gocommerce/gctest"
)

const testHashKey = "hs_response_hash_key"

// TestVerifySignature is pure logic and needs no database: it is the one piece
// of this module standing between the internet and marking orders paid.
func TestVerifySignature(t *testing.T) {
	t.Parallel()

	body := []byte(`{"event_id":"evt_1","event_type":"payment_succeeded"}`)

	for _, tc := range []struct {
		name    string
		header  string
		body    []byte
		wantErr bool
	}{
		{"a good signature passes", sign(string(body), testHashKey), body, false},
		{
			// The digest is hex, so case carries no information; refusing an
			// upper-cased one would be an outage over nothing.
			"an upper-cased signature passes",
			strings.ToUpper(sign(string(body), testHashKey)), body, false,
		},
		{"no header is refused", "", body, true},
		{"junk is refused", "not-a-signature", body, true},
		{"the wrong key is refused", sign(string(body), "nope"), body, true},
		{
			// The signature covers the body, so a body swapped after signing
			// must fail — the attack the HMAC exists for.
			"a tampered body is refused",
			sign(string(body), testHashKey),
			[]byte(`{"event_id":"evt_1","event_type":"payment_succeeded","extra":true}`),
			true,
		},
		{
			// SHA-256 of the same body with the same key: the right shape, the
			// wrong algorithm, and it must not be accepted.
			"a shorter digest is refused",
			sign(string(body), testHashKey)[:64], body, true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifySignature(tc.body, tc.header, testHashKey)
			if tc.wantErr && err == nil {
				t.Error("expected an error, got none")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// stubHyperswitch stands in for the router.
func stubHyperswitch(t *testing.T) *httptest.Server {
	t.Helper()
	return gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/payments":
			body, _ := io.ReadAll(r.Body)
			var in struct {
				Amount      int64 `json:"amount"`
				PaymentLink bool  `json:"payment_link"`
			}
			_ = json.Unmarshal(body, &in)
			if !in.PaymentLink {
				// Without this the response carries no link and the module
				// would have nothing to redirect to.
				http.Error(w, `{"error":{"message":"payment_link not requested"}}`, http.StatusBadRequest)
				return
			}
			// Echo the amount back so the test can prove the order total, in
			// minor units, is what reached the router.
			fmt.Fprintf(w, `{"payment_id":"pay_test_1","status":"requires_payment_method",
				"client_secret":"pay_test_1_secret_abc",
				"payment_link":{"link":"https://pay.example.test/pl_1?a=%d","payment_link_id":"pl_1"}}`,
				in.Amount)
		case r.Method == http.MethodPost && r.URL.Path == "/refunds":
			fmt.Fprint(w, `{"refund_id":"ref_test_9","status":"succeeded"}`)
		default:
			http.NotFound(w, r)
		}
	})
}

func newApp(t *testing.T) *gocommerce.App {
	t.Helper()
	server := stubHyperswitch(t)
	return gctest.New(t, New(Config{
		APIKey:          "hs_test",
		ResponseHashKey: testHashKey,
		ReturnURL:       "https://shop.example.test/thanks",
		BaseURL:         server.URL,
	}))
}

// The module's reason to exist, end to end: a hosted link that settles when
// Hyperswitch says the money arrived.
func TestCheckoutAndWebhook(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, "hyperswitch")
	if result.Payment.Kind != gocommerce.IntentRedirect {
		t.Fatalf("payment kind = %q, want redirect", result.Payment.Kind)
	}
	// The order total in minor units is what was sent, not a converted float.
	want := fmt.Sprintf("a=%d", result.Order.Total.AmountMinor)
	if got := result.Payment.ClientData["url"]; !strings.Contains(got, want) {
		t.Errorf("link = %q, want it to carry %s", got, want)
	}
	if result.Payment.ClientData["client_secret"] == "" {
		t.Error("no client_secret carried — a storefront wanting the SDK has nothing to use")
	}
	if result.Order.Status != gocommerce.OrderPending {
		t.Errorf("status = %q, want pending until Hyperswitch confirms", result.Order.Status)
	}

	body := event("evt_1", "payment_succeeded", "pay_test_1", "succeeded", result.Order.ID)
	if rec := postWebhook(t, app, body, sign(body, testHashKey)); rec.Code != http.StatusOK {
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
	if order.PaymentReference != "pay_test_1" {
		t.Errorf("payment reference = %q, want the payment id", order.PaymentReference)
	}
}

// A failed payment must leave the order unpaid and say why.
func TestFailedPaymentDoesNotSettle(t *testing.T) {
	app := newApp(t)
	result := gctest.PlaceOrder(t, app, "hyperswitch")

	body := event("evt_fail", "payment_failed", "pay_test_1", "failed", result.Order.ID)
	if rec := postWebhook(t, app, body, sign(body, testHashKey)); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}

	order, err := app.Order().Get(context.Background(), result.Order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if order.PaymentStatus == gocommerce.PaymentPaid {
		t.Error("a failed payment marked the order paid")
	}
}

// Hyperswitch retries with the same event_id, and with no timestamp in the
// signature a captured delivery stays valid for ever — so this claim is the
// only thing stopping a replay settling the order twice.
func TestWebhookReplayIsIdempotent(t *testing.T) {
	app := newApp(t)
	result := gctest.PlaceOrder(t, app, "hyperswitch")

	body := event("evt_dup", "payment_succeeded", "pay_dup", "succeeded", result.Order.ID)
	signature := sign(body, testHashKey)

	for i := range 2 {
		if rec := postWebhook(t, app, body, signature); rec.Code != http.StatusOK {
			t.Fatalf("delivery %d = %d: %s", i+1, rec.Code, rec.Body)
		}
	}

	var claims int
	if err := app.DB().QueryRow(
		`SELECT count(*) FROM payments_hyperswitch_events WHERE id = 'evt_dup'`,
	).Scan(&claims); err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if claims != 1 {
		t.Errorf("event rows = %d, want exactly 1", claims)
	}
}

func TestWebhookRejectsBadSignature(t *testing.T) {
	app := newApp(t)
	result := gctest.PlaceOrder(t, app, "hyperswitch")

	body := event("evt_forged", "payment_succeeded", "pay_forged", "succeeded", result.Order.ID)

	for _, header := range []string{"", "deadbeef", sign(body, "the-wrong-key")} {
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

	result := gctest.PlaceOrder(t, app, "hyperswitch")
	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "pay_test_1"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	order, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{}, nil)
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if order.PaymentStatus != gocommerce.PaymentRefunded {
		t.Errorf("payment status = %q, want refunded", order.PaymentStatus)
	}
	if len(order.Refunds) != 1 || order.Refunds[0].ProviderReference != "ref_test_9" {
		t.Errorf("refunds = %+v, want one carrying the id Hyperswitch answered with", order.Refunds)
	}
}

// A store with no return URL anywhere would get a 400 from Hyperswitch at the
// first real checkout; this says so before the network call.
func TestInitiateNeedsAReturnURL(t *testing.T) {
	server := stubHyperswitch(t)
	app := gctest.New(t, New(Config{
		APIKey:          "hs_test",
		ResponseHashKey: testHashKey,
		BaseURL:         server.URL,
	}))
	if _, err := app.Order().Checkout(context.Background(), "hyperswitch", gocommerce.CheckoutInput{}, ""); err == nil {
		t.Error("checkout succeeded with no return URL configured")
	}
}

// ------------------------------------------------------------------ helpers

func sign(body, key string) string {
	mac := hmac.New(sha512.New, []byte(key))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func event(id, kind, paymentID, status string, orderID int64) string {
	return fmt.Sprintf(
		`{"event_id":"%s","event_type":"%s","content":{"type":"payment_details","object":{"payment_id":"%s","status":"%s","amount":2500,"currency":"USD","metadata":{"order_id":"%d"}}}}`,
		id, kind, paymentID, status, orderID)
}

func postWebhook(t *testing.T, app *gocommerce.App, body, signature string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/checkout/hyperswitch/webhook", strings.NewReader(body))
	if signature != "" {
		req.Header.Set(signatureHeader, signature)
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
		if p.Code == "hyperswitch" {
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
		if code == "hyperswitch" {
			t.Fatal("an idle gateway must not be offered at checkout")
		}
	}
	if _, err := app.Order().Checkout(ctx, "hyperswitch", gocommerce.CheckoutInput{}, ""); err == nil || !strings.Contains(err.Error(), "no payment method") {
		t.Fatalf("checkout through an idle gateway = %v, want it refused as unknown", err)
	}

	on := true
	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{"api_key": "k-1", "response_hash_key": "h-1"}}); err != nil {
		t.Fatalf("configure from the panel: %v", err)
	}
	for _, p := range app.Settings().PaymentMethods {
		if p.Code == "hyperswitch" && !p.Configured {
			t.Fatal("configured from the panel, the settings should say so")
		}
	}
	offered := false
	for _, code := range app.Pay().Methods() {
		offered = offered || code == "hyperswitch"
	}
	if !offered {
		t.Fatal("configured from the panel, the gateway should be offered")
	}
}
