package gocommerce

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The password-reset suite.
//
// Two properties carry the whole design and each has a test whose name says so:
// the request route must answer identically for an address that belongs to an
// operator and one that does not, and the link must be built from configuration
// rather than from headers the caller controls. Everything else — single use,
// every session ended, siblings dropped on spend, a throttle that is not the
// login one — is a rule somebody would otherwise simplify away.

// ---------------------------------------------------------------- fixtures

// resetApp boots an engine with a real (recording) email backend, which is what
// separates "a reset can be delivered" from "a line will be logged".
func resetApp(t *testing.T) (*App, *recordingNotifier) {
	t.Helper()
	rec := &recordingNotifier{}
	return newTestApp(t, &notifyModule{rec: rec}), rec
}

// resetNotes drains the delivery goroutines and returns the reset emails.
func resetNotes(t *testing.T, app *App, rec *recordingNotifier) []Notification {
	t.Helper()
	app.Superusers().waitForResetDelivery(5 * time.Second)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out []Notification
	for _, n := range rec.sent {
		if n.Event == EventSuperuserPasswordReset {
			out = append(out, n)
		}
	}
	return out
}

// resetToken asks for a link and returns the token that was mailed.
func resetToken(t *testing.T, app *App, rec *recordingNotifier, email string) string {
	t.Helper()
	before := len(resetNotes(t, app, rec))
	if _, err := app.Superusers().RequestReset(context.Background(), email, "10.0.0.9"); err != nil {
		t.Fatalf("request reset for %s: %v", email, err)
	}
	notes := resetNotes(t, app, rec)
	if len(notes) != before+1 {
		t.Fatalf("got %d reset notifications, want %d", len(notes), before+1)
	}
	token := notes[len(notes)-1].Data["reset_token"]
	if token == "" {
		t.Fatal("the notification carries no reset_token")
	}
	return token
}

func resetRows(t *testing.T, app *App, superuserID int64) int {
	t.Helper()
	var n int
	query := `SELECT count(*) FROM superuser_password_resets`
	args := []any{}
	if superuserID != 0 {
		query += ` WHERE superuser_id = $1`
		args = append(args, superuserID)
	}
	if err := app.db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count reset rows: %v", err)
	}
	return n
}

// ------------------------------------------------------------- enumeration

// The whole contract of the request route: an address that belongs to an
// operator and one that does not are indistinguishable in status and in body.
func TestAResetRequestAnswersTheSameForAStrangerAsForAnOperator(t *testing.T) {
	app, rec := resetApp(t)
	su := newSuperuser(t, app, "known@example.com", "a good password")

	known := do(t, app, http.MethodPost, "/api/admin/password-reset",
		jsonBody(t, map[string]string{"identity": "known@example.com"}), fromIP("10.0.0.1"))
	stranger := do(t, app, http.MethodPost, "/api/admin/password-reset",
		jsonBody(t, map[string]string{"identity": "nobody@example.com"}), fromIP("10.0.0.2"))

	if known.Code != http.StatusAccepted || stranger.Code != http.StatusAccepted {
		t.Fatalf("status: known %d, stranger %d, want %d for both",
			known.Code, stranger.Code, http.StatusAccepted)
	}
	if known.Body.String() != stranger.Body.String() {
		t.Errorf("the two answers differ:\n known    %s\n stranger %s", known.Body, stranger.Body)
	}

	// And the work really was done for the one address that has an account.
	if n := resetRows(t, app, su.ID); n != 1 {
		t.Errorf("%d reset rows for the real operator, want 1", n)
	}
	if n := resetRows(t, app, 0); n != 1 {
		t.Errorf("%d reset rows in total, want 1 — an unknown address must write nothing", n)
	}
	if notes := resetNotes(t, app, rec); len(notes) != 1 || notes[0].To != "known@example.com" {
		t.Errorf("notifications = %+v, want exactly one to known@example.com", notes)
	}
}

