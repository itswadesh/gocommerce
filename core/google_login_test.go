package gocommerce

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// Signing in with Google (D237), against real Postgres with a stand-in for
// Google's token endpoint.

const googleTestClient = "client-id.apps.googleusercontent.com"

// googleStub answers the token endpoint with claims, and keeps what it was sent.
type googleStub struct {
	claims map[string]any
	status int
	got    url.Values
}

func (g *googleStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	g.got = r.PostForm
	if g.status != 0 {
		w.WriteHeader(g.status)
		return
	}
	payload, _ := json.Marshal(g.claims)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"id_token": "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".unsigned",
	})
}

func googleConfig(t *testing.T, g *googleStub, signup bool) Config {
	t.Helper()
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	cfg := testConfig(requireDB(t))
	cfg.Signup = signup
	cfg.GoogleClientID = googleTestClient
	cfg.GoogleClientSecret = "the secret"
	cfg.GoogleTokenURL = srv.URL
	return cfg
}

func googleApp(t *testing.T, g *googleStub, signup bool) *App {
	t.Helper()
	app := newApp(t, googleConfig(t, g, signup), &notifyModule{rec: &recordingNotifier{}})
	newSuperuser(t, app, "operator@example.com", "the operator's password")
	return app
}

func googleWho(email string) map[string]any {
	return map[string]any{
		"iss": "https://accounts.google.com", "aud": googleTestClient, "exp": time.Now().Add(time.Hour).Unix(),
		"nonce": "the nonce", "email": email, "email_verified": true,
	}
}

func signInGoogle(t *testing.T, app *App) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, app, http.MethodPost, "/api/admin/auth-with-google", jsonBody(t, map[string]string{
		"code": "the code", "code_verifier": "the verifier", "nonce": "the nonce",
		"redirect_uri": "https://admin.example.com/admin/auth/login",
	}))
}

