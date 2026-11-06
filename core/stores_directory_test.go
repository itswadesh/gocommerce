package gocommerce

import (
	"context"
	"net/http"
	"testing"
)

const directoryPhone = "+919876512345"

// directoryOwner signs somebody in with a number; proved says whether a code
// has confirmed it, which is what the list turns on.
func directoryOwner(t *testing.T, app *App, email string, proved bool) string {
	t.Helper()
	tok := signInAs(t, app, email, RoleOwner)
	if _, err := app.db.ExecContext(context.Background(), `
		UPDATE superusers SET phone = $2, phone_proved_at = CASE WHEN $3 THEN now() END WHERE email = $1`,
		email, directoryPhone, proved); err != nil {
		t.Fatal(err)
	}
	return tok
}

func directoryAllowed(t *testing.T, app *App, tok string) bool {
	t.Helper()
	rec := do(t, app, http.MethodGet, "/api/stores/directory/allowed", bearer(tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("allowed status = %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Allowed bool `json:"allowed"`
	}
	decodeData(t, rec, &out)
	return out.Allowed
}

func TestTheInstallOwnerSeesEveryStoreWithItsOwnersAndCounts(t *testing.T) {
	app := newTestApp(t)
	// Written the way someone would type it in the environment.
	app.cfg.PlatformOwnerPhone = "+91 98765 12345"
	tok := directoryOwner(t, app, "me@example.com", true)

	// Not on this store's team at all, and it still appears.
	other := addTestStore(t, app, "someone-elses")
	signInAs(t, app, "seller@example.com", RoleStaff)
	joinTestStore(t, app, other, "seller@example.com", RoleOwner)
	ctx := WithStore(context.Background(), other)
	for _, slug := range []string{"one", "two"} {
		if _, err := app.db.ExecContext(ctx,
			`INSERT INTO products (slug, title, currency) VALUES ($1, $1, 'INR')`, slug); err != nil {
			t.Fatal(err)
		}
	}

	if !directoryAllowed(t, app, tok) {
		t.Fatal("the install's owner is told they may not see the list")
	}
	rec := do(t, app, http.MethodGet, "/api/stores/directory", bearer(tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	var list []DirectoryStore
	decodeData(t, rec, &list)
	var found *DirectoryStore
	for i := range list {
		if list[i].ID == other {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatalf("list = %+v, missing the other store", list)
	}
	if found.Products != 2 || found.Orders != 0 {
		t.Fatalf("counts = %d products, %d orders, want this store's own 2 and 0", found.Products, found.Orders)
	}
	if len(found.Owners) != 1 || found.Owners[0].Email != "seller@example.com" {
		t.Fatalf("owners = %+v", found.Owners)
	}
	if list[0].ID != app.DefaultStore() || len(list[0].Owners) == 0 || list[0].Owners[0].Phone != directoryPhone {
		t.Fatalf("first store = %+v, want the default store owned by the number", list[0])
	}
}

func TestEveryoneElseIsRefusedTheStoreDirectory(t *testing.T) {
	app := newTestApp(t)
	app.cfg.PlatformOwnerPhone = directoryPhone

	cases := map[string]string{
		"a store's owner":      signInAs(t, app, "owner@example.com", RoleOwner),
		"the static token":     "test-admin-token",
		"the number, unproved": directoryOwner(t, app, "squatter@example.com", false),
	}
	for who, tok := range cases {
		rec := do(t, app, http.MethodGet, "/api/stores/directory", bearer(tok))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403: %s", who, rec.Code, rec.Body)
		}
		if directoryAllowed(t, app, tok) {
			t.Errorf("%s: told they may see the list", who)
		}
	}
	if rec := do(t, app, http.MethodGet, "/api/stores/directory"); rec.Code != http.StatusUnauthorized {
		t.Errorf("no credential: status = %d, want 401", rec.Code)
	}
}

// Unset is the state of every install but one: nobody qualifies, including a
// person with no phone, whose empty number must not match an empty setting.
func TestTheStoreDirectoryIsShutUntilAnOwnerIsNamed(t *testing.T) {
	app := newTestApp(t)
	for who, tok := range map[string]string{
		"no phone":      signInAs(t, app, "plain@example.com", RoleOwner),
		"proved number": directoryOwner(t, app, "proved@example.com", true),
	} {
		if rec := do(t, app, http.MethodGet, "/api/stores/directory", bearer(tok)); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", who, rec.Code)
		}
	}
}