// The HTTP-level statement of the same contract, plus the two failures that are
// facts about the request rather than about who has an account.
func TestResetRoutesAnswerTheSameWhoeverAsks(t *testing.T) {
	app, _ := resetApp(t)
	newSuperuser(t, app, "real@example.com", "a good password")

	for _, identity := range []string{"real@example.com", "ghost@example.com"} {
		rec := do(t, app, http.MethodPost, "/api/admin/password-reset",
			jsonBody(t, map[string]string{"identity": identity}), fromIP("10.1.0.1"))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s: status %d, want 202", identity, rec.Code)
		}
		var out ResetRequest
		decodeData(t, rec, &out)
		if !out.Accepted || out.Delivery != DeliveryEmail {
			t.Errorf("%s: got %+v, want accepted with delivery %q", identity, out, DeliveryEmail)
		}
	}

	bad := do(t, app, http.MethodPost, "/api/admin/password-reset",
		jsonBody(t, map[string]string{"identity": "not-an-email"}), fromIP("10.1.0.2"))
	if bad.Code != http.StatusBadRequest {
		t.Errorf("a malformed address gave %d, want 400", bad.Code)
	}

	rubbish := do(t, app, http.MethodPost, "/api/admin/password-reset/confirm",
		jsonBody(t, map[string]string{"token": "nonsense", "password": "a good password"}),
		fromIP("10.1.0.3"))
	if rubbish.Code != http.StatusBadRequest {
		t.Errorf("an unknown token gave %d, want 400", rubbish.Code)
	}
	if code := decodeError(t, rubbish).Code; code != "invalid_token" {
		t.Errorf("error code %q, want invalid_token", code)
	}
}

// --------------------------------------------------------------- the token

