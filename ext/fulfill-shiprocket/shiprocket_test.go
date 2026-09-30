package shiprocket

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

// created is the part of the adhoc-order payload this package is responsible
// for getting right.
type created struct {
	Weight  float64 `json:"weight"`
	Length  float64 `json:"length"`
	Breadth float64 `json:"breadth"`
	Height  float64 `json:"height"`
}

// recorder keeps the last order Shiprocket was asked to create.
type recorder struct {
	mu   sync.Mutex
	last created
}

func (r *recorder) set(c created) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = c
}

func (r *recorder) get() created {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

func newApp(t *testing.T, cfg Config) (*gocommerce.App, *recorder) {
	t.Helper()
	rec := &recorder{}
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/external/auth/login":
			_, _ = io.WriteString(w, `{"token":"stub-token"}`)
		case "/v1/external/orders/create/adhoc":
			body, _ := io.ReadAll(r.Body)
			var c created
			_ = json.Unmarshal(body, &c)
			rec.set(c)
			_, _ = io.WriteString(w, `{"order_id":1,"shipment_id":2,"status":"NEW"}`)
		case "/v1/external/courier/assign/awb":
			_, _ = io.WriteString(w, `{"response":{"data":{"awb_code":"AWB123","courier_name":"Stub"}}}`)
		case "/v1/external/courier/generate/label":
			_, _ = io.WriteString(w, `{"label_url":"https://labels.example.test/1.pdf"}`)
		default:
			http.NotFound(w, r)
		}
	})

	cfg.Email, cfg.Password, cfg.PickupLocation = "a@example.test", "secret", "Primary"
	cfg.BaseURL = server.URL
	return gctest.New(t, New(cfg)), rec
}

// measuredProduct creates a product whose single variant has a real weight and
// a real size, which is the case the engine can answer exactly.
func measuredProduct(t *testing.T, app *gocommerce.App, sku string) *gocommerce.Variant {
	t.Helper()
	price := int64(2500)
	stock := 10
	mm := func(v int) *int { return &v }
	grams := 750

	product, err := app.Products().CreateProduct(context.Background(), gocommerce.ProductInput{
		Title: "measured " + sku, Status: "active",
		Variants: []gocommerce.VariantInput{{
			SKU: sku, PriceMinor: price, StockOnHand: &stock,
			WeightGrams: &grams,
			Dimensions:  gocommerce.Dimensions{Length: mm(300), Width: mm(200), Height: mm(100)},
		}},
	})
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	variant := product.DefaultVariant()
	if variant == nil {
		t.Fatal("the product has no sellable variant")
	}
	return variant
}

