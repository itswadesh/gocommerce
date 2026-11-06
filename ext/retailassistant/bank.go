package retailassistant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const maxStatementBytes = 5 << 20

// The words Indian banks put over a statement's columns. A statement with no date
// and no money-out column is refused rather than guessed at.
var bankColumns = map[string]string{
	"date": "date", "txn date": "date", "tran date": "date", "transaction date": "date", "value date": "date", "value dt": "date", "post date": "date",
	"narration": "narration", "description": "narration", "particulars": "narration", "remarks": "narration", "transaction remarks": "narration", "details": "narration",
	"reference": "reference", "ref no": "reference", "chq ref no": "reference", "chq no": "reference", "cheque no": "reference", "utr": "reference", "utr no": "reference", "transaction id": "reference", "ref no cheque no": "reference",
	"debit": "debit", "withdrawal": "debit", "withdrawal amt": "debit", "withdrawal amount": "debit", "withdrawals": "debit", "dr": "debit", "debit amount": "debit", "withdrawal amt inr": "debit", "withdrawal amount inr": "debit",
	"amount": "amount", "transaction amount": "amount", "amount inr": "amount",
	"dr cr": "direction", "cr dr": "direction", "type": "direction", "debit credit": "direction",
}

// BankRow is one payment out of the shop's account, as its statement says it.
type BankRow struct {
	Date, Narration, Reference string
	AmountMinor                int64
}

// parseStatement reads a bank's CSV. Only money going out is kept: this ledger is
// what the shop pays suppliers. A row whose date or amount does not read is
// counted and left out, never guessed, so the person can see the file was not
// read in full.
func parseStatement(data []byte, exponent int) (rows []BankRow, skipped int, err error) {
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))))
	r.FieldsPerRecord, r.LazyQuotes = -1, true
	var columns map[string]int
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, 0, gocommerce.Validationf("this file could not be read as a CSV: %v", err)
		}
		if columns == nil {
			// Banks print the account's details above the table.
			columns = statementHeader(rec)
			continue
		}
		cell := func(name string) string {
			if i, ok := columns[name]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		text := cell("debit")
		if _, single := columns["amount"]; single {
			if d := strings.ToLower(cell("direction")); !strings.HasPrefix(d, "d") && !strings.HasPrefix(d, "w") {
				if cell("amount") != "" {
					continue
				}
			}
			text = cell("amount")
		}
		if cell("date") == "" && text == "" {
			continue
		}
		if text == "" || isZero(text) {
			continue
		}
		date, okDate := parseDate(cell("date"))
		amount, err := parseMoney(text, exponent)
		if !okDate || err != nil || amount <= 0 {
			skipped++
			continue
		}
		rows = append(rows, BankRow{Date: date, Narration: cell("narration"), Reference: cell("reference"), AmountMinor: amount})
	}
	if columns == nil {
		return nil, 0, gocommerce.Validationf("this does not look like a bank statement: no row names a date and a withdrawal or debit column")
	}
	return rows, skipped, nil
}

func isZero(s string) bool {
	return strings.Trim(s, "0., -") == ""
}

func statementHeader(rec []string) map[string]int {
	found := map[string]int{}
	for i, cell := range rec {
		if name, ok := bankColumns[labelOf(cell)]; ok {
			if _, taken := found[name]; !taken {
				found[name] = i
			}
		}
	}
	_, date := found["date"]
	_, debit := found["debit"]
	_, amount := found["amount"]
	_, direction := found["direction"]
	if date && (debit || (amount && direction)) {
		return found
	}
	return nil
}

// rowHash names a statement line, so importing an overlapping statement adds only
// what is new. The occurrence counts identical lines inside one file: two equal
// payments on one day with no reference are two payments.
func rowHash(r BankRow, occurrence int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d", r.Date, r.Narration, r.Reference, r.AmountMinor, occurrence)))
	return hex.EncodeToString(sum[:])
}

