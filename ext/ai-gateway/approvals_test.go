package aigateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

type retitle struct {
	ProductID int64  `json:"product_id"`
	Title     string `json:"title"`
}

// retitleAction is an action of the shape step 5 will register: it reads the
// store to preview, and changes it only through a core service.
func retitleAction(app func() *gocommerce.App, runs *atomic.Int32, right gocommerce.Right) Action {
	return Action{
		Kind:  "test.retitle",
		Right: right,
		Preview: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			var a retitle
			if err := json.Unmarshal(args, &a); err != nil {
				return nil, err
			}
			p, err := app().Products().GetProduct(ctx, a.ProductID)
			if err != nil {
				return nil, err
			}
			return json.Marshal(map[string]string{"from": p.Title, "to": a.Title})
		},
		Execute: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			runs.Add(1)
			time.Sleep(50 * time.Millisecond)
			var a retitle
			if err := json.Unmarshal(args, &a); err != nil {
				return nil, err
			}
			p, err := app().Products().UpdateProduct(ctx, a.ProductID, gocommerce.ProductPatch{Title: &a.Title})
			if err != nil {
				return nil, err
			}
			return json.Marshal(map[string]string{"title": p.Title})
		},
	}
}

type approvalHarness struct {
	app     *gocommerce.App
	m       *Module
	runs    *atomic.Int32
	product *gocommerce.Product
}

func newApprovals(t *testing.T, right gocommerce.Right) *approvalHarness {
	t.Helper()
	h := &approvalHarness{runs: &atomic.Int32{}}
	m := New(Config{})
	if err := m.RegisterAction(retitleAction(func() *gocommerce.App { return h.app }, h.runs, right)); err != nil {
		t.Fatal(err)
	}
	h.m = m
	h.app = gctest.New(t, m)
	h.product = gctest.CreateProduct(t, h.app, "WIDGET-1", 1999, 5)
	return h
}

func (h *approvalHarness) propose(t *testing.T, ctx context.Context, title, key string) *Approval {
	t.Helper()
	a, err := h.m.Propose(ctx, "test.retitle", retitle{ProductID: h.product.ID, Title: title}, key)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	return a
}

