package alltest

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// What the shop paid is the operator's number, and no route a shopper can
// call — core's or any module's — answers with it. The walk covers every
// public GET route this build mounts, so a route added next year is checked
// without anybody listing it here.
func TestNoPublicRouteShowsWhatTheShopPaid(t *testing.T) {
	app := gctest.New(t, everyModule()...)
	ctx := context.Background()
	p := gctest.CreateProduct(t, app, "COSTLY-ALL", 2500, 5)
	v := p.Variants[0]
	// Recorded the way the panel records it. Ten digits, so finding them in a
	// body is finding the cost.
	if rec := gctest.AdminRequest(t, app, http.MethodPatch, "/api/admin/variants/"+strconv.FormatInt(v.ID, 10),
		map[string]any{"cost_minor": int64(8642097531)}); rec.Code != http.StatusOK {
		t.Fatalf("record the cost = %d %s", rec.Code, rec.Body)
	}

	// On every shelf a storefront reads, so each route has it to give away.
	shelf, err := app.Collections().Create(ctx, gocommerce.CollectionInput{Slug: "costly-shelf", Title: "Shelf"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Collections().SetProductCollections(ctx, p.ID, []int64{shelf.ID}); err != nil {
		t.Fatal(err)
	}
	aisle, err := app.Categories().Create(ctx, gocommerce.CategoryInput{Slug: "costly-aisle", Title: "Aisle"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Products().UpdateProduct(ctx, p.ID, gocommerce.ProductPatch{CategoryID: gocommerce.SetID(aisle.ID)}); err != nil {
		t.Fatal(err)
	}
	rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/showcases", map[string]any{
		"title": "Costly", "video_url": "https://youtu.be/dQw4w9WgXcQ",
		"product_ids": []int64{p.ID}, "status": "published",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("showcase = %d %s", rec.Code, rec.Body)
	}
	var showcase struct {
		Slug string `json:"slug"`
	}
	gctest.DecodeData(t, rec, &showcase)
	rec = gctest.Request(t, app, http.MethodPost, "/x/wishlist", map[string]any{"email": "wants@example.com"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("wishlist = %d %s", rec.Code, rec.Body)
	}
	var wishlist struct {
		Token string `json:"token"`
	}
	gctest.DecodeData(t, rec, &wishlist)
	if rec := gctest.Request(t, app, http.MethodPost, "/x/wishlist/"+wishlist.Token+"/items",
		map[string]any{"product_id": p.ID, "variant_id": v.ID}); rec.Code != http.StatusCreated {
		t.Fatalf("save to wishlist = %d %s", rec.Code, rec.Body)
	}
	on := true
	for _, key := range []string{"product-feeds", "sitemap"} {
		if _, err := app.Plugins().Update(ctx, key, gocommerce.PluginPatch{Enabled: &on,
			Settings: map[string]any{"storefront_url": "https://shop.example"}}); err != nil {
			t.Fatalf("switch on %s: %v", key, err)
		}
	}

	reached := gctest.AssertPublicRoutesHideCost(t, app, p, shelf.Slug, aisle.Slug, showcase.Slug, wishlist.Token)
	for _, want := range []string{"/api/collections/{slug}", "/api/categories/{slug}", "/x/showcases/{slug}",
		"/x/wishlist/{token}", "/x/feeds/google.xml", "/x/feeds/meta.csv"} {
		if !slices.Contains(reached, want) {
			t.Errorf("GET %s never answered with the product, so the walk proves nothing there (reached %v)", want, reached)
		}
	}
}
