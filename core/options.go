package gocommerce

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Editing the option matrix.
//
// The existing AddOption appends one axis and nothing can change it afterwards,
// which is fine for an API and useless for an editor: a person renaming "Size"
// or dropping "XL" is doing one thing, and doing it as four calls leaves the
// product briefly incoherent between them.
//
// SetOptions takes the whole matrix and reconciles the variants to match, in
// one transaction. That is the shape the admin UI actually needs, and it is
// also the honest one — the option set and the variants that depend on it are a
// single fact, so they should change together or not at all.

// OptionSpec is one axis in a desired matrix.
//
// ID is what makes a rename possible. Matched by name alone, renaming "Size"
// to "Größe" is indistinguishable from deleting one axis and adding another —
// and the engine would dutifully strip every variant of its size and collapse
// them all onto the same empty combination. Sending the id says "this is the
// same axis, under a new name"; omitting it says "this one is new".
type OptionSpec struct {
	ID     *int64            `json:"id"`
	Name   string            `json:"name"`
	Values []OptionValueSpec `json:"values"`
}

// OptionValueSpec is one value on an axis in a desired matrix.
//
// ID does for a value exactly what OptionSpec.ID does for the axis above it,
// and for the same reason. Matched by text alone, changing "Red" to "Crimson"
// is indistinguishable from dropping one value and adding another — so every
// Red variant is deleted and a fresh Crimson one minted in its place, taking
// that variant's price, SKU, image and stock with it. A typo in a colour name
// cost the whole size/colour matrix. Sending the id says "this is the same
// value, under a new name"; omitting it says "this one is new".
//
// It unmarshals from a bare JSON string too, because that is what every client
// written before value ids existed sends, and `["S", "M"]` still means exactly
// what it always did: two values, neither of them claiming an identity.
type OptionValueSpec struct {
	ID    *int64 `json:"id"`
	Value string `json:"value"`
}

func (v *OptionValueSpec) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return err
		}
		v.ID, v.Value = nil, value
		return nil
	}
	// A named type with the same fields and no methods: unmarshalling into
	// OptionValueSpec itself here would call this function again, for ever.
	type plain OptionValueSpec
	var out plain
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return err
	}
	*v = OptionValueSpec(out)
	return nil
}

// OptionValues names values that have no identity yet — the whole matrix of a
// product being given options for the first time, where every value is new.
// An edit that means to keep what is there sends the ids it read instead.
func OptionValues(values ...string) []OptionValueSpec {
	out := make([]OptionValueSpec, 0, len(values))
	for _, value := range values {
		out = append(out, OptionValueSpec{Value: value})
	}
	return out
}

// OptionSet is the desired option matrix for a product.
type OptionSet struct {
	Options []OptionSpec `json:"options"`
	// GenerateVariants creates a variant for every combination that does not
	// have one yet. Off by default: adding an axis to a catalog of 40 products
	// should not quietly mint 200 sellable SKUs at a guessed price.
	GenerateVariants bool `json:"generate_variants"`
	// PriceMinor is the price for generated variants. Zero means "copy the
	// product's existing default", which is nearly always what was meant.
	PriceMinor *int64 `json:"price_minor"`
}

// OptionChange reports what SetOptions did, so an operator can be told rather
// than left to discover it.
type OptionChange struct {
	AxesAdded   []string `json:"axes_added"`
	AxesRemoved []string `json:"axes_removed"`
	AxesRenamed []string `json:"axes_renamed"`
	ValuesAdded []string `json:"values_added"`
	// ValuesRenamed is reported apart from the other two on purpose: a rename
	// keeps every variant that held the value, and an operator reading "Dropped
	// Size: Red / New values: Size: Crimson" would reasonably conclude they had
	// just destroyed their stock.
	ValuesRenamed   []string `json:"values_renamed"`
	ValuesRemoved   []string `json:"values_removed"`
	VariantsCreated []string `json:"variants_created"`
	VariantsRemoved []string `json:"variants_removed"`
}

