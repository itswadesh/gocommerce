package gocommerce

import (
	"fmt"
	"math"
	"strings"
)

// Package dimensions.
//
// The rule is weight's rule, which is money's rule: one canonical stored value
// plus the unit it is read in. `length_mm`, `width_mm` and `height_mm` are the
// facts — integers, exact, what a carrier API is given — and `dimension_unit`
// is presentation, exactly as a currency code is. See weight.go, which this
// deliberately mirrors rather than paraphrases: two files that each invent
// their own rounding rule will eventually disagree about the same parcel.
//
// One unit for all three sides, not one each. A box is measured in a single
// unit by whoever holds the tape, and three independent units would make
// "30 × 20 × 45" a sentence nobody can read without three more lookups.
//
// Millimetres rather than centimetres as the stored unit, because a millimetre
// is the resolution a carrier's own API takes and an integer count of them
// needs no conversion to be correct.

// The units a dimension may be entered in.
const (
	DimensionMillimetre = "mm"
	DimensionCentimetre = "cm"
	DimensionMetre      = "m"
	DimensionInch       = "in"
)

// DefaultDimensionUnit is what a variant gets when nothing says otherwise.
// Millimetres, because it is the canonical unit and needs no conversion.
const DefaultDimensionUnit = DimensionMillimetre

// mmPer holds the exact definitions. The international inch is 25.4 mm by
// definition, not by measurement, so this is not an approximation to be tidied
// up later.
var mmPer = map[string]float64{
	DimensionMillimetre: 1,
	DimensionCentimetre: 10,
	DimensionMetre:      1000,
	DimensionInch:       25.4,
}

// Dimensions is a parcel's three sides in whole millimetres. A nil side is one
// nobody has measured, which is not the same as zero: a zero-height parcel is a
// carrier error, an unmeasured one is just paperwork still to do.
type Dimensions struct {
	Length *int `json:"length_mm,omitempty"`
	Width  *int `json:"width_mm,omitempty"`
	Height *int `json:"height_mm,omitempty"`
}

// Set reports whether any side has been measured.
func (d Dimensions) Set() bool {
	return d.Length != nil || d.Width != nil || d.Height != nil
}

// DimensionValues is the same three sides as a person typed them, in the unit
// they chose. It is the form's shape; Dimensions is the API's.
type DimensionValues struct {
	Length *float64 `json:"length"`
	Width  *float64 `json:"width"`
	Height *float64 `json:"height"`
}

// ValidDimensionUnit reports whether unit is one the engine stores.
func ValidDimensionUnit(unit string) bool {
	_, ok := mmPer[unit]
	return ok
}

// NormalizeDimensionUnit accepts what a person or an import file is likely to
// write and returns the stored form. An unrecognised unit is an error rather
// than a silent fallback to millimetres: quietly reading "12 ft" as 12 mm
// understates a parcel by a factor of three hundred, and nothing downstream
// can detect it.
func NormalizeDimensionUnit(unit string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "", "mm", "millimetre", "millimetres", "millimeter", "millimeters":
		return DimensionMillimetre, nil
	case "cm", "centimetre", "centimetres", "centimeter", "centimeters":
		return DimensionCentimetre, nil
	case "m", "metre", "metres", "meter", "meters":
		return DimensionMetre, nil
	case "in", "inch", "inches", "\"":
		return DimensionInch, nil
	}
	return "", Validationf("%q is not a dimension unit; use mm, cm, m or in", unit)
}

// MillimetresFrom converts a length in unit into whole millimetres.
//
// It rounds to the nearest millimetre, which is the resolution the column
// stores. An inch is 25.4 mm and becomes 25 — losing 0.4 mm matters to no
// carrier, whereas storing a float that cannot represent 0.1 exactly would
// eventually produce a volume that does not multiply out.
func MillimetresFrom(value float64, unit string) (int, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, Validationf("a dimension must be a number")
	}
	if value < 0 {
		return 0, Validationf("a dimension cannot be negative")
	}
	per, ok := mmPer[unit]
	if !ok {
		return 0, Validationf("%q is not a dimension unit; use mm, cm, m or in", unit)
	}
	mm := math.Round(value * per)
	// An int overflow here would silently wrap into a negative length, which
	// the CHECK constraint would then reject with a baffling message.
	if mm > math.MaxInt32 {
		return 0, Validationf("that dimension is implausible")
	}
	return int(mm), nil
}

// DimensionInUnit converts stored millimetres into the given unit for display.
//
// The result is a float because that is what a person reads: 30 cm, not 300.
// It is never what gets stored — the millimetres are.
func DimensionInUnit(mm int, unit string) float64 {
	per, ok := mmPer[unit]
	if !ok || per == 0 {
		return float64(mm)
	}
	return float64(mm) / per
}

