package aigateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

func enqueue(t *testing.T, app *gocommerce.App, m *Module, ctx context.Context, input any, key string) int64 {
	t.Helper()
	var id int64
	err := gocommerce.InTx(ctx, app.DB(), func(tx *sql.Tx) error {
		var err error
		id, err = m.Enqueue(ctx, tx, "test.rewrite", input, key)
		return err
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	return id
}

func mustJob(t *testing.T, m *Module, ctx context.Context, id int64) *Job {
	t.Helper()
	j, err := m.Job(ctx, id)
	if err != nil {
		t.Fatalf("job %d: %v", id, err)
	}
	return j
}

func runJobs(t *testing.T, m *Module, ctx context.Context) int {
	t.Helper()
	n, err := m.RunJobs(ctx)
	if err != nil {
		t.Fatalf("run jobs: %v", err)
	}
	return n
}

// A job is written in its cause's transaction, once per key, and goes with
// it when that transaction rolls back.
func TestAJobIsEnqueuedInTheCallersTransactionOncePerKey(t *testing.T) {
	app, m, _ := newGateway(t, reply(http.StatusOK, "{}"), rewriteTask())
	ctx := gocommerce.WithTraceID(context.Background(), "trace-of-the-cause")

	first := enqueue(t, app, m, ctx, listing{Title: "x"}, "product:42")
	again := enqueue(t, app, m, ctx, listing{Title: "x"}, "product:42")
	if first == 0 || again != first {
		t.Errorf("ids %d then %d, want one job for one key", first, again)
	}
	if j := mustJob(t, m, ctx, first); j.Status != JobQueued || j.TraceID != "trace-of-the-cause" || j.Task != "test.rewrite" {
		t.Errorf("job = %+v", j)
	}

	rollback := errors.New("the cause failed")
	err := gocommerce.InTx(ctx, app.DB(), func(tx *sql.Tx) error {
		if _, err := m.Enqueue(ctx, tx, "test.rewrite", listing{Title: "y"}, "product:43"); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if n := countRows(t, app, ctx, "ai_jobs"); n != 1 {
		t.Errorf("%d jobs, want the rolled-back one gone", n)
	}

	err = gocommerce.InTx(ctx, app.DB(), func(tx *sql.Tx) error {
		_, err := m.Enqueue(ctx, tx, "no.such.task", nil, "k")
		return err
	})
	if err == nil {
		t.Error("a job for a task nobody registered was accepted")
	}
}

// A 529 is the provider having a bad minute: the job waits and tries again,
// and the second try's answer finishes it.
func TestAn529RetriesWithBackoff(t *testing.T) {
	var calls atomic.Int32
	app, m, _ := newGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			reply(529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)(w, r)
			return
		}
		reply(http.StatusOK, answer(smallID, goodAnswer, smallUsage))(w, r)
	}, rewriteTask())
	ctx := context.Background()
	configure(t, app, ctx, nil)
	id := enqueue(t, app, m, ctx, listing{Title: "x"}, "k1")

	runJobs(t, m, ctx)
	j := mustJob(t, m, ctx, id)
	if j.Status != JobQueued || j.Attempts != 1 || !strings.Contains(j.LastError, "Overloaded") {
		t.Fatalf("after a 529: %+v", j)
	}
	// The outbox's backoff: a second after the first failure.
	if wait := time.Until(j.NextRunAt); wait < 200*time.Millisecond || wait > 3*time.Second {
		t.Errorf("next try in %v, want about a second", wait)
	}
	if runJobs(t, m, ctx) != 0 {
		t.Error("a backed-off job ran before its time")
	}

	if _, err := app.DB().ExecContext(ctx, `UPDATE ai_jobs SET next_run_at = now() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	runJobs(t, m, ctx)
	j = mustJob(t, m, ctx, id)
	if j.Status != JobDone || j.Attempts != 2 || j.ExecutionID == nil || j.FinishedAt == nil {
		t.Fatalf("after the retry: %+v", j)
	}
	if compactJSON(t, j.Result) != compactJSON(t, []byte(goodAnswer)) {
		t.Errorf("result = %s", j.Result)
	}
	if e := mustExecution(t, m, ctx, *j.ExecutionID); e.JobID == nil || *e.JobID != id {
		t.Errorf("the run does not name its job: %+v", e.JobID)
	}
	// Both tries are on the record: the failure cost nothing, the answer
	// cost its tokens.
	var failed, ok int
	if err := app.DB().QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE outcome = 'provider_error'), count(*) FILTER (WHERE outcome = 'ok')
		FROM ai_executions WHERE job_id = $1`, id).Scan(&failed, &ok); err != nil {
		t.Fatal(err)
	}
	if failed != 1 || ok != 1 {
		t.Errorf("%d failed and %d ok runs for the job", failed, ok)
	}
}

// A 400 is a refusal, and asking again would be refused again: the job goes
// to the people's queue at once.
func TestA400FailsTheJobAtOnce(t *testing.T) {
	app, m, s := newGateway(t, reply(http.StatusBadRequest,
		`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`), rewriteTask())
	ctx := context.Background()
	configure(t, app, ctx, nil)
	id := enqueue(t, app, m, ctx, listing{Title: "x"}, "k1")

	runJobs(t, m, ctx)
	j := mustJob(t, m, ctx, id)
	if j.Status != JobFailed || j.Attempts != 1 || !strings.Contains(j.LastError, "prompt is too long") || j.FinishedAt == nil {
		t.Fatalf("job = %+v", j)
	}
	if _, err := app.DB().ExecContext(ctx, `UPDATE ai_jobs SET next_run_at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	runJobs(t, m, ctx)
	if s.count() != 1 {
		t.Errorf("%d calls, want the refusal not retried", s.count())
	}
}

// An invalid answer from the strong model fails the job without retrying.
func TestAnInvalidAnswerFailsTheJobWithoutRetrying(t *testing.T) {
	app, m, s := newGateway(t, byModel(map[string]string{
		smallID:  answer(smallID, badAnswer, smallUsage),
		strongID: answer(strongID, badAnswer, smallUsage),
	}), rewriteTask())
	ctx := context.Background()
	configure(t, app, ctx, nil)
	id := enqueue(t, app, m, ctx, listing{Title: "x"}, "k1")

	runJobs(t, m, ctx)
	if j := mustJob(t, m, ctx, id); j.Status != JobFailed || j.Attempts != 1 || !strings.Contains(j.LastError, "tags") {
		t.Fatalf("job = %+v", j)
	}
	if s.count() != 2 {
		t.Errorf("%d calls, want small then strong and no more", s.count())
	}
}

// Twelve transient failures in a row and the job is dead, listed for a
// person rather than retried for ever.
func TestAJobPastItsAttemptsIsDeadAndListed(t *testing.T) {
	app, m, _ := newGateway(t, reply(529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`), rewriteTask())
	ctx := context.Background()
	configure(t, app, ctx, nil)
	id := enqueue(t, app, m, ctx, listing{Title: "x"}, "k1")
	if _, err := app.DB().ExecContext(ctx, `UPDATE ai_jobs SET attempts = $2 WHERE id = $1`, id, maxAttempts-1); err != nil {
		t.Fatal(err)
	}

	runJobs(t, m, ctx)
	j := mustJob(t, m, ctx, id)
	if j.Status != JobDead || j.Attempts != maxAttempts || j.FinishedAt == nil {
		t.Fatalf("job = %+v", j)
	}

	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/ai-gateway/jobs?status=dead", nil)
	var dead []Job
	gctest.DecodeData(t, rec, &dead)
	if len(dead) != 1 || dead[0].ID != id || !strings.Contains(dead[0].LastError, "Overloaded") {
		t.Errorf("dead jobs listed = %+v", dead)
	}
}

