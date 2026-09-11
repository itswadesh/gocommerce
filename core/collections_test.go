package gocommerce

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// The collections suite. Collections are merchandising, so nothing here is a
// state machine and nothing emits an event — what can go wrong is quieter:
// membership that appends where it should replace, a tag list whose order
// depends on typing order, and a delete that takes the products with it.

// ---------------------------------------------------------------- fixtures

// collectionsApp boots an engine with the collection routes mounted.
//
// mountCollectionRoutes is called from mountCoreRoutes in the shipped wiring;
// mounting it a second time would panic on a duplicate ServeMux pattern, so
// this checks first and the suite passes either side of that line landing.
func collectionsApp(t *testing.T) *App {
	t.Helper()
	app := newTestApp(t)
	for _, r := range app.Routes() {
		if r.Path == "/api/collections" {
			return app
		}
	}
	app.mountCollectionRoutes()
	return app
}

func newCollection(t *testing.T, app *App, in CollectionInput) *Collection {
	t.Helper()
	c, err := app.Collections().Create(context.Background(), in)
	if err != nil {
		t.Fatalf("create collection %q: %v", in.Title, err)
	}
	return c
}

// taggedProduct creates an active product carrying the organisation fields.
func taggedProduct(t *testing.T, app *App, sku string, in ProductInput) *Product {
	t.Helper()
	price := int64(1000)
	stock := 5
	in.Title = "Product " + sku
	in.Status = ProductActive
	in.SKU = sku
	in.PriceMinor = &price
	in.Stock = &stock
	p, err := app.Products().CreateProduct(context.Background(), in)
	if err != nil {
		t.Fatalf("create product %s: %v", sku, err)
	}
	return p
}

func collectionIDs(products []*Product) []string {
	out := make([]string, 0, len(products))
	for _, p := range products {
		out = append(out, p.Slug)
	}
	return out
}

// ------------------------------------------------------------------- CRUD

