package gocommerce

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// The notification log is open to anybody who may read orders, and some of
// what the store sends is a key to somebody's account: a password reset, an
// invitation. The log shows those messages with the key hidden — to staff, to
// an owner, to the static admin token alike — while the email the person got
// still carries the live link and the link still works.

// logReader is somebody who may open the notification log.
type logReader struct {
	who  string
	auth func(*http.Request)
}

func logReaders(t *testing.T, app *App) []logReader {
	t.Helper()
	return []logReader{
		{"staff", bearer(signInAs(t, app, "log-staff@example.com", RoleStaff))},
		{"an owner", bearer(signInAs(t, app, "log-owner@example.com", RoleOwner))},
		{"the admin token", withAdmin},
	}
}

// loggedData is what the log shows reader for the newest row of event.
func loggedData(t *testing.T, app *App, reader logReader, event string) map[string]string {
	t.Helper()
	rows := listLogged(t, app, reader, event)
	return rows[0].Data
}

func listLogged(t *testing.T, app *App, reader logReader, event string) []NotificationRecord {
	t.Helper()
	rec := do(t, app, http.MethodGet, "/api/admin/notifications?event="+url.QueryEscape(event), reader.auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s listing %s = %d: %s", reader.who, event, rec.Code, rec.Body)
	}
	var body struct {
		Data []NotificationRecord `json:"data"`
	}
	decodeJSONBody(t, rec.Body.Bytes(), &body)
	if len(body.Data) == 0 {
		t.Fatalf("%s found no %s in the log: the test has nothing to look at", reader.who, event)
	}
	return body.Data
}

// assertLogHides reads every row of event the ways the panel does — the list,
// the row, the preview — as each reader, and fails on any secret it finds.
// Decoded values are searched rather than raw bodies: a six-digit code could
// otherwise turn up by chance in a timestamp's fraction of a second.
func assertLogHides(t *testing.T, app *App, readers []logReader, event string, secrets ...string) {
	t.Helper()
	for _, reader := range readers {
		for _, row := range listLogged(t, app, reader, event) {
			showsNone(t, reader.who+" listing "+event, row, secrets)
			id := strconv.FormatInt(row.ID, 10)

			one := do(t, app, http.MethodGet, "/api/admin/notifications/"+id, reader.auth)
			if one.Code != http.StatusOK {
				t.Fatalf("%s reading %s = %d: %s", reader.who, id, one.Code, one.Body)
			}
			var got struct {
				Data NotificationRecord `json:"data"`
			}
			decodeJSONBody(t, one.Body.Bytes(), &got)
			showsNone(t, reader.who+" reading "+event, got.Data, secrets)

			pv := do(t, app, http.MethodGet, "/api/admin/notifications/"+id+"/preview", reader.auth)
			if pv.Code != http.StatusOK {
				t.Fatalf("%s previewing %s = %d: %s", reader.who, id, pv.Code, pv.Body)
			}
			var preview struct {
				Data NotificationPreview `json:"data"`
			}
			decodeJSONBody(t, pv.Body.Bytes(), &preview)
			for _, s := range secrets {
				if s != "" && (strings.Contains(preview.Data.Subject, s) || strings.Contains(preview.Data.Body, s)) {
					t.Errorf("%s previewing %s sees %q:\n%s", reader.who, event, s, preview.Data.Body)
				}
			}
			if !strings.Contains(preview.Data.Body, hiddenCredential) {
				t.Errorf("%s previewing %s is not told something was hidden:\n%s", reader.who, event, preview.Data.Body)
			}
		}
	}
}

func showsNone(t *testing.T, where string, row NotificationRecord, secrets []string) {
	t.Helper()
	for k, v := range row.Data {
		for _, s := range secrets {
			if s != "" && strings.Contains(v, s) {
				t.Errorf("%s shows %s = %q", where, k, v)
			}
		}
	}
	for _, s := range secrets {
		if s != "" && strings.Contains(row.Error, s) {
			t.Errorf("%s shows the secret in the error: %q", where, row.Error)
		}
	}
}

func TestTheLogHidesAnOperatorsResetLinkAndCode(t *testing.T) {
	app, rec := resetApp(t)
	app.cfg.PanelURL = "https://admin.example"
	newSuperuser(t, app, "owner@example.com", "the old password")
	mail := resetMail(t, app, rec, "owner@example.com")
	token, code, link := mail.Data["reset_token"], mail.Data["reset_code"], mail.Data["reset_url"]
	if token == "" || code == "" || link == "" {
		t.Fatalf("the email carries %v; the test needs a token, a code and a link to look for", mail.Data)
	}

	readers := logReaders(t, app)
	assertLogHides(t, app, readers, EventSuperuserPasswordReset, token, code, link)

	// Whatever staff can read there opens nothing.
	ctx := context.Background()
	shown := loggedData(t, app, readers[0], EventSuperuserPasswordReset)
	if _, _, err := app.Superusers().ConfirmReset(ctx, ResetProof{Token: shown["reset_token"]}, "taken over", "10.0.0.5"); err == nil {
		t.Fatal("the token the log shows reset the owner's password")
	}
	if _, _, err := app.Superusers().ConfirmReset(ctx, codeProof("owner@example.com", shown["reset_code"]), "taken over", "10.0.0.5"); err == nil {
		t.Fatal("the code the log shows reset the owner's password")
	}

	// The email the owner got still works.
	if _, _, err := app.Superusers().ConfirmReset(ctx, ResetProof{Token: token}, "the new password", "10.0.0.1"); err != nil {
		t.Fatalf("the emailed link no longer works: %v", err)
	}
}

