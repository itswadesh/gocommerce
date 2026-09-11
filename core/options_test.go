package gocommerce

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// SetOptions is the one call that can destroy a catalog by accident: it edits
// the axes and the variants that depend on them together. These tests pin what
// survives an edit and what does not, because "my prices reset" is the kind of
// bug that is only discovered after the damage.

func optionsProduct(t *testing.T, app *App, sku string) *Product {
	t.Helper()
	price := int64(2500)
	stock := 10
	p, err := app.Products().CreateProduct(context.Background(), ProductInput{
		Title: "Tee " + sku, Status: ProductActive,
		SKU: sku, PriceMinor: &price, Stock: &stock,
	})
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	return p
}

func TestSetOptionsGeneratesTheMatrix(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	p := optionsProduct(t, app, "MATRIX-1")

	price := int64(1999)
	fresh, change, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options: []OptionSpec{
			{Name: "Size", Values: OptionValues("S", "M", "L")},
			{Name: "Color", Values: OptionValues("Black", "White")},
		},
		GenerateVariants: true,
		PriceMinor:       &price,
	})
	if err != nil {
		t.Fatalf("set options: %v", err)
	}

	if len(fresh.Options) != 2 {
		t.Fatalf("product has %d axes, want 2", len(fresh.Options))
	}
	// 3 sizes x 2 colours = 6 combinations. The product's original default
	// variant has no options, so it survives alongside them.
	if len(change.VariantsCreated) != 6 {
		t.Errorf("created %d variants, want 6: %v", len(change.VariantsCreated), change.VariantsCreated)
	}
	for _, v := range fresh.Variants {
		if len(v.Options) == 2 && v.Price.AmountMinor != price {
			t.Errorf("generated variant %s priced at %d, want %d", v.SKU, v.Price.AmountMinor, price)
		}
	}
}

// Renaming an axis must not touch what is being sold. A variant's price, SKU
// and stock describe the thing, not the label above it.
func TestRenamingAnAxisKeepsVariantsIntact(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	p := optionsProduct(t, app, "RENAME-1")

	if _, _, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options:          []OptionSpec{{Name: "Size", Values: OptionValues("S", "M")}},
		GenerateVariants: true,
	}); err != nil {
		t.Fatalf("first set: %v", err)
	}

	before, err := app.Products().GetProduct(ctx, p.ID)
	if err != nil {
		t.Fatalf("read product: %v", err)
	}
	// Give one variant a distinctive price and some stock, so its survival is
	// visible rather than assumed.
	var target *Variant
	for i := range before.Variants {
		if len(before.Variants[i].Options) == 1 {
			target = &before.Variants[i]
			break
		}
	}
	if target == nil {
		t.Fatal("no variant with an option to test with")
	}
	newPrice := int64(4242)
	if _, err := app.Products().UpdateVariant(ctx, target.ID, VariantPatch{PriceMinor: &newPrice}); err != nil {
		t.Fatalf("reprice: %v", err)
	}
	if _, err := app.Stock().SetOnHand(ctx, target.ID, 0, 7, ""); err != nil {
		t.Fatalf("set stock: %v", err)
	}

	// Rename the axis, keeping the same values. The id is what says "this is
	// the same axis" — without it the engine cannot tell a rename from a
	// delete-plus-add, and every variant would lose its size.
	axisID := before.Options[0].ID
	_, change, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options: []OptionSpec{{ID: &axisID, Name: "Größe", Values: OptionValues("S", "M")}},
	})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if len(change.VariantsRemoved) != 0 {
		t.Errorf("renaming an axis removed variants: %v", change.VariantsRemoved)
	}

	after, err := app.Products().GetVariant(ctx, target.ID)
	if err != nil {
		t.Fatalf("the variant did not survive the rename: %v", err)
	}
	if after.Price.AmountMinor != newPrice {
		t.Errorf("price = %d after rename, want %d", after.Price.AmountMinor, newPrice)
	}
	if after.StockOnHand != 7 {
		t.Errorf("stock = %d after rename, want 7", after.StockOnHand)
	}
	if after.SKU != target.SKU {
		t.Errorf("sku = %q after rename, want %q", after.SKU, target.SKU)
	}
}

