package identity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// Phone first, as Myntra signs in (D123): a store that allows both ways asks
// a shopper whose phone is new whether they already have an account, so a
// customer who signed up by email is not given a second, empty one.

type newPhone struct {
	NewPhone   bool   `json:"new_phone"`
	PhoneToken string `json:"phone_token"`
	Token      string `json:"token"`
}

func eitherApp(t *testing.T) (*gocommerce.App, *Module, *capture) {
	t.Helper()
	sms := &capture{}
	mod := New(Config{Notifier: sms})
	app := gctest.New(t, mod)
	signInBy(t, app, LoginEither)
	return app, mod, sms
}

// askIfNew verifies phone's code asking first, and returns the proof the
// engine hands back for a phone with no account.
func askIfNew(t *testing.T, app *gocommerce.App, mod *Module, sms *capture, phone string) newPhone {
	t.Helper()
	code := phoneCode(t, app, mod, sms, phone)
	rec := gctest.Request(t, app, http.MethodPost, "/x/identity/otp/verify",
		map[string]any{"phone": phone, "code": code, "ask_if_new": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("verify = %d %s", rec.Code, rec.Body)
	}
	var out newPhone
	gctest.DecodeData(t, rec, &out)
	return out
}

func accounts(t *testing.T, mod *Module) int {
	t.Helper()
	var n int
	if err := mod.db.QueryRowContext(context.Background(), `SELECT count(*) FROM identity_customers`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestANewPhoneJoinsTheShoppersEmailAccount(t *testing.T) {
	app, mod, sms := eitherApp(t)
	ada := register(t, app, "ada@example.com")

	got := askIfNew(t, app, mod, sms, "+919876543210")
	if !got.NewPhone || got.PhoneToken == "" || got.Token != "" {
		t.Fatalf("verify = %+v, want a new phone, its proof and no session", got)
	}
	if n := accounts(t, mod); n != 1 {
		t.Fatalf("%d accounts after the code, want only Ada's", n)
	}

	// A wrong password attaches nothing and leaves the proof to try again.
	if rec := gctest.Request(t, app, http.MethodPost, "/x/identity/login", map[string]any{
		"email": "ada@example.com", "password": "wrong-password", "phone": "+919876543210", "phone_token": got.PhoneToken,
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong password = %d %s", rec.Code, rec.Body)
	}
	rec := gctest.Request(t, app, http.MethodPost, "/x/identity/login", map[string]any{
		"email": "ada@example.com", "password": "correct horse", "phone": "+919876543210", "phone_token": got.PhoneToken,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("login with the phone = %d %s", rec.Code, rec.Body)
	}
	var in AuthResponse
	gctest.DecodeData(t, rec, &in)
	if in.Record == nil || in.Record.ID != ada.Record.ID || in.Record.Phone != "+919876543210" || !in.Record.PhoneVerified {
		t.Fatalf("signed in as %+v, want Ada with the phone verified", in.Record)
	}

	// From now on the phone opens Ada's account, with no question.
	if _, err := mod.db.ExecContext(context.Background(), `UPDATE identity_login_codes SET created_at = now() - interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	again := askIfNew(t, app, mod, sms, "+919876543210")
	if again.NewPhone || again.Token == "" {
		t.Fatalf("second phone sign-in = %+v, want a session", again)
	}
	if n := accounts(t, mod); n != 1 {
		t.Fatalf("%d accounts, want one", n)
	}
}

func TestANewPhoneOpensItsOwnAccountWhenTheShopperHasNone(t *testing.T) {
	app, mod, sms := eitherApp(t)
	got := askIfNew(t, app, mod, sms, "+919876543210")

	open := func() *httptest.ResponseRecorder {
		return gctest.Request(t, app, http.MethodPost, "/x/identity/otp/new-account",
			map[string]any{"phone": "+919876543210", "phone_token": got.PhoneToken})
	}
	rec := open()
	if rec.Code != http.StatusOK {
		t.Fatalf("new account = %d %s", rec.Code, rec.Body)
	}
	var in AuthResponse
	gctest.DecodeData(t, rec, &in)
	if in.Token == "" || in.Record == nil || in.Record.Phone != "+919876543210" || in.Record.Email != "" {
		t.Fatalf("signed in as %+v, want a new account on the phone", in.Record)
	}
	if rec := open(); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("the proof again = %d, want it spent", rec.Code)
	}
}

// The proof is the phone's: it opens nothing for another number.
func TestAPhoneProofIsOnlyItsPhones(t *testing.T) {
	app, mod, sms := eitherApp(t)
	register(t, app, "ada@example.com")
	got := askIfNew(t, app, mod, sms, "+919876543210")

	if rec := gctest.Request(t, app, http.MethodPost, "/x/identity/otp/new-account",
		map[string]any{"phone": "+919800000000", "phone_token": got.PhoneToken}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("another phone's proof = %d", rec.Code)
	}
	if rec := gctest.Request(t, app, http.MethodPost, "/x/identity/login", map[string]any{
		"email": "ada@example.com", "password": "correct horse", "phone": "+919800000000", "phone_token": got.PhoneToken,
	}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("login with another phone's proof = %d", rec.Code)
	}
}

// A store that signs in only by phone has no email to ask about: a new phone
// opens its account at once, as it always has.
func TestAPhoneOnlyStoreDoesNotAsk(t *testing.T) {
	app, mod, sms := phoneApp(t)
	got := askIfNew(t, app, mod, sms, "+919876543210")
	if got.NewPhone || got.Token == "" {
		t.Fatalf("verify = %+v, want a session", got)
	}
}
