package helcim

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
	"testing"
	"time"

	gocommerce "github.com/misiki/gocommerce/core"
	"github.com/misiki/gocommerce/gctest"
)

// Base64, as Helcim shows a verifier token.
const testVerifier = "aGVsY2ltLXZlcmlmaWVyLXRva2Vu"

// TestVerifySignature is pure logic and needs no database: it is the one piece
// of this module standing between the internet and marking orders paid.
func TestVerifySignature(t *testing.T) {
	t.Parallel()

	key, err := base64.StdEncoding.DecodeString(testVerifier)
	if err != nil {
		t.Fatalf("decode the verifier: %v", err)
	}

	body := []byte(`{"id":"25764674","type":"cardTransaction"}`)
	now := time.Unix(1_700_000_000, 0)
	stamp := strconv.FormatInt(now.Unix(), 10)
	good := signHeader("wh_1", stamp, body, key)

	for _, tc := range []struct {
		name           string
		id, ts, header string
		body           []byte
		at             time.Time
		wantErr        bool
	}{
		{"a good signature passes", "wh_1", stamp, good, body, now, false},
		{
			// Helcim sends several while a token is rotating, so any match is
			// a match.
			"one good signature among several passes",
			"wh_1", stamp, "v1,AAAA " + good, body, now, false,
		},
		{"no id is refused", "", stamp, good, body, now, true},
		{"no timestamp is refused", "wh_1", "", good, body, now, true},
		{"no signature is refused", "wh_1", stamp, "", body, now, true},
		{
			// The id is inside the signed string, so a delivery replayed under
			// a different id must fail.
			"a swapped id is refused", "wh_2", stamp, good, body, now, true,
		},
		{
			"a tampered body is refused", "wh_1", stamp, good,
			[]byte(`{"id":"99999999","type":"cardTransaction"}`), now, true,
		},
		{
			// The timestamp is signed, so an attacker cannot move it — which
			// is what makes the staleness bound worth having.
			"a stale delivery is refused", "wh_1", stamp, good, body,
			now.Add(2 * signatureTolerance), true,
		},
		{
			"a delivery from the future is refused", "wh_1", stamp, good, body,
			now.Add(-2 * signatureTolerance), true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifySignature(tc.body, tc.id, tc.ts, tc.header, key, tc.at)
			if tc.wantErr && err == nil {
				t.Error("expected an error, got none")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestRegisterRequiresConfiguration(t *testing.T) {
	t.Parallel()

	for _, cfg := range []Config{
		{},
		{APIToken: "k"},
		{VerifierToken: testVerifier},
		// A verifier that is not base64 must be refused at boot rather than
		// silently failing every webhook in production.
		{APIToken: "k", VerifierToken: "not base64!!"},
	} {
		if err := New(cfg).Register(nil); err == nil {
			t.Errorf("Register accepted an unusable config: %+v", cfg)
		}
	}
}

// helcimStub stands in for the API, and remembers the transaction it should
// report so a test can shape it.
type helcimStub struct {
	mu          sync.Mutex
	transaction cardTransaction
}

func (s *helcimStub) set(tx cardTransaction) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transaction = tx
}

func (s *helcimStub) get() cardTransaction {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transaction
}

func newApp(t *testing.T) (*gocommerce.App, *helcimStub) {
	t.Helper()
	stub := &helcimStub{}
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/helcim-pay/initialize":
			body, _ := io.ReadAll(r.Body)
			var in struct {
				Amount      float64 `json:"amount"`
				PaymentType string  `json:"paymentType"`
			}
			_ = json.Unmarshal(body, &in)
			// Echo the amount into the token so the test can prove what the
			// decimal conversion sent.
			fmt.Fprintf(w, `{"checkoutToken":"chk_%.2f","secretToken":"sec_1"}`, in.Amount)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/card-transactions/"):
			_ = json.NewEncoder(w).Encode(stub.get())
		case r.Method == http.MethodPost && r.URL.Path == "/v2/payment/refund":
			if r.Header.Get("idempotency-key") == "" {
				// Helcim refuses a payment call without one; so does this stub,
				// because a module that forgot would otherwise pass its tests.
				http.Error(w, `{"error":"idempotency-key is required"}`, http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"transactionId":"77001","status":"APPROVED","type":"refund"}`)
		default:
			http.NotFound(w, r)
		}
	})
	app := gctest.New(t, New(Config{
		APIToken:      "helcim_test",
		VerifierToken: testVerifier,
		BaseURL:       server.URL + "/v2",
	}))
	return app, stub
}

// The module's reason to exist, end to end: a checkout session that settles
// when Helcim says the money arrived.
func TestCheckoutAndWebhook(t *testing.T) {
	app, stub := newApp(t)
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, "helcim")
	if result.Payment.Kind != gocommerce.IntentClientAction {
		t.Fatalf("payment kind = %q, want client_action — Helcim has no hosted page to redirect to",
			result.Payment.Kind)
	}
	// 2500 minor units is 25.00, not 2500.00 and not 25.0.
	want := fmt.Sprintf("chk_%.2f", float64(result.Order.Total.AmountMinor)/100)
	if got := result.Payment.ClientData["checkout_token"]; got != want {
		t.Errorf("checkout token = %q, want %q", got, want)
	}
	if strings.Contains(fmt.Sprint(result.Payment.ClientData), "sec_1") {
		t.Error("the secret token reached the client")
	}

	stub.set(cardTransaction{
		TransactionID: "25764674", Status: "APPROVED", Type: "purchase",
		Amount:        float64(result.Order.Total.AmountMinor) / 100,
		Currency:      result.Order.Total.Currency,
		InvoiceNumber: result.Order.Number,
	})
	if rec := postWebhook(t, app, "wh_1", `{"id":"25764674","type":"cardTransaction"}`); rec.Code != http.StatusOK {
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
	// The reference stored is Helcim's transaction id, which is what a refund
	// is issued against — not the checkout token Initiate returned.
	if order.PaymentReference != "25764674" {
		t.Errorf("payment reference = %q, want the transaction id", order.PaymentReference)
	}
}

// The webhook body carries no amount, so the transaction read back is the only
// place a short payment can be caught.
func TestAmountMismatchDoesNotSettle(t *testing.T) {
	app, stub := newApp(t)
	result := gctest.PlaceOrder(t, app, "helcim")

	stub.set(cardTransaction{
		TransactionID: "2222", Status: "APPROVED", Type: "purchase",
		Amount:        1, // a dollar against a 25.00 order
		Currency:      result.Order.Total.Currency,
		InvoiceNumber: result.Order.Number,
	})
	if rec := postWebhook(t, app, "wh_short", `{"id":"2222","type":"cardTransaction"}`); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}

	order, err := app.Order().Get(context.Background(), result.Order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if order.PaymentStatus == gocommerce.PaymentPaid {
		t.Error("a transaction for the wrong amount settled the order")
	}
}

// A preauth is authorised, not taken. Settling on one would ship goods against
// money that has not moved.
func TestPreauthDoesNotSettle(t *testing.T) {
	app, stub := newApp(t)
	result := gctest.PlaceOrder(t, app, "helcim")

	stub.set(cardTransaction{
		TransactionID: "3333", Status: "APPROVED", Type: "preauth",
		Amount:        float64(result.Order.Total.AmountMinor) / 100,
		Currency:      result.Order.Total.Currency,
		InvoiceNumber: result.Order.Number,
	})
	if rec := postWebhook(t, app, "wh_preauth", `{"id":"3333","type":"cardTransaction"}`); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}

	order, err := app.Order().Get(context.Background(), result.Order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if order.PaymentStatus == gocommerce.PaymentPaid {
		t.Error("a preauthorisation settled the order")
	}
}

func TestWebhookReplayIsIdempotent(t *testing.T) {
	app, stub := newApp(t)
	result := gctest.PlaceOrder(t, app, "helcim")

	stub.set(cardTransaction{
		TransactionID: "4444", Status: "APPROVED", Type: "purchase",
		Amount:        float64(result.Order.Total.AmountMinor) / 100,
		Currency:      result.Order.Total.Currency,
		InvoiceNumber: result.Order.Number,
	})
	for i := range 2 {
		if rec := postWebhook(t, app, "wh_dup", `{"id":"4444","type":"cardTransaction"}`); rec.Code != http.StatusOK {
			t.Fatalf("delivery %d = %d: %s", i+1, rec.Code, rec.Body)
		}
	}

	var claims int
	if err := app.DB().QueryRow(
		`SELECT count(*) FROM payments_helcim_events WHERE id = 'cardTransaction:4444'`,
	).Scan(&claims); err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if claims != 1 {
		t.Errorf("event rows = %d, want exactly 1", claims)
	}
}

func TestWebhookRejectsBadSignature(t *testing.T) {
	app, _ := newApp(t)
	result := gctest.PlaceOrder(t, app, "helcim")

	body := `{"id":"5555","type":"cardTransaction"}`
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	wrong, _ := base64.StdEncoding.DecodeString("bm90LXRoZS1yaWdodC1rZXk=")

	for name, header := range map[string]string{
		"none":      "",
		"junk":      "v1,not-a-signature",
		"wrong key": signHeader("wh_forged", stamp, []byte(body), wrong),
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/checkout/helcim/webhook", strings.NewReader(body))
		req.Header.Set("webhook-id", "wh_forged")
		req.Header.Set("webhook-timestamp", stamp)
		if header != "" {
			req.Header.Set("webhook-signature", header)
		}
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status for a %s signature = %d, want 400", name, rec.Code)
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
	app, _ := newApp(t)
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, "helcim")
	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "25764674"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	order, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{}, nil)
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if order.PaymentStatus != gocommerce.PaymentRefunded {
		t.Errorf("payment status = %q, want refunded", order.PaymentStatus)
	}
	if len(order.Refunds) != 1 || order.Refunds[0].ProviderReference != "77001" {
		t.Errorf("refunds = %+v, want one carrying the id Helcim answered with", order.Refunds)
	}
}

// A refund before the webhook has supplied a transaction id has nothing to
// refund against, and must say so rather than post a checkout token.
func TestRefundNeedsATransactionID(t *testing.T) {
	app, _ := newApp(t)
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, "helcim")
	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "chk_25.00"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	if _, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{}, nil); err == nil {
		t.Error("refunding against a checkout token succeeded")
	}
}

// ------------------------------------------------------------------ helpers

func signHeader(id, timestamp string, body, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + timestamp + "."))
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func postWebhook(t *testing.T, app *gocommerce.App, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	key, _ := base64.StdEncoding.DecodeString(testVerifier)
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, "/api/checkout/helcim/webhook", strings.NewReader(body))
	req.Header.Set("webhook-id", id)
	req.Header.Set("webhook-timestamp", stamp)
	req.Header.Set("webhook-signature", signHeader(id, stamp, []byte(body), key))
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}