// Dropping a value takes its variants with it — that is the point — but only
// those.
func TestDroppingAValueRemovesOnlyItsVariants(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	p := optionsProduct(t, app, "DROP-1")

	if _, _, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options:          []OptionSpec{{Name: "Size", Values: OptionValues("S", "M", "L")}},
		GenerateVariants: true,
	}); err != nil {
		t.Fatalf("first set: %v", err)
	}

	withL, err := app.Products().GetProduct(ctx, p.ID)
	if err != nil {
		t.Fatalf("read product: %v", err)
	}
	sizeID := withL.Options[0].ID

	fresh, change, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options: []OptionSpec{{ID: &sizeID, Name: "Size", Values: OptionValues("S", "M")}},
	})
	if err != nil {
		t.Fatalf("drop L: %v", err)
	}

	if len(change.VariantsRemoved) != 1 {
		t.Errorf("removed %d variants, want exactly the L one: %v",
			len(change.VariantsRemoved), change.VariantsRemoved)
	}
	if len(change.ValuesRemoved) != 1 || !strings.Contains(change.ValuesRemoved[0], "L") {
		t.Errorf("values removed = %v, want Size: L", change.ValuesRemoved)
	}
	for _, v := range fresh.Variants {
		for _, opt := range v.Options {
			if opt == "L" {
				t.Errorf("variant %s still carries the dropped value L", v.SKU)
			}
		}
	}
}

// The engine resolves a variant's options by value alone, so the same value on
// two axes cannot be told apart. Refusing beats accepting and being silently
// wrong — this is the hole the panel used to paper over client-side.
func TestSetOptionsRefusesAmbiguousValues(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	p := optionsProduct(t, app, "AMBIG-1")

	_, _, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options: []OptionSpec{
			{Name: "Size", Values: OptionValues("Small", "Large")},
			{Name: "Cup", Values: OptionValues("Small", "Grande")},
		},
	})
	if err == nil {
		t.Fatal("accepted the same value on two axes; a variant's options could not be resolved")
	}
	if !strings.Contains(err.Error(), "Small") {
		t.Errorf("error = %q, want it to name the ambiguous value", err.Error())
	}

	// Two axes with distinct values are fine.
	if _, _, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options: []OptionSpec{
			{Name: "Size", Values: OptionValues("Small", "Large")},
			{Name: "Cup", Values: OptionValues("Single", "Double")},
		},
	}); err != nil {
		t.Errorf("distinct values across axes were refused: %v", err)
	}
}

func TestSetOptionsValidatesInput(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	p := optionsProduct(t, app, "VALID-1")

	for _, tc := range []struct {
		name string
		set  OptionSet
		want string
	}{
		{"no name", OptionSet{Options: []OptionSpec{{Name: "  ", Values: OptionValues("S")}}}, "name"},
		{"no values", OptionSet{Options: []OptionSpec{{Name: "Size", Values: OptionValues(" ")}}}, "no values"},
		{"duplicate axes", OptionSet{Options: []OptionSpec{
			{Name: "Size", Values: OptionValues("S")},
			{Name: "size", Values: OptionValues("M")},
		}}, "both called"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := app.Products().SetOptions(ctx, p.ID, tc.set); err == nil {
				t.Fatalf("accepted %s", tc.name)
			} else if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), tc.want)
			}
		})
	}

	// Duplicate values within one axis are de-duplicated rather than refused:
	// typing "S, S, M" is a slip, not a decision.
	fresh, _, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options: []OptionSpec{{Name: "Size", Values: OptionValues("S", " S ", "M")}},
	})
	if err != nil {
		t.Fatalf("de-duplication: %v", err)
	}
	if len(fresh.Options) != 1 || len(fresh.Options[0].Values) != 2 {
		t.Errorf("axis has %d values, want 2 after de-duplication", len(fresh.Options[0].Values))
	}
}

