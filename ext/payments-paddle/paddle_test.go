package paddle

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
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

const testSecret = "pdl_ntfset_test_secret"

// TestVerifySignature is pure logic and needs no database: it is the one piece
// of this module that stands between the internet and marking orders paid.
func TestVerifySignature(t *testing.T) {
	t.Parallel()

	body := []byte(`{"event_id":"evt_1","event_type":"transaction.completed"}`)
	now := time.Now()

	tests := []struct {
		name    string
		header  string
		body    []byte
		wantErr bool
	}{
		{"a good signature passes", sign(string(body), testSecret, now), body, false},
		{"no header is refused", "", body, true},
		{"junk is refused", "not-a-signature", body, true},
		{"a missing h1 is refused", "ts=" + strconv.FormatInt(now.Unix(), 10), body, true},
		{"a missing ts is refused", "h1=deadbeef", body, true},
		{"the wrong secret is refused", sign(string(body), "not-the-secret", now), body, true},
		{
			// The signature covers the body, so a body swapped after signing
			// must fail — this is the attack the HMAC exists for.
			"a tampered body is refused",
			sign(string(body), testSecret, now),
			[]byte(`{"event_id":"evt_1","event_type":"transaction.completed","extra":true}`),
			true,
		},
		{
			"an old payload is refused as a replay",
			sign(string(body), testSecret, now.Add(-30*time.Minute)),
			body, true,
		},
		{
			// A timestamp from the future is what a clock set wrong looks like,
			// and it is as suspect as an old one.
			"a payload from the future is refused",
			sign(string(body), testSecret, now.Add(30*time.Minute)),
			body, true,
		},
		{
			// Paddle sends several h1 values while a secret is being rotated.
			"any one of several h1 values passing is a pass",
			sign(string(body), testSecret, now) + ";h1=deadbeef",
			body, false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := verifySignature(tc.body, tc.header, testSecret, now, defaultTolerance)
			if tc.wantErr && err == nil {
				t.Error("expected an error, got none")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// stubPaddle stands in for Paddle's API.
func stubPaddle(t *testing.T) *httptest.Server {
	t.Helper()
	return gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/transactions":
			body, _ := io.ReadAll(r.Body)
			var in struct {
				CurrencyCode string `json:"currency_code"`
				Items        []struct {
					Quantity int `json:"quantity"`
					Price    struct {
						UnitPrice struct {
							Amount string `json:"amount"`
						} `json:"unit_price"`
					} `json:"price"`
				} `json:"items"`
			}
			_ = json.Unmarshal(body, &in)
			amount := ""
			if len(in.Items) > 0 {
				amount = in.Items[0].Price.UnitPrice.Amount
			}
			fmt.Fprintf(w, `{"data":{"id":"txn_test_123","status":"ready",
				"checkout":{"url":"https://pay.example.test/?_ptxn=txn_test_123"},
				"details":{"totals":{"total":"%s"}}}}`, amount)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/transactions/"):
			fmt.Fprint(w, `{"data":{"id":"txn_test_123",
				"details":{"line_items":[{"id":"txnitm_test_1"}]}}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/adjustments":
			fmt.Fprint(w, `{"data":{"id":"adj_test_1","status":"approved"}}`)
		default:
			http.NotFound(w, r)
		}
	})
}

func newApp(t *testing.T) (*gocommerce.App, *Module) {
	t.Helper()
	server := stubPaddle(t)
	mod := New(Config{
		APIKey:        "pdl_test_123",
		WebhookSecret: testSecret,
		BaseURL:       server.URL,
	})
	return gctest.New(t, mod), mod
}

// TestCheckoutAndWebhook is the module's reason to exist, end to end: a hosted
// checkout that settles when Paddle says the money arrived.
func TestCheckoutAndWebhook(t *testing.T) {
	app, _ := newApp(t)
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, "paddle")
	// Redirect rather than client_action: Paddle hosts the payment page, so
	// there is nothing for the storefront to render in place.
	if result.Payment.Kind != gocommerce.IntentRedirect {
		t.Fatalf("payment kind = %q, want redirect", result.Payment.Kind)
	}
	if got := result.Payment.ClientData["url"]; !strings.Contains(got, "txn_test_123") {
		t.Errorf("checkout url = %q, want the one Paddle returned", got)
	}
	if result.Order.Status != gocommerce.OrderPending {
		t.Errorf("status = %q, want pending until Paddle confirms", result.Order.Status)
	}

	body := fmt.Sprintf(
		`{"event_id":"evt_1","event_type":"transaction.completed","data":{"id":"txn_test_123","status":"completed","custom_data":{"order_id":"%d"}}}`,
		result.Order.ID)
	rec := postWebhook(t, app, body, sign(body, testSecret, time.Now()))
	if rec.Code != http.StatusOK {
		t.Fatalf("webhook status = %d, want 200: %s", rec.Code, rec.Body)
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
	if order.PaymentReference != "txn_test_123" {
		t.Errorf("payment reference = %q, want txn_test_123", order.PaymentReference)
	}
}

// transaction.paid fires before Paddle has finished collecting and can still be
// reversed. Settling on it would mark orders paid that are not.
func TestPaidAloneDoesNotSettle(t *testing.T) {
	app, _ := newApp(t)
	result := gctest.PlaceOrder(t, app, "paddle")

	body := fmt.Sprintf(
		`{"event_id":"evt_paid","event_type":"transaction.paid","data":{"id":"txn_test_123","status":"paid","custom_data":{"order_id":"%d"}}}`,
		result.Order.ID)
	if rec := postWebhook(t, app, body, sign(body, testSecret, time.Now())); rec.Code != http.StatusOK {
		t.Fatalf("webhook status = %d, want 200: %s", rec.Code, rec.Body)
	}

	order, err := app.Order().Get(context.Background(), result.Order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if order.PaymentStatus != gocommerce.PaymentPending {
		t.Errorf("payment status = %q — transaction.paid settled an order it should not have",
			order.PaymentStatus)
	}
}

// TestWebhookReplayIsIdempotent: Paddle retries, and a retry must not settle
// the order twice or double-count the sale.
func TestWebhookReplayIsIdempotent(t *testing.T) {
	app, _ := newApp(t)
	result := gctest.PlaceOrder(t, app, "paddle")

	body := fmt.Sprintf(
		`{"event_id":"evt_dup","event_type":"transaction.completed","data":{"id":"txn_test_123","status":"completed","custom_data":{"order_id":"%d"}}}`,
		result.Order.ID)
	signature := sign(body, testSecret, time.Now())

	for i := range 2 {
		if rec := postWebhook(t, app, body, signature); rec.Code != http.StatusOK {
			t.Fatalf("delivery %d: status = %d, want 200: %s", i+1, rec.Code, rec.Body)
		}
	}

	var claims int
	if err := app.DB().QueryRow(
		`SELECT count(*) FROM payments_paddle_events WHERE id = 'evt_dup'`).Scan(&claims); err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if claims != 1 {
		t.Errorf("event rows = %d, want exactly 1", claims)
	}

	order, err := app.Order().Get(context.Background(), result.Order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if order.PaymentStatus != gocommerce.PaymentPaid {
		t.Errorf("payment status = %q, want paid", order.PaymentStatus)
	}
}

func TestWebhookRejectsBadSignature(t *testing.T) {
	app, _ := newApp(t)
	result := gctest.PlaceOrder(t, app, "paddle")

	body := fmt.Sprintf(
		`{"event_id":"evt_forged","event_type":"transaction.completed","data":{"id":"txn_x","custom_data":{"order_id":"%d"}}}`,
		result.Order.ID)

	for _, header := range []string{"", "ts=1;h1=deadbeef", sign(body, "the-wrong-secret", time.Now())} {
		rec := postWebhook(t, app, body, header)
		if rec.Code != http.StatusBadRequest {
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

// TestRefund proves the optional Refunder capability is wired up, and that the
// adjustment id comes back — the id somebody quotes reconciling a statement.
func TestRefund(t *testing.T) {
	app, _ := newApp(t)
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, "paddle")
	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "txn_test_123"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	order, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{}, nil)
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if order.PaymentStatus != gocommerce.PaymentRefunded {
		t.Errorf("payment status = %q, want refunded", order.PaymentStatus)
	}
	if len(order.Refunds) != 1 {
		t.Fatalf("refund records = %d, want 1", len(order.Refunds))
	}
	if order.Refunds[0].ProviderReference != "adj_test_1" {
		t.Errorf("provider_reference = %q, want the adjustment id Paddle answered with",
			order.Refunds[0].ProviderReference)
	}
}

// ------------------------------------------------------------------ helpers

func sign(body, secret string, at time.Time) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte(":"))
	mac.Write([]byte(body))
	return "ts=" + ts + ";h1=" + hex.EncodeToString(mac.Sum(nil))
}

func postWebhook(t *testing.T, app *gocommerce.App, body, signature string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/checkout/paddle/webhook", strings.NewReader(body))
	if signature != "" {
		req.Header.Set("Paddle-Signature", signature)
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
		if p.Code == "paddle" {
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
		if code == "paddle" {
			t.Fatal("an idle gateway must not be offered at checkout")
		}
	}
	if _, err := app.Order().Checkout(ctx, "paddle", gocommerce.CheckoutInput{}, ""); err == nil || !strings.Contains(err.Error(), "no payment method") {
		t.Fatalf("checkout through an idle gateway = %v, want it refused as unknown", err)
	}

	on := true
	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{"api_key": "k-1", "webhook_secret": "w-1"}}); err != nil {
		t.Fatalf("configure from the panel: %v", err)
	}
	for _, p := range app.Settings().PaymentMethods {
		if p.Code == "paddle" && !p.Configured {
			t.Fatal("configured from the panel, the settings should say so")
		}
	}
	offered := false
	for _, code := range app.Pay().Methods() {
		offered = offered || code == "paddle"
	}
	if !offered {
		t.Fatal("configured from the panel, the gateway should be offered")
	}
}
