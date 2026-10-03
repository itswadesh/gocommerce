package b2b

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/ext/identity"
)

// Quote states. Expiry is read, not stored: a quoted quote past its
// expires_at reads as expired, so nothing has to run at midnight to make it
// true.
const (
	QuoteRequested = "requested"
	QuoteQuoted    = "quoted"
	QuoteAccepted  = "accepted"
	QuoteDeclined  = "declined"
	QuoteExpired   = "expired"
	// QuoteAwaitingApproval is a quote a buyer accepted over their approval
	// limit: claimed, so it cannot be accepted twice, and back to quoted if
	// the approval is turned down.
	QuoteAwaitingApproval = "awaiting_approval"
)

// QuoteLineInput is one line a buyer asks to be quoted, or a merchant prices.
type QuoteLineInput struct {
	VariantID int64 `json:"variant_id"`
	Quantity  int   `json:"quantity"`
	// UnitPriceMinor is the merchant's price. A buyer's request leaves it out.
	UnitPriceMinor *int64 `json:"unit_price_minor,omitempty"`
}

// QuoteRequest is a buyer asking for a price.
type QuoteRequest struct {
	Lines []QuoteLineInput `json:"lines"`
	Note  string           `json:"note"`
}

// QuoteReply is the merchant pricing a quote. Lines, when given, replace the
// quote's lines — a merchant may suggest a different quantity or add the
// accessory the buyer forgot — and every line must carry a price for the
// quote to be sent.
type QuoteReply struct {
	Lines     []QuoteLineInput `json:"lines"`
	Reply     *string          `json:"reply"`
	ExpiresAt *time.Time       `json:"expires_at"`
}

// QuoteAcceptance is a buyer taking a quote, with where it is going.
type QuoteAcceptance struct {
	PaymentMethod string             `json:"payment_method"`
	PONumber      string             `json:"po_number"`
	Name          string             `json:"name"`
	Phone         string             `json:"phone"`
	Address       gocommerce.Address `json:"address"`
}

const quoteColumns = `q.id, q.company_id, c.name, q.status, q.requested_by, q.requested_by_email,
	q.note, q.reply, q.declined_by, q.currency, q.expires_at, q.quoted_at, q.accepted_at,
	q.order_id, coalesce(q.order_number, ''), q.created_at, q.updated_at`

const quoteFrom = ` FROM b2b_quotes q
	JOIN b2b_companies c ON c.id = q.company_id`

func (m *Module) scanQuote(row rowScanner) (*Quote, string, error) {
	q := &Quote{}
	var currency string
	var expires, quoted, accepted sql.NullTime
	var order sql.NullInt64
	if err := row.Scan(&q.ID, &q.CompanyID, &q.CompanyName, &q.Status, &q.RequestedBy,
		&q.RequesterMail, &q.Note, &q.Reply, &q.DeclinedBy, &currency, &expires, &quoted,
		&accepted, &order, &q.OrderNumber, &q.CreatedAt, &q.UpdatedAt); err != nil {
		return nil, "", err
	}
	q.Number = quoteNumber(q.ID)
	for dst, src := range map[**time.Time]sql.NullTime{&q.ExpiresAt: expires, &q.QuotedAt: quoted, &q.AcceptedAt: accepted} {
		if src.Valid {
			t := src.Time
			*dst = &t
		}
	}
	if order.Valid {
		q.OrderID = &order.Int64
	}
	if q.Status == QuoteQuoted && q.ExpiresAt != nil && q.ExpiresAt.Before(time.Now()) {
		q.Status = QuoteExpired
	}
	return q, currency, nil
}

