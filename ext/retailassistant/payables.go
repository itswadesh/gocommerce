package retailassistant

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// The ledger. What a shop owes a supplier is
//
//	bills posted − credit notes − verified payments allocated to bills
//
// and verified money not yet allocated is an advance, shown beside it. An
// unverified payment is shown too and counts for nothing: a screenshot is
// somebody's word that money moved. Every figure is worked out from rows that
// are never edited to undo money, so it can be checked against the documents.

// rowQuerier is a transaction or the pool, for sums read from either.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func outstandingOf(ctx context.Context, q rowQuerier, supplierID int64, currency string) (int64, error) {
	var out int64
	err := q.QueryRowContext(ctx, `
		SELECT COALESCE((SELECT sum(total_minor) FROM retailassistant_purchase_invoices
		                 WHERE supplier_id = $1 AND currency = $2 AND reversed_at IS NULL), 0)
		     - COALESCE((SELECT sum(amount_minor) FROM retailassistant_supplier_credits
		                 WHERE supplier_id = $1 AND currency = $2), 0)
		     - COALESCE((SELECT sum(a.amount_minor) FROM retailassistant_payment_allocations a
		                 JOIN retailassistant_supplier_payments p ON p.id = a.payment_id
		                 WHERE p.supplier_id = $1 AND p.currency = $2), 0)`, supplierID, currency).Scan(&out)
	return out, err
}

// SupplierBalance is one supplier's account in one currency.
type SupplierBalance struct {
	ID          int64            `json:"id"`
	Name        string           `json:"name"`
	GSTIN       *string          `json:"gstin"`
	Invoiced    gocommerce.Money `json:"invoiced"`
	Credits     gocommerce.Money `json:"credits"`
	Paid        gocommerce.Money `json:"paid_allocated"`
	Outstanding gocommerce.Money `json:"outstanding"`
	Advance     gocommerce.Money `json:"advance"`
	Unverified  gocommerce.Money `json:"unverified"`
}

