package stripe

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// A refund whose answer was lost is retried with the same Idempotency-Key, so
// Stripe answers with the refund it already made instead of making a second.
func TestRefundRetryReusesTheIdempotencyKey(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	fail := true
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/payment_intents":
			_ = r.ParseForm()
			fmt.Fprintf(w, `{"id":"pi_test_123","client_secret":"s","status":"requires_payment_method","amount":%s}`, r.Form.Get("amount"))
		case "/v1/refunds":
			mu.Lock()
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			lost := fail
			fail = false
			mu.Unlock()
			if lost {
				w.WriteHeader(http.StatusGatewayTimeout)
				fmt.Fprint(w, `{"error":{"message":"timed out"}}`)
				return
			}
			fmt.Fprint(w, `{"id":"re_1","status":"succeeded"}`)
		default:
			http.NotFound(w, r)
		}
	})
	app := gctest.NewWithConfig(t, gocommerce.Config{AllowPrivateURLs: true},
		New(Config{SecretKey: "sk_test_123", WebhookSecret: testSecret, BaseURL: server.URL}))
	ctx := context.Background()
	result := gctest.PlaceOrder(t, app, "stripe")
	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "pi_test_123"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{AmountMinor: 1000}, nil); err == nil {
		t.Fatal("a refund with no answer was recorded")
	}
	if _, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{AmountMinor: 1000}, nil); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if _, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{AmountMinor: 1000}, nil); err != nil {
		t.Fatalf("second refund: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 3 || keys[0] == "" {
		t.Fatalf("Idempotency-Keys = %q, want three, set", keys)
	}
	if keys[0] != keys[1] {
		t.Errorf("retry key %q differs from the lost one %q", keys[1], keys[0])
	}
	if keys[2] == keys[0] {
		t.Errorf("a separate refund reused key %q", keys[0])
	}
}
