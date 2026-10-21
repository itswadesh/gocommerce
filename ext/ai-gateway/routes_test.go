package aigateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

func TestTheRoutesAreDocumentedAndGated(t *testing.T) {
	app, _, _ := newGateway(t, reply(http.StatusOK, "{}"))
	gctest.AssertAdminRoutesDeclareRights(t, app, "ai-gateway")
	gctest.AssertSpecCoversModuleRoutes(t, app, "ai-gateway")
}

// Owner holds every right; a manager reads usage and decides proposals but
// does not run the queue; staff and vendors see none of it until a store
// grants it.
func TestTheRightsAndWhoHoldsThem(t *testing.T) {
	app, _, _ := newGateway(t, reply(http.StatusOK, "{}"))
	ctx := context.Background()
	for role, want := range map[string][]gocommerce.Right{
		gocommerce.RoleOwner:   {RightRead, RightConfigure, RightApprove},
		gocommerce.RoleManager: {RightRead, RightApprove},
		gocommerce.RoleStaff:   {},
		gocommerce.RoleVendor:  {},
	} {
		got, err := app.Roles().Of(ctx, role)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range []gocommerce.Right{RightRead, RightConfigure, RightApprove} {
			if slices.Contains(got, r) != slices.Contains(want, r) {
				t.Errorf("%s carries %s = %v", role, r, slices.Contains(got, r))
			}
		}
	}

	staff := gctest.OperatorToken(t, app, "staff@example.com", gocommerce.RoleStaff)
	if rec := gctest.SessionRequest(t, app, staff, http.MethodGet, "/api/admin/x/ai-gateway/usage", nil); rec.Code != http.StatusForbidden {
		t.Errorf("staff reading usage = %d, want 403", rec.Code)
	}
	manager := gctest.OperatorToken(t, app, "manager@example.com", gocommerce.RoleManager)
	if rec := gctest.SessionRequest(t, app, manager, http.MethodGet, "/api/admin/x/ai-gateway/usage", nil); rec.Code != http.StatusOK {
		t.Errorf("manager reading usage = %d", rec.Code)
	}
	if rec := gctest.SessionRequest(t, app, manager, http.MethodPost, "/api/admin/x/ai-gateway/check", nil); rec.Code != http.StatusForbidden {
		t.Errorf("manager running the check = %d, want 403", rec.Code)
	}
}

// Usage adds up runs by day, task or model inside the window, with every
// day of the window present so a chart has no holes.
func TestUsageAddsUpByDayTaskAndModel(t *testing.T) {
	app, _, _ := newGateway(t, reply(http.StatusOK, "{}"))
	ctx := context.Background()
	insert := func(day, task, model, outcome string, cost int64, escalated bool) {
		steps := `[{"step":"small","outcome":"ok"}]`
		if escalated {
			steps = `[{"step":"small","outcome":"invalid"},{"step":"strong","outcome":"ok"}]`
		}
		if _, err := app.DB().ExecContext(ctx, `
			INSERT INTO ai_executions (task, task_version, steps, provider, model, input_tokens, output_tokens,
			                           outcome, cost_micro_minor, currency, created_at)
			VALUES ($1, 1, $2, 'anthropic', $3, 1000, 100, $4, $5, 'USD', ($6::date + time '12:00') AT TIME ZONE 'UTC')`,
			task, steps, model, outcome, cost, day); err != nil {
			t.Fatal(err)
		}
	}
	insert("2026-10-03", "product.rewrite", smallID, "ok", 150_000, false)
	insert("2026-10-03", "product.rewrite", strongID, "ok", 900_000, true)
	insert("2026-10-05", "message.classify", smallID, "invalid_output", 50_000, false)
	insert("2026-09-30", "product.rewrite", smallID, "ok", 999_999, false) // outside

	get := func(by string) usageReport {
		rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/ai-gateway/usage?from=2026-10-01&to=2026-10-07&by="+by, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("usage by %s = %d: %s", by, rec.Code, rec.Body)
		}
		var out usageReport
		gctest.DecodeData(t, rec, &out)
		return out
	}

	days := get("day")
	if len(days.Rows) != 7 || days.Rows[0].Key != "2026-10-01" || days.Rows[6].Key != "2026-10-07" {
		t.Fatalf("days = %+v, want the seven days of the window", days.Rows)
	}
	if d := days.Rows[2]; d.Key != "2026-10-03" || d.Runs != 2 || d.CostMicroMinor != 1_050_000 || d.Escalations != 1 {
		t.Errorf("the 3rd = %+v", d)
	}
	if d := days.Rows[4]; d.Runs != 1 || d.Failures != 1 {
		t.Errorf("the 5th = %+v", d)
	}
	if days.Total.Runs != 3 || days.Total.CostMicroMinor != 1_100_000 || days.Currency != "USD" {
		t.Errorf("total = %+v %s", days.Total, days.Currency)
	}

	tasks := get("task")
	if len(tasks.Rows) != 2 || tasks.Rows[0].Key != "product.rewrite" || tasks.Rows[0].CostMicroMinor != 1_050_000 {
		t.Errorf("by task = %+v, want the costliest first", tasks.Rows)
	}
	models := get("model")
	if len(models.Rows) != 2 || models.Rows[0].Key != strongID || models.Rows[1].InputTokens != 2000 {
		t.Errorf("by model = %+v", models.Rows)
	}

	if rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/ai-gateway/usage?by=colour", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("by=colour = %d", rec.Code)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/ai-gateway/usage?from=2026-10-07&to=2026-10-01", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("a window that ends before it starts = %d", rec.Code)
	}
	// With no window, this month so far.
	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/ai-gateway/usage", nil)
	var now usageReport
	gctest.DecodeData(t, rec, &now)
	if now.From != monthOf(time.Now()).Format(time.DateOnly) {
		t.Errorf("default window starts %s", now.From)
	}
}

