package gocommerce

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// Catalog fields that are neither price nor stock but sit beside them.
// TestVariantCostAndTax covers the two fields Shopify's pricing card needs
// beside a price. Cost is nullable on purpose: an emptied box means "nobody has
// costed this", and storing zero instead would report a 100% margin.
func TestVariantCostAndTax(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	price := int64(2000)

	p, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Costed tee", SKU: "COST-1", PriceMinor: &price,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	v := p.Variants[0]
	if v.Cost != nil {
		t.Errorf("a fresh variant has cost %v, want none recorded", v.Cost)
	}
	if !v.Taxable {
		t.Error("taxable defaulted to false; tax not charged is not recoverable")
	}

	updated, err := app.Products().UpdateVariant(ctx, v.ID, VariantPatch{CostMinor: SetAmount(800)})
	if err != nil {
		t.Fatalf("set cost: %v", err)
	}
	if updated.Cost == nil || updated.Cost.AmountMinor != 800 {
		t.Fatalf("cost = %v, want 800", updated.Cost)
	}
	if updated.Cost.Currency != updated.Price.Currency {
		t.Errorf("cost currency %q, want the store's %q",
			updated.Cost.Currency, updated.Price.Currency)
	}

	off := false
	updated, err = app.Products().UpdateVariant(ctx, v.ID, VariantPatch{Taxable: &off})
	if err != nil {
		t.Fatalf("clear taxable: %v", err)
	}
	if updated.Taxable {
		t.Error("taxable stayed true after being patched false")
	}
	// Clearing records "no cost", which is not a cost of zero — a zero would
	// report a 100% margin on something nobody has costed.
	cleared, err := app.Products().UpdateVariant(ctx, v.ID, VariantPatch{CostMinor: ClearAmount()})
	if err != nil {
		t.Fatalf("clear cost: %v", err)
	}
	if cleared.Cost != nil {
		t.Errorf("cost = %v after clearing, want none recorded", cleared.Cost)
	}
	if _, err := app.Products().UpdateVariant(ctx, v.ID, VariantPatch{CostMinor: SetAmount(800)}); err != nil {
		t.Fatalf("restore cost: %v", err)
	}

	// A patch that mentions neither leaves both alone.
	untouched, err := app.Products().UpdateVariant(ctx, v.ID, VariantPatch{SKU: ptr("COST-1B")})
	if err != nil {
		t.Fatalf("unrelated patch: %v", err)
	}
	if untouched.Cost == nil || untouched.Cost.AmountMinor != 800 || untouched.Taxable {
		t.Errorf("cost/taxable = %v/%v after an unrelated patch, want them unchanged",
			untouched.Cost, untouched.Taxable)
	}

	// And they arrive on creation too.
	seed := int64(500)
	no := false
	p2, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Gift card", Status: ProductActive,
		Variants: []VariantInput{{
			SKU: "COST-2", PriceMinor: 5000, CostMinor: &seed, Taxable: &no,
		}},
	})
	if err != nil {
		t.Fatalf("create with cost: %v", err)
	}
	if got := p2.Variants[0]; got.Cost == nil || got.Cost.AmountMinor != 500 || got.Taxable {
		t.Errorf("created variant cost/taxable = %v/%v, want 500/false", got.Cost, got.Taxable)
	}
}

// ------------------------------------------------------- customs and oversell

