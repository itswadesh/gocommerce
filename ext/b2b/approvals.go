package b2b

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/ext/identity"
)

// Approval states.
const (
	ApprovalPending   = "pending"
	ApprovalPlacing   = "placing"
	ApprovalApproved  = "approved"
	ApprovalRejected  = "rejected"
	ApprovalCancelled = "cancelled"
)

// requestApproval files a basket the guard refused for want of approval,
// with the figures the guard saw. A retry under the same idempotency key
// answers with the request it already made.
func (m *Module) requestApproval(ctx context.Context, p *placement, kind string, quoteID int64, method string, in gocommerce.CheckoutInput, idemKey string) (*Approval, error) {
	a := p.pending
	lines := make([]ApprovalLine, 0, len(a.Lines))
	for _, l := range a.Lines {
		lines = append(lines, ApprovalLine{VariantID: l.VariantID, SKU: l.SKU, Quantity: l.Quantity, UnitPrice: l.UnitPrice})
	}
	linesJSON, err := json.Marshal(lines)
	if err != nil {
		return nil, err
	}
	var key *string
	if k := strings.TrimSpace(idemKey); k != "" {
		key = &k
	}
	var quote *int64
	if quoteID != 0 {
		quote = &quoteID
	}
	var id int64
	err = m.db.QueryRowContext(ctx, `
		INSERT INTO b2b_approvals (company_id, requested_by, requested_by_email, kind, quote_id,
		                           currency, lines, subtotal_minor, total_minor, payment_method,
		                           po_number, checkout, idempotency_key)
		VALUES ($1, $2, $13, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (requested_by, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
		RETURNING id`,
		p.company.ID, p.buyer.ID, kind, quote, a.Total.Currency, linesJSON,
		a.Subtotal.AmountMinor, a.Total.AmountMinor, method, p.po, snapshotOf(in), key,
		strings.ToLower(p.buyer.Email)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) && key != nil {
		if err := m.db.QueryRowContext(ctx, `
			SELECT id FROM b2b_approvals WHERE requested_by = $1 AND idempotency_key = $2`,
			p.buyer.ID, *key).Scan(&id); err != nil {
			return nil, err
		}
		return m.Approval(ctx, id)
	}
	if err != nil {
		return nil, err
	}
	approval, err := m.Approval(ctx, id)
	if err != nil {
		return nil, err
	}
	m.notifyApprovers(ctx, p.company, approval)
	return approval, nil
}

const approvalColumns = `a.id, a.company_id, a.kind, a.quote_id, a.status, a.requested_by,
	a.requested_by_email, a.lines, a.subtotal_minor, a.total_minor, a.currency,
	a.payment_method, a.po_number, a.decided_by, a.decided_at, a.reason, a.last_error,
	a.order_id, coalesce(a.order_number, ''), a.created_at, a.updated_at`

