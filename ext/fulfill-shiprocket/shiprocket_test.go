package shiprocket

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

const (
	testEmail    = "api@example.test"
	testPassword = "s3cret-api-pass"
)

// created is the part of the adhoc-order payload this package is responsible
// for getting right.
type created struct {
	Weight        float64 `json:"weight"`
	Length        float64 `json:"length"`
	Breadth       float64 `json:"breadth"`
	Height        float64 `json:"height"`
	SubTotal      float64 `json:"sub_total"`
	PaymentMethod string  `json:"payment_method"`
	OrderID       string  `json:"order_id"`
	Phone         string  `json:"billing_phone"`
	Country       string  `json:"billing_country"`
}

func (f *fakeShiprocket) last(t *testing.T, email string) created {
	t.Helper()
	var newest *fakeOrder
	for _, o := range f.orders(email) {
		if newest == nil || o.ID > newest.ID {
			newest = o
		}
	}
	if newest == nil {
		t.Fatal("Shiprocket was never asked to create an order")
	}
	raw, _ := json.Marshal(newest.Payload)
	var c created
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// logBuffer keeps everything the engine logs, at every level, so a test can
// prove a secret is not in it.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

type harness struct {
	app  *gocommerce.App
	fake *fakeShiprocket
	logs *logBuffer
}

// newApp boots a store with the module configured in code against the fake.
func newApp(t *testing.T, cfg Config) (*gocommerce.App, *fakeShiprocket) {
	h := newHarness(t, cfg, gocommerce.Config{})
	return h.app, h.fake
}

func newHarness(t *testing.T, cfg Config, engine gocommerce.Config) *harness {
	t.Helper()
	fake := newFake(t)
	fake.account(testEmail, testPassword, "Primary", "560001")
	if cfg.Email == "" && cfg.BaseURL == "" {
		cfg.Email, cfg.Password, cfg.PickupLocation = testEmail, testPassword, "Primary"
	}
	cfg.BaseURL = fake.URL
	logs := &logBuffer{}
	engine.AllowPrivateURLs = true
	engine.Currency = "INR"
	engine.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return &harness{app: gctest.NewWithConfig(t, engine, New(cfg)), fake: fake, logs: logs}
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

func place(t *testing.T, app *gocommerce.App, ctx context.Context, variantID int64, quantity int, pin string) *gocommerce.Order {
	t.Helper()
	cart, err := app.Cart().Create(ctx, "")
	if err != nil {
		t.Fatalf("create cart: %v", err)
	}
	if _, err := app.Cart().AddLine(ctx, cart.Token, variantID, quantity); err != nil {
		t.Fatalf("add to cart: %v", err)
	}
	result, err := app.Order().Checkout(ctx, gocommerce.CodeCOD, gocommerce.CheckoutInput{
		CartID: cart.Token, Email: "buyer@example.test", Name: "Asha Rao", Phone: "+91 98765 43210",
		Address: gocommerce.Address{
			Line1: "1 Test Street", City: "Bengaluru", State: "KA", PostalCode: pin, Country: "IN",
		},
	}, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	return result.Order
}

func placeAndShip(t *testing.T, app *gocommerce.App, variantID int64, quantity int, meta map[string]string) *gocommerce.Order {
	t.Helper()
	ctx := context.Background()
	order := place(t, app, ctx, variantID, quantity, "560001")
	shipped, err := app.Ship().Create(ctx, order.ID, "shiprocket", gocommerce.ShipRequest{Meta: meta})
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	return shipped
}

// The bug this exists to prevent: a catalogue somebody took the trouble to
// weigh, and a config default declared to the carrier anyway.
func TestMeasuredVariantBeatsTheConfiguredDefault(t *testing.T) {
	app, fake := newApp(t, Config{
		DefaultWeightKg: 0.5,
		DefaultLengthCm: 15, DefaultBreadthCm: 15, DefaultHeightCm: 10,
	})
	variant := measuredProduct(t, app, "SR-MEASURED")
	placeAndShip(t, app, variant.ID, 1, nil)

	got := fake.last(t, testEmail)
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
	app, fake := newApp(t, Config{
		DefaultWeightKg: 0.5,
		DefaultLengthCm: 15, DefaultBreadthCm: 15, DefaultHeightCm: 10,
	})
	variant := measuredProduct(t, app, "SR-TWO")
	placeAndShip(t, app, variant.ID, 2, nil)

	got := fake.last(t, testEmail)
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
	app, fake := newApp(t, Config{DefaultWeightKg: 0.4})
	product := gctest.CreateProduct(t, app, "SR-UNWEIGHED", 2500, 10)
	placeAndShip(t, app, product.DefaultVariant().ID, 3, nil)

	if got := fake.last(t, testEmail).Weight; got != 1.2 {
		t.Errorf("weight = %v kg, want 1.2 — three units at the 0.4 default", got)
	}
}

// The operator is holding the box, so what they type wins over everything.
func TestOperatorMetaWins(t *testing.T) {
	app, fake := newApp(t, Config{
		DefaultWeightKg: 0.5,
		DefaultLengthCm: 15, DefaultBreadthCm: 15, DefaultHeightCm: 10,
	})
	variant := measuredProduct(t, app, "SR-META")
	placeAndShip(t, app, variant.ID, 1, map[string]string{
		"weight_kg": "2.2", "length_cm": "40",
	})

	got := fake.last(t, testEmail)
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

// The courier collects what the order costs — delivery included — and not the
// price of the goods alone.
func TestCashOnDeliveryCollectsTheOrdersTotal(t *testing.T) {
	h := newHarness(t, Config{}, gocommerce.Config{FlatShippingMinor: 4900})
	variant := measuredProduct(t, h.app, "SR-COD")
	order := placeAndShip(t, h.app, variant.ID, 2, nil)

	got := h.fake.last(t, testEmail)
	if order.Total.AmountMinor != 2*2500+4900 {
		t.Fatalf("order total = %d, the test expects 5000 + 4900 delivery", order.Total.AmountMinor)
	}
	if got.PaymentMethod != "COD" || got.SubTotal != 99 {
		t.Errorf("payment = %s, sub_total = %v; want COD collecting the order's 99.00", got.PaymentMethod, got.SubTotal)
	}
	if got.Phone != "9876543210" || got.Country != "India" {
		t.Errorf("phone %q, country %q; want ten digits and India", got.Phone, got.Country)
	}
	if order.Fulfillments[0].Carrier != "delhivery" || order.Fulfillments[0].LabelURL == "" {
		t.Errorf("fulfillment = %+v, want Delhivery named and the label kept", order.Fulfillments[0])
	}
}

// Half a cash-on-delivery order cannot say what its courier should collect.
func TestPartOfACashOnDeliveryOrderIsRefused(t *testing.T) {
	app, fake := newApp(t, Config{})
	a := measuredProduct(t, app, "SR-PART-A")
	ctx := context.Background()
	order := place(t, app, ctx, a.ID, 2, "560001")
	_, err := app.Ship().Create(ctx, order.ID, "shiprocket", gocommerce.ShipRequest{
		Lines: []gocommerce.ShipLine{{OrderLineID: order.Lines[0].ID, Quantity: 1}},
	})
	if err == nil || !strings.Contains(err.Error(), "one parcel") {
		t.Fatalf("shipping half a COD order = %v, want a refusal that explains", err)
	}
	if n := len(fake.orders(testEmail)); n != 0 {
		t.Errorf("%d orders reached Shiprocket", n)
	}
}

// The case the whole table exists for: a booking that failed half-way is
// pressed again, and Shiprocket ends up with one order and one waybill.
func TestARetryAfterARefusalBooksOnce(t *testing.T) {
	app, fake := newApp(t, Config{})
	variant := measuredProduct(t, app, "SR-RETRY")
	ctx := context.Background()
	order := place(t, app, ctx, variant.ID, 1, "560001")

	fake.refuseAWB = "Insufficient balance in wallet, please recharge"
	_, err := app.Ship().Create(ctx, order.ID, "shiprocket", gocommerce.ShipRequest{})
	if err == nil || !strings.Contains(err.Error(), "Insufficient balance") || !strings.Contains(err.Error(), "Ship again") {
		t.Fatalf("first attempt = %v, want Shiprocket's words and what to do next", err)
	}
	fake.refuseAWB = ""
	shipped, err := app.Ship().Create(ctx, order.ID, "shiprocket",
		gocommerce.ShipRequest{Meta: map[string]string{"courier_id": "1"}})
	if err != nil {
		t.Fatalf("second attempt: %v", err)
	}
	orders := fake.orders(testEmail)
	if len(orders) != 1 {
		t.Fatalf("Shiprocket has %d orders, want 1", len(orders))
	}
	if orders[0].AWB == "" || shipped.Fulfillments[0].Tracking != orders[0].AWB {
		t.Errorf("tracking %q, Shiprocket's waybill %q", shipped.Fulfillments[0].Tracking, orders[0].AWB)
	}
	if orders[0].CourierID != 1 || shipped.Fulfillments[0].Carrier != "bluedart" {
		t.Errorf("courier = %d, carrier %q; want the chosen Blue Dart", orders[0].CourierID, shipped.Fulfillments[0].Carrier)
	}
}

// Shiprocket made the order and the answer never arrived. The retry finds it.
func TestALostAnswerIsLookedUpNotRebooked(t *testing.T) {
	app, fake := newApp(t, Config{})
	variant := measuredProduct(t, app, "SR-LOST")
	fake.dropCreateAnswer = true
	placeAndShip(t, app, variant.ID, 1, nil)
	if n := len(fake.orders(testEmail)); n != 1 {
		t.Fatalf("Shiprocket has %d orders, want the one", n)
	}
	if n := fake.count("/v1/external/orders/create/adhoc"); n != 2 {
		t.Logf("create was called %d times", n)
	}
}

// A ten-day token can end early. The next call logs in again by itself.
func TestAnExpiredTokenIsRenewed(t *testing.T) {
	app, fake := newApp(t, Config{})
	variant := measuredProduct(t, app, "SR-TOKEN")
	quote := func(pin string) {
		t.Helper()
		order := place(t, app, context.Background(), variant.ID, 1, pin)
		rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/fulfill-shiprocket/orders/"+itoa(order.ID)+"/couriers", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("quote to %s = %d %s", pin, rec.Code, rec.Body)
		}
	}
	quote("560002")
	before := fake.logins
	quote("560003")
	if fake.logins != before {
		t.Fatalf("a live token was not reused: %d logins, want %d", fake.logins, before)
	}
	fake.expireTokens()
	quote("560004")
	if fake.logins != before+1 {
		t.Errorf("logins = %d, want one more than %d", fake.logins, before)
	}
}

// A store that changes its API user calls as the new one from the next
// request, not as the old one until its token runs out.
func TestChangingTheAPIUserTakesEffectAtOnce(t *testing.T) {
	h := newHarness(t, Config{BaseURL: "set"}, gocommerce.Config{})
	h.fake.account("new@example.test", "new-pass", "Primary", "560001")
	ctx := context.Background()
	use := func(email, password string) {
		t.Helper()
		on := true
		if _, err := h.app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{
			"email": email, "password": password, "pickup_location": "Primary", "base_url": h.fake.URL}}); err != nil {
			t.Fatal(err)
		}
	}
	variant := measuredProduct(t, h.app, "SR-SWITCH")
	use(testEmail, testPassword)
	placeAndShip(t, h.app, variant.ID, 1, nil)
	use("new@example.test", "new-pass")
	placeAndShip(t, h.app, variant.ID, 1, nil)
	if a, b := len(h.fake.orders(testEmail)), len(h.fake.orders("new@example.test")); a != 1 || b != 1 {
		t.Errorf("old account has %d orders, new has %d; want one each", a, b)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// What the ship dialog shows: who will carry it, for how much, by when, and
// whether they collect cash — asked once, not on every keystroke.
func TestCouriersAreQuotedAndKept(t *testing.T) {
	app, fake := newApp(t, Config{})
	variant := measuredProduct(t, app, "SR-QUOTE")
	order := place(t, app, context.Background(), variant.ID, 1, "110001")
	path := "/api/admin/x/fulfill-shiprocket/orders/" + itoa(order.ID) + "/couriers"

	var q Quote
	rec := gctest.AdminRequest(t, app, http.MethodGet, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("quote = %d %s", rec.Code, rec.Body)
	}
	gctest.DecodeData(t, rec, &q)
	if q.PickupPostcode != "560001" || q.DeliveryPostcode != "110001" || !q.COD || q.WeightKg != 0.75 {
		t.Errorf("quote = %+v", q)
	}
	if len(q.Couriers) != 2 {
		t.Fatalf("couriers = %+v, want the two that collect cash", q.Couriers)
	}
	if q.Couriers[0].ID != 43 || q.Couriers[0].Rate.AmountMinor != 5400 || q.Couriers[0].Rate.Currency != "INR" {
		t.Errorf("cheapest first = %+v, want Delhivery at 5400 paise", q.Couriers[0])
	}
	if q.Couriers[1].Rate.AmountMinor != 12390 || q.Couriers[1].EstimatedDays != 2 || q.Couriers[1].ETD != "Jun 29, 2024" {
		t.Errorf("blue dart = %+v, want 12390 paise, 2 days, Shiprocket's own date", q.Couriers[1])
	}
	if q.Cheapest != 43 || q.Fastest != 1 || q.Recommended != 43 {
		t.Errorf("picks = cheapest %d fastest %d recommended %d", q.Cheapest, q.Fastest, q.Recommended)
	}
	gctest.AdminRequest(t, app, http.MethodGet, path, nil)
	if n := fake.count("/v1/external/courier/serviceability/"); n != 1 {
		t.Errorf("serviceability asked %d times for the same parcel, want once", n)
	}
}

func TestAnUnservedPincodeIsSaidPlainly(t *testing.T) {
	app, _ := newApp(t, Config{})
	variant := measuredProduct(t, app, "SR-NOPE")
	order := place(t, app, context.Background(), variant.ID, 1, "999999")
	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/fulfill-shiprocket/orders/"+itoa(order.ID)+"/couriers", nil)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "not serviceable") {
		t.Errorf("quote to an unserved pincode = %d %s", rec.Code, rec.Body)
	}
}

// Connecting tries the account before it is saved, and lists where it can
// collect from.
func TestConnectTriesTheAccountAndListsItsPickups(t *testing.T) {
	h := newHarness(t, Config{BaseURL: "set"}, gocommerce.Config{})
	rec := gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/fulfill-shiprocket/connect",
		map[string]string{"email": testEmail, "password": testPassword})
	if rec.Code != http.StatusOK {
		t.Fatalf("connect = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Connected bool             `json:"connected"`
		Pickups   []PickupLocation `json:"pickup_locations"`
	}
	gctest.DecodeData(t, rec, &out)
	if !out.Connected || len(out.Pickups) != 1 || out.Pickups[0].Name != "Primary" || out.Pickups[0].Pincode != "560001" {
		t.Errorf("connect = %+v", out)
	}
	if strings.Contains(rec.Body.String(), testPassword) {
		t.Error("the password came back in the answer")
	}

	rec = gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/fulfill-shiprocket/connect",
		map[string]string{"email": testEmail, "password": "wrong"})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "did not accept") {
		t.Errorf("a wrong password = %d %s", rec.Code, rec.Body)
	}

	// The mask means the password already saved.
	ctx := context.Background()
	on := true
	if _, err := h.app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{
		"email": testEmail, "password": testPassword, "pickup_location": "Primary", "base_url": h.fake.URL}}); err != nil {
		t.Fatal(err)
	}
	rec = gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/fulfill-shiprocket/connect",
		map[string]string{"email": testEmail, "password": gocommerce.SecretMask})
	if rec.Code != http.StatusOK {
		t.Errorf("connect with the saved password = %d %s", rec.Code, rec.Body)
	}
}

