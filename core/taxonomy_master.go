package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// The Shopify taxonomy as the master tree every store browses (D143).
//
// It is read from the embedded file, once per process, and never written to a
// table. The file is the same in every store of one install and changes only
// with a release, so a global table would be a copy of it that a migration has
// to keep in step — and copying it into each store is what the owner ruled out.
// What a store owns is which nodes it has activated: those are ordinary rows in
// its own `categories`, linked back by `metadata.taxonomy_gid`, which is the
// link the whole-tree import has always written.

// TaxonomyNode is one node of the master, with whether this store has it.
type TaxonomyNode struct {
	// ID is the gid's last segment ("aa-1-1"), which is what a route carries:
	// the full gid has slashes in it.
	ID       string `json:"id"`
	GID      string `json:"gid"`
	ParentID string `json:"parent_id"` // "" for a root
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Depth    int    `json:"depth"`
	// ChildCount is the master's, not the store's: it says whether there is
	// more to browse.
	ChildCount int `json:"child_count"`
	// CategoryID is the store's category for this node, nil while inactive.
	// Title is that category's own name, which a store may have changed.
	Active     bool   `json:"active"`
	CategoryID *int64 `json:"category_id"`
	Title      string `json:"title,omitempty"`
}

type masterTree struct {
	nodes    []TaxonomyNode // file order: a parent precedes its children
	byID     map[string]int
	children map[string][]int // parent id -> indexes, file order
	folded   []string         // lower-cased full names, for search
}

var (
	masterOnce sync.Once
	master     *masterTree
)

const taxonomyGIDPrefix = "gid://shopify/TaxonomyCategory/"

// taxonomyMaster parses the embedded file on first use. ~14,600 nodes is a few
// megabytes of memory, paid only by a process that is asked to browse.
func taxonomyMaster() *masterTree {
	masterOnce.Do(func() {
		lines, _, _ := parseTaxonomy(strings.NewReader(shopifyTaxonomy))
		m := &masterTree{byID: map[string]int{}, children: map[string][]int{}}
		byPath := map[string]string{}
		for _, l := range lines {
			id := strings.TrimPrefix(l.gid, taxonomyGIDPrefix)
			parent := ""
			if len(l.path) > 1 {
				parent = byPath[strings.Join(l.path[:len(l.path)-1], pathSep)]
			}
			byPath[strings.Join(l.path, pathSep)] = id
			full := strings.Join(l.path, " / ")
			m.byID[id] = len(m.nodes)
			m.children[parent] = append(m.children[parent], len(m.nodes))
			m.nodes = append(m.nodes, TaxonomyNode{
				ID: id, GID: l.gid, ParentID: parent,
				Name: l.path[len(l.path)-1], FullName: full, Depth: len(l.path) - 1,
			})
			m.folded = append(m.folded, strings.ToLower(full))
		}
		for i := range m.nodes {
			m.nodes[i].ChildCount = len(m.children[m.nodes[i].ID])
		}
		master = m
	})
	return master
}

// node finds a master node by its short id or its full gid.
func (m *masterTree) node(id string) (TaxonomyNode, bool) {
	i, ok := m.byID[strings.TrimPrefix(strings.TrimSpace(id), taxonomyGIDPrefix)]
	if !ok {
		return TaxonomyNode{}, false
	}
	return m.nodes[i], true
}

