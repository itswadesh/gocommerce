package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"
)

// Outbox delivery policy.
const (
	// outboxMaxAttempts before a row is parked as dead. Dead-lettering beats
	// deleting: an event nobody could deliver is evidence, and evidence should
	// survive long enough to be looked at.
	outboxMaxAttempts = 12
	// outboxVisibility is how long a claimed row is hidden from other workers.
	// If the process dies mid-delivery the row reappears after this, which is
	// the at-least-once guarantee doing its job.
	outboxVisibility = 60 * time.Second
	outboxMaxBackoff = 15 * time.Minute
)

// outbox writes and delivers durable events.
//
// The problem it solves: committing an order and then publishing its event are
// two operations, and a process that dies between them loses the event while
// keeping the order — a paid order nobody was told about. Writing the event to
// a table inside the same transaction makes that gap impossible.
type outbox struct {
	db        *sql.DB
	bus       *eventBus
	log       *slog.Logger
	batchSize int
	poll      time.Duration

	// delivered is signalled after every successful pass; tests wait on it
	// instead of sleeping.
	delivered chan struct{}
	// wake lets a request that just wrote an event ask for immediate
	// delivery, so the common case does not wait out the poll interval.
	wake chan struct{}
}

func (o *outbox) nudge() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// write appends an event to the outbox inside the caller's transaction. It is
// the only way core state changes announce themselves.
func (o *outbox) write(ctx context.Context, tx *sql.Tx, name string, aggregateType string, aggregateID int64, payload any) error {
	id, err := newUUID()
	if err != nil {
		return err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s payload: %w", name, err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO outbox_events (event_id, event_name, event_version, aggregate_type, aggregate_id, payload)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, name, 1, aggregateType, aggregateID, data)
	if err != nil {
		return fmt.Errorf("write outbox event %s: %w", name, err)
	}
	return nil
}

// run is the dispatcher loop, started with the app and stopped with it.
func (o *outbox) run(ctx context.Context) {
	timer := time.NewTimer(o.poll)
	defer timer.Stop()

	for {
		n, err := o.deliverBatch(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			o.log.Error("outbox pass failed", "error", err)
		case n > 0:
			// Work found: come straight back for more rather than waiting out
			// the poll interval on a backlog.
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-o.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(o.poll)
		case <-timer.C:
			timer.Reset(o.poll)
		}
	}
}

// deliverBatch claims a batch of unpublished events and delivers them,
// returning how many were claimed.
//
// The claim uses FOR UPDATE SKIP LOCKED, which is what makes running several
// application instances safe: each worker takes rows no other worker holds,
// with no coordination beyond the database.
func (o *outbox) deliverBatch(ctx context.Context) (int, error) {
	rows, err := o.db.QueryContext(ctx, `
		UPDATE outbox_events o
		SET attempts = o.attempts + 1,
		    available_at = now() + make_interval(secs => $2)
		FROM (
			SELECT id FROM outbox_events
			WHERE published_at IS NULL AND NOT dead AND available_at <= now()
			ORDER BY id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		) AS claimed
		WHERE o.id = claimed.id
		RETURNING o.id, o.event_id, o.event_name, o.event_version,
		          o.aggregate_type, o.aggregate_id, o.payload, o.created_at, o.attempts`,
		o.batchSize, outboxVisibility.Seconds())
	if err != nil {
		return 0, fmt.Errorf("claim outbox batch: %w", err)
	}

	type claimed struct {
		rowID    int64
		attempts int
		event    Event
	}
	var batch []claimed
	for rows.Next() {
		var c claimed
		if err := rows.Scan(&c.rowID, &c.event.ID, &c.event.Name, &c.event.Version,
			&c.event.AggregateType, &c.event.AggregateID, &c.event.Data,
			&c.event.At, &c.attempts); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan outbox row: %w", err)
		}
		batch = append(batch, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("read outbox batch: %w", err)
	}
	rows.Close()

	for _, c := range batch {
		if err := o.bus.dispatch(ctx, c.event); err != nil {
			o.fail(ctx, c.rowID, c.attempts, c.event, err)
			continue
		}
		if err := o.markPublished(ctx, c.rowID); err != nil {
			// The handlers already ran. Failing to record that means the event
			// is redelivered later, which is exactly what at-least-once
			// promises and why handlers must be idempotent.
			o.log.Error("could not mark event published",
				"event_id", c.event.ID, "error", err)
		}
	}

	if len(batch) > 0 {
		o.signal()
	}
	return len(batch), nil
}

func (o *outbox) markPublished(ctx context.Context, rowID int64) error {
	_, err := o.db.ExecContext(ctx,
		`UPDATE outbox_events SET published_at = now(), last_error = NULL WHERE id = $1`, rowID)
	return err
}

// fail schedules a retry with exponential backoff, or parks the row when it
// has failed too many times.
func (o *outbox) fail(ctx context.Context, rowID int64, attempts int, e Event, cause error) {
	if attempts >= outboxMaxAttempts {
		if _, err := o.db.ExecContext(ctx,
			`UPDATE outbox_events SET dead = true, last_error = $2 WHERE id = $1`,
			rowID, cause.Error()); err != nil {
			o.log.Error("could not dead-letter event", "event_id", e.ID, "error", err)
		}
		o.log.Error("event dead-lettered after repeated failures",
			"event", e.Name, "event_id", e.ID, "attempts", attempts, "error", cause)
		return
	}

	delay := outboxBackoff(attempts)
	if _, err := o.db.ExecContext(ctx, `
		UPDATE outbox_events
		SET available_at = now() + make_interval(secs => $2), last_error = $3
		WHERE id = $1`, rowID, delay.Seconds(), cause.Error()); err != nil {
		o.log.Error("could not schedule event retry", "event_id", e.ID, "error", err)
	}
	o.log.Warn("event delivery failed, will retry",
		"event", e.Name, "event_id", e.ID, "attempts", attempts, "retry_in", delay)
}

func outboxBackoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	secs := math.Pow(2, float64(attempts-1))
	d := time.Duration(secs) * time.Second
	if d > outboxMaxBackoff || d <= 0 {
		return outboxMaxBackoff
	}
	return d
}