// SetOptions replaces a product's option axes and reconciles its variants.
//
// The rules, in the order they matter:
//
//   - A variant whose combination still exists is left completely alone. Its
//     price, SKU and stock survive a rename of the axis above it, because
//     nothing about the thing being sold changed.
//   - A variant whose combination no longer exists is deleted. Order lines keep
//     their own snapshot and merely lose the reference, so history stays
//     readable; cart lines cascade, because a line nobody can buy should not
//     block the operator.
//   - New combinations are created only when asked for.
func (c *Catalog) SetOptions(ctx context.Context, productID int64, in OptionSet) (*Product, *OptionChange, error) {
	for i := range in.Options {
		in.Options[i].Name = strings.TrimSpace(in.Options[i].Name)
		if in.Options[i].Name == "" {
			return nil, nil, Validationf("every option needs a name")
		}
		in.Options[i].Values = normalizeOptionValues(in.Options[i].Values)
		if len(in.Options[i].Values) == 0 {
			return nil, nil, Validationf("option %q has no values", in.Options[i].Name)
		}
	}
	if err := checkAxisNames(in.Options); err != nil {
		return nil, nil, err
	}
	if err := checkSpecIDs(in.Options); err != nil {
		return nil, nil, err
	}
	// The engine resolves a variant's options by value, so the same value on
	// two axes is ambiguous by construction — "Small" as both a Size and a Cup
	// cannot be told apart. Refusing is the only honest answer until the
	// resolver keys on (axis, value).
	if err := checkValuesUniqueAcrossAxes(in.Options); err != nil {
		return nil, nil, err
	}

	change := &OptionChange{}
	err := InTx(ctx, c.app.db, func(tx *sql.Tx) error {
		before, err := loadOptionMatrix(ctx, tx, productID)
		if err != nil {
			return err
		}
		if before == nil {
			return NotFoundf("product %d not found", productID)
		}

		// Every existing variant's combination, keyed by the *axis id* it came
		// from. Names change; ids do not, which is what lets a rename keep its
		// variants.
		existing, err := loadVariantCombinations(ctx, tx, productID)
		if err != nil {
			return err
		}

		// wanted maps each surviving axis id to the values it keeps, and
		// axisSpec to that axis's place in the request — which is what the
		// re-link below looks its new value rows up by. An axis with no id is
		// new and has no variants pointing at it yet.
		wanted := map[int64][]OptionValueSpec{}
		axisSpec := map[int64]int{}
		for i, o := range in.Options {
			if o.ID == nil {
				change.AxesAdded = append(change.AxesAdded, o.Name)
				continue
			}
			prev, known := before[*o.ID]
			if !known {
				return Validationf("this product has no option %d", *o.ID)
			}
			// A value id this axis never had is a stale read, not a rename, and
			// honouring it would re-point somebody else's variants. Refused for
			// the same reason an unknown axis id is.
			for _, value := range o.Values {
				if value.ID != nil && !prev.has(*value.ID) {
					return Validationf("option %q has no value %d", o.Name, *value.ID)
				}
			}
			wanted[*o.ID] = o.Values
			axisSpec[*o.ID] = i
			if !strings.EqualFold(prev.Name, o.Name) {
				change.AxesRenamed = append(change.AxesRenamed, prev.Name+" → "+o.Name)
			}
			added, renamed, removed := diffValues(prev.Values, o.Values)
			for _, v := range added {
				change.ValuesAdded = append(change.ValuesAdded, o.Name+": "+v)
			}
			for _, v := range renamed {
				change.ValuesRenamed = append(change.ValuesRenamed, o.Name+": "+v)
			}
			for _, v := range removed {
				change.ValuesRemoved = append(change.ValuesRemoved, o.Name+": "+v)
			}
		}
		for id, prev := range before {
			if _, keep := wanted[id]; !keep {
				change.AxesRemoved = append(change.AxesRemoved, prev.Name)
			}
		}

		// Order matters here, and the database enforces it:
		// variant_option_values references option values with ON DELETE
		// RESTRICT, so the axes cannot be dropped while any variant still
		// points at them. Retire the doomed variants, release the survivors'
		// references, and only then rebuild.

		// 1. Decide each existing variant's fate by whether its combination
		//    still exists in the new matrix.
		//
		//    Dropping an axis can also collapse two survivors onto each other:
		//    take Colour away from Size x Colour and both S/Red and S/Blue
		//    become plain S. They cannot both be kept — a combination is unique
		//    per product, in the unique index as much as in the shop — so the
		//    first in the catalog's own order keeps its price, SKU and stock
		//    and the rest are removed, reported like any other variant this
		//    edit takes with it. Merging beats refusing: otherwise an axis
		//    could never be deleted from a product that has variants, which is
		//    every product an axis is worth deleting from.
		var keep []variantCombination
		// Which value each survivor lands on, by axis id and by the value's
		// place in the request. Worked out before the matrix is torn down,
		// because it is the old rows that say which value the variant held.
		landing := map[int64]map[int64]int{}
		claimed := map[string]bool{}
		for _, v := range existing {
			if resolved, survives := resolveCombination(v, wanted); survives {
				key := survivingKey(resolved)
				if !claimed[key] {
					claimed[key] = true
					keep = append(keep, v)
					landing[v.ID] = resolved
					continue
				}
			}
			change.VariantsRemoved = append(change.VariantsRemoved, v.SKU)
			if _, err := tx.ExecContext(ctx, `DELETE FROM variants WHERE id = $1`, v.ID); err != nil {
				return Internalf(err, "remove variant %s", v.SKU)
			}
		}

		// 2. Release the survivors' references so the old values are free.
		for _, v := range keep {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM variant_option_values WHERE variant_id = $1`, v.ID); err != nil {
				return Internalf(err, "clear variant options")
			}
		}

		// 3. Now the axes can go, and the new matrix take their place.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM product_options WHERE product_id = $1`, productID); err != nil {
			return Internalf(err, "clear options")
		}
		valueIDs, err := insertOptionMatrix(ctx, tx, productID, in.Options)
		if err != nil {
			return err
		}

		// 4. Re-point the survivors at the newly inserted value rows. Their
		//    price, SKU and stock were never touched — only the ids beneath
		//    them moved.
		for _, v := range keep {
			var ids []int64
			for axisID, valueIndex := range landing[v.ID] {
				// By position in the request rather than by text: a renamed
				// value has no row under its old name to look up, and that
				// miss is what used to hand back a zero id and break the
				// foreign key.
				ids = append(ids, valueIDs[axisSpec[axisID]][valueIndex])
			}
			for _, id := range ids {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO variant_option_values (variant_id, option_value_id) VALUES ($1, $2)`,
					v.ID, id); err != nil {
					return Internalf(err, "re-link variant options")
				}
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE variants SET option_key = $2, updated_at = now() WHERE id = $1`,
				v.ID, optionKeyFor(ids)); err != nil {
				return Internalf(err, "update option key")
			}
		}

		if in.GenerateVariants {
			created, err := generateMissingVariants(ctx, tx, productID, in, valueIDs)
			if err != nil {
				return err
			}
			change.VariantsCreated = created
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	p, err := c.GetProduct(ctx, productID)
	if err != nil {
		return nil, nil, err
	}
	return p, change, nil
}

// ------------------------------------------------------------------ helpers

type variantCombination struct {
	ID    int64
	SKU   string
	Price int64
	// Keyed by option (axis) id, because names are what a rename changes.
	ByAxis map[int64]variantValue
}

// variantValue is the value a variant holds on one axis: the row it points at
// and the text on that row. The id is what survives a rename; the text is what
// a client that sent no ids can still be matched on.
type variantValue struct {
	ID    int64
	Value string
}

// axisState is an axis as it exists now.
type axisState struct {
	Name   string
	Values []optionValueState
}

// optionValueState is one of that axis's value rows as it exists now.
type optionValueState struct {
	ID    int64
	Value string
}

func (a *axisState) has(valueID int64) bool {
	for _, v := range a.Values {
		if v.ID == valueID {
			return true
		}
	}
	return false
}

// normalizeOptionValues trims, drops the blanks and folds duplicates.
//
// The first spelling wins, and it keeps that spelling's id with it: "Red, red"
// is one value, and which of the two rows the variants are re-pointed at has to
// be decided here rather than by whichever loop happens to look first.
func normalizeOptionValues(in []OptionValueSpec) []OptionValueSpec {
	seen := map[string]bool{}
	out := make([]OptionValueSpec, 0, len(in))
	for _, v := range in {
		v.Value = strings.TrimSpace(v.Value)
		if v.Value == "" {
			continue
		}
		if k := strings.ToLower(v.Value); !seen[k] {
			seen[k] = true
			out = append(out, v)
		}
	}
	return out
}

// checkSpecIDs refuses two specs claiming one identity — the same axis id on
// two axes, or the same value id twice on one axis.
//
// Malformed either way, and resolving it silently is the bad outcome: the
// second spec would quietly replace the first in every lookup keyed by that id,
// so a variant would be re-pointed at whichever of the two the loop reached
// last, under a name nobody asked for.
func checkSpecIDs(opts []OptionSpec) error {
	axes := map[int64]bool{}
	for _, o := range opts {
		if o.ID != nil {
			if axes[*o.ID] {
				return Validationf("two options both claim to be option %d", *o.ID)
			}
			axes[*o.ID] = true
		}
		seen := map[int64]bool{}
		for _, v := range o.Values {
			if v.ID == nil {
				continue
			}
			if seen[*v.ID] {
				return Validationf("option %q names value %d twice", o.Name, *v.ID)
			}
			seen[*v.ID] = true
		}
	}
	return nil
}

func checkAxisNames(opts []OptionSpec) error {
	seen := map[string]bool{}
	for _, o := range opts {
		k := strings.ToLower(o.Name)
		if seen[k] {
			return Conflictf("two options are both called %q", o.Name)
		}
		seen[k] = true
	}
	return nil
}

// checkValuesUniqueAcrossAxes is the guard for a real hole in the resolver:
// variant options are matched by value alone, so "Small" on two axes resolves
// ambiguously and silently. Until that is keyed on (axis, value), the only
// correct behaviour is to refuse the input rather than accept it and be wrong.
func checkValuesUniqueAcrossAxes(opts []OptionSpec) error {
	owner := map[string]string{}
	for _, o := range opts {
		for _, v := range o.Values {
			k := strings.ToLower(v.Value)
			if prev, clash := owner[k]; clash && prev != o.Name {
				return Conflictf(
					"%q is a value on both %q and %q; a variant's options are matched by value, so the two could not be told apart",
					v.Value, prev, o.Name)
			}
			owner[k] = o.Name
		}
	}
	return nil
}

// diffValues reports what one axis's edit did to its values.
//
// Matching is by id first and text second, which is the whole point: a spec
// carrying id 7 with the text "Crimson" is the row that used to say "Red" being
// renamed, and reporting that as a removal plus an addition would describe a
// destruction that did not happen. A spec with no id is a client that has never
// heard of value ids, and for those this is exactly the text comparison it
// always was.
func diffValues(before []optionValueState, after []OptionValueSpec) (added, renamed, removed []string) {
	byID := map[int64]optionValueState{}
	for _, v := range before {
		byID[v.ID] = v
	}

	kept := map[int64]bool{}
	for _, spec := range after {
		if spec.ID == nil {
			continue
		}
		prev, known := byID[*spec.ID]
		if !known {
			continue
		}
		kept[prev.ID] = true
		if !strings.EqualFold(prev.Value, spec.Value) {
			renamed = append(renamed, prev.Value+" → "+spec.Value)
		}
	}

	// An id-less spec matches by text, but only against a value no id has
	// already claimed: with "Red" renamed to "Crimson" and a fresh "Red" added
	// beside it, the fresh one is genuinely new.
	claimed := map[string]bool{}
	for _, spec := range after {
		if spec.ID != nil {
			continue
		}
		text := strings.ToLower(spec.Value)
		match := false
		for _, prev := range before {
			if !kept[prev.ID] && !claimed[strings.ToLower(prev.Value)] &&
				strings.EqualFold(prev.Value, spec.Value) {
				match = true
				claimed[text] = true
				break
			}
		}
		if !match {
			added = append(added, spec.Value)
		}
	}

	for _, prev := range before {
		if !kept[prev.ID] && !claimed[strings.ToLower(prev.Value)] {
			removed = append(removed, prev.Value)
		}
	}
	return added, renamed, removed
}

// resolveValue returns where in the new matrix a value a variant holds lands.
//
// By id before text, because that is what tells a rename from a replacement.
// Text is the fallback for a client that sent plain strings, and for a value
// that was dropped and re-added under the same name — both of which mean the
// variant is still selling the same thing.
func resolveValue(specs []OptionValueSpec, held variantValue) (int, bool) {
	for i, spec := range specs {
		if spec.ID != nil && *spec.ID == held.ID {
			return i, true
		}
	}
	for i, spec := range specs {
		if strings.EqualFold(spec.Value, held.Value) {
			return i, true
		}
	}
	return 0, false
}

// resolveCombination maps a variant onto the new matrix: for each axis that
// survives, which of that axis's values it now holds, by position in the
// request. ok is false when any surviving axis no longer offers the value it
// was holding, which is the variant being deleted.
//
// An axis that is going is simply skipped. The variant survives on its
// remaining axes; whether that leaves it identical to another survivor is
// survivingKey's question, not this one's.
func resolveCombination(v variantCombination, wanted map[int64][]OptionValueSpec) (map[int64]int, bool) {
	out := make(map[int64]int, len(v.ByAxis))
	for axisID, held := range v.ByAxis {
		specs, still := wanted[axisID]
		if !still {
			continue
		}
		at, found := resolveValue(specs, held)
		if !found {
			return nil, false
		}
		out[axisID] = at
	}
	return out, true
}

// survivingKey is the combination a variant will hold once the edit is applied:
// where it lands on each axis that stays, keyed by axis id.
//
// Keyed by the value's *position* in the new matrix rather than by its text,
// because normalizeOptionValues has already folded "Red" and "red" into one row
// — two variants holding them separately land on the same position, and this
// key is what has to predict that before the unique index does.
func survivingKey(resolved map[int64]int) string {
	axes := make([]int64, 0, len(resolved))
	for axisID := range resolved {
		axes = append(axes, axisID)
	}
	sort.Slice(axes, func(i, j int) bool { return axes[i] < axes[j] })
	parts := make([]string, len(axes))
	for i, axisID := range axes {
		parts[i] = fmt.Sprintf("%d=%d", axisID, resolved[axisID])
	}
	return strings.Join(parts, ",")
}

func loadOptionMatrix(ctx context.Context, tx *sql.Tx, productID int64) (map[int64]*axisState, error) {
	var exists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM products WHERE id = $1)`, productID).Scan(&exists); err != nil {
		return nil, Internalf(err, "check product")
	}
	if !exists {
		return nil, nil
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT o.id, o.name, v.id, v.value
		FROM product_options o
		LEFT JOIN product_option_values v ON v.option_id = o.id
		WHERE o.product_id = $1
		ORDER BY o.position, o.id, v.position, v.id`, productID)
	if err != nil {
		return nil, Internalf(err, "read options")
	}
	defer rows.Close()

	out := map[int64]*axisState{}
	for rows.Next() {
		var id int64
		var name string
		var valueID sql.NullInt64
		var value sql.NullString
		if err := rows.Scan(&id, &name, &valueID, &value); err != nil {
			return nil, Internalf(err, "scan option")
		}
		if _, ok := out[id]; !ok {
			out[id] = &axisState{Name: name}
		}
		if valueID.Valid && value.Valid {
			out[id].Values = append(out[id].Values,
				optionValueState{ID: valueID.Int64, Value: value.String})
		}
	}
	return out, rows.Err()
}

func loadVariantCombinations(ctx context.Context, tx *sql.Tx, productID int64) ([]variantCombination, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT v.id, v.sku, v.price_minor, o.id, ov.id, ov.value
		FROM variants v
		LEFT JOIN variant_option_values vov ON vov.variant_id = v.id
		LEFT JOIN product_option_values ov ON ov.id = vov.option_value_id
		LEFT JOIN product_options o ON o.id = ov.option_id
		WHERE v.product_id = $1
		ORDER BY v.position, v.id`, productID)
	if err != nil {
		return nil, Internalf(err, "read variants")
	}
	defer rows.Close()

	byID := map[int64]*variantCombination{}
	var order []int64
	for rows.Next() {
		var (
			id      int64
			sku     string
			price   int64
			axisID  sql.NullInt64
			valueID sql.NullInt64
			value   sql.NullString
		)
		if err := rows.Scan(&id, &sku, &price, &axisID, &valueID, &value); err != nil {
			return nil, Internalf(err, "scan variant")
		}
		v, ok := byID[id]
		if !ok {
			v = &variantCombination{ID: id, SKU: sku, Price: price, ByAxis: map[int64]variantValue{}}
			byID[id] = v
			order = append(order, id)
		}
		if axisID.Valid && valueID.Valid && value.Valid {
			v.ByAxis[axisID.Int64] = variantValue{ID: valueID.Int64, Value: value.String}
		}
	}
	out := make([]variantCombination, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, rows.Err()
}

// insertOptionMatrix writes the axes and returns the new option_value ids, in
// the shape of the request: ids[axis index][value index].
//
// By position rather than by name, because a renamed value has no name in
// common with the row it replaces — and that is the lookup the survivors are
// re-pointed through.
func insertOptionMatrix(ctx context.Context, tx *sql.Tx, productID int64, opts []OptionSpec) ([][]int64, error) {
	ids := make([][]int64, len(opts))
	for i, o := range opts {
		var optionID int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO product_options (product_id, name, position)
			VALUES ($1, $2, $3) RETURNING id`, productID, o.Name, i).Scan(&optionID); err != nil {
			if isUniqueViolation(err) {
				return nil, Conflictf("two options are both called %q", o.Name)
			}
			return nil, Internalf(err, "create option %s", o.Name)
		}
		ids[i] = make([]int64, len(o.Values))
		for j, v := range o.Values {
			var valueID int64
			if err := tx.QueryRowContext(ctx, `
				INSERT INTO product_option_values (option_id, value, position)
				VALUES ($1, $2, $3) RETURNING id`, optionID, v.Value, j).Scan(&valueID); err != nil {
				return nil, Internalf(err, "create option value %s", v.Value)
			}
			ids[i][j] = valueID
		}
	}
	return ids, nil
}