// chain is the node's ancestry, root first, the node last.
func (m *masterTree) chain(n TaxonomyNode) []TaxonomyNode {
	out := []TaxonomyNode{n}
	for n.ParentID != "" {
		n = m.nodes[m.byID[n.ParentID]]
		out = append(out, n)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// TaxonomyChildren returns one level of the master — the roots for "" — each
// marked with whether this store has activated it.
func (s *Categories) TaxonomyChildren(ctx context.Context, parent string) ([]TaxonomyNode, error) {
	m := taxonomyMaster()
	if parent = strings.TrimSpace(parent); parent != "" && parent != "root" {
		n, ok := m.node(parent)
		if !ok {
			return nil, NotFoundf("no taxonomy category %q", parent)
		}
		parent = n.ID
	} else {
		parent = ""
	}
	out := make([]TaxonomyNode, 0, len(m.children[parent]))
	for _, i := range m.children[parent] {
		out = append(out, m.nodes[i])
	}
	return out, s.markActive(ctx, out)
}

// SearchTaxonomy matches a term anywhere in a node's path, case-folded, in
// file order (which is depth-first), and reports the true number of matches.
// In memory, because the master is: a scan of 14,600 strings is far cheaper
// than any round trip.
func (s *Categories) SearchTaxonomy(ctx context.Context, term string, limit, offset int) ([]TaxonomyNode, int, error) {
	m := taxonomyMaster()
	term = strings.ToLower(strings.TrimSpace(term))
	if limit <= 0 {
		limit = DefaultCategoryPage
	}
	limit = min(limit, MaxLimit)
	out, total := []TaxonomyNode{}, 0
	for i, f := range m.folded {
		if !strings.Contains(f, term) {
			continue
		}
		if total >= offset && len(out) < limit {
			out = append(out, m.nodes[i])
		}
		total++
	}
	return out, total, s.markActive(ctx, out)
}

// markActive fills in the store's category for each node, in one query.
func (s *Categories) markActive(ctx context.Context, nodes []TaxonomyNode) error {
	if len(nodes) == 0 {
		return nil
	}
	gids := make([]string, len(nodes))
	for i, n := range nodes {
		gids[i] = n.GID
	}
	encoded, err := json.Marshal(gids)
	if err != nil {
		return err
	}
	rows, err := s.app.db.QueryContext(ctx, `
		SELECT DISTINCT ON (metadata->>'taxonomy_gid') metadata->>'taxonomy_gid', id, title
		FROM categories
		WHERE metadata->>'taxonomy_gid' IN (SELECT jsonb_array_elements_text($1::jsonb))
		ORDER BY metadata->>'taxonomy_gid', id`, encoded)
	if err != nil {
		return err
	}
	defer rows.Close()
	type have struct {
		id    int64
		title string
	}
	found := map[string]have{}
	for rows.Next() {
		var gid string
		var h have
		if err := rows.Scan(&gid, &h.id, &h.title); err != nil {
			return err
		}
		found[gid] = h
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range nodes {
		if h, ok := found[nodes[i].GID]; ok {
			id := h.id
			nodes[i].Active, nodes[i].CategoryID, nodes[i].Title = true, &id, h.title
		}
	}
	return nil
}

// TaxonomyActivation is what activating one node did.
type TaxonomyActivation struct {
	// Category is the store's category for the node asked for.
	Category *Category `json:"category"`
	// Created counts categories written, the node's missing ancestors
	// included; 0 means it was already active.
	Created int `json:"created"`
}

// ActivateTaxonomy makes a master node one of this store's categories, with
// every ancestor it is missing, so the store's tree stays whole.
//
// Each node in the chain is, in order: the store's category already linked to
// that gid; else a category of the store's own at the same place with the same
// name (case-folded in Go, for the reason existingPaths gives), which is
// adopted by writing the gid onto it rather than duplicated beside it; else a
// new category with the master's name and a slug unique in this store. The
// fields the taxonomy asks of a product follow the categories that were
// linked (D42). Idempotent: activating an active node writes nothing.
func (s *Categories) ActivateTaxonomy(ctx context.Context, id string) (*TaxonomyActivation, error) {
	m := taxonomyMaster()
	n, ok := m.node(id)
	if !ok {
		return nil, NotFoundf("no taxonomy category %q", id)
	}
	chain := m.chain(n)
	result := &TaxonomyActivation{}
	var leaf int64
	err := InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		var taken map[string]bool // read once, only if something is created
		var linked []string
		var parentID *int64
		for depth, node := range chain {
			catID, adopt, err := findForNode(ctx, tx, node, parentID)
			if err != nil {
				return err
			}
			switch {
			case catID != 0 && adopt:
				if _, err := tx.ExecContext(ctx, `
					UPDATE categories SET metadata = metadata || jsonb_build_object('taxonomy_gid', $2::text),
					       updated_at = now() WHERE id = $1`, catID, node.GID); err != nil {
					return err
				}
				linked = append(linked, node.GID)
			case catID == 0:
				if taken == nil {
					if taken, err = existingSlugs(ctx, tx); err != nil {
						return err
					}
				}
				if parentID != nil {
					d, err := depthOf(ctx, tx, *parentID)
					if err != nil {
						return err
					}
					if d+1 >= MaxCategoryDepth {
						return Validationf("categories nest at most %d deep", MaxCategoryDepth)
					}
				}
				path := make([]string, depth+1)
				for i := range path {
					path[i] = chain[i].Name
				}
				slug := uniqueSlug(node.Name, path, taken)
				if err := tx.QueryRowContext(ctx, `
					INSERT INTO categories (parent_id, slug, title, position, metadata)
					VALUES ($1, $2, $3,
					        (SELECT coalesce(max(position), -1) + 1 FROM categories
					         WHERE parent_id IS NOT DISTINCT FROM $1),
					        jsonb_build_object('taxonomy_gid', $4::text))
					RETURNING id`, parentID, slug, node.Name, node.GID).Scan(&catID); err != nil {
					return translateCategoryErr(err)
				}
				if err := writeAudit(ctx, tx, auditRecord{
					Action: AuditCategoryCreate, Entity: AuditEntityCategory,
					ID: catID, Label: node.Name, Summary: "Activated the taxonomy category " + node.FullName,
					After: map[string]any{"slug": slug, "title": node.Name, "parent_id": parentID, "taxonomy_gid": node.GID},
				}); err != nil {
					return err
				}
				linked = append(linked, node.GID)
				result.Created++
			}
			id := catID
			parentID = &id
		}
		leaf = *parentID
		return attachTaxonomyAttributes(ctx, tx, linked)
	})
	if err != nil {
		return nil, err
	}
	if result.Category, err = s.Get(ctx, leaf); err != nil {
		return nil, err
	}
	return result, nil
}

// findForNode returns the store's category for a master node: the one linked
// by gid (anywhere in the tree — a store may have moved it), else an unlinked
// one of the same name under parentID, which the caller adopts. 0 is neither.
func findForNode(ctx context.Context, tx *sql.Tx, node TaxonomyNode, parentID *int64) (int64, bool, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM categories WHERE metadata->>'taxonomy_gid' = $1
		ORDER BY id LIMIT 1`, node.GID).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id, title FROM categories
		WHERE parent_id IS NOT DISTINCT FROM $1 AND NOT metadata ? 'taxonomy_gid'
		ORDER BY id`, parentID)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	want := strings.ToLower(node.Name)
	for rows.Next() {
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			return 0, false, err
		}
		if strings.ToLower(title) == want {
			return id, true, nil
		}
	}
	return 0, false, rows.Err()
}

