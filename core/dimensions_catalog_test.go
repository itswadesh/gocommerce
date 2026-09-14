package gocommerce

import (
	"context"
	"testing"
)

// A parcel's three sides, through the service that stores them.
//
// The unit tests in dimensions_test.go prove the arithmetic. This proves the
// round trip: that a box typed in centimetres comes back in centimetres, that
// an unmeasured side stays unmeasured rather than becoming zero, and that
// patching one side leaves the others alone — which is the bug a naive
// "write all three columns" patch would ship.
func TestVariantDimensions(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	price := int64(2000)

	p, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Everyday backpack", SKU: "BOX-1", PriceMinor: &price,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	v := p.Variants[0]

	// A fresh variant has measured nothing. Three zeroes here would tell a
	// carrier the parcel is a flat sheet.
	if v.Dimensions.Set() {
		t.Errorf("a fresh variant has dimensions %+v, want none measured", v.Dimensions)
	}
	if v.Size != "" {
		t.Errorf("Size = %q on an unmeasured variant, want empty", v.Size)
	}
	if v.DimensionUnit != DefaultDimensionUnit {
		t.Errorf("DimensionUnit = %q, want the default %q", v.DimensionUnit, DefaultDimensionUnit)
	}

	// The form's shape: typed values plus the unit they were typed in.
	cm := "cm"
	updated, err := app.Products().UpdateVariant(ctx, v.ID, VariantPatch{
		Size: DimensionPatch{
			Length: SetMeasure(30), Width: SetMeasure(20), Height: SetMeasure(45),
		},
		DimensionUnit: &cm,
	})
	if err != nil {
		t.Fatalf("set dimensions: %v", err)
	}
	if updated.Dimensions.Length == nil || *updated.Dimensions.Length != 300 {
		t.Errorf("length = %v mm, want 300", updated.Dimensions.Length)
	}
	if updated.Dimensions.Width == nil || *updated.Dimensions.Width != 200 {
		t.Errorf("width = %v mm, want 200", updated.Dimensions.Width)
	}
	if updated.Dimensions.Height == nil || *updated.Dimensions.Height != 450 {
		t.Errorf("height = %v mm, want 450", updated.Dimensions.Height)
	}
	// The whole point of storing the unit: 30 goes in, 30 comes back.
	if want := "30 × 20 × 45 cm"; updated.Size != want {
		t.Errorf("Size = %q, want %q", updated.Size, want)
	}

	// Patching one side leaves the other two where they were.
	updated, err = app.Products().UpdateVariant(ctx, v.ID, VariantPatch{
		Size: DimensionPatch{Height: SetMeasure(50)},
	})
	if err != nil {
		t.Fatalf("patch height: %v", err)
	}
	if updated.Dimensions.Length == nil || *updated.Dimensions.Length != 300 {
		t.Errorf("length = %v after patching only the height, want 300 kept",
			updated.Dimensions.Length)
	}
	if updated.Dimensions.Height == nil || *updated.Dimensions.Height != 500 {
		t.Errorf("height = %v mm, want 500", updated.Dimensions.Height)
	}
	// The unit was not mentioned, so the box must still read in centimetres
	// rather than falling back to the stored millimetres.
	if want := "30 × 20 × 50 cm"; updated.Size != want {
		t.Errorf("Size = %q, want %q", updated.Size, want)
	}

	// Switching the unit alone restates the same box, it does not resize it.
	in := "in"
	updated, err = app.Products().UpdateVariant(ctx, v.ID, VariantPatch{DimensionUnit: &in})
	if err != nil {
		t.Fatalf("switch unit: %v", err)
	}
	if updated.Dimensions.Length == nil || *updated.Dimensions.Length != 300 {
		t.Errorf("length = %v mm after a unit switch, want the same 300",
			updated.Dimensions.Length)
	}
	if want := "11.811 × 7.874 × 19.685 in"; updated.Size != want {
		t.Errorf("Size = %q, want %q", updated.Size, want)
	}

	// A unit nobody stores is refused rather than silently read as millimetres.
	furlong := "furlongs"
	if _, err := app.Products().UpdateVariant(ctx, v.ID,
		VariantPatch{DimensionUnit: &furlong}); err == nil {
		t.Error("an unknown dimension unit was accepted; want a validation error")
	}

	// And a negative side is refused, before the CHECK constraint has to say so
	// in a message nobody can act on.
	if _, err := app.Products().UpdateVariant(ctx, v.ID,
		VariantPatch{Size: DimensionPatch{Width: SetMeasure(-5)}}); err == nil {
		t.Error("a negative dimension was accepted; want a validation error")
	}

	// An emptied box clears the side back to unmeasured. This is the state a
	// nil pointer cannot express and the reason the patch carries the tri-state:
	// "I never touched the width" and "there is no width" are different facts,
	// and storing zero for the second would tell a carrier the parcel is flat.
	back := "cm"
	cleared, err := app.Products().UpdateVariant(ctx, v.ID, VariantPatch{
		Size:          DimensionPatch{Width: ClearMeasure()},
		DimensionUnit: &back,
	})
	if err != nil {
		t.Fatalf("clear width: %v", err)
	}
	if cleared.Dimensions.Width != nil {
		t.Errorf("width = %v after clearing, want nil", cleared.Dimensions.Width)
	}
	if cleared.Dimensions.Length == nil || *cleared.Dimensions.Length != 300 {
		t.Errorf("length = %v after clearing only the width, want 300 kept",
			cleared.Dimensions.Length)
	}
	if want := "30 × — × 50 cm"; cleared.Size != want {
		t.Errorf("Size = %q, want %q", cleared.Size, want)
	}
}

