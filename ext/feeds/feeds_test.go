package feeds

import (
	"context"
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"testing"

	gocommerce "github.com/misiki/gocommerce/core"
	"github.com/misiki/gocommerce/gctest"
)

func TestTheFeedsCarryEveryActiveVariant(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ctx := context.Background()
	price := int64(1999)
	compare := int64(2499)
	p, err := app.Products().CreateProduct(ctx, gocommerce.ProductInput{
		Title: "Crew Tee", Slug: "crew-tee", Status: "active", Vendor: "Acme",
		Options: []gocommerce.OptionInput{{Name: "Size", Values: []string{"S", "M"}}},
		Variants: []gocommerce.VariantInput{
			{SKU: "TEE-S", PriceMinor: price, CompareAtPriceMinor: &compare, Options: []string{"S"}, StockOnHand: ptr(3), Barcode: "111"},
			{SKU: "TEE-M", PriceMinor: price, Options: []string{"M"}, TrackInventory: ptr(false)},
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := app.Products().CreateProduct(ctx, gocommerce.ProductInput{Title: "Draft", SKU: "DRAFT-1", PriceMinor: &price, Status: "draft"}); err != nil {
		t.Fatalf("draft: %v", err)
	}

	// Off: nothing to fetch. On without an address: told what to set.
	if rec := gctest.Request(t, app, http.MethodGet, "/x/feeds/google.xml", nil); rec.Code != http.StatusNotFound {
		t.Errorf("feed while off = %d", rec.Code)
	}
	on := true
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	if rec := gctest.Request(t, app, http.MethodGet, "/x/feeds/google.xml", nil); rec.Code != http.StatusConflict {
		t.Errorf("feed without a storefront = %d, want 409", rec.Code)
	}
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{Settings: map[string]any{"storefront_url": "https://shop.example", "brand": "House"}}); err != nil {
		t.Fatal(err)
	}

	rec := gctest.Request(t, app, http.MethodGet, "/x/feeds/google.xml", nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/rss+xml") {
		t.Fatalf("google feed = %d %s: %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`xmlns:g="http://base.google.com/ns/1.0"`,
		"<g:id>TEE-S</g:id>", "<g:title>Crew Tee - S</g:title>",
		"<g:link>https://shop.example/products/crew-tee?variant=TEE-S</g:link>",
		"<g:price>24.99 USD</g:price>", "<g:sale_price>19.99 USD</g:sale_price>",
		"<g:availability>in stock</g:availability>", "<g:brand>Acme</g:brand>", "<g:gtin>111</g:gtin>",
		"<g:item_group_id>product-" + strconv.FormatInt(p.ID, 10) + "</g:item_group_id>",
		"<g:id>TEE-M</g:id>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("google feed lacks %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, "DRAFT-1") {
		t.Errorf("the draft is in the feed")
	}
	if strings.Count(body, "<item>") != 2 {
		t.Errorf("items = %d, want the two active variants", strings.Count(body, "<item>"))
	}

	meta := gctest.Request(t, app, http.MethodGet, "/x/feeds/meta.csv", nil)
	if meta.Code != http.StatusOK {
		t.Fatalf("meta feed = %d: %s", meta.Code, meta.Body)
	}
	rows, err := csv.NewReader(strings.NewReader(meta.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("parse meta csv: %v", err)
	}
	if len(rows) != 3 || strings.Join(rows[0], ",") != strings.Join(metaHeader, ",") {
		t.Fatalf("meta rows = %v", rows)
	}
	col := func(row []string, name string) string {
		for i, h := range rows[0] {
			if h == name {
				return row[i]
			}
		}
		return ""
	}
	if col(rows[1], "id") != "TEE-S" || col(rows[1], "price") != "24.99 USD" || col(rows[1], "sale_price") != "19.99 USD" ||
		col(rows[1], "quantity_to_sell_on_facebook") != "3" || col(rows[2], "quantity_to_sell_on_facebook") != "999" {
		t.Errorf("meta rows = %v", rows[1:])
	}
}

func TestFeedRoutesAreDocumented(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	gctest.AssertSpecCoversModuleRoutes(t, app, "feeds")
}

func ptr[T any](v T) *T { return &v }

// The Feeds screen cannot tell an operator anything useful about a file that
// is rebuilt on every request unless it can ask what that file currently
// holds. Counting from the same walk the feed itself does is the point: a
// number that comes from anywhere else is a second implementation of "what is
// in the feed" and will disagree with it eventually.
//
// The three "missing" counts are the three things platforms actually reject
// items over, which is what turns the advice on that screen from a leaflet
// into a list of this store's own problems.
func TestTheStatusCountsWhatTheFeedActuallyHolds(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ctx := context.Background()
	price := int64(1500)

	// Two variants, a brand, a barcode on one of them, no pictures anywhere.
	if _, err := app.Products().CreateProduct(ctx, gocommerce.ProductInput{
		Title: "Crew Tee", Slug: "crew-tee", Status: "active", Vendor: "Acme",
		Options: []gocommerce.OptionInput{{Name: "Size", Values: []string{"S", "M"}}},
		Variants: []gocommerce.VariantInput{
			{SKU: "TEE-S", PriceMinor: price, Options: []string{"S"}, StockOnHand: ptr(4), Barcode: "111"},
			{SKU: "TEE-M", PriceMinor: price, Options: []string{"M"}, StockOnHand: ptr(0)},
		},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// A second product with no vendor at all, so "missing brand" is not zero.
	if _, err := app.Products().CreateProduct(ctx, gocommerce.ProductInput{
		Title: "Mug", Slug: "mug", Status: "active", SKU: "MUG-1", PriceMinor: &price, Stock: ptr(2),
	}); err != nil {
		t.Fatalf("mug: %v", err)
	}
	// A draft, which is in neither the feed nor the count.
	if _, err := app.Products().CreateProduct(ctx, gocommerce.ProductInput{
		Title: "Draft", SKU: "DRAFT-1", PriceMinor: &price, Status: "draft",
	}); err != nil {
		t.Fatalf("draft: %v", err)
	}

	// Switched off, the screen still needs to render, so the status answers
	// rather than 404s — it just says the feed is not being served.
	var off feedStatus
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/feeds/status", nil), &off)
	if off.Enabled || off.Configured {
		t.Errorf("status while off = %+v, want enabled and configured false", off)
	}

	on := true
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{
		Enabled: &on, Settings: map[string]any{"storefront_url": "https://shop.example"},
	}); err != nil {
		t.Fatal(err)
	}

	var got feedStatus
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/feeds/status", nil), &got)
	if !got.Enabled || !got.Configured {
		t.Fatalf("status = %+v, want it serving", got)
	}
	if got.Items != 3 {
		t.Errorf("items = %d, want the three active variants", got.Items)
	}
	if got.Products != 2 {
		t.Errorf("products = %d, want the two active products", got.Products)
	}
	if got.InStock != 2 || got.OutOfStock != 1 {
		t.Errorf("in stock = %d, out = %d, want 2 and 1", got.InStock, got.OutOfStock)
	}
	if got.MissingImage != 3 {
		t.Errorf("missing image = %d, want all three", got.MissingImage)
	}
	if got.MissingBrand != 1 {
		t.Errorf("missing brand = %d, want the mug", got.MissingBrand)
	}
	// Only the mug. The tee in size M has no barcode, but it has a vendor and
	// the feed sends its SKU as the MPN, and a brand with an MPN is the pair
	// Google accepts in place of a GTIN.
	if got.MissingIdentifier != 1 {
		t.Errorf("missing identifier = %d, want the mug alone", got.MissingIdentifier)
	}
	if got.Currency != "USD" || got.Storefront != "https://shop.example" {
		t.Errorf("currency = %q, storefront = %q", got.Currency, got.Storefront)
	}

	// The count and the feed are the same walk, so they cannot drift.
	rec := gctest.Request(t, app, http.MethodGet, "/x/feeds/google.xml", nil)
	if n := strings.Count(rec.Body.String(), "<item>"); n != got.Items {
		t.Errorf("the feed carries %d items and the status claims %d", n, got.Items)
	}
}
