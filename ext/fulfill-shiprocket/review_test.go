package shiprocket

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// onAccount is a way to pay that is neither cash on delivery nor paid yet: a
// trade account settled on terms.
type onAccount struct{}

func (onAccount) Code() string { return "on-account" }
func (onAccount) Initiate(context.Context, *gocommerce.Order, gocommerce.PayOptions) (gocommerce.PaymentIntent, error) {
	return gocommerce.PaymentIntent{Kind: gocommerce.IntentNone, Provider: "on-account"}, nil
}

type onAccountModule struct{}

func (onAccountModule) Name() string                       { return "on-account" }
func (onAccountModule) Migrations() []gocommerce.Migration { return nil }
func (onAccountModule) Register(app *gocommerce.App) error {
	app.RegisterPayment(onAccount{})
	return nil
}

// Only an unpaid cash-on-delivery order is collected on the doorstep. One paid
// at the counter, or one on a trade account, is not.
func TestOnlyUnpaidCashOnDeliveryIsCollected(t *testing.T) {
	fake := newFake(t)
	fake.account(testEmail, testPassword, "Primary", "560001")
	app := gctest.NewWithConfig(t, gocommerce.Config{AllowPrivateURLs: true, Currency: "INR"},
		onAccountModule{}, New(Config{Email: testEmail, Password: testPassword, PickupLocation: "Primary", BaseURL: fake.URL}))
	ctx := context.Background()
	variant := measuredProduct(t, app, "SR-TERMS")

	paid := place(t, app, ctx, variant.ID, 1, "560001")
	if _, err := app.Pay().MarkPaid(ctx, paid.ID, "cash at the counter"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Ship().Create(ctx, paid.ID, "shiprocket", gocommerce.ShipRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := fake.last(t, testEmail); got.PaymentMethod != "Prepaid" {
		t.Errorf("a paid COD order went out as %s", got.PaymentMethod)
	}

	cart, _ := app.Cart().Create(ctx, "")
	if _, err := app.Cart().AddLine(ctx, cart.Token, variant.ID, 1); err != nil {
		t.Fatal(err)
	}
	res, err := app.Order().Checkout(ctx, "on-account", gocommerce.CheckoutInput{
		CartID: cart.Token, Email: "buyer@example.test", Name: "Trade Buyer",
		Address: gocommerce.Address{Line1: "1 Mill Road", City: "Pune", PostalCode: "411001", Country: "IN"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Order().Confirm(ctx, res.Order.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Ship().Create(ctx, res.Order.ID, "shiprocket", gocommerce.ShipRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := fake.last(t, testEmail); got.PaymentMethod != "Prepaid" {
		t.Errorf("an order on account went out as %s, and its courier would ask for cash", got.PaymentMethod)
	}
}

// A booking resumed after the order was paid would still tell the courier to
// collect cash. It is refused instead.
func TestAResumedBookingWithNewTermsIsRefused(t *testing.T) {
	app, fake := newApp(t, Config{})
	variant := measuredProduct(t, app, "SR-NEWTERMS")
	ctx := context.Background()
	order := place(t, app, ctx, variant.ID, 1, "560001")

	fake.refuseAWB = "Insufficient balance"
	if _, err := app.Ship().Create(ctx, order.ID, "shiprocket", gocommerce.ShipRequest{}); err == nil {
		t.Fatal("the refused waybill was not reported")
	}
	fake.refuseAWB = ""
	if _, err := app.Pay().MarkPaid(ctx, order.ID, "paid by UPI"); err != nil {
		t.Fatal(err)
	}
	_, err := app.Ship().Create(ctx, order.ID, "shiprocket", gocommerce.ShipRequest{})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "cancel it") {
		t.Fatalf("resuming a COD booking on a paid order = %v, want a refusal that says to cancel it", err)
	}
	if n := len(fake.orders(testEmail)); n != 1 {
		t.Errorf("Shiprocket has %d orders, want the one", n)
	}
}

// Two stores on one Shiprocket account both have an order GC-000001. Each
// books its own.
func TestTwoStoresOnOneAccountBookTheirOwnParcels(t *testing.T) {
	h := newHarness(t, Config{BaseURL: "set"}, gocommerce.Config{})
	second, err := h.app.Stores().Create(context.Background(), "Second", "second")
	if err != nil {
		t.Fatal(err)
	}
	a, b := context.Background(), gocommerce.WithStore(context.Background(), second.ID)
	on := true
	for _, ctx := range []context.Context{a, b} {
		if _, err := h.app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{
			"email": testEmail, "password": testPassword, "pickup_location": "Primary"}}); err != nil {
			t.Fatal(err)
		}
	}
	va := measuredProduct(t, h.app, "SR-SHARED-A")
	pb, err := h.app.Products().CreateProduct(b, gocommerce.ProductInput{Title: "b", Status: "active",
		Variants: []gocommerce.VariantInput{{SKU: "SR-SHARED-B", PriceMinor: 1000, StockOnHand: ptr(5)}}})
	if err != nil {
		t.Fatal(err)
	}
	orderA, orderB := place(t, h.app, a, va.ID, 1, "560001"), place(t, h.app, b, pb.DefaultVariant().ID, 1, "560001")
	if orderA.Number != orderB.Number {
		t.Fatalf("the test wants the same order number in both stores, got %s and %s", orderA.Number, orderB.Number)
	}
	shippedA, err := h.app.Ship().Create(a, orderA.ID, "shiprocket", gocommerce.ShipRequest{})
	if err != nil {
		t.Fatal(err)
	}
	shippedB, err := h.app.Ship().Create(b, orderB.ID, "shiprocket", gocommerce.ShipRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(h.fake.orders(testEmail)); n != 2 {
		t.Errorf("Shiprocket has %d orders for two stores' parcels, want 2", n)
	}
	if shippedA.Fulfillments[0].Tracking == shippedB.Fulfillments[0].Tracking {
		t.Error("both stores recorded the same waybill")
	}
}

// An order cancelled in Shiprocket's own dashboard is never adopted; the next
// press books a new one.
func TestACancelledShiprocketOrderIsNotAdopted(t *testing.T) {
	app, fake := newApp(t, Config{})
	variant := measuredProduct(t, app, "SR-DASHCANCEL")
	ctx := context.Background()
	order := place(t, app, ctx, variant.ID, 1, "560001")

	fake.cancelledOnCreate, fake.dropCreateAnswer = true, true
	if _, err := app.Ship().Create(ctx, order.ID, "shiprocket", gocommerce.ShipRequest{}); err == nil ||
		!strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("adopting a cancelled order = %v, want a refusal naming it", err)
	}
	if _, err := app.Ship().Create(ctx, order.ID, "shiprocket", gocommerce.ShipRequest{}); err != nil {
		t.Fatalf("booking again: %v", err)
	}
	if got := fake.last(t, testEmail).OrderID; !strings.HasSuffix(got, "-2") {
		t.Errorf("the new booking's id = %q, want the next in the sequence", got)
	}
}

// The waybill was assigned and the answer lost: the same press finds it.
func TestALostWaybillAnswerIsFound(t *testing.T) {
	app, fake := newApp(t, Config{})
	variant := measuredProduct(t, app, "SR-LOSTAWB")
	fake.dropAWB, fake.assignThenDrop = true, true
	shipped := placeAndShip(t, app, variant.ID, 1, nil)
	orders := fake.orders(testEmail)
	if len(orders) != 1 || shipped.Fulfillments[0].Tracking != orders[0].AWB {
		t.Errorf("tracking %q, Shiprocket's waybill %q", shipped.Fulfillments[0].Tracking, orders[0].AWB)
	}
}

// No waybill and no answer: the operator is told the order exists, not that
// nothing was booked.
func TestAHalfBookingOverADeadLineSaysSo(t *testing.T) {
	app, fake := newApp(t, Config{})
	variant := measuredProduct(t, app, "SR-HALF")
	ctx := context.Background()
	order := place(t, app, ctx, variant.ID, 1, "560001")
	fake.dropAWB = true
	_, err := app.Ship().Create(ctx, order.ID, "shiprocket", gocommerce.ShipRequest{})
	var apiErr *gocommerce.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnprocessableEntity || !strings.Contains(apiErr.Message, "was created") {
		t.Fatalf("half-booked over a dead line = %v, want a 422 saying the Shiprocket order exists", err)
	}
}

// A store's saved password goes only where it was saved for: the address it
// is sent to is not a store setting.
func TestThePasswordCannotBeSentElsewhere(t *testing.T) {
	app, _ := newApp(t, Config{})
	p, err := app.Plugins().Get(context.Background(), PluginKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range p.Fields {
		if f.Key == "base_url" {
			t.Error("the API address is a store setting, so a store could send its saved password anywhere")
		}
	}
	rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/fulfill-shiprocket/connect",
		map[string]string{"email": testEmail, "password": gocommerce.SecretMask, "base_url": "https://attacker.example"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("connect naming an address = %d, want 400", rec.Code)
	}
}

// Cancelling resumes: once the waybill is called off the parcel will not
// travel, so a refused order cancel after it does not leave the order shipped.
func TestCancellingFinishesWhenTheOrderCancelIsRefused(t *testing.T) {
	app, fake := newApp(t, Config{})
	variant := measuredProduct(t, app, "SR-CANCEL2")
	shipped := placeAndShip(t, app, variant.ID, 1, nil)
	var rows []shipment
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet,
		"/api/admin/x/fulfill-shiprocket/orders/"+itoa(shipped.ID)+"/shipments", nil), &rows)
	fake.refuseOrderCancel = true
	rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/fulfill-shiprocket/shipments/"+itoa(rows[0].ID)+"/cancel", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body)
	}
	order, _ := app.Order().Get(context.Background(), shipped.ID)
	if order.Fulfillments[0].Status != "cancelled" {
		t.Errorf("parcel %s after its waybill was cancelled", order.Fulfillments[0].Status)
	}
}

// One lease at a time, and only its holder gives it back.
func TestTheLeaseBelongsToWhoeverTookIt(t *testing.T) {
	h := newHarness(t, Config{}, gocommerce.Config{})
	variant := measuredProduct(t, h.app, "SR-LEASE")
	shipped := placeAndShip(t, h.app, variant.ID, 1, nil)
	ctx := context.Background()
	rows, err := h.mod.shipmentsOf(ctx, shipped.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, mine, err := h.mod.lease(ctx, rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.mod.lease(ctx, rows[0].ID); err == nil {
		t.Fatal("a second lease was granted while the first was held")
	}
	h.mod.release(ctx, rows[0].ID, mine.Add(-time.Microsecond))
	if _, _, err := h.mod.lease(ctx, rows[0].ID); err == nil {
		t.Fatal("somebody else's release freed the lease")
	}
	h.mod.release(ctx, rows[0].ID, mine)
	if _, _, err := h.mod.lease(ctx, rows[0].ID); err != nil {
		t.Fatalf("the holder's release did not free it: %v", err)
	}
}
