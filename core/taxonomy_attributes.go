package gocommerce

import (
	"bufio"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// The fields Shopify's taxonomy asks of a product, per category.
//
// Built from the same release as shopify-categories.txt, out of the two other
// files Shopify publishes with it. Those two are 95MB between them and nearly
// all of it is repetition: every category carries the full text of every
// attribute it uses, and every attribute the full text of its values. Here each
// is written once, which is the same information in a twentieth of the space.
//
// Like the tree, it ships embedded rather than downloaded — an import that
// reaches the network fails in an air-gapped deployment and drifts between two
// installs of the same version — and nothing imports it automatically.

//go:embed taxonomy/shopify-category-attributes.txt
var shopifyCategoryAttributes string

// ShopifyCategoryAttributes returns the embedded attribute file, in two
// sections: the attribute dictionary, then one line per category.
func ShopifyCategoryAttributes() string { return shopifyCategoryAttributes }

// CategoryAttribute is one field a category asks of a product.
//
// Key and Label are the category's own, out of its metadata. Choices comes from
// the shared table and is filled in on the way out — see attachAttributes. No
// choices is a free-text field rather than a broken one: a store may declare a
// field nobody has published a value list for.
type CategoryAttribute struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Choices []string `json:"choices"`
}

// TaxonomyAttributeImport reports what an attribute import did.
type TaxonomyAttributeImport struct {
	// Attributes counts rows written to the shared dictionary. Categories
	// counts categories matched by taxonomy id and given their field list.
	// Unmatched counts lines naming a category this store does not have, which
	// is the ordinary case when the tree came from a different release or only
	// part of it was kept.
	Attributes int `json:"attributes"`
	Categories int `json:"categories"`
	Unmatched  int `json:"unmatched"`
	Skipped    int `json:"skipped"`
}

type attributeDef struct {
	Handle  string   `json:"handle"`
	Label   string   `json:"label"`
	Choices []string `json:"choices"`
}

// parseCategoryAttributes reads the two-section format:
//
//	handle = Label : value | value | ...
//	gid://shopify/TaxonomyCategory/x : handle , handle , ...
//
// Which kind a line is comes from which separator it has, not from which
// section it appears in, so a stray blank line cannot silently reinterpret the
// rest of the file. A malformed line is skipped and counted rather than fatal,
// for the reason the category parser gives: the file is data, and one bad row
// should not cost the operator the other fourteen thousand.
func parseCategoryAttributes(r io.Reader) ([]attributeDef, map[string][]string, int, error) {
	var (
		defs    []attributeDef
		seen    = map[string]int{}
		byGID   = map[string][]string{}
		skipped int
	)
	scanner := bufio.NewScanner(r)
	// One attribute's values run to a few kilobytes. A megabyte is generous,
	// and set explicitly because the failure it prevents — a truncated line —
	// would otherwise be silent.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if handle, rest, ok := strings.Cut(line, " = "); ok {
			label, values, _ := strings.Cut(rest, " : ")
			handle, label = strings.TrimSpace(handle), strings.TrimSpace(label)
			if handle == "" || label == "" {
				skipped++
				continue
			}
			def := attributeDef{Handle: handle, Label: label, Choices: []string{}}
			for _, v := range strings.Split(values, "|") {
				if v = strings.TrimSpace(v); v != "" {
					def.Choices = append(def.Choices, v)
				}
			}
			// Later wins, and the earlier row is replaced rather than appended.
			// The upsert below cannot touch one row twice in a statement, so a
			// file with a repeated handle would fail the whole import.
			if at, dup := seen[handle]; dup {
				defs[at] = def
			} else {
				seen[handle] = len(defs)
				defs = append(defs, def)
			}
			continue
		}

		gid, rest, ok := strings.Cut(line, " : ")
		if !ok {
			skipped++
			continue
		}
		handles := []string{}
		for _, h := range strings.Split(rest, ",") {
			if h = strings.TrimSpace(h); h != "" {
				handles = append(handles, h)
			}
		}
		if gid = strings.TrimSpace(gid); gid == "" || len(handles) == 0 {
			skipped++
			continue
		}
		byGID[gid] = handles
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, skipped, Internalf(err, "read the attribute file")
	}
	return defs, byGID, skipped, nil
}

