package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// The category file is the tree written down.
//
// A tree does not fit a spreadsheet as a parent column: an operator editing a
// file in Excel cannot be asked to keep numeric ids consistent, and a store's
// ids mean nothing in another store. So the trail from the root rides in one
// cell — "Apparel / Clothing / Shirts" — which is the same thing the product
// file's `category` column already says, and it is legible without the rest of
// the file for company.
//
// The path is the identity, exactly as `taxonomy import` treats it: a row
// whose trail already exists updates that category, and a row whose trail does
// not builds every missing step on the way. That makes a second run of the
// same file a no-op rather than a second tree.
//
// What the file deliberately cannot do is move or delete. Editing a path would
// otherwise mean that one typo silently drags a subtree — and the products
// filed under it — somewhere else, and there is no undo for that in a
// spreadsheet. Moving a category is a deliberate act with a cycle check and a
// product count in front of it, and it stays on the screen that has both
// (D62).

// categoryCSVHeader is the store's layout. `products` is written for the
// reader and ignored on the way in: it is counted from the products, not
// stored, and a file cannot file a product by writing a number here.
var categoryCSVHeader = []string{"path", "slug", "position", "products", "metadata"}

// ExportCategories streams the whole tree, depth first, so that a parent is
// always written above its children — which is what lets the importer resolve
// a parent from the rows it has already read.
func (t *Transfer) ExportCategories(ctx context.Context, out io.Writer, opts ExportOptions) error {
	if opts.Format == FormatShopify {
		return errCategoryDialect
	}
	w, err := NewCSVWriter(out, categoryCSVHeader)
	if err != nil {
		return err
	}

	// sortkey is the trail of (position, id) pairs, fixed width, so ordering
	// by it is depth-first in the operator's own order. A parent's key is a
	// prefix of its children's and therefore sorts above them.
	rows, err := t.app.db.QueryContext(ctx, `
		WITH RECURSIVE down AS (
		    SELECT c.id, c.slug, c.title::text AS path, c.position, c.metadata,
		           lpad(c.position::text, 10, '0') || ':' || lpad(c.id::text, 20, '0') || '/' AS sortkey
		    FROM categories c WHERE c.parent_id IS NULL
		  UNION ALL
		    SELECT c.id, c.slug, d.path || ' / ' || c.title, c.position, c.metadata,
		           d.sortkey || lpad(c.position::text, 10, '0') || ':' || lpad(c.id::text, 20, '0') || '/'
		    FROM categories c JOIN down d ON d.id = c.parent_id
		)
		SELECT d.path, d.slug, d.position, d.metadata,
		       (SELECT count(*) FROM products p WHERE p.category_id = d.id)
		FROM down d ORDER BY d.sortkey`)
	if err != nil {
		return Internalf(err, "read the category tree")
	}
	defer rows.Close()

	for rows.Next() {
		var path, slug, metadata string
		var position, products int
		if err := rows.Scan(&path, &slug, &position, &metadata, &products); err != nil {
			return Internalf(err, "scan the category tree")
		}
		if err := w.Write([]string{
			path, slug, strconv.Itoa(position), strconv.Itoa(products), metadata,
		}); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return Internalf(err, "read the category tree")
	}
	return w.Flush()
}

// errCategoryDialect is the answer to `format=shopify`. Shopify's own category
// export is its taxonomy file, which `POST /api/admin/import/taxonomy` already
// reads; answering with the store's CSV under that name would be a lie about
// what the file is.
var errCategoryDialect = Validationf(
	"the category file has one format; Shopify's taxonomy is imported at /api/admin/import/taxonomy")

