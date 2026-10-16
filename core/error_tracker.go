package gocommerce

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// The error tracker (D86): Sentry's idea, kept inside the store.
//
// A 500 tells the client "internal error" and nothing else, which is right —
// its cause names tables, hosts and code — and until now the cause went only
// to the process log, which nobody running a shop reads. The tracker keeps it
// where the people responsible for the store can: every 5xx and every panic,
// and every crash the panel reports from somebody's browser, grouped into one
// issue per failure (a fingerprint of its kind, its message with the numbers
// taken out, and where it happened), counted, with its latest occurrences.
//
// Reading it is its own right, errors.read, because it carries exactly what
// store.operate deliberately does not: the database's and the code's own
// words. Owners hold it; a store gives it to whom it chooses.
//
// Errors outside any store — a request that named none — stay in the log.

// errorEventsKept is how many occurrences an issue keeps; its count keeps
// every one.
const errorEventsKept = 20

// panelIssuesPerDay caps how many new issues the panel's reports may open in
// a store in a day.
const panelIssuesPerDay = 500

const migration0063ErrorTracker = `
CREATE TABLE error_issues (
    id          bigserial   PRIMARY KEY,
    fingerprint text        NOT NULL,
    source      text        NOT NULL CHECK (source IN ('server', 'panel')),
    title       text        NOT NULL,
    culprit     text        NOT NULL DEFAULT '',
    status      text        NOT NULL DEFAULT 'unresolved'
                            CHECK (status IN ('unresolved', 'resolved', 'ignored')),
    count       bigint      NOT NULL DEFAULT 1,
    first_seen  timestamptz NOT NULL DEFAULT now(),
    last_seen   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (fingerprint)
);
CREATE INDEX error_issues_seen ON error_issues (status, last_seen DESC);
CREATE TABLE error_events (
    id         bigserial   PRIMARY KEY,
    issue_id   bigint      NOT NULL REFERENCES error_issues (id) ON DELETE CASCADE,
    message    text        NOT NULL,
    stack      text        NOT NULL DEFAULT '',
    context    jsonb       NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX error_events_issue ON error_events (issue_id, id DESC);
`

// ErrorTracker records and reads a store's errors.
type ErrorTracker struct{ app *App }

// Errors returns the error tracker.
func (a *App) Errors() *ErrorTracker { return &ErrorTracker{app: a} }