func TestSetOptionsOverHTTP(t *testing.T) {
	app := newTestApp(t)
	p := optionsProduct(t, app, "HTTP-OPT-1")

	rec := do(t, app, "PUT", "/api/admin/products/"+strconv.FormatInt(p.ID, 10)+"/options", withAdmin,
		jsonBody(t, map[string]any{
			"options": []map[string]any{
				{"name": "Size", "values": []string{"S", "M"}},
			},
			"generate_variants": true,
		}))
	if rec.Code != 200 {
		t.Fatalf("PUT options = %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"changed"`) || !strings.Contains(body, `"product"`) {
		t.Errorf("response should carry both the product and what changed: %s", body)
	}
	if !strings.Contains(body, "variants_created") {
		t.Errorf("response does not report the generated variants: %s", body)
	}

	// It needs admin auth like everything else under /api/admin.
	rec = do(t, app, "PUT", "/api/admin/products/"+strconv.FormatInt(p.ID, 10)+"/options",
		jsonBody(t, map[string]any{"options": []map[string]any{{"name": "Size", "values": []string{"S"}}}}))
	if rec.Code != 401 {
		t.Errorf("unauthenticated = %d, want 401", rec.Code)
	}
}

// Deleting an axis collapses the variants under it onto each other: without
// Colour, S/Red and S/Blue are both just S. The engine used to leave that to
// the unique index on option_key, which meant the operator pressed "Save
// options" and got an internal error instead of a deleted option.
func TestDroppingAnAxisMergesTheVariantsItCollapses(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	p := optionsProduct(t, app, "MERGE-1")

	if _, _, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options: []OptionSpec{
			{Name: "Size", Values: OptionValues("S", "M")},
			{Name: "Colour", Values: OptionValues("Red", "Blue")},
		},
		GenerateVariants: true,
	}); err != nil {
		t.Fatalf("first set: %v", err)
	}

	before, err := app.Products().GetProduct(ctx, p.ID)
	if err != nil {
		t.Fatalf("read product: %v", err)
	}
	sizeID := before.Options[0].ID

	// The survivor is the first of each collapsed group in the catalog's own
	// order, and it keeps what it was selling for.
	survivors := map[string]*Variant{}
	for i := range before.Variants {
		size := ""
		for _, opt := range before.Variants[i].Options {
			if opt == "S" || opt == "M" {
				size = opt
			}
		}
		if size == "" {
			continue
		}
		if _, seen := survivors[size]; !seen {
			survivors[size] = &before.Variants[i]
		}
	}
	if len(survivors) != 2 {
		t.Fatalf("expected variants on both sizes to start with, got %d", len(survivors))
	}
	price := int64(3131)
	if _, err := app.Products().UpdateVariant(ctx, survivors["S"].ID, VariantPatch{PriceMinor: &price}); err != nil {
		t.Fatalf("reprice: %v", err)
	}

	fresh, change, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options: []OptionSpec{{ID: &sizeID, Name: "Size", Values: OptionValues("S", "M")}},
	})
	if err != nil {
		t.Fatalf("drop the colour axis: %v", err)
	}

	if len(change.AxesRemoved) != 1 || change.AxesRemoved[0] != "Colour" {
		t.Errorf("axes removed = %v, want Colour", change.AxesRemoved)
	}
	// Two of the four colour variants are merged away, and the operator is told
	// which — a deletion nobody is told about is the one that gets noticed in
	// the order that fails.
	if len(change.VariantsRemoved) != 2 {
		t.Errorf("removed %d variants, want the 2 duplicates: %v",
			len(change.VariantsRemoved), change.VariantsRemoved)
	}
	for _, sku := range change.VariantsRemoved {
		if sku == survivors["S"].SKU || sku == survivors["M"].SKU {
			t.Errorf("removed %s, which was first in its group and should have been kept", sku)
		}
	}

	sizes := map[string]int{}
	for _, v := range fresh.Variants {
		for _, opt := range v.Options {
			sizes[opt]++
		}
	}
	if sizes["S"] != 1 || sizes["M"] != 1 {
		t.Errorf("variants per size = %v, want exactly one each", sizes)
	}

	kept, err := app.Products().GetVariant(ctx, survivors["S"].ID)
	if err != nil {
		t.Fatalf("the survivor did not survive: %v", err)
	}
	if kept.Price.AmountMinor != price {
		t.Errorf("survivor price = %d, want %d", kept.Price.AmountMinor, price)
	}
}

