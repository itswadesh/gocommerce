package gocommerce

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// Sample products (D94, D159): a new store may start with a small catalogue to
// look at, marked so it can be erased in one go without touching anything
// else.

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
	// The merchant already sells a saree under the sample's address: theirs
	// stays, and the sample is skipped rather than refused.
	if _, err := app.Products().CreateProduct(ctx, ProductInput{
		Slug: "satin-saree", Title: "Our own saree", Status: ProductActive,
		PriceMinor: &price, Stock: &stock,
	}); err != nil {
		t.Fatal(err)
	}

	added, err := app.Products().AddSampleProducts(ctx)
	if err != nil {
		t.Fatalf("add samples: %v", err)
	}
	if added != 10 {
		t.Fatalf("added = %d, want 10 (eleven, less the saree the store already has)", added)
	}
	if got := sampleCount(t, app, ctx); got != 10 {
		t.Fatalf("count = %d, want 10", got)
	}

	kurta, err := app.Products().GetProductBySlug(ctx, "printed-anarkali-kurta")
	if err != nil {
		t.Fatal(err)
	}
	if !kurta.Sample || kurta.Status != ProductActive || len(kurta.Variants) != 15 {
		t.Fatalf("kurta = sample %v, status %q, %d variants; want a sample, active, 3 colours by 5 sizes",
			kurta.Sample, kurta.Status, len(kurta.Variants))
	}
	if kurta.Metadata[MetaBadge] != "Angrakha wrap" || len(kurta.Metadata[MetaHighlights].([]any)) != 4 {
		t.Fatalf("kurta metadata = %v, want its badge and four highlights", kurta.Metadata)
	}
	byOptions := map[string]Variant{}
	for _, v := range kurta.Variants {
		byOptions[strings.Join(v.Options, "/")] = v
	}
	first := byOptions["Rose/XS"]
	if first.SKU != "PRINTED-ANARKALI-KURTA-ROSE-XS" || first.Price != (Money{AmountMinor: 4500, Currency: "USD"}) ||
		first.CompareAtPrice == nil || first.CompareAtPrice.AmountMinor != 5500 || first.StockOnHand != 6 {
		t.Fatalf("rose XS = %s at %+v (was %+v) with %d on hand, want PRINTED-ANARKALI-KURTA-ROSE-XS at 45.00 USD, was 55.00, with 6",
			first.SKU, first.Price, first.CompareAtPrice, first.StockOnHand)
	}
	if soldOut, low := byOptions["Mustard/XL"].StockOnHand, byOptions["Indigo/XS"].StockOnHand; soldOut != 0 || low != 2 {
		t.Fatalf("mustard XL has %d and indigo XS %d, want the catalogue's sold-out size at 0 and its low one at 2", soldOut, low)
	}

	own, err := app.Products().GetProductBySlug(ctx, "satin-saree")
	if err != nil {
		t.Fatal(err)
	}
	if own.Sample || own.Title != "Our own saree" {
		t.Fatalf("the merchant's saree = sample %v, %q; want it untouched", own.Sample, own.Title)
	}
	// The shirt's photographs show another company's logo, so it is not in
	// the catalogue at all.
	if _, err := app.Products().GetProductBySlug(ctx, "mens-cotton-shirt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the cotton shirt = %v, want no such product", err)
	}

	var variants, onHand int
	if err := app.DB().QueryRowContext(ctx, `
		SELECT count(*), COALESCE(sum(s.on_hand), 0)
		FROM variants v JOIN products p ON p.id = v.product_id
		JOIN variant_stock s ON s.variant_id = v.id
		WHERE p.sample`).Scan(&variants, &onHand); err != nil {
		t.Fatal(err)
	}
	if variants != 160 || onHand != 1657 {
		t.Fatalf("samples hold %d variants with %d on hand, want 160 with 1657", variants, onHand)
	}
	// Stock to sell means a shopper can put one in a basket.
	cart := newCart(t, app)
	if got := addToCart(t, app, cart.Token, first.ID, 2); len(got.Lines) != 1 || got.Lines[0].Quantity != 2 {
		t.Fatalf("basket = %+v, want one line of 2 kurtas", got.Lines)
	}

	again, err := app.Products().AddSampleProducts(ctx)
	if err != nil {
		t.Fatalf("add samples again: %v", err)
	}
	if again != 0 || sampleCount(t, app, ctx) != 10 {
		t.Fatalf("a second add made %d, count %d; want 0 and still 10", again, sampleCount(t, app, ctx))
	}
}