func (m *Module) handleSuppliers(w http.ResponseWriter, r *http.Request) {
	out, err := m.supplierBalances(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list suppliers"))
		return
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

func (m *Module) supplierBalances(ctx context.Context) ([]SupplierBalance, error) {
	rows, err := m.db.QueryContext(ctx, `
		WITH invoiced AS (
		    SELECT supplier_id, currency, sum(total_minor) AS n FROM retailassistant_purchase_invoices
		    WHERE reversed_at IS NULL GROUP BY 1, 2
		), credits AS (
		    SELECT supplier_id, currency, sum(amount_minor) AS n FROM retailassistant_supplier_credits GROUP BY 1, 2
		), verified AS (
		    SELECT supplier_id, currency, sum(amount_minor) AS n FROM retailassistant_supplier_payments
		    WHERE status = 'verified' GROUP BY 1, 2
		), unverified AS (
		    SELECT supplier_id, currency, sum(amount_minor) AS n FROM retailassistant_supplier_payments
		    WHERE status = 'provisional' GROUP BY 1, 2
		), allocated AS (
		    SELECT p.supplier_id, p.currency, sum(a.amount_minor) AS n FROM retailassistant_payment_allocations a
		    JOIN retailassistant_supplier_payments p ON p.id = a.payment_id GROUP BY 1, 2
		), pairs AS (
		    SELECT supplier_id, currency FROM invoiced UNION SELECT supplier_id, currency FROM credits
		    UNION SELECT supplier_id, currency FROM verified UNION SELECT supplier_id, currency FROM unverified
		)
		SELECT s.id, s.name, s.gstin, COALESCE(x.currency, $1),
		       COALESCE(i.n, 0), COALESCE(c.n, 0), COALESCE(a.n, 0), COALESCE(v.n, 0), COALESCE(u.n, 0)
		FROM retailassistant_suppliers s
		LEFT JOIN pairs x ON x.supplier_id = s.id
		LEFT JOIN invoiced i ON i.supplier_id = s.id AND i.currency = x.currency
		LEFT JOIN credits c ON c.supplier_id = s.id AND c.currency = x.currency
		LEFT JOIN allocated a ON a.supplier_id = s.id AND a.currency = x.currency
		LEFT JOIN verified v ON v.supplier_id = s.id AND v.currency = x.currency
		LEFT JOIN unverified u ON u.supplier_id = s.id AND u.currency = x.currency
		ORDER BY s.name, 4`, m.app.Currency(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SupplierBalance{}
	for rows.Next() {
		var b SupplierBalance
		var cur string
		var inv, cred, alloc, ver, unver int64
		if err := rows.Scan(&b.ID, &b.Name, &b.GSTIN, &cur, &inv, &cred, &alloc, &ver, &unver); err != nil {
			return nil, err
		}
		mk := func(n int64) gocommerce.Money { return gocommerce.Money{AmountMinor: n, Currency: cur} }
		b.Invoiced, b.Credits, b.Paid = mk(inv), mk(cred), mk(alloc)
		b.Outstanding, b.Advance, b.Unverified = mk(inv-cred-alloc), mk(ver-alloc), mk(unver)
		out = append(out, b)
	}
	return out, rows.Err()
}

// Payable is one posted bill and what remains of it.
type Payable struct {
	ID           int64            `json:"id"`
	SupplierID   int64            `json:"supplier_id"`
	SupplierName string           `json:"supplier_name"`
	InvoiceNo    string           `json:"invoice_no"`
	InvoiceDate  string           `json:"invoice_date"`
	Total        gocommerce.Money `json:"total"`
	Remaining    gocommerce.Money `json:"remaining"`
	Reversed     bool             `json:"reversed"`
	Lines        int              `json:"lines"`
	// LinesReceived is how many lines have had their goods booked in. A bill
	// arriving says nothing about the goods having arrived.
	LinesReceived int `json:"lines_received"`
}

func (m *Module) handlePayables(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	supplier := int64(0)
	if v := r.URL.Query().Get("supplier_id"); v != "" {
		if supplier, err = strconv.ParseInt(v, 10, 64); err != nil {
			gocommerce.RespondError(w, r, gocommerce.Validationf("supplier_id must be a number"))
			return
		}
	}
	ctx := r.Context()
	var total int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM retailassistant_purchase_invoices
		WHERE ($1 = 0 OR supplier_id = $1)`, supplier).Scan(&total); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "count payables"))
		return
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT i.id, i.supplier_id, s.name, i.invoice_no, to_char(i.invoice_date, 'YYYY-MM-DD'), i.total_minor, i.currency,
		       i.total_minor - COALESCE((SELECT sum(a.amount_minor) FROM retailassistant_payment_allocations a WHERE a.invoice_id = i.id), 0),
		       i.reversed_at IS NOT NULL,
		       (SELECT count(*) FROM retailassistant_purchase_lines l WHERE l.invoice_id = i.id),
		       (SELECT count(*) FROM retailassistant_stock_receipts x JOIN retailassistant_purchase_lines l ON l.id = x.line_id WHERE l.invoice_id = i.id)
		FROM retailassistant_purchase_invoices i
		JOIN retailassistant_suppliers s ON s.id = i.supplier_id
		WHERE ($1 = 0 OR i.supplier_id = $1)
		ORDER BY i.invoice_date DESC, i.id DESC LIMIT $2 OFFSET $3`, supplier, limit, offset)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list payables"))
		return
	}
	defer rows.Close()
	out := []Payable{}
	for rows.Next() {
		var p Payable
		var tot, rem int64
		var cur string
		if err := rows.Scan(&p.ID, &p.SupplierID, &p.SupplierName, &p.InvoiceNo, &p.InvoiceDate, &tot, &cur, &rem, &p.Reversed, &p.Lines, &p.LinesReceived); err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list payables"))
			return
		}
		p.Total = gocommerce.Money{AmountMinor: tot, Currency: cur}
		p.Remaining = gocommerce.Money{AmountMinor: rem, Currency: cur}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list payables"))
		return
	}
	gocommerce.RespondList(w, out, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

// LedgerEntry is a payment or a credit note and how much of it is left to use.
type LedgerEntry struct {
	ID          int64            `json:"id"`
	SupplierID  int64            `json:"supplier_id"`
	Amount      gocommerce.Money `json:"amount"`
	Unallocated gocommerce.Money `json:"unallocated"`
	Status      string           `json:"status,omitempty"`
	Kind        string           `json:"kind,omitempty"`
	Reference   string           `json:"reference"`
	Date        string           `json:"date"`
	Reversed    bool             `json:"reversed"`
	// ReversesID marks the compensating entry for another row.
	ReversesID *int64 `json:"reverses_id,omitempty"`
}

func (m *Module) handleListPayments(w http.ResponseWriter, r *http.Request) {
	m.listLedger(w, r, `
		SELECT p.id, p.supplier_id, p.amount_minor, p.currency,
		       p.amount_minor - COALESCE((SELECT sum(a.amount_minor) FROM retailassistant_payment_allocations a WHERE a.payment_id = p.id), 0),
		       p.status, '', p.reference, to_char(p.paid_on, 'YYYY-MM-DD'),
		       EXISTS (SELECT 1 FROM retailassistant_supplier_payments x WHERE x.reverses_id = p.id), p.reverses_id
		FROM retailassistant_supplier_payments p
		WHERE ($1 = 0 OR p.supplier_id = $1) ORDER BY p.id DESC LIMIT 200`)
}

func (m *Module) handleListCredits(w http.ResponseWriter, r *http.Request) {
	m.listLedger(w, r, `
		SELECT c.id, c.supplier_id, c.amount_minor, c.currency,
		       c.amount_minor - COALESCE((SELECT sum(a.amount_minor) FROM retailassistant_payment_allocations a WHERE a.credit_id = c.id), 0),
		       '', c.kind, c.credit_no, to_char(c.created_at, 'YYYY-MM-DD'),
		       EXISTS (SELECT 1 FROM retailassistant_supplier_credits x WHERE x.reverses_id = c.id), c.reverses_id
		FROM retailassistant_supplier_credits c
		WHERE ($1 = 0 OR c.supplier_id = $1) ORDER BY c.id DESC LIMIT 200`)
}

func (m *Module) listLedger(w http.ResponseWriter, r *http.Request, query string) {
	var supplier int64
	if v := r.URL.Query().Get("supplier_id"); v != "" {
		var err error
		if supplier, err = strconv.ParseInt(v, 10, 64); err != nil {
			gocommerce.RespondError(w, r, gocommerce.Validationf("supplier_id must be a number"))
			return
		}
	}
	rows, err := m.db.QueryContext(r.Context(), query, supplier)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list the ledger"))
		return
	}
	defer rows.Close()
	out := []LedgerEntry{}
	for rows.Next() {
		var e LedgerEntry
		var amount, left int64
		var cur string
		if err := rows.Scan(&e.ID, &e.SupplierID, &amount, &cur, &left, &e.Status, &e.Kind, &e.Reference, &e.Date, &e.Reversed, &e.ReversesID); err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list the ledger"))
			return
		}
		e.Amount = gocommerce.Money{AmountMinor: amount, Currency: cur}
		e.Unallocated = gocommerce.Money{AmountMinor: left, Currency: cur}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list the ledger"))
		return
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

// keyFrom is a retry's name for itself. Every money-moving route takes one, so
// a button pressed twice over a bad connection records one thing.
func keyFrom(r *http.Request) any {
	if k := strings.TrimSpace(r.Header.Get("Idempotency-Key")); k != "" {
		return k
	}
	return nil
}

type paymentIn struct {
	SupplierID       int64  `json:"supplier_id"`
	AmountMinor      int64  `json:"amount_minor"`
	Method           string `json:"method"`
	PaidOn           string `json:"paid_on"`
	Reference        string `json:"reference"`
	Verified         bool   `json:"verified"`
	SourceDocumentID *int64 `json:"-"`
}

func (m *Module) requireSupplier(ctx context.Context, tx *sql.Tx, id int64) error {
	var ok bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM retailassistant_suppliers WHERE id = $1)`, id).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return gocommerce.NotFoundf("no supplier %d", id)
	}
	return nil
}

// insertPayment records one. Verified is a person's own word, such as cash they
// handed over or a bank statement they read, and it says who gave it.
func (m *Module) insertPayment(ctx context.Context, tx *sql.Tx, in paymentIn, currency string, key any) (int64, bool, error) {
	if in.AmountMinor <= 0 {
		return 0, false, gocommerce.Validationf("amount_minor must be more than zero")
	}
	if _, err := time.Parse("2006-01-02", in.PaidOn); err != nil {
		return 0, false, gocommerce.Validationf("paid_on is a date, such as 2026-11-05")
	}
	if err := m.requireSupplier(ctx, tx, in.SupplierID); err != nil {
		return 0, false, err
	}
	status, by, at := "provisional", "", any(nil)
	if in.Verified {
		status, by, at = "verified", actor(ctx), time.Now()
	}
	if key == "" {
		key = nil
	}
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO retailassistant_supplier_payments
		    (supplier_id, amount_minor, currency, method, paid_on, reference, status, source_document_id,
		     idempotency_key, verified_by, verified_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (store_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
		RETURNING id`,
		in.SupplierID, in.AmountMinor, currency, in.Method, in.PaidOn, in.Reference, status, in.SourceDocumentID,
		key, by, at, actor(ctx)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT id FROM retailassistant_supplier_payments WHERE idempotency_key = $1`, key).Scan(&id)
		return id, false, err
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, m.audit(ctx, tx, "payment.recorded", "payment", id, map[string]any{"amount_minor": in.AmountMinor, "status": status})
}

func (m *Module) handleCreatePayment(w http.ResponseWriter, r *http.Request) {
	var in paymentIn
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	currency := m.app.Currency(ctx)
	var id int64
	err := gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		var err error
		id, _, err = m.insertPayment(ctx, tx, in, currency, keyFrom(r))
		return err
	})
	if err != nil {
		respondTx(w, r, err, "record a payment")
		return
	}
	gocommerce.Respond(w, http.StatusCreated, map[string]int64{"id": id})
}

func (m *Module) handleVerifyPayment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		if err := lockRow(ctx, tx, "retailassistant_supplier_payments", id); err != nil {
			return err
		}
		var status string
		var reversed bool
		err := tx.QueryRowContext(ctx, `
			SELECT p.status, EXISTS (SELECT 1 FROM retailassistant_supplier_payments x WHERE x.reverses_id = p.id)
			FROM retailassistant_supplier_payments p WHERE p.id = $1 AND p.reverses_id IS NULL FOR UPDATE OF p`, id).Scan(&status, &reversed)
		if errors.Is(err, sql.ErrNoRows) {
			return gocommerce.NotFoundf("no payment %d", id)
		}
		if err != nil {
			return err
		}
		if reversed {
			return gocommerce.Conflictf("this payment was reversed")
		}
		if status == "verified" {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE retailassistant_supplier_payments
			SET status = 'verified', verified_by = $2, verified_at = now() WHERE id = $1`, id, actor(ctx)); err != nil {
			return err
		}
		return m.audit(ctx, tx, "payment.verified", "payment", id, nil)
	})
	if err != nil {
		respondTx(w, r, err, "verify a payment")
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]string{"status": "verified"})
}