// ErrorIssue is one failure, however often it has happened.
type ErrorIssue struct {
	ID          int64     `json:"id"`
	Fingerprint string    `json:"fingerprint"`
	Source      string    `json:"source"`
	Title       string    `json:"title"`
	Culprit     string    `json:"culprit"`
	Status      string    `json:"status"`
	Count       int64     `json:"count"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	// LastEvent and Events are read with one issue, not with the list.
	LastEvent *ErrorEvent   `json:"last_event,omitempty"`
	Events    []*ErrorEvent `json:"events,omitempty"`
}

// ErrorEvent is one occurrence.
type ErrorEvent struct {
	ID        int64          `json:"id"`
	Message   string         `json:"message"`
	Stack     string         `json:"stack"`
	Context   map[string]any `json:"context"`
	CreatedAt time.Time      `json:"created_at"`
}

// CapturedError is a failure to record.
type CapturedError struct {
	Source  string // "server" or "panel"
	Kind    string // "panic", "TypeError", …; empty for a plain error
	Message string
	Stack   string
	Culprit string // where it happened; worked out from the stack when empty
	Context map[string]any
}

// ErrorQuery filters the list.
type ErrorQuery struct {
	Status, Source, Search string
	Limit, Offset          int
}

var digitRuns = regexp.MustCompile(`[0-9]+`)

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// fingerprint groups occurrences: the same kind of failure, saying the same
// thing with its numbers taken out (an id, a count, a port), in the same place.
func fingerprint(c CapturedError) string {
	sum := sha1.Sum([]byte(c.Source + "\x00" + c.Kind + "\x00" + digitRuns.ReplaceAllString(clip(c.Message, 300), "0") + "\x00" + c.Culprit))
	return hex.EncodeToString(sum[:8])
}

// Capture records an occurrence for the store ctx is bound to, off the
// request: it is queued for one worker and the request goes on. Recording
// inline put a transaction and a row lock on every failing request, so one
// endpoint failing under load queued every request behind its own error and
// took the pool with it. A full queue drops the occurrence (and says so in the
// log) rather than slow anything down; a tracker that broke the requests it
// was recording would be the worse bug.
func (t *ErrorTracker) Capture(ctx context.Context, c CapturedError) {
	store, ok := StoreFrom(ctx)
	if !ok || store == installScope || t.app.errq == nil {
		return
	}
	t.app.errq.enqueue(context.WithoutCancel(ctx), c)
}

// errorQueue is the one worker that records occurrences.
type errorQueue struct {
	app   *App
	items chan queuedError
	done  chan struct{}
	once  sync.Once
}

type queuedError struct {
	ctx     context.Context
	err     CapturedError
	barrier chan struct{} // flush: closed when everything before it is recorded
}

const errorQueueSize = 256

func newErrorQueue(a *App) *errorQueue {
	q := &errorQueue{app: a, items: make(chan queuedError, errorQueueSize), done: make(chan struct{})}
	go q.run()
	return q
}

func (q *errorQueue) enqueue(ctx context.Context, c CapturedError) {
	defer func() { _ = recover() }() // a send on a queue shutdown has closed
	select {
	case q.items <- queuedError{ctx: ctx, err: c}:
	default:
		q.app.log.Warn("error tracker queue full; occurrence dropped", "message", clip(c.Message, 200))
	}
}

func (q *errorQueue) run() {
	defer close(q.done)
	for it := range q.items {
		if it.barrier != nil {
			close(it.barrier)
			continue
		}
		ctx, cancel := context.WithTimeout(it.ctx, 5*time.Second)
		if err := q.app.Errors().capture(ctx, it.err); err != nil {
			q.app.log.Error("error tracker could not record", "error", err, "message", clip(it.err.Message, 200))
		}
		cancel()
	}
}

// flush waits until everything queued before it is recorded.
func (q *errorQueue) flush() {
	b := make(chan struct{})
	q.items <- queuedError{barrier: b}
	<-b
}

// stop records what is queued and ends the worker, within ctx.
func (q *errorQueue) stop(ctx context.Context) {
	q.once.Do(func() { close(q.items) })
	select {
	case <-q.done:
	case <-ctx.Done():
	}
}

func (t *ErrorTracker) capture(ctx context.Context, c CapturedError) error {
	c.Message = clip(strings.TrimSpace(c.Message), 2000)
	c.Stack = clip(c.Stack, 32000)
	if c.Culprit == "" {
		if c.Source == "panel" {
			c.Culprit = jsCulprit(c.Stack)
		} else {
			c.Culprit = goCulprit(c.Stack)
		}
	}
	title, _, _ := strings.Cut(c.Message, "\n")
	if c.Kind != "" {
		title = c.Kind + ": " + title
	}
	ctxJSON, err := json.Marshal(c.Context)
	if err != nil || c.Context == nil {
		ctxJSON = []byte(`{}`)
	}
	fp := fingerprint(c)
	return InTx(ctx, t.app.db, func(tx *sql.Tx) error {
		// The panel's reports come from any signed-in browser, so a flood of
		// different messages could bury the real ones: past panelIssuesPerDay
		// new panel issues a day, only issues already open are counted.
		if c.Source == "panel" {
			var known bool
			var recent int
			if err := tx.QueryRowContext(ctx, `
				SELECT EXISTS (SELECT 1 FROM error_issues WHERE fingerprint = $1),
				       (SELECT count(*) FROM error_issues WHERE source = 'panel' AND first_seen > now() - interval '1 day')`,
				fp).Scan(&known, &recent); err != nil {
				return err
			}
			if !known && recent >= panelIssuesPerDay {
				return nil
			}
		}
		var issue int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO error_issues (fingerprint, source, title, culprit) VALUES ($1, $2, $3, $4)
			ON CONFLICT (store_id, fingerprint) DO UPDATE
			SET count = error_issues.count + 1, last_seen = now(), title = EXCLUDED.title,
			    status = CASE WHEN error_issues.status = 'resolved' THEN 'unresolved' ELSE error_issues.status END
			RETURNING id`, fp, c.Source, clip(title, 300), clip(c.Culprit, 300)).Scan(&issue); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO error_events (issue_id, message, stack, context) VALUES ($1, $2, $3, $4)`,
			issue, c.Message, c.Stack, ctxJSON); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			DELETE FROM error_events WHERE issue_id = $1 AND id NOT IN (
			    SELECT id FROM error_events WHERE issue_id = $1 ORDER BY id DESC LIMIT $2)`, issue, errorEventsKept)
		return err
	})
}

