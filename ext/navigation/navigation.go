// Package navigation is the storefront's menus: a header, a footer, a
// sidebar, each a tree of items an operator arranges by hand.
//
// An item points at something — a product, a collection, a category, a
// content page, the home page, or any URL — by handle rather than by id, so
// a menu survives a re-import and reads sensibly in an export. The public
// route resolves every item to a storefront path using the paths the
// Plugins screen holds, so a storefront renders the tree without knowing
// what kind of thing each item is; the kind travels too, for one that wants
// to know.
//
// The whole tree of a menu is saved in one call, because that is how it is
// edited: dragged into shape and saved, not one item at a time.
package navigation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/misiki/gocommerce/core"
)

const pluginKey = "navigation"

// Kinds an item may be.
const (
	KindURL        = "url"
	KindHome       = "home"
	KindProduct    = "product"
	KindCollection = "collection"
	KindCategory   = "category"
	KindPage       = "page"
)

var kinds = map[string]bool{KindURL: true, KindHome: true, KindProduct: true, KindCollection: true, KindCategory: true, KindPage: true}

// Config configures the module. Every path can also be set from the
// Plugins screen, which wins.
type Config struct {
	ProductPath, CollectionPath, CategoryPath, PagePath string
}

// Menu is one menu with its tree.
type Menu struct {
	ID        int64     `json:"id"`
	Handle    string    `json:"handle"`
	Title     string    `json:"title"`
	Items     []*Item   `json:"items"`
	ItemCount int       `json:"item_count"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Item is one entry, with the entries under it.
type Item struct {
	ID       int64   `json:"id,omitempty"`
	Title    string  `json:"title"`
	Kind     string  `json:"kind"`
	Target   string  `json:"target,omitempty"`
	URL      string  `json:"url"`
	Children []*Item `json:"children"`
}

// MenuInput creates or renames a menu.
type MenuInput struct {
	Handle string `json:"handle"`
	Title  string `json:"title"`
}

// Module is the routes and the tables.
type Module struct {
	cfg Config
	app *gocommerce.App
	db  *sql.DB
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "navigation" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_menus",
		SQL: `
CREATE TABLE navigation_menus (
    id         bigserial   PRIMARY KEY,
    handle     text        NOT NULL UNIQUE,
    title      text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
-- An item's place is its parent and its position; the tree is rewritten
-- whole on every save, so nothing here is ever updated in place.
CREATE TABLE navigation_items (
    id        bigserial PRIMARY KEY,
    menu_id   bigint    NOT NULL REFERENCES navigation_menus (id) ON DELETE CASCADE,
    parent_id bigint    REFERENCES navigation_items (id) ON DELETE CASCADE,
    title     text      NOT NULL,
    kind      text      NOT NULL CHECK (kind IN ('url', 'home', 'product', 'collection', 'category', 'page')),
    target    text      NOT NULL DEFAULT '',
    position  integer   NOT NULL DEFAULT 0
);
CREATE INDEX navigation_items_menu_idx ON navigation_items (menu_id, parent_id, position);
`,
	}, {
		// A store has a header and a footer from day one, as a Shopify store
		// has a main menu and a footer menu: a storefront can ask for them by
		// handle before anyone has opened the Menus screen. Only where none
		// exist — a store that already built its own keeps them.
		ID: "0002_default_menus",
		SQL: `
INSERT INTO navigation_menus (handle, title)
SELECT 'header', 'Header'
WHERE NOT EXISTS (SELECT 1 FROM navigation_menus WHERE handle = 'header');
INSERT INTO navigation_menus (handle, title)
SELECT 'footer', 'Footer'
WHERE NOT EXISTS (SELECT 1 FROM navigation_menus WHERE handle = 'footer');
INSERT INTO navigation_items (menu_id, title, kind, target, position)
SELECT m.id, v.title, v.kind, v.target, v.position
FROM navigation_menus m
JOIN (VALUES ('Home', 'home', '', 0), ('Shop', 'url', '/products', 1), ('Contact', 'url', '/contact', 2))
    AS v (title, kind, target, position) ON true
WHERE m.handle = 'header' AND NOT EXISTS (SELECT 1 FROM navigation_items i WHERE i.menu_id = m.id);
INSERT INTO navigation_items (menu_id, title, kind, target, position)
SELECT m.id, v.title, v.kind, v.target, v.position
FROM navigation_menus m
JOIN (VALUES ('Search', 'url', '/search', 0), ('Contact us', 'url', '/contact', 1), ('Privacy policy', 'url', '/pages/privacy', 2))
    AS v (title, kind, target, position) ON true
WHERE m.handle = 'footer' AND NOT EXISTS (SELECT 1 FROM navigation_items i WHERE i.menu_id = m.id);
`,
	}}
}

var handleRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.registerRights(app)
	m.app = app
	m.db = app.DB()
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: pluginKey, Title: "Menus", Category: "storefront", DefaultEnabled: true,
		Description: "The storefront's menus — header, footer, wherever — as trees of links to products, collections, categories, pages and URLs, edited from the Menus screen and served at /x/navigation/menus/{handle}.",
		Fields: []gocommerce.PluginField{
			{Key: "product_path", Label: "Product page path", Kind: "text", Default: "/products/{slug}", Public: true},
			{Key: "collection_path", Label: "Collection page path", Kind: "text", Default: "/collections/{slug}", Public: true},
			{Key: "category_path", Label: "Category page path", Kind: "text", Default: "/categories/{slug}", Public: true},
			{Key: "page_path", Label: "Content page path", Kind: "text", Default: "/pages/{slug}", Public: true},
		},
	})
	app.HandleFunc("GET /x/navigation/menus", m.handlePublicList)
	app.HandleFunc("GET /x/navigation/menus/{handle}", m.handlePublicGet)

	// A menu names the things the catalogue sells, but arranging one is not
	// editing them: its own rights, so a store can hand the storefront's
	// navigation to somebody without handing them the products.
	app.HandleAdminFunc("GET /api/admin/x/navigation/menus", m.handleList, rightMenusRead)
	app.HandleAdminFunc("POST /api/admin/x/navigation/menus", m.handleCreate, rightMenusWrite)
	app.HandleAdminFunc("GET /api/admin/x/navigation/menus/{id}", m.handleGet, rightMenusRead)
	app.HandleAdminFunc("PATCH /api/admin/x/navigation/menus/{id}", m.handleUpdate, rightMenusWrite)
	app.HandleAdminFunc("PUT /api/admin/x/navigation/menus/{id}/items", m.handleSetItems, rightMenusWrite)
	app.HandleAdminFunc("DELETE /api/admin/x/navigation/menus/{id}", m.handleDelete, rightMenusWrite)
	m.mountTransferRoutes(app)
	return nil
}

// paths is where each kind of thing lives on the storefront.
type paths struct{ product, collection, category, page string }

func (m *Module) paths(ctx context.Context) paths {
	settings, _ := m.app.Plugins().Settings(ctx, pluginKey)
	str := func(key, fallback, def string) string {
		if s, _ := settings[key].(string); strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
		if fallback != "" {
			return fallback
		}
		return def
	}
	return paths{
		product:    str("product_path", m.cfg.ProductPath, "/products/{slug}"),
		collection: str("collection_path", m.cfg.CollectionPath, "/collections/{slug}"),
		category:   str("category_path", m.cfg.CategoryPath, "/categories/{slug}"),
		page:       str("page_path", m.cfg.PagePath, "/pages/{slug}"),
	}
}

func (p paths) resolve(kind, target string) string {
	at := func(pattern string) string { return strings.ReplaceAll(pattern, "{slug}", target) }
	switch kind {
	case KindHome:
		return "/"
	case KindProduct:
		return at(p.product)
	case KindCollection:
		return at(p.collection)
	case KindCategory:
		return at(p.category)
	case KindPage:
		return at(p.page)
	}
	return target
}

// ----------------------------------------------------------------- reading

type flatItem struct {
	id, parent  int64
	title, kind string
	target      string
	position    int
}

// tree reads a menu's items and nests them.
func (m *Module) tree(ctx context.Context, menuID int64, p paths) ([]*Item, int, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT id, coalesce(parent_id, 0), title, kind, target, position
		 FROM navigation_items WHERE menu_id = $1 ORDER BY parent_id NULLS FIRST, position, id`, menuID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var flat []flatItem
	for rows.Next() {
		var f flatItem
		if err := rows.Scan(&f.id, &f.parent, &f.title, &f.kind, &f.target, &f.position); err != nil {
			return nil, 0, err
		}
		flat = append(flat, f)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	byID := map[int64]*Item{}
	for _, f := range flat {
		byID[f.id] = &Item{ID: f.id, Title: f.title, Kind: f.kind, Target: f.target, URL: p.resolve(f.kind, f.target), Children: []*Item{}}
	}
	roots := []*Item{}
	for _, f := range flat {
		if f.parent == 0 {
			roots = append(roots, byID[f.id])
		} else if parent := byID[f.parent]; parent != nil {
			parent.Children = append(parent.Children, byID[f.id])
		}
	}
	return roots, len(flat), nil
}

func (m *Module) menu(ctx context.Context, where string, arg any) (*Menu, error) {
	var mn Menu
	err := m.db.QueryRowContext(ctx,
		`SELECT id, handle, title, created_at, updated_at FROM navigation_menus WHERE `+where, arg).
		Scan(&mn.ID, &mn.Handle, &mn.Title, &mn.CreatedAt, &mn.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("no such menu")
	}
	if err != nil {
		return nil, err
	}
	items, n, err := m.tree(ctx, mn.ID, m.paths(ctx))
	if err != nil {
		return nil, err
	}
	mn.Items, mn.ItemCount = items, n
	return &mn, nil
}

// ----------------------------------------------------------------- public

func (m *Module) handlePublicList(w http.ResponseWriter, r *http.Request) {
	rows, err := m.db.QueryContext(r.Context(), `SELECT handle, title FROM navigation_menus ORDER BY title`)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	defer rows.Close()
	type summary struct {
		Handle string `json:"handle"`
		Title  string `json:"title"`
	}
	out := []summary{}
	for rows.Next() {
		var s summary
		if err := rows.Scan(&s.Handle, &s.Title); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		out = append(out, s)
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

func (m *Module) handlePublicGet(w http.ResponseWriter, r *http.Request) {
	mn, err := m.menu(r.Context(), "handle = $1", r.PathValue("handle"))
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, mn)
}

// ------------------------------------------------------------------ admin

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	rows, err := m.db.QueryContext(r.Context(), `
		SELECT n.id, n.handle, n.title, n.created_at, n.updated_at,
		       (SELECT count(*) FROM navigation_items i WHERE i.menu_id = n.id)
		FROM navigation_menus n ORDER BY n.title`)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	defer rows.Close()
	out := []*Menu{}
	for rows.Next() {
		var mn Menu
		if err := rows.Scan(&mn.ID, &mn.Handle, &mn.Title, &mn.CreatedAt, &mn.UpdatedAt, &mn.ItemCount); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		mn.Items = []*Item{}
		out = append(out, &mn)
	}
	gocommerce.RespondList(w, out, gocommerce.ListMeta{Total: len(out), Limit: len(out) + 1})
}

func (m *Module) handleCreate(w http.ResponseWriter, r *http.Request) {
	var in MenuInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	in.Handle = strings.ToLower(strings.TrimSpace(in.Handle))
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		gocommerce.RespondError(w, r, gocommerce.Validationf("a menu needs a title"))
		return
	}
	if in.Handle == "" {
		in.Handle = slugify(in.Title)
	}
	if !handleRE.MatchString(in.Handle) {
		gocommerce.RespondError(w, r, gocommerce.Validationf("handle must be lowercase letters, digits and single dashes"))
		return
	}
	var id int64
	err := m.db.QueryRowContext(r.Context(),
		`INSERT INTO navigation_menus (handle, title) VALUES ($1, $2) RETURNING id`, in.Handle, in.Title).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "navigation_menus_handle_key") {
			gocommerce.RespondError(w, r, gocommerce.Conflictf("a menu already has the handle %q", in.Handle))
			return
		}
		gocommerce.RespondError(w, r, err)
		return
	}
	mn, err := m.menu(r.Context(), "id = $1", id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusCreated, mn)
}

