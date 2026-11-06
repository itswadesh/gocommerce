package gocommerce

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itswadesh/gocommerce/internal/testdb"
)

// Signing in to the panel with a phone (D193). Against real Postgres, with a
// recording SMS notifier standing in for the provider: every security rule of
// the flow has a test here that fails when the rule is taken away.

const (
	knownPhone   = "+919876500001"
	unknownPhone = "+919876500002"
)

// smsStub is an SMS provider that records what it is given, the store it was
// given it under, and is ready only where ready says.
type smsStub struct {
	mu    sync.Mutex
	sent  []Notification
	under []string
	ready func(context.Context) bool
	fail  func(Notification) error
}

func (m *smsStub) Name() string            { return "sms-stub" }
func (m *smsStub) Migrations() []Migration { return nil }
func (m *smsStub) Register(app *App) error {
	app.RegisterNotifier(ChannelSMS, m)
	return nil
}

func (m *smsStub) Configured(ctx context.Context) bool { return m.ready == nil || m.ready(ctx) }

func (m *smsStub) Notify(ctx context.Context, n Notification) error {
	st, _ := StoreFrom(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, n)
	m.under = append(m.under, st)
	if m.fail != nil {
		return m.fail(n)
	}
	return nil
}

func (m *smsStub) notes() []Notification {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.sent)
}

func (m *smsStub) lastCode(t *testing.T, app *App, phone string) string {
	t.Helper()
	app.superusers.waitForResetDelivery(5 * time.Second)
	notes := m.notes()
	for i := len(notes) - 1; i >= 0; i-- {
		if notes[i].To == phone {
			if notes[i].Event != EventLoginCode || notes[i].Channel != ChannelSMS {
				t.Fatalf("sent %+v, want a sign-in code by SMS", notes[i])
			}
			return notes[i].Data["code"]
		}
	}
	t.Fatalf("no code was sent to %s", phone)
	return ""
}

// phoneApp is an install with SMS and a first owner, closed to sign-ups.
func phoneApp(t *testing.T) (*App, *smsStub) {
	t.Helper()
	sms := &smsStub{}
	app := newTestApp(t, sms)
	newSuperuser(t, app, "operator@example.com", "the operator's password")
	return app, sms
}

// phoneCodeSignupApp is the same with sign-ups open.
func phoneCodeSignupApp(t *testing.T) (*App, *smsStub) {
	t.Helper()
	sms := &smsStub{}
	app := newApp(t, signupConfig(t), &notifyModule{rec: &recordingNotifier{}}, sms)
	newSuperuser(t, app, "operator@example.com", "the operator's password")
	return app, sms
}

// withPhone gives an operator a phone, as a proved code would.
func withPhone(t *testing.T, app *App, su *Superuser, phone string) {
	t.Helper()
	if _, err := app.superusers.writeOwnPhone(context.Background(), su.ID, phone, "test", ""); err != nil {
		t.Fatal(err)
	}
}

func askPhoneCode(t *testing.T, app *App, phone string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	opts = append([]func(*http.Request){jsonBody(t, map[string]string{"phone": phone})}, opts...)
	return do(t, app, http.MethodPost, "/api/admin/auth-with-phone/code", opts...)
}

func sendPhoneCode(t *testing.T, app *App, phone, code string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	opts = append([]func(*http.Request){jsonBody(t, map[string]string{"phone": phone, "code": code})}, opts...)
	return do(t, app, http.MethodPost, "/api/admin/auth-with-phone", opts...)
}

