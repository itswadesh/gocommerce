package adyen

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	gocommerce "github.com/misiki/gocommerce/core"
	"github.com/misiki/gocommerce/gctest"
)

// A 32-byte key, hex, as the Customer Area hands them out.
const testHMACKey = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

const testMerchant = "GCTestMerchant"

// TestVerifyItem is pure logic and needs no database: it is the one piece of
// this module standing between the internet and marking orders paid.
func TestVerifyItem(t *testing.T) {
	t.Parallel()

	key, err := hex.DecodeString(testHMACKey)
	if err != nil {
		t.Fatalf("decode the key: %v", err)
	}

	base := notificationItem{
		EventCode:           "AUTHORISATION",
		MerchantAccountCode: testMerchant,
		MerchantReference:   "GC-1001",
		PSPReference:        "psp_1",
		Success:             "true",
	}
	base.Amount.Currency = "USD"
	base.Amount.Value = 2500

	signed := func(item notificationItem, withKey []byte) notificationItem {
		item.AdditionalData = map[string]string{"hmacSignature": signItem(item, withKey)}
		return item
	}

	wrongKey, _ := hex.DecodeString("2010203040506070809000a0b0c0d0e0f101112131415161718191a1b1c1d1e1")

	tampered := signed(base, key)
	// The amount is one of the eight signed fields, so moving it after signing
	// must fail — which is the attack the HMAC exists for.
	tampered.Amount.Value = 1

	swapped := signed(base, key)
	// So is the merchant reference, and that one decides which order settles.
	swapped.MerchantReference = "GC-9999"

	for _, tc := range []struct {
		name    string
		item    notificationItem
		wantErr bool
	}{
		{"a good signature passes", signed(base, key), false},
		{"no signature is refused", base, true},
		{"the wrong key is refused", signed(base, wrongKey), true},
		{"a changed amount is refused", tampered, true},
		{"a changed merchant reference is refused", swapped, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyItem(tc.item, key)
			if tc.wantErr && err == nil {
				t.Error("expected an error, got none")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// stubAdyen stands in for the Checkout API.
func stubAdyen(t *testing.T) *httptest.Server {
	t.Helper()
	return gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v71/paymentLinks":
			body, _ := io.ReadAll(r.Body)
			var in struct {
				Reference string `json:"reference"`
				Amount    struct {
					Value int64 `json:"value"`
				} `json:"amount"`
			}
			_ = json.Unmarshal(body, &in)
			w.WriteHeader(http.StatusCreated)
			// Echo the amount back so the test can prove the order total, in
			// minor units, is what reached Adyen.
			fmt.Fprintf(w, `{"id":"PL_TEST_1","url":"https://test.adyen.link/PL_TEST_1?v=%d","status":"active","reference":"%s"}`,
				in.Amount.Value, in.Reference)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/refunds"):
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"pspReference":"psp_refund_9","status":"received"}`)
		default:
			http.NotFound(w, r)
		}
	})
}

func newApp(t *testing.T) *gocommerce.App {
	t.Helper()
	server := stubAdyen(t)
	return gctest.New(t, New(Config{
		APIKey:          "adyen_test",
		MerchantAccount: testMerchant,
		HMACKey:         testHMACKey,
		BaseURL:         server.URL + "/v71",
		ReturnURL:       "https://shop.example.test/thanks",
	}))
}

// The module's reason to exist, end to end: a payment link that settles when
// Adyen's notification says the money arrived.
func TestCheckoutAndNotification(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, "adyen")
	if result.Payment.Kind != gocommerce.IntentRedirect {
		t.Fatalf("payment kind = %q, want redirect", result.Payment.Kind)
	}
	want := fmt.Sprintf("v=%d", result.Order.Total.AmountMinor)
	if got := result.Payment.ClientData["url"]; !strings.Contains(got, want) {
		t.Errorf("link = %q, want it to carry %s", got, want)
	}
	if result.Order.Status != gocommerce.OrderPending {
		t.Errorf("status = %q, want pending until Adyen confirms", result.Order.Status)
	}

	rec := postNotification(t, app, "AUTHORISATION", "psp_auth_1", result.Order.Number,
		result.Order.Total, "true")
	if rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}
	// Adyen retries until it reads exactly this.
	if got := strings.TrimSpace(rec.Body.String()); got != "[accepted]" {
		t.Errorf("body = %q, want [accepted]", got)
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
	// The reference stored is the authorisation's PSP reference, which is what
	// a refund is issued against — not the payment link id Initiate returned.
	if order.PaymentReference != "psp_auth_1" {
		t.Errorf("payment reference = %q, want the authorisation PSP reference", order.PaymentReference)
	}
}

// A refused authorisation must not settle the order.
func TestRefusedAuthorisationDoesNotSettle(t *testing.T) {
	app := newApp(t)
	result := gctest.PlaceOrder(t, app, "adyen")

	rec := postNotification(t, app, "AUTHORISATION", "psp_refused", result.Order.Number,
		result.Order.Total, "false")
	if rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}

	order, err := app.Order().Get(context.Background(), result.Order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if order.PaymentStatus == gocommerce.PaymentPaid {
		t.Error("a refused authorisation marked the order paid")
	}
}

// Adyen retries until it is told "[accepted]", so a redelivery is normal and
// must not settle the order twice.
func TestNotificationReplayIsIdempotent(t *testing.T) {
	app := newApp(t)
	result := gctest.PlaceOrder(t, app, "adyen")

	for i := range 2 {
		rec := postNotification(t, app, "AUTHORISATION", "psp_dup", result.Order.Number,
			result.Order.Total, "true")
		if rec.Code != http.StatusOK {
			t.Fatalf("delivery %d = %d: %s", i+1, rec.Code, rec.Body)
		}
	}

	var claims int
	if err := app.DB().QueryRow(
		`SELECT count(*) FROM payments_adyen_events WHERE id = 'AUTHORISATION:psp_dup'`,
	).Scan(&claims); err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if claims != 1 {
		t.Errorf("event rows = %d, want exactly 1", claims)
	}
}

func TestNotificationRejectsBadSignature(t *testing.T) {
	app := newApp(t)
	result := gctest.PlaceOrder(t, app, "adyen")

	item := itemFor("AUTHORISATION", "psp_forged", result.Order.Number, result.Order.Total, "true")
	wrongKey, _ := hex.DecodeString("2010203040506070809000a0b0c0d0e0f101112131415161718191a1b1c1d1e1")

	for name, signature := range map[string]string{
		"none":      "",
		"junk":      "not-a-signature",
		"wrong key": signItem(item, wrongKey),
	} {
		forged := item
		if signature != "" {
			forged.AdditionalData = map[string]string{"hmacSignature": signature}
		}
		if rec := postItems(t, app, forged); rec.Code != http.StatusBadRequest {
			t.Errorf("status for a %s signature = %d, want 400", name, rec.Code)
		}
	}

	order, err := app.Order().Get(context.Background(), result.Order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if order.PaymentStatus != gocommerce.PaymentPending {
		t.Errorf("payment status = %q — a forged notification changed the order", order.PaymentStatus)
	}
}

// A batch is verified whole before any of it is applied: one forged item must
// not let the valid ones beside it through.
func TestOneForgedItemRejectsTheWholeBatch(t *testing.T) {
	app := newApp(t)
	result := gctest.PlaceOrder(t, app, "adyen")

	key, _ := hex.DecodeString(testHMACKey)
	good := itemFor("AUTHORISATION", "psp_good", result.Order.Number, result.Order.Total, "true")
	good.AdditionalData = map[string]string{"hmacSignature": signItem(good, key)}

	bad := itemFor("AUTHORISATION", "psp_bad", result.Order.Number, result.Order.Total, "true")
	bad.AdditionalData = map[string]string{"hmacSignature": "forged"}

	if rec := postItems(t, app, good, bad); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	order, err := app.Order().Get(context.Background(), result.Order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if order.PaymentStatus == gocommerce.PaymentPaid {
		t.Error("the valid half of a forged batch settled the order")
	}
}

func TestRefund(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, "adyen")
	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "psp_auth_1"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	order, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{}, nil)
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if order.PaymentStatus != gocommerce.PaymentRefunded {
		t.Errorf("payment status = %q, want refunded", order.PaymentStatus)
	}
	if len(order.Refunds) != 1 || order.Refunds[0].ProviderReference != "psp_refund_9" {
		t.Errorf("refunds = %+v, want one carrying the PSP reference Adyen answered with", order.Refunds)
	}
}

// ------------------------------------------------------------------ helpers

func signItem(item notificationItem, key []byte) string {
	payload := strings.Join([]string{
		item.PSPReference,
		item.OriginalReference,
		item.MerchantAccountCode,
		item.MerchantReference,
		strconv.FormatInt(item.Amount.Value, 10),
		item.Amount.Currency,
		item.EventCode,
		item.Success,
	}, ":")
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func itemFor(eventCode, psp, reference string, total gocommerce.Money, success string) notificationItem {
	item := notificationItem{
		EventCode:           eventCode,
		MerchantAccountCode: testMerchant,
		MerchantReference:   reference,
		PSPReference:        psp,
		Success:             success,
	}
	item.Amount.Currency = total.Currency
	item.Amount.Value = total.AmountMinor
	return item
}

func postNotification(t *testing.T, app *gocommerce.App, eventCode, psp, reference string,
	total gocommerce.Money, success string) *httptest.ResponseRecorder {
	t.Helper()
	key, _ := hex.DecodeString(testHMACKey)
	item := itemFor(eventCode, psp, reference, total, success)
	item.AdditionalData = map[string]string{"hmacSignature": signItem(item, key)}
	return postItems(t, app, item)
}

func postItems(t *testing.T, app *gocommerce.App, items ...notificationItem) *httptest.ResponseRecorder {
	t.Helper()
	wrapped := make([]map[string]any, 0, len(items))
	for _, item := range items {
		wrapped = append(wrapped, map[string]any{"NotificationRequestItem": item})
	}
	body, err := json.Marshal(map[string]any{"live": "false", "notificationItems": wrapped})
	if err != nil {
		t.Fatalf("encode the notification: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/checkout/adyen/webhook", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
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
		if p.Code == "adyen" {
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
		if code == "adyen" {
			t.Fatal("an idle gateway must not be offered at checkout")
		}
	}
	if _, err := app.Order().Checkout(ctx, "adyen", gocommerce.CheckoutInput{}, ""); err == nil || !strings.Contains(err.Error(), "no payment method") {
		t.Fatalf("checkout through an idle gateway = %v, want it refused as unknown", err)
	}

	on := true
	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{"api_key": "k-1", "merchant_account": "AcmeECOM", "hmac_key": "0a0b0c", "base_url": "https://checkout-test.adyen.com/v71"}}); err != nil {
		t.Fatalf("configure from the panel: %v", err)
	}
	for _, p := range app.Settings().PaymentMethods {
		if p.Code == "adyen" && !p.Configured {
			t.Fatal("configured from the panel, the settings should say so")
		}
	}
	offered := false
	for _, code := range app.Pay().Methods() {
		offered = offered || code == "adyen"
	}
	if !offered {
		t.Fatal("configured from the panel, the gateway should be offered")
	}
}
