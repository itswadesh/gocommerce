package revenuecat

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	gocommerce "github.com/misiki/gocommerce/core"
	"github.com/misiki/gocommerce/gctest"
)

const (
	testAuthorization = "Bearer gctest-revenuecat-header"
	testSigningSecret = "rc_signing_secret"
	testLinkToken     = "abcd1234"
)

// TestMinorUnits is why the exponent table exists: a hundredth is not the minor
// unit of every currency, and RevenueCat reports a float.
func TestMinorUnits(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		amount   float64
		currency string
		want     int64
	}{
		{9.99, "USD", 999},
		{25, "EUR", 2500},
		// A yen is its own minor unit: 1200 JPY is 1200, not 120000.
		{1200, "JPY", 1200},
		// A dinar has three.
		{9.995, "KWD", 9995},
		// Floats do not hold 9.99 exactly; rounding, not truncation, is what
		// keeps this from being 998.
		{0.1 + 9.89, "USD", 999},
	} {
		got, err := minorUnits(tc.amount, tc.currency)
		if err != nil {
			t.Errorf("minorUnits(%v, %q): %v", tc.amount, tc.currency, err)
			continue
		}
		if got != tc.want {
			t.Errorf("minorUnits(%v, %q) = %d, want %d", tc.amount, tc.currency, got, tc.want)
		}
	}
	if _, err := minorUnits(1, "not-a-currency"); err == nil {
		t.Error("minorUnits accepted a value that is not a currency code")
	}
}

