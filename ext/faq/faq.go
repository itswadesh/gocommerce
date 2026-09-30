// Package faq is the questions a shop is asked often, and its answers.
//
// Every storefront has a page of them and every shop writes the same ones —
// where is my order, how do I return this, what does delivery cost — so the
// engine gives the answers a table, the panel a screen, and the storefront
// one address to read them from. They are grouped into sections the shop
// names itself, ordered by hand, and a draft is invisible to shoppers until
// it is published.
//
//	app, err := gocommerce.New(cfg, faq.New(faq.Config{}))
package faq

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	pluginKey   = "faq"
	maxQuestion = 300
	maxAnswer   = 8000
)

// Config configures the module. The Plugins screen holds the same heading
// and wins.
type Config struct {
	// Heading is what the storefront's page is called. Defaults to
	// "Frequently asked questions".
	Heading string
}

// Entry is one question and its answer.
type Entry struct {
	ID       int64  `json:"id"`
	Question string `json:"question"`
	Answer   string `json:"answer"`
	// Section groups entries on the page — "Delivery", "Returns". Empty is
	// the ungrouped set, which a storefront shows first.
	Section   string    `json:"section"`
	Position  int       `json:"position"`
	Published bool      `json:"published"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// EntryInput creates or replaces an entry.
type EntryInput struct {
	Question  string `json:"question"`
	Answer    string `json:"answer"`
	Section   string `json:"section"`
	Published *bool  `json:"published"`
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
func (m *Module) Name() string { return "faq" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_faq",
		SQL: `
CREATE TABLE faq_entries (
    id         bigserial   PRIMARY KEY,
    question   text        NOT NULL,
    answer     text        NOT NULL,
    section    text        NOT NULL DEFAULT '',
    -- The order is the shop's own: an FAQ page reads as a sequence, and
    -- sorting it by anything the database knows would scramble that.
    position   integer     NOT NULL DEFAULT 0,
    published  boolean     NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX faq_entries_order_idx ON faq_entries (section, position, id);
`,
	}}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.registerRights(app)
	m.app = app
	m.db = app.DB()

	heading := strings.TrimSpace(m.cfg.Heading)
	if heading == "" {
		heading = "Frequently asked questions"
	}
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: pluginKey, Title: "FAQ", Category: "storefront", DefaultEnabled: true,
		Description: "A page of the questions this shop is asked often, grouped into sections and ordered by hand, edited on the FAQ screen and served at /x/faq.",
		Fields: []gocommerce.PluginField{
			{Key: "heading", Label: "Page heading", Kind: "text", Public: true, Default: heading},
			{Key: "blurb", Label: "Line under the heading", Kind: "text", Public: true},
		},
	})

	app.HandleFunc("GET /x/faq", m.handlePublic)

	// An answer is shop copy, like a content page: the catalogue's rights,
	// because it is written and read alongside the rest of the storefront.
	app.HandleAdminFunc("GET /api/admin/x/faq", m.handleList, rightFAQRead)
	app.HandleAdminFunc("POST /api/admin/x/faq", m.handleCreate, rightFAQWrite)
	app.HandleAdminFunc("PATCH /api/admin/x/faq/{id}", m.handleUpdate, rightFAQWrite)
	app.HandleAdminFunc("DELETE /api/admin/x/faq/{id}", m.handleDelete, rightFAQWrite)
	// The whole order at once, the way the Menus screen saves a tree: an
	// FAQ page is reordered by dragging, and one request per moved row
	// would leave the list half-sorted if the second failed.
	app.HandleAdminFunc("PUT /api/admin/x/faq/order", m.handleReorder, rightFAQWrite)
	return nil
}

func (m *Module) enabled(ctx context.Context) bool {
	on, _ := m.app.Plugins().Enabled(ctx, pluginKey)
	return on
}

const entryColumns = `id, question, answer, section, position, published, created_at, updated_at`

func scan(sc interface{ Scan(...any) error }) (*Entry, error) {
	var e Entry
	if err := sc.Scan(&e.ID, &e.Question, &e.Answer, &e.Section, &e.Position, &e.Published, &e.CreatedAt, &e.UpdatedAt); err != nil {
		return nil, err
	}
	return &e, nil
}

// list reads the entries in the order the shop put them in.
func (m *Module) list(ctx context.Context, publishedOnly bool) ([]*Entry, error) {
	where := "1 = 1"
	if publishedOnly {
		where = "published"
	}
	rows, err := m.db.QueryContext(ctx,
		`SELECT `+entryColumns+` FROM faq_entries WHERE `+where+` ORDER BY section, position, id`)
	if err != nil {
		return nil, gocommerce.Internalf(err, "list faq entries")
	}
	defer rows.Close()
	out := []*Entry{}
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return nil, gocommerce.Internalf(err, "scan faq entry")
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func validate(in EntryInput) (EntryInput, error) {
	in.Question = strings.TrimSpace(in.Question)
	in.Answer = strings.TrimSpace(in.Answer)
	in.Section = strings.TrimSpace(in.Section)
	switch {
	case in.Question == "":
		return in, gocommerce.Validationf("a question is required")
	case len(in.Question) > maxQuestion:
		return in, gocommerce.Validationf("a question is at most %d characters", maxQuestion)
	case in.Answer == "":
		return in, gocommerce.Validationf("an answer is required — an unanswered question helps nobody")
	case len(in.Answer) > maxAnswer:
		return in, gocommerce.Validationf("an answer is at most %d characters", maxAnswer)
	}
	return in, nil
}

// ----------------------------------------------------------------- public

// publicEntry is an entry as the storefront sees it: no timestamps, no
// position, because the order is the order it arrives in.
type publicEntry struct {
	ID       int64  `json:"id"`
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

type publicSection struct {
	Section string        `json:"section"`
	Entries []publicEntry `json:"entries"`
}

func (m *Module) handlePublic(w http.ResponseWriter, r *http.Request) {
	if !m.enabled(r.Context()) {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("this store has no FAQ"))
		return
	}
	entries, err := m.list(r.Context(), true)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// Grouped in the order the sections first appear, which is the order
	// the shop arranged them in — not alphabetical, which would put
	// "Returns" before "Delivery" and read as a list somebody else sorted.
	sections := []publicSection{}
	at := map[string]int{}
	for _, e := range entries {
		i, seen := at[e.Section]
		if !seen {
			at[e.Section] = len(sections)
			i = len(sections)
			sections = append(sections, publicSection{Section: e.Section, Entries: []publicEntry{}})
		}
		sections[i].Entries = append(sections[i].Entries, publicEntry{ID: e.ID, Question: e.Question, Answer: e.Answer})
	}
	settings, _ := m.app.Plugins().Settings(r.Context(), pluginKey)
	text := func(key string) string {
		s, _ := settings[key].(string)
		return s
	}
	gocommerce.Respond(w, http.StatusOK, map[string]any{
		"heading":  text("heading"),
		"blurb":    text("blurb"),
		"sections": sections,
	})
}

// ------------------------------------------------------------------ admin

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	entries, err := m.list(r.Context(), false)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.RespondList(w, entries, gocommerce.ListMeta{Total: len(entries), Limit: len(entries), Offset: 0})
}

func (m *Module) handleCreate(w http.ResponseWriter, r *http.Request) {
	var in EntryInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	in, err := validate(in)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	published := true
	if in.Published != nil {
		published = *in.Published
	}
	// Last in its section, which is where a new question belongs until
	// somebody drags it.
	var next int
	if err := m.db.QueryRowContext(r.Context(),
		`SELECT coalesce(max(position), -1) + 1 FROM faq_entries WHERE section = $1`, in.Section).Scan(&next); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "place the faq entry"))
		return
	}
	e, err := scan(m.db.QueryRowContext(r.Context(), `
		INSERT INTO faq_entries (question, answer, section, position, published)
		VALUES ($1, $2, $3, $4, $5) RETURNING `+entryColumns,
		in.Question, in.Answer, in.Section, next, published))
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "create the faq entry"))
		return
	}
	gocommerce.Respond(w, http.StatusCreated, e)
}

func (m *Module) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := entryID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in EntryInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	in, err = validate(in)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	published := true
	if in.Published != nil {
		published = *in.Published
	}
	e, err := scan(m.db.QueryRowContext(r.Context(), `
		UPDATE faq_entries SET question = $2, answer = $3, section = $4, published = $5, updated_at = now()
		WHERE id = $1 RETURNING `+entryColumns,
		id, in.Question, in.Answer, in.Section, published))
	if err == sql.ErrNoRows {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no FAQ entry %d", id))
		return
	}
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "update the faq entry"))
		return
	}
	gocommerce.Respond(w, http.StatusOK, e)
}

func (m *Module) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := entryID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	res, err := m.db.ExecContext(r.Context(), `DELETE FROM faq_entries WHERE id = $1`, id)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "delete the faq entry"))
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no FAQ entry %d", id))
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// reorderInput is the whole list in the order it should read.
type reorderInput struct {
	// Entries is every entry's id with the section it now belongs to, in
	// order. An id the store does not hold is refused: a half-applied order
	// is worse than a refused one.
	Entries []struct {
		ID      int64  `json:"id"`
		Section string `json:"section"`
	} `json:"entries"`
}

func (m *Module) handleReorder(w http.ResponseWriter, r *http.Request) {
	var in reorderInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	err := gocommerce.InTx(r.Context(), m.db, func(tx *sql.Tx) error {
		var held int
		if err := tx.QueryRowContext(r.Context(), `SELECT count(*) FROM faq_entries`).Scan(&held); err != nil {
			return gocommerce.Internalf(err, "count faq entries")
		}
		if held != len(in.Entries) {
			return gocommerce.Validationf("the order must name every entry: %d sent, %d held", len(in.Entries), held)
		}
		// Position within the section, counted as the list is walked, so
		// the request carries the order and never the arithmetic.
		next := map[string]int{}
		for _, row := range in.Entries {
			section := strings.TrimSpace(row.Section)
			res, err := tx.ExecContext(r.Context(),
				`UPDATE faq_entries SET section = $2, position = $3, updated_at = now() WHERE id = $1`,
				row.ID, section, next[section])
			if err != nil {
				return gocommerce.Internalf(err, "reorder faq entries")
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return gocommerce.Validationf("no FAQ entry %d", row.ID)
			}
			next[section]++
		}
		return nil
	})
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	entries, err := m.list(r.Context(), false)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.RespondList(w, entries, gocommerce.ListMeta{Total: len(entries), Limit: len(entries), Offset: 0})
}

func entryID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, gocommerce.Validationf("the id must be a number")
	}
	return id, nil
}

// The rights this module's own screens are gated on.
//
// They were core's until now — catalog.read and catalog.write —
// which meant the permission could not be given without the thing it was
// borrowed from, and the screens had no row of their own on the Roles grid.
// Default names the roles that held the old right, so nobody's access moves.
const (
	rightFAQRead  gocommerce.Right = "faq.read"  // was catalog.read
	rightFAQWrite gocommerce.Right = "faq.write" // was catalog.write
)

func (m *Module) registerRights(app *gocommerce.App) {
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightFAQRead,
		Label:   "See the FAQ",
		Scope:   "The questions a storefront answers",
		Default: []string{gocommerce.RoleManager, gocommerce.RoleStaff},
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightFAQWrite,
		Label:   "Edit the FAQ",
		Scope:   "Writing, ordering and removing entries",
		Default: []string{gocommerce.RoleManager},
	})
}