func decodeApproval(t *testing.T, rec interface {
	Result() *http.Response
}) Approval {
	t.Helper()
	var env struct {
		Data  Approval `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Result().Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.Error != nil {
		t.Fatalf("error: %s", env.Error.Message)
	}
	return env.Data
}

// seen is what the panel sends to approve: the hash of the preview the
// person was shown when they opened the proposal.
func (h *approvalHarness) seen(t *testing.T, id int64) map[string]string {
	t.Helper()
	got := decodeApproval(t, gctest.AdminRequest(t, h.app, http.MethodGet, fmt.Sprintf("/api/admin/x/ai-gateway/approvals/%d", id), nil))
	if got.PreviewHash == "" {
		t.Fatal("an opened proposal carries no preview hash")
	}
	return map[string]string{"preview_hash": got.PreviewHash}
}

func previewOf(t *testing.T, raw json.RawMessage) map[string]string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("preview %s: %v", raw, err)
	}
	return m
}

// A proposal is filed once per key, with its preview, its trace and a week
// to live.
func TestAProposalIsFiledOnceWithItsPreview(t *testing.T) {
	h := newApprovals(t, gocommerce.RightCatalogWrite)
	ctx := gocommerce.WithTraceID(context.Background(), "trace-of-the-agent-run")

	a := h.propose(t, ctx, "Widget, two-pack", "retitle:1")
	again := h.propose(t, ctx, "Something else entirely", "retitle:1")
	if again.ID != a.ID {
		t.Errorf("ids %d and %d, want one proposal per key", a.ID, again.ID)
	}
	if a.Status != ApprovalPending || a.TraceID != "trace-of-the-agent-run" || a.RequestedByKeyID != nil {
		t.Errorf("proposal = %+v", a)
	}
	if p := previewOf(t, a.Preview); p["from"] != h.product.Title || p["to"] != "Widget, two-pack" {
		t.Errorf("preview = %v", p)
	}
	if left := time.Until(a.ExpiresAt); left < 6*24*time.Hour || left > 7*24*time.Hour+time.Minute {
		t.Errorf("expires in %v, want seven days", left)
	}
	if _, err := h.m.Propose(ctx, "no.such.action", nil, "k"); err == nil {
		t.Error("a proposal for an action nobody registered was filed")
	}
	if _, err := h.m.Propose(ctx, "test.retitle", retitle{ProductID: h.product.ID, Title: "x"}, ""); err == nil {
		t.Error("a proposal without an idempotency key was filed")
	}
	if _, err := h.m.Propose(ctx, "test.retitle", retitle{ProductID: 999999, Title: "x"}, "k2"); err == nil {
		t.Error("a proposal whose preview failed was filed")
	}
	if h.runs.Load() != 0 {
		t.Error("filing a proposal ran the action")
	}
}

// Opening a proposal recomputes its preview, so a change since it was filed
// shows as one.
func TestOpeningAProposalRecomputesItsPreview(t *testing.T) {
	h := newApprovals(t, gocommerce.RightCatalogWrite)
	ctx := context.Background()
	a := h.propose(t, ctx, "New title", "retitle:1")

	moved := "Renamed by somebody else"
	if _, err := h.app.Products().UpdateProduct(ctx, h.product.ID, gocommerce.ProductPatch{Title: &moved}); err != nil {
		t.Fatal(err)
	}
	got := decodeApproval(t, gctest.AdminRequest(t, h.app, http.MethodGet, fmt.Sprintf("/api/admin/x/ai-gateway/approvals/%d", a.ID), nil))
	if p := previewOf(t, got.Preview); p["from"] != moved {
		t.Errorf("preview = %v, want it read from the product as it is now", p)
	}
	if p := previewOf(t, got.ProposedPreview); p["from"] != h.product.Title {
		t.Errorf("proposed preview = %v, want the one filed", p)
	}
	if !got.Changed || got.Right != string(gocommerce.RightCatalogWrite) {
		t.Errorf("changed = %v, right = %q", got.Changed, got.Right)
	}
}

// Approving runs the action as the approver, labelled with the proposal, and
// core writes its own audit row for it, as for a click.
func TestApprovingRunsTheActionAsTheApprover(t *testing.T) {
	h := newApprovals(t, gocommerce.RightCatalogWrite)
	ctx := context.Background()
	a := h.propose(t, ctx, "Approved title", "retitle:1")
	owner := gctest.OperatorToken(t, h.app, "owner@example.com", gocommerce.RoleOwner)

	rec := gctest.SessionRequest(t, h.app, owner, http.MethodPost, fmt.Sprintf("/api/admin/x/ai-gateway/approvals/%d/approve", a.ID), h.seen(t, a.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("approve = %d: %s", rec.Code, rec.Body)
	}
	got := decodeApproval(t, rec)
	if got.Status != ApprovalExecuted || got.DecidedBy == nil || got.DecidedByEmail != "owner@example.com" || got.DecidedAt == nil {
		t.Errorf("approval = %+v", got)
	}
	if r := previewOf(t, got.Result); r["title"] != "Approved title" {
		t.Errorf("result = %v", r)
	}
	if p, _ := h.app.Products().GetProduct(ctx, h.product.ID); p.Title != "Approved title" {
		t.Errorf("title = %q", p.Title)
	}
	entries, _, err := h.app.Audit().List(ctx, gocommerce.AuditQuery{EntityType: "product", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range entries {
		if e.ActorLabel == fmt.Sprintf("approval #%d", a.ID) {
			found = true
			if e.ActorKind != gocommerce.ActorOperator || e.ActorEmail != "owner@example.com" {
				t.Errorf("audit actor = %s %s", e.ActorKind, e.ActorEmail)
			}
		}
	}
	if !found {
		t.Errorf("no audit row labelled with the approval among %+v", entries)
	}
}

// Two people approving one proposal at once run it once.
func TestADoubleApprovalRunsOnce(t *testing.T) {
	h := newApprovals(t, gocommerce.RightCatalogWrite)
	a := h.propose(t, context.Background(), "Once", "retitle:1")
	owner := gctest.OperatorToken(t, h.app, "owner@example.com", gocommerce.RoleOwner)
	body := h.seen(t, a.ID)

	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = gctest.SessionRequest(t, h.app, owner, http.MethodPost,
				fmt.Sprintf("/api/admin/x/ai-gateway/approvals/%d/approve", a.ID), body).Code
		}()
	}
	wg.Wait()
	slices.Sort(codes)
	if h.runs.Load() != 1 {
		t.Errorf("the action ran %d times", h.runs.Load())
	}
	if codes[0] != http.StatusOK || codes[1] != http.StatusConflict || codes[3] != http.StatusConflict {
		t.Errorf("codes = %v, want one 200 and the rest 409", codes)
	}
}

// Approval is a person's: an API key and the static token are refused, and so
// is a person holding ai.approve but not the action's own right.
func TestApprovalIsRefusedForAKeyAndForAPersonWithoutTheRight(t *testing.T) {
	h := newApprovals(t, gocommerce.RightTaxesWrite)
	ctx := context.Background()
	a := h.propose(t, ctx, "Refused", "retitle:1")
	path := fmt.Sprintf("/api/admin/x/ai-gateway/approvals/%d/approve", a.ID)

	_, secret, err := h.app.APIKeys().Create(ctx, gocommerce.APIKeyInput{Name: "Agent robot", Role: gocommerce.RoleOwner}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec := gctest.SessionRequest(t, h.app, secret, http.MethodPost, path, nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "person") {
		t.Errorf("an API key approving = %d: %s", rec.Code, rec.Body)
	}
	if rec := gctest.AdminRequest(t, h.app, http.MethodPost, path, nil); rec.Code != http.StatusForbidden {
		t.Errorf("the static token approving = %d", rec.Code)
	}
	if rec := gctest.AdminRequest(t, h.app, http.MethodPost, strings.Replace(path, "approve", "reject", 1), nil); rec.Code != http.StatusForbidden {
		t.Errorf("the static token rejecting = %d", rec.Code)
	}
	// A manager carries ai.approve and not taxes.write.
	manager := gctest.OperatorToken(t, h.app, "manager@example.com", gocommerce.RoleManager)
	if rec := gctest.SessionRequest(t, h.app, manager, http.MethodGet, "/api/admin/x/ai-gateway/approvals", nil); rec.Code != http.StatusOK {
		t.Errorf("a manager reading the queue = %d", rec.Code)
	}
	if rec := gctest.SessionRequest(t, h.app, manager, http.MethodPost, path, nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "taxes.write") {
		t.Errorf("a manager approving a taxes.write action = %d: %s", rec.Code, rec.Body)
	}
	staff := gctest.OperatorToken(t, h.app, "staff@example.com", gocommerce.RoleStaff)
	if rec := gctest.SessionRequest(t, h.app, staff, http.MethodGet, "/api/admin/x/ai-gateway/approvals", nil); rec.Code != http.StatusForbidden {
		t.Errorf("staff reading the queue = %d", rec.Code)
	}
	if h.runs.Load() != 0 {
		t.Error("a refused approval ran the action")
	}
	if got, _ := h.m.Approval(ctx, a.ID); got.Status != ApprovalPending {
		t.Errorf("a refused approval moved the proposal to %s", got.Status)
	}
}

// A proposal past its expiry reads as expired and cannot run.
func TestAnExpiredProposalCannotRun(t *testing.T) {
	h := newApprovals(t, gocommerce.RightCatalogWrite)
	ctx := context.Background()
	a := h.propose(t, ctx, "Too late", "retitle:1")
	if _, err := h.app.DB().ExecContext(ctx, `UPDATE approval_requests SET expires_at = now() - interval '1 minute' WHERE id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	owner := gctest.OperatorToken(t, h.app, "owner@example.com", gocommerce.RoleOwner)

	got := decodeApproval(t, gctest.SessionRequest(t, h.app, owner, http.MethodGet, fmt.Sprintf("/api/admin/x/ai-gateway/approvals/%d", a.ID), nil))
	if got.Status != ApprovalExpired {
		t.Errorf("status = %s, want expired", got.Status)
	}
	rec := gctest.SessionRequest(t, h.app, owner, http.MethodPost, fmt.Sprintf("/api/admin/x/ai-gateway/approvals/%d/approve", a.ID), nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "expired") {
		t.Errorf("approving an expired proposal = %d: %s", rec.Code, rec.Body)
	}
	if h.runs.Load() != 0 {
		t.Error("an expired proposal ran")
	}
	var list []Approval
	gctest.DecodeData(t, gctest.SessionRequest(t, h.app, owner, http.MethodGet, "/api/admin/x/ai-gateway/approvals?status=pending", nil), &list)
	if len(list) != 0 {
		t.Errorf("the pending queue lists an expired proposal: %+v", list)
	}
	gctest.DecodeData(t, gctest.SessionRequest(t, h.app, owner, http.MethodGet, "/api/admin/x/ai-gateway/approvals?status=expired", nil), &list)
	if len(list) != 1 || list[0].ID != a.ID {
		t.Errorf("the expired list = %+v", list)
	}
}

// Rejecting closes a proposal without running it, once.
func TestRejectingClosesAProposalWithoutRunningIt(t *testing.T) {
	h := newApprovals(t, gocommerce.RightCatalogWrite)
	a := h.propose(t, context.Background(), "No thanks", "retitle:1")
	owner := gctest.OperatorToken(t, h.app, "owner@example.com", gocommerce.RoleOwner)
	path := fmt.Sprintf("/api/admin/x/ai-gateway/approvals/%d/", a.ID)

	got := decodeApproval(t, gctest.SessionRequest(t, h.app, owner, http.MethodPost, path+"reject", nil))
	if got.Status != ApprovalRejected || got.DecidedByEmail != "owner@example.com" {
		t.Errorf("rejected = %+v", got)
	}
	if rec := gctest.SessionRequest(t, h.app, owner, http.MethodPost, path+"approve", nil); rec.Code != http.StatusConflict {
		t.Errorf("approving a rejected proposal = %d", rec.Code)
	}
	if h.runs.Load() != 0 {
		t.Error("a rejected proposal ran")
	}
}

// An action that fails leaves the proposal failed with the reason, not
// pending for another try against a store that has moved on.
func TestAFailedActionIsRecorded(t *testing.T) {
	h := newApprovals(t, gocommerce.RightCatalogWrite)
	ctx := context.Background()
	a := h.propose(t, ctx, "Fine", "retitle:1")
	empty := ""
	h.m.mu.Lock()
	act := h.m.actions["test.retitle"]
	execute := act.Execute
	act.Execute = func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
		var r retitle
		_ = json.Unmarshal(args, &r)
		_, err := h.app.Products().UpdateProduct(ctx, r.ProductID, gocommerce.ProductPatch{Title: &empty})
		return nil, err
	}
	h.m.mu.Unlock()
	defer func() { act.Execute = execute }()

	owner := gctest.OperatorToken(t, h.app, "owner@example.com", gocommerce.RoleOwner)
	got := decodeApproval(t, gctest.SessionRequest(t, h.app, owner, http.MethodPost, fmt.Sprintf("/api/admin/x/ai-gateway/approvals/%d/approve", a.ID), h.seen(t, a.ID)))
	if got.Status != ApprovalFailed || got.Error == "" {
		t.Errorf("approval = %+v, want failed with the reason", got)
	}
}

