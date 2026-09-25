package shipstation

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

// labelRequest is the part of the payload this package is responsible for
// getting right.
type labelRequest struct {
	Shipment struct {
		ServiceCode        string         `json:"service_code"`
		ExternalShipmentID string         `json:"external_shipment_id"`
		ShipFrom           map[string]any `json:"ship_from"`
		ShipTo             map[string]any `json:"ship_to"`
		Packages           []struct {
			Weight struct {
				Value float64 `json:"value"`
				Unit  string  `json:"unit"`
			} `json:"weight"`
			Dimensions struct {
				Length float64 `json:"length"`
				Width  float64 `json:"width"`
				Height float64 `json:"height"`
				Unit   string  `json:"unit"`
			} `json:"dimensions"`
		} `json:"packages"`
	} `json:"shipment"`
}

type stub struct {
	mu       sync.Mutex
	last     labelRequest
	noTrack  bool
	failWith string
}

func (s *stub) set(r labelRequest) { s.mu.Lock(); defer s.mu.Unlock(); s.last = r }
func (s *stub) get() labelRequest  { s.mu.Lock(); defer s.mu.Unlock(); return s.last }

func newApp(t *testing.T, cfg Config, shape func(*stub)) (*gocommerce.App, *stub) {
	t.Helper()
	st := &stub{}
	if shape != nil {
		shape(st)
	}
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v2/labels" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("API-Key") != "ss_test" {
			http.Error(w, `{"errors":[{"message":"bad key","error_code":"unauthorized"}]}`, http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var in labelRequest
		_ = json.Unmarshal(body, &in)
		st.set(in)

		st.mu.Lock()
		failWith, noTrack := st.failWith, st.noTrack
		st.mu.Unlock()

		switch {
		case failWith != "":
			http.Error(w, `{"errors":[{"message":"`+failWith+`","error_code":"invalid_address"}]}`, http.StatusBadRequest)
		case noTrack:
			_, _ = io.WriteString(w, `{"label_id":"se-1","status":"processing"}`)
		default:
			_, _ = io.WriteString(w, `{"label_id":"se-1","status":"completed","shipment_id":"se-ship-1","tracking_number":"9405511899223197428490","carrier_code":"stamps_com","label_download":{"pdf":"https://labels.example.test/1.pdf"}}`)
		}
	})

	cfg.APIKey = "ss_test"
	cfg.BaseURL = server.URL
	if cfg.ServiceCode == "" {
		cfg.ServiceCode = "usps_priority_mail"
	}
	if cfg.From.AddressLine1 == "" {
		cfg.From = Address{
			Name: "Acme", AddressLine1: "215 Clayton St", CityLocality: "San Francisco",
			StateProvince: "CA", PostalCode: "94117", CountryCode: "US", Phone: "+15553334444",
		}
	}
	return gctest.New(t, New(cfg)), st
}

// The module's reason to exist: a measured variant reaches ShipStation as its
// own weight and size, converted once and exactly.
func TestMeasuredParcelReachesShipStation(t *testing.T) {
	app, st := newApp(t, Config{DefaultWeightGrams: 500, DefaultLengthMM: 150, DefaultWidthMM: 150, DefaultHeightMM: 100}, nil)
	product := gctest.MeasuredProduct(t, app, "SS-1", 2500, 10, 750, 305, 200, 100)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	order := gctest.Ship(t, app, result.Order.ID, "shipstation", gocommerce.ShipRequest{})
	shipment := order.Fulfillments[0]
	if shipment.Tracking != "9405511899223197428490" {
		t.Errorf("tracking = %q, want the number ShipStation returned", shipment.Tracking)
	}
	// stamps_com is a USPS reseller: the parcel is carried by USPS and tracks
	// on USPS, whatever the account is called.
	if shipment.Carrier != "usps" {
		t.Errorf("carrier = %q, want usps", shipment.Carrier)
	}
	if shipment.LabelURL != "https://labels.example.test/1.pdf" {
		t.Errorf("label = %q, want the PDF ShipStation returned", shipment.LabelURL)
	}

	sent := st.get().Shipment
	if sent.ExternalShipmentID != result.Order.Number {
		t.Errorf("external_shipment_id = %q, want the order number", sent.ExternalShipmentID)
	}
	pkg := sent.Packages[0]
	if pkg.Weight.Value != 750 || pkg.Weight.Unit != "gram" {
		t.Errorf("weight = %v %s, want 750 gram straight from the variant", pkg.Weight.Value, pkg.Weight.Unit)
	}
	// 305 mm is 30.5 cm. Rounding it down is how a parcel gets refused for
	// being over its band.
	if pkg.Dimensions.Length != 30.5 || pkg.Dimensions.Width != 20 || pkg.Dimensions.Height != 10 ||
		pkg.Dimensions.Unit != "centimeter" {
		t.Errorf("size = %v x %v x %v %s, want 30.5 x 20 x 10 centimeter",
			pkg.Dimensions.Length, pkg.Dimensions.Width, pkg.Dimensions.Height, pkg.Dimensions.Unit)
	}
	if got := sent.ShipTo["postal_code"]; got != "94117" {
		t.Errorf("ship_to postal_code = %v, want the order's", got)
	}
	if got := sent.ShipFrom["address_line1"]; got != "215 Clayton St" {
		t.Errorf("ship_from = %v, want the configured ship-from", got)
	}
}

