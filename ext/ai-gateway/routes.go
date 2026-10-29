package aigateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

func (m *Module) mountRoutes(app *gocommerce.App) {
	const base = "/api/admin/x/ai-gateway/"
	app.HandleAdminFunc("GET "+base+"usage", m.handleUsage, RightRead)
	app.HandleAdminFunc("GET "+base+"budget", m.handleBudget, RightRead)
	app.HandleAdminFunc("GET "+base+"credit", m.handleCredit, RightRead)
	app.HandleAdminFunc("GET "+base+"credit/ledger", m.handleCreditLedger, gocommerce.RightStoreOperate)
	app.HandleAdminFunc("POST "+base+"credit/grant", m.handleGrant, gocommerce.RightStoreOperate)
	app.HandleAdminFunc("POST "+base+"credit/free-allowance", m.handleFreeAllowance, gocommerce.RightStoreOperate)
	app.HandleAdminFunc("GET "+base+"executions", m.handleExecutions, RightRead)
	app.HandleAdminFunc("GET "+base+"executions/{id}", m.handleExecution, RightRead)
	app.HandleAdminFunc("GET "+base+"jobs", m.handleJobs, RightRead)
	app.HandleAdminFunc("POST "+base+"jobs/{id}/retry", m.handleRetry, RightConfigure)
	app.HandleAdminFunc("POST "+base+"jobs/{id}/cancel", m.handleCancel, RightConfigure)
	app.HandleAdminFunc("POST "+base+"check", m.handleCheck, RightConfigure)
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, gocommerce.Validationf("id must be a positive integer")
	}
	return id, nil
}

func (m *Module) handleBudget(w http.ResponseWriter, r *http.Request) {
	b, err := m.Budget(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, b)
}

func (m *Module) handleCredit(w http.ResponseWriter, r *http.Request) {
	c, err := m.Credit(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, c)
}