// The password and the token it buys are never shown, logged or audited.
func TestTheSecretNeverLeavesTheServer(t *testing.T) {
	h := newHarness(t, Config{BaseURL: "set"}, gocommerce.Config{})
	ctx := context.Background()
	on := true
	if _, err := h.app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{
		"email": testEmail, "password": testPassword, "pickup_location": "Primary", "base_url": h.fake.URL}}); err != nil {
		t.Fatal(err)
	}
	variant := measuredProduct(t, h.app, "SR-SECRET")
	order := placeAndShip(t, h.app, variant.ID, 1, nil)
	h.fake.refuseAWB = "nope"
	gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/fulfill-shiprocket/connect",
		map[string]string{"email": testEmail, "password": "wrong-" + testPassword})

	var seen []string
	for _, target := range []string{
		"/api/admin/plugins/" + PluginKey,
		"/api/admin/x/fulfill-shiprocket/orders/" + itoa(order.ID) + "/shipments",
		"/api/admin/orders/" + itoa(order.ID),
		"/api/admin/audit",
	} {
		seen = append(seen, gctest.AdminRequest(t, h.app, http.MethodGet, target, nil).Body.String())
	}
	var audit string
	_ = h.app.DB().QueryRowContext(ctx, `SELECT coalesce(string_agg(t::text, ' '), '') FROM admin_audit t`).Scan(&audit)
	seen = append(seen, audit, h.logs.String())
	for i, s := range seen {
		if strings.Contains(s, testPassword) {
			t.Errorf("source %d holds the password", i)
		}
		if strings.Contains(s, "tok-") {
			t.Errorf("source %d holds a Shiprocket token", i)
		}
	}
}