// nextMinute lets a phone ask again.
func nextMinute(t *testing.T, app *App) {
	t.Helper()
	if _, err := app.db.ExecContext(context.Background(),
		`UPDATE superuser_login_codes SET created_at = created_at - interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
}

func otherCode(code string) string {
	if code == "000000" {
		return "000001"
	}
	return "000000"
}

func mustAsk(t *testing.T, app *App, phone string, opts ...func(*http.Request)) {
	t.Helper()
	if rec := askPhoneCode(t, app, phone, opts...); rec.Code != http.StatusAccepted {
		t.Fatalf("asking for a code = %d: %s", rec.Code, rec.Body)
	}
}

func assertWrongCode(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body)
	}
	if e := decodeError(t, rec); e.Code != "invalid_code" {
		t.Fatalf("code = %q, want invalid_code", e.Code)
	}
}

func TestACodeIsRandomStoredHashedAndNeverAnswered(t *testing.T) {
	app, sms := phoneApp(t)
	su := newSuperuser(t, app, "a@example.com", "the operator's password")
	withPhone(t, app, su, knownPhone)

	rec := askPhoneCode(t, app, knownPhone)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	first := sms.lastCode(t, app, knownPhone)
	if len(first) != phoneCodeLength {
		t.Fatalf("code = %q, want %d digits", first, phoneCodeLength)
	}
	if strings.Contains(rec.Body.String(), first) {
		t.Fatalf("the answer carries the code: %s", rec.Body)
	}
	var hash, whole string
	if err := app.db.QueryRowContext(context.Background(),
		`SELECT code_hash, r::text FROM superuser_login_codes r WHERE identity = $1`, knownPhone).Scan(&hash, &whole); err != nil {
		t.Fatal(err)
	}
	if hash != phoneCodeHash(knownPhone, first) {
		t.Fatalf("stored %q, want the code's hash", hash)
	}
	if strings.Contains(whole, first) {
		t.Fatalf("the row holds the code in the clear: %s", whole)
	}

	// Three more codes are never all the same as the first from a real source
	// of randomness: one in 10^18.
	same := true
	for range 3 {
		nextMinute(t, app)
		mustAsk(t, app, knownPhone)
		if sms.lastCode(t, app, knownPhone) != first {
			same = false
		}
	}
	if same {
		t.Fatal("four codes in a row were identical")
	}
}

func TestARightCodeSignsInAsAnEmailSignInDoes(t *testing.T) {
	app, sms := phoneApp(t)
	su := newSuperuser(t, app, "a@example.com", "the operator's password")
	withPhone(t, app, su, knownPhone)
	mustAsk(t, app, knownPhone)

	rec := sendPhoneCode(t, app, knownPhone, sms.lastCode(t, app, knownPhone))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var out authResponse
	decodeData(t, rec, &out)
	if out.Record == nil || out.Record.ID != su.ID || out.Record.Role != RoleOwner || len(out.Record.Rights) == 0 {
		t.Fatalf("record = %+v, want the operator with their role", out.Record)
	}
	if until := time.Until(out.ExpiresAt); until < sessionTTL-time.Hour || until > sessionTTL+time.Hour {
		t.Errorf("session lasts %v, want %v", until, sessionTTL)
	}

	// The token is an ordinary session: stored hashed, and accepted everywhere.
	var stored int
	if err := app.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM superuser_sessions WHERE token_hash = $1 AND superuser_id = $2`,
		hashToken(out.Token), su.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 1 {
		t.Fatal("no session row holds the token's hash")
	}
	if me := do(t, app, http.MethodGet, "/api/admin/me", bearer(out.Token)); me.Code != http.StatusOK {
		t.Fatalf("/me with the session = %d: %s", me.Code, me.Body)
	}
}

func TestEmailAndPasswordStillWorkBesideAPhone(t *testing.T) {
	app, _ := phoneApp(t)
	su := newSuperuser(t, app, "a@example.com", "the operator's password")
	withPhone(t, app, su, knownPhone)
	rec := do(t, app, http.MethodPost, "/api/admin/auth-with-password",
		jsonBody(t, map[string]string{"identity": "a@example.com", "password": "the operator's password"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	rec = do(t, app, http.MethodPost, "/api/admin/auth-with-password",
		jsonBody(t, map[string]string{"identity": "a@example.com", "password": "a wrong password"}))
	if e := decodeError(t, rec); rec.Code != http.StatusBadRequest || e.Code != "invalid_credentials" {
		t.Fatalf("wrong password = %d %q", rec.Code, e.Code)
	}
}

func TestAnExpiredCodeIsRefused(t *testing.T) {
	app, sms := phoneApp(t)
	withPhone(t, app, newSuperuser(t, app, "a@example.com", "the operator's password"), knownPhone)
	mustAsk(t, app, knownPhone)
	code := sms.lastCode(t, app, knownPhone)

	var left time.Duration
	var expires time.Time
	if err := app.db.QueryRowContext(context.Background(),
		`SELECT expires_at FROM superuser_login_codes WHERE identity = $1`, knownPhone).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if left = time.Until(expires); left > 11*time.Minute || left < 9*time.Minute {
		t.Fatalf("a code lives %v, want ten minutes", left)
	}
	if _, err := app.db.ExecContext(context.Background(),
		`UPDATE superuser_login_codes SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	assertWrongCode(t, sendPhoneCode(t, app, knownPhone, code))
}

func TestACodeWorksOnce(t *testing.T) {
	app, sms := phoneApp(t)
	withPhone(t, app, newSuperuser(t, app, "a@example.com", "the operator's password"), knownPhone)
	mustAsk(t, app, knownPhone)
	code := sms.lastCode(t, app, knownPhone)
	if rec := sendPhoneCode(t, app, knownPhone, code); rec.Code != http.StatusOK {
		t.Fatalf("first use = %d: %s", rec.Code, rec.Body)
	}
	assertWrongCode(t, sendPhoneCode(t, app, knownPhone, code))
}

// The whole point of consumed_at. A guess queued on the row lock began before
// the winner committed, so it would read expires_at = now() as still open.
func TestARightCodeSentManyTimesAtOnceSignsInOnce(t *testing.T) {
	app, sms := phoneApp(t)
	withPhone(t, app, newSuperuser(t, app, "a@example.com", "the operator's password"), knownPhone)
	for round := range 5 {
		if round > 0 {
			nextMinute(t, app)
		}
		mustAsk(t, app, knownPhone)
		code := sms.lastCode(t, app, knownPhone)
		var mu sync.Mutex
		wins := 0
		var wg sync.WaitGroup
		for range 12 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _, _, err := app.superusers.VerifyPhoneCode(context.Background(), knownPhone, code)
				if err == nil {
					mu.Lock()
					wins++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if wins != 1 {
			t.Fatalf("one code signed in %d times", wins)
		}
	}
}

func TestOnlyTheLatestCodeWorks(t *testing.T) {
	app, sms := phoneApp(t)
	withPhone(t, app, newSuperuser(t, app, "a@example.com", "the operator's password"), knownPhone)
	mustAsk(t, app, knownPhone)
	old := sms.lastCode(t, app, knownPhone)
	nextMinute(t, app)
	mustAsk(t, app, knownPhone)
	latest := sms.lastCode(t, app, knownPhone)
	if old == latest {
		t.Skip("the two codes happened to match")
	}
	assertWrongCode(t, sendPhoneCode(t, app, knownPhone, old))
	if rec := sendPhoneCode(t, app, knownPhone, latest); rec.Code != http.StatusOK {
		t.Fatalf("latest code = %d: %s", rec.Code, rec.Body)
	}
}

func TestFiveWrongCodesUseACodeUp(t *testing.T) {
	app, sms := phoneApp(t)
	withPhone(t, app, newSuperuser(t, app, "a@example.com", "the operator's password"), knownPhone)
	mustAsk(t, app, knownPhone)
	code := sms.lastCode(t, app, knownPhone)
	for range phoneCodeAttempts {
		assertWrongCode(t, sendPhoneCode(t, app, knownPhone, otherCode(code)))
	}
	assertWrongCode(t, sendPhoneCode(t, app, knownPhone, code))
}

func TestWrongCodesAtOnceGetOnlyFiveTries(t *testing.T) {
	app, sms := phoneApp(t)
	withPhone(t, app, newSuperuser(t, app, "a@example.com", "the operator's password"), knownPhone)
	mustAsk(t, app, knownPhone)
	code := sms.lastCode(t, app, knownPhone)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = app.superusers.spendLoginCode(context.Background(), knownPhone, otherCode(code), phoneCodeHash)
		}()
	}
	wg.Wait()
	var attempts int
	if err := app.db.QueryRowContext(context.Background(),
		`SELECT attempts FROM superuser_login_codes WHERE identity = $1`, knownPhone).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != phoneCodeAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, phoneCodeAttempts)
	}
}

func TestAPhoneGetsOneCodeAMinuteAndFiveADay(t *testing.T) {
	app, sms := phoneApp(t)
	withPhone(t, app, newSuperuser(t, app, "a@example.com", "the operator's password"), knownPhone)
	for i := range phoneCodesPerDay {
		if i > 0 {
			if rec := askPhoneCode(t, app, knownPhone); rec.Code != http.StatusTooManyRequests {
				t.Fatalf("code %d within the minute = %d, want 429: %s", i, rec.Code, rec.Body)
			}
			nextMinute(t, app)
		}
		mustAsk(t, app, knownPhone)
	}
	nextMinute(t, app)
	if rec := askPhoneCode(t, app, knownPhone); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code past the day's limit = %d, want 429: %s", rec.Code, rec.Body)
	}
	app.superusers.waitForResetDelivery(5 * time.Second)
	if n := len(sms.notes()); n != phoneCodesPerDay {
		t.Fatalf("%d texts sent, want %d", n, phoneCodesPerDay)
	}
}

func parallelAsks(t *testing.T, app *App, phone func(int) string, n int, ip func(int) string) int {
	t.Helper()
	var mu sync.Mutex
	accepted := 0
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := app.superusers.RequestPhoneCode(context.Background(), phone(i), ip(i))
			if err == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return accepted
}

func TestABurstForOnePhoneGetsOneCode(t *testing.T) {
	app, _ := phoneApp(t)
	// Several rounds, each on a number of its own: one burst can happen not to
	// overlap, and then a missing lock would go unseen.
	for round := range 8 {
		phone := fmt.Sprintf("+9198765%05d", round)
		got := parallelAsks(t, app, func(int) string { return phone }, 16,
			func(i int) string { return fmt.Sprintf("198.51.%d.%d", 100+round, i+1) })
		if got != 1 {
			t.Fatalf("%d codes for one phone at once, want 1", got)
		}
	}
}

func TestAClientGetsTwentyCodesAnHourEvenAllAtOnce(t *testing.T) {
	app, _ := phoneApp(t)
	number := func(i int) string { return "+9198765" + string(rune('1'+i/10)) + string(rune('0'+i%10)) + "000" }
	got := parallelAsks(t, app, number, phoneCodesPerClientHr+10, func(int) string { return "198.51.100.7" })
	if got != phoneCodesPerClientHr {
		t.Fatalf("%d codes from one client, want %d", got, phoneCodesPerClientHr)
	}
	// Over the limit whichever number it asks for, and another client is not.
	if rec := askPhoneCode(t, app, "+919876599999", fromIP("198.51.100.7")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a client over its limit = %d: %s", rec.Code, rec.Body)
	}
	mustAsk(t, app, "+919876599999", fromIP("198.51.100.8"))
}

func TestAClientsIPv6LimitCountsItsWhole64(t *testing.T) {
	app, _ := phoneApp(t)
	number := func(i int) string { return "+9198765" + string(rune('1'+i/10)) + string(rune('0'+i%10)) + "000" }
	for i := range phoneCodesPerClientHr {
		addr := fmt.Sprintf("2001:db8:1:1::%x", i+1)
		if err := app.superusers.RequestPhoneCode(context.Background(), number(i), addr); err != nil {
			t.Fatal(err)
		}
	}
	err := app.superusers.RequestPhoneCode(context.Background(), "+919876599999", "2001:db8:1:1::ffff")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests {
		t.Fatalf("another address in the /64 = %v, want 429", err)
	}
}

// Sign-up closed, so only a known phone gets a text: the words, the status and
// the limit must still not say which is which.
func TestTheAnswersAreTheSameForAKnownPhoneAndAnUnknownOne(t *testing.T) {
	app, sms := phoneApp(t)
	withPhone(t, app, newSuperuser(t, app, "a@example.com", "the operator's password"), knownPhone)
	disabled := "+919876500003"
	withPhone(t, app, newSuperuser(t, app, "d@example.com", "the operator's password"), disabled)
	if err := app.superusers.SetDisabled(context.Background(), disabled, true); err != nil {
		t.Fatal(err)
	}

	type answer struct {
		code int
		body string
	}
	ask := func(phone string) answer {
		rec := askPhoneCode(t, app, phone)
		return answer{rec.Code, rec.Body.String()}
	}
	want := ask(knownPhone)
	if want.code != http.StatusAccepted {
		t.Fatalf("known phone = %+v", want)
	}
	for _, phone := range []string{unknownPhone, disabled} {
		if got := ask(phone); got != want {
			t.Errorf("%s answered %+v, the known phone %+v", phone, got, want)
		}
	}
	// The same refusal inside the minute.
	limited := ask(knownPhone)
	if limited.code != http.StatusTooManyRequests {
		t.Fatalf("known phone inside the minute = %+v", limited)
	}
	for _, phone := range []string{unknownPhone, disabled} {
		if got := ask(phone); got != limited {
			t.Errorf("%s inside the minute answered %+v, the known phone %+v", phone, got, limited)
		}
	}
	// The difference is only who got a text.
	app.superusers.waitForResetDelivery(5 * time.Second)
	notes := sms.notes()
	if len(notes) != 1 || notes[0].To != knownPhone {
		t.Fatalf("texts = %+v, want one, to the known phone", notes)
	}

	// A wrong code reads the same, and so does a right code on a number nobody
	// can sign in with.
	code := notes[0].Data["code"]
	wrong := sendPhoneCode(t, app, knownPhone, otherCode(code))
	assertWrongCode(t, wrong)
	for _, phone := range []string{unknownPhone, disabled} {
		rec := sendPhoneCode(t, app, phone, "123456")
		if rec.Code != wrong.Code || rec.Body.String() != wrong.Body.String() {
			t.Errorf("%s wrong code = %d %s, known phone %d %s", phone, rec.Code, rec.Body, wrong.Code, wrong.Body)
		}
		if _, err := app.db.ExecContext(context.Background(),
			`UPDATE superuser_login_codes SET code_hash = $2 WHERE identity = $1`, phone, phoneCodeHash(phone, "654321")); err != nil {
			t.Fatal(err)
		}
		rec = sendPhoneCode(t, app, phone, "654321")
		if rec.Code != wrong.Code || rec.Body.String() != wrong.Body.String() {
			t.Errorf("%s right code = %d %s, want what a wrong code gets", phone, rec.Code, rec.Body)
		}
	}
}

func TestPhonesAreNormalisedToE164(t *testing.T) {
	for in, want := range map[string]string{
		"+919876543210":      "+919876543210",
		" +91 98765 43210 ":  "+919876543210",
		"+91-98765-43210":    "+919876543210",
		"+1 (555) 123.45.67": "+15551234567",
		"+91 98765 43210":    "+919876543210",
	} {
		got, err := NormalizePhone(in)
		if err != nil || got != want {
			t.Errorf("NormalizePhone(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "9876543210", "919876543210", "+0919876543210", "+1234567",
		"+1234567890123456", "+91 98765 x43210", "++919876543210", "+९१९८७६५४३२१०", "+91987654321;drop"} {
		if got, err := NormalizePhone(in); err == nil {
			t.Errorf("NormalizePhone(%q) = %q, want a refusal", in, got)
		}
	}

	// Every spelling is one person, and one limit.
	app, sms := phoneApp(t)
	withPhone(t, app, newSuperuser(t, app, "a@example.com", "the operator's password"), knownPhone)
	mustAsk(t, app, "+91 98765-00001")
	if rec := askPhoneCode(t, app, knownPhone); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a second spelling got round the limit: %d", rec.Code)
	}
	if rec := sendPhoneCode(t, app, "+91 98765 00001", sms.lastCode(t, app, knownPhone)); rec.Code != http.StatusOK {
		t.Fatalf("verifying with another spelling = %d: %s", rec.Code, rec.Body)
	}
	if rec := askPhoneCode(t, app, "9876500001"); rec.Code != http.StatusBadRequest {
		t.Fatalf("no country code = %d, want 400", rec.Code)
	}
}

func TestLogsNeverHoldACodeOrAFullNumber(t *testing.T) {
	var logged bytes.Buffer
	sms := &smsStub{fail: func(n Notification) error {
		return errors.New("provider says " + n.To + " and " + strings.TrimPrefix(n.To, "+") + " is refused")
	}}
	cfg := signupConfig(t)
	cfg.Logger = slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	app := newApp(t, cfg, &notifyModule{rec: &recordingNotifier{}}, sms)
	newSuperuser(t, app, "operator@example.com", "the operator's password")

	mustAsk(t, app, unknownPhone)
	code := sms.lastCode(t, app, unknownPhone)
	sendPhoneCode(t, app, unknownPhone, otherCode(code))
	if rec := sendPhoneCode(t, app, unknownPhone, code); rec.Code != http.StatusCreated {
		t.Fatalf("sign-up = %d: %s", rec.Code, rec.Body)
	}
	app.superusers.waitForResetDelivery(5 * time.Second)
	if !strings.Contains(logged.String(), "sign-in code could not be sent") {
		t.Fatalf("the failure was not logged: %s", logged.String())
	}
	if out := logged.String(); strings.Contains(out, strings.TrimPrefix(unknownPhone, "+")) || strings.Contains(out, code) {
		t.Fatalf("the log holds the number or the code:\n%s", out)
	}
}

func TestADisabledOperatorCannotGetIn(t *testing.T) {
	app, sms := phoneApp(t)
	ctx := context.Background()
	su := newSuperuser(t, app, "a@example.com", "the operator's password")
	withPhone(t, app, su, knownPhone)

	// A code asked for before the operator was disabled, and a session.
	mustAsk(t, app, knownPhone)
	code := sms.lastCode(t, app, knownPhone)
	sess := signIn(t, app, "a@example.com", "the operator's password", "198.51.100.1")

	if err := app.superusers.SetDisabled(ctx, knownPhone, true); err != nil {
		t.Fatal(err)
	}
	assertWrongCode(t, sendPhoneCode(t, app, knownPhone, code))
	if rec := do(t, app, http.MethodGet, "/api/admin/me", bearer(sess.Token)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a disabled operator's session = %d, want 401", rec.Code)
	}
	var sessions int
	if err := app.db.QueryRowContext(ctx, `SELECT count(*) FROM superuser_sessions WHERE superuser_id = $1`, su.ID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("%d sessions outlived disabling", sessions)
	}
	// A session row that somehow exists opens nothing either.
	if _, err := app.db.ExecContext(ctx, `INSERT INTO superuser_sessions (token_hash, superuser_id, expires_at)
		VALUES ($1, $2, now() + interval '1 hour')`, hashToken("left-behind"), su.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := app.superusers.Resolve(ctx, "left-behind"); ok {
		t.Fatal("a disabled operator's session resolved")
	}
	if _, _, err := app.superusers.Authenticate(ctx, "a@example.com", "the operator's password", "198.51.100.1"); !errors.Is(err, errPersonDisabled) {
		t.Fatalf("a disabled operator's password = %v", err)
	}
	// No session is made by any door, including one made after the check.
	if _, err := app.superusers.issue(ctx, su.ID); !errors.Is(err, errPersonDisabled) {
		t.Fatalf("issue for a disabled operator = %v", err)
	}
	nextMinute(t, app)
	before := len(sms.notes())
	mustAsk(t, app, knownPhone)
	app.superusers.waitForResetDelivery(5 * time.Second)
	if len(sms.notes()) != before {
		t.Fatal("a disabled operator was texted a code")
	}

	if err := app.superusers.SetDisabled(ctx, "A@Example.com", false); err != nil {
		t.Fatal(err)
	}
	signIn(t, app, "a@example.com", "the operator's password", "198.51.100.1")
	if err := app.superusers.SetDisabled(ctx, "nobody@example.com", true); err == nil {
		t.Fatal("disabling nobody succeeded")
	}
}

// A disabled person's number cannot sign up afresh where sign-up is open.
func TestADisabledPhoneCannotSignUpAgain(t *testing.T) {
	app, _ := phoneCodeSignupApp(t)
	withPhone(t, app, newSuperuser(t, app, "a@example.com", "the operator's password"), knownPhone)
	if err := app.superusers.SetDisabled(context.Background(), knownPhone, true); err != nil {
		t.Fatal(err)
	}
	mustAsk(t, app, knownPhone)
	if _, err := app.db.ExecContext(context.Background(),
		`UPDATE superuser_login_codes SET code_hash = $2 WHERE identity = $1`, knownPhone, phoneCodeHash(knownPhone, "654321")); err != nil {
		t.Fatal(err)
	}
	assertWrongCode(t, sendPhoneCode(t, app, knownPhone, "654321"))
	var n int
	if err := app.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM superusers WHERE phone = $1`, knownPhone).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d accounts on the phone", n)
	}
}

// A store's provider account is its staff's to read, and this code opens an
// account. So on an install that hosts several stores no code goes on a store's
// channel, whoever the person is (D211).
func TestACodeNeverTravelsThroughAnotherStoresChannel(t *testing.T) {
	sms := &smsStub{}
	app := newApp(t, signupConfig(t), &notifyModule{rec: &recordingNotifier{}}, sms)
	ctx := context.Background()
	newSuperuser(t, app, "operator@example.com", "the operator's password")
	b := addTestStore(t, app, "second")
	ctxB := WithStore(ctx, b)
	first := app.DefaultStore()

	ownerA := newSuperuser(t, app, "owner-a@example.com", "a-long-enough-password")
	withPhone(t, app, ownerA, "+919876500011")
	onlyB, err := app.Superusers().Create(ctxB, "only-b@example.com", "a-long-enough-password", RoleStaff)
	if err != nil {
		t.Fatal(err)
	}
	withPhone(t, app, onlyB, "+919876500012")
	both := newSuperuser(t, app, "both@example.com", "a-long-enough-password")
	withPhone(t, app, both, "+919876500013")
	if _, err := app.db.ExecContext(ctxB, `INSERT INTO store_members (store_id, superuser_id, role) VALUES ($1, $2, 'staff')`, b, both.ID); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, phone, via, want string
	}{
		{"someone of another store only", "+919876500011", b, installScope},
		{"someone of this store only", "+919876500012", b, installScope},
		{"someone of two stores", "+919876500013", b, installScope},
		{"someone of two stores, through the other", "+919876500013", first, installScope},
		{"a new person", "+919876500014", b, installScope},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nextMinute(t, app)
			mustAsk(t, app, tc.phone, header("X-Store", tc.via))
			app.superusers.waitForResetDelivery(5 * time.Second)
			sms.mu.Lock()
			defer sms.mu.Unlock()
			last := len(sms.sent) - 1
			if sms.sent[last].To != tc.phone || sms.under[last] != tc.want {
				t.Fatalf("sent to %s under %q, want %s under %q", sms.sent[last].To, sms.under[last], tc.phone, tc.want)
			}
		})
	}

	// On the install's channel the send leaves no row in any store's log.
	var rows int
	if err := app.db.QueryRowContext(ctxB,
		`SELECT count(*) FROM notifications WHERE recipient IN ('+919876500011', '+919876500012')`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("store B's log holds %d rows about sign-in codes", rows)
	}
}