// The budget route is the panel's header: what is spent and held against
// the cap, and whether the store can make calls at all, with the reason.
func TestTheBudgetSaysWhatIsSpentAndWhetherCallsCanBeMade(t *testing.T) {
	app, m, _ := newGateway(t, reply(http.StatusOK, answer(smallID, goodAnswer, smallUsage)), rewriteTask())
	ctx := context.Background()

	var b Budget
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/ai-gateway/budget", nil), &b)
	if b.Ready || !strings.Contains(b.Reason, "switched off") || b.CapMinor != nil {
		t.Errorf("idle budget = %+v", b)
	}

	configure(t, app, ctx, nil)
	if _, err := m.Run(ctx, "test.rewrite", listing{Title: "x"}); err != nil {
		t.Fatal(err)
	}
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/ai-gateway/budget", nil), &b)
	if !b.Ready || b.CapMinor == nil || *b.CapMinor != 5000 || b.SpentMicroMinor != 326_700 || b.Currency != "USD" {
		t.Errorf("budget = %+v", b)
	}
	if b.Provider != ProviderAnthropic || b.SmallModel != smallID || b.StrongModel != strongID {
		t.Errorf("connection = %s %s %s", b.Provider, b.SmallModel, b.StrongModel)
	}
}

func TestExecutionsAreListedAndReadWithTheirSteps(t *testing.T) {
	app, m, _ := newGateway(t, byModel(map[string]string{
		smallID:  answer(smallID, badAnswer, smallUsage),
		strongID: answer(strongID, goodAnswer, smallUsage),
	}), rewriteTask())
	ctx := context.Background()
	configure(t, app, ctx, nil)
	res, err := m.Run(ctx, "test.rewrite", listing{Title: "x"})
	if err != nil {
		t.Fatal(err)
	}

	var list []Execution
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/ai-gateway/executions?task=test.rewrite", nil), &list)
	if len(list) != 1 || list[0].ID != res.ExecutionID {
		t.Fatalf("list = %+v", list)
	}
	var one Execution
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet, fmt.Sprintf("/api/admin/x/ai-gateway/executions/%d", res.ExecutionID), nil), &one)
	if len(one.Steps) != 2 || one.Steps[0].Outcome != "invalid" || one.Model != strongID {
		t.Errorf("one = %+v", one)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/ai-gateway/executions/999999", nil); rec.Code != http.StatusNotFound {
		t.Errorf("no such run = %d", rec.Code)
	}
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/ai-gateway/executions?outcome=refused", nil), &list)
	if len(list) != 0 {
		t.Errorf("filtered to refused = %+v", list)
	}
}

// The check proves a key without spending a token. A key of the wrong shape
// is caught before anything leaves the store.
func TestTheConnectionCheck(t *testing.T) {
	status := http.StatusOK
	app, _, s := newGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			reply(status, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)(w, r)
			return
		}
		reply(http.StatusOK, anthropicModels)(w, r)
	})
	ctx := context.Background()
	check := func() checkResult {
		t.Helper()
		rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/ai-gateway/check", nil)
		var out checkResult
		gctest.DecodeData(t, rec, &out)
		return out
	}

	if got := check(); got.OK || !strings.Contains(got.Message, "API key") {
		t.Errorf("with no key = %+v", got)
	}

	configure(t, app, ctx, map[string]any{"api_key": "fake-key-for-testing"})
	got := check()
	if got.OK || !strings.Contains(got.Message, "sk-ant-") {
		t.Errorf("a key of the wrong shape = %+v", got)
	}
	if s.count() != 0 {
		t.Error("a key of the wrong shape was sent to the provider")
	}

	configure(t, app, ctx, map[string]any{"strong_model": "claude-opus-9"})
	got = check()
	if !got.OK || !got.Small.Listed || got.Strong.Listed || got.Strong.Model != "claude-opus-9" {
		t.Errorf("a working key = %+v, want the strong model reported missing", got)
	}
	if !strings.HasPrefix(s.last(t).URL, "https://api.anthropic.com/v1/models") {
		t.Errorf("asked %s", s.last(t).URL)
	}

	status = http.StatusUnauthorized
	if got := check(); got.OK || !strings.Contains(got.Message, "invalid x-api-key") {
		t.Errorf("a refused key = %+v", got)
	}
}

