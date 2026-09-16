// Package wishlist is what shoppers wanted and did not buy yet.
//
// A list is keyed by a token the storefront holds, the way a cart is: a
// guest saves something before there is an account to save it to, and the
// same list follows them into one when they make it. The shop's side of it
// is the more interesting half — what is wished for most, and which of
// those the store has run out of, which is a demand signal no other screen
// in the panel carries.
//
//	app, err := gocommerce.New(cfg, wishlist.New(wishlist.Config{}))
package wishlist

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/misiki/gocommerce/core"
)

const (
	pluginKey = "wishlist"
	// maxItems bounds one list. A wishlist is a shortlist; a thousand rows
	// under one token is a script, not a shopper.
	maxItems = 200
)

// Config configures the module. Nothing here is required.
type Config struct {
	// MaxItems overrides how many products one list may hold. Zero takes
	// the default of 200.
	MaxItems int
}

// List is one shopper's wishlist.
type List struct {
	ID    int64  `json:"id"`
	Token string `json:"token,omitempty"`
	// Email is optional and set by the storefront when it knows who this
	// is — so a shop can write to the people waiting for a restock.
	Email     string    `json:"email,omitempty"`
	Items     []Item    `json:"items"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Item is one product on a list, with enough of the product to draw it.
type Item struct {
	ID        int64  `json:"id"`
	ProductID int64  `json:"product_id"`
	VariantID *int64 `json:"variant_id,omitempty"`
	Title     string `json:"title"`
	Slug      string `json:"slug"`
	SKU       string `json:"sku,omitempty"`
	// Price is the variant's if the item names one, the product's cheapest
	// otherwise. Absent for a product that has since been deleted.
	Price *gocommerce.Money `json:"price,omitempty"`
	// Available is what the store could sell this minute. A wishlist is
	// where "notify me" lives, so whether it is in stock is the point.
	Available int       `json:"available"`
	InStock   bool      `json:"in_stock"`
	Deleted   bool      `json:"deleted,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Wanted is one product and how many people want it.
type Wanted struct {
	ProductID int64             `json:"product_id"`
	Title     string            `json:"title"`
	Slug      string            `json:"slug"`
	Status    string            `json:"status,omitempty"`
	Lists     int               `json:"lists"`
	Available int               `json:"available"`
	InStock   bool              `json:"in_stock"`
	Price     *gocommerce.Money `json:"price,omitempty"`
	// Waiting is how many of those lists gave an email, which is how many
	// people a restock notice could actually reach.
	Waiting int `json:"waiting"`
}

// ItemInput is what the storefront adds.
type ItemInput struct {
	ProductID int64  `json:"product_id"`
	VariantID *int64 `json:"variant_id"`
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
func (m *Module) Name() string { return "wishlist" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_wishlists",
		SQL: `
CREATE TABLE wishlists (
    id         bigserial   PRIMARY KEY,
    -- The only credential a guest holds, so it is 256 bits from crypto/rand,
    -- the same as a cart token.
    token      text        NOT NULL UNIQUE,
    email      text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX wishlists_email_idx ON wishlists (lower(email)) WHERE email <> '';

CREATE TABLE wishlist_items (
    id          bigserial   PRIMARY KEY,
    wishlist_id bigint      NOT NULL REFERENCES wishlists (id) ON DELETE CASCADE,
    -- Not a foreign key, for the reason a review's product_id is not one: a
    -- product that has been deleted was still wanted, and the ranking should
    -- say so rather than lose the row.
    product_id  bigint      NOT NULL,
    variant_id  bigint,
    created_at  timestamptz NOT NULL DEFAULT now()
);
-- Saving the same thing twice is not an error and not a second row. The
-- coalesce is what makes that true for a whole-product save as well, since
-- two NULLs are distinct to a plain unique index.
CREATE UNIQUE INDEX wishlist_items_once_idx
    ON wishlist_items (wishlist_id, product_id, coalesce(variant_id, 0));
CREATE INDEX wishlist_items_product_idx ON wishlist_items (product_id);
`,
	}}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.registerRights(app)
	m.app = app
	m.db = app.DB()
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: pluginKey, Title: "Wishlist", Category: "storefront", DefaultEnabled: true,
		Description: "A heart on every product and a page of the ones a shopper saved. The Wishlists screen shows what is wanted most and how much of it is out of stock.",
		Fields: []gocommerce.PluginField{
			{Key: "heading", Label: "Page heading", Kind: "text", Public: true, Default: "Your wishlist"},
			{Key: "collect_email", Label: "Ask for an email on the wishlist page", Kind: "bool", Public: true,
				Help: "So the shop can write when something wished for comes back in stock."},
		},
	})

	app.HandleFunc("POST /x/wishlist", m.handleCreate)
	app.HandleFunc("GET /x/wishlist/{token}", m.handleGet)
	app.HandleFunc("PATCH /x/wishlist/{token}", m.handleSetEmail)
	app.HandleFunc("POST /x/wishlist/{token}/items", m.handleAdd)
	app.HandleFunc("DELETE /x/wishlist/{token}/items/{id}", m.handleRemove)

	// A wishlist is a shopper's own data, so it reads with the customers'
	// right rather than the catalogue's — even the ranking, which is that
	// data counted.
	app.HandleAdminFunc("GET /api/admin/x/wishlist/wanted", m.handleWanted, rightWishlistsRead)
	app.HandleAdminFunc("GET /api/admin/x/wishlist/lists", m.handleLists, rightWishlistsRead)
	return nil
}

