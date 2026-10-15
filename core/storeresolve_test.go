package gocommerce

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itswadesh/gocommerce/internal/testdb"
)

// newStrictTestApp boots an engine the way `serve` runs it: no default store,
// so work that names no store sees none.
func newStrictTestApp(t *testing.T) *App {
	t.Helper()
	cfg := testConfig(testdb.Clone(t, "core:", func(dsn string) error {
		app, err := New(testConfig(dsn))
		if err != nil {
			return err
		}
		return app.Close()
	}))
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
	inv := invite(t, app, "owner-b@example.com", RoleStaff, nil)
	if _, _, err := app.invitations.Accept(ctxA, inv.Token, "a-long-enough-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Superusers().Update(ctxA, both.ID, "", "a-new-long-password"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("store A changed the password of store B's owner: %v", err)
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

// Accepting as an existing account checks that account's password, so it is
// a sign-in, and it is throttled like one: otherwise any store owner could mint
// invitations for somebody else's address and guess at their password freely.
func TestAcceptingAsAnExistingAccountIsThrottledLikeASignIn(t *testing.T) {
	app := newTestApp(t)
	b := addTestStore(t, app, "second")
	if _, err := app.Superusers().Create(WithStore(context.Background(), b), "victim@example.com", "the-real-password", RoleOwner); err != nil {
		t.Fatal(err)
	}
	inv := invite(t, app, "victim@example.com", RoleStaff, nil)
	for i := 0; i <= throttleFreeAttempts; i++ { // the free attempts, then one that starts the delay
		if _, _, err := app.invitations.AcceptFrom(context.Background(), inv.Token, fmt.Sprintf("guess-%d-long-enough", i), "203.0.113.9"); err == nil {
			t.Fatal("a wrong password was accepted")
		}
	}
	_, _, err := app.invitations.AcceptFrom(context.Background(), inv.Token, "the-real-password", "203.0.113.9")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests {
		t.Fatalf("after %d wrong passwords the right one was let through: %v", throttleFreeAttempts+1, err)
	}
}

// Somebody who already has an account joins another store by accepting an
// invitation, not by being added: adding them would put another store's
// owner within reach of this store's team screen (sign them out everywhere,
// mail them reset links) without their say.
func TestAnExistingPersonIsInvitedNotAdded(t *testing.T) {
	app := newTestApp(t)
	b := addTestStore(t, app, "second")
	ctxB := WithStore(context.Background(), b)
	if _, err := app.Superusers().Create(ctxB, "owner-b@example.com", "a-long-enough-password", RoleOwner); err != nil {
		t.Fatal(err)
	}
	_, err := app.Superusers().Create(context.Background(), "owner-b@example.com", "any-long-password", RoleStaff)
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "invit") {
		t.Fatalf("adding another store's owner directly: %v, want a conflict that says to invite them", err)
	}
}

// A person who works in another store cannot be signed out of every store
// from this one's team screen.
func TestSigningSomebodyOutEverywhereStaysInThisStoresLane(t *testing.T) {
	app := newTestApp(t)
	b := addTestStore(t, app, "second")
	ctxB := WithStore(context.Background(), b)
	both, err := app.Superusers().Create(ctxB, "both@example.com", "a-long-enough-password", RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	inv := invite(t, app, "both@example.com", RoleStaff, nil)
	if _, _, err := app.invitations.Accept(context.Background(), inv.Token, "a-long-enough-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Superusers().RevokeAll(context.Background(), both.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("store A signed store B's owner out everywhere: %v", err)
	}
}

// storeSeeingMail records which store each email was sent under: that store's
// template and provider account are what carried it.
type storeSeeingMail struct {
	mu    sync.Mutex
	under []string
}

func (m *storeSeeingMail) Name() string            { return "seeing-mail" }
func (m *storeSeeingMail) Migrations() []Migration { return nil }
func (m *storeSeeingMail) Register(app *App) error {
	app.RegisterNotifier(ChannelEmail, notifierFunc(func(ctx context.Context, n Notification) error {
		s, _ := StoreFrom(ctx)
		m.mu.Lock()
		m.under = append(m.under, n.Event+"@"+s)
		m.mu.Unlock()
		return nil
	}))
	return nil
}

// A reset link is a credential for the person everywhere they work. Store B's
// template and email account are B's owner's to read, so they may carry the
// link only for somebody who works in B alone; anybody else's goes by the
// install's own channel.
func TestAResetLinkNeverTravelsThroughAnotherStoresMail(t *testing.T) {
	mail := &storeSeeingMail{}
	app := newTestApp(t, mail)
	b := addTestStore(t, app, "second")
	ctxB := WithStore(context.Background(), b)
	// The victim owns store A; store B is somebody else's.
	if _, err := app.Superusers().Create(context.Background(), "owner-a@example.com", "a-long-enough-password", RoleOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Superusers().Create(ctxB, "only-b@example.com", "a-long-enough-password", RoleStaff); err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{"owner-a@example.com", "only-b@example.com"} {
		if _, err := app.Superusers().RequestReset(ctxB, email, "198.51.100.7"); err != nil {
			t.Fatal(err)
		}
	}
	app.superusers.waitForResetDelivery(5 * time.Second)
	mail.mu.Lock()
	defer mail.mu.Unlock()
	want := []string{EventSuperuserPasswordReset + "@" + installScope, EventSuperuserPasswordReset + "@" + b}
	got := slices.Clone(mail.under)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("resets sent under %v, want %v: store A's owner's link must not go through store B's mail", mail.under, want)
	}
}
