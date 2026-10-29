package aigateway

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
	"time"
	"unicode/utf8"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Outcomes a run ends with, as ai_executions records them.
const (
	OutcomeOK              = "ok"
	OutcomeDeterministic   = "deterministic"
	OutcomeCached          = "cached"
	OutcomeInvalidOutput   = "invalid_output"
	OutcomeRefused         = "refused"
	OutcomeProviderError   = "provider_error"
	OutcomeBudgetExhausted = "budget_exhausted"
	// OutcomeCreditExhausted is a store with no AI credit left: the free trial
	// is spent and nothing is topped up. No model is called.
	OutcomeCreditExhausted = "credit_exhausted"
	OutcomeNoPrice         = "no_price"
	// OutcomeTaskError is a task whose own code panicked: its Build, Check
	// or deterministic step. It is never tried again.
	OutcomeTaskError = "task_error"
	// OutcomePolicyBlocked is a request the store's data-sharing policy
	// stopped: a class the task needs is kept in, or nothing was left to send.
	OutcomePolicyBlocked = "policy_blocked"
)

// Result is what a run produced.
type Result struct {
	ExecutionID int64           `json:"execution_id"`
	Outcome     string          `json:"outcome"`
	Output      json.RawMessage `json:"output,omitempty"`
	Confidence  *int            `json:"confidence,omitempty"`
	// Model is the model that answered, "" when code or the cache did.
	Model          string `json:"model,omitempty"`
	CostMicroMinor int64  `json:"cost_micro_minor"`
	Currency       string `json:"currency"`
}

// RunError is a run that ended without an answer. Its record is written all
// the same, and ExecutionID names it.
type RunError struct {
	ExecutionID int64
	Outcome     string
	Reason      string
	// Transient is a failure worth trying again later: the provider timed
	// out, was rate limited or failed on its side. Everything else needs a
	// person.
	Transient bool
}

func (e *RunError) Error() string { return e.Outcome + ": " + e.Reason }

// Step is one thing a run tried, as its record lists them.
type Step struct {
	// Step is deterministic, cache, small or strong.
	Step string `json:"step"`
	// Outcome is hit or miss for the first two; for a model ok, invalid,
	// low_confidence, refused, provider_error, budget_exhausted or no_price.
	Outcome        string `json:"outcome"`
	Reason         string `json:"reason,omitempty"`
	Model          string `json:"model,omitempty"`
	ModelVersion   string `json:"model_version,omitempty"`
	Usage          *Usage `json:"usage,omitempty"`
	CostMicroMinor int64  `json:"cost_micro_minor,omitempty"`
	// Estimated is a call charged at its ceiling: sent, and no usage back.
	Estimated  bool  `json:"estimated,omitempty"`
	LatencyMS  int64 `json:"latency_ms,omitempty"`
	Confidence *int  `json:"confidence,omitempty"`
	// Dropped names the classes of data the policy kept out of the request.
	Dropped []string `json:"dropped,omitempty"`
}

// Run does the task now and returns its answer. It is for background code
// that holds no transaction and serves no request, such as an importer's own
// worker: it reserves, calls, settles and records exactly as the job runner
// does. Work caused by a request or an event is enqueued instead, so it
// survives a restart.
func (m *Module) Run(ctx context.Context, task string, input any) (Result, error) {
	t := m.task(task)
	if t == nil {
		return Result{}, fmt.Errorf("no ai task is called %q", task)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return Result{}, fmt.Errorf("ai task %s: encode the input: %w", task, err)
	}
	if gocommerce.TraceID(ctx) == "" {
		ctx = gocommerce.WithTraceID(ctx, "")
	}
	return m.execute(ctx, t, raw, nil)
}