func (m *Module) enabled(ctx context.Context) bool {
	on, _ := m.app.Plugins().Enabled(ctx, pluginKey)
	return on
}

func (m *Module) limit() int {
	if m.cfg.MaxItems > 0 {
		return m.cfg.MaxItems
	}
	return maxItems
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate a wishlist token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// itemsSelect reads a list's items with the product beside them.
//
// The price and the availability come from the variant the item names, or
// from the product's cheapest active variant when it names none — which is
// what a product card shows, so the wishlist page matches the page the
// shopper saved it from. A product that has been deleted leaves the row
// with its title empty, and the handler marks it.
const itemsSelect = `
SELECT i.id, i.product_id, i.variant_id, coalesce(p.title, ''), coalesce(p.slug, ''), coalesce(p.currency, ''),
       coalesce(v.sku, ''),
       (SELECT min(price_minor) FROM variants x
         WHERE x.product_id = i.product_id AND x.active
           AND (i.variant_id IS NULL OR x.id = i.variant_id)),
       coalesce((SELECT sum(CASE WHEN x.track_inventory THEN greatest(coalesce((SELECT sum(vs.on_hand - vs.reserved) FROM variant_stock vs WHERE vs.variant_id = x.id), 0), 0) ELSE 1000000 END)
                   FROM variants x
                  WHERE x.product_id = i.product_id AND x.active
                    AND (i.variant_id IS NULL OR x.id = i.variant_id)), 0),
       i.created_at
FROM wishlist_items i
LEFT JOIN products p ON p.id = i.product_id
LEFT JOIN variants v ON v.id = i.variant_id
WHERE i.wishlist_id = $1
ORDER BY i.id DESC`

func (m *Module) items(ctx context.Context, listID int64) ([]Item, error) {
	rows, err := m.db.QueryContext(ctx, itemsSelect, listID)
	if err != nil {
		return nil, gocommerce.Internalf(err, "read the wishlist")
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var it Item
		var variantID sql.NullInt64
		var currency string
		var priceMinor sql.NullInt64
		if err := rows.Scan(&it.ID, &it.ProductID, &variantID, &it.Title, &it.Slug, &currency,
			&it.SKU, &priceMinor, &it.Available, &it.CreatedAt); err != nil {
			return nil, gocommerce.Internalf(err, "scan a wishlist item")
		}
		if variantID.Valid {
			id := variantID.Int64
			it.VariantID = &id
		}
		if priceMinor.Valid && currency != "" {
			it.Price = &gocommerce.Money{AmountMinor: priceMinor.Int64, Currency: currency}
		}
		it.Deleted = it.Title == ""
		it.InStock = it.Available > 0
		out = append(out, it)
	}
	return out, rows.Err()
}

// byToken reads a list, without its items.
func (m *Module) byToken(ctx context.Context, token string) (*List, error) {
	var l List
	err := m.db.QueryRowContext(ctx,
		`SELECT id, token, email, created_at, updated_at FROM wishlists WHERE token = $1`, token).
		Scan(&l.ID, &l.Token, &l.Email, &l.CreatedAt, &l.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, gocommerce.NotFoundf("no such wishlist")
	}
	if err != nil {
		return nil, gocommerce.Internalf(err, "read the wishlist")
	}
	return &l, nil
}

// open reads the list a storefront request names, refusing when the plugin
// is off — one gate for every public route.
func (m *Module) open(r *http.Request) (*List, error) {
	if !m.enabled(r.Context()) {
		return nil, gocommerce.NotFoundf("this store has no wishlist")
	}
	return m.byToken(r.Context(), strings.TrimSpace(r.PathValue("token")))
}

// ----------------------------------------------------------------- public

func (m *Module) handleCreate(w http.ResponseWriter, r *http.Request) {
	if !m.enabled(r.Context()) {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("this store has no wishlist"))
		return
	}
	var in struct {
		Email string `json:"email"`
	}
	// A body is optional here: a storefront that only wants a token posts
	// nothing at all.
	if r.ContentLength > 0 {
		if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
	}
	email, err := cleanEmail(in.Email)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	token, err := newToken()
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "create a wishlist"))
		return
	}
	var l List
	if err := m.db.QueryRowContext(r.Context(),
		`INSERT INTO wishlists (token, email) VALUES ($1, $2) RETURNING id, token, email, created_at, updated_at`,
		token, email).Scan(&l.ID, &l.Token, &l.Email, &l.CreatedAt, &l.UpdatedAt); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "create a wishlist"))
		return
	}
	l.Items = []Item{}
	gocommerce.Respond(w, http.StatusCreated, l)
}