// A store with SMS of its own and an install with none, on an install hosting
// several: no code goes out on it, and no phone is offered.
func TestAnInstallWithNoSMSOfItsOwnSendsNothingForAnotherStoresPeople(t *testing.T) {
	sms := &smsStub{}
	app := newTestApp(t, sms)
	b := addTestStore(t, app, "second")
	sms.ready = func(ctx context.Context) bool {
		st, _ := StoreFrom(ctx)
		return st == b
	}
	newSuperuser(t, app, "owner-a@example.com", "a-long-enough-password")
	withPhone(t, app, newSuperuser(t, app, "a@example.com", "a-long-enough-password"), knownPhone)
	if rec := askPhoneCode(t, app, knownPhone, header("X-Store", b)); rec.Code != http.StatusConflict {
		t.Fatalf("asking = %d, want 409: %s", rec.Code, rec.Body)
	}
	app.superusers.waitForResetDelivery(5 * time.Second)
	if n := len(sms.notes()); n != 0 {
		t.Fatalf("%d texts went out on the store's channel", n)
	}
	if phoneState(t, app, header("X-Store", b))["phone_login"] {
		t.Error("a phone is offered with no install channel to text it on")
	}
	if phoneState(t, app, header("X-Store", b))["phone_signup"] {
		t.Error("a phone sign-up is offered with no install channel to text a new person on")
	}
}