// execute is one run, start to record. When the run belongs to a job, the
// job is settled in the same transaction as the record.
//
// An error that is not a RunError means the store's own database failed
// mid-run; nothing is recorded, because the record could not be written
// either, and a job treats it as worth another try.
func (m *Module) execute(ctx context.Context, t *Task, input json.RawMessage, job *claimedJob) (Result, error) {
	st, err := m.settings(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := st.ready(); err != nil {
		return Result{}, err
	}
	rec := &Execution{
		Task: t.Name, TaskVersion: t.Version, TraceID: gocommerce.TraceID(ctx),
		Provider: st.provider, Policy: st.policy, Currency: m.app.Currency(ctx),
		ReviewStatus: "none", Steps: []Step{}, SourceRefs: []SourceRef{},
	}
	if job != nil {
		rec.JobID = &job.id
	}
	res, runErr, err := m.guardedLadder(ctx, t, input, st, rec, job)
	if err != nil {
		return Result{}, err
	}
	res.Currency = rec.Currency
	res.CostMicroMinor = rec.CostMicroMinor
	if runErr != nil {
		rec.Outcome = runErr.Outcome
	} else {
		rec.Outcome = res.Outcome
		rec.Confidence = res.Confidence
	}

	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		id, err := m.writeExecution(ctx, tx, rec)
		if err != nil {
			return err
		}
		res.ExecutionID = id
		if runErr != nil {
			runErr.ExecutionID = id
		}
		if runErr == nil && t.CacheFor > 0 && res.Outcome == OutcomeOK {
			if err := m.remember(ctx, tx, t, input, st.policy, res.Output, id); err != nil {
				return err
			}
		}
		if job != nil {
			return m.finishJob(ctx, tx, job, outcomeFor(job, res, runErr))
		}
		return nil
	})
	if err != nil {
		return res, fmt.Errorf("record the run of %s: %w", t.Name, err)
	}
	if runErr != nil {
		return res, runErr
	}
	return res, nil
}

// guardedLadder is the ladder with the task's own code fenced off. A Build,
// Check or deterministic step that panics ends the run as task_error, with
// the panic as its reason and whatever it had already spent on the record:
// the work cannot be done as written, and trying it again would only pay
// for the same call to end the same way.
func (m *Module) guardedLadder(ctx context.Context, t *Task, input json.RawMessage, st *settings, rec *Execution, job *claimedJob) (res Result, runErr *RunError, err error) {
	defer func() {
		if p := recover(); p != nil {
			m.log.Error("ai task panicked", "task", t.Name, "panic", p, "stack", string(debug.Stack()))
			reason := fmt.Sprintf("the task's own code failed: %v", p)
			rec.Steps = append(rec.Steps, Step{Step: "task", Outcome: "error", Reason: reason})
			res, runErr, err = Result{Outcome: OutcomeTaskError}, &RunError{Outcome: OutcomeTaskError, Reason: reason}, nil
		}
	}()
	return m.ladder(ctx, t, input, st, rec, job)
}

// callResult is one call to a provider and what it was charged.
type callResult struct {
	resp      Response
	err       error
	cost      int64
	estimated bool
	latency   time.Duration
}

// call makes one call against the reservation the caller holds and settles
// it on every way out, on a context of its own: a run cancelled mid-call has
// still spent what it spent, and the reservation must not stay held for the
// rest of the month. A transport that panics is caught here, where the
// reservation is, and charged at the ceiling: whether the request left is
// not known, and the cap errs towards spending less.
func (m *Module) call(ctx context.Context, t *Task, job *claimedJob, provider Provider, req Request, month time.Time, ceiling int64, p prices) (cr callResult) {
	cr.cost, cr.estimated = ceiling, true
	defer func() {
		if r := recover(); r != nil {
			m.log.Error("ai provider call panicked", "task", t.Name, "panic", r, "stack", string(debug.Stack()))
			cr.err = &ProviderError{Provider: provider.Name(), Message: fmt.Sprintf("the call panicked: %v", r), panicked: true}
			cr.cost, cr.estimated = ceiling, true
		}
		if err := m.settle(context.WithoutCancel(ctx), month, ceiling, cr.cost); err != nil {
			m.log.Error("ai budget could not be settled", "task", t.Name, "error", err)
		}
		// The same cost comes off the credit, and the hold is released. A call
		// that brought nothing back costs nothing here too (cr.cost is 0), so a
		// failed model call never draws credit.
		if err := m.settleCredit(context.WithoutCancel(ctx), ceiling, cr.cost); err != nil {
			m.log.Error("ai credit could not be settled", "task", t.Name, "error", err)
		}
	}()
	if job != nil {
		m.renew(ctx, job)
	}
	callCtx, cancel := context.WithTimeout(ctx, m.cfg.CallTimeout)
	defer cancel()
	started := time.Now()
	cr.resp, cr.err = provider.Generate(callCtx, req)
	cr.latency = time.Since(started)
	cr.cost, cr.estimated = p.cost(cr.resp.Usage), false
	// A request that left and brought no usage back, timed out or
	// unreadable, may still be billed in full: charged at the ceiling, so a
	// slow provider retried twelve times cannot spend past the cap.
	var pe *ProviderError
	if errors.As(cr.err, &pe) && pe.Sent && cr.resp.Usage == (Usage{}) {
		cr.cost, cr.estimated = ceiling, true
	}
	return cr
}

