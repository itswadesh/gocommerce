package retailassistant

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"mime"
	"net/http"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// csvText keeps a spreadsheet from running a cell as a formula. Supplier names and
// references come off documents strangers wrote, so a name like =HYPERLINK(...)
// must arrive as text in the owner's Excel.
func csvText(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func nullAmount(n sql.NullInt64, currency string) string {
	if !n.Valid {
		return ""
	}
	return plainAmount(n.Int64, currency)
}

// exports are the files the owner's accountant asks for. Each has one query, so
// a figure in a file is the figure on the screen and is not worked out twice.
var exports = map[string]func(*Module, context.Context) ([][]string, error){
	"suppliers": (*Module).exportSuppliers,
	"bills":     (*Module).exportBills,
	"payments":  (*Module).exportPayments,
	"credits":   (*Module).exportCredits,
}

// handleExport answers with a CSV, one of the few responses that is not the JSON
// envelope, as a download like the document's own file.
func (m *Module) handleExport(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	build, ok := exports[kind]
	if !ok {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no export called %q; choose suppliers, bills, payments or credits", kind))
		return
	}
	rows, err := build(m, r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "export %s", kind))
		return
	}
	var b bytes.Buffer
	// Excel reads a CSV without this mark as the machine's code page, and ₹ and
	// Hindi names arrive as nonsense.
	b.WriteString("\xef\xbb\xbf")
	if err := csv.NewWriter(&b).WriteAll(rows); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "export %s", kind))
		return
	}
	name := "supplier-" + kind + "-" + time.Now().Format("2006-01-02") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	_, _ = w.Write(b.Bytes())
}

func (m *Module) exportSuppliers(ctx context.Context) ([][]string, error) {
	balances, err := m.supplierBalances(ctx)
	if err != nil {
		return nil, err
	}
	out := [][]string{{"Supplier", "GSTIN", "Currency", "Billed", "Credit notes", "Paid", "You owe", "Paid in advance", "Not verified"}}
	for _, b := range balances {
		gstin := ""
		if b.GSTIN != nil {
			gstin = *b.GSTIN
		}
		cur := b.Outstanding.Currency
		out = append(out, []string{csvText(b.Name), csvText(gstin), cur,
			plainAmount(b.Invoiced.AmountMinor, cur), plainAmount(b.Credits.AmountMinor, cur), plainAmount(b.Paid.AmountMinor, cur),
			plainAmount(b.Outstanding.AmountMinor, cur), plainAmount(b.Advance.AmountMinor, cur), plainAmount(b.Unverified.AmountMinor, cur)})
	}
	return out, nil
}

func (m *Module) exportBills(ctx context.Context) ([][]string, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT s.name, COALESCE(s.gstin, ''), i.invoice_no, to_char(i.invoice_date, 'YYYY-MM-DD'), i.currency,
		       i.subtotal_minor, i.tax_minor, i.total_minor,
		       i.total_minor - COALESCE((SELECT sum(a.amount_minor) FROM retailassistant_payment_allocations a WHERE a.invoice_id = i.id), 0),
		       i.reversed_at IS NOT NULL
		FROM retailassistant_purchase_invoices i JOIN retailassistant_suppliers s ON s.id = i.supplier_id
		ORDER BY i.invoice_date, i.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := [][]string{{"Supplier", "GSTIN", "Bill number", "Bill date", "Currency", "Subtotal", "Tax", "Total", "Left to pay", "Reversed"}}
	for rows.Next() {
		var name, gstin, no, date, cur string
		var sub, tax sql.NullInt64
		var total, left int64
		var reversed bool
		if err := rows.Scan(&name, &gstin, &no, &date, &cur, &sub, &tax, &total, &left, &reversed); err != nil {
			return nil, err
		}
		out = append(out, []string{csvText(name), csvText(gstin), csvText(no), date, cur,
			nullAmount(sub, cur), nullAmount(tax, cur), plainAmount(total, cur), plainAmount(left, cur), yesNo(reversed)})
	}
	return out, rows.Err()
}

func (m *Module) exportPayments(ctx context.Context) ([][]string, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT s.name, to_char(p.paid_on, 'YYYY-MM-DD'), p.currency, p.amount_minor, p.method, p.reference, p.status, p.reverses_id IS NOT NULL
		FROM retailassistant_supplier_payments p JOIN retailassistant_suppliers s ON s.id = p.supplier_id
		ORDER BY p.paid_on, p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := [][]string{{"Supplier", "Paid on", "Currency", "Amount", "Paid by", "Reference", "Status", "Reverses an earlier payment"}}
	for rows.Next() {
		var name, date, cur, method, reference, status string
		var amount int64
		var reversal bool
		if err := rows.Scan(&name, &date, &cur, &amount, &method, &reference, &status, &reversal); err != nil {
			return nil, err
		}
		out = append(out, []string{csvText(name), date, cur, plainAmount(amount, cur), csvText(method), csvText(reference), status, yesNo(reversal)})
	}
	return out, rows.Err()
}

func (m *Module) exportCredits(ctx context.Context) ([][]string, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT s.name, to_char(c.created_at, 'YYYY-MM-DD'), c.currency, c.amount_minor, c.kind, c.credit_no, c.reason, c.reverses_id IS NOT NULL
		FROM retailassistant_supplier_credits c JOIN retailassistant_suppliers s ON s.id = c.supplier_id
		ORDER BY c.created_at, c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := [][]string{{"Supplier", "Recorded on", "Currency", "Amount", "Kind", "Credit note number", "Reason", "Reverses an earlier credit note"}}
	for rows.Next() {
		var name, date, cur, kind, no, reason string
		var amount int64
		var reversal bool
		if err := rows.Scan(&name, &date, &cur, &amount, &kind, &no, &reason, &reversal); err != nil {
			return nil, err
		}
		out = append(out, []string{csvText(name), date, cur, plainAmount(amount, cur), kind, csvText(no), csvText(reason), yesNo(reversal)})
	}
	return out, rows.Err()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
