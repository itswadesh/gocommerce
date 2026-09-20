package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// A collection is a named, hand-curated grouping of products: "New in", a
// seasonal edit, the six things on the home page.
//
// Rule-based ("smart") collections are deliberately absent. They need a query
// language, a re-evaluation schedule and a story about what happens when the
// rule changes under a live storefront; what a catalog this size actually uses
// is a list somebody picked, in the order they picked it.
type Collection struct {
	ID          int64  `json:"id"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Position orders collections against each other — a navigation menu, not
	// the order of products inside one.
	Position  int       `json:"position"`
	Metadata  Metadata  `json:"metadata"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ProductCollection is the shape a collection takes when it rides along on a
// product: enough to render a chip and link to the collection, and no more.
// Carrying the whole Collection would put a description on every row of a
// product listing that nothing renders.
type ProductCollection struct {
	ID    int64  `json:"id"`
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

// CollectionInput creates a collection.
type CollectionInput struct {
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Position    *int     `json:"position"`
	Metadata    Metadata `json:"metadata"`
}

// CollectionPatch updates one. A nil field is left alone, which is what
// distinguishes "not mentioned" from "set to empty".
type CollectionPatch struct {
	Slug        *string   `json:"slug"`
	Title       *string   `json:"title"`
	Description *string   `json:"description"`
	Position    *int      `json:"position"`
	Metadata    *Metadata `json:"metadata"`
}

// Collections owns collections and product membership in them.
//
// Nothing here emits an event. Merchandising is not a state machine: moving a
// product between collections changes what a storefront shows, not what the
// store owes anyone, and an event nothing could act on is a promise the outbox
// would have to keep forever.
// collectionEvent is what a collection change announces: enough to build the
// URL of the page that is now different, and nothing else.
type collectionEvent struct {
	ID    int64  `json:"id"`
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

type Collections struct {
	app *App
}

// Collections returns the collections service. It is built per call rather
// than held on App because it carries nothing but the App itself.
func (a *App) Collections() *Collections { return &Collections{app: a} }

// -------------------------------------------------------------------- service

const collectionColumns = `id, slug, title, description, position, metadata, created_at, updated_at`

func scanCollection(row interface{ Scan(...any) error }) (*Collection, error) {
	var c Collection
	var meta []byte
	if err := row.Scan(&c.ID, &c.Slug, &c.Title, &c.Description, &c.Position,
		&meta, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	if err := scanMetadata(meta, &c.Metadata); err != nil {
		return nil, err
	}
	return &c, nil
}

// Create adds a collection. The slug is derived from the title when omitted,
// the same way a product's is.
func (s *Collections) Create(ctx context.Context, in CollectionInput) (*Collection, error) {
	if err := normalizeCollectionInput(&in); err != nil {
		return nil, err
	}
	meta, err := in.Metadata.value()
	if err != nil {
		return nil, Validationf("metadata is not valid JSON: %v", err)
	}
	position := 0
	if in.Position != nil {
		position = *in.Position
	}
	// The single statement gains a transaction so the change and the record of
	// it commit together. There is no network I/O anywhere in the method, so
	// rule 5 is untouched.
	var c *Collection
	err = InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		var cerr error
		c, cerr = scanCollection(tx.QueryRowContext(ctx, `
			INSERT INTO collections (slug, title, description, position, metadata)
			VALUES ($1, $2, $3, $4, $5) RETURNING `+collectionColumns,
			in.Slug, in.Title, in.Description, position, meta))
		if cerr != nil {
			return translateCollectionErr(cerr)
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditCollectionCreate, Entity: AuditEntityCollection,
			ID: c.ID, Label: c.Title, Summary: "Created the collection " + c.Title,
			After: map[string]any{"slug": c.Slug, "title": c.Title, "position": c.Position},
		})
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Get loads a collection by id.
func (s *Collections) Get(ctx context.Context, id int64) (*Collection, error) {
	return s.one(ctx, `id = $1`, id)
}

// GetBySlug loads a collection by its URL slug.
func (s *Collections) GetBySlug(ctx context.Context, slug string) (*Collection, error) {
	return s.one(ctx, `slug = $1`, strings.TrimSpace(slug))
}

func (s *Collections) one(ctx context.Context, where string, arg any) (*Collection, error) {
	c, err := scanCollection(s.app.db.QueryRowContext(ctx,
		`SELECT `+collectionColumns+` FROM collections WHERE `+where, arg))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, NotFoundf("collection not found")
		}
		return nil, err
	}
	return c, nil
}

// List returns a page of every collection and the total count.
func (s *Collections) List(ctx context.Context, limit, offset int) ([]*Collection, int, error) {
	return s.list(ctx, `true`, limit, offset)
}

// activeCollections is the storefront's idea of a collection worth showing:
// one with something in it a shopper can actually buy. A collection holding
// nothing but drafts is a work in progress, and linking to an empty page is
// worse than not linking at all.
const activeCollections = `EXISTS (
	SELECT 1 FROM product_collections pc
	JOIN products p ON p.id = pc.product_id
	WHERE pc.collection_id = collections.id AND p.status = 'active')`

// ListActive returns the collections a storefront should show.
func (s *Collections) ListActive(ctx context.Context, limit, offset int) ([]*Collection, int, error) {
	return s.list(ctx, activeCollections, limit, offset)
}

func (s *Collections) list(ctx context.Context, where string, limit, offset int) ([]*Collection, int, error) {
	var total int
	if err := s.app.db.QueryRowContext(ctx,
		`SELECT count(*) FROM collections WHERE `+where).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	rows, err := s.app.db.QueryContext(ctx,
		`SELECT `+collectionColumns+` FROM collections WHERE `+where+
			` ORDER BY position, id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []*Collection{}
	for rows.Next() {
		c, err := scanCollection(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

// Update applies a patch.
func (s *Collections) Update(ctx context.Context, id int64, patch CollectionPatch) (*Collection, error) {
	sets, args := []string{}, []any{}
	after := map[string]any{}
	add := func(column string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", column, len(args)))
		after[column] = v
	}
	if patch.Slug != nil {
		slug := strings.TrimSpace(*patch.Slug)
		if slug == "" {
			return nil, Validationf("slug must not be empty")
		}
		add("slug", slug)
	}
	if patch.Title != nil {
		title := strings.TrimSpace(*patch.Title)
		if title == "" {
			return nil, Validationf("title must not be empty")
		}
		add("title", title)
	}
	if patch.Description != nil {
		add("description", strings.TrimSpace(*patch.Description))
	}
	if patch.Position != nil {
		add("position", *patch.Position)
	}
	if patch.Metadata != nil {
		meta, err := patch.Metadata.value()
		if err != nil {
			return nil, Validationf("metadata is not valid JSON: %v", err)
		}
		add("metadata", meta)
	}
	if len(sets) == 0 {
		return s.Get(ctx, id)
	}
	sets = append(sets, "updated_at = now()")
	args = append(args, id)

	var c *Collection
	err := InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		was, err := scanCollection(tx.QueryRowContext(ctx,
			`SELECT `+collectionColumns+` FROM collections WHERE id = $1 FOR UPDATE`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("collection %d does not exist", id)
		}
		if err != nil {
			return err
		}
		before := map[string]any{}
		for column, v := range map[string]any{
			"slug": was.Slug, "title": was.Title,
			"description": was.Description, "position": was.Position,
		} {
			if _, changed := after[column]; changed {
				before[column] = v
			}
		}

		c, err = scanCollection(tx.QueryRowContext(ctx,
			"UPDATE collections SET "+strings.Join(sets, ", ")+
				fmt.Sprintf(" WHERE id = $%d RETURNING ", len(args))+collectionColumns, args...))
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("collection %d does not exist", id)
		}
		if err != nil {
			return translateCollectionErr(err)
		}
		if err := writeAudit(ctx, tx, auditRecord{
			Action: AuditCollectionUpdate, Entity: AuditEntityCollection,
			ID: c.ID, Label: c.Title, Summary: "Edited the collection " + c.Title,
			Before: before, After: after,
		}); err != nil {
			return err
		}
		return s.app.outbox.write(ctx, tx, EventCollectionUpdated, AggregateCollection, c.ID,
			collectionEvent{ID: c.ID, Slug: c.Slug, Title: c.Title})
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Delete removes a collection and the membership rows pointing at it. The
// products themselves are untouched: a collection groups products, it does not
// own them, and deleting "Summer sale" must not delete the summer stock.
func (s *Collections) Delete(ctx context.Context, id int64) error {
	return InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		var slug, title string
		err := tx.QueryRowContext(ctx,
			`DELETE FROM collections WHERE id = $1 RETURNING slug, title`, id).Scan(&slug, &title)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("collection %d does not exist", id)
		}
		if err != nil {
			return err
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditCollectionDelete, Entity: AuditEntityCollection,
			ID: id, Label: title, Summary: "Deleted the collection " + title,
			Before: map[string]any{"slug": slug, "title": title},
		})
	})
}

// SetProductCollections replaces a product's membership with exactly
// collectionIDs, in the order given.
//
// Replace rather than merge, because the caller holds the whole list: an "add"
// endpoint quietly re-creates a membership the operator removed in another tab,
// and there is no way for them to see that it happened.
func (s *Collections) SetProductCollections(ctx context.Context, productID int64, collectionIDs []int64) error {
	ids := dedupeIDs(collectionIDs)
	return InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM products WHERE id = $1)`, productID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return NotFoundf("product %d does not exist", productID)
		}
		if err := requireCollections(ctx, tx, ids); err != nil {
			return err
		}
		// A reconcile, not a delete and a re-insert: a membership that
		// survives the save keeps the place a curator gave it inside that
		// collection (D40).
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM product_collections
			WHERE product_id = $1 AND NOT (collection_id = ANY($2::bigint[]))`,
			productID, int64Array(ids)); err != nil {
			return err
		}
		if len(ids) > 0 {
			// One statement, ordered by WITH ORDINALITY: the array's order is
			// the caller's and it survives, without a round trip per
			// collection. dedupeIDs above is load-bearing rather than tidy —
			// ON CONFLICT cannot touch one row twice in a single statement,
			// the hazard parseCategoryAttributes already documents for the
			// attribute importer.
			//
			// member_position is computed, never sent: where this product
			// sits inside each collection is that collection's curation, so a
			// membership that survives keeps its place and a new one lands at
			// the end rather than at the front of somebody's home page. The
			// subquery is per collection_id and each row of this statement
			// names a different one, so no two rows collide on it.
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO product_collections (product_id, collection_id, position, member_position)
				SELECT $1, ids.collection_id, ids.ord - 1,
				       coalesce((SELECT max(pc.member_position) + 1 FROM product_collections pc
				                 WHERE pc.collection_id = ids.collection_id), 0)
				FROM unnest($2::bigint[]) WITH ORDINALITY AS ids(collection_id, ord)
				ON CONFLICT (product_id, collection_id)
				DO UPDATE SET position = excluded.position`,
				productID, int64Array(ids)); err != nil {
				return err
			}
		}
		// Filed against the product rather than against each collection: a
		// merchandiser asks what happened to this product, and collections have
		// no screen of their own to ask it from.
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditProductCollectionsSet, Entity: AuditEntityProduct,
			ID: productID, Summary: "Changed which collections this product is in",
			After: map[string]any{"collection_ids": ids},
		})
	})
}