func proveEmail(t *testing.T, app *App, id int64) {
	t.Helper()
	if _, err := app.db.ExecContext(context.Background(), `UPDATE superusers SET email_proved = email WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
}

func TestGoogleSendsTheCodeAndSecretAndSignsInAProvedAddress(t *testing.T) {
	g := &googleStub{claims: googleWho("Operator@Example.com")}
	app := googleApp(t, g, false)
	su, err := app.superusers.personOnEmail(context.Background(), "operator@example.com")
	if err != nil || su == nil {
		t.Fatalf("operator: %v %v", su, err)
	}
	proveEmail(t, app, su.ID)

	rec := signInGoogle(t, app)
	if rec.Code != http.StatusOK {
		t.Fatalf("sign in = %d %s", rec.Code, rec.Body)
	}
	var out authResponse
	decodeData(t, rec, &out)
	if out.Token == "" || out.Record.ID != su.ID {
		t.Fatalf("answer %+v", out)
	}
	for k, want := range map[string]string{"code": "the code", "code_verifier": "the verifier",
		"client_id": googleTestClient, "client_secret": "the secret", "grant_type": "authorization_code",
		"redirect_uri": "https://admin.example.com/admin/auth/login"} {
		if g.got.Get(k) != want {
			t.Errorf("Google got %s = %q, want %q", k, g.got.Get(k), want)
		}
	}
	// A proved address keeps its password.
	if _, _, err := app.superusers.Authenticate(context.Background(), "operator@example.com", "the operator's password", "198.51.100.1"); err != nil {
		t.Errorf("the password stopped working: %v", err)
	}
}

// Whoever typed an address at sign-up, unproved, must not keep a way into the
// account once its real owner arrives.
func TestGoogleEndsAStrangersWayIntoAnUnprovedAddress(t *testing.T) {
	g := &googleStub{claims: googleWho("operator@example.com")}
	app := googleApp(t, g, false)
	ctx := context.Background()
	_, stranger, err := app.superusers.Authenticate(ctx, "operator@example.com", "the operator's password", "198.51.100.1")
	if err != nil {
		t.Fatal(err)
	}

	rec := signInGoogle(t, app)
	if rec.Code != http.StatusOK {
		t.Fatalf("sign in = %d %s", rec.Code, rec.Body)
	}
	if _, _, err := app.superusers.Authenticate(ctx, "operator@example.com", "the operator's password", "198.51.100.2"); err == nil {
		t.Error("the old password still signs in")
	}
	if _, _, ok := app.superusers.Touch(ctx, stranger.Token); ok {
		t.Error("the old session survived")
	}
	var out authResponse
	decodeData(t, rec, &out)
	if _, _, ok := app.superusers.Touch(ctx, out.Token); !ok {
		t.Error("the new session does not work")
	}
}

// A proved phone is somebody real: Google is refused, and nothing is wiped.
func TestGoogleLeavesAProvedPhoneAlone(t *testing.T) {
	g := &googleStub{claims: googleWho("operator@example.com")}
	app := googleApp(t, g, false)
	ctx := context.Background()
	su, err := app.superusers.personOnEmail(ctx, "operator@example.com")
	if err != nil || su == nil {
		t.Fatalf("operator: %v %v", su, err)
	}
	withPhone(t, app, su, knownPhone)

	if rec := signInGoogle(t, app); rec.Code != http.StatusConflict {
		t.Fatalf("sign in = %d %s", rec.Code, rec.Body)
	}
	if _, _, err := app.superusers.Authenticate(ctx, "operator@example.com", "the operator's password", "198.51.100.1"); err != nil {
		t.Errorf("the password stopped working: %v", err)
	}
	if on, err := app.superusers.personOnPhone(ctx, knownPhone); err != nil || on == nil || on.ID != su.ID {
		t.Errorf("the phone came off: %v %v", on, err)
	}

	proveEmail(t, app, su.ID)
	if rec := signInGoogle(t, app); rec.Code != http.StatusOK {
		t.Fatalf("with the email proved = %d %s", rec.Code, rec.Body)
	}
}

func TestGoogleMakesAnAccountOnlyWhereSignUpIsOpen(t *testing.T) {
	g := &googleStub{claims: googleWho("new@example.com")}
	closed := googleApp(t, g, false)
	if rec := signInGoogle(t, closed); rec.Code != http.StatusForbidden {
		t.Fatalf("closed install = %d %s", rec.Code, rec.Body)
	}

	open := googleApp(t, g, true)
	rec := signInGoogle(t, open)
	if rec.Code != http.StatusCreated {
		t.Fatalf("open install = %d %s", rec.Code, rec.Body)
	}
	if again := signInGoogle(t, open); again.Code != http.StatusOK {
		t.Fatalf("second sign in = %d %s", again.Code, again.Body)
	}
	if n, err := open.superusers.CountPeople(context.Background()); err != nil || n != 2 {
		t.Errorf("people = %d, %v; want the operator and one new person", n, err)
	}
}

func TestGoogleRefusesATokenNotForThisAttempt(t *testing.T) {
	g := &googleStub{}
	app := googleApp(t, g, true)
	for name, change := range map[string]func(map[string]any){
		"another client":  func(c map[string]any) { c["aud"] = "someone-else" },
		"another issuer":  func(c map[string]any) { c["iss"] = "https://evil.example.com" },
		"another attempt": func(c map[string]any) { c["nonce"] = "another" },
		"no nonce":        func(c map[string]any) { delete(c, "nonce") },
		"expired":         func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() },
		"unverified":      func(c map[string]any) { c["email_verified"] = false },
		"no address":      func(c map[string]any) { delete(c, "email") },
	} {
		g.claims = googleWho("new@example.com")
		change(g.claims)
		if rec := signInGoogle(t, app); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
	}
	g.status = http.StatusBadRequest
	if rec := signInGoogle(t, app); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a refused code = %d %s", rec.Code, rec.Body)
	}
	g.status = http.StatusServiceUnavailable
	if rec := signInGoogle(t, app); rec.Code != http.StatusBadGateway {
		t.Errorf("Google down = %d %s", rec.Code, rec.Body)
	}
}

func TestADisabledPersonCannotSignInWithGoogle(t *testing.T) {
	g := &googleStub{claims: googleWho("operator@example.com")}
	app := googleApp(t, g, false)
	if _, err := app.db.ExecContext(context.Background(), `UPDATE superusers SET disabled_at = now() WHERE email = 'operator@example.com'`); err != nil {
		t.Fatal(err)
	}
	if rec := signInGoogle(t, app); rec.Code != http.StatusForbidden {
		t.Fatalf("disabled = %d %s", rec.Code, rec.Body)
	}
}

// The button shows only where the route can work.
func TestAuthStateOffersGoogleOnlyWhenSetUp(t *testing.T) {
	read := func(t *testing.T, app *App) string {
		t.Helper()
		var out struct {
			ID string `json:"google_client_id"`
		}
		decodeData(t, do(t, app, http.MethodGet, "/api/admin/auth-state"), &out)
		return out.ID
	}
	set := googleApp(t, &googleStub{}, false)
	if got := read(t, set); got != googleTestClient {
		t.Errorf("set up: %q", got)
	}
	half := googleConfig(t, &googleStub{}, false)
	half.GoogleClientSecret = ""
	app := newApp(t, half)
	if got := read(t, app); got != "" {
		t.Errorf("no secret: %q", got)
	}
	if rec := signInGoogle(t, app); rec.Code != http.StatusConflict {
		t.Errorf("no secret, sign in = %d %s", rec.Code, rec.Body)
	}
}
