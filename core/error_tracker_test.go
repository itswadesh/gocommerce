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
	app.errq.flush()
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
	app.errq.flush()
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
		"stack":"TypeError: Cannot read properties of undefined (reading 'total')\n    at render (http://127.0.0.1:8090/_app/immutable/nodes/12.BDNuN_ZX.js:40:17)\n    at x (http://127.0.0.1:8090/_app/immutable/chunks/a.js:1:1)",
		"url":"/dash/orders?page=2"}`
	for i := 0; i < 2; i++ {
		if rec := doBody(t, app, http.MethodPost, "/api/admin/errors", body, withAdmin); rec.Code != http.StatusAccepted {
			t.Fatalf("report = %d: %s", rec.Code, rec.Body)
		}
	}
	issues := listIssues(t, app, "?source=panel")
	if len(issues) != 1 || issues[0].Count != 2 || issues[0].Source != "panel" ||
		issues[0].Culprit != "_app/immutable/nodes/12.js" || !strings.HasPrefix(issues[0].Title, "TypeError: ") {
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
	app.errq.flush()
	if issues, _, err := app.Errors().List(ctx, ErrorQuery{Limit: 10}); err != nil || len(issues) != 0 {
		t.Fatalf("another store's list = %+v %v", issues, err)
	}
}

// Release builds are -trimpath: no frame carries a leading slash, and the
// culprit is still the code that failed, not the runtime or the recorder.
func TestTheCulpritIsTheCodeThatFailedInATrimmedBuild(t *testing.T) {
	stack := "goroutine 7 [running]:\n" +
		"runtime/debug.Stack()\n\truntime/debug/stack.go:26 +0x5e\n" +
		"github.com/itswadesh/gocommerce/core.captureServerError(0xc0001)\n\tgithub.com/itswadesh/gocommerce/core/error_tracker.go:331 +0x45\n" +
		"github.com/itswadesh/gocommerce/core.RespondError({0x1, 0x2})\n\tgithub.com/itswadesh/gocommerce/core/httpx.go:160 +0x1\n" +
		"github.com/itswadesh/gocommerce/core.(*App).handleListProducts(0xc0002, {0x3})\n\tgithub.com/itswadesh/gocommerce/core/catalog_http.go:88 +0x2c4\n" +
		"net/http.HandlerFunc.ServeHTTP(0x1, {0x2})\n\tnet/http/server.go:2294 +0x29\n"
	if got := goCulprit(stack); got != "core.(*App).handleListProducts (catalog_http.go)" {
		t.Fatalf("culprit = %q", got)
	}
	panicked := "goroutine 9 [running]:\n" +
		"runtime/debug.Stack()\n\truntime/debug/stack.go:26 +0x5e\n" +
		"github.com/itswadesh/gocommerce/core.(*App).recoverMW.func1.1()\n\tgithub.com/itswadesh/gocommerce/core/httpx.go:290 +0x5\n" +
		"panic({0x1, 0x2})\n\truntime/panic.go:791 +0x132\n" +
		"github.com/itswadesh/gocommerce/ext/cms.(*Module).handlePage(0x1)\n\tgithub.com/itswadesh/gocommerce/ext/cms/cms.go:120 +0x9\n"
	if got := goCulprit(panicked); got != "cms.(*Module).handlePage (cms.go)" {
		t.Fatalf("panic culprit = %q", got)
	}
}

// A path that carries a token is kept without it: the route's pattern where
// the mux matched one, the path with the token blanked otherwise.
func TestTokensStayOutOfTheRecord(t *testing.T) {
	if got := blankTokens("/api/admin/password-reset/k3J9aZx0QmW2pLr7Tn5Yb8Vc"); got != "/api/admin/password-reset/{token}" {
		t.Fatalf("blanked = %q", got)
	}
	if got := blankTokens("/api/admin/orders/42"); got != "/api/admin/orders/42" {
		t.Fatalf("an id was blanked: %q", got)
	}
	r, _ := http.NewRequest(http.MethodGet, "/api/admin/invitations/accept/k3J9aZx0QmW2pLr7Tn5Yb8Vc", nil)
	r.Pattern = "GET /api/admin/invitations/accept/{token}"
	if got := safePath(r); got != "/api/admin/invitations/accept/{token}" {
		t.Fatalf("safe path = %q", got)
	}
}

// One operator cannot fill the page: past the limit a report is accepted and
// dropped.
func TestPanelReportsAreLimitedPerOperator(t *testing.T) {
	app := newTestApp(t)
	staff := signInAs(t, app, "flood@example.com", RoleStaff)
	for i := 0; i < reportLimit+10; i++ {
		// Letters, not digits: digits are taken out of the fingerprint.
		body := fmt.Sprintf(`{"kind":"Error","message":"flood %c%c","url":"/dash"}`, 'a'+i%26, 'a'+i/26)
		if rec := doBody(t, app, http.MethodPost, "/api/admin/errors", body, bearer(staff)); rec.Code != http.StatusAccepted {
			t.Fatalf("report %d = %d", i, rec.Code)
		}
	}
	if got := listIssues(t, app, "?source=panel"); len(got) != reportLimit {
		t.Fatalf("issues from one operator in a minute = %d, want %d", len(got), reportLimit)
	}
}

// A demo store's visitors are signed in as its owners: its error page is shut,
// like custom reports.
func TestADemoStoreKeepsItsErrorsShut(t *testing.T) {
	app := newTestApp(t)
	if err := app.Stores().SetDemo(context.Background(), app.DefaultStore(), true); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, app, http.MethodGet, "/api/admin/errors", withAdmin); rec.Code != http.StatusForbidden {
		t.Fatalf("errors on a demo = %d", rec.Code)
	}
}