// requireCollections rejects the whole request when any id is unknown, so a
// typo in one of five ids does not silently store the other four.
func requireCollections(ctx context.Context, tx *sql.Tx, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM collections WHERE id = ANY($1::bigint[])`, int64Array(ids))
	if err != nil {
		return err
	}
	defer rows.Close()

	found := make(map[int64]bool, len(ids))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		found[id] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if !found[id] {
			return NotFoundf("collection %d does not exist", id)
		}
	}
	return nil
}

// requireProducts rejects the whole request when any id is unknown, for the
// reason requireCollections gives: a typo in one of five ids must not
// silently curate the other four.
func requireProducts(ctx context.Context, tx *sql.Tx, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM products WHERE id = ANY($1::bigint[])`, int64Array(ids))
	if err != nil {
		return err
	}
	defer rows.Close()

	found := make(map[int64]bool, len(ids))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		found[id] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if !found[id] {
			return NotFoundf("product %d does not exist", id)
		}
	}
	return nil
}

// ProductsInCollection returns a page of a collection's members in the order
// they were curated. Narrow it with q — Status in particular, since a
// storefront wants the active ones and the panel wants all of them.
func (s *Collections) ProductsInCollection(ctx context.Context, collectionID int64, q ProductQuery) ([]*Product, int, error) {
	q.CollectionID = collectionID
	return s.app.catalog.ListProducts(ctx, q)
}

