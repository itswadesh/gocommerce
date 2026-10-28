package gocommerce

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

// The master tree and a store's activations of it (D146).

func activate(t *testing.T, app *App, ctx context.Context, id string) *TaxonomyActivation {
	t.Helper()
	res, err := app.Categories().ActivateTaxonomy(ctx, id)
	if err != nil {
		t.Fatalf("activate %s: %v", id, err)
	}
	return res
}

func TestTaxonomyMasterIsTheWholeEmbeddedTree(t *testing.T) {
	m := taxonomyMaster()
	if len(m.nodes) < 14000 {
		t.Fatalf("master has %d nodes, want the whole published tree", len(m.nodes))
	}
	n, ok := m.node("gid://shopify/TaxonomyCategory/aa-1-1-1")
	if !ok || n.ParentID != "aa-1-1" || n.Depth != 3 ||
		n.FullName != "Apparel & Accessories / Clothing / Activewear / Activewear Pants" {
		t.Fatalf("aa-1-1-1 = %+v", n)
	}
	if got := m.chain(n); len(got) != 4 || got[0].ID != "aa" || got[3].ID != "aa-1-1-1" {
		t.Errorf("chain = %+v", got)
	}
}

// Activating a deep node brings its ancestors, with the master's names, the
// fields the taxonomy names follow, and a second activation writes nothing.
func TestActivateTaxonomyBringsAncestorsAndIsIdempotent(t *testing.T) {
	app := categoriesApp(t)
	ctx := context.Background()
	svc := app.Categories()

	first := activate(t, app, ctx, "aa-1-1")
	if first.Created != 3 {
		t.Errorf("created = %d, want the node and its two ancestors", first.Created)
	}
	if c := first.Category; c.Title != "Activewear" || c.Slug != "activewear" ||
		c.FullName != "Apparel & Accessories / Clothing / Activewear" ||
		c.Metadata["taxonomy_gid"] != "gid://shopify/TaxonomyCategory/aa-1-1" {
		t.Errorf("category = %+v", c)
	}
	if _, ok := first.Category.Metadata["attributes"]; !ok {
		t.Error("the taxonomy's fields did not follow the activated category")
	}
	if _, err := svc.GetAttribute(ctx, "color"); err != nil {
		t.Errorf("the dictionary entry a field names was not written: %v", err)
	}

	again := activate(t, app, ctx, "aa-1-1")
	if again.Created != 0 || again.Category.ID != first.Category.ID {
		t.Errorf("second activation = %+v, want the same category and nothing created", again)
	}
	// A sibling reuses the ancestors already there.
	if sib := activate(t, app, ctx, "aa-1-1-1"); sib.Created != 1 {
		t.Errorf("child activation created %d, want 1", sib.Created)
	}
	all, _ := svc.List(ctx)
	if len(all) != 4 {
		t.Errorf("store has %d categories, want 4", len(all))
	}

	// Browsing shows which are active.
	kids, err := svc.TaxonomyChildren(ctx, "aa-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range kids {
		if want := k.ID == "aa-1-1"; k.Active != want {
			t.Errorf("%s active = %v, want %v", k.ID, k.Active, want)
		}
	}
}

// A category the store typed by hand at the same place is adopted, not
// duplicated, and keeps its own fields; a renamed one keeps its link.
func TestActivateTaxonomyAdoptsAndKeepsRenames(t *testing.T) {
	app := categoriesApp(t)
	ctx := context.Background()
	svc := app.Categories()

	mine := newCategory(t, app, CategoryInput{Title: "apparel & accessories",
		Metadata: Metadata{"attributes": []any{map[string]any{"key": "fit", "label": "Fit"}}}})
	res := activate(t, app, ctx, "aa-1")
	if res.Created != 1 {
		t.Errorf("created = %d, want only Clothing", res.Created)
	}
	got, _ := svc.Get(ctx, mine.ID)
	if got.Metadata["taxonomy_gid"] != "gid://shopify/TaxonomyCategory/aa" {
		t.Errorf("hand-typed root was not adopted: %+v", got.Metadata)
	}
	if attrs, _ := got.Metadata["attributes"].([]any); len(attrs) != 1 {
		t.Errorf("adopted category's own fields were replaced: %+v", got.Metadata["attributes"])
	}

	// Rename the activated category; it stays the master's node.
	title := "Clothes"
	if _, err := svc.Update(ctx, res.Category.ID, CategoryPatch{Title: &title}); err != nil {
		t.Fatal(err)
	}
	kids, _ := svc.TaxonomyChildren(ctx, "aa")
	for _, k := range kids {
		if k.ID == "aa-1" && (!k.Active || k.Title != "Clothes" || k.Name != "Clothing") {
			t.Errorf("renamed node = %+v", k)
		}
	}
	if again := activate(t, app, ctx, "aa-1"); again.Created != 0 {
		t.Error("a renamed category was activated a second time")
	}
}