// The mirror of TestAnInvitationIsTheOnlyTimeTheTokenExists: the database holds
// the hash and nothing else, and no outbox row carries the secret.
func TestAResetLinkIsTheOnlyPlaceTheTokenExists(t *testing.T) {
	app, rec := resetApp(t)
	su := newSuperuser(t, app, "locked@example.com", "a good password")

	token := resetToken(t, app, rec, "locked@example.com")
	notes := resetNotes(t, app, rec)
	note := notes[len(notes)-1]

	if note.Channel != ChannelEmail || note.To != "locked@example.com" {
		t.Errorf("notification = %+v, want an email to the operator", note)
	}
	if got := note.Data["expires_in_minutes"]; got != "60" {
		t.Errorf("expires_in_minutes = %q, want 60", got)
	}
	if note.Data["operator_email"] != "locked@example.com" {
		t.Errorf("operator_email = %q", note.Data["operator_email"])
	}

	var stored string
	if err := app.db.QueryRowContext(context.Background(),
		`SELECT token_hash FROM superuser_password_resets WHERE superuser_id = $1`,
		su.ID).Scan(&stored); err != nil {
		t.Fatalf("read the stored reset: %v", err)
	}
	if stored == token {
		t.Fatal("the plaintext token is in the table; a leaked database would hand out working links")
	}
	if stored != hashToken(token) {
		t.Errorf("stored hash does not match the token that was sent")
	}

	// The guard on the no-outbox decision. Outbox rows are never deleted, so an
	// event carrying this token would keep a live credential forever.
	var leaked int
	if err := app.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM outbox_events WHERE payload::text LIKE '%' || $1 || '%'`,
		token).Scan(&leaked); err != nil {
		t.Fatalf("search the outbox: %v", err)
	}
	if leaked != 0 {
		t.Errorf("%d outbox rows carry the reset token; this flow must not use the outbox", leaked)
	}
}

// The security test for the whole design. It fails loudly if anybody reroutes
// the link through acceptURL "for consistency".
func TestTheResetLinkComesFromConfigurationNotTheRequest(t *testing.T) {
	dsn := requireDB(t)
	rec := &recordingNotifier{}
	cfg := testConfig(dsn)
	cfg.PanelURL = "https://shop.example/"
	app, err := New(cfg, &notifyModule{rec: rec})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	newSuperuser(t, app, "owner@example.com", "a good password")

	do(t, app, http.MethodPost, "/api/admin/password-reset",
		jsonBody(t, map[string]string{"identity": "owner@example.com"}),
		header("X-Forwarded-Host", "evil.example"),
		header("Host", "evil.example"),
		func(r *http.Request) { r.Host = "evil.example" })

	notes := resetNotes(t, app, rec)
	if len(notes) != 1 {
		t.Fatalf("got %d notifications, want 1", len(notes))
	}
	link := notes[0].Data["reset_url"]
	if !strings.HasPrefix(link, "https://shop.example/reset-password/") {
		t.Errorf("reset_url = %q, want it built from Config.PanelURL", link)
	}
	for key, value := range notes[0].Data {
		if strings.Contains(value, "evil.example") {
			t.Errorf("%s carries the attacker's host: %q", key, value)
		}
	}
}

// With no PanelURL the email carries a code to paste, and says so by leaving
// reset_url out — templates branch on its absence.
func TestWithNoPanelURLTheEmailCarriesACodeAndNoLink(t *testing.T) {
	app, rec := resetApp(t)
	newSuperuser(t, app, "owner@example.com", "a good password")

	resetToken(t, app, rec, "owner@example.com")
	note := resetNotes(t, app, rec)[0]

	if _, ok := note.Data["reset_url"]; ok {
		t.Errorf("reset_url is present with no Config.PanelURL: %q", note.Data["reset_url"])
	}
	if note.Data["reset_token"] == "" {
		t.Error("there is no reset_token either, so the email is useless")
	}
}

// End to end through the link itself: the URL the operator clicks and the hash
// the table holds have to agree.
func TestTheResetEmailLinkActuallyWorks(t *testing.T) {
	dsn := requireDB(t)
	rec := &recordingNotifier{}
	cfg := testConfig(dsn)
	cfg.PanelURL = "https://shop.example"
	app, err := New(cfg, &notifyModule{rec: rec})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	newSuperuser(t, app, "owner@example.com", "the old password")
	if _, err := app.Superusers().RequestReset(context.Background(),
		"owner@example.com", "10.0.0.1"); err != nil {
		t.Fatalf("request reset: %v", err)
	}
	link := resetNotes(t, app, rec)[0].Data["reset_url"]
	token := link[strings.LastIndex(link, "/")+1:]

	confirm := do(t, app, http.MethodPost, "/api/admin/password-reset/confirm",
		jsonBody(t, map[string]string{"token": token, "password": "the new password"}),
		fromIP("10.0.0.1"))
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm from the emailed link: %d (%s)", confirm.Code, confirm.Body)
	}
	if _, _, err := app.Superusers().Authenticate(context.Background(),
		"owner@example.com", "the new password", "10.0.0.2"); err != nil {
		t.Errorf("sign in with the password set through the link: %v", err)
	}
}

// -------------------------------------------------------------- no delivery

// A link nobody can be sent is an hour of live credential bought for nothing.
func TestNoEmailDeliveryMeansNoTokenIsStored(t *testing.T) {
	app := newTestApp(t) // the built-in logger and nothing else
	newSuperuser(t, app, "alone@example.com", "a good password")

	out, err := app.Superusers().RequestReset(context.Background(), "alone@example.com", "10.0.0.1")
	if err != nil {
		t.Fatalf("request reset: %v", err)
	}
	if out.Delivery != DeliveryNone {
		t.Errorf("delivery = %q, want %q", out.Delivery, DeliveryNone)
	}
	if n := resetRows(t, app, 0); n != 0 {
		t.Errorf("%d reset rows written when nothing can be delivered, want 0", n)
	}

	rec := do(t, app, http.MethodGet, "/api/admin/auth-state")
	var state struct {
		PasswordReset bool `json:"password_reset"`
	}
	decodeData(t, rec, &state)
	if state.PasswordReset {
		t.Error("auth-state claims a reset can be delivered when only the logger is installed")
	}
}

// The one thing that would make the panel's warning a lie: counting notifiers
// instead of looking past the built-in logger.
func TestAuthStateReportsRealDeliveryOnly(t *testing.T) {
	plain := newTestApp(t)
	withEmail, _ := resetApp(t)

	read := func(app *App) bool {
		rec := do(t, app, http.MethodGet, "/api/admin/auth-state")
		var state struct {
			PasswordReset bool `json:"password_reset"`
		}
		decodeData(t, rec, &state)
		return state.PasswordReset
	}

	if read(plain) {
		t.Error("password_reset is true with only the built-in logger registered")
	}
	if !read(withEmail) {
		t.Error("password_reset is false with a real email notifier registered")
	}
}

// ------------------------------------------------------------- spending one

func TestAResetLinkWorksOnceAndSignsTheOperatorIn(t *testing.T) {
	app, rec := resetApp(t)
	newSuperuser(t, app, "locked@example.com", "the old password")
	token := resetToken(t, app, rec, "locked@example.com")

	ctx := context.Background()
	su, sess, err := app.Superusers().ConfirmReset(ctx, token, "the new password", "10.0.0.1")
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if su.Email != "locked@example.com" {
		t.Errorf("confirmed as %q", su.Email)
	}
	if _, ok := app.Superusers().Resolve(ctx, sess.Token); !ok {
		t.Error("the session handed back by the confirm does not resolve")
	}
	if _, _, err := app.Superusers().Authenticate(ctx, "locked@example.com", "the new password", "10.0.0.2"); err != nil {
		t.Errorf("the new password does not work: %v", err)
	}
	if _, _, err := app.Superusers().Authenticate(ctx, "locked@example.com", "the old password", "10.0.0.3"); err == nil {
		t.Error("the old password still works after a reset")
	}

	if _, _, err := app.Superusers().ConfirmReset(ctx, token, "another password", "10.0.0.1"); err == nil {
		t.Error("the same link worked twice")
	}
	if n := resetRows(t, app, 0); n != 0 {
		t.Errorf("%d reset rows survive a spent link, want 0", n)
	}
}

// The opposite of changing your own password, which keeps this browser. A reset
// is how somebody answers a compromise, so nothing that predates it survives.
func TestConfirmingAResetEndsEverySession(t *testing.T) {
	app, rec := resetApp(t)
	newSuperuser(t, app, "locked@example.com", "the old password")
	ctx := context.Background()

	first := signIn(t, app, "locked@example.com", "the old password", "10.0.0.1")
	second := signIn(t, app, "locked@example.com", "the old password", "10.0.0.2")

	token := resetToken(t, app, rec, "locked@example.com")
	_, fresh, err := app.Superusers().ConfirmReset(ctx, token, "the new password", "10.0.0.3")
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}

	for name, sess := range map[string]*Session{"first": first, "second": second} {
		if _, ok := app.Superusers().Resolve(ctx, sess.Token); ok {
			t.Errorf("the %s session survived the reset", name)
		}
	}
	if _, ok := app.Superusers().Resolve(ctx, fresh.Token); !ok {
		t.Error("the session issued by the reset does not resolve")
	}
}

// Validation happens before the token is touched, so a typo does not burn the
// one link the operator has.
func TestAShortPasswordDoesNotSpendTheLink(t *testing.T) {
	app, rec := resetApp(t)
	su := newSuperuser(t, app, "locked@example.com", "the old password")
	token := resetToken(t, app, rec, "locked@example.com")
	ctx := context.Background()

	_, _, err := app.Superusers().ConfirmReset(ctx, token, "short", "10.0.0.1")
	if err == nil {
		t.Fatal("a five-character password was accepted")
	}
	if n := resetRows(t, app, su.ID); n != 1 {
		t.Fatalf("%d reset rows after a rejected password, want the link to still be there", n)
	}
	if _, _, err := app.Superusers().ConfirmReset(ctx, token, "a good password", "10.0.0.1"); err != nil {
		t.Errorf("the link no longer works after a rejected password: %v", err)
	}
}

// Expiry is the predicate in the claim, not the sweeper: the row is still there
// and the answer is still no.
func TestAnExpiredResetLinkIsRefused(t *testing.T) {
	app, rec := resetApp(t)
	su := newSuperuser(t, app, "locked@example.com", "the old password")
	token := resetToken(t, app, rec, "locked@example.com")
	ctx := context.Background()

	if _, err := app.db.ExecContext(ctx,
		`UPDATE superuser_password_resets
		 SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatalf("age the reset row: %v", err)
	}

	_, _, err := app.Superusers().ConfirmReset(ctx, token, "a good password", "10.0.0.1")
	if err == nil {
		t.Fatal("an expired link was accepted")
	}
	var expired, unknown *APIError
	if !errors.As(err, &expired) {
		t.Fatalf("error is not an APIError: %v", err)
	}
	_, _, err = app.Superusers().ConfirmReset(ctx, "nonsense", "a good password", "10.0.0.1")
	if !errors.As(err, &unknown) {
		t.Fatalf("error is not an APIError: %v", err)
	}
	if expired.Message != unknown.Message || expired.Code != unknown.Code {
		t.Errorf("an expired link says %q and an unknown one says %q; they must be the same",
			expired.Message, unknown.Message)
	}
	if n := resetRows(t, app, su.ID); n != 1 {
		t.Errorf("%d rows after refusing an expired link — expiry must be the predicate, not a delete", n)
	}
}