// plumbing is a frame that is not where anything failed: the runtime, net/http,
// and the engine's own recording and recovering. Judged by the function, not
// the file, because release builds are -trimpath and their paths carry none of
// the prefixes a file test would look for.
func plumbing(fn string) bool {
	for _, p := range []string{"runtime.", "runtime/debug.", "net/http.", "panic("} {
		if strings.HasPrefix(fn, p) {
			return true
		}
	}
	for _, own := range []string{"/core.RespondError", "/core.captureServerError", "/core.capturePanic",
		"/core.(*App).recoverMW", "/core.(*ErrorTracker)."} {
		if strings.Contains(fn, own) {
			return true
		}
	}
	return false
}

// goCulprit is the first frame of a Go stack that is not plumbing: the code
// that failed.
func goCulprit(stack string) string {
	lines := strings.Split(stack, "\n")
	for i := 0; i+1 < len(lines); i++ {
		fn := strings.TrimSpace(lines[i])
		loc := strings.TrimSpace(lines[i+1])
		if !strings.HasPrefix(lines[i+1], "\t") || fn == "" {
			continue
		}
		file, _, _ := strings.Cut(loc, " +")
		if plumbing(fn) {
			continue
		}
		if at := strings.LastIndex(fn, "("); at > 0 {
			fn = fn[:at]
		}
		if at := strings.LastIndex(fn, "/"); at >= 0 {
			fn = fn[at+1:]
		}
		// The file without its line: a line moves with every edit above it,
		// and the issue should not.
		base, _, _ := strings.Cut(file, ":")
		if at := strings.LastIndex(base, "/"); at >= 0 {
			base = base[at+1:]
		}
		return fn + " (" + base + ")"
	}
	return ""
}

var jsFrame = regexp.MustCompile(`(https?://[^\s)]+|/[^\s)]+):(\d+):\d+`)

var chunkHash = regexp.MustCompile(`\.[A-Za-z0-9_-]{6,}(\.js)$`)

// jsCulprit is the file of a browser stack's first frame, without the build's
// content hash ("nodes/12.Ab3xYz9.js" is "nodes/12.js"): a hash and a line
// number change with every release, and an issue that changed its name every
// deploy would never be seen to come back.
func jsCulprit(stack string) string {
	m := jsFrame.FindStringSubmatch(stack)
	if m == nil {
		return ""
	}
	where := m[1]
	if u, err := url.Parse(where); err == nil && u.Path != "" {
		where = u.Path
	}
	return chunkHash.ReplaceAllString(strings.TrimPrefix(where, "/"), "$1")
}

