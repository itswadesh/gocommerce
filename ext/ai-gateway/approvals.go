package aigateway

import (
	"bytes"
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
	"slices"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// The approval queue (D105, ADR 0003): the store side's one object for
// "something an agent wants done, waiting for a person".
//
// An agent never runs a write that reaches a customer, moves money or stock,
// or changes a price, a catalogue or a published page. It files a proposal
// naming an action and its arguments, and a person with that action's own
// right approves it. Approving runs the action as the approver, through the
// same core service the button in the panel calls, so core writes its own
// audit row and event in its own transaction (rule 3) and the trail says who
// let it happen and which proposal it was.
//
// This step registers no actions: MCP's arrive in step 5. The queue, its
// rules and its screen are here so that step adds actions and nothing else.

// RightApprove opens the queue. Approving an action also needs the action's
// own right.
const RightApprove gocommerce.Right = "ai.approve"

// Approval states. approved is the moment between the decision and the
// action's end, held so two approvals cannot both run it.
const (
	ApprovalPending  = "pending"
	ApprovalApproved = "approved"
	ApprovalExecuted = "executed"
	ApprovalFailed   = "failed"
	ApprovalRejected = "rejected"
	ApprovalExpired  = "expired"
)

// proposalLife is how long a proposal waits for a person.
const proposalLife = 7 * 24 * time.Hour

// executeTimeout bounds an approved action. Once claimed it runs to the end
// whatever the request does, so it needs a bound of its own.
const executeTimeout = 30 * time.Second

// Action is something a person may approve.
type Action struct {
	// Kind names it: "order.refund", "variant.set_price".
	Kind string
	// Right is what approving it needs, beside ai.approve: the right the
	// panel's own button for this act is gated on.
	Right gocommerce.Right
	// Preview says what the action would do to the store as it is now. It
	// must only read: it runs when a proposal is filed, opened and approved.
	Preview func(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
	// Execute does it, through core services, with the approver on ctx.
	Execute func(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}

var actionKindRE = regexp.MustCompile(`^[a-z0-9]+([_-][a-z0-9]+)*(\.[a-z0-9]+([_-][a-z0-9]+)*)+$`)

// RegisterAction makes an action proposable.
func (m *Module) RegisterAction(a Action) error {
	switch {
	case !actionKindRE.MatchString(a.Kind):
		return fmt.Errorf("ai action %q: name it area.verb in lowercase, e.g. order.refund", a.Kind)
	case a.Right == "":
		return fmt.Errorf("ai action %s: name the right approving it needs", a.Kind)
	case a.Preview == nil || a.Execute == nil:
		return fmt.Errorf("ai action %s: needs both Preview and Execute", a.Kind)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.actions[a.Kind]; ok {
		return fmt.Errorf("ai action %s is already registered", a.Kind)
	}
	act := a
	m.actions[a.Kind] = &act
	return nil
}

func (m *Module) action(kind string) *Action {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.actions[kind]
}

// Approval is one proposal.
type Approval struct {
	ID      int64           `json:"id"`
	Kind    string          `json:"kind"`
	Args    json.RawMessage `json:"args"`
	Preview json.RawMessage `json:"preview"`
	// ProposedPreview is the preview as filed, beside Preview recomputed
	// now; read on a single proposal only. Changed says they differ.
	ProposedPreview json.RawMessage `json:"proposed_preview,omitempty"`
	// PreviewHash names Preview; approving sends back the one reviewed.
	PreviewHash string `json:"preview_hash,omitempty"`
	Changed     bool   `json:"changed,omitempty"`
	// Stale says why the preview could not be recomputed.
	Stale string `json:"stale,omitempty"`
	// Right is what approving needs beside ai.approve; empty when this build
	// has no such action.
	Right string `json:"right"`
	// RequestedByKeyID is the agent key that filed it, once agent keys exist.
	RequestedByKeyID *int64          `json:"requested_by_key_id"`
	RequestedByLabel string          `json:"requested_by_label"`
	TraceID          string          `json:"trace_id"`
	Status           string          `json:"status"`
	ExpiresAt        time.Time       `json:"expires_at"`
	DecidedBy        *int64          `json:"decided_by"`
	DecidedByEmail   string          `json:"decided_by_email,omitempty"`
	DecidedAt        *time.Time      `json:"decided_at"`
	Result           json.RawMessage `json:"result,omitempty"`
	Error            string          `json:"error,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
}

// approvalStatus reads a pending proposal past its time as expired, so the
// expiry needs no sweeper to be true.
const approvalStatus = `CASE WHEN status = 'pending' AND expires_at <= now() THEN 'expired' ELSE status END`

const approvalColumns = `id, kind, args, preview, requested_by_key_id, requested_by_label, trace_id, ` +
	approvalStatus + `, expires_at, decided_by, decided_by_email, decided_at, result, coalesce(error, ''), created_at`

func (m *Module) scanApproval(row interface{ Scan(...any) error }) (*Approval, error) {
	var a Approval
	var args, preview, result []byte
	if err := row.Scan(&a.ID, &a.Kind, &args, &preview, &a.RequestedByKeyID, &a.RequestedByLabel, &a.TraceID,
		&a.Status, &a.ExpiresAt, &a.DecidedBy, &a.DecidedByEmail, &a.DecidedAt, &result, &a.Error, &a.CreatedAt); err != nil {
		return nil, err
	}
	a.Args, a.Preview = args, preview
	a.PreviewHash = previewHash(preview)
	if len(result) > 0 {
		a.Result = result
	}
	if act := m.action(a.Kind); act != nil {
		a.Right = string(act.Right)
	}
	return &a, nil
}

// Propose files a proposal, or returns the one already filed under the key.
// Its preview is computed now; a proposal whose preview fails is not filed,
// because a person could not judge it.
func (m *Module) Propose(ctx context.Context, kind string, args any, idemKey string) (*Approval, error) {
	act := m.action(kind)
	if act == nil {
		return nil, fmt.Errorf("no ai action is called %q", kind)
	}
	if idemKey = strings.TrimSpace(idemKey); idemKey == "" {
		return nil, errors.New("a proposal needs an idempotency key, so a retried request files it once")
	}
	if existing, err := m.approvalByKey(ctx, idemKey); err != nil || existing != nil {
		return existing, err
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("ai action %s: encode the arguments: %w", kind, err)
	}
	preview, err := act.Preview(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("ai action %s: preview: %w", kind, err)
	}
	// Who asked, in the words every trail prints. A key resolves to a
	// Superuser named "api key: <name>"; work with nobody on its context is
	// the engine's own.
	label := "the engine"
	if su := gocommerce.SuperuserFrom(ctx); su != nil {
		label = su.Email
	}
	var id int64
	err = m.db.QueryRowContext(ctx, `
		INSERT INTO approval_requests (kind, args, preview, idem_key, requested_by_label, trace_id, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, now() + make_interval(secs => $7))
		ON CONFLICT (store_id, idem_key) DO NOTHING
		RETURNING id`,
		kind, raw, []byte(preview), idemKey, label, gocommerce.TraceID(ctx), proposalLife.Seconds()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		// Filed by a racing request between the read above and this insert.
		return m.approvalByKey(ctx, idemKey)
	}
	if err != nil {
		return nil, err
	}
	return m.Approval(ctx, id)
}

func (m *Module) approvalByKey(ctx context.Context, key string) (*Approval, error) {
	a, err := m.scanApproval(m.db.QueryRowContext(ctx,
		`SELECT `+approvalColumns+` FROM approval_requests WHERE idem_key = $1`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

// Approval reads one of ctx's store's proposals as filed.
func (m *Module) Approval(ctx context.Context, id int64) (*Approval, error) {
	a, err := m.scanApproval(m.db.QueryRowContext(ctx,
		`SELECT `+approvalColumns+` FROM approval_requests WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("no proposal %d", id)
	}
	return a, err
}

var approvalStatuses = []string{ApprovalPending, ApprovalApproved, ApprovalExecuted, ApprovalFailed, ApprovalRejected, ApprovalExpired}

// Approvals is a page of ctx's store's proposals, newest first.
func (m *Module) Approvals(ctx context.Context, status string, limit, offset int) ([]*Approval, int, error) {
	where, args := "true", []any{}
	switch status {
	case "":
	case ApprovalPending:
		where = `status = 'pending' AND expires_at > now()`
	case ApprovalExpired:
		where = `(status = 'expired' OR (status = 'pending' AND expires_at <= now()))`
	default:
		where, args = `status = $1`, append(args, status)
	}
	var total int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM approval_requests WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := m.db.QueryContext(ctx, `SELECT `+approvalColumns+` FROM approval_requests WHERE `+where+
		fmt.Sprintf(` ORDER BY id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Approval{}
	for rows.Next() {
		a, err := m.scanApproval(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// recompute puts the store as it is now beside the proposal as filed.
func (m *Module) recompute(ctx context.Context, a *Approval) {
	a.ProposedPreview = a.Preview
	act := m.action(a.Kind)
	if act == nil {
		a.Stale = fmt.Sprintf("this build has no action called %q, so it cannot be previewed or run", a.Kind)
		return
	}
	fresh, err := act.Preview(ctx, a.Args)
	if err != nil {
		a.Stale = "the preview could not be recomputed: " + err.Error()
		return
	}
	a.Preview = fresh
	a.PreviewHash = previewHash(fresh)
	a.Changed = !sameJSON(a.ProposedPreview, fresh)
}

// previewHash names a preview by its content, with keys in a fixed order, so
// two previews that say the same thing have the same name.
func previewHash(preview json.RawMessage) string {
	var v any
	canonical := []byte(preview)
	if json.Unmarshal(preview, &v) == nil {
		canonical, _ = json.Marshal(v)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	ca, _ := json.Marshal(x)
	cb, _ := json.Marshal(y)
	return bytes.Equal(ca, cb)
}

// person is the signed-in operator when the caller is a person, else nil.
//
// Approval is a person's act (ADR 0003). The static token puts no Superuser
// on the context. An API key does, with no ID, because no superusers row
// stands behind it; agent keys will carry their own key id beside that zero,
// never in it, since ID is what the audit trail records as the operator. So a
// positive ID is what makes the caller a person, and it stays the test when
// keys gain ids of their own.
func person(ctx context.Context) *gocommerce.Superuser {
	su := gocommerce.SuperuserFrom(ctx)
	if su == nil || su.ID <= 0 {
		return nil
	}
	return su
}

// decide checks that the caller may decide this proposal: a person, holding
// the action's right. An action this build does not have can still be
// rejected, by ai.approve alone, so an orphaned proposal can be cleared.
func (m *Module) decide(ctx context.Context, id int64, approving bool) (*gocommerce.Superuser, *Approval, *Action, error) {
	su := person(ctx)
	if su == nil {
		return nil, nil, nil, gocommerce.Forbiddenf(
			"deciding a proposal needs a person signed in to the panel; an API key or the admin token cannot approve or reject one")
	}
	// Checked here as well as on the routes: another module calling the Go
	// API from a route of its own must not decide for somebody the store has
	// not let decide proposals.
	if !su.Has(RightApprove) {
		return nil, nil, nil, gocommerce.Forbiddenf("your role (%s) does not carry %s, which deciding a proposal needs", su.Role, RightApprove)
	}
	a, err := m.Approval(ctx, id)
	if err != nil {
		return nil, nil, nil, err
	}
	act := m.action(a.Kind)
	if act == nil && approving {
		return nil, nil, nil, gocommerce.Conflictf("this build has no action called %q, so proposal %d cannot run; reject it instead", a.Kind, id)
	}
	if act != nil && !su.Has(act.Right) {
		return nil, nil, nil, gocommerce.Forbiddenf("your role (%s) does not carry %s, which %s needs", su.Role, act.Right, a.Kind)
	}
	switch a.Status {
	case ApprovalPending:
	case ApprovalExpired:
		return nil, nil, nil, gocommerce.Conflictf("proposal %d expired on %s and cannot run", id, a.ExpiresAt.Format(time.RFC3339))
	default:
		return nil, nil, nil, gocommerce.Conflictf("proposal %d is already %s", id, a.Status)
	}
	return su, a, act, nil
}

// Approve runs a pending proposal as the person on ctx. seen is the hash of
// the preview they reviewed (Approval.PreviewHash, from opening it).
func (m *Module) Approve(ctx context.Context, id int64, seen string) (*Approval, error) {
	su, a, act, err := m.decide(ctx, id, true)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(seen) == "" {
		return nil, gocommerce.Validationf("preview_hash is required: send the preview_hash of the proposal as you reviewed it, so what runs is what you saw")
	}
	// Recomputed once more: the person approves the store as it is, and the
	// action runs against it, never against the snapshot. If it moved since
	// they looked, they review it again; nothing runs on a preview nobody saw.
	preview, err := act.Preview(ctx, a.Args)
	if err != nil {
		return nil, gocommerce.Conflictf("the preview could not be recomputed, so proposal %d was not run: %v", id, err)
	}
	if now := previewHash(preview); now != seen {
		return nil, gocommerce.Conflictf("The store changed while you were looking; review it again.").WithDetails(map[string]any{
			"preview":          preview,
			"preview_hash":     now,
			"proposed_preview": a.Preview,
		})
	}
	// One conditional UPDATE is the whole of "runs once": of two approvals,
	// one moves the row from pending and the other finds it moved.
	res, err := m.db.ExecContext(ctx, `
		UPDATE approval_requests
		   SET status = 'approved', preview = $2, decided_by = $3, decided_by_email = $4, decided_at = now()
		 WHERE id = $1 AND status = 'pending' AND expires_at > now()`,
		id, []byte(preview), su.ID, su.Email)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		now, err := m.Approval(ctx, id)
		if err != nil {
			return nil, err
		}
		return nil, gocommerce.Conflictf("proposal %d is already %s", id, now.Status)
	}

	// Once claimed, the action runs to its end even if the request goes: a
	// half-run approval is worse than a slow one.
	run, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.executeTimeout)
	defer cancel()
	run = gocommerce.WithActorLabel(run, fmt.Sprintf("approval #%d", id))
	result, execErr := m.runAction(run, act, a.Args)

	// The outcome is written on a context of its own. The action's may be
	// spent, by its deadline or by the request going, and a write on it
	// would fail and leave the proposal "approved" for ever.
	write, done := context.WithTimeout(context.WithoutCancel(ctx), outcomeTimeout)
	defer done()
	if execErr != nil {
		_, err = m.db.ExecContext(write, `
			UPDATE approval_requests SET status = 'failed', error = $2
			 WHERE id = $1 AND status = 'approved'`, id, truncate(execErr.Error(), 1000))
	} else {
		var stored any
		if result != nil {
			stored = []byte(result)
		}
		_, err = m.db.ExecContext(write, `
			UPDATE approval_requests SET status = 'executed', result = $2
			 WHERE id = $1 AND status = 'approved'`, id, stored)
	}
	if err != nil {
		return nil, err
	}
	return m.Approval(write, id)
}

// outcomeTimeout bounds writing a decided proposal's outcome.
const outcomeTimeout = 10 * time.Second

// runAction runs an approved action. A panic is the action's failure, with the
// panic as its reason, not the request's: the proposal must still end.
func (m *Module) runAction(ctx context.Context, act *Action, args json.RawMessage) (out json.RawMessage, err error) {
	defer func() {
		if p := recover(); p != nil {
			m.log.Error("ai action panicked", "action", act.Kind, "panic", p, "stack", string(debug.Stack()))
			out, err = nil, fmt.Errorf("the action failed: %v", p)
		}
	}()
	return act.Execute(ctx, args)
}

// Reject closes a pending proposal without running it.
func (m *Module) Reject(ctx context.Context, id int64) (*Approval, error) {
	su, _, _, err := m.decide(ctx, id, false)
	if err != nil {
		return nil, err
	}
	res, err := m.db.ExecContext(ctx, `
		UPDATE approval_requests
		   SET status = 'rejected', decided_by = $2, decided_by_email = $3, decided_at = now()
		 WHERE id = $1 AND status = 'pending' AND expires_at > now()`, id, su.ID, su.Email)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		now, err := m.Approval(ctx, id)
		if err != nil {
			return nil, err
		}
		return nil, gocommerce.Conflictf("proposal %d is already %s", id, now.Status)
	}
	return m.Approval(ctx, id)
}

// ------------------------------------------------------------------ routes

func (m *Module) mountApprovalRoutes(app *gocommerce.App) {
	const base = "/api/admin/x/ai-gateway/approvals"
	app.HandleAdminFunc("GET "+base, m.handleApprovals, RightApprove)
	app.HandleAdminFunc("GET "+base+"/{id}", m.handleApproval, RightApprove)
	app.HandleAdminFunc("POST "+base+"/{id}/approve", m.handleApprove, RightApprove)
	app.HandleAdminFunc("POST "+base+"/{id}/reject", m.handleReject, RightApprove)
}

func (m *Module) handleApprovals(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && !slices.Contains(approvalStatuses, status) {
		gocommerce.RespondError(w, r, gocommerce.Validationf("status is one of %s", strings.Join(approvalStatuses, ", ")))
		return
	}
	list, total, err := m.Approvals(r.Context(), status, limit, offset)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.RespondList(w, list, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (m *Module) handleApproval(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	a, err := m.Approval(r.Context(), id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// Only an open proposal is worth recomputing: a decided one shows what
	// was decided on.
	if a.Status == ApprovalPending {
		m.recompute(r.Context(), a)
	}
	gocommerce.Respond(w, http.StatusOK, a)
}

func (m *Module) handleApprove(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PreviewHash string `json:"preview_hash"`
	}
	// The body is optional to decode, not to send: Approve refuses an
	// approval that names no reviewed preview, after the checks on who is
	// asking, so a caller who may not decide is told that first.
	if r.ContentLength != 0 {
		if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
	}
	m.approvalAction(w, r, func(ctx context.Context, id int64) (*Approval, error) {
		return m.Approve(ctx, id, in.PreviewHash)
	})
}

func (m *Module) handleReject(w http.ResponseWriter, r *http.Request) {
	m.approvalAction(w, r, m.Reject)
}

func (m *Module) approvalAction(w http.ResponseWriter, r *http.Request, act func(context.Context, int64) (*Approval, error)) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	a, err := act(r.Context(), id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, a)
}
