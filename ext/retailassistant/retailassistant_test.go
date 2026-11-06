package retailassistant

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// Every fixture here is made up in the test. No real supplier's document is in
// this repository.

const (
	testSecret = "test-app-secret"
	phoneID    = "1000"
	senderA    = "919800000001"
	senderB    = "919800000002"
	stranger   = "919800000099"
)

// fakeMeta is the Graph API: it serves files and records what was sent.
type fakeMeta struct {
	mu    sync.Mutex
	sent  []sentMessage
	files map[string][]byte
	srv   *httptest.Server
}

type sentMessage struct{ To, Body string }

func (f *fakeMeta) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/"+graphVersion+"/")
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/messages"):
		var in struct {
			To   string `json:"to"`
			Text struct {
				Body string `json:"body"`
			} `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.sent = append(f.sent, sentMessage{in.To, in.Text.Body})
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.out"}]}`))
	case strings.HasPrefix(r.URL.Path, "/download/"):
		data, ok := f.files[strings.TrimPrefix(r.URL.Path, "/download/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(data)
	default:
		if _, ok := f.files[p]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"url": f.srv.URL + "/download/" + p})
	}
}

func (f *fakeMeta) to(phone string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, s := range f.sent {
		if s.To == phone {
			out = append(out, s.Body)
		}
	}
	return out
}

type harness struct {
	t      *testing.T
	app    *gocommerce.App
	mod    *Module
	meta   *fakeMeta
	second string // the slug of Store B
}

func setup(t *testing.T) *harness {
	t.Helper()
	return setupWith(t, func(*Config) {})
}

func setupWith(t *testing.T, change func(*Config)) *harness {
	t.Helper()
	meta := &fakeMeta{files: map[string][]byte{}}
	meta.srv = gctest.StubHTTP(t, meta.handler)
	cfg := Config{
		AppSecret: testSecret, VerifyToken: "verify-me", AccessToken: "test-access-token", PhoneNumberID: phoneID,
		BaseURL: meta.srv.URL, Client: meta.srv.Client(),
	}
	change(&cfg)
	mod := New(cfg)
	mods := []gocommerce.Module{mod}
	if cfg.Gateway != nil {
		mods = []gocommerce.Module{cfg.Gateway, mod}
	}
	app := gctest.NewWithConfig(t, gocommerce.Config{Currency: "INR"}, mods...)
	st, err := app.Stores().Create(context.Background(), "Store B", "store-b")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, app: app, mod: mod, meta: meta, second: st.Slug}
	h.enable("")
	h.enable(h.second)
	return h
}

func path(p, store string) string {
	if store == "" {
		return adminBase + p
	}
	sep := "?"
	if strings.Contains(p, "?") {
		sep = "&"
	}
	return adminBase + p + sep + "store=" + store
}

func (h *harness) enable(store string) {
	h.t.Helper()
	on := true
	ctx := context.Background()
	if store != "" {
		st, err := h.app.Stores().BySlug(ctx, store)
		if err != nil {
			h.t.Fatal(err)
		}
		ctx = gocommerce.WithStore(ctx, st.ID)
	}
	if _, err := h.app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{Enabled: &on}); err != nil {
		h.t.Fatalf("switch on: %v", err)
	}
}

func (h *harness) setRetention(days int) {
	h.t.Helper()
	if _, err := h.app.Plugins().Update(h.ctxOf(""), pluginKey, gocommerce.PluginPatch{Settings: map[string]any{"retention_days": days}}); err != nil {
		h.t.Fatalf("set retention: %v", err)
	}
}

// admin calls an admin route and decodes data into out.
func (h *harness) admin(method, target string, body, out any, want int) {
	h.t.Helper()
	rec := gctest.AdminRequest(h.t, h.app, method, target, body)
	if rec.Code != want {
		h.t.Fatalf("%s %s = %d, want %d: %s", method, target, rec.Code, want, rec.Body)
	}
	if out != nil {
		gctest.DecodeData(h.t, rec, out)
	}
}

func signed(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (h *harness) webhook(body []byte, signature string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/x/retailassistant/whatsapp/webhook", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", signature)
	rec := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(rec, req)
	return rec
}

func envelope(messages ...map[string]any) []byte {
	raw, _ := json.Marshal(map[string]any{"object": "whatsapp_business_account", "entry": []any{map[string]any{
		"changes": []any{map[string]any{"field": "messages", "value": map[string]any{
			"metadata": map[string]any{"phone_number_id": phoneID}, "messages": messages,
		}}},
	}}})
	return raw
}

func (h *harness) deliver(messages ...map[string]any) {
	h.t.Helper()
	body := envelope(messages...)
	if rec := h.webhook(body, signed(body, testSecret)); rec.Code != http.StatusOK {
		h.t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}
}

func text(id, from, body string) map[string]any {
	return map[string]any{"id": id, "from": from, "type": "text", "text": map[string]any{"body": body}}
}

func (h *harness) file(id, from, media, filename string, data []byte) map[string]any {
	h.meta.mu.Lock()
	h.meta.files[media] = data
	h.meta.mu.Unlock()
	return map[string]any{"id": id, "from": from, "type": "document",
		"document": map[string]any{"id": media, "filename": filename, "mime_type": "application/octet-stream"}}
}

