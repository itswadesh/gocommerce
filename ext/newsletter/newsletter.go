// Package newsletter is the signup box: who asked to hear from the store,
// and who asked to stop.
//
// A subscription is an email address and a consent. The storefront posts
// the address; the store keeps it, with where it came from and when, and
// hands back a token the unsubscribe link carries. An operator reads the
// list, exports it for whatever sends the mail, and removes an address on
// request. Nothing here sends a newsletter: that is a marketing tool's job,
// and the export is how the list reaches one.
package newsletter

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const pluginKey = "newsletter"

// Statuses.
const (
	StatusSubscribed   = "subscribed"
	StatusUnsubscribed = "unsubscribed"
)

// Config configures the module.
type Config struct{}

// Subscription is one address and its consent.
type Subscription struct {
	ID             int64      `json:"id"`
	Email          string     `json:"email"`
	Name           string     `json:"name,omitempty"`
	Source         string     `json:"source,omitempty"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	UnsubscribedAt *time.Time `json:"unsubscribed_at,omitempty"`
}

// SubscribeInput is what the storefront posts.
type SubscribeInput struct {
	Email  string `json:"email"`
	Name   string `json:"name"`
	Source string `json:"source"`
	// Website is a honeypot: a field no person sees, so a bot that fills
	// every box fills this one and is quietly accepted and dropped.
	Website string `json:"website"`
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
func (m *Module) Name() string { return "newsletter" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_subscriptions",
		SQL: `
CREATE TABLE newsletter_subscriptions (
    id              bigserial   PRIMARY KEY,
    email           text        NOT NULL UNIQUE,
    name            text        NOT NULL DEFAULT '',
    source          text        NOT NULL DEFAULT '',
    status          text        NOT NULL DEFAULT 'subscribed' CHECK (status IN ('subscribed', 'unsubscribed')),
    -- The unsubscribe link carries this rather than the address, so a link
    -- forwarded on cannot be edited into somebody else's.
    token           text        NOT NULL UNIQUE,
    created_at      timestamptz NOT NULL DEFAULT now(),
    unsubscribed_at timestamptz
);
CREATE INDEX newsletter_subscriptions_status_idx ON newsletter_subscriptions (status, id DESC);
`,
	}}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.registerRights(app)
	m.app = app
	m.db = app.DB()
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: pluginKey, Title: "Newsletter", Category: "marketing", DefaultEnabled: true,
		Description: "A signup box on the storefront: addresses are kept with their consent, listed and exported from the Newsletter screen, and removed with the unsubscribe link every mail should carry.",
		Fields: []gocommerce.PluginField{
			{Key: "heading", Label: "Signup heading", Kind: "text", Public: true, Default: "Get the news"},
			{Key: "blurb", Label: "Signup text", Kind: "text", Public: true, Default: "New arrivals and offers, now and then. Unsubscribe any time."},
			{Key: "thanks", Label: "After signing up", Kind: "text", Public: true, Default: "Thanks — you're on the list."},
		},
	})
	app.HandleFunc("POST /x/newsletter/subscribe", m.handleSubscribe)
	app.HandleFunc("POST /x/newsletter/unsubscribe", m.handleUnsubscribe)
	app.HandleFunc("GET /x/newsletter/unsubscribe", m.handleUnsubscribe)

	// The list is who the store may write to — the customers' right to read,
	// data.export to walk out with, store.operate to remove.
	app.HandleAdminFunc("GET /api/admin/x/newsletter/subscriptions", m.handleList, rightNewsletterRead)
	app.HandleAdminFunc("GET /api/admin/x/newsletter/subscriptions.csv", m.handleExport, gocommerce.RightDataExport)
	app.HandleAdminFunc("POST /api/admin/x/newsletter/subscriptions", m.handleAdd, rightNewsletterWrite)
	app.HandleAdminFunc("DELETE /api/admin/x/newsletter/subscriptions/{id}", m.handleDelete, rightNewsletterWrite)
	return nil
}

func (m *Module) enabled(ctx context.Context) bool {
	on, _ := m.app.Plugins().Enabled(ctx, pluginKey)
	return on
}

func cleanEmail(raw string) (string, error) {
	addr, err := mail.ParseAddress(strings.TrimSpace(raw))
	if err != nil || strings.ContainsAny(addr.Address, " <>") {
		return "", gocommerce.Validationf("that does not look like an email address")
	}
	return strings.ToLower(addr.Address), nil
}

func token() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// subscribe records an address, or re-subscribes one that had left. The
// same address twice is a no-op with the same answer, so a form submitted
// twice does not shout.
func (m *Module) subscribe(ctx context.Context, in SubscribeInput) (*Subscription, error) {
	email, err := cleanEmail(in.Email)
	if err != nil {
		return nil, err
	}
	tok, err := token()
	if err != nil {
		return nil, err
	}
	var sub Subscription
	err = m.db.QueryRowContext(ctx, `
		INSERT INTO newsletter_subscriptions (email, name, source, token)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (email) DO UPDATE SET
		    status = 'subscribed', unsubscribed_at = NULL,
		    name = CASE WHEN EXCLUDED.name <> '' THEN EXCLUDED.name ELSE newsletter_subscriptions.name END,
		    source = CASE WHEN newsletter_subscriptions.status = 'unsubscribed' THEN EXCLUDED.source ELSE newsletter_subscriptions.source END
		RETURNING id, email, name, source, status, created_at, unsubscribed_at`,
		email, strings.TrimSpace(in.Name), strings.TrimSpace(in.Source), tok).
		Scan(&sub.ID, &sub.Email, &sub.Name, &sub.Source, &sub.Status, &sub.CreatedAt, &sub.UnsubscribedAt)
	if err != nil {
		return nil, err
	}
	return &sub, nil
}

// ----------------------------------------------------------------- public

func (m *Module) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	if !m.enabled(r.Context()) {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("the newsletter is not open on this store"))
		return
	}
	var in SubscribeInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// A filled honeypot is a bot: say yes, keep nothing.
	if strings.TrimSpace(in.Website) != "" {
		gocommerce.Respond(w, http.StatusCreated, map[string]string{"status": StatusSubscribed})
		return
	}
	sub, err := m.subscribe(r.Context(), in)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// Not the row: an address and a token are not a storefront's to show.
	gocommerce.Respond(w, http.StatusCreated, map[string]string{"status": sub.Status})
}

func (m *Module) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	if r.Method == http.MethodPost {
		var in struct {
			Token string `json:"token"`
		}
		if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		tok = in.Token
	}
	if strings.TrimSpace(tok) == "" {
		gocommerce.RespondError(w, r, gocommerce.Validationf("the unsubscribe link is missing its token"))
		return
	}
	res, err := m.db.ExecContext(r.Context(), `
		UPDATE newsletter_subscriptions SET status = 'unsubscribed', unsubscribed_at = now()
		WHERE token = $1 AND status = 'subscribed'`, strings.TrimSpace(tok))
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// A link clicked twice, or a token nobody has, gets the same quiet
	// answer: nothing about the list is learnable from this route.
	_, _ = res.RowsAffected()
	gocommerce.Respond(w, http.StatusOK, map[string]string{"status": StatusUnsubscribed})
}

// ------------------------------------------------------------------ admin

func (m *Module) list(ctx context.Context, search, status string, limit, offset int) ([]*Subscription, int, error) {
	where, args := []string{"1 = 1"}, []any{}
	if status != "" {
		args = append(args, status)
		where = append(where, fmt.Sprintf("status = $%d", len(args)))
	}
	if s := strings.TrimSpace(search); s != "" {
		args = append(args, "%"+strings.ToLower(s)+"%")
		where = append(where, fmt.Sprintf("(email LIKE $%d OR lower(name) LIKE $%d)", len(args), len(args)))
	}
	clause := strings.Join(where, " AND ")
	var total int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM newsletter_subscriptions WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = gocommerce.DefaultLimit
	}
	args = append(args, limit, offset)
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, email, name, source, status, created_at, unsubscribed_at
		FROM newsletter_subscriptions WHERE `+clause+
		fmt.Sprintf(` ORDER BY id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Subscription{}
	for rows.Next() {
		var s Subscription
		if err := rows.Scan(&s.ID, &s.Email, &s.Name, &s.Source, &s.Status, &s.CreatedAt, &s.UnsubscribedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, &s)
	}
	return out, total, rows.Err()
}

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != StatusSubscribed && status != StatusUnsubscribed {
		gocommerce.RespondError(w, r, gocommerce.Validationf("status must be subscribed or unsubscribed"))
		return
	}
	rows, total, err := m.list(r.Context(), r.URL.Query().Get("q"), status, limit, offset)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.RespondList(w, rows, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

// handleExport is the list as a mailing tool wants it: every subscribed
// address, one per row, with the name and the date consent was given.
func (m *Module) handleExport(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = StatusSubscribed
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="newsletter-%s.csv"`, time.Now().UTC().Format("2006-01-02")))
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"email", "name", "source", "status", "subscribed_at", "unsubscribed_at"})
	for offset := 0; ; offset += 500 {
		rows, _, err := m.list(r.Context(), "", status, 500, offset)
		if err != nil || len(rows) == 0 {
			break
		}
		for _, s := range rows {
			left := ""
			if s.UnsubscribedAt != nil {
				left = s.UnsubscribedAt.UTC().Format(time.RFC3339)
			}
			_ = cw.Write([]string{s.Email, s.Name, s.Source, s.Status, s.CreatedAt.UTC().Format(time.RFC3339), left})
		}
		if len(rows) < 500 {
			break
		}
	}
	cw.Flush()
}