func phoneState(t *testing.T, app *App, opts ...func(*http.Request)) map[string]bool {
	t.Helper()
	var out map[string]any
	decodeData(t, do(t, app, http.MethodGet, "/api/admin/auth-state", opts...), &out)
	got := map[string]bool{}
	for _, k := range []string{"phone_login", "phone_signup", "signup"} {
		v, ok := out[k].(bool)
		if !ok {
			t.Fatalf("auth-state has no %s: %v", k, out)
		}
		got[k] = v
	}
	return got
}

// scopedSMS carries nothing it has no template for, the way MSG91 does.
type scopedSMS struct{ smsStub }

func (s *scopedSMS) Serves(context.Context, Notification) bool { return false }
func (s *scopedSMS) Register(app *App) error {
	app.RegisterNotifier(ChannelSMS, s)
	return nil
}

func TestWithNoSMSTheOptionIsNotOffered(t *testing.T) {
	for name, mods := range map[string][]Module{
		"no provider":            nil,
		"provider not set up":    {&smsStub{ready: func(context.Context) bool { return false }}},
		"no template for a code": {&scopedSMS{}},
	} {
		t.Run(name, func(t *testing.T) {
			app := newTestApp(t, mods...)
			newSuperuser(t, app, "a@example.com", "a-long-enough-password")
			if phoneState(t, app)["phone_login"] {
				t.Error("auth-state offers a phone with nothing to text it")
			}
			rec := askPhoneCode(t, app, knownPhone)
			if e := decodeError(t, rec); rec.Code != http.StatusConflict || e.Code != "sms_unavailable" {
				t.Errorf("asking anyway = %d %q", rec.Code, e.Code)
			}
		})
	}
}