func (o *outbox) signal() {
	select {
	case o.delivered <- struct{}{}:
	default:
	}
}

// DrainOutbox delivers every event that is due, returning how many were
// processed. Tests use it to make delivery synchronous without waiting on the
// dispatcher's poll interval.
//
// It cannot recover a dead-lettered row: deliverBatch's claim filters NOT
// dead, deliberately, so a parked row is invisible to the dispatcher forever.
// RequeueEvent is what un-parks one.
func (a *App) DrainOutbox(ctx context.Context) (int, error) {
	total := 0
	for {
		n, err := a.outbox.deliverBatch(ctx)
		if err != nil {
			return total, err
		}
		total += n
		if n == 0 {
			return total, nil
		}
	}
}

// DrainOutboxFor delivers until nothing is due or the budget is spent,
// reporting whether it stopped short.
//
// Two things separate it from DrainOutbox, and both exist because this one is
// reachable from an HTTP request.
//
// The delivery work runs on a context that cannot be cancelled. deliverBatch
// claims a whole batch in one UPDATE — attempts + 1, available_at pushed out —
// before it dispatches any of it, so a pass cut off mid-dispatch records a
// delivery failure against every event it had claimed and not yet reached:
// retries burned, backoff scheduled, and at outboxMaxAttempts a perfectly
// healthy event parked as dead. The background dispatcher never meets that
// because o.run holds the server's lifetime context; a request would meet it
// every time a browser navigated away. context.WithoutCancel severs the
// request's cancellation from the delivery without losing its values.
//
// And the budget is checked between passes, never inside one, for the same
// reason: a pass either runs whole or does not start. The honest residual is
// therefore one pass beyond the budget — at most Config.OutboxBatchSize events,
// each capped by runHandler at Config.HandlerTimeout — and a store that wants a
// tighter ceiling lowers one of those two rather than cutting a claimed batch
// in half.
func (a *App) DrainOutboxFor(ctx context.Context, budget time.Duration) (delivered int, capped bool, err error) {
	work := context.WithoutCancel(ctx)
	deadline := time.Now().Add(budget)
	for {
		n, err := a.outbox.deliverBatch(work)
		if err != nil {
			return delivered, false, err
		}
		delivered += n
		if n == 0 {
			return delivered, false, nil
		}
		if !time.Now().Before(deadline) {
			return delivered, true, nil
		}
	}
}

