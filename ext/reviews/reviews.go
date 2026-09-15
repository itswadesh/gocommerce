// Package reviews is what shoppers say about a product: a rating out of
// five and some words, moderated before it shows.
//
// A review arrives from the storefront and waits for an operator unless
// the Plugins screen says to approve on sight; approved reviews are what
// the storefront reads, with the average and the count beside them. A
// review is marked verified when its email has bought the product, which
// the store can tell from its own orders — and the Plugins screen can make
// that a condition of posting at all.
package reviews

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/misiki/gocommerce/core"
)

const (
	pluginKey = "product-reviews"
	maxBody   = 4000
)

// Statuses.
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRejected = "rejected"
)

// Config configures the module. The Plugins screen holds the same two
// switches and wins.
type Config struct {
	// AutoApprove shows a review the moment it is posted.
	AutoApprove bool
	// RequirePurchase refuses a review from an email that has not bought
	// the product.
	RequirePurchase bool
}

// Review is one review.
type Review struct {
	ID        int64     `json:"id"`
	ProductID int64     `json:"product_id"`
	Product   string    `json:"product,omitempty"`
	Name      string    `json:"name"`
	Email     string    `json:"email,omitempty"`
	Rating    int       `json:"rating"`
	Title     string    `json:"title,omitempty"`
	Body      string    `json:"body"`
	Status    string    `json:"status"`
	Verified  bool      `json:"verified"`
	Reply     string    `json:"reply,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ReviewInput is what the storefront posts.
type ReviewInput struct {
	ProductID int64  `json:"product_id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Rating    int    `json:"rating"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	// Website is a honeypot: a field no person sees.
	Website string `json:"website"`
}

// ReviewPatch is what an operator changes.
type ReviewPatch struct {
	Status *string `json:"status"`
	// Reply is the store's public answer under the review.
	Reply *string `json:"reply"`
}

// Summary is a product's reviews in numbers.
type Summary struct {
	ProductID    int64       `json:"product_id"`
	Count        int         `json:"count"`
	Average      float64     `json:"average"`
	Distribution map[int]int `json:"distribution"`
}

// Module is the routes and the table.
type Module struct {
	cfg Config
	app *gocommerce.App
	db  *sql.DB
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "reviews" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_reviews",
		SQL: `
CREATE TABLE reviews (
    id         bigserial   PRIMARY KEY,
    -- Not a foreign key: a review of a product that has since been deleted
    -- is still a fact about the store, and moderation reads it either way.
    product_id bigint      NOT NULL,
    name       text        NOT NULL,
    email      text        NOT NULL,
    rating     integer     NOT NULL CHECK (rating BETWEEN 1 AND 5),
    title      text        NOT NULL DEFAULT '',
    body       text        NOT NULL,
    status     text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
    verified   boolean     NOT NULL DEFAULT false,
    reply      text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reviews_product_idx ON reviews (product_id, status, id DESC);
CREATE INDEX reviews_status_idx  ON reviews (status, id DESC);
`,
	}}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	m.db = app.DB()
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: pluginKey, Title: "Product reviews", Category: "storefront", DefaultEnabled: true,
		Description: "Ratings and reviews on the product page, moderated on the Reviews screen. A review from an email that bought the product is marked verified.",
		Fields: []gocommerce.PluginField{
			{Key: "auto_approve", Label: "Show reviews without moderation", Kind: "bool", Default: m.cfg.AutoApprove},
			{Key: "require_purchase", Label: "Only buyers may review", Kind: "bool", Default: m.cfg.RequirePurchase, Public: true},
			{Key: "prompt", Label: "Form heading", Kind: "text", Public: true, Default: "Write a review"},
		},
	})
	app.HandleFunc("GET /x/reviews", m.handlePublicList)
	app.HandleFunc("POST /x/reviews", m.handleSubmit)

	// A review is catalogue copy the store did not write: the catalogue's
	// rights, because it appears on the product page beside the rest.
	app.HandleAdminFunc("GET /api/admin/x/reviews", m.handleList, gocommerce.RightCatalogRead)
	app.HandleAdminFunc("PATCH /api/admin/x/reviews/{id}", m.handleUpdate, gocommerce.RightCatalogWrite)
	app.HandleAdminFunc("DELETE /api/admin/x/reviews/{id}", m.handleDelete, gocommerce.RightCatalogWrite)
	m.mountTransferRoutes(app)
	return nil
}

type policy struct {
	enabled, autoApprove, requirePurchase bool
}

