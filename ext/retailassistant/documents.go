package retailassistant

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

func marshalDetail(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, gocommerce.Validationf("the id must be a number")
	}
	return id, nil
}

// Document is one file as the review screen sees it. The bytes have their own
// route, because a list of drafts has no reason to carry them.
type Document struct {
	ID         int64      `json:"id"`
	Sender     string     `json:"sender"`
	Filename   string     `json:"filename"`
	MimeType   string     `json:"mime_type"`
	Status     string     `json:"status"`
	Version    int        `json:"version"`
	LastError  string     `json:"last_error,omitempty"`
	ReviewedBy string     `json:"reviewed_by,omitempty"`
	ReviewedAt *time.Time `json:"reviewed_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	Draft      *Draft     `json:"draft"`
	// Extraction is what was read before anybody corrected it.
	Extraction *Draft `json:"extraction,omitempty"`
}

const documentColumns = `id, sender, filename, detected_mime, status, version, last_error, reviewed_by, reviewed_at, created_at, draft, extraction`

func scanDocument(row interface{ Scan(...any) error }) (*Document, error) {
	var d Document
	var draft, extraction []byte
	if err := row.Scan(&d.ID, &d.Sender, &d.Filename, &d.MimeType, &d.Status, &d.Version, &d.LastError,
		&d.ReviewedBy, &d.ReviewedAt, &d.CreatedAt, &draft, &extraction); err != nil {
		return nil, err
	}
	if draft != nil {
		if err := json.Unmarshal(draft, &d.Draft); err != nil {
			return nil, err
		}
	}
	if extraction != nil {
		if err := json.Unmarshal(extraction, &d.Extraction); err != nil {
			return nil, err
		}
	}
	return &d, nil
}

func (m *Module) handleListDocuments(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	status := r.URL.Query().Get("status")
	var total int
	if err := m.db.QueryRowContext(ctx, `
		SELECT count(*) FROM retailassistant_documents WHERE $1 = '' OR status = $1`, status).Scan(&total); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "count documents"))
		return
	}
	rows, err := m.db.QueryContext(ctx, `SELECT `+documentColumns+` FROM retailassistant_documents
		WHERE $1 = '' OR status = $1 ORDER BY id DESC LIMIT $2 OFFSET $3`, status, limit, offset)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list documents"))
		return
	}
	defer rows.Close()
	out := []*Document{}
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list documents"))
			return
		}
		d.Extraction = nil
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list documents"))
		return
	}
	gocommerce.RespondList(w, out, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (m *Module) document(ctx context.Context, id int64) (*Document, error) {
	d, err := scanDocument(m.db.QueryRowContext(ctx, `SELECT `+documentColumns+` FROM retailassistant_documents WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("no document %d", id)
	}
	if err != nil {
		return nil, gocommerce.Internalf(err, "read a document")
	}
	return d, nil
}

func (m *Module) handleGetDocument(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	d, err := m.document(r.Context(), id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, d)
}

var safeFilename = regexp.MustCompile(`[^\w.\- ]+`)

// handleDocumentFile answers with the original, always as a download: the bytes
// are a stranger's until proved otherwise, and must not run on the panel's
// origin as a page.
func (m *Module) handleDocumentFile(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var content []byte
	var filename string
	err = m.db.QueryRowContext(r.Context(), `SELECT content, filename FROM retailassistant_documents WHERE id = $1`, id).Scan(&content, &filename)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && content == nil) {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("document %d has no file", id))
		return
	}
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read a document"))
		return
	}
	name := strings.TrimSpace(safeFilename.ReplaceAllString(filename, "_"))
	if name == "" {
		name = "document-" + strconv.FormatInt(id, 10)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	_, _ = w.Write(content)
}

// reviewable locks a document for a change. Version is what the reviewer was
// looking at: somebody else's correction since then is a conflict, not an
// overwrite they never saw.
func reviewable(ctx context.Context, tx *sql.Tx, id int64, version int) (*Draft, string, error) {
	var status, sender string
	var current int
	var raw []byte
	err := tx.QueryRowContext(ctx, `
		SELECT status, version, draft, sender FROM retailassistant_documents WHERE id = $1 FOR UPDATE`, id).
		Scan(&status, &current, &raw, &sender)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", gocommerce.NotFoundf("no document %d", id)
	}
	if err != nil {
		return nil, "", err
	}
	if status != "needs_review" {
		return nil, "", gocommerce.Conflictf("this document is %s, so it cannot be reviewed", status)
	}
	if version != current {
		return nil, "", gocommerce.Conflictf("somebody changed this document since you opened it; open it again")
	}
	var d Draft
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, "", err
	}
	return &d, sender, nil
}

