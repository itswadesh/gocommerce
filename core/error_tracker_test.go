package gocommerce

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// boomModule fails on purpose: one route answers a 500 with an internal cause,
// the other panics.
type boomModule struct{}

func (boomModule) Name() string            { return "boom" }
func (boomModule) Migrations() []Migration { return nil }
func (boomModule) Register(app *App) error {
	app.HandleAdminFunc("GET /api/admin/x/boom/fail", func(w http.ResponseWriter, r *http.Request) {
		RespondError(w, r, Internalf(errors.New(`pq: relation "ghost_42" does not exist`), "read the ghosts"))
	}, RightCatalogRead)
	app.HandleAdminFunc("GET /api/admin/x/boom/panic", func(w http.ResponseWriter, r *http.Request) {
		var m map[string]int
		m["boom"]++ // a nil map: the panic a real bug would raise
	}, RightCatalogRead)
	return nil
}

type errorIssueView struct {
	ID        int64  `json:"id"`
	Source    string `json:"source"`
	Title     string `json:"title"`
	Culprit   string `json:"culprit"`
	Status    string `json:"status"`
	Count     int64  `json:"count"`
	LastEvent *struct {
		Message string         `json:"message"`
		Stack   string         `json:"stack"`
		Context map[string]any `json:"context"`
	} `json:"last_event"`
	Events []struct {
		Message string `json:"message"`
	} `json:"events"`
}

func listIssues(t *testing.T, app *App, query string, opts ...func(*http.Request)) []errorIssueView {
	t.Helper()
	rec := do(t, app, http.MethodGet, "/api/admin/errors"+query, append([]func(*http.Request){withAdmin}, opts...)...)
	if rec.Code != http.StatusOK {
		t.Fatalf("list errors = %d: %s", rec.Code, rec.Body)
	}
	var out []errorIssueView
	decodeData(t, rec, &out)
	return out
}

func getIssue(t *testing.T, app *App, id int64) errorIssueView {
	t.Helper()
	rec := do(t, app, http.MethodGet, fmt.Sprintf("/api/admin/errors/%d", id), withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("get error = %d: %s", rec.Code, rec.Body)
	}
	var out errorIssueView
	decodeData(t, rec, &out)
	return out
}

// A 500 keeps its cause from the client — the response still says only
// "internal error" — and gives it to the store's error page instead: one issue
// for the same failure however often it happens, with what was asked.
func TestAServerErrorBecomesAnIssue(t *testing.T) {
	app := newTestApp(t, boomModule{})
	for i := 0; i < 2; i++ {
		rec := do(t, app, http.MethodGet, "/api/admin/x/boom/fail", withAdmin)
		if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "ghost") {
			t.Fatalf("the response = %d %s, want a 500 that names nothing", rec.Code, rec.Body)
		}
	}
	issues := listIssues(t, app, "")
	if len(issues) != 1 {
		t.Fatalf("issues = %+v, want one", issues)
	}
	is := issues[0]
	if is.Source != "server" || is.Count != 2 || is.Status != "unresolved" || !strings.Contains(is.Title, `relation "ghost_42" does not exist`) {
		t.Fatalf("issue = %+v", is)
	}
	full := getIssue(t, app, is.ID)
	if full.LastEvent == nil || full.LastEvent.Context["path"] != "/api/admin/x/boom/fail" || full.LastEvent.Context["method"] != "GET" ||
		full.LastEvent.Stack == "" || len(full.Events) != 2 {
		t.Fatalf("issue detail = %+v", full)
	}
}

// A panic is answered with a 500 as before, and becomes an issue with its
// stack.
func TestAPanicBecomesAnIssue(t *testing.T) {
	app := newTestApp(t, boomModule{})
	if rec := do(t, app, http.MethodGet, "/api/admin/x/boom/panic", withAdmin); rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic answered %d", rec.Code)
	}
	issues := listIssues(t, app, "")
	if len(issues) != 1 || issues[0].Source != "server" || !strings.Contains(issues[0].Title, "nil map") {
		t.Fatalf("issues = %+v", issues)
	}
	full := getIssue(t, app, issues[0].ID)
	if full.LastEvent == nil || !strings.Contains(full.LastEvent.Stack, "error_tracker_test.go") {
		t.Fatalf("the panic's stack does not name where it happened: %+v", full.LastEvent)
	}
}