func TestTwoUnitsFallBackOnTheConfiguredBox(t *testing.T) {
	app, st := newApp(t, Config{DefaultWeightGrams: 500, DefaultLengthMM: 150, DefaultWidthMM: 150, DefaultHeightMM: 100}, nil)
	product := gctest.MeasuredProduct(t, app, "SS-2", 2500, 10, 750, 300, 200, 100)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 2)
	gctest.Ship(t, app, result.Order.ID, "shipstation", gocommerce.ShipRequest{})

	pkg := st.get().Shipment.Packages[0]
	if pkg.Weight.Value != 1500 {
		t.Errorf("weight = %v g, want 1500 — mass is additive", pkg.Weight.Value)
	}
	if pkg.Dimensions.Length != 15 {
		t.Errorf("length = %v cm, want the configured 15", pkg.Dimensions.Length)
	}
}

// A label ShipStation is still processing has no tracking number, and a
// shipment nobody can track is not a shipment.
func TestLabelWithoutTrackingIsAFailure(t *testing.T) {
	app, _ := newApp(t, Config{}, func(s *stub) { s.noTrack = true })
	product := gctest.CreateProduct(t, app, "SS-3", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	if _, err := app.Ship().Create(t.Context(), result.Order.ID, "shipstation", gocommerce.ShipRequest{}); err == nil {
		t.Error("a label with no tracking number was accepted")
	}
}

// ShipStation's error array is the only place the reason lives, and an
// operator with "400" alone has nothing to act on.
func TestErrorsAreReported(t *testing.T) {
	app, _ := newApp(t, Config{}, func(s *stub) { s.failWith = "Address is undeliverable" })
	product := gctest.CreateProduct(t, app, "SS-4", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	_, err := app.Ship().Create(t.Context(), result.Order.ID, "shipstation", gocommerce.ShipRequest{})
	if err == nil {
		t.Fatal("a rejected label was treated as a shipment")
	}
	if !strings.Contains(err.Error(), "Address is undeliverable") {
		t.Errorf("error = %v, want ShipStation's own reason in it", err)
	}
}

func TestMetaOverridesTheServiceCode(t *testing.T) {
	app, st := newApp(t, Config{}, nil)
	product := gctest.CreateProduct(t, app, "SS-5", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	gctest.Ship(t, app, result.Order.ID, "shipstation", gocommerce.ShipRequest{
		Meta: map[string]string{"service_code": "ups_ground"},
	})
	if got := st.get().Shipment.ServiceCode; got != "ups_ground" {
		t.Errorf("service_code = %q, want the operator's choice", got)
	}
}

func TestCarrierFor(t *testing.T) {
	t.Parallel()
	for code, want := range map[string]string{
		"stamps_com":     "usps",
		"usps":           "usps",
		"endicia":        "usps",
		"ups_walleted":   "ups",
		"fedex":          "fedex",
		"dhl_express":    "dhl",
		"canada_post":    "canada-post",
		"seko_ecommerce": "",
		"":               "",
	} {
		if got := carrierFor(code); got != want {
			t.Errorf("carrierFor(%q) = %q, want %q", code, got, want)
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
		if p.Code == "shipstation" {
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
	if _, err := app.Ship().Create(ctx, result.Order.ID, "shipstation", gocommerce.ShipRequest{Tracking: "T-1"}); err == nil || !strings.Contains(err.Error(), "not set up") {
		t.Fatalf("shipping through an idle provider = %v, want a refusal that says it is not set up", err)
	}

	on := true
	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{"api_key": "k-1", "service_code": "usps_priority_mail", "from_address_line1": "1 Ship St", "from_country_code": "US"}}); err != nil {
		t.Fatalf("configure from the panel: %v", err)
	}
	for _, p := range app.Settings().FulfillmentProviders {
		if p.Code == "shipstation" && !p.Configured {
			t.Fatal("configured from the panel, the settings should say so")
		}
	}

}
