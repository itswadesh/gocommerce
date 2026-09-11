package gocommerce

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The operator's view of the outbox. What these prove, over and above the
// dispatcher's own tests, is that a delivery that failed permanently can be
// read, understood and repaired without psql — and that repairing it cannot
// break the two properties the outbox is built on: the twelve-attempt bound,
// and a dispatcher that no operator can stall.

// ---------------------------------------------------------------- fixtures

// brokenModule is a consumer that is down until somebody fixes it, which is
// the situation the whole screen exists for. It subscribes to everything,
// because a dead letter has to be reachable whatever the event was.
type brokenModule struct {
	mu      sync.Mutex
	healthy bool
	seen    int64
}

func (m *brokenModule) Name() string            { return "broken" }
func (m *brokenModule) Migrations() []Migration { return nil }

func (m *brokenModule) Register(app *App) error {
	app.Subscribe("*", func(ctx context.Context, e Event) error {
		atomic.AddInt64(&m.seen, 1)
		m.mu.Lock()
		healthy := m.healthy
		m.mu.Unlock()
		if healthy {
			return nil
		}
		return fmt.Errorf("consumer is down (%s)", e.Name)
	})
	return nil
}

func (m *brokenModule) heal() {
	m.mu.Lock()
	m.healthy = true
	m.mu.Unlock()
}

// drainPast delivers once and then drags every unpublished row back to now.
//
// Backoff is exponential and reaches fifteen minutes, so a test that waited for
// it would take fifteen minutes; this is the same trick the dispatcher's own
// retry test uses. Twelve passes is what outboxMaxAttempts costs.
func drainPast(t *testing.T, app *App, passes int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < passes; i++ {
		if _, err := app.DrainOutbox(ctx); err != nil {
			t.Fatalf("drain: %v", err)
		}
		if _, err := app.DB().ExecContext(ctx,
			`UPDATE outbox_events SET available_at = now() WHERE published_at IS NULL AND NOT dead`); err != nil {
			t.Fatalf("reset backoff: %v", err)
		}
	}
}

// parkDeadLetters runs delivery until every failing event has spent its budget.
// The dead state is reached through fail() rather than fabricated with SQL,
// which is what makes these tests say anything about the real dispatcher.
func parkDeadLetters(t *testing.T, app *App) {
	t.Helper()
	drainPast(t, app, outboxMaxAttempts)
	_, dead, err := app.PendingEvents(context.Background())
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if dead == 0 {
		t.Fatal("no event was parked: the fixture is not reaching fail()")
	}
}