// ladder tries each step in turn and fills the record as it goes.
func (m *Module) ladder(ctx context.Context, t *Task, input json.RawMessage, st *settings, rec *Execution, job *claimedJob) (Result, *RunError, error) {
	fail := func(outcome, reason string, transient bool) (Result, *RunError, error) {
		return Result{Outcome: outcome}, &RunError{Outcome: outcome, Reason: reason, Transient: transient}, nil
	}

	if t.Deterministic != nil {
		out, ok, err := t.Deterministic(ctx, input)
		switch {
		case err != nil:
			rec.Steps = append(rec.Steps, Step{Step: "deterministic", Outcome: "error", Reason: err.Error()})
			return fail(OutcomeRefused, err.Error(), false)
		case ok:
			if err := t.schema.validate(out); err != nil {
				// The task's own code answered wrongly. The model can still
				// answer, and the record says why it was asked.
				rec.Steps = append(rec.Steps, Step{Step: "deterministic", Outcome: "invalid", Reason: err.Error()})
				break
			}
			rec.Steps = append(rec.Steps, Step{Step: "deterministic", Outcome: "hit"})
			return Result{Outcome: OutcomeDeterministic, Output: out}, nil, nil
		default:
			rec.Steps = append(rec.Steps, Step{Step: "deterministic", Outcome: "miss"})
		}
	}

	if t.CacheFor > 0 {
		out, err := m.recall(ctx, t, input, st.policy)
		if err != nil {
			return Result{}, nil, fmt.Errorf("read the ai cache: %w", err)
		}
		if out != nil {
			rec.Steps = append(rec.Steps, Step{Step: "cache", Outcome: "hit"})
			return Result{Outcome: OutcomeCached, Output: out}, nil, nil
		}
		rec.Steps = append(rec.Steps, Step{Step: "cache", Outcome: "miss"})
	}

	if len(t.Ladder) == 0 {
		return fail(OutcomeRefused, "the task's code could not answer and it has no model to ask", false)
	}

	var (
		built    *rendered
		lastWhy  string
		provider Provider
	)
	for i, tier := range t.Ladder {
		model := st.models[tier]
		step := Step{Step: string(tier), Model: model}
		p := st.prices[tier]
		if !p.complete {
			step.Outcome, step.Reason = "no_price", fmt.Sprintf("the %s model has no input or output price, so the cap could not be kept", tier)
			rec.Steps = append(rec.Steps, step)
			return fail(OutcomeNoPrice, step.Reason, false)
		}
		if model == "" {
			step.Outcome, step.Reason = "refused", fmt.Sprintf("no %s model is named in the settings", tier)
			rec.Steps = append(rec.Steps, step)
			return fail(OutcomeRefused, step.Reason, false)
		}

		if built == nil {
			req, refs, err := t.Build(ctx, input, st.policy)
			if err != nil {
				step.Outcome, step.Reason = "refused", "the request could not be built: "+err.Error()
				rec.Steps = append(rec.Steps, step)
				return fail(OutcomeRefused, step.Reason, false)
			}
			if refs != nil {
				rec.SourceRefs = refs
			}
			r, err := render(req, t, st.policy)
			var pe *policyError
			switch {
			case errors.As(err, &pe):
				step.Outcome, step.Reason = "policy_blocked", err.Error()
				rec.Steps = append(rec.Steps, step)
				return fail(OutcomePolicyBlocked, err.Error(), false)
			case err != nil:
				step.Outcome, step.Reason = "refused", err.Error()
				rec.Steps = append(rec.Steps, step)
				return fail(OutcomeRefused, err.Error(), false)
			}
			if est := estimateTokens(r.req); est > t.MaxInputTokens {
				step.Outcome = "refused"
				step.Reason = fmt.Sprintf("the request comes to about %d input tokens, over this task's limit of %d", est, t.MaxInputTokens)
				rec.Steps = append(rec.Steps, step)
				return fail(OutcomeRefused, step.Reason, false)
			}
			built = &r
			provider, err = newProvider(st.provider, st.apiKey, m.client)
			if err != nil {
				step.Outcome, step.Reason = "refused", err.Error()
				rec.Steps = append(rec.Steps, step)
				return fail(OutcomeRefused, err.Error(), false)
			}
		}
		step.Dropped = built.dropped

		ceiling := p.ceiling(t.MaxInputTokens, t.MaxOutputTokens)
		month := monthOf(time.Now())

		// Credit first: a store with nothing left is refused before the model
		// is called, and no budget reservation is taken that would then have to
		// be unwound. Only a model call draws credit — a deterministic answer
		// or a cache hit, handled above, has already returned.
		okCredit, err := m.reserveCredit(ctx, ceiling)
		if err != nil {
			return Result{}, nil, fmt.Errorf("reserve against the ai credit: %w", err)
		}
		if !okCredit {
			step.Outcome = "credit_exhausted"
			step.Reason = m.creditReason(ctx)
			rec.Steps = append(rec.Steps, step)
			return fail(OutcomeCreditExhausted, step.Reason, false)
		}

		ok, err := m.reserve(ctx, month, *st.capMinor, ceiling)
		if err != nil {
			m.releaseCredit(ctx, ceiling)
			return Result{}, nil, fmt.Errorf("reserve against the ai budget: %w", err)
		}
		if !ok {
			m.releaseCredit(ctx, ceiling)
			step.Outcome = "budget_exhausted"
			step.Reason = "this month's cap cannot cover the most this call could cost"
			rec.Steps = append(rec.Steps, step)
			return fail(OutcomeBudgetExhausted, step.Reason, false)
		}

		req := built.req
		req.Model = model
		cr := m.call(ctx, t, job, provider, req, month, ceiling, p)
		resp, callErr := cr.resp, cr.err
		step.LatencyMS = cr.latency.Milliseconds()
		rec.LatencyMS += step.LatencyMS
		rec.Provider, rec.Model = provider.Name(), model
		rec.Usage = rec.Usage.add(resp.Usage)
		rec.CostMicroMinor += cr.cost
		step.CostMicroMinor = cr.cost
		step.Estimated = cr.estimated
		rec.CostEstimated = rec.CostEstimated || cr.estimated
		if resp.Usage != (Usage{}) {
			u := resp.Usage
			step.Usage = &u
		}
		if resp.Model != "" {
			step.ModelVersion, rec.ModelVersion = resp.Model, resp.Model
		}

		if callErr != nil {
			var pe *ProviderError
			transient := true
			if errors.As(callErr, &pe) {
				transient = pe.Transient
			}
			step.Reason = callErr.Error()
			// A transport that panicked, or a 200 that could not be read:
			// the provider's side failed, and asking again would pay again.
			if pe != nil && (pe.panicked || pe.Status == http.StatusOK) {
				step.Outcome = "provider_error"
				rec.Steps = append(rec.Steps, step)
				return fail(OutcomeProviderError, step.Reason, false)
			}
			if transient {
				step.Outcome = "provider_error"
				rec.Steps = append(rec.Steps, step)
				return fail(OutcomeProviderError, step.Reason, true)
			}
			step.Outcome = "refused"
			rec.Steps = append(rec.Steps, step)
			return fail(OutcomeRefused, step.Reason, false)
		}

		out, confidence, why := m.judge(ctx, t, resp)
		step.Confidence = confidence
		if why == "" && confidence != nil && *confidence < t.EscalateBelow {
			step.Outcome = "low_confidence"
			step.Reason = fmt.Sprintf("confidence %d is under this task's %d", *confidence, t.EscalateBelow)
		} else if why != "" {
			step.Outcome, step.Reason = "invalid", why
		} else {
			step.Outcome = "ok"
			rec.Steps = append(rec.Steps, step)
			return Result{Outcome: OutcomeOK, Output: out, Confidence: confidence, Model: model}, nil, nil
		}
		rec.Steps = append(rec.Steps, step)
		lastWhy = step.Reason
		if i == len(t.Ladder)-1 {
			break
		}
	}
	return fail(OutcomeInvalidOutput, lastWhy, false)
}