func placeAndShip(t *testing.T, app *gocommerce.App, variantID int64, quantity int, meta map[string]string) {
	t.Helper()
	ctx := context.Background()

	cart, err := app.Cart().Create(ctx, "")
	if err != nil {
		t.Fatalf("create cart: %v", err)
	}
	if _, err := app.Cart().AddLine(ctx, cart.Token, variantID, quantity); err != nil {
		t.Fatalf("add to cart: %v", err)
	}
	result, err := app.Order().Checkout(ctx, gocommerce.CodeCOD, gocommerce.CheckoutInput{
		CartID: cart.Token, Email: "buyer@example.test", Name: "Buyer",
		Address: gocommerce.Address{
			Line1: "1 Test Street", City: "Testville", PostalCode: "560001", Country: "IN",
		},
	}, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if _, err := app.Ship().Create(ctx, result.Order.ID, "shiprocket",
		gocommerce.ShipRequest{Meta: meta}); err != nil {
		t.Fatalf("ship: %v", err)
	}
}

// The bug this exists to prevent: a catalogue somebody took the trouble to
// weigh, and a config default declared to the carrier anyway.
func TestMeasuredVariantBeatsTheConfiguredDefault(t *testing.T) {
	app, rec := newApp(t, Config{
		DefaultWeightKg: 0.5,
		DefaultLengthCm: 15, DefaultBreadthCm: 15, DefaultHeightCm: 10,
	})
	variant := measuredProduct(t, app, "SR-MEASURED")
	placeAndShip(t, app, variant.ID, 1, nil)

	got := rec.get()
	if got.Weight != 0.75 {
		t.Errorf("weight = %v kg, want 0.75 — the variant's 750 g, not the 0.5 default", got.Weight)
	}
	if got.Length != 30 || got.Breadth != 20 || got.Height != 10 {
		t.Errorf("size = %v x %v x %v cm, want 30 x 20 x 10 from the variant",
			got.Length, got.Breadth, got.Height)
	}
}

// Two units weigh twice as much and are not twice as wide. See
// gocommerce.Parcel for why the engine stops at weight.
func TestTwoUnitsSumTheWeightAndFallBackOnSize(t *testing.T) {
	app, rec := newApp(t, Config{
		DefaultWeightKg: 0.5,
		DefaultLengthCm: 15, DefaultBreadthCm: 15, DefaultHeightCm: 10,
	})
	variant := measuredProduct(t, app, "SR-TWO")
	placeAndShip(t, app, variant.ID, 2, nil)

	got := rec.get()
	if got.Weight != 1.5 {
		t.Errorf("weight = %v kg, want 1.5 — mass is additive", got.Weight)
	}
	if got.Length != 15 || got.Breadth != 15 || got.Height != 10 {
		t.Errorf("size = %v x %v x %v cm, want the configured fallback: two boxes side by side are not one box",
			got.Length, got.Breadth, got.Height)
	}
}

// An unweighed variant leaves the engine with a floor rather than a weight, and
// a floor is the one thing that must not be declared to a carrier.
func TestUnweighedVariantUsesTheDefault(t *testing.T) {
	app, rec := newApp(t, Config{DefaultWeightKg: 0.4})
	product := gctest.CreateProduct(t, app, "SR-UNWEIGHED", 2500, 10)
	placeAndShip(t, app, product.DefaultVariant().ID, 3, nil)

	if got := rec.get().Weight; got != 1.2 {
		t.Errorf("weight = %v kg, want 1.2 — three units at the 0.4 default", got)
	}
}

// The operator is holding the box, so what they type wins over everything.
func TestOperatorMetaWins(t *testing.T) {
	app, rec := newApp(t, Config{
		DefaultWeightKg: 0.5,
		DefaultLengthCm: 15, DefaultBreadthCm: 15, DefaultHeightCm: 10,
	})
	variant := measuredProduct(t, app, "SR-META")
	placeAndShip(t, app, variant.ID, 1, map[string]string{
		"weight_kg": "2.2", "length_cm": "40",
	})

	got := rec.get()
	if got.Weight != 2.2 {
		t.Errorf("weight = %v kg, want the operator's 2.2", got.Weight)
	}
	if got.Length != 40 {
		t.Errorf("length = %v cm, want the operator's 40", got.Length)
	}
	// What they did not override still comes from the variant.
	if got.Breadth != 20 {
		t.Errorf("breadth = %v cm, want 20 from the variant", got.Breadth)
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
		if p.Code == "shiprocket" {
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
	if _, err := app.Ship().Create(ctx, result.Order.ID, "shiprocket", gocommerce.ShipRequest{Tracking: "T-1"}); err == nil || !strings.Contains(err.Error(), "not set up") {
		t.Fatalf("shipping through an idle provider = %v, want a refusal that says it is not set up", err)
	}

	on := true
	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{"email": "api@example.com", "password": "pw", "pickup_location": "Primary"}}); err != nil {
		t.Fatalf("configure from the panel: %v", err)
	}
	for _, p := range app.Settings().FulfillmentProviders {
		if p.Code == "shiprocket" && !p.Configured {
			t.Fatal("configured from the panel, the settings should say so")
		}
	}

}