func listEvents(t *testing.T, app *App, queryString string) (rows []*OutboxEvent, meta ListMeta, raw string) {
	t.Helper()
	rec := do(t, app, "GET", "/api/admin/events"+queryString, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/events%s = %d: %s", queryString, rec.Code, rec.Body)
	}
	var out struct {
		Data []*OutboxEvent `json:"data"`
		Meta ListMeta       `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	return out.Data, out.Meta, rec.Body.String()
}

func getEvent(t *testing.T, app *App, id string) *OutboxEvent {
	t.Helper()
	rec := do(t, app, "GET", "/api/admin/events/"+id, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET event %s = %d: %s", id, rec.Code, rec.Body)
	}
	var out struct {
		Data *OutboxEvent `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	return out.Data
}

func retryEvent(t *testing.T, app *App, id string) (*OutboxEvent, int, string) {
	t.Helper()
	rec := do(t, app, "POST", "/api/admin/events/"+id+"/retry", withAdmin)
	var out struct {
		Data *OutboxEvent `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out.Data, rec.Code, rec.Body.String()
}

// ------------------------------------------------------------------- reading

// The three states have to partition the table, and each count has to be the
// count of its own filter rather than of the table.
func TestEventListFiltersByState(t *testing.T) {
	broken := &brokenModule{}
	app := newTestApp(t, broken)

	// One delivered event, written while the consumer was healthy.
	broken.heal()
	placeOrder(t, app, "EVT-OK")
	if _, err := app.DrainOutbox(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}

	// One that fails, and stays pending after a single attempt.
	broken.mu.Lock()
	broken.healthy = false
	broken.mu.Unlock()
	placeOrder(t, app, "EVT-BAD")
	if _, err := app.DrainOutbox(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}

	published, meta, _ := listEvents(t, app, "?state=published")
	if len(published) == 0 || meta.Total != len(published) {
		t.Fatalf("published = %d rows, meta.total %d; want at least one and agreement", len(published), meta.Total)
	}
	for _, e := range published {
		if e.State != EventStatePublished || e.PublishedAt == nil {
			t.Errorf("row %s in the published filter reads state %q", e.ID, e.State)
		}
	}

	pending, pendingMeta, _ := listEvents(t, app, "?state=pending")
	if len(pending) != 1 || pendingMeta.Total != 1 {
		t.Fatalf("pending = %d rows (meta %d), want exactly the failing one", len(pending), pendingMeta.Total)
	}
	if pending[0].Attempts < 1 {
		t.Errorf("attempts = %d, want at least 1 — the claim increments it", pending[0].Attempts)
	}
	if pending[0].LastError == "" {
		t.Error("a failed delivery left no last_error, which is what an operator opens this screen for")
	}

	dead, deadMeta, body := listEvents(t, app, "?state=dead")
	if len(dead) != 0 || deadMeta.Total != 0 {
		t.Errorf("dead = %d rows (meta %d), want none — one failure is not twelve", len(dead), deadMeta.Total)
	}
	// envelope.Data is `any` with json:"data,omitempty", and an interface
	// holding a nil slice is not empty for omitempty: a nil would serialise as
	// "data": null and the screen's {#each} would throw on the empty state.
	if !strings.Contains(body, `"data":[]`) {
		t.Errorf("an empty page must serialise \"data\":[], got %s", body)
	}
}

// The bug the bare `dead` predicate had. A row can carry both flags, and the
// filter that finds it must agree with the state it reports.
func TestEventStatesStillPartitionARowThatIsBothPublishedAndDead(t *testing.T) {
	broken := &brokenModule{}
	app := newTestApp(t, broken)
	ctx := context.Background()

	placeOrder(t, app, "EVT-RACE")
	parkDeadLetters(t, app)

	rows, _, _ := listEvents(t, app, "?state=dead")
	if len(rows) != 1 {
		t.Fatalf("dead = %d rows, want 1", len(rows))
	}
	id := rows[0].ID

	// Fabricated, deliberately, and the only place in this file that is. The
	// race that produces both flags needs a batch to outlive its sixty-second
	// visibility window, which a test cannot force without sleeping a minute:
	// markPublished does not clear `dead` and fail does not look at
	// published_at, so the combination is reachable in production whenever a
	// second worker re-claims a row the first is still delivering.
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE outbox_events SET published_at = now() WHERE event_id = $1`, id); err != nil {
		t.Fatalf("fabricate the race: %v", err)
	}

	seen := map[string]int{}
	for _, state := range []string{EventStatePending, EventStateDead, EventStatePublished} {
		found, meta, _ := listEvents(t, app, "?state="+state)
		if meta.Total != len(found) {
			t.Errorf("state=%s: meta.total %d but %d rows", state, meta.Total, len(found))
		}
		for _, e := range found {
			if e.ID != id {
				continue
			}
			seen[state]++
			if e.State != state {
				t.Errorf("the %s filter returned a row whose own state reads %q", state, e.State)
			}
		}
	}
	if seen[EventStatePublished] != 1 || seen[EventStateDead] != 0 || seen[EventStatePending] != 0 {
		t.Errorf("the raced row appeared as %v; want exactly once, under published", seen)
	}
}

func TestEventListOmitsPayloadAndDetailCarriesIt(t *testing.T) {
	broken := &brokenModule{}
	app := newTestApp(t, broken)

	order := placeOrder(t, app, "EVT-PAYLOAD")
	if _, err := app.DrainOutbox(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}

	rows, _, body := listEvents(t, app, "")
	if len(rows) == 0 {
		t.Fatal("the listing is empty")
	}
	// A page of order payloads is megabytes, and the list is for finding the
	// row rather than reading it.
	if strings.Contains(body, `"payload"`) {
		t.Errorf("a list row carries its payload:\n%s", body)
	}
	if !strings.Contains(body, `"last_error"`) || !strings.Contains(body, `"attempts"`) {
		t.Errorf("the list dropped what an operator scans it for:\n%s", body)
	}

	detail := getEvent(t, app, rows[0].ID)
	if len(detail.Payload) == 0 {
		t.Fatal("the detail route returned no payload")
	}
	var payload OrderEvent
	if err := json.Unmarshal(detail.Payload, &payload); err != nil {
		t.Fatalf("the payload is not an OrderEvent: %v", err)
	}
	if payload.OrderID != order.ID {
		t.Errorf("payload order id = %d, want %d", payload.OrderID, order.ID)
	}
	if detail.Seq == 0 {
		t.Error("seq is missing: the bigserial is the ordering key and is reported as seq")
	}
}

func TestUnknownAndMalformedEventIDs(t *testing.T) {
	app := newTestApp(t)

	// A well-formed uuid nobody wrote.
	rec := do(t, app, "GET", "/api/admin/events/6f1a9f1e-0000-4000-8000-000000000000", withAdmin)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown uuid = %d, want 404: %s", rec.Code, rec.Body)
	}

	// Not a uuid at all. PostgreSQL would raise a cast error here, which
	// RespondError renders as a 500 for what is plainly a bad request.
	rec = do(t, app, "GET", "/api/admin/events/nonsense", withAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed id = %d, want 400: %s", rec.Code, rec.Body)
	}
	rec = do(t, app, "POST", "/api/admin/events/nonsense/retry", withAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed id on retry = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestEventListRefusesAnAggregateIDWithoutItsType(t *testing.T) {
	broken := &brokenModule{}
	app := newTestApp(t, broken)
	broken.heal()

	first := placeOrder(t, app, "EVT-AGG-1")
	placeOrder(t, app, "EVT-AGG-2")
	if _, err := app.DrainOutbox(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}

	for _, raw := range []string{"?aggregate_id=1", "?aggregate_id=0", "?aggregate_id=-1", "?aggregate_id=abc"} {
		rec := do(t, app, "GET", "/api/admin/events"+raw, withAdmin)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400: %s", raw, rec.Code, rec.Body)
			continue
		}
		body := rec.Body.String()
		if raw == "?aggregate_id=1" {
			if !strings.Contains(body, "aggregate_type") {
				t.Errorf("the refusal does not name aggregate_type: %s", body)
			}
		} else if !strings.Contains(body, "positive integer") {
			t.Errorf("the refusal does not name the constraint: %s", body)
		}
	}

	rows, meta, _ := listEvents(t, app,
		fmt.Sprintf("?aggregate_type=order&aggregate_id=%d", first.ID))
	if len(rows) == 0 || meta.Total != len(rows) {
		t.Fatalf("the pair filter returned %d rows (meta %d)", len(rows), meta.Total)
	}
	for _, e := range rows {
		if e.AggregateID != first.ID || e.AggregateType != AggregateOrder {
			t.Errorf("the pair filter returned %s/%d", e.AggregateType, e.AggregateID)
		}
	}
}

// ----------------------------------------------------------------- repairing

// The whole feature in one test: DrainOutbox alone could never have recovered
// this row, because the claim filters NOT dead.
func TestRetryUnparksADeadEventAndItDelivers(t *testing.T) {
	broken := &brokenModule{}
	app := newTestApp(t, broken)
	ctx := context.Background()

	placeOrder(t, app, "EVT-DEAD")
	parkDeadLetters(t, app)

	dead, _, _ := listEvents(t, app, "?state=dead")
	if len(dead) != 1 {
		t.Fatalf("dead = %d rows, want 1", len(dead))
	}
	if dead[0].Attempts != outboxMaxAttempts {
		t.Errorf("attempts = %d, want %d", dead[0].Attempts, outboxMaxAttempts)
	}
	if dead[0].LastError == "" {
		t.Error("a dead letter with no last_error is evidence of nothing")
	}

	// A drain cannot see it at all: that is the property the retry route exists
	// to work around, not a bug in the fixture.
	if n, err := app.DrainOutbox(ctx); err != nil || n != 0 {
		t.Fatalf("drain over a dead letter = %d, %v; want 0, nil", n, err)
	}

	broken.heal()
	repaired, code, body := retryEvent(t, app, dead[0].ID)
	if code != http.StatusOK {
		t.Fatalf("retry = %d: %s", code, body)
	}
	if repaired.State != EventStatePending {
		t.Errorf("state after the un-park = %q, want pending", repaired.State)
	}
	if repaired.Attempts != 0 {
		t.Errorf("attempts after the un-park = %d, want 0 — a fixed cause earns a fresh budget",
			repaired.Attempts)
	}

	if _, err := app.DrainOutbox(ctx); err != nil {
		t.Fatalf("drain after the repair: %v", err)
	}
	pending, stillDead, err := app.PendingEvents(ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if pending != 0 || stillDead != 0 {
		t.Errorf("after the repair: %d pending, %d dead; want 0, 0", pending, stillDead)
	}
}

// Without this, a double-click on the drawer holds a permanently broken event
// in retry forever and it never becomes the evidence the policy produces.
func TestRetryOnAPendingEventKeepsItsAttemptBudget(t *testing.T) {
	broken := &brokenModule{}
	app := newTestApp(t, broken)
	ctx := context.Background()

	placeOrder(t, app, "EVT-PENDING")
	drainPast(t, app, 3)

	rows, _, _ := listEvents(t, app, "?state=pending")
	if len(rows) != 1 || rows[0].Attempts != 3 {
		t.Fatalf("want one pending row at 3 attempts, got %d rows at %d", len(rows), rows[0].Attempts)
	}
	id := rows[0].ID

	// Push the next attempt out of reach, the way real backoff does.
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE outbox_events SET available_at = now() + interval '15 minutes' WHERE event_id = $1`,
		id); err != nil {
		t.Fatalf("set backoff: %v", err)
	}

	before := time.Now()
	expedited, code, body := retryEvent(t, app, id)
	if code != http.StatusOK {
		t.Fatalf("retry = %d: %s", code, body)
	}
	if expedited.State != EventStatePending {
		t.Errorf("state = %q, want pending", expedited.State)
	}
	if expedited.Attempts != 3 {
		t.Errorf("attempts = %d, want 3 — expediting must not refill the budget", expedited.Attempts)
	}
	if expedited.AvailableAt.After(before.Add(time.Minute)) {
		t.Errorf("available_at = %s, want brought forward to now", expedited.AvailableAt)
	}

	// Nine more failures is twelve in total, so the bound survived the
	// operator's impatience.
	drainPast(t, app, outboxMaxAttempts-3)
	_, dead, err := app.PendingEvents(ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if dead != 1 {
		t.Errorf("dead = %d, want 1: the twelve-attempt bound did not survive the retry", dead)
	}
}

// The diagnosis outlives the repair, on both paths.
func TestRetryPreservesLastError(t *testing.T) {
	broken := &brokenModule{}
	app := newTestApp(t, broken)

	placeOrder(t, app, "EVT-ERR-1")
	placeOrder(t, app, "EVT-ERR-2")
	parkDeadLetters(t, app)

	dead, _, _ := listEvents(t, app, "?state=dead")
	if len(dead) != 2 {
		t.Fatalf("dead = %d rows, want 2", len(dead))
	}
	single, bulk := dead[0], dead[1]
	if single.LastError == "" || bulk.LastError == "" {
		t.Fatal("a dead letter arrived with no last_error")
	}

	repaired, code, body := retryEvent(t, app, single.ID)
	if code != http.StatusOK {
		t.Fatalf("retry = %d: %s", code, body)
	}
	if repaired.LastError != single.LastError {
		t.Errorf("the single retry rewrote last_error: %q -> %q", single.LastError, repaired.LastError)
	}

	rec := do(t, app, "POST", "/api/admin/events/retry-dead", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry-dead = %d: %s", rec.Code, rec.Body)
	}
	after := getEvent(t, app, bulk.ID)
	if after.LastError != bulk.LastError {
		t.Errorf("the bulk requeue rewrote last_error: %q -> %q", bulk.LastError, after.LastError)
	}
}

func TestRetryRefusesAnAlreadyDeliveredEvent(t *testing.T) {
	broken := &brokenModule{}
	app := newTestApp(t, broken)
	broken.heal()

	placeOrder(t, app, "EVT-DONE")
	if _, err := app.DrainOutbox(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	rows, _, _ := listEvents(t, app, "?state=published")
	if len(rows) == 0 {
		t.Fatal("nothing was published")
	}
	delivered := rows[0]

	_, code, body := retryEvent(t, app, delivered.ID)
	if code != http.StatusConflict {
		t.Fatalf("retry on a delivered event = %d, want 409: %s", code, body)
	}
	if !strings.Contains(body, "replay") {
		t.Errorf("the refusal does not say why: %s", body)
	}

	again := getEvent(t, app, delivered.ID)
	if again.PublishedAt == nil || !again.PublishedAt.Equal(*delivered.PublishedAt) {
		t.Error("the refused retry moved published_at")
	}
}

// The NOWAIT decision. An unbounded lock wait on an HTTP path has no upper
// bound at all: InTx sets no statement timeout and net/http's WriteTimeout does
// not cancel a request context, so a waiter parks a pooled connection with
// nothing to show the operator.
func TestRetryIsRefusedNotBlockedWhenTheRowIsHeld(t *testing.T) {
	broken := &brokenModule{}
	app := newTestApp(t, broken)
	ctx := context.Background()

	placeOrder(t, app, "EVT-HELD")
	parkDeadLetters(t, app)
	rows, _, _ := listEvents(t, app, "?state=dead")
	if len(rows) != 1 {
		t.Fatalf("dead = %d rows, want 1", len(rows))
	}
	id := rows[0].ID

	tx, err := app.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	var held int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM outbox_events WHERE event_id = $1 FOR UPDATE`, id).Scan(&held); err != nil {
		t.Fatalf("hold the row: %v", err)
	}

	done := make(chan *struct {
		code int
		body string
	}, 1)
	go func() {
		_, code, body := retryEvent(t, app, id)
		done <- &struct {
			code int
			body string
		}{code, body}
	}()

	select {
	case got := <-done:
		if got.code != http.StatusConflict {
			t.Errorf("retry over a held row = %d, want 409: %s", got.code, got.body)
		}
		if !strings.Contains(got.body, "being requeued right now") {
			t.Errorf("the refusal does not explain itself: %s", got.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the retry blocked on the row lock instead of refusing")
	}
}

// The other half of the locking story: an operator holding a row can never
// stall delivery, because the dispatcher's claim skips locked rows.
func TestRetryNeverStallsTheDispatcher(t *testing.T) {
	broken := &brokenModule{}
	app := newTestApp(t, broken)
	broken.heal()
	ctx := context.Background()

	placeOrder(t, app, "EVT-STALL")
	rows, _, _ := listEvents(t, app, "?state=pending")
	if len(rows) != 1 {
		t.Fatalf("pending = %d rows, want 1", len(rows))
	}

	tx, err := app.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	var held int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM outbox_events WHERE event_id = $1 FOR UPDATE`, rows[0].ID).Scan(&held); err != nil {
		_ = tx.Rollback()
		t.Fatalf("hold the row: %v", err)
	}

	drained := make(chan int, 1)
	go func() {
		n, err := app.DrainOutbox(ctx)
		if err != nil {
			t.Errorf("drain: %v", err)
		}
		drained <- n
	}()
	select {
	case n := <-drained:
		if n != 0 {
			t.Errorf("drain claimed %d rows while one was held, want 0 (SKIP LOCKED)", n)
		}
	case <-time.After(5 * time.Second):
		_ = tx.Rollback()
		t.Fatal("the dispatcher blocked on a row an operator held")
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if n, err := app.DrainOutbox(ctx); err != nil || n != 1 {
		t.Fatalf("drain after the lock went = %d, %v; want 1, nil", n, err)
	}
}

func TestRequeueDeadTakesOnlyTheNamedEvents(t *testing.T) {
	broken := &brokenModule{}
	app := newTestApp(t, broken)
	ctx := context.Background()

	order := placeOrder(t, app, "EVT-NAMED")
	if _, err := app.Pay().MarkPaid(ctx, order.ID, "ref-1"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	parkDeadLetters(t, app)

	dead, _, _ := listEvents(t, app, "?state=dead")
	if len(dead) != 2 {
		t.Fatalf("dead = %d rows, want 2 (order.created and order.paid)", len(dead))
	}

	rec := do(t, app, "POST", "/api/admin/events/retry-dead?name="+EventOrderPaid, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry-dead = %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Data struct {
			Requeued  int `json:"requeued"`
			Remaining int `json:"remaining"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Data.Requeued != 1 || out.Data.Remaining != 1 {
		t.Errorf("requeued %d, remaining %d; want 1 and 1", out.Data.Requeued, out.Data.Remaining)
	}

	for _, e := range dead {
		after := getEvent(t, app, e.ID)
		want := EventStateDead
		if e.Name == EventOrderPaid {
			want = EventStatePending
		}
		if after.State != want {
			t.Errorf("%s is %q after a name-filtered requeue, want %q", e.Name, after.State, want)
		}
	}
}

// One click cannot restart an unbounded backlog, and the response can actually
// render "Retried 500 of 3,214" — which a bare `requeued` could not.
func TestRequeueDeadIsCappedAndReportsWhatRemains(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	// Driven directly: parking six hundred rows through a failing handler would
	// be 7,200 delivery attempts, and what is under test here is the cap and
	// the count rather than the road to dead.
	const parked = requeueDeadMax + 105
	if _, err := app.DB().ExecContext(ctx, `
		INSERT INTO outbox_events (event_id, event_name, event_version, aggregate_type,
		                           aggregate_id, payload, attempts, last_error, dead)
		SELECT md5(i::text || clock_timestamp()::text)::uuid, 'order.paid', 1, 'order',
		       i, '{}'::jsonb, $2, 'consumer is down', true
		FROM generate_series(1, $1) AS i`, parked, outboxMaxAttempts); err != nil {
		t.Fatalf("park a backlog: %v", err)
	}

	requeued, remaining, err := app.RequeueDeadEvents(ctx, "", 10_000)
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if requeued != requeueDeadMax {
		t.Errorf("requeued = %d, want the cap %d", requeued, requeueDeadMax)
	}
	if remaining != parked-requeueDeadMax {
		t.Errorf("remaining = %d, want %d", remaining, parked-requeueDeadMax)
	}

	// And `remaining` is the truth about the table, not an arithmetic guess.
	var stillDead int
	if err := app.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM outbox_events WHERE dead AND published_at IS NULL`).Scan(&stillDead); err != nil {
		t.Fatalf("count: %v", err)
	}
	if stillDead != remaining {
		t.Errorf("remaining said %d, the table says %d", remaining, stillDead)
	}
}

// ------------------------------------------------------------------ the gate

// Duplicates the rows in rights_test.go deliberately: that table proves the
// right exists, this proves all four routes carry it.
func TestEventRoutesRequireStoreOperate(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	signIn := func(email, role string) string {
		t.Helper()
		if _, err := app.Superusers().Create(ctx, email, "a-long-enough-password", role); err != nil {
			t.Fatalf("create %s: %v", role, err)
		}
		_, session, err := app.Superusers().Authenticate(ctx, email, "a-long-enough-password", "127.0.0.1")
		if err != nil {
			t.Fatalf("sign in %s: %v", role, err)
		}
		return session.Token
	}
	as := func(token string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
	}

	staff := signIn("staff@example.com", RoleStaff)
	manager := signIn("manager@example.com", RoleManager)
	owner := signIn("owner@example.com", RoleOwner)

	routes := []struct {
		method, path string
	}{
		{"GET", "/api/admin/events"},
		{"GET", "/api/admin/events/6f1a9f1e-0000-4000-8000-000000000000"},
		{"POST", "/api/admin/events/6f1a9f1e-0000-4000-8000-000000000000/retry"},
		{"POST", "/api/admin/events/retry-dead"},
	}
	for _, r := range routes {
		for _, who := range []struct {
			name, token string
		}{{"manager", manager}, {"staff", staff}} {
			rec := do(t, app, r.method, r.path, as(who.token))
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s as %s = %d, want 403: %s", r.method, r.path, who.name, rec.Code, rec.Body)
				continue
			}
			if !strings.Contains(rec.Body.String(), string(RightStoreOperate)) {
				t.Errorf("the refusal does not name store.operate: %s", rec.Body)
			}
		}
		// An owner and a static token get past the gate. A 404 here is the
		// handler answering about a uuid nobody wrote, which is the point.
		for _, token := range []string{owner, testAdminToken} {
			rec := do(t, app, r.method, r.path, as(token))
			if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
				t.Errorf("%s %s was refused for a caller who carries the right: %d %s",
					r.method, r.path, rec.Code, rec.Body)
			}
		}
	}
}
