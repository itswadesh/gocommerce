package gocommerce

import (
	"context"
	"testing"
)

// The sold count (D134): counted from real orders, rounded down, shown only
// when the shop says so and past its threshold.

func TestSoldBandRoundsDown(t *testing.T) {
	for _, c := range []struct{ n, want int64 }{
		{0, 0}, {9, 0}, {10, 10}, {34, 30}, {99, 90}, {100, 100}, {999, 900},
		{1000, 1000}, {45399, 45000}, {120500, 120000},
	} {
		if got := soldBand(c.n); got != c.want {
			t.Errorf("soldBand(%d) = %d, want %d", c.n, got, c.want)
		}
	}
}

func switchPlugin(t *testing.T, app *App, key string, on bool, settings map[string]any) {
	t.Helper()
	if _, err := app.Plugins().Update(context.Background(), key, PluginPatch{Enabled: &on, Settings: settings}); err != nil {
		t.Fatalf("switch %s: %v", key, err)
	}
}

func TestSoldCountIsCountedFromRealOrdersOnly(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	p := simpleProduct(t, app, "SOLD-1", 1000, 500)
	variant := p.DefaultVariant().ID

	order := func(qty int) *Order {
		cart := newCart(t, app)
		addToCart(t, app, cart.Token, variant, qty)
		res, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
		if err != nil {
			t.Fatalf("checkout: %v", err)
		}
		return res.Order
	}
	order(20)
	order(14)
	cancelled := order(50)
	if _, err := app.Order().Cancel(ctx, cancelled.ID, "changed their mind"); err != nil {
		t.Fatal(err)
	}
	sample := order(60)
	if _, err := app.db.ExecContext(ctx, `UPDATE orders SET sample = true WHERE id = $1`, sample.ID); err != nil {
		t.Fatal(err)
	}

	refresh := func() {
		t.Helper()
		if err := app.Products().RefreshSalesCounts(ctx); err != nil {
			t.Fatalf("refresh: %v", err)
		}
	}

	// Off by default: nothing is counted and nothing is said.
	refresh()
	if got := getPublicProduct(t, app, p.ID); got.SoldAtLeast != nil {
		t.Fatalf("sold_at_least = %d with the feature off", *got.SoldAtLeast)
	}

	switchPlugin(t, app, SoldCountPluginKey, true, nil)
	refresh()
	got := getPublicProduct(t, app, p.ID)
	if got.SoldAtLeast == nil || *got.SoldAtLeast != 30 {
		t.Fatalf("sold_at_least = %v, want 30 (34 sold: not the cancelled 50 nor the sample 60)", got.SoldAtLeast)
	}

	// Under the shop's threshold the count is not shown at all.
	switchPlugin(t, app, SoldCountPluginKey, true, map[string]any{"threshold": 40})
	if got := getPublicProduct(t, app, p.ID); got.SoldAtLeast != nil {
		t.Errorf("sold_at_least = %d under a threshold of 40", *got.SoldAtLeast)
	}

	// A product nobody bought says nothing.
	other := simpleProduct(t, app, "SOLD-2", 1000, 5)
	switchPlugin(t, app, SoldCountPluginKey, true, map[string]any{"threshold": 1})
	if got := getPublicProduct(t, app, other.ID); got.SoldAtLeast != nil {
		t.Errorf("an unsold product says %d sold", *got.SoldAtLeast)
	}
}
