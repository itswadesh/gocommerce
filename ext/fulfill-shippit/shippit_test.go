package shippit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/misiki/gocommerce/core"
	"github.com/misiki/gocommerce/gctest"
)

// booking is the part of the payload this package is responsible for getting
// right.
type booking struct {
	Order struct {
		CourierType           string         `json:"courier_type"`
		CourierAllocation     string         `json:"courier_allocation"`
		DeliveryAddress       string         `json:"delivery_address"`
		DeliverySuburb        string         `json:"delivery_suburb"`
		DeliveryPostcode      string         `json:"delivery_postcode"`
		DeliveryCountryCode   string         `json:"delivery_country_code"`
		ReceiverName          string         `json:"receiver_name"`
		ReceiverContactNumber string         `json:"receiver_contact_number"`
		AuthorityToLeave      string         `json:"authority_to_leave"`
		RetailerInvoice       string         `json:"retailer_invoice"`
		UserAttributes        map[string]any `json:"user_attributes"`
		ParcelAttributes      []struct {
			Qty    int     `json:"qty"`
			Weight float64 `json:"weight"`
			Length float64 `json:"length"`
			Width  float64 `json:"width"`
			Depth  float64 `json:"depth"`
		} `json:"parcel_attributes"`
	} `json:"order"`
}

type stub struct {
	mu        sync.Mutex
	last      booking
	labelCode int
	noTrack   bool
	failWith  string
}

func (s *stub) get() booking { s.mu.Lock(); defer s.mu.Unlock(); return s.last }

func newApp(t *testing.T, cfg Config, shape func(*stub)) (*gocommerce.App, *stub) {
	t.Helper()
	st := &stub{}
	if shape != nil {
		shape(st)
	}
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer sp_test" {
			http.Error(w, `{"error":"bad key"}`, http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/3/orders":
			raw, _ := io.ReadAll(r.Body)
			var in booking
			_ = json.Unmarshal(raw, &in)
			st.mu.Lock()
			st.last = in
			failWith, noTrack := st.failWith, st.noTrack
			st.mu.Unlock()

			switch {
			case failWith != "":
				http.Error(w, `{"errors":{"delivery_postcode":["`+failWith+`"]}}`, http.StatusUnprocessableEntity)
			case noTrack:
				_, _ = io.WriteString(w, `{"response":{"id":26599,"state":"draft"}}`)
			default:
				_, _ = io.WriteString(w, `{"response":{"id":26599,"tracking_number":"PPu38Wz2TdoNj","courier_job_id":"30734876324","courier_name":"eParcel","state":"processing"}}`)
			}

		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/label"):
			st.mu.Lock()
			code := st.labelCode
			st.mu.Unlock()
			if code != 0 && code != http.StatusOK {
				http.Error(w, `{"error":"label not ready"}`, code)
				return
			}
			_, _ = io.WriteString(w, `{"response":{"qualified_url":"https://labels.example.test/PPu38Wz2TdoNj.pdf"}}`)

		default:
			http.NotFound(w, r)
		}
	})

	cfg.APIKey = "sp_test"
	cfg.BaseURL = server.URL + "/api/3"
	if cfg.CourierType == "" {
		cfg.CourierType = "standard"
	}
	return gctest.New(t, New(cfg)), st
}

// The module's reason to exist, and the unit trap that comes with it: Shippit
// reads metres, so a 305 mm box is 0.305 and not 30.5.
func TestBookingCarriesTheParcelInKilogramsAndMetres(t *testing.T) {
	app, st := newApp(t, Config{}, nil)
	product := gctest.MeasuredProduct(t, app, "SPT-1", 2500, 10, 750, 305, 200, 100)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	order := gctest.Ship(t, app, result.Order.ID, "shippit", gocommerce.ShipRequest{})
	shipment := order.Fulfillments[0]
	if shipment.Tracking != "PPu38Wz2TdoNj" {
		t.Errorf("tracking = %q, want the number Shippit returned", shipment.Tracking)
	}
	// eParcel is Australia Post's parcel service; the parcel tracks there.
	if shipment.Carrier != "australia-post" {
		t.Errorf("carrier = %q, want australia-post", shipment.Carrier)
	}
	if shipment.LabelURL != "https://labels.example.test/PPu38Wz2TdoNj.pdf" {
		t.Errorf("label = %q, want the pre-signed URL", shipment.LabelURL)
	}

	sent := st.get().Order
	if sent.RetailerInvoice != result.Order.Number {
		t.Errorf("retailer_invoice = %q, want the order number", sent.RetailerInvoice)
	}
	if sent.CourierType != "standard" {
		t.Errorf("courier_type = %q, want the configured band", sent.CourierType)
	}
	if len(sent.ParcelAttributes) != 1 {
		t.Fatalf("parcel_attributes = %d, want 1", len(sent.ParcelAttributes))
	}
	parcel := sent.ParcelAttributes[0]
	// One parcel, whatever is in it: telling Shippit "2" books two boxes.
	if parcel.Qty != 1 {
		t.Errorf("qty = %d, want 1 — this is one box", parcel.Qty)
	}
	if parcel.Weight != 0.75 {
		t.Errorf("weight = %v kg, want 0.75 from 750 g", parcel.Weight)
	}
	if parcel.Length != 0.305 || parcel.Width != 0.2 || parcel.Depth != 0.1 {
		t.Errorf("size = %v x %v x %v m, want 0.305 x 0.2 x 0.1 — Shippit reads metres",
			parcel.Length, parcel.Width, parcel.Depth)
	}
	if sent.DeliveryPostcode != "94117" || sent.DeliverySuburb != "Testville" {
		t.Errorf("address = %+v, want the order's", sent)
	}
	if sent.UserAttributes["first_name"] != "GC" || sent.UserAttributes["last_name"] != "Test" {
		t.Errorf("user_attributes = %+v, want the buyer's name split", sent.UserAttributes)
	}
	// Authorising a courier to leave a parcel is the shopper's call.
	if sent.AuthorityToLeave != "No" {
		t.Errorf("authority_to_leave = %q, want No by default", sent.AuthorityToLeave)
	}
}