// The panel reports its own crashes; the same one twice is one issue, named by
// the first frame of its stack.
func TestPanelErrorsAreReported(t *testing.T) {
	app := newTestApp(t)
	body := `{"kind":"TypeError","message":"Cannot read properties of undefined (reading 'total')",
		"stack":"TypeError: Cannot read properties of undefined (reading 'total')\n    at render (http://127.0.0.1:8090/_app/immutable/nodes/12.Ab3.js:40:17)\n    at x (http://127.0.0.1:8090/_app/immutable/chunks/a.js:1:1)",
		"url":"/dash/orders?page=2"}`
	for i := 0; i < 2; i++ {
		if rec := doBody(t, app, http.MethodPost, "/api/admin/errors", body, withAdmin); rec.Code != http.StatusAccepted {
			t.Fatalf("report = %d: %s", rec.Code, rec.Body)
		}
	}
	issues := listIssues(t, app, "?source=panel")
	if len(issues) != 1 || issues[0].Count != 2 || issues[0].Source != "panel" ||
		!strings.Contains(issues[0].Culprit, "nodes/12.Ab3.js:40") || !strings.HasPrefix(issues[0].Title, "TypeError: ") {
		t.Fatalf("panel issues = %+v", issues)
	}
	full := getIssue(t, app, issues[0].ID)
	if full.LastEvent.Context["url"] != "/dash/orders" {
		t.Fatalf("the url kept its query, or was lost: %+v", full.LastEvent.Context)
	}
	if rec := doBody(t, app, http.MethodPost, "/api/admin/errors", `{"message":""}`, withAdmin); rec.Code != http.StatusBadRequest {
		t.Fatalf("an empty report = %d", rec.Code)
	}
}

// Resolved comes back when it happens again, as Sentry's regression does;
// ignored stays ignored however often.
func TestAResolvedErrorReopensWhenItHappensAgain(t *testing.T) {
	app := newTestApp(t, boomModule{})
	do(t, app, http.MethodGet, "/api/admin/x/boom/fail", withAdmin)
	id := listIssues(t, app, "")[0].ID
	set := func(status string) {
		t.Helper()
		rec := doBody(t, app, http.MethodPatch, fmt.Sprintf("/api/admin/errors/%d", id), `{"status":"`+status+`"}`, withAdmin)
		if rec.Code != http.StatusOK {
			t.Fatalf("set %s = %d: %s", status, rec.Code, rec.Body)
		}
	}
	set("resolved")
	if got := listIssues(t, app, "?status=unresolved"); len(got) != 0 {
		t.Fatalf("a resolved issue is still listed as unresolved: %+v", got)
	}
	do(t, app, http.MethodGet, "/api/admin/x/boom/fail", withAdmin)
	if got := getIssue(t, app, id); got.Status != "unresolved" || got.Count != 2 {
		t.Fatalf("after it happened again: %+v", got)
	}
	set("ignored")
	do(t, app, http.MethodGet, "/api/admin/x/boom/fail", withAdmin)
	if got := getIssue(t, app, id); got.Status != "ignored" || got.Count != 3 {
		t.Fatalf("an ignored issue: %+v", got)
	}
	if rec := doBody(t, app, http.MethodPatch, fmt.Sprintf("/api/admin/errors/%d", id), `{"status":"fixed"}`, withAdmin); rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown status = %d", rec.Code)
	}
}

// Only the latest occurrences are kept; the count keeps every one.
func TestOnlyTheLatestOccurrencesAreKept(t *testing.T) {
	app := newTestApp(t, boomModule{})
	for i := 0; i < errorEventsKept+5; i++ {
		do(t, app, http.MethodGet, "/api/admin/x/boom/fail", withAdmin)
	}
	is := listIssues(t, app, "")[0]
	full := getIssue(t, app, is.ID)
	if is.Count != int64(errorEventsKept+5) || len(full.Events) != errorEventsKept {
		t.Fatalf("count %d, events kept %d", is.Count, len(full.Events))
	}
}

// Raw errors name internals, so reading them is its own right, which staff do
// not hold; reporting a crash needs none, since anybody's panel can crash.
func TestErrorsAreTheirOwnRight(t *testing.T) {
	app := newTestApp(t, boomModule{})
	staff := signInAs(t, app, "staff@example.com", RoleStaff)
	if rec := do(t, app, http.MethodGet, "/api/admin/errors", bearer(staff)); rec.Code != http.StatusForbidden {
		t.Fatalf("staff reading errors = %d", rec.Code)
	}
	if rec := doBody(t, app, http.MethodPost, "/api/admin/errors", `{"kind":"Error","message":"boom","url":"/dash"}`, bearer(staff)); rec.Code != http.StatusAccepted {
		t.Fatalf("staff reporting a crash = %d: %s", rec.Code, rec.Body)
	}
	owner := signInAs(t, app, "owner@example.com", RoleOwner)
	if rec := do(t, app, http.MethodGet, "/api/admin/errors", bearer(owner)); rec.Code != http.StatusOK {
		t.Fatalf("owner reading errors = %d", rec.Code)
	}
}

// One store's errors are not another's.
func TestErrorsAreKeptToTheirStore(t *testing.T) {
	app := newTestApp(t, boomModule{})
	do(t, app, http.MethodGet, "/api/admin/x/boom/fail", withAdmin)
	other := addTestStore(t, app, "elsewhere")
	if got := listIssues(t, app, "", header("x-litekart-store", other)); len(got) != 0 {
		t.Fatalf("another store sees %+v", got)
	}
	ctx := WithStore(context.Background(), other)
	if issues, _, err := app.Errors().List(ctx, ErrorQuery{Limit: 10}); err != nil || len(issues) != 0 {
		t.Fatalf("another store's list = %+v %v", issues, err)
	}
}