func TestCollectionCRUD(t *testing.T) {
	app := collectionsApp(t)
	ctx := context.Background()
	svc := app.Collections()

	created := newCollection(t, app, CollectionInput{
		Title: "Summer Sale", Description: "  Hot things.  ",
		Metadata: Metadata{"hero": "summer.jpg"},
	})
	if created.Slug != "summer-sale" {
		t.Errorf("slug = %q, want summer-sale", created.Slug)
	}
	if created.Description != "Hot things." {
		t.Errorf("description = %q, want it trimmed", created.Description)
	}
	if created.Metadata["hero"] != "summer.jpg" {
		t.Errorf("metadata = %v, want the hero key preserved", created.Metadata)
	}

	byID, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	bySlug, err := svc.GetBySlug(ctx, "summer-sale")
	if err != nil {
		t.Fatalf("GetBySlug: %v", err)
	}
	if byID.ID != created.ID || bySlug.ID != created.ID {
		t.Errorf("Get/GetBySlug returned %d/%d, want %d", byID.ID, bySlug.ID, created.ID)
	}

	newCollection(t, app, CollectionInput{Title: "Winter", Position: ptr(1)})
	list, total, err := svc.List(ctx, 50, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Fatalf("List returned %d of %d, want 2 of 2", len(list), total)
	}
	// position 0 before position 1, whatever order they were created in.
	if list[0].Title != "Summer Sale" || list[1].Title != "Winter" {
		t.Errorf("List order = %q, %q; want Summer Sale then Winter", list[0].Title, list[1].Title)
	}

	updated, err := svc.Update(ctx, created.ID, CollectionPatch{
		Title: ptr("Summer Edit"), Position: ptr(9),
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Title != "Summer Edit" || updated.Position != 9 {
		t.Errorf("Update = %q/%d, want Summer Edit/9", updated.Title, updated.Position)
	}
	// A patch that mentions neither must not touch what it did not mention.
	if updated.Slug != "summer-sale" {
		t.Errorf("slug = %q after a title patch, want it unchanged", updated.Slug)
	}

	if err := svc.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete = %v, want a not-found error", err)
	}
	if err := svc.Delete(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete = %v, want a not-found error", err)
	}
}

func TestCollectionSlugDerivationAndCollision(t *testing.T) {
	app := collectionsApp(t)
	ctx := context.Background()
	svc := app.Collections()

	derived := newCollection(t, app, CollectionInput{Title: "New In — Spring 2026!"})
	if derived.Slug != "new-in-spring-2026" {
		t.Errorf("derived slug = %q, want new-in-spring-2026", derived.Slug)
	}

	// The same title twice derives the same slug, and the unique index is what
	// reports it — a conflict, not a silent second collection with the same URL.
	_, err := svc.Create(ctx, CollectionInput{Title: "New In — Spring 2026!"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate slug = %v, want a conflict", err)
	}

	// An explicit slug sidesteps the collision.
	explicit := newCollection(t, app, CollectionInput{
		Title: "New In — Spring 2026!", Slug: "new-in-spring-2026-b",
	})
	if explicit.Slug != "new-in-spring-2026-b" {
		t.Errorf("slug = %q, want the one supplied", explicit.Slug)
	}

	// Patching onto a taken slug is the same conflict from the other side.
	if _, err := svc.Update(ctx, explicit.ID, CollectionPatch{Slug: ptr("new-in-spring-2026")}); !errors.Is(err, ErrConflict) {
		t.Errorf("patch onto a taken slug = %v, want a conflict", err)
	}

	// A title with nothing slug-safe in it cannot derive one, and saying so is
	// better than inventing "untitled-4".
	if _, err := svc.Create(ctx, CollectionInput{Title: "!!!"}); !errors.Is(err, ErrValidation) {
		t.Errorf("unsluggable title = %v, want a validation error", err)
	}
	if _, err := svc.Create(ctx, CollectionInput{Title: "   "}); !errors.Is(err, ErrValidation) {
		t.Errorf("blank title = %v, want a validation error", err)
	}
}

// ------------------------------------------------------------- membership

// SetProductCollections replaces. An earlier draft appended, and a product
// removed from a collection in the panel came back on the next save.
func TestSetProductCollectionsReplaces(t *testing.T) {
	app := collectionsApp(t)
	ctx := context.Background()
	svc := app.Collections()

	p := simpleProduct(t, app, "SKU-MEMBER", 1500, 3)
	summer := newCollection(t, app, CollectionInput{Title: "Summer"})
	winter := newCollection(t, app, CollectionInput{Title: "Winter"})
	sale := newCollection(t, app, CollectionInput{Title: "Sale"})

	if err := svc.SetProductCollections(ctx, p.ID, []int64{summer.ID, winter.ID}); err != nil {
		t.Fatalf("first set: %v", err)
	}
	if err := svc.SetProductCollections(ctx, p.ID, []int64{sale.ID}); err != nil {
		t.Fatalf("second set: %v", err)
	}

	got, err := app.Products().GetProduct(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetProduct: %v", err)
	}
	if len(got.Collections) != 1 || got.Collections[0].ID != sale.ID {
		t.Fatalf("collections = %+v, want only %d", got.Collections, sale.ID)
	}
	if got.Collections[0].Slug != "sale" || got.Collections[0].Title != "Sale" {
		t.Errorf("collections[0] = %+v, want the slug and title carried too", got.Collections[0])
	}

	// The emptied collections still exist; only the membership went.
	if _, err := svc.Get(ctx, summer.ID); err != nil {
		t.Errorf("Summer after being replaced away: %v", err)
	}

	// An empty list clears membership rather than being ignored.
	if err := svc.SetProductCollections(ctx, p.ID, nil); err != nil {
		t.Fatalf("clearing set: %v", err)
	}
	got, err = app.Products().GetProduct(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetProduct: %v", err)
	}
	if len(got.Collections) != 0 {
		t.Errorf("collections = %+v, want none", got.Collections)
	}
}

func TestSetProductCollectionsRejectsUnknownIDs(t *testing.T) {
	app := collectionsApp(t)
	ctx := context.Background()
	svc := app.Collections()

	p := simpleProduct(t, app, "SKU-UNKNOWN", 900, 1)
	real := newCollection(t, app, CollectionInput{Title: "Real"})

	err := svc.SetProductCollections(ctx, p.ID, []int64{real.ID, 999999})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown collection id = %v, want a not-found error", err)
	}
	// All or nothing: the good id must not have been stored either.
	got, err := app.Products().GetProduct(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetProduct: %v", err)
	}
	if len(got.Collections) != 0 {
		t.Errorf("collections = %+v, want the whole request rejected", got.Collections)
	}

	if err := svc.SetProductCollections(ctx, 999999, []int64{real.ID}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown product id = %v, want a not-found error", err)
	}
}

// Deleting a collection must not delete what was in it. The cascade runs from
// the collection to the membership rows and stops there.
func TestDeletingACollectionKeepsItsProducts(t *testing.T) {
	app := collectionsApp(t)
	ctx := context.Background()
	svc := app.Collections()

	p := simpleProduct(t, app, "SKU-SURVIVES", 2500, 4)
	c := newCollection(t, app, CollectionInput{Title: "Doomed"})
	if err := svc.SetProductCollections(ctx, p.ID, []int64{c.ID}); err != nil {
		t.Fatalf("set: %v", err)
	}

	if err := svc.Delete(ctx, c.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := app.Products().GetProduct(ctx, p.ID)
	if err != nil {
		t.Fatalf("the product did not survive its collection: %v", err)
	}
	if len(got.Collections) != 0 {
		t.Errorf("collections = %+v, want the membership gone with the collection", got.Collections)
	}
	if len(got.Variants) != 1 {
		t.Errorf("variants = %d, want the product intact", len(got.Variants))
	}
}

func TestProductsInCollectionAreCuratedOrder(t *testing.T) {
	app := collectionsApp(t)
	ctx := context.Background()
	svc := app.Collections()

	first := simpleProduct(t, app, "SKU-ORDER-1", 100, 1)
	second := simpleProduct(t, app, "SKU-ORDER-2", 200, 1)
	third := simpleProduct(t, app, "SKU-ORDER-3", 300, 1)
	c := newCollection(t, app, CollectionInput{Title: "Edit"})

	// Curated back to front, so a listing ordered by product id would disagree.
	for _, p := range []*Product{third, second, first} {
		if err := svc.SetProductCollections(ctx, p.ID, []int64{c.ID}); err != nil {
			t.Fatalf("set: %v", err)
		}
	}
	products, total, err := svc.ProductsInCollection(ctx, c.ID, ProductQuery{Status: ProductActive})
	if err != nil {
		t.Fatalf("ProductsInCollection: %v", err)
	}
	if total != 3 || len(products) != 3 {
		t.Fatalf("got %d of %d products, want 3 of 3", len(products), total)
	}
	// Arrival order, not product_id: each save appends to the tail of this
	// collection's curation, so the products added third, second and first come
	// back in that order. Before member_position existed they all held 0 here
	// and the tie-break was the id — an order nobody had chosen.
	if products[0].ID != third.ID || products[1].ID != second.ID || products[2].ID != first.ID {
		t.Errorf("order = %v, want the order they were added in", collectionIDs(products))
	}
}

// ----------------------------------------------------------------- tagging

func TestProductTagsAreNormalised(t *testing.T) {
	app := collectionsApp(t)
	ctx := context.Background()

	p := taggedProduct(t, app, "SKU-TAGS", ProductInput{
		Tags: []string{"summer", "  Linen  ", "SUMMER", "", "   ", "cotton", "Summer"},
	})
	want := []string{"cotton", "Linen", "summer"}
	assertTags(t, p.Tags, want)

	// Sorted, de-duplicated and case-preserving on the way back out of the
	// database too, not only in the value Create happened to return.
	reread, err := app.Products().GetProduct(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetProduct: %v", err)
	}
	assertTags(t, reread.Tags, want)

	patched, err := app.Products().UpdateProduct(ctx, p.ID, ProductPatch{
		Tags: &[]string{"Wool", "wool", " sale "},
	})
	if err != nil {
		t.Fatalf("UpdateProduct: %v", err)
	}
	assertTags(t, patched.Tags, []string{"sale", "Wool"})

	// An omitted tag list is not an empty one.
	untouched, err := app.Products().UpdateProduct(ctx, p.ID, ProductPatch{Title: ptr("Renamed")})
	if err != nil {
		t.Fatalf("UpdateProduct: %v", err)
	}
	assertTags(t, untouched.Tags, []string{"sale", "Wool"})

	// An explicitly empty list is.
	cleared, err := app.Products().UpdateProduct(ctx, p.ID, ProductPatch{Tags: &[]string{}})
	if err != nil {
		t.Fatalf("UpdateProduct: %v", err)
	}
	assertTags(t, cleared.Tags, []string{})
}

func TestProductOrganisationFieldsRoundTrip(t *testing.T) {
	app := collectionsApp(t)
	ctx := context.Background()

	p := taggedProduct(t, app, "SKU-ORG", ProductInput{
		ProductType: "  Shirt  ", Vendor: "Acme",
		SEOTitle: "Acme Shirt", SEODescription: "A shirt by Acme.",
	})
	if p.ProductType != "Shirt" || p.Vendor != "Acme" {
		t.Errorf("type/vendor = %q/%q, want Shirt/Acme trimmed", p.ProductType, p.Vendor)
	}
	if p.SEOTitle != "Acme Shirt" || p.SEODescription != "A shirt by Acme." {
		t.Errorf("seo = %q/%q, want it stored", p.SEOTitle, p.SEODescription)
	}

	patched, err := app.Products().UpdateProduct(ctx, p.ID, ProductPatch{
		Vendor: ptr("Globex"), SEOTitle: ptr(""),
	})
	if err != nil {
		t.Fatalf("UpdateProduct: %v", err)
	}
	if patched.Vendor != "Globex" {
		t.Errorf("vendor = %q, want Globex", patched.Vendor)
	}
	if patched.SEOTitle != "" {
		t.Errorf("seo_title = %q, want it cleared", patched.SEOTitle)
	}
	if patched.ProductType != "Shirt" {
		t.Errorf("product_type = %q, want it untouched by a patch that omitted it", patched.ProductType)
	}
}

// --------------------------------------------------------------- filtering

func TestAdminProductFilters(t *testing.T) {
	app := collectionsApp(t)

	taggedProduct(t, app, "SKU-F1", ProductInput{
		Vendor: "Acme", ProductType: "Shirt", Tags: []string{"summer", "linen"},
	})
	taggedProduct(t, app, "SKU-F2", ProductInput{
		Vendor: "Acme", ProductType: "Trousers", Tags: []string{"summer"},
	})
	taggedProduct(t, app, "SKU-F3", ProductInput{
		Vendor: "Globex", ProductType: "Shirt", Tags: []string{"winter"},
	})

	cases := []struct {
		query string
		want  int
	}{
		{"", 3},
		{"?vendor=Acme", 2},
		{"?vendor=Globex", 1},
		{"?vendor=Nobody", 0},
		{"?product_type=Shirt", 2},
		{"?tag=summer", 2},
		{"?tag=linen", 1},
		{"?tag=nothing", 0},
		{"?vendor=Acme&product_type=Shirt", 1},
		{"?vendor=Acme&tag=winter", 0},
	}
	for _, tc := range cases {
		rec := do(t, app, http.MethodGet, "/api/admin/products"+tc.query, withAdmin)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", tc.query, rec.Code, rec.Body)
		}
		var products []*Product
		decodeData(t, rec, &products)
		if len(products) != tc.want {
			t.Errorf("GET /api/admin/products%s returned %d products, want %d",
				tc.query, len(products), tc.want)
		}
	}

	// The pagination contract is unchanged by the new filters.
	rec := do(t, app, http.MethodGet, "/api/admin/products?vendor=Acme&limit=1&page=2", withAdmin)
	var page []*Product
	decodeData(t, rec, &page)
	if len(page) != 1 {
		t.Fatalf("page 2 of 1 returned %d products, want 1", len(page))
	}
}

// ------------------------------------------------------------------- HTTP

func TestCollectionAdminHTTP(t *testing.T) {
	app := collectionsApp(t)

	rec := do(t, app, http.MethodPost, "/api/admin/collections", withAdmin,
		jsonBody(t, map[string]any{"title": "Home Page", "position": 3}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	var created Collection
	decodeData(t, rec, &created)
	if created.Slug != "home-page" || created.Position != 3 {
		t.Errorf("created = %+v, want slug home-page at position 3", created)
	}

	id := strconv.FormatInt(created.ID, 10)
	rec = do(t, app, http.MethodGet, "/api/admin/collections/"+id, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET one = %d: %s", rec.Code, rec.Body)
	}

	rec = do(t, app, http.MethodPatch, "/api/admin/collections/"+id, withAdmin,
		jsonBody(t, map[string]any{"description": "The six things on the home page."}))
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", rec.Code, rec.Body)
	}
	var patched Collection
	decodeData(t, rec, &patched)
	if patched.Description != "The six things on the home page." {
		t.Errorf("description = %q, want it patched", patched.Description)
	}

	rec = do(t, app, http.MethodGet, "/api/admin/collections", withAdmin)
	var list []*Collection
	decodeData(t, rec, &list)
	if len(list) != 1 {
		t.Errorf("admin list returned %d collections, want 1", len(list))
	}

	rec = do(t, app, http.MethodDelete, "/api/admin/collections/"+id, withAdmin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body)
	}
	rec = do(t, app, http.MethodGet, "/api/admin/collections/"+id, withAdmin)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET after DELETE = %d, want 404", rec.Code)
	}

	// Every admin route is an admin route.
	for _, target := range []string{"/api/admin/collections", "/api/admin/collections/1"} {
		if rec := do(t, app, http.MethodGet, target); rec.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated GET %s = %d, want 401", target, rec.Code)
		}
	}
}