// allocation names what is being spent against a bill.
type allocation struct {
	paymentID, creditID *int64
	invoiceID, amount   int64
}

// lockRow takes a row's lock in a statement of its own, and that is the point.
// A subquery on the statement holding FOR UPDATE reads the snapshot from before
// the wait for the lock, so whoever queued behind a committing allocation would
// still add up the old balance. The next statement gets a fresh snapshot.
func lockRow(ctx context.Context, tx *sql.Tx, table string, id int64) error {
	_, err := tx.ExecContext(ctx, `SELECT 1 FROM `+table+` WHERE id = $1 FOR UPDATE`, id)
	return err
}

// allocate spends part of a payment or credit against a bill. Both rows are
// locked first, so two allocations racing cannot each find the same balance.
func (m *Module) allocate(ctx context.Context, tx *sql.Tx, a allocation, key any) (int64, error) {
	if key != nil {
		var existing int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM retailassistant_payment_allocations WHERE idempotency_key = $1`, key).Scan(&existing)
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	if a.amount <= 0 {
		return 0, gocommerce.Validationf("amount_minor must be more than zero")
	}
	var supplier int64
	var currency, status string
	var amount, used int64
	var reversed bool
	var sourceErr error
	if a.paymentID != nil {
		if err := lockRow(ctx, tx, "retailassistant_supplier_payments", *a.paymentID); err != nil {
			return 0, err
		}
		sourceErr = tx.QueryRowContext(ctx, `
			SELECT p.supplier_id, p.currency, p.status, p.amount_minor,
			       COALESCE((SELECT sum(x.amount_minor) FROM retailassistant_payment_allocations x WHERE x.payment_id = p.id), 0),
			       EXISTS (SELECT 1 FROM retailassistant_supplier_payments r WHERE r.reverses_id = p.id)
			FROM retailassistant_supplier_payments p WHERE p.id = $1 AND p.reverses_id IS NULL FOR UPDATE OF p`, *a.paymentID).
			Scan(&supplier, &currency, &status, &amount, &used, &reversed)
	} else {
		status = "verified"
		if err := lockRow(ctx, tx, "retailassistant_supplier_credits", *a.creditID); err != nil {
			return 0, err
		}
		sourceErr = tx.QueryRowContext(ctx, `
			SELECT c.supplier_id, c.currency, c.amount_minor,
			       COALESCE((SELECT sum(x.amount_minor) FROM retailassistant_payment_allocations x WHERE x.credit_id = c.id), 0),
			       EXISTS (SELECT 1 FROM retailassistant_supplier_credits r WHERE r.reverses_id = c.id)
			FROM retailassistant_supplier_credits c WHERE c.id = $1 AND c.reverses_id IS NULL FOR UPDATE OF c`, *a.creditID).
			Scan(&supplier, &currency, &amount, &used, &reversed)
	}
	if errors.Is(sourceErr, sql.ErrNoRows) {
		return 0, gocommerce.NotFoundf("no such payment or credit note")
	}
	if sourceErr != nil {
		return 0, sourceErr
	}
	switch {
	case reversed:
		return 0, gocommerce.Conflictf("this was reversed, so it cannot be allocated")
	case status != "verified":
		return 0, gocommerce.Conflictf("verify this payment first: an unverified payment settles nothing")
	}
	var invSupplier, total, paid int64
	var invCurrency string
	var invReversed bool
	if err := lockRow(ctx, tx, "retailassistant_purchase_invoices", a.invoiceID); err != nil {
		return 0, err
	}
	err := tx.QueryRowContext(ctx, `
		SELECT i.supplier_id, i.currency, i.total_minor,
		       COALESCE((SELECT sum(x.amount_minor) FROM retailassistant_payment_allocations x WHERE x.invoice_id = i.id), 0),
		       i.reversed_at IS NOT NULL
		FROM retailassistant_purchase_invoices i WHERE i.id = $1 FOR UPDATE OF i`, a.invoiceID).
		Scan(&invSupplier, &invCurrency, &total, &paid, &invReversed)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, gocommerce.NotFoundf("no invoice %d", a.invoiceID)
	}
	if err != nil {
		return 0, err
	}
	switch {
	case invReversed:
		return 0, gocommerce.Conflictf("this invoice was reversed")
	case invSupplier != supplier:
		return 0, gocommerce.Validationf("this invoice belongs to another supplier")
	case invCurrency != currency:
		return 0, gocommerce.Validationf("this invoice is in %s, not %s", invCurrency, currency)
	case a.amount > amount-used:
		return 0, gocommerce.Validationf("only %s of this is left to allocate", plainAmount(amount-used, currency))
	case a.amount > total-paid:
		return 0, gocommerce.Validationf("only %s of this invoice is still owed", plainAmount(total-paid, currency))
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO retailassistant_payment_allocations (invoice_id, payment_id, credit_id, amount_minor, idempotency_key, created_by)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		a.invoiceID, a.paymentID, a.creditID, a.amount, key, actor(ctx)).Scan(&id); err != nil {
		return 0, err
	}
	return id, m.audit(ctx, tx, "allocation.made", "allocation", id, map[string]any{"invoice_id": a.invoiceID, "amount_minor": a.amount})
}

type allocateIn struct {
	InvoiceID   int64 `json:"invoice_id"`
	AmountMinor int64 `json:"amount_minor"`
}

func (m *Module) handleAllocatePayment(w http.ResponseWriter, r *http.Request) {
	m.handleAllocate(w, r, func(id int64, a *allocation) { a.paymentID = &id })
}

func (m *Module) handleAllocateCredit(w http.ResponseWriter, r *http.Request) {
	m.handleAllocate(w, r, func(id int64, a *allocation) { a.creditID = &id })
}

func (m *Module) handleAllocate(w http.ResponseWriter, r *http.Request, source func(int64, *allocation)) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in allocateIn
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	a := allocation{invoiceID: in.InvoiceID, amount: in.AmountMinor}
	source(id, &a)
	ctx := r.Context()
	var out int64
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		var err error
		out, err = m.allocate(ctx, tx, a, keyFrom(r))
		return err
	})
	if err != nil {
		respondTx(w, r, err, "allocate")
		return
	}
	gocommerce.Respond(w, http.StatusCreated, map[string]int64{"id": out})
}

// reverseAllocations writes the compensating row for each live allocation that
// where names, so reversing a payment hands its bills back in one step.
func (m *Module) reverseAllocations(ctx context.Context, tx *sql.Tx, where string, id int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO retailassistant_payment_allocations (invoice_id, payment_id, credit_id, amount_minor, reverses_id, created_by)
		SELECT a.invoice_id, a.payment_id, a.credit_id, -a.amount_minor, a.id, $2
		FROM retailassistant_payment_allocations a
		WHERE `+where+` = $1 AND a.reverses_id IS NULL
		  AND NOT EXISTS (SELECT 1 FROM retailassistant_payment_allocations r WHERE r.reverses_id = a.id)`, id, actor(ctx))
	return err
}