// SetCollectionProducts replaces a collection's membership with exactly
// productIDs, in the order given — which is the order a storefront shows them
// in, and until now the only order nothing could choose.
//
// Replace rather than merge, for the reason SetProductCollections gives. It
// writes member_position and never position: where a collection sits in one
// product's own list is that product's business, and a curation pass must not
// reshuffle six product editors' chips (D40).
func (s *Collections) SetCollectionProducts(ctx context.Context, collectionID int64, productIDs []int64) error {
	ids := dedupeIDs(productIDs)
	return InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		var title, slug string
		err := tx.QueryRowContext(ctx,
			`SELECT title, slug FROM collections WHERE id = $1`, collectionID).Scan(&title, &slug)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("collection %d does not exist", collectionID)
		}
		if err != nil {
			return err
		}
		if err := requireProducts(ctx, tx, ids); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM product_collections
			WHERE collection_id = $1 AND NOT (product_id = ANY($2::bigint[]))`,
			collectionID, int64Array(ids)); err != nil {
			return err
		}
		if len(ids) > 0 {
			// The mirror of the product-side write: ordinality carries the
			// caller's order into member_position, and `position` — where this
			// collection sits in each product's own list — is computed so a new
			// membership lands at the end of that list instead of at its head.
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO product_collections (product_id, collection_id, position, member_position)
				SELECT ids.product_id, $1,
				       coalesce((SELECT max(pc.position) + 1 FROM product_collections pc
				                 WHERE pc.product_id = ids.product_id), 0),
				       ids.ord - 1
				FROM unnest($2::bigint[]) WITH ORDINALITY AS ids(product_id, ord)
				ON CONFLICT (product_id, collection_id)
				DO UPDATE SET member_position = excluded.member_position`,
				collectionID, int64Array(ids)); err != nil {
				return err
			}
		}
		// Filed against the collection, which is the record an operator was
		// looking at. The product-side write files the same kind of change
		// against the product for the same reason.
		if err := writeAudit(ctx, tx, auditRecord{
			Action: AuditCollectionProductsSet, Entity: AuditEntityCollection,
			ID: collectionID, Label: title,
			Summary: "Changed what is in the collection " + title + ", and in what order",
			After:   map[string]any{"product_ids": ids},
		}); err != nil {
			return err
		}
		// The same event as an edit to the collection, because a consumer asks
		// the same question of both: the page is different, go and look.
		return s.app.outbox.write(ctx, tx, EventCollectionUpdated, AggregateCollection, collectionID,
			collectionEvent{ID: collectionID, Slug: slug, Title: title})
	})
}

