package feeds

import (
	"context"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// The feed's channel is named after the shop. It used to take the order-number
// prefix, so the demo store's feed was titled "GC-".
func TestAFeedIsTitledWithTheStoresName(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ctx := context.Background()
	if _, err := app.Profile().Set(ctx, gocommerce.StoreProfile{Name: "ShopNx Demo"}, nil); err != nil {
		t.Fatal(err)
	}
	on := true
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{Enabled: &on,
		Settings: map[string]any{"storefront_url": "https://shop.example"}}); err != nil {
		t.Fatal(err)
	}
	rec := gctest.Request(t, app, http.MethodGet, "/x/feeds/google.xml", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>ShopNx Demo</title>") {
		t.Fatalf("google feed = %d, want it titled with the store's name: %.300s", rec.Code, rec.Body)
	}
}

// A store's feed is priced in the store's own currency. It used to take the
// install's, so the rupee demo store on a dollar install sent every item to
// Google as "2299.00 USD".
func TestAFeedIsPricedInTheStoresCurrency(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	if app.Currency(context.Background()) == "INR" {
		t.Skip("the test install already sells in rupees")
	}
	second, err := app.Stores().Create(context.Background(), "Rupee Shop", "rupee-shop")
	if err != nil {
		t.Fatal(err)
	}
	ctx := gocommerce.WithStore(context.Background(), second.ID)
	p, err := app.Profile().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.Currency = "INR"
	if _, err := app.Profile().Set(ctx, *p, nil); err != nil {
		t.Fatalf("choose INR: %v", err)
	}
	price := int64(229900)
	if _, err := app.Products().CreateProduct(ctx, gocommerce.ProductInput{Title: "Kurta", SKU: "KURTA-1", PriceMinor: &price, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	on := true
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{Enabled: &on,
		Settings: map[string]any{"storefront_url": "https://shop.example"}}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/x/feeds/google.xml", nil)
	req.Header.Set("X-Store", second.ID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "<g:price>2299.00 INR</g:price>") {
		t.Fatalf("google feed = %d, want the price in INR: %.600s", rec.Code, rec.Body)
	}
}

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

	// Off: nothing to fetch. On with no address of the store's own and none
	// typed: the fetch is refused and says what to set, because a feed that is
	// on and empty is how a new store gets lost.
	if rec := gctest.Request(t, app, http.MethodGet, "/x/feeds/google.xml", nil); rec.Code != http.StatusNotFound {
		t.Errorf("feed while off = %d", rec.Code)
	}
	on := true
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	if rec := gctest.Request(t, app, http.MethodGet, "/x/feeds/google.xml", nil); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "storefront URL") {
		t.Fatalf("feed with no address = %d: %s, want it refused naming the setting", rec.Code, rec.Body)
	}
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{Enabled: &on,
		Settings: map[string]any{"storefront_url": "https://shop.example", "brand": "House"}}); err != nil {
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
		"<g:link>https://shop.example/products/crew-tee?variant_id=" + strconv.FormatInt(variantID(t, p, "TEE-S"), 10) + "</g:link>",
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

// Switching a feed on is the one step: its links use the store's own address
// unless the store typed another.
func TestAFeedUsesTheStoresOwnAddressWithNoneTyped(t *testing.T) {
	app := gctest.NewWithConfig(t, gocommerce.Config{BaseDomain: "shops.example.com"}, New(Config{}))
	live := gctest.CreateProduct(t, app, "FD-LIVE", 1000, 1)
	on := true
	if _, err := app.Plugins().Update(context.Background(), pluginKey, gocommerce.PluginPatch{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	feed := gctest.Request(t, app, http.MethodGet, "/x/feeds/google.xml", nil)
	if feed.Code != http.StatusOK {
		t.Fatalf("feed = %d: %s", feed.Code, feed.Body)
	}
	if want := ".shops.example.com/products/" + live.Slug; !strings.Contains(feed.Body.String(), want) {
		t.Errorf("feed = %s, want a link ending %s", feed.Body, want)
	}
}

// variantID is the id of the variant with that SKU.
func variantID(t *testing.T, p *gocommerce.Product, sku string) int64 {
	t.Helper()
	for _, v := range p.Variants {
		if v.SKU == sku {
			return v.ID
		}
	}
	t.Fatalf("no variant %s", sku)
	return 0
}
