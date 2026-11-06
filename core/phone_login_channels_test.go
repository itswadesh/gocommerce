package gocommerce

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// What a code or a reset may travel on, and what proving an address ends (D211).

const (
	theirPhone = "+919876511111"
	mine       = "+919876522222"
)

func openInstall(t *testing.T) (*App, *smsStub, *recordingNotifier) {
	t.Helper()
	sms := &smsStub{}
	rec := &recordingNotifier{}
	app := newApp(t, signupConfig(t), &notifyModule{rec: rec}, sms)
	newSuperuser(t, app, "operator@example.com", "the operator's password")
	return app, sms, rec
}

func signedUp(t *testing.T, app *App, identity, pass string) authResponse {
	t.Helper()
	r := do(t, app, http.MethodPost, "/api/admin/signup", jsonBody(t, map[string]string{"identity": identity, "password": pass}))
	if r.Code != http.StatusCreated {
		t.Fatalf("signup = %d: %s", r.Code, r.Body)
	}
	var out authResponse
	decodeData(t, r, &out)
	return out
}

func openStore(t *testing.T, app *App, token string) string {
	t.Helper()
	if r := doBody(t, app, http.MethodPost, "/api/stores/create", `{"name":"Their shop"}`, bearer(token)); r.Code != http.StatusCreated {
		t.Fatalf("create store = %d: %s", r.Code, r.Body)
	}
	var stores []MyStore
	decodeData(t, do(t, app, http.MethodGet, "/api/stores/my", bearer(token)), &stores)
	return stores[len(stores)-1].ID
}

func (m *smsStub) lastUnder() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.under[len(m.under)-1]
}

func (m *smsStub) lastTo(event, to string) string {
	for _, n := range m.notes() {
		if n.Event == event && n.To == to {
			return n.Data["code"]
		}
	}
	return ""
}

func emailedResetCode(t *testing.T, app *App, rec *recordingNotifier, to string) string {
	t.Helper()
	app.superusers.waitForResetDelivery(5 * time.Second)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for i := len(rec.sent) - 1; i >= 0; i-- {
		if rec.sent[i].To == to && rec.sent[i].Event == EventSuperuserPasswordReset {
			return rec.sent[i].Data["reset_code"]
		}
	}
	t.Fatalf("no reset mailed to %s", to)
	return ""
}

func askReset(t *testing.T, app *App, identity string) {
	t.Helper()
	if r := do(t, app, http.MethodPost, "/api/admin/password-reset", jsonBody(t, map[string]string{"identity": identity})); r.Code != http.StatusAccepted {
		t.Fatalf("reset = %d: %s", r.Code, r.Body)
	}
}

func confirmReset(t *testing.T, app *App, identity, code, password string) (int, authResponse) {
	t.Helper()
	r := do(t, app, http.MethodPost, "/api/admin/password-reset/confirm",
		jsonBody(t, map[string]string{"identity": identity, "code": code, "password": password}))
	var out authResponse
	if r.Code == http.StatusOK {
		decodeData(t, r, &out)
	}
	return r.Code, out
}

func personState(t *testing.T, app *App, id int64) (email, phone string, proved bool) {
	t.Helper()
	if err := app.db.QueryRowContext(context.Background(), `
		SELECT coalesce(email, ''), coalesce(phone, ''), phone_proved_at IS NOT NULL FROM superusers WHERE id = $1`, id).
		Scan(&email, &phone, &proved); err != nil {
		t.Fatal(err)
	}
	return
}

func TestACodeAskedForFromInsideAnAccountGoesOnTheInstallsChannel(t *testing.T) {
	app, sms, _ := openInstall(t)
	in := signedUp(t, app, "someone@example.com", "a-long-enough-password")
	st := openStore(t, app, in.Token)
	r := do(t, app, http.MethodPost, "/api/admin/me/phone/code", bearer(in.Token), header("X-Store", st),
		jsonBody(t, map[string]string{"phone": theirPhone}))
	if r.Code != http.StatusAccepted {
		t.Fatalf("ask = %d: %s", r.Code, r.Body)
	}
	app.superusers.waitForResetDelivery(5 * time.Second)
	if got := sms.lastUnder(); got != installScope {
		t.Fatalf("the code left under %q, want the install's channel", got)
	}
}

