package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The shop's own details are the one thing on the Store screen a store may
// change, because they are facts about a business rather than decisions the
// binary was started with.
func TestAStoreDescribesItself(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	// A store that has said nothing gets blanks, not an error: the migration
	// makes the row, so there is always something to read. Its name is the
	// one exception, suggested so that no store is listed as nothing (0067).
	empty, err := app.Profile().Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(strings.Fields(empty.Name)) != 2 || empty.Country != "" || empty.Email != "" {
		t.Errorf("a fresh store = %+v, want a suggested name and blanks", empty)
	}

	saved, err := app.Profile().Set(ctx, StoreProfile{
		Name:         "  The Corner Shop  ",
		LegalName:    "Corner Retail Ltd",
		Email:        "hello@corner.example",
		Phone:        "+44 20 7946 0000",
		AddressLine1: "14 Bridge Street",
		City:         "Bath",
		PostalCode:   "BA1 1AA",
		Country:      "gb",
		TaxID:        "GB123456789",
	}, nil)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	// Trimmed, because a name with a trailing space prints with one.
	if saved.Name != "The Corner Shop" {
		t.Errorf("name = %q, want it trimmed", saved.Name)
	}
	// Upper-cased rather than refused: a country typed in lower case is the
	// right country.
	if saved.Country != "GB" {
		t.Errorf("country = %q, want GB", saved.Country)
	}

	again, err := app.Profile().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.Name != "The Corner Shop" || again.TaxID != "GB123456789" {
		t.Errorf("after a reread = %+v", again)
	}

	// Blanking a field clears it: an empty string is how this form says
	// "nothing", so there is no separate way to unset one.
	cleared, err := app.Profile().Set(ctx, StoreProfile{Name: "The Corner Shop"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.TaxID != "" || cleared.City != "" {
		t.Errorf("a field left out was kept: %+v", cleared)
	}
}

func TestTheStoreProfileRefusesWhatItCannotStore(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	for name, in := range map[string]StoreProfile{
		"a country that is not a code": {Country: "United Kingdom"},
		"an address that is a document": {
			AddressLine1: strings.Repeat("x", MaxProfileField+1),
		},
		"something that is not an email": {Email: "hello.corner.example"},
	} {
		if _, err := app.Profile().Set(ctx, in, nil); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestStoreProfileRoutes(t *testing.T) {
	app := newTestApp(t)

	// Reading carries no right, the way settings does not: a shop's own name
	// is not a secret from its own staff.
	staff := signInAs(t, app, "staff@example.com", RoleStaff)
	if rec := do(t, app, http.MethodGet, "/api/admin/store", bearer(staff)); rec.Code != http.StatusOK {
		t.Fatalf("staff reading the store = %d, want 200: %s", rec.Code, rec.Body)
	}
	// Writing is store.write, which staff does not carry by default.
	if rec := do(t, app, http.MethodPatch, "/api/admin/store", bearer(staff),
		jsonBody(t, map[string]string{"name": "Mine now"})); rec.Code != http.StatusForbidden {
		t.Errorf("staff writing the store = %d, want 403", rec.Code)
	}

	rec := do(t, app, http.MethodPatch, "/api/admin/store", withAdmin,
		jsonBody(t, map[string]string{"name": "The Corner Shop", "country": "gb"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Data StoreProfile `json:"data"`
	}
	decodeJSONBody(t, rec.Body.Bytes(), &body)
	if body.Data.Name != "The Corner Shop" || body.Data.Country != "GB" {
		t.Errorf("data = %+v", body.Data)
	}

	if bad := do(t, app, http.MethodPatch, "/api/admin/store", withAdmin,
		jsonBody(t, map[string]string{"country": "United Kingdom"})); bad.Code != http.StatusBadRequest {
		t.Errorf("a country that is not a code = %d, want 400", bad.Code)
	}
	if denied := do(t, app, http.MethodGet, "/api/admin/store"); denied.Code != http.StatusUnauthorized {
		t.Errorf("with no token = %d, want 401", denied.Code)
	}
}

// A store keeps its own clock and its own voice.
//
// Neither belongs in Config. The timezone is a fact about where the shop is,
// and it decides what "today" means on an invoice and in a report — a store in
// Auckland running on a UTC server closes its day seventeen hours late. The
// language is the store's main one (D87), which a shop chooses and changes.
func TestAStoreKeepsItsOwnClockAndVoice(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	// A store that has said nothing runs on UTC and writes in the language
	// the binary was started with.
	fresh, err := app.Profile().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Timezone != "" || fresh.Language != app.cfg.DefaultLanguage {
		t.Errorf("a fresh store = %+v, want no timezone and the binary's language", fresh)
	}
	if loc := app.Profile().LocationOf(ctx); loc != time.UTC {
		t.Errorf("with nothing set the clock = %v, want UTC", loc)
	}

	saved, err := app.Profile().Set(ctx, StoreProfile{
		Name: "The Corner Shop", Timezone: " Europe/London ", Language: "EN",
	}, nil)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if saved.Timezone != "Europe/London" {
		t.Errorf("timezone = %q, want it trimmed", saved.Timezone)
	}
	// Lower-cased rather than refused, the way the country code is upper-cased:
	// "EN" is the right language typed the wrong way.
	if saved.Language != "en" {
		t.Errorf("language = %q, want it folded to lower case", saved.Language)
	}
	if loc := app.Profile().LocationOf(ctx); loc == nil || loc.String() != "Europe/London" {
		t.Errorf("clock = %v, want Europe/London", loc)
	}
}

// A timezone the machine cannot load is refused at the door rather than
// discovered by an invoice printing the wrong date for a year.
func TestTheStoreProfileRefusesAClockItCannotRead(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	_, err := app.Profile().Set(ctx, StoreProfile{Timezone: "Mars/Olympus_Mons"}, nil)
	if err == nil {
		t.Fatal("a timezone nothing can load should be refused")
	}
	if !strings.Contains(err.Error(), "timezone") {
		t.Errorf("error = %v, want it to name the field", err)
	}

	// And a main language the store does not sell in: it would be served to
	// every shopper who asks for nothing.
	if _, err := app.Profile().Set(ctx, StoreProfile{Language: "xh"}, nil); err == nil {
		t.Fatal("a main language the store does not sell in should be refused")
	}
}

// The store's main language is the fallback for a message; a preference the
// order recorded wins while the store still sells in it (D87).
func TestTheStoreLanguageIsTheFallbackForAMessage(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	if got := app.Profile().MessageLanguage(ctx, ""); got != app.Settings().DefaultLanguage {
		t.Errorf("with nothing set = %q, want the binary's %q", got, app.Settings().DefaultLanguage)
	}
	if _, err := app.Profile().Set(ctx, StoreProfile{Languages: []string{"hi", "en", "fr"}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := app.Profile().MessageLanguage(ctx, ""); got != "hi" {
		t.Errorf("with nothing recorded = %q, want the main language hi", got)
	}
	// A preference recorded on the order wins: it is the customer's. A
	// regional tag finds its language.
	if got := app.Profile().MessageLanguage(ctx, "fr-CA"); got != "fr" {
		t.Errorf("with the order saying fr-CA = %q, want fr", got)
	}
	// One the store has dropped is one nobody keeps the wording of.
	if got := app.Profile().MessageLanguage(ctx, "de"); got != "hi" {
		t.Errorf("with the order saying de = %q, want the main language hi", got)
	}
}

// Anyone may read the shop's name and how a buyer reaches it — the trade
// portal greets a buyer with it before they have signed in (D77) — and
// nothing else from the profile: the address and the tax number stay behind
// the admin route.
func TestTheStoresPublicFaceIsItsNameAndContactsOnly(t *testing.T) {
	app := newTestApp(t)

	rec := do(t, app, http.MethodGet, "/api/store")
	if rec.Code != http.StatusOK {
		t.Fatalf("a store that has said nothing = %d, want 200 with blanks: %s", rec.Code, rec.Body)
	}

	if _, err := app.Profile().Set(context.Background(), StoreProfile{
		Name: "The Corner Shop", Email: "hello@corner.example", Phone: "+44 20 7946 0000",
		SupportURL: "https://corner.example/help", AddressLine1: "14 Bridge Street", City: "Bath",
		TaxID: "GB123456789",
	}, nil); err != nil {
		t.Fatalf("set: %v", err)
	}
	rec = do(t, app, http.MethodGet, "/api/store")
	if rec.Code != http.StatusOK {
		t.Fatalf("with no token = %d, want 200: %s", rec.Code, rec.Body)
	}
	var body struct {
		Data map[string]any `json:"data"`
	}
	decodeJSONBody(t, rec.Body.Bytes(), &body)
	if langs, _ := body.Data["languages"].([]any); len(langs) != 1 || langs[0] != "en" {
		t.Errorf("public languages = %v, want [en]", body.Data["languages"])
	}
	delete(body.Data, "languages")
	want := map[string]any{
		"name": "The Corner Shop", "email": "hello@corner.example",
		"phone": "+44 20 7946 0000", "support_url": "https://corner.example/help",
		"language": "en",
	}
	if len(body.Data) != len(want) {
		t.Errorf("public fields = %v, want exactly %v", body.Data, want)
	}
	for k, v := range want {
		if body.Data[k] != v {
			t.Errorf("%s = %q, want %q", k, body.Data[k], v)
		}
	}
}