func (m *Module) handleCorrect(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in struct {
		Version int   `json:"version"`
		Draft   Draft `json:"draft"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	var out *Document
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		before, _, err := reviewable(ctx, tx, id, in.Version)
		if err != nil {
			return err
		}
		next := in.Draft
		// Where a figure came from is the reader's record and not the reviewer's
		// to rewrite; the exceptions are worked out again from what now stands.
		next.Source = before.Source
		if next.Currency == "" {
			next.Currency = before.Currency
		}
		if next.Lines == nil {
			next.Lines = []Line{}
		}
		next.Exceptions = Check(next)
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE retailassistant_documents SET draft = $2, version = version + 1 WHERE id = $1`, id, raw); err != nil {
			return err
		}
		return m.audit(ctx, tx, "document.corrected", "document", id, map[string]any{"before": before, "after": next})
	})
	if err == nil {
		out, err = m.document(ctx, id)
	}
	if err != nil {
		respondTx(w, r, err, "correct a document")
		return
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

// respondTx answers a transaction's error: its refusal, or a 500 that says what
// was being done.
func respondTx(w http.ResponseWriter, r *http.Request, err error, doing string) {
	var api *gocommerce.APIError
	if errors.As(err, &api) {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.RespondError(w, r, gocommerce.Internalf(err, "%s", doing))
}

// handleApprove turns a reviewed document into a ledger entry, in one
// transaction, so a document is posted and its entry exists or neither is true.
//
// Figures that do not add up are refused unless the reviewer says they looked
// (accept_mismatch), because real bills carry round-offs. Saying so is recorded.
func (m *Module) handleApprove(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in struct {
		Version        int  `json:"version"`
		AcceptMismatch bool `json:"accept_mismatch"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	currency := m.app.Currency(ctx)
	var out map[string]any
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		d, sender, err := reviewable(ctx, tx, id, in.Version)
		if err != nil {
			return err
		}
		if err := checkApprovable(*d, in.AcceptMismatch); err != nil {
			return err
		}
		if d.Currency != "" && d.Currency != currency {
			return gocommerce.Validationf("this store's ledger is in %s, and this document says %s", currency, d.Currency)
		}
		supplierID, err := m.resolveSupplier(ctx, tx, d.Supplier)
		if err != nil {
			return err
		}
		var entity string
		var entityID int64
		switch d.DocumentType {
		case KindInvoice:
			entity = "invoice"
			entityID, err = m.postInvoice(ctx, tx, id, supplierID, *d, currency)
		case KindCredit:
			entity = "credit"
			entityID, _, err = m.insertCredit(ctx, tx, creditIn{
				SupplierID: supplierID, AmountMinor: *d.Amounts.Total, Kind: creditKind(d.Kind), CreditNo: d.InvoiceNo,
				Reason: d.Reason, SourceDocumentID: &id,
			}, currency, "")
		case KindPayment:
			// A screenshot is somebody's word that money moved, so it starts
			// provisional and counts for nothing until a person verifies it.
			entity = "payment"
			entityID, _, err = m.insertPayment(ctx, tx, paymentIn{
				SupplierID: supplierID, AmountMinor: *d.Amounts.Total, Method: d.Method, PaidOn: d.InvoiceDate,
				Reference: d.InvoiceNo, SourceDocumentID: &id,
			}, currency, "")
		default:
			return gocommerce.Validationf("document_type is %s, %s or %s", KindInvoice, KindCredit, KindPayment)
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE retailassistant_documents
			SET status = 'posted', reviewed_by = $2, reviewed_at = now(), version = version + 1 WHERE id = $1`, id, actor(ctx)); err != nil {
			return err
		}
		if err := m.audit(ctx, tx, "document.approved", "document", id, map[string]any{
			"entity": entity, "entity_id": entityID, "accept_mismatch": in.AcceptMismatch,
		}); err != nil {
			return err
		}
		out = map[string]any{"entity": entity, "entity_id": entityID, "supplier_id": supplierID}

		outstanding, err := outstandingOf(ctx, tx, supplierID, currency)
		if err != nil {
			return err
		}
		return m.tell(ctx, tx, "posted:"+strconv.FormatInt(id, 10), sender, fmt.Sprintf(
			"%s from %s is in your books. You now owe %s %s.",
			entityWord(entity), d.Supplier.Name, currency, plainAmount(outstanding, currency)))
	})
	if err != nil {
		respondTx(w, r, err, "approve a document")
		return
	}
	m.nudge()
	gocommerce.Respond(w, http.StatusOK, out)
}

func entityWord(entity string) string {
	switch entity {
	case "invoice":
		return "A bill"
	case "credit":
		return "A credit note"
	}
	return "An unverified payment"
}

