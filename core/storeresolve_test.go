package gocommerce

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newStrictTestApp boots an engine the way `serve` runs it: no default store,
// so work that names no store sees none.
func newStrictTestApp(t *testing.T) *App {
	t.Helper()
	cfg := testConfig(requireDB(t))
	cfg.DefaultStore = ""
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app
}

func firstStore(t *testing.T, app *App) string {
	t.Helper()
	stores, err := app.Stores().Active(context.Background())
	if err != nil || len(stores) == 0 {
		t.Fatalf("no store: %v", err)
	}
	return stores[0].ID
}

func storeSlug(t *testing.T, app *App, id string) string {
	t.Helper()
	st, err := app.Stores().Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return st.Slug
}

// sessionIn creates a person who is staff of store, in role, and signs them in.
func sessionIn(t *testing.T, app *App, store, email, role string) string {
	t.Helper()
	ctx := WithStore(context.Background(), store)
	if _, err := app.Superusers().Create(ctx, email, "a-long-enough-password", role); err != nil {
		t.Fatalf("create %s: %v", email, err)
	}
	_, session, err := app.Superusers().Authenticate(ctx, email, "a-long-enough-password", "127.0.0.1")
	if err != nil {
		t.Fatalf("sign in %s: %v", email, err)
	}
	return session.Token
}

func TestARequestFindsItsStore(t *testing.T) {
	app := newStrictTestApp(t)
	one := firstStore(t, app)
	two := addTestStore(t, app, "two")
	slugOne := storeSlug(t, app, one)
	cases := []struct {
		name   string
		set    func(*http.Request)
		status int
	}{
		{"header id", header("x-litekart-store", two), 200},
		{"X-Store slug", header("X-Store", slugOne), 200},
		{"X-Store id", header("X-Store", two), 200},
		{"query", func(r *http.Request) { r.URL.RawQuery = "store=" + two }, 200},
		{"cookie", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "litekart_store_id", Value: two}) }, 200},
		{"nothing, two stores", func(r *http.Request) {}, 400},
		{"unknown id", header("x-litekart-store", "store_01JA2Z3K4M5N6P7Q8R9S0T1V2W"), 404},
		{"garbage", header("x-litekart-store", "'; drop table stores;--"), 404},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(t, app, http.MethodGet, "/api/admin/products", withAdmin, c.set)
			if rec.Code != c.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, c.status, rec.Body)
			}
		})
	}
}

func TestAnInstallWithOneStoreNeedsNoHeader(t *testing.T) {
	app := newStrictTestApp(t)
	if rec := do(t, app, http.MethodGet, "/api/admin/products", withAdmin); rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if rec := do(t, app, http.MethodGet, "/api/products"); rec.Code != 200 {
		t.Fatalf("storefront status = %d: %s", rec.Code, rec.Body)
	}
}

func TestASuspendedStoreIsUnavailable(t *testing.T) {
	app := newStrictTestApp(t)
	two := addTestStore(t, app, "two")
	if _, err := app.db.ExecContext(context.Background(), `UPDATE stores SET status = 'suspended' WHERE id = $1`, two); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, app, http.MethodGet, "/api/products", header("x-litekart-store", two)); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestStaffOfOneStoreCannotOpenAnother(t *testing.T) {
	app := newStrictTestApp(t)
	one := firstStore(t, app)
	two := addTestStore(t, app, "two")
	token := sessionIn(t, app, one, "staff@example.com", RoleStaff)
	rec := do(t, app, http.MethodGet, "/api/admin/products", bearer(token), header("x-litekart-store", two))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body)
	}
	// With no store named, a person in exactly one store lands in it.
	if rec := do(t, app, http.MethodGet, "/api/admin/products", bearer(token)); rec.Code != 200 {
		t.Fatalf("sole-membership status = %d: %s", rec.Code, rec.Body)
	}
}

func TestAnAPIKeyCannotBeAimedAtAnotherStore(t *testing.T) {
	app := newStrictTestApp(t)
	one := firstStore(t, app)
	two := addTestStore(t, app, "two")
	_, key, err := app.APIKeys().Create(WithStore(context.Background(), one), APIKeyInput{Name: "ci", Role: RoleOwner}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec := do(t, app, http.MethodGet, "/api/admin/products", bearer(key)); rec.Code != 200 {
		t.Fatalf("the key in its own store = %d: %s", rec.Code, rec.Body)
	}
	rec := do(t, app, http.MethodGet, "/api/admin/products", bearer(key), header("x-litekart-store", two))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("the key aimed at another store = %d, want 403", rec.Code)
	}
}

// One store's team screen cannot change the email or password of somebody who
// also works in another store, nor mail them a reset link from here if they
// are not on this store's team.
func TestOneStoreCannotTakeOverAnotherStoresPerson(t *testing.T) {
	app := newTestApp(t)
	b := addTestStore(t, app, "second")
	ctxA := context.Background()
	ctxB := WithStore(context.Background(), b)
	both, err := app.Superusers().Create(ctxB, "owner-b@example.com", "a-long-enough-password", RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Superusers().Create(ctxA, "owner-b@example.com", "ignored-password", RoleStaff); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Superusers().Update(ctxA, both.ID, "", "a-new-long-password"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("store A changed the password of store B's owner: %v", err)
	}
	if _, _, err := app.Superusers().Authenticate(ctxB, "owner-b@example.com", "ignored-password", "127.0.0.1"); err == nil {
		t.Fatal("joining a second store replaced the person's password")
	}
	onlyB, err := app.Superusers().Create(ctxB, "staff-b@example.com", "a-long-enough-password", RoleStaff)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Superusers().RequestResetFor(ctxA, onlyB.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("store A mailed a reset link to store B's staff: %v", err)
	}
}

func TestAnExistingAccountAcceptsWithItsOwnPassword(t *testing.T) {
	app := newTestApp(t)
	b := addTestStore(t, app, "second")
	ctxB := WithStore(context.Background(), b)
	if _, err := app.Superusers().Create(ctxB, "joiner@example.com", "the-real-password", RoleOwner); err != nil {
		t.Fatal(err)
	}
	inv := invite(t, app, "joiner@example.com", RoleStaff, nil)
	if _, _, err := app.invitations.Accept(context.Background(), inv.Token, "an-attackers-password"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an invitation link set a new password on an existing account: %v", err)
	}
	if _, _, err := app.invitations.Accept(context.Background(), inv.Token, "the-real-password"); err != nil {
		t.Fatalf("accept with the real password: %v", err)
	}
}

// The sign-in screen asks before anybody has a session, so before any store
// is named. "Can anybody sign in" is a question about the install, and a
// person who works in one store signs in to it with no store named.
func TestSigningInNeedsNoStoreNamed(t *testing.T) {
	app := newStrictTestApp(t)
	one := firstStore(t, app)
	addTestStore(t, app, "two")
	sessionIn(t, app, one, "manager@example.com", RoleManager)

	rec := do(t, app, http.MethodGet, "/api/admin/auth-state")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"installed":true`) {
		t.Fatalf("auth-state with two stores = %d %s, want installed", rec.Code, rec.Body)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/auth-with-password",
		strings.NewReader(`{"identity":"manager@example.com","password":"a-long-enough-password"}`))
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	app.Handler().ServeHTTP(out, req)
	if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"role":"manager"`) {
		t.Fatalf("sign-in with no store named = %d %s, want the person's role in their one store", out.Code, out.Body)
	}
}