// ImportCategories reads a category file, adding what is missing and updating
// what is already there.
//
// The whole file is one transaction, the same judgement `taxonomy import`
// makes: half a tree is worse than none, because the branches that failed are
// invisible and a re-run looks like it had nothing to do. A row the file gets
// wrong is therefore refused in Go, before any statement runs — a failed
// statement would poison the transaction and cost the operator the other 999
// rows, which is the one thing row errors exist to prevent.
func (t *Transfer) ImportCategories(ctx context.Context, in io.Reader, opts ImportOptions) (*ImportResult, error) {
	if opts.Format == FormatShopify {
		return nil, errCategoryDialect
	}
	start := time.Now()
	r, err := NewCSVReader(in)
	if err != nil {
		return nil, err
	}
	if !r.Has("path") {
		return nil, Validationf("the CSV is missing the required column %q", "path")
	}

	result := &ImportResult{DryRun: opts.DryRun, Format: FormatNative}
	err = InTx(ctx, t.app.db, func(tx *sql.Tx) error {
		// The whole tree, and every slug, read once: a file of two hundred
		// rows would otherwise be six hundred round trips to answer questions
		// whose answers do not change.
		byPath, err := existingPaths(ctx, tx)
		if err != nil {
			return err
		}
		taken, slugByID, err := categorySlugs(ctx, tx)
		if err != nil {
			return err
		}

		insert, err := tx.PrepareContext(ctx, `
			INSERT INTO categories (parent_id, slug, title, position, metadata)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`)
		if err != nil {
			return Internalf(err, "prepare the insert")
		}
		defer insert.Close()

		for {
			row, err := r.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				result.Errors = append(result.Errors, RowError{Line: r.Line(), Message: err.Error()})
				result.Skipped++
				continue
			}
			fail := func(format string, args ...any) {
				result.Errors = append(result.Errors, RowError{Line: row.Line(), Message: fmt.Sprintf(format, args...)})
				result.Skipped++
			}

			parts := splitCategoryPath(row.Get("path"))
			if len(parts) == 0 {
				fail("path is empty; a row has to say where the category sits, as in %q", "Apparel / Shirts")
				continue
			}
			if len(parts) > MaxCategoryDepth {
				fail("that path is %d deep and the tree stops at %d", len(parts), MaxCategoryDepth)
				continue
			}
			// Read as "does this row say anything here", not "what does this
			// column hold": an absent column and a blank cell both mean the
			// row is silent, and a silent row must not move a position or
			// empty the metadata that carries a category's taxonomy id and
			// its attribute declarations.
			position, err := row.Int("position", 0)
			if err != nil {
				fail("%v", err)
				continue
			}
			metadata := "{}"
			if row.Has("metadata") {
				if metadata, err = categoryMetadata(row.Get("metadata")); err != nil {
					fail("%v", err)
					continue
				}
			}

			// Resolved before anything is written, so that a slug the file
			// cannot have costs one row rather than a half-built trail.
			leafKey := categoryPathKey(parts)
			leafID, exists := byPath[leafKey]
			wantSlug := slugify(row.Get("slug"))
			if wantSlug != "" && taken[wantSlug] && !(exists && slugByID[leafID] == wantSlug) {
				fail("another category already has the slug %q", wantSlug)
				continue
			}

			if exists {
				if !opts.overwrite() {
					result.Skipped++
					continue
				}
				var sets []string
				var args []any
				set := func(col string, v any) {
					args = append(args, v)
					sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
				}
				if row.Has("position") {
					set("position", position)
				}
				if row.Has("metadata") {
					set("metadata", metadata)
				}
				renaming := wantSlug != "" && wantSlug != slugByID[leafID]
				if renaming {
					set("slug", wantSlug)
				}
				if len(sets) == 0 {
					// The row named a category and said nothing about it,
					// which is a file of paths confirming a tree. There is
					// nothing to write, and touching updated_at would only
					// make the tree look edited.
					result.Updated++
					continue
				}
				sets = append(sets, "updated_at = now()")
				args = append(args, leafID)
				if _, err := tx.ExecContext(ctx,
					"UPDATE categories SET "+strings.Join(sets, ", ")+
						fmt.Sprintf(" WHERE id = $%d", len(args)), args...); err != nil {
					return translateCategoryErr(err)
				}
				if renaming {
					delete(taken, slugByID[leafID])
					taken[wantSlug] = true
					slugByID[leafID] = wantSlug
				}
				result.Updated++
				continue
			}

			// Every missing step of the trail, root first. A file that names
			// only the leaf still builds a tree that makes sense, which is
			// what an operator typing three paths into a spreadsheet expects.
			var parentID *int64
			for depth, name := range parts {
				key := categoryPathKey(parts[:depth+1])
				if id, ok := byPath[key]; ok {
					parentID = &id
					continue
				}
				leaf := depth == len(parts)-1
				slug := ""
				if leaf && wantSlug != "" {
					slug = wantSlug
					taken[slug] = true
				} else {
					slug = uniqueSlug(name, parts[:depth+1], taken)
				}
				// Only the leaf is the row's own: an ancestor the file never
				// mentioned takes the defaults, and gets its own row's values
				// if the file names it later.
				pos, meta := 0, "{}"
				if leaf {
					pos, meta = position, metadata
				}
				var id int64
				if err := insert.QueryRowContext(ctx, parentID, slug, name, pos, meta).Scan(&id); err != nil {
					return translateCategoryErr(err)
				}
				byPath[key] = id
				slugByID[id] = slug
				parentID = &id
				result.Created++
			}
		}

		if opts.DryRun {
			return errDryRun
		}
		return nil
	})
	if err != nil && !errors.Is(err, errDryRun) {
		return nil, err
	}
	result.Duration = time.Since(start).String()
	return result, nil
}

// splitCategoryPath reads a trail in either separator: the store writes
// "Apparel / Shirts" and Shopify's taxonomy writes "Apparel > Shirts", and an
// operator should not have to know which one their file came from.
func splitCategoryPath(path string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '>' }) {
		if name := strings.TrimSpace(part); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// categoryPathKey is the trail as existingPaths holds it: unit-separated and
// folded to lower case in Go rather than in SQL, for the reason existingPaths
// explains at length.
func categoryPathKey(parts []string) string {
	return strings.ToLower(strings.Join(parts, pathSep))
}

// categorySlugs reads every slug once, both ways round: which slugs are spoken
// for, and which one each category currently holds. The second is what tells a
// row that names its own slug from a row that names somebody else's.
func categorySlugs(ctx context.Context, tx *sql.Tx) (map[string]bool, map[int64]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, slug FROM categories`)
	if err != nil {
		return nil, nil, Internalf(err, "read category slugs")
	}
	defer rows.Close()

	taken := map[string]bool{}
	byID := map[int64]string{}
	for rows.Next() {
		var id int64
		var slug string
		if err := rows.Scan(&id, &slug); err != nil {
			return nil, nil, Internalf(err, "scan category slugs")
		}
		taken[slug] = true
		byID[id] = slug
	}
	if err := rows.Err(); err != nil {
		return nil, nil, Internalf(err, "read category slugs")
	}
	return taken, byID, nil
}

// categoryMetadata checks the cell here rather than letting PostgreSQL refuse
// it: inside one transaction a rejected statement costs the whole file, and a
// mistyped brace in one cell should cost one row.
func categoryMetadata(cell string) (string, error) {
	cell = strings.TrimSpace(cell)
	if cell == "" {
		return "{}", nil
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(cell), &object); err != nil {
		return "", fmt.Errorf("metadata is not a JSON object: %v", err)
	}
	return cell, nil
}