func (m *Module) handleImportStatement(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxStatementBytes))
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Validationf("a statement can be up to %d MB", maxStatementBytes>>20))
		return
	}
	ctx := r.Context()
	currency := m.app.Currency(ctx)
	rows, skipped, err := parseStatement(data, gocommerce.CurrencyExponent(currency))
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	added := 0
	seen := map[string]int{}
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		for _, row := range rows {
			plain := rowHash(row, 0)
			seen[plain]++
			res, err := tx.ExecContext(ctx, `
				INSERT INTO retailassistant_bank_lines (line_date, narration, reference, amount_minor, currency, line_hash, imported_by)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				ON CONFLICT (store_id, line_hash) DO NOTHING`,
				row.Date, row.Narration, row.Reference, row.AmountMinor, currency, rowHash(row, seen[plain]), actor(ctx))
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			added += int(n)
		}
		return nil
	})
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "import a bank statement"))
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]int{"added": added, "already_here": len(rows) - added, "unreadable": skipped})
}

// BankLine is a line from a statement and the payments it might be.
type BankLine struct {
	ID         int64            `json:"id"`
	Date       string           `json:"date"`
	Narration  string           `json:"narration"`
	Reference  string           `json:"reference"`
	Amount     gocommerce.Money `json:"amount"`
	Status     string           `json:"status"`
	PaymentID  *int64           `json:"payment_id"`
	Candidates []BankCandidate  `json:"candidates"`
}

// BankCandidate is a recorded payment of exactly this amount, near this date.
// It is a suggestion: a person says which one it is.
type BankCandidate struct {
	PaymentID    int64  `json:"payment_id"`
	SupplierName string `json:"supplier_name"`
	PaidOn       string `json:"paid_on"`
	Reference    string `json:"reference"`
	Status       string `json:"status"`
	// SameReference means the statement names this payment's own reference.
	SameReference bool `json:"same_reference"`
}

const bankCandidateDays = 7