// Removing the last axis leaves the product sold as one thing, which is one
// variant — the same merge, all the way down.
func TestDroppingEveryAxisLeavesOneVariant(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	p := optionsProduct(t, app, "MERGE-2")

	if _, _, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options:          []OptionSpec{{Name: "Size", Values: OptionValues("S", "M", "L")}},
		GenerateVariants: true,
	}); err != nil {
		t.Fatalf("first set: %v", err)
	}

	fresh, _, err := app.Products().SetOptions(ctx, p.ID, OptionSet{Options: nil})
	if err != nil {
		t.Fatalf("clear the options: %v", err)
	}
	if len(fresh.Options) != 0 {
		t.Errorf("product kept %d axes", len(fresh.Options))
	}
	if len(fresh.Variants) != 1 {
		t.Errorf("product has %d variants with no options, want 1", len(fresh.Variants))
	}
}

// Retyping a value in a different case is the same value — normalizeOptionValues
// folds them — so the variants holding it must be re-linked, not orphaned onto a
// value row that does not exist.
func TestRecasingAValueKeepsItsVariants(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	p := optionsProduct(t, app, "CASE-1")

	if _, _, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options:          []OptionSpec{{Name: "Size", Values: OptionValues("S", "M")}},
		GenerateVariants: true,
	}); err != nil {
		t.Fatalf("first set: %v", err)
	}
	before, err := app.Products().GetProduct(ctx, p.ID)
	if err != nil {
		t.Fatalf("read product: %v", err)
	}
	sizeID := before.Options[0].ID

	_, change, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options: []OptionSpec{{ID: &sizeID, Name: "size", Values: OptionValues("s", "m")}},
	})
	if err != nil {
		t.Fatalf("recase the values: %v", err)
	}
	if len(change.VariantsRemoved) != 0 {
		t.Errorf("recasing removed variants: %v", change.VariantsRemoved)
	}
	if len(change.ValuesRemoved) != 0 || len(change.ValuesAdded) != 0 {
		t.Errorf("recasing counted as a value change: removed %v, added %v",
			change.ValuesRemoved, change.ValuesAdded)
	}
	for i := range before.Variants {
		if _, err := app.Products().GetVariant(ctx, before.Variants[i].ID); err != nil {
			t.Errorf("variant %s did not survive the recase: %v", before.Variants[i].SKU, err)
		}
	}
}

