package b2b

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/ext/identity"
)

// placement is a checkout this module is making on a company's behalf. Its
// presence in the context is what tells the guard, and the on-account
// method, that the order is coming through here.
type placement struct {
	company  *Company
	buyer    *identity.Customer
	member   *Member
	po       string
	approved bool // an approver has already said yes: skip the threshold
	// What the order is for, written into its metadata.
	approvalID, quoteID int64

	// Filled by the guard when it refuses for want of approval, so the
	// caller can file the request with the figures that triggered it.
	pending *gocommerce.CheckoutAttempt
}

type placementKey struct{}

func withPlacement(ctx context.Context, p *placement) context.Context {
	return context.WithValue(ctx, placementKey{}, p)
}

func placementFrom(ctx context.Context) *placement {
	p, _ := ctx.Value(placementKey{}).(*placement)
	return p
}

// errApprovalRequired is the guard's answer when a buyer's order is over the
// company's threshold. The checkout route turns it into an approval request
// rather than an error, so a client sees it only from a path that cannot.
const codeApprovalRequired = "approval_required"

// ------------------------------------------------------------ on account

// onAccount is the payment method for an order the company pays for later.
type onAccount struct{ m *Module }

func (p *onAccount) Code() string        { return CodeOnAccount }
func (p *onAccount) DisplayName() string { return "On account" }

// Configured says the method exists only inside this module's own checkout.
// Everywhere else — the public method list, a public checkout, a phone order
// through the panel — it is absent, which is the answer a storefront should
// get: nobody pays on account without being a buyer for a company that has
// one, and only this module knows who that is.
func (p *onAccount) Configured(ctx context.Context) bool {
	return placementFrom(ctx) != nil
}

// Initiate takes no money: the order is confirmed and the payment stays
// pending until the invoice is settled and somebody marks it paid.
func (p *onAccount) Initiate(ctx context.Context, o *gocommerce.Order, _ gocommerce.PayOptions) (gocommerce.PaymentIntent, error) {
	return gocommerce.PaymentIntent{Kind: gocommerce.IntentNone}, nil
}

// ------------------------------------------------------------------ guard

