package meilisearch

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	metalpricing "github.com/itswadesh/gocommerce/ext/metal-pricing"
	"github.com/itswadesh/gocommerce/gctest"
)

// Every way a variant's price reaches the catalogue must reach a search card on
// the next incremental sync, and a rate save must cost Meilisearch nothing until
// the worker sends its one batch.
func TestEveryVariantPriceChangeReachesTheNextSync(t *testing.T) {
	fake := newFake()
	server := gctest.StubHTTP(t, fake.handler)
	m := New(Config{Poll: -1})
	app := gctest.NewWithConfig(t, gocommerce.Config{AllowPrivateURLs: true}, m, metalpricing.New(metalpricing.Config{}))
	enable(t, app, server.URL)
	ctx := context.Background()

	n := 0
	sized := func(t *testing.T) *gocommerce.Product {
		t.Helper()
		n++
		sku := func(size string) string { return fmt.Sprintf("FRESH-%d-%s", n, size) }
		p, err := app.Products().CreateProduct(ctx, gocommerce.ProductInput{
			Title: fmt.Sprintf("Fresh %d", n), Status: "active",
			Options: []gocommerce.OptionInput{{Name: "Size", Values: []string{"S", "M", "L"}}},
			Variants: []gocommerce.VariantInput{
				{SKU: sku("S"), PriceMinor: 1000, Options: []string{"S"}},
				{SKU: sku("M"), PriceMinor: 2000, Options: []string{"M"}},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	baseline := func(t *testing.T) {
		t.Helper()
		if _, _, err := m.Sync(ctx, true); err != nil {
			t.Fatal(err)
		}
	}
	// next is the incremental sync under test: how many documents it sent, and
	// how many calls it took to send them.
	next := func(t *testing.T) (docs, calls int) {
		t.Helper()
		before := len(fake.keys)
		docs, _, err := m.Sync(ctx, false)
		if err != nil {
			t.Fatal(err)
		}
		return docs, len(fake.keys) - before
	}
	priced := func(t *testing.T, p *gocommerce.Product, low, high int64) {
		t.Helper()
		doc := fake.docs[jsonID(float64(p.ID))]
		if doc["price_minor"] != float64(low) || doc["price_max_minor"] != float64(high) {
			t.Errorf("card shows %v to %v, want %d to %d", doc["price_minor"], doc["price_max_minor"], low, high)
		}
	}

	t.Run("edited", func(t *testing.T) {
		p := sized(t)
		baseline(t)
		cheaper := int64(500)
		if _, err := app.Products().UpdateVariant(ctx, p.Variants[0].ID, gocommerce.VariantPatch{PriceMinor: &cheaper}); err != nil {
			t.Fatal(err)
		}
		if docs, _ := next(t); docs != 1 {
			t.Errorf("sent %d documents, want the one changed product", docs)
		}
		priced(t, p, 500, 2000)
	})

	t.Run("repriced by a metal rate", func(t *testing.T) {
		a, b := sized(t), sized(t)
		rec := gctest.AdminRequest(t, app, http.MethodPut, "/api/admin/x/metal-pricing/rates/gold-22k",
			map[string]any{"name": "Gold 22K", "rate_minor": 6_000_00})
		if rec.Code != http.StatusOK {
			t.Fatalf("rate = %d %s", rec.Code, rec.Body)
		}
		for _, v := range []gocommerce.Variant{a.Variants[0], a.Variants[1], b.Variants[0]} {
			rec = gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/metal-pricing/recipes", map[string]any{
				"sku": v.SKU, "rate_code": "gold-22k", "weight_mg": 10_000, "making_kind": "flat", "making_value": 0})
			if rec.Code != http.StatusOK {
				t.Fatalf("recipe = %d %s", rec.Code, rec.Body)
			}
		}
		baseline(t)
		before := len(fake.keys)
		rec = gctest.AdminRequest(t, app, http.MethodPut, "/api/admin/x/metal-pricing/rates/gold-22k",
			map[string]any{"name": "Gold 22K", "rate_minor": 7_000_00})
		if rec.Code != http.StatusOK {
			t.Fatalf("rate = %d %s", rec.Code, rec.Body)
		}
		if got := len(fake.keys) - before; got != 0 {
			t.Errorf("saving a rate called Meilisearch %d times, want none", got)
		}
		if docs, calls := next(t); docs != 2 || calls != 1 {
			t.Errorf("sent %d documents in %d calls, want 2 in one batch", docs, calls)
		}
		priced(t, a, 70_000_00, 70_000_00)
		priced(t, b, 2000, 70_000_00)
	})

	t.Run("imported", func(t *testing.T) {
		p := sized(t)
		baseline(t)
		file := "product_slug,sku,price_minor\n" + p.Slug + "," + p.Variants[1].SKU + ",9000\n"
		if res, err := app.Data().ImportProducts(ctx, strings.NewReader(file), gocommerce.ImportOptions{}); err != nil || len(res.Errors) != 0 {
			t.Fatalf("import = %+v, %v", res, err)
		}
		next(t)
		priced(t, p, 1000, 9000)
	})

	t.Run("moved to another product by an import", func(t *testing.T) {
		from, to := sized(t), gctest.CreateProduct(t, app, fmt.Sprintf("FRESH-TO-%d", n), 5000, 1)
		baseline(t)
		file := "product_slug,sku,price_minor\n" + to.Slug + "," + from.Variants[0].SKU + ",1000\n"
		if res, err := app.Data().ImportProducts(ctx, strings.NewReader(file), gocommerce.ImportOptions{}); err != nil || len(res.Errors) != 0 {
			t.Fatalf("import = %+v, %v", res, err)
		}
		next(t)
		priced(t, from, 2000, 2000)
		priced(t, to, 1000, 5000)
	})

	t.Run("cheapest variant deleted", func(t *testing.T) {
		p := sized(t)
		baseline(t)
		if err := app.Products().DeleteVariant(ctx, p.Variants[0].ID); err != nil {
			t.Fatal(err)
		}
		next(t)
		priced(t, p, 2000, 2000)
	})

	t.Run("every variant gone with its option", func(t *testing.T) {
		p := sized(t)
		baseline(t)
		if _, _, err := app.Products().SetOptions(ctx, p.ID, gocommerce.OptionSet{
			Options: []gocommerce.OptionSpec{{ID: &p.Options[0].ID, Name: "Size", Values: gocommerce.OptionValues("XL")}},
		}); err != nil {
			t.Fatal(err)
		}
		next(t)
		priced(t, p, 0, 0)
	})

	t.Run("a new dearer variant", func(t *testing.T) {
		p := sized(t)
		baseline(t)
		if _, err := app.Products().CreateVariant(ctx, p.ID, gocommerce.VariantInput{
			SKU: fmt.Sprintf("FRESH-%d-L", n), PriceMinor: 4000, Options: []string{"L"},
		}); err != nil {
			t.Fatal(err)
		}
		next(t)
		priced(t, p, 1000, 4000)
	})

	t.Run("an untouched product is not sent again", func(t *testing.T) {
		sized(t)
		baseline(t)
		if docs, calls := next(t); docs != 0 || calls != 0 {
			t.Errorf("sent %d documents in %d calls with nothing changed", docs, calls)
		}
	})
}
