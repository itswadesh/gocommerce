package gocommerce

import (
	"context"
	"testing"
)

// buyWith runs a checkout against a cart holding one unit of a 10000-minor
// product, so the basket subtotal is a known number the bands can be written
// against.
func buyWith(t *testing.T, app *App, sku string, shape func(*CheckoutInput)) (*Order, error) {
	t.Helper()
	p := simpleProduct(t, app, sku, 10000, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, p.Variants[0].ID, 1)

	in := checkoutInput(cart.Token)
	in.Address.Country = "US"
	if shape != nil {
		shape(&in)
	}
	res, err := app.orders.Checkout(context.Background(), CodeCOD, in, "")
	if err != nil {
		return nil, err
	}
	return res.Order, nil
}

// Nothing changes for a store that has not configured any of this. That is the
// whole of the back-compatibility promise: no rates, no new field on the
// request, same number on the order.
func TestWithNoRatesConfiguredCheckoutStillChargesTheFlatNumber(t *testing.T) {
	app := newTestApp(t)
	app.cfg.FlatShippingMinor = 500

	order, err := buyWith(t, app, "FLAT-1", nil)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if order.Shipping.AmountMinor != 500 {
		t.Errorf("shipping = %d, want the configured 500", order.Shipping.AmountMinor)
	}
	if order.ShippingMethod != "" {
		t.Errorf("shipping_method = %q, want empty when no rate was involved", order.ShippingMethod)
	}
}

// The point of the feature: the shopper picks, and that is what they are
// charged and what the order remembers.
func TestAChosenRateIsWhatTheOrderIsChargedAndRemembers(t *testing.T) {
	app := newTestApp(t)
	app.cfg.FlatShippingMinor = 500
	z := zone(t, app, "US", []string{"US"}, nil)
	rate(t, app, z.ID, "Standard", 4900, 0, nil)
	express := rate(t, app, z.ID, "Express", 19900, 0, nil)

	order, err := buyWith(t, app, "PICK-1", func(in *CheckoutInput) {
		in.ShippingRateID = &express.ID
	})
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if order.Shipping.AmountMinor != 19900 {
		t.Errorf("shipping = %d, want the chosen 19900", order.Shipping.AmountMinor)
	}
	if order.ShippingMethod != "Express" {
		t.Errorf("shipping_method = %q, want Express", order.ShippingMethod)
	}
}

// A client that has not been updated must not start paying the most expensive
// option by accident, and must not be refused either. The cheapest that applies
// is the only defensible silent answer.
func TestWithRatesButNoChoiceTheCheapestApplies(t *testing.T) {
	app := newTestApp(t)
	z := zone(t, app, "US", []string{"US"}, nil)
	rate(t, app, z.ID, "Express", 19900, 0, nil)
	rate(t, app, z.ID, "Standard", 4900, 0, nil)

	order, err := buyWith(t, app, "CHEAP-1", nil)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if order.Shipping.AmountMinor != 4900 {
		t.Errorf("shipping = %d, want the cheapest 4900", order.Shipping.AmountMinor)
	}
	if order.ShippingMethod != "Standard" {
		t.Errorf("shipping_method = %q, want Standard", order.ShippingMethod)
	}
}

// A rate that stopped applying between the quote and the order is the same
// class of problem as a price that moved or stock that went, and it gets the
// same answer: refuse, and say so.
func TestARateThatNoLongerAppliesIsRefused(t *testing.T) {
	app := newTestApp(t)
	z := zone(t, app, "US", []string{"US"}, nil)
	// Only applies to baskets of 50000 and up; the test basket is 10000.
	tooBig := rate(t, app, z.ID, "Bulk", 0, 50000, nil)
	rate(t, app, z.ID, "Standard", 4900, 0, i64(50000))

	if _, err := buyWith(t, app, "STALE-1", func(in *CheckoutInput) {
		in.ShippingRateID = &tooBig.ID
	}); err == nil {
		t.Fatal("a rate outside its band was accepted, want a refusal")
	}
}

// A rate from another store's zone, or one that has been deleted, is not a
// rate this basket may use.
func TestAnUnknownRateIsRefused(t *testing.T) {
	app := newTestApp(t)
	z := zone(t, app, "US", []string{"US"}, nil)
	rate(t, app, z.ID, "Standard", 4900, 0, nil)

	missing := int64(999999)
	if _, err := buyWith(t, app, "GHOST-1", func(in *CheckoutInput) {
		in.ShippingRateID = &missing
	}); err == nil {
		t.Fatal("an unknown rate id was accepted, want a refusal")
	}
}

// Configured rates that cover nowhere near the shopper means this store does
// not deliver there, and saying so is better than quietly charging the old flat
// number for a parcel nobody can send.
func TestADestinationWithNoZoneIsRefusedOnceRatesExist(t *testing.T) {
	app := newTestApp(t)
	app.cfg.FlatShippingMinor = 500
	z := zone(t, app, "India only", []string{"IN"}, nil)
	rate(t, app, z.ID, "Standard", 4900, 0, nil)

	if _, err := buyWith(t, app, "NOWHERE-1", nil); err == nil {
		t.Fatal("a destination no zone covers was accepted, want a refusal")
	}
}

// Free shipping is a discount on the shipping, whatever the shipping turned out
// to be. It worked against the flat number and must keep working against a rate.
func TestFreeShippingStillZeroesAChosenRate(t *testing.T) {
	app := newTestApp(t)
	z := zone(t, app, "US", []string{"US"}, nil)
	std := rate(t, app, z.ID, "Standard", 4900, 0, nil)

	if _, err := app.discounts.Create(context.Background(), DiscountInput{
		Code: "SHIPFREE", Title: "Free delivery", Kind: DiscountFreeShipping,
	}); err != nil {
		t.Fatalf("create discount: %v", err)
	}

	p := simpleProduct(t, app, "FREESHIP-1", 10000, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, p.Variants[0].ID, 1)
	if err := app.carts.SetDiscountCode(context.Background(), cart.Token, "SHIPFREE"); err != nil {
		t.Fatalf("apply discount: %v", err)
	}

	in := checkoutInput(cart.Token)
	in.Address.Country = "US"
	in.ShippingRateID = &std.ID
	res, err := app.orders.Checkout(context.Background(), CodeCOD, in, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if res.Order.Shipping.AmountMinor != 0 {
		t.Errorf("shipping = %d with free shipping applied, want 0", res.Order.Shipping.AmountMinor)
	}
	// The method is still what was chosen: the shopper picked Standard and the
	// warehouse still has to send it that way.
	if res.Order.ShippingMethod != "Standard" {
		t.Errorf("shipping_method = %q, want Standard", res.Order.ShippingMethod)
	}
}