// Spending one link closes the door on the others: revoking the one you can see
// has to mean something.
func TestSpendingOneLinkKillsTheOthers(t *testing.T) {
	app, rec := resetApp(t)
	su := newSuperuser(t, app, "locked@example.com", "the old password")

	first := resetToken(t, app, rec, "locked@example.com")
	second := resetToken(t, app, rec, "locked@example.com")
	if n := resetRows(t, app, su.ID); n != 2 {
		t.Fatalf("%d live links, want 2 — asking twice must not supersede", n)
	}

	ctx := context.Background()
	if _, _, err := app.Superusers().ConfirmReset(ctx, second, "a good password", "10.0.0.1"); err != nil {
		t.Fatalf("confirm with the second link: %v", err)
	}
	if _, _, err := app.Superusers().ConfirmReset(ctx, first, "another password", "10.0.0.1"); err == nil {
		t.Error("the older link still works after one was spent")
	}
	if n := resetRows(t, app, su.ID); n != 0 {
		t.Errorf("%d rows left, want 0", n)
	}
}

// The denial of service this design refuses: an unauthenticated attacker must
// not be able to invalidate the link already sitting in a victim's mailbox.
func TestAskingAgainDoesNotKillTheLinkAlreadySent(t *testing.T) {
	app, rec := resetApp(t)
	newSuperuser(t, app, "victim@example.com", "the old password")

	first := resetToken(t, app, rec, "victim@example.com")
	resetToken(t, app, rec, "victim@example.com")

	if _, _, err := app.Superusers().ConfirmReset(context.Background(),
		first, "a good password", "10.0.0.1"); err != nil {
		t.Errorf("the link already sent stopped working when somebody asked again: %v", err)
	}
}

