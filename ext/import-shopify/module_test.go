package shopify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// The module against a real engine and a stub Shopify.
//
// The client's own tests cover paging and money in isolation; these cover the
// thing those cannot: that a catalogue actually arrives, with its variants,
// options and pictures attached, and that running the import twice updates what
// it brought over rather than importing it again.

// stubShop serves one page of products, as Shopify would.
func stubShop(t *testing.T, products []Product) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Shopify-Access-Token") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "/shop.json"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"shop": map[string]any{"name": "Acme Supply", "currency": "USD"},
			})
		case strings.Contains(r.URL.Path, "/products/count.json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"count": len(products)})
		case strings.Contains(r.URL.Path, "/products.json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"products": products})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testProducts() []Product {
	shopify := "shopify"
	return []Product{{
		ID: 8001, Title: "Merino crew neck", Handle: "merino-crew-neck",
		BodyHTML: "<p>Soft &amp; warm.</p>", Vendor: "Northwool", ProductType: "Knitwear",
		Status: "active", Tags: "wool, winter",
		Options: []Option{{Name: "Size", Values: []string{"S", "M"}}},
		Images: []Image{
			{ID: 9001, Src: "https://cdn.example/merino-1.jpg", Alt: "Front"},
			{ID: 9002, Src: "https://cdn.example/merino-2.jpg", Alt: "Back"},
		},
		Variants: []Variant{
			{
				ID: 7001, SKU: "MER-S", Price: "89.00", Barcode: "5060001",
				InventoryQuantity: 3, InventoryMgmt: &shopify, Taxable: true,
				RequiresShipping: true, Grams: 320, Option1: strptr("S"), ImageID: idptr(9001),
			},
			{
				ID: 7002, SKU: "MER-M", Price: "89.00", CompareAtPrice: strptr("110.00"),
				InventoryQuantity: 0, InventoryMgmt: &shopify, Taxable: true,
				RequiresShipping: true, Grams: 340, Option1: strptr("M"),
			},
		},
	}}
}

func idptr(v int64) *int64 { return &v }

// newImportApp boots an engine with this module registered and points it at a
// stub shop.
func newImportApp(t *testing.T, srv *httptest.Server) (*gocommerce.App, *Module) {
	t.Helper()
	if os.Getenv("GOCOMMERCE_TEST_DB") == "" {
		t.Skip("GOCOMMERCE_TEST_DB is not set")
	}
	m := New()
	app := gctest.New(t, m)

	// The settings a merchant would type on the plugins screen.
	on := true
	if _, err := app.Plugins().Update(context.Background(), pluginKey, gocommerce.PluginPatch{
		Enabled: &on,
		Settings: map[string]any{
			"shop":         "acme.myshopify.com",
			"access_token": "shpat_test",
		},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	return app, m
}

// run drives one import to completion, with the stub standing in for Shopify.
func run(t *testing.T, app *gocommerce.App, m *Module, srv *httptest.Server) *Job {
	t.Helper()
	ctx := context.Background()

	client, err := m.settings(ctx)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	client.HTTP = &http.Client{Transport: rewriteToTest(srv.URL)}

	shop, err := client.Verify(ctx)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	var id int64
	if err := app.DB().QueryRowContext(ctx, `
		INSERT INTO import_shopify_jobs (shop, status, step, total)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		client.Shop, StatusRunning, "reading the catalogue", shop.Products).Scan(&id); err != nil {
		t.Fatalf("job row: %v", err)
	}

	m.run(ctx, id, client)

	job, err := m.job(ctx, id)
	if err != nil {
		t.Fatalf("read job: %v", err)
	}
	return job
}

func TestACatalogueArrives(t *testing.T) {
	srv := stubShop(t, testProducts())
	app, m := newImportApp(t, srv)
	ctx := context.Background()

	job := run(t, app, m, srv)
	if job.Status != StatusDone {
		t.Fatalf("status = %q (%s), want done", job.Status, job.Message)
	}
	if job.Created != 1 || job.Updated != 0 || job.Failed != 0 {
		t.Errorf("created %d, updated %d, failed %d — want 1/0/0", job.Created, job.Updated, job.Failed)
	}
	// The bar has to reach the end, or an import that finished looks stuck.
	if job.Percent != 100 {
		t.Errorf("percent = %d at the end, want 100", job.Percent)
	}

	p, err := app.Products().GetProductBySlug(ctx, "merino-crew-neck")
	if err != nil {
		t.Fatalf("the product did not arrive: %v", err)
	}
	if p.Title != "Merino crew neck" {
		t.Errorf("title = %q", p.Title)
	}
	// Shopify's vendor is the brand, and it stays the brand.
	if p.Vendor != "Northwool" {
		t.Errorf("vendor = %q, want Northwool", p.Vendor)
	}
	if strings.Contains(p.Description, "<") {
		t.Errorf("markup reached the description: %q", p.Description)
	}
	if len(p.Options) != 1 || len(p.Options[0].Values) != 2 {
		t.Errorf("options = %+v, want one axis of two", p.Options)
	}
	if len(p.Variants) != 2 {
		t.Fatalf("variants = %d, want 2", len(p.Variants))
	}

	bySKU := map[string]gocommerce.Variant{}
	for _, v := range p.Variants {
		bySKU[v.SKU] = v
	}
	small, ok := bySKU["MER-S"]
	if !ok {
		t.Fatalf("MER-S did not arrive; got %v", bySKU)
	}
	if small.Price.AmountMinor != 8900 {
		t.Errorf("price = %d, want 8900", small.Price.AmountMinor)
	}
	if small.StockOnHand != 3 {
		t.Errorf("stock = %d, want 3", small.StockOnHand)
	}
	if small.Barcode != "5060001" {
		t.Errorf("barcode = %q", small.Barcode)
	}
	medium := bySKU["MER-M"]
	if medium.CompareAtPrice == nil || medium.CompareAtPrice.AmountMinor != 11000 {
		t.Errorf("compare-at = %v, want 11000", medium.CompareAtPrice)
	}

	// The pictures are linked, in the product's own order.
	media, err := app.MediaLibrary().ForProduct(ctx, p.ID)
	if err != nil {
		t.Fatalf("media: %v", err)
	}
	if len(media) != 2 {
		t.Fatalf("pictures = %d, want 2", len(media))
	}
	if media[0].URL != "https://cdn.example/merino-1.jpg" {
		t.Errorf("first picture = %q, want the one Shopify listed first", media[0].URL)
	}
}

// The property that makes an import re-runnable: a second pass updates the rows
// the first one made rather than putting a second copy beside them.
func TestRunningItTwiceUpdatesRatherThanDuplicates(t *testing.T) {
	products := testProducts()
	srv := stubShop(t, products)
	app, m := newImportApp(t, srv)
	ctx := context.Background()

	if job := run(t, app, m, srv); job.Created != 1 {
		t.Fatalf("first run created %d, want 1", job.Created)
	}

	// The merchant renames it in Shopify, which changes its handle. Matching on
	// the handle would import it again under the new name; matching on the id
	// is what makes this an update.
	products[0].Title = "Merino crew neck (new photo)"
	products[0].Handle = "merino-crew-neck-2024"
	srv2 := stubShop(t, products)

	job := run(t, app, m, srv2)
	if job.Created != 0 || job.Updated != 1 {
		t.Errorf("second run created %d and updated %d — want 0 and 1", job.Created, job.Updated)
	}

	list, total, err := app.Products().ListProducts(ctx, gocommerce.ProductQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Errorf("the store holds %d products after two imports, want 1", total)
	}
	if len(list) == 1 && list[0].Title != "Merino crew neck (new photo)" {
		t.Errorf("title = %q, want the renamed one", list[0].Title)
	}
}

// A product that cannot be mapped is one warning and a count, not a dead
// import: a catalogue of nine hundred must not be stopped by one bad row.
func TestOneBadProductDoesNotStopTheImport(t *testing.T) {
	products := append(testProducts(), Product{
		// No title and no variants: unmappable.
		ID: 8002, Handle: "broken",
	})
	srv := stubShop(t, products)
	app, m := newImportApp(t, srv)

	job := run(t, app, m, srv)
	if job.Status != StatusDone {
		t.Fatalf("status = %q, want done — one bad row must not fail the job", job.Status)
	}
	if job.Created != 1 || job.Failed != 1 {
		t.Errorf("created %d, failed %d — want 1 and 1", job.Created, job.Failed)
	}
	if len(job.Warnings) == 0 {
		t.Error("the failure left no warning, so nobody can find out which product it was")
	}
	if !strings.Contains(strings.Join(job.Warnings, " "), "broken") {
		t.Errorf("the warning does not name the product: %v", job.Warnings)
	}
	_ = app
}

// A refused token stops the walk and says so, rather than reporting an empty
// success.
func TestARefusedTokenFailsTheJob(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	app, m := newImportApp(t, srv)
	ctx := context.Background()

	client, err := m.settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	client.HTTP = &http.Client{Transport: rewriteToTest(srv.URL)}

	if _, err := client.Verify(ctx); err == nil {
		t.Fatal("a refused token verified")
	} else if !strings.Contains(err.Error(), "refused") {
		t.Errorf("error = %v, want it to say the token was refused", err)
	}

	var id int64
	if err := app.DB().QueryRowContext(ctx, `
		INSERT INTO import_shopify_jobs (shop, status, total) VALUES ($1, $2, $3) RETURNING id`,
		"acme.myshopify.com", StatusRunning, 1).Scan(&id); err != nil {
		t.Fatal(err)
	}
	m.run(ctx, id, client)

	job, err := m.job(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != StatusFailed {
		t.Errorf("status = %q, want failed", job.Status)
	}
	if job.Message == "" {
		t.Error("a failed job with no message leaves nothing to act on")
	}
}

// The progress bar has to be honest while it runs, not only at the end.
func TestProgressIsWrittenAsItGoes(t *testing.T) {
	many := []Product{}
	for i := 0; i < 5; i++ {
		p := testProducts()[0]
		p.ID = int64(9100 + i)
		p.Handle = fmt.Sprintf("thing-%d", i)
		p.Title = fmt.Sprintf("Thing %d", i)
		for j := range p.Variants {
			p.Variants[j].ID = int64(9200 + i*10 + j)
			p.Variants[j].SKU = fmt.Sprintf("THING-%d-%d", i, j)
		}
		many = append(many, p)
	}
	srv := stubShop(t, many)
	app, m := newImportApp(t, srv)

	job := run(t, app, m, srv)
	if job.Done != 5 || job.Total != 5 {
		t.Errorf("done %d of %d, want 5 of 5", job.Done, job.Total)
	}
	if job.Percent != 100 {
		t.Errorf("percent = %d", job.Percent)
	}
	if job.FinishedAt == nil || job.FinishedAt.Before(job.StartedAt) {
		t.Errorf("finished_at = %v, started_at = %v", job.FinishedAt, job.StartedAt)
	}
	if time.Since(job.StartedAt) > time.Minute {
		t.Errorf("started_at looks wrong: %v", job.StartedAt)
	}
	_ = app
}