// PendingEvents reports how many events are waiting to be delivered, and how
// many were parked as undeliverable.
func (a *App) PendingEvents(ctx context.Context) (pending, dead int, err error) {
	err = a.db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE published_at IS NULL AND NOT dead),
		       count(*) FILTER (WHERE dead)
		FROM outbox_events`).Scan(&pending, &dead)
	return pending, dead, err
}

// ------------------------------------------------------ the operator's view
//
// Everything below serves one screen and one question: what did not get
// delivered, why, and can it be made to go now. Plain App methods rather than
// an `app.Events()` service, because the two readers this sits beside —
// DrainOutbox and PendingEvents — are already plain App methods in this file
// with the same audience, and `app.Events()` would read as the event bus,
// which is a different thing one file over.

// Event states an operator filters by. Derived from the row rather than
// stored: `dead` and `published_at` are the facts, and one word is what a
// query string and a screen can both say.
const (
	EventStatePending   = "pending"
	EventStateDead      = "dead"
	EventStatePublished = "published"
)

// requeueDeadMax bounds one bulk requeue. A click that quietly restarts 40,000
// deliveries is not a repair.
const requeueDeadMax = 500

// requeueTimeout bounds a requeue transaction. InTx sets no statement timeout
// and net/http's WriteTimeout does not cancel a request context, so without
// this a lock wait parks a pooled connection indefinitely.
const requeueTimeout = 10 * time.Second

// The three states an operator sees, derived in one place so the CASE, the
// filters and the dead-letter index cannot drift apart.
//
// published wins over dead because a row can carry both flags and the delivery
// is the fact that matters: markPublished does not clear `dead`, fail does not
// look at `published_at`, and a batch of OutboxBatchSize rows dispatched at up
// to HandlerTimeout each outlives the sixty-second visibility window — so a
// second worker can re-claim a row the first is still delivering, and the two
// finish in either order.
const outboxStateCase = `CASE WHEN published_at IS NOT NULL THEN 'published'
	                          WHEN dead THEN 'dead' ELSE 'pending' END`

// Mirrors of the CASE, so the three filters partition the table. `dead` on its
// own is the predicate that reads naturally and is wrong: it returns rows whose
// own state field says published.
const (
	outboxWherePublished = `published_at IS NOT NULL`
	outboxWhereDead      = `dead AND published_at IS NULL`
	outboxWherePending   = `published_at IS NULL AND NOT dead`
)

const outboxRowColumns = `id, event_id, event_name, event_version, aggregate_type, aggregate_id, ` +
	outboxStateCase + `, attempts, coalesce(last_error, ''), created_at, available_at, published_at`

// OutboxEvent is one row of the outbox as an operator reads it: the delivery
// facts around an event, not the event as a handler received it — hence
// `payload` rather than `data`.
type OutboxEvent struct {
	// Seq is the bigserial. It orders the table and nothing else; the public
	// identity of an event is its uuid, which is what a handler dedupes on and
	// what every log line prints.
	Seq           int64           `json:"seq"`
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Version       int             `json:"v"`
	AggregateType string          `json:"aggregate_type"`
	AggregateID   int64           `json:"aggregate_id"`
	State         string          `json:"state"`
	Attempts      int             `json:"attempts"`
	LastError     string          `json:"last_error,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	AvailableAt   time.Time       `json:"available_at"`
	PublishedAt   *time.Time      `json:"published_at,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"` // detail only
}

// EventQuery filters the operator's listing.
type EventQuery struct {
	Name          string
	AggregateType string
	// AggregateID is 0 for "any". The handler refuses it without an
	// AggregateType: outbox_aggregate_idx is on the pair, so the second column
	// alone cannot use it and would be a sequential scan of every event ever
	// delivered.
	AggregateID   int64
	State         string // "" = any
	Limit, Offset int
}

func scanOutboxEvent(row scanner, withPayload bool) (*OutboxEvent, error) {
	e := &OutboxEvent{}
	var published sql.NullTime
	var payload []byte
	dest := []any{&e.Seq, &e.ID, &e.Name, &e.Version, &e.AggregateType, &e.AggregateID,
		&e.State, &e.Attempts, &e.LastError, &e.CreatedAt, &e.AvailableAt, &published}
	if withPayload {
		dest = append(dest, &payload)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	if published.Valid {
		t := published.Time
		e.PublishedAt = &t
	}
	if len(payload) > 0 {
		e.Payload = json.RawMessage(payload)
	}
	return e, nil
}

// isLockNotAvailable reports a 55P03 — FOR UPDATE NOWAIT found the row held by
// somebody else. Same shape as isUniqueViolation: the driver is database/sql,
// so the SQLSTATE arrives inside the message.
func isLockNotAvailable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "55P03")
}