// Renaming a VALUE must not touch what is being sold, for exactly the reason
// renaming an axis must not: "Red" becoming "Crimson" is a relabelling, and
// every Red variant's price, SKU, image and stock describe the thing rather
// than the label. Without value ids this was a delete plus an add, and the
// whole colour column of a matrix was destroyed by fixing a typo.
func TestRenamingAValueKeepsItsVariants(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	p := optionsProduct(t, app, "VRENAME-1")

	if _, _, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options:          []OptionSpec{{Name: "Colour", Values: OptionValues("Red", "Blue")}},
		GenerateVariants: true,
	}); err != nil {
		t.Fatalf("first set: %v", err)
	}

	before, err := app.Products().GetProduct(ctx, p.ID)
	if err != nil {
		t.Fatalf("read product: %v", err)
	}
	axis := before.Options[0]
	var red *Variant
	for i := range before.Variants {
		for _, opt := range before.Variants[i].Options {
			if opt == "Red" {
				red = &before.Variants[i]
			}
		}
	}
	if red == nil {
		t.Fatal("no Red variant to test with")
	}
	// A price and a stock count, so the variant's survival is visible rather
	// than assumed.
	price := int64(4242)
	if _, err := app.Products().UpdateVariant(ctx, red.ID, VariantPatch{PriceMinor: &price}); err != nil {
		t.Fatalf("reprice: %v", err)
	}
	if _, err := app.Stock().SetOnHand(ctx, red.ID, 0, 9, ""); err != nil {
		t.Fatalf("set stock: %v", err)
	}

	redID, blueID := axis.Values[0].ID, axis.Values[1].ID
	fresh, change, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options: []OptionSpec{{ID: &axis.ID, Name: "Colour", Values: []OptionValueSpec{
			{ID: &redID, Value: "Crimson"},
			{ID: &blueID, Value: "Blue"},
		}}},
	})
	if err != nil {
		t.Fatalf("rename the value: %v", err)
	}

	if len(change.VariantsRemoved) != 0 {
		t.Errorf("renaming a value removed variants: %v", change.VariantsRemoved)
	}
	// Reported as a rename and not as one deletion plus one addition: an
	// operator reading "Dropped Colour: Red" would reasonably conclude their
	// stock had just gone.
	if len(change.ValuesRenamed) != 1 || !strings.Contains(change.ValuesRenamed[0], "Red → Crimson") {
		t.Errorf("values renamed = %v, want Colour: Red → Crimson", change.ValuesRenamed)
	}
	if len(change.ValuesAdded) != 0 || len(change.ValuesRemoved) != 0 {
		t.Errorf("a rename counted as a value change: added %v, removed %v",
			change.ValuesAdded, change.ValuesRemoved)
	}

	after, err := app.Products().GetVariant(ctx, red.ID)
	if err != nil {
		t.Fatalf("the Red variant did not survive its own rename: %v", err)
	}
	if after.Price.AmountMinor != price {
		t.Errorf("price = %d after the rename, want %d", after.Price.AmountMinor, price)
	}
	if after.StockOnHand != 9 {
		t.Errorf("stock = %d after the rename, want 9", after.StockOnHand)
	}
	if after.SKU != red.SKU {
		t.Errorf("sku = %q after the rename, want %q", after.SKU, red.SKU)
	}
	// And it is selling the new name, not the old one.
	if len(after.Options) != 1 || after.Options[0] != "Crimson" {
		t.Errorf("variant options = %v, want [Crimson]", after.Options)
	}
	if len(fresh.Variants) != len(before.Variants) {
		t.Errorf("variant count went from %d to %d", len(before.Variants), len(fresh.Variants))
	}
}

// Reordering values is the same edit with the same ids in a different order,
// and it must move nothing but the display order.
func TestReorderingValuesKeepsEveryVariant(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	p := optionsProduct(t, app, "VORDER-1")

	if _, _, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options:          []OptionSpec{{Name: "Size", Values: OptionValues("S", "M", "L")}},
		GenerateVariants: true,
	}); err != nil {
		t.Fatalf("first set: %v", err)
	}
	before, err := app.Products().GetProduct(ctx, p.ID)
	if err != nil {
		t.Fatalf("read product: %v", err)
	}
	axis := before.Options[0]
	s, m, l := axis.Values[0].ID, axis.Values[1].ID, axis.Values[2].ID

	fresh, change, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options: []OptionSpec{{ID: &axis.ID, Name: "Size", Values: []OptionValueSpec{
			{ID: &l, Value: "L"},
			{ID: &m, Value: "M"},
			{ID: &s, Value: "S"},
		}}},
	})
	if err != nil {
		t.Fatalf("reorder: %v", err)
	}
	if len(change.VariantsRemoved) != 0 || len(change.ValuesAdded) != 0 || len(change.ValuesRemoved) != 0 {
		t.Errorf("reordering was not a no-op: %+v", change)
	}
	if got := []string{
		fresh.Options[0].Values[0].Value,
		fresh.Options[0].Values[1].Value,
		fresh.Options[0].Values[2].Value,
	}; got[0] != "L" || got[1] != "M" || got[2] != "S" {
		t.Errorf("values = %v, want L M S", got)
	}
	for i := range before.Variants {
		if _, err := app.Products().GetVariant(ctx, before.Variants[i].ID); err != nil {
			t.Errorf("variant %s did not survive the reorder: %v", before.Variants[i].SKU, err)
		}
	}
}

