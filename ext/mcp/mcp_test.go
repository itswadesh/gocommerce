package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// rpc sends one JSON-RPC call to the MCP endpoint and returns the result.
func rpc(t *testing.T, app *gocommerce.App, method string, params any) map[string]any {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/mcp", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status = %d: %s", method, rec.Code, rec.Body)
	}
	var resp struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("%s: decode: %v (body %s)", method, err, rec.Body)
	}
	if resp.Error != nil {
		t.Fatalf("%s: rpc error %d: %s", method, resp.Error.Code, resp.Error.Message)
	}
	return resp.Result
}

// callTool runs a tool and returns its text content, plus whether the tool
// reported a domain error.
func callTool(t *testing.T, app *gocommerce.App, name string, args map[string]any) (string, bool) {
	t.Helper()
	result := rpc(t, app, "tools/call", map[string]any{"name": name, "arguments": args})

	isError, _ := result["isError"].(bool)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		return "", isError
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return text, isError
}

// sessionTool calls a tool as a signed-in operator rather than with the static
// admin token, which is the only way to see a right enforced at all: a static
// token carries every right by design.
//
// It returns the JSON-RPC error rather than failing on it, because a refusal is
// what several of these tests are looking for.
func sessionTool(t *testing.T, app *gocommerce.App, token, name string, args map[string]any) (map[string]any, *rpcError) {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args}}
	rec := gctest.SessionRequest(t, app, token, http.MethodPost, "/api/admin/x/mcp", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status = %d: %s", name, rec.Code, rec.Body)
	}
	var resp struct {
		Result map[string]any `json:"result"`
		Error  *rpcError      `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("%s: decode: %v (body %s)", name, err, rec.Body)
	}
	return resp.Result, resp.Error
}

func TestEndpointRequiresAdminToken(t *testing.T) {
	app := gctest.New(t, New(Config{}))

	// The module writes no authentication of its own: mounting through
	// HandleAdmin is what protects it.
	rec := gctest.Request(t, app, http.MethodPost, "/api/admin/x/mcp",
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, want 401", rec.Code)
	}
}

func TestInitializeAndToolsList(t *testing.T) {
	app := gctest.New(t, New(Config{ServerName: "example-store"}))

	init := rpc(t, app, "initialize", map[string]any{"protocolVersion": protocolVersion})
	if got := init["protocolVersion"]; got != protocolVersion {
		t.Errorf("protocolVersion = %v, want %s", got, protocolVersion)
	}
	server, _ := init["serverInfo"].(map[string]any)
	if server["name"] != "example-store" {
		t.Errorf("server name = %v, want example-store", server["name"])
	}

	list := rpc(t, app, "tools/list", nil)
	tools, _ := list["tools"].([]any)
	names := map[string]bool{}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		name, _ := tool["name"].(string)
		names[name] = true
		if _, ok := tool["inputSchema"]; !ok {
			t.Errorf("tool %q has no inputSchema; an agent cannot call it safely", name)
		}
	}
	for _, want := range []string{
		"store_info", "list_products", "get_product", "list_orders", "get_order",
		"list_low_stock_variants", "update_variant_inventory",
		"mark_order_paid", "cancel_order", "create_fulfillment", "mark_order_delivered",
	} {
		if !names[want] {
			t.Errorf("tool %q is missing", want)
		}
	}
}

func TestReadOnlyWithholdsMutatingTools(t *testing.T) {
	app := gctest.New(t, New(Config{ReadOnly: true}))

	list := rpc(t, app, "tools/list", nil)
	tools, _ := list["tools"].([]any)
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		name, _ := tool["name"].(string)
		switch name {
		case "cancel_order", "mark_order_paid", "update_variant_inventory", "create_fulfillment":
			t.Errorf("read-only mode still offers the mutating tool %q", name)
		}
	}
}

// TestScriptedAgentFlow is the milestone's proof: an agent runs a store
// through the same domain services a person would use, and every change it
// makes is recorded.
func TestScriptedAgentFlow(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ctx := context.Background()

	// A shopper places an order the ordinary way.
	result := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)

	// The agent orients itself.
	info, isErr := callTool(t, app, "store_info", nil)
	if isErr {
		t.Fatalf("store_info failed: %s", info)
	}
	if !strings.Contains(info, `"currency": "USD"`) {
		t.Errorf("store_info should report the currency: %s", info)
	}

	// It finds the order.
	orders, isErr := callTool(t, app, "list_orders", map[string]any{"status": "confirmed"})
	if isErr {
		t.Fatalf("list_orders failed: %s", orders)
	}
	if !strings.Contains(orders, result.Order.Number) {
		t.Errorf("list_orders should include %s: %s", result.Order.Number, orders)
	}

	// It settles, ships and delivers it.
	if out, isErr := callTool(t, app, "mark_order_paid",
		map[string]any{"order_id": result.Order.ID, "reference": "collected in cash"}); isErr {
		t.Fatalf("mark_order_paid failed: %s", out)
	}
	if out, isErr := callTool(t, app, "create_fulfillment",
		map[string]any{"order_id": result.Order.ID, "tracking": "AGENT-1"}); isErr {
		t.Fatalf("create_fulfillment failed: %s", out)
	}
	if out, isErr := callTool(t, app, "mark_order_delivered",
		map[string]any{"order_id": result.Order.ID}); isErr {
		t.Fatalf("mark_order_delivered failed: %s", out)
	}

	order, err := app.Order().Get(ctx, result.Order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if order.Status != gocommerce.OrderDelivered || order.PaymentStatus != gocommerce.PaymentPaid {
		t.Errorf("order = (%s, %s), want (delivered, paid)", order.Status, order.PaymentStatus)
	}

	// It restocks what is running low.
	variant, err := app.Products().GetVariantBySKU(ctx, "GCTEST-cod")
	if err != nil {
		t.Fatalf("get variant: %v", err)
	}
	if out, isErr := callTool(t, app, "update_variant_inventory",
		map[string]any{"variant_id": variant.ID, "adjust": 40}); isErr {
		t.Fatalf("update_variant_inventory failed: %s", out)
	}
	restocked, err := app.Products().GetVariantBySKU(ctx, "GCTEST-cod")
	if err != nil {
		t.Fatalf("get variant: %v", err)
	}
	if restocked.StockOnHand != variant.StockOnHand+40 {
		t.Errorf("stock = %d, want %d", restocked.StockOnHand, variant.StockOnHand+40)
	}

	// Everything the agent changed is on the record. When software can cancel
	// orders on its own, "who did that" has to be answerable.
	var audited int
	if err := app.DB().QueryRowContext(ctx, `SELECT count(*) FROM mcp_audit`).Scan(&audited); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if audited != 4 {
		t.Errorf("audit rows = %d, want 4 (one per mutating call)", audited)
	}

	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/mcp/audit", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit endpoint status = %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "mark_order_paid") {
		t.Errorf("audit log should name the tools called: %s", rec.Body)
	}
}

// TestToolErrorsReachTheAgent: a refused domain operation is an answer the
// agent can act on, not a transport failure.
func TestToolErrorsReachTheAgent(t *testing.T) {
	app := gctest.New(t, New(Config{}))

	result := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	if _, isErr := callTool(t, app, "create_fulfillment",
		map[string]any{"order_id": result.Order.ID, "tracking": "T-1"}); isErr {
		t.Fatal("shipping a confirmed order should succeed")
	}

	text, isErr := callTool(t, app, "cancel_order",
		map[string]any{"order_id": result.Order.ID, "reason": "agent changed its mind"})
	if !isErr {
		t.Fatal("cancelling a shipped order should be refused")
	}
	if !strings.Contains(text, "already shipped") {
		t.Errorf("the agent should be told why: %s", text)
	}

	// The refusal is audited too — an attempt is as interesting as a success.
	var outcome string
	if err := app.DB().QueryRowContext(context.Background(),
		`SELECT outcome FROM mcp_audit WHERE tool = 'cancel_order' ORDER BY id DESC LIMIT 1`).
		Scan(&outcome); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if outcome != "error" {
		t.Errorf("audited outcome = %q, want error", outcome)
	}
}

func TestUnknownMethodAndTool(t *testing.T) {
	app := gctest.New(t, New(Config{}))

	rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/mcp",
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "does/not/exist"})
	var resp struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Errorf("error = %+v, want method-not-found", resp.Error)
	}
}

// TestAgentRoutesRequireStoreOperate: a second operating surface onto the store
// is what store.operate names, and an operator reaching it from a browser is
// now checked.
//
// The static admin token is unaffected, which is the half that matters for
// compatibility: every documented agent integration keeps working.
func TestAgentRoutesRequireStoreOperate(t *testing.T) {
	app := gctest.New(t, New(Config{}))

	// Nobody holds store.operate by default except an owner: it is absent from
	// both configurable role sets on purpose.
	staff := gctest.OperatorToken(t, app, "staff@example.com", gocommerce.RoleStaff)
	manager := gctest.OperatorToken(t, app, "manager@example.com", gocommerce.RoleManager)
	owner := gctest.OperatorToken(t, app, "owner@example.com", gocommerce.RoleOwner)

	list := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}
	for _, who := range []struct {
		name, token string
	}{{"staff", staff}, {"manager", manager}} {
		rec := gctest.SessionRequest(t, app, who.token, http.MethodPost, "/api/admin/x/mcp", list)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s calling the agent endpoint = %d, want 403: %s", who.name, rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), "agent.") {
			t.Errorf("%s: the refusal does not name the missing right: %s", who.name, rec.Body)
		}
		rec = gctest.SessionRequest(t, app, who.token, http.MethodGet, "/api/admin/x/mcp/audit", nil)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s reading the audit = %d, want 403: %s", who.name, rec.Code, rec.Body)
		}
	}

	if rec := gctest.SessionRequest(t, app, owner, http.MethodPost, "/api/admin/x/mcp", list); rec.Code != http.StatusOK {
		t.Errorf("owner calling the agent endpoint = %d, want 200: %s", rec.Code, rec.Body)
	}
	// A static admin token carries every right, so an existing integration is
	// not broken by the new gate.
	if rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/mcp", list); rec.Code != http.StatusOK {
		t.Errorf("the static admin token = %d, want 200: %s", rec.Code, rec.Body)
	}
}

// TestToolsListSaysWhichToolsAreReadOnly: the server knows which tools change
// state and says so, rather than leaving a client to guess from a hardcoded
// list of builtin names — which would be wrong for anything Config.Tools
// contributed.
func TestToolsListSaysWhichToolsAreReadOnly(t *testing.T) {
	readOnlyHints := func(app *gocommerce.App) map[string]bool {
		t.Helper()
		result := rpc(t, app, "tools/list", nil)
		tools, _ := result["tools"].([]any)
		if len(tools) == 0 {
			t.Fatal("tools/list returned nothing")
		}
		out := map[string]bool{}
		for _, raw := range tools {
			tool, _ := raw.(map[string]any)
			name, _ := tool["name"].(string)
			annotations, ok := tool["annotations"].(map[string]any)
			if !ok {
				t.Fatalf("tool %q carries no annotations", name)
			}
			hint, ok := annotations["readOnlyHint"].(bool)
			if !ok {
				t.Fatalf("tool %q carries no readOnlyHint", name)
			}
			out[name] = hint
		}
		return out
	}

	hints := readOnlyHints(gctest.New(t, New(Config{})))
	for _, mutating := range []string{
		"update_variant_inventory", "mark_order_paid", "cancel_order",
		"create_fulfillment", "mark_order_delivered",
	} {
		hint, present := hints[mutating]
		if !present {
			t.Errorf("%s is not listed at all", mutating)
			continue
		}
		if hint {
			t.Errorf("%s reports readOnlyHint true, and it settles or ships an order", mutating)
		}
	}
	for _, reading := range []string{"store_info", "list_products", "list_orders", "get_order"} {
		if hint, present := hints[reading]; present && !hint {
			t.Errorf("%s reports readOnlyHint false, and it only reads", reading)
		}
	}

	// A read-only store withholds the mutating tools entirely, so nothing it
	// lists can report false.
	for name, hint := range readOnlyHints(gctest.New(t, New(Config{ReadOnly: true}))) {
		if !hint {
			t.Errorf("a read-only store lists %s as mutating", name)
		}
	}
}

// TestAuditCalledAtIsATimestamp: the column was scanned into an `any` and
// printed with fmt.Sprint, which produces Go's own layout — "2026-09-09
// 12:00:00 +0000 UTC" — and no client date parser reads that, so every
// timestamp this route served was unusable to the reader it was for.
func TestAuditCalledAtIsATimestamp(t *testing.T) {
	app := gctest.New(t, New(Config{}))

	result := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	if out, isErr := callTool(t, app, "mark_order_paid",
		map[string]any{"order_id": result.Order.ID, "reference": "cash"}); isErr {
		t.Fatalf("mark_order_paid failed: %s", out)
	}

	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/mcp/audit", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit status = %d: %s", rec.Code, rec.Body)
	}
	var entries []struct {
		Tool     string    `json:"tool"`
		CalledAt time.Time `json:"called_at"`
	}
	gctest.DecodeData(t, rec, &entries)
	if len(entries) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(entries))
	}
	if entries[0].CalledAt.IsZero() {
		t.Errorf("called_at did not decode as a timestamp: %s", rec.Body)
	}
}

// TestToolRightsAreCheckedPerCall: store.operate opens the door, and the tool
// decides what may come through it.
//
// This is the hole the mount-time right leaves open on its own: requireRights
// runs once before the body is parsed, so without a check inside callTool a
// session operator holding store.operate could settle payments, ship, cancel
// and move stock without orders.write, orders.fulfill or inventory.write, and
// read every order without orders.read.
func TestToolRightsAreCheckedPerCall(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ctx := context.Background()

	// Staff re-cut to reach the endpoint at all, and deliberately without
	// inventory.write: store.operate is what the mount asks for, and the
	// question this test asks is what the mount does NOT decide.
	if _, err := app.Roles().Set(ctx, gocommerce.RoleStaff, []gocommerce.Right{
		gocommerce.RightCatalogRead, gocommerce.RightOrdersRead,
		gocommerce.RightOrdersWrite, "agent.dispatch",
	}, nil); err != nil {
		t.Fatalf("re-cut staff: %v", err)
	}
	staff := gctest.OperatorToken(t, app, "staff@example.com", gocommerce.RoleStaff)
	owner := gctest.OperatorToken(t, app, "owner@example.com", gocommerce.RoleOwner)

	order := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	variant, err := app.Products().GetVariantBySKU(ctx, "GCTEST-cod")
	if err != nil {
		t.Fatalf("get variant: %v", err)
	}

	// The right it holds: mark_order_paid is orders.write, as it is on
	// POST /api/admin/orders/{id}/pay.
	result, rpcErr := sessionTool(t, app, staff, "mark_order_paid",
		map[string]any{"order_id": order.Order.ID, "reference": "cash"})
	if rpcErr != nil {
		t.Fatalf("staff holds orders.write and was refused: %s", rpcErr.Message)
	}
	if isError, _ := result["isError"].(bool); isError {
		t.Fatalf("mark_order_paid failed: %v", result["content"])
	}

	// The one it does not.
	_, rpcErr = sessionTool(t, app, staff, "update_variant_inventory",
		map[string]any{"variant_id": variant.ID, "adjust": 5})
	if rpcErr == nil {
		t.Fatal("staff without inventory.write moved stock through the agent door")
	}
	if rpcErr.Code != codeInvalidRequest {
		t.Errorf("refusal code = %d, want %d", rpcErr.Code, codeInvalidRequest)
	}
	if !strings.Contains(rpcErr.Message, string(gocommerce.RightInventoryWrite)) {
		t.Errorf("the refusal does not name the missing right: %s", rpcErr.Message)
	}
	// Refused rather than half-done.
	after, err := app.Products().GetVariantBySKU(ctx, "GCTEST-cod")
	if err != nil {
		t.Fatalf("get variant: %v", err)
	}
	if after.StockOnHand != variant.StockOnHand {
		t.Errorf("stock moved to %d from %d on a refused call", after.StockOnHand, variant.StockOnHand)
	}

	// An owner carries every right, and so does the static admin token: a
	// documented agent integration is not narrowed by any of this.
	if _, rpcErr := sessionTool(t, app, owner, "update_variant_inventory",
		map[string]any{"variant_id": variant.ID, "adjust": 5}); rpcErr != nil {
		t.Errorf("owner was refused: %s", rpcErr.Message)
	}
	if _, isErr := callTool(t, app, "update_variant_inventory",
		map[string]any{"variant_id": variant.ID, "adjust": 5}); isErr {
		t.Error("the static admin token was refused")
	}
}

// TestRefusedMutatingCallIsAudited: an attempted privileged call is the thing
// the trail is read for, so it is written even though nothing changed.
func TestRefusedMutatingCallIsAudited(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ctx := context.Background()

	if _, err := app.Roles().Set(ctx, gocommerce.RoleStaff, []gocommerce.Right{
		gocommerce.RightCatalogRead, "agent.dispatch",
	}, nil); err != nil {
		t.Fatalf("re-cut staff: %v", err)
	}
	staff := gctest.OperatorToken(t, app, "staff@example.com", gocommerce.RoleStaff)

	order := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	if _, rpcErr := sessionTool(t, app, staff, "mark_order_paid",
		map[string]any{"order_id": order.Order.ID}); rpcErr == nil {
		t.Fatal("staff without orders.write settled an order")
	}

	var tool, outcome, detail string
	if err := app.DB().QueryRowContext(ctx, `
		SELECT tool, outcome, coalesce(detail, '') FROM mcp_audit ORDER BY id DESC LIMIT 1`).
		Scan(&tool, &outcome, &detail); err != nil {
		t.Fatalf("read the audit: %v", err)
	}
	if tool != "mark_order_paid" || outcome != "error" {
		t.Errorf("audited (%s, %s), want (mark_order_paid, error)", tool, outcome)
	}
	if !strings.Contains(detail, string(gocommerce.RightOrdersWrite)) {
		t.Errorf("the audited detail does not name the missing right: %q", detail)
	}

	// A read refused the same way leaves nothing behind, because a read leaves
	// nothing behind either way: only tools declaring Mutates are audited.
	if _, rpcErr := sessionTool(t, app, staff, "list_orders", nil); rpcErr == nil {
		t.Fatal("staff without orders.read listed orders")
	}
	var rows int
	if err := app.DB().QueryRowContext(ctx, `SELECT count(*) FROM mcp_audit`).Scan(&rows); err != nil {
		t.Fatalf("count the audit: %v", err)
	}
	if rows != 1 {
		t.Errorf("audit rows = %d, want 1 — a refused read is not a mutation", rows)
	}
}

func TestModuleContract(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	gctest.AssertAdminRoutesDeclareRights(t, app, "mcp")
	gctest.AssertSpecCoversModuleRoutes(t, app, "mcp")
}
