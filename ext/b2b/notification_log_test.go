package b2b

import (
	"net/http"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// The store's notification log is open to anybody who may read orders. An
// invitation's token is the invitee's key to the company: the log shows the
// message with the token hidden, and the email still carries one that works.
func TestTheNotificationLogHidesAnInvitationsToken(t *testing.T) {
	f := newFixture(t)
	_, tok := f.account(t, "invited@acme.test", false)
	if rec := gctest.AdminRequest(t, f.app, http.MethodPost, f.companyPath()+"/members",
		map[string]any{"email": "invited@acme.test", "role": RoleBuyer}); rec.Code != http.StatusAccepted {
		t.Fatalf("invite = %d: %s", rec.Code, rec.Body)
	}
	sent := f.mail.All()
	token := sent[len(sent)-1].Data["invite_token"]
	if token == "" {
		t.Fatal("the invitation carries no token")
	}

	gctest.AssertNotificationLogHides(t, f.app, EventInvitation, token)

	// What the log shows accepts nothing; the emailed token still does.
	shown := loggedInvitation(t, f.app)["invite_token"]
	if r := gctest.SessionRequest(t, f.app, tok, http.MethodPost, "/x/b2b/invitations/accept",
		map[string]any{"token": shown}); r.Code == http.StatusOK {
		t.Fatalf("the token the log shows (%q) was accepted", shown)
	}
	if r := gctest.SessionRequest(t, f.app, tok, http.MethodPost, "/x/b2b/invitations/accept",
		map[string]any{"token": token}); r.Code != http.StatusOK {
		t.Fatalf("the emailed token = %d: %s", r.Code, r.Body)
	}
}

func loggedInvitation(t *testing.T, app *gocommerce.App) map[string]string {
	t.Helper()
	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/notifications?event="+EventInvitation, nil)
	var rows []gocommerce.NotificationRecord
	gctest.DecodeData(t, rec, &rows)
	if len(rows) == 0 {
		t.Fatal("the invitation is not in the log")
	}
	return rows[0].Data
}