func TestACodeAskedForFromInsideAnAccountIsRefusedWhereTheInstallCannotCarryIt(t *testing.T) {
	sms := &smsStub{}
	sms.ready = func(ctx context.Context) bool { st, _ := StoreFrom(ctx); return st != installScope }
	app := newApp(t, signupConfig(t), &notifyModule{rec: &recordingNotifier{}}, sms)
	newSuperuser(t, app, "operator@example.com", "the operator's password")
	in := signedUp(t, app, "someone@example.com", "a-long-enough-password")
	st := openStore(t, app, in.Token)
	r := do(t, app, http.MethodPost, "/api/admin/me/phone/code", bearer(in.Token), header("X-Store", st),
		jsonBody(t, map[string]string{"phone": theirPhone}))
	if r.Code != http.StatusConflict {
		t.Fatalf("ask = %d, want 409: %s", r.Code, r.Body)
	}
	if n := len(sms.notes()); n != 0 {
		t.Fatalf("%d texts went out on the store's channel", n)
	}
}

// A code proves an address to the person who named it, and that person may be
// anybody. It must not sign in to the address.
func TestACodeForAddingANumberDoesNotSignInToIt(t *testing.T) {
	app, sms, _ := openInstall(t)
	holder := newSuperuser(t, app, "holder@example.com", "a-long-enough-password")
	withPhone(t, app, holder, theirPhone)
	asker := signedUp(t, app, "someone@example.com", "a-long-enough-password")
	st := openStore(t, app, asker.Token)
	do(t, app, http.MethodPost, "/api/admin/me/phone/code", bearer(asker.Token), header("X-Store", st),
		jsonBody(t, map[string]string{"phone": theirPhone}))
	assertWrongCode(t, sendPhoneCode(t, app, theirPhone, sms.lastCode(t, app, theirPhone)))

	// And the other way: a sign-in code adds nothing to an account.
	nextMinute(t, app)
	mustAsk(t, app, mine)
	r := do(t, app, http.MethodPut, "/api/admin/me/phone", bearer(asker.Token), header("X-Store", st),
		jsonBody(t, map[string]string{"phone": mine, "code": sms.lastCode(t, app, mine), "current_password": "a-long-enough-password"}))
	assertWrongCode(t, r)
}