func TestThePhoneOptionFollowsTheInstall(t *testing.T) {
	sms := &smsStub{}
	app := newTestApp(t, sms)
	if phoneState(t, app)["phone_login"] {
		t.Error("a phone is offered before there is an owner; the installer takes an email")
	}
	newSuperuser(t, app, "a@example.com", "a-long-enough-password")
	if s := phoneState(t, app); !s["phone_login"] || s["phone_signup"] {
		t.Errorf("closed install: %v, want a phone to sign in with and none to sign up with", s)
	}
	open, _ := phoneCodeSignupApp(t)
	if s := phoneState(t, open); !s["phone_login"] || !s["phone_signup"] {
		t.Errorf("open install: %v", s)
	}
}

func TestADemoHasNoPhone(t *testing.T) {
	sms := &smsStub{}
	cfg := signupConfig(t)
	cfg.Demo = true
	app := newApp(t, cfg, sms)
	newSuperuser(t, app, "a@example.com", "a-long-enough-password")
	if s := phoneState(t, app); s["phone_login"] || s["phone_signup"] {
		t.Errorf("demo: %v", s)
	}
	if rec := askPhoneCode(t, app, knownPhone); rec.Code != http.StatusConflict {
		t.Errorf("asking on a demo = %d", rec.Code)
	}
	if rec := sendPhoneCode(t, app, knownPhone, "123456"); rec.Code != http.StatusConflict {
		t.Errorf("verifying on a demo = %d", rec.Code)
	}
}