// Country of origin and HS code are typed by people copying from documents, so
// what counts is what the engine accepts and what it stores.
func TestCustomsFieldsAreNormalized(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	price := int64(1000)
	stock := 5

	p, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Customs tee", Status: ProductActive,
		SKU: "CUSTOMS-1", PriceMinor: &price, Stock: &stock,
	})
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	v := p.DefaultVariant()

	// The three spellings of one tariff number, and a lowercase country.
	for _, given := range []string{"610910", "6109.10", "6109 10"} {
		country := "gb"
		updated, err := app.Products().UpdateVariant(ctx, v.ID, VariantPatch{
			OriginCountry: &country,
			HSCode:        &given,
		})
		if err != nil {
			t.Fatalf("patch with hs_code %q: %v", given, err)
		}
		if updated.HSCode != "610910" {
			t.Errorf("hs_code %q stored as %q, want 610910", given, updated.HSCode)
		}
		if updated.OriginCountry != "GB" {
			t.Errorf("origin_country stored as %q, want GB", updated.OriginCountry)
		}
	}

	// And what it refuses, with a reason rather than a constraint violation.
	for _, bad := range []struct{ field, value string }{
		{"hs_code", "61"},
		{"hs_code", "shirt"},
		{"origin_country", "GBR"},
		{"origin_country", "12"},
	} {
		patch := VariantPatch{}
		if bad.field == "hs_code" {
			patch.HSCode = &bad.value
		} else {
			patch.OriginCountry = &bad.value
		}
		_, err := app.Products().UpdateVariant(ctx, v.ID, patch)
		if err == nil {
			t.Errorf("%s = %q was accepted", bad.field, bad.value)
			continue
		}
		if !strings.Contains(err.Error(), bad.field) {
			t.Errorf("%s = %q refused with %q, which does not name the field",
				bad.field, bad.value, err.Error())
		}
	}

	// Emptied means "not recorded", and empty is not a validation failure.
	empty := ""
	cleared, err := app.Products().UpdateVariant(ctx, v.ID, VariantPatch{
		OriginCountry: &empty, HSCode: &empty,
	})
	if err != nil {
		t.Fatalf("clear the customs fields: %v", err)
	}
	if cleared.HSCode != "" || cleared.OriginCountry != "" {
		t.Errorf("cleared to %q / %q", cleared.OriginCountry, cleared.HSCode)
	}
}

// The customs fields and the oversell switch ride the CSV like every other
// variant column: a store that keeps its catalog in a spreadsheet keeps these
// there too, and a round trip that quietly dropped them would reset them on the
// next import.
func TestProductCSVCarriesCustomsAndOversell(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	price := int64(2500)
	stock := 4
	p, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "CSV tee", Status: ProductActive,
		SKU: "CSV-CUSTOMS-1", PriceMinor: &price, Stock: &stock,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	on := true
	country, code := "PT", "6109.10"
	if _, err := app.Products().UpdateVariant(ctx, p.DefaultVariant().ID, VariantPatch{
		ContinueSelling: &on, OriginCountry: &country, HSCode: &code,
	}); err != nil {
		t.Fatalf("set the fields: %v", err)
	}

	var buf bytes.Buffer
	if err := app.Data().ExportProducts(ctx, &buf, ProductQuery{}, ExportOptions{}); err != nil {
		t.Fatalf("export: %v", err)
	}
	csv := buf.String()
	for _, want := range []string{"continue_selling", "origin_country", "hs_code", "PT", "610910"} {
		if !strings.Contains(csv, want) {
			t.Errorf("the export does not carry %q", want)
		}
	}

	// Reset them, then import the file back: the values must return.
	off := false
	empty := ""
	if _, err := app.Products().UpdateVariant(ctx, p.DefaultVariant().ID, VariantPatch{
		ContinueSelling: &off, OriginCountry: &empty, HSCode: &empty,
	}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, err := app.Data().ImportProducts(ctx, strings.NewReader(csv), ImportOptions{}); err != nil {
		t.Fatalf("import: %v", err)
	}
	back, err := app.Products().GetVariantBySKU(ctx, "CSV-CUSTOMS-1")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !back.ContinueSelling || back.OriginCountry != "PT" || back.HSCode != "610910" {
		t.Errorf("round trip lost them: continue_selling=%v origin=%q hs=%q",
			back.ContinueSelling, back.OriginCountry, back.HSCode)
	}

	// A file from before these columns existed still imports, and says nothing
	// about fields it does not carry — that is what keying by column name buys.
	old := "product_slug,product_title,product_status,sku,price_minor\n" +
		"csv-tee,CSV tee,active,CSV-CUSTOMS-1,2500\n"
	if _, err := app.Data().ImportProducts(ctx, strings.NewReader(old), ImportOptions{}); err != nil {
		t.Fatalf("import an older file: %v", err)
	}
	after, err := app.Products().GetVariantBySKU(ctx, "CSV-CUSTOMS-1")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if after.OriginCountry != "PT" || after.HSCode != "610910" {
		t.Errorf("origin = %q, hs = %q after a file without the columns; an absent column says "+
			"nothing, so a price list must not strip the customs paperwork", after.OriginCountry, after.HSCode)
	}
}

// A compare-at price has three states on a patch and only a NullableAmount can
// carry them: omitted leaves it alone, a number sets it, null takes the item off
// sale. With a plain pointer the last two are the same wire value, so a
// struck-through price could be set and never removed.
func TestVariantCompareAtPriceClears(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	price := int64(2000)
	p, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Sale tee", SKU: "SALE-1", PriceMinor: &price,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	target := "/api/admin/variants/" + strconv.FormatInt(p.DefaultVariant().ID, 10)

	patch := func(body string) *Variant {
		t.Helper()
		rec := doBody(t, app, http.MethodPatch, target, body, withAdmin)
		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH %s = %d: %s", body, rec.Code, rec.Body)
		}
		var v Variant
		decodeData(t, rec, &v)
		return &v
	}

	if v := patch(`{"compare_at_price_minor":3000}`); v.CompareAtPrice == nil ||
		v.CompareAtPrice.AmountMinor != 3000 {
		t.Fatalf("compare_at_price = %+v, want 3000", v.CompareAtPrice)
	}
	// An unrelated patch must not disturb it — that is what "omitted" means.
	if v := patch(`{"sku":"SALE-1-B"}`); v.CompareAtPrice == nil ||
		v.CompareAtPrice.AmountMinor != 3000 {
		t.Errorf("compare_at_price = %+v after an unrelated patch, want it left alone", v.CompareAtPrice)
	}
	if v := patch(`{"compare_at_price_minor":null}`); v.CompareAtPrice != nil {
		t.Errorf("compare_at_price = %+v after sending null, want it cleared", v.CompareAtPrice)
	}

	// Negative is a client mistake and comes back as one: before the guard it
	// reached the column CHECK, which translateCatalogErr does not recognise, so
	// the answer was a 500.
	for _, body := range []string{`{"compare_at_price_minor":-1}`, `{"cost_minor":-1}`} {
		rec := doBody(t, app, http.MethodPatch, target, body, withAdmin)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("PATCH %s = %d, want 400: %s", body, rec.Code, rec.Body)
			continue
		}
		if code := decodeError(t, rec).Code; code != "validation_failed" {
			t.Errorf("PATCH %s error code = %q, want validation_failed", body, code)
		}
	}
}