func TestTheLogHidesAnInvitationLink(t *testing.T) {
	rec := &recordingNotifier{}
	app := newTestApp(t, &notifyModule{rec: rec})
	code, inv := postInvitation(t, app, `{"email":"new-owner@example.com","role":"owner"}`)
	if code != http.StatusCreated || inv["emailed"] != true {
		t.Fatalf("invite = %d %v", code, inv)
	}
	token, _ := inv["token"].(string)
	link, _ := inv["accept_url"].(string)
	if token == "" || link == "" {
		t.Fatalf("the invitation carries no token or link: %v", inv)
	}

	readers := logReaders(t, app)
	assertLogHides(t, app, readers, EventSuperuserInvitation, token, link)

	// Read as a link, what the log shows accepts nothing.
	ctx := context.Background()
	shown := loggedData(t, app, readers[0], EventSuperuserInvitation)["invite_url"]
	guess := shown
	if i := strings.LastIndex(guess, "/"); i >= 0 {
		guess = guess[i+1:]
	}
	if i := strings.IndexAny(guess, "?#"); i >= 0 {
		guess = guess[:i]
	}
	for _, try := range []string{shown, guess} {
		if _, _, err := app.Team().Accept(ctx, try, "a-long-enough-password"); err == nil {
			t.Fatalf("what the log shows (%q) accepted the invitation", try)
		}
	}

	// The invitee's own link still works.
	if _, _, err := app.Team().Accept(ctx, token, "a-long-enough-password"); err != nil {
		t.Fatalf("the emailed invitation no longer works: %v", err)
	}
}

// Sending a message again sends what was sent: the live link, to the person it
// was for. Only what the log shows is hidden.
func TestAResendStillCarriesTheLiveLinkToItsRecipient(t *testing.T) {
	app, rec := resetApp(t)
	app.cfg.PanelURL = "https://admin.example"
	newSuperuser(t, app, "owner@example.com", "the old password")
	link := resetMail(t, app, rec, "owner@example.com").Data["reset_url"]

	staff := logReaders(t, app)[0]
	row := listLogged(t, app, staff, EventSuperuserPasswordReset)[0]
	again := do(t, app, http.MethodPost, "/api/admin/notifications/"+strconv.FormatInt(row.ID, 10)+"/resend", staff.auth)
	if again.Code != http.StatusOK {
		t.Fatalf("resend = %d: %s", again.Code, again.Body)
	}
	var body struct {
		Data NotificationRecord `json:"data"`
	}
	decodeJSONBody(t, again.Body.Bytes(), &body)
	showsNone(t, "the resend's answer", body.Data, []string{link})

	notes := resetNotes(t, app, rec)
	last := notes[len(notes)-1]
	if len(notes) != 2 || last.To != "owner@example.com" || last.Data["reset_url"] != link {
		t.Fatalf("resent %d emails, the last to %q with link %q; want the live link %q to the owner",
			len(notes), last.To, last.Data["reset_url"], link)
	}
}

func TestCredentialsAreHiddenByNameAndByValue(t *testing.T) {
	const tok = "tok-0123456789abcdef"
	rec := &NotificationRecord{
		Error: "provider said: bad link https://x.example/reset/" + tok,
		Data: map[string]string{
			// By name.
			"reset_token": tok, "reset_code": "123456", "reset_url": "https://x.example/reset/" + tok,
			"invite_url": "https://x.example/invite/abc", "invite_token": "abc", "verify_token": "v",
			"verify_url": "https://x.example/confirm?token=v", "cart_token": "c", "recovery_url": "https://x.example/cart/c",
			"discount_code": "WELCOME10", "magic_link": "https://x.example/m/1", "token": "t", "code": "c",
			"url": "u", "link": "l", "password": "p", "temp_password": "p", "client_secret": "s", "Access_Token": "a",
			// Not credentials.
			"order_number": "1001", "customer_email": "ada@example.com", "postal_code": "N1 1AA",
			"total_minor": "2500", "tracking": "1Z999", "expires_in_minutes": "60",
			// By value: a known credential, or a URL that carries one.
			"note":    "your link: https://x.example/reset/" + tok + " — use it soon",
			"message": "open https://shop.example/confirm?lang=en&token=zzz&ref=mail",
			// Empty stays empty, so a template that branches on it reads as the
			// message did.
			"verify_link": "",
		},
	}
	hideNotificationCredentials(rec)

	for _, k := range []string{"reset_token", "reset_code", "reset_url", "invite_url", "invite_token", "verify_token",
		"verify_url", "cart_token", "recovery_url", "discount_code", "magic_link", "token", "code", "url", "link",
		"password", "temp_password", "client_secret", "Access_Token"} {
		if rec.Data[k] != hiddenCredential {
			t.Errorf("%s = %q, want it hidden", k, rec.Data[k])
		}
	}
	want := map[string]string{
		"order_number": "1001", "customer_email": "ada@example.com", "postal_code": "N1 1AA",
		"total_minor": "2500", "tracking": "1Z999", "expires_in_minutes": "60",
		"note":        "your link: " + hiddenCredential + " — use it soon",
		"message":     "open https://shop.example/confirm?lang=en&token=" + hiddenCredential + "&ref=mail",
		"verify_link": "",
	}
	for k, v := range want {
		if rec.Data[k] != v {
			t.Errorf("%s = %q, want %q", k, rec.Data[k], v)
		}
	}
	if strings.Contains(rec.Error, tok) || !strings.Contains(rec.Error, hiddenCredential) {
		t.Errorf("error = %q, want the token hidden and the rest kept", rec.Error)
	}
}