// Quote reads one quote with its lines.
func (m *Module) Quote(ctx context.Context, id int64) (*Quote, error) {
	q, currency, err := m.scanQuote(m.db.QueryRowContext(ctx,
		`SELECT `+quoteColumns+quoteFrom+` WHERE q.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("quote %d does not exist", id)
	}
	if err != nil {
		return nil, err
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT variant_id, sku, title, quantity, unit_price_minor
		FROM b2b_quote_lines WHERE quote_id = $1 ORDER BY position, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	q.Lines = []QuoteLine{}
	var total int64
	priced := true
	for rows.Next() {
		var l QuoteLine
		var price sql.NullInt64
		if err := rows.Scan(&l.VariantID, &l.SKU, &l.Title, &l.Quantity, &price); err != nil {
			return nil, err
		}
		if price.Valid {
			l.UnitPrice = &gocommerce.Money{AmountMinor: price.Int64, Currency: currency}
			total += price.Int64 * int64(l.Quantity)
		} else {
			priced = false
		}
		q.Lines = append(q.Lines, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if priced && len(q.Lines) > 0 {
		q.Total = &gocommerce.Money{AmountMinor: total, Currency: currency}
	}
	return q, nil
}

// Quotes lists quotes, newest first. A zero companyID is every company's;
// requestedBy narrows to one buyer's; status filters, "expired" included.
func (m *Module) Quotes(ctx context.Context, companyID, requestedBy int64, status string, limit, offset int) ([]*Quote, int, error) {
	where, args := []string{"true"}, []any{}
	if companyID > 0 {
		args = append(args, companyID)
		where = append(where, "q.company_id = $"+strconv.Itoa(len(args)))
	}
	if requestedBy > 0 {
		args = append(args, requestedBy)
		where = append(where, "q.requested_by = $"+strconv.Itoa(len(args)))
	}
	switch status {
	case "":
	case QuoteExpired:
		where = append(where, "q.status = 'quoted' AND q.expires_at < now()")
	case QuoteQuoted:
		where = append(where, "q.status = 'quoted' AND (q.expires_at IS NULL OR q.expires_at >= now())")
	default:
		args = append(args, status)
		where = append(where, "q.status = $"+strconv.Itoa(len(args)))
	}
	clause := strings.Join(where, " AND ")
	var total int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*)`+quoteFrom+` WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := m.db.QueryContext(ctx, `SELECT q.id`+quoteFrom+` WHERE `+clause+
		` ORDER BY q.id DESC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := []*Quote{}
	for _, id := range ids {
		q, err := m.Quote(ctx, id)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, q)
	}
	return out, total, rows.Err()
}

// RequestQuote files a buyer's request for a price.
func (m *Module) RequestQuote(ctx context.Context, buyer *identity.Customer, req QuoteRequest) (*Quote, error) {
	_, company, err := m.memberAndCompany(ctx, buyer)
	if err != nil {
		return nil, err
	}
	if company.Status == StatusClosed {
		return nil, gocommerce.Forbiddenf("%s's account is closed", company.Name)
	}
	var id int64
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO b2b_quotes (company_id, requested_by, requested_by_email, currency, note)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			company.ID, buyer.ID, strings.ToLower(buyer.Email), m.app.Config().Currency,
			strings.TrimSpace(req.Note)).Scan(&id); err != nil {
			return err
		}
		// A buyer asks for quantities, never prices: whatever they send in
		// unit_price_minor is not what the store offers.
		stripped := make([]QuoteLineInput, len(req.Lines))
		for i, l := range req.Lines {
			stripped[i] = QuoteLineInput{VariantID: l.VariantID, Quantity: l.Quantity}
		}
		return m.writeQuoteLines(ctx, tx, id, stripped, false, company)
	})
	if err != nil {
		return nil, err
	}
	return m.Quote(ctx, id)
}

// writeQuoteLines replaces a quote's lines, reading each variant's SKU and
// title so the quote still says what it was for if the product is renamed.
//
// A line outside the company's catalogue is refused, whichever side wrote it:
// the buyer could not accept a quote that holds one — the guard refuses its
// order — so the store must not be able to send one either.
func (m *Module) writeQuoteLines(ctx context.Context, tx *sql.Tx, quoteID int64, lines []QuoteLineInput, needPrices bool, company *Company) error {
	if len(lines) == 0 {
		return gocommerce.Validationf("a quote needs at least one line")
	}
	type written struct {
		variantID, productID int64
		sku                  string
	}
	var wrote []written
	seen := map[int64]bool{}
	if _, err := tx.ExecContext(ctx, `DELETE FROM b2b_quote_lines WHERE quote_id = $1`, quoteID); err != nil {
		return err
	}
	for i, l := range lines {
		if l.Quantity <= 0 {
			return gocommerce.Validationf("every line needs a quantity")
		}
		if seen[l.VariantID] {
			return gocommerce.Validationf("variant %d is listed twice", l.VariantID)
		}
		seen[l.VariantID] = true
		if needPrices && l.UnitPriceMinor == nil {
			return gocommerce.Validationf("variant %d has no price", l.VariantID)
		}
		if l.UnitPriceMinor != nil && *l.UnitPriceMinor < 0 {
			return gocommerce.Validationf("a price must not be negative")
		}
		var sku, title string
		var productID int64
		err := tx.QueryRowContext(ctx, `
			SELECT v.sku, p.title, p.id FROM variants v JOIN products p ON p.id = v.product_id
			WHERE v.id = $1`, l.VariantID).Scan(&sku, &title, &productID)
		if errors.Is(err, sql.ErrNoRows) {
			return gocommerce.Validationf("variant %d does not exist", l.VariantID)
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO b2b_quote_lines (quote_id, variant_id, sku, title, quantity, unit_price_minor, position)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			quoteID, l.VariantID, sku, title, l.Quantity, l.UnitPriceMinor, i); err != nil {
			return err
		}
		wrote = append(wrote, written{variantID: l.VariantID, productID: productID, sku: sku})
	}
	if company == nil || company.CatalogueID == nil {
		return nil
	}
	ids := make([]int64, len(wrote))
	for i, w := range wrote {
		ids[i] = w.productID
	}
	outside, err := outsideCatalogue(ctx, tx, *company.CatalogueID, ids)
	if err != nil || len(outside) == 0 {
		return err
	}
	var conflicts []gocommerce.LineConflict
	for _, w := range wrote {
		if outside[w.productID] {
			conflicts = append(conflicts, gocommerce.LineConflict{VariantID: w.variantID, SKU: w.sku, Reason: RejectNotInCatalogue})
		}
	}
	return notInCatalogue(company, conflicts)
}

