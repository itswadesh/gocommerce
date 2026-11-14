package gocommerce

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

// A small shop's order rules (D271): pickup, a minimum order, a pause and the
// shop's own questions, each off until switched on and each enforced by
// checkout itself.

func setPlugin(t *testing.T, app *App, key string, on bool, settings map[string]any) {
	t.Helper()
	if _, err := app.Plugins().Update(context.Background(), key, PluginPatch{Enabled: &on, Settings: settings}); err != nil {
		t.Fatalf("switch %s to %v: %v", key, on, err)
	}
}

func cartWith(t *testing.T, app *App, sku string, priceMinor int64, qty int) string {
	t.Helper()
	product := simpleProduct(t, app, sku, priceMinor, 10)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, qty)
	return cart.Token
}

func apiCode(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}

func TestAPickupOrderNeedsNoAddressAndPaysNoDelivery(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if _, err := app.Profile().Set(ctx, StoreProfile{Name: "Rasoi Bakery", Phone: "+91 98765 43210", Country: "IN", State: "OD"}, nil); err != nil {
		t.Fatal(err)
	}
	cart := cartWith(t, app, "PICK-CAKE", 45000, 1)
	in := CheckoutInput{CartID: cart, Email: "shopper@example.com", Name: "A Shopper", Pickup: true}

	if _, err := app.Order().Checkout(ctx, CodeCOD, in, ""); err == nil || !strings.Contains(err.Error(), "does not offer pickup") {
		t.Fatalf("pickup with the plugin off: %v, want refused", err)
	}
	setPlugin(t, app, PickupPluginKey, true, map[string]any{"address": "12 Market Road, Bhubaneswar"})

	q, err := app.Order().Quote(ctx, "", in)
	if err != nil {
		t.Fatal(err)
	}
	if q.Shipping.AmountMinor != 0 || q.ShippingMethod != PickupMethod {
		t.Fatalf("quote shipping %+v %q, want nothing and %q", q.Shipping, q.ShippingMethod, PickupMethod)
	}
	res, err := app.Order().Checkout(ctx, CodeCOD, in, "")
	if err != nil {
		t.Fatal(err)
	}
	o := res.Order
	if o.Shipping.AmountMinor != 0 || o.ShippingMethod != PickupMethod || o.Address.Country != "IN" {
		t.Fatalf("order shipping %+v %q, country %q; want free pickup taxed in the shop's country", o.Shipping, o.ShippingMethod, o.Address.Country)
	}
	if text := whatsappOrderText("Rasoi Bakery", o); !strings.Contains(text, PickupMethod) || strings.Contains(text, "Deliver to") {
		t.Fatalf("the WhatsApp message does not say pickup:\n%s", text)
	}
}

func TestBelowTheMinimumOrderCheckoutIsRefused(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	setPlugin(t, app, MinimumOrderPluginKey, true, map[string]any{"amount": "200"})
	exp := CurrencyExponent(app.Currency(ctx))
	unit := int64(150)
	for i := 0; i < exp; i++ {
		unit *= 10
	}
	cart := cartWith(t, app, "MIN-BUN", unit, 1)
	_, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart), "")
	if apiCode(err) != "below_minimum_order" || !strings.Contains(err.Error(), "more to place it") {
		t.Fatalf("150 against a minimum of 200: %v, want below_minimum_order", err)
	}
	// Two buns make 300, over the minimum.
	cart = cartWith(t, app, "MIN-BUN-2", unit, 2)
	if _, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart), ""); err != nil {
		t.Fatalf("300 against a minimum of 200: %v", err)
	}
}

func TestPausedOrdersAreRefusedButAnOperatorCanStillPlaceOne(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	setPlugin(t, app, PauseOrdersPluginKey, true, map[string]any{"message": "Closed for Diwali, back on Monday."})
	cart := cartWith(t, app, "PAUSE-1", 5000, 1)
	_, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart), "")
	if apiCode(err) != "orders_paused" || !strings.Contains(err.Error(), "Closed for Diwali") {
		t.Fatalf("checkout while paused: %v, want the store's message", err)
	}
	p, err := app.Products().GetVariantBySKU(ctx, "PAUSE-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Order().Create(ctx, NewOrderInput{
		Email: "phone@example.com", Name: "On the phone", Address: checkoutInput("").Address,
		Lines: []NewOrderLine{{VariantID: p.ID, Quantity: 1}},
	}); err != nil {
		t.Fatalf("an operator's order while paused: %v", err)
	}
	setPlugin(t, app, PauseOrdersPluginKey, false, nil)
	if _, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart), ""); err != nil {
		t.Fatalf("checkout once resumed: %v", err)
	}
}

func TestCheckoutAnswersAreKeptOnTheOrderAndSentOnWhatsApp(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if _, err := app.Profile().Set(ctx, StoreProfile{Name: "Rasoi Bakery", Phone: "+91 98765 43210", Country: "IN"}, nil); err != nil {
		t.Fatal(err)
	}
	setWhatsAppOrders(t, app, true, "")
	setPlugin(t, app, CheckoutQuestionsPluginKey, true, map[string]any{"questions": "Preferred delivery time *\nMessage on the cake\n"})
	cart := cartWith(t, app, "ASK-CAKE", 45000, 1)

	in := checkoutInput(cart)
	in.Answers = map[string]string{"Message on the cake": "Happy birthday"}
	if _, err := app.Order().Checkout(ctx, CodeWhatsApp, in, ""); err == nil || !strings.Contains(err.Error(), "Preferred delivery time") {
		t.Fatalf("a required question left empty: %v, want refused", err)
	}
	in.Answers["Preferred delivery time"] = " Evening "
	in.Answers["Not asked"] = "dropped"
	in.Metadata = Metadata{CheckoutAnswersKey: "forged"}
	res, err := app.Order().Checkout(ctx, CodeWhatsApp, in, "")
	if err != nil {
		t.Fatal(err)
	}
	o, err := app.Order().Get(ctx, res.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := o.CheckoutAnswers()
	want := [][2]string{{"Preferred delivery time", "Evening"}, {"Message on the cake", "Happy birthday"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("answers %v, want %v", got, want)
	}
	text, _ := url.QueryUnescape(res.Payment.ClientData["whatsapp_url"])
	if !strings.Contains(text, "Preferred delivery time: Evening") || !strings.Contains(text, "Message on the cake: Happy birthday") {
		t.Fatalf("the WhatsApp message lacks the answers:\n%s", text)
	}
}

func TestQuestionsParseOnePerLineWithAStarForRequired(t *testing.T) {
	got := parseCheckoutQuestions(" Time * \n\n  Note\n*\n")
	if len(got) != 2 || got[0] != (checkoutQuestion{"Time", true}) || got[1] != (checkoutQuestion{"Note", false}) {
		t.Fatalf("parsed %+v", got)
	}
}

// A shop that never filled in its country still takes a pickup order.
func TestAPickupOrderNeedsNoCountryFromAShopWithNone(t *testing.T) {
	app := newTestApp(t)
	setPlugin(t, app, PickupPluginKey, true, map[string]any{"address": "At the counter"})
	cart := cartWith(t, app, "PICK-NOWHERE", 5000, 1)
	in := CheckoutInput{CartID: cart, Email: "shopper@example.com", Pickup: true}
	if _, err := app.Order().Quote(context.Background(), "", in); err != nil {
		t.Fatalf("quote: %v", err)
	}
	if _, err := app.Order().Checkout(context.Background(), CodeCOD, in, ""); err != nil {
		t.Fatalf("checkout: %v", err)
	}
}