// dimensionDigits renders one stored length in unit, without the unit name.
func dimensionDigits(mm int, unit string) string {
	if unit == DimensionMillimetre || !ValidDimensionUnit(unit) {
		return fmt.Sprintf("%d", mm)
	}
	s := strings.TrimRight(strings.TrimRight(
		fmt.Sprintf("%.3f", DimensionInUnit(mm, unit)), "0"), ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// FormatDimension renders a stored length the way its unit wants to be read.
//
// Millimetres get no decimals because a fractional millimetre is below the
// stored resolution; the others get up to three, with trailing zeros trimmed,
// so 300 mm reads as "30 cm" rather than "30.000 cm".
func FormatDimension(mm int, unit string) string {
	if !ValidDimensionUnit(unit) {
		unit = DimensionMillimetre
	}
	return dimensionDigits(mm, unit) + " " + unit
}

// FormatDimensions renders a whole parcel as "L × W × H unit", or "" when
// nothing has been measured.
//
// A partially measured parcel shows the sides it has and an em dash for the
// ones it does not, rather than hiding the lot: "30 × — × 45 cm" says what is
// missing, where an empty string says only that somebody has not finished.
func FormatDimensions(d Dimensions, unit string) string {
	if !d.Set() {
		return ""
	}
	if !ValidDimensionUnit(unit) {
		unit = DimensionMillimetre
	}
	side := func(mm *int) string {
		if mm == nil {
			return "—"
		}
		return dimensionDigits(*mm, unit)
	}
	return fmt.Sprintf("%s × %s × %s %s", side(d.Length), side(d.Width), side(d.Height), unit)
}

// resolveDimensions turns whatever a caller supplied into what the columns
// store: whole millimetres per side, and the unit to read them in.
//
// A client may send millimetres directly (the API's shape) or values plus a
// unit (a form's shape). When both arrive for a side, the typed value wins —
// a caller that computed millimetres itself and then also sent "30 cm" has
// contradicted itself, and the human-entered number is the one a person can
// check. This is resolveWeight's rule, applied per side.
func resolveDimensions(mm Dimensions, values DimensionValues, unit string) (Dimensions, string, error) {
	normalized, err := NormalizeDimensionUnit(unit)
	if err != nil {
		return Dimensions{}, "", err
	}

	side := func(stored *int, typed *float64) (*int, error) {
		switch {
		case typed != nil:
			v, err := MillimetresFrom(*typed, normalized)
			if err != nil {
				return nil, err
			}
			return &v, nil
		case stored != nil:
			if *stored < 0 {
				return nil, Validationf("a dimension cannot be negative")
			}
			return stored, nil
		}
		// No measurement at all. The unit still travels, so a variant measured
		// later is read in the unit the operator already chose.
		return nil, nil
	}

	var out Dimensions
	if out.Length, err = side(mm.Length, values.Length); err != nil {
		return Dimensions{}, "", err
	}
	if out.Width, err = side(mm.Width, values.Width); err != nil {
		return Dimensions{}, "", err
	}
	if out.Height, err = side(mm.Height, values.Height); err != nil {
		return Dimensions{}, "", err
	}
	return out, normalized, nil
}

// DimensionPatch is the three sides as a patch, in whatever unit the patch
// names: a side left out is one the patch does not mention and is kept, an
// explicit null clears it back to unmeasured, and a number sets it.
//
// Only one shape, unlike the create input's two. A patch that wants exact
// millimetres names "mm" as its unit, which costs a caller nothing and means
// there is one answer to "what does this side say" rather than two that can
// disagree. The three states are the reason it is not just *float64: emptying a
// box and never touching it are different intentions, and a nil pointer cannot
// tell them apart — the distinction NullableAmount exists for on a cost.
type DimensionPatch struct {
	Length NullableFloat64 `json:"length"`
	Width  NullableFloat64 `json:"width"`
	Height NullableFloat64 `json:"height"`
}

// Mentioned reports whether the patch speaks about any side at all.
func (p DimensionPatch) Mentioned() bool {
	return p.Length.Present || p.Width.Present || p.Height.Present
}

// DimensionColumn pairs a stored column with what a patch says about it. The
// column name travels with the value so the caller writes them in a fixed
// order — a map here would produce a different SET clause on every save, which
// is the kind of thing that makes a slow query log unreadable.
type DimensionColumn struct {
	Column string
	// MM is the value to store, or nil to clear the side.
	MM *int
}

// resolveDimensionPatch turns a patch into the columns to write, plus the unit
// to read them back in.
//
// It returns only the sides the patch mentioned, which is what keeps a change
// to the height from erasing a length somebody measured earlier.
func resolveDimensionPatch(patch DimensionPatch, unit string) ([]DimensionColumn, string, error) {
	normalized, err := NormalizeDimensionUnit(unit)
	if err != nil {
		return nil, "", err
	}

	var out []DimensionColumn
	for _, side := range []struct {
		column string
		value  NullableFloat64
	}{
		{"length_mm", patch.Length},
		{"width_mm", patch.Width},
		{"height_mm", patch.Height},
	} {
		if !side.value.Present {
			continue
		}
		if side.value.Value == nil {
			out = append(out, DimensionColumn{Column: side.column})
			continue
		}
		mm, err := MillimetresFrom(*side.value.Value, normalized)
		if err != nil {
			return nil, "", err
		}
		out = append(out, DimensionColumn{Column: side.column, MM: &mm})
	}
	return out, normalized, nil
}
