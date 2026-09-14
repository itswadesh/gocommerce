// Package translations serves catalog content in the language a shopper asked
// for.
//
//	app, err := gocommerce.New(gocommerce.Config{
//	    Languages: []string{"en", "fr", "de"},
//	}, translations.New())
//
// # Why this is a module
//
// The engine already had every part of this except the words. `Config.Languages`
// declares what a store serves, `core/i18n.go` negotiates the request down to
// one of them, `Translator` is the port that supplies overrides, and
// `translateProducts` applies them on every public product read — degrading to
// the default language rather than failing the request when a lookup errors.
// What was missing was any implementation of that port and anywhere to put the
// text, so a store configured for `en,fr` negotiated French correctly and was
// then handed the English title.
//
// Filling that in from a module rather than from core is the engine's own
// design, not a preference: the port exists precisely so translations can be
// somebody else's table, and rule 3 means this module owns its rows and never
// writes a core one. It is the shape `ext/webhooks` already uses.
//
// # What is translated
//
// Products: title, description, and the two SEO fields. That is what the engine
// applies today, and the SEO pair is what makes a translated page worth serving
// — a French listing under an English meta description is half a translation.
//
// Category and collection *names* are not translated, because the engine has no
// seam for them: `translateProducts` is the only hook on the read path, and
// adding two more belongs in core rather than here. A store serving a French
// catalogue will still see English category names in its navigation, and that
// is stated rather than quietly left to be discovered.
package translations

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	gocommerce "github.com/misiki/gocommerce/core"
)

// The fields a product may carry a translation for.
//
// An allow-list rather than anything the caller sends, for two reasons. It is
// what stops a typo — "titel" — becoming a row that silently never applies; and
// it keeps the stored object the same shape as what the engine reads, so a
// field added to the engine later is a deliberate addition here rather than
// something that quietly started working.
var productFields = []string{"title", "description", "seo_title", "seo_description"}

// Module is the translation store.
type Module struct {
	db  *sql.DB
	log *slog.Logger
}

// New builds the module. Register it with gocommerce.New.
func New() *Module { return &Module{} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "translations" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_translations",
		SQL: `
			CREATE TABLE translations_entries (
			    kind       text        NOT NULL CHECK (kind <> ''),
			    entity_id  bigint      NOT NULL,
			    language   text        NOT NULL CHECK (language <> ''),
			    fields     jsonb       NOT NULL DEFAULT '{}',
			    updated_at timestamptz NOT NULL DEFAULT now(),
			    PRIMARY KEY (kind, entity_id, language)
			);`,
	}}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.db = app.DB()
	m.log = app.Log()

	app.RegisterTranslator(m)

	// catalog.read and catalog.write rather than a right of this module's own:
	// a product's French title is the product's content, and somebody trusted
	// to rewrite the English one is trusted to write the French.
	app.HandleAdminFunc("GET /api/admin/x/translations/{kind}/{id}",
		m.handleList, gocommerce.RightCatalogRead)
	app.HandleAdminFunc("PUT /api/admin/x/translations/{kind}/{id}/{language}",
		m.handlePut, gocommerce.RightCatalogWrite)
	app.HandleAdminFunc("DELETE /api/admin/x/translations/{kind}/{id}/{language}",
		m.handleDelete, gocommerce.RightCatalogWrite)
	return nil
}

// ------------------------------------------------------------------ the port