// ImportCategoryAttributes loads the field definitions and attaches them to
// categories already in the tree.
//
// It matches on `metadata.taxonomy_gid`, which ImportTaxonomy writes on every
// leaf it creates, so this runs after that one and touches only categories that
// came from the same source. A category somebody typed by hand has no taxonomy
// id and is left alone, metadata included — that is the operator's own.
//
// One transaction, like the tree import and for the same reason: half a set of
// definitions is worse than none, because nothing in the result would say which
// half is missing.
func (s *Categories) ImportCategoryAttributes(ctx context.Context, r io.Reader) (TaxonomyAttributeImport, error) {
	defs, byGID, skipped, err := parseCategoryAttributes(r)
	if err != nil {
		return TaxonomyAttributeImport{}, err
	}
	result := TaxonomyAttributeImport{Skipped: skipped}
	if len(defs) == 0 {
		return result, Validationf("that file contains no attribute definitions")
	}

	// The label written onto a category comes from the dictionary, so the two
	// can never disagree about what a field is called.
	label := make(map[string]string, len(defs))
	for _, d := range defs {
		label[d.Handle] = d.Label
	}

	encodedDefs, err := json.Marshal(defs)
	if err != nil {
		return TaxonomyAttributeImport{}, Internalf(err, "encode the attribute definitions")
	}

	// What a category stores is only which fields it asks for, not what may be
	// answered: choices belong to the attribute. Its own type rather than
	// CategoryAttribute with an empty list, so the stored JSON has no `choices`
	// key at all and nothing later mistakes a null for "no choices".
	type storedField struct {
		Key   string `json:"key"`
		Label string `json:"label"`
	}
	type categoryFields struct {
		GID    string        `json:"gid"`
		Fields []storedField `json:"fields"`
	}
	batch := make([]categoryFields, 0, len(byGID))
	for gid, handles := range byGID {
		fields := make([]storedField, 0, len(handles))
		for _, h := range handles {
			name, ok := label[h]
			if !ok {
				// A category naming a field the dictionary does not define.
				// There is nothing to show, so it is left out rather than
				// rendered as a field with a blank label.
				result.Skipped++
				continue
			}
			// Writing the choices here instead would put 29MB of repeated text
			// in the table and 400KB of it in every page of a listing.
			fields = append(fields, storedField{Key: h, Label: name})
		}
		if len(fields) > 0 {
			batch = append(batch, categoryFields{GID: gid, Fields: fields})
		}
	}
	encodedBatch, err := json.Marshal(batch)
	if err != nil {
		return TaxonomyAttributeImport{}, Internalf(err, "encode the field lists")
	}

	err = InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		// JSON in, arrays out — the same round trip tags take, so there is no
		// hand-written PostgreSQL array quoting anywhere to get wrong.
		//
		// Upsert rather than replace: a store may have written attribute rows
		// of its own, and re-importing the published set should not take them
		// away.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO taxonomy_attributes (handle, label, choices)
			SELECT d->>'handle', d->>'label',
			       ARRAY(SELECT jsonb_array_elements_text(d->'choices'))
			FROM jsonb_array_elements($1::jsonb) AS d
			ON CONFLICT (handle) DO UPDATE
			SET label = excluded.label,
			    choices = excluded.choices,
			    updated_at = now()`, encodedDefs); err != nil {
			return Internalf(err, "write the attribute definitions")
		}
		result.Attributes = len(defs)

		var touched int
		if err := tx.QueryRowContext(ctx, `
			WITH incoming AS (
			    SELECT d->>'gid' AS gid, d->'fields' AS fields
			    FROM jsonb_array_elements($1::jsonb) AS d
			), updated AS (
			    UPDATE categories c
			    SET metadata = jsonb_set(c.metadata, '{attributes}', i.fields, true)
			    FROM incoming i
			    WHERE c.metadata->>'taxonomy_gid' = i.gid
			    RETURNING 1
			)
			SELECT count(*) FROM updated`, encodedBatch).Scan(&touched); err != nil {
			return Internalf(err, "attach the field lists")
		}
		result.Categories = touched
		result.Unmatched = len(batch) - touched
		return nil
	})
	if err != nil {
		return TaxonomyAttributeImport{}, err
	}
	return result, nil
}

// ------------------------------------------------------------- the dictionary

// TaxonomyAttribute is one entry in the shared dictionary: what a field is
// called, and which values it offers wherever a category asks for it.
type TaxonomyAttribute struct {
	Handle    string    `json:"handle"`
	Label     string    `json:"label"`
	Choices   []string  `json:"choices"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TaxonomyAttributeInput creates one. The handle is supplied rather than
// derived from the label: it is the key a category's metadata names (M13), so
// deriving it would make renaming a label silently orphan every category using
// the field.
type TaxonomyAttributeInput struct {
	Handle  string   `json:"handle"`
	Label   string   `json:"label"`
	Choices []string `json:"choices"`
}

// TaxonomyAttributePatch updates one. Handle is absent on purpose — see
// UpdateAttribute.
type TaxonomyAttributePatch struct {
	Label   *string   `json:"label"`
	Choices *[]string `json:"choices"`
}

// TaxonomyAttributeQuery filters the dictionary listing.
type TaxonomyAttributeQuery struct {
	Search string
	Limit  int
	Offset int
}

const taxonomyAttributeColumns = `handle, label, to_jsonb(choices), updated_at`

func scanTaxonomyAttribute(row interface{ Scan(...any) error }) (*TaxonomyAttribute, error) {
	var a TaxonomyAttribute
	var choices []byte
	if err := row.Scan(&a.Handle, &a.Label, &choices, &a.UpdatedAt); err != nil {
		return nil, err
	}
	if err := scanTags(choices, &a.Choices); err != nil {
		return nil, Internalf(err, "decode the attribute choices")
	}
	return &a, nil
}

// ListAttributes returns a page of the dictionary, ordered by handle — a
// dictionary's order is its spelling.
func (s *Categories) ListAttributes(ctx context.Context, q TaxonomyAttributeQuery) ([]*TaxonomyAttribute, int, error) {
	where, args := []string{"true"}, []any{}
	if term := strings.TrimSpace(q.Search); term != "" {
		args = append(args, "%"+strings.ToLower(term)+"%")
		where = append(where, fmt.Sprintf(
			"(lower(handle) LIKE $%d OR lower(label) LIKE $%d)", len(args), len(args)))
	}
	clause := strings.Join(where, " AND ")

	var total int
	if err := s.app.db.QueryRowContext(ctx,
		`SELECT count(*) FROM taxonomy_attributes WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	args = append(args, limit, q.Offset)
	rows, err := s.app.db.QueryContext(ctx,
		`SELECT `+taxonomyAttributeColumns+` FROM taxonomy_attributes WHERE `+clause+
			fmt.Sprintf(" ORDER BY handle LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []*TaxonomyAttribute{}
	for rows.Next() {
		a, err := scanTaxonomyAttribute(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// GetAttribute reads one entry by handle.
func (s *Categories) GetAttribute(ctx context.Context, handle string) (*TaxonomyAttribute, error) {
	handle, err := normalizeHandle(handle)
	if err != nil {
		return nil, err
	}
	a, err := scanTaxonomyAttribute(s.app.db.QueryRowContext(ctx,
		`SELECT `+taxonomyAttributeColumns+` FROM taxonomy_attributes WHERE handle = $1`, handle))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("attribute %q is not in the dictionary", handle)
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

// CreateAttribute adds an entry to the dictionary.
func (s *Categories) CreateAttribute(ctx context.Context, in TaxonomyAttributeInput) (*TaxonomyAttribute, error) {
	handle, err := normalizeHandle(in.Handle)
	if err != nil {
		return nil, err
	}
	label := strings.TrimSpace(in.Label)
	if label == "" {
		return nil, Validationf("label is required")
	}
	choices, err := choicesValue(in.Choices)
	if err != nil {
		return nil, err
	}

	var out *TaxonomyAttribute
	err = InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		var err error
		out, err = scanTaxonomyAttribute(tx.QueryRowContext(ctx,
			`INSERT INTO taxonomy_attributes (handle, label, choices)
			 VALUES ($1, $2, `+tagsExpr(3)+`)
			 RETURNING `+taxonomyAttributeColumns, handle, label, choices))
		if err != nil {
			return translateAttributeErr(err)
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditTaxonomyAttributeCreate, Entity: AuditEntityTaxonomyAttribute,
			Key: out.Handle, Label: out.Label,
			Summary: "Added the field " + out.Label + " to the dictionary",
			After:   map[string]any{"handle": out.Handle, "label": out.Label, "choices": out.Choices},
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateAttribute applies a patch.
//
// The handle is not patchable: it is the key every category's metadata holds,
// and renaming it here would detach them all without saying so. Moving a field
// to a new handle is a create, an edit of the categories that name it, and a
// delete (D42).
func (s *Categories) UpdateAttribute(ctx context.Context, handle string, patch TaxonomyAttributePatch) (*TaxonomyAttribute, error) {
	handle, err := normalizeHandle(handle)
	if err != nil {
		return nil, err
	}
	set, args := []string{}, []any{handle}
	after := map[string]any{}
	if patch.Label != nil {
		label := strings.TrimSpace(*patch.Label)
		if label == "" {
			return nil, Validationf("label must not be empty")
		}
		args = append(args, label)
		set = append(set, fmt.Sprintf("label = $%d", len(args)))
		after["label"] = label
	}
	if patch.Choices != nil {
		choices, err := choicesValue(*patch.Choices)
		if err != nil {
			return nil, err
		}
		args = append(args, choices)
		set = append(set, "choices = "+tagsExpr(len(args)))
		after["choices"] = *patch.Choices
	}
	if len(set) == 0 {
		// An empty patch is a read rather than an error: the caller asked for
		// nothing to change, and nothing did.
		return s.GetAttribute(ctx, handle)
	}

	var out *TaxonomyAttribute
	err = InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		was, err := scanTaxonomyAttribute(tx.QueryRowContext(ctx,
			`SELECT `+taxonomyAttributeColumns+
				` FROM taxonomy_attributes WHERE handle = $1 FOR UPDATE`, handle))
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("attribute %q is not in the dictionary", handle)
		}
		if err != nil {
			return err
		}
		before := map[string]any{}
		if _, changed := after["label"]; changed {
			before["label"] = was.Label
		}
		if _, changed := after["choices"]; changed {
			before["choices"] = was.Choices
		}

		out, err = scanTaxonomyAttribute(tx.QueryRowContext(ctx,
			`UPDATE taxonomy_attributes SET `+strings.Join(set, ", ")+`, updated_at = now()
			 WHERE handle = $1 RETURNING `+taxonomyAttributeColumns, args...))
		if err != nil {
			return err
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditTaxonomyAttributeUpdate, Entity: AuditEntityTaxonomyAttribute,
			Key: out.Handle, Label: out.Label,
			Summary: "Edited the field " + out.Label,
			Before:  before, After: after,
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteAttribute removes an entry.
//
// Categories that name the handle are left exactly as they are: with no row
// here the field simply offers no fixed choices, which is a free-text field
// rather than a broken one — the same rule attachAttributes already applies to
// a handle nobody has defined. So this is never refused for being in use.
func (s *Categories) DeleteAttribute(ctx context.Context, handle string) error {
	handle, err := normalizeHandle(handle)
	if err != nil {
		return err
	}
	return InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		var label string
		err := tx.QueryRowContext(ctx,
			`DELETE FROM taxonomy_attributes WHERE handle = $1 RETURNING label`, handle).Scan(&label)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("attribute %q is not in the dictionary", handle)
		}
		if err != nil {
			return err
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditTaxonomyAttributeDelete, Entity: AuditEntityTaxonomyAttribute,
			Key: handle, Label: label,
			Summary: "Removed the field " + label + " from the dictionary",
			Before:  map[string]any{"handle": handle, "label": label},
		})
	})
}

// normalizeHandle trims, lower-cases and checks what a handle has to be able to
// do: name a field inside a category's metadata, and survive a URL path segment.
//
// Folded, because the match in attachAttributes is a literal `= ANY(...)` and
// the column is `handle text PRIMARY KEY` (M13) — so "Color" and "color" are two
// rows, only one of which any category will ever match, while the case-folded
// list search shows them as the same thing. Every published handle is lowercase
// ASCII, so nothing that exists moves. Whitespace and a slash are refused: a
// handle carrying either can be stored and then never addressed again.
func normalizeHandle(s string) (string, error) {
	handle := strings.ToLower(strings.TrimSpace(s))
	if handle == "" {
		return "", Validationf("handle is required")
	}
	if strings.ContainsAny(handle, " \t\r\n/") {
		return "", Validationf("a handle carries no spaces and no slashes")
	}
	return handle, nil
}

// choicesValue renders an attribute's values for tagsExpr.
//
// Unlike tags they are neither sorted nor case-folded: "XS, S, M, L, XL" is the
// publisher's order and the only useful one. Blanks and exact repeats go,
// because a picker showing one value twice is a bug nobody can fix from the
// panel.
func choicesValue(in []string) ([]byte, error) {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, Internalf(err, "encode the attribute choices")
	}
	return encoded, nil
}

func translateAttributeErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "taxonomy_attributes_pkey") {
		return Conflictf("that handle is already in the dictionary")
	}
	return err
}

// attachAttributes fills in each category's Attributes: the fields its own
// metadata declares, with the choices the shared table holds for them.
//
// The metadata itself is left exactly as it was read. A client that shows a
// category and writes it back should not find that merely reading it had grown
// the row by every value list it mentions.
func attachAttributes(ctx context.Context, db *sql.DB, cats []*Category) error {
	type decl struct {
		Key   string `json:"key"`
		Label string `json:"label"`
	}
	declared := make([][]decl, len(cats))
	seen := map[string]bool{}
	handles := []string{}

	for i, c := range cats {
		raw, ok := c.Metadata["attributes"]
		if !ok {
			continue
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		var list []decl
		if err := json.Unmarshal(encoded, &list); err != nil {
			// Metadata is the operator's own and may hold anything under this
			// key. Something that is not a field list is not an error; it is
			// simply not a field list.
			continue
		}
		declared[i] = list
		for _, d := range list {
			if d.Key != "" && !seen[d.Key] {
				seen[d.Key] = true
				handles = append(handles, d.Key)
			}
		}
	}
	if len(handles) == 0 {
		return nil
	}

	encoded, err := json.Marshal(handles)
	if err != nil {
		return Internalf(err, "encode the attribute handles")
	}
	rows, err := db.QueryContext(ctx, `
		SELECT handle, to_jsonb(choices) FROM taxonomy_attributes
		WHERE handle = ANY(ARRAY(SELECT jsonb_array_elements_text($1::jsonb)))`, encoded)
	if err != nil {
		return Internalf(err, "read the attribute choices")
	}
	defer rows.Close()

	choices := map[string][]string{}
	for rows.Next() {
		var handle string
		var raw []byte
		if err := rows.Scan(&handle, &raw); err != nil {
			return Internalf(err, "scan the attribute choices")
		}
		var values []string
		if err := scanTags(raw, &values); err != nil {
			return Internalf(err, "decode the attribute choices")
		}
		choices[handle] = values
	}
	if err := rows.Err(); err != nil {
		return Internalf(err, "read the attribute choices")
	}

	for i, c := range cats {
		for _, d := range declared[i] {
			if d.Key == "" {
				continue
			}
			label := d.Label
			if label == "" {
				label = d.Key
			}
			values := choices[d.Key]
			if values == nil {
				values = []string{}
			}
			c.Attributes = append(c.Attributes, CategoryAttribute{
				Key: d.Key, Label: label, Choices: values,
			})
		}
	}
	return nil
}
