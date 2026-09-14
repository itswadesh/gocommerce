package nimbuspost

import (
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
	OrderNumber       string         `json:"order_number"`
	PaymentType       string         `json:"payment_type"`
	OrderAmount       float64        `json:"order_amount"`
	PackageWeight     int            `json:"package_weight"`
	PackageLength     float64        `json:"package_length"`
	PackageBreadth    float64        `json:"package_breadth"`
	PackageHeight     float64        `json:"package_height"`
	RequestAutoPickup string         `json:"request_auto_pickup"`
	CourierID         string         `json:"courier_id"`
	Consignee         map[string]any `json:"consignee"`
	Pickup            map[string]any `json:"pickup"`
	OrderItems        []struct {
		Name  string  `json:"name"`
		Qty   int     `json:"qty"`
		Price float64 `json:"price"`
		SKU   string  `json:"sku"`
	} `json:"order_items"`
}

type stub struct {
	mu     sync.Mutex
	last   booking
	logins int
	refuse string
	noAWB  bool
}

func (s *stub) set(b booking) { s.mu.Lock(); defer s.mu.Unlock(); s.last = b }
func (s *stub) get() booking  { s.mu.Lock(); defer s.mu.Unlock(); return s.last }

func newApp(t *testing.T, cfg Config, shape func(*stub)) (*gocommerce.App, *stub) {
	t.Helper()
	st := &stub{}
	if shape != nil {
		shape(st)
	}
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/users/login":
			st.mu.Lock()
			st.logins++
			st.mu.Unlock()
			_, _ = io.WriteString(w, `{"status":true,"data":"jwt-token"}`)
		case "/v1/shipments":
			if r.Header.Get("Authorization") != "Bearer jwt-token" {
				http.Error(w, `{"status":false,"message":"Unauthenticated"}`, http.StatusUnauthorized)
				return
			}
			body, _ := io.ReadAll(r.Body)
			var b booking
			_ = json.Unmarshal(body, &b)
			st.set(b)

			st.mu.Lock()
			refuse, noAWB := st.refuse, st.noAWB
			st.mu.Unlock()

			switch {
			case refuse != "":
				// 200 with status false: the shape that makes a refusal look
				// like a success to anything checking only the HTTP code.
				_, _ = io.WriteString(w, `{"status":false,"message":"`+refuse+`"}`)
			case noAWB:
				_, _ = io.WriteString(w, `{"status":true,"data":{"shipment_id":321,"awb_number":""}}`)
			default:
				_, _ = io.WriteString(w, `{"status":true,"data":{"order_id":123,"shipment_id":321,"awb_number":"NP123456789","courier_id":5,"courier_name":"Delhivery Surface","label":"https://labels.example.test/np.pdf"}}`)
			}
		default:
			http.NotFound(w, r)
		}
	})

	cfg.Email, cfg.Password = "ops@example.test", "secret"
	cfg.BaseURL = server.URL + "/v1"
	if cfg.WarehouseName == "" {
		cfg.WarehouseName = "Primary"
	}
	return gctest.New(t, New(cfg)), st
}

func TestRegisterRequiresConfiguration(t *testing.T) {
	t.Parallel()

	for _, cfg := range []Config{
		{},
		{Email: "a@b.test"},
		{Email: "a@b.test", Password: "p"},
	} {
		if err := New(cfg).Register(nil); err == nil {
			t.Errorf("Register accepted an incomplete config: %+v", cfg)
		}
	}
}

// The module's reason to exist, and the aggregator part of it: the carrier is
// read from whichever courier NimbusPost chose.
func TestBookingCarriesTheMeasuredParcelAndTheChosenCourier(t *testing.T) {
	app, st := newApp(t, Config{}, nil)
	product := gctest.MeasuredProduct(t, app, "NP-1", 2500, 10, 750, 305, 200, 100)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	order := gctest.Ship(t, app, result.Order.ID, "nimbuspost", gocommerce.ShipRequest{})
	shipment := order.Fulfillments[0]
	if shipment.Tracking != "NP123456789" {
		t.Errorf("tracking = %q, want the waybill NimbusPost returned", shipment.Tracking)
	}
	// The courier took it, not NimbusPost: the tracking number is Delhivery's.
	if shipment.Carrier != "delhivery" {
		t.Errorf("carrier = %q, want delhivery, read from the courier name", shipment.Carrier)
	}
	if shipment.LabelURL != "https://labels.example.test/np.pdf" {
		t.Errorf("label = %q, want the URL NimbusPost returned", shipment.LabelURL)
	}

	sent := st.get()
	if sent.OrderNumber != result.Order.Number {
		t.Errorf("order_number = %q, want the order number", sent.OrderNumber)
	}
	if sent.PackageWeight != 750 {
		t.Errorf("package_weight = %d, want 750 grams straight from the variant", sent.PackageWeight)
	}
	if sent.PackageLength != 30.5 || sent.PackageBreadth != 20 || sent.PackageHeight != 10 {
		t.Errorf("size = %v x %v x %v cm, want 30.5 x 20 x 10",
			sent.PackageLength, sent.PackageBreadth, sent.PackageHeight)
	}
	if sent.Pickup["warehouse_name"] != "Primary" {
		t.Errorf("warehouse_name = %v, want the configured warehouse", sent.Pickup["warehouse_name"])
	}
	if sent.Consignee["pincode"] != "94117" {
		t.Errorf("consignee pincode = %v, want the order's", sent.Consignee["pincode"])
	}
	// Scheduling a van is somebody's afternoon, so it is off unless asked for.
	if sent.RequestAutoPickup != "no" {
		t.Errorf("request_auto_pickup = %q, want no by default", sent.RequestAutoPickup)
	}
}