func TestTwoUnitsFallBackOnTheConfiguredBox(t *testing.T) {
	app, st := newApp(t, Config{DefaultLengthMM: 150, DefaultWidthMM: 150, DefaultHeightMM: 100}, nil)
	product := gctest.MeasuredProduct(t, app, "SPT-2", 2500, 10, 750, 305, 200, 100)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 2)
	gctest.Ship(t, app, result.Order.ID, "shippit", gocommerce.ShipRequest{})

	parcel := st.get().Order.ParcelAttributes[0]
	if parcel.Weight != 1.5 {
		t.Errorf("weight = %v kg, want 1.5 — mass is additive", parcel.Weight)
	}
	if parcel.Length != 0.15 {
		t.Errorf("length = %v m, want the configured 0.15", parcel.Length)
	}
	if parcel.Qty != 1 {
		t.Errorf("qty = %d, want 1 — two items in one box is still one box", parcel.Qty)
	}
}

// The booking is what matters; a label that cannot be fetched must not undo it.
func TestAMissingLabelStillShips(t *testing.T) {
	app, _ := newApp(t, Config{}, func(s *stub) { s.labelCode = http.StatusNotFound })
	product := gctest.CreateProduct(t, app, "SPT-3", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	order := gctest.Ship(t, app, result.Order.ID, "shippit", gocommerce.ShipRequest{})
	shipment := order.Fulfillments[0]
	if shipment.Tracking == "" {
		t.Error("the shipment lost its tracking number over a missing label")
	}
	if shipment.LabelURL != "" {
		t.Errorf("label = %q, want none", shipment.LabelURL)
	}
}

// A booking with no tracking number is half-made, and the order must not move.
func TestBookingWithoutTrackingIsAFailure(t *testing.T) {
	app, _ := newApp(t, Config{}, func(s *stub) { s.noTrack = true })
	product := gctest.CreateProduct(t, app, "SPT-4", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	if _, err := app.Ship().Create(t.Context(), result.Order.ID, "shippit", gocommerce.ShipRequest{}); err == nil {
		t.Error("a booking with no tracking number was accepted")
	}
}

func TestValidationErrorsAreReported(t *testing.T) {
	app, _ := newApp(t, Config{}, func(s *stub) { s.failWith = "is not a valid postcode" })
	product := gctest.CreateProduct(t, app, "SPT-5", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	_, err := app.Ship().Create(t.Context(), result.Order.ID, "shippit", gocommerce.ShipRequest{})
	if err == nil || !strings.Contains(err.Error(), "delivery_postcode is not a valid postcode") {
		t.Errorf("error = %v, want the field and the reason in it", err)
	}
}

// The shopper's answer about leaving a parcel at the door beats the store's
// default, because it is their risk.
func TestMetaOverridesTheDefaults(t *testing.T) {
	app, st := newApp(t, Config{AuthorityToLeave: true}, nil)
	product := gctest.CreateProduct(t, app, "SPT-6", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	gctest.Ship(t, app, result.Order.ID, "shippit", gocommerce.ShipRequest{
		Meta: map[string]string{
			"authority_to_leave": "no",
			"courier_type":       "express",
			"courier_allocation": "Couriers Please",
		},
	})
	sent := st.get().Order
	if sent.AuthorityToLeave != "No" {
		t.Errorf("authority_to_leave = %q, want the shopper's No over the store's default", sent.AuthorityToLeave)
	}
	if sent.CourierType != "express" || sent.CourierAllocation != "Couriers Please" {
		t.Errorf("courier = %q / %q, want the operator's choice", sent.CourierType, sent.CourierAllocation)
	}
}

func TestCarrierFor(t *testing.T) {
	t.Parallel()
	for courier, want := range map[string]string{
		"eParcel":         "australia-post",
		"StarTrack":       "australia-post",
		"Australia Post":  "australia-post",
		"Fedex":           "fedex",
		"DHL eCommerce":   "dhl",
		"Couriers Please": "",
		"":                "",
	} {
		if got := carrierFor(courier); got != want {
			t.Errorf("carrierFor(%q) = %q, want %q", courier, got, want)
		}
	}
}

func TestErrorList(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ raw, want string }{
		{`["one","two"]`, "one; two"},
		// Sorted, so the same failure reads the same way twice.
		{`{"b":["second"],"a":["first"]}`, "a first; b second"},
		{``, ""},
	} {
		if got := errorList([]byte(tc.raw)); got != tc.want {
			t.Errorf("errorList(%q) = %q, want %q", tc.raw, got, tc.want)
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
		if p.Code == "shippit" {
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
	if _, err := app.Ship().Create(ctx, result.Order.ID, "shippit", gocommerce.ShipRequest{Tracking: "T-1"}); err == nil || !strings.Contains(err.Error(), "not set up") {
		t.Fatalf("shipping through an idle provider = %v, want a refusal that says it is not set up", err)
	}

	on := true
	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{"api_key": "k-1", "courier_type": "standard"}}); err != nil {
		t.Fatalf("configure from the panel: %v", err)
	}
	for _, p := range app.Settings().FulfillmentProviders {
		if p.Code == "shippit" && !p.Configured {
			t.Fatal("configured from the panel, the settings should say so")
		}
	}

}
