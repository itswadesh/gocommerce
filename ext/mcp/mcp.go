// Package mcp exposes a gocommerce store to AI agents over the Model Context
// Protocol.
//
// The agent never gets database access. Every tool calls the same domain
// service a REST request would, so there is one state machine and one place
// where an order becomes paid — whether a human, an application or an agent
// asked for it.
//
//	app, err := gocommerce.New(cfg, mcp.New(mcp.Config{}))
//
// The endpoint is mounted at /api/admin/x/mcp and inherits admin
// authentication, so the store's admin token is the agent's credential. For a
// desktop agent that speaks stdio instead, call mcp.ServeStdio(app, m) from
// main() in place of ListenAndServe.
//
// MCP is JSON-RPC 2.0, which the standard library can speak on its own — so
// this module takes no dependency and lives in ext/ with the others.
package mcp

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/misiki/gocommerce/core"
)

// protocolVersion is the MCP revision this module implements.
const protocolVersion = "2024-11-05"

// Config configures the module.
type Config struct {
	// ServerName is reported to the agent during initialize.
	ServerName string
	// ReadOnly withholds every mutating tool. Worth switching on while you
	// are still deciding how much you trust the agent on the other end.
	ReadOnly bool
	// Tools contributed by other modules, wired explicitly in main():
	//
	//	mcp.New(mcp.Config{Tools: invoices.Tools()})
	//
	// There is no discovery: if you want a module's tools exposed, you say so.
	Tools []Tool
}

// Tool is one callable operation.
type Tool struct {
	Name        string
	Description string
	// InputSchema is a JSON Schema object describing Arguments.
	InputSchema map[string]any
	// Mutates marks a tool that changes state, so ReadOnly can withhold it
	// and the audit log can record it.
	Mutates bool
	// Rights the caller must hold to run this tool. Empty means the route's
	// own gate is the whole check.
	//
	// The route cannot express this. requireRights is an all-of check run once
	// before the body is parsed, and this one route dispatches every tool — so
	// any single mount-time right either locks a read-only agent out or hands
	// a catalog-only role mark_order_paid. The tool is the only place the
	// question can be asked, so it is asked here.
	Rights []gocommerce.Right
	// Call runs the tool and returns whatever should be shown to the agent.
	Call func(ctx context.Context, args json.RawMessage) (any, error)
}

// Module is the MCP server.
type Module struct {
	cfg   Config
	app   *gocommerce.App
	log   *slog.Logger
	db    *sql.DB
	tools map[string]Tool
	order []string
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "mcp" }

// Migrations implements gocommerce.Module.
//
// The audit table is the point: when an agent can cancel an order, "which
// agent did that, and when" has to be answerable.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_audit",
		SQL: `
			CREATE TABLE mcp_audit (
			    id        bigserial   PRIMARY KEY,
			    tool      text        NOT NULL,
			    arguments jsonb       NOT NULL DEFAULT '{}',
			    outcome   text        NOT NULL,
			    detail    text,
			    called_at timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX mcp_audit_called_idx ON mcp_audit (called_at DESC);`,
	}}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.registerRights(app)
	m.app = app
	m.log = app.Log()
	m.db = app.DB()
	if m.cfg.ServerName == "" {
		m.cfg.ServerName = "gocommerce"
	}

	m.tools = map[string]Tool{}
	for _, t := range append(m.builtinTools(), m.cfg.Tools...) {
		if m.cfg.ReadOnly && t.Mutates {
			continue
		}
		if _, exists := m.tools[t.Name]; exists {
			return fmt.Errorf("mcp: two tools are named %q", t.Name)
		}
		m.tools[t.Name] = t
		m.order = append(m.order, t.Name)
	}

	// Mounting through HandleAdmin means the admin token is the agent's
	// credential — this module writes no authentication of its own.
	//
	// A second operating surface onto the store, handed to an agent: that is
	// what store.operate names. It narrows nothing for an agent, since a static
	// admin token carries every right by design, but a session-authenticated
	// operator reaching this from a browser is now checked, which is the case
	// that should be.
	//
	// The mount-time right is not the whole check, and is not meant to be. One
	// route dispatches every tool, from catalog.read to orders.fulfill, and
	// requireRights runs once before the body is parsed — so each tool names
	// the rights it needs and callTool checks them against the operator on the
	// context. Without that second half store.operate would be no weaker than
	// orders.write ∪ orders.fulfill ∪ inventory.write ∪ orders.read for
	// everybody who holds it, which is why D24 gives it to the owner alone.
	app.HandleAdminFunc("POST /api/admin/x/mcp", m.handleHTTP, rightAgentDispatch)
	app.HandleAdminFunc("GET /api/admin/x/mcp/audit", m.handleAudit, rightAgentRead)
	return nil
}