// loadProductCollections attaches each product's collections in one query for
// a whole page, which is what keeps a catalog listing from costing a query per
// product.
func (a *App) loadProductCollections(ctx context.Context, byID map[int64]*Product, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := a.db.QueryContext(ctx, `
		SELECT pc.product_id, c.id, c.slug, c.title
		FROM product_collections pc
		JOIN collections c ON c.id = pc.collection_id
		WHERE pc.product_id = ANY($1::bigint[])
		ORDER BY pc.position, c.id`, int64Array(ids))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var productID int64
		var pc ProductCollection
		if err := rows.Scan(&productID, &pc.ID, &pc.Slug, &pc.Title); err != nil {
			return err
		}
		if p := byID[productID]; p != nil {
			p.Collections = append(p.Collections, pc)
		}
	}
	return rows.Err()
}

// ------------------------------------------------------------------- helpers

func normalizeCollectionInput(in *CollectionInput) error {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return Validationf("title is required")
	}
	if in.Slug = strings.TrimSpace(in.Slug); in.Slug == "" {
		in.Slug = slugify(in.Title)
	}
	if in.Slug == "" {
		return Validationf("slug could not be derived from the title; supply one")
	}
	in.Description = strings.TrimSpace(in.Description)
	return nil
}

// dedupeIDs keeps the first occurrence of each id. Order is the caller's
// curation, so it survives; a repeated id is an intent expressed twice, not a
// primary-key violation to report back.
func dedupeIDs(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func translateCollectionErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "collections_slug_key") {
		return Conflictf("that slug is already used by another collection")
	}
	return err
}

