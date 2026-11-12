package retailassistant

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/itswadesh/gocommerce/gctest"
)

var testFileKey = base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))

func (h *harness) stored(id int64) (content []byte, sealed bool) {
	h.t.Helper()
	if err := h.app.DB().QueryRowContext(h.ctxOf(""), `SELECT content, content_sealed FROM retailassistant_documents WHERE id = $1`, id).Scan(&content, &sealed); err != nil {
		h.t.Fatal(err)
	}
	return content, sealed
}

func (h *harness) arrive(number string) Document {
	h.t.Helper()
	h.deliver(h.file("msg-"+number, senderA, "media-"+number, number+".csv", csvInvoice(number, 1180)))
	h.work("")
	return h.documents("")[0]
}

func TestFilesAreSealedAndOnlyOpenOnTheirOwnRow(t *testing.T) {
	h := setupWith(t, func(c *Config) { c.FileKey = testFileKey })
	h.link("", senderA)
	first := h.arrive("SEAL-1")
	content, sealed := h.stored(first.ID)
	if !sealed || strings.Contains(string(content), "ABC Cosmetics") {
		t.Fatalf("sealed = %v, and the bytes hold the bill in clear: %q", sealed, content)
	}
	rec := gctest.AdminRequest(t, h.app, http.MethodGet, path(fmt.Sprintf("/documents/%d/file", first.ID), ""), nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ABC Cosmetics") {
		t.Fatalf("download = %d %q", rec.Code, rec.Body.String())
	}
	if first.Draft == nil || first.Draft.InvoiceNo != "SEAL-1" {
		t.Fatalf("the reader did not see the plain file: %+v", first.Draft)
	}
	// Bytes moved to another row must not show up as that row's bill.
	second := h.arrive("SEAL-2")
	if _, err := h.app.DB().ExecContext(h.ctxOf(""), `UPDATE retailassistant_documents SET content = $1 WHERE id = $2`, content, second.ID); err != nil {
		t.Fatal(err)
	}
	h.admin(http.MethodGet, path(fmt.Sprintf("/documents/%d/file", second.ID), ""), nil, nil, http.StatusConflict)
}

func TestABadKeyKeepsNoFileInClearAndDoesNotStopTheEngine(t *testing.T) {
	h := setupWith(t, func(c *Config) { c.FileKey = "too-short" })
	h.link("", senderA)
	d := h.arrive("BAD-KEY")
	content, _ := h.stored(d.ID)
	if d.Status != "failed" || content != nil || !strings.Contains(d.LastError, "RETAIL_ASSISTANT_FILE_KEY") {
		t.Fatalf("status = %s, error = %q, %d bytes kept", d.Status, d.LastError, len(content))
	}
}

// clamd is a stand-in that speaks INSTREAM and flags any file holding a marker.
func clamd(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				r := bufio.NewReader(conn)
				if cmd, err := r.ReadString(0); err != nil || cmd != "zINSTREAM\x00" {
					return
				}
				var body strings.Builder
				for {
					var size uint32
					if binary.Read(r, binary.BigEndian, &size) != nil || size == 0 {
						break
					}
					if _, err := io.CopyN(&body, r, int64(size)); err != nil {
						return
					}
				}
				if strings.Contains(body.String(), "INFECTED") {
					_, _ = conn.Write([]byte("stream: Test.Virus FOUND\x00"))
					return
				}
				_, _ = conn.Write([]byte("stream: OK\x00"))
			}()
		}
	}()
	return l.Addr().String()
}

func TestAFlaggedFileIsNotKept(t *testing.T) {
	h := setupWith(t, func(c *Config) { c.ClamdAddr = clamd(t) })
	h.link("", senderA)
	clean := h.arrive("CLEAN-1")
	if clean.Status != "needs_review" {
		t.Fatalf("a clean file is %s: %s", clean.Status, clean.LastError)
	}
	h.deliver(h.file("msg-bad", senderA, "media-bad", "bad.csv", []byte("INFECTED")))
	h.work("")
	bad := h.documents("")[0]
	content, _ := h.stored(bad.ID)
	if bad.Status != "failed" || content != nil || !strings.Contains(bad.LastError, "Test.Virus") {
		t.Fatalf("status = %s, error = %q, %d bytes kept", bad.Status, bad.LastError, len(content))
	}
}

func TestNoFileIsKeptUnscannedWhileTheScannerIsDown(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	h := setupWith(t, func(c *Config) { c.ClamdAddr = addr })
	h.link("", senderA)
	d := h.arrive("DOWN-1")
	content, _ := h.stored(d.ID)
	if d.Status != "parsing" || content != nil || !strings.Contains(d.LastError, "virus scanner") {
		t.Fatalf("status = %s, error = %q, %d bytes kept", d.Status, d.LastError, len(content))
	}
}