// A product created with its boxes already measured, which is the import path
// rather than the form path.
func TestCreateVariantWithDimensions(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	mm := func(v int) *int { return &v }
	cm := func(v float64) *float64 { return &v }

	p, err := app.Products().CreateProduct(ctx, ProductInput{
		Title:   "Mug",
		Options: []OptionInput{{Name: "Size", Values: []string{"Small", "Large"}}},
		Variants: []VariantInput{
			{
				SKU: "MUG-BOX-S", PriceMinor: 1500, Options: []string{"Small"},
				Dimensions:    Dimensions{Length: mm(120), Width: mm(90), Height: mm(110)},
				DimensionUnit: "mm",
			},
			{
				SKU: "MUG-BOX-L", PriceMinor: 1900, Options: []string{"Large"},
				// Partly measured: the width is not known yet.
				DimensionValues: DimensionValues{Length: cm(12), Height: cm(11)},
				DimensionUnit:   "cm",
			},
		},
	})
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	if len(p.Variants) != 2 {
		t.Fatalf("got %d variants, want 2", len(p.Variants))
	}

	bySKU := map[string]Variant{}
	for _, v := range p.Variants {
		bySKU[v.SKU] = v
	}

	small, ok := bySKU["MUG-BOX-S"]
	if !ok {
		t.Fatal("the small variant is missing")
	}
	if small.Dimensions.Length == nil || *small.Dimensions.Length != 120 {
		t.Errorf("length = %v, want 120 mm", small.Dimensions.Length)
	}
	if want := "120 × 90 × 110 mm"; small.Size != want {
		t.Errorf("Size = %q, want %q", small.Size, want)
	}

	// A partly measured parcel says which side is missing rather than hiding
	// the two that are known.
	large, ok := bySKU["MUG-BOX-L"]
	if !ok {
		t.Fatal("the large variant is missing")
	}
	if large.Dimensions.Width != nil {
		t.Errorf("width = %v, want nil for an unmeasured side", large.Dimensions.Width)
	}
	if large.Dimensions.Length == nil || *large.Dimensions.Length != 120 {
		t.Errorf("length = %v, want 12 cm as 120 mm", large.Dimensions.Length)
	}
	if want := "12 × — × 11 cm"; large.Size != want {
		t.Errorf("Size = %q, want %q", large.Size, want)
	}
}
