package delhivery

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/misiki/gocommerce/core"
	"github.com/misiki/gocommerce/gctest"
)

// manifest is the part of the payload this package is responsible for getting
// right.
type manifest struct {
	PickupLocation struct {
		Name string `json:"name"`
	} `json:"pickup_location"`
	Shipments []struct {
		Name           string  `json:"name"`
		Add            string  `json:"add"`
		Pin            string  `json:"pin"`
		Phone          string  `json:"phone"`
		Order          string  `json:"order"`
		PaymentMode    string  `json:"payment_mode"`
		CODAmount      float64 `json:"cod_amount"`
		TotalAmount    float64 `json:"total_amount"`
		Quantity       int     `json:"quantity"`
		Weight         int     `json:"weight"`
		ShipmentLength float64 `json:"shipment_length"`
		ShipmentWidth  float64 `json:"shipment_width"`
		ShipmentHeight float64 `json:"shipment_height"`
		SellerName     string  `json:"seller_name"`
		Waybill        string  `json:"waybill"`
	} `json:"shipments"`
}

type stub struct {
	mu      sync.Mutex
	last    manifest
	format  string
	refuse  bool
	noPacks bool
}

func (s *stub) set(m manifest, format string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last, s.format = m, format
}

func (s *stub) get() (manifest, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last, s.format
}

func newApp(t *testing.T, cfg Config, shape func(*stub)) (*gocommerce.App, *stub) {
	t.Helper()
	st := &stub{}
	if shape != nil {
		shape(st)
	}
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/cmu/create.json" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Token dl_test" {
			http.Error(w, `{"error":"bad token"}`, http.StatusUnauthorized)
			return
		}
		// Delhivery takes a form, not a JSON body. Reading it the other way is
		// how this integration silently sends nothing.
		if err := r.ParseForm(); err != nil {
			http.Error(w, `{"error":"not a form"}`, http.StatusBadRequest)
			return
		}
		var m manifest
		if err := json.Unmarshal([]byte(r.PostForm.Get("data")), &m); err != nil {
			http.Error(w, `{"error":"data is not JSON"}`, http.StatusBadRequest)
			return
		}
		st.set(m, r.PostForm.Get("format"))

		st.mu.Lock()
		refuse, noPacks := st.refuse, st.noPacks
		st.mu.Unlock()

		switch {
		case noPacks:
			_, _ = w.Write([]byte(`{"success":false,"packages":[],"rmk":"ClientWarehouse matching query does not exist."}`))
		case refuse:
			_, _ = w.Write([]byte(`{"success":false,"packages":[{"waybill":"","refnum":"GC-1","status":"Fail","remarks":["pincode not serviceable"]}]}`))
		default:
			_, _ = w.Write([]byte(`{"success":true,"package_count":1,"packages":[{"waybill":"1234567890123","refnum":"GC-1","status":"Success","remarks":[""]}]}`))
		}
	})

	cfg.Token = "dl_test"
	cfg.BaseURL = server.URL
	if cfg.PickupLocation == "" {
		cfg.PickupLocation = "Primary"
	}
	if cfg.SellerName == "" {
		cfg.SellerName = "Acme Retail"
	}
	return gctest.New(t, New(cfg)), st
}

func TestRegisterRequiresConfiguration(t *testing.T) {
	t.Parallel()

	for _, cfg := range []Config{
		{},
		{Token: "t"},
		{Token: "t", PickupLocation: "Primary"},
	} {
		if err := New(cfg).Register(nil); err == nil {
			t.Errorf("Register accepted an incomplete config: %+v", cfg)
		}
	}
}

// The module's reason to exist, including the shape Delhivery actually takes:
// a JSON payload inside a form field, not a JSON body.
func TestManifestIsSentAsAFormWithJSONInside(t *testing.T) {
	app, st := newApp(t, Config{}, nil)
	product := gctest.MeasuredProduct(t, app, "DL-1", 2500, 10, 750, 305, 200, 100)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	order := gctest.Ship(t, app, result.Order.ID, "delhivery", gocommerce.ShipRequest{})
	shipment := order.Fulfillments[0]
	if shipment.Tracking != "1234567890123" {
		t.Errorf("tracking = %q, want the waybill Delhivery assigned", shipment.Tracking)
	}
	if shipment.Carrier != "delhivery" {
		t.Errorf("carrier = %q, want delhivery", shipment.Carrier)
	}

	sent, format := st.get()
	if format != "json" {
		t.Errorf("format field = %q, want json", format)
	}
	if sent.PickupLocation.Name != "Primary" {
		t.Errorf("pickup_location = %q, want the configured warehouse", sent.PickupLocation.Name)
	}
	line := sent.Shipments[0]
	if line.Order != result.Order.Number {
		t.Errorf("order = %q, want the order number", line.Order)
	}
	if line.Weight != 750 {
		t.Errorf("weight = %d, want 750 grams straight from the variant", line.Weight)
	}
	// 305 mm is 30.5 cm; rounding it down is how a parcel is re-banded at the hub.
	if line.ShipmentLength != 30.5 || line.ShipmentWidth != 20 || line.ShipmentHeight != 10 {
		t.Errorf("size = %v x %v x %v cm, want 30.5 x 20 x 10",
			line.ShipmentLength, line.ShipmentWidth, line.ShipmentHeight)
	}
	if line.SellerName != "Acme Retail" {
		t.Errorf("seller_name = %q, want the configured seller", line.SellerName)
	}
}

