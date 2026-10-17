package gocommerce

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Signing up: a person makes their own account, and opens their store on the
// next screen through the create form every person already has. It is off
// unless the install opens it, because a shop run for one business has no
// business taking accounts from strangers; and it stays shut while the
// install has no email of its own, because that is the only way an address
// somebody else signed up with ever comes back to its owner.

// signupConfig is the configuration of an install that takes sign-ups. Mail
// is a module, so it is the caller's to pass to New.
func signupConfig(t *testing.T) Config {
	t.Helper()
	cfg := testConfig(requireDB(t))
	cfg.Signup = true
	return cfg
}

func newApp(t *testing.T, cfg Config, mods ...Module) *App {
	t.Helper()
	app, err := New(cfg, mods...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app
}

// signupApp is an install that takes sign-ups and can send email, and has
// its owner (sign-up waits for one).
func signupApp(t *testing.T) *App {
	t.Helper()
	app := newApp(t, signupConfig(t), &notifyModule{rec: &recordingNotifier{}})
	newSuperuser(t, app, "operator@example.com", "the operator's password")
	return app
}

func signUp(t *testing.T, app *App, email, password string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	opts = append([]func(*http.Request){jsonBody(t, map[string]string{"email": email, "password": password})}, opts...)
	return do(t, app, http.MethodPost, "/api/admin/signup", opts...)
}

func signupState(t *testing.T, app *App) bool {
	t.Helper()
	var out struct {
		Signup bool `json:"signup"`
	}
	decodeData(t, do(t, app, http.MethodGet, "/api/admin/auth-state"), &out)
	return out.Signup
}

func assertSignupClosed(t *testing.T, app *App) {
	t.Helper()
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
	if signupState(t, app) {
		t.Error("auth-state offers a sign-up the route refuses")
	}
}

func TestSignUpIsClosedUnlessTheInstallOpensIt(t *testing.T) {
	assertSignupClosed(t, newTestApp(t, &notifyModule{rec: &recordingNotifier{}}))
}

// A stranger who signs up with somebody else's address holds it until its
// owner resets the password, and only email can carry that reset. Without the
// install's own email, an open sign-up would be a way to take an address for
// good.
func TestSignUpStaysClosedWithoutTheInstallsOwnEmail(t *testing.T) {
	assertSignupClosed(t, newApp(t, signupConfig(t)))
}

// A demo signs every visitor in as its one account, so an account of your own
// could never be signed back in to.
func TestSignUpIsClosedOnADemo(t *testing.T) {
	cfg := signupConfig(t)
	cfg.Demo = true
	assertSignupClosed(t, newApp(t, cfg, &notifyModule{rec: &recordingNotifier{}}))
}

// A fresh install belongs to whoever runs the installer, and a stranger who
// signed up first and opened a store would shut it (it is open only while no
// store has anybody in it), leaving the first store with no owner. So sign-up
// waits for the install's first owner.
func TestSignUpWaitsForTheInstallsFirstOwner(t *testing.T) {
	app := newApp(t, signupConfig(t), &notifyModule{rec: &recordingNotifier{}})
	assertSignupClosed(t, app)

	install := do(t, app, http.MethodPost, "/api/admin/install",
		jsonBody(t, map[string]string{"email": "operator@example.com", "password": "the operator's password"}))
	if install.Code != http.StatusCreated {
		t.Fatalf("install after a refused sign-up = %d, want 201: %s", install.Code, install.Body)
	}
	if !signupState(t, app) {
		t.Error("sign-up is still closed once the install has its owner")
	}
}

func TestAuthStateSaysSignUpIsOpen(t *testing.T) {
	if !signupState(t, signupApp(t)) {
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

// Naming a store while signing up grants nothing there: the person is not on
// its team, and its admin routes say so.
func TestSigningUpThroughAStoreGrantsNothingThere(t *testing.T) {
	app := signupApp(t)
	here := storeSlug(t, app, app.DefaultStore())
	rec := signUp(t, app, "visitor@example.com", "a long password", header("x-litekart-store", here))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	var out authResponse
	decodeData(t, rec, &out)
	team := do(t, app, http.MethodGet, "/api/admin/superusers", bearer(out.Token), header("x-litekart-store", here))
	if team.Code != http.StatusForbidden {
		t.Fatalf("the store's team list = %d, want 403: %s", team.Code, team.Body)
	}
	if e := decodeError(t, team); e.Code != ErrNotAMember.Code {
		t.Errorf("code = %q, want %s", e.Code, ErrNotAMember.Code)
	}
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
	cfg := signupConfig(t)
	cfg.DefaultStore = ""
	app := newApp(t, cfg, &notifyModule{rec: &recordingNotifier{}})
	two := addTestStore(t, app, "two")
	if _, err := app.Superusers().Create(WithStore(context.Background(), two), "operator@example.com", "the operator's password", RoleOwner); err != nil {
		t.Fatalf("the install's owner: %v", err)
	}

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

// Every attempt that could make an account or name a taken one counts. One
// refused for its own shape tells nobody anything, so it costs nothing — or a
// stream of empty bodies would shut the door on everyone behind one proxy.
func TestSignUpsFromOneAddressAreLimited(t *testing.T) {
	app := signupApp(t)
	app.superusers.signups = newWindowLimit(2, time.Hour)
	newSuperuser(t, app, "taken@example.com", "the owner's password")

	for range 3 {
		if rec := signUp(t, app, "", "", fromIP("10.0.0.7")); rec.Code != http.StatusBadRequest {
			t.Fatalf("an empty sign-up = %d, want 400: %s", rec.Code, rec.Body)
		}
	}
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
	if msg := decodeError(t, rec).Message; !strings.Contains(msg, "minute") {
		t.Errorf("message = %q, want a wait in minutes", msg)
	}
	// Another client is not held up by this one.
	if rec := signUp(t, app, "three@example.com", "a long password", fromIP("10.0.0.8")); rec.Code != http.StatusCreated {
		t.Fatalf("another address = %d, want 201: %s", rec.Code, rec.Body)
	}
}

// One IPv6 subscriber holds a whole /64, so the limit is the network's.
func TestSignUpsFromOneIPv6NetworkShareALimit(t *testing.T) {
	app := signupApp(t)
	app.superusers.signups = newWindowLimit(1, time.Hour)
	from := func(addr string) func(*http.Request) {
		return func(r *http.Request) { r.RemoteAddr = "[" + addr + "]:54321" }
	}
	if rec := signUp(t, app, "a@example.com", "a long password", from("2001:db8:1:2::1")); rec.Code != http.StatusCreated {
		t.Fatalf("first = %d, want 201: %s", rec.Code, rec.Body)
	}
	if rec := signUp(t, app, "b@example.com", "a long password", from("2001:db8:1:2::ffff")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("same /64 = %d, want 429: %s", rec.Code, rec.Body)
	}
	if rec := signUp(t, app, "c@example.com", "a long password", from("2001:db8:1:3::1")); rec.Code != http.StatusCreated {
		t.Fatalf("another /64 = %d, want 201: %s", rec.Code, rec.Body)
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

// A wait of twenty seconds is not "0s".
func TestTooManySignUpsNeverSaysToWaitNoTime(t *testing.T) {
	for wait, want := range map[time.Duration]string{
		20 * time.Second:                "1 minute",
		time.Minute + time.Second:       "2 minutes",
		59*time.Minute + 30*time.Second: "60 minutes",
	} {
		if msg := tooManySignups(wait).Message; !strings.HasSuffix(msg, "try again in "+want) {
			t.Errorf("wait %v: %q, want it to end %q", wait, msg, want)
		}
	}
}

// The forgot-password screen says "link" or "code" from this, so it must be
// the same for every caller: it is a fact about the install.
func TestAResetRequestSaysWhetherTheEmailCarriesALink(t *testing.T) {
	for _, panelURL := range []string{"", "https://shop.example"} {
		cfg := testConfig(requireDB(t))
		cfg.PanelURL = panelURL
		app := newApp(t, cfg, &notifyModule{rec: &recordingNotifier{}})
		t.Cleanup(func() { app.Superusers().waitForResetDelivery(5 * time.Second) })
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
		var state struct {
			ResetLink bool `json:"reset_link"`
		}
		decodeData(t, do(t, app, http.MethodGet, "/api/admin/auth-state"), &state)
		if want := panelURL != ""; state.ResetLink != want {
			t.Errorf("PanelURL %q: auth-state reset_link = %v, want %v", panelURL, state.ResetLink, want)
		}
	}
}

// Store addresses are hosts under the base domain, so the names a platform
// keeps for itself are not anybody's store's.
func TestAReservedAddressIsNobodysStore(t *testing.T) {
	app := signupApp(t)
	tok := signInAs(t, app, "founder@example.com", RoleOwner)
	typed := doBody(t, app, http.MethodPost, "/api/stores/create", `{"name":"Mine","slug":"www"}`, bearer(tok))
	if typed.Code != http.StatusBadRequest {
		t.Fatalf("a typed reserved address = %d, want 400: %s", typed.Code, typed.Body)
	}
	named := doBody(t, app, http.MethodPost, "/api/stores/create", `{"name":"Admin"}`, bearer(tok))
	if named.Code != http.StatusCreated {
		t.Fatalf("a store named Admin = %d, want 201: %s", named.Code, named.Body)
	}
	var out struct {
		Store Store `json:"store"`
	}
	decodeData(t, named, &out)
	if out.Store.Slug == "admin" || !strings.HasPrefix(out.Store.Slug, "admin-") {
		t.Errorf("a store named Admin is at %q, want a numbered admin-…", out.Store.Slug)
	}
}

// Where strangers can make accounts, one of them opens stores at a measured
// pace; the install's own token is not a stranger.
func TestStrangersOpenStoresAtAMeasuredPace(t *testing.T) {
	app := signupApp(t)
	app.storeOpens = newWindowLimit(1, 24*time.Hour)
	rec := signUp(t, app, "busy@example.com", "a long password")
	var out authResponse
	decodeData(t, rec, &out)

	if first := doBody(t, app, http.MethodPost, "/api/stores/create", `{"name":"One"}`, bearer(out.Token)); first.Code != http.StatusCreated {
		t.Fatalf("first store = %d: %s", first.Code, first.Body)
	}
	second := doBody(t, app, http.MethodPost, "/api/stores/create", `{"name":"Two"}`, bearer(out.Token))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second store = %d, want 429: %s", second.Code, second.Body)
	}
	if byToken := doBody(t, app, http.MethodPost, "/api/stores/create", `{"name":"Three"}`, withAdmin); byToken.Code != http.StatusCreated {
		t.Fatalf("the install's token = %d, want 201: %s", byToken.Code, byToken.Body)
	}
}
