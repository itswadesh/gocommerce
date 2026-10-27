// Package layouts is the storefront's home page as the shop arranges it: an
// ordered list of sections — a hero, product grids, image and text, benefits —
// any type any number of times at any position, edited as a draft and
// published without a deployment (D125).
//
// The page is a document, not rows: its order is the order of its list, and a
// publish swaps one whole document for another, so a shopper never sees a
// page half moved. Each publish is kept as an immutable revision, which is
// what rolling back returns to. A draft carries a version number, and a save
// that names an old one is refused rather than overwriting somebody's newer
// edit.
//
// The document holds references, never commerce facts: a product grid names
// a collection or product ids, and the public route resolves them to the live,
// active products of the request's channel, so a price or a stock level on the
// page is always the catalogue's.
package layouts

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	pluginKey  = "layouts"
	previewTTL = 30 * time.Minute

	rightLayoutsRead  = "layouts.read"
	rightLayoutsWrite = "layouts.write"
)

// Config configures the module. It has nothing to set yet.
type Config struct{}

// Module is the routes and the tables.
type Module struct {
	cfg Config
	app *gocommerce.App
	db  *sql.DB
	// tick is how often scheduled publishes are looked for: a page goes live
	// within this of its time.
	tick time.Duration
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg, tick: 30 * time.Second} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "layouts" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_layouts",
		SQL: `
CREATE TABLE layout_pages (
    id                 bigserial   PRIMARY KEY,
    page               text        NOT NULL UNIQUE,
    -- The draft, whole. version counts its saves: a save names the version
    -- it was read at, and a stale one is refused.
    draft              jsonb       NOT NULL,
    version            integer     NOT NULL DEFAULT 1,
    -- The revision the storefront shows; null until the first publish.
    published_revision bigint,
    updated_at         timestamptz NOT NULL DEFAULT now(),
    updated_by         text        NOT NULL DEFAULT ''
);
-- Every published document, kept as it was: rolling back publishes one of
-- these again rather than editing history.
CREATE TABLE layout_revisions (
    id            bigserial   PRIMARY KEY,
    page          text        NOT NULL,
    number        integer     NOT NULL,
    document      jsonb       NOT NULL,
    published_at  timestamptz NOT NULL DEFAULT now(),
    published_by  text        NOT NULL DEFAULT '',
    restored_from integer,
    UNIQUE (page, number)
);
-- Short-lived keys that let the storefront show the draft. Only the hash is
-- kept: the key itself is in one browser tab's address bar.
CREATE TABLE layout_previews (
    id         bigserial   PRIMARY KEY,
    page       text        NOT NULL,
    token_hash text        NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL
);
`,
	},
		gocommerce.ScopeMigration("0002_store_scope", "layout_pages", "layout_revisions", "layout_previews"),
		// A preview key opens the unpublished page; staff may run any saved
		// report, and a hash is all a report could do anything with.
		gocommerce.WithholdFromReports("0003_withhold_from_reports", "layout_previews"),
		{
			// A page's draft, set to publish at a time (D129). The document is
			// the draft as it was when scheduled, so what goes live is what
			// the shop looked at, not whatever the autosave holds by then.
			ID: "0004_schedules",
			SQL: `
CREATE TABLE layout_schedules (
    id            bigserial   PRIMARY KEY,
    page          text        NOT NULL UNIQUE,
    document      jsonb       NOT NULL,
    draft_version integer     NOT NULL,
    publish_at    timestamptz NOT NULL,
    scheduled_at  timestamptz NOT NULL DEFAULT now(),
    scheduled_by  text        NOT NULL DEFAULT ''
);
ALTER TABLE layout_revisions ADD COLUMN scheduled boolean NOT NULL DEFAULT false;
`,
		},
		gocommerce.ScopeMigration("0005_schedules_store_scope", "layout_schedules"),
	}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.registerRights(app)
	m.app = app
	m.db = app.DB()
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: pluginKey, Title: "Theme editor", Category: "storefront", DefaultEnabled: true,
		Description: "Arrange the storefront's home page, product page and collection page from sections — a hero, a slideshow, product grids, collections, video, " +
			"questions and answers, reviews — in any order, set the store's colours, fonts and corner rounding, and publish each page now or at a time you choose, " +
			"without a deployment. On by default; switched off, or until a page's first publish, the storefront shows its built-in page and its own look.",
	})

	app.HandleFunc("GET /x/layouts/theme", m.handleTheme)
	app.HandleFunc("GET /x/layouts/{page}", m.handlePublic)

	app.HandleAdminFunc("GET /api/admin/x/layouts/sections", m.handleRegistry, rightLayoutsRead)
	app.HandleAdminFunc("GET /api/admin/x/layouts/{page}", m.handleGet, rightLayoutsRead)
	app.HandleAdminFunc("PUT /api/admin/x/layouts/{page}", m.handleSave, rightLayoutsWrite)
	app.HandleAdminFunc("POST /api/admin/x/layouts/{page}/publish", m.handlePublish, rightLayoutsWrite)
	app.HandleAdminFunc("GET /api/admin/x/layouts/{page}/revisions", m.handleRevisions, rightLayoutsRead)
	app.HandleAdminFunc("POST /api/admin/x/layouts/{page}/rollback", m.handleRollback, rightLayoutsWrite)
	app.HandleAdminFunc("POST /api/admin/x/layouts/{page}/reset", m.handleReset, rightLayoutsWrite)
	app.HandleAdminFunc("POST /api/admin/x/layouts/{page}/preview", m.handlePreview, rightLayoutsWrite)
	app.HandleAdminFunc("POST /api/admin/x/layouts/{page}/schedule", m.handleSchedule, rightLayoutsWrite)
	app.HandleAdminFunc("DELETE /api/admin/x/layouts/{page}/schedule", m.handleCancelSchedule, rightLayoutsWrite)

	app.OnStart(m.start)
	return nil
}

