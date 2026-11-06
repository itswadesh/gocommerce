package gocommerce

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Somebody who signed up with a phone has no password, and must still be able to
// change how they sign in, or a lost SIM locks them out (D195).

type noPasswordPerson struct {
	app   *App
	sms   *smsStub
	mail  *recordingNotifier
	token func(*http.Request)
	id    int64
}

func phonePerson(t *testing.T) *noPasswordPerson {
	t.Helper()
	sms, mail := &smsStub{}, &recordingNotifier{}
	app := newApp(t, signupConfig(t), &notifyModule{rec: mail}, sms)
	newSuperuser(t, app, "operator@example.com", "the operator's password")
	mustAsk(t, app, unknownPhone)
	var out authResponse
	decodeData(t, sendPhoneCode(t, app, unknownPhone, sms.lastCode(t, app, unknownPhone)), &out)
	token := bearer(out.Token)
	// The account screen is inside a store, so they have opened one.
	if rec := doBody(t, app, http.MethodPost, "/api/stores/create", `{"name":"Asha Jewels"}`, token); rec.Code != http.StatusCreated {
		t.Fatalf("open a store = %d: %s", rec.Code, rec.Body)
	}
	var mine []MyStore
	decodeData(t, do(t, app, http.MethodGet, "/api/stores/my", token), &mine)
	atStore := header("X-Store", mine[0].ID)
	inStore := func(r *http.Request) {
		token(r)
		atStore(r)
	}
	return &noPasswordPerson{app, sms, mail, inStore, out.Record.ID}
}

func (p *noPasswordPerson) put(t *testing.T, path string, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, p.app, http.MethodPut, path, p.token, jsonBody(t, body))
}

// emailed is the code last emailed to address.
func (p *noPasswordPerson) emailed(t *testing.T, address string) string {
	t.Helper()
	p.app.superusers.waitForResetDelivery(5 * time.Second)
	p.mail.mu.Lock()
	defer p.mail.mu.Unlock()
	for i := len(p.mail.sent) - 1; i >= 0; i-- {
		if n := p.mail.sent[i]; n.Event == EventLoginCode && n.To == address {
			if n.Channel != ChannelEmail {
				t.Fatalf("sent %+v, want an email", n)
			}
			return n.Data["code"]
		}
	}
	t.Fatalf("no code was emailed to %s", address)
	return ""
}

func (p *noPasswordPerson) askEmail(t *testing.T, address string) {
	t.Helper()
	if rec := do(t, p.app, http.MethodPost, "/api/admin/me/email/code", p.token, jsonBody(t, map[string]string{"email": address})); rec.Code != http.StatusAccepted {
		t.Fatalf("asking for a code = %d: %s", rec.Code, rec.Body)
	}
}

func TestAPhonePersonAddsAnEmailAndGetsBackInWithoutTheirPhone(t *testing.T) {
	p := phonePerson(t)
	const email = "asha@example.com"
	p.askEmail(t, email)
	code := p.emailed(t, email)

	assertWrongCode(t, p.put(t, "/api/admin/me/email", map[string]string{"email": email, "code": otherCode(code)}))
	assertWrongCode(t, p.put(t, "/api/admin/me/email", map[string]string{"email": "other@example.com", "code": code}))
	if rec := p.put(t, "/api/admin/me/email", map[string]string{"email": email, "code": code}); rec.Code != http.StatusOK {
		t.Fatalf("adding = %d: %s", rec.Code, rec.Body)
	}
	if rec := do(t, p.app, http.MethodGet, "/api/admin/me", p.token); rec.Code != http.StatusOK {
		t.Fatalf("their session after adding = %d", rec.Code)
	}

	// The phone is gone. The email and a reset are the way back.
	if rec := do(t, p.app, http.MethodPost, "/api/admin/password-reset", jsonBody(t, map[string]string{"identity": email})); rec.Code != http.StatusAccepted {
		t.Fatalf("reset = %d: %s", rec.Code, rec.Body)
	}
	notes := resetNotes(t, p.app, p.mail)
	if len(notes) != 1 || notes[0].To != email {
		t.Fatalf("reset emails = %+v", notes)
	}
	if rec := do(t, p.app, http.MethodPost, "/api/admin/password-reset/confirm", jsonBody(t, map[string]string{
		"identity": email, "code": notes[0].Data["reset_code"], "password": "a brand new password"})); rec.Code != http.StatusOK {
		t.Fatalf("confirm = %d: %s", rec.Code, rec.Body)
	}
	in := do(t, p.app, http.MethodPost, "/api/admin/auth-with-password", jsonBody(t, map[string]string{"identity": email, "password": "a brand new password"}))
	if in.Code != http.StatusOK {
		t.Fatalf("signing in by email = %d: %s", in.Code, in.Body)
	}
	var out authResponse
	decodeData(t, in, &out)
	if out.Record.ID != p.id {
		t.Fatalf("signed in as %d, want %d", out.Record.ID, p.id)
	}
}

