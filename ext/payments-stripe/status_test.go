package stripe

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// intentStub answers GET /v1/payment_intents/{id} with whatever the test sets,
// in the shape Stripe documents.
type intentStub struct {
	mu     sync.Mutex
	answer string
	asked  []string
}

func (s *intentStub) set(body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answer = body
}

func newStatusApp(t *testing.T) (*gocommerce.App, *intentStub) {
	t.Helper()
	stub := &intentStub{}
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer sk_test_123" {
			http.Error(w, `{"error":{"message":"Invalid API Key provided"}}`, http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/payment_intents":
			_ = r.ParseForm()
			fmt.Fprintf(w, `{"id":"pi_test_123","client_secret":"pi_test_123_secret","status":"requires_payment_method","amount":%s}`, r.Form.Get("amount"))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/payment_intents/"):
			stub.mu.Lock()
			stub.asked = append(stub.asked, strings.TrimPrefix(r.URL.Path, "/v1/payment_intents/"))
			body := stub.answer
			stub.mu.Unlock()
			fmt.Fprint(w, body)
		default:
			http.NotFound(w, r)
		}
	})
	mod := New(Config{SecretKey: "sk_test_123", WebhookSecret: testSecret, BaseURL: server.URL})
	return gctest.NewWithConfig(t, gocommerce.Config{AllowPrivateURLs: true}, mod), stub
}

// The lost-webhook case: Stripe says the intent succeeded, for this order's
// amount, and checking records it with the intent as the reference.
func TestCheckPaymentRecordsASucceededIntent(t *testing.T) {
	app, stub := newStatusApp(t)
	result := gctest.PlaceOrder(t, app, "stripe")
	stub.set(fmt.Sprintf(`{"id":"pi_test_123","object":"payment_intent","status":"succeeded","amount":2500,"amount_received":2500,"currency":"usd","metadata":{"order_id":"%d"}}`, result.Order.ID))

	check, err := app.Pay().CheckPayment(context.Background(), result.Order.ID)
	if err != nil {
		t.Fatalf("check payment: %v", err)
	}
	if !check.Gateway.Paid || !check.Recorded || check.Gateway.Reference != "pi_test_123" {
		t.Errorf("check = %+v, want paid and recorded as pi_test_123", check)
	}
	if check.Order.PaymentStatus != gocommerce.PaymentPaid || check.Order.Status != gocommerce.OrderConfirmed {
		t.Errorf("order %s / %s, want confirmed and paid", check.Order.Status, check.Order.PaymentStatus)
	}
	if len(stub.asked) != 1 || stub.asked[0] != "pi_test_123" {
		t.Errorf("asked about %v, want the order's own intent", stub.asked)
	}
}

// What Stripe says that is not a settled payment is answered and not recorded,
// and a succeeded intent for some other amount or order is not this payment.
func TestStatusReportsOnlyWhatStripeSaid(t *testing.T) {
	app, stub := newStatusApp(t)
	result := gctest.PlaceOrder(t, app, "stripe")
	id := result.Order.ID
	for _, tc := range []struct {
		name         string
		body         string
		paid, failed bool
		detail       string
	}{
		{"not paid yet", `{"id":"pi_test_123","status":"requires_payment_method","amount":2500,"amount_received":0,"currency":"usd","metadata":{"order_id":"%d"}}`, false, false, "requires_payment_method"},
		{"still processing", `{"id":"pi_test_123","status":"processing","amount":2500,"amount_received":0,"currency":"usd","metadata":{"order_id":"%d"}}`, false, false, "processing"},
		{"declined", `{"id":"pi_test_123","status":"requires_payment_method","amount":2500,"amount_received":0,"currency":"usd","metadata":{"order_id":"%d"},"last_payment_error":{"message":"Your card was declined."}}`, false, true, "Your card was declined."},
		{"cancelled", `{"id":"pi_test_123","status":"canceled","amount":2500,"amount_received":0,"currency":"usd","metadata":{"order_id":"%d"},"cancellation_reason":"abandoned"}`, false, true, "abandoned"},
		{"another amount", `{"id":"pi_test_123","status":"succeeded","amount":100,"amount_received":100,"currency":"usd","metadata":{"order_id":"%d"}}`, false, false, "100"},
		{"another currency", `{"id":"pi_test_123","status":"succeeded","amount":2500,"amount_received":2500,"currency":"eur","metadata":{"order_id":"%d"}}`, false, false, "EUR"},
		{"another order", `{"id":"pi_test_123","status":"succeeded","amount":2500,"amount_received":2500,"currency":"usd","metadata":{"order_id":"%d1"}}`, false, false, "another order"},
	} {
		stub.set(fmt.Sprintf(tc.body, id))
		check, err := app.Pay().CheckPayment(context.Background(), id)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if check.Gateway.Paid != tc.paid || check.Gateway.Failed != tc.failed || !strings.Contains(check.Gateway.Detail, tc.detail) {
			t.Errorf("%s: gateway = %+v, want paid %v failed %v detail containing %q", tc.name, check.Gateway, tc.paid, tc.failed, tc.detail)
		}
		if check.Recorded || check.Order.PaymentStatus != gocommerce.PaymentPending {
			t.Errorf("%s: recorded %v, payment %q; want nothing recorded", tc.name, check.Recorded, check.Order.PaymentStatus)
		}
	}
}

func TestStripeIsAdvertisedAsCheckable(t *testing.T) {
	app, _ := newStatusApp(t)
	for _, p := range app.Settings().PaymentMethods {
		if p.Code == "stripe" && !p.ChecksPayments {
			t.Error("stripe: checks_payments = false")
		}
	}
}