// start publishes what is due on a ticker until shutdown, the way the other
// modules' background passes run: once at boot, so a page whose time passed
// while the server was down goes live at once, then every tick.
func (m *Module) start(ctx context.Context) error {
	go func() {
		t := time.NewTicker(m.tick)
		defer t.Stop()
		for {
			err := m.app.ForEachStore(ctx, func(ctx context.Context) error {
				_, err := m.PublishDue(ctx)
				return err
			})
			if err != nil && ctx.Err() == nil {
				m.app.Log().Error("layouts: scheduled publish failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return nil
}

func (m *Module) registerRights(app *gocommerce.App) {
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightLayoutsRead,
		Label:   "See the theme editor",
		Scope:   "The home, product and collection pages' sections, their drafts, schedules and published versions",
		Default: []string{gocommerce.RoleManager, gocommerce.RoleStaff},
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightLayoutsWrite,
		Label:   "Edit and publish the storefront's pages",
		Scope:   "Saving a page's draft, resetting it, publishing or scheduling it, previewing it and rolling back",
		Default: []string{gocommerce.RoleManager},
	})
}

// ---------------------------------------------------------------- the page

// State is what the editor works from.
type State struct {
	Page     string   `json:"page"`
	Document Document `json:"document"`
	// Version is the draft's; 0 when nothing has been saved, and the
	// document is the starting template.
	Version int `json:"version"`
	// Saved is false until the first save: the document is a suggestion.
	Saved     bool       `json:"saved"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	UpdatedBy string     `json:"updated_by,omitempty"`
	Published *Revision  `json:"published"`
	// Changed is whether the draft differs from what is published.
	Changed bool `json:"changed"`
	// Scheduled is the publish waiting for its time, or null.
	Scheduled *Schedule `json:"scheduled"`
}

// Revision is one published document.
type Revision struct {
	Number       int       `json:"number"`
	PublishedAt  time.Time `json:"published_at"`
	PublishedBy  string    `json:"published_by"`
	RestoredFrom *int      `json:"restored_from,omitempty"`
	// Scheduled is true when it went live at a time set beforehand, and
	// PublishedBy is then who set it.
	Scheduled bool      `json:"scheduled,omitempty"`
	Sections  int       `json:"sections"`
	Document  *Document `json:"document,omitempty"`
}

// Schedule is a draft set to publish at a time.
type Schedule struct {
	PublishAt   time.Time `json:"publish_at"`
	ScheduledAt time.Time `json:"scheduled_at"`
	ScheduledBy string    `json:"scheduled_by"`
	// Version is the draft's version that was scheduled, and Outdated
	// whether the draft has been saved since: those edits are not in it.
	Version  int  `json:"version"`
	Outdated bool `json:"outdated"`
}

func pageKey(r *http.Request) (string, error) {
	p := r.PathValue("page")
	if pageInfo(p) == nil {
		return "", gocommerce.NotFoundf("there is no page %q to arrange; the home, product and collection pages can be", p)
	}
	return p, nil
}

func actor(ctx context.Context) string {
	if su := gocommerce.SuperuserFrom(ctx); su != nil {
		return su.Email
	}
	return "an API token"
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (m *Module) state(ctx context.Context, q queryer, page string) (*State, error) {
	st := &State{Page: page}
	var (
		draft     []byte
		pubID     sql.NullInt64
		updatedAt time.Time
	)
	err := q.QueryRowContext(ctx, `
		SELECT draft, version, published_revision, updated_at, updated_by
		FROM layout_pages WHERE page = $1`, page).Scan(&draft, &st.Version, &pubID, &updatedAt, &st.UpdatedBy)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		st.Document = Starter(page)
		return st, nil
	case err != nil:
		return nil, gocommerce.Internalf(err, "read the page")
	}
	if st.Scheduled, err = m.schedule(ctx, q, page); err != nil {
		return nil, err
	}
	if st.Scheduled != nil {
		st.Scheduled.Outdated = st.Scheduled.Version != st.Version
	}
	st.Saved, st.UpdatedAt = true, &updatedAt
	if err := json.Unmarshal(draft, &st.Document); err != nil {
		return nil, gocommerce.Internalf(err, "read the page's draft")
	}
	if pubID.Valid {
		rev, doc, err := m.revision(ctx, q, "id = $1", pubID.Int64)
		if err != nil {
			return nil, err
		}
		st.Published = rev
		st.Changed = !bytes.Equal(canonical(st.Document), canonical(*doc))
	} else {
		st.Changed = true
	}
	return st, nil
}

func canonical(d Document) []byte {
	b, _ := json.Marshal(d)
	return b
}

func (m *Module) revision(ctx context.Context, q queryer, where string, arg ...any) (*Revision, *Document, error) {
	rev := &Revision{}
	var raw []byte
	var from sql.NullInt64
	err := q.QueryRowContext(ctx, `
		SELECT number, document, published_at, published_by, restored_from, scheduled
		FROM layout_revisions WHERE `+where, arg...).Scan(&rev.Number, &raw, &rev.PublishedAt, &rev.PublishedBy, &from, &rev.Scheduled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, gocommerce.NotFoundf("there is no such published version")
	}
	if err != nil {
		return nil, nil, gocommerce.Internalf(err, "read a published version")
	}
	if from.Valid {
		n := int(from.Int64)
		rev.RestoredFrom = &n
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, gocommerce.Internalf(err, "read a published version")
	}
	rev.Sections = len(doc.Sections)
	return rev, &doc, nil
}

// Save replaces the draft, when version is the one it was read at.
func (m *Module) Save(ctx context.Context, page string, version int, doc Document) (*State, error) {
	clean, err := Validate(page, doc)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		return nil, gocommerce.Internalf(err, "encode the page")
	}
	if len(raw) > MaxDocBytes {
		return nil, gocommerce.Validationf("the page is too large to save (%d KB, at most %d KB)", len(raw)>>10, MaxDocBytes>>10)
	}
	var out *State
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE layout_pages SET draft = $2, version = version + 1, updated_at = now(), updated_by = $4
			WHERE page = $1 AND version = $3`, page, raw, version, actor(ctx))
		if err != nil {
			return gocommerce.Internalf(err, "save the page")
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if version != 0 {
				return stale()
			}
			// The first save. ON CONFLICT so two first saves at once are one
			// insert and one stale refusal, not two pages.
			res, err := tx.ExecContext(ctx, `
				INSERT INTO layout_pages (page, draft, updated_by) VALUES ($1, $2, $3)
				ON CONFLICT (store_id, page) DO NOTHING`, page, raw, actor(ctx))
			if err != nil {
				return gocommerce.Internalf(err, "save the page")
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return stale()
			}
		}
		out, err = m.state(ctx, tx, page)
		return err
	})
	return out, err
}