// Two stores, two Shiprocket accounts: each books on its own, and a store
// that has not connected one cannot borrow its neighbour's.
func TestEachStoreBooksOnItsOwnAccount(t *testing.T) {
	h := newHarness(t, Config{BaseURL: "set"}, gocommerce.Config{})
	h.fake.account("b@example.test", "b-pass", "Second warehouse", "400001")
	second, err := h.app.Stores().Create(context.Background(), "Second", "second")
	if err != nil {
		t.Fatal(err)
	}
	a := context.Background()
	b := gocommerce.WithStore(context.Background(), second.ID)

	variantA := measuredProduct(t, h.app, "SR-STORE-A")
	orderB := func() *gocommerce.Order {
		p, err := h.app.Products().CreateProduct(b, gocommerce.ProductInput{Title: "b", Status: "active",
			Variants: []gocommerce.VariantInput{{SKU: "SR-STORE-B", PriceMinor: 1000, StockOnHand: ptr(5)}}})
		if err != nil {
			t.Fatal(err)
		}
		return place(t, h.app, b, p.DefaultVariant().ID, 1, "560001")
	}()

	// Only store A has connected an account so far.
	on := true
	if _, err := h.app.Plugins().Update(a, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{
		"email": testEmail, "password": testPassword, "pickup_location": "Primary", "base_url": h.fake.URL}}); err != nil {
		t.Fatal(err)
	}
	orderA := place(t, h.app, a, variantA.ID, 1, "560001")
	if _, err := h.app.Ship().Create(a, orderA.ID, "shiprocket", gocommerce.ShipRequest{}); err != nil {
		t.Fatalf("store A ships: %v", err)
	}
	if _, err := h.app.Ship().Create(b, orderB.ID, "shiprocket", gocommerce.ShipRequest{}); err == nil {
		t.Fatal("store B shipped through Shiprocket without an account of its own")
	}

	if _, err := h.app.Plugins().Update(b, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{
		"email": "b@example.test", "password": "b-pass", "pickup_location": "Second warehouse", "base_url": h.fake.URL}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Ship().Create(b, orderB.ID, "shiprocket", gocommerce.ShipRequest{}); err != nil {
		t.Fatalf("store B ships: %v", err)
	}
	ordersA, ordersB := h.fake.orders(testEmail), h.fake.orders("b@example.test")
	if len(ordersA) != 1 || len(ordersB) != 1 {
		t.Fatalf("account A has %d orders, B has %d; want one each", len(ordersA), len(ordersB))
	}
	if ordersA[0].Payload["pickup_location"] != "Primary" || ordersB[0].Payload["pickup_location"] != "Second warehouse" {
		t.Errorf("pickups = %v / %v", ordersA[0].Payload["pickup_location"], ordersB[0].Payload["pickup_location"])
	}
	// And A's rows are not B's to see.
	rec := gctest.AdminRequest(t, h.app, http.MethodGet,
		"/api/admin/x/fulfill-shiprocket/orders/"+itoa(orderA.ID)+"/shipments?store="+second.ID, nil)
	if strings.Contains(rec.Body.String(), ordersA[0].AWB) {
		t.Error("store B read store A's Shiprocket booking")
	}
}

