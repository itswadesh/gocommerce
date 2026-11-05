package gocommerce

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

type precheckModule struct {
	fn    CheckoutPrecheck
	calls int
}

func (m *precheckModule) Name() string            { return "precheck" }
func (m *precheckModule) Migrations() []Migration { return nil }
func (m *precheckModule) Register(app *App) error {
	app.RegisterCheckoutPrecheck(func(ctx context.Context, c *CheckoutIntent) error {
		m.calls++
		return m.fn(ctx, c)
	})
	return nil
}

func TestACheckoutPrecheckRefusesBeforeAnythingIsReserved(t *testing.T) {
	mod := &precheckModule{}
	app := newTestApp(t, mod)
	ctx := context.Background()
	variant := simpleProduct(t, app, "PRE-1", 2500, 5).DefaultVariant()
	cart, err := app.Cart().Create(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Cart().AddLine(ctx, cart.Token, variant.ID, 1); err != nil {
		t.Fatal(err)
	}
	in := CheckoutInput{CartID: cart.Token, Email: "a@example.com", Name: "A Shopper", Address: Address{
		Line1: "1 Main St", City: "Austin", State: "TX", PostalCode: "78701", Country: "US",
	}}

	// A broken module is a 500, not a refusal the shopper reads.
	mod.fn = func(context.Context, *CheckoutIntent) error { return errors.New("boom") }
	var apiErr *APIError
	if _, err := app.Order().Checkout(ctx, CodeCOD, in, ""); !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
		t.Fatalf("broken precheck = %v, want a 500", err)
	}

	mod.fn = func(_ context.Context, c *CheckoutIntent) error {
		if c.Method != CodeCOD || c.Input.CartID != cart.Token {
			t.Errorf("intent = %+v", c)
		}
		return Forbiddenf("not this one")
	}
	if _, err := app.Order().Checkout(ctx, CodeCOD, in, ""); !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("refusing precheck = %v, want a 403", err)
	}
	if orders, _, err := app.Order().List(ctx, OrderQuery{}); err != nil || len(orders) != 0 {
		t.Fatalf("orders after a refusal = %v, %v; want none", orders, err)
	}

	// A retry of an order already made is not asked again: its answer may have
	// changed, and the shopper's order exists.
	mod.fn = func(context.Context, *CheckoutIntent) error { return nil }
	first, err := app.Order().Checkout(ctx, CodeCOD, in, "key-1")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	calls := mod.calls
	mod.fn = func(context.Context, *CheckoutIntent) error { return Forbiddenf("changed its mind") }
	again, err := app.Order().Checkout(ctx, CodeCOD, in, "key-1")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if again.Order.ID != first.Order.ID || mod.calls != calls {
		t.Fatalf("replay made order %d (first %d) with %d more prechecks", again.Order.ID, first.Order.ID, mod.calls-calls)
	}
}