func (m *Module) handleCreditLedger(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	list, total, err := m.CreditLedger(r.Context(), limit, offset)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.RespondList(w, list, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

// creditChange is a grant or a new free allowance, in minor units of the
// store's currency.
type creditChange struct {
	AmountMinor int64  `json:"amount_minor"`
	Note        string `json:"note"`
}

func (m *Module) handleGrant(w http.ResponseWriter, r *http.Request) {
	m.creditWrite(w, r, m.Grant)
}

func (m *Module) handleFreeAllowance(w http.ResponseWriter, r *http.Request) {
	m.creditWrite(w, r, m.SetFreeAllowance)
}

// creditWrite is a grant or a free trial, which only the install's own admin
// token may make. Credit is what the platform's key spends, so a store whose
// owner could grant it, and every owner holds store.operate, could spend the
// platform's money as it liked; this is the install owner's act, as a store's
// plan is (D119).
func (m *Module) creditWrite(w http.ResponseWriter, r *http.Request, apply func(context.Context, int64, string) (*Credit, error)) {
	if !gocommerce.AdminTokenFrom(r.Context()) {
		gocommerce.RespondError(w, r, gocommerce.Forbiddenf("AI credit is granted by the platform's operator with the install's admin token, not from a store's own panel"))
		return
	}
	var in creditChange
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	c, err := apply(r.Context(), in.AmountMinor, strings.TrimSpace(in.Note))
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, c)
}

func (m *Module) handleExecutions(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	q := r.URL.Query()
	query := ExecutionQuery{Task: strings.TrimSpace(q.Get("task")), Outcome: strings.TrimSpace(q.Get("outcome")), Limit: limit, Offset: offset}
	if s := q.Get("job_id"); s != "" {
		if query.JobID, err = strconv.ParseInt(s, 10, 64); err != nil {
			gocommerce.RespondError(w, r, gocommerce.Validationf("job_id must be a number"))
			return
		}
	}
	list, total, err := m.Executions(r.Context(), query)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.RespondList(w, list, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (m *Module) handleExecution(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	e, err := m.Execution(r.Context(), id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, e)
}

var jobStatuses = []string{JobQueued, JobRunning, JobDone, JobFailed, JobDead, JobCancelled}

func (m *Module) handleJobs(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && !slices.Contains(jobStatuses, status) {
		gocommerce.RespondError(w, r, gocommerce.Validationf("status is one of %s", strings.Join(jobStatuses, ", ")))
		return
	}
	list, total, err := m.Jobs(r.Context(), status, limit, offset)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.RespondList(w, list, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (m *Module) handleRetry(w http.ResponseWriter, r *http.Request) {
	m.jobAction(w, r, m.RetryJob)
}

func (m *Module) handleCancel(w http.ResponseWriter, r *http.Request) {
	m.jobAction(w, r, m.CancelJob)
}

func (m *Module) jobAction(w http.ResponseWriter, r *http.Request, act func(context.Context, int64) (*Job, error)) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	j, err := act(r.Context(), id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, j)
}

// ------------------------------------------------------------------ usage

type usageRow struct {
	Key              string `json:"key"`
	Runs             int    `json:"runs"`
	CostMicroMinor   int64  `json:"cost_micro_minor"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	// Escalations are runs that asked the strong model after the small one;
	// failures are runs that ended without an answer.
	Escalations int `json:"escalations"`
	Failures    int `json:"failures"`
}

type usageReport struct {
	From     string     `json:"from"`
	To       string     `json:"to"`
	By       string     `json:"by"`
	Currency string     `json:"currency"`
	Rows     []usageRow `json:"rows"`
	Total    usageRow   `json:"total"`
}

// handleUsage adds up the store's runs inside a window of UTC days, by day,
// task or model. Days come back whole, empty ones included, so a chart of a
// month has thirty bars and no gaps to reason about; tasks and models come
// back costliest first.
func (m *Module) handleUsage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	by := q.Get("by")
	if by == "" {
		by = "day"
	}
	var key string
	switch by {
	case "day":
		key = `to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD')`
	case "task":
		key = `task`
	case "model":
		key = `model`
	default:
		gocommerce.RespondError(w, r, gocommerce.Validationf("by is day, task or model"))
		return
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	from, to := monthOf(today), today
	var err error
	if s := q.Get("from"); s != "" {
		if from, err = time.Parse(time.DateOnly, s); err != nil {
			gocommerce.RespondError(w, r, gocommerce.Validationf("from is a date, YYYY-MM-DD"))
			return
		}
	}
	if s := q.Get("to"); s != "" {
		if to, err = time.Parse(time.DateOnly, s); err != nil {
			gocommerce.RespondError(w, r, gocommerce.Validationf("to is a date, YYYY-MM-DD"))
			return
		}
	}
	if to.Before(from) {
		gocommerce.RespondError(w, r, gocommerce.Validationf("the window ends before it starts"))
		return
	}
	if to.Sub(from) > 366*24*time.Hour {
		gocommerce.RespondError(w, r, gocommerce.Validationf("a window is at most a year"))
		return
	}

	rows, err := m.db.QueryContext(r.Context(), `
		SELECT `+key+`, count(*), coalesce(sum(cost_micro_minor), 0),
		       coalesce(sum(input_tokens), 0), coalesce(sum(output_tokens), 0),
		       coalesce(sum(cache_read_tokens), 0), coalesce(sum(cache_write_tokens), 0),
		       count(*) FILTER (WHERE steps @> '[{"step":"small"}]' AND steps @> '[{"step":"strong"}]'),
		       count(*) FILTER (WHERE outcome NOT IN ('ok', 'deterministic', 'cached'))
		FROM ai_executions
		WHERE created_at >= $1::timestamp AT TIME ZONE 'UTC'
		  AND created_at < ($2::date + 1)::timestamp AT TIME ZONE 'UTC'
		GROUP BY 1
		ORDER BY 3 DESC, 1`,
		from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	defer rows.Close()
	report := usageReport{From: from.Format(time.DateOnly), To: to.Format(time.DateOnly), By: by,
		Currency: m.app.Currency(r.Context()), Rows: []usageRow{}}
	found := map[string]usageRow{}
	for rows.Next() {
		var u usageRow
		if err := rows.Scan(&u.Key, &u.Runs, &u.CostMicroMinor, &u.InputTokens, &u.OutputTokens,
			&u.CacheReadTokens, &u.CacheWriteTokens, &u.Escalations, &u.Failures); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		found[u.Key] = u
		report.Rows = append(report.Rows, u)
		report.Total.Runs += u.Runs
		report.Total.CostMicroMinor += u.CostMicroMinor
		report.Total.InputTokens += u.InputTokens
		report.Total.OutputTokens += u.OutputTokens
		report.Total.CacheReadTokens += u.CacheReadTokens
		report.Total.CacheWriteTokens += u.CacheWriteTokens
		report.Total.Escalations += u.Escalations
		report.Total.Failures += u.Failures
	}
	if err := rows.Err(); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if by == "day" {
		report.Rows = report.Rows[:0]
		for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
			k := d.Format(time.DateOnly)
			u := found[k]
			u.Key = k
			report.Rows = append(report.Rows, u)
		}
	}
	gocommerce.Respond(w, http.StatusOK, report)
}

// ------------------------------------------------------------------ check

type modelCheck struct {
	Model  string `json:"model"`
	Listed bool   `json:"listed"`
}

type checkResult struct {
	OK       bool       `json:"ok"`
	Provider string     `json:"provider"`
	Message  string     `json:"message"`
	Small    modelCheck `json:"small"`
	Strong   modelCheck `json:"strong"`
}

// keyShapes are how each provider's keys begin. A key that does not is a
// paste of the wrong thing — another provider's key, an organisation id —
// and is caught before it leaves the store.
var keyShapes = map[string]struct{ prefix, where string }{
	ProviderAnthropic: {"sk-ant-", "console.anthropic.com, under API keys"},
	ProviderOpenAI:    {"sk-", "platform.openai.com, under API keys"},
}

// handleCheck asks the provider which models the store's key reaches. That
// spends no tokens, proves the key works, and says whether the two models
// the settings name are ones the provider offers. The answer is always 200:
// a key that does not work is the check's finding, not the route's failure.
func (m *Module) handleCheck(w http.ResponseWriter, r *http.Request) {
	st, err := m.settings(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	out := checkResult{Provider: st.provider,
		Small: modelCheck{Model: st.models[TierSmall]}, Strong: modelCheck{Model: st.models[TierStrong]}}
	shape := keyShapes[st.provider]
	switch {
	// The platform's key is the install's to check: asking about it from a
	// store would tell the store what Anthropic says about the platform's
	// account.
	case st.platform:
		out.OK = true
		out.Message = "This store uses the platform's AI, paid from its AI credit; there is no key of its own to check."
	case st.apiKey == "":
		out.Message = "No API key is set. Add one under Settings › Plugins › AI gateway."
	case !strings.HasPrefix(st.apiKey, shape.prefix):
		out.Message = fmt.Sprintf("That is not an %s API key: they begin %s. Copy the key again from %s.",
			providerTitle(st.provider), shape.prefix, shape.where)
	}
	if out.Message != "" {
		gocommerce.Respond(w, http.StatusOK, out)
		return
	}
	p, err := newProvider(st.provider, st.apiKey, m.client)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	models, err := p.(modelLister).Models(ctx)
	if err != nil {
		var pe *ProviderError
		if errors.As(err, &pe) && pe.Status == http.StatusUnauthorized {
			out.Message = fmt.Sprintf("%s did not accept the key: %s", providerTitle(st.provider), pe.Message)
		} else {
			out.Message = fmt.Sprintf("The key could not be checked: %v", err)
		}
		gocommerce.Respond(w, http.StatusOK, out)
		return
	}
	out.OK = true
	out.Small.Listed = slices.Contains(models, out.Small.Model)
	out.Strong.Listed = slices.Contains(models, out.Strong.Model)
	var missing []string
	for _, c := range []modelCheck{out.Small, out.Strong} {
		if !c.Listed {
			missing = append(missing, c.Model)
		}
	}
	out.Message = fmt.Sprintf("%s accepted the key.", providerTitle(st.provider))
	if len(missing) > 0 {
		out.Message += fmt.Sprintf(" It does not offer %s under that name; check the model names in the settings.", strings.Join(missing, " or "))
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

func providerTitle(name string) string {
	switch name {
	case ProviderAnthropic:
		return "Anthropic"
	case ProviderOpenAI:
		return "OpenAI"
	}
	return name
}