func (m *Module) handleGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	mn, err := m.menu(r.Context(), "id = $1", id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, mn)
}

func (m *Module) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in struct {
		Handle *string `json:"handle"`
		Title  *string `json:"title"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	sets, args := []string{"updated_at = now()"}, []any{}
	if in.Title != nil {
		if strings.TrimSpace(*in.Title) == "" {
			gocommerce.RespondError(w, r, gocommerce.Validationf("a menu needs a title"))
			return
		}
		args = append(args, strings.TrimSpace(*in.Title))
		sets = append(sets, fmt.Sprintf("title = $%d", len(args)))
	}
	if in.Handle != nil {
		h := strings.ToLower(strings.TrimSpace(*in.Handle))
		if !handleRE.MatchString(h) {
			gocommerce.RespondError(w, r, gocommerce.Validationf("handle must be lowercase letters, digits and single dashes"))
			return
		}
		args = append(args, h)
		sets = append(sets, fmt.Sprintf("handle = $%d", len(args)))
	}
	args = append(args, id)
	res, err := m.db.ExecContext(r.Context(),
		"UPDATE navigation_menus SET "+strings.Join(sets, ", ")+fmt.Sprintf(" WHERE id = $%d", len(args)), args...)
	if err != nil {
		if strings.Contains(err.Error(), "navigation_menus_handle_key") {
			gocommerce.RespondError(w, r, gocommerce.Conflictf("a menu already has that handle"))
			return
		}
		gocommerce.RespondError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("menu %d does not exist", id))
		return
	}
	mn, err := m.menu(r.Context(), "id = $1", id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, mn)
}

// handleSetItems replaces a menu's whole tree in one transaction. Ids are
// not kept: an item is its title, kind and target in a place, and a tree
// dragged into a new shape is a new tree.
func (m *Module) handleSetItems(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in struct {
		Items []*Item `json:"items"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if err := validateItems(in.Items, 0); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	err = gocommerce.InTx(r.Context(), m.db, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS (SELECT 1 FROM navigation_menus WHERE id = $1)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return gocommerce.NotFoundf("menu %d does not exist", id)
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM navigation_items WHERE menu_id = $1`, id); err != nil {
			return err
		}
		if err := insertItems(r.Context(), tx, id, nil, in.Items); err != nil {
			return err
		}
		_, err := tx.ExecContext(r.Context(), `UPDATE navigation_menus SET updated_at = now() WHERE id = $1`, id)
		return err
	})
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	mn, err := m.menu(r.Context(), "id = $1", id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, mn)
}

// maxMenuDepth caps a menu's nesting. Four is what a header bar can render
// without becoming a filesystem browser, and it is the number the CSV
// importer checks per row so that too deep costs one row rather than a file.
const maxMenuDepth = 4

func validateItems(items []*Item, depth int) error {
	// An empty child list is not a level. The depth check used to run before
	// this, so a leaf at the fourth level recursed into its own nil children
	// at depth four and tripped the limit — which made "at most four levels"
	// mean three, and only for anybody who tried it.
	if len(items) == 0 {
		return nil
	}
	if depth >= maxMenuDepth {
		return gocommerce.Validationf("a menu goes at most %d levels deep", maxMenuDepth)
	}
	for _, it := range items {
		if it == nil {
			return gocommerce.Validationf("an item is missing")
		}
		it.Title = strings.TrimSpace(it.Title)
		it.Target = strings.TrimSpace(it.Target)
		if it.Title == "" {
			return gocommerce.Validationf("every item needs a title")
		}
		if it.Kind == "" {
			it.Kind = KindURL
		}
		if !kinds[it.Kind] {
			return gocommerce.Validationf("item %q: kind must be url, home, product, collection, category or page", it.Title)
		}
		if it.Kind != KindHome && it.Target == "" {
			return gocommerce.Validationf("item %q needs a link: a URL, or the handle of what it points at", it.Title)
		}
		if err := validateItems(it.Children, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func insertItems(ctx context.Context, tx *sql.Tx, menuID int64, parent *int64, items []*Item) error {
	for i, it := range items {
		var id int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO navigation_items (menu_id, parent_id, title, kind, target, position)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			menuID, parent, it.Title, it.Kind, it.Target, i).Scan(&id); err != nil {
			return err
		}
		if err := insertItems(ctx, tx, menuID, &id, it.Children); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	res, err := m.db.ExecContext(r.Context(), `DELETE FROM navigation_menus WHERE id = $1`, id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("menu %d does not exist", id))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, gocommerce.Validationf("id must be a positive integer")
	}
	return id, nil
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	return strings.Trim(slugRE.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// MarshalJSON keeps an item's children as [] rather than null, which is what
// a storefront's loop wants.
func (it *Item) MarshalJSON() ([]byte, error) {
	type alias Item
	a := alias(*it)
	if a.Children == nil {
		a.Children = []*Item{}
	}
	return json.Marshal(a)
}

// The rights this module's own screens are gated on.
//
// They were core's until now — catalog.read and catalog.write —
// which meant the permission could not be given without the thing it was
// borrowed from, and the screens had no row of their own on the Roles grid.
// Default names the roles that held the old right, so nobody's access moves.
const (
	rightMenusRead  gocommerce.Right = "menus.read"  // was catalog.read
	rightMenusWrite gocommerce.Right = "menus.write" // was catalog.write
)

func (m *Module) registerRights(app *gocommerce.App) {
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightMenusRead,
		Label:   "See the storefront menus",
		Scope:   "The header, the footer and any other menu",
		Default: []string{gocommerce.RoleManager, gocommerce.RoleStaff},
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightMenusWrite,
		Label:   "Arrange the storefront menus",
		Scope:   "Adding, moving and removing items",
		Default: []string{gocommerce.RoleManager},
	})
}
