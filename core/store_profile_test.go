package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
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
