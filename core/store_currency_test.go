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

// An unpaid order waiting to go out leaves the currency open (D212), and keeps
// the currency its shopper was shown. Paying it then fixes the store's.
func TestAnUnpaidOrderLeavesTheCurrencyOpen(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := placeOrder(t, app, "CUR-UNPAID")
	was := order.Currency
	p, err := app.Profile().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.CurrencyLocked {
		t.Fatal("a store whose only order is unpaid reports its currency fixed")
	}
	p.Currency = "EUR"
	if _, err := app.Profile().Set(ctx, *p, nil); err != nil {
		t.Fatalf("change with an unpaid order: %v", err)
	}
	if app.Currency(ctx) != "EUR" {
		t.Fatalf("currency = %q, want EUR", app.Currency(ctx))
	}
	again, err := app.Order().Get(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Currency != was {
		t.Fatalf("the unpaid order moved from %s to %s", was, again.Currency)
	}
	if _, err := app.Pay().MarkPaid(ctx, order.ID, "CUR-UNPAID-REF"); err != nil {
		t.Fatalf("pay the order placed before the change: %v", err)
	}
	if p, _ = app.Profile().Get(ctx); !p.CurrencyLocked {
		t.Fatal("a store with a paid order reports its currency unlocked")
	}
}

// Cash on delivery can be collected before anybody marks the order paid, so an
// order sent out fixes the currency unpaid.
func TestAnOrderSentOutFixesTheCurrency(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := placeOrder(t, app, "CUR-SENT")
	if _, err := app.db.ExecContext(ctx, `UPDATE orders SET status = 'shipped' WHERE id = $1`, order.ID); err != nil {
		t.Fatal(err)
	}
	if p, _ := app.Profile().Get(ctx); !p.CurrencyLocked {
		t.Fatal("a store with a shipped order reports its currency unlocked")
	}
}

// A store opened in the wrong currency can still be put right with the sample
// shop in it, though most sample orders read as paid, and they move with it.
func TestSampleOrdersFollowTheCurrency(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if _, err := app.AddSampleShop(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := app.Profile().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.CurrencyLocked {
		t.Fatal("sample orders fixed the currency")
	}
	p.Currency = "EUR"
	if _, err := app.Profile().Set(ctx, *p, nil); err != nil {
		t.Fatalf("change with the sample shop: %v", err)
	}
	var other int
	if err := app.db.QueryRowContext(ctx, `SELECT count(*) FROM orders WHERE sample AND currency <> 'EUR'`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if other != 0 {
		t.Fatalf("%d sample orders kept the old currency", other)
	}
}

// A store opened in dollars and moved to rupees gets the sample shop's rupee
// prices, not its dollar face values relabelled (D262), and so does adding it
// again. A price the merchant changed is theirs and stays.
func TestTheSampleShopIsPricedAgainInANewCurrency(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if app.Currency(ctx) != "USD" {
		t.Fatalf("this test opens the store in dollars, not %q", app.Currency(ctx))
	}
	if _, err := app.AddSampleShop(ctx); err != nil {
		t.Fatal(err)
	}
	kurta, err := app.Products().GetProductBySlug(ctx, "printed-anarkali-kurta")
	if err != nil {
		t.Fatal(err)
	}
	theirs := kurta.Variants[1].ID
	if _, err := app.db.ExecContext(ctx, `UPDATE variants SET price_minor = 777 WHERE id = $1`, theirs); err != nil {
		t.Fatal(err)
	}
	p, err := app.Profile().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.Currency = "INR"
	if _, err := app.Profile().Set(ctx, *p, nil); err != nil {
		t.Fatalf("move to rupees: %v", err)
	}

	priced := func(when string) {
		t.Helper()
		kurta, err := app.Products().GetProductBySlug(ctx, "printed-anarkali-kurta")
		if err != nil {
			t.Fatal(err)
		}
		v := kurta.Variants[0]
		if v.Price != (Money{AmountMinor: 349900, Currency: "INR"}) || v.CompareAtPrice == nil || v.CompareAtPrice.AmountMinor != 429900 {
			t.Fatalf("%s: kurta = %+v, was %+v; want 349900 INR, was 429900", when, v.Price, v.CompareAtPrice)
		}
	}
	priced("after the move")
	var price int64
	if err := app.db.QueryRowContext(ctx, `SELECT price_minor FROM variants WHERE id = $1`, theirs).Scan(&price); err != nil || price != 777 {
		t.Fatalf("the merchant's price = %d (%v), want 777", price, err)
	}
	var minimum int64
	var title string
	if err := app.db.QueryRowContext(ctx,
		`SELECT min_subtotal_minor, title FROM discounts WHERE sample AND code = 'BUNDLE15'`).Scan(&minimum, &title); err != nil {
		t.Fatal(err)
	}
	if minimum != 500000 || title != "15% off a basket of 5,000 or more" {
		t.Fatalf("BUNDLE15 = %d %q, want the rupee minimum and title", minimum, title)
	}

	if _, err := app.EraseSampleShop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := app.AddSampleShop(ctx); err != nil {
		t.Fatal(err)
	}
	priced("added again")
}

// Once an order is paid the store's currency is fixed: changing it would change
// what that order's amounts mean.
func TestTheCurrencyIsFixedOnceAnOrderIsPaid(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	paidOrder(t, app, "CUR-ORDER", 2500, 3)
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
