package translations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

func putKind(t *testing.T, app *gocommerce.App, kind string, id int64, lang string, fields map[string]string) {
	t.Helper()
	rec := gctest.AdminRequest(t, app, http.MethodPut,
		"/api/admin/x/translations/"+kind+"/"+itoa(id)+"/"+lang, map[string]any{"fields": fields})
	if rec.Code != http.StatusOK {
		t.Fatalf("put %s %d in %s = %d: %s", kind, id, lang, rec.Code, rec.Body)
	}
}

func getIn(t *testing.T, app *gocommerce.App, target, lang string, v any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Accept-Language", lang)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s in %s = %d: %s", target, lang, rec.Code, rec.Body)
	}
	gctest.DecodeData(t, rec, v)
}

// A shopper who asks in French browses French: the category a product is
// filed under, its whole path, and the collections it is in — on their own
// routes and riding along on the product — while the panel keeps the store's
// own text.
func TestCategoryAndCollectionNamesReachTheStorefront(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()
	jewellery, err := app.Categories().Create(ctx, gocommerce.CategoryInput{Slug: "jewellery", Title: "Jewellery"})
	if err != nil {
		t.Fatal(err)
	}
	rings, err := app.Categories().Create(ctx, gocommerce.CategoryInput{Slug: "rings", Title: "Rings", ParentID: &jewellery.ID})
	if err != nil {
		t.Fatal(err)
	}
	festive, err := app.Collections().Create(ctx, gocommerce.CollectionInput{Slug: "festive", Title: "Festive", Description: "For the season"})
	if err != nil {
		t.Fatal(err)
	}
	product := gctest.CreateProduct(t, app, "TR-NAMES", 2500, 5)
	if rec := gctest.AdminRequest(t, app, http.MethodPatch, "/api/admin/products/"+itoa(product.ID),
		map[string]any{"category_id": rings.ID}); rec.Code != http.StatusOK {
		t.Fatalf("file the product = %d: %s", rec.Code, rec.Body)
	}
	if err := app.Collections().SetCollectionProducts(ctx, festive.ID, []int64{product.ID}); err != nil {
		t.Fatal(err)
	}

	putKind(t, app, gocommerce.KindCategory, jewellery.ID, "fr", map[string]string{"title": "Bijoux"})
	putKind(t, app, gocommerce.KindCategory, rings.ID, "fr", map[string]string{"title": "Bagues"})
	putKind(t, app, gocommerce.KindCollection, festive.ID, "fr", map[string]string{"title": "Fêtes", "description": "Pour la saison"})

	var flat []gocommerce.Category
	getIn(t, app, "/api/categories?flat=1", "fr", &flat)
	names := map[int64]string{}
	for _, c := range flat {
		names[c.ID] = c.Title + " | " + c.FullName
	}
	if names[rings.ID] != "Bagues | Bijoux / Bagues" || names[jewellery.ID] != "Bijoux | Bijoux" {
		t.Errorf("categories in French = %v", names)
	}
	var tree []gocommerce.Category
	getIn(t, app, "/api/categories", "fr", &tree)
	if len(tree) != 1 || tree[0].Title != "Bijoux" || len(tree[0].Children) != 1 || tree[0].Children[0].Title != "Bagues" {
		t.Errorf("tree in French = %+v", tree)
	}
	var page struct {
		Title    string `json:"title"`
		FullName string `json:"full_name"`
	}
	getIn(t, app, "/api/categories/rings", "fr", &page)
	if page.Title != "Bagues" || page.FullName != "Bijoux / Bagues" {
		t.Errorf("category page in French = %+v", page)
	}

	var collections []gocommerce.Collection
	getIn(t, app, "/api/collections", "fr", &collections)
	if len(collections) != 1 || collections[0].Title != "Fêtes" || collections[0].Description != "Pour la saison" {
		t.Errorf("collections in French = %+v", collections)
	}

	got := readPublic(t, app, product.ID, "fr")
	if got.Category == nil || got.Category.Title != "Bagues" || got.Category.FullName != "Bijoux / Bagues" {
		t.Errorf("the product's category in French = %+v", got.Category)
	}
	if len(got.Collections) != 1 || got.Collections[0].Title != "Fêtes" {
		t.Errorf("the product's collections in French = %+v", got.Collections)
	}

	// German has nothing written: the store's own names, not blanks.
	got = readPublic(t, app, product.ID, "de")
	if got.Category == nil || got.Category.FullName != "Jewellery / Rings" {
		t.Errorf("the product's category in German = %+v", got.Category)
	}

	// The panel edits the store's own text, whatever the operator's browser asks.
	req := httptest.NewRequest(http.MethodGet, "/api/admin/categories?flat=1", nil)
	req.Header.Set("Authorization", "Bearer "+gctest.AdminToken)
	req.Header.Set("Accept-Language", "fr")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	var admin []gocommerce.Category
	gctest.DecodeData(t, rec, &admin)
	for _, c := range admin {
		if c.ID == rings.ID && c.Title != "Rings" {
			t.Errorf("the admin list = %q, want the store's own Rings", c.Title)
		}
	}
}

