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

// "Email or phone": both ways work side by side.
func TestBothWaysWorkWhenTheStoreAllowsBoth(t *testing.T) {
	sms := &capture{}
	mod := New(Config{Notifier: sms})
	app := gctest.New(t, mod)
	signInBy(t, app, LoginEither)
	register(t, app, "ada@example.com")
	phoneCode(t, app, mod, sms, "+919876543210")
}

// An email account proves a phone with a code, and from then on signing in
// with that phone opens the same account.
func TestAnEmailAccountLinksAPhone(t *testing.T) {
	sms := &capture{}
	mod := New(Config{Notifier: sms})
	app := gctest.New(t, mod)
	signInBy(t, app, LoginEither)
	auth := register(t, app, "ada@example.com")

	if rec := as(t, app, auth.Token, http.MethodPost, "/x/identity/me/phone", map[string]any{"phone": "+91 98765 43210"}); rec.Code != http.StatusAccepted {
		t.Fatalf("ask to link = %d %s", rec.Code, rec.Body)
	}
	mod.waitForSends(5 * time.Second)
	code := sms.last().Data["code"]
	rec := as(t, app, auth.Token, http.MethodPost, "/x/identity/me/phone/verify", map[string]any{"phone": "+919876543210", "code": code})
	if rec.Code != http.StatusOK {
		t.Fatalf("link = %d %s", rec.Code, rec.Body)
	}
	var me Customer
	gctest.DecodeData(t, rec, &me)
	if !me.PhoneVerified || me.Phone != "+919876543210" {
		t.Fatalf("after linking = %+v, want the phone verified", me)
	}

	// Signing in with the phone now opens the email account.
	if _, err := mod.db.ExecContext(context.Background(), `UPDATE identity_login_codes SET created_at = now() - interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	code = phoneCode(t, app, mod, sms, "+919876543210")
	rec = gctest.Request(t, app, http.MethodPost, "/x/identity/otp/verify", map[string]any{"phone": "+919876543210", "code": code})
	var byPhone AuthResponse
	gctest.DecodeData(t, rec, &byPhone)
	if byPhone.Record == nil || byPhone.Record.ID != auth.Record.ID || byPhone.Record.Email != "ada@example.com" {
		t.Fatalf("phone sign-in = %+v, want account %d", byPhone.Record, auth.Record.ID)
	}
}

// A wrong code links nothing, and a phone that is already another account's
// way in is not taken from it.
func TestAPhoneIsLinkedOnlyWithItsCodeAndOnlyOnce(t *testing.T) {
	sms := &capture{}
	mod := New(Config{Notifier: sms})
	app := gctest.New(t, mod)
	signInBy(t, app, LoginEither)

	// The phone opens an account of its own first.
	code := phoneCode(t, app, mod, sms, "+919876543210")
	gctest.Request(t, app, http.MethodPost, "/x/identity/otp/verify", map[string]any{"phone": "+919876543210", "code": code})
	if _, err := mod.db.ExecContext(context.Background(), `UPDATE identity_login_codes SET created_at = now() - interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}

	auth := register(t, app, "ada@example.com")
	if rec := as(t, app, auth.Token, http.MethodPost, "/x/identity/me/phone/verify", map[string]any{"phone": "+919876543210", "code": "0000"}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("link with no code sent = %d, want 422", rec.Code)
	}
	as(t, app, auth.Token, http.MethodPost, "/x/identity/me/phone", map[string]any{"phone": "+919876543210"})
	mod.waitForSends(5 * time.Second)
	code = sms.last().Data["code"]
	rec := as(t, app, auth.Token, http.MethodPost, "/x/identity/me/phone/verify", map[string]any{"phone": "+919876543210", "code": code})
	if rec.Code != http.StatusConflict {
		t.Fatalf("link a phone with its own account = %d %s, want 409", rec.Code, rec.Body)
	}
}

