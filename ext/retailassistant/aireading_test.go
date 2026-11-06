package retailassistant

import (
	"context"
	"encoding/json"
	"fmt"
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

// fakeClaude answers every model with the next queued answer, or the last one.
type fakeClaude struct {
	mu       sync.Mutex
	answers  []string
	requests []map[string]any
}

func (c *fakeClaude) serve(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	c.mu.Lock()
	c.requests = append(c.requests, body)
	answer := ""
	if n := len(c.requests); n <= len(c.answers) {
		answer = c.answers[n-1]
	} else if len(c.answers) > 0 {
		answer = c.answers[len(c.answers)-1]
	}
	c.mu.Unlock()
	model, _ := body["model"].(string)
	text, _ := json.Marshal(answer)
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"id":"msg_1","type":"message","role":"assistant","model":%q,
	  "content":[{"type":"text","text":%s}],"stop_reason":"end_turn",
	  "usage":{"input_tokens":3000,"output_tokens":400,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`, model, text)
}

func (c *fakeClaude) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

type hostRewrite func(*http.Request) (*http.Response, error)

func (f hostRewrite) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// setupAI is a store whose gateway talks to a stand-in, with AI reading on unless
// a test turns something off.
func setupAI(t *testing.T, change func(*Config)) (*harness, *fakeClaude) {
	t.Helper()
	c := &fakeClaude{}
	srv := httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(srv.Close)
	to, _ := url.Parse(srv.URL)
	client := &http.Client{Transport: hostRewrite(func(r *http.Request) (*http.Response, error) {
		out := r.Clone(r.Context())
		out.URL.Scheme, out.URL.Host, out.Host = to.Scheme, to.Host, to.Host
		return http.DefaultTransport.RoundTrip(out)
	})}
	gw := aigateway.New(aigateway.Config{Client: client, CallTimeout: 5 * time.Second, PollInterval: time.Hour,
		PlatformAnthropicKey: "sk-ant-platform", PlatformUSDRates: "INR=1",
		PlatformSmallPrices: "1,5,1,1", PlatformStrongPrices: "3,15,3,3"})
	h := setupWith(t, func(cfg *Config) {
		cfg.Gateway = gw
		if change != nil {
			change(cfg)
		}
	})
	on := true
	if _, err := h.app.Plugins().Update(context.Background(), aigateway.PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{
		"api_key": "sk-ant-test", "monthly_cap": 100000,
		"small_price_input": 100, "small_price_output": 500, "strong_price_input": 300, "strong_price_output": 1500,
		"share_documents": true,
	}}); err != nil {
		t.Fatal(err)
	}
	h.setSetting("ai_reading", true)
	return h, c
}

func (h *harness) setSetting(key string, value any) {
	h.t.Helper()
	if _, err := h.app.Plugins().Update(h.ctxOf(""), pluginKey, gocommerce.PluginPatch{Settings: map[string]any{key: value}}); err != nil {
		h.t.Fatalf("set %s: %v", key, err)
	}
}

func goodRead(total string, confidence int) string {
	return fmt.Sprintf(`{"document_type":"purchase_invoice","supplier_name":"ABC Cosmetics","gstin":"21abcde1234f1z5","invoice_no":"PDF-1",
	 "invoice_date":"2026-11-05","subtotal":"1000.00","tax":"180.00","total":%q,"payment_method":null,
	 "lines":[{"description":"Lip colour","sku":null,"barcode":null,"hsn":null,"quantity":"10","unit":null,"unit_price":"100.00","mrp":null,"line_total":"1000.00"}],
	 "confidence":%d,"unclear":["The stamp over the date is faint."]}`, total, confidence)
}

var pdfBytes = []byte("%PDF-1.7 a supplier's bill")

func (h *harness) arrivePDF(id string) Document {
	h.t.Helper()
	h.deliver(h.file("msg-"+id, senderA, "media-"+id, id+".pdf", pdfBytes))
	h.work("")
	return h.documents("")[0]
}

func TestAPDFIsReadByAIAndStillNeedsAPersonWhoLooked(t *testing.T) {
	h, claude := setupAI(t, nil)
	claude.answers = []string{goodRead("1180.00", 95)}
	h.link("", senderA)
	d := h.arrivePDF("a")

	if d.Status != "needs_review" || d.Draft == nil || d.Draft.Source.ExtractionMethod != extractionAI {
		t.Fatalf("status %s, draft %+v", d.Status, d.Draft)
	}
	if d.Draft.InvoiceNo != "PDF-1" || *d.Draft.Amounts.Total != 118000 || d.Draft.InvoiceDate != "2026-11-05" {
		t.Fatalf("the reading did not reach the draft: %+v", d.Draft)
	}
	if *d.Draft.Supplier.GSTIN != "21ABCDE1234F1Z5" || d.Draft.Source.Notes[0] == "" || *d.Draft.Source.Confidence != 95 {
		t.Fatalf("draft = %+v", d.Draft)
	}
	if h.count("", "retailassistant_purchase_invoices") != 0 {
		t.Fatal("a model's reading posted itself")
	}
	// The whole risk of a model: a wrong figure that looks right. Nobody approves
	// one without saying they compared it with the file.
	approve := path(fmt.Sprintf("/documents/%d/approve", d.ID), "")
	h.admin(http.MethodPost, approve, map[string]any{"version": d.Version}, nil, http.StatusBadRequest)
	if h.count("", "retailassistant_purchase_invoices") != 0 {
		t.Fatal("approved without comparing")
	}
	var out struct {
		SupplierID int64 `json:"supplier_id"`
	}
	h.admin(http.MethodPost, approve, map[string]any{"version": d.Version, "compared_with_file": true}, &out, http.StatusOK)
	if got := h.balance(out.SupplierID).Outstanding.AmountMinor; got != 118000 {
		t.Fatalf("outstanding = %d", got)
	}
	var audits int
	if err := h.app.DB().QueryRowContext(h.ctxOf(""), `SELECT count(*) FROM retailassistant_audit WHERE event = 'document.read_by_ai'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("%d audit rows for the reading", audits)
	}
}

func TestNothingLeavesUntilTheStoreSaysSo(t *testing.T) {
	h, claude := setupAI(t, nil)
	claude.answers = []string{goodRead("1180.00", 95)}
	h.link("", senderA)

	h.setSetting("ai_reading", false)
	if d := h.arrivePDF("off"); d.Draft.Source.ExtractionMethod == extractionAI {
		t.Fatal("read with the store's switch off")
	}
	h.setSetting("ai_reading", true)
	if _, err := h.app.Plugins().Update(context.Background(), aigateway.PluginKey, gocommerce.PluginPatch{Settings: map[string]any{"share_documents": false}}); err != nil {
		t.Fatal(err)
	}
	d := h.arrivePDF("policy")
	if d.Draft.Source.ExtractionMethod == extractionAI {
		t.Fatal("read with the gateway refusing documents")
	}
	if claude.calls() != 0 {
		t.Fatalf("%d calls reached the provider", claude.calls())
	}
	h.admin(http.MethodPost, path(fmt.Sprintf("/documents/%d/read", d.ID), ""), nil, nil, http.StatusConflict)
}

func TestASpreadsheetIsNeverSent(t *testing.T) {
	h, claude := setupAI(t, nil)
	claude.answers = []string{goodRead("1180.00", 95)}
	h.link("", senderA)
	d := h.arrive("CSV-1")
	if d.Draft.Source.ExtractionMethod != "csv" || claude.calls() != 0 {
		t.Fatalf("method %s, %d calls", d.Draft.Source.ExtractionMethod, claude.calls())
	}
	h.admin(http.MethodPost, path(fmt.Sprintf("/documents/%d/read", d.ID), ""), nil, nil, http.StatusConflict)
}

func TestFiguresThatDisagreeGoUpTheLadder(t *testing.T) {
	h, claude := setupAI(t, nil)
	claude.answers = []string{goodRead("9999.00", 99), goodRead("1180.00", 90)}
	h.link("", senderA)
	d := h.arrivePDF("ladder")
	if claude.calls() != 2 {
		t.Fatalf("%d calls, want a second opinion", claude.calls())
	}
	if *d.Draft.Amounts.Total != 118000 {
		t.Fatalf("total = %d", *d.Draft.Amounts.Total)
	}
}

func TestAnUnreadableAmountIsNeverAFigureAndThePersonCanStillType(t *testing.T) {
	h, claude := setupAI(t, nil)
	claude.answers = []string{goodRead("about a thousand", 90)}
	h.link("", senderA)
	d := h.arrivePDF("junk")
	if d.Status != "needs_review" || d.Draft.Source.ExtractionMethod == extractionAI || d.Draft.Amounts.Total != nil {
		t.Fatalf("status %s, draft %+v", d.Status, d.Draft)
	}
	if !strings.Contains(d.LastError, "Reading with AI failed") {
		t.Fatalf("last error = %q", d.LastError)
	}
	h.admin(http.MethodPost, path(fmt.Sprintf("/documents/%d/read", d.ID), ""), nil, nil, http.StatusBadGateway)

	// A person asking again, once the model can read it.
	claude.mu.Lock()
	claude.answers, claude.requests = []string{goodRead("1180.00", 90)}, nil
	claude.mu.Unlock()
	var again Document
	h.admin(http.MethodPost, path(fmt.Sprintf("/documents/%d/read", d.ID), ""), nil, &again, http.StatusOK)
	if again.Draft.Source.ExtractionMethod != extractionAI || again.LastError != "" {
		t.Fatalf("after reading again: %+v", again)
	}
}

func TestAReviewersTypingBeatsAReadingThatArrivesLate(t *testing.T) {
	h, claude := setupAI(t, nil)
	claude.answers = []string{goodRead("1180.00", 90)}
	h.link("", senderA)
	h.setSetting("ai_reading", false)
	d := h.arrivePDF("late")
	h.admin(http.MethodPost, path(fmt.Sprintf("/documents/%d/correct", d.ID), ""), map[string]any{
		"version": d.Version, "draft": map[string]any{"document_type": KindInvoice, "invoice_no": "TYPED"},
	}, nil, http.StatusOK)
	err := h.mod.applyRead(h.ctxOf(""), d.ID, d.Version, Draft{InvoiceNo: "FROM-AI"}, aigateway.Result{})
	if err != errKeptTheirs {
		t.Fatalf("err = %v", err)
	}
	var got Document
	h.admin(http.MethodGet, path(fmt.Sprintf("/documents/%d", d.ID), ""), nil, &got, http.StatusOK)
	if got.Draft.InvoiceNo != "TYPED" {
		t.Fatalf("invoice no = %q", got.Draft.InvoiceNo)
	}
}

func TestASealedFileIsOpenedForTheModelAndNotCopiedIntoItsRecords(t *testing.T) {
	h, claude := setupAI(t, func(c *Config) { c.FileKey = testFileKey })
	claude.answers = []string{goodRead("1180.00", 95)}
	h.link("", senderA)
	d := h.arrivePDF("sealed")
	if d.Draft.Source.ExtractionMethod != extractionAI {
		t.Fatalf("draft = %+v", d.Draft)
	}
	sent, _ := json.Marshal(claude.requests[0])
	if !strings.Contains(string(sent), "JVBERi0") {
		t.Fatalf("the model was not sent the opened PDF: %.300s", sent)
	}
	var inRecords int
	if err := h.app.DB().QueryRowContext(h.ctxOf(""), `
		SELECT count(*) FROM ai_executions e WHERE e::text LIKE '%JVBERi0%'`).Scan(&inRecords); err != nil {
		t.Fatal(err)
	}
	if inRecords != 0 {
		t.Fatal("the bill was copied into the gateway's records")
	}
}