// The export takes the listing's whole filter vocabulary, through the same
// builder, so a reconciliation does not have to download the catalogue.
func TestExportProductsHonoursFilters(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	price := int64(1000)
	stock := 2
	mk := func(sku, status, vendor string, tags []string) *Product {
		t.Helper()
		p, err := app.Products().CreateProduct(ctx, ProductInput{
			Title: "Filtered " + sku, Status: status, Vendor: vendor, Tags: tags,
			SKU: sku, PriceMinor: &price, Stock: &stock,
		})
		if err != nil {
			t.Fatalf("create %s: %v", sku, err)
		}
		return p
	}
	live := mk("EXP-LIVE", ProductActive, "Acme", []string{"summer"})
	mk("EXP-DRAFT", ProductDraft, "Other", nil)

	parent, err := app.Categories().Create(ctx, CategoryInput{Title: "Outerwear"})
	if err != nil {
		t.Fatalf("create parent category: %v", err)
	}
	child, err := app.Categories().Create(ctx, CategoryInput{Title: "Parkas", ParentID: &parent.ID})
	if err != nil {
		t.Fatalf("create child category: %v", err)
	}
	if _, err := app.Products().UpdateProduct(ctx, live.ID,
		ProductPatch{CategoryID: SetID(child.ID)}); err != nil {
		t.Fatalf("file the product: %v", err)
	}
	col := newCollection(t, app, CollectionInput{Title: "Window"})
	if err := app.Collections().SetCollectionProducts(ctx, col.ID, []int64{live.ID}); err != nil {
		t.Fatalf("curate: %v", err)
	}

	// A second variant on the one that survives every filter: its rows have to
	// stay contiguous, because that is what the importer reads.
	if _, err := app.Products().AddOption(ctx, live.ID,
		OptionInput{Name: "Size", Values: []string{"S", "M"}}); err != nil {
		t.Fatalf("add option: %v", err)
	}
	if _, err := app.Products().CreateVariant(ctx, live.ID, VariantInput{
		SKU: "EXP-LIVE-M", PriceMinor: 1000, Options: []string{"M"},
	}); err != nil {
		t.Fatalf("create the second variant: %v", err)
	}

	export := func(q ProductQuery) string {
		t.Helper()
		var buf bytes.Buffer
		if err := app.Data().ExportProducts(ctx, &buf, q, ExportOptions{}); err != nil {
			t.Fatalf("export: %v", err)
		}
		return buf.String()
	}

	all := export(ProductQuery{})
	if !strings.Contains(all, "EXP-LIVE") || !strings.Contains(all, "EXP-DRAFT") {
		t.Fatalf("an unfiltered export is missing rows:\n%s", all)
	}
	for _, q := range []ProductQuery{
		{Status: ProductActive},
		{Vendor: "Acme"},
		{Tag: "summer"},
		{CategoryID: parent.ID}, // the ancestor, proving categoryFilter still expands
		{CollectionID: col.ID},
		{Search: "Filtered EXP-LIVE"},
	} {
		csv := export(q)
		if strings.Contains(csv, "EXP-DRAFT") {
			t.Errorf("%+v exported the row it should have filtered out:\n%s", q, csv)
		}
		if !strings.Contains(csv, "EXP-LIVE") {
			t.Errorf("%+v exported nothing:\n%s", q, csv)
		}
		// Both of the surviving product's variants, one after the other.
		lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(csv, "\r\n", "\n")), "\n")
		if len(lines) != 3 {
			t.Errorf("%+v exported %d lines, want a header and two contiguous variant rows:\n%s",
				q, len(lines), csv)
		}
	}
}