func (m *Module) handleReversePayment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		// Waits out an allocation in flight, so reverseAllocations below sees it.
		if err := lockRow(ctx, tx, "retailassistant_supplier_payments", id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO retailassistant_supplier_payments
			    (supplier_id, amount_minor, currency, method, paid_on, reference, status, reverses_id, created_by)
			SELECT supplier_id, -amount_minor, currency, method, paid_on, reference, status, id, $2
			FROM retailassistant_supplier_payments
			WHERE id = $1 AND reverses_id IS NULL
			ON CONFLICT (store_id, reverses_id) WHERE reverses_id IS NOT NULL DO NOTHING`, id, actor(ctx))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return gocommerce.Conflictf("payment %d does not exist, or was already reversed", id)
		}
		if err := m.reverseAllocations(ctx, tx, "a.payment_id", id); err != nil {
			return err
		}
		return m.audit(ctx, tx, "payment.reversed", "payment", id, nil)
	})
	if err != nil {
		respondTx(w, r, err, "reverse a payment")
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]bool{"reversed": true})
}

type creditIn struct {
	SupplierID       int64  `json:"supplier_id"`
	AmountMinor      int64  `json:"amount_minor"`
	Kind             string `json:"kind"`
	CreditNo         string `json:"credit_no"`
	Reason           string `json:"reason"`
	InvoiceID        int64  `json:"invoice_id"`
	SourceDocumentID *int64 `json:"-"`
}

func (m *Module) insertCredit(ctx context.Context, tx *sql.Tx, in creditIn, currency string, key any) (int64, bool, error) {
	if in.AmountMinor <= 0 {
		return 0, false, gocommerce.Validationf("amount_minor must be more than zero")
	}
	if err := m.requireSupplier(ctx, tx, in.SupplierID); err != nil {
		return 0, false, err
	}
	if key == "" {
		key = nil
	}
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO retailassistant_supplier_credits
		    (supplier_id, amount_minor, currency, kind, credit_no, reason, source_document_id, idempotency_key, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (store_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
		RETURNING id`,
		in.SupplierID, in.AmountMinor, currency, creditKind(in.Kind), in.CreditNo, in.Reason, in.SourceDocumentID, key, actor(ctx)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT id FROM retailassistant_supplier_credits WHERE idempotency_key = $1`, key).Scan(&id)
		return id, false, err
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, m.audit(ctx, tx, "credit.recorded", "credit", id, map[string]any{"amount_minor": in.AmountMinor})
}

// handleCreateCredit records a credit note, and spends it on the bill it names.
func (m *Module) handleCreateCredit(w http.ResponseWriter, r *http.Request) {
	var in creditIn
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	currency := m.app.Currency(ctx)
	var id int64
	err := gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		var created bool
		var err error
		id, created, err = m.insertCredit(ctx, tx, in, currency, keyFrom(r))
		if err != nil || !created || in.InvoiceID == 0 {
			return err
		}
		_, err = m.allocate(ctx, tx, allocation{creditID: &id, invoiceID: in.InvoiceID, amount: in.AmountMinor}, nil)
		return err
	})
	if err != nil {
		respondTx(w, r, err, "record a credit note")
		return
	}
	gocommerce.Respond(w, http.StatusCreated, map[string]int64{"id": id})
}

func (m *Module) handleReverseCredit(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		if err := lockRow(ctx, tx, "retailassistant_supplier_credits", id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO retailassistant_supplier_credits
			    (supplier_id, amount_minor, currency, kind, credit_no, reason, reverses_id, created_by)
			SELECT supplier_id, -amount_minor, currency, kind, credit_no, reason, id, $2
			FROM retailassistant_supplier_credits
			WHERE id = $1 AND reverses_id IS NULL
			ON CONFLICT (store_id, reverses_id) WHERE reverses_id IS NOT NULL DO NOTHING`, id, actor(ctx))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return gocommerce.Conflictf("credit note %d does not exist, or was already reversed", id)
		}
		if err := m.reverseAllocations(ctx, tx, "a.credit_id", id); err != nil {
			return err
		}
		return m.audit(ctx, tx, "credit.reversed", "credit", id, nil)
	})
	if err != nil {
		respondTx(w, r, err, "reverse a credit note")
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]bool{"reversed": true})
}

