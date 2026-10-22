package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
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
	app.cfg.PanelURL = "https://admin.example.com"
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

// The invitation page tells whoever holds the link nothing about whether its
// address already has an account: inviters can mint links for any address,
// and a difference here would be a way to find out who uses the platform.
func TestTheInvitePageSaysNothingAboutAnExistingAccount(t *testing.T) {
	app := newTestApp(t)
	b := addTestStore(t, app, "second")
	if _, err := app.Superusers().Create(WithStore(context.Background(), b), "known@example.com", "a-long-enough-password", RoleOwner); err != nil {
		t.Fatal(err)
	}
	known := invite(t, app, "known@example.com", RoleStaff, nil)
	fresh := invite(t, app, "fresh@example.com", RoleStaff, nil)
	var shapes []string
	for _, tok := range []string{known.Token, fresh.Token} {
		rec := do(t, app, http.MethodGet, "/api/admin/invitations/accept/"+tok)
		if rec.Code != http.StatusOK {
			t.Fatalf("lookup = %d: %s", rec.Code, rec.Body)
		}
		var env struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		if _, ok := env.Data["has_account"]; ok {
			t.Fatalf("the lookup says whether %v has an account", env.Data["email"])
		}
		keys := make([]string, 0, len(env.Data))
		for k := range env.Data {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		shapes = append(shapes, strings.Join(keys, ","))
	}
	if shapes[0] != shapes[1] {
		t.Fatalf("an existing account's invitation reads differently: %s / %s", shapes[0], shapes[1])
	}
}

// An emailed invitation link comes only from configuration: the panel URL, or
// with several brands the store's brand's (D111 review). Without one, the link
// is still shown to the owner who asked, built from the request so it works
// from where they stand, but it is not emailed — a forwarded host header is
// whatever a proxy or a client made it.
func TestAnInvitationLinkIsEmailedOnlyFromConfiguration(t *testing.T) {
	rec := &recordingNotifier{}
	app := newTestApp(t, &notifyModule{rec: rec})
	forged := []func(*http.Request){withAdmin, header("X-Forwarded-Host", "evil.example"), onHost("evil.example")}

	res := doBody(t, app, http.MethodPost, "/api/admin/invitations", `{"email":"first@example.com","role":"staff"}`, forged...)
	var inv map[string]any
	decodeData(t, res, &inv)
	if res.Code != http.StatusCreated || inv["emailed"] != false || inv["email_error"] == "" || inv["accept_url"] == "" {
		t.Fatalf("no panel URL: %d %v; want the link shown and not emailed, with a reason", res.Code, inv)
	}
	if rec.count(EventSuperuserInvitation) != 0 {
		t.Fatal("an invitation link built from the request was emailed")
	}

	app.cfg.PanelURL = "https://admin.example.com"
	res = doBody(t, app, http.MethodPost, "/api/admin/invitations", `{"email":"second@example.com","role":"staff"}`, forged...)
	decodeData(t, res, &inv)
	link, _ := inv["accept_url"].(string)
	if inv["emailed"] != true || !strings.HasPrefix(link, "https://admin.example.com/admin/auth/accept-invite/") {
		t.Fatalf("with a panel URL: %v; want it emailed from the panel URL", inv)
	}
	for _, n := range rec.sent {
		for k, v := range n.Data {
			if strings.Contains(v, "evil.example") {
				t.Errorf("%s carries the forged host: %q", k, v)
			}
		}
	}
}