func (m *Module) handleBankLines(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	status := r.URL.Query().Get("status")
	var total int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM retailassistant_bank_lines WHERE $1 = '' OR status = $1`, status).Scan(&total); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "count bank lines"))
		return
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, to_char(line_date, 'YYYY-MM-DD'), narration, reference, amount_minor, currency, status, payment_id
		FROM retailassistant_bank_lines WHERE $1 = '' OR status = $1
		ORDER BY line_date DESC, id DESC LIMIT $2 OFFSET $3`, status, limit, offset)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list bank lines"))
		return
	}
	defer rows.Close()
	out := []BankLine{}
	for rows.Next() {
		var l BankLine
		var minor int64
		var cur string
		if err := rows.Scan(&l.ID, &l.Date, &l.Narration, &l.Reference, &minor, &cur, &l.Status, &l.PaymentID); err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list bank lines"))
			return
		}
		l.Amount = gocommerce.Money{AmountMinor: minor, Currency: cur}
		l.Candidates = []BankCandidate{}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list bank lines"))
		return
	}
	rows.Close()
	for i := range out {
		if out[i].Status != "unmatched" {
			continue
		}
		if out[i].Candidates, err = m.candidates(ctx, out[i]); err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "suggest payments for a bank line"))
			return
		}
	}
	gocommerce.RespondList(w, out, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

// candidates is one query per unmatched line on a page.
//
// ponytail: fine for a page of 50 in a small shop's ledger; a single statement
// with a lateral join when a store's ledger makes this slow.
func (m *Module) candidates(ctx context.Context, l BankLine) ([]BankCandidate, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT p.id, s.name, to_char(p.paid_on, 'YYYY-MM-DD'), p.reference, p.status,
		       p.reference <> '' AND (position(lower(p.reference) IN lower($4)) > 0 OR position(lower(p.reference) IN lower($5)) > 0) AS same
		FROM retailassistant_supplier_payments p
		JOIN retailassistant_suppliers s ON s.id = p.supplier_id
		WHERE p.amount_minor = $1 AND p.reverses_id IS NULL
		  AND p.paid_on BETWEEN $2::date - $3::int AND $2::date + $3::int
		  AND NOT EXISTS (SELECT 1 FROM retailassistant_supplier_payments x WHERE x.reverses_id = p.id)
		  AND NOT EXISTS (SELECT 1 FROM retailassistant_bank_lines b WHERE b.payment_id = p.id)
		ORDER BY same DESC, abs(p.paid_on - $2::date), p.id`,
		l.Amount.AmountMinor, l.Date, bankCandidateDays, l.Reference, l.Narration)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BankCandidate{}
	for rows.Next() {
		var c BankCandidate
		if err := rows.Scan(&c.PaymentID, &c.SupplierName, &c.PaidOn, &c.Reference, &c.Status, &c.SameReference); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type lockedLine struct {
	id, amount      int64
	date, reference string
	status, cur     string
	payment         sql.NullInt64
	verifiedByMatch bool
}

func lockLine(ctx context.Context, tx *sql.Tx, id int64) (*lockedLine, error) {
	var l lockedLine
	l.id = id
	err := tx.QueryRowContext(ctx, `
		SELECT amount_minor, to_char(line_date, 'YYYY-MM-DD'), reference, status, currency, payment_id, verified_by_match
		FROM retailassistant_bank_lines WHERE id = $1 FOR UPDATE`, id).
		Scan(&l.amount, &l.date, &l.reference, &l.status, &l.cur, &l.payment, &l.verifiedByMatch)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("no bank line %d", id)
	}
	return &l, err
}

// matchLine ties a statement line to one payment. This is what a bank statement
// is for: a payment a person had only a screenshot of becomes verified here, and
// the line remembers that it did so, so undoing the match can undo that too.
func (m *Module) matchLine(ctx context.Context, tx *sql.Tx, l *lockedLine, paymentID int64) error {
	if l.status != "unmatched" {
		return gocommerce.Conflictf("this line is already %s", l.status)
	}
	if err := lockRow(ctx, tx, "retailassistant_supplier_payments", paymentID); err != nil {
		return err
	}
	var amount int64
	var cur, status string
	var unusable bool
	err := tx.QueryRowContext(ctx, `
		SELECT p.amount_minor, p.currency, p.status,
		       p.reverses_id IS NOT NULL OR EXISTS (SELECT 1 FROM retailassistant_supplier_payments x WHERE x.reverses_id = p.id)
		FROM retailassistant_supplier_payments p WHERE p.id = $1`, paymentID).Scan(&amount, &cur, &status, &unusable)
	if errors.Is(err, sql.ErrNoRows) {
		return gocommerce.NotFoundf("no payment %d", paymentID)
	}
	if err != nil {
		return err
	}
	if unusable {
		return gocommerce.Conflictf("this payment was reversed")
	}
	// ponytail: one line to one payment. One transfer paying two recorded
	// payments is refused; add a split when a real statement needs it.
	if amount != l.amount || cur != l.cur {
		return gocommerce.Conflictf("the statement says %s %s and this payment says %s %s", l.cur, plainAmount(l.amount, l.cur), cur, plainAmount(amount, cur))
	}
	var taken bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM retailassistant_bank_lines WHERE payment_id = $1)`, paymentID).Scan(&taken); err != nil {
		return err
	}
	if taken {
		return gocommerce.Conflictf("another statement line is already matched to this payment")
	}
	verifiedHere := status != "verified"
	if verifiedHere {
		if _, err := tx.ExecContext(ctx, `
			UPDATE retailassistant_supplier_payments SET status = 'verified', verified_by = $2, verified_at = now() WHERE id = $1`, paymentID, actor(ctx)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE retailassistant_bank_lines
		SET status = 'matched', payment_id = $2, verified_by_match = $3, reviewed_by = $4, reviewed_at = now() WHERE id = $1`,
		l.id, paymentID, verifiedHere, actor(ctx)); err != nil {
		return err
	}
	return m.audit(ctx, tx, "bank.matched", "bank_line", l.id, map[string]any{"payment_id": paymentID, "verified_by_match": verifiedHere})
}

func (m *Module) bankAction(w http.ResponseWriter, r *http.Request, doing string, act func(ctx context.Context, tx *sql.Tx, l *lockedLine) error) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		l, err := lockLine(ctx, tx, id)
		if err != nil {
			return err
		}
		return act(ctx, tx, l)
	})
	if err != nil {
		respondTx(w, r, err, doing)
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]bool{"done": true})
}

func (m *Module) handleMatchLine(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PaymentID int64 `json:"payment_id"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	m.bankAction(w, r, "match a bank line", func(ctx context.Context, tx *sql.Tx, l *lockedLine) error {
		return m.matchLine(ctx, tx, l, in.PaymentID)
	})
}

// handleRecordFromLine is a payment nobody recorded, found on the statement.
func (m *Module) handleRecordFromLine(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SupplierID int64 `json:"supplier_id"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	m.bankAction(w, r, "record a payment from a bank line", func(ctx context.Context, tx *sql.Tx, l *lockedLine) error {
		if l.status != "unmatched" {
			return gocommerce.Conflictf("this line is already %s", l.status)
		}
		// Provisional, so matchLine is what verifies it and a later undo takes the
		// payment back with the proof it rested on.
		id, _, err := m.insertPayment(ctx, tx, paymentIn{
			SupplierID: in.SupplierID, AmountMinor: l.amount, Method: "Bank", PaidOn: l.date, Reference: l.reference,
		}, l.cur, nil)
		if err != nil {
			return err
		}
		return m.matchLine(ctx, tx, l, id)
	})
}

func (m *Module) handleIgnoreLine(w http.ResponseWriter, r *http.Request) {
	m.bankAction(w, r, "ignore a bank line", func(ctx context.Context, tx *sql.Tx, l *lockedLine) error {
		if l.status != "unmatched" {
			return gocommerce.Conflictf("this line is already %s", l.status)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE retailassistant_bank_lines SET status = 'ignored', reviewed_by = $2, reviewed_at = now() WHERE id = $1`, l.id, actor(ctx)); err != nil {
			return err
		}
		return m.audit(ctx, tx, "bank.ignored", "bank_line", l.id, nil)
	})
}