// link names a phone in a store and has it prove itself from that phone.
func (h *harness) link(store, phone string) {
	h.t.Helper()
	var l PhoneLink
	h.admin(http.MethodPost, path("/phone-links", store), map[string]string{"phone": "+" + phone}, &l, http.StatusCreated)
	h.deliver(text("link-"+store+phone, phone, "LINK "+l.Code))
}

func (h *harness) ctxOf(store string) context.Context {
	h.t.Helper()
	ctx := context.Background()
	if store == "" {
		return gocommerce.WithStore(ctx, h.app.DefaultStore())
	}
	st, err := h.app.Stores().BySlug(ctx, store)
	if err != nil {
		h.t.Fatal(err)
	}
	return gocommerce.WithStore(ctx, st.ID)
}

func (h *harness) work(store string) {
	h.t.Helper()
	if err := h.mod.WorkPass(h.ctxOf(store)); err != nil {
		h.t.Fatalf("work pass: %v", err)
	}
}

func (h *harness) documents(store string) []Document {
	h.t.Helper()
	var out []Document
	h.admin(http.MethodGet, path("/documents", store), nil, &out, http.StatusOK)
	return out
}

func (h *harness) count(store, table string) int {
	h.t.Helper()
	var n int
	if err := h.app.DB().QueryRowContext(h.ctxOf(store), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func csvInvoice(number string, total int) []byte {
	sub := total * 100 / 118
	return []byte(fmt.Sprintf(`Supplier,ABC Cosmetics
GSTIN,21ABCDE1234F1Z5
Invoice No,%s
Invoice Date,05/11/2026

Description,Qty,Rate,Amount
Lip colour shade 01,10,%d.00,%d.00

Subtotal,%d.00
Tax,%d.00
Grand Total,%d.00
`, number, sub/10, sub, sub, total-sub, total))
}

// Spec test 1: the one number sorts senders into their stores, a stranger gets
// the way in and keeps nothing, and neither the body nor a forged signature can
// choose a store.
func TestTheSharedNumberRoutesBySenderAndNeverToAStranger(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	h.link(h.second, senderB)

	h.deliver(h.file("a1", senderA, "m-a", "a.csv", csvInvoice("A-1", 11800)))
	h.deliver(h.file("b1", senderB, "m-b", "b.csv", csvInvoice("B-1", 23600)))
	h.work("")
	h.work(h.second)

	docsA, docsB := h.documents(""), h.documents(h.second)
	if len(docsA) != 1 || docsA[0].Draft.InvoiceNo != "A-1" {
		t.Fatalf("store A documents = %+v", docsA)
	}
	if len(docsB) != 1 || docsB[0].Draft.InvoiceNo != "B-1" {
		t.Fatalf("store B documents = %+v", docsB)
	}

	h.deliver(h.file("x1", stranger, "m-x", "x.csv", csvInvoice("X-1", 100)))
	if h.count("", "retailassistant_documents") != 1 || h.count(h.second, "retailassistant_documents") != 1 {
		t.Fatal("a stranger's file was kept in a store")
	}
	if got := h.meta.to(stranger); len(got) != 1 || !strings.Contains(got[0], "not linked") {
		t.Fatalf("the stranger was told %q", got)
	}

	body := envelope(h.file("forged", senderA, "m-f", "f.csv", csvInvoice("F-1", 100)))
	if rec := h.webhook(body, signed(body, "another-secret")); rec.Code != http.StatusForbidden {
		t.Fatalf("a forged signature = %d", rec.Code)
	}
	if rec := h.webhook(body, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("no signature = %d", rec.Code)
	}
	if h.count("", "retailassistant_documents") != 1 {
		t.Fatal("a forged message was kept")
	}
}

// On a number shared with another service, that service's customers write to it
// about their orders, and must hear nothing from here. A link still works.
func TestAQuietNumberSaysNothingToStrangers(t *testing.T) {
	h := setup(t)
	h.mod.cfg.QuietToStrangers = true
	h.deliver(h.file("x1", stranger, "m-x", "x.csv", csvInvoice("X-1", 100)))
	if got := h.meta.to(stranger); len(got) != 0 {
		t.Fatalf("a stranger was told %q", got)
	}
	if h.count("", "retailassistant_documents") != 0 {
		t.Fatal("a stranger's file was kept")
	}
	h.link("", senderA)
	if got := h.meta.to(senderA); len(got) != 1 || !strings.Contains(got[0], "now linked") {
		t.Fatalf("the phone being linked was told %q", got)
	}
}

// A code proves the number: another phone sending it gets nowhere, and a store
// naming a number attaches nothing until that number answers.
func TestALinkCodeOnlyWorksFromTheNumberItWasIssuedFor(t *testing.T) {
	h := setup(t)
	var l PhoneLink
	h.admin(http.MethodPost, path("/phone-links", ""), map[string]string{"phone": "+" + senderA}, &l, http.StatusCreated)

	h.deliver(h.file("early", senderA, "m-e", "e.csv", csvInvoice("E-1", 100)))
	if h.count("", "retailassistant_documents") != 0 {
		t.Fatal("a named but unproved number posted a document")
	}
	h.deliver(text("steal", stranger, "LINK "+l.Code))
	var links []PhoneLink
	h.admin(http.MethodGet, path("/phone-links", ""), nil, &links, http.StatusOK)
	if links[0].VerifiedAt != nil {
		t.Fatal("another number proved this link")
	}
	h.deliver(text("wrong", senderA, "LINK 000000"))
	h.deliver(text("right", senderA, "LINK "+l.Code))
	h.admin(http.MethodGet, path("/phone-links", ""), nil, &links, http.StatusOK)
	if links[0].VerifiedAt == nil || links[0].Code != "" {
		t.Fatalf("link = %+v", links[0])
	}
}

// One phone in two stores: nothing is posted until it says which.
func TestAPhoneInTwoStoresChoosesBeforeAnythingIsKept(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	h.link(h.second, senderA)

	h.deliver(h.file("two1", senderA, "m-t", "t.csv", csvInvoice("T-1", 100)))
	if h.count("", "retailassistant_documents")+h.count(h.second, "retailassistant_documents") != 0 {
		t.Fatal("a document was posted into a store by guesswork")
	}
	h.deliver(text("two2", senderA, "STORE "+h.second))
	h.deliver(h.file("two3", senderA, "m-t", "t.csv", csvInvoice("T-1", 100)))
	if h.count("", "retailassistant_documents") != 0 || h.count(h.second, "retailassistant_documents") != 1 {
		t.Fatal("the document did not go to the chosen store")
	}
}

// Spec test 2: the original is kept, each field says where it came from, a wrong
// total is flagged, and a figure that is not there stays null.
func TestExtractionKeepsTheOriginalAndInventsNothing(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	bad := []byte("Supplier,ABC Cosmetics\nInvoice No,W-1\nInvoice Date,05/11/2026\n\nDescription,Qty,Rate,Amount\nLip colour,1,100.00,100.00\n\nSubtotal,100.00\nTax,18.00\nGrand Total,999.00\n")
	h.deliver(h.file("w1", senderA, "m-w", "w.csv", bad))
	h.work("")

	docs := h.documents("")
	d := docs[0].Draft
	if docs[0].Status != "needs_review" {
		t.Fatalf("status = %s; nothing is posted unread", docs[0].Status)
	}
	if d.Supplier.GSTIN != nil {
		t.Errorf("a missing GSTIN was invented: %v", *d.Supplier.GSTIN)
	}
	if d.Lines[0].Barcode != nil || d.Lines[0].MRPMinor != nil || d.Lines[0].HSN != nil {
		t.Errorf("a missing line field was invented: %+v", d.Lines[0])
	}
	if d.Source.Provenance["invoice_no"] == "" || d.Source.ExtractionMethod != "csv" {
		t.Errorf("source = %+v", d.Source)
	}
	if !strings.Contains(strings.Join(d.Exceptions, " "), "add up") {
		t.Errorf("exceptions = %v, want the wrong total flagged", d.Exceptions)
	}
	rec := gctest.AdminRequest(t, h.app, http.MethodGet, path("/documents/"+fmt.Sprint(docs[0].ID)+"/file", ""), nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), bad) {
		t.Fatalf("original = %d, %d bytes", rec.Code, rec.Body.Len())
	}
	if ct := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(ct, "attachment") {
		t.Errorf("the original is served as %q, want a download", ct)
	}

	// Refused until a person says they looked, and that is written down.
	h.admin(http.MethodPost, path(fmt.Sprintf("/documents/%d/approve", docs[0].ID), ""), map[string]any{"version": docs[0].Version}, nil, http.StatusBadRequest)
	h.admin(http.MethodPost, path(fmt.Sprintf("/documents/%d/approve", docs[0].ID), ""), map[string]any{"version": docs[0].Version, "accept_mismatch": true}, nil, http.StatusOK)
}

func TestXLSXAndDOCXAreReadAndAPDFOrPhotoIsLeftToAPerson(t *testing.T) {
	x := zipOf(t, map[string]string{
		"xl/workbook.xml":      `<workbook/>`,
		"xl/sharedStrings.xml": `<sst><si><t>Supplier</t></si><si><t>ABC Cosmetics</t></si><si><t>Description</t></si><si><t>Qty</t></si><si><t>Amount</t></si><si><r><t>Lip </t></r><r><t>colour</t></r></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData>
			<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>
			<row r="2"><c r="A2" t="inlineStr"><is><t>Invoice No</t></is></c><c r="B2" t="inlineStr"><is><t>X-9</t></is></c></row>
			<row r="3"><c r="A3" t="inlineStr"><is><t>Date</t></is></c><c r="B3"><v>46331</v></c></row>
			<row r="4"><c r="A4" t="inlineStr"><is><t>Total</t></is></c><c r="B4"><v>118</v></c></row>
			<row r="6"><c r="A6" t="s"><v>2</v></c><c r="B6" t="s"><v>3</v></c><c r="C6" t="s"><v>4</v></c></row>
			<row r="7"><c r="A7" t="s"><v>5</v></c><c r="B7"><v>2</v></c><c r="C7"><v>118</v></c></row>
		</sheetData></worksheet>`,
	})
	mime, d := Extract(x, "bill.xlsx", "INR")
	if !strings.Contains(mime, "spreadsheet") || d.InvoiceNo != "X-9" || d.Supplier.Name != "ABC Cosmetics" {
		t.Fatalf("xlsx: %s %+v", mime, d)
	}
	if d.Amounts.Total == nil || *d.Amounts.Total != 11800 || len(d.Lines) != 1 || d.Lines[0].Description != "Lip colour" {
		t.Fatalf("xlsx amounts: %+v %+v", d.Amounts, d.Lines)
	}
	if d.InvoiceDate != "2026-11-05" {
		t.Errorf("xlsx date = %q", d.InvoiceDate)
	}

	word := zipOf(t, map[string]string{"word/document.xml": `<w:document xmlns:w="w"><w:body>
		<w:p><w:r><w:t>Supplier: DEF Beauty</w:t></w:r></w:p>
		<w:p><w:r><w:t>Invoice No: D-3</w:t></w:r></w:p>
		<w:p><w:r><w:t>Date: 2026-11-05</w:t></w:r></w:p>
		<w:p><w:r><w:t>Total: 50.00</w:t></w:r></w:p>
		<w:tbl><w:tr><w:tc><w:p><w:r><w:t>Item</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Amount</w:t></w:r></w:p></w:tc></w:tr>
		<w:tr><w:tc><w:p><w:r><w:t>Kajal</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>50.00</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
		</w:body></w:document>`})
	mime, d = Extract(word, "bill.docx", "INR")
	if !strings.Contains(mime, "wordprocessing") || d.Supplier.Name != "DEF Beauty" || d.InvoiceNo != "D-3" || len(d.Lines) != 1 || *d.Amounts.Total != 5000 {
		t.Fatalf("docx: %s %+v", mime, d)
	}
	if len(d.Exceptions) != 0 {
		t.Errorf("docx exceptions = %v", d.Exceptions)
	}

	// The extension does not decide: a PDF called .xlsx is a PDF.
	for name, data := range map[string][]byte{
		"fake.xlsx": []byte("%PDF-1.7 Grand Total 999"),
		"photo.jpg": {0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3},
		"pic.png":   []byte("\x89PNG\r\n\x1a\nxxxx"),
	} {
		_, d := Extract(data, name, "INR")
		if d.Amounts.Total != nil || d.Supplier.Name != "" || len(d.Exceptions) == 0 {
			t.Errorf("%s: %+v, want nothing read and a person told", name, d)
		}
	}
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestParseMoneyGoesThroughDigitsNotFloats(t *testing.T) {
	for in, want := range map[string]int64{"1,18,000.50": 11800050, "₹ 1180": 118000, "0.07": 7, "19.9": 1990, "100.000": 10000} {
		if got, err := parseMoney(in, 2); err != nil || got != want {
			t.Errorf("parseMoney(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "1.234", "-5", "12e3"} {
		if got, err := parseMoney(in, 2); err == nil {
			t.Errorf("parseMoney(%q) = %d, want refused", in, got)
		}
	}
}

// post runs a CSV bill for total through the whole path and returns the bill's
// invoice id and its supplier.
func (h *harness) post(number string, total int) (invoiceID, supplierID int64) {
	h.t.Helper()
	id := "msg-" + number
	h.deliver(h.file(id, senderA, "media-"+number, number+".csv", csvInvoice(number, total)))
	h.work("")
	docs := h.documents("")
	var out struct {
		EntityID   int64 `json:"entity_id"`
		SupplierID int64 `json:"supplier_id"`
	}
	// Whole-rupee fixtures round when split into subtotal and tax, which a real
	// reviewer would correct; the test says it looked.
	h.admin(http.MethodPost, path(fmt.Sprintf("/documents/%d/approve", docs[0].ID), ""),
		map[string]any{"version": docs[0].Version, "accept_mismatch": true}, &out, http.StatusOK)
	return out.EntityID, out.SupplierID
}

func (h *harness) balance(supplier int64) SupplierBalance {
	h.t.Helper()
	var out []SupplierBalance
	h.admin(http.MethodGet, path("/suppliers", ""), nil, &out, http.StatusOK)
	for _, b := range out {
		if b.ID == supplier {
			return b
		}
	}
	h.t.Fatalf("no supplier %d in %+v", supplier, out)
	return SupplierBalance{}
}

func (h *harness) pay(supplier, minor int64, verified bool) int64 {
	h.t.Helper()
	var out struct {
		ID int64 `json:"id"`
	}
	h.admin(http.MethodPost, path("/payments", ""), map[string]any{
		"supplier_id": supplier, "amount_minor": minor, "method": "cash", "paid_on": "2026-11-05", "verified": verified,
	}, &out, http.StatusCreated)
	return out.ID
}

// Spec test 3: the same webhook twice, and the same bill sent again as a new
// message, make one liability.
func TestDuplicatesPostOneLiability(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	msg := h.file("dup1", senderA, "m-d", "d.csv", csvInvoice("DUP-1", 11800))
	h.deliver(msg)
	h.deliver(msg)
	h.work("")
	if n := h.count("", "retailassistant_documents"); n != 1 {
		t.Fatalf("a replayed webhook made %d documents", n)
	}
	first := h.documents("")[0]
	h.admin(http.MethodPost, path(fmt.Sprintf("/documents/%d/approve", first.ID), ""), map[string]any{"version": first.Version, "accept_mismatch": true}, nil, http.StatusOK)

	// Approving again is refused: it is posted.
	h.admin(http.MethodPost, path(fmt.Sprintf("/documents/%d/approve", first.ID), ""), map[string]any{"version": first.Version + 1, "accept_mismatch": true}, nil, http.StatusConflict)

	h.deliver(h.file("dup2", senderA, "m-d2", "d.csv", csvInvoice("DUP-1", 11800)))
	h.work("")
	again := h.documents("")[0]
	if !strings.Contains(strings.Join(again.Draft.Exceptions, " "), "same file") {
		t.Errorf("exceptions = %v, want the resend flagged", again.Draft.Exceptions)
	}
	h.admin(http.MethodPost, path(fmt.Sprintf("/documents/%d/approve", again.ID), ""), map[string]any{"version": again.Version, "accept_mismatch": true}, nil, http.StatusConflict)
	if n := h.count("", "retailassistant_purchase_invoices"); n != 1 {
		t.Fatalf("%d invoices posted, want 1", n)
	}
}

// Two suppliers can each have an invoice with the same number.
func TestTheSameNumberFromAnotherSupplierIsAnotherBill(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	h.post("SAME-1", 1000)
	other := []byte("Supplier,Other Traders\nInvoice No,SAME-1\nDate,05/11/2026\nTotal,500.00\n\nDescription,Amount\nKajal,500.00\n")
	h.deliver(h.file("o1", senderA, "m-o", "o.csv", other))
	h.work("")
	doc := h.documents("")[0]
	h.admin(http.MethodPost, path(fmt.Sprintf("/documents/%d/approve", doc.ID), ""), map[string]any{"version": doc.Version}, nil, http.StatusOK)
	if n := h.count("", "retailassistant_purchase_invoices"); n != 2 {
		t.Fatalf("%d invoices, want 2", n)
	}
}

// Spec test 4, in the spec's own figures.
func TestOutstandingIsBillsLessCreditsLessVerifiedPaymentsAllocated(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	invoice, supplier := h.post("L-1", 10000)
	if got := h.balance(supplier).Outstanding.AmountMinor; got != 1000000 {
		t.Fatalf("after the bill, outstanding = %d, want 1000000", got)
	}

	// An unverified proof counts for nothing, and cannot be allocated.
	unverified := h.pay(supplier, 50000, false)
	b := h.balance(supplier)
	if b.Outstanding.AmountMinor != 1000000 || b.Unverified.AmountMinor != 50000 || b.Advance.AmountMinor != 0 {
		t.Fatalf("after an unverified payment: %+v", b)
	}
	h.admin(http.MethodPost, path(fmt.Sprintf("/payments/%d/allocate", unverified), ""),
		map[string]any{"invoice_id": invoice, "amount_minor": 50000}, nil, http.StatusConflict)

	paid := h.pay(supplier, 300000, true)
	h.admin(http.MethodPost, path(fmt.Sprintf("/payments/%d/allocate", paid), ""),
		map[string]any{"invoice_id": invoice, "amount_minor": 300000}, nil, http.StatusCreated)
	h.admin(http.MethodPost, path("/credits", ""), map[string]any{
		"supplier_id": supplier, "amount_minor": 100000, "kind": "return", "credit_no": "CN-1", "invoice_id": invoice,
	}, nil, http.StatusCreated)

	b = h.balance(supplier)
	if b.Outstanding.AmountMinor != 600000 {
		t.Fatalf("outstanding = %d, want 600000 (₹6,000)", b.Outstanding.AmountMinor)
	}
	var payables []Payable
	h.admin(http.MethodGet, path("/payables", ""), nil, &payables, http.StatusOK)
	if payables[0].Remaining.AmountMinor != 600000 {
		t.Fatalf("remaining on the bill = %d", payables[0].Remaining.AmountMinor)
	}

	// Verifying the proof moves it from unverified to an advance, and only
	// allocating it settles anything.
	h.admin(http.MethodPost, path(fmt.Sprintf("/payments/%d/verify", unverified), ""), nil, nil, http.StatusOK)
	b = h.balance(supplier)
	if b.Outstanding.AmountMinor != 600000 || b.Advance.AmountMinor != 50000 || b.Unverified.AmountMinor != 0 {
		t.Fatalf("after verifying: %+v", b)
	}

	var entries int
	if err := h.app.DB().QueryRowContext(h.ctxOf(""), `SELECT count(*) FROM retailassistant_audit WHERE event IN ('payment.recorded', 'allocation.made', 'credit.recorded', 'document.approved', 'payment.verified')`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries < 6 {
		t.Errorf("audit lines = %d, want every step written down", entries)
	}
}

// Spec test 5: an advance pays a later bill without counting the cash twice.
func TestAnAdvanceSettlesALaterBillOnce(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	_, supplier := h.post("ADV-0", 100)
	var payables []Payable
	h.admin(http.MethodGet, path("/payables", ""), nil, &payables, http.StatusOK)
	first := payables[0].ID

	advance := h.pay(supplier, 200000, true)
	b := h.balance(supplier)
	if b.Advance.AmountMinor != 200000 || b.Outstanding.AmountMinor != 10000 {
		t.Fatalf("after the advance: %+v", b)
	}
	later, _ := h.post("ADV-1", 5000)
	h.admin(http.MethodPost, path(fmt.Sprintf("/payments/%d/allocate", advance), ""),
		map[string]any{"invoice_id": later, "amount_minor": 200000}, nil, http.StatusCreated)
	b = h.balance(supplier)
	if b.Advance.AmountMinor != 0 || b.Outstanding.AmountMinor != 310000 {
		t.Fatalf("after applying it: %+v", b)
	}
	// There is nothing left of it to spend on the other bill.
	h.admin(http.MethodPost, path(fmt.Sprintf("/payments/%d/allocate", advance), ""),
		map[string]any{"invoice_id": first, "amount_minor": 1}, nil, http.StatusBadRequest)
}

func TestAllocationsCannotOverrunAndARetryRecordsOneThing(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	invoice, supplier := h.post("OVER-1", 100)
	big := h.pay(supplier, 90000, true)
	h.admin(http.MethodPost, path(fmt.Sprintf("/payments/%d/allocate", big), ""),
		map[string]any{"invoice_id": invoice, "amount_minor": 10001}, nil, http.StatusBadRequest)

	req := func() *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]any{"invoice_id": invoice, "amount_minor": 4000})
		r := httptest.NewRequest(http.MethodPost, path(fmt.Sprintf("/payments/%d/allocate", big), ""), bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+gctest.AdminToken)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "same-press")
		rec := httptest.NewRecorder()
		h.app.Handler().ServeHTTP(rec, r)
		return rec
	}
	if rec := req(); rec.Code != http.StatusCreated {
		t.Fatalf("first press = %d %s", rec.Code, rec.Body)
	}
	if rec := req(); rec.Code != http.StatusCreated {
		t.Fatalf("second press = %d %s", rec.Code, rec.Body)
	}
	if n := h.count("", "retailassistant_payment_allocations"); n != 1 {
		t.Fatalf("%d allocations, want 1", n)
	}
}

// Undoing money writes a compensating row and erases nothing.
func TestReversalsCompensateAndKeepHistory(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	invoice, supplier := h.post("REV-1", 100)
	paid := h.pay(supplier, 4000, true)
	h.admin(http.MethodPost, path(fmt.Sprintf("/payments/%d/allocate", paid), ""),
		map[string]any{"invoice_id": invoice, "amount_minor": 4000}, nil, http.StatusCreated)
	if got := h.balance(supplier).Outstanding.AmountMinor; got != 6000 {
		t.Fatalf("outstanding = %d", got)
	}
	// While money is against it, the bill stays.
	h.admin(http.MethodPost, path(fmt.Sprintf("/invoices/%d/reverse", invoice), ""), map[string]string{"reason": "wrong"}, nil, http.StatusConflict)

	h.admin(http.MethodPost, path(fmt.Sprintf("/payments/%d/reverse", paid), ""), nil, nil, http.StatusOK)
	h.admin(http.MethodPost, path(fmt.Sprintf("/payments/%d/reverse", paid), ""), nil, nil, http.StatusConflict)
	b := h.balance(supplier)
	if b.Outstanding.AmountMinor != 10000 || b.Advance.AmountMinor != 0 {
		t.Fatalf("after reversing: %+v", b)
	}
	if n := h.count("", "retailassistant_supplier_payments"); n != 2 {
		t.Fatalf("%d payment rows, want the payment and its reversal", n)
	}
	if n := h.count("", "retailassistant_payment_allocations"); n != 2 {
		t.Fatalf("%d allocation rows, want the allocation and its reversal", n)
	}
	h.admin(http.MethodPost, path(fmt.Sprintf("/payments/%d/allocate", paid), ""),
		map[string]any{"invoice_id": invoice, "amount_minor": 1}, nil, http.StatusConflict)

	h.admin(http.MethodPost, path(fmt.Sprintf("/invoices/%d/reverse", invoice), ""), map[string]string{"reason": "wrong"}, nil, http.StatusOK)
	if got := h.balance(supplier).Outstanding.AmountMinor; got != 0 {
		t.Fatalf("after reversing the bill, outstanding = %d", got)
	}
	if n := h.count("", "retailassistant_purchase_invoices"); n != 1 {
		t.Fatalf("the reversed bill was erased: %d rows", n)
	}
}

// Spec test 6: a bill moves no stock; booking goods in moves it, once.
func TestABillMovesNoStockAndAReceiptMovesItOnce(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	product := gctest.CreateProduct(t, h.app, "LIP-01", 25000, 5)
	variant := product.DefaultVariant()
	onHand := func() int {
		v, err := h.app.Products().GetVariant(h.ctxOf(""), variant.ID)
		if err != nil {
			t.Fatal(err)
		}
		return v.StockOnHand
	}
	invoice, _ := h.post("STK-1", 1180)
	if got := onHand(); got != 5 {
		t.Fatalf("a bill moved stock to %d", got)
	}
	var line int64
	if err := h.app.DB().QueryRowContext(h.ctxOf(""), `SELECT id FROM retailassistant_purchase_lines WHERE invoice_id = $1`, invoice).Scan(&line); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"lines": []map[string]any{{"line_id": line, "variant_id": variant.ID, "quantity": 10}}}
	h.admin(http.MethodPost, path(fmt.Sprintf("/invoices/%d/receive", invoice), ""), body, nil, http.StatusOK)
	if got := onHand(); got != 15 {
		t.Fatalf("stock = %d after receipt, want 15", got)
	}
	h.admin(http.MethodPost, path(fmt.Sprintf("/invoices/%d/receive", invoice), ""), body, nil, http.StatusConflict)
	if got := onHand(); got != 15 {
		t.Fatalf("stock = %d after a repeat, want 15", got)
	}
	// A variant that does not exist frees the line rather than sticking it.
	fresh, _ := h.post("STK-2", 1180)
	var freshLine int64
	if err := h.app.DB().QueryRowContext(h.ctxOf(""), `SELECT id FROM retailassistant_purchase_lines WHERE invoice_id = $1`, fresh).Scan(&freshLine); err != nil {
		t.Fatal(err)
	}
	rec := gctest.AdminRequest(t, h.app, http.MethodPost, path(fmt.Sprintf("/invoices/%d/receive", fresh), ""),
		map[string]any{"lines": []map[string]any{{"line_id": freshLine, "variant_id": 999999, "quantity": 1}}})
	if rec.Code < 400 {
		t.Fatalf("a missing variant = %d", rec.Code)
	}
	var lines []PurchaseLine
	h.admin(http.MethodGet, path(fmt.Sprintf("/invoices/%d/lines", invoice), ""), nil, &lines, http.StatusOK)
	if len(lines) != 1 || lines[0].Receipt == nil || lines[0].Receipt.Quantity != 10 || lines[0].Receipt.Status != "posted" {
		t.Fatalf("lines after a receipt = %+v", lines)
	}
	h.admin(http.MethodGet, path(fmt.Sprintf("/invoices/%d/lines", fresh), ""), nil, &lines, http.StatusOK)
	if len(lines) != 1 || lines[0].Receipt != nil {
		t.Fatalf("a refused receipt still shows: %+v", lines)
	}
	h.admin(http.MethodGet, path("/invoices/999999/lines", ""), nil, nil, http.StatusNotFound)
	if n := h.count("", "retailassistant_stock_receipts"); n != 1 {
		t.Fatalf("%d receipts, want the one that moved stock", n)
	}
}