// DeactivateTaxonomy takes a master node, and everything the store activated
// beneath it, out of the store's categories.
//
// It refuses — naming what is in the way — while products are filed anywhere
// in that branch, or while the branch holds a category of the store's own,
// because deactivating must never take a product's category or a merchant's
// own work with it. Deleted deepest first, each through deleteCategory, so the
// usual audit is written and a tax rate naming one still refuses the lot. A
// node that is not active is a no-op.
func (s *Categories) DeactivateTaxonomy(ctx context.Context, id string) error {
	n, ok := taxonomyMaster().node(id)
	if !ok {
		return NotFoundf("no taxonomy category %q", id)
	}
	return InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		var root int64
		err := tx.QueryRowContext(ctx, `
			SELECT id FROM categories WHERE metadata->>'taxonomy_gid' = $1
			ORDER BY id LIMIT 1`, n.GID).Scan(&root)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		type row struct {
			id       int64
			depth    int
			title    string
			linked   bool
			products int
		}
		rows, err := tx.QueryContext(ctx, `
			WITH RECURSIVE down AS (
			    SELECT id, 0 AS depth FROM categories WHERE id = $1
			  UNION ALL
			    SELECT c.id, down.depth + 1 FROM down JOIN categories c ON c.parent_id = down.id
			    WHERE down.depth < $2
			)
			SELECT c.id, down.depth, c.title, c.metadata ? 'taxonomy_gid',
			       (SELECT count(*) FROM products p WHERE p.category_id = c.id)
			FROM down JOIN categories c ON c.id = down.id`, root, MaxCategoryDepth)
		if err != nil {
			return err
		}
		var branch []row
		products := 0
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.depth, &r.title, &r.linked, &r.products); err != nil {
				rows.Close()
				return err
			}
			branch = append(branch, r)
			products += r.products
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if products > 0 {
			return Conflictf("%s still has %s filed under it; recategorise them first",
				n.Name, plural(products, "product", "products"))
		}
		for _, r := range branch {
			if !r.linked {
				return Conflictf("%s holds your own category %s; move or delete it first", n.Name, r.title)
			}
		}
		sort.SliceStable(branch, func(i, j int) bool { return branch[i].depth > branch[j].depth })
		for _, r := range branch {
			if err := deleteCategory(ctx, tx, r.id); err != nil {
				return err
			}
		}
		return nil
	})
}

