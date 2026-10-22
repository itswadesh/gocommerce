package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// Emails to staff carry a brand (D111): its name, and links to its panel. A
// store's emails are its brand's; a password reset is the brand of the admin
// domain it was asked from; with no request, the brand the person arrived
// through. The link is only ever a configured brand's panel URL.

func brandedResetApp(t *testing.T) (*App, *recordingNotifier) {
	t.Helper()
	app, rec := resetApp(t)
	app.cfg.Brands = append(twoBrands(), apexBrand())
	return app, rec
}

func lastReset(t *testing.T, app *App, rec *recordingNotifier) Notification {
	t.Helper()
	notes := resetNotes(t, app, rec)
	if len(notes) == 0 {
		t.Fatal("no reset email was sent")
	}
	return notes[len(notes)-1]
}

func personBrandHost(t *testing.T, app *App, email string) string {
	t.Helper()
	var host *string
	if err := app.db.QueryRow(`SELECT brand_host FROM superusers WHERE email = $1`, email).Scan(&host); err != nil {
		t.Fatal(err)
	}
	if host == nil {
		return ""
	}
	return *host
}

func TestAResetAskedForOnABrandLinksBackToIt(t *testing.T) {
	app, rec := brandedResetApp(t)
	newSuperuser(t, app, "owner@example.com", "a good password")
	joinTestStore(t, app, inBetaStore(t, app), "owner@example.com", RoleOwner)
	res := do(t, app, http.MethodPost, "/api/admin/password-reset",
		jsonBody(t, map[string]string{"identity": "owner@example.com"}), onHost("admin.beta.test"))
	var out ResetRequest
	decodeData(t, res, &out)
	if !out.Link {
		t.Fatal("the answer says the email carries no link, on a brand with a panel URL")
	}
	note := lastReset(t, app, rec)
	if link := note.Data["reset_url"]; !strings.HasPrefix(link, "https://admin.beta.test/admin/auth/reset-password/") {
		t.Errorf("reset_url = %q, want Beta's panel", link)
	}
	if note.Data["brand_name"] != "Beta" {
		t.Errorf("brand_name = %q, want Beta", note.Data["brand_name"])
	}
}

// The security property of D95, kept with several brands: the host picks among
// the configured panels, and anything else gets the first. No header a caller
// sends ever becomes a link.
func TestAResetLinkNeverGoesToAHostThatIsNoBrands(t *testing.T) {
	app, rec := brandedResetApp(t)
	newSuperuser(t, app, "owner@example.com", "a good password")
	// In a store of each of the first two brands, so the request may pick
	// either.
	joinTestStore(t, app, inBetaStore(t, app), "owner@example.com", RoleOwner)
	for _, tc := range []struct {
		host, forwarded, want string
	}{
		{"evil.example", "evil.example", "https://admin.alpha.test/"},
		{"admin.beta.test.evil.example", "", "https://admin.alpha.test/"},
		{"admin.alpha.test", "admin.beta.test", "https://admin.alpha.test/"},
		{"admin.beta.test", "evil.example", "https://admin.beta.test/"},
	} {
		do(t, app, http.MethodPost, "/api/admin/password-reset",
			jsonBody(t, map[string]string{"identity": "owner@example.com"}),
			header("X-Forwarded-Host", tc.forwarded), onHost(tc.host), fromIP("10.0.2."+itoa(int64(len(tc.host)))))
		note := lastReset(t, app, rec)
		if link := note.Data["reset_url"]; !strings.HasPrefix(link, tc.want) {
			t.Errorf("Host %s, X-Forwarded-Host %s: reset_url = %q, want %s…", tc.host, tc.forwarded, link, tc.want)
		}
		for key, value := range note.Data {
			if strings.Contains(value, "evil.example") {
				t.Errorf("%s carries the caller's host: %q", key, value)
			}
		}
	}
}