// Spec test 9: Store B cannot reach Store A's records by id, by naming the store
// it is not on, or through the row policy directly.
func TestStoresCannotReachEachOther(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	invoice, supplier := h.post("ISO-1", 1000)
	docA := h.documents("")[0].ID

	for _, target := range []string{
		fmt.Sprintf("/documents/%d", docA), fmt.Sprintf("/documents/%d/file", docA),
	} {
		h.admin(http.MethodGet, path(target, h.second), nil, nil, http.StatusNotFound)
	}
	if got := h.documents(h.second); len(got) != 0 {
		t.Fatalf("store B lists %d of store A's documents", len(got))
	}
	var suppliers []SupplierBalance
	h.admin(http.MethodGet, path("/suppliers", h.second), nil, &suppliers, http.StatusOK)
	if len(suppliers) != 0 {
		t.Fatalf("store B sees %+v", suppliers)
	}
	h.admin(http.MethodPost, path(fmt.Sprintf("/invoices/%d/reverse", invoice), h.second), map[string]string{"reason": "x"}, nil, http.StatusNotFound)
	h.admin(http.MethodPost, path("/payments", h.second), map[string]any{
		"supplier_id": supplier, "amount_minor": 100, "paid_on": "2026-11-05",
	}, nil, http.StatusNotFound)

	// The row policy itself, with no handler in the way.
	for _, table := range []string{"retailassistant_documents", "retailassistant_purchase_invoices", "retailassistant_suppliers", "retailassistant_phone_links"} {
		if n := h.count(h.second, table); n != 0 {
			t.Errorf("%s: store B reads %d of store A's rows", table, n)
		}
		if n := h.count("", table); n == 0 {
			t.Errorf("%s: store A reads none of its own rows", table)
		}
	}
	if _, err := h.app.DB().ExecContext(h.ctxOf(h.second), `UPDATE retailassistant_purchase_invoices SET invoice_no = 'hijacked'`); err != nil {
		t.Fatal(err)
	}
	var number string
	if err := h.app.DB().QueryRowContext(h.ctxOf(""), `SELECT invoice_no FROM retailassistant_purchase_invoices WHERE id = $1`, invoice).Scan(&number); err != nil || number != "ISO-1" {
		t.Fatalf("store B changed store A's invoice: %q, %v", number, err)
	}

	// Store A's owner, naming store B: naming a store grants nothing.
	owner := gctest.OperatorToken(t, h.app, "a-owner@example.com", gocommerce.RoleOwner)
	if rec := gctest.SessionRequest(t, h.app, owner, http.MethodGet, path("/documents", h.second), nil); rec.Code != http.StatusForbidden {
		t.Errorf("store A's owner reading store B = %d, want 403", rec.Code)
	}
}