func TestAPhonePersonChangesTheirNumberWithItsCode(t *testing.T) {
	p := phonePerson(t)
	if rec := do(t, p.app, http.MethodPost, "/api/admin/me/phone/code", p.token, jsonBody(t, map[string]string{"phone": knownPhone})); rec.Code != http.StatusAccepted {
		t.Fatalf("asking = %d: %s", rec.Code, rec.Body)
	}
	code := p.sms.lastCode(t, p.app, knownPhone)
	assertWrongCode(t, p.put(t, "/api/admin/me/phone", map[string]string{"phone": knownPhone, "code": otherCode(code)}))
	if rec := p.put(t, "/api/admin/me/phone", map[string]string{"phone": knownPhone, "code": code}); rec.Code != http.StatusOK {
		t.Fatalf("changing = %d: %s", rec.Code, rec.Body)
	}

	nextMinute(t, p.app)
	mustAsk(t, p.app, knownPhone)
	var out authResponse
	decodeData(t, sendPhoneCode(t, p.app, knownPhone, p.sms.lastCode(t, p.app, knownPhone)), &out)
	if out.Record.ID != p.id {
		t.Fatalf("the new number opened %d, want %d", out.Record.ID, p.id)
	}
}

func TestAPhonePersonCannotTakeAnEmailInUse(t *testing.T) {
	p := phonePerson(t)
	const taken = "operator@example.com"
	p.askEmail(t, taken)
	rec := p.put(t, "/api/admin/me/email", map[string]string{"email": taken, "code": p.emailed(t, taken)})
	if rec.Code != http.StatusConflict {
		t.Fatalf("taking an address = %d: %s", rec.Code, rec.Body)
	}
}

func TestAPhonePersonWithAnEmailCanDropThePhoneButNotTheirOnlyAddress(t *testing.T) {
	p := phonePerson(t)
	del := func() *httptest.ResponseRecorder {
		return do(t, p.app, http.MethodDelete, "/api/admin/me/phone", p.token, jsonBody(t, map[string]string{}))
	}
	if rec := del(); rec.Code != http.StatusConflict {
		t.Fatalf("removing their only address = %d: %s", rec.Code, rec.Body)
	}
	const email = "asha@example.com"
	p.askEmail(t, email)
	if rec := p.put(t, "/api/admin/me/email", map[string]string{"email": email, "code": p.emailed(t, email)}); rec.Code != http.StatusOK {
		t.Fatalf("adding = %d: %s", rec.Code, rec.Body)
	}
	if rec := del(); rec.Code != http.StatusOK {
		t.Fatalf("removing the phone = %d: %s", rec.Code, rec.Body)
	}
}

// The password is only waived for someone who has none.
func TestSomebodyWithAPasswordMustStillGiveIt(t *testing.T) {
	sms, mail := &smsStub{}, &recordingNotifier{}
	app := newApp(t, signupConfig(t), &notifyModule{rec: mail}, sms)
	const pass = "a-long-enough-password"
	newSuperuser(t, app, "a@example.com", pass)
	token := bearer(signIn(t, app, "a@example.com", pass, "198.51.100.1").Token)
	p := &noPasswordPerson{app: app, mail: mail, token: token}

	p.askEmail(t, "new@example.com")
	code := p.emailed(t, "new@example.com")
	for _, body := range []map[string]string{
		{"email": "new@example.com", "code": code},
		{"email": "new@example.com", "code": code, "current_password": "not the password"},
	} {
		if rec := p.put(t, "/api/admin/me/email", body); rec.Code != http.StatusForbidden {
			t.Fatalf("%v = %d: %s", body, rec.Code, rec.Body)
		}
	}
	// Refused before the code was spent, so it still works with the password.
	if rec := p.put(t, "/api/admin/me/email", map[string]string{"email": "new@example.com", "code": code, "current_password": pass}); rec.Code != http.StatusOK {
		t.Fatalf("with the password = %d: %s", rec.Code, rec.Body)
	}
	if rec := do(t, app, http.MethodPut, "/api/admin/me/phone", token, jsonBody(t, map[string]string{"phone": knownPhone, "code": "123456"})); rec.Code != http.StatusForbidden {
		t.Fatalf("a phone with no password given = %d: %s", rec.Code, rec.Body)
	}
}

func TestAnEmailCodeCannotBeAskedForAnotherPersonsSignIn(t *testing.T) {
	p := phonePerson(t)
	// No token, no route.
	if rec := do(t, p.app, http.MethodPost, "/api/admin/me/email/code", jsonBody(t, map[string]string{"email": "x@example.com"})); rec.Code == http.StatusAccepted {
		t.Fatalf("anonymous asking = %d", rec.Code)
	}
	if rec := do(t, p.app, http.MethodPut, "/api/admin/me/email", jsonBody(t, map[string]string{"email": "x@example.com", "code": "123456"})); rec.Code == http.StatusOK {
		t.Fatalf("anonymous setting = %d", rec.Code)
	}
	// Limits are the phone's: a second code inside the minute is refused.
	p.askEmail(t, "limited@example.com")
	if rec := do(t, p.app, http.MethodPost, "/api/admin/me/email/code", p.token, jsonBody(t, map[string]string{"email": "limited@example.com"})); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a second code = %d", rec.Code)
	}
}
