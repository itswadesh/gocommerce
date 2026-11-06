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
	if _, err := app.db.ExecContext(ctx, `INSERT INTO store_domains (host, store_id, verified_at) VALUES ('seller.example.com', $1, now())`, other); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.ExecContext(ctx, `INSERT INTO store_domains (host, store_id) VALUES ('pending.example.co.in', $1)`, other); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"one", "two"} {
		if _, err := app.db.ExecContext(ctx,
			`INSERT INTO products (slug, title, currency) VALUES ($1, $1, 'INR')`, slug); err != nil {
			t.Fatal(err)
		}
	}

	// The older store has done something and the newer one nothing, so the
	// older one leads until the newer one acts, below.
	if _, err := app.db.ExecContext(WithStore(context.Background(), app.DefaultStore()),
		`INSERT INTO admin_audit (action, entity_type, entity_id, summary) VALUES ('store.update', 'store', '1', 'Renamed the store')`); err != nil {
		t.Fatal(err)
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
	if len(found.Owners) != 1 || found.Owners[0].Email != "seller@example.com" || found.Owners[0].EmailVerified {
		t.Fatalf("owners = %+v, want the email, not proved", found.Owners)
	}
	if found.Domain != "seller.example.com" {
		t.Fatalf("domain = %q", found.Domain)
	}
	if len(found.Domains) != 2 || found.Domains[0] != (DirectoryDomain{"seller.example.com", true}) ||
		found.Domains[1] != (DirectoryDomain{"pending.example.co.in", false}) {
		t.Fatalf("domains = %+v, want the verified one first and the pending one kept", found.Domains)
	}
	if found.LastActivity != "" || found.LastActivityAt != nil {
		t.Fatalf("activity = %q at %v, want none yet", found.LastActivity, found.LastActivityAt)
	}
	if list[0].ID != app.DefaultStore() {
		t.Fatalf("first store = %+v, want the one with activity ahead of the one with none", list[0])
	}
	if found.CreatedAt.IsZero() {
		t.Fatal("the store has no creation time")
	}
	if first := list[0]; len(first.Owners) == 0 || first.Owners[0].Phone != directoryPhone || !first.Owners[0].PhoneVerified {
		t.Fatalf("first store = %+v, want it owned by the proved number", first)
	}

	// Whichever log holds the newest thing names the activity, and an address
	// stops counting as proved once the account moves off it.
	fetch := func() DirectoryStore {
		t.Helper()
		var again []DirectoryStore
		decodeData(t, do(t, app, http.MethodGet, "/api/stores/directory", bearer(tok)), &again)
		return again[0]
	}
	if _, err := app.db.ExecContext(ctx, `INSERT INTO admin_audit (action, entity_type, entity_id, summary)
		VALUES ('product.update', 'product', '1', 'Renamed a product')`); err != nil {
		t.Fatal(err)
	}
	if got := fetch(); got.ID != other || got.LastActivity != "Renamed a product" || got.LastActivityAt == nil {
		t.Fatalf("after staff acted: %q at %v", got.LastActivity, got.LastActivityAt)
	}
	if _, err := app.db.ExecContext(ctx, `INSERT INTO outbox_events (event_id, event_name, aggregate_type, aggregate_id, payload)
		VALUES (gen_random_uuid(), 'order.placed', 'order', 1, '{}')`); err != nil {
		t.Fatal(err)
	}
	if got := fetch(); got.LastActivity != "order.placed" {
		t.Fatalf("after a shopper acted: %q", got.LastActivity)
	}
	proveEmail := func(sql string) {
		t.Helper()
		if _, err := app.db.ExecContext(context.Background(), sql); err != nil {
			t.Fatal(err)
		}
	}
	proveEmail(`UPDATE superusers SET email_proved = email WHERE email = 'seller@example.com'`)
	if got := fetch(); !got.Owners[0].EmailVerified {
		t.Fatalf("proved address not marked: %+v", got.Owners)
	}
	proveEmail(`UPDATE superusers SET email = 'moved@example.com' WHERE email = 'seller@example.com'`)
	if got := fetch(); got.Owners[0].EmailVerified {
		t.Fatalf("a new address kept the old one's proof: %+v", got.Owners)
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

// A store with only an unverified domain keeps it on the list, and does not get
// to claim it as where the shop is.
func TestAnUnverifiedDomainIsListedButIsNotTheStoresAddress(t *testing.T) {
	app := newTestApp(t)
	app.cfg.PlatformOwnerPhone = directoryPhone
	tok := directoryOwner(t, app, "me@example.com", true)
	store := addTestStore(t, app, "pending-only")
	if _, err := app.db.ExecContext(WithStore(context.Background(), store),
		`INSERT INTO store_domains (host, store_id) VALUES ('pending.example.co.in', $1)`, store); err != nil {
		t.Fatal(err)
	}
	var list []DirectoryStore
	decodeData(t, do(t, app, http.MethodGet, "/api/stores/directory", bearer(tok)), &list)
	for _, row := range list {
		if row.ID != store {
			continue
		}
		if row.Domain != "" || len(row.Domains) != 1 || row.Domains[0].Verified {
			t.Fatalf("row = %+v", row)
		}
		return
	}
	t.Fatal("the store is not listed")
}
