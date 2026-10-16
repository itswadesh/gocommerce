package translations

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// The translation screen's list (D87): a page of one kind's items, each with
// the store's own text beside its translation into one language, and how many
// of the store's items that language covers — "12 of 40 translated" is the
// first thing an owner translating a catalogue wants to know.
//
// It reads the core tables the text lives in and writes none of them (rule 3),
// the way wishlist and b2b read products.

// Item is one product, category or collection as the screen shows it.
type Item struct {
	ID int64 `json:"id"`
	// Path is where a category sits, "Jewellery / Rings"; empty for the others.
	Path string `json:"path,omitempty"`
	// Original is the store's own text, in its main language.
	Original map[string]string `json:"original"`
	// Translation is null until somebody writes one.
	Translation map[string]string `json:"translation"`
	UpdatedAt   *time.Time        `json:"updated_at"`
}

// Items is one page of them, with the counts the screen heads it with.
type Items struct {
	Language string   `json:"language"`
	Fields   []string `json:"fields"`
	Items    []Item   `json:"items"`
	// Matching is how many items the search and the status filter match.
	Matching int `json:"matching"`
	// Total and Translated are the store's whole catalogue of this kind.
	Total      int `json:"total"`
	Translated int `json:"translated"`
	Limit      int `json:"limit"`
	Offset     int `json:"offset"`
}

// Status filters for the list.
const (
	StatusMissing    = "missing"
	StatusTranslated = "translated"
)

func (m *Module) handleItems(w http.ResponseWriter, r *http.Request) {
	kind, err := kindOf(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	q := r.URL.Query()
	language, err := m.storeLanguage(r.Context(), kind, strings.TrimSpace(q.Get("language")))
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	status := q.Get("status")
	if status != "" && status != StatusMissing && status != StatusTranslated {
		gocommerce.RespondError(w, r, gocommerce.Validationf("status is %q or %q", StatusMissing, StatusTranslated))
		return
	}
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	out, err := m.items(r.Context(), kind, language, strings.TrimSpace(q.Get("q")), status, limit, offset)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

func (m *Module) items(ctx context.Context, kind, language, search, status string, limit, offset int) (*Items, error) {
	fields := fieldsOf[kind]
	table := tableOf[kind]
	// The field names are this package's own allow-list, and each is the
	// column it translates, so they are safe to write into the query.
	pairs := make([]string, 0, len(fields))
	for _, f := range fields {
		pairs = append(pairs, "'"+f+"', coalesce(e."+f+", '')")
	}
	// An archived product is one nobody is shown; translating it is work for
	// nothing, so it is neither listed nor counted.
	live := "TRUE"
	if kind == gocommerce.KindProduct {
		live = "e.status <> '" + gocommerce.ProductArchived + "'"
	}

	// An item is translated once its title is: a description alone leaves the
	// shopper reading the main language's name, which is what they see first.
	from := `
		FROM ` + table + ` e
		LEFT JOIN translations_entries t
		  ON t.kind = $1 AND t.entity_id = e.id AND lower(t.language) = lower($2)
		WHERE ` + live + `
		  AND ($3 = '' OR e.title ILIKE '%' || $3 || '%' ESCAPE '\')
		  AND ($4 = '' OR ($4 = 'translated') = coalesce(t.fields ? 'title', false))`
	args := []any{kind, language, escapeLike(search), status}

	out := &Items{Language: language, Fields: fields, Items: []Item{}, Limit: limit, Offset: offset}
	// Counted on its own rather than beside each row, so a page past the end
	// still knows how many there are and the screen can page back.
	if err := m.db.QueryRowContext(ctx, `SELECT count(*)`+from, args...).Scan(&out.Matching); err != nil {
		return nil, err
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT e.id, jsonb_build_object(`+strings.Join(pairs, ", ")+`), t.fields, t.updated_at`+from+`
		ORDER BY lower(e.title), e.id
		LIMIT $5 OFFSET $6`, append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var it Item
		var original, translation []byte
		if err := rows.Scan(&it.ID, &original, &translation, &it.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(original, &it.Original); err != nil {
			return nil, err
		}
		if translation != nil {
			if err := json.Unmarshal(translation, &it.Translation); err != nil {
				return nil, err
			}
		}
		out.Items = append(out.Items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := m.db.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM `+table+` e WHERE `+live+`),
		       (SELECT count(*) FROM translations_entries t
		        WHERE t.kind = $1 AND lower(t.language) = lower($2) AND t.fields ? 'title'
		          AND EXISTS (SELECT 1 FROM `+table+` e WHERE e.id = t.entity_id AND `+live+`))`,
		kind, language).Scan(&out.Total, &out.Translated); err != nil {
		return nil, err
	}
	if kind == gocommerce.KindCategory {
		return out, m.categoryPaths(ctx, out.Items)
	}
	return out, nil
}

// categoryPaths fills in where each category on the page sits, since two
// categories called "Rings" under different parents are told apart by it.
func (m *Module) categoryPaths(ctx context.Context, items []Item) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]int64, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	rows, err := m.db.QueryContext(ctx, `
		WITH RECURSIVE chain AS (
		    SELECT id AS leaf, parent_id, title::text AS path, 0 AS depth
		    FROM categories WHERE id = ANY($1::bigint[])
		    UNION ALL
		    SELECT chain.leaf, c.parent_id, c.title || ' / ' || chain.path, chain.depth + 1
		    FROM categories c JOIN chain ON c.id = chain.parent_id
		    WHERE chain.depth < $2)
		SELECT leaf, path FROM chain WHERE parent_id IS NULL`, int64Array(ids), gocommerce.MaxCategoryDepth)
	if err != nil {
		return err
	}
	defer rows.Close()
	paths := map[int64]string{}
	for rows.Next() {
		var id int64
		var path string
		if err := rows.Scan(&id, &path); err != nil {
			return err
		}
		paths[id] = path
	}
	for i := range items {
		items[i].Path = paths[items[i].ID]
	}
	return rows.Err()
}

// escapeLike makes a search term match itself: a "%" typed into the box is a
// percent sign, not "anything".
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