// ------------------------------------------------------- what else ends one

func TestChangingAPasswordOrEmailKillsAnOutstandingResetLink(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		act  func(t *testing.T, app *App, su *Superuser)
	}{
		{"a password change", func(t *testing.T, app *App, su *Superuser) {
			if _, err := app.Superusers().Update(ctx, su.ID, "", "a replacement password"); err != nil {
				t.Fatalf("update password: %v", err)
			}
		}},
		{"an email change", func(t *testing.T, app *App, su *Superuser) {
			if _, err := app.Superusers().Update(ctx, su.ID, "moved@example.com", ""); err != nil {
				t.Fatalf("update email: %v", err)
			}
		}},
		{"changing your own password", func(t *testing.T, app *App, su *Superuser) {
			sess := signIn(t, app, su.Email, "the old password", "10.0.0.1")
			if _, err := app.Superusers().UpdateSelf(ctx, su.ID,
				"the old password", "", "a replacement password", sess.Token); err != nil {
				t.Fatalf("update self: %v", err)
			}
		}},
		{"revoking every session", func(t *testing.T, app *App, su *Superuser) {
			signIn(t, app, su.Email, "the old password", "10.0.0.1")
			n, err := app.Superusers().RevokeAll(ctx, su.ID)
			if err != nil {
				t.Fatalf("revoke all: %v", err)
			}
			// The regression guard on the count: it says sessions, and the
			// reset rows deleted beside them must not be added to it.
			if n != 1 {
				t.Errorf("RevokeAll returned %d, want 1 — the count is sessions only", n)
			}
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app, rec := resetApp(t)
			su := newSuperuser(t, app, "locked@example.com", "the old password")
			token := resetToken(t, app, rec, "locked@example.com")

			c.act(t, app, su)

			if n := resetRows(t, app, su.ID); n != 0 {
				t.Errorf("%d reset links survive %s", n, c.name)
			}
			if _, _, err := app.Superusers().ConfirmReset(ctx, token, "a good password", "10.0.0.2"); err == nil {
				t.Errorf("the reset link still works after %s", c.name)
			}
		})
	}
}