func (m *Module) handleGet(w http.ResponseWriter, r *http.Request) {
	l, err := m.open(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if l.Items, err = m.items(r.Context(), l.ID); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, l)
}

func (m *Module) handleSetEmail(w http.ResponseWriter, r *http.Request) {
	l, err := m.open(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in struct {
		Email string `json:"email"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	email, err := cleanEmail(in.Email)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if err := m.db.QueryRowContext(r.Context(),
		`UPDATE wishlists SET email = $2, updated_at = now() WHERE id = $1 RETURNING email, updated_at`,
		l.ID, email).Scan(&l.Email, &l.UpdatedAt); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "save the wishlist's email"))
		return
	}
	if l.Items, err = m.items(r.Context(), l.ID); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, l)
}

func (m *Module) handleAdd(w http.ResponseWriter, r *http.Request) {
	l, err := m.open(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in ItemInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if in.ProductID <= 0 {
		gocommerce.RespondError(w, r, gocommerce.Validationf("product_id is required"))
		return
	}
	// The product has to exist, and a named variant has to belong to it —
	// otherwise a list fills with rows that can never be drawn.
	var ok bool
	if in.VariantID != nil {
		err = m.db.QueryRowContext(r.Context(),
			`SELECT EXISTS (SELECT 1 FROM variants WHERE id = $1 AND product_id = $2)`, *in.VariantID, in.ProductID).Scan(&ok)
	} else {
		err = m.db.QueryRowContext(r.Context(),
			`SELECT EXISTS (SELECT 1 FROM products WHERE id = $1)`, in.ProductID).Scan(&ok)
	}
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "check the product"))
		return
	}
	if !ok {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no such product"))
		return
	}

	var held int
	if err := m.db.QueryRowContext(r.Context(),
		`SELECT count(*) FROM wishlist_items WHERE wishlist_id = $1`, l.ID).Scan(&held); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "count the wishlist"))
		return
	}
	if held >= m.limit() {
		gocommerce.RespondError(w, r, gocommerce.Conflictf("a wishlist holds at most %d products", m.limit()))
		return
	}

	// Saving the same thing twice is what a shopper does when they forget
	// they already did; it is not an error and not a second row.
	if _, err := m.db.ExecContext(r.Context(), `
		INSERT INTO wishlist_items (wishlist_id, product_id, variant_id) VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING`, l.ID, in.ProductID, in.VariantID); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "save to the wishlist"))
		return
	}
	if _, err := m.db.ExecContext(r.Context(), `UPDATE wishlists SET updated_at = now() WHERE id = $1`, l.ID); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "touch the wishlist"))
		return
	}
	if l.Items, err = m.items(r.Context(), l.ID); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusCreated, l)
}

func (m *Module) handleRemove(w http.ResponseWriter, r *http.Request) {
	l, err := m.open(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		gocommerce.RespondError(w, r, gocommerce.Validationf("the id must be a number"))
		return
	}
	res, err := m.db.ExecContext(r.Context(),
		`DELETE FROM wishlist_items WHERE id = $1 AND wishlist_id = $2`, id, l.ID)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "remove from the wishlist"))
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("that is not on this wishlist"))
		return
	}
	if l.Items, err = m.items(r.Context(), l.ID); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, l)
}

// ------------------------------------------------------------------ admin

// wantedSelect ranks products by how many lists hold them.
//
// `waiting` counts only the lists that gave an email, because that is how
// many people a restock notice could actually reach — a number a shop acts
// on differently from the raw count beside it.
const wantedSelect = `
SELECT i.product_id, coalesce(p.title, ''), coalesce(p.slug, ''), coalesce(p.status, ''), coalesce(p.currency, ''),
       count(DISTINCT i.wishlist_id),
       count(DISTINCT i.wishlist_id) FILTER (WHERE w.email <> ''),
       (SELECT min(price_minor) FROM variants x WHERE x.product_id = i.product_id AND x.active),
       coalesce((SELECT sum(CASE WHEN x.track_inventory THEN greatest(coalesce((SELECT sum(vs.on_hand - vs.reserved) FROM variant_stock vs WHERE vs.variant_id = x.id), 0), 0) ELSE 1000000 END)
                   FROM variants x WHERE x.product_id = i.product_id AND x.active), 0)
FROM wishlist_items i
JOIN wishlists w ON w.id = i.wishlist_id
LEFT JOIN products p ON p.id = i.product_id
GROUP BY i.product_id, p.title, p.slug, p.status, p.currency`

func (m *Module) handleWanted(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// "Out of stock only" is the question this screen exists to answer, so
	// it is a filter rather than something to read down the list for.
	having := ""
	if r.URL.Query().Get("out_of_stock") == "1" {
		having = ` HAVING coalesce((SELECT sum(CASE WHEN x.track_inventory THEN greatest(coalesce((SELECT sum(vs.on_hand - vs.reserved) FROM variant_stock vs WHERE vs.variant_id = x.id), 0), 0) ELSE 1000000 END)
		                             FROM variants x WHERE x.product_id = i.product_id AND x.active), 0) = 0`
	}

	var total int
	if err := m.db.QueryRowContext(r.Context(),
		`SELECT count(*) FROM (`+wantedSelect+having+`) ranked`).Scan(&total); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "count what is wanted"))
		return
	}
	rows, err := m.db.QueryContext(r.Context(),
		wantedSelect+having+` ORDER BY count(DISTINCT i.wishlist_id) DESC, i.product_id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read what is wanted"))
		return
	}
	defer rows.Close()
	out := []Wanted{}
	for rows.Next() {
		var it Wanted
		var currency string
		var priceMinor sql.NullInt64
		if err := rows.Scan(&it.ProductID, &it.Title, &it.Slug, &it.Status, &currency,
			&it.Lists, &it.Waiting, &priceMinor, &it.Available); err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "scan what is wanted"))
			return
		}
		if priceMinor.Valid && currency != "" {
			it.Price = &gocommerce.Money{AmountMinor: priceMinor.Int64, Currency: currency}
		}
		it.InStock = it.Available > 0
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read what is wanted"))
		return
	}
	gocommerce.RespondList(w, out, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (m *Module) handleLists(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	where, args := "1 = 1", []any{}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		args = append(args, "%"+strings.ToLower(q)+"%")
		where = fmt.Sprintf("lower(w.email) LIKE $%d", len(args))
	}
	if r.URL.Query().Get("with_email") == "1" {
		where += " AND w.email <> ''"
	}

	var total int
	if err := m.db.QueryRowContext(r.Context(),
		`SELECT count(*) FROM wishlists w WHERE `+where, args...).Scan(&total); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "count wishlists"))
		return
	}
	args = append(args, limit, offset)
	rows, err := m.db.QueryContext(r.Context(), fmt.Sprintf(`
		SELECT w.id, w.email, w.created_at, w.updated_at,
		       (SELECT count(*) FROM wishlist_items i WHERE i.wishlist_id = w.id)
		FROM wishlists w WHERE %s ORDER BY w.updated_at DESC LIMIT $%d OFFSET $%d`,
		where, len(args)-1, len(args)), args...)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read wishlists"))
		return
	}
	defer rows.Close()

	// A list as an operator sees it: no token. The token is the shopper's
	// credential, and a screen that prints it hands every operator the key
	// to somebody's list.
	type adminList struct {
		ID        int64     `json:"id"`
		Email     string    `json:"email,omitempty"`
		Items     int       `json:"items"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	out := []adminList{}
	for rows.Next() {
		var l adminList
		if err := rows.Scan(&l.ID, &l.Email, &l.CreatedAt, &l.UpdatedAt, &l.Items); err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "scan a wishlist"))
			return
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read wishlists"))
		return
	}
	gocommerce.RespondList(w, out, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

func cleanEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" {
		return "", nil
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return "", gocommerce.Validationf("that does not look like an email address")
	}
	return email, nil
}

// The rights this module's own screens are gated on.
//
// They were core's until now — customers.read —
// which meant the permission could not be given without the thing it was
// borrowed from, and the screens had no row of their own on the Roles grid.
// Default names the roles that held the old right, so nobody's access moves.
const (
	rightWishlistsRead gocommerce.Right = "wishlists.read" // was customers.read
)

func (m *Module) registerRights(app *gocommerce.App) {
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightWishlistsRead,
		Label:   "See what shoppers saved",
		Scope:   "What is wanted, and how many are waiting for it",
		Default: []string{gocommerce.RoleManager, gocommerce.RoleStaff},
	})
}