// List is a page of the store's issues, most recently seen first.
func (t *ErrorTracker) List(ctx context.Context, q ErrorQuery) ([]*ErrorIssue, int, error) {
	if q.Limit <= 0 {
		q.Limit = DefaultLimit
	}
	rows, err := t.app.db.QueryContext(ctx, `
		SELECT id, fingerprint, source, title, culprit, status, count, first_seen, last_seen, count(*) OVER ()
		FROM error_issues
		WHERE ($1 = '' OR status = $1) AND ($2 = '' OR source = $2)
		  AND ($3 = '' OR title ILIKE '%' || $3 || '%' OR culprit ILIKE '%' || $3 || '%')
		ORDER BY last_seen DESC, id DESC
		LIMIT $4 OFFSET $5`, q.Status, q.Source, strings.TrimSpace(q.Search), q.Limit, q.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*ErrorIssue{}
	total := 0
	for rows.Next() {
		var is ErrorIssue
		if err := rows.Scan(&is.ID, &is.Fingerprint, &is.Source, &is.Title, &is.Culprit, &is.Status,
			&is.Count, &is.FirstSeen, &is.LastSeen, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, &is)
	}
	return out, total, rows.Err()
}

// Get is one issue with its latest occurrences, newest first.
func (t *ErrorTracker) Get(ctx context.Context, id int64) (*ErrorIssue, error) {
	var is ErrorIssue
	err := t.app.db.QueryRowContext(ctx, `
		SELECT id, fingerprint, source, title, culprit, status, count, first_seen, last_seen
		FROM error_issues WHERE id = $1`, id).Scan(&is.ID, &is.Fingerprint, &is.Source, &is.Title,
		&is.Culprit, &is.Status, &is.Count, &is.FirstSeen, &is.LastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("no error %d", id)
	}
	if err != nil {
		return nil, err
	}
	rows, err := t.app.db.QueryContext(ctx, `
		SELECT id, message, stack, context, created_at FROM error_events
		WHERE issue_id = $1 ORDER BY id DESC LIMIT $2`, id, errorEventsKept)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var e ErrorEvent
		var raw []byte
		if err := rows.Scan(&e.ID, &e.Message, &e.Stack, &raw, &e.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &e.Context)
		is.Events = append(is.Events, &e)
	}
	if len(is.Events) > 0 {
		is.LastEvent = is.Events[0]
	}
	return &is, rows.Err()
}

// SetStatus resolves, ignores or reopens an issue.
func (t *ErrorTracker) SetStatus(ctx context.Context, id int64, status string) (*ErrorIssue, error) {
	switch status {
	case "unresolved", "resolved", "ignored":
	default:
		return nil, Validationf("status is unresolved, resolved or ignored")
	}
	res, err := t.app.db.ExecContext(ctx, `UPDATE error_issues SET status = $2 WHERE id = $1`, id, status)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, NotFoundf("no error %d", id)
	}
	return t.Get(ctx, id)
}

// ---- Capturing on the request path ----

// requestErrors rides on every request from recoverMW inward: the tracker, and
// the store once storeMW has resolved it, so a panic — caught outside storeMW
// — is still recorded against the store it happened in.
type requestErrors struct {
	tracker *ErrorTracker
	store   string
}

type requestErrorsKey struct{}

func requestErrorsFrom(ctx context.Context) *requestErrors {
	re, _ := ctx.Value(requestErrorsKey{}).(*requestErrors)
	return re
}

// noteStore tells the request's error record which store it is serving.
func noteStore(ctx context.Context, store string) {
	if re := requestErrorsFrom(ctx); re != nil {
		re.store = store
	}
}

var tokenish = regexp.MustCompile(`^[A-Za-z0-9_-]{20,}$`)

// safePath is where a request went, without the secrets some paths carry: an
// invitation's or a reset's token, a cart's. The route's own pattern when the
// mux matched one ("/api/admin/invitations/accept/{token}"), else the path
// with any token-like segment blanked.
func safePath(r *http.Request) string {
	if r.Pattern != "" {
		if _, path, ok := strings.Cut(r.Pattern, " "); ok {
			return path
		}
		return r.Pattern
	}
	return blankTokens(r.URL.Path)
}

func blankTokens(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if tokenish.MatchString(p) {
			parts[i] = "{token}"
		}
	}
	return strings.Join(parts, "/")
}

func requestContext(r *http.Request, status int) map[string]any {
	c := map[string]any{"method": r.Method, "path": safePath(r), "status": status, "version": Version}
	if su := SuperuserFrom(r.Context()); su != nil {
		c["operator"] = su.Email
	}
	return c
}

// captureServerError records a 5xx answered through RespondError, with the
// cause the response leaves out.
func captureServerError(r *http.Request, status int, err error) {
	re := requestErrorsFrom(r.Context())
	if re == nil {
		return
	}
	re.tracker.Capture(r.Context(), CapturedError{
		Source: "server", Message: err.Error(), Stack: string(debug.Stack()),
		Context: requestContext(r, status),
	})
}