// Translate implements gocommerce.Translator.
//
// One query for a whole page, which is the reason the port is batched: a
// listing of fifty products must not become fifty lookups.
//
// A regional tag falls back to its primary subtag, so a row stored for "fr"
// serves a shopper asking for "fr-CA". Both are fetched in the same query and
// the exact match wins per entity — a store that has bothered to write a
// Canadian French title should get it, and one that has not should still get
// French rather than English.
func (m *Module) Translate(ctx context.Context, lang, kind string, ids []int64) (map[int64]map[string]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return nil, nil
	}
	base := primarySubtag(lang)

	rows, err := m.db.QueryContext(ctx, `
		SELECT entity_id, language, fields
		FROM translations_entries
		WHERE kind = $1 AND entity_id = ANY($2) AND lower(language) IN ($3, $4)`,
		kind, int64Array(ids), strings.ToLower(lang), strings.ToLower(base))
	if err != nil {
		return nil, fmt.Errorf("translations: reading %s overrides: %w", kind, err)
	}
	defer rows.Close()

	out := make(map[int64]map[string]string, len(ids))
	exact := make(map[int64]bool, len(ids))
	for rows.Next() {
		var (
			id       int64
			language string
			raw      []byte
		)
		if err := rows.Scan(&id, &language, &raw); err != nil {
			return nil, err
		}
		isExact := strings.EqualFold(language, lang)
		// The primary-subtag row must not overwrite the exact one, whichever
		// order the rows arrive in.
		if exact[id] && !isExact {
			continue
		}
		fields := map[string]string{}
		if err := json.Unmarshal(raw, &fields); err != nil {
			// One unreadable row is not worth failing a page of products over,
			// and the engine's own fallback would hide the error anyway.
			m.log.Error("translations: unreadable fields",
				"kind", kind, "entity_id", id, "language", language, "error", err)
			continue
		}
		out[id] = fields
		if isExact {
			exact[id] = true
		}
	}
	return out, rows.Err()
}

// primarySubtag reduces "fr-CA" to "fr". A tag with no region is its own
// primary subtag.
func primarySubtag(tag string) string {
	if i := strings.IndexAny(tag, "-_"); i > 0 {
		return tag[:i]
	}
	return tag
}

// int64Array renders ids as a PostgreSQL array literal.
//
// Hand-built rather than passed through a driver's array type because core
// takes exactly one third-party dependency and this module takes none (rule 2),
// so there is no pq.Array here to reach for. The values are int64s the engine
// produced, so there is nothing to escape — but they are formatted rather than
// concatenated from strings so that stays true if the type ever changes.
func int64Array(ids []int64) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(id, 10))
	}
	b.WriteByte('}')
	return b.String()
}

// ------------------------------------------------------------------- routes