// ON DELETE CASCADE rather than SET NULL: an outstanding reset for an operator
// who no longer exists is not history, it is a live key to an account that is gone.
func TestDeletingAnOperatorTakesTheirResetLinkWithThem(t *testing.T) {
	app, rec := resetApp(t)
	newSuperuser(t, app, "staying@example.com", "a good password")
	going := newSuperuser(t, app, "going@example.com", "a good password")

	resetToken(t, app, rec, "going@example.com")
	if err := app.Superusers().Delete(context.Background(), going.ID); err != nil {
		t.Fatalf("delete superuser: %v", err)
	}
	if n := resetRows(t, app, 0); n != 0 {
		t.Errorf("%d reset rows outlive the operator they belong to", n)
	}
}

// The test that justifies the separate table: the two token namespaces cannot
// cross, and the redeemer that INSERTs superusers is not reachable with a reset
// token.
func TestAResetTokenIsNotAnInvitation(t *testing.T) {
	app, rec := resetApp(t)
	newSuperuser(t, app, "owner@example.com", "a good password")
	token := resetToken(t, app, rec, "owner@example.com")

	inv, err := app.Team().Invite(context.Background(), "newcomer@example.com", RoleStaff, nil)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}

	asInvitation := do(t, app, http.MethodPost, "/api/admin/invitations/accept/"+token,
		jsonBody(t, map[string]string{"password": "a good password"}), fromIP("10.0.0.1"))
	if asInvitation.Code == http.StatusCreated || asInvitation.Code == http.StatusOK {
		t.Fatalf("a reset token was redeemed as an invitation (%d)", asInvitation.Code)
	}

	asReset := do(t, app, http.MethodPost, "/api/admin/password-reset/confirm",
		jsonBody(t, map[string]string{"token": inv.Token, "password": "a good password"}),
		fromIP("10.0.0.1"))
	if asReset.Code != http.StatusBadRequest {
		t.Errorf("an invitation token was accepted as a reset (%d)", asReset.Code)
	}
}

