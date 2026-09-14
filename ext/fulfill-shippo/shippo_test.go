package shippo

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/misiki/gocommerce/core"
	"github.com/misiki/gocommerce/gctest"
)

// purchase is the part of the transaction payload this package is responsible
// for getting right.
type purchase struct {
	CarrierAccount    string `json:"carrier_account"`
	ServicelevelToken string `json:"servicelevel_token"`
	Async             bool   `json:"async"`
	Shipment          struct {
		AddressFrom map[string]any `json:"address_from"`
		AddressTo   map[string]any `json:"address_to"`
		Parcels     []struct {
			Length       int    `json:"length"`
			Width        int    `json:"width"`
			Height       int    `json:"height"`
			DistanceUnit string `json:"distance_unit"`
			Weight       int    `json:"weight"`
			MassUnit     string `json:"mass_unit"`
		} `json:"parcels"`
	} `json:"shipment"`
}

type stub struct {
	mu     sync.Mutex
	last   purchase
	status string
}

func (s *stub) set(p purchase) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = p
}

func (s *stub) get() purchase {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func (s *stub) setStatus(status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

func (s *stub) getStatus() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == "" {
		return "SUCCESS"
	}
	return s.status
}

func newApp(t *testing.T, cfg Config) (*gocommerce.App, *stub) {
	t.Helper()
	st := &stub{}
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/transactions" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "ShippoToken shippo_test" {
			http.Error(w, `{"detail":"bad token"}`, http.StatusUnauthorized)
			return
		}
		if r.Header.Get("SHIPPO-API-VERSION") == "" {
			// An unpinned client gets whatever Shippo is serving that week.
			http.Error(w, `{"detail":"no API version pinned"}`, http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var p purchase
		_ = json.Unmarshal(body, &p)
		st.set(p)

		switch st.getStatus() {
		case "ERROR":
			_, _ = io.WriteString(w, `{"object_id":"tx_err","status":"ERROR","messages":[{"source":"USPS","code":"0001","text":"Address is undeliverable"}]}`)
		case "NO_TRACKING":
			_, _ = io.WriteString(w, `{"object_id":"tx_bare","status":"SUCCESS","label_url":"https://labels.example.test/x.pdf"}`)
		default:
			_, _ = io.WriteString(w, `{"object_id":"tx_1","status":"SUCCESS","tracking_number":"9400111899223197428490","label_url":"https://labels.example.test/1.pdf","tracking_url_provider":"https://tools.usps.com/x"}`)
		}
	})

	cfg.APIKey = "shippo_test"
	cfg.BaseURL = server.URL
	if cfg.CarrierAccount == "" {
		cfg.CarrierAccount = "acct_1"
	}
	if cfg.ServicelevelToken == "" {
		cfg.ServicelevelToken = "usps_priority"
	}
	if cfg.From.Street1 == "" {
		cfg.From = Address{
			Name: "Acme", Street1: "215 Clayton St", City: "San Francisco",
			State: "CA", Zip: "94117", Country: "US", Phone: "+15553334444",
		}
	}
	return gctest.New(t, New(cfg)), st
}

func TestRegisterRequiresConfiguration(t *testing.T) {
	t.Parallel()

	from := Address{Street1: "1 A St", Country: "US"}
	for _, cfg := range []Config{
		{},
		{APIKey: "k"},
		{APIKey: "k", CarrierAccount: "a"},
		{APIKey: "k", CarrierAccount: "a", ServicelevelToken: "usps_priority"}, // no From
		{APIKey: "k", CarrierAccount: "a", ServicelevelToken: "usps_priority",
			From: Address{Street1: "1 A St"}}, // no country
		{APIKey: "k", CarrierAccount: "a", From: from}, // no service level
	} {
		if err := New(cfg).Register(nil); err == nil {
			t.Errorf("Register accepted an incomplete config: %+v", cfg)
		}
	}
}

// The module's reason to exist: a measured variant reaches the carrier as its
// own weight and size, in the units the engine already keeps them in.
func TestMeasuredParcelReachesShippoUnconverted(t *testing.T) {
	app, st := newApp(t, Config{DefaultWeightGrams: 500, DefaultLengthMM: 150, DefaultWidthMM: 150, DefaultHeightMM: 100})
	product := gctest.MeasuredProduct(t, app, "SHIPPO-1", 2500, 10, 750, 300, 200, 100)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	order := gctest.Ship(t, app, result.Order.ID, "shippo", gocommerce.ShipRequest{})
	if len(order.Fulfillments) != 1 {
		t.Fatalf("fulfillments = %d, want 1", len(order.Fulfillments))
	}
	shipment := order.Fulfillments[0]
	if shipment.Tracking != "9400111899223197428490" {
		t.Errorf("tracking = %q, want the number Shippo returned", shipment.Tracking)
	}
	if shipment.Carrier != "usps" {
		t.Errorf("carrier = %q, want usps, derived from the service level", shipment.Carrier)
	}
	if shipment.LabelURL != "https://labels.example.test/1.pdf" {
		t.Errorf("label = %q, want the URL Shippo returned", shipment.LabelURL)
	}

	sent := st.get()
	if sent.Async {
		t.Error("async was requested — a QUEUED purchase leaves the order shipped with no tracking number")
	}
	if len(sent.Shipment.Parcels) != 1 {
		t.Fatalf("parcels = %d, want 1", len(sent.Shipment.Parcels))
	}
	parcel := sent.Shipment.Parcels[0]
	if parcel.Weight != 750 || parcel.MassUnit != "g" {
		t.Errorf("weight = %d %s, want 750 g straight from the variant", parcel.Weight, parcel.MassUnit)
	}
	if parcel.Length != 300 || parcel.Width != 200 || parcel.Height != 100 || parcel.DistanceUnit != "mm" {
		t.Errorf("size = %d x %d x %d %s, want 300 x 200 x 100 mm from the variant",
			parcel.Length, parcel.Width, parcel.Height, parcel.DistanceUnit)
	}
	if got := sent.Shipment.AddressTo["zip"]; got != "94117" {
		t.Errorf("address_to zip = %v, want the order's", got)
	}
	if got := sent.Shipment.AddressFrom["street1"]; got != "215 Clayton St" {
		t.Errorf("address_from street = %v, want the configured ship-from", got)
	}
}