func ptr(n int) *int { return &n }

// Cancelling calls the parcel off at Shiprocket and on the order; the next
// booking is a new Shiprocket order, since Shiprocket will not reuse the id.
func TestCancelCallsItOffEverywhere(t *testing.T) {
	app, fake := newApp(t, Config{})
	variant := measuredProduct(t, app, "SR-CANCEL")
	shipped := placeAndShip(t, app, variant.ID, 1, nil)

	var rows []shipment
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet,
		"/api/admin/x/fulfill-shiprocket/orders/"+itoa(shipped.ID)+"/shipments", nil), &rows)
	if len(rows) != 1 || rows[0].FulfillmentID != shipped.Fulfillments[0].ID || !rows[0].PickupRequested {
		t.Fatalf("rows = %+v", rows)
	}
	rec := gctest.AdminRequest(t, app, http.MethodPost,
		"/api/admin/x/fulfill-shiprocket/shipments/"+itoa(rows[0].ID)+"/cancel", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body)
	}
	if o := fake.orders(testEmail)[0]; !o.Cancelled {
		t.Error("Shiprocket was not told")
	}
	order, _ := app.Order().Get(context.Background(), shipped.ID)
	if order.Status != gocommerce.OrderConfirmed || order.Fulfillments[0].Status != "cancelled" {
		t.Errorf("order %s, parcel %s; want confirmed and cancelled", order.Status, order.Fulfillments[0].Status)
	}

	if _, err := app.Ship().Create(context.Background(), shipped.ID, "shiprocket", gocommerce.ShipRequest{}); err != nil {
		t.Fatalf("ship again: %v", err)
	}
	if got := fake.last(t, testEmail).OrderID; got != shipped.Number+"-2" {
		t.Errorf("second booking's id = %q, want %s-2", got, shipped.Number)
	}
}

