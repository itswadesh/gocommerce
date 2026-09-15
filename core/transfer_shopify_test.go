package gocommerce

import (
	"bytes"
	"context"
	"encoding/csv"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The store's CSV has two dialects of one model: its own, and Shopify's, so a
// file exported from Shopify imports here unchanged and a file exported here
// in Shopify's layout imports there. These tests are the product half.

// A Shopify product export, as Shopify writes it: product fields on the first
// row of a handle, option names on that row only, one row per variant, and a
// trailing row that carries nothing but a picture.
const shopifyProductsFixture = `Handle,Title,Body (HTML),Vendor,Product Category,Type,Tags,Published,Option1 Name,Option1 Value,Option2 Name,Option2 Value,Option3 Name,Option3 Value,Variant SKU,Variant Grams,Variant Inventory Tracker,Variant Inventory Qty,Variant Inventory Policy,Variant Fulfillment Service,Variant Price,Variant Compare At Price,Variant Requires Shipping,Variant Taxable,Variant Barcode,Image Src,Image Position,Image Alt Text,Gift Card,SEO Title,SEO Description,Variant Image,Variant Weight Unit,Variant Tax Code,Cost per item,Status
crew-tee,Crew Tee,"<p>Soft.</p>",Acme,Apparel & Accessories > Clothing,Shirts,"summer, basics",TRUE,Color,Black,Size,S,,,TEE-BLK-S,180,shopify,4,deny,manual,19.99,24.99,TRUE,TRUE,111,https://cdn.example/tee-1.jpg,1,Front,FALSE,Crew Tee | Acme,A soft tee.,https://cdn.example/tee-1.jpg,g,,8.50,active
crew-tee,,,,,,,,,Black,,M,,,TEE-BLK-M,180,shopify,0,continue,manual,19.99,,TRUE,TRUE,,https://cdn.example/tee-2.jpg,2,Back,,,,https://cdn.example/tee-1.jpg,g,,,
crew-tee,,,,,,,,,White,,S,,,TEE-WHT-S,180,,,deny,manual,21.00,,TRUE,FALSE,,https://cdn.example/tee-3.jpg,3,,,,,https://cdn.example/tee-3.jpg,g,,,
crew-tee,,,,,,,,,,,,,,,,,,,,,,,,,https://cdn.example/tee-4.jpg,4,Detail,,,,,,,,
mug,Mug,,,,,,FALSE,Title,Default Title,,,,,,300,shopify,12,deny,manual,9.00,,TRUE,TRUE,,,,,,,,,g,,,draft
`

func readCSV(t *testing.T, raw string) (header []string, rows [][]string) {
	t.Helper()
	records, err := csv.NewReader(strings.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v\n%s", err, raw)
	}
	if len(records) == 0 {
		t.Fatalf("empty csv")
	}
	return records[0], records[1:]
}

// cell reads one column of a parsed row by header name.
func cell(t *testing.T, header, row []string, name string) string {
	t.Helper()
	for i, h := range header {
		if h == name {
			if i < len(row) {
				return row[i]
			}
			return ""
		}
	}
	t.Fatalf("no column %q in %v", name, header)
	return ""
}

func importShopifyFixture(t *testing.T, app *App) *ImportResult {
	t.Helper()
	ctx := context.Background()
	// The category the file names, so it can be matched by its full path.
	root, err := app.Categories().Create(ctx, CategoryInput{Title: "Apparel & Accessories"})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	if _, err := app.Categories().Create(ctx, CategoryInput{Title: "Clothing", ParentID: &root.ID}); err != nil {
		t.Fatalf("create child: %v", err)
	}
	result, err := app.Data().ImportProducts(ctx, strings.NewReader(shopifyProductsFixture), ImportOptions{})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	return result
}

func TestAShopifyProductFileImports(t *testing.T) {
	app, _ := mediaApp(t)
	ctx := context.Background()
	result := importShopifyFixture(t, app)
	if result.Created != 2 || len(result.Errors) != 0 {
		t.Fatalf("result = %+v, want two products created and no row errors", result)
	}

	tee, err := app.Products().GetProductBySlug(ctx, "crew-tee")
	if err != nil {
		t.Fatalf("crew-tee: %v", err)
	}
	if tee.Title != "Crew Tee" || tee.Description != "<p>Soft.</p>" || tee.Vendor != "Acme" || tee.ProductType != "Shirts" {
		t.Errorf("product = %q / %q / %q / %q", tee.Title, tee.Description, tee.Vendor, tee.ProductType)
	}
	// Tags come back in the store's own order — folded, sorted — as they do
	// from every other write.
	if strings.Join(tee.Tags, ",") != "basics,summer" || tee.Status != ProductActive {
		t.Errorf("tags %v status %q, want basics,summer and active", tee.Tags, tee.Status)
	}
	if tee.SEOTitle != "Crew Tee | Acme" || tee.SEODescription != "A soft tee." {
		t.Errorf("seo = %q / %q", tee.SEOTitle, tee.SEODescription)
	}
	if tee.Category == nil || tee.Category.FullName != "Apparel & Accessories / Clothing" {
		t.Errorf("category = %+v, want Clothing matched by its full path", tee.Category)
	}
	if len(tee.Options) != 2 || tee.Options[0].Name != "Color" || tee.Options[1].Name != "Size" ||
		len(tee.Options[0].Values) != 2 || tee.Options[0].Values[1].Value != "White" {
		t.Errorf("options = %+v, want Color (Black, White) and Size (S, M)", tee.Options)
	}
	if len(tee.Variants) != 3 {
		t.Fatalf("variants = %d, want 3", len(tee.Variants))
	}
	bySKU := map[string]Variant{}
	for _, v := range tee.Variants {
		bySKU[v.SKU] = v
	}
	blkS := bySKU["TEE-BLK-S"]
	if blkS.Price.AmountMinor != 1999 || blkS.CompareAtPrice == nil || blkS.CompareAtPrice.AmountMinor != 2499 ||
		blkS.Cost == nil || blkS.Cost.AmountMinor != 850 {
		t.Errorf("TEE-BLK-S money = %+v / %+v / %+v, want 19.99, 24.99 and 8.50 in minor units", blkS.Price, blkS.CompareAtPrice, blkS.Cost)
	}
	if !blkS.Taxable || blkS.Barcode != "111" || blkS.WeightGrams == nil || *blkS.WeightGrams != 180 || blkS.WeightUnit != "g" {
		t.Errorf("TEE-BLK-S = taxable %v barcode %q weight %v %s", blkS.Taxable, blkS.Barcode, blkS.WeightGrams, blkS.WeightUnit)
	}
	if !blkS.TrackInventory || blkS.ContinueSelling || blkS.StockOnHand != 4 {
		t.Errorf("TEE-BLK-S stock = track %v continue %v on hand %d, want tracked, deny, 4", blkS.TrackInventory, blkS.ContinueSelling, blkS.StockOnHand)
	}
	if got := strings.Join(blkS.Options, "/"); got != "Black/S" {
		t.Errorf("TEE-BLK-S options = %q", got)
	}
	if blkM := bySKU["TEE-BLK-M"]; !blkM.ContinueSelling || blkM.StockOnHand != 0 {
		t.Errorf("TEE-BLK-M = %+v, want continue selling at zero", blkM)
	}
	if whtS := bySKU["TEE-WHT-S"]; whtS.TrackInventory || whtS.Taxable || whtS.Price.AmountMinor != 2100 {
		t.Errorf("TEE-WHT-S = tracked %v taxable %v price %d, want untracked, untaxed, 21.00", whtS.TrackInventory, whtS.Taxable, whtS.Price.AmountMinor)
	}

	// The pictures, in Image Position order, alt text and all; each variant
	// shows the one its row named, and the fourth row was a picture alone.
	media, err := app.MediaLibrary().ForProduct(ctx, tee.ID)
	if err != nil {
		t.Fatalf("media: %v", err)
	}
	var urls, alts []string
	for _, m := range media {
		urls = append(urls, m.URL)
		alts = append(alts, m.Alt)
	}
	if strings.Join(urls, " ") != "https://cdn.example/tee-1.jpg https://cdn.example/tee-2.jpg https://cdn.example/tee-3.jpg https://cdn.example/tee-4.jpg" {
		t.Errorf("pictures = %v", urls)
	}
	if strings.Join(alts, "|") != "Front|Back||Detail" {
		t.Errorf("alt texts = %v", alts)
	}
	for sku, want := range map[string]string{"TEE-BLK-S": "tee-1", "TEE-BLK-M": "tee-1", "TEE-WHT-S": "tee-3"} {
		if img := bySKU[sku].Image; img == nil || !strings.HasSuffix(img.URL, want+".jpg") {
			t.Errorf("%s shows %+v, want %s", sku, img, want)
		}
	}

	// "Title / Default Title" is Shopify for "no options"; a blank SKU gets the
	// handle, because a variant here must have one; Published FALSE and a
	// Status column agree on draft.
	mug, err := app.Products().GetProductBySlug(ctx, "mug")
	if err != nil {
		t.Fatalf("mug: %v", err)
	}
	if len(mug.Options) != 0 || len(mug.Variants) != 1 || mug.Variants[0].SKU != "mug" || mug.Status != ProductDraft {
		t.Errorf("mug = options %v variants %+v status %q", mug.Options, mug.Variants, mug.Status)
	}
	if mug.Variants[0].StockOnHand != 12 || mug.Variants[0].WeightGrams == nil || *mug.Variants[0].WeightGrams != 300 {
		t.Errorf("mug variant = %+v, want 12 on hand and 300 g", mug.Variants[0])
	}
}

func TestTheShopifyExportRoundTrips(t *testing.T) {
	app, _ := mediaApp(t)
	ctx := context.Background()
	importShopifyFixture(t, app)

	var buf bytes.Buffer
	if err := app.Data().ExportProducts(ctx, &buf, ProductQuery{}, ExportOptions{Format: FormatShopify}); err != nil {
		t.Fatalf("export: %v", err)
	}
	header, rows := readCSV(t, buf.String())
	if strings.Join(header, ",") != strings.Join(shopifyProductHeader, ",") {
		t.Errorf("header = %v", header)
	}
	if len(rows) != 5 {
		t.Fatalf("rows = %d, want three variants, one picture-only row and the mug:\n%s", len(rows), buf.String())
	}
	first := rows[0]
	for name, want := range map[string]string{
		"Handle": "crew-tee", "Title": "Crew Tee", "Body (HTML)": "<p>Soft.</p>", "Vendor": "Acme",
		"Product Category": "Apparel & Accessories > Clothing", "Type": "Shirts", "Tags": "basics, summer",
		"Published": "TRUE", "Option1 Name": "Color", "Option1 Value": "Black", "Option2 Name": "Size", "Option2 Value": "S",
		"Variant SKU": "TEE-BLK-S", "Variant Grams": "180", "Variant Inventory Tracker": "shopify", "Variant Inventory Qty": "4",
		"Variant Inventory Policy": "deny", "Variant Fulfillment Service": "manual", "Variant Price": "19.99",
		"Variant Compare At Price": "24.99", "Variant Requires Shipping": "TRUE", "Variant Taxable": "TRUE", "Variant Barcode": "111",
		"Image Src": "https://cdn.example/tee-1.jpg", "Image Position": "1", "Image Alt Text": "Front", "Gift Card": "FALSE",
		"SEO Title": "Crew Tee | Acme", "SEO Description": "A soft tee.", "Variant Image": "https://cdn.example/tee-1.jpg",
		"Variant Weight Unit": "g", "Cost per item": "8.50", "Status": "active",
	} {
		if got := cell(t, header, first, name); got != want {
			t.Errorf("first row %s = %q, want %q", name, got, want)
		}
	}
	// The second variant's row repeats nothing about the product but its handle
	// and its own option values, and carries the second picture.
	second := rows[1]
	if cell(t, header, second, "Title") != "" || cell(t, header, second, "Option1 Name") != "" ||
		cell(t, header, second, "Option1 Value") != "Black" || cell(t, header, second, "Option2 Value") != "M" ||
		cell(t, header, second, "Variant Inventory Policy") != "continue" || cell(t, header, second, "Image Position") != "2" {
		t.Errorf("second row = %v", second)
	}
	// Four pictures on three variants: the fourth rides on a row of its own.
	extra := rows[3]
	if cell(t, header, extra, "Handle") != "crew-tee" || cell(t, header, extra, "Variant SKU") != "" ||
		cell(t, header, extra, "Image Src") != "https://cdn.example/tee-4.jpg" || cell(t, header, extra, "Image Position") != "4" ||
		cell(t, header, extra, "Image Alt Text") != "Detail" {
		t.Errorf("picture-only row = %v", extra)
	}
	mug := rows[4]
	if cell(t, header, mug, "Option1 Name") != "Title" || cell(t, header, mug, "Option1 Value") != "Default Title" ||
		cell(t, header, mug, "Variant SKU") != "mug" || cell(t, header, mug, "Published") != "FALSE" || cell(t, header, mug, "Status") != "draft" {
		t.Errorf("mug row = %v", mug)
	}

	// And the export imports back: nothing new, everything found, no
	// complaints — the round trip is what makes the dialect trustworthy.
	again, err := app.Data().ImportProducts(ctx, strings.NewReader(buf.String()), ImportOptions{})
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if again.Created != 0 || again.Updated != 2 || len(again.Errors) != 0 {
		t.Errorf("re-import = %+v, want the two products updated and nothing else", again)
	}
	tee, _ := app.Products().GetProductBySlug(ctx, "crew-tee")
	media, _ := app.MediaLibrary().ForProduct(ctx, tee.ID)
	if len(tee.Variants) != 3 || len(media) != 4 {
		t.Errorf("after the round trip: %d variants, %d pictures", len(tee.Variants), len(media))
	}
}

// The export prefixes site-relative picture URLs with the store's own
// address, because a file that says "/media/abc.jpg" means nothing in
// another store's importer.
func TestTheShopifyExportWritesAbsolutePictureURLs(t *testing.T) {
	app, _ := mediaApp(t)
	ctx := context.Background()
	png := tinyPNG(t)
	item, err := app.MediaLibrary().Upload(ctx, "one.png", "image/png", bytes.NewReader(png), int64(len(png)))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	price := int64(500)
	p, err := app.Products().CreateProduct(ctx, ProductInput{Title: "Pin", SKU: "PIN", PriceMinor: &price})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := app.MediaLibrary().SetProductMedia(ctx, p.ID, []int64{item.ID}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	var buf bytes.Buffer
	if err := app.Data().ExportProducts(ctx, &buf, ProductQuery{}, ExportOptions{Format: FormatShopify, BaseURL: "https://shop.example"}); err != nil {
		t.Fatalf("export: %v", err)
	}
	header, rows := readCSV(t, buf.String())
	if got := cell(t, header, rows[0], "Image Src"); !strings.HasPrefix(got, "https://shop.example/media/") {
		t.Errorf("Image Src = %q, want it under https://shop.example/media/", got)
	}
}

func TestAnImportCanLeaveExistingProductsAlone(t *testing.T) {
	app, _ := mediaApp(t)
	ctx := context.Background()
	importShopifyFixture(t, app)

	renamed := strings.Replace(shopifyProductsFixture, "crew-tee,Crew Tee,", "crew-tee,Renamed Tee,", 1)
	result, err := app.Data().ImportProducts(ctx, strings.NewReader(renamed), ImportOptions{Overwrite: new(bool)})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Created != 0 || result.Updated != 0 || result.Skipped != 5 || len(result.Errors) != 0 {
		t.Errorf("result = %+v, want every row skipped and nothing written", result)
	}
	tee, _ := app.Products().GetProductBySlug(ctx, "crew-tee")
	if tee.Title != "Crew Tee" {
		t.Errorf("title = %q, want the existing product left alone", tee.Title)
	}
	// A new handle in the same file is still created.
	added := renamed + "hat,Hat,,,,,,TRUE,Title,Default Title,,,,,HAT,,,,,,5.00,,,,,,,,,,,,,,,active\n"
	result, err = app.Data().ImportProducts(ctx, strings.NewReader(added), ImportOptions{Overwrite: new(bool)})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Created != 1 || result.Skipped != 5 {
		t.Errorf("result = %+v, want the hat created and the rest skipped", result)
	}
}

// The store's own file learned the columns Shopify's has, so nothing is lost
// choosing either: merchandising, cost and tax, weight unit, and pictures.
func TestTheNativeFileCarriesMerchandisingAndPictures(t *testing.T) {
	app, _ := mediaApp(t)
	ctx := context.Background()
	importShopifyFixture(t, app)

	var buf bytes.Buffer
	if err := app.Data().ExportProducts(ctx, &buf, ProductQuery{}, ExportOptions{}); err != nil {
		t.Fatalf("export: %v", err)
	}
	header, rows := readCSV(t, buf.String())
	for _, col := range []string{"vendor", "product_type", "tags", "category", "seo_title", "seo_description",
		"cost_minor", "taxable", "weight_unit", "images", "variant_images"} {
		if !containsString(header, col) {
			t.Errorf("header lacks %q: %v", col, header)
		}
	}
	first := rows[0]
	if cell(t, header, first, "vendor") != "Acme" || cell(t, header, first, "tags") != "basics,summer" ||
		cell(t, header, first, "category") != "Apparel & Accessories / Clothing" || cell(t, header, first, "cost_minor") != "850" ||
		cell(t, header, first, "taxable") != "true" || cell(t, header, first, "weight_unit") != "g" {
		t.Errorf("first row = %v", first)
	}
	if got := cell(t, header, first, "images"); got != "https://cdn.example/tee-1.jpg|https://cdn.example/tee-2.jpg|https://cdn.example/tee-3.jpg|https://cdn.example/tee-4.jpg" {
		t.Errorf("images = %q", got)
	}
	if got := cell(t, header, rows[1], "images"); got != "" {
		t.Errorf("second row repeats the product's pictures: %q", got)
	}
	if got := cell(t, header, rows[2], "variant_images"); got != "https://cdn.example/tee-3.jpg" {
		t.Errorf("TEE-WHT-S variant_images = %q", got)
	}

	// Editing the file edits the product: two pictures in a new order, new
	// tags, a cost cleared — and an untouched column leaves its field alone.
	edited := "product_slug,sku,price_minor,images,tags\n" +
		"crew-tee,TEE-BLK-S,1999,https://cdn.example/tee-2.jpg|https://cdn.example/tee-1.jpg,\"winter,basics\"\n"
	if _, err := app.Data().ImportProducts(ctx, strings.NewReader(edited), ImportOptions{}); err != nil {
		t.Fatalf("import edit: %v", err)
	}
	tee, _ := app.Products().GetProductBySlug(ctx, "crew-tee")
	media, _ := app.MediaLibrary().ForProduct(ctx, tee.ID)
	if len(media) != 2 || !strings.HasSuffix(media[0].URL, "tee-2.jpg") || !strings.HasSuffix(media[1].URL, "tee-1.jpg") {
		t.Errorf("pictures after the edit = %+v, want tee-2 then tee-1", media)
	}
	if strings.Join(tee.Tags, ",") != "basics,winter" || tee.Vendor != "Acme" {
		t.Errorf("tags %v vendor %q, want the tags replaced and the vendor kept", tee.Tags, tee.Vendor)
	}
	// The picture a variant showed is gone from the product, so it shows none;
	// the one that stayed is still shown by the variant that named it.
	for _, v := range tee.Variants {
		switch v.SKU {
		case "TEE-WHT-S":
			if v.Image != nil {
				t.Errorf("TEE-WHT-S still shows %+v after its picture left the product", v.Image)
			}
		case "TEE-BLK-S":
			if v.Image == nil || !strings.HasSuffix(v.Image.URL, "tee-1.jpg") {
				t.Errorf("TEE-BLK-S shows %+v, want tee-1 kept", v.Image)
			}
		}
	}
}

// The routes: the format is read off the file's header on the way in, and
// asked for by name on the way out.
func TestTransferRoutesSpeakBothDialects(t *testing.T) {
	app, _ := mediaApp(t)

	rec := do(t, app, http.MethodPost, "/api/admin/import/products", withAdmin, textBody(shopifyProductsFixture))
	if rec.Code != http.StatusOK {
		t.Fatalf("import = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Data ImportResult `json:"data"`
	}
	decodeJSONBody(t, rec.Body.Bytes(), &body)
	if body.Data.Created != 2 || body.Data.Format != FormatShopify {
		t.Errorf("result = %+v, want two created from a file recognised as Shopify's", body.Data)
	}

	out := do(t, app, http.MethodGet, "/api/admin/export/admin-products?format=shopify", withAdmin)
	if out.Code != http.StatusOK {
		t.Fatalf("export = %d: %s", out.Code, out.Body)
	}
	if line, _, _ := strings.Cut(out.Body.String(), "\n"); strings.TrimSpace(line) != strings.Join(shopifyProductHeader, ",") {
		t.Errorf("shopify export header = %q", line)
	}
	native := do(t, app, http.MethodGet, "/api/admin/export/admin-products", withAdmin)
	if line, _, _ := strings.Cut(native.Body.String(), "\n"); !strings.HasPrefix(line, "product_slug,") {
		t.Errorf("native export header = %q", line)
	}
	if bad := do(t, app, http.MethodGet, "/api/admin/export/admin-products?format=woocommerce", withAdmin); bad.Code != http.StatusBadRequest {
		t.Errorf("unknown format = %d, want 400", bad.Code)
	}
	// The picture URL in a route export carries the request's own host.
	if !strings.Contains(out.Body.String(), "https://cdn.example/tee-1.jpg") {
		t.Errorf("export lost the pictures:\n%s", out.Body.String())
	}
}

func textBody(s string) func(*http.Request) {
	return func(r *http.Request) {
		r.Body = io.NopCloser(strings.NewReader(s))
		r.Header.Set("Content-Type", "text/csv")
	}
}

// tinyPNG is a one-pixel picture, enough for the library to hold a file.
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}