// capturePanic records a panic recoverMW caught.
func capturePanic(r *http.Request, p any, stack []byte) {
	re := requestErrorsFrom(r.Context())
	if re == nil || re.store == "" {
		return
	}
	re.tracker.Capture(WithStore(r.Context(), re.store), CapturedError{
		Source: "server", Kind: "panic", Message: fmt.Sprint(p), Stack: string(stack),
		Context: requestContext(r, http.StatusInternalServerError),
	})
}

// ---- Routes ----

func (a *App) mountErrorRoutes() {
	a.HandleAdminFunc("GET /api/admin/errors", a.handleListErrors, RightErrorsRead)
	a.HandleAdminFunc("GET /api/admin/errors/{id}", a.handleGetError, RightErrorsRead)
	a.HandleAdminFunc("PATCH /api/admin/errors/{id}", a.handleSetErrorStatus, RightErrorsWrite)
	// A crash in anybody's panel is reported, whatever they may read.
	a.HandleAdminFunc("POST /api/admin/errors", a.handleReportPanelError)
}

func (a *App) handleListErrors(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := Page(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	q := r.URL.Query()
	query := ErrorQuery{Status: q.Get("status"), Source: q.Get("source"), Search: q.Get("q"), Limit: limit, Offset: offset}
	switch query.Status {
	case "", "unresolved", "resolved", "ignored":
	default:
		RespondError(w, r, Validationf("status is unresolved, resolved or ignored"))
		return
	}
	switch query.Source {
	case "", "server", "panel":
	default:
		RespondError(w, r, Validationf("source is server or panel"))
		return
	}
	issues, total, err := a.Errors().List(r.Context(), query)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	RespondList(w, issues, ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (a *App) handleGetError(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	is, err := a.Errors().Get(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, is)
}

func (a *App) handleSetErrorStatus(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	is, err := a.Errors().SetStatus(r.Context(), id, in.Status)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, is)
}

// reportLimit is how many panel reports one operator may send a store a
// minute; past it a report is answered as accepted and dropped.
const reportLimit = 20

type reportWindow struct {
	start time.Time
	n     int
}

var (
	reportMu      sync.Mutex
	reportWindows = map[string]*reportWindow{}
)

// allowReport counts a report against its store and operator.
func allowReport(key string, now time.Time) bool {
	reportMu.Lock()
	defer reportMu.Unlock()
	if len(reportWindows) > 10000 {
		for k, w := range reportWindows {
			if now.Sub(w.start) > time.Minute {
				delete(reportWindows, k)
			}
		}
	}
	w := reportWindows[key]
	if w == nil || now.Sub(w.start) > time.Minute {
		reportWindows[key] = &reportWindow{start: now, n: 1}
		return true
	}
	w.n++
	return w.n <= reportLimit
}

func (a *App) handleReportPanelError(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
		Stack   string `json:"stack"`
		URL     string `json:"url"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	if strings.TrimSpace(in.Message) == "" {
		RespondError(w, r, Validationf("a report needs the error's message"))
		return
	}
	// Anybody signed in may report, so nobody may report without limit: a
	// browser in a loop, or somebody filling the store's page on purpose.
	who := "token"
	if su := SuperuserFrom(r.Context()); su != nil {
		who = su.Email
	}
	if store, _ := StoreFrom(r.Context()); !allowReport(store+"\x00"+who, time.Now()) {
		Respond(w, http.StatusAccepted, map[string]bool{"recorded": false})
		return
	}
	// The path only: a query string can carry a token or a search somebody
	// typed, and the page is what says where it broke.
	page := in.URL
	if u, err := url.Parse(in.URL); err == nil {
		page = u.Path
	}
	page = blankTokens(page)
	c := map[string]any{"url": clip(page, 300), "user_agent": clip(r.UserAgent(), 300), "version": Version}
	if su := SuperuserFrom(r.Context()); su != nil {
		c["operator"] = su.Email
	}
	a.Errors().Capture(r.Context(), CapturedError{
		Source: "panel", Kind: clip(strings.TrimSpace(in.Kind), 80), Message: in.Message, Stack: in.Stack, Context: c,
	})
	Respond(w, http.StatusAccepted, map[string]bool{"recorded": true})
}