// The screen's list: every item with its translation beside it, filtered to
// what is left to do, and the count an owner reads first.
func TestTheTranslationListShowsWhatIsLeft(t *testing.T) {
	app := newApp(t)
	shirt := gctest.CreateProduct(t, app, "TR-L1", 2500, 5)
	gctest.CreateProduct(t, app, "TR-L2", 2500, 5)
	gctest.CreateProduct(t, app, "TR-L3", 2500, 5)
	put(t, app, shirt.ID, "fr", map[string]string{"title": "Chemise"})

	list := func(query string) Items {
		t.Helper()
		rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/translations/product?"+query, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("list %s = %d: %s", query, rec.Code, rec.Body)
		}
		var out Items
		gctest.DecodeData(t, rec, &out)
		return out
	}

	all := list("language=fr")
	if all.Total != 3 || all.Translated != 1 || all.Matching != 3 || len(all.Items) != 3 {
		t.Fatalf("all = total %d translated %d matching %d items %d", all.Total, all.Translated, all.Matching, len(all.Items))
	}
	for _, it := range all.Items {
		if it.Original["title"] == "" {
			t.Errorf("item %d has no original title: %v", it.ID, it.Original)
		}
		if (it.ID == shirt.ID) != (it.Translation != nil) {
			t.Errorf("item %d translation = %v", it.ID, it.Translation)
		}
	}
	if it := list("language=fr&status=translated"); it.Matching != 1 || it.Items[0].Translation["title"] != "Chemise" {
		t.Errorf("translated = %+v", it)
	}
	if left := list("language=fr&status=missing&limit=1"); left.Matching != 2 || len(left.Items) != 1 || left.Total != 3 {
		t.Errorf("missing, one a page = matching %d, items %d, total %d", left.Matching, len(left.Items), left.Total)
	}
	if none := list("language=fr&q=" + "nothing-matches-this"); none.Matching != 0 || len(none.Items) != 0 {
		t.Errorf("a search for nothing = %+v", none)
	}

	for _, bad := range []string{"language=en", "language=es", "language=fr&status=done", "language="} {
		if rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/translations/product?"+bad, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("list %s = %d, want 400", bad, rec.Code)
		}
	}
}

// A translation is into one of the store's languages other than its main one,
// of something that exists.
func TestATranslationIsIntoAnotherOfTheStoresLanguages(t *testing.T) {
	app := newApp(t)
	product := gctest.CreateProduct(t, app, "TR-LANG", 2500, 5)
	for lang, want := range map[string]int{"en": http.StatusBadRequest, "es": http.StatusBadRequest, "FR": http.StatusOK} {
		if rec := put(t, app, product.ID, lang, map[string]string{"title": "x"}); rec.Code != want {
			t.Errorf("put in %s = %d, want %d: %s", lang, rec.Code, want, rec.Body)
		}
	}
	if rec := put(t, app, product.ID+1000, "fr", map[string]string{"title": "x"}); rec.Code != http.StatusNotFound {
		t.Errorf("put for a product that is not there = %d, want 404", rec.Code)
	}
	var entries []Entry
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/translations/product/"+itoa(product.ID), nil), &entries)
	if len(entries) != 1 || entries[0].Language != "fr" {
		t.Errorf("stored = %+v, want one row spelt the store's way, fr", entries)
	}
}

// A page past the end still knows how many there are, so the screen can say
// so and page back; and an item counts as translated once its title is.
func TestTheListCountsPastTheEndAndByTitle(t *testing.T) {
	app := newApp(t)
	first := gctest.CreateProduct(t, app, "TR-P1", 2500, 5)
	gctest.CreateProduct(t, app, "TR-P2", 2500, 5)
	gctest.CreateProduct(t, app, "TR-P3", 2500, 5)
	put(t, app, first.ID, "fr", map[string]string{"description": "Seulement la description"})

	list := func(query string) Items {
		t.Helper()
		rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/translations/product?"+query, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("list %s = %d: %s", query, rec.Code, rec.Body)
		}
		var out Items
		gctest.DecodeData(t, rec, &out)
		return out
	}
	if past := list("language=fr&offset=10"); past.Matching != 3 || len(past.Items) != 0 {
		t.Errorf("past the end = matching %d, items %d; want 3, 0", past.Matching, len(past.Items))
	}
	all := list("language=fr")
	if all.Translated != 0 {
		t.Errorf("translated = %d, want 0: a description alone is not a translated product", all.Translated)
	}
	if missing := list("language=fr&status=missing"); missing.Matching != 3 {
		t.Errorf("not translated = %d, want 3 including the one with only a description", missing.Matching)
	}
}