// A value id belonging to another axis — or to nothing — is a stale read, and
// honouring it would re-point somebody else's variants. Refused, exactly as an
// unknown axis id is.
func TestSetOptionsRefusesAStaleValueID(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	p := optionsProduct(t, app, "VSTALE-1")

	if _, _, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options: []OptionSpec{
			{Name: "Size", Values: OptionValues("S", "M")},
			{Name: "Colour", Values: OptionValues("Red")},
		},
		GenerateVariants: true,
	}); err != nil {
		t.Fatalf("first set: %v", err)
	}
	before, err := app.Products().GetProduct(ctx, p.ID)
	if err != nil {
		t.Fatalf("read product: %v", err)
	}
	size, colour := before.Options[0], before.Options[1]
	other := colour.Values[0].ID

	if _, _, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options: []OptionSpec{
			{ID: &size.ID, Name: "Size", Values: []OptionValueSpec{{ID: &other, Value: "S"}}},
			{ID: &colour.ID, Name: "Colour", Values: OptionValues("Red")},
		},
	}); err == nil {
		t.Fatal("accepted a value id from a different axis")
	} else if !strings.Contains(err.Error(), "no value") {
		t.Errorf("error = %q, want it to say the axis has no such value", err.Error())
	}

	missing := int64(999999)
	if _, _, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options: []OptionSpec{
			{ID: &size.ID, Name: "Size", Values: []OptionValueSpec{{ID: &missing, Value: "S"}}},
		},
	}); err == nil {
		t.Fatal("accepted a value id that does not exist")
	}

	// One axis naming the same value twice is malformed either way, and
	// resolving it silently would move variants onto whichever came first.
	first := size.Values[0].ID
	if _, _, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options: []OptionSpec{
			{ID: &size.ID, Name: "Size", Values: []OptionValueSpec{
				{ID: &first, Value: "S"},
				{ID: &first, Value: "Small"},
			}},
		},
	}); err == nil {
		t.Fatal("accepted one value id twice on one axis")
	}
}

