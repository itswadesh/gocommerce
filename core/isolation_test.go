package gocommerce

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Two stores in one database, as `serve` runs them, each holding one of
// everything a merchant makes first under the same names. Everything here
// goes through the HTTP API with the store named the way a client names it,
// so it is the whole path — resolution, binding, row security — under test.

type seeded struct {
	store                                  string
	product, variant, collection, category int64
	discount, order                        int64
	orderNumber                            string
}

func seedStore(t *testing.T, app *App, store string) seeded {
	t.Helper()
	ctx := WithStore(context.Background(), store)
	s := seeded{store: store}
	price, stock := int64(1500), 10
	p, err := app.Products().CreateProduct(ctx, ProductInput{Title: "Tee", Status: ProductActive, SKU: "TEE-01", PriceMinor: &price, Stock: &stock})
	if err != nil {
		t.Fatal(err)
	}
	s.product, s.variant = p.ID, p.DefaultVariant().ID
	col, err := app.Collections().Create(ctx, CollectionInput{Title: "Summer"})
	if err != nil {
		t.Fatal(err)
	}
	s.collection = col.ID
	cat, err := app.Categories().Create(ctx, CategoryInput{Title: "Apparel"})
	if err != nil {
		t.Fatal(err)
	}
	s.category = cat.ID
	d, err := app.Discounts().Create(ctx, DiscountInput{Code: "SAVE10", Title: "Ten off", Kind: DiscountPercentage, ValueBP: 1000})
	if err != nil {
		t.Fatal(err)
	}
	s.discount = d.ID
	o := placeOrderIn(t, app, ctx, "ORDER-"+store[len(store)-4:])
	s.order, s.orderNumber = o.ID, o.Number
	return s
}

func twoStores(t *testing.T) (*App, seeded, seeded) {
	t.Helper()
	app := newStrictTestApp(t)
	a := seedStore(t, app, firstStore(t, app))
	b := seedStore(t, app, addTestStore(t, app, "b"))
	return app, a, b
}

func asStore(store string) func(*http.Request) { return header("x-litekart-store", store) }

func isoBody(body string) func(*http.Request) {
	return func(r *http.Request) {
		r.Body = http.NoBody
		if body != "" {
			r.Body = ioNopCloser(strings.NewReader(body))
			r.ContentLength = int64(len(body))
		}
		r.Header.Set("Content-Type", "application/json")
	}
}

func TestStoreBCannotSeeOrTouchStoreAsRows(t *testing.T) {
	app, a, b := twoStores(t)
	reads := []string{
		fmt.Sprintf("/api/products/%d", a.product),
		fmt.Sprintf("/api/admin/collections/%d", a.collection),
		fmt.Sprintf("/api/admin/categories/%d", a.category),
		fmt.Sprintf("/api/admin/discounts/%d", a.discount),
		fmt.Sprintf("/api/admin/orders/%d", a.order),
	}
	for _, path := range reads {
		if rec := do(t, app, http.MethodGet, path, withAdmin, asStore(b.store)); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s as store B = %d, want 404: %s", path, rec.Code, rec.Body)
		}
		if rec := do(t, app, http.MethodGet, path, withAdmin, asStore(a.store)); rec.Code != http.StatusOK {
			t.Errorf("GET %s as store A = %d, want 200 (the test would prove nothing): %s", path, rec.Code, rec.Body)
		}
	}
	productA := fmt.Sprintf("/api/admin/products/%d", a.product)
	if rec := do(t, app, http.MethodPatch, productA, withAdmin, asStore(b.store), isoBody(`{"title":"Hijacked"}`)); rec.Code != http.StatusNotFound {
		t.Errorf("PATCH store A's product as B = %d, want 404", rec.Code)
	}
	if rec := do(t, app, http.MethodDelete, productA, withAdmin, asStore(b.store)); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE store A's product as B = %d, want 404", rec.Code)
	}
	// Still there, untouched, for store A.
	got, err := app.Products().GetProduct(WithStore(context.Background(), a.store), a.product)
	if err != nil || got.Title != "Tee" {
		t.Fatalf("store A's product after B's attempts: %+v, %v", got, err)
	}
}

func TestStoreBsListsHoldOnlyStoreBsRows(t *testing.T) {
	app, a, b := twoStores(t)
	rec := do(t, app, http.MethodGet, "/api/admin/orders", withAdmin, asStore(b.store))
	if rec.Code != http.StatusOK {
		t.Fatalf("list orders as B = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, fmt.Sprintf(`"id":%d,`, a.order)) {
		t.Errorf("store B's order list includes store A's order %d", a.order)
	}
	if !strings.Contains(body, fmt.Sprintf(`"id":%d,`, b.order)) {
		t.Errorf("store B's order list lacks its own order %d: %s", b.order, body)
	}
}

func TestStoreBCannotUseStoreAsThings(t *testing.T) {
	app, a, b := twoStores(t)
	ctxB := WithStore(context.Background(), b.store)
	cart, err := app.Cart().Create(ctxB, "")
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, app, http.MethodPost, "/api/carts/"+cart.Token+"/line-items", asStore(b.store),
		isoBody(fmt.Sprintf(`{"variant_id": %d, "quantity": 1}`, a.variant)))
	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("adding store A's variant to store B's cart = %d, want a 4xx: %s", rec.Code, rec.Body)
	}
	// A code only store A has does nothing in store B.
	if _, err := app.Discounts().Create(WithStore(context.Background(), a.store),
		DiscountInput{Code: "ONLYA", Title: "A only", Kind: DiscountPercentage, ValueBP: 500}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Cart().AddLine(ctxB, cart.Token, b.variant, 1); err != nil {
		t.Fatal(err)
	}
	rec = do(t, app, http.MethodPut, "/api/carts/"+cart.Token+"/discount", asStore(b.store), isoBody(`{"code":"ONLYA"}`))
	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("store A's code in store B = %d, want a 4xx: %s", rec.Code, rec.Body)
	}
	// And store A cannot read store B's cart by its token.
	if rec := do(t, app, http.MethodGet, "/api/carts/"+cart.Token, asStore(a.store)); rec.Code != http.StatusNotFound {
		t.Fatalf("store A reading store B's cart = %d, want 404", rec.Code)
	}
}

func TestBothStoresCanUseTheSameNames(t *testing.T) {
	_, a, b := twoStores(t) // the same SKU, collection, category and code in each
	if a.orderNumber != "GC-000001" || b.orderNumber != "GC-000001" {
		t.Fatalf("first orders = %s and %s, want GC-000001 in each store", a.orderNumber, b.orderNumber)
	}
}

type nopCloser struct{ *strings.Reader }

func (nopCloser) Close() error { return nil }

func ioNopCloser(r *strings.Reader) nopCloser { return nopCloser{r} }

// The suite above passes because of the row policy, not by accident: with the
// policy switched off on products, store B can read store A's product. If this
// ever stops leaking, the isolation tests have stopped measuring anything.
func TestWithoutTheRowPolicyStoreBWouldSeeStoreA(t *testing.T) {
	app, a, b := twoStores(t)
	path := fmt.Sprintf("/api/products/%d", a.product)
	if _, err := app.db.ExecContext(context.Background(),
		`ALTER TABLE products NO FORCE ROW LEVEL SECURITY, DISABLE ROW LEVEL SECURITY`); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, app, http.MethodGet, path, withAdmin, asStore(b.store)); rec.Code != http.StatusOK {
		t.Fatalf("with the policy off, GET store A's product as B = %d; the isolation suite is not measuring the policy", rec.Code)
	}
}