// Two stores side by side: neither reads, lists, retries or cancels the
// other's jobs, runs, budget or cached answers, through the Go API or the
// routes.
func TestTwoStoresNeverSeeEachOthersAIData(t *testing.T) {
	task := rewriteTask()
	task.CacheFor = time.Hour
	app, m, s := newGateway(t, reply(http.StatusOK, answer(smallID, goodAnswer, smallUsage)), task)
	second, err := app.Stores().Create(context.Background(), "Second", "second")
	if err != nil {
		t.Fatal(err)
	}
	a := context.Background()
	b := gocommerce.WithStore(context.Background(), second.ID)
	configure(t, app, a, nil)
	configure(t, app, b, nil)

	res, err := m.Run(a, "test.rewrite", listing{Title: "same question"})
	if err != nil {
		t.Fatal(err)
	}
	jobA := enqueue(t, app, m, a, listing{Title: "y"}, "shared-key")

	// The Go API, bound to B.
	for _, table := range []string{"ai_jobs", "ai_executions", "ai_budgets", "ai_cache"} {
		if n := countRows(t, app, b, table); n != 0 {
			t.Errorf("store B sees %d rows of %s", n, table)
		}
	}
	if _, err := m.Execution(b, res.ExecutionID); err == nil {
		t.Error("store B read store A's run")
	}
	if _, err := m.Job(b, jobA); err == nil {
		t.Error("store B read store A's job")
	}
	if bud := mustBudget(t, m, b); bud.SpentMicroMinor != 0 {
		t.Errorf("store B's budget shows %d spent", bud.SpentMicroMinor)
	}
	// The same question asked in B is B's own: a call, not A's cached answer.
	calls := s.count()
	if res, err := m.Run(b, "test.rewrite", listing{Title: "same question"}); err != nil || res.Outcome != OutcomeOK || s.count() != calls+1 {
		t.Errorf("B's run = %+v %v after %d calls; it must not hit A's cache", res, err, s.count()-calls)
	}
	// One idempotency key, two stores: two jobs.
	if jobB := enqueue(t, app, m, b, listing{Title: "y"}, "shared-key"); jobB == jobA {
		t.Error("store B's job was deduplicated against store A's")
	}
	// B's run leaves A's figures as they were.
	if bud := mustBudget(t, m, a); bud.SpentMicroMinor != 326_700 {
		t.Errorf("store A's spend moved to %d", bud.SpentMicroMinor)
	}

	// The routes, naming store B.
	asB := func(method, path string) *testResponse {
		return do(t, app, method, "/api/admin/x/ai-gateway/"+path, second.ID)
	}
	if r := asB(http.MethodGet, fmt.Sprintf("executions/%d", res.ExecutionID)); r.code != http.StatusNotFound {
		t.Errorf("B reading A's run = %d", r.code)
	}
	for _, path := range []string{fmt.Sprintf("jobs/%d/retry", jobA), fmt.Sprintf("jobs/%d/cancel", jobA)} {
		if r := asB(http.MethodPost, path); r.code != http.StatusNotFound {
			t.Errorf("B: POST %s = %d, want 404", path, r.code)
		}
	}
	if j := mustJob(t, m, a, jobA); j.Status != JobQueued {
		t.Errorf("A's job became %s", j.Status)
	}
	var execs []Execution
	asB(http.MethodGet, "executions").decode(t, &execs)
	for _, e := range execs {
		if e.ID == res.ExecutionID {
			t.Error("B's run list carries A's run")
		}
	}
	var jobs []Job
	asB(http.MethodGet, "jobs").decode(t, &jobs)
	for _, j := range jobs {
		if j.ID == jobA {
			t.Error("B's job list carries A's job")
		}
	}
	var usage usageReport
	asB(http.MethodGet, "usage?by=task").decode(t, &usage)
	if usage.Total.CostMicroMinor != 326_700 || usage.Total.Runs != 1 {
		t.Errorf("B's usage = %+v, want B's one run only", usage.Total)
	}
}

type testResponse struct {
	code int
	body []byte
}

func (r *testResponse) decode(t *testing.T, v any) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(r.body, &env); err != nil || r.code != http.StatusOK {
		t.Fatalf("%d: %s", r.code, r.body)
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		t.Fatal(err)
	}
}

// do sends an admin request naming a store.
func do(t *testing.T, app *gocommerce.App, method, target, store string) *testResponse {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("Authorization", "Bearer "+gctest.AdminToken)
	req.Header.Set("X-Store", store)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return &testResponse{code: rec.Code, body: rec.Body.Bytes()}
}