// Sign-in codes and resets for a person of one store go on the install's channel,
// the store's provider settings being its staff's to read.
func TestSignInAndResetCodesNeverLeaveOnAStoresChannelWhereStoresAreOthers(t *testing.T) {
	sms := &smsStub{}
	rec := &recordingNotifier{}
	app := newApp(t, testConfig(requireDB(t)), &notifyModule{rec: rec}, sms)
	second := addTestStore(t, app, "second")
	onlyThere, err := app.Superusers().Create(WithStore(context.Background(), second), "only@example.com", "a-long-enough-password", RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	withPhone(t, app, onlyThere, theirPhone)

	mustAsk(t, app, theirPhone, header("X-Store", second))
	sms.lastCode(t, app, theirPhone)
	if got := sms.lastUnder(); got != installScope {
		t.Errorf("sign-in code left under %q", got)
	}
	nextMinute(t, app)
	do(t, app, http.MethodPost, "/api/admin/password-reset", header("X-Store", second), jsonBody(t, map[string]string{"identity": theirPhone}))
	app.superusers.waitForResetDelivery(5 * time.Second)
	if sms.lastTo(EventSuperuserPasswordReset, theirPhone) == "" {
		t.Fatal("no reset was texted")
	}
	if got := sms.lastUnder(); got != installScope {
		t.Errorf("texted reset left under %q", got)
	}
}

// One store and no sign-ups: nobody else is hosted here, and the panel's provider
// is the only one a self-hosted shop has.
func TestASingleStoreInstallUsesTheProviderSetInItsPanel(t *testing.T) {
	sms := &smsStub{}
	sms.ready = func(ctx context.Context) bool { st, _ := StoreFrom(ctx); return st != installScope }
	app := newApp(t, testConfig(requireDB(t)), &notifyModule{rec: &recordingNotifier{}}, sms)
	su := newSuperuser(t, app, "owner@example.com", "a-long-enough-password")
	withPhone(t, app, su, theirPhone)
	if !phoneState(t, app)["phone_login"] {
		t.Fatal("a phone is not offered")
	}
	mustAsk(t, app, theirPhone)
	sms.lastCode(t, app, theirPhone)
	if got := sms.lastUnder(); got != app.DefaultStore() {
		t.Fatalf("code left under %q, want the one store %q", got, app.DefaultStore())
	}

	// The moment a second store opens, the panel's provider is one store's.
	addTestStore(t, app, "second")
	if phoneState(t, app)["phone_login"] {
		t.Fatal("a phone is still offered with no channel of the install's own")
	}
}

// Open sign-ups mean strangers, even with one store so far: it could be theirs.
func TestAnOpenInstallWithOneStoreStillUsesItsOwnChannel(t *testing.T) {
	sms := &smsStub{}
	app := newApp(t, signupConfig(t), &notifyModule{rec: &recordingNotifier{}}, sms)
	su := newSuperuser(t, app, "owner@example.com", "a-long-enough-password")
	withPhone(t, app, su, theirPhone)
	mustAsk(t, app, theirPhone)
	sms.lastCode(t, app, theirPhone)
	if got := sms.lastUnder(); got != installScope {
		t.Fatalf("code left under %q", got)
	}
}

// Whoever typed a number at sign-up must not hold an email that resets the account
// after its real owner has proved the number.
func TestTheFirstCodeEndsAnEmailPutOnTheAccount(t *testing.T) {
	app, sms, rec := openInstall(t)
	const pass = "a-long-enough-password"
	typed := signedUp(t, app, theirPhone, pass)
	st := openStore(t, app, typed.Token)
	if r := do(t, app, http.MethodPatch, "/api/admin/me", bearer(typed.Token), header("X-Store", st),
		jsonBody(t, map[string]string{"current_password": pass, "email": "typed@example.com"})); r.Code != http.StatusOK {
		t.Fatalf("add email = %d: %s", r.Code, r.Body)
	}
	mustAsk(t, app, theirPhone)
	var owner authResponse
	decodeData(t, sendPhoneCode(t, app, theirPhone, sms.lastCode(t, app, theirPhone)), &owner)
	if owner.Record == nil || owner.Record.Email != "" {
		t.Fatalf("the code answered %+v, want no email", owner.Record)
	}
	if email, _, _ := personState(t, app, typed.Record.ID); email != "" {
		t.Fatalf("email = %q after the first code", email)
	}
	askReset(t, app, "typed@example.com")
	app.superusers.waitForResetDelivery(5 * time.Second)
	if n := rec.count(EventSuperuserPasswordReset); n != 0 {
		t.Fatalf("%d resets mailed to the old address", n)
	}
}

// Whoever typed an email at sign-up must not keep a phone on the account after its
// real owner has come back through a reset.
func TestAResetByEmailEndsThePhoneOnTheAccount(t *testing.T) {
	app, sms, rec := openInstall(t)
	const pass = "a-long-enough-password"
	const addr = "owner@example.com"
	typed := signedUp(t, app, addr, pass)
	st := openStore(t, app, typed.Token)
	do(t, app, http.MethodPost, "/api/admin/me/phone/code", bearer(typed.Token), header("X-Store", st), jsonBody(t, map[string]string{"phone": mine}))
	if r := do(t, app, http.MethodPut, "/api/admin/me/phone", bearer(typed.Token), header("X-Store", st),
		jsonBody(t, map[string]string{"phone": mine, "code": sms.lastCode(t, app, mine), "current_password": pass})); r.Code != http.StatusOK {
		t.Fatalf("add phone = %d: %s", r.Code, r.Body)
	}
	askReset(t, app, addr)
	if code, _ := confirmReset(t, app, addr, emailedResetCode(t, app, rec, addr), "the real owner's password"); code != http.StatusOK {
		t.Fatalf("confirm = %d", code)
	}
	if _, phone, proved := personState(t, app, typed.Record.ID); phone != "" || proved {
		t.Fatalf("phone = %q proved = %v after a reset by email", phone, proved)
	}
	nextMinute(t, app)
	mustAsk(t, app, mine)
	var out authResponse
	r := sendPhoneCode(t, app, mine, sms.lastCode(t, app, mine))
	if r.Code == http.StatusOK || r.Code == http.StatusCreated {
		decodeData(t, r, &out)
		if out.Record.ID == typed.Record.ID {
			t.Fatal("the phone still opens the account")
		}
	}
}

func TestAResetByTextProvesTheNumberAndEndsAnEmailFromBeforeThat(t *testing.T) {
	app, sms, _ := openInstall(t)
	const pass = "a-long-enough-password"
	typed := signedUp(t, app, theirPhone, pass)
	st := openStore(t, app, typed.Token)
	do(t, app, http.MethodPatch, "/api/admin/me", bearer(typed.Token), header("X-Store", st),
		jsonBody(t, map[string]string{"current_password": pass, "email": "typed@example.com"}))
	askReset(t, app, theirPhone)
	app.superusers.waitForResetDelivery(5 * time.Second)
	const chosen = "the password just chosen"
	code, after := confirmReset(t, app, theirPhone, sms.lastTo(EventSuperuserPasswordReset, theirPhone), chosen)
	if code != http.StatusOK {
		t.Fatalf("confirm = %d", code)
	}
	email, _, proved := personState(t, app, typed.Record.ID)
	if email != "" || !proved {
		t.Fatalf("email = %q proved = %v after a reset by text", email, proved)
	}

	// So the next code is not a first code: nothing just chosen is ended.
	mustAsk(t, app, theirPhone)
	if r := sendPhoneCode(t, app, theirPhone, sms.lastCode(t, app, theirPhone)); r.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", r.Code, r.Body)
	}
	if _, _, err := app.superusers.Authenticate(context.Background(), theirPhone, chosen, "198.51.100.5"); err != nil {
		t.Errorf("the chosen password stopped working: %v", err)
	}
	if got := do(t, app, http.MethodGet, "/api/stores/my", bearer(after.Token)); got.Code != http.StatusOK {
		t.Errorf("the reset's session = %d", got.Code)
	}
}