// PriceQuote is the merchant answering a quote: prices, a reply and an
// expiry. It can be called again until the quote is sent; a sent quote that
// needs changing is re-priced the same way and sent again.
func (m *Module) PriceQuote(ctx context.Context, id int64, in QuoteReply) (*Quote, error) {
	q, err := m.Quote(ctx, id)
	if err != nil {
		return nil, err
	}
	switch q.Status {
	case QuoteRequested, QuoteQuoted, QuoteExpired:
	default:
		return nil, gocommerce.Conflictf("quote %s is %s and can no longer be changed", q.Number, q.Status)
	}
	company, err := m.Company(ctx, q.CompanyID)
	if err != nil {
		return nil, err
	}
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		if in.Lines != nil {
			if err := m.writeQuoteLines(ctx, tx, id, in.Lines, false, company); err != nil {
				return err
			}
		}
		sets := []string{"updated_at = now()"}
		args := []any{id}
		if in.Reply != nil {
			args = append(args, strings.TrimSpace(*in.Reply))
			sets = append(sets, "reply = $"+strconv.Itoa(len(args)))
		}
		if in.ExpiresAt != nil {
			args = append(args, *in.ExpiresAt)
			sets = append(sets, "expires_at = $"+strconv.Itoa(len(args)))
		}
		// Changing a sent quote takes it back to requested: the buyer must
		// not accept a price that is being rewritten under them.
		sets = append(sets, "status = 'requested'")
		_, err := tx.ExecContext(ctx, `UPDATE b2b_quotes SET `+strings.Join(sets, ", ")+` WHERE id = $1`, args...)
		return err
	})
	if err != nil {
		return nil, err
	}
	return m.Quote(ctx, id)
}

// SendQuote offers a priced quote to the buyer. Every line must carry a price,
// and the quote gets the configured lifetime when it has no expiry of its own.
func (m *Module) SendQuote(ctx context.Context, id int64) (*Quote, error) {
	q, err := m.Quote(ctx, id)
	if err != nil {
		return nil, err
	}
	if q.Status != QuoteRequested {
		return nil, gocommerce.Conflictf("quote %s is %s; only a requested quote can be sent", q.Number, q.Status)
	}
	if q.Total == nil {
		return nil, gocommerce.Validationf("every line needs a price before the quote can be sent")
	}
	// The catalogue may have narrowed since the lines were written, and a
	// quote the buyer cannot accept is not worth sending.
	if err := m.quoteInCatalogue(ctx, q); err != nil {
		return nil, err
	}
	expires := time.Now().Add(m.cfg.QuoteTTL)
	if q.ExpiresAt != nil {
		if !q.ExpiresAt.After(time.Now()) {
			return nil, gocommerce.Validationf("expires_at is in the past")
		}
		expires = *q.ExpiresAt
	}
	if _, err := m.db.ExecContext(ctx, `
		UPDATE b2b_quotes SET status = 'quoted', quoted_at = now(), expires_at = $2, updated_at = now()
		WHERE id = $1 AND status = 'requested'`, id, expires); err != nil {
		return nil, err
	}
	q, err = m.Quote(ctx, id)
	if err != nil {
		return nil, err
	}
	m.notifyQuoteReady(ctx, q)
	return q, nil
}

// DeclineQuote closes a quote without an order, from either side. by records
// which side: "store" or the buyer's address.
func (m *Module) DeclineQuote(ctx context.Context, id int64, by string) (*Quote, error) {
	res, err := m.db.ExecContext(ctx, `
		UPDATE b2b_quotes SET status = 'declined', declined_by = $2, updated_at = now()
		WHERE id = $1 AND status IN ('requested', 'quoted')`, id, by)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		q, err := m.Quote(ctx, id)
		if err != nil {
			return nil, err
		}
		return nil, gocommerce.Conflictf("quote %s is %s and cannot be declined", q.Number, q.Status)
	}
	return m.Quote(ctx, id)
}