// Deactivating removes the branch, refuses while products or the store's own
// categories sit in it, and is a no-op on an inactive node.
func TestDeactivateTaxonomy(t *testing.T) {
	app := categoriesApp(t)
	ctx := context.Background()
	svc := app.Categories()

	leaf := activate(t, app, ctx, "aa-1-1").Category
	price := int64(100)
	p, err := app.Products().CreateProduct(ctx, ProductInput{Title: "Leggings", PriceMinor: &price})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Products().UpdateProduct(ctx, p.ID, ProductPatch{CategoryID: NullableID{Present: true, Value: &leaf.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeactivateTaxonomy(ctx, "aa"); checkoutStatus(err) != http.StatusConflict {
		t.Fatalf("deactivate with a product filed under it = %v, want a conflict", err)
	}
	if _, err := app.Products().UpdateProduct(ctx, p.ID, ProductPatch{CategoryID: NullableID{Present: true}}); err != nil {
		t.Fatal(err)
	}

	own := newCategory(t, app, CategoryInput{Title: "Yoga", ParentID: &leaf.ID})
	if err := svc.DeactivateTaxonomy(ctx, "aa-1"); checkoutStatus(err) != http.StatusConflict {
		t.Fatalf("deactivate over the store's own category = %v, want a conflict", err)
	}
	if err := svc.Delete(ctx, own.ID); err != nil {
		t.Fatal(err)
	}

	if err := svc.DeactivateTaxonomy(ctx, "aa-1"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	all, _ := svc.List(ctx)
	if len(all) != 1 || all[0].Title != "Apparel & Accessories" {
		t.Errorf("left %v, want only the root", titles(all))
	}
	if err := svc.DeactivateTaxonomy(ctx, "aa-1"); err != nil {
		t.Errorf("deactivating an inactive node = %v, want a no-op", err)
	}
	if err := svc.DeactivateTaxonomy(ctx, "zz-nope"); checkoutStatus(err) != http.StatusNotFound {
		t.Errorf("unknown node = %v, want not found", err)
	}
}

// What a store activated is its own: another store sees the node inactive,
// its storefront lists none of it, and it activates its own copy with the
// same slug.
func TestTaxonomyActivationIsPerStore(t *testing.T) {
	app := newStrictTestApp(t)
	a := firstStore(t, app)
	b := addTestStore(t, app, "b")
	ctxA, ctxB := WithStore(context.Background(), a), WithStore(context.Background(), b)

	activate(t, app, ctxA, "aa-1")
	roots, err := app.Categories().TaxonomyChildren(ctxB, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range roots {
		if n.Active {
			t.Errorf("store b sees %s active", n.ID)
		}
	}
	if list, _ := app.Categories().List(ctxB); len(list) != 0 {
		t.Errorf("store b has %v", titles(list))
	}
	resB := activate(t, app, ctxB, "aa-1")
	if resB.Created != 2 || resB.Category.Slug != "clothing" {
		t.Errorf("store b activation = %+v", resB)
	}
	if err := app.Categories().DeactivateTaxonomy(ctxB, "aa"); err != nil {
		t.Fatal(err)
	}
	if list, _ := app.Categories().List(ctxA); len(list) != 2 {
		t.Errorf("store b's deactivation reached store a: %v", titles(list))
	}
}

// Over HTTP: browse, search, activate, and the storefront lists only what is
// active.
func TestTaxonomyRoutesAndPublicList(t *testing.T) {
	app := taxonomyApp(t)
	if !hasRoute(app, "/api/categories") {
		app.mountCategoryRoutes()
	}

	rec := do(t, app, http.MethodGet, "/api/admin/taxonomy?q=activewear%20pants", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d: %s", rec.Code, rec.Body)
	}
	var found []TaxonomyNode
	decodeData(t, rec, &found)
	if len(found) == 0 || found[0].ID != "aa-1-1-1" {
		t.Fatalf("search found %+v", found)
	}

	rec = do(t, app, http.MethodPut, "/api/admin/taxonomy/aa-1-1-1/activation", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("activate = %d: %s", rec.Code, rec.Body)
	}

	var public []*Category
	decodeData(t, do(t, app, http.MethodGet, "/api/categories?flat=1"), &public)
	want := []string{"Apparel & Accessories", "Clothing", "Activewear", "Activewear Pants"}
	if got := titles(public); !equalStrings(got, want) {
		t.Errorf("public list = %v, want %v", got, want)
	}

	rec = do(t, app, http.MethodGet, "/api/admin/taxonomy?parent=aa-1-1", withAdmin)
	var kids []TaxonomyNode
	decodeData(t, rec, &kids)
	if len(kids) == 0 || !kids[0].Active || kids[0].CategoryID == nil {
		t.Errorf("children = %+v", kids)
	}

	rec = do(t, app, http.MethodDelete, "/api/admin/taxonomy/aa-1-1/activation", withAdmin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("deactivate = %d: %s", rec.Code, rec.Body)
	}
	decodeData(t, do(t, app, http.MethodGet, "/api/categories?flat=1"), &public)
	if got := titles(public); !equalStrings(got, want[:2]) {
		t.Errorf("public list after deactivating = %v", got)
	}
	if rec := do(t, app, http.MethodGet, "/api/admin/taxonomy?parent=nope", withAdmin); rec.Code != http.StatusNotFound {
		t.Errorf("unknown parent = %d", rec.Code)
	}
	if rec := do(t, app, http.MethodPut, fmt.Sprintf("/api/admin/taxonomy/%s/activation", "aa"),
		header("Authorization", "")); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated activate = %d", rec.Code)
	}
}

func hasRoute(app *App, path string) bool {
	for _, r := range app.Routes() {
		if r.Path == path {
			return true
		}
	}
	return false
}
