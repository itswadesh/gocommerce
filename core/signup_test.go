package gocommerce

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Signing up: a person makes their own account, and opens their store on the
// next screen through the create form every person already has. It is off
// unless the install opens it, because a shop run for one business has no
// business taking accounts from strangers.

func signupApp(t *testing.T) *App {
	t.Helper()
	cfg := testConfig(requireDB(t))
	cfg.Signup = true
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app
}

func signUp(t *testing.T, app *App, email, password string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	opts = append([]func(*http.Request){jsonBody(t, map[string]string{"email": email, "password": password})}, opts...)
	return do(t, app, http.MethodPost, "/api/admin/signup", opts...)
}

func TestSignUpIsClosedUnlessTheInstallOpensIt(t *testing.T) {
	app := newTestApp(t)
	rec := signUp(t, app, "stranger@example.com", "a long password")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body)
	}
	if e := decodeError(t, rec); e.Code != "signup_closed" {
		t.Errorf("code = %q, want signup_closed", e.Code)
	}
	var n int
	if err := app.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM superusers WHERE email = 'stranger@example.com'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("a closed sign-up made %d accounts", n)
	}

	state := do(t, app, http.MethodGet, "/api/admin/auth-state")
	var out struct {
		Signup bool `json:"signup"`
	}
	decodeData(t, state, &out)
	if out.Signup {
		t.Error("auth-state offers sign-up on an install that has not opened it")
	}
}

func TestAuthStateSaysSignUpIsOpen(t *testing.T) {
	app := signupApp(t)
	var out struct {
		Signup bool `json:"signup"`
	}
	decodeData(t, do(t, app, http.MethodGet, "/api/admin/auth-state"), &out)
	if !out.Signup {
		t.Error("auth-state does not say sign-up is open")
	}
}

// The account is the person's alone: whatever store the request happened to
// reach (here the default one), signing up makes them staff of none of them.
// Their store is the one they open next.
func TestSigningUpMakesAPersonInNoStoreAndSignsThemIn(t *testing.T) {
	app := signupApp(t)
	rec := signUp(t, app, "  Founder@Example.com ", "a long password")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	var out authResponse
	decodeData(t, rec, &out)
	if out.Token == "" || out.Record == nil || out.Record.Email != "founder@example.com" {
		t.Fatalf("response = %+v, want a session for founder@example.com", out)
	}
	if out.Record.Role != "" || len(out.Record.Rights) != 0 {
		t.Errorf("record = %+v, want no role and no rights before a store exists", out.Record)
	}

	mine := do(t, app, http.MethodGet, "/api/stores/my", bearer(out.Token))
	if mine.Code != http.StatusOK {
		t.Fatalf("stores/my = %d: %s", mine.Code, mine.Body)
	}
	var stores []MyStore
	decodeData(t, mine, &stores)
	if len(stores) != 0 {
		t.Fatalf("a new person works in %+v, want no store", stores)
	}

	// And the next screen's create form opens their store, with them as owner.
	created := doBody(t, app, http.MethodPost, "/api/stores/create", `{"name":"Founder Goods"}`, bearer(out.Token))
	if created.Code != http.StatusCreated {
		t.Fatalf("create store = %d: %s", created.Code, created.Body)
	}
	var entry struct {
		Record Superuser `json:"record"`
	}
	decodeData(t, created, &entry)
	if entry.Record.Role != RoleOwner {
		t.Errorf("role in the new store = %q, want owner", entry.Record.Role)
	}

	// The password they chose is the one that signs them in.
	signIn(t, app, "founder@example.com", "a long password", "10.0.0.4")
}

// Saying so is the price of an open sign-up: there is no way to refuse an
// address without saying it is taken, short of making the first step an email.
// The account behind it is untouched either way.
func TestATakenAddressIsToldToSignIn(t *testing.T) {
	app := signupApp(t)
	newSuperuser(t, app, "owner@example.com", "the owner's password")

	rec := signUp(t, app, "OWNER@example.com", "somebody else's")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body)
	}
	if e := decodeError(t, rec); e.Code != "email_taken" {
		t.Errorf("code = %q, want email_taken", e.Code)
	}
	signIn(t, app, "owner@example.com", "the owner's password", "10.0.0.5")
}