// An invitation is its store's: its link goes to the store's brand, whichever
// admin domain sent it, and the person who accepts it is remembered as having
// arrived through that brand — which is what a later email with no request
// behind it goes by.
func TestAnInvitationIsItsStoresBrandAndThePersonKeepsIt(t *testing.T) {
	app, rec := brandedResetApp(t)
	beta := addTestStore(t, app, "dark-shop")
	inBrand(t, app, beta, "admin.beta.test")

	code, inv := postInvitationTo(t, app, beta, "admin.alpha.test", `{"email":"new@example.com","role":"staff"}`)
	if code != http.StatusCreated {
		t.Fatalf("invite = %d %v", code, inv)
	}
	link, _ := inv["accept_url"].(string)
	if !strings.HasPrefix(link, "https://admin.beta.test/admin/auth/accept-invite/") || !strings.Contains(link, "store="+beta) {
		t.Fatalf("accept_url = %q, want Beta's panel and the store", link)
	}
	var sent Notification
	for _, n := range rec.sent {
		if n.Event == EventSuperuserInvitation {
			sent = n
		}
	}
	if sent.Data["invite_url"] != link || sent.Data["brand_name"] != "Beta" {
		t.Fatalf("invitation email data = %v, want Beta and the same link", sent.Data)
	}

	token := link[strings.LastIndex(link, "/")+1 : strings.Index(link, "?")]
	res := doBody(t, app, http.MethodPost, "/api/admin/invitations/accept/"+token+"?store="+beta,
		`{"password":"a long enough password"}`, onHost("admin.beta.test"))
	if res.Code != http.StatusCreated {
		t.Fatalf("accept = %d %s", res.Code, res.Body)
	}
	if got := personBrandHost(t, app, "new@example.com"); got != "admin.beta.test" {
		t.Fatalf("recorded brand = %q, want admin.beta.test", got)
	}

	// No request and no store: the person's own brand.
	if _, err := app.Superusers().RequestReset(context.Background(), "new@example.com", "10.0.3.1"); err != nil {
		t.Fatal(err)
	}
	note := lastReset(t, app, rec)
	if !strings.HasPrefix(note.Data["reset_url"], "https://admin.beta.test/") || note.Data["brand_name"] != "Beta" {
		t.Fatalf("a background reset went out as %q to %q, want Beta's", note.Data["brand_name"], note.Data["reset_url"])
	}

	// Somebody who arrived before brands has none recorded: the first brand.
	newSuperuser(t, app, "older@example.com", "a good password")
	if _, err := app.Superusers().RequestReset(context.Background(), "older@example.com", "10.0.3.2"); err != nil {
		t.Fatal(err)
	}
	if note := lastReset(t, app, rec); note.Data["brand_name"] != "Alpha" {
		t.Fatalf("a person with no brand: %q, want the first brand", note.Data["brand_name"])
	}
}

// A reset an owner sends a teammate from the team screen is the store's.
func TestATeamResetGoesOutInTheStoresBrand(t *testing.T) {
	app, rec := brandedResetApp(t)
	beta := addTestStore(t, app, "dark-shop")
	inBrand(t, app, beta, "admin.beta.test")
	in := WithStore(context.Background(), beta)
	su, err := app.Superusers().Create(in, "mate@example.com", "a long enough password", RoleStaff)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Superusers().RequestResetFor(in, su.ID); err != nil {
		t.Fatal(err)
	}
	if note := lastReset(t, app, rec); !strings.HasPrefix(note.Data["reset_url"], "https://admin.beta.test/") || note.Data["brand_name"] != "Beta" {
		t.Fatalf("team reset = %q, %q; want Beta's", note.Data["brand_name"], note.Data["reset_url"])
	}
}

func TestSigningUpRemembersTheBrand(t *testing.T) {
	app := signupApp(t)
	app.cfg.Brands = twoBrands()
	if rec := signUp(t, app, "newcomer@example.com", "a long enough password", onHost("admin.beta.test")); rec.Code != http.StatusCreated {
		t.Fatalf("sign up = %d %s", rec.Code, rec.Body)
	}
	if got := personBrandHost(t, app, "newcomer@example.com"); got != "admin.beta.test" {
		t.Fatalf("recorded brand = %q, want admin.beta.test", got)
	}
}

