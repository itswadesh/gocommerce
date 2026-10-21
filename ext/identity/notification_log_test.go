package identity

import (
	"net/http"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// engineMail is an email backend registered with the engine, so the module's
// messages go through the engine's funnel — and into its log — the way they
// do in a store, rather than straight to Config.Notifier.
type engineMail struct{ rec *gctest.RecordingNotifier }

func (engineMail) Name() string                       { return "engine-mail" }
func (engineMail) Migrations() []gocommerce.Migration { return nil }
func (m engineMail) Register(app *gocommerce.App) error {
	app.RegisterNotifier(gocommerce.ChannelEmail, m.rec)
	return nil
}

// lastOf is the newest message sent for event.
func lastOf(t *testing.T, rec *gctest.RecordingNotifier, event string) gocommerce.Notification {
	t.Helper()
	all := rec.All()
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Event == event {
			return all[i]
		}
	}
	t.Fatalf("no %s was sent", event)
	return gocommerce.Notification{}
}

// The store's notification log is open to anybody who may read orders. A
// shopper's reset and confirmation links are the shopper's alone: the log
// shows the message with the link hidden, and the email still carries one
// that works.
func TestTheNotificationLogHidesAShoppersLinks(t *testing.T) {
	mail := &gctest.RecordingNotifier{}
	app := gctest.New(t, engineMail{rec: mail}, New(Config{
		ResetURL:  "https://shop.example/reset?token={token}",
		VerifyURL: "https://shop.example/confirm?token={token}",
	}))
	ada := register(t, app, "ada@example.com")

	if rec := as(t, app, ada.Token, http.MethodPost, "/x/identity/me/email-verification", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("ask for a confirmation = %d: %s", rec.Code, rec.Body)
	}
	verify := lastOf(t, mail, EventEmailVerification)
	if rec := gctest.Request(t, app, http.MethodPost, "/x/identity/password-reset",
		map[string]any{"email": "ada@example.com"}); rec.Code != http.StatusAccepted {
		t.Fatalf("ask for a reset = %d: %s", rec.Code, rec.Body)
	}
	reset := lastOf(t, mail, EventPasswordReset)
	if reset.Data["reset_url"] == "" || verify.Data["verify_url"] == "" {
		t.Fatalf("reset %v, confirmation %v: the test needs links to look for", reset.Data, verify.Data)
	}

	gctest.AssertNotificationLogHides(t, app, EventPasswordReset, reset.Data["reset_token"], reset.Data["reset_url"])
	gctest.AssertNotificationLogHides(t, app, EventEmailVerification, verify.Data["verify_token"], verify.Data["verify_url"])

	if rec := confirmToken(t, app, verify.Data["verify_token"]); rec.Code != http.StatusOK {
		t.Errorf("the emailed confirmation link = %d: %s", rec.Code, rec.Body)
	}
	if rec := gctest.Request(t, app, http.MethodPost, "/x/identity/password-reset/confirm", map[string]any{
		"token": reset.Data["reset_token"], "password": "a brand new one",
	}); rec.Code != http.StatusOK {
		t.Errorf("the emailed reset link = %d: %s", rec.Code, rec.Body)
	}
}
