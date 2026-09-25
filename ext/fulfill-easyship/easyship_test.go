package easyship

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

// shipmentRequest is the part of the payload this package is responsible for
// getting right.
type shipmentRequest struct {
	CourierServiceID   string         `json:"courier_service_id"`
	BuyLabel           bool           `json:"buy_label"`
	OriginAddress      map[string]any `json:"origin_address"`
	DestinationAddress map[string]any `json:"destination_address"`
	OrderData          map[string]any `json:"order_data"`
	Parcels            []struct {
		TotalActualWeight float64 `json:"total_actual_weight"`
		Box               struct {
			OuterDimensions struct {
				Length float64 `json:"length"`
				Width  float64 `json:"width"`
				Height float64 `json:"height"`
			} `json:"outer_dimensions"`
		} `json:"box"`
		Items []struct {
			Description          string  `json:"description"`
			SKU                  string  `json:"sku"`
			Quantity             int     `json:"quantity"`
			DeclaredCustomsValue float64 `json:"declared_customs_value"`
			DeclaredCurrency     string  `json:"declared_currency"`
		} `json:"items"`
	} `json:"parcels"`
}

type stub struct {
	mu       sync.Mutex
	last     shipmentRequest
	noLabel  bool
	failWith string
}

func (s *stub) set(r shipmentRequest) { s.mu.Lock(); defer s.mu.Unlock(); s.last = r }
func (s *stub) get() shipmentRequest  { s.mu.Lock(); defer s.mu.Unlock(); return s.last }

func newApp(t *testing.T, cfg Config, shape func(*stub)) (*gocommerce.App, *stub) {
	t.Helper()
	st := &stub{}
	if shape != nil {
		shape(st)
	}
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/2024-09/shipments" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer es_test" {
			http.Error(w, `{"error":{"message":"bad token"}}`, http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var in shipmentRequest
		_ = json.Unmarshal(body, &in)
		st.set(in)

		st.mu.Lock()
		failWith, noLabel := st.failWith, st.noLabel
		st.mu.Unlock()

		switch {
		case failWith != "":
			http.Error(w, `{"error":{"code":"invalid","message":"`+failWith+`"}}`, http.StatusUnprocessableEntity)
		case noLabel:
			_, _ = io.WriteString(w, `{"shipment":{"easyship_shipment_id":"ESSG10006002","label_state":"failed","trackings":[]}}`)
		default:
			_, _ = io.WriteString(w, `{"shipment":{"easyship_shipment_id":"ESSG10006002","label_state":"generated",
				"courier_service":{"name":"Courier 1","umbrella_name":"DHL"},
				"trackings":[{"tracking_number":"794870954852","handler":"aramex","tracking_state":"active"}],
				"shipping_documents":[
					{"category":"commercial_invoice","format":"url","url":"https://docs.example.test/ci.pdf"},
					{"category":"label","format":"url","url":"https://docs.example.test/label.pdf"}]}}`)
		}
	})

	cfg.Token = "es_test"
	cfg.BaseURL = server.URL
	if cfg.CourierServiceID == "" {
		cfg.CourierServiceID = "courier-1"
	}
	if cfg.From.Line1 == "" {
		cfg.From = Address{
			ContactName: "Acme", Line1: "Kennedy Town", City: "Hong Kong",
			PostalCode: "0000", CountryAlpha2: "HK", ContactPhone: "+852-3008-5678",
		}
	}
	return gctest.New(t, New(cfg)), st
}

// The module's reason to exist: a measured variant reaches Easyship in the
// account's units, and the label comes back with the shipment.
func TestMeasuredParcelReachesEasyshipInMetric(t *testing.T) {
	app, st := newApp(t, Config{}, nil)
	product := gctest.MeasuredProduct(t, app, "ES-1", 2500, 10, 750, 305, 200, 100)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	order := gctest.Ship(t, app, result.Order.ID, "easyship", gocommerce.ShipRequest{})
	shipment := order.Fulfillments[0]
	if shipment.Tracking != "794870954852" {
		t.Errorf("tracking = %q, want the number Easyship returned", shipment.Tracking)
	}
	if shipment.Carrier != "aramex" {
		t.Errorf("carrier = %q, want aramex, from the tracking handler", shipment.Carrier)
	}
	// The commercial invoice is in the same list and is not what goes on the box.
	if shipment.LabelURL != "https://docs.example.test/label.pdf" {
		t.Errorf("label = %q, want the document categorised as a label", shipment.LabelURL)
	}

	sent := st.get()
	if !sent.BuyLabel {
		t.Error("buy_label was not set — a shipment with no label is a state the engine cannot hold")
	}
	if sent.OrderData["platform_order_number"] != result.Order.Number {
		t.Errorf("platform_order_number = %v, want the order number", sent.OrderData["platform_order_number"])
	}
	parcel := sent.Parcels[0]
	if parcel.TotalActualWeight != 0.75 {
		t.Errorf("weight = %v, want 0.75 kg from 750 g", parcel.TotalActualWeight)
	}
	// 305 mm is 30.5 cm; rounding it down is how a parcel is refused for being
	// over its band.
	if parcel.Box.OuterDimensions.Length != 30.5 || parcel.Box.OuterDimensions.Width != 20 ||
		parcel.Box.OuterDimensions.Height != 10 {
		t.Errorf("box = %v x %v x %v cm, want 30.5 x 20 x 10",
			parcel.Box.OuterDimensions.Length, parcel.Box.OuterDimensions.Width,
			parcel.Box.OuterDimensions.Height)
	}
	if len(parcel.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(parcel.Items))
	}
	item := parcel.Items[0]
	if item.Quantity != 1 || item.SKU != "ES-1" || item.DeclaredCustomsValue != 25 || item.DeclaredCurrency == "" {
		t.Errorf("item = %+v, want one ES-1 declared at 25 in the order's currency", item)
	}
}

