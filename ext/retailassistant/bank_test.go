package retailassistant

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/misiki-in/gocommerce/gctest"
)

const statementCSV = `Statement of account,,,,,
Account,XXXX1234,,,,
Date,Narration,Chq./Ref.No.,Value Dt,Withdrawal Amt.,Deposit Amt.,Closing Balance
05/11/26,UPI-ABC COSMETICS-UTR777,UTR777,05/11/26,"3,000.00",,"47,000.00"
06/11/26,SALARY CREDIT,,06/11/26,,"10,000.00","57,000.00"
07/11/26,NEFT SOMEONE,N123,07/11/26,1500.00,,"55,500.00"
not a date,BROKEN ROW,,,250.00,,
`

func (h *harness) importStatement(body string) map[string]int {
	h.t.Helper()
	rec := gctest.AdminUpload(h.t, h.app, http.MethodPost, path("/bank-statements", ""), "text/csv", body)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("import = %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]int
	gctest.DecodeData(h.t, rec, &out)
	return out
}

func (h *harness) bankLines(status string) []BankLine {
	h.t.Helper()
	var out []BankLine
	h.admin(http.MethodGet, path("/bank-lines?status="+status, ""), nil, &out, http.StatusOK)
	return out
}

func (h *harness) payOn(supplier, minor int64, on, reference string, verified bool) int64 {
	h.t.Helper()
	var out struct {
		ID int64 `json:"id"`
	}
	h.admin(http.MethodPost, path("/payments", ""), map[string]any{
		"supplier_id": supplier, "amount_minor": minor, "paid_on": on, "reference": reference, "verified": verified,
	}, &out, http.StatusCreated)
	return out.ID
}

func TestParseStatementReadsMoneyOutOnly(t *testing.T) {
	rows, skipped, err := parseStatement([]byte(statementCSV), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || skipped != 1 {
		t.Fatalf("%d rows, %d skipped: %+v", len(rows), skipped, rows)
	}
	if rows[0].AmountMinor != 300000 || rows[0].Date != "2026-11-05" || rows[0].Reference != "UTR777" {
		t.Fatalf("first row = %+v", rows[0])
	}
}

func TestParseStatementWithASingleAmountColumn(t *testing.T) {
	csv := "Txn Date,Description,Amount,Dr/Cr\n05/11/2026,PAID A,100.00,DR\n05/11/2026,GOT B,900.00,CR\n"
	rows, _, err := parseStatement([]byte(csv), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].AmountMinor != 10000 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestAFileThatIsNotAStatementIsRefused(t *testing.T) {
	h := setup(t)
	rec := gctest.AdminUpload(t, h.app, http.MethodPost, path("/bank-statements", ""), "text/csv", "name,age\nabc,12\n")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("= %d", rec.Code)
	}
}

func TestReimportingAStatementAddsOnlyWhatIsNew(t *testing.T) {
	h := setup(t)
	first := h.importStatement(statementCSV)
	if first["added"] != 2 || first["unreadable"] != 1 {
		t.Fatalf("first = %v", first)
	}
	again := h.importStatement(statementCSV)
	if again["added"] != 0 || again["already_here"] != 2 {
		t.Fatalf("again = %v", again)
	}
	// Two equal payments on one day are two payments.
	twice := "Date,Narration,Withdrawal Amt.\n08/11/26,CASH,100.00\n08/11/26,CASH,100.00\n"
	if got := h.importStatement(twice); got["added"] != 2 {
		t.Fatalf("twice = %v", got)
	}
}

func TestAStatementLineVerifiesAPaymentAndUndoingTakesThatBack(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	invoice, supplier := h.post("BANK-1", 11800)
	paid := h.payOn(supplier, 300000, "2026-11-04", "UTR777", false)
	if got := h.balance(supplier).Unverified.AmountMinor; got != 300000 {
		t.Fatalf("unverified = %d", got)
	}
	h.importStatement(statementCSV)
	lines := h.bankLines("unmatched")
	var line BankLine
	for _, l := range lines {
		if l.Amount.AmountMinor == 300000 {
			line = l
		}
	}
	if len(line.Candidates) != 1 || line.Candidates[0].PaymentID != paid || !line.Candidates[0].SameReference {
		t.Fatalf("candidates = %+v", line.Candidates)
	}
	match := path(fmt.Sprintf("/bank-lines/%d/match", line.ID), "")
	h.admin(http.MethodPost, match, map[string]int64{"payment_id": paid}, nil, http.StatusOK)
	b := h.balance(supplier)
	if b.Unverified.AmountMinor != 0 || b.Advance.AmountMinor != 300000 {
		t.Fatalf("after matching: %+v", b)
	}
	// Once only, either way round.
	h.admin(http.MethodPost, match, map[string]int64{"payment_id": paid}, nil, http.StatusConflict)
	other := h.bankLines("unmatched")[0]
	h.admin(http.MethodPost, path(fmt.Sprintf("/bank-lines/%d/match", other.ID), ""), map[string]int64{"payment_id": paid}, nil, http.StatusConflict)

	h.admin(http.MethodPost, path(fmt.Sprintf("/payments/%d/allocate", paid), ""), map[string]any{"invoice_id": invoice, "amount_minor": 100000}, nil, http.StatusCreated)
	undo := path(fmt.Sprintf("/bank-lines/%d/undo", line.ID), "")
	h.admin(http.MethodPost, undo, nil, nil, http.StatusConflict)
	var alloc int64
	if err := h.app.DB().QueryRowContext(h.ctxOf(""), `SELECT id FROM retailassistant_payment_allocations WHERE payment_id = $1`, paid).Scan(&alloc); err != nil {
		t.Fatal(err)
	}
	h.admin(http.MethodPost, path(fmt.Sprintf("/allocations/%d/reverse", alloc), ""), nil, nil, http.StatusOK)
	h.admin(http.MethodPost, undo, nil, nil, http.StatusOK)
	if got := h.balance(supplier).Unverified.AmountMinor; got != 300000 {
		t.Fatalf("after undoing, unverified = %d", got)
	}
}

func TestAPaymentAlreadyVerifiedStaysSoWhenAMatchIsUndone(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	_, supplier := h.post("BANK-2", 11800)
	paid := h.payOn(supplier, 300000, "2026-11-05", "", true)
	h.importStatement(statementCSV)
	var line BankLine
	for _, l := range h.bankLines("unmatched") {
		if l.Amount.AmountMinor == 300000 {
			line = l
		}
	}
	h.admin(http.MethodPost, path(fmt.Sprintf("/bank-lines/%d/match", line.ID), ""), map[string]int64{"payment_id": paid}, nil, http.StatusOK)
	h.admin(http.MethodPost, path(fmt.Sprintf("/bank-lines/%d/undo", line.ID), ""), nil, nil, http.StatusOK)
	if got := h.balance(supplier).Advance.AmountMinor; got != 300000 {
		t.Fatalf("advance = %d", got)
	}
}

func TestAWrongAmountIsRefusedAndNothingVerifies(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	_, supplier := h.post("BANK-3", 11800)
	paid := h.payOn(supplier, 299999, "2026-11-05", "", false)
	h.importStatement(statementCSV)
	line := h.bankLines("unmatched")[0]
	h.admin(http.MethodPost, path(fmt.Sprintf("/bank-lines/%d/match", line.ID), ""), map[string]int64{"payment_id": paid}, nil, http.StatusConflict)
	if got := h.balance(supplier).Unverified.AmountMinor; got != 299999 {
		t.Fatalf("unverified = %d", got)
	}
}

func TestALineNobodyRecordedBecomesAVerifiedPaymentOnce(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	_, supplier := h.post("BANK-4", 11800)
	h.importStatement(statementCSV)
	line := h.bankLines("unmatched")[0]
	record := path(fmt.Sprintf("/bank-lines/%d/record", line.ID), "")
	h.admin(http.MethodPost, record, map[string]int64{"supplier_id": supplier}, nil, http.StatusOK)
	h.admin(http.MethodPost, record, map[string]int64{"supplier_id": supplier}, nil, http.StatusConflict)
	if got := h.balance(supplier).Advance.AmountMinor; got != line.Amount.AmountMinor {
		t.Fatalf("advance = %d, line = %d", got, line.Amount.AmountMinor)
	}
	if n := h.count("", "retailassistant_supplier_payments"); n != 1 {
		t.Fatalf("%d payments", n)
	}
}

func TestStoresCannotReachEachOthersStatements(t *testing.T) {
	h := setup(t)
	h.importStatement(statementCSV)
	line := h.bankLines("unmatched")[0]
	var theirs []BankLine
	h.admin(http.MethodGet, path("/bank-lines", h.second), nil, &theirs, http.StatusOK)
	if len(theirs) != 0 {
		t.Fatalf("Store B reads %d of Store A's lines", len(theirs))
	}
	h.admin(http.MethodPost, path(fmt.Sprintf("/bank-lines/%d/ignore", line.ID), h.second), nil, nil, http.StatusNotFound)
}

func TestInsightsTellTheTruthAboutWhatIsKnown(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	invoice, supplier := h.post("INS-1", 11800)
	h.pay(supplier, 300000, true)
	paid := h.pay(supplier, 100000, false)
	_ = paid
	_ = invoice
	product := gctest.CreateProduct(t, h.app, "LOW-1", 25000, 3)

	var got Insights
	h.admin(http.MethodGet, path("/insights", ""), nil, &got, http.StatusOK)
	if got.Ledger.Outstanding.AmountMinor != 1180000 || got.Ledger.Unverified.AmountMinor != 100000 || got.Ledger.UnverifiedCount != 1 {
		t.Fatalf("ledger = %+v", got.Ledger)
	}
	if got.Ledger.LinesNotBookedIn != 1 || len(got.Ledger.TopOwed) != 1 || got.Ledger.TopOwed[0].ID != supplier {
		t.Fatalf("ledger = %+v", got.Ledger)
	}
	if got.Stock == nil || len(got.Stock.LowStock) != 1 || got.Stock.LowStock[0].VariantID != product.DefaultVariant().ID {
		t.Fatalf("stock = %+v", got.Stock)
	}
	// A shop with no sales recorded has not "sold nothing".
	if got.Stock.SlowWithheld == "" || len(got.Stock.SlowStock) != 0 {
		t.Fatalf("slow stock claimed with no sales records: %+v", got.Stock)
	}
	if got.Stock.ItemsWithNoCost != 1 {
		t.Fatalf("items with no cost = %d", got.Stock.ItemsWithNoCost)
	}

	// Sales records too recent: still withheld.
	gctest.Buy(t, h.app, "cod", product.DefaultVariant().ID, 1)
	h.admin(http.MethodGet, path("/insights", ""), nil, &got, http.StatusOK)
	if got.Stock.Sales90d != 1 || got.Stock.SlowWithheld == "" {
		t.Fatalf("stock = %+v", got.Stock)
	}
}

func TestSlowStockAppearsOnceRecordsReachFarEnough(t *testing.T) {
	h := setup(t)
	sold := gctest.CreateProduct(t, h.app, "SOLD-1", 25000, 20)
	slow := gctest.CreateProduct(t, h.app, "SLOW-1", 25000, 20)
	gctest.Buy(t, h.app, "cod", sold.DefaultVariant().ID, 1)
	gctest.Buy(t, h.app, "cod", sold.DefaultVariant().ID, 1)
	// One order old enough to prove the records reach back; one recent.
	if _, err := h.app.DB().ExecContext(h.ctxOf(""), `
		UPDATE orders SET created_at = now() - interval '200 days' WHERE id = (SELECT min(id) FROM orders)`); err != nil {
		t.Fatal(err)
	}
	var got Insights
	h.admin(http.MethodGet, path("/insights", ""), nil, &got, http.StatusOK)
	if got.Stock.SlowWithheld != "" || len(got.Stock.SlowStock) != 1 || got.Stock.SlowStock[0].VariantID != slow.DefaultVariant().ID {
		t.Fatalf("stock = %+v", got.Stock)
	}
}

func TestSomeoneWhoCannotReadOrdersGetsTheLedgerOnly(t *testing.T) {
	h := setup(t)
	token := gctest.AgentKey(t, h.app, rightRead)
	rec := gctest.SessionRequest(t, h.app, token, http.MethodGet, path("/insights", ""), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	var got Insights
	gctest.DecodeData(t, rec, &got)
	if got.Stock != nil {
		t.Fatalf("stock figures reached somebody without the right: %+v", got.Stock)
	}
}

func TestTheMorningSummaryIsOptInAndComesOnceADay(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	h.optIn()
	_, _ = h.post("SUM-1", 11800)
	h.work("")
	before := len(h.meta.to(senderA))
	morning := time.Date(2026, 11, 6, 9, 0, 0, 0, ist)
	pass := func(at time.Time) {
		t.Helper()
		if err := h.mod.SummaryPass(h.ctxOf(""), at); err != nil {
			t.Fatal(err)
		}
		h.work("")
	}
	pass(morning)
	if got := len(h.meta.to(senderA)); got != before {
		t.Fatalf("%d messages with the summary off", got-before)
	}
	h.setSetting("morning_summary", true)
	pass(morning.Add(-3 * time.Hour))
	if got := len(h.meta.to(senderA)); got != before {
		t.Fatal("sent before the hour")
	}
	pass(morning)
	pass(morning.Add(time.Hour))
	msgs := h.meta.to(senderA)
	if len(msgs) != before+1 || !strings.Contains(msgs[len(msgs)-1], "11800.00") {
		t.Fatalf("messages = %q", msgs[before:])
	}
	pass(morning.Add(24 * time.Hour))
	if got := len(h.meta.to(senderA)); got != before+2 {
		t.Fatalf("the next day: %d", got-before)
	}
}
