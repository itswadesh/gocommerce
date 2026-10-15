package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// An invitation, the Dokploy way: email and role in, and the link goes out by
// the store's own email provider with no provider to choose.

func postInvitation(t *testing.T, app *App, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/invitations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	withAdmin(req)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	var env struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Data == nil {
		_ = json.Unmarshal(rec.Body.Bytes(), &env.Data)
	}
	return rec.Code, env.Data
}

func TestAnInvitationIsEmailedWithALinkToItsStore(t *testing.T) {
	rec := &recordingNotifier{}
	app := newTestApp(t, &notifyModule{rec: rec})
	code, inv := postInvitation(t, app, `{"email":"new@example.com","role":"staff"}`)
	if code != http.StatusCreated {
		t.Fatalf("status = %d: %v", code, inv)
	}
	if inv["emailed"] != true {
		t.Fatalf("emailed = %v, want true", inv["emailed"])
	}
	if rec.count(EventSuperuserInvitation) != 1 {
		t.Fatalf("invitation emails sent = %d, want 1", rec.count(EventSuperuserInvitation))
	}
	sent := rec.sent[len(rec.sent)-1]
	link := sent.Data["invite_url"]
	if sent.To != "new@example.com" || !strings.Contains(link, "/admin/auth/accept-invite/") ||
		!strings.Contains(link, "store="+app.DefaultStore()) {
		t.Fatalf("sent to %q with link %q; want the invitee and a link naming the store", sent.To, link)
	}
	if link != inv["accept_url"] {
		t.Fatalf("the email's link %q differs from the one shown to copy %q", link, inv["accept_url"])
	}
}

func TestAnInvitationCanBeIssuedWithoutAnEmail(t *testing.T) {
	rec := &recordingNotifier{}
	app := newTestApp(t, &notifyModule{rec: rec})
	code, inv := postInvitation(t, app, `{"email":"copy@example.com","role":"staff","send_email":false}`)
	if code != http.StatusCreated || inv["emailed"] != false || inv["accept_url"] == "" {
		t.Fatalf("status %d, %v", code, inv)
	}
	if rec.count(EventSuperuserInvitation) != 0 {
		t.Fatal("an invitation asked for as a link only was emailed")
	}
}

func TestAnInvitationLastsTwoDays(t *testing.T) {
	app := newTestApp(t)
	inv := invite(t, app, "two-days@example.com", RoleStaff, nil)
	if d := time.Until(inv.ExpiresAt); d < 47*time.Hour || d > 49*time.Hour {
		t.Fatalf("expires in %v, want 48h", d)
	}
}

func TestARevokedInvitationIsKeptAsRevoked(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	inv := invite(t, app, "gone@example.com", RoleStaff, nil)
	if err := app.invitations.Revoke(ctx, inv.ID); err != nil {
		t.Fatal(err)
	}
	list, err := app.invitations.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Status != InvitationRevoked {
		t.Fatalf("list = %+v, want the one invitation, revoked", list)
	}
	if _, err := app.invitations.Lookup(ctx, inv.Token); err == nil || !strings.Contains(err.Error(), "withdrawn") {
		t.Fatalf("lookup of a revoked link = %v, want it called withdrawn", err)
	}
}

func TestTheAcceptPageKnowsWhetherTheInviteeHasAnAccount(t *testing.T) {
	app := newTestApp(t)
	b := addTestStore(t, app, "second")
	if _, err := app.Superusers().Create(WithStore(context.Background(), b), "known@example.com", "a-long-enough-password", RoleOwner); err != nil {
		t.Fatal(err)
	}
	known := invite(t, app, "known@example.com", RoleStaff, nil)
	fresh := invite(t, app, "fresh@example.com", RoleStaff, nil)
	for _, c := range []struct {
		token string
		want  bool
	}{{known.Token, true}, {fresh.Token, false}} {
		got, err := app.invitations.Lookup(context.Background(), c.token)
		if err != nil {
			t.Fatal(err)
		}
		if got.HasAccount != c.want {
			t.Errorf("%s: has_account = %v, want %v", got.Email, got.HasAccount, c.want)
		}
	}
}