func TestSetProductCollectionsHTTP(t *testing.T) {
	app := collectionsApp(t)

	p := simpleProduct(t, app, "SKU-HTTP", 1200, 2)
	one := newCollection(t, app, CollectionInput{Title: "One"})
	two := newCollection(t, app, CollectionInput{Title: "Two"})
	target := "/api/admin/products/" + strconv.FormatInt(p.ID, 10) + "/collections"

	rec := do(t, app, http.MethodPut, target, withAdmin,
		jsonBody(t, map[string]any{"collection_ids": []int64{two.ID, one.ID}}))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body)
	}
	var got Product
	decodeData(t, rec, &got)
	if len(got.Collections) != 2 {
		t.Fatalf("collections = %+v, want 2", got.Collections)
	}
	// The response carries the order that was sent, which is the order stored.
	if got.Collections[0].ID != two.ID || got.Collections[1].ID != one.ID {
		t.Errorf("collections = %+v, want %d then %d", got.Collections, two.ID, one.ID)
	}

	rec = do(t, app, http.MethodPut, target, withAdmin,
		jsonBody(t, map[string]any{"collection_ids": []int64{9_999_999}}))
	if rec.Code != http.StatusNotFound {
		t.Errorf("PUT with an unknown id = %d, want 404", rec.Code)
	}
	if code := decodeError(t, rec).Code; code != "not_found" {
		t.Errorf("error code = %q, want not_found", code)
	}

	if rec := do(t, app, http.MethodPut, target); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated PUT = %d, want 401", rec.Code)
	}
}