// judge decides whether a response is an answer: not declined, not cut off,
// the schema's shape with nothing extra, and passing the task's own Check.
func (m *Module) judge(ctx context.Context, t *Task, resp Response) (json.RawMessage, *int, string) {
	switch {
	case resp.StopReason == "max_tokens" || resp.StopReason == "length":
		return nil, nil, "the answer was cut off at the token limit"
	case resp.JSON == nil && resp.Refusal != "":
		return nil, nil, "the model declined: " + truncate(resp.Refusal, 200)
	case resp.JSON == nil:
		return nil, nil, "the model gave no answer"
	}
	if err := t.schema.validate(resp.JSON); err != nil {
		return nil, nil, err.Error()
	}
	if t.Check == nil {
		return resp.JSON, nil, ""
	}
	confidence, err := t.Check(ctx, resp.JSON)
	if err != nil {
		return nil, nil, "the task's check refused it: " + err.Error()
	}
	confidence = min(max(confidence, 0), 100)
	return resp.JSON, &confidence, ""
}

// Tokens are estimated before sending, and the estimate decides refusal
// against MaxInputTokens, so it errs large: refusing early beats sending a
// request that costs more than its reservation. ASCII text counts one token
// per three bytes, which over-counts English (about four characters a
// token); every other character counts one and a half, because Chinese,
// Japanese and Indic scripts run at a token a character or more, which one
// per three bytes would have under-counted. requestOverhead is what a
// provider adds around the request: the forced tool's own instructions,
// roles, formatting.
const requestOverhead = 512