// The catalogue is priced in rupees. A store selling in rupees gets those
// prices; any other currency gets the dollar figure in its own units, never
// a rupee number read as dollars or yen.
func TestSampleProductsArePricedInTheStoresCurrency(t *testing.T) {
	for _, tc := range []struct {
		currency       string
		price, compare int64
	}{
		{"INR", 349900, 429900},
		{"USD", 4500, 5500},
		{"JPY", 45, 55},
		{"KWD", 45000, 55000},
	} {
		t.Run(tc.currency, func(t *testing.T) {
			app := newTestApp(t)
			ctx := withStoreCurrency(context.Background(), tc.currency)
			if _, err := app.Products().AddSampleProducts(ctx); err != nil {
				t.Fatal(err)
			}
			kurta, err := app.Products().GetProductBySlug(ctx, "printed-anarkali-kurta")
			if err != nil {
				t.Fatal(err)
			}
			v := kurta.Variants[0]
			if v.Price != (Money{AmountMinor: tc.price, Currency: tc.currency}) || v.CompareAtPrice == nil || v.CompareAtPrice.AmountMinor != tc.compare {
				t.Fatalf("kurta = %+v, was %+v; want %d %s, was %d", v.Price, v.CompareAtPrice, tc.price, tc.currency, tc.compare)
			}
			// A garment the catalogue does not mark down has no compare-at price.
			yoke, err := app.Products().GetProductBySlug(ctx, "printed-yoke-kurta")
			if err != nil {
				t.Fatal(err)
			}
			if yoke.Variants[0].CompareAtPrice != nil {
				t.Fatalf("yoke kurta compare-at = %+v, want none", yoke.Variants[0].CompareAtPrice)
			}
		})
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
	kurta, err := app.Products().GetProductBySlug(ctx, "printed-anarkali-kurta")
	if err != nil {
		t.Fatal(err)
	}
	renamed := "Our first kurta"
	if _, err := app.Products().UpdateProduct(ctx, kurta.ID, ProductPatch{Title: &renamed}); err != nil {
		t.Fatal(err)
	}

	deleted, err := app.Products().DeleteSampleProducts(ctx)
	if err != nil {
		t.Fatalf("erase samples: %v", err)
	}
	if deleted != 11 || sampleCount(t, app, ctx) != 0 {
		t.Fatalf("erased %d, %d left; want 11 and none", deleted, sampleCount(t, app, ctx))
	}
	if got, err := app.Products().GetProduct(ctx, own.ID); err != nil || got.Title != "Test OWN-1" {
		t.Fatalf("the merchant's product after the erase = %v, %v; want it kept", got, err)
	}
	if got := len(outboxEvents(t, app, EventProductDeleted)); got != 11 {
		t.Errorf("product.deleted events = %d, want one per sample, 11", got)
	}
	var audited int
	if err := app.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM admin_audit WHERE action = $1`, AuditProductDelete).Scan(&audited); err != nil {
		t.Fatal(err)
	}
	if audited != 11 {
		t.Errorf("audited deletes = %d, want 11", audited)
	}

	again, err := app.Products().DeleteSampleProducts(ctx)
	if err != nil || again != 0 {
		t.Fatalf("a second erase = %d, %v; want 0", again, err)
	}
}

// Every colour has its own photographs, and each of its variants shows them
// and no other colour's: which is what lets a storefront swap the gallery as a
// shopper picks a colour. Every picture is the engine's own, in the binary.
func TestSampleProductsComeWithTheirPhotos(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if _, err := app.Products().AddSampleProducts(ctx); err != nil {
		t.Fatal(err)
	}

	var bare int
	if err := app.DB().QueryRowContext(ctx, `
		SELECT count(*) FROM variants v JOIN products p ON p.id = v.product_id
		WHERE p.sample AND NOT EXISTS (SELECT 1 FROM variant_media vm WHERE vm.variant_id = v.id)`).Scan(&bare); err != nil {
		t.Fatal(err)
	}
	if bare != 0 {
		t.Fatalf("%d sample variants show no picture, want none", bare)
	}

	jumpsuit, err := app.Products().GetProductBySlug(ctx, "paisley-print-jumpsuit")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range jumpsuit.Variants {
		colour := strings.ToLower(v.Options[0])
		var got []string
		for _, img := range v.Images {
			got = append(got, strings.TrimPrefix(img.URL, SampleMediaPrefix)+" | "+img.Alt)
		}
		want := []string{
			"paisley-print-jumpsuit-" + colour + "-front.webp | Paisley print jumpsuit in " + colour + ", front view",
			"paisley-print-jumpsuit-" + colour + "-side.webp | Paisley print jumpsuit in " + colour + ", side view",
			"paisley-print-jumpsuit-" + colour + "-back.webp | Paisley print jumpsuit in " + colour + ", back view",
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("jumpsuit %v shows\n%s\nwant\n%s", v.Options, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}

	// Every one of them is served, from the binary, on every install.
	var urls []string
	rows, err := app.DB().QueryContext(ctx, `SELECT url FROM media WHERE sample ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			t.Fatal(err)
		}
		urls = append(urls, u)
	}
	rows.Close()
	if len(urls) != 83 {
		t.Fatalf("sample pictures = %d, want 83: every colour's shots of eleven products", len(urls))
	}
	for _, url := range urls {
		rec := do(t, app, http.MethodGet, url)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/webp" ||
			!strings.HasPrefix(rec.Body.String(), "RIFF") {
			t.Fatalf("GET %s = %d %q, want 200 image/webp with the picture", url, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	// D96's pictures are still served, for the stores whose sample shops and
	// products still link them.
	if rec := do(t, app, http.MethodGet, "/sample-media/1/cotton-crew-tee-black.webp"); rec.Code != http.StatusOK {
		t.Fatalf("a D96 picture = %d, want it still served", rec.Code)
	}
	for _, url := range []string{
		"/sample-media/1/../sample_products.go",
		"/sample-media/3/printed-anarkali-kurta-rose-front.webp",
		"/sample-media/2/..%2Fcatalogue.json",
	} {
		if rec := do(t, app, http.MethodGet, url); rec.Code == http.StatusOK {
			t.Fatalf("GET %s = 200, want nothing served", url)
		}
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