// guard is the module's checkout guard (D67). It runs inside phase A with the
// order's final figures, which is what makes a credit limit a control.
func (m *Module) guard(ctx context.Context, a *gocommerce.CheckoutAttempt) error {
	p := placementFrom(ctx)
	if p == nil {
		return m.guardForeign(ctx, a)
	}

	// One decision per company at a time. Without it two buyers checking out
	// together could each see room under the limit that only one of them
	// fits in; the lock is released when this checkout commits or rolls back,
	// so the second sees the first's order.
	if _, err := a.Tx.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('b2b.company.' || $1::bigint, 0))`, p.company.ID); err != nil {
		return err
	}
	// Read under the lock, so a limit lowered a moment ago is the one applied.
	var status string
	var limit, threshold, catalogue sql.NullInt64
	var requirePO bool
	if err := a.Tx.QueryRowContext(ctx, `
		SELECT status, credit_limit_minor, approval_threshold_minor, require_po, catalogue_id
		FROM b2b_companies WHERE id = $1`, p.company.ID).
		Scan(&status, &limit, &threshold, &requirePO, &catalogue); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return gocommerce.NotFoundf("that company no longer exists")
		}
		return err
	}
	switch {
	case status == StatusClosed:
		return gocommerce.Forbiddenf("%s's account is closed", p.company.Name)
	case requirePO && strings.TrimSpace(p.po) == "":
		return gocommerce.Validationf("%s requires a purchase order number on every order", p.company.Name)
	}

	// Before the approval threshold: a line the company may not buy is
	// refused now, not filed for an approver whose yes would be refused here
	// anyway. An approved basket and an accepted quote come through here too,
	// so a catalogue narrowed since they were asked for still holds.
	if catalogue.Valid {
		if err := m.guardCatalogue(ctx, a, p.company, catalogue.Int64); err != nil {
			return err
		}
	}

	if !p.approved && p.member != nil && p.member.Role == RoleBuyer &&
		threshold.Valid && a.Total.AmountMinor > threshold.Int64 {
		snapshot := *a
		snapshot.Tx = nil
		p.pending = &snapshot
		return &gocommerce.APIError{Status: http.StatusConflict, Code: codeApprovalRequired,
			Message: "this order is over " + p.company.Name + "'s approval limit and needs an approver"}
	}

	if a.Method != CodeOnAccount {
		return nil
	}
	if status == StatusOnHold {
		return gocommerce.Forbiddenf("%s's account is on hold; pay for this order another way", p.company.Name)
	}
	if !limit.Valid {
		return gocommerce.Forbiddenf("%s has no account with this store; pay for this order another way", p.company.Name)
	}
	outstanding, _, _, err := m.outstanding(ctx, a.Tx, p.company.ID)
	if err != nil {
		return err
	}
	if outstanding+a.Total.AmountMinor > limit.Int64 {
		available := limit.Int64 - outstanding
		if available < 0 {
			available = 0
		}
		return &gocommerce.APIError{Status: http.StatusForbidden, Code: "credit_limit_exceeded",
			Message: "this order is over " + p.company.Name + "'s credit limit: " +
				strconv.FormatInt(available, 10) + " of " + strconv.FormatInt(limit.Int64, 10) +
				" (minor units) is available"}
	}
	return nil
}

// guardForeign judges a checkout that did not come through this module.
func (m *Module) guardForeign(ctx context.Context, a *gocommerce.CheckoutAttempt) error {
	// Configured already hides the method, so this is the belt to its braces.
	if a.Method == CodeOnAccount {
		return gocommerce.Forbiddenf("orders on account are placed through the company's account")
	}
	// The key this module writes into an order's metadata is reserved, so
	// that reading it back means something.
	if _, ok := a.Input.Metadata[metaKey]; ok {
		return gocommerce.Validationf("metadata.%s is reserved for orders placed for a company", metaKey)
	}
	// An operator placing an order by hand is trusted, as everywhere else.
	if a.ByOperator || a.VerifiedEmail == "" {
		return nil
	}
	// A basket priced as a company's buyer carries the company's prices, and
	// the company's rules come with them: an order placed around this module
	// would skip the approval a junior buyer needs. The guard reads the
	// member row with the checkout's own transaction.
	var company string
	err := a.Tx.QueryRowContext(ctx, `
		SELECT c.name FROM b2b_members bm JOIN b2b_companies c ON c.id = bm.company_id
		WHERE bm.email = $1`, strings.ToLower(a.VerifiedEmail)).Scan(&company)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return &gocommerce.APIError{Status: http.StatusForbidden, Code: "company_checkout_required",
		Message: "this basket carries " + company + "'s prices; place it through the company account (POST /x/b2b/checkout)"}
}

// outstanding is what a company owes on account: orders placed on account
// that are neither paid nor cancelled. It reads the orders themselves rather
// than this module's ledger, because the ledger row is written after the
// order commits and the order's metadata is written with it.
func (m *Module) outstanding(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, companyID int64) (owed, overdue int64, overdueOrders int, err error) {
	err = q.QueryRowContext(ctx, `
		SELECT coalesce(sum(o.total_minor), 0),
		       coalesce(sum(o.total_minor) FILTER (WHERE bo.due_at < now()), 0),
		       count(*) FILTER (WHERE bo.due_at < now())
		FROM orders o
		LEFT JOIN b2b_orders bo ON bo.order_id = o.id
		WHERE o.payment_provider = $2
		  AND o.payment_status IN ('pending', 'failed')
		  AND o.status <> 'cancelled'
		  AND o.metadata -> 'b2b' ->> 'company_id' = $1`,
		strconv.FormatInt(companyID, 10), CodeOnAccount).Scan(&owed, &overdue, &overdueOrders)
	return
}

// Credit reports where a company stands on its account.
func (m *Module) Credit(ctx context.Context, companyID int64) (*Credit, error) {
	c, err := m.Company(ctx, companyID)
	if err != nil {
		return nil, err
	}
	owed, overdue, overdueOrders, err := m.outstanding(ctx, m.db, companyID)
	if err != nil {
		return nil, err
	}
	out := &Credit{
		Limit: c.CreditLimit, Outstanding: m.money(owed), Overdue: m.money(overdue),
		OverdueOrders: overdueOrders, NetDays: c.NetDays,
	}
	if c.CreditLimit != nil {
		avail := c.CreditLimit.AmountMinor - owed
		if avail < 0 {
			avail = 0
		}
		a := m.money(avail)
		out.Available = &a
	}
	return out, nil
}

// ------------------------------------------------------------- placing

// CheckoutRequest is a buyer checking out their cart for their company.
type CheckoutRequest struct {
	CartID string `json:"cart_id"`
	// PaymentMethod defaults to on_account when the company has an account.
	PaymentMethod  string             `json:"payment_method"`
	PONumber       string             `json:"po_number"`
	Name           string             `json:"name"`
	Phone          string             `json:"phone"`
	Address        gocommerce.Address `json:"address"`
	ShippingRateID *int64             `json:"shipping_rate_id,omitempty"`
	PaymentData    map[string]string  `json:"payment_data"`
	ReturnURL      string             `json:"return_url"`
	// LineIDs checks out only these lines of the cart, by the ids its
	// line_items carry, and leaves the rest in the basket. Empty is all of it.
	LineIDs []int64 `json:"line_ids,omitempty"`
}

// orderMeta is what an order placed for a company records about it, in the
// same transaction as the order.
func orderMeta(p *placement, netDays int) gocommerce.Metadata {
	b := map[string]any{
		"company_id": p.company.ID,
		"company":    p.company.Code,
		"po_number":  p.po,
		"net_days":   netDays,
	}
	if p.buyer != nil {
		b["placed_by"] = p.buyer.ID
	}
	if p.approvalID != 0 {
		b["approval_id"] = p.approvalID
	}
	if p.quoteID != 0 {
		b["quote_id"] = p.quoteID
	}
	return gocommerce.Metadata{metaKey: b}
}

// Checkout places a buyer's cart for their company. The cart is priced as
// the buyer first — the company's group prices — and the guard then applies
// the company's rules with the final figures. An order over a buyer's
// approval limit becomes an approval request instead, returned with a nil
// result. With LineIDs only those lines are placed; see checkoutLines.
func (m *Module) Checkout(ctx context.Context, buyer *identity.Customer, req CheckoutRequest, idemKey string) (*gocommerce.CheckoutResult, *Approval, error) {
	mem, company, err := m.memberAndCompany(ctx, buyer)
	if err != nil {
		return nil, nil, err
	}
	if company.Status == StatusClosed {
		return nil, nil, gocommerce.Forbiddenf("%s's account is closed", company.Name)
	}
	method := strings.TrimSpace(req.PaymentMethod)
	if method == "" {
		if company.CreditLimit == nil {
			return nil, nil, gocommerce.Validationf("%s has no account with this store; choose a payment_method", company.Name)
		}
		method = CodeOnAccount
	}
	if strings.TrimSpace(req.CartID) == "" {
		return nil, nil, gocommerce.Validationf("cart_id is required")
	}

	p := &placement{company: company, buyer: buyer, member: mem, po: strings.TrimSpace(req.PONumber)}
	in := gocommerce.CheckoutInput{
		CartID: req.CartID, Email: buyer.Email,
		Name: firstNonEmpty(req.Name, buyer.Name), Phone: firstNonEmpty(req.Phone, buyer.Phone),
		Address: req.Address, ShippingRateID: req.ShippingRateID,
		PaymentData: req.PaymentData, ReturnURL: req.ReturnURL,
		Metadata: orderMeta(p, company.NetDays),
	}
	if len(req.LineIDs) > 0 {
		return m.checkoutLines(ctx, p, method, in, req.LineIDs, idemKey)
	}
	if _, err := m.claimForCheckout(ctx, buyer.ID, req.CartID, idemKey); err != nil {
		return nil, nil, err
	}
	return m.place(ctx, p, method, in, idemKey)
}

// claimForCheckout prices a basket as the buyer: their company's group
// prices, through the address identity has confirmed (D66). The claim
// re-prices every line to today's, which is why a buyer's checkout never
// meets price_changed — and why a basket rebuilt from the same lines at
// today's prices is checked out at exactly what this one would be.
//
// A retry of a checkout that went through finds its basket already converted,
// and the claim refuses a converted basket. Core answers that retry from the
// Idempotency-Key, so with a key the refusal is core's to make: the claim
// answers nil, nil, and the caller checks out regardless.
func (m *Module) claimForCheckout(ctx context.Context, buyerID int64, token, idemKey string) (*gocommerce.Cart, error) {
	cart, err := m.accounts.ClaimCart(ctx, buyerID, token)
	if err != nil && strings.TrimSpace(idemKey) != "" && errors.Is(err, gocommerce.ErrConflict) {
		return nil, nil
	}
	return cart, err
}

// place checks a basket out for the company. An order over a buyer's
// approval limit is filed as a request instead, and returned in its place.
func (m *Module) place(ctx context.Context, p *placement, method string, in gocommerce.CheckoutInput, idemKey string) (*gocommerce.CheckoutResult, *Approval, error) {
	result, err := m.app.Order().Checkout(withPlacement(ctx, p), method, in, idemKey)
	if err != nil {
		var apiErr *gocommerce.APIError
		if errors.As(err, &apiErr) && apiErr.Code == codeApprovalRequired && p.pending != nil {
			approval, aerr := m.requestApproval(ctx, p, "cart", 0, method, in, idemKey)
			return nil, approval, aerr
		}
		return nil, nil, err
	}
	if err := m.recordOrder(ctx, p, result.Order, method, p.company.NetDays); err != nil {
		m.app.Log().Error("b2b: could not record a company order; the reconciler will", "order", result.Order.ID, "error", err)
	}
	return result, nil, nil
}

// ------------------------------------------------------- partial checkout

// partialCheckout is a row of b2b_partial_checkouts: the basket the lines came
// from, which lines, and the basket built from them.
type partialCheckout struct {
	id      int64
	source  string
	lineIDs []int64
	cart    string
}

// checkoutLines checks out some of a basket. The chosen lines are copied into
// a basket of their own — on the same storefront, priced as the buyer,
// carrying the same discount code — and that basket goes through place, so
// the guard, the credit limit and the approval threshold judge exactly what
// they would judge in a basket holding only those lines. The lines leave the
// buyer's basket only once the copy has become an order; a request for
// approval filed instead leaves the basket as it was.
func (m *Module) checkoutLines(ctx context.Context, p *placement, method string, in gocommerce.CheckoutInput, lineIDs []int64, idemKey string) (*gocommerce.CheckoutResult, *Approval, error) {
	want, err := distinctIDs(lineIDs)
	if err != nil {
		return nil, nil, err
	}
	key := strings.TrimSpace(idemKey)
	if key != "" {
		// Under a key this may be a retry. The attempt it repeats either filed
		// a request, which is the answer again, or built a basket, which is
		// checked out again: core replays an order from its key only for the
		// same basket, and the first attempt may already have taken the lines
		// out of this one.
		if a, err := m.approvalByKey(ctx, p.buyer.ID, key); err != nil || a != nil {
			return nil, a, err
		}
		prior, err := m.partialWhere(ctx, `customer_id = $1 AND idempotency_key = $2`, p.buyer.ID, key)
		if err != nil {
			return nil, nil, err
		}
		if prior != nil {
			if prior.source != in.CartID || !slices.Equal(prior.lineIDs, want) {
				return nil, nil, gocommerce.Validationf("this Idempotency-Key was already used for a different request")
			}
			if prior.cart == "" {
				return nil, nil, gocommerce.Conflictf("this checkout is still being prepared; try again in a moment")
			}
			return m.placePartial(ctx, p, method, in, prior, idemKey)
		}
	}

	cart, err := m.claimForCheckout(ctx, p.buyer.ID, in.CartID, key)
	if err != nil {
		return nil, nil, err
	}
	if cart == nil {
		// Converted, and retried under a key: the whole basket was checked
		// out, and core answers for it.
		return m.place(ctx, p, method, in, idemKey)
	}
	lines := make(map[int64]gocommerce.CartLine, len(cart.Lines))
	for _, l := range cart.Lines {
		lines[l.ID] = l
	}
	for _, id := range want {
		if _, ok := lines[id]; !ok {
			return nil, nil, gocommerce.Validationf("line %d is not in this cart", id)
		}
	}
	// Every line is the whole basket, and checked out as itself it is
	// converted like any other rather than left open and empty.
	if len(want) == len(cart.Lines) {
		return m.place(ctx, p, method, in, idemKey)
	}

	part, err := m.claimPartial(ctx, p.buyer.ID, key, in.CartID, want)
	if err != nil {
		return nil, nil, err
	}
	part.cart, err = m.buildPartial(ctx, p.buyer.ID, cart, want, lines)
	if err == nil {
		_, err = m.db.ExecContext(ctx, `
			UPDATE b2b_partial_checkouts SET cart_token = $2, updated_at = now() WHERE id = $1`,
			part.id, part.cart)
	}
	if err != nil {
		m.settlePartial(ctx, part)
		return nil, nil, err
	}
	return m.placePartial(ctx, p, method, in, part, idemKey)
}

func (m *Module) placePartial(ctx context.Context, p *placement, method string, in gocommerce.CheckoutInput, part *partialCheckout, idemKey string) (*gocommerce.CheckoutResult, *Approval, error) {
	in.CartID = part.cart
	result, approval, err := m.place(ctx, p, method, in, idemKey)
	m.settlePartial(ctx, part)
	return result, approval, err
}

// distinctIDs is the line ids sorted, refusing a repeat: a line checked out
// twice in one order is a client bug, not twice the quantity.
func distinctIDs(ids []int64) ([]int64, error) {
	out := slices.Clone(ids)
	slices.Sort(out)
	for i, id := range out {
		if id <= 0 {
			return nil, gocommerce.Validationf("line_ids must be positive integers")
		}
		if i > 0 && out[i-1] == id {
			return nil, gocommerce.Validationf("line %d is listed twice", id)
		}
	}
	return out, nil
}

// claimPartial records a partial checkout before anything is built, and that
// is what makes it the only one of that basket in flight: a second submission
// of the same lines finds the first and is refused, as a second checkout of a
// whole basket waits on core's row lock and finds it converted. One left
// behind by a crash, older than any checkout runs, is settled and the claim
// made again.
func (m *Module) claimPartial(ctx context.Context, customerID int64, key, source string, ids []int64) (*partialCheckout, error) {
	idsJSON, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	for range 2 {
		var id int64
		err := m.db.QueryRowContext(ctx, `
			INSERT INTO b2b_partial_checkouts (customer_id, idempotency_key, source_cart, line_ids)
			VALUES ($1, nullif($2, ''), $3, $4)
			ON CONFLICT DO NOTHING
			RETURNING id`, customerID, key, source, idsJSON).Scan(&id)
		if err == nil {
			return &partialCheckout{id: id, source: source, lineIDs: ids}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		stale, err := m.partialWhere(ctx, `source_cart = $1 AND status = 'placing'
			AND updated_at < now() - interval '10 minutes'`, source)
		if err != nil {
			return nil, err
		}
		if stale == nil {
			break
		}
		m.settlePartial(ctx, stale)
	}
	return nil, gocommerce.Conflictf("part of this basket is already being checked out; wait for it to finish")
}

// buildPartial opens the basket a partial checkout places. On the same
// storefront, because that is what every line's price was resolved against;
// claimed for the buyer before a line goes in, so each is added at their
// price; with the same discount code, which the checkout judges under its own
// lock exactly as it would have on the whole basket.
func (m *Module) buildPartial(ctx context.Context, buyerID int64, source *gocommerce.Cart, ids []int64, lines map[int64]gocommerce.CartLine) (string, error) {
	built, err := m.app.Cart().CreateInChannel(ctx, "", source.ChannelCode)
	if err != nil {
		return "", err
	}
	if _, err := m.accounts.ClaimCart(ctx, buyerID, built.Token); err != nil {
		return built.Token, err
	}
	detail, err := m.app.Cart().Get(ctx, source.ID)
	if err != nil {
		return built.Token, err
	}
	if detail.DiscountCode != "" {
		if err := m.app.Cart().SetDiscountCode(ctx, built.Token, detail.DiscountCode); err != nil {
			return built.Token, err
		}
	}
	for _, id := range ids {
		l := lines[id]
		if _, err := m.app.Cart().AddLine(ctx, built.Token, l.VariantID, l.Quantity); err != nil {
			return built.Token, lineConflict(l, err)
		}
	}
	return built.Token, nil
}

// lineConflict gives a line that would not go into the new basket the shape
// a refused checkout gives it, so a storefront handles one answer whichever
// way the basket was checked out.
func lineConflict(l gocommerce.CartLine, err error) error {
	if !errors.Is(err, gocommerce.ErrConflict) {
		return err
	}
	c := gocommerce.LineConflict{VariantID: l.VariantID, SKU: l.SKU, Reason: gocommerce.ReasonInactive}
	if l.Available >= 0 && l.Available < l.Quantity {
		c.Reason, c.Available, c.Requested = gocommerce.ReasonInsufficientStock, l.Available, l.Quantity
	}
	return gocommerce.Conflictf("the cart is no longer valid at these prices").WithDetails([]gocommerce.LineConflict{c})
}

// settlePartial finishes a partial checkout however it went. The chosen lines
// leave the buyer's basket exactly when the basket built from them has become
// an order — placed, or placed and still waiting on a gateway, as a whole
// basket is converted either way. Otherwise the built basket is emptied, so
// the abandoned-cart sweep never writes to the buyer about a basket they
// never saw, and the record goes, so a retry starts again from their basket.
//
// Failures are logged, not returned: the order, or the request, already
// exists, and the caller must hear about that rather than about the tidying.
func (m *Module) settlePartial(ctx context.Context, part *partialCheckout) {
	if part.cart != "" {
		built, err := m.app.Cart().GetByToken(ctx, part.cart)
		if err != nil && !errors.Is(err, gocommerce.ErrNotFound) {
			// Left in flight; the reconciler settles it once the basket reads.
			m.app.Log().Warn("b2b: could not settle a partial checkout", "partial", part.id, "error", err)
			return
		}
		if err == nil && built.Status == gocommerce.CartConverted {
			for _, id := range part.lineIDs {
				if _, err := m.app.Cart().RemoveLine(ctx, part.source, id); err != nil && !errors.Is(err, gocommerce.ErrNotFound) {
					m.app.Log().Warn("b2b: a line checked out on its own is still in the buyer's basket",
						"partial", part.id, "line", id, "error", err)
				}
			}
			if _, err := m.db.ExecContext(ctx, `
				UPDATE b2b_partial_checkouts SET status = 'placed', updated_at = now() WHERE id = $1`, part.id); err != nil {
				m.app.Log().Warn("b2b: could not mark a partial checkout placed", "partial", part.id, "error", err)
			}
			return
		}
		if err == nil {
			for _, l := range built.Lines {
				if _, err := m.app.Cart().RemoveLine(ctx, part.cart, l.ID); err != nil && !errors.Is(err, gocommerce.ErrNotFound) {
					m.app.Log().Warn("b2b: could not empty an unused partial basket", "partial", part.id, "error", err)
				}
			}
		}
	}
	if _, err := m.db.ExecContext(ctx, `DELETE FROM b2b_partial_checkouts WHERE id = $1`, part.id); err != nil {
		m.app.Log().Warn("b2b: could not forget a partial checkout", "partial", part.id, "error", err)
	}
}

func (m *Module) partialWhere(ctx context.Context, where string, args ...any) (*partialCheckout, error) {
	list, err := m.partialsWhere(ctx, where+` LIMIT 1`, args...)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return list[0], nil
}

func (m *Module) partialsWhere(ctx context.Context, where string, args ...any) ([]*partialCheckout, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, source_cart, line_ids, cart_token FROM b2b_partial_checkouts WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*partialCheckout
	for rows.Next() {
		part := &partialCheckout{}
		var ids []byte
		if err := rows.Scan(&part.id, &part.source, &ids, &part.cart); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(ids, &part.lineIDs); err != nil {
			return nil, err
		}
		out = append(out, part)
	}
	return out, rows.Err()
}

// reconcilePartials settles partial checkouts a crash left in flight, and
// forgets finished ones after a week — long past any retry of the request
// that made them.
func (m *Module) reconcilePartials(ctx context.Context) error {
	stale, err := m.partialsWhere(ctx,
		`status = 'placing' AND updated_at < now() - interval '10 minutes'`)
	if err != nil {
		return err
	}
	for _, part := range stale {
		m.settlePartial(ctx, part)
	}
	_, err = m.db.ExecContext(ctx, `
		DELETE FROM b2b_partial_checkouts WHERE status = 'placed' AND updated_at < now() - interval '7 days'`)
	return err
}

// approvalByKey is the request a buyer filed under an Idempotency-Key, if any.
func (m *Module) approvalByKey(ctx context.Context, buyerID int64, key string) (*Approval, error) {
	var id int64
	err := m.db.QueryRowContext(ctx,
		`SELECT id FROM b2b_approvals WHERE requested_by = $1 AND idempotency_key = $2`, buyerID, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return m.Approval(ctx, id)
}

// placeAgreed places an order from a snapshot — an approved basket or an
// accepted quote — at the prices in it, through Orders.Create (D68).
func (m *Module) placeAgreed(ctx context.Context, p *placement, method string, checkout gocommerce.CheckoutInput, lines []ApprovalLine) (*gocommerce.CheckoutResult, error) {
	in := gocommerce.NewOrderInput{
		PaymentMethod: method, Email: checkout.Email, Phone: checkout.Phone, Name: checkout.Name,
		Address: checkout.Address, Metadata: orderMeta(p, p.company.NetDays),
	}
	for _, l := range lines {
		price := l.UnitPrice.AmountMinor
		in.Lines = append(in.Lines, gocommerce.NewOrderLine{
			VariantID: l.VariantID, Quantity: l.Quantity, UnitPriceMinor: &price,
		})
	}
	result, err := m.app.Order().Create(withPlacement(ctx, p), in)
	if err != nil {
		return nil, err
	}
	if err := m.recordOrder(ctx, p, result.Order, method, p.company.NetDays); err != nil {
		m.app.Log().Error("b2b: could not record a company order; the reconciler will", "order", result.Order.ID, "error", err)
	}
	return result, nil
}

// recordOrder files an order in the company's ledger. due_at is set for an
// order on account only: anything else was paid for at checkout.
func (m *Module) recordOrder(ctx context.Context, p *placement, o *gocommerce.Order, method string, netDays int) error {
	var due *time.Time
	if method == CodeOnAccount {
		d := o.CreatedAt.AddDate(0, 0, netDays)
		if o.CreatedAt.IsZero() {
			d = time.Now().AddDate(0, 0, netDays)
		}
		due = &d
	}
	var customer *int64
	if p.buyer != nil {
		customer = &p.buyer.ID
	}
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO b2b_orders (order_id, order_number, company_id, customer_id, po_number,
		                        on_account, due_at, approval_id, quote_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, nullif($8, 0), nullif($9, 0))
		ON CONFLICT (order_id) DO NOTHING`,
		o.ID, o.Number, p.company.ID, customer, p.po, method == CodeOnAccount, due,
		p.approvalID, p.quoteID)
	return err
}

