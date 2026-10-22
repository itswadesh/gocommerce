package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// What the shop paid for a thing is the operator's number. It is the floor
// under every price the store sets, and a storefront that hands it to every
// visitor hands it to every competitor.

// paid is the recorded cost. Ten digits, so that finding it in a response is
// finding the cost and not a stretch of some timestamp's fraction of a second.
const paid = 8642097531

// costedShelf is an active product whose one variant has a cost recorded the
// way the panel records one, filed in a collection and a category so the
// storefront's shelf routes have it on them too.
func costedShelf(t *testing.T, app *App, sku string, costMinor int64) (*Product, *Collection, *Category) {
	t.Helper()
	ctx := context.Background()
	p := simpleProduct(t, app, sku, 2500, 5)
	v := p.DefaultVariant()
	rec := do(t, app, http.MethodPatch, "/api/admin/variants/"+strconv.FormatInt(v.ID, 10),
		withAdmin, jsonBody(t, map[string]any{"cost_minor": costMinor}))
	if rec.Code != http.StatusOK {
		t.Fatalf("record the cost = %d: %s", rec.Code, rec.Body)
	}
	shelf, err := app.Collections().Create(ctx, CollectionInput{Slug: strings.ToLower(sku) + "-shelf", Title: "Shelf"})
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	if err := app.Collections().SetProductCollections(ctx, p.ID, []int64{shelf.ID}); err != nil {
		t.Fatalf("file in collection: %v", err)
	}
	aisle, err := app.Categories().Create(ctx, CategoryInput{Slug: strings.ToLower(sku) + "-aisle", Title: "Aisle"})
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	if _, err := app.Products().UpdateProduct(ctx, p.ID, ProductPatch{CategoryID: SetID(aisle.ID)}); err != nil {
		t.Fatalf("file in category: %v", err)
	}
	return p, shelf, aisle
}

// Every route a shopper can call that answers with the catalogue, called with
// no credentials. Each must find the product — or the test proves nothing — and
// none may say what it cost.
func TestThePublicCatalogueNeverShowsWhatTheShopPaid(t *testing.T) {
	app := newTestApp(t)
	p, shelf, aisle := costedShelf(t, app, "COSTLY-1", paid)
	v := p.DefaultVariant()
	pid, vid := strconv.FormatInt(p.ID, 10), strconv.FormatInt(v.ID, 10)

	for _, target := range []string{
		"/api/products",
		"/api/products?q=COSTLY",
		"/api/products/" + pid,
		"/api/products/slug/" + p.Slug,
		"/api/products/sku/" + v.SKU,
		"/api/variants?product_id=" + pid,
		"/api/variants/" + vid,
		"/api/collections/" + shelf.Slug,
		"/api/categories/" + aisle.Slug,
	} {
		rec := do(t, app, http.MethodGet, target)
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, v.SKU) {
			t.Errorf("GET %s = %d without the product, so it proves nothing: %s", target, rec.Code, body)
			continue
		}
		if strings.Contains(body, `"cost"`) || strings.Contains(body, strconv.Itoa(paid)) {
			t.Errorf("GET %s tells a shopper what the shop paid: %s", target, body)
		}
	}
}

// The fix must not take the number away from the people it belongs to: the
// panel's product and variant screens are where it is typed and read.
func TestTheOperatorStillSeesWhatTheShopPaid(t *testing.T) {
	app := newTestApp(t)
	p, _, _ := costedShelf(t, app, "COSTLY-2", paid)
	v := p.DefaultVariant()
	pid, vid := strconv.FormatInt(p.ID, 10), strconv.FormatInt(v.ID, 10)
	owner := signInAs(t, app, "owner-cost@example.com", RoleOwner)
	want := `"cost":{"amount_minor":` + strconv.Itoa(paid)

	for _, c := range []struct {
		who    string
		auth   func(*http.Request)
		method string
		target string
		opts   []func(*http.Request)
	}{
		{"the admin token", withAdmin, http.MethodGet, "/api/admin/products/" + pid, nil},
		{"an owner", bearer(owner), http.MethodGet, "/api/admin/products/" + pid, nil},
		{"an owner", bearer(owner), http.MethodGet, "/api/admin/products?q=COSTLY", nil},
		{"an owner", bearer(owner), http.MethodPatch, "/api/admin/variants/" + vid,
			[]func(*http.Request){jsonBody(t, map[string]any{"taxable": true})}},
	} {
		rec := do(t, app, c.method, c.target, append([]func(*http.Request){c.auth}, c.opts...)...)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s %s = %d: %s", c.who, c.method, c.target, rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s reading %s %s does not see the cost: %s", c.who, c.method, c.target, rec.Body)
		}
	}
}

// The rule lives in the variant's JSON, not in each handler: a variant read
// for anybody but an operator on an admin route encodes without its cost, so a
// route written next year — in core or in a module — cannot hand it out by
// forgetting to take it off. The Go value keeps it, because a Go caller asking
// the catalogue is not a shopper.
func TestAVariantKeepsItsCostOutOfJSONUnlessReadForAnOperator(t *testing.T) {
	app := newTestApp(t)
	p, _, _ := costedShelf(t, app, "COSTLY-3", paid)
	id := p.DefaultVariant().ID

	plain, err := app.Products().GetVariant(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Cost == nil || plain.Cost.AmountMinor != paid {
		t.Fatalf("Go caller's cost = %v, want %d: the Go API must not lose it", plain.Cost, paid)
	}
	if raw, _ := json.Marshal(plain); strings.Contains(string(raw), `"cost"`) {
		t.Errorf("a variant read outside an admin route encodes its cost: %s", raw)
	}

	seen, err := app.Products().GetVariant(WithOperatorView(context.Background()), id)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(seen); !strings.Contains(string(raw), `"cost":{"amount_minor":`+strconv.Itoa(paid)) {
		t.Errorf("a variant read for an operator encodes without its cost: %s", raw)
	}
}
