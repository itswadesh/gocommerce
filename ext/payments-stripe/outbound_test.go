package stripe

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// The API base URL is a store setting, so the module's own client is the
// install's guarded one: pointed at a private address, a payment fails
// without that address being reached.
func TestAPrivateBaseURLIsNotCalled(t *testing.T) {
	var hits atomic.Int32
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1) })
	mod := New(Config{SecretKey: "sk_test_123", WebhookSecret: testSecret, BaseURL: server.URL})
	app := gctest.New(t, mod)

	result := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	_, err := mod.Initiate(context.Background(), result.Order, gocommerce.PayOptions{})
	if err == nil || !strings.Contains(err.Error(), "is not a public address") {
		t.Fatalf("initiate against a loopback base URL = %v, want the refusal", err)
	}
	if hits.Load() != 0 {
		t.Errorf("the private address was reached %d times", hits.Load())
	}
}