func TestVerifySignature(t *testing.T) {
	t.Parallel()

	body := []byte(`{"event":{"id":"evt_1"}}`)
	now := time.Unix(1_700_000_000, 0)
	stamp := strconv.FormatInt(now.Unix(), 10)
	good := signHeader(stamp, body, testSigningSecret)

	for _, tc := range []struct {
		name    string
		header  string
		body    []byte
		at      time.Time
		wantErr bool
	}{
		{"a good signature passes", good, body, now, false},
		{"no header is refused", "", body, now, true},
		{"a header with no v1 is refused", "t=" + stamp, body, now, true},
		{"the wrong secret is refused", signHeader(stamp, body, "nope"), body, now, true},
		{"a tampered body is refused", good, []byte(`{"event":{"id":"evt_2"}}`), now, true},
		{
			// The timestamp is signed, so an attacker cannot move it — which
			// is what makes this bound real.
			"a stale delivery is refused", good, body, now.Add(2 * signatureTolerance), true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifySignature(tc.body, tc.header, testSigningSecret, tc.at)
			if tc.wantErr && err == nil {
				t.Error("expected an error, got none")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func newApp(t *testing.T) *gocommerce.App {
	t.Helper()
	return gctest.New(t, New(Config{
		LinkToken:            testLinkToken,
		WebhookAuthorization: testAuthorization,
		SigningSecret:        testSigningSecret,
	}))
}

// A checkout makes no network call at all: the purchase link is a URL, and it
// has to carry the order number, because that is the only handle the webhook
// will give back.
func TestCheckoutBuildsAPurchaseLink(t *testing.T) {
	app := newApp(t)

	result := gctest.PlaceOrder(t, app, "revenuecat")
	if result.Payment.Kind != gocommerce.IntentRedirect {
		t.Fatalf("payment kind = %q, want redirect", result.Payment.Kind)
	}
	link := result.Payment.ClientData["url"]
	want := "https://pay.rev.cat/" + testLinkToken + "/" + result.Order.Number
	if !strings.HasPrefix(link, want) {
		t.Errorf("link = %q, want it to start with %q", link, want)
	}
	if !strings.Contains(link, "email=gctest%40example.com") {
		t.Errorf("link = %q, want the shopper's email carried into it", link)
	}
	if result.Payment.Reference != result.Order.Number {
		t.Errorf("reference = %q, want the order number (the app user id)", result.Payment.Reference)
	}
	if result.Order.Status != gocommerce.OrderPending {
		t.Errorf("status = %q, want pending until RevenueCat confirms", result.Order.Status)
	}
}

// The module's reason to exist, end to end.
func TestWebhookSettlesTheOrder(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, "revenuecat")
	body := purchase("evt_1", "NON_RENEWING_PURCHASE", result.Order.Number,
		float64(result.Order.Total.AmountMinor)/100, result.Order.Total.Currency, "txn_9")
	if rec := postWebhook(t, app, body, testAuthorization, true); rec.Code != http.StatusOK {
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
	if order.PaymentReference != "txn_9" {
		t.Errorf("payment reference = %q, want the store transaction id", order.PaymentReference)
	}
}

// The price is RevenueCat's, so this check is the only thing standing between a
// misconfigured catalogue and orders settling for the wrong money.
func TestPriceMismatchDoesNotSettle(t *testing.T) {
	app := newApp(t)
	result := gctest.PlaceOrder(t, app, "revenuecat")

	for name, body := range map[string]string{
		"a cheaper product": purchase("evt_low", "NON_RENEWING_PURCHASE", result.Order.Number,
			1, result.Order.Total.Currency, "txn_low"),
		"another currency": purchase("evt_ccy", "NON_RENEWING_PURCHASE", result.Order.Number,
			float64(result.Order.Total.AmountMinor)/100, "EUR", "txn_ccy"),
	} {
		if rec := postWebhook(t, app, body, testAuthorization, true); rec.Code != http.StatusOK {
			t.Fatalf("webhook for %s = %d: %s", name, rec.Code, rec.Body)
		}
	}

	order, err := app.Order().Get(context.Background(), result.Order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if order.PaymentStatus == gocommerce.PaymentPaid {
		t.Error("a purchase for the wrong amount settled the order")
	}
}

func TestWebhookReplayIsIdempotent(t *testing.T) {
	app := newApp(t)
	result := gctest.PlaceOrder(t, app, "revenuecat")

	body := purchase("evt_dup", "INITIAL_PURCHASE", result.Order.Number,
		float64(result.Order.Total.AmountMinor)/100, result.Order.Total.Currency, "txn_dup")
	for i := range 2 {
		if rec := postWebhook(t, app, body, testAuthorization, true); rec.Code != http.StatusOK {
			t.Fatalf("delivery %d = %d: %s", i+1, rec.Code, rec.Body)
		}
	}

	var claims int
	if err := app.DB().QueryRow(
		`SELECT count(*) FROM payments_revenuecat_events WHERE id = 'evt_dup'`,
	).Scan(&claims); err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if claims != 1 {
		t.Errorf("event rows = %d, want exactly 1", claims)
	}
}

// The shared Authorization header is the primary gate, and it must hold on its
// own — a correct HMAC with the wrong header is still a stranger.
func TestWebhookRejectsABadAuthorizationHeader(t *testing.T) {
	app := newApp(t)
	result := gctest.PlaceOrder(t, app, "revenuecat")

	body := purchase("evt_forged", "NON_RENEWING_PURCHASE", result.Order.Number,
		float64(result.Order.Total.AmountMinor)/100, result.Order.Total.Currency, "txn_forged")

	for _, header := range []string{"", "Bearer wrong", strings.ToUpper(testAuthorization)} {
		if rec := postWebhook(t, app, body, header, true); rec.Code != http.StatusBadRequest {
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

// With signing switched on, the right header and no signature is still refused.
func TestWebhookRejectsAMissingSignature(t *testing.T) {
	app := newApp(t)
	result := gctest.PlaceOrder(t, app, "revenuecat")

	body := purchase("evt_unsigned", "NON_RENEWING_PURCHASE", result.Order.Number,
		float64(result.Order.Total.AmountMinor)/100, result.Order.Total.Currency, "txn_unsigned")
	if rec := postWebhook(t, app, body, testAuthorization, false); rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// RevenueCat has no refund API for Web Billing, so the provider must not claim
// the capability — the engine's answer has to be "not supported", not a silent
// no-op.
func TestRefundIsNotSupported(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, "revenuecat")
	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "txn_9"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	if _, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{}, nil); err == nil {
		t.Error("refund succeeded, but RevenueCat has no API for one")
	}
}

// ------------------------------------------------------------------ helpers

func signHeader(timestamp string, body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	return "t=" + timestamp + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func purchase(id, kind, appUserID string, price float64, currency, transactionID string) string {
	return fmt.Sprintf(
		`{"api_version":"1.0","event":{"id":"%s","type":"%s","app_user_id":"%s","product_id":"digital_thing","price_in_purchased_currency":%v,"currency":"%s","transaction_id":"%s","store":"RC_BILLING"}}`,
		id, kind, appUserID, price, currency, transactionID)
}

func postWebhook(t *testing.T, app *gocommerce.App, body, authorization string, sign bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/checkout/revenuecat/webhook", strings.NewReader(body))
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if sign {
		stamp := strconv.FormatInt(time.Now().Unix(), 10)
		req.Header.Set(signatureHeader, signHeader(stamp, []byte(body), testSigningSecret))
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
		if p.Code == "revenuecat" {
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
		if code == "revenuecat" {
			t.Fatal("an idle gateway must not be offered at checkout")
		}
	}
	if _, err := app.Order().Checkout(ctx, "revenuecat", gocommerce.CheckoutInput{}, ""); err == nil || !strings.Contains(err.Error(), "no payment method") {
		t.Fatalf("checkout through an idle gateway = %v, want it refused as unknown", err)
	}

	on := true
	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{"link_token": "tok", "webhook_authorization": "Bearer x"}}); err != nil {
		t.Fatalf("configure from the panel: %v", err)
	}
	for _, p := range app.Settings().PaymentMethods {
		if p.Code == "revenuecat" && !p.Configured {
			t.Fatal("configured from the panel, the settings should say so")
		}
	}
	offered := false
	for _, code := range app.Pay().Methods() {
		offered = offered || code == "revenuecat"
	}
	if !offered {
		t.Fatal("configured from the panel, the gateway should be offered")
	}
}