func TestUnpaidOrderIsCODAndPaidIsPrepaid(t *testing.T) {
	app, st := newApp(t, Config{}, nil)
	product := gctest.CreateProduct(t, app, "NP-2", 2500, 10)

	unpaid := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 2)
	gctest.Ship(t, app, unpaid.Order.ID, "nimbuspost", gocommerce.ShipRequest{})
	if got := st.get(); got.PaymentType != "cod" || got.OrderAmount != 50 {
		t.Errorf("payment_type = %q with amount %v, want cod and 50.00", got.PaymentType, got.OrderAmount)
	}

	paid := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)
	if _, err := app.Pay().MarkPaid(t.Context(), paid.Order.ID, "ref"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	gctest.Ship(t, app, paid.Order.ID, "nimbuspost", gocommerce.ShipRequest{})
	if got := st.get(); got.PaymentType != "prepaid" {
		t.Errorf("payment_type = %q, want prepaid — a courier must not ask a customer who has paid", got.PaymentType)
	}
}

// 200 with status false is the shape that makes a refusal look like a success.
func TestStatusFalseIsAFailure(t *testing.T) {
	app, _ := newApp(t, Config{}, func(s *stub) { s.refuse = "Pincode is not serviceable" })
	product := gctest.CreateProduct(t, app, "NP-3", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	_, err := app.Ship().Create(t.Context(), result.Order.ID, "nimbuspost", gocommerce.ShipRequest{})
	if err == nil {
		t.Fatal("a refused booking was treated as a shipment")
	}
	if !strings.Contains(err.Error(), "not serviceable") {
		t.Errorf("error = %v, want NimbusPost's own reason in it", err)
	}

	order, getErr := app.Order().Get(t.Context(), result.Order.ID)
	if getErr != nil {
		t.Fatalf("get order: %v", getErr)
	}
	if order.Status == gocommerce.OrderShipped {
		t.Error("the order moved to shipped on a refused booking")
	}
}

// A shipment with no waybill is half-booked, and the operator has to be told
// which one to cancel.
func TestShipmentWithoutAWaybillIsAFailure(t *testing.T) {
	app, _ := newApp(t, Config{}, func(s *stub) { s.noAWB = true })
	product := gctest.CreateProduct(t, app, "NP-4", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	_, err := app.Ship().Create(t.Context(), result.Order.ID, "nimbuspost", gocommerce.ShipRequest{})
	if err == nil || !strings.Contains(err.Error(), "321") {
		t.Errorf("error = %v, want the shipment id in it so it can be cancelled", err)
	}
}

// The token is minted once and reused; logging in per shipment would be a
// round trip nobody asked for.
func TestTokenIsCached(t *testing.T) {
	app, st := newApp(t, Config{}, nil)
	product := gctest.CreateProduct(t, app, "NP-5", 2500, 10)
	for range 2 {
		result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)
		gctest.Ship(t, app, result.Order.ID, "nimbuspost", gocommerce.ShipRequest{})
	}

	st.mu.Lock()
	logins := st.logins
	st.mu.Unlock()
	if logins != 1 {
		t.Errorf("logins = %d, want 1 — the token is cached", logins)
	}
}

func TestMetaCanChooseACourier(t *testing.T) {
	app, st := newApp(t, Config{AutoPickup: true}, nil)
	product := gctest.CreateProduct(t, app, "NP-6", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	gctest.Ship(t, app, result.Order.ID, "nimbuspost", gocommerce.ShipRequest{
		Meta: map[string]string{"courier_id": "17"},
	})
	sent := st.get()
	if sent.CourierID != "17" {
		t.Errorf("courier_id = %q, want the operator's choice", sent.CourierID)
	}
	if sent.RequestAutoPickup != "yes" {
		t.Errorf("request_auto_pickup = %q, want yes when configured", sent.RequestAutoPickup)
	}
}

func TestCarrierFor(t *testing.T) {
	t.Parallel()
	for courier, want := range map[string]string{
		"Delhivery Surface":  "delhivery",
		"XpressBees 0.5 K.G": "xpressbees",
		"Blue Dart":          "bluedart",
		"Ecom Express":       "ecom-express",
		"India Post":         "india-post",
		"Some New Courier":   "",
		"":                   "",
	} {
		if got := carrierFor(courier); got != want {
			t.Errorf("carrierFor(%q) = %q, want %q", courier, got, want)
		}
	}
}

func TestMessage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ raw, want string }{
		{`"Pincode is not serviceable"`, "Pincode is not serviceable"},
		{`["a","b"]`, "a; b"},
		{``, "NimbusPost gave no reason"},
	} {
		if got := message([]byte(tc.raw)); got != tc.want {
			t.Errorf("message(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
