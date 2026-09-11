package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The team listing carries what only a listing asks: how many sessions each
// operator holds right now. An owner who can end somebody's sessions has to be
// able to see whether there are any — 0 and 4 mean quite different things to
// somebody who has lost a laptop.

func teamRows(t *testing.T, app *App) []SuperuserRow {
	t.Helper()
	rec := do(t, app, "GET", "/api/admin/superusers", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data []SuperuserRow `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body)
	}
	return env.Data
}

func TestTheTeamListingCountsLiveSessions(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	busy := newSuperuser(t, app, "busy@example.com", "a-long-enough-password")
	newSuperuser(t, app, "quiet@example.com", "a-long-enough-password")
	before := time.Now().Add(-time.Minute)
	signIn(t, app, "busy@example.com", "a-long-enough-password", "127.0.0.1")
	signIn(t, app, "busy@example.com", "a-long-enough-password", "127.0.0.2")

	rows := teamRows(t, app)
	var seenBusy, seenQuiet bool
	for _, r := range rows {
		switch r.Email {
		case "busy@example.com":
			seenBusy = true
			if r.Sessions != 2 {
				t.Errorf("busy has %d session(s), want 2", r.Sessions)
			}
			if r.NewestSession == nil || r.NewestSession.Before(before) {
				t.Errorf("newest_session = %v, want a time inside the window", r.NewestSession)
			}
		case "quiet@example.com":
			seenQuiet = true
			if r.Sessions != 0 || r.NewestSession != nil {
				t.Errorf("quiet has %d session(s) and newest %v, want 0 and null",
					r.Sessions, r.NewestSession)
			}
		}
	}
	if !seenBusy || !seenQuiet {
		t.Fatalf("listing = %+v, want both operators", rows)
	}

	// The number in front of the button and the number in the toast are the
	// same number, which is the whole complaint this closes.
	ended, err := app.Superusers().RevokeAll(ctx, busy.ID)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if ended != 2 {
		t.Errorf("revoke ended %d session(s), want the 2 the listing showed", ended)
	}
	for _, r := range teamRows(t, app) {
		if r.Email == "busy@example.com" && (r.Sessions != 0 || r.NewestSession != nil) {
			t.Errorf("after the revoke busy has %d session(s) and newest %v, want 0 and null",
				r.Sessions, r.NewestSession)
		}
	}
}

func TestASuperuserRecordNeverCarriesASessionCount(t *testing.T) {
	app := newTestApp(t)

	// Superuser is returned directly by login, refresh, create and set-role. A
	// "sessions": 0 on a successful sign-in would be a fact that is both false
	// and unfixable from those paths, which is why the count rides beside the
	// record rather than inside it.
	newSuperuser(t, app, "record@example.com", "a-long-enough-password")
	checks := []struct {
		name string
		rec  string
	}{}

	login := doBody(t, app, "POST", "/api/admin/auth-with-password",
		`{"identity":"record@example.com","password":"a-long-enough-password"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", login.Code, login.Body.String())
	}
	checks = append(checks, struct {
		name string
		rec  string
	}{"auth-with-password", login.Body.String()})

	var auth struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &auth); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	refresh := doBody(t, app, "POST", "/api/admin/auth-refresh", `{}`, bearer(auth.Token))
	checks = append(checks, struct {
		name string
		rec  string
	}{"auth-refresh", refresh.Body.String()})

	created := doBody(t, app, "POST", "/api/admin/superusers",
		`{"email":"fresh@example.com","password":"a-long-enough-password","role":"staff"}`, withAdmin)
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", created.Code, created.Body.String())
	}
	checks = append(checks, struct {
		name string
		rec  string
	}{"create", created.Body.String()})

	var made struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &made); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	role := doBody(t, app, "PUT",
		"/api/admin/superusers/"+strconv.FormatInt(made.Data.ID, 10)+"/role",
		`{"role":"manager"}`, withAdmin)
	checks = append(checks, struct {
		name string
		rec  string
	}{"set-role", role.Body.String()})

	for _, c := range checks {
		if strings.Contains(c.rec, `"sessions"`) {
			t.Errorf("%s carries a sessions key: %s", c.name, c.rec)
		}
	}
}