func stale() error {
	return &gocommerce.APIError{Status: http.StatusConflict, Code: "stale_draft",
		Message: "Somebody saved this page after you opened it. Reload to see their changes, then make yours again."}
}

// Publish makes the saved draft at version what the storefront shows.
func (m *Module) Publish(ctx context.Context, page string, version int) (*State, error) {
	var out *State
	err := gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		var raw []byte
		var current int
		err := tx.QueryRowContext(ctx, `SELECT draft, version FROM layout_pages WHERE page = $1 FOR UPDATE`, page).Scan(&raw, &current)
		if errors.Is(err, sql.ErrNoRows) {
			return gocommerce.Conflictf("Save the page before publishing it.")
		}
		if err != nil {
			return gocommerce.Internalf(err, "read the page")
		}
		if current != version {
			return stale()
		}
		if err := m.publish(ctx, tx, page, raw, publishing{by: actor(ctx)}); err != nil {
			return err
		}
		out, err = m.state(ctx, tx, page)
		return err
	})
	return out, err
}

// publishing is how a revision came to be: who published it, and whether it
// restored an earlier one or went live on a schedule.
type publishing struct {
	by           string
	restoredFrom *int
	scheduled    bool
}

// publish adds document as the page's next revision and points the page at
// it, in the caller's transaction: the storefront swaps from one whole page
// to the next. The button, a rollback and a schedule all publish through
// here, so the history reads the same whichever it was.
func (m *Module) publish(ctx context.Context, tx *sql.Tx, page string, document []byte, how publishing) error {
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO layout_revisions (page, number, document, published_by, restored_from, scheduled)
		VALUES ($1, coalesce((SELECT max(number) FROM layout_revisions WHERE page = $1), 0) + 1, $2, $3, $4, $5)
		RETURNING id`, page, document, how.by, how.restoredFrom, how.scheduled).Scan(&id)
	if err != nil {
		return gocommerce.Internalf(err, "publish the page")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE layout_pages SET published_revision = $2 WHERE page = $1`, page, id); err != nil {
		return gocommerce.Internalf(err, "publish the page")
	}
	return nil
}

