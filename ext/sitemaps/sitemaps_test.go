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
