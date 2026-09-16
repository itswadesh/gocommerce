package sitemaps

import (
	"context"
	"net/http"
	"strings"
	"testing"

	gocommerce "github.com/misiki/gocommerce/core"
	"github.com/misiki/gocommerce/gctest"
)

func TestTheSitemapListsWhatIsLive(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ctx := context.Background()
	live := gctest.CreateProduct(t, app, "SM-LIVE", 1000, 1)
	price := int64(500)
	if _, err := app.Products().CreateProduct(ctx, gocommerce.ProductInput{Title: "Hidden", SKU: "SM-DRAFT", PriceMinor: &price, Status: "draft"}); err != nil {
		t.Fatal(err)
	}
	collection, err := app.Collections().Create(ctx, gocommerce.CollectionInput{Title: "Summer"})
	if err != nil {
		t.Fatalf("collection: %v", err)
	}
	// A collection is live when it holds a live product.
	if err := app.Collections().SetCollectionProducts(ctx, collection.ID, []int64{live.ID}); err != nil {
		t.Fatalf("fill collection: %v", err)
	}

	if rec := gctest.Request(t, app, http.MethodGet, "/x/sitemaps/sitemap.xml", nil); rec.Code != http.StatusNotFound {
		t.Errorf("sitemap while off = %d", rec.Code)
	}
	on := true
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{
		"storefront_url": "https://shop.example/", "product_path": "/p/{slug}",
	}}); err != nil {
		t.Fatal(err)
	}

	index := gctest.Request(t, app, http.MethodGet, "/x/sitemaps/sitemap.xml", nil)
	if index.Code != http.StatusOK || !strings.Contains(index.Body.String(), "/x/sitemaps/products.xml</loc>") ||
		!strings.Contains(index.Body.String(), "/x/sitemaps/collections.xml</loc>") || strings.Contains(index.Body.String(), "pages.xml") {
		t.Errorf("index = %d:\n%s", index.Code, index.Body)
	}
	products := gctest.Request(t, app, http.MethodGet, "/x/sitemaps/products.xml", nil)
	body := products.Body.String()
	if products.Code != http.StatusOK || !strings.Contains(body, "<loc>https://shop.example/p/"+live.Slug+"</loc>") || !strings.Contains(body, "<lastmod>") {
		t.Errorf("products = %d:\n%s", products.Code, body)
	}
	if strings.Contains(body, "hidden") {
		t.Errorf("the draft is in the sitemap")
	}
	collections := gctest.Request(t, app, http.MethodGet, "/x/sitemaps/collections.xml", nil)
	if collections.Code != http.StatusOK || !strings.Contains(collections.Body.String(), "https://shop.example/collections/"+collection.Slug) {
		t.Errorf("collections = %d:\n%s", collections.Code, collections.Body)
	}
	if pages := gctest.Request(t, app, http.MethodGet, "/x/sitemaps/pages.xml", nil); pages.Code != http.StatusNotFound {
		t.Errorf("pages without the cms module = %d, want 404", pages.Code)
	}
}

func TestSitemapRoutesAreDocumented(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	gctest.AssertSpecCoversModuleRoutes(t, app, "sitemaps")
}

// A sitemap opened in a browser should be readable, which it is not as raw
// XML. Each response carries a stylesheet instruction and the sheet is served
// beside it; a crawler ignores the instruction, so the XML underneath is
// unchanged.
func TestEverySitemapPointsAtAStylesheetThatIsServed(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ctx := context.Background()
	gctest.CreateProduct(t, app, "SM-XSL", 1000, 1)

	on := true
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{
		"storefront_url": "https://shop.example/",
	}}); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/x/sitemaps/sitemap.xml",
		"/x/sitemaps/products.xml",
		"/x/sitemaps/collections.xml",
	} {
		rec := gctest.Request(t, app, http.MethodGet, path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", path, rec.Code, rec.Body)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `<?xml-stylesheet type="text/xsl" href="`+stylesheetPath+`"?>`) {
			t.Errorf("%s carries no stylesheet instruction:\n%s", path, body[:min(300, len(body))])
		}
		// The instruction belongs between the declaration and the root
		// element, which is the only place it is legal.
		decl := strings.Index(body, "<?xml ")
		pi := strings.Index(body, "<?xml-stylesheet")
		root := strings.Index(body, "<sitemapindex")
		if root < 0 {
			root = strings.Index(body, "<urlset")
		}
		if !(decl >= 0 && decl < pi && pi < root) {
			t.Errorf("%s: declaration at %d, instruction at %d, root at %d — wrong order", path, decl, pi, root)
		}
		// And the XML itself is untouched, which is what a crawler reads.
		if !strings.Contains(body, "http://www.sitemaps.org/schemas/sitemap/0.9") {
			t.Errorf("%s lost its namespace", path)
		}
	}

	// The sheet is served, and as XSL rather than as a download.
	sheet := gctest.Request(t, app, http.MethodGet, stylesheetPath, nil)
	if sheet.Code != http.StatusOK {
		t.Fatalf("stylesheet = %d", sheet.Code)
	}
	if ct := sheet.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/xsl") {
		t.Errorf("stylesheet content type = %q", ct)
	}
	body := sheet.Body.String()
	// It handles both shapes: a reader following the index into products.xml
	// should not land on a differently-styled page.
	for _, want := range []string{"s:sitemapindex", "s:urlset", "xsl:stylesheet"} {
		if !strings.Contains(body, want) {
			t.Errorf("the stylesheet has no %s", want)
		}
	}
}

// The sheet answers even while the plugin is off. It describes nothing about
// the store, and a 404 here would leave a browser showing raw XML for the
// second between switching the plugin on and the first fetch.
func TestTheStylesheetIsServedWhetherOrNotTheSitemapIs(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	if rec := gctest.Request(t, app, http.MethodGet, "/x/sitemaps/sitemap.xml", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("the sitemap should be off: %d", rec.Code)
	}
	if rec := gctest.Request(t, app, http.MethodGet, stylesheetPath, nil); rec.Code != http.StatusOK {
		t.Errorf("stylesheet while the sitemap is off = %d, want 200", rec.Code)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
