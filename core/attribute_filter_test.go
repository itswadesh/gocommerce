package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// Listing products by what a category asked about them.
//
// The answers have been storable since the taxonomy landed — a category
// declares its fields, the editor collects them, and they sit in
// `metadata.category` as `{handle: [values]}`. Nothing could read them back:
// ProductQuery filtered on search, status, vendor, product type, category, tag
// and collection, so a store could record "material: Canvas" on five hundred
// products and had no way to list the canvas ones.
//
// The semantics asserted here are the ones every faceted listing uses, and they
// are not symmetric: several values of ONE attribute widen the result (canvas
// or leather), while several attributes narrow it (canvas AND black). Getting
// that backwards produces a filter that returns nothing as soon as a second box
// is ticked, which is the usual bug.
func TestProductsByAttribute(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	price := int64(4000)

	make := func(title, sku string, meta map[string]any) *Product {
		t.Helper()
		p, err := app.Products().CreateProduct(ctx, ProductInput{
			Title: title, SKU: sku, PriceMinor: &price, Status: "active",
			Metadata: Metadata{"category": meta},
		})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		return p
	}

	canvasBlack := make("Canvas backpack", "ATTR-1", map[string]any{
		"bag-material": []string{"Canvas"},
		"color":        []string{"Black"},
	})
	leatherBlack := make("Leather satchel", "ATTR-2", map[string]any{
		"bag-material": []string{"Leather"},
		"color":        []string{"Black"},
	})
	canvasTan := make("Canvas tote", "ATTR-3", map[string]any{
		"bag-material": []string{"Canvas"},
		"color":        []string{"Tan"},
	})
	// Two answers to one field: a bag can honestly be both.
	mixed := make("Canvas and leather holdall", "ATTR-4", map[string]any{
		"bag-material": []string{"Canvas", "Leather"},
		"color":        []string{"Tan"},
	})
	make("Unanswered duffel", "ATTR-5", map[string]any{})

	ids := func(q ProductQuery) map[int64]bool {
		t.Helper()
		q.Limit = 50
		got, _, err := app.Products().ListProducts(ctx, q)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		out := map[int64]bool{}
		for _, p := range got {
			out[p.ID] = true
		}
		return out
	}
	want := func(got map[int64]bool, label string, expect ...*Product) {
		t.Helper()
		if len(got) != len(expect) {
			t.Errorf("%s: got %d product(s), want %d", label, len(got), len(expect))
		}
		for _, p := range expect {
			if !got[p.ID] {
				t.Errorf("%s: %q is missing", label, p.Title)
			}
		}
	}

	// One value of one attribute.
	want(ids(ProductQuery{Attributes: []AttributeFilter{
		{Key: "bag-material", Values: []string{"Canvas"}},
	}}), "material=Canvas", canvasBlack, canvasTan, mixed)

	// Two values of the SAME attribute widen: canvas or leather.
	want(ids(ProductQuery{Attributes: []AttributeFilter{
		{Key: "bag-material", Values: []string{"Canvas", "Leather"}},
	}}), "material=Canvas|Leather", canvasBlack, leatherBlack, canvasTan, mixed)

	// Two DIFFERENT attributes narrow: canvas and black.
	want(ids(ProductQuery{Attributes: []AttributeFilter{
		{Key: "bag-material", Values: []string{"Canvas"}},
		{Key: "color", Values: []string{"Black"}},
	}}), "material=Canvas & color=Black", canvasBlack)

	// A combination nothing answers is an empty list, not an error.
	want(ids(ProductQuery{Attributes: []AttributeFilter{
		{Key: "bag-material", Values: []string{"Leather"}},
		{Key: "color", Values: []string{"Tan"}},
	}}), "material=Leather & color=Tan", mixed)

	// A value nobody recorded matches nothing.
	want(ids(ProductQuery{Attributes: []AttributeFilter{
		{Key: "bag-material", Values: []string{"Hemp"}},
	}}), "material=Hemp")

	// Matching is exact, not a substring: "Can" is not "Canvas". A LIKE here
	// would quietly match across values and could not use the index.
	want(ids(ProductQuery{Attributes: []AttributeFilter{
		{Key: "bag-material", Values: []string{"Can"}},
	}}), "material=Can")

	// An attribute filter composes with the ordinary ones rather than replacing
	// them: this is what a storefront does when someone narrows inside a search.
	want(ids(ProductQuery{
		Search:     "tote",
		Attributes: []AttributeFilter{{Key: "bag-material", Values: []string{"Canvas"}}},
	}), "search + material", canvasTan)

	// An empty filter is not a filter. A key with no values must not silently
	// become "match nothing", which would make an unticked facet box empty the
	// screen.
	all := ids(ProductQuery{Attributes: []AttributeFilter{{Key: "bag-material"}}})
	if len(all) < 5 {
		t.Errorf("an empty value list filtered the listing down to %d; it should not filter at all", len(all))
	}
}

// The same filter over HTTP, where it arrives as a repeated query parameter.
func TestProductsByAttributeHTTP(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	price := int64(4000)

	for _, tc := range []struct {
		title, sku string
		meta       map[string]any
	}{
		{"Canvas pack", "HATTR-1", map[string]any{"bag-material": []string{"Canvas"}, "color": []string{"Black"}}},
		{"Leather pack", "HATTR-2", map[string]any{"bag-material": []string{"Leather"}, "color": []string{"Black"}}},
		{"Canvas tote", "HATTR-3", map[string]any{"bag-material": []string{"Canvas"}, "color": []string{"Tan"}}},
	} {
		if _, err := app.Products().CreateProduct(ctx, ProductInput{
			Title: tc.title, SKU: tc.sku, PriceMinor: &price, Status: "active",
			Metadata: Metadata{"category": tc.meta},
		}); err != nil {
			t.Fatalf("create %s: %v", tc.title, err)
		}
	}

	count := func(query string) int {
		t.Helper()
		rec := do(t, app, http.MethodGet, "/api/admin/products?"+query, withAdmin)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET ?%s = %d: %s", query, rec.Code, rec.Body.String())
		}
		var body struct {
			Data []struct {
				ID int64 `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return len(body.Data)
	}

	if got := count("attr=bag-material:Canvas"); got != 2 {
		t.Errorf("?attr=bag-material:Canvas returned %d, want 2", got)
	}
	if got := count("attr=bag-material:Canvas&attr=bag-material:Leather"); got != 3 {
		t.Errorf("two values of one attribute returned %d, want 3 (they widen)", got)
	}
	if got := count("attr=bag-material:Canvas&attr=color:Black"); got != 1 {
		t.Errorf("two attributes returned %d, want 1 (they narrow)", got)
	}
	// A value containing a colon survives, because only the first one splits.
	if got := count("attr=color:Black:ish"); got != 0 {
		t.Errorf("a colon inside the value returned %d, want 0 rather than an error", got)
	}
	// Junk is ignored rather than fatal: a hand-edited URL should narrow to
	// nothing or to everything, never 500.
	rec := do(t, app, http.MethodGet, "/api/admin/products?attr=nocolon", withAdmin)
	if rec.Code != http.StatusOK {
		t.Errorf("?attr=nocolon = %d, want it ignored with 200", rec.Code)
	}
}
