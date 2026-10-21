package gocommerce

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/itswadesh/gocommerce/internal/testdb"
)

// tracer is a module whose one route answers with the trace id its handler
// was given, which is the only way to see what a module reads.
type tracer struct{}

func (tracer) Name() string            { return "tracer" }
func (tracer) Migrations() []Migration { return nil }
func (tracer) Register(app *App) error {
	app.HandleFunc("GET /x/tracer/echo", func(w http.ResponseWriter, r *http.Request) {
		Respond(w, http.StatusOK, map[string]string{"trace_id": TraceID(r.Context())})
	})
	return nil
}

// lockedBuffer is a log sink the server goroutine and the test can share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) lines() []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(b.buf.Bytes()))
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func newTracedApp(t *testing.T) (*App, *lockedBuffer) {
	t.Helper()
	mods := []Module{tracer{}}
	dsn := testdb.Clone(t, "core:"+moduleKey(mods), func(dsn string) error {
		app, err := New(testConfig(dsn), mods...)
		if err != nil {
			return err
		}
		return app.Close()
	})
	logs := &lockedBuffer{}
	cfg := testConfig(dsn)
	cfg.Logger = slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	app, err := New(cfg, mods...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app, logs
}

func echoedTrace(t *testing.T, rec interface{ Result() *http.Response }) string {
	t.Helper()
	var body struct {
		Data struct {
			TraceID string `json:"trace_id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Result().Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Data.TraceID
}

var generatedTrace = regexp.MustCompile(`^[0-9a-f]{32}$`)

// A request that brings no trace id is given one: in the response, and on
// the context every handler and module reads.
func TestARequestWithoutATraceIDIsGivenOne(t *testing.T) {
	app, _ := newTracedApp(t)
	rec := do(t, app, http.MethodGet, "/x/tracer/echo")
	if rec.Code != http.StatusOK {
		t.Fatalf("echo = %d: %s", rec.Code, rec.Body)
	}
	got := rec.Header().Get("X-Trace-Id")
	if !generatedTrace.MatchString(got) {
		t.Fatalf("X-Trace-Id = %q, want 32 hex characters", got)
	}
	if seen := echoedTrace(t, rec); seen != got {
		t.Errorf("the handler read %q and the response said %q", seen, got)
	}
	if again := do(t, app, http.MethodGet, "/x/tracer/echo").Header().Get("X-Trace-Id"); again == got {
		t.Error("two requests were given the same trace id")
	}
}

// One the caller brings is kept when it is 8 to 64 letters, digits, dashes
// or underscores, and replaced when it is anything else.
func TestASuppliedTraceIDIsKeptOnlyWhenItIsWellFormed(t *testing.T) {
	app, _ := newTracedApp(t)
	cases := []struct {
		name, in string
		kept     bool
	}{
		{"a uuid", "3f2b8c1e-9d4a-4c55-8e21-7a0b6c9d1e2f", true},
		{"eight characters", "abcd_123", true},
		{"sixty-four characters", strings.Repeat("a", 64), true},
		{"seven characters", "abcdefg", false},
		{"sixty-five characters", strings.Repeat("a", 65), false},
		{"a space", "abcd efgh", false},
		{"a line break", "abcdefgh\nlevel=ERROR", false},
		{"punctuation", "abcd.efgh", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, app, http.MethodGet, "/x/tracer/echo", func(r *http.Request) {
				r.Header["X-Trace-Id"] = []string{tc.in}
			})
			got := rec.Header().Get("X-Trace-Id")
			if tc.kept && got != tc.in {
				t.Fatalf("X-Trace-Id = %q, want %q kept", got, tc.in)
			}
			if !tc.kept && (got == tc.in || !generatedTrace.MatchString(got)) {
				t.Fatalf("X-Trace-Id = %q, want %q replaced by a fresh one", got, tc.in)
			}
			if seen := echoedTrace(t, rec); seen != got {
				t.Errorf("the handler read %q and the response said %q", seen, got)
			}
		})
	}
}

// The request log line names the store and the trace (D104), so one line in
// the log leads to every record the request left behind.
func TestTheRequestLogLineCarriesTheStoreAndTheTrace(t *testing.T) {
	app, logs := newTracedApp(t)
	store := firstStore(t, app)
	rec := do(t, app, http.MethodGet, "/x/tracer/echo", header("X-Trace-Id", "trace-for-the-log"))
	if rec.Code != http.StatusOK {
		t.Fatalf("echo = %d: %s", rec.Code, rec.Body)
	}
	for _, line := range logs.lines() {
		if line["msg"] != "request" || line["path"] != "/x/tracer/echo" {
			continue
		}
		if line["trace_id"] != "trace-for-the-log" {
			t.Errorf("trace_id = %v, want trace-for-the-log", line["trace_id"])
		}
		if line["store"] != store {
			t.Errorf("store = %v, want %s", line["store"], store)
		}
		return
	}
	t.Fatal("no request line was logged")
}

// A storefront on another origin can read the trace id it was given, to
// quote it in a support request.
func TestACrossOriginCallerCanReadTheTraceID(t *testing.T) {
	app, _ := newTracedApp(t)
	app.cfg.CORSOrigins = []string{"https://shop.example.com"}
	rec := do(t, app, http.MethodGet, "/x/tracer/echo", header("Origin", "https://shop.example.com"))
	if got := rec.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "X-Trace-Id") {
		t.Errorf("Access-Control-Expose-Headers = %q, want X-Trace-Id in it", got)
	}
}
