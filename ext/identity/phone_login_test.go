package identity

import (
	"context"
	"net/http"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// A store chooses one way for customers to sign in (D116): an email and a
// password, or a phone and a code sent to it by SMS.

func signInBy(t *testing.T, app *gocommerce.App, method string) {
	t.Helper()
	rec := gctest.AdminRequest(t, app, http.MethodPatch, "/api/admin/plugins/"+PluginKey,
		map[string]any{"settings": map[string]any{"login_method": method}})
	if rec.Code != http.StatusOK {
		t.Fatalf("choose %s = %d %s", method, rec.Code, rec.Body)
	}
}

// phoneCode asks for a code and returns the one the SMS carried.
func phoneCode(t *testing.T, app *gocommerce.App, mod *Module, sms *capture, phone string) string {
	t.Helper()
	before := sms.count()
	if rec := gctest.Request(t, app, http.MethodPost, "/x/identity/otp", map[string]any{"phone": phone}); rec.Code != http.StatusAccepted {
		t.Fatalf("ask for a code = %d %s", rec.Code, rec.Body)
	}
	mod.waitForSends(5 * time.Second)
	if sms.count() != before+1 {
		t.Fatalf("sent %d messages, want one more", sms.count()-before)
	}
	n := sms.last()
	if n.Channel != gocommerce.ChannelSMS || n.Event != EventLoginCode || len(n.Data["code"]) != 4 {
		t.Fatalf("sent %+v, want a 4-digit code by SMS", n)
	}
	return n.Data["code"]
}

func phoneApp(t *testing.T) (*gocommerce.App, *Module, *capture) {
	t.Helper()
	sms := &capture{}
	mod := New(Config{Notifier: sms})
	app := gctest.New(t, mod)
	signInBy(t, app, LoginPhoneOTP)
	return app, mod, sms
}

func TestAPhoneSignsInWithACode(t *testing.T) {
	app, mod, sms := phoneApp(t)
	code := phoneCode(t, app, mod, sms, "+91 98765 43210")

	rec := gctest.Request(t, app, http.MethodPost, "/x/identity/otp/verify",
		map[string]any{"phone": "+919876543210", "code": code})
	if rec.Code != http.StatusOK {
		t.Fatalf("verify = %d %s", rec.Code, rec.Body)
	}
	var first AuthResponse
	gctest.DecodeData(t, rec, &first)
	if first.Token == "" || first.Record == nil || first.Record.Phone != "+919876543210" || first.Record.Email != "" {
		t.Fatalf("signed in as %+v, want a new account on the phone", first.Record)
	}
	if me := as(t, app, first.Token, http.MethodGet, "/x/identity/me", nil); me.Code != http.StatusOK {
		t.Fatalf("the session opens nothing: %d", me.Code)
	}

	// The same phone later is the same account, not a second one.
	sms.mu.Lock()
	sms.sent = nil
	sms.mu.Unlock()
	if _, err := mod.db.ExecContext(context.Background(), `UPDATE identity_login_codes SET created_at = now() - interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	code = phoneCode(t, app, mod, sms, "+919876543210")
	rec = gctest.Request(t, app, http.MethodPost, "/x/identity/otp/verify",
		map[string]any{"phone": "+919876543210", "code": code})
	var again AuthResponse
	gctest.DecodeData(t, rec, &again)
	if again.Record == nil || again.Record.ID != first.Record.ID {
		t.Fatalf("second sign-in = %+v, want account %d", again.Record, first.Record.ID)
	}
}

// Four digits are guessable, so a code takes five tries and then opens
// nothing, the right digits included.
func TestAWrongPhoneCodeSpendsATry(t *testing.T) {
	app, mod, sms := phoneApp(t)
	code := phoneCode(t, app, mod, sms, "+919876543210")
	wrong := "0000"
	if code == wrong {
		wrong = "1111"
	}
	for i := 0; i < loginCodeAttempts; i++ {
		rec := gctest.Request(t, app, http.MethodPost, "/x/identity/otp/verify",
			map[string]any{"phone": "+919876543210", "code": wrong})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("wrong code %d = %d, want 422", i+1, rec.Code)
		}
	}
	rec := gctest.Request(t, app, http.MethodPost, "/x/identity/otp/verify",
		map[string]any{"phone": "+919876543210", "code": code})
	if rec.Code == http.StatusOK {
		t.Fatal("the right code worked after every try was spent")
	}
}

func TestPhoneCodesAreNotSentBackToBack(t *testing.T) {
	app, mod, sms := phoneApp(t)
	phoneCode(t, app, mod, sms, "+919876543210")
	rec := gctest.Request(t, app, http.MethodPost, "/x/identity/otp", map[string]any{"phone": "+919876543210"})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second ask at once = %d, want 429", rec.Code)
	}
}

// A phone without its country code would be a different person's in another
// country, so it is asked for.
func TestAPhoneNeedsItsCountryCode(t *testing.T) {
	app, _, _ := phoneApp(t)
	rec := gctest.Request(t, app, http.MethodPost, "/x/identity/otp", map[string]any{"phone": "98765 43210"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("phone without +country = %d, want 400", rec.Code)
	}
}

// Only the chosen way works: the other is refused by the engine, not just
// hidden by the storefront.
func TestOnlyTheChosenWayToSignInWorks(t *testing.T) {
	app, _, _ := phoneApp(t)
	for _, path := range []string{"/x/identity/register", "/x/identity/login", "/x/identity/password-reset"} {
		rec := gctest.Request(t, app, http.MethodPost, path,
			map[string]any{"email": "a@example.com", "password": "correct horse"})
		if rec.Code != http.StatusConflict || errorCode(t, rec) != "sign_in_method" {
			t.Fatalf("%s while phone sign-in is chosen = %d %s, want 409", path, rec.Code, rec.Body)
		}
	}

	email, _ := newApp(t)
	rec := gctest.Request(t, email, http.MethodPost, "/x/identity/otp", map[string]any{"phone": "+919876543210"})
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "sign_in_method" {
		t.Fatalf("phone code while email sign-in is chosen = %d %s, want 409", rec.Code, rec.Body)
	}
}

// The storefront reads the choice without signing in.
func TestTheStorefrontCanReadTheChoice(t *testing.T) {
	app, _, _ := phoneApp(t)
	rec := gctest.Request(t, app, http.MethodGet, "/api/plugins", nil)
	var list []gocommerce.PluginPublic
	gctest.DecodeData(t, rec, &list)
	for _, p := range list {
		if p.Key == PluginKey {
			if p.Settings["login_method"] != LoginPhoneOTP {
				t.Fatalf("public settings = %v, want login_method %s", p.Settings, LoginPhoneOTP)
			}
			return
		}
	}
	t.Fatalf("plugins = %+v, want %s among them", list, PluginKey)
}