// The public listing shows collections a shopper can open: ones holding at
// least one active product. An empty collection, or one holding only drafts,
// is a work in progress and linking to it produces an empty page.
func TestPublicCollectionListingIsActiveOnly(t *testing.T) {
	app := collectionsApp(t)
	ctx := context.Background()
	svc := app.Collections()

	live := newCollection(t, app, CollectionInput{Title: "Live"})
	drafts := newCollection(t, app, CollectionInput{Title: "Drafts"})
	newCollection(t, app, CollectionInput{Title: "Empty"})

	active := simpleProduct(t, app, "SKU-LIVE", 1000, 2)
	draft, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Not ready", SKU: "SKU-DRAFT", PriceMinor: ptr(int64(1000)),
	})
	if err != nil {
		t.Fatalf("create draft product: %v", err)
	}
	if err := svc.SetProductCollections(ctx, active.ID, []int64{live.ID}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := svc.SetProductCollections(ctx, draft.ID, []int64{drafts.ID}); err != nil {
		t.Fatalf("set: %v", err)
	}

	rec := do(t, app, http.MethodGet, "/api/collections")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/collections = %d: %s", rec.Code, rec.Body)
	}
	var list []*Collection
	decodeData(t, rec, &list)
	if len(list) != 1 || list[0].ID != live.ID {
		t.Fatalf("public listing = %+v, want only %q", list, "Live")
	}

	// The admin listing shows all three, which is the point of the split.
	rec = do(t, app, http.MethodGet, "/api/admin/collections", withAdmin)
	var all []*Collection
	decodeData(t, rec, &all)
	if len(all) != 3 {
		t.Errorf("admin listing returned %d collections, want 3", len(all))
	}
}