// Entry is one entity's translation into one language.
type Entry struct {
	Kind      string            `json:"kind"`
	EntityID  int64             `json:"entity_id"`
	Language  string            `json:"language"`
	Fields    map[string]string `json:"fields"`
	UpdatedAt string            `json:"updated_at"`
}

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	kind, id, err := kindAndID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}

	rows, err := m.db.QueryContext(r.Context(), `
		SELECT kind, entity_id, language, fields, updated_at
		FROM translations_entries
		WHERE kind = $1 AND entity_id = $2
		ORDER BY language`, kind, id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	defer rows.Close()

	// An empty list rather than null: a product with no translations yet is the
	// ordinary case, and a client should be able to render it without a guard.
	out := []Entry{}
	for rows.Next() {
		var (
			e   Entry
			raw []byte
		)
		if err := rows.Scan(&e.Kind, &e.EntityID, &e.Language, &raw, &e.UpdatedAt); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		e.Fields = map[string]string{}
		if err := json.Unmarshal(raw, &e.Fields); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

func (m *Module) handlePut(w http.ResponseWriter, r *http.Request) {
	kind, id, err := kindAndID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	language, err := languageOf(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}

	var in struct {
		Fields map[string]string `json:"fields"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}

	fields, err := cleanFields(kind, in.Fields)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// Every field emptied is the same intention as deleting the row, and
	// leaving an empty object behind would show up as a language this product
	// claims to be translated into.
	if len(fields) == 0 {
		if _, err := m.db.ExecContext(r.Context(), `
			DELETE FROM translations_entries
			WHERE kind = $1 AND entity_id = $2 AND lower(language) = lower($3)`,
			kind, id, language); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	encoded, err := json.Marshal(fields)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}

	// A replace rather than a merge, like every other whole-object PUT in this
	// engine: the caller sent the translation it wants, and a merge would make
	// removing one field impossible.
	var e Entry
	var raw []byte
	if err := m.db.QueryRowContext(r.Context(), `
		INSERT INTO translations_entries (kind, entity_id, language, fields)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (kind, entity_id, language)
		DO UPDATE SET fields = excluded.fields, updated_at = now()
		RETURNING kind, entity_id, language, fields, updated_at`,
		kind, id, language, encoded,
	).Scan(&e.Kind, &e.EntityID, &e.Language, &raw, &e.UpdatedAt); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	e.Fields = map[string]string{}
	if err := json.Unmarshal(raw, &e.Fields); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, e)
}

func (m *Module) handleDelete(w http.ResponseWriter, r *http.Request) {
	kind, id, err := kindAndID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	language, err := languageOf(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if _, err := m.db.ExecContext(r.Context(), `
		DELETE FROM translations_entries
		WHERE kind = $1 AND entity_id = $2 AND lower(language) = lower($3)`,
		kind, id, language); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// 204 whether or not a row went. Deleting a translation that is not there
	// leaves the world in the state the caller asked for.
	w.WriteHeader(http.StatusNoContent)
}

// ------------------------------------------------------------------ helpers

func kindAndID(r *http.Request) (string, int64, error) {
	kind := strings.TrimSpace(r.PathValue("kind"))
	// Only the kinds the engine actually applies. Accepting "variant" would
	// store rows nothing reads, which looks like a translation that does not
	// work rather than one that was never wired up.
	if kind != gocommerce.KindProduct {
		return "", 0, gocommerce.Validationf(
			"kind must be %q; the engine applies translations to products only", gocommerce.KindProduct)
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || id <= 0 {
		return "", 0, gocommerce.Validationf("id must be a positive number")
	}
	return kind, id, nil
}

func languageOf(r *http.Request) (string, error) {
	language := strings.TrimSpace(r.PathValue("language"))
	if language == "" {
		return "", gocommerce.Validationf("a translation needs a language")
	}
	if len(language) > 35 {
		// BCP 47's own practical ceiling. A longer one is a path segment that
		// wandered in rather than a language.
		return "", gocommerce.Validationf("%q is not a language tag", language)
	}
	for _, ch := range language {
		if ch != '-' && ch != '_' &&
			!(ch >= 'a' && ch <= 'z') && !(ch >= 'A' && ch <= 'Z') && !(ch >= '0' && ch <= '9') {
			return "", gocommerce.Validationf("%q is not a language tag", language)
		}
	}
	return language, nil
}

// cleanFields keeps the known fields, trims them, and drops the empty ones.
//
// Empty is dropped rather than stored because the engine treats an empty
// override as absent anyway — storing it would be a row that says "translated"
// and reads as English.
func cleanFields(kind string, in map[string]string) (map[string]string, error) {
	allowed := productFields
	known := make(map[string]bool, len(allowed))
	for _, f := range allowed {
		known[f] = true
	}

	out := make(map[string]string, len(in))
	var unknown []string
	for key, value := range in {
		key = strings.TrimSpace(key)
		if !known[key] {
			unknown = append(unknown, key)
			continue
		}
		if v := strings.TrimSpace(value); v != "" {
			out[key] = v
		}
	}
	if len(unknown) > 0 {
		// Refused rather than ignored: a typo that is silently dropped is a
		// translation somebody believes they wrote.
		sort.Strings(unknown)
		return nil, gocommerce.Validationf(
			"unknown field(s) %s for %s; translatable fields are %s",
			strings.Join(unknown, ", "), kind, strings.Join(allowed, ", "))
	}
	return out, nil
}

var _ gocommerce.Translator = (*Module)(nil)
var _ gocommerce.Module = (*Module)(nil)