// A person with a proved number and an email who resets by text keeps the email.
func TestAResetByTextOnAProvedNumberKeepsTheEmail(t *testing.T) {
	app, sms, _ := openInstall(t)
	su := newSuperuser(t, app, "both@example.com", "a-long-enough-password")
	withPhone(t, app, su, theirPhone)
	askReset(t, app, theirPhone)
	app.superusers.waitForResetDelivery(5 * time.Second)
	if code, _ := confirmReset(t, app, theirPhone, sms.lastTo(EventSuperuserPasswordReset, theirPhone), "the password just chosen"); code != http.StatusOK {
		t.Fatalf("confirm = %d", code)
	}
	if email, phone, _ := personState(t, app, su.ID); email != "both@example.com" || phone != theirPhone {
		t.Fatalf("email = %q phone = %q", email, phone)
	}
}

// A code mailed to the account's email must not count as a text to its number, or
// it would prove a number nobody had a message on.
func TestAnEmailedResetCodeCannotBeSpentNamingThePhone(t *testing.T) {
	app, _, rec := openInstall(t)
	const pass = "a-long-enough-password"
	typed := signedUp(t, app, theirPhone, pass)
	st := openStore(t, app, typed.Token)
	do(t, app, http.MethodPatch, "/api/admin/me", bearer(typed.Token), header("X-Store", st),
		jsonBody(t, map[string]string{"current_password": pass, "email": "typed@example.com"}))
	askReset(t, app, "typed@example.com")
	code, _ := confirmReset(t, app, theirPhone, emailedResetCode(t, app, rec, "typed@example.com"), "the password just chosen")
	if code == http.StatusOK {
		t.Fatal("an emailed code was spent naming the phone")
	}
	if _, _, proved := personState(t, app, typed.Record.ID); proved {
		t.Fatal("the number was proved by a code that went to an email")
	}
}

// A sign-in already checking its password when the first code ends that password
// must not come back with a session.
func TestASignInUnderWayDoesNotOutliveTheFirstCode(t *testing.T) {
	app, sms, _ := openInstall(t)
	old := pbkdf2Iterations
	pbkdf2Iterations = 1_500_000
	t.Cleanup(func() { pbkdf2Iterations = old })
	const pass = "the password that ends"
	signedUp(t, app, theirPhone, pass)
	mustAsk(t, app, theirPhone)
	code := sms.lastCode(t, app, theirPhone)

	done := make(chan *Session, 1)
	go func() {
		_, sess, err := app.superusers.Authenticate(context.Background(), theirPhone, pass, "203.0.113.1")
		if err != nil {
			done <- nil
			return
		}
		done <- sess
	}()
	time.Sleep(60 * time.Millisecond)
	if r := sendPhoneCode(t, app, theirPhone, code); r.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", r.Code, r.Body)
	}
	if sess := <-done; sess != nil {
		if _, ok := app.superusers.Resolve(context.Background(), sess.Token); ok {
			t.Fatal("a sign-in finished after the first code with a live session")
		}
	}
}