// Retry puts failed work back with a fresh set of attempts; cancel takes
// work out of the queue for good. Both answer only for what they can act on.
func TestJobsAreRetriedAndCancelledFromTheRoutes(t *testing.T) {
	var refuse atomic.Bool
	refuse.Store(true)
	app, m, _ := newGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if refuse.Load() {
			reply(http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`)(w, r)
			return
		}
		reply(http.StatusOK, answer(smallID, goodAnswer, smallUsage))(w, r)
	}, rewriteTask())
	ctx := context.Background()
	configure(t, app, ctx, nil)

	failed := enqueue(t, app, m, ctx, listing{Title: "x"}, "k1")
	runJobs(t, m, ctx)
	queued := enqueue(t, app, m, ctx, listing{Title: "y"}, "k2")

	post := func(path string) int {
		return gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/ai-gateway/jobs/"+path, nil).Code
	}
	if code := post(fmt.Sprintf("%d/cancel", queued)); code != http.StatusOK {
		t.Fatalf("cancel = %d", code)
	}
	if j := mustJob(t, m, ctx, queued); j.Status != JobCancelled || j.FinishedAt == nil {
		t.Errorf("cancelled job = %+v", j)
	}
	if code := post(fmt.Sprintf("%d/cancel", queued)); code != http.StatusConflict {
		t.Errorf("cancelling twice = %d, want 409", code)
	}

	refuse.Store(false)
	if code := post(fmt.Sprintf("%d/retry", failed)); code != http.StatusOK {
		t.Fatalf("retry = %d", code)
	}
	if j := mustJob(t, m, ctx, failed); j.Status != JobQueued || j.Attempts != 0 || j.LastError == "" {
		t.Errorf("retried job = %+v, want queued afresh with its diagnosis kept", j)
	}
	runJobs(t, m, ctx)
	if j := mustJob(t, m, ctx, failed); j.Status != JobDone {
		t.Errorf("after the retry = %+v", j)
	}
	if code := post(fmt.Sprintf("%d/retry", failed)); code != http.StatusConflict {
		t.Errorf("retrying a finished job = %d, want 409", code)
	}
	if code := post("999999/retry"); code != http.StatusNotFound {
		t.Errorf("retrying no job = %d, want 404", code)
	}
}

// A job keeps the trace of the request that caused it, and so does its run.
func TestAJobCarriesItsTraceIntoItsRun(t *testing.T) {
	app, m, _ := newGateway(t, reply(http.StatusOK, answer(smallID, goodAnswer, smallUsage)), rewriteTask())
	ctx := context.Background()
	configure(t, app, ctx, nil)
	id := enqueue(t, app, m, gocommerce.WithTraceID(ctx, "trace-from-the-request"), listing{Title: "x"}, "k1")

	runJobs(t, m, ctx)
	j := mustJob(t, m, ctx, id)
	if j.ExecutionID == nil {
		t.Fatalf("job = %+v", j)
	}
	if e := mustExecution(t, m, ctx, *j.ExecutionID); e.TraceID != "trace-from-the-request" {
		t.Errorf("the run's trace = %q", e.TraceID)
	}
}

// A job whose store has the gateway off goes to a person with the reason,
// and no call is made.
func TestAJobForAStoreWithTheGatewayOffFailsWithTheReason(t *testing.T) {
	app, m, s := newGateway(t, reply(http.StatusOK, answer(smallID, goodAnswer, smallUsage)), rewriteTask())
	ctx := context.Background()
	id := enqueue(t, app, m, ctx, listing{Title: "x"}, "k1")

	runJobs(t, m, ctx)
	if j := mustJob(t, m, ctx, id); j.Status != JobFailed || !strings.Contains(j.LastError, "switched off") {
		t.Errorf("job = %+v", j)
	}
	if s.count() != 0 {
		t.Error("a call was made for a store with the gateway off")
	}
}

// A job claimed by a worker that then died is claimed again once its lock
// runs out, and the late worker cannot overwrite the new one's outcome.
func TestAnAbandonedClaimIsTakenAgain(t *testing.T) {
	app, m, _ := newGateway(t, reply(http.StatusOK, answer(smallID, goodAnswer, smallUsage)), rewriteTask())
	ctx := context.Background()
	configure(t, app, ctx, nil)
	id := enqueue(t, app, m, ctx, listing{Title: "x"}, "k1")

	claimed, err := m.claim(ctx)
	if err != nil || claimed == nil || claimed.id != id {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if again, _ := m.claim(ctx); again != nil {
		t.Fatal("a claimed job was claimed twice inside its lock")
	}
	if _, err := app.DB().ExecContext(ctx, `UPDATE ai_jobs SET locked_until = now() - interval '1 second' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	runJobs(t, m, ctx)
	if j := mustJob(t, m, ctx, id); j.Status != JobDone || j.Attempts != 2 {
		t.Fatalf("job = %+v", j)
	}
	// The first worker comes back with a failure: it is too late to count.
	if err := m.finishJob(ctx, nil, claimed, jobOutcome{status: JobFailed, lastError: "late"}); err != nil {
		t.Fatal(err)
	}
	if j := mustJob(t, m, ctx, id); j.Status != JobDone {
		t.Errorf("a stale worker overwrote the outcome: %+v", j)
	}
}

// A job cancelled and retried while a worker still held it: the retry puts
// the attempt count back where it was, so it cannot be what tells the two
// claims apart. The first worker's late answer must not land on the second.
func TestALateWorkerCannotWriteOverARetriedClaim(t *testing.T) {
	app, m, _ := newGateway(t, reply(http.StatusOK, answer(smallID, goodAnswer, smallUsage)), rewriteTask())
	ctx := context.Background()
	configure(t, app, ctx, nil)
	id := enqueue(t, app, m, ctx, listing{Title: "x"}, "k1")

	stale, err := m.claim(ctx)
	if err != nil || stale == nil {
		t.Fatalf("first claim = %+v, %v", stale, err)
	}
	if _, err := m.CancelJob(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RetryJob(ctx, id); err != nil {
		t.Fatal(err)
	}
	fresh, err := m.claim(ctx)
	if err != nil || fresh == nil || fresh.attempts != stale.attempts {
		t.Fatalf("second claim = %+v, %v; want the same attempt number as the first", fresh, err)
	}

	if err := m.finishJob(ctx, nil, stale, jobOutcome{status: JobFailed, lastError: "the stale worker"}); err != nil {
		t.Fatal(err)
	}
	if j := mustJob(t, m, ctx, id); j.Status != JobRunning || j.LastError == "the stale worker" {
		t.Fatalf("job = %+v: the stale worker wrote over the new claim", j)
	}
	if err := m.finishJob(ctx, nil, fresh, jobOutcome{status: JobDone}); err != nil {
		t.Fatal(err)
	}
	if j := mustJob(t, m, ctx, id); j.Status != JobDone {
		t.Errorf("the live worker's answer did not land: %+v", j)
	}
}
