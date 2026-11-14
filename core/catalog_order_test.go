package gocommerce

import (
	"context"
	"testing"
)

// Drag to reorder and duplicate (D272).

func listedSKUs(t *testing.T, app *App) []string {
	t.Helper()
	list, _, err := app.Products().ListProducts(context.Background(), ProductQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range list {
		out = append(out, p.Variants[0].SKU)
	}
	return out
}

func TestReorderingProductsSwapsOnlyTheRowsNamed(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	a := simpleProduct(t, app, "ORD-A", 100, 1)
	b := simpleProduct(t, app, "ORD-B", 100, 1)
	c := simpleProduct(t, app, "ORD-C", 100, 1)
	if got := listedSKUs(t, app); got[0] != "ORD-C" || got[2] != "ORD-A" {
		t.Fatalf("before any reorder %v, want newest first", got)
	}
	// Swap A and C: B keeps its place in the middle.
	if err := app.Products().ReorderProducts(ctx, []int64{a.ID, c.ID}); err != nil {
		t.Fatal(err)
	}
	if got := listedSKUs(t, app); got[0] != "ORD-A" || got[1] != "ORD-B" || got[2] != "ORD-C" {
		t.Fatalf("after swapping A and C %v", got)
	}
	// A product added later leads the list, as a new one always has.
	simpleProduct(t, app, "ORD-D", 100, 1)
	if got := listedSKUs(t, app); got[0] != "ORD-D" || got[1] != "ORD-A" {
		t.Fatalf("after adding D %v", got)
	}
	if err := app.Products().ReorderProducts(ctx, []int64{b.ID, b.ID}); err == nil {
		t.Fatal("a product named twice was accepted")
	}
	if err := app.Products().ReorderProducts(ctx, []int64{b.ID, 999999}); err == nil {
		t.Fatal("an unknown product was accepted")
	}
}

func TestDuplicatingAProductMakesADraftCopyWithItsOwnSKU(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	src := simpleProduct(t, app, "DUP-CAKE", 45000, 5)
	dup, err := app.Products().DuplicateProduct(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dup.Status != ProductDraft || dup.Title != src.Title+" (copy)" || dup.Slug == src.Slug {
		t.Fatalf("copy %q %q %q", dup.Status, dup.Title, dup.Slug)
	}
	v := dup.Variants[0]
	if v.SKU == "" || v.SKU == "DUP-CAKE" || v.Price != src.Variants[0].Price || v.StockOnHand != 0 {
		t.Fatalf("copied variant %+v", v)
	}
	again, err := app.Products().DuplicateProduct(ctx, src.ID)
	if err != nil {
		t.Fatalf("a second copy: %v", err)
	}
	if again.Slug == dup.Slug || again.Variants[0].SKU == v.SKU {
		t.Fatalf("the second copy shares %q / %q", again.Slug, again.Variants[0].SKU)
	}
}
