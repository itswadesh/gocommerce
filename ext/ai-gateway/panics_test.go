package aigateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/itswadesh/gocommerce/gctest"
)

// A task whose own code panics is a task that cannot be done, not a store
// that goes down: the job fails with the reason and is not tried again, the
// call it already paid for is settled and recorded, and the runner carries on.
func TestAPanickingCheckFailsTheJobAndSettles(t *testing.T) {
	task := rewriteTask()
	task.Check = func(ctx context.Context, out json.RawMessage) (int, error) { panic("the check fell over") }
	app, m, s := newGateway(t, reply(http.StatusOK, answer(smallID, goodAnswer, smallUsage)), task)
	ctx := context.Background()
	configure(t, app, ctx, nil)
	id := enqueue(t, app, m, ctx, listing{Title: "x"}, "k1")

	runJobs(t, m, ctx)
	j := mustJob(t, m, ctx, id)
	if j.Status != JobFailed || j.Attempts != 1 || !strings.Contains(j.LastError, "the check fell over") {
		t.Fatalf("job = %+v, want failed with the panic as its reason", j)
	}
	if j.ExecutionID == nil {
		t.Fatal("the run was not recorded")
	}
	e := mustExecution(t, m, ctx, *j.ExecutionID)
	const cost = 412*100 + 187*500 + 1536*125
	if e.Outcome != OutcomeTaskError || e.CostMicroMinor != cost {
		t.Errorf("record = %s at %d, want task_error at the call's cost %d", e.Outcome, e.CostMicroMinor, cost)
	}
	if b := mustBudget(t, m, ctx); b.ReservedMicroMinor != 0 || b.SpentMicroMinor != cost {
		t.Errorf("budget = %+v, want the call settled and nothing held", b)
	}
	// Never tried again: a panic is not a provider having a bad minute.
	if _, err := app.DB().ExecContext(ctx, `UPDATE ai_jobs SET next_run_at = now() - interval '1 hour', locked_until = NULL`); err != nil {
		t.Fatal(err)
	}
	runJobs(t, m, ctx)
	if s.count() != 1 {
		t.Errorf("%d calls, want the panicking job left alone", s.count())
	}
}

// A transport that panics mid-call: whether the request left is unknown, so
// the most it could have cost is what is charged, and nothing stays held.
func TestAPanickingTransportFailsTheJobAndSettles(t *testing.T) {
	m := New(Config{
		Client: &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
			panic("the transport fell over")
		})},
		CallTimeout:  2 * time.Second,
		PollInterval: time.Hour,
	})
	if err := m.RegisterTask(rewriteTask()); err != nil {
		t.Fatal(err)
	}
	app := gctest.New(t, m)
	ctx := context.Background()
	configure(t, app, ctx, nil)
	id := enqueue(t, app, m, ctx, listing{Title: "x"}, "k1")

	runJobs(t, m, ctx)
	j := mustJob(t, m, ctx, id)
	if j.Status != JobFailed || !strings.Contains(j.LastError, "the transport fell over") || j.ExecutionID == nil {
		t.Fatalf("job = %+v, want failed with the panic as its reason and a record", j)
	}
	// The small model's ceiling: 4000 in at 125, 1000 out at 500.
	const ceiling = 4000*125 + 1000*500
	e := mustExecution(t, m, ctx, *j.ExecutionID)
	if e.Outcome != OutcomeProviderError || e.CostMicroMinor != ceiling {
		t.Errorf("record = %s at %d, want provider_error charged at the ceiling %d", e.Outcome, e.CostMicroMinor, ceiling)
	}
	if b := mustBudget(t, m, ctx); b.ReservedMicroMinor != 0 || b.SpentMicroMinor != ceiling {
		t.Errorf("budget = %+v, want the ceiling spent and nothing held", b)
	}
}

// Run is for background code of its own; a panic in the task reaches that
// code as an error, never as a panic.
func TestRunTurnsAPanickingBuildIntoAnError(t *testing.T) {
	task := rewriteTask()
	task.Build = func(ctx context.Context, input json.RawMessage, p Policy) (Request, []SourceRef, error) {
		panic("the build fell over")
	}
	app, m, s := newGateway(t, reply(http.StatusOK, answer(smallID, goodAnswer, smallUsage)), task)
	ctx := context.Background()
	configure(t, app, ctx, nil)

	res, err := m.Run(ctx, "test.rewrite", listing{Title: "x"})
	re := runErr(t, err)
	if re.Outcome != OutcomeTaskError || re.Transient || !strings.Contains(re.Reason, "the build fell over") {
		t.Fatalf("error = %+v", re)
	}
	if s.count() != 0 || res.ExecutionID == 0 {
		t.Errorf("%d calls, record %d: want none sent and the run recorded", s.count(), res.ExecutionID)
	}
	if b := mustBudget(t, m, ctx); b.ReservedMicroMinor != 0 {
		t.Errorf("%d held after a run that never called", b.ReservedMicroMinor)
	}
}