func TestAPhoneSignsUpWhereSignUpIsOpen(t *testing.T) {
	app, sms := phoneCodeSignupApp(t)
	mustAsk(t, app, unknownPhone)
	rec := sendPhoneCode(t, app, unknownPhone, sms.lastCode(t, app, unknownPhone))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var out authResponse
	decodeData(t, rec, &out)
	if out.Record.Email != "" || out.Record.Phone != unknownPhone || out.Record.Role != "" || len(out.Record.Rights) != 0 {
		t.Fatalf("record = %+v, want a person with a phone in no store", out.Record)
	}

	// They open a store on the next screen and own it.
	created := doBody(t, app, http.MethodPost, "/api/stores/create", `{"name":"Mehta Jewels"}`, bearer(out.Token))
	if created.Code != http.StatusCreated {
		t.Fatalf("create store = %d: %s", created.Code, created.Body)
	}
	var mine []MyStore
	decodeData(t, do(t, app, http.MethodGet, "/api/stores/my", bearer(out.Token)), &mine)
	if len(mine) != 1 || mine[0].Role != RoleOwner {
		t.Fatalf("stores = %+v", mine)
	}

	// Signing in again opens the same account.
	nextMinute(t, app)
	mustAsk(t, app, unknownPhone)
	again := sendPhoneCode(t, app, unknownPhone, sms.lastCode(t, app, unknownPhone), header("X-Store", mine[0].ID))
	var second authResponse
	decodeData(t, again, &second)
	if again.Code != http.StatusOK || second.Record.ID != out.Record.ID || second.Record.Role != RoleOwner {
		t.Fatalf("second sign-in = %d %+v", again.Code, second.Record)
	}
}

// As `serve` runs: no default store, and a sign-in that names none. This is what
// a visitor on the install's admin domain does.
func TestAPhoneSignsInWithNoStoreNamed(t *testing.T) {
	sms := &smsStub{}
	dsn := testdb.Clone(t, "core:"+moduleKey([]Module{sms}), func(dsn string) error {
		app, err := New(testConfig(dsn), sms)
		if err != nil {
			return err
		}
		return app.Close()
	})
	cfg := testConfig(dsn)
	cfg.DefaultStore = NoDefaultStore
	app := newApp(t, cfg, sms)
	su, err := app.Superusers().Create(WithStore(context.Background(), addTestStore(t, app, "second")), "a@example.com", "a-long-enough-password", RoleStaff)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.ExecContext(context.Background(), `UPDATE superusers SET phone = $2 WHERE id = $1`, su.ID, knownPhone); err != nil {
		t.Fatal(err)
	}

	if s := phoneState(t, app); !s["phone_login"] {
		t.Fatalf("auth-state with no store named: %v", s)
	}
	mustAsk(t, app, knownPhone)
	rec := sendPhoneCode(t, app, knownPhone, sms.lastCode(t, app, knownPhone))
	var out authResponse
	decodeData(t, rec, &out)
	if rec.Code != http.StatusOK || out.Record == nil || out.Record.Role != RoleStaff {
		t.Fatalf("status = %d, record = %+v, want the person's role in their one store", rec.Code, out.Record)
	}
}

