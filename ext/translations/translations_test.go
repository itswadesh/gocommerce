package translations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// newApp boots a store that serves English, French and Canadian French.
//
// The languages matter: core/i18n.go negotiates a request down to one of
// Config.Languages and nothing else, so a store that does not declare French
// never asks this module for French — which is the behaviour, not a limitation
// of the test.
func newApp(t *testing.T) *gocommerce.App {
	t.Helper()
	return gctest.NewWithConfig(t, gocommerce.Config{
		DefaultLanguage: "en",
		Languages:       []string{"en", "fr", "fr-CA", "de"},
	}, New())
}

func put(t *testing.T, app *gocommerce.App, id int64, lang string, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return gctest.AdminRequest(t, app, http.MethodPut,
		"/api/admin/x/translations/product/"+itoa(id)+"/"+lang,
		map[string]any{"fields": fields})
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// readPublic fetches a product from the storefront route in one language.
func readPublic(t *testing.T, app *gocommerce.App, id int64, acceptLanguage string) gocommerce.Product {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/products/"+itoa(id), nil)
	if acceptLanguage != "" {
		req.Header.Set("Accept-Language", acceptLanguage)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET the product in %q = %d: %s", acceptLanguage, rec.Code, rec.Body)
	}
	var body struct {
		Data gocommerce.Product `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Data
}

// The module's reason to exist: a shopper who asks in French reads French.
func TestTranslatedProductReachesTheStorefront(t *testing.T) {
	app := newApp(t)
	product := gctest.CreateProduct(t, app, "TR-1", 2500, 5)

	if rec := put(t, app, product.ID, "fr", map[string]string{
		"title":       "Chemise en lin",
		"description": "Une chemise en lin, coupée à Porto.",
	}); rec.Code != http.StatusOK {
		t.Fatalf("put the French translation = %d: %s", rec.Code, rec.Body)
	}

	fr := readPublic(t, app, product.ID, "fr")
	if fr.Title != "Chemise en lin" {
		t.Errorf("French title = %q, want the translation", fr.Title)
	}
	if fr.Description != "Une chemise en lin, coupée à Porto." {
		t.Errorf("French description = %q, want the translation", fr.Description)
	}

	// The store's own language is untouched, and costs no lookup at all.
	en := readPublic(t, app, product.ID, "en")
	if en.Title != product.Title {
		t.Errorf("English title = %q, want the stored %q", en.Title, product.Title)
	}

	// A language the store serves but nobody has translated into falls back
	// rather than coming back blank.
	de := readPublic(t, app, product.ID, "de")
	if de.Title != product.Title {
		t.Errorf("German title = %q, want the English fallback %q", de.Title, product.Title)
	}
}

// The SEO pair reaches the storefront too, which is what makes a translated
// page worth serving: a French listing under an English meta description is
// half a translation, and it is the half a search engine reads.
func TestTranslatedSEOReachesTheStorefront(t *testing.T) {
	app := newApp(t)
	product := gctest.CreateProduct(t, app, "TR-SEO", 2500, 5)

	if rec := put(t, app, product.ID, "fr", map[string]string{
		"seo_title":       "Chemise en lin — la boutique",
		"seo_description": "Chemises en lin coupées à Porto, expédiées sous 48 h.",
	}); rec.Code != http.StatusOK {
		t.Fatalf("put = %d: %s", rec.Code, rec.Body)
	}

	fr := readPublic(t, app, product.ID, "fr")
	if fr.SEOTitle != "Chemise en lin — la boutique" {
		t.Errorf("seo_title = %q, want the translation", fr.SEOTitle)
	}
	if fr.SEODescription != "Chemises en lin coupées à Porto, expédiées sous 48 h." {
		t.Errorf("seo_description = %q, want the translation", fr.SEODescription)
	}
	// And the untranslated title still reads in the store's own language.
	if fr.Title != product.Title {
		t.Errorf("title = %q, want the untranslated %q", fr.Title, product.Title)
	}
}

// A field left untranslated keeps its stored text. Half a translation must not
// blank the other half, which is what a naive whole-object overwrite would do.
func TestUntranslatedFieldsKeepTheirText(t *testing.T) {
	app := newApp(t)
	product := gctest.CreateProduct(t, app, "TR-2", 2500, 5)

	if rec := put(t, app, product.ID, "fr", map[string]string{
		"title": "Chemise en lin",
	}); rec.Code != http.StatusOK {
		t.Fatalf("put = %d: %s", rec.Code, rec.Body)
	}

	fr := readPublic(t, app, product.ID, "fr")
	if fr.Title != "Chemise en lin" {
		t.Errorf("title = %q, want the translation", fr.Title)
	}
	if fr.Description != product.Description {
		t.Errorf("description = %q, want the untranslated %q", fr.Description, product.Description)
	}
}

// A regional tag falls back to its primary subtag, and an exact row beats it.
func TestRegionalTagFallsBackToTheLanguage(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()
	product := gctest.CreateProduct(t, app, "TR-3", 2500, 5)

	if rec := put(t, app, product.ID, "fr", map[string]string{"title": "Chemise"}); rec.Code != http.StatusOK {
		t.Fatalf("put fr = %d: %s", rec.Code, rec.Body)
	}

	mod := New()
	mod.db = app.DB()
	mod.log = app.Log()

	// Only "fr" exists, so "fr-CA" takes it.
	got, err := mod.Translate(ctx, "fr-CA", gocommerce.KindProduct, []int64{product.ID})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if got[product.ID]["title"] != "Chemise" {
		t.Errorf("fr-CA title = %q, want the fr row", got[product.ID]["title"])
	}

	// Once a Canadian row exists it wins, whichever order the rows come back in.
	if rec := put(t, app, product.ID, "fr-CA", map[string]string{"title": "Chandail"}); rec.Code != http.StatusOK {
		t.Fatalf("put fr-CA = %d: %s", rec.Code, rec.Body)
	}
	got, err = mod.Translate(ctx, "fr-CA", gocommerce.KindProduct, []int64{product.ID})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if got[product.ID]["title"] != "Chandail" {
		t.Errorf("fr-CA title = %q, want the exact fr-CA row to win", got[product.ID]["title"])
	}

	// And plain "fr" is unaffected by the Canadian one.
	got, err = mod.Translate(ctx, "fr", gocommerce.KindProduct, []int64{product.ID})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if got[product.ID]["title"] != "Chemise" {
		t.Errorf("fr title = %q, want the fr row", got[product.ID]["title"])
	}
}

// One query for a page, which is the reason the port is batched.
func TestTranslateIsBatched(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()
	a := gctest.CreateProduct(t, app, "TR-BATCH-A", 1000, 5)
	b := gctest.CreateProduct(t, app, "TR-BATCH-B", 1000, 5)
	c := gctest.CreateProduct(t, app, "TR-BATCH-C", 1000, 5)

	for _, p := range []*gocommerce.Product{a, b} {
		if rec := put(t, app, p.ID, "fr", map[string]string{"title": "Titre " + p.Title}); rec.Code != http.StatusOK {
			t.Fatalf("put = %d: %s", rec.Code, rec.Body)
		}
	}

	mod := New()
	mod.db = app.DB()
	mod.log = app.Log()

	got, err := mod.Translate(ctx, "fr", gocommerce.KindProduct, []int64{a.ID, b.ID, c.ID})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d translated product(s), want 2 — the untranslated one must be absent, not blank", len(got))
	}
	if _, ok := got[c.ID]; ok {
		t.Error("the untranslated product came back with an entry")
	}
}

// The admin surface: list, replace, empty-means-delete, delete.
func TestAdminRoundTrip(t *testing.T) {
	app := newApp(t)
	product := gctest.CreateProduct(t, app, "TR-4", 2500, 5)
	path := "/api/admin/x/translations/product/" + itoa(product.ID)

	// Nothing yet, and an empty list rather than null.
	rec := gctest.AdminRequest(t, app, http.MethodGet, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body)
	}
	var list []Entry
	gctest.DecodeData(t, rec, &list)
	if len(list) != 0 {
		t.Errorf("a fresh product has %d translation(s), want 0", len(list))
	}

	if rec := put(t, app, product.ID, "fr", map[string]string{
		"title":     "Chemise",
		"seo_title": "Chemise en lin — la boutique",
	}); rec.Code != http.StatusOK {
		t.Fatalf("put = %d: %s", rec.Code, rec.Body)
	}
	if rec := put(t, app, product.ID, "de", map[string]string{"title": "Hemd"}); rec.Code != http.StatusOK {
		t.Fatalf("put de = %d: %s", rec.Code, rec.Body)
	}

	both := listOf(t, app, path)
	if len(both) != 2 {
		t.Fatalf("got %d translation(s), want 2", len(both))
	}
	// Ordered by language, so a picker renders the same way twice.
	if both[0].Language != "de" || both[1].Language != "fr" {
		t.Errorf("languages = %q, %q; want them ordered", both[0].Language, both[1].Language)
	}

	// A PUT replaces rather than merges: seo_title was not sent, so it goes.
	if rec := put(t, app, product.ID, "fr", map[string]string{"title": "Chemise"}); rec.Code != http.StatusOK {
		t.Fatalf("replace = %d: %s", rec.Code, rec.Body)
	}
	for _, e := range listOf(t, app, path) {
		if e.Language == "fr" {
			if _, still := e.Fields["seo_title"]; still {
				t.Error("a replacing PUT kept a field it did not carry")
			}
		}
	}

	// Emptying every field is the same intention as deleting the row.
	if rec := put(t, app, product.ID, "de", map[string]string{"title": "  "}); rec.Code != http.StatusNoContent {
		t.Fatalf("emptying = %d, want 204: %s", rec.Code, rec.Body)
	}
	if left := listOf(t, app, path); len(left) != 1 {
		t.Errorf("after emptying German there are %d translation(s), want 1", len(left))
	}

	// Delete, and deleting again is still fine.
	for range 2 {
		if rec := gctest.AdminRequest(t, app, http.MethodDelete, path+"/fr", nil); rec.Code != http.StatusNoContent {
			t.Fatalf("delete = %d, want 204: %s", rec.Code, rec.Body)
		}
	}
	if left := listOf(t, app, path); len(left) != 0 {
		t.Errorf("after deleting there are %d translation(s), want 0", len(left))
	}
}

// listOf reads the translations of one entity into a *fresh* slice.
//
// Decoding into a reused one is a trap rather than a style preference:
// json.Unmarshal keeps the entries of a map it finds already allocated, so a
// second decode merges the first result's fields into the second's — which made
// a correct replacing PUT look as though it had kept a field it dropped.
func listOf(t *testing.T, app *gocommerce.App, path string) []Entry {
	t.Helper()
	var out []Entry
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet, path, nil), &out)
	return out
}

// A typo in a field name is refused rather than stored, because a dropped field
// is a translation somebody believes they wrote.
func TestUnknownFieldsAreRefused(t *testing.T) {
	app := newApp(t)
	product := gctest.CreateProduct(t, app, "TR-5", 2500, 5)

	rec := put(t, app, product.ID, "fr", map[string]string{"titel": "Chemise"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown field = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestBadPathsAreRefused(t *testing.T) {
	app := newApp(t)
	product := gctest.CreateProduct(t, app, "TR-6", 2500, 5)

	// The engine applies translations to products only, so storing a variant
	// row would look like a translation that does not work.
	if rec := gctest.AdminRequest(t, app, http.MethodPut,
		"/api/admin/x/translations/variant/1/fr",
		map[string]any{"fields": map[string]string{"title": "x"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("kind=variant = %d, want 400", rec.Code)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodPut,
		"/api/admin/x/translations/product/"+itoa(product.ID)+"/not-a-language!",
		map[string]any{"fields": map[string]string{"title": "x"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("a junk language = %d, want 400", rec.Code)
	}
}

// Every admin route this module mounts names the rights it needs, and the
// served contract describes them — the two checks `doctor` makes of core.
func TestModuleContract(t *testing.T) {
	app := newApp(t)
	gctest.AssertAdminRoutesDeclareRights(t, app, "translations")
	gctest.AssertSpecCoversModuleRoutes(t, app, "translations")
}