// ---------------------------------------------------------------- throttles

// Both directions in one test, because sharing the login throttle breaks in
// both: a reset request would clear a failed-login budget, and a burst of reset
// requests would lock an address out of the login it is trying to recover.
func TestTheResetThrottleIsNotTheLoginThrottle(t *testing.T) {
	app, _ := resetApp(t)
	newSuperuser(t, app, "alice@example.com", "the right password")
	ctx := context.Background()

	// (a) Somebody who has just failed five logins is exactly who needs a link.
	for i := 0; i <= throttleFreeAttempts; i++ {
		_, _, _ = app.Superusers().Authenticate(ctx, "alice@example.com", "wrong", "10.0.0.1")
	}
	rec := do(t, app, http.MethodPost, "/api/admin/password-reset",
		jsonBody(t, map[string]string{"identity": "alice@example.com"}), fromIP("10.0.0.1"))
	if rec.Code != http.StatusAccepted {
		t.Errorf("a reset request after five failed logins gave %d, want 202", rec.Code)
	}

	// (b) And a burst of reset requests must not spend the login budget. The
	// spray bucket is the one that would be charged, so the count has to clear
	// throttleSprayAttempts rather than throttleFreeAttempts — a test calibrated
	// at six would pass while the hole stood.
	app2, _ := resetApp(t)
	newSuperuser(t, app2, "bob@example.com", "the right password")
	for i := 0; i <= throttleSprayAttempts; i++ {
		// A different address each time, so the per-address bucket is not what
		// stops us before the spray bucket is full.
		do(t, app2, http.MethodPost, "/api/admin/password-reset",
			jsonBody(t, map[string]string{"identity": "nobody" + string(rune('a'+i%26)) + "@example.com"}),
			fromIP("10.0.0.2"))
	}
	if _, _, err := app2.Superusers().Authenticate(ctx, "bob@example.com", "the right password", "10.0.0.2"); err != nil {
		t.Errorf("the correct password was refused after reset requests from the same address: %v", err)
	}
}

func TestRepeatedResetRequestsAreThrottled(t *testing.T) {
	app, _ := resetApp(t)
	newSuperuser(t, app, "alice@example.com", "a good password")

	request := func() *httptest.ResponseRecorder {
		return do(t, app, http.MethodPost, "/api/admin/password-reset",
			jsonBody(t, map[string]string{"identity": "alice@example.com"}), fromIP("10.0.0.7"))
	}
	// Every request is an unconditional failure — there is no outcome that
	// vouches for the requester — so the budget runs out after the free ones.
	for i := range throttleFreeAttempts + 1 {
		if rec := request(); rec.Code != http.StatusAccepted {
			t.Fatalf("request %d gave %d, want 202: %s", i+1, rec.Code, rec.Body)
		}
	}
	rec := request()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429: %s", rec.Code, rec.Body)
	}
	if code := decodeError(t, rec).Code; code != "too_many_attempts" {
		t.Errorf("code = %q, want too_many_attempts", code)
	}
}

func TestGuessingAResetTokenIsThrottled(t *testing.T) {
	app, rec := resetApp(t)
	newSuperuser(t, app, "locked@example.com", "the old password")
	token := resetToken(t, app, rec, "locked@example.com")

	guess := func(ip string) *httptest.ResponseRecorder {
		return do(t, app, http.MethodPost, "/api/admin/password-reset/confirm",
			jsonBody(t, map[string]string{"token": "rubbish", "password": "a good password"}),
			fromIP(ip))
	}
	for i := range throttleFreeAttempts + 1 {
		if r := guess("10.0.0.8"); r.Code != http.StatusBadRequest {
			t.Fatalf("guess %d gave %d, want the ordinary refusal: %s", i+1, r.Code, r.Body)
		}
	}
	if r := guess("10.0.0.8"); r.Code != http.StatusTooManyRequests {
		t.Errorf("guessing tokens was never throttled; status %d", r.Code)
	}

	// And a real link from somewhere else still works, because the budget is
	// keyed on the address the guesses came from.
	good := do(t, app, http.MethodPost, "/api/admin/password-reset/confirm",
		jsonBody(t, map[string]string{"token": token, "password": "a good password"}),
		fromIP("10.0.0.9"))
	if good.Code != http.StatusOK {
		t.Errorf("a valid link from another address gave %d, want 200 (%s)", good.Code, good.Body)
	}
}