func TestPublicCollectionBySlugCarriesItsProducts(t *testing.T) {
	app := collectionsApp(t)
	ctx := context.Background()
	svc := app.Collections()

	c := newCollection(t, app, CollectionInput{Title: "Shop All"})
	active := simpleProduct(t, app, "SKU-PUB-1", 1000, 2)
	draft, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Hidden", SKU: "SKU-PUB-2", PriceMinor: ptr(int64(1000)),
	})
	if err != nil {
		t.Fatalf("create draft product: %v", err)
	}
	for _, p := range []*Product{active, draft} {
		if err := svc.SetProductCollections(ctx, p.ID, []int64{c.ID}); err != nil {
			t.Fatalf("set: %v", err)
		}
	}

	rec := do(t, app, http.MethodGet, "/api/collections/shop-all")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET by slug = %d: %s", rec.Code, rec.Body)
	}
	var page struct {
		Slug     string     `json:"slug"`
		Title    string     `json:"title"`
		Products []*Product `json:"products"`
	}
	decodeData(t, rec, &page)
	if page.Slug != "shop-all" || page.Title != "Shop All" {
		t.Errorf("page = %+v, want the collection flattened into it", page)
	}
	// The draft is a member, but a shopper cannot see it.
	if len(page.Products) != 1 || page.Products[0].ID != active.ID {
		t.Fatalf("products = %v, want only the active one", collectionIDs(page.Products))
	}

	if rec := do(t, app, http.MethodGet, "/api/collections/no-such-thing"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug = %d, want 404", rec.Code)
	}
}

// ------------------------------------------------------------------ helpers

func ptr[T any](v T) *T { return &v }

func assertTags(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("tags = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tags = %v, want %v", got, want)
		}
	}
}

// --------------------------------------------------------------- curation

// memberOrder reads a collection's membership straight out of the table, in
// whichever of the two orderings the caller names. It goes around the service
// deliberately: these tests are about which column carries which order.
func memberOrder(t *testing.T, app *App, collectionID int64, orderBy string) []int64 {
	t.Helper()
	rows, err := app.DB().QueryContext(context.Background(),
		`SELECT product_id FROM product_collections WHERE collection_id = $1 ORDER BY `+orderBy,
		collectionID)
	if err != nil {
		t.Fatalf("read the membership: %v", err)
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the membership: %v", err)
	}
	return out
}

func productIDs(products []*Product) []int64 {
	out := make([]int64, 0, len(products))
	for _, p := range products {
		out = append(out, p.ID)
	}
	return out
}