func TestRetentionTakesFilesAndLeavesTheBooks(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	_, supplier := h.post("OLD-1", 1180)
	posted := h.documents("")[0]
	rejected := h.arrive("OLD-2")
	h.admin(http.MethodPost, path(fmt.Sprintf("/documents/%d/reject", rejected.ID), ""), map[string]int{"version": rejected.Version}, nil, http.StatusOK)
	waiting := h.arrive("OLD-3")
	recent := h.arrive("NEW-1")
	h.admin(http.MethodPost, path(fmt.Sprintf("/documents/%d/reject", recent.ID), ""), map[string]int{"version": recent.Version}, nil, http.StatusOK)
	if _, err := h.app.DB().ExecContext(h.ctxOf(""), `
		UPDATE retailassistant_documents SET reviewed_at = now() - interval '40 days', created_at = now() - interval '40 days' WHERE id <> $1`, recent.ID); err != nil {
		t.Fatal(err)
	}
	pass := func() {
		t.Helper()
		if err := h.mod.RetentionPass(h.ctxOf("")); err != nil {
			t.Fatal(err)
		}
	}
	before := h.balance(supplier).Outstanding.AmountMinor

	pass()
	if c, _ := h.stored(posted.ID); c == nil {
		t.Fatal("with no retention set, a file was removed")
	}

	h.setRetention(30)
	pass()
	if c, _ := h.stored(posted.ID); c != nil {
		t.Error("a posted file past retention was kept")
	}
	if c, _ := h.stored(rejected.ID); c != nil {
		t.Error("a rejected file past retention was kept")
	}
	if c, _ := h.stored(waiting.ID); c == nil {
		t.Error("a file still in review was removed")
	}
	if c, _ := h.stored(recent.ID); c == nil {
		t.Error("a recent file was removed")
	}
	var got Document
	h.admin(http.MethodGet, path(fmt.Sprintf("/documents/%d", rejected.ID), ""), nil, &got, http.StatusOK)
	if got.Draft != nil || got.FileRemovedAt == nil {
		t.Errorf("a rejected document kept what was read from it: %+v", got)
	}
	h.admin(http.MethodGet, path(fmt.Sprintf("/documents/%d", posted.ID), ""), nil, &got, http.StatusOK)
	if got.Draft == nil {
		t.Error("a posted document lost what was read from it")
	}
	if after := h.balance(supplier).Outstanding.AmountMinor; after != before {
		t.Errorf("retention changed a balance from %d to %d", before, after)
	}
	h.admin(http.MethodGet, path(fmt.Sprintf("/documents/%d/file", posted.ID), ""), nil, nil, http.StatusNotFound)
}

func TestAFileIsDeletedOnRequestOnlyAfterReview(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	d := h.arrive("DEL-1")
	h.admin(http.MethodPost, path(fmt.Sprintf("/documents/%d/remove-file", d.ID), ""), nil, nil, http.StatusConflict)
	h.admin(http.MethodPost, path(fmt.Sprintf("/documents/%d/reject", d.ID), ""), map[string]int{"version": d.Version}, nil, http.StatusOK)
	h.admin(http.MethodPost, path(fmt.Sprintf("/documents/%d/remove-file", d.ID), ""), nil, nil, http.StatusOK)
	if c, _ := h.stored(d.ID); c != nil {
		t.Fatal("the file is still there")
	}
	var audits int
	if err := h.app.DB().QueryRowContext(h.ctxOf(""), `SELECT count(*) FROM retailassistant_audit WHERE event = 'document.file_removed'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("%d audit rows", audits)
	}
}

func TestOnlyADocumentOutsideTheBooksCanBeDeleted(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	invoice, _ := h.post("DEL-2", 1180)
	var posted int64
	if err := h.app.DB().QueryRowContext(h.ctxOf(""),
		`SELECT document_id FROM retailassistant_purchase_invoices WHERE id = $1`, invoice).Scan(&posted); err != nil {
		t.Fatal(err)
	}
	h.admin(http.MethodDelete, path(fmt.Sprintf("/documents/%d", posted), ""), nil, nil, http.StatusConflict)

	d := h.arrive("DEL-3")
	h.admin(http.MethodDelete, path(fmt.Sprintf("/documents/%d", d.ID), ""), nil, nil, http.StatusOK)
	h.admin(http.MethodGet, path(fmt.Sprintf("/documents/%d", d.ID), ""), nil, nil, http.StatusNotFound)
	h.admin(http.MethodDelete, path(fmt.Sprintf("/documents/%d", d.ID), ""), nil, nil, http.StatusNotFound)

	// Meta redelivers at least once; the kept message row stops it coming back.
	h.deliver(h.file("msg-DEL-3", senderA, "media-DEL-3", "DEL-3.csv", csvInvoice("DEL-3", 1180)))
	h.work("")
	for _, left := range h.documents("") {
		if left.ID == d.ID || left.Filename == "DEL-3.csv" {
			t.Fatalf("the deleted document came back: %+v", left)
		}
	}
	var audits int
	if err := h.app.DB().QueryRowContext(h.ctxOf(""),
		`SELECT count(*) FROM retailassistant_audit WHERE event = 'document.deleted' AND entity_id = $1`, d.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("%d audit rows", audits)
	}
}

func TestExportsAgreeWithTheBooksAndCannotRunAsFormulas(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	_, supplier := h.post("EXP-1", 1180)
	h.pay(supplier, 50000, true)
	if _, err := h.app.DB().ExecContext(h.ctxOf(""), `UPDATE retailassistant_suppliers SET name = '=HYPERLINK("x")' WHERE id = $1`, supplier); err != nil {
		t.Fatal(err)
	}
	get := func(kind string) string {
		t.Helper()
		rec := gctest.AdminRequest(t, h.app, http.MethodGet, path("/export/"+kind, ""), nil)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") {
			t.Fatalf("%s = %d %s", kind, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	if s := get("suppliers"); !strings.Contains(s, `"'=HYPERLINK(""x"")"`) || !strings.Contains(s, "1180.00") {
		t.Errorf("suppliers export:\n%s", s)
	}
	if s := get("bills"); !strings.Contains(s, "EXP-1") || !strings.Contains(s, "1180.00") {
		t.Errorf("bills export:\n%s", s)
	}
	if s := get("payments"); !strings.Contains(s, "500.00") {
		t.Errorf("payments export:\n%s", s)
	}
	if s := get("credits"); !strings.Contains(s, "Credit note number") {
		t.Errorf("credits export:\n%s", s)
	}
	h.admin(http.MethodGet, path("/export/passwords", ""), nil, nil, http.StatusNotFound)
}