// ------------------------------------------------------------------ lookup

// The deliberate departure from the invitation lookup: enough to show a dead
// link before anybody types a password twice, and nothing that names the account.
func TestLookingUpAResetLinkSaysWhenItDiesAndNothingElse(t *testing.T) {
	app, rec := resetApp(t)
	newSuperuser(t, app, "locked@example.com", "the old password")
	token := resetToken(t, app, rec, "locked@example.com")

	live := do(t, app, http.MethodGet, "/api/admin/password-reset/"+token, fromIP("10.0.0.1"))
	if live.Code != http.StatusOK {
		t.Fatalf("lookup of a live link gave %d (%s)", live.Code, live.Body)
	}
	body := live.Body.String()
	for _, forbidden := range []string{"locked@example.com", "\"email\"", "\"id\"", "\"role\""} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the lookup body contains %s: %s", forbidden, body)
		}
	}
	var target ResetTarget
	decodeData(t, live, &target)
	if target.ExpiresAt.IsZero() || time.Until(target.ExpiresAt) > passwordResetTTL+time.Minute {
		t.Errorf("expires_at = %s, want about an hour out", target.ExpiresAt)
	}

	dead := do(t, app, http.MethodGet, "/api/admin/password-reset/rubbish", fromIP("10.0.0.1"))
	if dead.Code != http.StatusBadRequest {
		t.Errorf("lookup of an unknown token gave %d, want 400", dead.Code)
	}
	if code := decodeError(t, dead).Code; code != "invalid_token" {
		t.Errorf("error code %q, want invalid_token", code)
	}
}

// ------------------------------------------------------------- the team door

func TestTheTeamCanMailAResetLink(t *testing.T) {
	app, rec := resetApp(t)
	target := newSuperuser(t, app, "forgetful@example.com", "a good password")

	sent := do(t, app, http.MethodPost,
		"/api/admin/superusers/"+strconv.FormatInt(target.ID, 10)+"/password-reset", withAdmin)
	if sent.Code != http.StatusAccepted {
		t.Fatalf("owner-sent reset gave %d (%s)", sent.Code, sent.Body)
	}
	notes := resetNotes(t, app, rec)
	if len(notes) != 1 || notes[0].To != "forgetful@example.com" {
		t.Fatalf("notifications = %+v, want one to the operator named", notes)
	}

	// Staff cannot: this is team management, and team.write is the gate.
	if _, err := app.Superusers().Create(context.Background(),
		"staff@example.com", "a good password", RoleStaff); err != nil {
		t.Fatalf("create staff: %v", err)
	}
	staff := signIn(t, app, "staff@example.com", "a good password", "10.0.0.1")
	refused := do(t, app, http.MethodPost,
		"/api/admin/superusers/"+strconv.FormatInt(target.ID, 10)+"/password-reset", bearer(staff.Token))
	if refused.Code != http.StatusForbidden {
		t.Errorf("staff got %d, want 403", refused.Code)
	}
	if msg := decodeError(t, refused).Message; !strings.Contains(msg, string(RightTeamWrite)) {
		t.Errorf("the refusal does not name team.write: %q", msg)
	}

	missing := do(t, app, http.MethodPost, "/api/admin/superusers/999999/password-reset", withAdmin)
	if missing.Code != http.StatusNotFound {
		t.Errorf("an unknown id gave %d, want 404", missing.Code)
	}
}