// -------------------------------------------------------------------- routes

// mountCollectionRoutes wires the collection endpoints. It is called from
// mountCoreRoutes alongside the other mount*Routes.
func (a *App) mountCollectionRoutes() {
	a.HandleFunc("GET /api/collections", a.handleListCollections)
	a.HandleFunc("GET /api/collections/{slug}", a.handleGetCollectionBySlug)

	a.HandleAdminFunc("GET /api/admin/collections", a.handleAdminListCollections, RightCollectionsRead)
	a.HandleAdminFunc("POST /api/admin/collections", a.handleCreateCollection, RightCollectionsWrite)
	a.HandleAdminFunc("GET /api/admin/collections/{id}", a.handleAdminGetCollection, RightCollectionsRead)
	a.HandleAdminFunc("PATCH /api/admin/collections/{id}", a.handleUpdateCollection, RightCollectionsWrite)
	a.HandleAdminFunc("DELETE /api/admin/collections/{id}", a.handleDeleteCollection, RightCollectionsWrite)
	a.HandleAdminFunc("PUT /api/admin/products/{id}/collections", a.handleSetProductCollections, RightCollectionsWrite)
	// The other axis: what is in one collection, and in what order. Reading it
	// is catalog.read because it is a product listing; writing it is the
	// same right that moves a product between collections.
	a.HandleAdminFunc("GET /api/admin/collections/{id}/products", a.handleAdminListCollectionProducts, RightCollectionsRead)
	a.HandleAdminFunc("PUT /api/admin/collections/{id}/products", a.handleSetCollectionProducts, RightCollectionsWrite)
}

// ------------------------------------------------------------------- public