func TestATeamResetDoesNotTextAPhoneAccount(t *testing.T) {
	app, sms, _ := openInstall(t)
	in := signedUp(t, app, theirPhone, "a-long-enough-password")
	st := openStore(t, app, in.Token)
	r := do(t, app, http.MethodPost, "/api/admin/superusers/"+itoa(in.Record.ID)+"/password-reset", bearer(in.Token), header("X-Store", st))
	if r.Code != http.StatusConflict {
		t.Fatalf("team reset = %d, want 409: %s", r.Code, r.Body)
	}
	app.superusers.waitForResetDelivery(5 * time.Second)
	if n := len(sms.notes()); n != 0 {
		t.Fatalf("%d texts", n)
	}
}

func TestTheDailyLimitCountsEveryTextedCodeAndNoEmails(t *testing.T) {
	cfg := signupConfig(t)
	cfg.SMSCodesPerDay = 3
	sms := &smsStub{}
	rec := &recordingNotifier{}
	app := newApp(t, cfg, &notifyModule{rec: rec}, sms)
	su := newSuperuser(t, app, "someone@example.com", "a-long-enough-password")
	token := bearer(signIn(t, app, "someone@example.com", "a-long-enough-password", "198.51.100.1").Token)
	withPhone(t, app, su, theirPhone)

	// An email code does not use up a text.
	if r := do(t, app, http.MethodPost, "/api/admin/me/email/code", token, jsonBody(t, map[string]string{"email": "new@example.com"})); r.Code != http.StatusAccepted {
		t.Fatalf("email code = %d: %s", r.Code, r.Body)
	}
	// One each: sign-in, adding a number, a reset by text.
	mustAsk(t, app, "+919876500071")
	if r := do(t, app, http.MethodPost, "/api/admin/me/phone/code", token, jsonBody(t, map[string]string{"phone": "+919876500072"})); r.Code != http.StatusAccepted {
		t.Fatalf("own code = %d: %s", r.Code, r.Body)
	}
	askReset(t, app, theirPhone)
	app.superusers.waitForResetDelivery(5 * time.Second)
	if n := len(sms.notes()); n != 3 {
		t.Fatalf("%d texts before the limit, want 3", n)
	}

	// Full: every route is refused, a reset by the same answer as ever.
	if r := askPhoneCode(t, app, "+919876500073"); r.Code != http.StatusTooManyRequests {
		t.Errorf("sign-in code = %d", r.Code)
	}
	if r := do(t, app, http.MethodPost, "/api/admin/me/phone/code", token, jsonBody(t, map[string]string{"phone": "+919876500074"})); r.Code != http.StatusTooManyRequests {
		t.Errorf("own code = %d", r.Code)
	}
	askReset(t, app, theirPhone)
	app.superusers.waitForResetDelivery(5 * time.Second)
	if n := len(sms.notes()); n != 3 {
		t.Errorf("%d texts after the limit, want 3", n)
	}
	// A counted reset holds no number and charges no phone.
	var stored int
	if err := app.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM superuser_login_codes WHERE identity = $1`, theirPhone).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Errorf("%d rows charged to the phone for its reset", stored)
	}
}

func TestRemovingOrReplacingAPhoneEndsEverythingElseOpen(t *testing.T) {
	app, sms := phoneApp(t)
	const pass = "a-long-enough-password"
	su := newSuperuser(t, app, "a@example.com", pass)
	withPhone(t, app, su, theirPhone)
	other := func() string {
		nextMinute(t, app)
		mustAsk(t, app, theirPhone)
		var out authResponse
		decodeData(t, sendPhoneCode(t, app, theirPhone, sms.lastCode(t, app, theirPhone)), &out)
		return out.Token
	}
	caller := signIn(t, app, "a@example.com", pass, "198.51.100.1").Token
	opened := other()
	if _, _, err := app.superusers.issueReset(context.Background(), su.ID, "a@example.com", false); err != nil {
		t.Fatal(err)
	}

	// Replacing it.
	nextMinute(t, app)
	do(t, app, http.MethodPost, "/api/admin/me/phone/code", bearer(caller), jsonBody(t, map[string]string{"phone": mine}))
	if r := do(t, app, http.MethodPut, "/api/admin/me/phone", bearer(caller),
		jsonBody(t, map[string]string{"phone": mine, "code": sms.lastCode(t, app, mine), "current_password": pass})); r.Code != http.StatusOK {
		t.Fatalf("replace = %d: %s", r.Code, r.Body)
	}
	if got := do(t, app, http.MethodGet, "/api/admin/me", bearer(opened)); got.Code != http.StatusUnauthorized {
		t.Errorf("a session opened with the old number = %d after replacing it", got.Code)
	}
	if got := do(t, app, http.MethodGet, "/api/admin/me", bearer(caller)); got.Code != http.StatusOK {
		t.Errorf("the caller's own session = %d", got.Code)
	}
	var resets int
	if err := app.db.QueryRowContext(context.Background(), `SELECT count(*) FROM superuser_password_resets WHERE superuser_id = $1`, su.ID).Scan(&resets); err != nil {
		t.Fatal(err)
	}
	if resets != 0 {
		t.Errorf("%d resets outlived the old number", resets)
	}

	// Removing it.
	second := signIn(t, app, "a@example.com", pass, "198.51.100.1").Token
	if r := do(t, app, http.MethodDelete, "/api/admin/me/phone", bearer(caller), jsonBody(t, map[string]string{"current_password": pass})); r.Code != http.StatusOK {
		t.Fatalf("remove = %d: %s", r.Code, r.Body)
	}
	if got := do(t, app, http.MethodGet, "/api/admin/me", bearer(second)); got.Code != http.StatusUnauthorized {
		t.Errorf("another session = %d after removing the phone", got.Code)
	}
	if got := do(t, app, http.MethodGet, "/api/admin/me", bearer(caller)); got.Code != http.StatusOK {
		t.Errorf("the caller's own session = %d", got.Code)
	}
}

// Adding a first number ends nothing: nothing was lost.
func TestAddingAFirstPhoneEndsNoSessions(t *testing.T) {
	app, sms := phoneApp(t)
	const pass = "a-long-enough-password"
	newSuperuser(t, app, "a@example.com", pass)
	caller := signIn(t, app, "a@example.com", pass, "198.51.100.1").Token
	second := signIn(t, app, "a@example.com", pass, "198.51.100.1").Token
	do(t, app, http.MethodPost, "/api/admin/me/phone/code", bearer(caller), jsonBody(t, map[string]string{"phone": mine}))
	if r := do(t, app, http.MethodPut, "/api/admin/me/phone", bearer(caller),
		jsonBody(t, map[string]string{"phone": mine, "code": sms.lastCode(t, app, mine), "current_password": pass})); r.Code != http.StatusOK {
		t.Fatalf("add = %d: %s", r.Code, r.Body)
	}
	if got := do(t, app, http.MethodGet, "/api/admin/me", bearer(second)); got.Code != http.StatusOK {
		t.Errorf("another session = %d", got.Code)
	}
}

// With real hashes, a wrong password takes as long for an account with no password
// as for no account at all.
func TestAnAccountWithNoPasswordIsNotTheFastAnswer(t *testing.T) {
	app, _ := phoneApp(t)
	old := pbkdf2Iterations
	pbkdf2Iterations = 600_000
	t.Cleanup(func() { pbkdf2Iterations = old })
	ctx := context.Background()
	h, err := hashPassword("anything")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.ExecContext(ctx, `UPDATE superusers SET password_hash = $1`, h); err != nil {
		t.Fatal(err)
	}
	if _, err := app.superusers.signUpPhone(ctx, theirPhone); err != nil {
		t.Fatal(err)
	}
	took := func(identity, ip string) time.Duration {
		start := time.Now()
		_, _, _ = app.superusers.Authenticate(ctx, identity, "not the password", ip)
		return time.Since(start)
	}
	var known, unknown time.Duration
	for i := 0; i < 3; i++ {
		known += took(theirPhone, "198.51.100.10")
		unknown += took(mine, "198.51.100.11")
	}
	if known < unknown/3 {
		t.Fatalf("an account with no password answers in %v, no account in %v", known/3, unknown/3)
	}
}

func TestOperatorCodesGoOnlyToTheInstallsCountries(t *testing.T) {
	const abroad = "+14155550100"
	app, sms, _ := openInstall(t)
	in := signedUp(t, app, "someone@example.com", "a-long-enough-password")
	st := openStore(t, app, in.Token)
	refused := func(r interface{ Result() *http.Response }, what string) {
		t.Helper()
		if got := r.Result().StatusCode; got != http.StatusConflict {
			t.Errorf("%s = %d, want 409", what, got)
		}
	}
	refused(askPhoneCode(t, app, abroad), "sign-in code")
	refused(do(t, app, http.MethodPost, "/api/admin/me/phone/code", bearer(in.Token), header("X-Store", st),
		jsonBody(t, map[string]string{"phone": abroad})), "own code")
	var rows int
	if err := app.db.QueryRowContext(context.Background(), `SELECT count(*) FROM superuser_login_codes`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	app.superusers.waitForResetDelivery(5 * time.Second)
	if rows != 0 || len(sms.notes()) != 0 {
		t.Errorf("%d rows stored and %d texts sent for a refused country", rows, len(sms.notes()))
	}

	app.cfg.SMSCountries = ParseSMSCountries("91, +1")
	mustAsk(t, app, abroad)
	if sms.lastCode(t, app, abroad) == "" {
		t.Error("an allowed country got no code")
	}
}

func TestAnUnreadableCountryListMeansIndia(t *testing.T) {
	for _, in := range []string{"", "91,", "india", "1,x", "0", "1234"} {
		if got := ParseSMSCountries(in); len(got) != 1 || got[0] != "91" {
			t.Errorf("%q = %v", in, got)
		}
	}
	if got := ParseSMSCountries(" 91, +1 "); len(got) != 2 || got[0] != "91" || got[1] != "1" {
		t.Errorf("list = %v", got)
	}
}

func TestAnIndianNumberHasOneShape(t *testing.T) {
	for _, bad := range []string{"+91 0 98765 11111", "+91 98765 1111", "+91 98765 111111", "+91 58765 11111", "+91 11 2345 6789"} {
		if got, err := NormalizePhone(bad); err == nil {
			t.Errorf("%q = %q, want refused", bad, got)
		}
	}
	for good, want := range map[string]string{"+91 98765 11111": "+919876511111", "+91 6000000000": "+916000000000", "+1 415 555 0100": "+14155550100", "+971 50 123 4567": "+971501234567"} {
		if got, err := NormalizePhone(good); err != nil || got != want {
			t.Errorf("%q = %q, %v", good, got, err)
		}
	}
	if !strings.HasPrefix(fmt.Sprint(reachProbe), "+91") {
		t.Fatal("the probe is meant to be Indian")
	}
	if _, err := NormalizePhone(reachProbe); err != nil {
		t.Fatalf("the probe is refused: %v", err)
	}
}

// emailOnlyOnStores is an email provider set up in stores' panels and not for the
// install.
type emailOnlyOnStores struct{ smsStub }

func (m *emailOnlyOnStores) Register(app *App) error {
	m.ready = func(ctx context.Context) bool { st, _ := StoreFrom(ctx); return st != installScope }
	app.RegisterNotifier(ChannelEmail, m)
	return nil
}

// Nobody is promised a reset that cannot be sent on the install's channel.
func TestAStoresOwnEmailPromisesNoResetWhereStoresAreOthers(t *testing.T) {
	mail := &emailOnlyOnStores{}
	app := newApp(t, testConfig(requireDB(t)), mail)
	second := addTestStore(t, app, "second")
	if _, err := app.Superusers().Create(WithStore(context.Background(), second), "only@example.com", "a-long-enough-password", RoleOwner); err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	decodeData(t, do(t, app, http.MethodGet, "/api/admin/auth-state", header("X-Store", second)), &state)
	if state["password_reset"] != false {
		t.Errorf("auth-state password_reset = %v", state["password_reset"])
	}
	var out ResetRequest
	r := do(t, app, http.MethodPost, "/api/admin/password-reset", header("X-Store", second), jsonBody(t, map[string]string{"identity": "only@example.com"}))
	decodeData(t, r, &out)
	if out.Delivery != DeliveryNone {
		t.Errorf("delivery = %q", out.Delivery)
	}
	app.superusers.waitForResetDelivery(5 * time.Second)
	if n := len(mail.notes()); n != 0 {
		t.Errorf("%d mails on the store's channel", n)
	}
}
