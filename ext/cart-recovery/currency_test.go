package cartrecovery

import (
	"context"
	"net/http"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// A record keeps the currency its basket was in (D85), and a store may change
// its currency until its first order, so one store can hold records in two
// currencies. These are the cases that makes possible.

// sellIn moves the store onto code. Core relabels its open baskets; the
// records already made keep the currency they were made in.
func (h *harness) sellIn(code string) {
	h.t.Helper()
	if h.app.Config().Currency == code {
		h.t.Fatalf("the install already sells in %s, so the two currencies could not be told apart", code)
	}
	ctx := context.Background()
	p, err := h.app.Profile().Get(ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	p.Currency = code
	if _, err := h.app.Profile().Set(ctx, *p, nil); err != nil {
		h.t.Fatalf("sell in %s: %v", code, err)
	}
}

// 2500 left in dollars and 5000 left in rupees are not 7500 of anything: the
// figures count what is in the store's currency now, and the count of baskets
// still counts them all.
func TestBasketsInAnotherCurrencyAreNotAddedIn(t *testing.T) {
	h := newHarness(t, Config{StorefrontURL: "https://shop.example.com"}, testAppConfig())
	install := h.app.Config().Currency
	_, before, _ := h.basket("MIX-OLD", "old@example.com", 1)
	h.idle(before, 45)
	h.pass()

	h.sellIn("INR")
	_, after, _ := h.basket("MIX-NEW", "new@example.com", 2)
	h.idle(after, 45)
	h.pass()
	if got := h.record(before).Subtotal; got.Currency != install || got.AmountMinor != 2500 {
		t.Fatalf("the first record = %+v, want 2500 %s as it was made", got, install)
	}

	var s Summary
	gctest.DecodeData(t, gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/cart-recovery/summary", nil), &s)
	if s.Abandoned != 2 {
		t.Errorf("abandoned = %d, want both baskets counted", s.Abandoned)
	}
	if s.Potential.AmountMinor != 5000 || s.Potential.Currency != "INR" {
		t.Errorf("summary potential = %+v, want only the rupee basket's 5000 INR", s.Potential)
	}

	var an Analytics
	gctest.DecodeData(t, gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/cart-recovery/analytics", nil), &an)
	if an.Summary.Potential.AmountMinor != 5000 {
		t.Errorf("analytics potential = %+v, want 5000 INR", an.Summary.Potential)
	}
	var trend int64
	for _, p := range an.Trend {
		trend += p.Potential.AmountMinor
	}
	if trend != 5000 {
		t.Errorf("the trend adds up to %d, want 5000: %+v", trend, an.Trend)
	}
}

// A shopper who comes back to a basket the store relabelled is now pricing it
// in the new currency, and the record follows the basket.
func TestAResumedBasketTakesTheCurrencyItIsNowIn(t *testing.T) {
	h := newHarness(t, Config{StorefrontURL: "https://shop.example.com"}, testAppConfig())
	token, cartID, variant := h.basket("MIX-BACK", "back@example.com", 1)
	h.idle(cartID, 45)
	h.pass()

	h.sellIn("INR")
	if _, err := h.app.Cart().AddLine(context.Background(), token, variant, 1); err != nil {
		t.Fatalf("add line: %v", err)
	}
	if res := h.pass(); res.Resumed != 1 {
		t.Fatalf("pass = %+v, want the record resumed", res)
	}
	if got := h.record(cartID).Subtotal; got.AmountMinor != 5000 || got.Currency != "INR" {
		t.Errorf("the resumed record = %+v, want 5000 INR, the basket as it is now", got)
	}
}

// The order a basket became is in the order's own currency. A basket left in
// dollars and bought after the store moved to rupees was bought in rupees, and
// that order counts toward what the store recovered.
func TestARecoveredOrderIsInItsOwnCurrency(t *testing.T) {
	h := newHarness(t, Config{StorefrontURL: "https://shop.example.com"}, testAppConfig())
	install := h.app.Config().Currency
	token, cartID, _ := h.basket("MIX-BUY", "buyer@example.com", 1)
	h.idle(cartID, 45)
	h.pass()

	h.sellIn("INR")
	result, err := h.app.Order().Checkout(context.Background(), "cod", gocommerce.CheckoutInput{
		CartID: token, Email: "buyer@example.com", Name: "Bina Shah", Phone: "+15555550123",
		Address: gocommerce.Address{Line1: "1 Test Street", City: "Testville", State: "CA",
			PostalCode: "94117", Country: "US"},
	}, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	gctest.DrainOutbox(t, h.app)
	if result.Order.Total.Currency != "INR" {
		t.Fatalf("the order is in %q; the test needs it in INR", result.Order.Total.Currency)
	}

	a := h.record(cartID)
	if a.Subtotal.Currency != install {
		t.Fatalf("the record = %+v, want the basket's %s as it was left", a.Subtotal, install)
	}
	if a.RecoveredOrder == nil || a.RecoveredOrder.Total.Currency != "INR" ||
		a.RecoveredOrder.Total.AmountMinor != result.Order.Total.AmountMinor {
		t.Errorf("recovered order = %+v, want %+v", a.RecoveredOrder, result.Order.Total)
	}

	var s Summary
	gctest.DecodeData(t, gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/cart-recovery/summary", nil), &s)
	if s.RecoveredRevenue != result.Order.Total {
		t.Errorf("recovered revenue = %+v, want the order's %+v", s.RecoveredRevenue, result.Order.Total)
	}
	if s.Potential.AmountMinor != 0 {
		t.Errorf("potential = %+v, want nothing: the only basket was left in %s", s.Potential, install)
	}
}
