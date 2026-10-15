package gocommerce

import (
	"context"
	"sync"
	"testing"
)

// Under `serve` a context with no store sees no rows, so background work that
// forgot to bind a store would sweep nothing, silently. These run the engine's
// own background passes on a strict app with two stores.

func TestSweepsReachEveryStoreUnderServe(t *testing.T) {
	app := newStrictTestApp(t)
	stores := []string{firstStore(t, app), addTestStore(t, app, "second")}
	for _, s := range stores {
		cart, err := app.Cart().Create(WithStore(context.Background(), s), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app.db.ExecContext(WithStore(context.Background(), s),
			`UPDATE carts SET expires_at = now() - interval '1 hour' WHERE token = $1`, cart.Token); err != nil {
			t.Fatal(err)
		}
	}
	app.runSweepOnce(context.Background())
	for _, s := range stores {
		var n int
		if err := app.db.QueryRowContext(WithStore(context.Background(), s),
			`SELECT count(*) FROM carts`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("store %s still has %d expired empty carts", s, n)
		}
	}
}

func TestAnEventIsDeliveredInItsOwnStore(t *testing.T) {
	app := newStrictTestApp(t)
	b := addTestStore(t, app, "second")
	var mu sync.Mutex
	var seen []string
	app.Subscribe("product.created", func(ctx context.Context, e Event) error {
		s, _ := StoreFrom(ctx)
		mu.Lock()
		seen = append(seen, s)
		mu.Unlock()
		return nil
	})
	price, stock := int64(500), 1
	if _, err := app.Products().CreateProduct(WithStore(context.Background(), b),
		ProductInput{Title: "Mug", Status: ProductActive, SKU: "MUG", PriceMinor: &price, Stock: &stock}); err != nil {
		t.Fatal(err)
	}
	app.deliverOutboxOnce(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != b {
		t.Fatalf("product.created handled bound to %q, want once in %q", seen, b)
	}
}

func TestUnboundWorkCannotWriteUnderServe(t *testing.T) {
	app := newStrictTestApp(t)
	if _, err := app.db.ExecContext(context.Background(),
		`INSERT INTO collections (title, slug) VALUES ('x', 'x')`); err == nil {
		t.Fatal("an unbound write landed somewhere")
	}
}