// generateMissingVariants mints the combinations that have no variant yet.
func generateMissingVariants(
	ctx context.Context, tx *sql.Tx, productID int64, in OptionSet, valueIDs [][]int64,
) ([]string, error) {
	price := int64(0)
	if in.PriceMinor != nil {
		price = *in.PriceMinor
	} else {
		// Copy whatever the product already sells for; a generated variant at
		// zero is a variant that can be bought for nothing.
		_ = tx.QueryRowContext(ctx,
			`SELECT price_minor FROM variants WHERE product_id = $1 ORDER BY position, id LIMIT 1`,
			productID).Scan(&price)
	}

	var baseSKU string
	if err := tx.QueryRowContext(ctx,
		`SELECT slug FROM products WHERE id = $1`, productID).Scan(&baseSKU); err != nil {
		return nil, Internalf(err, "read product slug")
	}

	counts := make([]int, len(in.Options))
	for i, o := range in.Options {
		counts[i] = len(o.Values)
	}

	var created []string
	for _, combo := range cartesian(counts) {
		ids := make([]int64, len(combo))
		parts := make([]string, len(combo))
		for axis, at := range combo {
			ids[axis] = valueIDs[axis][at]
			parts[axis] = in.Options[axis].Values[at].Value
		}
		key := optionKeyFor(ids)

		var taken bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM variants WHERE product_id = $1 AND option_key = $2)`,
			productID, key).Scan(&taken); err != nil {
			return nil, Internalf(err, "check combination")
		}
		if taken {
			continue
		}

		sku := strings.ToUpper(baseSKU + "-" + strings.Join(parts, "-"))
		sku = strings.ReplaceAll(sku, " ", "-")
		var variantID int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO variants (product_id, sku, price_minor, option_key, position)
			VALUES ($1, $2, $3, $4, (SELECT coalesce(max(position), -1) + 1 FROM variants WHERE product_id = $1))
			RETURNING id`, productID, sku, price, key).Scan(&variantID)
		if err != nil {
			if isUniqueViolation(err) {
				// A SKU collision across the catalog is the operator's to
				// resolve; skipping silently would leave a hole in the matrix
				// they never asked about.
				return nil, Conflictf("cannot generate variant %q: that sku is already used", sku)
			}
			return nil, Internalf(err, "create variant %s", sku)
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO variant_option_values (variant_id, option_value_id) VALUES ($1, $2)`,
				variantID, id); err != nil {
				return nil, Internalf(err, "link generated variant")
			}
		}
		created = append(created, sku)
	}
	return created, nil
}

// cartesian expands the axes into every combination, in axis order: one entry
// per axis holding which of that axis's values this combination takes.
//
// Positions rather than names, because two values on two axes can no longer be
// told apart by their text alone once either of them can be renamed.
func cartesian(counts []int) [][]int {
	out := [][]int{{}}
	for axis, n := range counts {
		var next [][]int
		for _, base := range out {
			for at := 0; at < n; at++ {
				combo := make([]int, len(base), axis+1)
				copy(combo, base)
				next = append(next, append(combo, at))
			}
		}
		out = next
	}
	return out
}

// optionKeyFor builds the sorted, joined key the unique index enforces
// combination uniqueness on.
func optionKeyFor(ids []int64) string {
	sorted := append([]int64(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	parts := make([]string, len(sorted))
	for i, id := range sorted {
		parts[i] = fmt.Sprint(id)
	}
	return strings.Join(parts, ",")
}
