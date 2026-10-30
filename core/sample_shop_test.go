package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A sample shop (D96): the sample products and everything a store shows
// around them — categories, collections, discounts and orders, and through the
// orders its customers — all marked, so one erase takes them and nothing else.

func TestASampleShopHasEverythingAStoreShows(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	added, err := app.AddSampleShop(ctx)
	if err != nil {
		t.Fatalf("add the sample shop: %v", err)
	}
	want := SampleCounts{"products": 11, "categories": 8, "collections": 2, "discounts": 2, "orders": 12, "customers": 6}
	for kind, n := range want {
		if added[kind] != n {
			t.Errorf("added %d %s, want %d (all: %v)", added[kind], kind, n, added)
		}
	}

	// Every sample product sits in the sample category the catalogue files
	// it in, under Women or Men.
	kurta, err := app.Products().GetProductBySlug(ctx, "printed-anarkali-kurta")
	if err != nil {
		t.Fatal(err)
	}
	if kurta.Category == nil {
		t.Fatal("the kurta has no category")
	}
	var catPath string
	if err := app.DB().QueryRowContext(ctx, `
		SELECT p.slug || '/' || c.slug FROM categories c JOIN categories p ON p.id = c.parent_id
		WHERE c.id = $1 AND c.sample AND p.sample`, kurta.Category.ID).Scan(&catPath); err != nil || catPath != "women/women-ethnic-wear" {
		t.Fatalf("the kurta's category = %q, %v; want the sample ethnic wear category under women", catPath, err)
	}
	// Each sample category has a picture, which a storefront shows on its
	// own category tiles (the connector reads metadata.image), and the
	// engine serves it.
	var pictures []string
	rows, err := app.DB().QueryContext(ctx, `SELECT metadata->>'image' FROM categories WHERE sample ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		pictures = append(pictures, p)
	}
	rows.Close()
	if len(pictures) != 8 {
		t.Fatalf("sample categories with a picture = %d, want all 8", len(pictures))
	}
	for _, p := range pictures {
		if rec := do(t, app, http.MethodGet, p); !strings.HasPrefix(p, SampleMediaPrefix) || rec.Code != http.StatusOK {
			t.Fatalf("category picture %q = %d, want one of the sample set, served", p, rec.Code)
		}
	}
	var uncategorised int
	if err := app.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM products WHERE sample AND category_id IS NULL`).Scan(&uncategorised); err != nil || uncategorised != 0 {
		t.Fatalf("%d sample products have no category (%v), want none", uncategorised, err)
	}

	// New arrivals holds four products and the festive edit five.
	var members int
	if err := app.DB().QueryRowContext(ctx, `
		SELECT count(*) FROM product_collections pc JOIN collections c ON c.id = pc.collection_id
		WHERE c.sample`).Scan(&members); err != nil || members != 9 {
		t.Fatalf("sample collections hold %d products (%v), want 9", members, err)
	}

	// A sample discount can only ever take money off a sample product.
	own := simpleProduct(t, app, "OWN-1", 1500, 3)
	var scopes []string
	rows, err = app.DB().QueryContext(ctx, `SELECT scope FROM discounts WHERE sample`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		scopes = append(scopes, s)
	}
	rows.Close()
	if len(scopes) != 2 || scopes[0] != DiscountScopeProducts || scopes[1] != DiscountScopeProducts {
		t.Fatalf("sample discount scopes = %v, want both limited to products", scopes)
	}
	var reachesOwn bool
	if err := app.DB().QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM discount_targets dt JOIN discounts d ON d.id = dt.discount_id
		               WHERE d.sample AND dt.target_id = $1)`, own.ID).Scan(&reachesOwn); err != nil {
		t.Fatal(err)
	}
	if reachesOwn {
		t.Fatal("a sample discount targets the merchant's own product")
	}

	// The orders say they are samples, spread over the last month, and told
	// nobody: no event, so no email, SMS or webhook went anywhere.
	var numbers []string
	rows, err = app.DB().QueryContext(ctx, `SELECT number, created_at FROM orders WHERE sample ORDER BY created_at`)
	if err != nil {
		t.Fatal(err)
	}
	var oldest, newest time.Time
	for rows.Next() {
		var n string
		var at time.Time
		if err := rows.Scan(&n, &at); err != nil {
			t.Fatal(err)
		}
		if oldest.IsZero() {
			oldest = at
		}
		newest = at
		numbers = append(numbers, n)
	}
	rows.Close()
	for _, n := range numbers {
		if !strings.HasPrefix(n, "SAMPLE-") {
			t.Fatalf("order number %q does not say it is a sample", n)
		}
	}
	if span := newest.Sub(oldest); span < 20*24*time.Hour || time.Since(oldest) > 31*24*time.Hour {
		t.Fatalf("orders run from %v to %v, want them spread over the last month", oldest, newest)
	}
	if got := len(outboxEvents(t, app, EventOrderCreated)); got != 0 {
		t.Fatalf("order.created events = %d, want none: a sample order notifies nobody", got)
	}
	customers, total, err := app.Order().Customers(ctx, CustomerQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 6 || !strings.HasSuffix(customers[0].Email, "@example.com") {
		t.Fatalf("customers = %d (first %q), want 6 at example.com, an address nobody receives mail at", total, customers[0].Email)
	}

	// Asking twice adds nothing.
	again, err := app.AddSampleShop(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for kind, n := range again {
		if n != 0 && kind != "customers" {
			t.Errorf("a second add made %d %s, want none", n, kind)
		}
	}
	counts, err := app.SampleShopCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for kind, n := range want {
		if counts[kind] != n {
			t.Errorf("counted %d %s, want %d", counts[kind], kind, n)
		}
	}
}

func TestErasingTheSampleShopLeavesTheMerchantsOwn(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if _, err := app.AddSampleShop(ctx); err != nil {
		t.Fatal(err)
	}
	// The merchant's own things, beside the samples.
	own := simpleProduct(t, app, "OWN-1", 1500, 3)
	cat, err := app.Categories().Create(ctx, CategoryInput{Title: "Our range"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Collections().Create(ctx, CollectionInput{Title: "Our picks"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Discounts().Create(ctx, DiscountInput{Code: "OURS", Title: "Ours", Kind: DiscountPercentage, ValueBP: 500}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Data().ImportOrders(ctx, strings.NewReader(
		"number,email,sku,quantity,unit_price_minor\nOWN-100,real@example.org,OWN-1,1,1500\n"), ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	// The merchant filed their own product in a sample category: that one
	// stays, because erasing it would take their product's category away.
	// Its parent stays with it, since a category cannot go while another is
	// under it.
	var dresses int64
	if err := app.DB().QueryRowContext(ctx, `SELECT id FROM categories WHERE slug = 'women-dresses'`).Scan(&dresses); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Products().UpdateProduct(ctx, own.ID, ProductPatch{CategoryID: SetID(dresses)}); err != nil {
		t.Fatal(err)
	}

	erased, err := app.EraseSampleShop(ctx)
	if err != nil {
		t.Fatalf("erase: %v", err)
	}
	want := SampleCounts{"products": 11, "categories": 6, "collections": 2, "discounts": 2, "orders": 12}
	for kind, n := range want {
		if erased[kind] != n {
			t.Errorf("erased %d %s, want %d (all: %v)", erased[kind], kind, n, erased)
		}
	}

	for table, wantLeft := range map[string]int{"products": 1, "collections": 1, "discounts": 1, "orders": 1} {
		var left int
		if err := app.DB().QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left != wantLeft {
			t.Errorf("%s left = %d, want the merchant's %d", table, left, wantLeft)
		}
	}
	var cats []string
	rows, err := app.DB().QueryContext(ctx, `SELECT slug FROM categories ORDER BY slug`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		cats = append(cats, s)
	}
	rows.Close()
	if strings.Join(cats, ",") != cat.Slug+",women,women-dresses" {
		t.Fatalf("categories left = %v, want the merchant's own, the sample one their product is in and its parent", cats)
	}
	var lines int
	if err := app.DB().QueryRowContext(ctx, `SELECT count(*) FROM order_lines`).Scan(&lines); err != nil || lines != 1 {
		t.Fatalf("order lines left = %d (%v), want the merchant's one", lines, err)
	}

	counts, err := app.SampleShopCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts.total() != 0 {
		t.Fatalf("counts after the erase = %v, want nothing: the category in use is the merchant's now", counts)
	}
}

func TestTheSampleShopOverTheAdminAPI(t *testing.T) {
	app := newTestApp(t)
	staff := signInAs(t, app, "staff@example.com", RoleStaff)

	rec := do(t, app, http.MethodPost, "/api/admin/sample-content", bearer(staff))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("add as staff = %d, want 403: %s", rec.Code, rec.Body)
	}
	rec = do(t, app, http.MethodPost, "/api/admin/sample-content", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("add = %d: %s", rec.Code, rec.Body)
	}
	var added struct {
		Added SampleCounts `json:"added"`
	}
	decodeData(t, rec, &added)
	if added.Added["products"] != 11 || added.Added["orders"] != 12 {
		t.Fatalf("added = %v, want 11 products and 12 orders", added.Added)
	}

	var counted struct {
		Counts SampleCounts `json:"counts"`
		Total  int          `json:"total"`
	}
	rec = do(t, app, http.MethodGet, "/api/admin/sample-content", bearer(staff))
	if rec.Code != http.StatusOK {
		t.Fatalf("count as staff = %d: %s", rec.Code, rec.Body)
	}
	decodeData(t, rec, &counted)
	if counted.Counts["products"] != 11 || counted.Total == 0 {
		t.Fatalf("counts = %+v, want 11 products and a total", counted)
	}

	if rec := do(t, app, http.MethodDelete, "/api/admin/sample-content", bearer(staff)); rec.Code != http.StatusForbidden {
		t.Fatalf("erase as staff = %d, want 403: %s", rec.Code, rec.Body)
	}
	rec = do(t, app, http.MethodDelete, "/api/admin/sample-content", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("erase = %d: %s", rec.Code, rec.Body)
	}
	decodeData(t, do(t, app, http.MethodGet, "/api/admin/sample-content", withAdmin), &counted)
	if counted.Total != 0 {
		t.Fatalf("counts after the erase = %+v, want nothing left", counted)
	}
}

// The basket discount's minimum is the shop's money, as the prices are: 5,000
// in a store selling in rupees, a face value of 100 in any other.
func TestTheSampleBasketDiscountIsInTheStoresCurrency(t *testing.T) {
	for _, tc := range []struct {
		currency, title string
		minimum         int64
	}{
		{"INR", "15% off a basket of 5,000 or more", 500000},
		{"USD", "15% off a basket of 100 or more", 10000},
		{"JPY", "15% off a basket of 100 or more", 100},
	} {
		t.Run(tc.currency, func(t *testing.T) {
			app := newTestApp(t)
			ctx := withStoreCurrency(context.Background(), tc.currency)
			if _, err := app.AddSampleShop(ctx); err != nil {
				t.Fatal(err)
			}
			d, err := app.Discounts().GetByCode(ctx, "BUNDLE15")
			if err != nil {
				t.Fatal(err)
			}
			if d.Title != tc.title || d.MinSubtotalMinor == nil || *d.MinSubtotalMinor != tc.minimum {
				t.Fatalf("BUNDLE15 = %q from %v, want %q from %d", d.Title, d.MinSubtotalMinor, tc.title, tc.minimum)
			}
		})
	}
}

// Nothing a client sends can file its own row among the samples.
func TestAClientCannotMarkItsOwnRowsAsSamples(t *testing.T) {
	app := newTestApp(t)
	for _, tc := range []struct{ path, body string }{
		{"/api/admin/categories", `{"title":"Mine","sample":true}`},
		{"/api/admin/collections", `{"title":"Mine","sample":true}`},
		{"/api/admin/discounts", `{"code":"MINE","title":"Mine","kind":"percentage","value_bp":100,"sample":true}`},
	} {
		if rec := doBody(t, app, http.MethodPost, tc.path, tc.body, withAdmin); rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s with sample = %d, want 400: %s", tc.path, rec.Code, rec.Body)
		}
	}
}