// ------------------------------------------------------------------ JSON-RPC

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Error lets a transport-level failure travel as an ordinary error and still
// be recognised by errors.As, so the dispatcher can render it as a JSON-RPC
// error rather than flattening it into a generic internal one.
func (e *rpcError) Error() string { return e.Message }

// JSON-RPC error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInternalError  = -32603
)

func (m *Module) handleHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", Error: &rpcError{codeParseError, "could not read the request"}})
		return
	}

	// A notification carries no id and expects no reply; 202 is the honest
	// answer to "I have nothing to say back".
	resp, notification := m.dispatch(r.Context(), body)
	if notification {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeRPC(w, resp)
}

func (m *Module) dispatch(ctx context.Context, body []byte) (rpcResponse, bool) {
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return rpcResponse{JSONRPC: "2.0", Error: &rpcError{codeParseError, "malformed JSON-RPC request"}}, false
	}
	isNotification := len(req.ID) == 0

	result, err := m.call(ctx, req)
	if err != nil {
		var rpcErr *rpcError
		if !errors.As(err, &rpcErr) {
			rpcErr = &rpcError{codeInternalError, err.Error()}
		}
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcErr}, isNotification
	}
	if isNotification {
		return rpcResponse{}, true
	}
	return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}, false
}

func (m *Module) call(ctx context.Context, req rpcRequest) (any, error) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo": map[string]any{
				"name":    m.cfg.ServerName,
				"version": gocommerce.Version,
			},
			"instructions": "This is a gocommerce store. Read tools are safe to " +
				"call freely. Tools that change an order or stock take effect " +
				"immediately and are recorded in the store's audit log.",
		}, nil

	case "notifications/initialized", "ping":
		return map[string]any{}, nil

	case "tools/list":
		return map[string]any{"tools": m.describeTools()}, nil

	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &rpcError{codeInvalidRequest, "malformed tool call"}
		}
		return m.callTool(ctx, params.Name, params.Arguments)

	default:
		return nil, &rpcError{codeMethodNotFound, "unknown method " + req.Method}
	}
}

func (m *Module) describeTools() []map[string]any {
	out := make([]map[string]any, 0, len(m.order))
	for _, name := range m.order {
		t := m.tools[name]
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": schema,
			// MCP's own vocabulary for "this tool does not change anything".
			// readOnlyHint is a ToolAnnotation, which arrived in revision
			// 2025-03-26 and so postdates the 2024-11-05 this server
			// advertises — emitting it early is an extension either way, but a
			// forward-compatible one that a newer client reads natively and an
			// older one ignores, which a bare "mutates" member would not be.
			//
			// It is the only thing that tells a tool which reads apart from one
			// that settles a payment. Under Config.ReadOnly the mutating tools
			// are withheld entirely, so every tool left reports true.
			"annotations": map[string]any{"readOnlyHint": !t.Mutates},
		})
	}
	return out
}

// callTool checks the caller's rights, runs the tool and records what
// happened.
//
// A failure is reported as tool content with isError set, not as a JSON-RPC
// error: the agent asked a reasonable question and got a real answer — "that
// order is already shipped" is information it can act on, whereas a transport
// error is not. A refusal for want of a right is the exception, and goes back
// as a JSON-RPC error: no arguments the agent could have sent would have
// worked, so it is the transport saying no rather than an answer.
func (m *Module) callTool(ctx context.Context, name string, args json.RawMessage) (any, error) {
	tool, ok := m.tools[name]
	if !ok {
		return nil, &rpcError{codeMethodNotFound, "unknown tool " + name}
	}

	// A nil superuser is the static admin token or ServeStdio — the credential
	// requireRights already exempts on every core route, because it is the
	// bootstrap credential and has no role whose rights could be read.
	if su := gocommerce.SuperuserFrom(ctx); su != nil {
		for _, right := range tool.Rights {
			if su.Has(right) {
				continue
			}
			refused := fmt.Errorf("your role (%s) does not carry %s, which %s needs",
				su.Role, right, name)
			// Audited although nothing changed: an attempted privileged call
			// is exactly what the trail is read for.
			if tool.Mutates {
				m.audit(ctx, name, args, refused)
			}
			return nil, &rpcError{codeInvalidRequest, refused.Error()}
		}
	}

	result, err := tool.Call(ctx, args)
	if tool.Mutates {
		m.audit(ctx, name, args, err)
	}
	if err != nil {
		return map[string]any{
			"isError": true,
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
		}, nil
	}

	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, &rpcError{codeInternalError, "could not encode the result"}
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(encoded)}},
	}, nil
}