// Cash on delivery is unpaid, so the courier has to collect — and collect this
// parcel's value, not the whole order's.
func TestUnpaidOrderIsCOD(t *testing.T) {
	app, st := newApp(t, Config{}, nil)
	product := gctest.CreateProduct(t, app, "DL-2", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 2)
	gctest.Ship(t, app, result.Order.ID, "delhivery", gocommerce.ShipRequest{})

	sent, _ := st.get()
	line := sent.Shipments[0]
	if line.PaymentMode != "COD" {
		t.Errorf("payment_mode = %q, want COD on an unpaid order", line.PaymentMode)
	}
	if line.CODAmount != 50 {
		t.Errorf("cod_amount = %v, want 50.00 for two units at 25.00", line.CODAmount)
	}
	if line.Quantity != 2 {
		t.Errorf("quantity = %d, want 2", line.Quantity)
	}
}

// A paid order collects nothing: a courier asking a customer who has already
// paid is the worse half of getting this wrong.
func TestPaidOrderIsPrepaidAndCollectsNothing(t *testing.T) {
	app, st := newApp(t, Config{}, nil)
	product := gctest.CreateProduct(t, app, "DL-3", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)
	if _, err := app.Pay().MarkPaid(t.Context(), result.Order.ID, "ref"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	gctest.Ship(t, app, result.Order.ID, "delhivery", gocommerce.ShipRequest{})

	line, _ := st.get()
	if line.Shipments[0].PaymentMode != "Prepaid" || line.Shipments[0].CODAmount != 0 {
		t.Errorf("payment_mode = %q with cod_amount %v, want Prepaid and nothing to collect",
			line.Shipments[0].PaymentMode, line.Shipments[0].CODAmount)
	}
}

// Delhivery answers 200 with success=false, so the HTTP code alone would
// report a refusal as a shipped order.
func TestRefusedShipmentIsAFailure(t *testing.T) {
	app, _ := newApp(t, Config{}, func(s *stub) { s.refuse = true })
	product := gctest.CreateProduct(t, app, "DL-4", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	_, err := app.Ship().Create(t.Context(), result.Order.ID, "delhivery", gocommerce.ShipRequest{})
	if err == nil {
		t.Fatal("a refused manifest was treated as a shipment")
	}
	if !strings.Contains(err.Error(), "pincode not serviceable") {
		t.Errorf("error = %v, want Delhivery's own remark in it", err)
	}

	order, getErr := app.Order().Get(t.Context(), result.Order.ID)
	if getErr != nil {
		t.Fatalf("get order: %v", getErr)
	}
	if order.Status == gocommerce.OrderShipped {
		t.Error("the order moved to shipped on a refused manifest")
	}
}

// A wrongly named warehouse is the commonest Delhivery failure, and it comes
// back as an empty package list with the reason at the top level.
func TestNoPackagesReportsTheTopLevelReason(t *testing.T) {
	app, _ := newApp(t, Config{}, func(s *stub) { s.noPacks = true })
	product := gctest.CreateProduct(t, app, "DL-5", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	_, err := app.Ship().Create(t.Context(), result.Order.ID, "delhivery", gocommerce.ShipRequest{})
	if err == nil || !strings.Contains(err.Error(), "ClientWarehouse") {
		t.Errorf("error = %v, want Delhivery's reason in it", err)
	}
}

// A store that pre-prints labels pulls waybills from Delhivery's pool and
// manifests against them.
func TestMetaCanSupplyAWaybill(t *testing.T) {
	app, st := newApp(t, Config{}, nil)
	product := gctest.CreateProduct(t, app, "DL-6", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	gctest.Ship(t, app, result.Order.ID, "delhivery", gocommerce.ShipRequest{
		Meta: map[string]string{"waybill": "9999999999999"},
	})
	sent, _ := st.get()
	if sent.Shipments[0].Waybill != "9999999999999" {
		t.Errorf("waybill = %q, want the one the operator supplied", sent.Shipments[0].Waybill)
	}
}

func TestRemarks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw      string
		fallback string
		want     string
	}{
		{`["pincode not serviceable"]`, "", "pincode not serviceable"},
		{`["a","b"]`, "", "a; b"},
		// Some responses send a bare string rather than a list.
		{`"single reason"`, "", "single reason"},
		// An empty list is Delhivery saying nothing, so the top-level rmk is
		// the only thing left to report.
		{`[""]`, "top level reason", "top level reason"},
		{``, "top level reason", "top level reason"},
		{``, "", "Delhivery gave no reason"},
	} {
		if got := remarks([]byte(tc.raw), tc.fallback); got != tc.want {
			t.Errorf("remarks(%q, %q) = %q, want %q", tc.raw, tc.fallback, got, tc.want)
		}
	}
}