// A store that signs in by email only has nothing to link a phone to.
func TestLinkingAPhoneNeedsPhoneSignIn(t *testing.T) {
	app, _ := newApp(t)
	auth := register(t, app, "ada@example.com")
	rec := as(t, app, auth.Token, http.MethodPost, "/x/identity/me/phone", map[string]any{"phone": "+919876543210"})
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "sign_in_method" {
		t.Fatalf("link on an email-only store = %d %s, want 409", rec.Code, rec.Body)
	}
}

// The other way round: an account opened with a phone adds an email and a
// first password, and from then on either signs in to it.
func TestAPhoneAccountAddsAnEmail(t *testing.T) {
	sms := &capture{}
	mod := New(Config{Notifier: sms})
	app := gctest.New(t, mod)
	signInBy(t, app, LoginEither)
	code := phoneCode(t, app, mod, sms, "+919876543210")
	rec := gctest.Request(t, app, http.MethodPost, "/x/identity/otp/verify", map[string]any{"phone": "+919876543210", "code": code})
	var byPhone AuthResponse
	gctest.DecodeData(t, rec, &byPhone)

	if rec := as(t, app, byPhone.Token, http.MethodPatch, "/x/identity/me", map[string]any{"email": "ada@example.com"}); rec.Code != http.StatusOK {
		t.Fatalf("add email = %d %s", rec.Code, rec.Body)
	}
	// No password yet, so none is asked for.
	if rec := as(t, app, byPhone.Token, http.MethodPut, "/x/identity/me/password", map[string]any{"password": "correct horse"}); rec.Code != http.StatusOK {
		t.Fatalf("first password = %d %s", rec.Code, rec.Body)
	}
	rec = gctest.Request(t, app, http.MethodPost, "/x/identity/login", map[string]any{"email": "ada@example.com", "password": "correct horse"})
	var byEmail AuthResponse
	gctest.DecodeData(t, rec, &byEmail)
	if byEmail.Record == nil || byEmail.Record.ID != byPhone.Record.ID || !byEmail.Record.PhoneVerified {
		t.Fatalf("email sign-in = %+v, want account %d with its phone", byEmail.Record, byPhone.Record.ID)
	}

	// With a password, changing it needs the current one again.
	if rec := as(t, app, byEmail.Token, http.MethodPut, "/x/identity/me/password", map[string]any{"password": "another horse"}); rec.Code == http.StatusOK {
		t.Fatal("a password was replaced without the current one")
	}
}

// phone_verified describes the phone on the profile: typing another number
// there does not make it a proved one.
func TestAnEditedPhoneIsNotVerified(t *testing.T) {
	sms := &capture{}
	mod := New(Config{Notifier: sms})
	app := gctest.New(t, mod)
	signInBy(t, app, LoginPhoneOTP)
	code := phoneCode(t, app, mod, sms, "+919876543210")
	rec := gctest.Request(t, app, http.MethodPost, "/x/identity/otp/verify", map[string]any{"phone": "+919876543210", "code": code})
	var auth AuthResponse
	gctest.DecodeData(t, rec, &auth)
	rec = as(t, app, auth.Token, http.MethodPatch, "/x/identity/me", map[string]any{"phone": "+911111111111"})
	var me Customer
	gctest.DecodeData(t, rec, &me)
	if me.PhoneVerified {
		t.Fatalf("after typing another phone = %+v, want it unverified", me)
	}
}

// Phone sign-in is free for Indian numbers; any other country needs the Pro
// plan (D118).
func TestPhoneCodesOutsideIndiaNeedPro(t *testing.T) {
	app, mod, sms := phoneApp(t)
	rec := gctest.Request(t, app, http.MethodPost, "/x/identity/otp", map[string]any{"phone": "+44 7700 900123"})
	if rec.Code != http.StatusForbidden || errorCode(t, rec) != "plan_required" {
		t.Fatalf("a UK phone on the free plan = %d %s, want 403 plan_required", rec.Code, rec.Body)
	}
	phoneCode(t, app, mod, sms, "+919876543210")

	if err := app.Stores().SetPlan(context.Background(), app.DefaultStore(), gocommerce.PlanPro); err != nil {
		t.Fatal(err)
	}
	phoneCode(t, app, mod, sms, "+44 7700 900123")
}
