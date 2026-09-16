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
	// makes the row, so there is always something to read.
	empty, err := app.Profile().Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if empty.Name != "" || empty.Country != "" {
		t.Errorf("a fresh store = %+v, want blanks", empty)
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
// language is which one a customer is written to in when the order recorded no
// preference of its own, which is a choice a shop makes and changes.
func TestAStoreKeepsItsOwnClockAndVoice(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	// Blank is a real answer: a store that has said nothing runs on UTC and
	// writes in the language the binary was started with.
	fresh, err := app.Profile().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Timezone != "" || fresh.Language != "" {
		t.Errorf("a fresh store = %+v, want blanks", fresh)
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

	// And a language the binary has no content for: choosing it would send
	// every customer a message in a language nobody translated.
	if _, err := app.Profile().Set(ctx, StoreProfile{Language: "xh"}, nil); err == nil {
		t.Fatal("a language this binary was not started with should be refused")
	}
}

// The store's language is the fallback for a message, and it beats the
// binary's — the panel is the nearer hand, the same rule a plugin setting
// follows over its Config.
func TestTheStoreLanguageIsTheFallbackForAMessage(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	if got := app.Profile().MessageLanguage(ctx, ""); got != app.Settings().DefaultLanguage {
		t.Errorf("with nothing set = %q, want the binary's %q", got, app.Settings().DefaultLanguage)
	}
	// A preference recorded on the order wins over both: it is the customer's.
	if got := app.Profile().MessageLanguage(ctx, "fr"); got != "fr" {
		t.Errorf("with the order saying fr = %q, want fr", got)
	}
}
