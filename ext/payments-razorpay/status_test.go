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
	notes    map[string]string // Razorpay order id → the store order id in its notes
	extra    map[string]string // payments GET /v1/payments/{id} knows beyond the list
}

func (s *razorpayStub) set(items string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.payments = items
}

func newStatusApp(t *testing.T) (*gocommerce.App, *razorpayStub) {
	t.Helper()
	stub := &razorpayStub{payments: `[]`, notes: map[string]string{}, extra: map[string]string{}}
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
			notes, _ := in["notes"].(map[string]any)
			stub.notes["order_RZP1"] = fmt.Sprint(notes["order_id"])
			fmt.Fprintf(w, `{"id":"order_RZP1","entity":"order","amount":%v,"currency":"%v","status":"created"}`, in["amount"], in["currency"])
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/orders/") && strings.Count(r.URL.Path, "/") == 3:
			id := strings.TrimPrefix(r.URL.Path, "/v1/orders/")
			note, ok := stub.notes[id]
			if !ok {
				http.Error(w, `{"error":{"code":"BAD_REQUEST_ERROR","description":"The id provided does not exist"}}`, http.StatusBadRequest)
				return
			}
			fmt.Fprintf(w, `{"id":%q,"entity":"order","amount":2500,"currency":"INR","status":"paid","notes":{"order_id":%q}}`, id, note)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/payments/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/payments/")
			var items []map[string]any
			_ = json.Unmarshal([]byte(stub.payments), &items)
			for _, it := range items {
				if it["id"] == id {
					b, _ := json.Marshal(it)
					w.Write(b)
					return
				}
			}
			if body, ok := stub.extra[id]; ok {
				fmt.Fprint(w, body)
				return
			}
			http.Error(w, `{"error":{"code":"BAD_REQUEST_ERROR","description":"The id provided does not exist"}}`, http.StatusBadRequest)
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

// A payment reference typed onto an order by hand is not trusted to be this
// order's: before refunding it, the module reads the payment back and checks
// its Razorpay order was made for this order, for this amount.
func TestRefundRefusesAPaymentOfAnotherOrder(t *testing.T) {
	app, stub := newStatusApp(t)
	ctx := context.Background()
	result := gctest.PlaceOrder(t, app, "razorpay")
	stub.mu.Lock()
	stub.notes["order_ELSE"] = "999999"
	stub.extra["pay_ELSE"] = `{"id":"pay_ELSE","entity":"payment","amount":2500,"currency":"INR","status":"captured","order_id":"order_ELSE"}`
	stub.extra["pay_SMALL"] = `{"id":"pay_SMALL","entity":"payment","amount":100,"currency":"INR","status":"captured","order_id":"order_RZP1"}`
	stub.mu.Unlock()
	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "pay_ELSE"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	if _, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{}, nil); err == nil {
		t.Error("refunded another order's payment")
	}
	ref := "pay_SMALL"
	if _, err := app.Order().Update(ctx, result.Order.ID, gocommerce.OrderPatch{PaymentReference: &ref}); err != nil {
		t.Fatalf("edit reference: %v", err)
	}
	if _, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{}, nil); err == nil {
		t.Error("refunded a payment for another amount")
	}
	if len(stub.refunded) != 0 {
		t.Errorf("refunds sent: %v, want none", stub.refunded)
	}
}

// Asked about a payment reference, Razorpay's answer counts only when that
// payment's Razorpay order was made for this order.
func TestStatusBindsAPaymentReferenceToTheOrder(t *testing.T) {
	app, stub := newStatusApp(t)
	ctx := context.Background()
	result := gctest.PlaceOrder(t, app, "razorpay")
	stub.mu.Lock()
	stub.notes["order_ELSE"] = "999999"
	stub.extra["pay_ELSE"] = `{"id":"pay_ELSE","entity":"payment","amount":2500,"currency":"INR","status":"captured","order_id":"order_ELSE"}`
	stub.mu.Unlock()
	ref := "pay_ELSE"
	if _, err := app.Order().Update(ctx, result.Order.ID, gocommerce.OrderPatch{PaymentReference: &ref}); err != nil {
		t.Fatalf("edit reference: %v", err)
	}
	check, err := app.Pay().CheckPayment(ctx, result.Order.ID)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if check.Gateway.Paid || check.Recorded || !strings.Contains(check.Gateway.Detail, "another order") {
		t.Errorf("gateway = %+v, want another order's payment reported and nothing recorded", check.Gateway)
	}

	// And a Razorpay order made for some other store order is not this one's.
	ref = "order_ELSE"
	if _, err := app.Order().Update(ctx, result.Order.ID, gocommerce.OrderPatch{PaymentReference: &ref}); err != nil {
		t.Fatalf("edit reference: %v", err)
	}
	stub.set(`[` + payment("pay_X", "captured", 2500, "INR", "") + `]`)
	check, err = app.Pay().CheckPayment(ctx, result.Order.ID)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if check.Gateway.Paid || !strings.Contains(check.Gateway.Detail, "another order") {
		t.Errorf("gateway = %+v, want a Razorpay order for another order refused", check.Gateway)
	}
}