// The papers a parcel needs, fetched when asked for and kept.
func TestDocumentsAreFetchedAndKept(t *testing.T) {
	app, _ := newApp(t, Config{})
	variant := measuredProduct(t, app, "SR-DOCS")
	shipped := placeAndShip(t, app, variant.ID, 1, nil)
	var rows []shipment
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet,
		"/api/admin/x/fulfill-shiprocket/orders/"+itoa(shipped.ID)+"/shipments", nil), &rows)
	for _, kind := range []string{"manifest", "invoice"} {
		rec := gctest.AdminRequest(t, app, http.MethodPost,
			"/api/admin/x/fulfill-shiprocket/shipments/"+itoa(rows[0].ID)+"/documents/"+kind, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", kind, rec.Code, rec.Body)
		}
		var row shipment
		gctest.DecodeData(t, rec, &row)
		if (kind == "manifest" && !strings.HasPrefix(row.ManifestURL, "https://manifests.example.test/")) ||
			(kind == "invoice" && !strings.HasPrefix(row.InvoiceURL, "https://invoices.example.test/")) {
			t.Errorf("%s: row = %+v", kind, row)
		}
	}
}

func TestAdminRoutesAreDeclared(t *testing.T) {
	app, _ := newApp(t, Config{})
	gctest.AssertAdminRoutesDeclareRights(t, app, "fulfill-shiprocket")
	gctest.AssertSpecCoversModuleRoutes(t, app, "fulfill-shiprocket")
}

// TestInstalledIdleUntilConfigured: with nothing in Config the module still
// registers — the panel lists it — but the settings say it is not set up and
// nothing can be sent through it, until its fields are typed into the plugin.
func TestInstalledIdleUntilConfigured(t *testing.T) {
	app := gctest.NewWithConfig(t, gocommerce.Config{AllowPrivateURLs: true}, New(Config{}))
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
