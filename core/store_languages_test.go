package gocommerce

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// A store sells in the install's languages until it chooses its own; its
// choice is what the profile, the settings and its public face report.
func TestAStoreChoosesItsLanguages(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if got := app.Languages(ctx); !slices.Equal(got, []string{"en"}) {
		t.Fatalf("default languages = %v, want the install's [en]", got)
	}

	rec := do(t, app, http.MethodPatch, "/api/admin/store", withAdmin,
		jsonBody(t, map[string]any{"languages": []string{"EN", "hi", "or", "hi"}, "language": "HI"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", rec.Code, rec.Body)
	}
	var saved StoreProfile
	decodeData(t, rec, &saved)
	want := []string{"hi", "en", "or"}
	if saved.Language != "hi" || !slices.Equal(saved.Languages, want) {
		t.Fatalf("saved language %q, languages %v; want hi, %v", saved.Language, saved.Languages, want)
	}
	if got := app.Languages(ctx); !slices.Equal(got, want) {
		t.Errorf("store languages = %v, want %v", got, want)
	}

	var settings StoreSettings
	decodeData(t, do(t, app, http.MethodGet, "/api/admin/settings", withAdmin), &settings)
	if settings.DefaultLanguage != "hi" || !slices.Equal(settings.Languages, want) {
		t.Errorf("settings = %q %v, want hi %v", settings.DefaultLanguage, settings.Languages, want)
	}
	var public struct {
		Language  string   `json:"language"`
		Languages []string `json:"languages"`
	}
	decodeData(t, do(t, app, http.MethodGet, "/api/store"), &public)
	if public.Language != "hi" || !slices.Equal(public.Languages, want) {
		t.Errorf("public face = %q %v, want hi %v", public.Language, public.Languages, want)
	}

	// The main language alone reorders what is stored; leaving both out
	// keeps them, so a client that sends neither cannot reset them.
	p, err := app.Profile().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.Languages, p.Language = nil, "or"
	if _, err := app.Profile().Set(ctx, *p, nil); err != nil {
		t.Fatal(err)
	}
	p.Languages, p.Language = nil, ""
	if _, err := app.Profile().Set(ctx, *p, nil); err != nil {
		t.Fatal(err)
	}
	if got := app.Languages(ctx); !slices.Equal(got, []string{"or", "hi", "en"}) {
		t.Errorf("after choosing or as main = %v, want [or hi en]", got)
	}
}

// A request is negotiated against its own store's languages: one store's
// Hindi is not offered by a store that sells only in English.
func TestARequestIsServedInItsStoresLanguages(t *testing.T) {
	app := newTestApp(t)
	hindi := addTestStore(t, app, "hindi-shop")
	if _, err := app.Profile().Set(WithStore(context.Background(), hindi),
		StoreProfile{Languages: []string{"hi", "en"}}, nil); err != nil {
		t.Fatal(err)
	}

	served := func(store, accept string) string {
		t.Helper()
		rec := do(t, app, http.MethodGet, "/api/store", func(r *http.Request) {
			r.Header.Set("X-Store", store)
			if accept != "" {
				r.Header.Set("Accept-Language", accept)
			}
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/store for %s = %d: %s", store, rec.Code, rec.Body)
		}
		return rec.Header().Get("Content-Language")
	}
	if got := served("hindi-shop", "hi-IN,en;q=0.5"); got != "hi" {
		t.Errorf("Hindi store asked for hi-IN = %q, want hi", got)
	}
	if got := served("hindi-shop", ""); got != "hi" {
		t.Errorf("Hindi store asked for nothing = %q, want its main language hi", got)
	}
	defaultStore, err := app.Stores().Get(context.Background(), app.storeOf(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	if got := served(defaultStore.Slug, "hi-IN,en;q=0.5"); got != "en" {
		t.Errorf("English-only store asked for hi-IN = %q, want en", got)
	}
}

// What a store's list may hold: tags spelled the usual way, each once, at
// least one, at most MaxStoreLanguages, and a main language from among them.
func TestStoreLanguagesAreFoldedAndChecked(t *testing.T) {
	for in, want := range map[string]string{
		"HI": "hi", " fr_ca ": "fr-CA", "zh-hant": "zh-Hant", "sr-latn-rs": "sr-Latn-RS", "es-419": "es-419",
	} {
		if got, err := normalizeLanguage(in); err != nil || got != want {
			t.Errorf("normalizeLanguage(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "e", "english", "hi--in", "hi in", "हिन्दी"} {
		if _, err := normalizeLanguage(bad); err == nil {
			t.Errorf("normalizeLanguage(%q) should be refused", bad)
		}
	}

	got, err := normalizeLanguages([]string{"en", "HI", "en"}, "hi")
	if err != nil || !slices.Equal(got, []string{"hi", "en"}) {
		t.Errorf("normalizeLanguages = %v, %v; want [hi en]", got, err)
	}
	if _, err := normalizeLanguages(nil, ""); err == nil {
		t.Error("a store with no language should be refused")
	}
	if _, err := normalizeLanguages([]string{"en"}, "fr"); err == nil || !strings.Contains(err.Error(), "fr") {
		t.Errorf("a main language outside the list = %v, want a refusal naming it", err)
	}
	many := make([]string, 0, MaxStoreLanguages+1)
	for _, a := range "abcdefghijklmnopqrstuvwxyz" {
		for _, b := range "ab" {
			many = append(many, string(a)+string(b))
		}
	}
	if _, err := normalizeLanguages(many[:MaxStoreLanguages+1], ""); err == nil {
		t.Errorf("%d languages should be refused", MaxStoreLanguages+1)
	}
}

// Each store has its own: another store's choice does not move this one, and
// a store that never chose keeps the install's list, default first.
func TestEachStoreHasItsOwnLanguages(t *testing.T) {
	dsn := requireDB(t)
	cfg := testConfig(dsn)
	cfg.DefaultLanguage = "en"
	cfg.Languages = []string{"fr", "en"}
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	other := WithStore(context.Background(), addTestStore(t, app, "odia-shop"))
	if _, err := app.Profile().Set(other, StoreProfile{Languages: []string{"or", "en"}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := app.Languages(other); !slices.Equal(got, []string{"or", "en"}) {
		t.Errorf("the store that chose = %v, want [or en]", got)
	}
	if got := app.Languages(context.Background()); !slices.Equal(got, []string{"en", "fr"}) {
		t.Errorf("the store that did not = %v, want the install's, default first: [en fr]", got)
	}
}

// The install's own list is spelt the way a store's is, so a store that
// never chose compares equal to its own list saved back.
func TestTheInstallsLanguagesAreSpeltTheUsualWay(t *testing.T) {
	dsn := requireDB(t)
	cfg := testConfig(dsn)
	cfg.DefaultLanguage = "en"
	cfg.Languages = []string{"en", "fr_ca"}
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if got := app.Languages(context.Background()); !slices.Equal(got, []string{"en", "fr-CA"}) {
		t.Errorf("install languages = %v, want [en fr-CA]", got)
	}
}