// The way `serve` runs it: several stores, no default, and a sign-up page that
// names none of them — the person has none yet. Like signing in, signing up is
// not one store's.
func TestSigningUpNeedsNoStoreNamed(t *testing.T) {
	cfg := testConfig(requireDB(t))
	cfg.DefaultStore = ""
	cfg.Signup = true
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	addTestStore(t, app, "two")

	rec := signUp(t, app, "unbound@example.com", "a long password")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
}

func TestSignUpRefusesWhatSignInWouldRefuse(t *testing.T) {
	app := signupApp(t)
	for _, c := range []struct{ email, password string }{
		{"short@example.com", "seven77"},
		{"not an address", "a long password"},
		{"", "a long password"},
	} {
		rec := signUp(t, app, c.email, c.password)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("sign up %q / %q = %d, want 400: %s", c.email, c.password, rec.Code, rec.Body)
		}
	}
}

// Every attempt counts, the refused ones included: a 409 says whether an
// address has an account, so asking it is what the limit is for.
func TestSignUpsFromOneAddressAreLimited(t *testing.T) {
	app := signupApp(t)
	app.superusers.signups = newWindowLimit(2, time.Hour)
	newSuperuser(t, app, "taken@example.com", "the owner's password")

	if rec := signUp(t, app, "taken@example.com", "a long password", fromIP("10.0.0.7")); rec.Code != http.StatusConflict {
		t.Fatalf("first = %d, want 409: %s", rec.Code, rec.Body)
	}
	if rec := signUp(t, app, "one@example.com", "a long password", fromIP("10.0.0.7")); rec.Code != http.StatusCreated {
		t.Fatalf("second = %d, want 201: %s", rec.Code, rec.Body)
	}
	rec := signUp(t, app, "two@example.com", "a long password", fromIP("10.0.0.7"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third = %d, want 429: %s", rec.Code, rec.Body)
	}
	// Another client is not held up by this one.
	if rec := signUp(t, app, "three@example.com", "a long password", fromIP("10.0.0.8")); rec.Code != http.StatusCreated {
		t.Fatalf("another address = %d, want 201: %s", rec.Code, rec.Body)
	}
}

func TestAWindowLimitForgetsWhatIsOlderThanItsWindow(t *testing.T) {
	l := newWindowLimit(1, time.Minute)
	now := time.Now()
	if _, ok := l.take("a", now); !ok {
		t.Fatal("the first take was refused")
	}
	if wait, ok := l.take("a", now.Add(30*time.Second)); ok || wait <= 0 || wait > 30*time.Second {
		t.Fatalf("second take = %v, %v; want refused with at most 30s to wait", wait, ok)
	}
	if _, ok := l.take("a", now.Add(61*time.Second)); !ok {
		t.Fatal("a take after the window was refused")
	}
}

// The forgot-password screen says "link" or "code" from this, so it must be
// the same for every caller: it is a fact about the install.
func TestAResetRequestSaysWhetherTheEmailCarriesALink(t *testing.T) {
	for _, panelURL := range []string{"", "https://shop.example"} {
		rec := &recordingNotifier{}
		cfg := testConfig(requireDB(t))
		cfg.PanelURL = panelURL
		app, err := New(cfg, &notifyModule{rec: rec})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		newSuperuser(t, app, "owner@example.com", "a good password")
		for _, email := range []string{"owner@example.com", "nobody@example.com"} {
			res := do(t, app, http.MethodPost, "/api/admin/password-reset",
				jsonBody(t, map[string]string{"identity": email}), fromIP("10.0.1.1"))
			var out ResetRequest
			decodeData(t, res, &out)
			if want := panelURL != ""; out.Link != want {
				t.Errorf("PanelURL %q, %s: link = %v, want %v", panelURL, email, out.Link, want)
			}
		}
		app.Superusers().waitForResetDelivery(5 * time.Second)
		_ = app.Close()
	}
}