func sameIDs(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// The one thing in M27 that cannot be corrected after release: the backfill has
// to freeze the order stores can see today, which is `ORDER BY position,
// product_id` — the shipped expression, not product_id alone. A fixture whose
// members all belong to one collection passes either way, which is why this one
// puts them in different numbers of collections.
func TestMigrationFreezesTheVisibleCurationOrder(t *testing.T) {
	app := collectionsApp(t)
	ctx := context.Background()
	svc := app.Collections()

	edit := newCollection(t, app, CollectionInput{Title: "Edit"})
	other := newCollection(t, app, CollectionInput{Title: "Other"})
	third := newCollection(t, app, CollectionInput{Title: "Third"})

	// Ids ascend p1, p2, p3 while their `position` inside Edit descends 2, 1, 0,
	// so the shipped order is the reverse of the product_id order and the two
	// candidate backfills cannot both pass.
	p1 := simpleProduct(t, app, "SKU-FREEZE-1", 100, 1)
	p2 := simpleProduct(t, app, "SKU-FREEZE-2", 100, 1)
	p3 := simpleProduct(t, app, "SKU-FREEZE-3", 100, 1)
	for _, seed := range []struct {
		id  int64
		ids []int64
	}{
		{p1.ID, []int64{other.ID, third.ID, edit.ID}},
		{p2.ID, []int64{other.ID, edit.ID}},
		{p3.ID, []int64{edit.ID}},
	} {
		if err := svc.SetProductCollections(ctx, seed.id, seed.ids); err != nil {
			t.Fatalf("seed membership: %v", err)
		}
	}

	shipped := memberOrder(t, app, edit.ID, "position, product_id")
	if !sameIDs(shipped, []int64{p3.ID, p2.ID, p1.ID}) {
		t.Fatalf("the fixture is not what this test needs: shipped order = %v", shipped)
	}

	// Back to the pre-migration state — one column, every member tied at 0 —
	// and then the migration's own statement, not a copy of it.
	if _, err := app.DB().ExecContext(ctx, `UPDATE product_collections SET member_position = 0`); err != nil {
		t.Fatalf("reset member_position: %v", err)
	}
	if _, err := app.DB().ExecContext(ctx, curationBackfill); err != nil {
		t.Fatalf("run the backfill: %v", err)
	}

	if got := memberOrder(t, app, edit.ID, "member_position, product_id"); !sameIDs(got, shipped) {
		t.Errorf("after the backfill the order is %v, want the order stores can see today, %v", got, shipped)
	}
}

func TestSetCollectionProductsCurates(t *testing.T) {
	app := collectionsApp(t)
	ctx := context.Background()
	svc := app.Collections()

	first := simpleProduct(t, app, "SKU-CURATE-1", 100, 1)
	second := simpleProduct(t, app, "SKU-CURATE-2", 200, 1)
	third := simpleProduct(t, app, "SKU-CURATE-3", 300, 1)
	c := newCollection(t, app, CollectionInput{Title: "Window"})

	if err := svc.SetCollectionProducts(ctx, c.ID, []int64{third.ID, first.ID, second.ID}); err != nil {
		t.Fatalf("curate: %v", err)
	}
	members, total, err := svc.ProductsInCollection(ctx, c.ID, ProductQuery{})
	if err != nil {
		t.Fatalf("ProductsInCollection: %v", err)
	}
	if total != 3 || !sameIDs(productIDs(members), []int64{third.ID, first.ID, second.ID}) {
		t.Fatalf("order = %v, want the order that was sent", productIDs(members))
	}

	// A subset drops the memberships it leaves out, and nothing else.
	if err := svc.SetCollectionProducts(ctx, c.ID, []int64{first.ID}); err != nil {
		t.Fatalf("shrink: %v", err)
	}
	members, _, err = svc.ProductsInCollection(ctx, c.ID, ProductQuery{})
	if err != nil {
		t.Fatalf("ProductsInCollection: %v", err)
	}
	if !sameIDs(productIDs(members), []int64{first.ID}) {
		t.Errorf("members = %v, want only the one that survived", productIDs(members))
	}
	if _, err := app.Products().GetProduct(ctx, third.ID); err != nil {
		t.Errorf("the dropped product did not survive losing its membership: %v", err)
	}

	// An explicit empty list empties the collection and keeps it.
	if err := svc.SetCollectionProducts(ctx, c.ID, []int64{}); err != nil {
		t.Fatalf("empty: %v", err)
	}
	_, total, err = svc.ProductsInCollection(ctx, c.ID, ProductQuery{})
	if err != nil {
		t.Fatalf("ProductsInCollection: %v", err)
	}
	if total != 0 {
		t.Errorf("total = %d, want the collection emptied", total)
	}
	if _, err := svc.Get(ctx, c.ID); err != nil {
		t.Errorf("the collection did not survive being emptied: %v", err)
	}

	if err := svc.SetCollectionProducts(ctx, 999999, []int64{first.ID}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown collection = %v, want a not-found error", err)
	}
	if err := svc.SetCollectionProducts(ctx, c.ID, []int64{first.ID, 999999}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown product = %v, want a not-found error", err)
	}
	if _, total, _ = svc.ProductsInCollection(ctx, c.ID, ProductQuery{}); total != 0 {
		t.Errorf("the rejected request stored %d memberships, want none", total)
	}
}

// The D40 regression. One column cannot carry both orderings, so each writer
// must leave the other's alone — in either order.
func TestTheTwoCollectionOrderingsAreIndependent(t *testing.T) {
	app := collectionsApp(t)
	ctx := context.Background()
	svc := app.Collections()

	a := simpleProduct(t, app, "SKU-AXIS-A", 100, 1)
	b := simpleProduct(t, app, "SKU-AXIS-B", 100, 1)
	window := newCollection(t, app, CollectionInput{Title: "Window"})
	sale := newCollection(t, app, CollectionInput{Title: "Sale"})

	assertBoth := func(when string) {
		t.Helper()
		members, _, err := svc.ProductsInCollection(ctx, window.ID, ProductQuery{})
		if err != nil {
			t.Fatalf("%s: ProductsInCollection: %v", when, err)
		}
		if !sameIDs(productIDs(members), []int64{b.ID, a.ID}) {
			t.Errorf("%s: the collection's order is %v, want [b a]", when, productIDs(members))
		}
		got, err := app.Products().GetProduct(ctx, a.ID)
		if err != nil {
			t.Fatalf("%s: GetProduct: %v", when, err)
		}
		if len(got.Collections) != 2 ||
			got.Collections[0].ID != sale.ID || got.Collections[1].ID != window.ID {
			t.Errorf("%s: a's chips are %+v, want Sale then Window", when, got.Collections)
		}
	}

	if err := svc.SetProductCollections(ctx, b.ID, []int64{window.ID}); err != nil {
		t.Fatalf("seed b: %v", err)
	}
	if err := svc.SetCollectionProducts(ctx, window.ID, []int64{b.ID, a.ID}); err != nil {
		t.Fatalf("curate: %v", err)
	}
	if err := svc.SetProductCollections(ctx, a.ID, []int64{sale.ID, window.ID}); err != nil {
		t.Fatalf("re-save a's collections: %v", err)
	}
	assertBoth("product-side save after a curation")

	// And the other way round: curating again must not disturb the chips.
	if err := svc.SetCollectionProducts(ctx, window.ID, []int64{b.ID, a.ID}); err != nil {
		t.Fatalf("curate again: %v", err)
	}
	assertBoth("curation after a product-side save")
}

// The reconcile, not a delete and a re-insert: re-saving a product's collections
// unchanged must not move it inside any of them.
func TestSetProductCollectionsKeepsCuratedPlaces(t *testing.T) {
	app := collectionsApp(t)
	ctx := context.Background()
	svc := app.Collections()

	head := simpleProduct(t, app, "SKU-KEEP-HEAD", 100, 1)
	middle := simpleProduct(t, app, "SKU-KEEP-MIDDLE", 100, 1)
	tail := simpleProduct(t, app, "SKU-KEEP-TAIL", 100, 1)
	one := newCollection(t, app, CollectionInput{Title: "One"})
	two := newCollection(t, app, CollectionInput{Title: "Two"})

	for _, c := range []*Collection{one, two} {
		if err := svc.SetCollectionProducts(ctx, c.ID, []int64{head.ID, middle.ID, tail.ID}); err != nil {
			t.Fatalf("curate %s: %v", c.Title, err)
		}
	}
	if err := svc.SetProductCollections(ctx, middle.ID, []int64{one.ID, two.ID}); err != nil {
		t.Fatalf("re-save the middle product: %v", err)
	}
	for _, c := range []*Collection{one, two} {
		members, _, err := svc.ProductsInCollection(ctx, c.ID, ProductQuery{})
		if err != nil {
			t.Fatalf("ProductsInCollection: %v", err)
		}
		if !sameIDs(productIDs(members), []int64{head.ID, middle.ID, tail.ID}) {
			t.Errorf("%s = %v, want the curated order untouched", c.Title, productIDs(members))
		}
	}

	// A membership created from the product side lands at the tail of the
	// collection's curation rather than at its head.
	newcomer := simpleProduct(t, app, "SKU-KEEP-NEW", 100, 1)
	if err := svc.SetProductCollections(ctx, newcomer.ID, []int64{one.ID}); err != nil {
		t.Fatalf("add a newcomer: %v", err)
	}
	members, _, err := svc.ProductsInCollection(ctx, one.ID, ProductQuery{})
	if err != nil {
		t.Fatalf("ProductsInCollection: %v", err)
	}
	if !sameIDs(productIDs(members), []int64{head.ID, middle.ID, tail.ID, newcomer.ID}) {
		t.Errorf("one = %v, want the newcomer at the end", productIDs(members))
	}
}

func TestCollectionProductsHTTP(t *testing.T) {
	app := collectionsApp(t)
	ctx := context.Background()

	active := simpleProduct(t, app, "SKU-HTTP-ACTIVE", 100, 1)
	price := int64(100)
	draft, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Staged", SKU: "SKU-HTTP-DRAFT", PriceMinor: &price,
	})
	if err != nil {
		t.Fatalf("create the draft: %v", err)
	}
	c := newCollection(t, app, CollectionInput{Title: "Window"})
	target := "/api/admin/collections/" + strconv.FormatInt(c.ID, 10) + "/products"

	rec := do(t, app, http.MethodPut, target, withAdmin,
		jsonBody(t, map[string]any{"product_ids": []int64{draft.ID, active.ID}}))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("PUT answered with a body: %s", rec.Body)
	}

	rec = do(t, app, http.MethodGet, target, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body)
	}
	var members []*Product
	decodeData(t, rec, &members)
	// Drafts included: staging them is what an operator is doing here.
	if !sameIDs(productIDs(members), []int64{draft.ID, active.ID}) {
		t.Errorf("members = %v, want the draft first, as curated", productIDs(members))
	}

	// A collection nobody has is a 404 rather than an empty page, which is what
	// a curation screen has to know before it offers to reorder anything.
	if rec := do(t, app, http.MethodGet, "/api/admin/collections/999999/products", withAdmin); rec.Code != http.StatusNotFound {
		t.Errorf("GET an unknown collection = %d, want 404", rec.Code)
	}
	rec = do(t, app, http.MethodGet, target+"?collection_id=1", withAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("GET with ?collection_id= = %d, want 400", rec.Code)
	}
	if code := decodeError(t, rec).Code; code != "validation_failed" {
		t.Errorf("error code = %q, want validation_failed", code)
	}

	rec = do(t, app, http.MethodPut, target, withAdmin,
		jsonBody(t, map[string]any{"product_ids": []int64{active.ID, 999999}}))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("PUT with an unknown product = %d, want 404", rec.Code)
	}
	if code := decodeError(t, rec).Code; code != "not_found" {
		t.Errorf("error code = %q, want not_found", code)
	}
	members, _, err = app.Collections().ProductsInCollection(ctx, c.ID, ProductQuery{})
	if err != nil {
		t.Fatalf("ProductsInCollection: %v", err)
	}
	if !sameIDs(productIDs(members), []int64{draft.ID, active.ID}) {
		t.Errorf("the rejected PUT changed the membership to %v", productIDs(members))
	}

	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if rec := do(t, app, method, target); rec.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated %s = %d, want 401", method, rec.Code)
		}
	}
}

