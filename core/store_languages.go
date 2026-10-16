package gocommerce

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
)

// Each store's own languages (D87).
//
// D21 made the languages a start-up setting, one list per install, and D83 then
// put many stores in one install — so a shop in Odisha selling in English and
// Odia shared its list with every other store on the machine. Now a store
// chooses its own, on Store settings, at any time: an order keeps the language
// it recorded, so nothing a store already did changes meaning when the list
// does. A store that never chooses serves Config.Languages, as before.
//
// The first language is the store's main one. It is what a request that asks
// for nothing is served in, what the catalogue is written in (a translation is
// for the others), and what a customer is written to in when nothing says
// otherwise. One list rather than a list and a separate default, so the main
// language cannot be one the store does not serve.

// migration0065StoreLanguages gives a store its own languages. NULL is the
// install's.
//
// store_profile.language, the store's "language for messages", is no longer
// read: the main language is the first of these. It never decided anything —
// an order always records its language (orders.lang is NOT NULL), and every
// other message was given Config.DefaultLanguage before reaching it — so it is
// marked rather than carried over. Migrations are append-only, so the column
// stays.
const migration0065StoreLanguages = `
ALTER TABLE stores ADD COLUMN languages text[]
    CHECK (languages IS NULL OR cardinality(languages) BETWEEN 1 AND 30);
COMMENT ON COLUMN store_profile.language IS
    'Unread since 0065_store_languages: a store''s main language is the first of stores.languages.';
`

// MaxStoreLanguages bounds a store's list. India alone has 22 scheduled
// languages; a store translating into more than this is describing a
// translation business.
const MaxStoreLanguages = 30

// languageTagRE is the shape a tag has once folded to lower case: a two- or
// three-letter language, then subtags. Which language is the store's business,
// as a currency is (D14); this only refuses what no client would ever send.
var languageTagRE = regexp.MustCompile(`^[a-z]{2,3}(-[a-z0-9]{1,8})*$`)

type storeLanguagesKey struct{}

// withStoreLanguages records, beside the store, the languages storeMW already
// read with it, so negotiating every request does not read the row again.
func withStoreLanguages(ctx context.Context, langs []string) context.Context {
	return context.WithValue(ctx, storeLanguagesKey{}, langs)
}

// Languages is what the store ctx is bound to sells in, its main language
// first: its own list, else the install's Config.Languages (D87). It never
// returns an empty list, so Languages(ctx)[0] is always the main language.
// The slice is the caller's to keep.
func (a *App) Languages(ctx context.Context) []string {
	if langs, ok := ctx.Value(storeLanguagesKey{}).([]string); ok && len(langs) > 0 {
		return append([]string(nil), langs...)
	}
	if store := a.storeOf(ctx); store != "" {
		var raw []byte
		if err := a.db.QueryRowContext(ctx, `SELECT to_jsonb(languages) FROM stores WHERE id = $1`, store).Scan(&raw); err == nil {
			if langs := decodeLanguages(raw); len(langs) > 0 {
				return langs
			}
		}
	}
	return a.installLanguages()
}

// mainLanguage is the first of the store's languages.
func (a *App) mainLanguage(ctx context.Context) string { return a.Languages(ctx)[0] }

// requestLanguages is what languageMW negotiates against: the store's, which
// storeMW put on the request, or the install's for a path no store answers —
// without a read, since those are the panel's own files.
func (a *App) requestLanguages(ctx context.Context) []string {
	if langs, ok := ctx.Value(storeLanguagesKey{}).([]string); ok && len(langs) > 0 {
		return langs
	}
	return a.installLanguages()
}

// installLanguages is Config.Languages with Config.DefaultLanguage first, the
// order a store's own list has. applyDefaults guarantees the default is in
// the list, not that it leads it.
func (a *App) installLanguages() []string {
	out := []string{a.cfg.DefaultLanguage}
	for _, l := range a.cfg.Languages {
		if !containsFold(out, l) {
			out = append(out, l)
		}
	}
	return out
}

// languagesOf is a store row's languages, or the install's.
func (a *App) languagesOf(st *Store) []string {
	if st != nil && len(st.Languages) > 0 {
		return st.Languages
	}
	return a.installLanguages()
}

func decodeLanguages(raw []byte) []string {
	var langs []string
	if len(raw) == 0 || json.Unmarshal(raw, &langs) != nil {
		return nil
	}
	return langs
}

// normalizeLanguage folds a tag to its usual spelling — "HI" is "hi", "fr_ca"
// is "fr-CA", "zh-hant" is "zh-Hant" — and refuses what is not a tag.
func normalizeLanguage(tag string) (string, error) {
	t := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(tag), "_", "-"))
	if !languageTagRE.MatchString(t) {
		return "", Validationf("%q is not a language code; use one such as %q or %q", tag, "hi", "fr-CA")
	}
	parts := strings.Split(t, "-")
	for i, p := range parts[1:] {
		switch {
		case len(p) == 4 && isLetters(p):
			parts[i+1] = strings.ToUpper(p[:1]) + p[1:] // a script: zh-Hant
		case len(p) == 2 && isLetters(p):
			parts[i+1] = strings.ToUpper(p) // a region: fr-CA
		}
	}
	return strings.Join(parts, "-"), nil
}

func isLetters(s string) bool {
	for _, r := range s {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// normalizeLanguages is a store's list as it will be stored: each tag folded,
// each once, main first. An empty main keeps the list's own first.
func normalizeLanguages(list []string, main string) ([]string, error) {
	out := make([]string, 0, len(list))
	for _, tag := range list {
		l, err := normalizeLanguage(tag)
		if err != nil {
			return nil, err
		}
		if !containsFold(out, l) {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return nil, Validationf("a store sells in at least one language")
	}
	if len(out) > MaxStoreLanguages {
		return nil, Validationf("a store has at most %d languages", MaxStoreLanguages)
	}
	if strings.TrimSpace(main) == "" {
		return out, nil
	}
	m, err := normalizeLanguage(main)
	if err != nil {
		return nil, err
	}
	at := -1
	for i, l := range out {
		if l == m {
			at = i
		}
	}
	if at < 0 {
		return nil, Validationf("the main language, %q, is not one of the store's languages; add it to them first", m)
	}
	return append([]string{m}, append(out[:at:at], out[at+1:]...)...), nil
}
