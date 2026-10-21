package amazon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	aigateway "github.com/itswadesh/gocommerce/ext/ai-gateway"
)

// providerStandIn answers in Anthropic's place for the gateway: the hosts are
// constants, so the gateway is given a client that sends them here.
type providerStandIn struct {
	mu     sync.Mutex
	bodies []string
	srv    *httptest.Server
}

func (p *providerStandIn) client() *http.Client {
	to, _ := url.Parse(p.srv.URL)
	return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		out := r.Clone(r.Context())
		out.URL.Scheme, out.URL.Host, out.Host = to.Scheme, to.Host, to.Host
		return http.DefaultTransport.RoundTrip(out)
	})}
}

func (p *providerStandIn) calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.bodies...)
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const gatewayRewrite = `{"title": "Acme steel widget, two-pack", "description": "<p>Two steel widgets.</p>",
  "product_type": "Widgets", "vendor": "Acme", "tags": ["widget", "steel"],
  "seo_title": "Acme steel widget", "seo_description": "Two steel widgets from Acme."}`

func newGatewayHarness(t *testing.T) (*harness, *aigateway.Module, *providerStandIn) {
	t.Helper()
	p := &providerStandIn{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.bodies = append(p.bodies, string(body))
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001",
		  "content":[{"type":"tool_use","id":"toolu_1","name":"answer","input":`+gatewayRewrite+`}],
		  "stop_reason":"tool_use","usage":{"input_tokens":1800,"output_tokens":240,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`)
	}))
	t.Cleanup(p.srv.Close)
	gw := aigateway.New(aigateway.Config{Client: p.client(), PollInterval: time.Hour})
	h := buildHarness(t, Config{Gateway: gw}, true, gocommerce.Config{}, gw)
	return h, gw, p
}

func switchOnGateway(t *testing.T, app *gocommerce.App, settings map[string]any) {
	t.Helper()
	all := map[string]any{
		"api_key": "sk-ant-test", "monthly_cap": 1000,
		"small_price_input": 100, "small_price_output": 500,
		"strong_price_input": 300, "strong_price_output": 1500,
	}
	for k, v := range settings {
		all[k] = v
	}
	on := true
	if _, err := app.Plugins().Update(context.Background(), aigateway.PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: all}); err != nil {
		t.Fatal(err)
	}
}

// With the gateway on for the store, the rewrite is one of its tasks:
// counted, capped and recorded, and the environment's model is not asked.
func TestTheRewriteGoesThroughTheGatewayWhenItIsOn(t *testing.T) {
	h, gw, p := newGatewayHarness(t)
	switchOnGateway(t, h.app, nil)
	job := h.wait(t, h.start(t, map[string]any{"url": h.listingWithVariants()}).ID)
	if job.Status != StatusDone {
		t.Fatalf("status = %s: %s", job.Status, job.Message)
	}
	product, err := h.app.Products().GetProduct(context.Background(), *job.ProductID)
	if err != nil {
		t.Fatal(err)
	}
	if product.Title != "Acme steel widget, two-pack" {
		t.Errorf("title = %q, want the gateway's rewrite", product.Title)
	}
	if h.llmCalls() != 0 {
		t.Errorf("the environment's model was asked %d times", h.llmCalls())
	}
	calls := p.calls()
	if len(calls) != 1 {
		t.Fatalf("%d calls through the gateway", len(calls))
	}
	// The listing travels as catalogue data, fenced.
	if !strings.Contains(calls[0], `class=\"catalogue\"`) || !strings.Contains(calls[0], "Acme Widget Pro") {
		t.Errorf("the request does not carry the listing as catalogue data: %s", calls[0])
	}

	runs, _, err := gw.Executions(context.Background(), aigateway.ExecutionQuery{Task: "product.rewrite"})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Outcome != aigateway.OutcomeOK || runs[0].CostMicroMinor != 1800*100+240*500 {
		t.Fatalf("runs = %+v", runs)
	}
	if len(runs[0].SourceRefs) != 1 || runs[0].SourceRefs[0].ID != "B0PARENT01" {
		t.Errorf("source refs = %+v, want the listing's ASIN", runs[0].SourceRefs)
	}
}

// A gateway that is installed but not switched on changes nothing: the
// environment's model rewrites, as before.
func TestWithTheGatewayOffTheEnvironmentsModelRewrites(t *testing.T) {
	h, gw, p := newGatewayHarness(t)
	job := h.wait(t, h.start(t, map[string]any{"url": h.listingWithVariants()}).ID)
	if job.Status != StatusDone {
		t.Fatalf("status = %s: %s", job.Status, job.Message)
	}
	if h.llmCalls() != 1 || len(p.calls()) != 0 {
		t.Errorf("environment model %d calls, gateway %d", h.llmCalls(), len(p.calls()))
	}
	if runs, _, _ := gw.Executions(context.Background(), aigateway.ExecutionQuery{}); len(runs) != 0 {
		t.Errorf("an idle gateway recorded %d runs", len(runs))
	}
}

// When the gateway refuses — here, a cap that cannot cover the call — the
// import keeps the listing's copy and says why. The environment's key is not
// a way round the store's own cap.
func TestAGatewayRefusalKeepsTheListingsCopyAndSaysWhy(t *testing.T) {
	h, _, p := newGatewayHarness(t)
	switchOnGateway(t, h.app, map[string]any{"monthly_cap": 0})
	job := h.wait(t, h.start(t, map[string]any{"url": h.listingWithVariants()}).ID)
	if job.Status != StatusDone {
		t.Fatalf("status = %s: %s", job.Status, job.Message)
	}
	if !containsPrefix(job.Warnings, "the rewrite failed") || !strings.Contains(strings.Join(job.Warnings, " "), "cap") {
		t.Errorf("warnings = %v, want the refused rewrite and the cap named", job.Warnings)
	}
	if h.llmCalls() != 0 || len(p.calls()) != 0 {
		t.Errorf("a refused rewrite still called a model: env %d, gateway %d", h.llmCalls(), len(p.calls()))
	}
}

// The task the importer registers is one the gateway can hold an answer to.
func TestTheRewriteTaskIsAcceptedByTheGateway(t *testing.T) {
	gw := aigateway.New(aigateway.Config{})
	if err := gw.RegisterTask(rewriteTask()); err != nil {
		t.Fatal(err)
	}
	var r rewrite
	dec := json.NewDecoder(strings.NewReader(gatewayRewrite))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		t.Fatalf("the schema's answer does not decode into the importer's type: %v", err)
	}
}

func (h *harness) llmCalls() int {
	h.llm.mu.Lock()
	defer h.llm.mu.Unlock()
	return h.llm.calls
}
