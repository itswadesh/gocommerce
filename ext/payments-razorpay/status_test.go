package razorpay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// razorpayStub answers the three calls this module makes, in the shapes
// Razorpay documents, and remembers what it was asked.
type razorpayStub struct {
	mu       sync.Mutex
	payments string // the items of GET /v1/orders/{id}/payments
	asked    []string
	refunded []string
}

func (s *razorpayStub) set(items string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payments = items
}

func newStatusApp(t *testing.T) (*gocommerce.App, *razorpayStub) {
	t.Helper()
	stub := &razorpayStub{payments: `[]`}
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if user, pass, ok := r.BasicAuth(); !ok || user != "rzp_test_key" || pass != "rzp_test_secret" {
			http.Error(w, `{"error":{"code":"BAD_REQUEST_ERROR","description":"Authentication failed"}}`, http.StatusUnauthorized)
			return
		}
		stub.mu.Lock()
		defer stub.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/orders":
			body, _ := io.ReadAll(r.Body)
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			fmt.Fprintf(w, `{"id":"order_RZP1","entity":"order","amount":%v,"currency":"%v","status":"created"}`, in["amount"], in["currency"])
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/orders/") && strings.HasSuffix(r.URL.Path, "/payments"):
			stub.asked = append(stub.asked, r.URL.Path)
			var items []json.RawMessage
			_ = json.Unmarshal([]byte(stub.payments), &items)
			fmt.Fprintf(w, `{"entity":"collection","count":%d,"items":%s}`, len(items), stub.payments)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/payments/") && strings.HasSuffix(r.URL.Path, "/refund"):
			stub.refunded = append(stub.refunded, r.URL.Path)
			fmt.Fprint(w, `{"id":"rfnd_1","entity":"refund","amount":2500,"currency":"INR","status":"processed"}`)
		default:
			http.NotFound(w, r)
		}
	})
	mod := New(Config{KeyID: "rzp_test_key", KeySecret: "rzp_test_secret", WebhookSecret: "whsec", BaseURL: server.URL})
	return gctest.NewWithConfig(t, gocommerce.Config{AllowPrivateURLs: true, Currency: "INR"}, mod), stub
}

func payment(id, status string, amount int64, currency, extra string) string {
	return fmt.Sprintf(`{"id":"%s","entity":"payment","amount":%d,"currency":"%s","status":"%s","order_id":"order_RZP1","method":"upi","captured":%v%s}`,
		id, amount, currency, status, status == "captured", extra)
}

// A captured payment the webhook never reported is found on the Razorpay order
// and recorded, and it can then be refunded like one the webhook brought.
func TestCheckPaymentRecordsACapturedPayment(t *testing.T) {
	app, stub := newStatusApp(t)
	ctx := context.Background()
	result := gctest.PlaceOrder(t, app, "razorpay")
	stub.set(`[` + payment("pay_FAIL1", "failed", 2500, "INR", `,"error_description":"Payment was declined"`) + `,` +
		payment("pay_OK1", "captured", 2500, "INR", "") + `]`)

	check, err := app.Pay().CheckPayment(ctx, result.Order.ID)
	if err != nil {
		t.Fatalf("check payment: %v", err)
	}
	if !check.Recorded || check.Gateway.Reference != "pay_OK1" || check.Order.PaymentStatus != gocommerce.PaymentPaid {
		t.Fatalf("check = %+v, order payment %q; want pay_OK1 recorded", check.Gateway, check.Order.PaymentStatus)
	}
	if len(stub.asked) != 1 || stub.asked[0] != "/v1/orders/order_RZP1/payments" {
		t.Errorf("asked %v, want the order's Razorpay order", stub.asked)
	}

	if _, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{}, nil); err != nil {
		t.Fatalf("refund after a checked payment: %v", err)
	}
	if len(stub.refunded) != 1 || stub.refunded[0] != "/v1/payments/pay_OK1/refund" {
		t.Errorf("refunded %v, want the captured payment", stub.refunded)
	}
}

// Anything but a capture of the order's amount in its currency is answered and
// left alone.
func TestRazorpayStatusReportsOnlyWhatRazorpaySaid(t *testing.T) {
	app, stub := newStatusApp(t)
	result := gctest.PlaceOrder(t, app, "razorpay")
	for _, tc := range []struct {
		name   string
		items  string
		failed bool
		detail string
	}{
		{"no attempt yet", `[]`, false, "no payment"},
		{"authorised, not captured", `[` + payment("pay_A", "authorized", 2500, "INR", "") + `]`, false, "authorized"},
		{"declined", `[` + payment("pay_F", "failed", 2500, "INR", `,"error_description":"Payment was declined by the bank"`) + `]`, true, "declined by the bank"},
		{"captured for less", `[` + payment("pay_L", "captured", 100, "INR", "") + `]`, false, "100"},
	} {
		stub.set(tc.items)
		check, err := app.Pay().CheckPayment(context.Background(), result.Order.ID)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if check.Gateway.Paid || check.Gateway.Failed != tc.failed || !strings.Contains(check.Gateway.Detail, tc.detail) {
			t.Errorf("%s: gateway = %+v, want failed %v and detail containing %q", tc.name, check.Gateway, tc.failed, tc.detail)
		}
		if check.Recorded || check.Order.PaymentStatus != gocommerce.PaymentPending {
			t.Errorf("%s: recorded %v, payment %q", tc.name, check.Recorded, check.Order.PaymentStatus)
		}
	}
}
