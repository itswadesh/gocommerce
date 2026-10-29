package aigateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// newGateway boots an engine with the gateway, its provider calls going to a
// stand-in, and the given tasks registered.
func newGateway(t *testing.T, handler http.HandlerFunc, tasks ...Task) (*gocommerce.App, *Module, *standIn) {
	t.Helper()
	s := newStandIn(t, handler)
	m := New(Config{Client: s.client(), CallTimeout: 2 * time.Second, PollInterval: time.Hour})
	for _, task := range tasks {
		if err := m.RegisterTask(task); err != nil {
			t.Fatalf("register %s: %v", task.Name, err)
		}
	}
	app := gctest.New(t, m)
	return app, m, s
}

// Prices in minor units per million tokens, as an owner types them: the
// small model at $1 in and $5 out, cache reads at 10c and writes at $1.25;
// the strong one at $3 and $15 with no cache prices of its own.
var standardSettings = map[string]any{
	"api_key":                 "sk-ant-test-key",
	"monthly_cap":             5000,
	"small_price_input":       100,
	"small_price_output":      500,
	"small_price_cache_read":  10,
	"small_price_cache_write": 125,
	"strong_price_input":      300,
	"strong_price_output":     1500,
}

// configure switches the gateway on for ctx's store with the standard
// settings, changed by extra; a nil in extra clears that setting.
func configure(t *testing.T, app *gocommerce.App, ctx context.Context, extra map[string]any) {
	t.Helper()
	settings := map[string]any{}
	for k, v := range standardSettings {
		settings[k] = v
	}
	for k, v := range extra {
		settings[k] = v
	}
	on := true
	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: settings}); err != nil {
		t.Fatalf("configure the gateway: %v", err)
	}
}

// answer is an Anthropic reply in the recorded shape: the structured output
// as one text block, and usage.
func answer(model, input string, u Usage) string {
	text, _ := json.Marshal(input)
	return fmt.Sprintf(`{
  "id": "msg_test", "type": "message", "role": "assistant", "model": %q,
  "content": [{"type": "text", "text": %s}],
  "stop_reason": "end_turn", "stop_sequence": null,
  "usage": {"input_tokens": %d, "cache_creation_input_tokens": %d, "cache_read_input_tokens": %d, "output_tokens": %d, "service_tier": "standard"}
}`, model, text, u.InputTokens, u.CacheWriteTokens, u.CacheReadTokens, u.OutputTokens)
}

// byModel answers each model with its own reply: the ladder's two rungs are
// told apart by what the request asked for.
func byModel(replies map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		reply, ok := replies[body.Model]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"type":"error","error":{"type":"not_found_error","message":"model: %s"}}`, body.Model)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}
}

const (
	goodAnswer = `{"title": "Steel widget, two-pack", "tags": ["widget", "steel"]}`
	// No tags: the schema requires them.
	badAnswer = `{"title": "Steel widget"}`
	smallID   = "claude-haiku-5-5"
	strongID  = "claude-sonnet-5-5"
)

var smallUsage = Usage{InputTokens: 412, OutputTokens: 187, CacheReadTokens: 0, CacheWriteTokens: 1536}

type listing struct {
	Title string `json:"title"`
	Notes string `json:"notes,omitempty"`
}

// rewriteTask is a task in the shape a real one has: catalogue data in a
// fence, a schema, a ladder and a check.
func rewriteTask() Task {
	return Task{
		Name:            "test.rewrite",
		Version:         1,
		System:          "Rewrite the listing for the store's catalogue.",
		Schema:          testSchema,
		MaxInputTokens:  4000,
		MaxOutputTokens: 1000,
		Ladder:          []Tier{TierSmall, TierStrong},
		Build: func(ctx context.Context, input json.RawMessage, p Policy) (Request, []SourceRef, error) {
			var in listing
			if err := json.Unmarshal(input, &in); err != nil {
				return Request{}, nil, err
			}
			return Request{Messages: []Message{{
				Role: "user", Text: "Rewrite this listing.",
				Data: []Data{{Class: DataCatalogue, Label: "listing", Value: in}},
			}}}, []SourceRef{{Kind: "product", ID: "42"}}, nil
		},
		Check: func(ctx context.Context, out json.RawMessage) (int, error) {
			var a struct {
				Title string   `json:"title"`
				Tags  []string `json:"tags"`
			}
			if err := json.Unmarshal(out, &a); err != nil {
				return 0, err
			}
			if strings.TrimSpace(a.Title) == "" {
				return 0, errors.New("the title is empty")
			}
			return 90, nil
		},
	}
}

func mustExecution(t *testing.T, m *Module, ctx context.Context, id int64) *Execution {
	t.Helper()
	e, err := m.Execution(ctx, id)
	if err != nil {
		t.Fatalf("execution %d: %v", id, err)
	}
	return e
}

func mustBudget(t *testing.T, m *Module, ctx context.Context) *Budget {
	t.Helper()
	b, err := m.Budget(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func countRows(t *testing.T, app *gocommerce.App, ctx context.Context, table string) int {
	t.Helper()
	var n int
	if err := app.DB().QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func runErr(t *testing.T, err error) *RunError {
	t.Helper()
	var re *RunError
	if !errors.As(err, &re) {
		t.Fatalf("error = %v, want a RunError", err)
	}
	return re
}