// The pointer field, which is the difference between a deliberate clear and a
// client bug wiping a thousand memberships.
func TestSetCollectionProductsRequiresTheList(t *testing.T) {
	app := collectionsApp(t)
	ctx := context.Background()

	p := simpleProduct(t, app, "SKU-REQUIRED", 100, 1)
	c := newCollection(t, app, CollectionInput{Title: "Window"})
	if err := app.Collections().SetCollectionProducts(ctx, c.ID, []int64{p.ID}); err != nil {
		t.Fatalf("curate: %v", err)
	}
	target := "/api/admin/collections/" + strconv.FormatInt(c.ID, 10) + "/products"

	rec := doBody(t, app, http.MethodPut, target, `{}`, withAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT {} = %d, want 400", rec.Code)
	}
	if err := decodeError(t, rec); err.Code != "validation_failed" ||
		!strings.Contains(err.Message, "product_ids") {
		t.Errorf("error = %+v, want one naming product_ids", err)
	}
	if _, total, _ := app.Collections().ProductsInCollection(ctx, c.ID, ProductQuery{}); total != 1 {
		t.Errorf("a body of {} changed the membership to %d rows", total)
	}

	rec = doBody(t, app, http.MethodPut, target, `{"product_ids":[]}`, withAdmin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT [] = %d: %s", rec.Code, rec.Body)
	}
	if _, total, _ := app.Collections().ProductsInCollection(ctx, c.ID, ProductQuery{}); total != 0 {
		t.Errorf("an explicit empty array left %d rows, want the collection emptied", total)
	}
}

