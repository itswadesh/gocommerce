package gocommerce

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// Every keyed import takes four modes: upsert (the default), insert only,
// update only, and delete. Each one has to hold under a dry run, which writes
// nothing and reports what Import would do.

func productEventCount(t *testing.T, app *App, name string) int {
	t.Helper()
	var n int
	if err := app.DB().QueryRowContext(context.Background(),
		`SELECT count(*) FROM outbox_events WHERE event_name = $1`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestUpdateOnlyChangesWhatExistsAndReportsTheRest(t *testing.T) {
	app, _ := mediaApp(t)
	ctx := context.Background()
	importShopifyFixture(t, app)

	file := "product_slug,sku,product_title\ncrew-tee,TEE-BLK-S,Big tee\nghost,GHOST-1,Ghost\n"
	result, err := app.Data().ImportProducts(ctx, strings.NewReader(file), ImportOptions{Mode: ImportUpdate})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Created != 0 || result.Updated != 1 || result.Skipped != 1 || len(result.Errors) != 1 ||
		result.Errors[0].Line != 3 || !strings.Contains(result.Errors[0].Message, `"ghost"`) {
		t.Errorf("result = %+v, want the tee updated and ghost reported on line 3", result)
	}
	if tee, _ := app.Products().GetProductBySlug(ctx, "crew-tee"); tee.Title != "Big tee" {
		t.Errorf("tee title = %q, want Big tee", tee.Title)
	}
	if _, err := app.Products().GetProductBySlug(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ghost lookup = %v, want it never created", err)
	}
}

func TestDeleteModeDeletesTheProductsAFileNames(t *testing.T) {
	app, _ := mediaApp(t)
	ctx := context.Background()
	importShopifyFixture(t, app)
	events := productEventCount(t, app, EventProductDeleted)

	// The Shopify export as it is: four crew-tee rows are one product.
	file := shopifyProductsFixture + "ghost,,,,,,,,,,,,,,,,,,,,,,,,,,,,,,,,,,,\n"
	dry, err := app.Data().ImportProducts(ctx, strings.NewReader(file), ImportOptions{Mode: ImportDelete, DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dry.Deleted != 2 || dry.Skipped != 1 || len(dry.Errors) != 1 || dry.Errors[0].Line != 7 {
		t.Errorf("dry run = %+v, want two would-be deletes and ghost reported on line 7", dry)
	}
	if _, err := app.Products().GetProductBySlug(ctx, "mug"); err != nil {
		t.Errorf("a dry run deleted mug: %v", err)
	}
	if got := productEventCount(t, app, EventProductDeleted); got != events {
		t.Errorf("a dry run left %d product.deleted events", got-events)
	}

	// The store's own layout needs only the key to delete by, and a slug
	// named twice is counted once, dry run or not.
	twice := "product_slug\ncrew-tee\nmug\ncrew-tee\n"
	dry, err = app.Data().ImportProducts(ctx, strings.NewReader(twice), ImportOptions{Mode: ImportDelete, DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dry.Deleted != 2 || len(dry.Errors) != 1 || dry.Errors[0].Line != 4 {
		t.Errorf("dry run of a repeated slug = %+v, want 2 and line 4 reported", dry)
	}
	result, err := app.Data().ImportProducts(ctx, strings.NewReader(twice), ImportOptions{Mode: ImportDelete})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if result.Deleted != 2 || result.Created != 0 || result.Updated != 0 || len(result.Errors) != 1 {
		t.Errorf("result = %+v, want both products deleted", result)
	}
	for _, slug := range []string{"crew-tee", "mug"} {
		if _, err := app.Products().GetProductBySlug(ctx, slug); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s lookup = %v, want it gone", slug, err)
		}
	}
	// Each delete told the outbox, in the transaction that made it.
	if got := productEventCount(t, app, EventProductDeleted); got != events+2 {
		t.Errorf("product.deleted events = %d, want %d", got-events, 2)
	}
}

func TestCategoryModesInsertUpdateAndDelete(t *testing.T) {
	app := categoriesApp(t)
	ctx := context.Background()
	_, _, shirts, footwear := tree(t, app)

	update := "path,position\nApparel / Clothing / Shirts,4\nApparel / Hats,1\n"
	result, err := app.Data().ImportCategories(ctx, strings.NewReader(update), ImportOptions{Mode: ImportUpdate})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if result.Updated != 1 || result.Created != 0 || len(result.Errors) != 1 || result.Errors[0].Line != 3 {
		t.Errorf("update = %+v, want Shirts updated and Hats reported", result)
	}

	insert, err := app.Data().ImportCategories(ctx, strings.NewReader(update), ImportOptions{Mode: ImportInsert})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if insert.Created != 1 || insert.Updated != 0 || insert.Skipped != 1 {
		t.Errorf("insert = %+v, want Hats built and Shirts left alone", insert)
	}

	// A product filed under Shirts keeps Shirts, and with it Clothing: Go
	// refuses those. A tax rate naming Footwear keeps Footwear: the database
	// refuses that one, and it still costs only its own row. Socks, listed
	// after its parent, is still deleted first.
	price, stock := int64(1500), 1
	if _, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Shirt", Status: ProductActive, SKU: "MODE-1", PriceMinor: &price, Stock: &stock, CategoryID: &shirts.ID,
	}); err != nil {
		t.Fatal(err)
	}
	newCategory(t, app, CategoryInput{Title: "Socks", ParentID: &footwear.ID})
	if _, err := app.Taxes().Create(ctx, TaxRateInput{Name: "Shoe tax", RateBP: 500, CategoryID: &footwear.ID}); err != nil {
		t.Fatal(err)
	}
	doomed := "path\nApparel / Hats\nApparel / Clothing\nApparel / Clothing / Shirts\nApparel / Footwear\nApparel / Footwear / Socks\nNowhere\n"
	dry, err := app.Data().ImportCategories(ctx, strings.NewReader(doomed), ImportOptions{Mode: ImportDelete, DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dry.Deleted != 2 || dry.Skipped != 4 || len(dry.Errors) != 4 {
		t.Errorf("dry run = %+v, want Hats and Socks deletable, four rows refused", dry)
	}
	for _, e := range dry.Errors {
		if e.Line == 5 && !strings.Contains(e.Message, "tax rate") {
			t.Errorf("Footwear refused with %q, want the tax rate named", e.Message)
		}
	}
	result, err = app.Data().ImportCategories(ctx, strings.NewReader(doomed), ImportOptions{Mode: ImportDelete})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if result.Deleted != dry.Deleted {
		t.Errorf("delete = %+v, want what the dry run said", result)
	}
	list, err := app.Categories().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range list {
		names = append(names, c.FullName)
	}
	if strings.Join(names, ",") != "Apparel,Apparel / Clothing,Apparel / Clothing / Shirts,Apparel / Footwear" {
		t.Errorf("tree = %v, want Hats and Socks gone and the ones in use kept", names)
	}
}

func TestAnImportRefusesAModeItDoesNotTake(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	_, err := app.Data().ImportOrders(ctx, strings.NewReader("number,email,sku,quantity,unit_price_minor\n"), ImportOptions{Mode: ImportDelete})
	if !errors.Is(err, ErrValidation) {
		t.Errorf("orders with delete = %v, want a validation error", err)
	}
	_, err = app.Data().ImportInventory(ctx, strings.NewReader("sku,location,on_hand\n"), ImportOptions{Mode: ImportInsert})
	if !errors.Is(err, ErrValidation) {
		t.Errorf("inventory with insert = %v, want a validation error", err)
	}
	_, err = app.Data().ImportProducts(ctx, strings.NewReader("product_slug,sku\n"), ImportOptions{Mode: "replace"})
	if !errors.Is(err, ErrValidation) {
		t.Errorf("products with replace = %v, want a validation error", err)
	}
}

func TestTheModeRouteParamAndItsOverwriteAlias(t *testing.T) {
	app, _ := mediaApp(t)
	importShopifyFixture(t, app)
	renamed := strings.Replace(shopifyProductsFixture, "crew-tee,Crew Tee,", "crew-tee,Renamed Tee,", 1)

	for _, tc := range []struct {
		query   string
		updated int
	}{
		{"?overwrite=0", 0},
		{"?mode=insert", 0},
		{"?mode=update&dry_run=1", 2},
		{"?mode=update&overwrite=0&dry_run=1", 2},
		{"", 2},
	} {
		rec := do(t, app, http.MethodPost, "/api/admin/import/products"+tc.query, withAdmin, textBody(renamed))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", tc.query, rec.Code, rec.Body)
		}
		var body struct {
			Data ImportResult `json:"data"`
		}
		decodeJSONBody(t, rec.Body.Bytes(), &body)
		if body.Data.Updated != tc.updated || body.Data.Created != 0 {
			t.Errorf("%s = %+v, want %d updated", tc.query, body.Data, tc.updated)
		}
	}
	if rec := do(t, app, http.MethodPost, "/api/admin/import/orders?mode=delete", withAdmin, textBody("number\n")); rec.Code != http.StatusBadRequest {
		t.Errorf("orders with mode=delete = %d, want 400", rec.Code)
	}
}