func (m *Module) handleReverseAllocation(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO retailassistant_payment_allocations (invoice_id, payment_id, credit_id, amount_minor, reverses_id, created_by)
			SELECT invoice_id, payment_id, credit_id, -amount_minor, id, $2
			FROM retailassistant_payment_allocations
			WHERE id = $1 AND reverses_id IS NULL
			ON CONFLICT (store_id, reverses_id) WHERE reverses_id IS NOT NULL DO NOTHING`, id, actor(ctx))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return gocommerce.Conflictf("allocation %d does not exist, or was already reversed", id)
		}
		return m.audit(ctx, tx, "allocation.reversed", "allocation", id, nil)
	})
	if err != nil {
		respondTx(w, r, err, "reverse an allocation")
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]bool{"reversed": true})
}

// handleReverseInvoice takes a posted bill back out, keeping the row. It refuses
// while money is against it, because which of those stays is a person's call.
func (m *Module) handleReverseInvoice(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		if err := lockRow(ctx, tx, "retailassistant_purchase_invoices", id); err != nil {
			return err
		}
		var allocated, received int64
		var reversed bool
		err := tx.QueryRowContext(ctx, `
			SELECT COALESCE((SELECT sum(a.amount_minor) FROM retailassistant_payment_allocations a WHERE a.invoice_id = i.id), 0),
			       (SELECT count(*) FROM retailassistant_stock_receipts x JOIN retailassistant_purchase_lines l ON l.id = x.line_id WHERE l.invoice_id = i.id),
			       i.reversed_at IS NOT NULL
			FROM retailassistant_purchase_invoices i WHERE i.id = $1 FOR UPDATE OF i`, id).Scan(&allocated, &received, &reversed)
		if errors.Is(err, sql.ErrNoRows) {
			return gocommerce.NotFoundf("no invoice %d", id)
		}
		if err != nil {
			return err
		}
		switch {
		case reversed:
			return gocommerce.Conflictf("this invoice was already reversed")
		case allocated != 0:
			return gocommerce.Conflictf("reverse the payments and credit notes against this invoice first")
		case received != 0:
			return gocommerce.Conflictf("goods were booked in against this invoice, so correct the stock and record a credit note instead")
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE retailassistant_purchase_invoices
			SET reversed_at = now(), reversed_by = $2, reversal_reason = $3 WHERE id = $1`, id, actor(ctx), in.Reason); err != nil {
			return err
		}
		return m.audit(ctx, tx, "invoice.reversed", "invoice", id, map[string]string{"reason": in.Reason})
	})
	if err != nil {
		respondTx(w, r, err, "reverse an invoice")
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]bool{"reversed": true})
}