// handleUndoLine puts a line back. A payment this match verified goes back to
// unverified, unless money has been allocated from it, because that would change
// what a bill says is owed behind the person's back.
func (m *Module) handleUndoLine(w http.ResponseWriter, r *http.Request) {
	m.bankAction(w, r, "undo a bank line", func(ctx context.Context, tx *sql.Tx, l *lockedLine) error {
		if l.status == "unmatched" {
			return nil
		}
		if l.payment.Valid && l.verifiedByMatch {
			if err := lockRow(ctx, tx, "retailassistant_supplier_payments", l.payment.Int64); err != nil {
				return err
			}
			var allocated int64
			if err := tx.QueryRowContext(ctx, `
				SELECT COALESCE(sum(amount_minor), 0) FROM retailassistant_payment_allocations WHERE payment_id = $1`, l.payment.Int64).Scan(&allocated); err != nil {
				return err
			}
			if allocated != 0 {
				return gocommerce.Conflictf("%s of this payment is allocated to a bill. Reverse that first", plainAmount(allocated, l.cur))
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE retailassistant_supplier_payments SET status = 'provisional', verified_by = '', verified_at = NULL WHERE id = $1`, l.payment.Int64); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE retailassistant_bank_lines
			SET status = 'unmatched', payment_id = NULL, verified_by_match = false, reviewed_by = '', reviewed_at = NULL WHERE id = $1`, l.id); err != nil {
			return err
		}
		return m.audit(ctx, tx, "bank.undone", "bank_line", l.id, map[string]any{"payment_id": l.payment.Int64})
	})
}