// ListEvents returns a page of the outbox, newest first.
//
// Rows carry no payload: a page of order payloads is megabytes, and the list is
// for finding the row rather than reading it. `last_error` is included, because
// that is what an operator scans the list for.
func (a *App) ListEvents(ctx context.Context, q EventQuery) ([]*OutboxEvent, int, error) {
	where, args := []string{"true"}, []any{}
	if name := strings.TrimSpace(q.Name); name != "" {
		args = append(args, name)
		where = append(where, fmt.Sprintf("event_name = $%d", len(args)))
	}
	if t := strings.TrimSpace(q.AggregateType); t != "" {
		args = append(args, t)
		where = append(where, fmt.Sprintf("aggregate_type = $%d", len(args)))
		if q.AggregateID > 0 {
			args = append(args, q.AggregateID)
			where = append(where, fmt.Sprintf("aggregate_id = $%d", len(args)))
		}
	}
	switch q.State {
	case EventStatePublished:
		where = append(where, outboxWherePublished)
	case EventStateDead:
		where = append(where, outboxWhereDead)
	case EventStatePending:
		where = append(where, outboxWherePending)
	}
	clause := strings.Join(where, " AND ")

	var total int
	if err := a.db.QueryRowContext(ctx,
		`SELECT count(*) FROM outbox_events WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit, offset := q.Limit, q.Offset
	if limit <= 0 {
		limit = DefaultLimit
	}
	args = append(args, limit, offset)
	rows, err := a.db.QueryContext(ctx,
		`SELECT `+outboxRowColumns+` FROM outbox_events WHERE `+clause+
			fmt.Sprintf(" ORDER BY id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	// Never a nil slice: envelope.Data is `any` with json:"data,omitempty", and
	// an interface holding a nil slice is not empty for omitempty — it would
	// serialise "data": null, and the screen's {#each} would throw where an
	// empty page should have shown its empty state.
	out := []*OutboxEvent{}
	for rows.Next() {
		e, err := scanOutboxEvent(rows, false)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// GetEvent reads one event by its uuid, payload included.
func (a *App) GetEvent(ctx context.Context, eventID string) (*OutboxEvent, error) {
	row := a.db.QueryRowContext(ctx,
		`SELECT `+outboxRowColumns+`, payload FROM outbox_events WHERE event_id = $1`, eventID)
	e, err := scanOutboxEvent(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("no event %s", eventID)
	}
	return e, err
}

// RequeueEvent is the repair: it makes one event deliverable again and asks the
// dispatcher to look now.
//
// It is two acts behind one verb, split by the row's state under the lock,
// because a dead row and a merely backed-off row need different things:
//
//   - dead: un-parked with a fresh twelve-attempt budget. deliverBatch's claim
//     filters NOT dead, so a parked row is invisible to the dispatcher forever
//     and DrainOutbox can never recover it — this is the only thing that can.
//   - pending: brought forward only. `attempts` is the whole dead-letter bound,
//     and resetting it here would let an operator — or one double-click — hold
//     a permanently broken event in retry forever, so it never becomes the
//     evidence outboxMaxAttempts exists to produce. Backoff reaches fifteen
//     minutes, so "wait for the poll" is not an answer either.
//   - published: refused. Repairing a delivery that never happened and
//     re-running one that did are different acts with different blast radii,
//     and this engine has no verb for the second.
//
// Neither act clears `last_error`. The diagnosis outlives the repair: fail
// overwrites it on the next failure and markPublished clears it on success, so
// nothing stale survives a delivery, and an operator who clicks before reading
// the drawer keeps the reason.
//
// Expediting a pending row is an accepted race. A future `available_at` means
// either "backed off" or "claimed by a worker under a sixty-second visibility
// window", and SQL cannot tell those apart — both carry a future timestamp and
// no other marker. Moving it to now therefore cancels an in-flight claim's
// window, and that event may be dispatched twice. Delivery is at-least-once and
// handlers are contractually idempotent, so this is the contract working rather
// than a new hazard; it is also why the state CASE gives published priority.
func (a *App) RequeueEvent(ctx context.Context, eventID string) (*OutboxEvent, error) {
	ctx, cancel := context.WithTimeout(ctx, requeueTimeout)
	defer cancel()

	var out *OutboxEvent
	err := InTx(ctx, a.db, func(tx *sql.Tx) error {
		var rowID int64
		var state string
		var published sql.NullTime
		// NOWAIT, not SKIP LOCKED and not a plain wait. SKIP LOCKED would
		// answer 200 having requeued nothing on the one row the operator named.
		// A plain FOR UPDATE has no upper bound on an HTTP path: InTx sets no
		// statement timeout and WriteTimeout does not cancel a request context,
		// so a waiter parks a pooled connection with nothing to show for it.
		// The realistic holder is another operator's RequeueDeadEvents, which
		// holds up to 500 row locks for the length of its UPDATE — not the
		// dispatcher, whose claim releases its locks when it finishes reading
		// the batch into memory, before any handler runs.
		err := tx.QueryRowContext(ctx, `
			SELECT id, `+outboxStateCase+`, published_at
			FROM outbox_events WHERE event_id = $1
			FOR UPDATE NOWAIT`, eventID).Scan(&rowID, &state, &published)
		switch {
		case isLockNotAvailable(err):
			return Conflictf("that event is being requeued right now; try again in a moment")
		case errors.Is(err, sql.ErrNoRows):
			return NotFoundf("no event %s", eventID)
		case err != nil:
			return err
		}

		switch state {
		case EventStatePublished:
			return Conflictf("event %s was delivered at %s; retrying it would be a replay, not a repair",
				eventID, published.Time.Format(time.RFC3339))
		case EventStateDead:
			if _, err := tx.ExecContext(ctx, `
				UPDATE outbox_events
				SET dead = false, attempts = 0, available_at = now()
				WHERE id = $1`, rowID); err != nil {
				return err
			}
		default:
			if _, err := tx.ExecContext(ctx,
				`UPDATE outbox_events SET available_at = now() WHERE id = $1`, rowID); err != nil {
				return err
			}
		}

		row := tx.QueryRowContext(ctx,
			`SELECT `+outboxRowColumns+`, payload FROM outbox_events WHERE id = $1`, rowID)
		out, err = scanOutboxEvent(row, true)
		return err
	})
	if err != nil {
		return nil, err
	}
	// After the commit, never inside it: a dispatcher woken while the row is
	// still invisible to it looks, finds nothing, and goes back to sleep.
	a.nudgeOutbox()
	return out, nil
}

// RequeueDeadEvents un-parks a bounded batch of dead letters, optionally
// narrowed to one event name, and reports what is left.
//
// `remaining` is counted inside the same transaction as the UPDATE so the pair
// cannot disagree: the 500-row cap means an operator has to be told what is
// still parked — "Retried 500 of 3,214" is the message the cap requires — and a
// second request would be racy, because the dispatcher is already delivering
// the rows this call just freed.
//
// SKIP LOCKED here where the single-row path takes NOWAIT: two operators
// clearing a backlog should take disjoint sets rather than one waiting on the
// other, and one operator retrying a single row should not cost the bulk pass
// that row's neighbours. Neither can stall delivery — the dispatcher's own
// claim skips whatever either holds.
func (a *App) RequeueDeadEvents(ctx context.Context, name string, limit int) (requeued, remaining int, err error) {
	if limit <= 0 || limit > requeueDeadMax {
		limit = requeueDeadMax
	}
	ctx, cancel := context.WithTimeout(ctx, requeueTimeout)
	defer cancel()

	err = InTx(ctx, a.db, func(tx *sql.Tx) error {
		// Reset per attempt: InTx does not retry, but a caller reading these
		// named results after an error must not see a half-filled count.
		requeued, remaining = 0, 0
		where := outboxWhereDead
		args := []any{limit}
		if name = strings.TrimSpace(name); name != "" {
			args = append(args, name)
			where += fmt.Sprintf(" AND event_name = $%d", len(args))
		}
		// The dispatcher's own claim, line for line, so one queue pattern has
		// one spelling in this file.
		rows, err := tx.QueryContext(ctx, `
			UPDATE outbox_events o
			SET dead = false, attempts = 0, available_at = now()
			FROM (
				SELECT id FROM outbox_events
				WHERE `+where+`
				ORDER BY id
				FOR UPDATE SKIP LOCKED
				LIMIT $1
			) AS claimed
			WHERE o.id = claimed.id
			RETURNING o.event_id`, args...)
		if err != nil {
			return err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		requeued = len(ids)
		if requeued > 0 {
			// Which rows moved, which a count cannot answer when somebody asks
			// a week later what was redelivered. The route logs who asked.
			a.log.Info("outbox dead letters un-parked", "count", requeued, "event_ids", ids)
		}
		// Index-only against outbox_dead_idx, whose predicate is this same
		// constant.
		return tx.QueryRowContext(ctx,
			`SELECT count(*) FROM outbox_events WHERE `+outboxWhereDead).Scan(&remaining)
	})
	if err != nil {
		return 0, 0, err
	}
	if requeued > 0 {
		a.nudgeOutbox()
	}
	return requeued, remaining, nil
}
