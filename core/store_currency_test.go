package gocommerce

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// A store sells in the install's currency until it chooses its own; choosing
// one before any order relabels what it has priced so far, and everything made
// afterwards — products, carts — is in it.
func TestAStoreChoosesItsCurrencyBeforeItsFirstOrder(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if got := app.Currency(ctx); got != app.cfg.Currency {
		t.Fatalf("default currency = %q, want the install's %q", got, app.cfg.Currency)
	}
	before := simpleProduct(t, app, "CUR-OLD", 2500, 3)

	p, err := app.Profile().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.Currency != app.cfg.Currency || p.CurrencyLocked {
		t.Fatalf("profile = %q locked=%v", p.Currency, p.CurrencyLocked)
	}
	p.Currency = "inr"
	saved, err := app.Profile().Set(ctx, *p, nil)
	if err != nil {
		t.Fatalf("choose INR: %v", err)
	}
	if saved.Currency != "INR" || app.Currency(ctx) != "INR" {
		t.Fatalf("after choosing: profile %q, store %q", saved.Currency, app.Currency(ctx))
	}

	relabelled, err := app.Products().GetProduct(ctx, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := relabelled.DefaultVariant().Price.Currency; got != "INR" {
		t.Errorf("a product priced before the change is in %q, want INR", got)
	}
	cart := newCart(t, app)
	if cart.Currency != "INR" {
		t.Errorf("a new cart is in %q, want INR", cart.Currency)
	}

	rec := do(t, app, http.MethodGet, "/api/admin/settings", withAdmin)
	var settings struct {
		Currency string `json:"currency"`
	}
	decodeData(t, rec, &settings)
	if settings.Currency != "INR" {
		t.Errorf("settings currency = %q, want INR", settings.Currency)
	}
}

// Once a store has an order its currency is fixed: changing it would change
// what that order's amounts mean.
func TestTheCurrencyIsFixedOnceTheStoreHasAnOrder(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	placeOrder(t, app, "CUR-ORDER")
	p, err := app.Profile().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !p.CurrencyLocked {
		t.Fatal("a store with an order reports its currency unlocked")
	}
	p.Currency = "EUR"
	_, err = app.Profile().Set(ctx, *p, nil)
	var api *APIError
	if !errors.As(err, &api) || api.Status != http.StatusConflict {
		t.Fatalf("change after an order = %v, want 409", err)
	}
	if app.Currency(ctx) != app.cfg.Currency {
		t.Fatalf("a refused change moved the currency to %q", app.Currency(ctx))
	}
	// Saving the rest of the profile with the same currency still works.
	p.Currency = app.cfg.Currency
	p.City = "Pune"
	if _, err := app.Profile().Set(ctx, *p, nil); err != nil {
		t.Fatalf("saving the profile unchanged: %v", err)
	}
}

func TestACurrencyIsAThreeLetterCode(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	p, _ := app.Profile().Get(ctx)
	for _, bad := range []string{"RUPEES", "IN", "1NR"} {
		p.Currency = bad
		var api *APIError
		if _, err := app.Profile().Set(ctx, *p, nil); !errors.As(err, &api) || api.Status != http.StatusBadRequest {
			t.Errorf("%q: err = %v, want 400", bad, err)
		}
	}
	// Empty leaves it as it is, so a client that does not send it cannot
	// reset it.
	p.Currency = ""
	if saved, err := app.Profile().Set(ctx, *p, nil); err != nil || saved.Currency != app.cfg.Currency {
		t.Fatalf("empty: %+v %v", saved, err)
	}
}

// A store can be opened in its own currency; a code that is not one opens
// nothing.
func TestAStoreIsOpenedInTheCurrencyItAsksFor(t *testing.T) {
	app := newTestApp(t)
	tok := signInAs(t, app, "rupee@example.com", RoleStaff)
	if rec := doBody(t, app, http.MethodPost, "/api/stores/create", `{"name":"Bad","slug":"bad","currency":"RUPEES"}`, bearer(tok)); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad currency = %d: %s", rec.Code, rec.Body)
	}
	if _, err := app.Stores().BySlug(context.Background(), "bad"); err == nil {
		t.Fatal("a refused create left a store behind")
	}
	rec := doBody(t, app, http.MethodPost, "/api/stores/create", `{"name":"Rupee Shop","slug":"rupee","currency":"inr"}`, bearer(tok))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body)
	}
	st, err := app.Stores().BySlug(context.Background(), "rupee")
	if err != nil {
		t.Fatal(err)
	}
	if got := app.Currency(WithStore(context.Background(), st.ID)); got != "INR" {
		t.Fatalf("new store's currency = %q, want INR", got)
	}
}

// Each store has its own: another store's choice does not move this one.
func TestEachStoreHasItsOwnCurrency(t *testing.T) {
	app := newTestApp(t)
	other := WithStore(context.Background(), addTestStore(t, app, "rupee-shop"))
	p, err := app.Profile().Get(other)
	if err != nil {
		t.Fatal(err)
	}
	p.Currency = "INR"
	if _, err := app.Profile().Set(other, *p, nil); err != nil {
		t.Fatal(err)
	}
	if app.Currency(other) != "INR" || app.Currency(context.Background()) != app.cfg.Currency {
		t.Fatalf("other %q, this %q", app.Currency(other), app.Currency(context.Background()))
	}
}

// An order that was cancelled before any money moved leaves nothing whose
// amounts a new currency would change, so it does not fix the currency. One
// that was paid does: the payment and its refund were in the old currency.
func TestOnlyCancelledUnpaidOrdersLeaveTheCurrencyOpen(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := placeOrder(t, app, "CUR-CANCEL")
	if _, err := app.Order().Cancel(ctx, order.ID, "test"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	p, err := app.Profile().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.CurrencyLocked {
		t.Fatal("a store whose only order was cancelled unpaid reports its currency fixed")
	}
	p.Currency = "INR"
	if _, err := app.Profile().Set(ctx, *p, nil); err != nil {
		t.Fatalf("change after a cancelled order: %v", err)
	}
	if app.Currency(ctx) != "INR" {
		t.Fatalf("currency = %q, want INR", app.Currency(ctx))
	}

	paid := paidOrder(t, app, "CUR-PAID", 2500, 3)
	if _, err := app.Order().Cancel(ctx, paid.ID, "test"); err != nil {
		t.Fatalf("cancel the paid order: %v", err)
	}
	if p, _ = app.Profile().Get(ctx); !p.CurrencyLocked {
		t.Fatal("a store with a paid, cancelled order reports its currency unlocked")
	}
}