func (m *Module) policy(ctx context.Context) policy {
	enabled, _ := m.app.Plugins().Enabled(ctx, pluginKey)
	settings, _ := m.app.Plugins().Settings(ctx, pluginKey)
	flag := func(key string, fallback bool) bool {
		if v, ok := settings[key].(bool); ok {
			return v
		}
		return fallback
	}
	return policy{enabled: enabled, autoApprove: flag("auto_approve", m.cfg.AutoApprove), requirePurchase: flag("require_purchase", m.cfg.RequirePurchase)}
}

// bought reports whether an email has an order carrying the product that
// left the shelf — the store's own definition of a sale.
func (m *Module) bought(ctx context.Context, email string, productID int64) (bool, error) {
	var yes bool
	err := m.db.QueryRowContext(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM orders o JOIN order_lines l ON l.order_id = o.id
		    WHERE lower(o.email) = $1 AND l.product_id = $2
		      AND o.status IN ('confirmed', 'partial', 'shipped', 'delivered'))`, email, productID).Scan(&yes)
	return yes, err
}

const reviewColumns = `r.id, r.product_id, coalesce(p.title, ''), r.name, r.email, r.rating, r.title, r.body, r.status, r.verified, r.reply, r.created_at, r.updated_at`

func scan(sc interface{ Scan(...any) error }) (*Review, error) {
	var rv Review
	if err := sc.Scan(&rv.ID, &rv.ProductID, &rv.Product, &rv.Name, &rv.Email, &rv.Rating, &rv.Title, &rv.Body,
		&rv.Status, &rv.Verified, &rv.Reply, &rv.CreatedAt, &rv.UpdatedAt); err != nil {
		return nil, err
	}
	return &rv, nil
}

func (m *Module) query(ctx context.Context, where string, args []any, limit, offset int) ([]*Review, int, error) {
	var total int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM reviews r WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = gocommerce.DefaultLimit
	}
	args = append(args, limit, offset)
	rows, err := m.db.QueryContext(ctx, `
		SELECT `+reviewColumns+` FROM reviews r LEFT JOIN products p ON p.id = r.product_id
		WHERE `+where+fmt.Sprintf(` ORDER BY r.id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Review{}
	for rows.Next() {
		rv, err := scan(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, rv)
	}
	return out, total, rows.Err()
}

// summary is the approved reviews of one product in numbers.
func (m *Module) summary(ctx context.Context, productID int64) (*Summary, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT rating, count(*) FROM reviews WHERE product_id = $1 AND status = 'approved' GROUP BY rating`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	s := &Summary{ProductID: productID, Distribution: map[int]int{1: 0, 2: 0, 3: 0, 4: 0, 5: 0}}
	sum := 0
	for rows.Next() {
		var rating, n int
		if err := rows.Scan(&rating, &n); err != nil {
			return nil, err
		}
		s.Distribution[rating] = n
		s.Count += n
		sum += rating * n
	}
	if s.Count > 0 {
		s.Average = float64(int(float64(sum)/float64(s.Count)*10+0.5)) / 10
	}
	return s, rows.Err()
}

// ----------------------------------------------------------------- public

// publicReview is a review as the storefront sees it: no email.
type publicReview struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Rating    int       `json:"rating"`
	Title     string    `json:"title,omitempty"`
	Body      string    `json:"body"`
	Verified  bool      `json:"verified"`
	Reply     string    `json:"reply,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func (m *Module) handlePublicList(w http.ResponseWriter, r *http.Request) {
	if !m.policy(r.Context()).enabled {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("reviews are not open on this store"))
		return
	}
	productID, err := strconv.ParseInt(r.URL.Query().Get("product_id"), 10, 64)
	if err != nil || productID <= 0 {
		gocommerce.RespondError(w, r, gocommerce.Validationf("product_id is required"))
		return
	}
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	rows, total, err := m.query(r.Context(), "r.product_id = $1 AND r.status = 'approved'", []any{productID}, limit, offset)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	summary, err := m.summary(r.Context(), productID)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	out := make([]publicReview, 0, len(rows))
	for _, rv := range rows {
		out = append(out, publicReview{ID: rv.ID, Name: rv.Name, Rating: rv.Rating, Title: rv.Title, Body: rv.Body, Verified: rv.Verified, Reply: rv.Reply, CreatedAt: rv.CreatedAt})
	}
	gocommerce.Respond(w, http.StatusOK, map[string]any{
		"reviews": out, "summary": summary,
		"meta": map[string]int{"total": total, "limit": limit, "offset": offset},
	})
}