// Renaming a value while adding a new one under the old name is two different
// facts, and both have to land: the variants follow the id, and the name that
// was freed up is genuinely new.
func TestRenamingAValueAndReusingItsName(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	p := optionsProduct(t, app, "VREUSE-1")

	if _, _, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options:          []OptionSpec{{Name: "Colour", Values: OptionValues("Red")}},
		GenerateVariants: true,
	}); err != nil {
		t.Fatalf("first set: %v", err)
	}
	before, err := app.Products().GetProduct(ctx, p.ID)
	if err != nil {
		t.Fatalf("read product: %v", err)
	}
	axis := before.Options[0]
	redID := axis.Values[0].ID
	var red *Variant
	for i := range before.Variants {
		if len(before.Variants[i].Options) == 1 {
			red = &before.Variants[i]
		}
	}
	if red == nil {
		t.Fatal("no Red variant to test with")
	}

	_, change, err := app.Products().SetOptions(ctx, p.ID, OptionSet{
		Options: []OptionSpec{{ID: &axis.ID, Name: "Colour", Values: []OptionValueSpec{
			{ID: &redID, Value: "Crimson"},
			{Value: "Red"},
		}}},
		// Deliberately not generating: the freed-up name would mint a SKU the
		// renamed variant is still holding, and the engine refusing that is a
		// different lesson, pinned by the generation tests above.
	})
	if err != nil {
		t.Fatalf("rename and reuse: %v", err)
	}
	if len(change.ValuesRenamed) != 1 {
		t.Errorf("values renamed = %v, want the one rename", change.ValuesRenamed)
	}
	if len(change.ValuesAdded) != 1 || !strings.Contains(change.ValuesAdded[0], "Red") {
		t.Errorf("values added = %v, want the reused name to count as new", change.ValuesAdded)
	}
	if len(change.ValuesRemoved) != 0 {
		t.Errorf("values removed = %v, want none", change.ValuesRemoved)
	}
	after, err := app.Products().GetVariant(ctx, red.ID)
	if err != nil {
		t.Fatalf("the original variant did not survive: %v", err)
	}
	if len(after.Options) != 1 || after.Options[0] != "Crimson" {
		t.Errorf("the variant follows the id, not the name: options = %v", after.Options)
	}
}

// Every client written before value ids sends plain strings, and a values list
// of bare strings has to keep meaning exactly what it always did.
func TestSetOptionsAcceptsBareStringValuesOverHTTP(t *testing.T) {
	app := newTestApp(t)
	p := optionsProduct(t, app, "HTTP-VAL-1")
	path := "/api/admin/products/" + strconv.FormatInt(p.ID, 10) + "/options"

	rec := do(t, app, "PUT", path, withAdmin, jsonBody(t, map[string]any{
		"options":           []map[string]any{{"name": "Colour", "values": []string{"Red", "Blue"}}},
		"generate_variants": true,
	}))
	if rec.Code != 200 {
		t.Fatalf("bare string values = %d: %s", rec.Code, rec.Body)
	}

	fresh, err := app.Products().GetProduct(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("read product: %v", err)
	}
	axis := fresh.Options[0]
	if len(axis.Values) != 2 {
		t.Fatalf("axis has %d values, want 2", len(axis.Values))
	}

	// And the object form, with the ids the read just handed back, renames.
	rec = do(t, app, "PUT", path, withAdmin, jsonBody(t, map[string]any{
		"options": []map[string]any{{
			"id":   axis.ID,
			"name": "Colour",
			"values": []map[string]any{
				{"id": axis.Values[0].ID, "value": "Crimson"},
				{"id": axis.Values[1].ID, "value": "Blue"},
			},
		}},
	}))
	if rec.Code != 200 {
		t.Fatalf("value ids = %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "values_renamed") {
		t.Errorf("response does not report the rename: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "variants_removed\":[\"") {
		t.Errorf("the rename deleted variants: %s", rec.Body.String())
	}
}

// The unmarshaller takes both shapes, and this is the one place that is pinned
// without a database in the way.
func TestOptionValueSpecUnmarshalling(t *testing.T) {
	var set OptionSet
	if err := json.Unmarshal([]byte(`{"options":[
		{"name":"Colour","values":["Red",{"id":7,"value":"Blue"},{"value":"Green"}]}
	]}`), &set); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	values := set.Options[0].Values
	if len(values) != 3 {
		t.Fatalf("got %d values, want 3", len(values))
	}
	if values[0].ID != nil || values[0].Value != "Red" {
		t.Errorf("a bare string should be a value with no id: %+v", values[0])
	}
	if values[1].ID == nil || *values[1].ID != 7 || values[1].Value != "Blue" {
		t.Errorf("an object should keep its id: %+v", values[1])
	}
	if values[2].ID != nil || values[2].Value != "Green" {
		t.Errorf("an object with no id is a new value: %+v", values[2])
	}
}