// attachTaxonomyAttributes gives newly linked categories the fields the
// embedded attribute file names for them, writing only the dictionary entries
// those fields use — the same result as the full attribute import, scoped to
// what was activated, so one click does not upsert the whole dictionary.
func attachTaxonomyAttributes(ctx context.Context, tx *sql.Tx, gids []string) error {
	if len(gids) == 0 {
		return nil
	}
	defs, byGID := taxonomyAttributeSet()
	type storedField struct {
		Key   string `json:"key"`
		Label string `json:"label"`
	}
	type categoryFields struct {
		GID    string        `json:"gid"`
		Fields []storedField `json:"fields"`
	}
	var batch []categoryFields
	used := map[string]bool{}
	var usedDefs []attributeDef
	for _, gid := range gids {
		var fields []storedField
		for _, h := range byGID[gid] {
			d, ok := defs[h]
			if !ok {
				continue
			}
			fields = append(fields, storedField{Key: h, Label: d.Label})
			if !used[h] {
				used[h] = true
				usedDefs = append(usedDefs, d)
			}
		}
		if len(fields) > 0 {
			batch = append(batch, categoryFields{GID: gid, Fields: fields})
		}
	}
	if len(batch) == 0 {
		return nil
	}
	encodedDefs, err := json.Marshal(usedDefs)
	if err != nil {
		return err
	}
	encodedBatch, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	// The same two statements ImportCategoryAttributes runs, over the subset.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO taxonomy_attributes (handle, label, choices)
		SELECT d->>'handle', d->>'label', ARRAY(SELECT jsonb_array_elements_text(d->'choices'))
		FROM jsonb_array_elements($1::jsonb) AS d
		ON CONFLICT (store_id, handle) DO UPDATE
		SET label = excluded.label, choices = excluded.choices, updated_at = now()`, encodedDefs); err != nil {
		return fmt.Errorf("write the attribute definitions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE categories c
		SET metadata = jsonb_set(c.metadata, '{attributes}', i.d->'fields', true)
		FROM jsonb_array_elements($1::jsonb) AS i(d)
		WHERE c.metadata->>'taxonomy_gid' = i.d->>'gid'
		  -- An adopted category of the store's own keeps the fields it declared.
		  AND coalesce(jsonb_array_length(c.metadata->'attributes'), 0) = 0`, encodedBatch); err != nil {
		return fmt.Errorf("attach the field lists: %w", err)
	}
	return nil
}

var (
	attrSetOnce sync.Once
	attrDefs    map[string]attributeDef
	attrByGID   map[string][]string
)

// taxonomyAttributeSet is the embedded attribute file, parsed once.
func taxonomyAttributeSet() (map[string]attributeDef, map[string][]string) {
	attrSetOnce.Do(func() {
		defs, byGID, _, _ := parseCategoryAttributes(strings.NewReader(shopifyCategoryAttributes))
		attrDefs = make(map[string]attributeDef, len(defs))
		for _, d := range defs {
			attrDefs[d.Handle] = d
		}
		attrByGID = byGID
	})
	return attrDefs, attrByGID
}
