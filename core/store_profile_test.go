package gocommerce

import (
	"context"
	"net/http"
	"slices"
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
	if hours, _ := body.Data["opening_hours"].([]any); hours == nil || len(hours) != 0 {
		t.Errorf("public opening hours = %v, want an empty list", body.Data["opening_hours"])
	}
	delete(body.Data, "opening_hours")
	want := map[string]any{
		"name": "The Corner Shop", "email": "hello@corner.example",
		"phone": "+44 20 7946 0000", "support_url": "https://corner.example/help",
		"language": "en", "images_path": ImagesPath(app.defaultStore),
		// Its logo and icon (D148), empty until it chooses them.
		"logo_url": "", "logo_dark_url": "", "icon_url": "",
		// The address only once the store says to show it (D156).
		"address": nil, "map_location": "",
		// The country on its own, so a storefront can write the phone number
		// in full; empty until the store chooses one.
		"country": "",
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

// An Indian store's tax id is the GSTIN on every tax invoice it issues, so a
// mistyped one is refused when it is saved and a good one is stored canonical.
func TestAProfileWithAnInvalidGSTINIsRefused(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	_, err := app.Profile().Set(ctx, StoreProfile{Country: "IN", State: "Maharashtra", TaxID: "27AAPFU0939F1ZW"}, nil)
	if err == nil || !strings.Contains(err.Error(), "the last character should be V") {
		t.Fatalf("a GSTIN with the wrong check character = %v, want it refused naming V", err)
	}
	saved, err := app.Profile().Set(ctx, StoreProfile{Country: "IN", State: "Maharashtra", TaxID: " 27aapfu0939f1zv "}, nil)
	if err != nil {
		t.Fatalf("a good GSTIN: %v", err)
	}
	if saved.TaxID != "27AAPFU0939F1ZV" {
		t.Errorf("tax id stored as %q, want 27AAPFU0939F1ZV", saved.TaxID)
	}
}

func TestAProfileWithAGSTINFromAnotherStateIsRefused(t *testing.T) {
	app := newTestApp(t)
	_, err := app.Profile().Set(context.Background(),
		StoreProfile{Country: "IN", State: "Odisha", TaxID: "27AAPFU0939F1ZV"}, nil)
	if err == nil || !strings.Contains(err.Error(), "registered in Maharashtra (27), but the store's state is Odisha (21)") {
		t.Errorf("a Maharashtra GSTIN on an Odisha store = %v, want both states named", err)
	}
}

func TestAGBVATNumberOnAGBStoreStillSaves(t *testing.T) {
	app := newTestApp(t)
	saved, err := app.Profile().Set(context.Background(),
		StoreProfile{Country: "GB", TaxID: "GB123456789"}, nil)
	if err != nil {
		t.Fatalf("a UK VAT number: %v", err)
	}
	if saved.TaxID != "GB123456789" {
		t.Errorf("tax id stored as %q, want it as typed", saved.TaxID)
	}
}

// A storefront's contact page shows where the shop is, when it is open and a
// map (D156). The address is the one an invoice prints, which for a shop run
// from home is somebody's front door, so it is public only once the store
// says to show it; a map location and opening hours exist only for that page,
// so setting one is the decision to publish it.
func TestTheContactPageShowsOnlyWhatTheStoreChoseToPublish(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	hidden, shown := false, true
	pin := " 20.2961 , 85.8245 "

	saved, err := app.Profile().Set(ctx, StoreProfile{
		Name: "The Corner Shop", AddressLine1: "14 Bridge Street", City: "Bath", PostalCode: "BA1 1AA",
		Country: "GB", ShowAddress: &hidden, MapLocation: &pin,
		OpeningHours: []OpeningHours{
			{Day: " SAT ", Opens: "10:00", Closes: "16:00"},
			{Day: "mon", Opens: "9:30", Closes: "18:00"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if saved.MapLocation == nil || *saved.MapLocation != "20.2961,85.8245" {
		t.Errorf("map location = %v, want the pair without spaces", saved.MapLocation)
	}
	// Folded, padded and in the week's order, so every reader can print them
	// without parsing a second time.
	wantHours := []OpeningHours{{Day: "mon", Opens: "09:30", Closes: "18:00"}, {Day: "sat", Opens: "10:00", Closes: "16:00"}}
	if !slices.Equal(saved.OpeningHours, wantHours) {
		t.Errorf("opening hours = %+v, want %+v", saved.OpeningHours, wantHours)
	}

	public := publicStoreOf(t, app)
	// The country is public on its own: a storefront needs it to write the
	// phone number with its country code, and it says nothing about the door.
	if public.Country != "GB" {
		t.Errorf("public country = %q, want GB", public.Country)
	}
	if public.Address != nil {
		t.Errorf("an address the store keeps to itself was published: %+v", public.Address)
	}
	if public.MapLocation != "20.2961,85.8245" || !slices.Equal(public.OpeningHours, wantHours) {
		t.Errorf("public map and hours = %q %+v", public.MapLocation, public.OpeningHours)
	}

	// Shown on purpose. The rest of the profile is left out, so absent fields
	// keep what they held: a script that predates them cannot wipe them.
	if _, err := app.Profile().Set(ctx, StoreProfile{
		Name: "The Corner Shop", AddressLine1: "14 Bridge Street", City: "Bath", PostalCode: "BA1 1AA",
		Country: "GB", ShowAddress: &shown,
	}, nil); err != nil {
		t.Fatalf("show the address: %v", err)
	}
	public = publicStoreOf(t, app)
	want := publicAddress{Line1: "14 Bridge Street", City: "Bath", PostalCode: "BA1 1AA", Country: "GB"}
	if public.Address == nil || *public.Address != want {
		t.Errorf("public address = %+v, want %+v", public.Address, want)
	}
	if public.MapLocation != "20.2961,85.8245" || len(public.OpeningHours) != 2 {
		t.Errorf("a save that did not send the map or the hours changed them: %q %+v", public.MapLocation, public.OpeningHours)
	}

	// Empty clears: no hours, no map, and an address switched on with nothing
	// in it is no address.
	blank := ""
	if _, err := app.Profile().Set(ctx, StoreProfile{
		Name: "The Corner Shop", ShowAddress: &shown, MapLocation: &blank, OpeningHours: []OpeningHours{},
	}, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	public = publicStoreOf(t, app)
	if public.Address != nil || public.MapLocation != "" || len(public.OpeningHours) != 0 {
		t.Errorf("after clearing = %+v", public)
	}

	// Through the route, as the panel saves it.
	rec := do(t, app, http.MethodPatch, "/api/admin/store", withAdmin, jsonBody(t, map[string]any{
		"name": "The Corner Shop", "city": "Bath", "show_address": true,
		"map_location":  "https://maps.app.goo.gl/AbCdEf123",
		"opening_hours": []map[string]string{{"day": "sun", "opens": "11:00", "closes": "15:00"}},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rec.Code, rec.Body)
	}
	public = publicStoreOf(t, app)
	if public.Address == nil || public.Address.City != "Bath" || public.MapLocation != "https://maps.app.goo.gl/AbCdEf123" ||
		len(public.OpeningHours) != 1 || public.OpeningHours[0].Day != "sun" {
		t.Errorf("after the route = %+v", public)
	}
}

// A storefront frames the map location and prints the hours, so what cannot
// be either is refused as it is typed, naming the field.
func TestAMapLocationOrOpeningHoursThatCannotBeShownIsRefused(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	at := func(s string) *string { return &s }

	for _, ok := range []string{
		"-33.8688,151.2093",
		"https://www.google.com/maps/place/Bath/@51.38,-2.36,14z",
		"https://www.google.co.in/maps/embed?pb=!1m18!1m12",
		"https://maps.google.com/?q=Bath",
		"https://goo.gl/maps/AbCdEf",
		// A Business Profile's "Share profile" links.
		"https://g.page/r/CabcDEF123",
		"https://g.co/kgs/AbC123",
	} {
		if _, err := app.Profile().Set(ctx, StoreProfile{MapLocation: at(ok)}, nil); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"the shop by the river",
		"91.0, 10.0",
		"10.0, 181",
		"http://www.google.com/maps/place/Bath",
		"https://www.google.com/search?q=bath",
		"https://evil.example/maps/embed",
		"https://www.google.com.evil.example/maps",
		"https://maps.app.goo.gl.evil.example/x",
		"javascript:alert(1)",
		"https://g.co/other/AbC123",
		`<iframe src="https://evil.example/maps/embed?pb=x"></iframe>`,
		"https://www.google.com/maps/" + strings.Repeat("x", MaxMapLocation),
	} {
		_, err := app.Profile().Set(ctx, StoreProfile{MapLocation: at(bad)}, nil)
		if err == nil || !strings.Contains(err.Error(), "map location") {
			t.Errorf("%q = %v, want it refused naming the map location", bad, err)
		}
	}

	snippet := `<iframe src="https://www.google.com/maps/embed?pb=!1m18!1m12!3d18.52!2d73.85" width="600" height="450" style="border:0;" allowfullscreen="" loading="lazy"></iframe>`
	saved, err := app.Profile().Set(ctx, StoreProfile{MapLocation: at(snippet)}, nil)
	if err != nil || *saved.MapLocation != "https://www.google.com/maps/embed?pb=!1m18!1m12!3d18.52!2d73.85" {
		t.Errorf("a pasted embed snippet = %v, %v; want its src kept", saved.MapLocation, err)
	}

	for name, hours := range map[string][]OpeningHours{
		"a day that is not one":   {{Day: "funday", Opens: "09:00", Closes: "17:00"}},
		"a time past midnight":    {{Day: "mon", Opens: "09:00", Closes: "25:00"}},
		"no time at all":          {{Day: "mon"}},
		"closing before opening":  {{Day: "mon", Opens: "18:00", Closes: "09:00"}},
		"the same day twice":      {{Day: "mon", Opens: "09:00", Closes: "12:00"}, {Day: "MON", Opens: "13:00", Closes: "17:00"}},
		"a time with seconds too": {{Day: "mon", Opens: "09:00:00", Closes: "17:00"}},
	} {
		_, err := app.Profile().Set(ctx, StoreProfile{OpeningHours: hours}, nil)
		if err == nil || !strings.Contains(err.Error(), "opening hours") {
			t.Errorf("%s = %v, want it refused naming the opening hours", name, err)
		}
	}

	rec := do(t, app, http.MethodPatch, "/api/admin/store", withAdmin,
		jsonBody(t, map[string]any{"map_location": "the shop by the river"}))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a map location nothing can show = %d, want 400: %s", rec.Code, rec.Body)
	}
}
