package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// Somebody may sign up with a phone instead of an email (D191). It is the same
// account from then on: signing in, being told the number is taken, getting back
// in after forgetting the password, and opening a store.

const testPhone = "+919876543210"

type smsModule struct{ rec *recordingNotifier }

func (m *smsModule) Name() string            { return "sms-recorder" }
func (m *smsModule) Migrations() []Migration { return nil }
func (m *smsModule) Register(app *App) error {
	app.RegisterNotifier(ChannelSMS, m.rec)
	return nil
}

func phoneSignupApp(t *testing.T, mods ...Module) *App {
	t.Helper()
	app := newApp(t, signupConfig(t), append([]Module{&notifyModule{rec: &recordingNotifier{}}}, mods...)...)
	newSuperuser(t, app, "operator@example.com", "the operator's password")
	return app
}

func post(t *testing.T, app *App, path string, body map[string]string) (int, string, authResponse) {
	t.Helper()
	rec := do(t, app, http.MethodPost, path, jsonBody(t, body))
	var out authResponse
	if rec.Code < 300 {
		decodeData(t, rec, &out)
	}
	var failed struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &failed)
	return rec.Code, failed.Error.Code, out
}

func TestSigningUpWithAPhoneIsAnAccountYouCanSignInTo(t *testing.T) {
	app := phoneSignupApp(t)

	code, _, made := post(t, app, "/api/admin/signup", map[string]string{"identity": "+91 98765 43210", "password": "a good password"})
	if code != http.StatusCreated {
		t.Fatalf("sign up = %d", code)
	}
	if made.Record.Phone != testPhone || made.Record.Email != "" {
		t.Fatalf("record = %+v, want the phone and no email", made.Record)
	}
	if made.Token == "" {
		t.Fatal("signing up did not sign them in")
	}

	if code, _, in := post(t, app, "/api/admin/auth-with-password", map[string]string{"identity": "+91 98765-43210", "password": "a good password"}); code != http.StatusOK || in.Record.ID != made.Record.ID {
		t.Fatalf("sign in by phone = %d, %+v", code, in.Record)
	}
	if code, _, _ := post(t, app, "/api/admin/auth-with-password", map[string]string{"identity": testPhone, "password": "the wrong password"}); code != http.StatusBadRequest {
		t.Fatalf("sign in with the wrong password = %d", code)
	}
	if code, errCode, _ := post(t, app, "/api/admin/signup", map[string]string{"identity": testPhone, "password": "another password"}); code != http.StatusConflict || errCode != "email_taken" {
		t.Fatalf("a taken number = %d %q", code, errCode)
	}
}

func TestSigningUpStillTakesAnEmailUnderEitherName(t *testing.T) {
	app := phoneSignupApp(t)
	for name, email := range map[string]string{"identity": "one@example.com", "email": "two@example.com"} {
		code, _, made := post(t, app, "/api/admin/signup", map[string]string{name: email, "password": "a good password"})
		if code != http.StatusCreated || made.Record.Email != email || made.Record.Phone != "" {
			t.Fatalf("%s: sign up = %d, %+v", name, code, made.Record)
		}
	}
}

func TestSigningUpRefusesWhatIsNeitherAnEmailNorAPhone(t *testing.T) {
	app := phoneSignupApp(t)
	for _, identity := range []string{"", "not-an-email", "9876543210", "+91 123"} {
		if code, _, _ := post(t, app, "/api/admin/signup", map[string]string{"identity": identity, "password": "a good password"}); code != http.StatusBadRequest {
			t.Errorf("%q = %d, want 400", identity, code)
		}
	}
	if code, _, _ := post(t, app, "/api/admin/signup", map[string]string{"identity": testPhone, "password": "short"}); code != http.StatusBadRequest {
		t.Errorf("a short password = %d, want 400", code)
	}
}

func TestAPhoneAccountGetsBackInByACodeInAText(t *testing.T) {
	sms := &recordingNotifier{}
	app := phoneSignupApp(t, &smsModule{rec: sms})
	post(t, app, "/api/admin/signup", map[string]string{"identity": testPhone, "password": "a good password"})

	rec := do(t, app, http.MethodPost, "/api/admin/password-reset", jsonBody(t, map[string]string{"identity": testPhone}))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("reset = %d %s", rec.Code, rec.Body)
	}
	var asked ResetRequest
	decodeData(t, rec, &asked)
	if asked.Delivery != DeliverySMS {
		t.Fatalf("delivery = %q, want sms", asked.Delivery)
	}
	notes := resetNotes(t, app, sms)
	if len(notes) != 1 || notes[0].Channel != ChannelSMS || notes[0].To != testPhone || notes[0].Data["code"] == "" {
		t.Fatalf("texts = %+v", notes)
	}

	if code, _, _ := post(t, app, "/api/admin/password-reset/confirm", map[string]string{"identity": testPhone, "code": notes[0].Data["code"], "password": "a new good password"}); code != http.StatusOK {
		t.Fatalf("confirm = %d", code)
	}
	if code, _, _ := post(t, app, "/api/admin/auth-with-password", map[string]string{"identity": testPhone, "password": "a new good password"}); code != http.StatusOK {
		t.Fatalf("sign in with the new password = %d", code)
	}
}

// A stranger learns nothing, and an unknown number with no SMS provider gets the
// same answer as a known one.
func TestAPhoneResetSaysNoneWhereNoTextCanBeSent(t *testing.T) {
	app := phoneSignupApp(t)
	post(t, app, "/api/admin/signup", map[string]string{"identity": testPhone, "password": "a good password"})
	for _, phone := range []string{testPhone, "+919876500000"} {
		rec := do(t, app, http.MethodPost, "/api/admin/password-reset", jsonBody(t, map[string]string{"identity": phone}))
		var asked ResetRequest
		decodeData(t, rec, &asked)
		if rec.Code != http.StatusAccepted || asked.Delivery != DeliveryNone {
			t.Fatalf("%s: %d delivery %q", phone, rec.Code, asked.Delivery)
		}
	}
}

// Every read of a person has to survive there being no email: the row that
// reaches the most code is the one opening a store, and its announcement.
func TestAPhoneAccountOpensAStoreAndIsNamedInItsAnnouncement(t *testing.T) {
	app := phoneSignupApp(t)
	_, _, made := post(t, app, "/api/admin/signup", map[string]string{"identity": testPhone, "password": "a good password"})

	rec := doBody(t, app, http.MethodPost, "/api/stores/create", `{"name":"Corner Shop","slug":"corner"}`, bearer(made.Token))
	if rec.Code != http.StatusCreated {
		t.Fatalf("open a store = %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Store  Store     `json:"store"`
		Record Superuser `json:"record"`
	}
	decodeData(t, rec, &out)
	if out.Record.Phone != testPhone || out.Record.Role != RoleOwner {
		t.Fatalf("record = %+v", out.Record)
	}
	var owner string
	if err := app.DB().QueryRowContext(WithStore(context.Background(), out.Store.ID),
		`SELECT payload->>'owner_phone' FROM outbox_events WHERE event_name = $1`, EventStoreCreated).Scan(&owner); err != nil {
		t.Fatalf("read the event: %v", err)
	}
	if owner != testPhone {
		t.Fatalf("owner_phone = %q", owner)
	}

	// The team screen and the audit trail read the same person.
	team := do(t, app, http.MethodGet, "/api/admin/superusers", bearer(made.Token), func(r *http.Request) { r.Header.Set("x-litekart-store", out.Store.ID) })
	if team.Code != http.StatusOK {
		t.Fatalf("team = %d: %s", team.Code, team.Body)
	}
}