// D191 lets anybody type anybody's number at sign-up. Whoever did must not end
// up sharing the account with the number's real owner.
func TestTheFirstCodeTakesAnAccountFromWhoeverTypedTheNumber(t *testing.T) {
	app, sms := phoneCodeSignupApp(t)
	const pass = "the stranger's password"
	rec := do(t, app, http.MethodPost, "/api/admin/signup", jsonBody(t, map[string]string{"identity": unknownPhone, "password": pass}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("sign-up = %d: %s", rec.Code, rec.Body)
	}
	var strangers authResponse
	decodeData(t, rec, &strangers)

	mustAsk(t, app, unknownPhone)
	rec = sendPhoneCode(t, app, unknownPhone, sms.lastCode(t, app, unknownPhone))
	var owners authResponse
	decodeData(t, rec, &owners)
	if rec.Code != http.StatusOK || owners.Record == nil || owners.Record.ID != strangers.Record.ID {
		t.Fatalf("the code = %d %+v", rec.Code, owners.Record)
	}
	if got := do(t, app, http.MethodGet, "/api/stores/my", bearer(strangers.Token)); got.Code != http.StatusUnauthorized {
		t.Errorf("the stranger's session = %d, want 401", got.Code)
	}
	if got := do(t, app, http.MethodGet, "/api/stores/my", bearer(owners.Token)); got.Code != http.StatusOK {
		t.Errorf("the owner's session = %d", got.Code)
	}
	if _, _, err := app.superusers.Authenticate(context.Background(), unknownPhone, pass, "198.51.100.9"); err == nil {
		t.Error("the stranger's password still opens the account")
	}

	// Once proved, a later code ends nothing: this session survives the next.
	nextMinute(t, app)
	mustAsk(t, app, unknownPhone)
	if rec := sendPhoneCode(t, app, unknownPhone, sms.lastCode(t, app, unknownPhone)); rec.Code != http.StatusOK {
		t.Fatalf("second code = %d: %s", rec.Code, rec.Body)
	}
	if got := do(t, app, http.MethodGet, "/api/stores/my", bearer(owners.Token)); got.Code != http.StatusOK {
		t.Errorf("a second sign-in ended the first: %d", got.Code)
	}
}

func TestAClosedInstallMakesNoAccountFromAPhone(t *testing.T) {
	app, sms := phoneApp(t)
	mustAsk(t, app, unknownPhone)
	app.superusers.waitForResetDelivery(5 * time.Second)
	if n := len(sms.notes()); n != 0 {
		t.Fatalf("%d texts to a number with no account, on a closed install", n)
	}
	if _, err := app.db.ExecContext(context.Background(),
		`UPDATE superuser_login_codes SET code_hash = $2 WHERE identity = $1`, unknownPhone, phoneCodeHash(unknownPhone, "654321")); err != nil {
		t.Fatal(err)
	}
	assertWrongCode(t, sendPhoneCode(t, app, unknownPhone, "654321"))
	var n int
	if err := app.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM superusers WHERE phone = $1`, unknownPhone).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("a closed install made an account")
	}
}

// A phone account has no password. No password opens it, and the screens that
// read a person must not fall over on a person with no email.
func TestAPhoneAccountWorksEverywhereAnAccountIsRead(t *testing.T) {
	app, sms := phoneCodeSignupApp(t)
	mustAsk(t, app, unknownPhone)
	var out authResponse
	decodeData(t, sendPhoneCode(t, app, unknownPhone, sms.lastCode(t, app, unknownPhone)), &out)
	token := bearer(out.Token)

	if _, _, err := app.superusers.Authenticate(context.Background(), unknownPhone, "", "198.51.100.1"); err == nil {
		t.Fatal("an empty password opened a phone account")
	}
	if rec := doBody(t, app, http.MethodPost, "/api/stores/create", `{"name":"Mehta Jewels"}`, token); rec.Code != http.StatusCreated {
		t.Fatalf("create store = %d: %s", rec.Code, rec.Body)
	}
	var mine []MyStore
	decodeData(t, do(t, app, http.MethodGet, "/api/stores/my", token), &mine)
	atStore := header("X-Store", mine[0].ID)

	var me struct {
		Superuser   *Superuser `json:"superuser"`
		HasPassword *bool      `json:"has_password"`
	}
	decodeData(t, do(t, app, http.MethodGet, "/api/admin/me", token, atStore), &me)
	if me.Superuser == nil || me.Superuser.Phone != unknownPhone || me.HasPassword == nil || *me.HasPassword {
		t.Fatalf("/me = %+v", me)
	}
	if rec := do(t, app, http.MethodGet, "/api/admin/superusers", token, atStore); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), unknownPhone) {
		t.Fatalf("team list = %d: %s", rec.Code, rec.Body)
	}
	if rec := do(t, app, http.MethodPost, "/api/admin/superusers/"+itoa(out.Record.ID)+"/password-reset", token, atStore); rec.Code >= 500 {
		t.Fatalf("a reset for a person with no email = %d: %s", rec.Code, rec.Body)
	}
	if rec := doBody(t, app, http.MethodPost, "/api/admin/me/revoke-sessions", "", token, atStore); rec.Code >= 500 {
		t.Fatalf("revoke = %d: %s", rec.Code, rec.Body)
	}
}

// Whichever row comes back first, the dummy check must be a real one, or "no
// such account" answers measurably faster than "wrong password".
func TestTheDummyCheckIgnoresAPhoneAccountsEmptyHash(t *testing.T) {
	app, _ := phoneApp(t)
	if _, err := app.db.ExecContext(context.Background(), `DELETE FROM superusers`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.superusers.signUpPhone(context.Background(), knownPhone); err != nil {
		t.Fatal(err)
	}
	newSuperuser(t, app, "a@example.com", "a-long-enough-password")
	hash := app.superusers.dummyHash(context.Background())
	if !strings.HasPrefix(hash, pbkdf2Scheme) {
		t.Fatalf("dummy hash = %q, want a real one", hash)
	}
}

func TestAnOperatorAddsAPhoneWithItsCodeAndTheirPassword(t *testing.T) {
	app, sms := phoneApp(t)
	const pass = "a-long-enough-password"
	su := newSuperuser(t, app, "a@example.com", pass)
	token := bearer(signIn(t, app, "a@example.com", pass, "198.51.100.1").Token)
	put := func(body map[string]string) *httptest.ResponseRecorder {
		return do(t, app, http.MethodPut, "/api/admin/me/phone", token, jsonBody(t, body))
	}

	if rec := do(t, app, http.MethodPost, "/api/admin/me/phone/code", token, jsonBody(t, map[string]string{"phone": knownPhone})); rec.Code != http.StatusAccepted {
		t.Fatalf("asking = %d: %s", rec.Code, rec.Body)
	}
	code := sms.lastCode(t, app, knownPhone)

	// A wrong password spends nothing, so the code still works afterwards.
	if rec := put(map[string]string{"phone": knownPhone, "code": code, "current_password": "not the password"}); rec.Code != http.StatusForbidden {
		t.Fatalf("wrong password = %d: %s", rec.Code, rec.Body)
	}
	assertWrongCode(t, put(map[string]string{"phone": knownPhone, "code": otherCode(code), "current_password": pass}))
	assertWrongCode(t, put(map[string]string{"phone": unknownPhone, "code": code, "current_password": pass}))
	if rec := put(map[string]string{"phone": knownPhone, "code": code, "current_password": pass}); rec.Code != http.StatusOK {
		t.Fatalf("adding = %d: %s", rec.Code, rec.Body)
	}

	// The phone now opens this account.
	nextMinute(t, app)
	mustAsk(t, app, knownPhone)
	var out authResponse
	decodeData(t, sendPhoneCode(t, app, knownPhone, sms.lastCode(t, app, knownPhone)), &out)
	if out.Record == nil || out.Record.ID != su.ID {
		t.Fatalf("the phone opened %+v", out.Record)
	}

	// A number that opens another account is not taken from it.
	other := newSuperuser(t, app, "b@example.com", pass)
	otherToken := bearer(signIn(t, app, "b@example.com", pass, "198.51.100.2").Token)
	nextMinute(t, app)
	if rec := do(t, app, http.MethodPost, "/api/admin/me/phone/code", otherToken, jsonBody(t, map[string]string{"phone": knownPhone})); rec.Code != http.StatusAccepted {
		t.Fatalf("asking = %d: %s", rec.Code, rec.Body)
	}
	rec := do(t, app, http.MethodPut, "/api/admin/me/phone", otherToken, jsonBody(t,
		map[string]string{"phone": knownPhone, "code": sms.lastCode(t, app, knownPhone), "current_password": pass}))
	if rec.Code != http.StatusConflict {
		t.Fatalf("taking a number = %d: %s", rec.Code, rec.Body)
	}
	_ = other

	// Removing it needs the password, and then the phone opens nothing.
	del := func(password string) *httptest.ResponseRecorder {
		return do(t, app, http.MethodDelete, "/api/admin/me/phone", token, jsonBody(t, map[string]string{"current_password": password}))
	}
	if rec := del("not the password"); rec.Code != http.StatusForbidden {
		t.Fatalf("removing with a wrong password = %d", rec.Code)
	}
	if rec := del(pass); rec.Code != http.StatusOK {
		t.Fatalf("removing = %d: %s", rec.Code, rec.Body)
	}
	nextMinute(t, app)
	before := len(sms.notes())
	mustAsk(t, app, knownPhone)
	app.superusers.waitForResetDelivery(5 * time.Second)
	if len(sms.notes()) != before {
		t.Fatal("a removed phone was texted a code")
	}
}

func TestATeammatesPhoneIsMaskedOnADemo(t *testing.T) {
	su := &Superuser{Phone: "+919876543210"}
	if got := MaskPhone(su.Phone); strings.Contains(got, "9876543210") || !strings.HasSuffix(got, "10") {
		t.Fatalf("MaskPhone = %q", got)
	}
	if got := MaskPhoneIn("refused 919876543210 and +919876543210", su.Phone); strings.Contains(got, "9876543210") {
		t.Fatalf("MaskPhoneIn = %q", got)
	}
}

func TestTheSignInTemplateIsTheEnginesOwn(t *testing.T) {
	app := newTestApp(t)
	tpl, ok, err := app.NotifyTemplates().GetIn(WithStore(context.Background(), installScope), ChannelSMS, EventLoginCode, "en")
	if err != nil || !ok {
		t.Fatalf("template = %v, %v: an install with no identity module could not word a code", ok, err)
	}
	body, err := RenderNotifyText(tpl.Body, map[string]string{"code": "123456", "store_name": "ShopNx"})
	if err != nil || !strings.Contains(body, "123456") || !strings.Contains(body, "ShopNx") {
		t.Fatalf("body = %q, %v", body, err)
	}
}

// Support moves somebody who lost their SIM to a new number (D197): the new
// number signs in at once, and whatever the old SIM's holder had open ends.
func TestSupportChangesALostPhone(t *testing.T) {
	p := phonePerson(t)
	ctx := context.Background()
	const fresh = "+919876512345"
	if err := p.app.superusers.ChangePhone(ctx, unknownPhone, "+91 98765 12345"); err != nil {
		t.Fatalf("change: %v", err)
	}
	if rec := do(t, p.app, http.MethodGet, "/api/stores/my", p.token); rec.Code != http.StatusUnauthorized {
		t.Errorf("the old session = %d, want it ended", rec.Code)
	}
	su, err := p.app.superusers.personOnPhone(ctx, fresh)
	if err != nil || su == nil || su.ID != p.id || !su.phoneProved {
		t.Fatalf("new number = %+v, %v; want person %d, proved", su, err, p.id)
	}
	if other, _ := p.app.superusers.personOnPhone(ctx, unknownPhone); other != nil {
		t.Errorf("the old number still opens %+v", other)
	}
	if err := p.app.superusers.ChangePhone(ctx, "nobody@example.com", "+919876512346"); err == nil {
		t.Error("changing nobody's phone succeeded")
	}
	newSuperuser(t, p.app, "other@example.com", "another good password")
	if err := p.app.superusers.ChangePhone(ctx, "other@example.com", fresh); err == nil {
		t.Error("took a number from the person who has it")
	}
}

// Many numbers from many addresses cannot run up the install's SMS bill.
func TestTheInstallStopsTextingCodesAtItsDailyLimit(t *testing.T) {
	app, _ := phoneApp(t)
	app.cfg.SMSCodesPerDay = 3
	number := func(i int) string { return fmt.Sprintf("+91987651%04d", i) }
	client := func(i int) string { return fmt.Sprintf("198.51.100.%d", i+1) }
	if got := parallelAsks(t, app, number, 8, client); got != 3 {
		t.Fatalf("%d codes stored, want 3", got)
	}
	if rec := askPhoneCode(t, app, "+919876599999", fromIP("198.51.100.200")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("past the limit = %d: %s", rec.Code, rec.Body)
	}
}
