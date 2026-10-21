package aigateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Model work caused by a request or an event is a job (D106, ADR 0004): a
// row written in the transaction that caused it, so a crash between "the
// message arrived" and "the message was classified" loses nothing, and run
// after the commit by this module's runner, so no transaction waits on a
// provider (rule 5).
//
// The runner is the outbox's, one layer out, and deliberately the same shape:
// claim with FOR UPDATE SKIP LOCKED under a visibility window, one store at a
// time and bound to it, back off, and park what will not go through. Where it
// differs is what earns a retry. An event that fails is retried whatever the
// reason; a model call costs money each time, so only a transient failure —
// a timeout, a rate limit, a provider's own outage — is tried again. A
// refusal, an answer that fails its checks on the strong model, an exhausted
// budget or a missing price goes straight to failed with its reason, and
// stays there for a person: that is the task's human queue.

// Job states.
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobDone      = "done"
	JobFailed    = "failed"
	JobDead      = "dead"
	JobCancelled = "cancelled"
)

// Job is one piece of queued model work, as the panel lists it. Its input
// is not listed: it is store data, and the run's record says what was sent.
type Job struct {
	ID          int64           `json:"id"`
	Task        string          `json:"task"`
	Status      string          `json:"status"`
	Attempts    int             `json:"attempts"`
	MaxAttempts int             `json:"max_attempts"`
	NextRunAt   time.Time       `json:"next_run_at"`
	LastError   string          `json:"last_error,omitempty"`
	TraceID     string          `json:"trace_id"`
	Result      json.RawMessage `json:"result,omitempty"`
	ExecutionID *int64          `json:"execution_id,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	FinishedAt  *time.Time      `json:"finished_at,omitempty"`
}

// Enqueue writes a job in the caller's transaction. A key already used in
// this store returns the job it made, so an event delivered twice makes one
// job; an empty key never matches another.
func (m *Module) Enqueue(ctx context.Context, tx *sql.Tx, task string, input any, idemKey string) (int64, error) {
	if m.task(task) == nil {
		return 0, fmt.Errorf("no ai task is called %q", task)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return 0, fmt.Errorf("ai task %s: encode the input: %w", task, err)
	}
	var key any
	if idemKey = strings.TrimSpace(idemKey); idemKey != "" {
		key = idemKey
	}
	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO ai_jobs (task, input, idem_key, trace_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (store_id, idem_key) DO NOTHING
		RETURNING id`, task, raw, key, gocommerce.TraceID(ctx)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT id FROM ai_jobs WHERE idem_key = $1`, idemKey).Scan(&id)
	}
	return id, err
}

// claimedJob is a job a worker holds. attempts is the fence: an outcome is
// written only while the row still carries the attempt this worker claimed,
// so a worker that lost its lock and comes back late changes nothing.
type claimedJob struct {
	id       int64
	task     string
	input    json.RawMessage
	attempts int
	trace    string
}

// claim takes one due job of ctx's store, or none. A running job whose lock
// has run out belonged to a worker that died, and is due again.
func (m *Module) claim(ctx context.Context) (*claimedJob, error) {
	var j claimedJob
	err := m.db.QueryRowContext(ctx, `
		WITH due AS MATERIALIZED (
			SELECT id FROM ai_jobs
			WHERE (status = 'queued' AND next_run_at <= now())
			   OR (status = 'running' AND locked_until <= now())
			ORDER BY next_run_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE ai_jobs j
		   SET status = 'running', attempts = j.attempts + 1,
		       locked_until = now() + make_interval(secs => $1)
		  FROM due
		 WHERE j.id = due.id
		RETURNING j.id, j.task, j.input, j.attempts, j.trace_id`,
		visibility.Seconds()).Scan(&j.id, &j.task, &j.input, &j.attempts, &j.trace)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim an ai job: %w", err)
	}
	return &j, nil
}

// renew extends a claim before a call, so a job's second rung does not run
// past the window the first one started.
func (m *Module) renew(ctx context.Context, j *claimedJob) {
	if _, err := m.db.ExecContext(ctx, `
		UPDATE ai_jobs SET locked_until = now() + make_interval(secs => $3)
		WHERE id = $1 AND attempts = $2 AND status = 'running'`,
		j.id, j.attempts, visibility.Seconds()); err != nil {
		m.log.Warn("ai job lock could not be renewed", "job", j.id, "error", err)
	}
}

// jobOutcome is where a job goes after a run.
type jobOutcome struct {
	status      string
	lastError   string
	result      json.RawMessage
	executionID *int64
	retryIn     time.Duration
}

func outcomeFor(j *claimedJob, res Result, runErr *RunError) jobOutcome {
	var exec *int64
	if res.ExecutionID != 0 {
		id := res.ExecutionID
		exec = &id
	}
	switch {
	case runErr == nil:
		return jobOutcome{status: JobDone, result: res.Output, executionID: exec}
	case runErr.Transient && j.attempts >= maxAttempts:
		return jobOutcome{status: JobDead, lastError: runErr.Reason, executionID: exec}
	case runErr.Transient:
		return jobOutcome{status: JobQueued, lastError: runErr.Reason, executionID: exec, retryIn: backoff(j.attempts)}
	default:
		return jobOutcome{status: JobFailed, lastError: runErr.Reason, executionID: exec}
	}
}

// backoff is the outbox's: a second, doubling, capped at fifteen minutes.
func backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := time.Duration(math.Pow(2, float64(attempts-1))) * time.Second
	if d > maxBackoff || d <= 0 {
		return maxBackoff
	}
	return d
}

// finishJob writes a job's outcome if this worker still holds it. tx is the
// run record's transaction, or nil when there is no record.
func (m *Module) finishJob(ctx context.Context, tx *sql.Tx, j *claimedJob, o jobOutcome) error {
	type execer interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	}
	var q execer = m.db
	if tx != nil {
		q = tx
	}
	var result any
	if o.result != nil {
		result = []byte(o.result)
	}
	var lastError any
	if o.lastError != "" {
		lastError = truncate(o.lastError, 1000)
	}
	finished := o.status != JobQueued
	_, err := q.ExecContext(ctx, `
		UPDATE ai_jobs
		   SET status = $3,
		       last_error = CASE WHEN $3 = 'done' THEN NULL ELSE coalesce($4, last_error) END,
		       result = coalesce($5, result),
		       execution_id = coalesce($6, execution_id),
		       next_run_at = now() + make_interval(secs => $7),
		       locked_until = NULL,
		       finished_at = CASE WHEN $8 THEN now() END
		 WHERE id = $1 AND attempts = $2 AND status = 'running'`,
		j.id, j.attempts, o.status, lastError, result, o.executionID, o.retryIn.Seconds(), finished)
	return err
}

// process runs one claimed job and settles it.
func (m *Module) process(ctx context.Context, j *claimedJob) {
	ctx = gocommerce.WithTraceID(ctx, j.trace)
	t := m.task(j.task)
	if t == nil {
		// A job outlived the binary that knew its task: a person decides.
		m.settleWithoutRecord(ctx, j, jobOutcome{status: JobFailed,
			lastError: fmt.Sprintf("this build has no task called %q", j.task)})
		return
	}
	_, err := m.execute(ctx, t, j.input, j)
	var re *RunError
	switch {
	case err == nil, errors.As(err, &re):
		// The run's own transaction settled the job with its record.
	case errors.Is(err, ErrNotReady):
		m.settleWithoutRecord(ctx, j, jobOutcome{status: JobFailed, lastError: err.Error()})
	default:
		// The store's database failed under the run: worth another try, on
		// the same budget of attempts as a provider's outage.
		o := jobOutcome{status: JobQueued, lastError: err.Error(), retryIn: backoff(j.attempts)}
		if j.attempts >= maxAttempts {
			o = jobOutcome{status: JobDead, lastError: err.Error()}
		}
		m.settleWithoutRecord(ctx, j, o)
	}
}

func (m *Module) settleWithoutRecord(ctx context.Context, j *claimedJob, o jobOutcome) {
	if err := m.finishJob(context.WithoutCancel(ctx), nil, j, o); err != nil {
		m.log.Error("ai job could not be settled", "job", j.id, "error", err)
	}
}

// RunJobs runs what is due. A context bound to a store runs that store's
// jobs; one bound to none runs every store's, each bound in turn (D83). It
// is exported for the reason DrainOutbox is: a test, or an operator in a
// hurry, can run a pass instead of waiting out the ticker.
func (m *Module) RunJobs(ctx context.Context) (int, error) {
	if _, bound := gocommerce.StoreFrom(ctx); bound {
		return m.runStore(ctx)
	}
	total := 0
	err := m.app.ForEachStore(ctx, func(ctx context.Context) error {
		n, err := m.runStore(ctx)
		total += n
		return err
	})
	return total, err
}

func (m *Module) runStore(ctx context.Context) (int, error) {
	n := 0
	for n < jobsPerPass {
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		j, err := m.claim(ctx)
		if err != nil {
			return n, err
		}
		if j == nil {
			break
		}
		m.process(ctx, j)
		n++
	}
	return n, m.forget(ctx)
}

// start spawns the runner and returns: OnStart runs before the listener
// accepts traffic, so a hook that blocked would be a store that never serves.
func (m *Module) start(ctx context.Context) error {
	go m.loop(ctx)
	return nil
}

func (m *Module) loop(ctx context.Context) {
	m.log.Info("ai job runner started", "poll", m.cfg.PollInterval)
	t := time.NewTicker(m.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := m.RunJobs(ctx); err != nil && !errors.Is(err, context.Canceled) {
				m.log.Error("ai job pass failed", "error", err)
			}
		}
	}
}

// ------------------------------------------------------------- the screen

const jobColumns = `id, task, status, attempts, next_run_at, coalesce(last_error, ''), trace_id, result,
	execution_id, created_at, finished_at`

func scanJob(row interface{ Scan(...any) error }) (*Job, error) {
	j := Job{MaxAttempts: maxAttempts}
	var result []byte
	if err := row.Scan(&j.ID, &j.Task, &j.Status, &j.Attempts, &j.NextRunAt, &j.LastError, &j.TraceID,
		&result, &j.ExecutionID, &j.CreatedAt, &j.FinishedAt); err != nil {
		return nil, err
	}
	if len(result) > 0 {
		j.Result = result
	}
	return &j, nil
}

// Job reads one of ctx's store's jobs.
func (m *Module) Job(ctx context.Context, id int64) (*Job, error) {
	j, err := scanJob(m.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM ai_jobs WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("no AI job %d", id)
	}
	return j, err
}

// Jobs is a page of ctx's store's jobs, newest first, optionally in one state.
func (m *Module) Jobs(ctx context.Context, status string, limit, offset int) ([]*Job, int, error) {
	where, args := "true", []any{}
	if status != "" {
		where, args = "status = $1", append(args, status)
	}
	var total int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM ai_jobs WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := m.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM ai_jobs WHERE `+where+
		fmt.Sprintf(` ORDER BY id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, j)
	}
	return out, total, rows.Err()
}

// RetryJob puts a failed, dead or cancelled job back in the queue with a
// fresh set of attempts: a retry a person asked for is a fresh start, not
// attempt thirteen of twelve. Its last error stays until the next run
// replaces it, so the diagnosis outlives the repair.
func (m *Module) RetryJob(ctx context.Context, id int64) (*Job, error) {
	return m.moveJob(ctx, id, `
		UPDATE ai_jobs SET status = 'queued', attempts = 0, next_run_at = now(),
		       locked_until = NULL, finished_at = NULL
		 WHERE id = $1 AND status IN ('failed', 'dead', 'cancelled')`,
		"only a failed, dead or cancelled job can be retried")
}

// CancelJob takes a job out of the queue for good. A running job's worker
// finds it cancelled when it comes to write its outcome, and writes nothing;
// the call it was making is not stopped, and its record is kept.
func (m *Module) CancelJob(ctx context.Context, id int64) (*Job, error) {
	return m.moveJob(ctx, id, `
		UPDATE ai_jobs SET status = 'cancelled', locked_until = NULL, finished_at = now()
		 WHERE id = $1 AND status IN ('queued', 'running', 'failed', 'dead')`,
		"a finished or cancelled job cannot be cancelled")
}

func (m *Module) moveJob(ctx context.Context, id int64, update, refusal string) (*Job, error) {
	res, err := m.db.ExecContext(ctx, update, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		j, err := m.Job(ctx, id)
		if err != nil {
			return nil, err
		}
		return nil, gocommerce.Conflictf("job %d is %s: %s", id, j.Status, refusal)
	}
	return m.Job(ctx, id)
}