// PurchaseLine is one line of a posted bill and whether its goods were booked in.
type PurchaseLine struct {
	ID          int64   `json:"id"`
	Description string  `json:"description"`
	SKU         *string `json:"sku"`
	Barcode     *string `json:"barcode"`
	// Quantity is as the bill printed it, which need not be a whole number.
	Quantity *string `json:"quantity"`
	Unit     *string `json:"unit"`
	// Receipt stays pending when the server died between recording it and moving
	// stock, which a person has to check rather than repeat.
	Receipt *LineReceipt `json:"receipt"`
	// VariantID is the variant whose SKU is exactly the one on the line. It is a
	// suggestion: the person receiving the goods still confirms it.
	VariantID *int64 `json:"variant_id"`
}

// LineReceipt is what was booked in for a line.
type LineReceipt struct {
	VariantID  int64  `json:"variant_id"`
	LocationID int64  `json:"location_id"`
	Quantity   int    `json:"quantity"`
	Status     string `json:"status"`
}

func (m *Module) handleInvoiceLines(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	var exists bool
	if err := m.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM retailassistant_purchase_invoices WHERE id = $1)`, id).Scan(&exists); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read an invoice"))
		return
	}
	if !exists {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no invoice %d", id))
		return
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT l.id, l.description, l.sku, l.barcode, l.quantity, l.unit, x.variant_id, x.location_id, x.quantity, x.status
		FROM retailassistant_purchase_lines l
		LEFT JOIN retailassistant_stock_receipts x ON x.line_id = l.id
		WHERE l.invoice_id = $1 ORDER BY l.id`, id)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list invoice lines"))
		return
	}
	defer rows.Close()
	out := []PurchaseLine{}
	for rows.Next() {
		var l PurchaseLine
		var variant, location sql.NullInt64
		var quantity sql.NullInt32
		var status sql.NullString
		if err := rows.Scan(&l.ID, &l.Description, &l.SKU, &l.Barcode, &l.Quantity, &l.Unit, &variant, &location, &quantity, &status); err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list invoice lines"))
			return
		}
		if status.Valid {
			l.Receipt = &LineReceipt{VariantID: variant.Int64, LocationID: location.Int64, Quantity: int(quantity.Int32), Status: status.String}
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list invoice lines"))
		return
	}
	for i := range out {
		if l := &out[i]; l.Receipt == nil && l.SKU != nil && *l.SKU != "" {
			if v, err := m.app.Products().GetVariantBySKU(ctx, *l.SKU); err == nil {
				l.VariantID = &v.ID
			}
		}
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

// handleReceive books goods in. It is a separate act from the bill: a bill says
// goods are owed, this says they are on the shelf.
//
// Exactly once matters more than always. The receipt row commits before stock
// moves, so a retry finds it and stops; a crash between the two leaves a pending
// row that a person reads, rather than stock counted twice. Stock().Adjust takes
// no idempotency key, and this is what that costs.
func (m *Module) handleReceive(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in struct {
		Lines []struct {
			LineID     int64 `json:"line_id"`
			VariantID  int64 `json:"variant_id"`
			LocationID int64 `json:"location_id"`
			Quantity   int   `json:"quantity"`
		} `json:"lines"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if len(in.Lines) == 0 {
		gocommerce.RespondError(w, r, gocommerce.Validationf("name the lines that arrived"))
		return
	}
	ctx := r.Context()
	var invoiceNo string
	var reversed bool
	err = m.db.QueryRowContext(ctx, `SELECT invoice_no, reversed_at IS NOT NULL FROM retailassistant_purchase_invoices WHERE id = $1`, id).Scan(&invoiceNo, &reversed)
	if errors.Is(err, sql.ErrNoRows) {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no invoice %d", id))
		return
	}
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read an invoice"))
		return
	}
	if reversed {
		gocommerce.RespondError(w, r, gocommerce.Conflictf("this invoice was reversed"))
		return
	}
	received := 0
	for _, l := range in.Lines {
		if l.Quantity <= 0 || l.VariantID <= 0 {
			gocommerce.RespondError(w, r, gocommerce.Validationf("each line needs a variant and a quantity above zero"))
			return
		}
		var receiptID int64
		err := m.db.QueryRowContext(ctx, `
			INSERT INTO retailassistant_stock_receipts (line_id, variant_id, location_id, quantity, status, created_by)
			SELECT l.id, $3, $4, $5, 'pending', $6 FROM retailassistant_purchase_lines l
			WHERE l.id = $1 AND l.invoice_id = $2
			ON CONFLICT (store_id, line_id) DO NOTHING RETURNING id`,
			l.LineID, id, l.VariantID, l.LocationID, l.Quantity, actor(ctx)).Scan(&receiptID)
		if errors.Is(err, sql.ErrNoRows) {
			gocommerce.RespondError(w, r, gocommerce.Conflictf("line %d is not on this invoice, or was already received", l.LineID))
			return
		}
		if err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "record a goods receipt"))
			return
		}
		if _, err := m.app.Stock().Adjust(ctx, l.VariantID, l.LocationID, l.Quantity, "Goods received on purchase invoice "+invoiceNo); err != nil {
			// Nothing moved, so the line is free to be tried again.
			_, _ = m.db.ExecContext(ctx, `DELETE FROM retailassistant_stock_receipts WHERE id = $1`, receiptID)
			gocommerce.RespondError(w, r, err)
			return
		}
		err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `UPDATE retailassistant_stock_receipts SET status = 'posted' WHERE id = $1`, receiptID); err != nil {
				return err
			}
			return m.audit(ctx, tx, "goods.received", "line", l.LineID, map[string]any{"variant_id": l.VariantID, "quantity": l.Quantity})
		})
		if err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "record a goods receipt"))
			return
		}
		received++
	}
	gocommerce.Respond(w, http.StatusOK, map[string]int{"received": received})
}