// Get the account's unit system wrong and every parcel is declared at about
// 2.2 times the wrong weight, so the switch has to actually switch.
func TestImperialAccountSendsPoundsAndInches(t *testing.T) {
	app, st := newApp(t, Config{Imperial: true}, nil)
	product := gctest.MeasuredProduct(t, app, "ES-2", 2500, 10, 1000, 254, 254, 254)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)
	gctest.Ship(t, app, result.Order.ID, "easyship", gocommerce.ShipRequest{})

	parcel := st.get().Parcels[0]
	if parcel.TotalActualWeight != 2.205 {
		t.Errorf("weight = %v lb, want 2.205 from 1000 g", parcel.TotalActualWeight)
	}
	if parcel.Box.OuterDimensions.Length != 10 {
		t.Errorf("length = %v in, want 10 from 254 mm", parcel.Box.OuterDimensions.Length)
	}
}

func TestTwoUnitsFallBackOnTheConfiguredBox(t *testing.T) {
	app, st := newApp(t, Config{DefaultLengthMM: 150, DefaultWidthMM: 150, DefaultHeightMM: 100}, nil)
	product := gctest.MeasuredProduct(t, app, "ES-3", 2500, 10, 750, 300, 200, 100)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 2)
	gctest.Ship(t, app, result.Order.ID, "easyship", gocommerce.ShipRequest{})

	parcel := st.get().Parcels[0]
	if parcel.TotalActualWeight != 1.5 {
		t.Errorf("weight = %v kg, want 1.5 — mass is additive", parcel.TotalActualWeight)
	}
	if parcel.Box.OuterDimensions.Length != 15 {
		t.Errorf("length = %v cm, want the configured 15", parcel.Box.OuterDimensions.Length)
	}
	if parcel.Items[0].Quantity != 2 {
		t.Errorf("declared quantity = %d, want 2", parcel.Items[0].Quantity)
	}
}

// A shipment that exists with no label is the half-booked state the engine has
// no room for, and an operator has to be told which shipment to cancel.
func TestShipmentWithoutALabelIsAFailure(t *testing.T) {
	app, _ := newApp(t, Config{}, func(s *stub) { s.noLabel = true })
	product := gctest.CreateProduct(t, app, "ES-4", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	_, err := app.Ship().Create(t.Context(), result.Order.ID, "easyship", gocommerce.ShipRequest{})
	if err == nil {
		t.Fatal("a shipment with no label was accepted")
	}
	if !strings.Contains(err.Error(), "ESSG10006002") {
		t.Errorf("error = %v, want the Easyship id in it so the shipment can be cancelled", err)
	}
}

func TestErrorsAreReported(t *testing.T) {
	app, _ := newApp(t, Config{}, func(s *stub) { s.failWith = "Destination postal code is invalid" })
	product := gctest.CreateProduct(t, app, "ES-5", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	_, err := app.Ship().Create(t.Context(), result.Order.ID, "easyship", gocommerce.ShipRequest{})
	if err == nil || !strings.Contains(err.Error(), "postal code is invalid") {
		t.Errorf("error = %v, want Easyship's own reason in it", err)
	}
}

func TestMetaOverridesTheCourier(t *testing.T) {
	app, st := newApp(t, Config{}, nil)
	product := gctest.CreateProduct(t, app, "ES-6", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	gctest.Ship(t, app, result.Order.ID, "easyship", gocommerce.ShipRequest{
		Meta: map[string]string{"courier_service_id": "courier-express"},
	})
	if got := st.get().CourierServiceID; got != "courier-express" {
		t.Errorf("courier_service_id = %q, want the operator's choice", got)
	}
}

func TestCarrierFor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		names []string
		want  string
	}{
		{[]string{"aramex", "DHL"}, "aramex"},
		// The handler is empty for some couriers, and then the umbrella name
		// is the only thing left to go on.
		{[]string{"", "DHL Express"}, "dhl"},
		{[]string{"usps"}, "usps"},
		{[]string{"", "Royal Mail"}, "royal-mail"},
		{[]string{"sf express"}, ""},
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
		if p.Code == "easyship" {
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
	if _, err := app.Ship().Create(ctx, result.Order.ID, "easyship", gocommerce.ShipRequest{Tracking: "T-1"}); err == nil || !strings.Contains(err.Error(), "not set up") {
		t.Fatalf("shipping through an idle provider = %v, want a refusal that says it is not set up", err)
	}

	on := true
	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{"token": "t-1", "courier_service_id": "cs-1", "from_line1": "1 Ship St", "from_country": "SG"}}); err != nil {
		t.Fatalf("configure from the panel: %v", err)
	}
	for _, p := range app.Settings().FulfillmentProviders {
		if p.Code == "easyship" && !p.Configured {
			t.Fatal("configured from the panel, the settings should say so")
		}
	}

}
