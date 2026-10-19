package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// Sample products (D94): a new store may start with a small catalogue to look
// at, marked so it can be erased in one go without touching anything else.

func sampleCount(t *testing.T, app *App, ctx context.Context) int {
	t.Helper()
	n, err := app.Products().SampleProductCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSampleProductsAreAddedOnceWithStockToSell(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	price, stock := int64(4000), 2
	// The merchant already sells a tote under the sample's address: theirs
	// stays, and the sample is skipped rather than refused.
	if _, err := app.Products().CreateProduct(ctx, ProductInput{
		Slug: "canvas-tote-bag", Title: "Our own tote", Status: ProductActive,
		PriceMinor: &price, Stock: &stock,
	}); err != nil {
		t.Fatal(err)
	}

	added, err := app.Products().AddSampleProducts(ctx)
	if err != nil {
		t.Fatalf("add samples: %v", err)
	}
	if added != 7 {
		t.Fatalf("added = %d, want 7 (eight, less the tote the store already has)", added)
	}
	if got := sampleCount(t, app, ctx); got != 7 {
		t.Fatalf("count = %d, want 7", got)
	}

	tee, err := app.Products().GetProductBySlug(ctx, "cotton-crew-tee")
	if err != nil {
		t.Fatal(err)
	}
	if !tee.Sample || tee.Status != ProductActive || len(tee.Variants) != 6 {
		t.Fatalf("tee = sample %v, status %q, %d variants; want a sample, active, 6 variants",
			tee.Sample, tee.Status, len(tee.Variants))
	}
	first := tee.Variants[0]
	if first.SKU != "COTTON-CREW-TEE-S-BLACK" || first.Price != (Money{AmountMinor: 2500, Currency: "USD"}) || first.StockOnHand != 12 {
		t.Fatalf("first tee variant = %s at %+v with %d on hand, want COTTON-CREW-TEE-S-BLACK at 2500 USD with 12",
			first.SKU, first.Price, first.StockOnHand)
	}

	own, err := app.Products().GetProductBySlug(ctx, "canvas-tote-bag")
	if err != nil {
		t.Fatal(err)
	}
	if own.Sample || own.Title != "Our own tote" {
		t.Fatalf("the merchant's tote = sample %v, %q; want it untouched", own.Sample, own.Title)
	}

	var variants, onHand int
	if err := app.DB().QueryRowContext(ctx, `
		SELECT count(*), COALESCE(sum(s.on_hand), 0)
		FROM variants v JOIN products p ON p.id = v.product_id
		JOIN variant_stock s ON s.variant_id = v.id
		WHERE p.sample`).Scan(&variants, &onHand); err != nil {
		t.Fatal(err)
	}
	if variants != 16 || onHand != 221 {
		t.Fatalf("samples hold %d variants with %d on hand, want 16 with 221", variants, onHand)
	}
	// Stock to sell means a shopper can put one in a basket.
	cart := newCart(t, app)
	if got := addToCart(t, app, cart.Token, first.ID, 2); len(got.Lines) != 1 || got.Lines[0].Quantity != 2 {
		t.Fatalf("basket = %+v, want one line of 2 tees", got.Lines)
	}

	again, err := app.Products().AddSampleProducts(ctx)
	if err != nil {
		t.Fatalf("add samples again: %v", err)
	}
	if again != 0 || sampleCount(t, app, ctx) != 7 {
		t.Fatalf("a second add made %d, count %d; want 0 and still 7", again, sampleCount(t, app, ctx))
	}
}

func TestErasingSampleProductsLeavesTheMerchantsOwn(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if _, err := app.Products().AddSampleProducts(ctx); err != nil {
		t.Fatal(err)
	}
	own := simpleProduct(t, app, "OWN-1", 1500, 3)
	// An edited sample is still a sample: the flag is the engine's, not a
	// field the merchant's edit can clear.
	tee, err := app.Products().GetProductBySlug(ctx, "cotton-crew-tee")
	if err != nil {
		t.Fatal(err)
	}
	renamed := "Our first tee"
	if _, err := app.Products().UpdateProduct(ctx, tee.ID, ProductPatch{Title: &renamed}); err != nil {
		t.Fatal(err)
	}

	deleted, err := app.Products().DeleteSampleProducts(ctx)
	if err != nil {
		t.Fatalf("erase samples: %v", err)
	}
	if deleted != 8 || sampleCount(t, app, ctx) != 0 {
		t.Fatalf("erased %d, %d left; want 8 and none", deleted, sampleCount(t, app, ctx))
	}
	if got, err := app.Products().GetProduct(ctx, own.ID); err != nil || got.Title != "Test OWN-1" {
		t.Fatalf("the merchant's product after the erase = %v, %v; want it kept", got, err)
	}
	if got := len(outboxEvents(t, app, EventProductDeleted)); got != 8 {
		t.Errorf("product.deleted events = %d, want one per sample, 8", got)
	}
	var audited int
	if err := app.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM admin_audit WHERE action = $1`, AuditProductDelete).Scan(&audited); err != nil {
		t.Fatal(err)
	}
	if audited != 8 {
		t.Errorf("audited deletes = %d, want 8", audited)
	}

	again, err := app.Products().DeleteSampleProducts(ctx)
	if err != nil || again != 0 {
		t.Fatalf("a second erase = %d, %v; want 0", again, err)
	}
}

// Every sample has a photograph, and a colour shows its own: the black tee
// nominates the black picture and the white tee the white one, which is what
// lets a storefront swap the gallery as a shopper picks a colour.
func TestSampleProductsComeWithTheirPhotos(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if _, err := app.Products().AddSampleProducts(ctx); err != nil {
		t.Fatal(err)
	}

	var bare int
	if err := app.DB().QueryRowContext(ctx, `
		SELECT count(*) FROM products p
		WHERE p.sample AND NOT EXISTS (SELECT 1 FROM product_media pm WHERE pm.product_id = p.id)`).Scan(&bare); err != nil {
		t.Fatal(err)
	}
	if bare != 0 {
		t.Fatalf("%d sample products have no picture, want none", bare)
	}

	tee, err := app.Products().GetProductBySlug(ctx, "cotton-crew-tee")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range tee.Variants {
		if len(v.Images) != 1 {
			t.Fatalf("tee %v shows %d pictures, want its colour's one", v.Options, len(v.Images))
		}
		colour := strings.ToLower(v.Options[1])
		if !strings.HasSuffix(v.Images[0].URL, "/cotton-crew-tee-"+colour+".webp") || v.Images[0].Alt == "" {
			t.Fatalf("tee %v shows %q (alt %q), want the %s tee's picture with alt text",
				v.Options, v.Images[0].URL, v.Images[0].Alt, colour)
		}
	}

	// The picture is the engine's own, served from the binary on every
	// install, with or without somewhere to put uploads.
	url := tee.Variants[0].Images[0].URL
	if !strings.HasPrefix(url, "/sample-media/") {
		t.Fatalf("picture url = %q, want one the engine serves under /sample-media/", url)
	}
	rec := do(t, app, http.MethodGet, url)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/webp" ||
		!strings.HasPrefix(rec.Body.String(), "RIFF") {
		t.Fatalf("GET %s = %d %q, want 200 image/webp with the picture", url, rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := do(t, app, http.MethodGet, "/sample-media/1/../sample_products.go"); rec.Code == http.StatusOK {
		t.Fatalf("a path outside the pictures was served")
	}

	// Erasing the samples takes their pictures out of the media library too.
	if _, err := app.Products().DeleteSampleProducts(ctx); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := app.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM media WHERE url LIKE '/sample-media/%'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d sample pictures left in the media library, want none", left)
	}
}

// The flag is the engine's alone: a client cannot mark its own product a
// sample, which would put it in the next erase.
func TestAClientCannotMarkAProductASample(t *testing.T) {
	app := newTestApp(t)
	rec := doBody(t, app, http.MethodPost, "/api/admin/products",
		`{"title":"Mine","status":"active","price_minor":100,"sample":true}`, withAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create with sample = %d, want 400: %s", rec.Code, rec.Body)
	}
	p := simpleProduct(t, app, "MINE-1", 100, 1)
	rec = doBody(t, app, http.MethodPatch, "/api/admin/products/"+itoa(p.ID), `{"sample":true}`, withAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("patch with sample = %d, want 400: %s", rec.Code, rec.Body)
	}
	if got := sampleCount(t, app, context.Background()); got != 0 {
		t.Fatalf("count = %d, want 0", got)
	}
}