// Rollback publishes an earlier revision's document again, as a new revision,
// and makes it the draft too, so the next edit starts from what is live.
func (m *Module) Rollback(ctx context.Context, page string, number int) (*State, error) {
	var out *State
	err := gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		_, doc, err := m.revision(ctx, tx, "page = $1 AND number = $2", page, number)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return gocommerce.Internalf(err, "encode the page")
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE layout_pages SET draft = $2, version = version + 1, updated_at = now(), updated_by = $3
			WHERE page = $1`, page, raw, actor(ctx)); err != nil {
			return gocommerce.Internalf(err, "restore the page")
		}
		if err := m.publish(ctx, tx, page, raw, publishing{by: actor(ctx), restoredFrom: &number}); err != nil {
			return err
		}
		out, err = m.state(ctx, tx, page)
		return err
	})
	return out, err
}

// Reset throws the draft away: it becomes the published document again, or
// the starting page when nothing has been published. Like a save it names the
// version it was read at, so an edit made in another tab since is never
// discarded unseen; the storefront is untouched either way. Before the first
// save there is nothing to throw away, and the starter is answered as it is.
func (m *Module) Reset(ctx context.Context, page string, version int) (*State, error) {
	var out *State
	err := gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		var current int
		var pubID sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT version, published_revision FROM layout_pages WHERE page = $1 FOR UPDATE`, page).Scan(&current, &pubID)
		if errors.Is(err, sql.ErrNoRows) {
			if version != 0 {
				return stale()
			}
			out, err = m.state(ctx, tx, page)
			return err
		}
		if err != nil {
			return gocommerce.Internalf(err, "read the page")
		}
		if current != version {
			return stale()
		}
		doc := Starter(page)
		if pubID.Valid {
			_, published, err := m.revision(ctx, tx, "id = $1", pubID.Int64)
			if err != nil {
				return err
			}
			doc = *published
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return gocommerce.Internalf(err, "encode the page")
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE layout_pages SET draft = $2, version = version + 1, updated_at = now(), updated_by = $3
			WHERE page = $1`, page, raw, actor(ctx)); err != nil {
			return gocommerce.Internalf(err, "reset the page")
		}
		out, err = m.state(ctx, tx, page)
		return err
	})
	return out, err
}

// Revisions lists the page's published versions, newest first.
func (m *Module) Revisions(ctx context.Context, page string) ([]Revision, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT number, published_at, published_by, restored_from, scheduled, jsonb_array_length(document->'sections')
		FROM layout_revisions WHERE page = $1 ORDER BY number DESC LIMIT 100`, page)
	if err != nil {
		return nil, gocommerce.Internalf(err, "list the published versions")
	}
	defer rows.Close()
	out := []Revision{}
	for rows.Next() {
		var rev Revision
		var from sql.NullInt64
		if err := rows.Scan(&rev.Number, &rev.PublishedAt, &rev.PublishedBy, &from, &rev.Scheduled, &rev.Sections); err != nil {
			return nil, gocommerce.Internalf(err, "list the published versions")
		}
		if from.Valid {
			n := int(from.Int64)
			rev.RestoredFrom = &n
		}
		out = append(out, rev)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- previews

// Preview issues a key that lets the storefront show the saved draft for half
// an hour — the starting page, before the first save, so the theme editor can
// frame the page as soon as it opens without writing anything.
func (m *Module) Preview(ctx context.Context, page string) (string, time.Time, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, gocommerce.Internalf(err, "make a preview key")
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	expires := time.Now().Add(previewTTL)
	if _, err := m.db.ExecContext(ctx, `DELETE FROM layout_previews WHERE expires_at < now()`); err != nil {
		return "", time.Time{}, gocommerce.Internalf(err, "make a preview key")
	}
	if _, err := m.db.ExecContext(ctx, `INSERT INTO layout_previews (page, token_hash, expires_at) VALUES ($1, $2, $3)`,
		page, hashToken(token), expires); err != nil {
		return "", time.Time{}, gocommerce.Internalf(err, "make a preview key")
	}
	return token, expires, nil
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte("layouts-preview:" + t))
	return base64.RawStdEncoding.EncodeToString(sum[:])
}

// draftFor is the draft a live preview key opens — the starting page while
// nothing is saved — or not found.
func (m *Module) draftFor(ctx context.Context, page, token string) (*Document, error) {
	var raw []byte
	err := m.db.QueryRowContext(ctx, `
		SELECT p.draft FROM layout_previews v LEFT JOIN layout_pages p ON p.page = v.page
		WHERE v.token_hash = $1 AND v.page = $2 AND v.expires_at > now()`, hashToken(token), page).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("this preview has expired; open a new one from the editor")
	}
	if err != nil {
		return nil, gocommerce.Internalf(err, "read the preview")
	}
	if raw == nil {
		doc := Starter(page)
		return &doc, nil
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, gocommerce.Internalf(err, "read the preview")
	}
	return &doc, nil
}

// published is the document the storefront shows, or not found.
func (m *Module) published(ctx context.Context, page string) (*Revision, *Document, error) {
	return m.revision(ctx, m.db, `id = (SELECT published_revision FROM layout_pages WHERE page = $1)`, page)
}