func (m *Module) scanApproval(row rowScanner) (*Approval, error) {
	a := &Approval{}
	var quote, decidedBy, order sql.NullInt64
	var decidedAt sql.NullTime
	var lines []byte
	var currency string
	if err := row.Scan(&a.ID, &a.CompanyID, &a.Kind, &quote, &a.Status, &a.RequestedBy,
		&a.RequesterMail, &lines, &a.Subtotal.AmountMinor, &a.Total.AmountMinor, &currency,
		&a.PaymentMethod, &a.PONumber, &decidedBy, &decidedAt, &a.Reason, &a.LastError,
		&order, &a.OrderNumber, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	a.Subtotal.Currency, a.Total.Currency = currency, currency
	if err := json.Unmarshal(lines, &a.Lines); err != nil {
		return nil, err
	}
	if quote.Valid {
		a.QuoteID = &quote.Int64
	}
	if decidedBy.Valid {
		a.DecidedBy = &decidedBy.Int64
	}
	if decidedAt.Valid {
		t := decidedAt.Time
		a.DecidedAt = &t
	}
	if order.Valid {
		a.OrderID = &order.Int64
	}
	return a, nil
}

const approvalFrom = ` FROM b2b_approvals a`

// Approval reads one request.
func (m *Module) Approval(ctx context.Context, id int64) (*Approval, error) {
	a, err := m.scanApproval(m.db.QueryRowContext(ctx,
		`SELECT `+approvalColumns+approvalFrom+` WHERE a.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("approval %d does not exist", id)
	}
	return a, err
}

// Approvals lists requests, newest first. A zero companyID is every
// company's; requestedBy narrows to one buyer's; an empty status is all.
func (m *Module) Approvals(ctx context.Context, companyID, requestedBy int64, status string, limit, offset int) ([]*Approval, int, error) {
	where, args := []string{"true"}, []any{}
	if companyID > 0 {
		args = append(args, companyID)
		where = append(where, "a.company_id = $"+strconv.Itoa(len(args)))
	}
	if requestedBy > 0 {
		args = append(args, requestedBy)
		where = append(where, "a.requested_by = $"+strconv.Itoa(len(args)))
	}
	if status != "" {
		args = append(args, status)
		where = append(where, "a.status = $"+strconv.Itoa(len(args)))
	}
	clause := strings.Join(where, " AND ")
	var total int
	if err := m.db.QueryRowContext(ctx,
		`SELECT count(*) FROM b2b_approvals a WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := m.db.QueryContext(ctx, `SELECT `+approvalColumns+approvalFrom+` WHERE `+clause+
		` ORDER BY a.id DESC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Approval{}
	for rows.Next() {
		a, err := m.scanApproval(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// Approve places the order an approver said yes to. The request is claimed
// first â€” pending to placing, in one statement â€” so two approvers clicking
// at once place it once. A failure to place, an item sold out or the credit
// limit since used up, puts it back to pending with the reason, for the
// approver to read and try again or reject.
func (m *Module) Approve(ctx context.Context, approver *identity.Customer, id int64) (*Approval, *gocommerce.CheckoutResult, error) {
	mem, company, err := m.memberAndCompany(ctx, approver)
	if err != nil {
		return nil, nil, err
	}
	if mem.Role != RoleAdmin && mem.Role != RoleApprover {
		return nil, nil, gocommerce.Forbiddenf("only an approver or an admin may approve an order")
	}
	res, err := m.db.ExecContext(ctx, `
		UPDATE b2b_approvals SET status = 'placing', updated_at = now()
		WHERE id = $1 AND company_id = $2 AND status = 'pending'`, id, company.ID)
	if err != nil {
		return nil, nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := m.approvalFor(ctx, company.ID, id); err != nil {
			return nil, nil, err
		}
		return nil, nil, gocommerce.Conflictf("that request is no longer waiting for approval")
	}

	result, placeErr := m.placeApproved(ctx, company, id)
	if placeErr != nil {
		msg := placeErr.Error()
		var apiErr *gocommerce.APIError
		if errors.As(placeErr, &apiErr) {
			msg = apiErr.Message
		}
		if _, err := m.db.ExecContext(ctx, `
			UPDATE b2b_approvals SET status = 'pending', last_error = $2, updated_at = now()
			WHERE id = $1`, id, msg); err != nil {
			m.app.Log().Error("b2b: could not release an approval after a failed placement", "approval", id, "error", err)
		}
		return nil, nil, placeErr
	}
	if _, err := m.db.ExecContext(ctx, `
		UPDATE b2b_approvals
		SET status = 'approved', decided_by = $2, decided_at = now(), last_error = '',
		    order_id = $3, order_number = $4, updated_at = now()
		WHERE id = $1`, id, approver.ID, result.Order.ID, result.Order.Number); err != nil {
		return nil, nil, err
	}
	approval, err := m.Approval(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	m.notifyDecision(ctx, company, approval)
	return approval, result, nil
}

// placeApproved places the snapshot an approval holds, on the requester's
// behalf â€” it is their order, an approver only said yes to it.
func (m *Module) placeApproved(ctx context.Context, company *Company, id int64) (*gocommerce.CheckoutResult, error) {
	var requestedBy int64
	var kind, method, po string
	var quote sql.NullInt64
	var linesJSON, checkoutJSON []byte
	if err := m.db.QueryRowContext(ctx, `
		SELECT requested_by, kind, quote_id, payment_method, po_number, lines, checkout
		FROM b2b_approvals WHERE id = $1`, id).
		Scan(&requestedBy, &kind, &quote, &method, &po, &linesJSON, &checkoutJSON); err != nil {
		return nil, err
	}
	var lines []ApprovalLine
	if err := json.Unmarshal(linesJSON, &lines); err != nil {
		return nil, err
	}
	var snap checkoutSnapshot
	if err := json.Unmarshal(checkoutJSON, &snap); err != nil {
		return nil, err
	}
	requester, err := m.accounts.CustomerByID(ctx, requestedBy)
	if err != nil {
		return nil, gocommerce.Conflictf("the buyer who asked for this no longer has an account")
	}
	reqMem, err := m.MemberOf(ctx, requestedBy)
	if err != nil || reqMem.CompanyID != company.ID {
		return nil, gocommerce.Conflictf("the buyer who asked for this no longer buys for %s", company.Name)
	}
	// The buyer accepted this quote while it was on offer, and accepting it
	// claimed it. Its expiry since then does not matter: the approver is
	// approving an acceptance that was made in time.
	if kind == "quote" && quote.Valid {
		q, err := m.Quote(ctx, quote.Int64)
		if err != nil {
			return nil, err
		}
		if q.Status != QuoteAwaitingApproval {
			return nil, gocommerce.Conflictf("quote %s is %s, not waiting for this approval", q.Number, q.Status)
		}
	}
	p := &placement{company: company, buyer: requester, member: reqMem, po: po, approved: true, approvalID: id}
	if quote.Valid {
		p.quoteID = quote.Int64
	}
	in := gocommerce.CheckoutInput{Email: snap.Email, Name: snap.Name, Phone: snap.Phone, Address: snap.Address}
	result, err := m.placeAgreed(ctx, p, method, in, lines)
	if err != nil {
		return nil, err
	}
	if quote.Valid {
		m.markQuoteAccepted(ctx, quote.Int64, result.Order)
	}
	return result, nil
}

// Reject turns a request down.
func (m *Module) Reject(ctx context.Context, approver *identity.Customer, id int64, reason string) (*Approval, error) {
	mem, company, err := m.memberAndCompany(ctx, approver)
	if err != nil {
		return nil, err
	}
	if mem.Role != RoleAdmin && mem.Role != RoleApprover {
		return nil, gocommerce.Forbiddenf("only an approver or an admin may reject an order")
	}
	return m.closeApproval(ctx, company, id, ApprovalRejected, &approver.ID, reason, true)
}

// CancelApproval withdraws a request. Only its requester may: an approver
// who does not want it rejects it, and says why.
func (m *Module) CancelApproval(ctx context.Context, buyer *identity.Customer, id int64) (*Approval, error) {
	_, company, err := m.memberAndCompany(ctx, buyer)
	if err != nil {
		return nil, err
	}
	a, err := m.approvalFor(ctx, company.ID, id)
	if err != nil {
		return nil, err
	}
	if a.RequestedBy != buyer.ID {
		return nil, gocommerce.Forbiddenf("only the buyer who asked may withdraw a request")
	}
	return m.closeApproval(ctx, company, id, ApprovalCancelled, nil, "", false)
}

func (m *Module) closeApproval(ctx context.Context, company *Company, id int64, status string, by *int64, reason string, notify bool) (*Approval, error) {
	res, err := m.db.ExecContext(ctx, `
		UPDATE b2b_approvals
		SET status = $3, decided_by = $4, decided_at = now(), reason = $5, updated_at = now()
		WHERE id = $1 AND company_id = $2 AND status = 'pending'`,
		id, company.ID, status, by, strings.TrimSpace(reason))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := m.approvalFor(ctx, company.ID, id); err != nil {
			return nil, err
		}
		return nil, gocommerce.Conflictf("that request is no longer waiting for approval")
	}
	a, err := m.Approval(ctx, id)
	if err != nil {
		return nil, err
	}
	// A quote accepted into an approval was claimed; turned down, it goes
	// back on offer for as long as it was going to be.
	if a.Kind == "quote" && a.QuoteID != nil {
		if _, err := m.db.ExecContext(ctx, `
			UPDATE b2b_quotes SET status = 'quoted', updated_at = now()
			WHERE id = $1 AND status = 'awaiting_approval'`, *a.QuoteID); err != nil {
			return nil, err
		}
	}
	if notify {
		m.notifyDecision(ctx, company, a)
	}
	return a, nil
}

// approvalFor reads a request only if it is this company's, and answers 404
// rather than 403 otherwise: another company's requests are not something to
// confirm the existence of.
func (m *Module) approvalFor(ctx context.Context, companyID, id int64) (*Approval, error) {
	a, err := m.Approval(ctx, id)
	if err != nil {
		return nil, err
	}
	if a.CompanyID != companyID {
		return nil, gocommerce.NotFoundf("approval %d does not exist", id)
	}
	return a, nil
}

// reconcileApprovals settles a request a crash left in "placing". If its
// order exists — the order's metadata names the approval, in the order's own
// transaction — it was approved; if not, and long enough has passed that no
// placement can still be running, it goes back to pending for an approver to
// try again.
func (m *Module) reconcileApprovals(ctx context.Context) error {
	if _, err := m.db.ExecContext(ctx, `
		UPDATE b2b_approvals a
		SET status = 'approved', order_id = o.id, order_number = o.number,
		    decided_at = coalesce(a.decided_at, now()), updated_at = now()
		FROM orders o
		WHERE a.status = 'placing'
		  AND o.metadata -> 'b2b' ->> 'approval_id' = a.id::text`); err != nil {
		return err
	}
	_, err := m.db.ExecContext(ctx, `
		UPDATE b2b_approvals
		SET status = 'pending', last_error = 'the order was interrupted while being placed; approve it again',
		    updated_at = now()
		WHERE status = 'placing' AND updated_at < now() - interval '10 minutes'`)
	return err
}