func TestAdminProductsFilterByCollection(t *testing.T) {
	app := collectionsApp(t)
	ctx := context.Background()

	member := simpleProduct(t, app, "SKU-FILTER-MEMBER", 100, 1)
	price := int64(100)
	draft, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Staged", SKU: "SKU-FILTER-DRAFT", PriceMinor: &price,
	})
	if err != nil {
		t.Fatalf("create the draft: %v", err)
	}
	simpleProduct(t, app, "SKU-FILTER-OUTSIDER", 100, 1)
	c := newCollection(t, app, CollectionInput{Title: "Window"})
	if err := app.Collections().SetCollectionProducts(ctx, c.ID, []int64{draft.ID, member.ID}); err != nil {
		t.Fatalf("curate: %v", err)
	}

	rec := do(t, app, http.MethodGet,
		"/api/admin/products?collection_id="+strconv.FormatInt(c.ID, 10), withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body)
	}
	var got []*Product
	decodeData(t, rec, &got)
	if !sameIDs(productIDs(got), []int64{draft.ID, member.ID}) {
		t.Errorf("filtered listing = %v, want the members in curated order", productIDs(got))
	}

	if rec := do(t, app, http.MethodGet, "/api/admin/products?collection_id=abc", withAdmin); rec.Code != http.StatusBadRequest {
		t.Errorf("?collection_id=abc = %d, want 400", rec.Code)
	}
	// A collection nobody has is an empty page rather than a 404: the listing
	// is a filter, and the sub-route is the one that knows about existence.
	rec = do(t, app, http.MethodGet, "/api/admin/products?collection_id=999999", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("?collection_id=999999 = %d, want 200", rec.Code)
	}
	var none []*Product
	decodeData(t, rec, &none)
	if len(none) != 0 {
		t.Errorf("an unknown collection returned %d products, want none", len(none))
	}
}
