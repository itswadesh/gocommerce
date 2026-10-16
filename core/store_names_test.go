package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// A suggested name is an adjective and a place to shop, and its address is
// the same two words as the engine accepts an address.
func TestASuggestedNameIsTwoShopWords(t *testing.T) {
	word := regexp.MustCompile(`^[A-Z][a-z]+$`)
	for range 200 {
		name, slug := randomStoreName()
		words := strings.Fields(name)
		if len(words) != 2 || !word.MatchString(words[0]) || !word.MatchString(words[1]) ||
			!slices.Contains(storeNameAdjectives, words[0]) || !slices.Contains(storeNameNouns, words[1]) {
			t.Fatalf("name %q is not an adjective and a noun from the lists", name)
		}
		if slug != strings.ToLower(words[0]+"-"+words[1]) || !storeSlugRE.MatchString(slug) {
			t.Fatalf("slug %q for %q", slug, name)
		}
	}
	for in, want := range map[string]string{
		"Varni Jewels": "varni-jewels", " Café  Noir!! ": "caf-noir", "वर्णी ज्वेल्स": "", "Shop 24/7": "shop-24-7",
	} {
		if got := storeSlugFrom(in); got != want {
			t.Errorf("storeSlugFrom(%q) = %q, want %q", in, got, want)
		}
	}
}

// A suggestion never names an address a store already has: the pairing is
// tried again, and a number settles it when the words keep coming up taken.
func TestASuggestionAvoidsATakenAddress(t *testing.T) {
	app := newTestApp(t)
	addTestStore(t, app, "saffron-orchard")
	always := func() (string, string) { return "Saffron Orchard", "saffron-orchard" }
	name, slug, err := app.Stores().suggestName(context.Background(), always)
	if err != nil {
		t.Fatal(err)
	}
	if name != "Saffron Orchard" || !regexp.MustCompile(`^saffron-orchard-\d{3}$`).MatchString(slug) {
		t.Errorf("suggestion = %q %q, want the name with a numbered address", name, slug)
	}
	// A taken pairing is tried again before a number is reached for.
	tries := 0
	second := func() (string, string) {
		tries++
		if tries == 1 {
			return "Saffron Orchard", "saffron-orchard"
		}
		return "Golden Lane", "golden-lane"
	}
	if name, slug, _ := app.Stores().suggestName(context.Background(), second); name != "Golden Lane" || slug != "golden-lane" {
		t.Errorf("after a taken pairing = %q %q, want the next free one", name, slug)
	}

	rec := do(t, app, http.MethodGet, "/api/stores/suggest", withAdmin)
	var got struct{ Name, Slug string }
	decodeData(t, rec, &got)
	if rec.Code != http.StatusOK || len(strings.Fields(got.Name)) != 2 || !storeSlugRE.MatchString(got.Slug) {
		t.Errorf("GET /api/stores/suggest = %d %+v", rec.Code, got)
	}
}

// A store opened without a name takes a suggested one; one opened with a name
// and no address takes its address from the name, numbered when taken, or a
// suggested one when the name has no Latin letters. An address somebody chose
// and somebody else has is still refused.
func TestAStoreIsNeverOpenedWithoutAName(t *testing.T) {
	app := newTestApp(t)
	tok := signInAs(t, app, "namer@example.com", RoleStaff)
	open := func(body string, want int) Store {
		t.Helper()
		rec := doBody(t, app, http.MethodPost, "/api/stores/create", body, bearer(tok))
		if rec.Code != want {
			t.Fatalf("create %s = %d, want %d: %s", body, rec.Code, want, rec.Body)
		}
		var out struct {
			Data struct {
				Store Store `json:"store"`
			} `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Data.Store
	}

	st := open(`{}`, http.StatusCreated)
	if len(strings.Fields(st.Name)) != 2 || !storeSlugRE.MatchString(st.Slug) {
		t.Errorf("a store opened with nothing = %q at %q", st.Name, st.Slug)
	}
	profile, err := app.Profile().Get(WithStore(context.Background(), st.ID))
	if err != nil || profile.Name != st.Name {
		t.Errorf("its profile's name = %q (%v), want %q", profile.Name, err, st.Name)
	}
	if st = open(`{"name":"Varni Jewels"}`, http.StatusCreated); st.Slug != "varni-jewels" {
		t.Errorf("named only = %q", st.Slug)
	}
	if st = open(`{"name":"Varni Jewels"}`, http.StatusCreated); !strings.HasPrefix(st.Slug, "varni-jewels-") {
		t.Errorf("the same name again = %q, want a numbered address", st.Slug)
	}
	if st = open(`{"name":"वर्णी ज्वेल्स"}`, http.StatusCreated); st.Name != "वर्णी ज्वेल्स" || !storeSlugRE.MatchString(st.Slug) {
		t.Errorf("a Hindi name = %q at %q, want the name kept and a suggested address", st.Name, st.Slug)
	}
	open(`{"name":"Again","slug":"varni-jewels"}`, http.StatusConflict)
}

// Stores that had no name in the directory were given one; the profile's name,
// which buyers see, is not touched, and lends itself when it has one.
func TestUnnamedStoresAreGivenAName(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	named := addTestStore(t, app, "named")
	lent := addTestStore(t, app, "lent")
	if _, err := app.db.ExecContext(ctx, `UPDATE stores SET name = '' WHERE id = $1`, lent); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.ExecContext(WithStore(ctx, lent), `UPDATE store_profile SET name = 'Varni Jewels'`); err != nil {
		t.Fatal(err)
	}
	id := addTestStore(t, app, "nameless")
	if _, err := app.db.ExecContext(ctx, `UPDATE stores SET name = '' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.ExecContext(WithStore(ctx, id), `UPDATE store_profile SET name = ''`); err != nil {
		t.Fatal(err)
	}
	if err := InTx(ctx, app.db, func(tx *sql.Tx) error { return migrate0067NameUnnamedStores(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
	st, err := app.Stores().Get(ctx, id)
	if err != nil || len(strings.Fields(st.Name)) != 2 {
		t.Fatalf("the nameless store = %+v, %v", st, err)
	}
	profile, err := app.Profile().Get(WithStore(ctx, id))
	if err != nil || profile.Name != "" {
		t.Errorf("its profile's name = %q (%v), want it left blank for its owner", profile.Name, err)
	}
	if st, _ := app.Stores().Get(ctx, lent); st == nil || st.Name != "Varni Jewels" {
		t.Errorf("the store whose profile had a name = %+v, want that name", st)
	}
	if st, _ := app.Stores().Get(ctx, named); st == nil || st.Name != "named" {
		t.Errorf("a store that had a name = %+v, want it untouched", st)
	}
	if other, _ := app.Stores().BySlug(ctx, "nameless"); other == nil || other.Slug != "nameless" {
		t.Errorf("its address moved: %+v", other)
	}
}
