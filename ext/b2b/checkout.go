package b2b

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
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
	var limit, threshold sql.NullInt64
	var requirePO bool
	if err := a.Tx.QueryRowContext(ctx, `
		SELECT status, credit_limit_minor, approval_threshold_minor, require_po
		FROM b2b_companies WHERE id = $1`, p.company.ID).
		Scan(&status, &limit, &threshold, &requirePO); err != nil {
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
// result.
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
	// Price the basket as this buyer: their company's group prices, through
	// the address identity has confirmed (D66).
	if _, err := m.accounts.ClaimCart(ctx, buyer.ID, req.CartID); err != nil {
		return nil, nil, err
	}

	p := &placement{company: company, buyer: buyer, member: mem, po: strings.TrimSpace(req.PONumber)}
	in := gocommerce.CheckoutInput{
		CartID: req.CartID, Email: buyer.Email,
		Name: firstNonEmpty(req.Name, buyer.Name), Phone: firstNonEmpty(req.Phone, buyer.Phone),
		Address: req.Address, ShippingRateID: req.ShippingRateID,
		PaymentData: req.PaymentData, ReturnURL: req.ReturnURL,
		Metadata: orderMeta(p, company.NetDays),
	}
	result, err := m.app.Order().Checkout(withPlacement(ctx, p), method, in, idemKey)
	if err != nil {
		var apiErr *gocommerce.APIError
		if errors.As(err, &apiErr) && apiErr.Code == codeApprovalRequired && p.pending != nil {
			approval, aerr := m.requestApproval(ctx, p, "cart", 0, method, in, idemKey)
			return nil, approval, aerr
		}
		return nil, nil, err
	}
	if err := m.recordOrder(ctx, p, result.Order, method, company.NetDays); err != nil {
		m.app.Log().Error("b2b: could not record a company order; the reconciler will", "order", result.Order.ID, "error", err)
	}
	return result, nil, nil
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

// CompanyOrders reads a company's ledger, newest first. customerID narrows it
// to one buyer's orders; zero is every buyer's. onAccountOnly keeps the orders
// placed on account, and overdueOnly those of them past their due date and
// still unpaid.
func (m *Module) CompanyOrders(ctx context.Context, companyID, customerID int64, onAccountOnly, overdueOnly bool, limit, offset int) ([]*CompanyOrder, int, error) {
	where := []string{"true"}
	args := []any{}
	if companyID > 0 {
		args = append(args, companyID)
		where = append(where, "bo.company_id = $"+strconv.Itoa(len(args)))
	}
	if customerID > 0 {
		args = append(args, customerID)
		where = append(where, "bo.customer_id = $"+strconv.Itoa(len(args)))
	}
	if onAccountOnly {
		where = append(where, "bo.on_account")
	}
	if overdueOnly {
		where = append(where, "bo.on_account AND bo.due_at < now() AND o.payment_status IN ('pending', 'failed') AND o.status <> 'cancelled'")
	}
	clause := strings.Join(where, " AND ")
	from := ` FROM b2b_orders bo JOIN orders o ON o.id = bo.order_id JOIN b2b_companies c ON c.id = bo.company_id WHERE ` + clause
	var total int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*)`+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := m.db.QueryContext(ctx, `
		SELECT bo.order_id, bo.order_number, o.status, o.payment_status, o.total_minor, o.currency,
		       bo.po_number, o.email, bo.on_account, bo.due_at, bo.company_id, c.name, o.created_at`+from+`
		ORDER BY bo.order_id DESC
		LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*CompanyOrder{}
	now := time.Now()
	for rows.Next() {
		co := &CompanyOrder{}
		var due sql.NullTime
		if err := rows.Scan(&co.OrderID, &co.Number, &co.Status, &co.PaymentStatus,
			&co.Total.AmountMinor, &co.Total.Currency, &co.PONumber, &co.PlacedBy,
			&co.OnAccount, &due, &co.CompanyID, &co.CompanyName, &co.CreatedAt); err != nil {
			return nil, 0, err
		}
		if due.Valid {
			d := due.Time
			co.DueAt = &d
			co.Overdue = co.OnAccount && d.Before(now) &&
				(co.PaymentStatus == gocommerce.PaymentPending || co.PaymentStatus == gocommerce.PaymentFailed) &&
				co.Status != gocommerce.OrderCancelled
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