func plainAmount(minor int64, currency string) string {
	exp := gocommerce.CurrencyExponent(currency)
	unit := int64(1)
	for range exp {
		unit *= 10
	}
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	if exp == 0 {
		return sign + strconv.FormatInt(minor, 10)
	}
	return fmt.Sprintf("%s%d.%0*d", sign, minor/unit, exp, minor%unit)
}

// checkApprovable splits Check's findings: what is missing can never be waved
// through, and what merely does not add up can, by a person who looked.
func checkApprovable(d Draft, acceptMismatch bool) error {
	problems := Check(d)
	if len(problems) == 0 {
		return nil
	}
	var refused []string
	for _, p := range problems {
		if !acceptMismatch || !strings.Contains(p, "add up") {
			refused = append(refused, p)
		}
	}
	if len(refused) == 0 {
		return nil
	}
	return &gocommerce.APIError{
		Status: http.StatusBadRequest, Code: "validation_failed",
		Message: strings.Join(refused, " "), Details: map[string]any{"problems": refused},
	}
}

func creditKind(s string) string {
	switch s {
	case "return", "damaged":
		return s
	}
	return "other"
}

func normalizeName(s string) string { return labelOf(s) }

// resolveSupplier finds a supplier by GSTIN, else by name, and makes one if
// neither is known. The name comes off the document, never from who forwarded it.
func (m *Module) resolveSupplier(ctx context.Context, tx *sql.Tx, p Party) (int64, error) {
	name := strings.TrimSpace(p.Name)
	norm := normalizeName(name)
	var id int64
	if p.GSTIN != nil && *p.GSTIN != "" {
		err := tx.QueryRowContext(ctx, `SELECT id FROM retailassistant_suppliers WHERE gstin = $1`, *p.GSTIN).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	var gstin any
	if p.GSTIN != nil && *p.GSTIN != "" {
		gstin = *p.GSTIN
	}
	err := tx.QueryRowContext(ctx, `
		INSERT INTO retailassistant_suppliers (name, normalized_name, gstin) VALUES ($1, $2, $3)
		ON CONFLICT (store_id, normalized_name) DO UPDATE
		SET gstin = COALESCE(retailassistant_suppliers.gstin, EXCLUDED.gstin)
		RETURNING id`, name, norm, gstin).Scan(&id)
	return id, err
}

func (m *Module) postInvoice(ctx context.Context, tx *sql.Tx, documentID, supplierID int64, d Draft, currency string) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO retailassistant_purchase_invoices
		    (supplier_id, document_id, invoice_no, invoice_date, subtotal_minor, tax_minor, total_minor, currency, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (store_id, supplier_id, lower(invoice_no)) WHERE reversed_at IS NULL DO NOTHING
		RETURNING id`,
		supplierID, documentID, strings.TrimSpace(d.InvoiceNo), d.InvoiceDate,
		d.Amounts.Subtotal, d.Amounts.Tax, *d.Amounts.Total, currency, actor(ctx)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, gocommerce.Conflictf("invoice %s from %s is already in the books", d.InvoiceNo, d.Supplier.Name)
	}
	if err != nil {
		return 0, err
	}
	for _, l := range d.Lines {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO retailassistant_purchase_lines
			    (invoice_id, description, sku, barcode, hsn, quantity, unit, unit_price_minor, tax_rate_bps, mrp_minor, line_total_minor)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			id, l.Description, l.SKU, l.Barcode, l.HSN, l.Quantity, l.Unit, l.UnitPriceMinor, l.TaxRateBps, l.MRPMinor, l.LineTotalMinor); err != nil {
			return 0, err
		}
	}
	return id, nil
}

func (m *Module) handleReject(w http.ResponseWriter, r *http.Request) {
	m.closeDocument(w, r, "rejected")
}

// handleRetry sends a failed document round again.
func (m *Module) handleRetry(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	res, err := m.db.ExecContext(ctx, `
		UPDATE retailassistant_documents
		SET status = 'received', attempts = 0, available_at = now(), last_error = ''
		WHERE id = $1 AND status = 'failed'`, id)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "retry a document"))
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		gocommerce.RespondError(w, r, gocommerce.Conflictf("only a failed document can be retried"))
		return
	}
	m.nudge()
	gocommerce.Respond(w, http.StatusOK, map[string]bool{"queued": true})
}

func (m *Module) closeDocument(w http.ResponseWriter, r *http.Request, status string) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in struct {
		Version int `json:"version"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		if _, _, err := reviewable(ctx, tx, id, in.Version); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE retailassistant_documents
			SET status = $2, reviewed_by = $3, reviewed_at = now(), version = version + 1 WHERE id = $1`, id, status, actor(ctx)); err != nil {
			return err
		}
		return m.audit(ctx, tx, "document."+status, "document", id, nil)
	})
	if err != nil {
		respondTx(w, r, err, "close a document")
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]string{"status": status})
}