// handleAdd is an operator typing an address in by hand — a customer who
// asked at the counter.
func (m *Module) handleAdd(w http.ResponseWriter, r *http.Request) {
	var in SubscribeInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if in.Source == "" {
		in.Source = "admin"
	}
	sub, err := m.subscribe(r.Context(), in)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusCreated, sub)
}

func (m *Module) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		gocommerce.RespondError(w, r, gocommerce.Validationf("id must be a positive integer"))
		return
	}
	res, err := m.db.ExecContext(r.Context(), `DELETE FROM newsletter_subscriptions WHERE id = $1`, id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("subscription %d does not exist", id))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// The rights this module's own screens are gated on.
//
// They were core's until now — customers.read and store.operate —
// which meant the permission could not be given without the thing it was
// borrowed from, and the screens had no row of their own on the Roles grid.
// Default names the roles that held the old right, so nobody's access moves.
const (
	rightNewsletterRead  gocommerce.Right = "newsletter.read"  // was customers.read
	rightNewsletterWrite gocommerce.Right = "newsletter.write" // was store.operate
)

func (m *Module) registerRights(app *gocommerce.App) {
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightNewsletterRead,
		Label:   "See the newsletter list",
		Scope:   "Who signed up — personal data",
		Default: []string{gocommerce.RoleManager, gocommerce.RoleStaff},
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightNewsletterWrite,
		Label:   "Edit the newsletter list",
		Scope:   "Adding and removing subscriptions",
		Default: []string(nil),
	})
}
