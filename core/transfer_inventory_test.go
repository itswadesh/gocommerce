package gocommerce

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
)

// The inventory file is the stock-take's shape: one row per variant per
// location, in the store's own columns or Shopify's, and it imports back.

func stockAtCode(t *testing.T, app *App, variantID int64, code string) VariantStock {
	t.Helper()
	levels, err := app.Stock().ByLocation(context.Background(), variantID)
	if err != nil {
		t.Fatalf("by location: %v", err)
	}
	for _, l := range levels {
		if l.LocationCode == code {
			return l
		}
	}
	t.Fatalf("no stock row at %s: %+v", code, levels)
	return VariantStock{}
}

func TestInventoryFilesInBothDialects(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	p := simpleProduct(t, app, "INV-1", 1000, 5)
	v := p.DefaultVariant()
	shop := newLocation(t, app, "shop", "The Shop", 1)
	if _, err := app.Stock().SetOnHand(ctx, v.ID, shop.ID, 3, "stocked the shop"); err != nil {
		t.Fatalf("set on hand: %v", err)
	}

	var buf bytes.Buffer
	if err := app.Data().ExportInventory(ctx, &buf, ExportOptions{}); err != nil {
		t.Fatalf("export: %v", err)
	}
	header, rows := readCSV(t, buf.String())
	if strings.Join(header, ",") != strings.Join(inventoryCSVHeader, ",") {
		t.Errorf("native header = %v", header)
	}
	byLocation := map[string][]string{}
	for _, r := range rows {
		if cell(t, header, r, "sku") == "INV-1" {
			byLocation[cell(t, header, r, "location")] = r
		}
	}
	if len(byLocation) != 2 || cell(t, header, byLocation["shop"], "on_hand") != "3" || cell(t, header, byLocation["shop"], "available") != "3" {
		t.Errorf("native rows for INV-1 = %v, want the default location and the shop", byLocation)
	}

	buf.Reset()
	if err := app.Data().ExportInventory(ctx, &buf, ExportOptions{Format: FormatShopify}); err != nil {
		t.Fatalf("export shopify: %v", err)
	}
	header, rows = readCSV(t, buf.String())
	if strings.Join(header, ",") != strings.Join(shopifyInventoryHeader, ",") {
		t.Errorf("shopify header = %v", header)
	}
	var shopRow []string
	for _, r := range rows {
		if cell(t, header, r, "SKU") == "INV-1" && cell(t, header, r, "Location") == "The Shop" {
			shopRow = r
		}
	}
	if shopRow == nil {
		t.Fatalf("no row for INV-1 at The Shop:\n%s", buf.String())
	}
	for name, want := range map[string]string{
		"Handle": p.Slug, "Title": p.Title, "Option1 Name": "Title", "Option1 Value": "Default Title",
		"Committed": "0", "Available": "3", "On hand": "3", "Incoming": "0", "Unavailable": "0",
	} {
		if got := cell(t, header, shopRow, name); got != want {
			t.Errorf("shopify %s = %q, want %q", name, got, want)
		}
	}

	// Shopify's file back, with a count changed: the location is named the
	// way the file names it.
	edited := strings.Join(shopifyInventoryHeader, ",") + "\n" +
		p.Slug + "," + p.Title + ",Title,Default Title,,,,,INV-1,,,The Shop,0,0,0,9,9\n"
	result, err := app.Data().ImportInventory(ctx, strings.NewReader(edited), ImportOptions{})
	if err != nil {
		t.Fatalf("import shopify: %v", err)
	}
	if result.Updated != 1 || len(result.Errors) != 0 || result.Format != FormatShopify {
		t.Errorf("result = %+v", result)
	}
	if got := stockAtCode(t, app, v.ID, "shop"); got.OnHand != 9 {
		t.Errorf("shop on hand = %d, want 9", got.OnHand)
	}

	// The store's own file: code or blank for the default; Available alone is
	// read against what is reserved; a row that names nothing is refused.
	native := "sku,location,on_hand,available\nINV-1,shop,2,\nINV-1,,,7\nNOPE,shop,1,\nINV-1,attic,1,\nINV-1,shop,,\n"
	result, err = app.Data().ImportInventory(ctx, strings.NewReader(native), ImportOptions{})
	if err != nil {
		t.Fatalf("import native: %v", err)
	}
	if result.Updated != 2 || len(result.Errors) != 3 {
		t.Errorf("result = %+v, want two rows applied and three refused", result)
	}
	if got := stockAtCode(t, app, v.ID, "shop"); got.OnHand != 2 {
		t.Errorf("shop on hand = %d, want 2", got.OnHand)
	}
	if got := stockAtCode(t, app, v.ID, "default"); got.OnHand != 7 {
		t.Errorf("default on hand = %d, want 7 from Available", got.OnHand)
	}

	// A rehearsal writes nothing.
	dry, err := app.Data().ImportInventory(ctx, strings.NewReader("sku,location,on_hand\nINV-1,shop,50\n"), ImportOptions{DryRun: true})
	if err != nil || dry.Updated != 1 || !dry.DryRun {
		t.Fatalf("dry run = %+v, %v", dry, err)
	}
	if got := stockAtCode(t, app, v.ID, "shop"); got.OnHand != 2 {
		t.Errorf("a dry run changed the shelf: %d", got.OnHand)
	}
}

func TestInventoryRoutes(t *testing.T) {
	app := newTestApp(t)
	simpleProduct(t, app, "INV-R", 500, 2)

	out := do(t, app, http.MethodGet, "/api/admin/export/admin-inventory?format=shopify", withAdmin)
	if out.Code != http.StatusOK || !strings.HasPrefix(out.Body.String(), "Handle,Title,") {
		t.Errorf("export = %d: %s", out.Code, out.Body)
	}
	rec := do(t, app, http.MethodPost, "/api/admin/import/inventory", withAdmin, textBody("sku,location,on_hand\nINV-R,,11\n"))
	if rec.Code != http.StatusOK {
		t.Fatalf("import = %d: %s", rec.Code, rec.Body)
	}
	v, _ := app.Products().GetVariantBySKU(context.Background(), "INV-R")
	if v.StockOnHand != 11 {
		t.Errorf("on hand = %d after the route, want 11", v.StockOnHand)
	}
	if denied := do(t, app, http.MethodPost, "/api/admin/import/inventory", textBody("sku\n")); denied.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", denied.Code)
	}
}