func (m *Module) handleSubmit(w http.ResponseWriter, r *http.Request) {
	pol := m.policy(r.Context())
	if !pol.enabled {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("reviews are not open on this store"))
		return
	}
	var in ReviewInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if strings.TrimSpace(in.Website) != "" {
		gocommerce.Respond(w, http.StatusCreated, map[string]string{"status": StatusPending})
		return
	}
	in.Name, in.Title, in.Body = strings.TrimSpace(in.Name), strings.TrimSpace(in.Title), strings.TrimSpace(in.Body)
	switch {
	case in.ProductID <= 0:
		gocommerce.RespondError(w, r, gocommerce.Validationf("product_id is required"))
		return
	case in.Rating < 1 || in.Rating > 5:
		gocommerce.RespondError(w, r, gocommerce.Validationf("rating must be from 1 to 5"))
		return
	case in.Name == "":
		gocommerce.RespondError(w, r, gocommerce.Validationf("a name is required"))
		return
	case in.Body == "":
		gocommerce.RespondError(w, r, gocommerce.Validationf("a few words are required"))
		return
	case len(in.Body) > maxBody:
		gocommerce.RespondError(w, r, gocommerce.Validationf("the review is too long: %d characters at most", maxBody))
		return
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(in.Email))
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Validationf("that does not look like an email address"))
		return
	}
	email := strings.ToLower(addr.Address)
	if _, err := m.app.Products().GetProduct(r.Context(), in.ProductID); err != nil {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("product %d does not exist", in.ProductID))
		return
	}
	verified, err := m.bought(r.Context(), email, in.ProductID)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if pol.requirePurchase && !verified {
		gocommerce.RespondError(w, r, gocommerce.Validationf("only customers who bought this product may review it; use the email your order was placed with"))
		return
	}
	status := StatusPending
	if pol.autoApprove {
		status = StatusApproved
	}
	if _, err := m.db.ExecContext(r.Context(), `
		INSERT INTO reviews (product_id, name, email, rating, title, body, status, verified)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		in.ProductID, in.Name, email, in.Rating, in.Title, in.Body, status, verified); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusCreated, map[string]any{"status": status, "verified": verified})
}

// ------------------------------------------------------------------ admin

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	q := r.URL.Query()
	where, args := []string{"1 = 1"}, []any{}
	if status := q.Get("status"); status != "" {
		if status != StatusPending && status != StatusApproved && status != StatusRejected {
			gocommerce.RespondError(w, r, gocommerce.Validationf("status must be pending, approved or rejected"))
			return
		}
		args = append(args, status)
		where = append(where, fmt.Sprintf("r.status = $%d", len(args)))
	}
	if pid, _ := strconv.ParseInt(q.Get("product_id"), 10, 64); pid > 0 {
		args = append(args, pid)
		where = append(where, fmt.Sprintf("r.product_id = $%d", len(args)))
	}
	if s := strings.TrimSpace(q.Get("q")); s != "" {
		args = append(args, "%"+strings.ToLower(s)+"%")
		where = append(where, fmt.Sprintf("(lower(r.name) LIKE $%d OR r.email LIKE $%d OR lower(r.body) LIKE $%d OR lower(r.title) LIKE $%d)", len(args), len(args), len(args), len(args)))
	}
	rows, total, err := m.query(r.Context(), strings.Join(where, " AND "), args, limit, offset)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.RespondList(w, rows, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, gocommerce.Validationf("id must be a positive integer")
	}
	return id, nil
}

func (m *Module) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var patch ReviewPatch
	if err := gocommerce.DecodeJSON(w, r, &patch); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	sets, args := []string{"updated_at = now()"}, []any{}
	if patch.Status != nil {
		if *patch.Status != StatusPending && *patch.Status != StatusApproved && *patch.Status != StatusRejected {
			gocommerce.RespondError(w, r, gocommerce.Validationf("status must be pending, approved or rejected"))
			return
		}
		args = append(args, *patch.Status)
		sets = append(sets, fmt.Sprintf("status = $%d", len(args)))
	}
	if patch.Reply != nil {
		args = append(args, strings.TrimSpace(*patch.Reply))
		sets = append(sets, fmt.Sprintf("reply = $%d", len(args)))
	}
	args = append(args, id)
	res, err := m.db.ExecContext(r.Context(), "UPDATE reviews SET "+strings.Join(sets, ", ")+fmt.Sprintf(" WHERE id = $%d", len(args)), args...)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("review %d does not exist", id))
		return
	}
	rows, _, err := m.query(r.Context(), "r.id = $1", []any{id}, 1, 0)
	if err != nil || len(rows) == 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("review %d does not exist", id))
		return
	}
	gocommerce.Respond(w, http.StatusOK, rows[0])
}

func (m *Module) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	res, err := m.db.ExecContext(r.Context(), `DELETE FROM reviews WHERE id = $1`, id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("review %d does not exist", id))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