// AcceptQuote places the order a quote offered, at its prices. A buyer whose
// quote is over the company's approval limit files it for approval instead,
// and the approval places it.
func (m *Module) AcceptQuote(ctx context.Context, buyer *identity.Customer, id int64, in QuoteAcceptance) (*gocommerce.CheckoutResult, *Approval, error) {
	mem, company, err := m.memberAndCompany(ctx, buyer)
	if err != nil {
		return nil, nil, err
	}
	q, err := m.quoteFor(ctx, company.ID, id)
	if err != nil {
		return nil, nil, err
	}
	if q.RequestedBy != buyer.ID && mem.Role != RoleAdmin && mem.Role != RoleApprover {
		return nil, nil, gocommerce.NotFoundf("quote %d does not exist", id)
	}
	if err := m.quoteStillOpen(ctx, q.ID); err != nil {
		return nil, nil, err
	}
	method := strings.TrimSpace(in.PaymentMethod)
	if method == "" {
		if company.CreditLimit == nil {
			return nil, nil, gocommerce.Validationf("%s has no account with this store; choose a payment_method", company.Name)
		}
		method = CodeOnAccount
	}
	lines := make([]ApprovalLine, 0, len(q.Lines))
	for _, l := range q.Lines {
		lines = append(lines, ApprovalLine{VariantID: l.VariantID, SKU: l.SKU, Quantity: l.Quantity, UnitPrice: *l.UnitPrice})
	}
	p := &placement{company: company, buyer: buyer, member: mem, po: strings.TrimSpace(in.PONumber), quoteID: q.ID}
	checkout := gocommerce.CheckoutInput{
		Email: buyer.Email, Name: firstNonEmpty(in.Name, buyer.Name),
		Phone: firstNonEmpty(in.Phone, buyer.Phone), Address: in.Address,
	}
	// Claim it first, in one statement, so two accepts at once place one
	// order: the second finds it no longer on offer. A failure to place hands
	// it back.
	res, err := m.db.ExecContext(ctx, `
		UPDATE b2b_quotes SET status = 'awaiting_approval', updated_at = now()
		WHERE id = $1 AND status = 'quoted' AND (expires_at IS NULL OR expires_at >= now())`, q.ID)
	if err != nil {
		return nil, nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil, gocommerce.Conflictf("quote %s is no longer on offer", q.Number)
	}
	release := func() {
		if _, err := m.db.ExecContext(ctx, `
			UPDATE b2b_quotes SET status = 'quoted', updated_at = now()
			WHERE id = $1 AND status = 'awaiting_approval'`, q.ID); err != nil {
			m.app.Log().Error("b2b: could not hand a quote back after a failed acceptance", "quote", q.ID, "error", err)
		}
	}

	result, err := m.placeAgreed(ctx, p, method, checkout, lines)
	if err != nil {
		var apiErr *gocommerce.APIError
		if errors.As(err, &apiErr) && apiErr.Code == codeApprovalRequired && p.pending != nil {
			// Stays claimed while the approval is open.
			approval, aerr := m.requestApproval(ctx, p, "quote", q.ID, method, checkout, "")
			if aerr != nil {
				release()
			}
			return nil, approval, aerr
		}
		release()
		return nil, nil, err
	}
	m.markQuoteAccepted(ctx, q.ID, result.Order)
	return result, nil, nil
}

func (m *Module) markQuoteAccepted(ctx context.Context, id int64, o *gocommerce.Order) {
	if _, err := m.db.ExecContext(ctx, `
		UPDATE b2b_quotes SET status = 'accepted', accepted_at = now(), order_id = $2,
		       order_number = $3, updated_at = now()
		WHERE id = $1`, id, o.ID, o.Number); err != nil {
		m.app.Log().Error("b2b: could not mark a quote accepted", "quote", id, "order", o.ID, "error", err)
	}
}

// quoteStillOpen refuses a quote that is not on offer: not yet sent, already
// taken, declined, or past its expiry.
func (m *Module) quoteStillOpen(ctx context.Context, id int64) error {
	q, err := m.Quote(ctx, id)
	if err != nil {
		return err
	}
	switch q.Status {
	case QuoteQuoted:
		return nil
	case QuoteExpired:
		return &gocommerce.APIError{Status: http.StatusConflict, Code: "quote_expired",
			Message: "quote " + q.Number + " expired; ask for it to be quoted again"}
	default:
		return gocommerce.Conflictf("quote %s is %s, not on offer", q.Number, q.Status)
	}
}

// quoteFor reads a quote only if it is this company's; another company's is
// a 404, not a 403.
func (m *Module) quoteFor(ctx context.Context, companyID, id int64) (*Quote, error) {
	q, err := m.Quote(ctx, id)
	if err != nil {
		return nil, err
	}
	if q.CompanyID != companyID {
		return nil, gocommerce.NotFoundf("quote %d does not exist", id)
	}
	return q, nil
}
