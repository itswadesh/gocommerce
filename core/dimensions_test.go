package gocommerce

import "testing"

func TestNormalizeDimensionUnit(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"", DimensionMillimetre},
		{"mm", DimensionMillimetre},
		{"millimetre", DimensionMillimetre},
		{"cm", DimensionCentimetre},
		{" CM ", DimensionCentimetre},
		{"centimeters", DimensionCentimetre},
		{"m", DimensionMetre},
		{"metres", DimensionMetre},
		{"in", DimensionInch},
		{"inches", DimensionInch},
		{`"`, DimensionInch},
	} {
		got, err := NormalizeDimensionUnit(tc.in)
		if err != nil {
			t.Errorf("NormalizeDimensionUnit(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeDimensionUnit(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// A unit nobody stores must be refused rather than silently read as
	// millimetres: quietly taking "12 ft" as 12 mm understates a parcel by a
	// factor of three hundred, and a carrier quote built on it is wrong in a
	// way nothing downstream can detect.
	if _, err := NormalizeDimensionUnit("furlongs"); err == nil {
		t.Error("NormalizeDimensionUnit(\"furlongs\") = nil error, want a validation error")
	}
}

func TestMillimetresFrom(t *testing.T) {
	for _, tc := range []struct {
		value float64
		unit  string
		want  int
	}{
		{0, DimensionMillimetre, 0},
		{450, DimensionMillimetre, 450},
		{2.5, DimensionCentimetre, 25},
		{1, DimensionMetre, 1000},
		// The inch is 25.4 mm by definition, and the column stores whole
		// millimetres, so it rounds to 25 rather than truncating to 25.
		{1, DimensionInch, 25},
		{12, DimensionInch, 305},
	} {
		got, err := MillimetresFrom(tc.value, tc.unit)
		if err != nil {
			t.Errorf("MillimetresFrom(%v, %q): %v", tc.value, tc.unit, err)
			continue
		}
		if got != tc.want {
			t.Errorf("MillimetresFrom(%v, %q) = %d, want %d", tc.value, tc.unit, got, tc.want)
		}
	}

	if _, err := MillimetresFrom(-1, DimensionCentimetre); err == nil {
		t.Error("a negative dimension was accepted; want a validation error")
	}
	if _, err := MillimetresFrom(1e12, DimensionMetre); err == nil {
		t.Error("an overflowing dimension was accepted; want a validation error")
	}
}

func TestFormatDimension(t *testing.T) {
	for _, tc := range []struct {
		mm   int
		unit string
		want string
	}{
		{450, DimensionMillimetre, "450 mm"},
		{25, DimensionCentimetre, "2.5 cm"},
		{300, DimensionCentimetre, "30 cm"},
		{1000, DimensionMetre, "1 m"},
		{305, DimensionInch, "12.008 in"},
	} {
		if got := FormatDimension(tc.mm, tc.unit); got != tc.want {
			t.Errorf("FormatDimension(%d, %q) = %q, want %q", tc.mm, tc.unit, got, tc.want)
		}
	}
}

func TestResolveDimensions(t *testing.T) {
	mm := func(v int) *int { return &v }
	f := func(v float64) *float64 { return &v }

	// The form's shape: three values plus the unit they were typed in.
	got, unit, err := resolveDimensions(
		Dimensions{}, DimensionValues{Length: f(30), Width: f(20), Height: f(45)}, "cm")
	if err != nil {
		t.Fatalf("resolveDimensions: %v", err)
	}
	if unit != DimensionCentimetre {
		t.Errorf("unit = %q, want cm", unit)
	}
	if got.Length == nil || *got.Length != 300 || got.Width == nil || *got.Width != 200 ||
		got.Height == nil || *got.Height != 450 {
		t.Errorf("resolveDimensions = %+v, want 300/200/450 mm", got)
	}

	// The API's shape: millimetres directly.
	got, unit, err = resolveDimensions(
		Dimensions{Length: mm(300), Width: mm(200), Height: mm(450)}, DimensionValues{}, "cm")
	if err != nil {
		t.Fatalf("resolveDimensions: %v", err)
	}
	if unit != DimensionCentimetre || got.Length == nil || *got.Length != 300 {
		t.Errorf("resolveDimensions = %+v unit %q, want 300mm in cm", got, unit)
	}

	// Both. The typed value wins, for the reason resolveWeight gives: a caller
	// that sent both has contradicted itself, and the human-entered number is
	// the one a person can check.
	got, _, err = resolveDimensions(
		Dimensions{Length: mm(999)}, DimensionValues{Length: f(30)}, "cm")
	if err != nil {
		t.Fatalf("resolveDimensions: %v", err)
	}
	if got.Length == nil || *got.Length != 300 {
		t.Errorf("length = %v, want the typed 30 cm as 300 mm", got.Length)
	}

	// No dimensions at all. The unit still travels, so a variant measured later
	// is read back in the unit the operator already chose.
	got, unit, err = resolveDimensions(Dimensions{}, DimensionValues{}, "in")
	if err != nil {
		t.Fatalf("resolveDimensions: %v", err)
	}
	if unit != DimensionInch {
		t.Errorf("unit = %q, want in", unit)
	}
	if got.Length != nil || got.Width != nil || got.Height != nil {
		t.Errorf("resolveDimensions = %+v, want all nil", got)
	}
}