// TestASKUIsDerivedWhenNoneIsGiven covers the first SKU a product has.
//
// Generating the option matrix has always named its variants after the
// product — slug plus the option values — but the variant that exists before
// there are any options had to be typed by hand, and so did one added on its
// own afterwards. That is a code somebody invents at the moment they are least
// able to: a new product, no range to be consistent with, and a required field
// between them and saving. So an absent SKU is derived by the same rule, and
// stays editable afterwards like any other.
func TestASKUIsDerivedWhenNoneIsGiven(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	price := int64(2500)

	// A product with no options and no SKU: named after itself.
	p, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Linen field jumper", Status: "active", PriceMinor: &price,
	})
	if err != nil {
		t.Fatalf("create without a sku: %v", err)
	}
	if got := p.DefaultVariant().SKU; got != "LINEN-FIELD-JUMPER" {
		t.Errorf("derived sku = %q, want LINEN-FIELD-JUMPER", got)
	}

	// One given explicitly is still exactly what was asked for.
	given, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Wool scarf", Status: "active", SKU: "wool-01", PriceMinor: &price,
	})
	if err != nil {
		t.Fatalf("create with a sku: %v", err)
	}
	if got := given.DefaultVariant().SKU; got != "wool-01" {
		t.Errorf("sku = %q, want the one that was given", got)
	}

	// A second product whose title derives the same code does not collide: the
	// engine numbers it rather than refusing, because the operator did not
	// choose this code and cannot be asked to resolve a clash in it.
	again, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Linen field jumper", Slug: "linen-field-jumper-2", Status: "active", PriceMinor: &price,
	})
	if err != nil {
		t.Fatalf("create a second: %v", err)
	}
	if got := again.DefaultVariant().SKU; got == "LINEN-FIELD-JUMPER" || got == "" {
		t.Errorf("second derived sku = %q, want a distinct one", got)
	}

	// And a variant added by hand to a product that has options.
	withAxis, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Cotton cap", Status: "active", SKU: "CAP", PriceMinor: &price,
		Options: []OptionInput{{Name: "Size", Values: []string{"S", "M"}}},
		Variants: []VariantInput{
			{SKU: "CAP-S", PriceMinor: price, Options: []string{"S"}},
		},
	})
	if err != nil {
		t.Fatalf("create with an axis: %v", err)
	}
	v, err := app.Products().CreateVariant(ctx, withAxis.ID, VariantInput{
		PriceMinor: price, Options: []string{"M"},
	})
	if err != nil {
		t.Fatalf("create a variant without a sku: %v", err)
	}
	if v.SKU != "COTTON-CAP-M" {
		t.Errorf("derived variant sku = %q, want COTTON-CAP-M", v.SKU)
	}

	// Still editable: the derived code is a starting point, not a decision.
	renamed := "CAP-MEDIUM"
	updated, err := app.Products().UpdateVariant(ctx, v.ID, VariantPatch{SKU: &renamed})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if updated.SKU != renamed {
		t.Errorf("sku after rename = %q, want %q", updated.SKU, renamed)
	}
}