// What a provider charges for a document, at the most: a PDF page is sent
// as its text and an image of the page, and an image is resized to about a
// megapixel. Floors, not averages: the estimate refuses early rather than
// reserve less than the call costs.
const (
	tokensPerPage  = 3000
	TokensPerImage = 1600
)

func estimateTokens(req Request) int {
	var ascii, wide int
	count := func(s string) {
		for _, r := range s {
			if r < utf8.RuneSelf {
				ascii++
			} else {
				wide++
			}
		}
	}
	count(req.System)
	count(string(req.Schema))
	for _, msg := range req.Messages {
		count(msg.Text)
	}
	n := ascii/3 + (wide*3+1)/2 + requestOverhead
	for _, d := range req.Documents {
		switch {
		case d.MediaType == "application/pdf":
			n += max(1, pdfPages(d.Bytes)) * tokensPerPage
		case strings.HasPrefix(d.MediaType, "image/"):
			n += TokensPerImage
		default:
			n += len(d.Bytes) / 3
		}
	}
	return n
}

// pdfPageRE finds a page object, not the page tree (/Pages).
var pdfPageRE = regexp.MustCompile(`/Type\s*/Page\b`)

// pdfPages counts a PDF's page objects without parsing it. A compressed
// object stream hides them, and then the count is low; the per-page floor
// and the one-page minimum are what keep that from mattering much.
func pdfPages(b []byte) int {
	return len(pdfPageRE.FindAllIndex(b, -1))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ------------------------------------------------------------------ cache

// cacheKey is the task, its version and the input under the policy: the
// same question asked under a different policy is a different request. The
// store is the row's own column, so two stores never share an answer.
func cacheKey(input json.RawMessage, p Policy) string {
	h := sha256.New()
	policy, _ := json.Marshal(p)
	h.Write(policy)
	h.Write([]byte{0})
	h.Write(input)
	return hex.EncodeToString(h.Sum(nil))
}

func (m *Module) recall(ctx context.Context, t *Task, input json.RawMessage, p Policy) (json.RawMessage, error) {
	var out []byte
	err := m.db.QueryRowContext(ctx, `
		SELECT output FROM ai_cache
		WHERE task = $1 AND task_version = $2 AND input_hash = $3 AND expires_at > now()`,
		t.Name, t.Version, cacheKey(input, p)).Scan(&out)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return out, err
}

func (m *Module) remember(ctx context.Context, tx *sql.Tx, t *Task, input json.RawMessage, p Policy, out json.RawMessage, executionID int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO ai_cache (task, task_version, input_hash, output, execution_id, expires_at)
		VALUES ($1, $2, $3, $4, $5, now() + make_interval(secs => $6))
		ON CONFLICT (store_id, task, task_version, input_hash)
		DO UPDATE SET output = EXCLUDED.output, execution_id = EXCLUDED.execution_id, expires_at = EXCLUDED.expires_at`,
		t.Name, t.Version, cacheKey(input, p), []byte(out), executionID, t.CacheFor.Seconds())
	return err
}

// forget clears the store's expired answers, a pass at a time.
func (m *Module) forget(ctx context.Context) error {
	_, err := m.db.ExecContext(ctx, `DELETE FROM ai_cache WHERE expires_at <= now()`)
	return err
}
