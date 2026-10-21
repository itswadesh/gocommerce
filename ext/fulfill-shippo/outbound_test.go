package shippo

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// A carrier's API base URL is a store setting too, held to the same rule.
func TestAPrivateBaseURLIsNotCalled(t *testing.T) {
	var hits atomic.Int32
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1) })
	app := gctest.New(t, New(Config{
		APIKey: "shippo_test", BaseURL: server.URL, CarrierAccount: "acct_1", ServicelevelToken: "usps_priority",
		From: Address{Name: "Acme", Street1: "215 Clayton St", City: "San Francisco", State: "CA", Zip: "94117", Country: "US"},
	}))

	result := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	_, err := app.Ship().Create(context.Background(), result.Order.ID, "shippo", gocommerce.ShipRequest{})
	if err == nil || !strings.Contains(err.Error(), "is not a public address") {
		t.Fatalf("shipping through a loopback base URL = %v, want the refusal", err)
	}
	if hits.Load() != 0 {
		t.Errorf("the private address was reached %d times", hits.Load())
	}
}