// Two units weigh twice as much and are not twice as wide.
func TestTwoUnitsFallBackOnTheConfiguredBox(t *testing.T) {
	app, st := newApp(t, Config{DefaultWeightGrams: 500, DefaultLengthMM: 150, DefaultWidthMM: 150, DefaultHeightMM: 100})
	product := gctest.MeasuredProduct(t, app, "SHIPPO-2", 2500, 10, 750, 300, 200, 100)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 2)
	gctest.Ship(t, app, result.Order.ID, "shippo", gocommerce.ShipRequest{})

	parcel := st.get().Shipment.Parcels[0]
	if parcel.Weight != 1500 {
		t.Errorf("weight = %d g, want 1500 — mass is additive", parcel.Weight)
	}
	if parcel.Length != 150 || parcel.Width != 150 || parcel.Height != 100 {
		t.Errorf("size = %d x %d x %d mm, want the configured box: two items are not one wider item",
			parcel.Length, parcel.Width, parcel.Height)
	}
}

// An unweighed catalogue leaves the engine with a floor, and a floor must not
// reach a carrier as if it were the parcel's weight.
func TestUnweighedVariantUsesTheDefault(t *testing.T) {
	app, st := newApp(t, Config{DefaultWeightGrams: 400})
	product := gctest.CreateProduct(t, app, "SHIPPO-3", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 3)
	gctest.Ship(t, app, result.Order.ID, "shippo", gocommerce.ShipRequest{})

	if got := st.get().Shipment.Parcels[0].Weight; got != 1200 {
		t.Errorf("weight = %d g, want 1200 — three units at the 400 g default", got)
	}
}

// The operator can choose a different service for one parcel without the store
// being reconfigured.
func TestMetaOverridesTheServiceLevel(t *testing.T) {
	app, st := newApp(t, Config{})
	product := gctest.CreateProduct(t, app, "SHIPPO-4", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	order := gctest.Ship(t, app, result.Order.ID, "shippo", gocommerce.ShipRequest{
		Meta: map[string]string{"servicelevel_token": "fedex_2_day", "carrier_account": "acct_fedex"},
	})
	sent := st.get()
	if sent.ServicelevelToken != "fedex_2_day" || sent.CarrierAccount != "acct_fedex" {
		t.Errorf("sent %q on %q, want the operator's choice", sent.ServicelevelToken, sent.CarrierAccount)
	}
	if got := order.Fulfillments[0].Carrier; got != "fedex" {
		t.Errorf("carrier = %q, want fedex", got)
	}
}

// Shippo answers 200 with status ERROR, so the HTTP code alone would report a
// failed purchase as a shipped order.
func TestErrorStatusIsAFailure(t *testing.T) {
	app, st := newApp(t, Config{})
	st.setStatus("ERROR")
	product := gctest.CreateProduct(t, app, "SHIPPO-5", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	_, err := app.Ship().Create(t.Context(), result.Order.ID, "shippo", gocommerce.ShipRequest{})
	if err == nil {
		t.Fatal("a Shippo ERROR transaction was treated as a shipment")
	}
	order, getErr := app.Order().Get(t.Context(), result.Order.ID)
	if getErr != nil {
		t.Fatalf("get order: %v", getErr)
	}
	if len(order.Fulfillments) != 0 {
		t.Errorf("fulfillments = %d, want none", len(order.Fulfillments))
	}
	if order.Status == gocommerce.OrderShipped {
		t.Error("the order moved to shipped on a failed label purchase")
	}
}

// A success with no tracking number is not a shipment anyone can act on.
func TestSuccessWithoutTrackingIsAFailure(t *testing.T) {
	app, st := newApp(t, Config{})
	st.setStatus("NO_TRACKING")
	product := gctest.CreateProduct(t, app, "SHIPPO-6", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	if _, err := app.Ship().Create(t.Context(), result.Order.ID, "shippo", gocommerce.ShipRequest{}); err == nil {
		t.Error("a transaction with no tracking number was accepted")
	}
}

func TestCarrierFor(t *testing.T) {
	t.Parallel()
	for token, want := range map[string]string{
		"usps_priority":         "usps",
		"ups_ground":            "ups",
		"fedex_2_day":           "fedex",
		"dhl_express_worldwide": "dhl",
		"canada_post_expedited": "canada-post",
		// Unknown is not a failure: the engine works it out from the number.
		"correios_pac": "",
		"":             "",
	} {
		if got := carrierFor(token); got != want {
			t.Errorf("carrierFor(%q) = %q, want %q", token, got, want)
		}
	}
}
