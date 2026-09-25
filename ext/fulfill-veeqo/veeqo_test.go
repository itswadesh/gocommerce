package veeqo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// record is the part of the payload this package is responsible for getting
// right.
type record struct {
	AllocationID int64 `json:"allocation_id"`
	OrderID      int64 `json:"order_id"`
	Shipment     struct {
		TrackingNumberAttributes struct {
			TrackingNumber string `json:"tracking_number"`
		} `json:"tracking_number_attributes"`
		CarrierID         int  `json:"carrier_id"`
		NotifyCustomer    bool `json:"notify_customer"`
		UpdateRemoteOrder bool `json:"update_remote_order"`
	} `json:"shipment"`
}

type stub struct {
	mu      sync.Mutex
	last    record
	orders  string // the JSON the orders search answers with
	queried string
	carrier string
}

func (s *stub) get() (record, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last, s.queried
}

func newApp(t *testing.T, cfg Config, orders string) (*gocommerce.App, *stub) {
	t.Helper()
	st := &stub{orders: orders, carrier: "Royal Mail"}
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("x-api-key") != "vq_test" {
			http.Error(w, `{"error":"bad key"}`, http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/orders":
			st.mu.Lock()
			st.queried = r.URL.Query().Get("query")
			body := st.orders
			st.mu.Unlock()
			_, _ = io.WriteString(w, body)

		case r.Method == http.MethodPost && r.URL.Path == "/shipments":
			raw, _ := io.ReadAll(r.Body)
			var in record
			_ = json.Unmarshal(raw, &in)
			st.mu.Lock()
			st.last = in
			carrier := st.carrier
			st.mu.Unlock()
			_, _ = io.WriteString(w, `{"id":900,"carrier":{"id":7,"name":"`+carrier+`"},
				"tracking_number":{"tracking_number":"`+in.Shipment.TrackingNumberAttributes.TrackingNumber+`"}}`)

		default:
			http.NotFound(w, r)
		}
	})

	cfg.APIKey = "vq_test"
	cfg.BaseURL = server.URL
	return gctest.New(t, New(cfg)), st
}

// oneOrder is what Veeqo answers when the store's order is there and allocated.
func oneOrder(number string) string {
	return `[{"id":501,"number":"` + number + `","allocations":[{"id":601}]}]`
}

func shipOrder(t *testing.T, app *gocommerce.App, sku string, req gocommerce.ShipRequest) (*gocommerce.Order, string, error) {
	t.Helper()
	product := gctest.CreateProduct(t, app, sku, 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)
	order, err := app.Ship().Create(t.Context(), result.Order.ID, "veeqo", req)
	return order, result.Order.Number, err
}