// Two stores: neither lists, reads, approves or rejects the other's
// proposals, and one key in two stores files two proposals.
func TestTwoStoresNeverSeeEachOthersProposals(t *testing.T) {
	h := newApprovals(t, gocommerce.RightCatalogWrite)
	second, err := h.app.Stores().Create(context.Background(), "Second", "second")
	if err != nil {
		t.Fatal(err)
	}
	a := h.propose(t, context.Background(), "Store A's", "same-key")
	b := gocommerce.WithStore(context.Background(), second.ID)

	if n := countRows(t, h.app, b, "approval_requests"); n != 0 {
		t.Errorf("store B sees %d proposals", n)
	}
	if _, err := h.m.Approval(b, a.ID); err == nil {
		t.Error("store B read store A's proposal")
	}
	for _, path := range []string{fmt.Sprintf("approvals/%d", a.ID)} {
		if r := do(t, h.app, http.MethodGet, "/api/admin/x/ai-gateway/"+path, second.ID); r.code != http.StatusNotFound {
			t.Errorf("B: GET %s = %d", path, r.code)
		}
	}
	var list []Approval
	do(t, h.app, http.MethodGet, "/api/admin/x/ai-gateway/approvals", second.ID).decode(t, &list)
	if len(list) != 0 {
		t.Errorf("B's queue = %+v", list)
	}
	// A person of store B, owner there, cannot decide A's proposal.
	if _, err := h.app.Superusers().Create(b, "owner-b@example.com", gctest.OperatorPassword, gocommerce.RoleOwner); err != nil {
		t.Fatal(err)
	}
	_, session, err := h.app.Superusers().Authenticate(b, "owner-b@example.com", gctest.OperatorPassword, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	ownerB := session.Token
	for _, verb := range []string{"approve", "reject"} {
		req := fmt.Sprintf("/api/admin/x/ai-gateway/approvals/%d/%s", a.ID, verb)
		rec := gctest.SessionRequest(t, h.app, ownerB, http.MethodPost, req+"?store="+second.ID, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("B's owner: %s = %d: %s", verb, rec.Code, rec.Body)
		}
	}
	if got, _ := h.m.Approval(context.Background(), a.ID); got.Status != ApprovalPending || h.runs.Load() != 0 {
		t.Errorf("A's proposal = %s after B's attempts, %d runs", got.Status, h.runs.Load())
	}
	// The same key in B is B's own proposal. B has no product, so B's
	// proposal names one of its own.
	price := int64(500)
	productB, err := h.app.Products().CreateProduct(b, gocommerce.ProductInput{Title: "B's widget", Status: gocommerce.ProductActive, SKU: "B-1", PriceMinor: &price})
	if err != nil {
		t.Fatal(err)
	}
	pb, err := h.m.Propose(b, "test.retitle", retitle{ProductID: productB.ID, Title: "B"}, "same-key")
	if err != nil || pb.ID == a.ID {
		t.Errorf("B's proposal under A's key = %+v, %v", pb, err)
	}
}

func TestActionsAreRegisteredOnlyWhenTheyCanBeKept(t *testing.T) {
	m := New(Config{})
	runs := &atomic.Int32{}
	good := retitleAction(func() *gocommerce.App { return nil }, runs, gocommerce.RightCatalogWrite)
	if err := m.RegisterAction(good); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterAction(good); err == nil {
		t.Error("an action registered twice")
	}
	for name, change := range map[string]func(*Action){
		"no kind":    func(a *Action) { a.Kind = "" },
		"no right":   func(a *Action) { a.Right = "" },
		"no preview": func(a *Action) { a.Preview = nil },
		"no execute": func(a *Action) { a.Execute = nil },
	} {
		a := good
		a.Kind = "test." + strings.ReplaceAll(name, " ", "-")
		change(&a)
		if err := m.RegisterAction(a); err == nil {
			t.Errorf("%s: registered", name)
		}
	}
}

// replaceExecute swaps the demonstration action's Execute for the test.
func (h *approvalHarness) replaceExecute(t *testing.T, execute func(ctx context.Context, args json.RawMessage) (json.RawMessage, error)) {
	t.Helper()
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	h.m.actions["test.retitle"].Execute = execute
}

func (h *approvalHarness) approveAsOwner(t *testing.T, id int64) (int, Approval) {
	t.Helper()
	owner := gctest.OperatorToken(t, h.app, "owner@example.com", gocommerce.RoleOwner)
	rec := gctest.SessionRequest(t, h.app, owner, http.MethodPost, fmt.Sprintf("/api/admin/x/ai-gateway/approvals/%d/approve", id), h.seen(t, id))
	var env struct {
		Data Approval `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec.Code, env.Data
}

// An approved action that runs out its time ends failed with the reason. The
// outcome is written on a context of its own: the one the action exhausted
// cannot write anything, and the proposal used to stay "approved" for ever.
func TestAnApprovedActionThatRunsOutOfTimeEndsFailed(t *testing.T) {
	h := newApprovals(t, gocommerce.RightCatalogWrite)
	h.m.executeTimeout = 200 * time.Millisecond
	h.replaceExecute(t, func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	a := h.propose(t, context.Background(), "Slow", "retitle:1")

	code, got := h.approveAsOwner(t, a.ID)
	if code != http.StatusOK || got.Status != ApprovalFailed || !strings.Contains(got.Error, "deadline") {
		t.Fatalf("approve = %d %+v, want 200 and failed with the deadline named", code, got)
	}
	if now, _ := h.m.Approval(context.Background(), a.ID); now.Status != ApprovalFailed {
		t.Errorf("the proposal is %s", now.Status)
	}
}

// An approved action that panics ends failed with the panic as its reason,
// and the store keeps serving.
func TestAnApprovedActionThatPanicsEndsFailed(t *testing.T) {
	h := newApprovals(t, gocommerce.RightCatalogWrite)
	h.replaceExecute(t, func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
		panic("the action fell over")
	})
	a := h.propose(t, context.Background(), "Boom", "retitle:1")

	code, got := h.approveAsOwner(t, a.ID)
	if code != http.StatusOK || got.Status != ApprovalFailed || !strings.Contains(got.Error, "the action fell over") {
		t.Fatalf("approve = %d %+v, want 200 and failed with the panic named", code, got)
	}
}

// The person approves what they were shown. If the store moved between
// opening the proposal and pressing Approve, nothing runs: the answer is 409
// with the preview as it is now, for them to review again.
func TestApprovingAPreviewThatHasSinceChangedRunsNothing(t *testing.T) {
	h := newApprovals(t, gocommerce.RightCatalogWrite)
	ctx := context.Background()
	a := h.propose(t, ctx, "Approved title", "retitle:1")
	owner := gctest.OperatorToken(t, h.app, "owner@example.com", gocommerce.RoleOwner)
	path := fmt.Sprintf("/api/admin/x/ai-gateway/approvals/%d/approve", a.ID)

	shown := h.seen(t, a.ID)
	moved := "Renamed while the drawer was open"
	if _, err := h.app.Products().UpdateProduct(ctx, h.product.ID, gocommerce.ProductPatch{Title: &moved}); err != nil {
		t.Fatal(err)
	}
	rec := gctest.SessionRequest(t, h.app, owner, http.MethodPost, path, shown)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "changed while you were looking") {
		t.Fatalf("approve on a stale preview = %d: %s", rec.Code, rec.Body)
	}
	var env struct {
		Error struct {
			Details struct {
				Preview     map[string]string `json:"preview"`
				PreviewHash string            `json:"preview_hash"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Error.Details.Preview["from"] != moved || env.Error.Details.PreviewHash == "" || env.Error.Details.PreviewHash == shown["preview_hash"] {
		t.Errorf("409 details = %+v, want the preview as it is now and its new hash", env.Error.Details)
	}
	if h.runs.Load() != 0 {
		t.Error("an approval of a stale preview ran the action")
	}
	if got, _ := h.m.Approval(ctx, a.ID); got.Status != ApprovalPending {
		t.Errorf("the proposal is %s, want still pending", got.Status)
	}

	rec = gctest.SessionRequest(t, h.app, owner, http.MethodPost, path, map[string]string{"preview_hash": env.Error.Details.PreviewHash})
	if rec.Code != http.StatusOK || decodeApproval(t, rec).Status != ApprovalExecuted {
		t.Fatalf("approve on the reviewed preview = %d: %s", rec.Code, rec.Body)
	}
}

// An approval that says nothing about what was reviewed is refused.
func TestApprovingNeedsThePreviewThatWasReviewed(t *testing.T) {
	h := newApprovals(t, gocommerce.RightCatalogWrite)
	a := h.propose(t, context.Background(), "Unseen", "retitle:1")
	owner := gctest.OperatorToken(t, h.app, "owner@example.com", gocommerce.RoleOwner)
	rec := gctest.SessionRequest(t, h.app, owner, http.MethodPost, fmt.Sprintf("/api/admin/x/ai-gateway/approvals/%d/approve", a.ID), nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "preview_hash") {
		t.Fatalf("approve without the reviewed preview = %d: %s", rec.Code, rec.Body)
	}
	if h.runs.Load() != 0 {
		t.Error("it ran")
	}
}

// probe calls the gateway's Go API from a route of its own, gated on
// catalog.write alone, the way another module might.
type probe struct{ m *Module }

func (p probe) Name() string                       { return "ai-probe" }
func (p probe) Migrations() []gocommerce.Migration { return nil }
func (p probe) Register(app *gocommerce.App) error {
	app.HandleAdminFunc("POST /api/admin/x/ai-probe/approve/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		var in struct {
			PreviewHash string `json:"preview_hash"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		a, err := p.m.Approve(r.Context(), id, in.PreviewHash)
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		gocommerce.Respond(w, http.StatusOK, a)
	}, gocommerce.RightCatalogWrite)
	return nil
}

// Approve checks ai.approve itself, not only on its own route: a module
// calling it from a route of its own cannot approve for somebody the store
// has not let decide proposals.
func TestTheGoAPIChecksTheApproveRightItself(t *testing.T) {
	runs := &atomic.Int32{}
	var app *gocommerce.App
	m := New(Config{})
	if err := m.RegisterAction(retitleAction(func() *gocommerce.App { return app }, runs, gocommerce.RightCatalogWrite)); err != nil {
		t.Fatal(err)
	}
	app = gctest.New(t, m, probe{m})
	ctx := context.Background()
	product := gctest.CreateProduct(t, app, "WIDGET-1", 1999, 5)
	a, err := m.Propose(ctx, "test.retitle", retitle{ProductID: product.ID, Title: "x"}, "k")
	if err != nil {
		t.Fatal(err)
	}
	// A manager whose store took ai.approve away, and left catalog.write.
	rights, err := app.Roles().Of(ctx, gocommerce.RoleManager)
	if err != nil {
		t.Fatal(err)
	}
	kept := slices.DeleteFunc(slices.Clone(rights), func(r gocommerce.Right) bool { return r == RightApprove })
	if _, err := app.Roles().Set(ctx, gocommerce.RoleManager, kept, nil); err != nil {
		t.Fatal(err)
	}
	manager := gctest.OperatorToken(t, app, "manager@example.com", gocommerce.RoleManager)
	rec := gctest.SessionRequest(t, app, manager, http.MethodPost, fmt.Sprintf("/api/admin/x/ai-probe/approve/%d", a.ID),
		map[string]string{"preview_hash": a.PreviewHash})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "ai.approve") {
		t.Fatalf("approving through another route without ai.approve = %d: %s", rec.Code, rec.Body)
	}
	if runs.Load() != 0 {
		t.Error("it ran")
	}
}