func (a *App) handleListCollections(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := Page(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	list, total, err := a.Collections().ListActive(r.Context(), limit, offset)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	RespondList(w, list, ListMeta{Total: total, Limit: limit, Offset: offset})
}

// collectionPage is the storefront's collection view: the collection, flattened
// into the response, plus the page of active products in it. One request, not
// two, because there is no useful moment at which a client has one and not the
// other.
type collectionPage struct {
	*Collection
	Products []*Product `json:"products"`
}

func (a *App) handleGetCollectionBySlug(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := Page(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	collections := a.Collections()
	c, err := collections.GetBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	products, total, err := collections.ProductsInCollection(r.Context(), c.ID, ProductQuery{
		Status: ProductActive, Limit: limit, Offset: offset,
	})
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if products == nil {
		products = []*Product{}
	}
	a.translateProducts(r, products)

	// meta describes the products: the collection is a single record, and the
	// only thing on this response a client can page through is what is in it.
	RespondList(w, collectionPage{Collection: c, Products: products},
		ListMeta{Total: total, Limit: limit, Offset: offset})
}

// -------------------------------------------------------------------- admin

func (a *App) handleAdminListCollections(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := Page(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	list, total, err := a.Collections().List(r.Context(), limit, offset)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	RespondList(w, list, ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (a *App) handleAdminGetCollection(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	c, err := a.Collections().Get(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, c)
}

func (a *App) handleCreateCollection(w http.ResponseWriter, r *http.Request) {
	var in CollectionInput
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	c, err := a.Collections().Create(r.Context(), in)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusCreated, c)
}

func (a *App) handleUpdateCollection(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var patch CollectionPatch
	if err := DecodeJSON(w, r, &patch); err != nil {
		RespondError(w, r, err)
		return
	}
	c, err := a.Collections().Update(r.Context(), id, patch)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, c)
}

func (a *App) handleDeleteCollection(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.Collections().Delete(r.Context(), id); err != nil {
		RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSetProductCollections replaces the product's set and answers with the
// product, so the caller sees the membership the store now holds rather than
// the one it just asked for.
func (a *App) handleSetProductCollections(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var in struct {
		CollectionIDs []int64 `json:"collection_ids"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.Collections().SetProductCollections(r.Context(), id, in.CollectionIDs); err != nil {
		RespondError(w, r, err)
		return
	}
	p, err := a.catalog.GetProduct(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, p)
}

// handleAdminListCollectionProducts is the read that pairs with the PUT below.
//
// ?collection_id= on the product listing answers the same question; this one
// 404s for a collection that does not exist rather than returning an empty
// page, which is what a curation screen has to know before it offers to
// reorder anything. Drafts are included: staging them is what an operator is
// doing here, and only the public slug route is active-only.
func (a *App) handleAdminListCollectionProducts(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	limit, offset, err := Page(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	collections := a.Collections()
	if _, err := collections.Get(r.Context(), id); err != nil {
		RespondError(w, r, err)
		return
	}
	pq, err := productQueryFrom(r.URL.Query())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// The path already named the collection, so a collection_id in the query
	// string is a filter that would be silently discarded — the exact failure
	// queryInt64 refuses to allow for a value it cannot parse.
	if pq.CollectionID > 0 {
		RespondError(w, r, Validationf(
			"collection_id is not a filter on this route; the path already names the collection"))
		return
	}
	pq.Limit, pq.Offset = limit, offset
	products, total, err := collections.ProductsInCollection(r.Context(), id, pq)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if products == nil {
		products = []*Product{}
	}
	RespondList(w, products, ListMeta{Total: total, Limit: limit, Offset: offset})
}

// handleSetCollectionProducts replaces the membership and answers 204.
//
// No echo: the body a curation screen needs is the whole membership, and a
// whole membership is exactly what a paged list cannot promise — an echo
// bounded by DefaultLimit after a PUT of 300 ids is a list the screen could
// re-save from and lose the tail. The GET above is the read.
func (a *App) handleSetCollectionProducts(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var in struct {
		ProductIDs *[]int64 `json:"product_ids"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	// A pointer, because DecodeJSON rejects a wrong key but nothing makes a
	// right one mandatory — and a body of {} would otherwise decode to nil and
	// empty a collection that may hold thousands of memberships. An explicit []
	// stays the deliberate way to clear it.
	if in.ProductIDs == nil {
		RespondError(w, r, Validationf(
			"product_ids is required; send an empty array to empty the collection"))
		return
	}
	if err := a.Collections().SetCollectionProducts(r.Context(), id, *in.ProductIDs); err != nil {
		RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