func (m *Module) audit(ctx context.Context, tool string, args json.RawMessage, callErr error) {
	outcome, detail := "ok", ""
	if callErr != nil {
		outcome, detail = "error", callErr.Error()
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO mcp_audit (tool, arguments, outcome, detail)
		VALUES ($1, $2, $3, nullif($4, ''))`, tool, []byte(args), outcome, detail); err != nil {
		m.log.Error("could not write the MCP audit record", "tool", tool, "error", err)
	}
}

func (m *Module) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var total int
	if err := m.db.QueryRowContext(r.Context(), `SELECT count(*) FROM mcp_audit`).Scan(&total); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	rows, err := m.db.QueryContext(r.Context(), `
		SELECT id, tool, arguments, outcome, coalesce(detail, ''), called_at
		FROM mcp_audit ORDER BY id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	defer rows.Close()

	type entry struct {
		ID        int64           `json:"id"`
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
		Outcome   string          `json:"outcome"`
		Detail    string          `json:"detail,omitempty"`
		// time.Time, not string. Scanning the column into an `any` and running
		// it through fmt.Sprint produced Go's own layout — "2026-09-09
		// 12:00:00 +0000 UTC" — which is neither RFC 3339 nor anything a
		// client's date parser accepts, so every timestamp this route has ever
		// served was unreadable to the reader it was for.
		CalledAt time.Time `json:"called_at"`
	}
	list := []entry{}
	for rows.Next() {
		var e entry
		var raw []byte
		if err := rows.Scan(&e.ID, &e.Tool, &raw, &e.Outcome, &e.Detail, &e.CalledAt); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		e.Arguments = raw
		list = append(list, e)
	}
	gocommerce.RespondList(w, list, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

func writeRPC(w http.ResponseWriter, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// ------------------------------------------------------------------- stdio

// ServeStdio runs the MCP server over stdin and stdout, for a desktop agent
// that launches the store as a subprocess. Call it from main() in place of
// ListenAndServe.
//
// A module cannot add a run mode to the engine on its own, and should not be
// able to: which mode a binary runs in is the application author's decision,
// made visibly in main().
func ServeStdio(app *gocommerce.App, m *Module) error {
	if m.app == nil {
		return errors.New("mcp: the module must be registered with the app before serving stdio")
	}
	ctx := context.Background()
	reader := bufio.NewScanner(os.Stdin)
	reader.Buffer(make([]byte, 0, 64*1024), 1<<20)
	writer := json.NewEncoder(os.Stdout)

	for reader.Scan() {
		line := strings.TrimSpace(reader.Text())
		if line == "" {
			continue
		}
		resp, notification := m.dispatch(ctx, []byte(line))
		if notification {
			continue
		}
		if err := writer.Encode(resp); err != nil {
			return fmt.Errorf("mcp: write response: %w", err)
		}
	}
	return reader.Err()
}

// The rights this module's surface is gated on.
//
// Both were store.operate, and the dispatch endpoint is why that was
// uncomfortable: it hands an agent the whole domain-tool surface, which is a
// larger thing than draining an outbox. Reading what the agent did is not, so
// the two are separate — and both default to nobody, exactly as store.operate
// did, so this changes nothing about who holds them today.
const (
	rightAgentRead     gocommerce.Right = "agent.read"     // was store.operate
	rightAgentDispatch gocommerce.Right = "agent.dispatch" // was store.operate
)

func (m *Module) registerRights(app *gocommerce.App) {
	app.RegisterRight(gocommerce.RightSpec{
		Right: rightAgentRead,
		Label: "See what the agent did",
		Scope: "Every tool call an agent made, and what it changed",
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right: rightAgentDispatch,
		Label: "Let an agent act",
		Scope: "The dispatch endpoint — the whole domain-tool surface",
	})
}