// reconcileOrders files any order whose metadata names a company but whose
// ledger row never got written — the crash between the checkout committing
// and recordOrder running.
func (m *Module) reconcileOrders(ctx context.Context) error {
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO b2b_orders (order_id, order_number, company_id, customer_id, po_number,
		                        on_account, due_at, approval_id, quote_id, created_at)
		SELECT o.id, o.number, (o.metadata -> 'b2b' ->> 'company_id')::bigint,
		       (o.metadata -> 'b2b' ->> 'placed_by')::bigint,
		       coalesce(o.metadata -> 'b2b' ->> 'po_number', ''),
		       o.payment_provider = $1,
		       CASE WHEN o.payment_provider = $1
		            THEN o.created_at + make_interval(days => coalesce((o.metadata -> 'b2b' ->> 'net_days')::int, 30))
		       END,
		       (o.metadata -> 'b2b' ->> 'approval_id')::bigint,
		       (o.metadata -> 'b2b' ->> 'quote_id')::bigint,
		       o.created_at
		FROM orders o
		JOIN b2b_companies c ON c.id = (o.metadata -> 'b2b' ->> 'company_id')::bigint
		WHERE o.metadata ? 'b2b'
		  AND NOT EXISTS (SELECT 1 FROM b2b_orders bo WHERE bo.order_id = o.id)
		ON CONFLICT (order_id) DO NOTHING`, CodeOnAccount)
	return err
}

// CompanyOrderQuery narrows a company's ledger.
type CompanyOrderQuery struct {
	// CompanyID is one company's; zero is every company's.
	CompanyID int64
	// CustomerID narrows to one buyer's orders; zero is every buyer's.
	CustomerID int64
	// OnAccountOnly keeps the orders placed on account, and OverdueOnly those
	// of them past their due date and still unpaid.
	OnAccountOnly bool
	OverdueOnly   bool
	// Search matches part of the order number or the PO number, in any case —
	// and of the email it was placed with, except on a demo store, where that
	// is masked and a contains-match would read it back a character at a time.
	Search        string
	Demo          bool
	Limit, Offset int
}

const companyOrderColumns = `bo.order_id, bo.order_number, o.status, o.payment_status, o.total_minor,
	o.currency, bo.po_number, o.email, bo.on_account, bo.due_at, bo.company_id, c.name,
	bo.approval_id, bo.quote_id, o.created_at`

const companyOrderFrom = ` FROM b2b_orders bo JOIN orders o ON o.id = bo.order_id
	JOIN b2b_companies c ON c.id = bo.company_id`

// scanCompanyOrder reads one ledger row. Overdue is worked out here, against
// the clock, rather than stored: an order becomes overdue at midnight with
// nothing having happened to it.
func scanCompanyOrder(row rowScanner, now time.Time) (*CompanyOrder, error) {
	co := &CompanyOrder{}
	var due sql.NullTime
	var approval, quote sql.NullInt64
	if err := row.Scan(&co.OrderID, &co.Number, &co.Status, &co.PaymentStatus,
		&co.Total.AmountMinor, &co.Total.Currency, &co.PONumber, &co.PlacedBy,
		&co.OnAccount, &due, &co.CompanyID, &co.CompanyName, &approval, &quote, &co.CreatedAt); err != nil {
		return nil, err
	}
	if due.Valid {
		d := due.Time
		co.DueAt = &d
		co.Overdue = co.OnAccount && d.Before(now) &&
			(co.PaymentStatus == gocommerce.PaymentPending || co.PaymentStatus == gocommerce.PaymentFailed) &&
			co.Status != gocommerce.OrderCancelled
	}
	if approval.Valid {
		co.ApprovalID = &approval.Int64
	}
	if quote.Valid {
		co.QuoteID = &quote.Int64
	}
	return co, nil
}

// CompanyOrder is one order as a company's ledger reads it. An order that was
// not placed for a company is a 404: it has no ledger row to read.
func (m *Module) CompanyOrder(ctx context.Context, orderID int64) (*CompanyOrder, error) {
	co, err := scanCompanyOrder(m.db.QueryRowContext(ctx,
		`SELECT `+companyOrderColumns+companyOrderFrom+` WHERE bo.order_id = $1`, orderID), time.Now())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("order %d was not placed for a company", orderID)
	}
	return co, err
}

// CompanyOrders reads the ledger, newest first.
func (m *Module) CompanyOrders(ctx context.Context, q CompanyOrderQuery) ([]*CompanyOrder, int, error) {
	where := []string{"true"}
	args := []any{}
	if q.CompanyID > 0 {
		args = append(args, q.CompanyID)
		where = append(where, "bo.company_id = $"+strconv.Itoa(len(args)))
	}
	if q.CustomerID > 0 {
		args = append(args, q.CustomerID)
		where = append(where, "bo.customer_id = $"+strconv.Itoa(len(args)))
	}
	if q.OnAccountOnly {
		where = append(where, "bo.on_account")
	}
	if q.OverdueOnly {
		where = append(where, "bo.on_account AND bo.due_at < now() AND o.payment_status IN ('pending', 'failed') AND o.status <> 'cancelled'")
	}
	if s := strings.ToLower(strings.TrimSpace(q.Search)); s != "" {
		args = append(args, "%"+s+"%")
		n := "$" + strconv.Itoa(len(args))
		match := "lower(bo.order_number) LIKE " + n + " OR lower(bo.po_number) LIKE " + n
		if !q.Demo {
			match += " OR lower(o.email) LIKE " + n
		}
		where = append(where, "("+match+")")
	}
	from := companyOrderFrom + ` WHERE ` + strings.Join(where, " AND ")
	var total int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*)`+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, q.Limit, q.Offset)
	rows, err := m.db.QueryContext(ctx, `SELECT `+companyOrderColumns+from+`
		ORDER BY bo.order_id DESC
		LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*CompanyOrder{}
	now := time.Now()
	for rows.Next() {
		co, err := scanCompanyOrder(rows, now)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, co)
	}
	return out, total, rows.Err()
}

// memberAndCompany resolves a signed-in account to its membership and its
// company, re-filing the account's address in the company's group first if
// it has changed since it joined.
func (m *Module) memberAndCompany(ctx context.Context, acct *identity.Customer) (*Member, *Company, error) {
	mem, err := m.MemberOf(ctx, acct.ID)
	if err != nil {
		return nil, nil, gocommerce.Forbiddenf("this account is not a buyer for any company")
	}
	if err := m.syncMember(ctx, mem, acct.Email, acct.EmailVerified); err != nil {
		return nil, nil, err
	}
	company, err := m.Company(ctx, mem.CompanyID)
	if err != nil {
		return nil, nil, err
	}
	return mem, company, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// checkoutSnapshot is the part of a checkout an approval keeps, to place the
// order from later.
type checkoutSnapshot struct {
	Email   string             `json:"email"`
	Name    string             `json:"name"`
	Phone   string             `json:"phone"`
	Address gocommerce.Address `json:"address"`
}

func snapshotOf(in gocommerce.CheckoutInput) []byte {
	b, _ := json.Marshal(checkoutSnapshot{Email: in.Email, Name: in.Name, Phone: in.Phone, Address: in.Address})
	return b
}