// Where a brand's panel host is also a store's own domain, the request is that
// store's and the email is still the brand's.
func TestAResetOnAPanelHostThatIsAStoresDomainIsThatBrands(t *testing.T) {
	app, rec := brandedResetApp(t)
	held := addTestStore(t, app, "held-shop")
	inBrand(t, app, held, "staff.gamma.test")
	if _, err := app.db.Exec(`INSERT INTO store_domains (host, store_id, verified_at) VALUES ('staff.gamma.test', $1, now())`, held); err != nil {
		t.Fatal(err)
	}
	newSuperuser(t, app, "owner@example.com", "a good password")
	joinTestStore(t, app, held, "owner@example.com", RoleOwner)
	do(t, app, http.MethodPost, "/api/admin/password-reset",
		jsonBody(t, map[string]string{"identity": "owner@example.com"}), onHost("staff.gamma.test"))
	if note := lastReset(t, app, rec); !strings.HasPrefix(note.Data["reset_url"], "https://staff.gamma.test/") || note.Data["brand_name"] != "Gamma" {
		t.Fatalf("reset = %q, %q; want Gamma's", note.Data["brand_name"], note.Data["reset_url"])
	}
}

// The default wording says the brand's name where it said GoCommerce, and
// reads cleanly for a logged message from before there was one.
func TestTheDefaultStaffEmailsNameTheBrand(t *testing.T) {
	for _, tc := range []struct {
		event string
		data  map[string]string
		want  string
	}{
		{EventSuperuserPasswordReset, map[string]string{"brand_name": "Beta"}, "Reset your Beta password"},
		{EventSuperuserPasswordReset, map[string]string{}, "Reset your password"},
		{EventSuperuserInvitation, map[string]string{"brand_name": "Beta", "store_name": "Dark Shop"}, "Join Dark Shop on Beta"},
		{EventSuperuserInvitation, map[string]string{"store_name": "Dark Shop"}, "Join Dark Shop"},
	} {
		var subject string
		for _, d := range coreNotifyTemplates {
			if d.Channel == ChannelEmail && d.Event == tc.event {
				subject = d.Subject
			}
		}
		got, err := RenderNotifyText(subject, tc.data)
		if err != nil || got != tc.want {
			t.Errorf("%s with %v = %q, %v; want %q", tc.event, tc.data, got, err, tc.want)
		}
		if strings.Contains(subject, "GoCommerce") {
			t.Errorf("%s's subject still names GoCommerce: %q", tc.event, subject)
		}
	}
}

// postInvitationTo invites into store with the install's token, through host.
func postInvitationTo(t *testing.T, app *App, store, host, body string) (int, map[string]any) {
	t.Helper()
	rec := doBody(t, app, http.MethodPost, "/api/admin/invitations", body, withAdmin, header("X-Store", store), onHost(host))
	var out map[string]any
	if rec.Code == http.StatusCreated {
		decodeData(t, rec, &out)
	}
	return rec.Code, out
}

// A public reset is in the brand it was asked from only when the person
// belongs there (D111 review): a store in that brand, or arriving through it.
// Otherwise a stranger could choose which brand's name a real person's email
// carries. The answer to the request does not change either way.
func TestAResetGoesOutOnlyInABrandThePersonBelongsTo(t *testing.T) {
	app, rec := brandedResetApp(t)
	// Arrived through Beta, with no store anywhere.
	if _, err := app.db.Exec(`INSERT INTO superusers (email, password_hash, brand_host) VALUES ('arrived@example.com', 'x', 'admin.beta.test')`); err != nil {
		t.Fatal(err)
	}
	// No arrival recorded, one store, in Beta.
	beta := addTestStore(t, app, "dark-shop")
	inBrand(t, app, beta, "admin.beta.test")
	if _, err := app.Superusers().Create(WithStore(context.Background(), beta), "staffed@example.com", "a long enough password", RoleStaff); err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{"arrived@example.com", "staffed@example.com"} {
		res := do(t, app, http.MethodPost, "/api/admin/password-reset",
			jsonBody(t, map[string]string{"identity": email}), onHost("admin.alpha.test"), fromIP("10.0.4."+itoa(int64(len(email)))))
		if res.Code != http.StatusAccepted {
			t.Fatalf("%s: %d %s", email, res.Code, res.Body)
		}
		if note := lastReset(t, app, rec); note.To != email || note.Data["brand_name"] != "Beta" ||
			!strings.HasPrefix(note.Data["reset_url"], "https://admin.beta.test/") {
			t.Errorf("%s asked on Alpha's admin: %q to %q, want Beta's", email, note.Data["brand_name"], note.Data["reset_url"])
		}
	}
}

// inBetaStore opens a store in Beta and returns its id.
func inBetaStore(t *testing.T, app *App) string {
	t.Helper()
	id := addTestStore(t, app, "beta-own")
	inBrand(t, app, id, "admin.beta.test")
	return id
}