// The module's reason to exist: Veeqo learns a parcel went out, against the
// allocation the warehouse picked from.
func TestShipmentIsRecordedAgainstTheAllocation(t *testing.T) {
	app, st := newApp(t, Config{}, "")
	product := gctest.CreateProduct(t, app, "VQ-1", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	st.mu.Lock()
	st.orders = oneOrder(result.Order.Number)
	st.mu.Unlock()

	order := gctest.Ship(t, app, result.Order.ID, "veeqo",
		gocommerce.ShipRequest{Tracking: "RM123456789GB"})
	shipment := order.Fulfillments[0]
	if shipment.Tracking != "RM123456789GB" {
		t.Errorf("tracking = %q, want what actually went out", shipment.Tracking)
	}
	if shipment.Carrier != "royal-mail" {
		t.Errorf("carrier = %q, want royal-mail, read from what Veeqo called it", shipment.Carrier)
	}

	sent, queried := st.get()
	if queried != result.Order.Number {
		t.Errorf("searched Veeqo for %q, want the order number", queried)
	}
	if sent.OrderID != 501 || sent.AllocationID != 601 {
		t.Errorf("recorded against order %d allocation %d, want 501 and 601", sent.OrderID, sent.AllocationID)
	}
	if sent.Shipment.TrackingNumberAttributes.TrackingNumber != "RM123456789GB" {
		t.Errorf("tracking sent = %q", sent.Shipment.TrackingNumberAttributes.TrackingNumber)
	}
	// Two emails about one parcel is how a shop looks disorganised, and the
	// engine already sends one.
	if sent.Shipment.NotifyCustomer {
		t.Error("Veeqo was asked to email the customer as well as the engine")
	}
	// This store is the sales channel; pushing back into it is the loop
	// nobody wants.
	if sent.Shipment.UpdateRemoteOrder {
		t.Error("Veeqo was asked to push the shipment back to the remote order")
	}
	if sent.Shipment.CarrierID != carrierOther {
		t.Errorf("carrier_id = %d, want Veeqo's Other (%d)", sent.Shipment.CarrierID, carrierOther)
	}
}

// This provider records a parcel; it cannot invent one.
func TestATrackingNumberIsRequired(t *testing.T) {
	app, _ := newApp(t, Config{}, `[]`)

	_, _, err := shipOrder(t, app, "VQ-2", gocommerce.ShipRequest{})
	if err == nil || !strings.Contains(err.Error(), "tracking number") {
		t.Errorf("error = %v, want it to say a tracking number is required", err)
	}
}

// An operator who knows beats anything this module could work out, and passing
// both ids skips the search entirely.
func TestMetaCanNameTheAllocation(t *testing.T) {
	app, st := newApp(t, Config{}, `[]`)

	_, _, err := shipOrder(t, app, "VQ-3", gocommerce.ShipRequest{
		Tracking: "1Z999AA10123456784",
		Meta:     map[string]string{"allocation_id": "77", "order_id": "88"},
	})
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	sent, queried := st.get()
	if queried != "" {
		t.Errorf("searched Veeqo for %q, want no search at all", queried)
	}
	if sent.AllocationID != 77 || sent.OrderID != 88 {
		t.Errorf("recorded against order %d allocation %d, want 88 and 77", sent.OrderID, sent.AllocationID)
	}
}

// Refusals, each with a reason an operator can act on rather than a guess.
func TestRefusalsAreSpecific(t *testing.T) {
	for name, tc := range map[string]struct {
		orders string
		want   string
	}{
		"no such order": {`[]`, "no Veeqo order is numbered"},
		"not allocated": {`[{"id":501,"number":"%s","allocations":[]}]`, "no allocation"},
		"several allocations": {
			`[{"id":501,"number":"%s","allocations":[{"id":601},{"id":602}]}]`, "2 allocations",
		},
		"several orders": {
			`[{"id":501,"number":"%s","allocations":[{"id":601}]},{"id":502,"number":"%s","allocations":[{"id":602}]}]`,
			"pass Meta",
		},
	} {
		t.Run(name, func(t *testing.T) {
			app, st := newApp(t, Config{}, "")
			product := gctest.CreateProduct(t, app, "VQ-"+name[:4], 2500, 10)
			result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

			body := strings.ReplaceAll(tc.orders, "%s", result.Order.Number)
			st.mu.Lock()
			st.orders = body
			st.mu.Unlock()

			_, err := app.Ship().Create(t.Context(), result.Order.ID, "veeqo",
				gocommerce.ShipRequest{Tracking: "RM123456789GB"})
			if err == nil {
				t.Fatal("the shipment was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// Veeqo's search is fuzzy: "GC-100" also returns "GC-1001", and recording a
// parcel against the wrong order is worse than refusing.
func TestFuzzySearchResultsAreFilteredExactly(t *testing.T) {
	app, st := newApp(t, Config{}, "")
	product := gctest.CreateProduct(t, app, "VQ-5", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	st.mu.Lock()
	st.orders = `[{"id":999,"number":"` + result.Order.Number + `-OLD","allocations":[{"id":111}]},
		{"id":501,"number":"` + result.Order.Number + `","allocations":[{"id":601}]}]`
	st.mu.Unlock()

	gctest.Ship(t, app, result.Order.ID, "veeqo", gocommerce.ShipRequest{Tracking: "RM123456789GB"})
	sent, _ := st.get()
	if sent.OrderID != 501 {
		t.Errorf("recorded against Veeqo order %d, want the exact number match (501)", sent.OrderID)
	}
}

func TestCarrierFor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		names []string
		want  string
	}{
		{[]string{"Royal Mail"}, "royal-mail"},
		{[]string{"UPS"}, "ups"},
		// "Other" is Veeqo saying it does not know, so whatever the operator
		// put on the ship request is the better answer.
		{[]string{"Other", "dhl"}, "dhl"},
		{[]string{"Other", "not-a-carrier"}, ""},
		{nil, ""},
	} {
		if got := carrierFor(tc.names...); got != tc.want {
			t.Errorf("carrierFor(%v) = %q, want %q", tc.names, got, tc.want)
		}
	}
}

// TestInstalledIdleUntilConfigured: with nothing in Config the module still
// registers — the panel lists it — but the settings say it is not set up and
// nothing can be sent through it, until its fields are typed into the plugin.
func TestInstalledIdleUntilConfigured(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ctx := context.Background()

	var info *gocommerce.ProviderInfo
	for _, p := range app.Settings().FulfillmentProviders {
		if p.Code == "veeqo" {
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
	result := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	if _, err := app.Ship().Create(ctx, result.Order.ID, "veeqo", gocommerce.ShipRequest{Tracking: "T-1"}); err == nil || !strings.Contains(err.Error(), "not set up") {
		t.Fatalf("shipping through an idle provider = %v, want a refusal that says it is not set up", err)
	}

	on := true
	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{"api_key": "k-1"}}); err != nil {
		t.Fatalf("configure from the panel: %v", err)
	}
	for _, p := range app.Settings().FulfillmentProviders {
		if p.Code == "veeqo" && !p.Configured {
			t.Fatal("configured from the panel, the settings should say so")
		}
	}

}