// A store that did not opt in behaves as though the module were not there.
func TestAStoreThatDidNotOptInGetsNothing(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	off := false
	if _, err := h.app.Plugins().Update(h.ctxOf(""), pluginKey, gocommerce.PluginPatch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	h.admin(http.MethodGet, path("/documents", ""), nil, nil, http.StatusNotFound)
	var status struct {
		Enabled bool `json:"enabled"`
	}
	h.admin(http.MethodGet, path("/status", ""), nil, &status, http.StatusOK)
	if status.Enabled {
		t.Fatal("status says on")
	}
	h.deliver(h.file("off1", senderA, "m-off", "o.csv", csvInvoice("OFF-1", 100)))
	if n := h.count("", "retailassistant_documents"); n != 0 {
		t.Fatalf("a switched-off store kept %d documents", n)
	}
	// Core goes on selling.
	gctest.PlaceOrder(t, h.app, gocommerce.CodeCOD)
}

// Acknowledgements go only to a number that opted in, once, after commit.
func TestAcknowledgementsAreOptIn(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	h.post("ACK-1", 1000)
	h.work("")
	for _, m := range h.meta.to(senderA) {
		if strings.Contains(m, "waiting for review") || strings.Contains(m, "in your books") {
			t.Fatalf("a number that did not opt in was told %q", m)
		}
	}
	var links []PhoneLink
	h.admin(http.MethodGet, path("/phone-links", ""), nil, &links, http.StatusOK)
	h.admin(http.MethodPatch, path(fmt.Sprintf("/phone-links/%d", links[0].ID), ""), map[string]bool{"notify": true}, nil, http.StatusOK)

	h.post("ACK-2", 1000)
	h.work("")
	h.work("")
	var ack, posted int
	for _, m := range h.meta.to(senderA) {
		if strings.Contains(m, "waiting for review") {
			ack++
		}
		if strings.Contains(m, "You now owe INR 2000.00") {
			posted++
		}
	}
	if ack != 1 || posted != 1 {
		t.Fatalf("acks = %d, posted = %d, in %v", ack, posted, h.meta.to(senderA))
	}
}

func TestTheVerifyHandshake(t *testing.T) {
	h := setup(t)
	rec := gctest.Request(t, h.app, http.MethodGet, "/x/retailassistant/whatsapp/webhook?hub.mode=subscribe&hub.verify_token=verify-me&hub.challenge=abc", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "abc" {
		t.Fatalf("handshake = %d %q", rec.Code, rec.Body)
	}
	rec = gctest.Request(t, h.app, http.MethodGet, "/x/retailassistant/whatsapp/webhook?hub.mode=subscribe&hub.verify_token=wrong&hub.challenge=abc", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a wrong token = %d", rec.Code)
	}
}

func TestRightsAndContract(t *testing.T) {
	h := setup(t)
	gctest.AssertAdminRoutesDeclareRights(t, h.app, moduleName)
	gctest.AssertSpecCoversModuleRoutes(t, h.app, moduleName)

	staff := gctest.OperatorToken(t, h.app, "staff@example.com", gocommerce.RoleStaff)
	if rec := gctest.SessionRequest(t, h.app, staff, http.MethodGet, path("/documents", ""), nil); rec.Code != http.StatusForbidden {
		t.Errorf("staff reading supplier accounts = %d, want 403", rec.Code)
	}
	manager := gctest.OperatorToken(t, h.app, "manager@example.com", gocommerce.RoleManager)
	if rec := gctest.SessionRequest(t, h.app, manager, http.MethodGet, path("/documents", ""), nil); rec.Code != http.StatusOK {
		t.Errorf("a manager reading supplier accounts = %d, want 200", rec.Code)
	}
	if rec := gctest.SessionRequest(t, h.app, manager, http.MethodPost, path("/phone-links", ""), map[string]string{"phone": "+919800000011"}); rec.Code != http.StatusForbidden {
		t.Errorf("a manager linking a phone = %d, want 403: only the owner decides who can post", rec.Code)
	}
}

func TestSecretsAreKeptFromReports(t *testing.T) {
	mod := New(Config{})
	app := gctest.NewWithReports(t, mod)
	gctest.AssertReportsCannotRead(t, app, "retailassistant_phone_links", "code_hash")
	gctest.AssertReportsCannotRead(t, app, "retailassistant_documents", "content")
}
