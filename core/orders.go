package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Order statuses. Cancellation is a controlled transition from pending or
// confirmed, not a generic update.
const (
	OrderPending   = "pending"
	OrderConfirmed = "confirmed"
	// OrderPartial means some of it has gone out and some has not. `shipped`
	// still means all of it, which is what every client, filter and report
	// written before parcels existed already assumes. It is derived from the
	// fulfillment lines by settleOrderShipping and never assigned by a caller,
	// so the status and the parcels cannot disagree.
	OrderPartial   = "partial"
	OrderShipped   = "shipped"
	OrderDelivered = "delivered"
	OrderCancelled = "cancelled"
)

// Payment statuses.
const (
	PaymentPending  = "pending"
	PaymentPaid     = "paid"
	PaymentFailed   = "failed"
	PaymentRefunded = "refunded"
)

// Refund statuses. `pending` exists because the provider call cannot happen
// inside a transaction (AGENTS rule 5): the row is committed before the gateway
// is asked, and while it sits there it reserves that amount against a second
// refund spending the same money. Only `succeeded` is money that moved.
const (
	RefundPending   = "pending"
	RefundSucceeded = "succeeded"
	RefundFailed    = "failed"
)

// Order is an immutable record of a sale in progress. Its lines and its
// customer details are snapshots: a historical order must stay readable and
// legally meaningful after the catalog moves on.
type Order struct {
	ID               int64  `json:"id"`
	Number           string `json:"number"`
	Status           string `json:"status"`
	PaymentStatus    string `json:"payment_status"`
	PaymentProvider  string `json:"payment_provider"`
	PaymentReference string `json:"payment_reference,omitempty"`
	Currency         string `json:"currency"`

	Subtotal Money `json:"subtotal"`
	Shipping Money `json:"shipping"`
	// ShippingMethod is the name of the rate the shopper chose, snapshotted
	// ChannelCode is the storefront that sold it, empty on a store with no
	// channels and on every order placed before they existed.
	ChannelCode string `json:"channel,omitempty"`
	// for the reason every snapshot here exists: the rate can be renamed,
	// repriced or deleted, and the order still has to say what was agreed.
	// Empty means no rate was involved — a store still on the flat number.
	ShippingMethod string `json:"shipping_method,omitempty"`
	Discount       Money  `json:"discount"`
	// Tax is what was charged across every line. When TaxInclusive it is part
	// of Subtotal rather than added to it, which is what that flag is for.
	Tax          Money `json:"tax"`
	TaxInclusive bool  `json:"tax_inclusive"`
	Total        Money `json:"total"`
	// Refunded is what has gone back to the customer, running total. It stays
	// under Total for a partial refund and reaches it for a full one, at which
	// point PaymentStatus becomes refunded — so `paid` no longer implies the
	// store still holds all of it, and this is the field that says how much.
	Refunded Money `json:"refunded"`

	Email   string  `json:"email"`
	Phone   string  `json:"phone,omitempty"`
	Name    string  `json:"name,omitempty"`
	Address Address `json:"address"`

	Language string `json:"language"`
	// Discounts is what this order was given, by value. It is a snapshot: the
	// rule that produced it may be edited or deleted, and this stays true.
	Discounts    []AppliedDiscount `json:"discounts,omitempty"`
	Lines        []OrderLine       `json:"line_items"`
	Fulfillments []Fulfillment     `json:"fulfillments"`
	// Refunds is the ledger behind Refunded, oldest first. A guest's copy holds
	// only the refunds that actually moved money — see Redact.
	Refunds []OrderRefund `json:"refunds"`
	// Returns is what came back off this order. It changes nothing about what
	// the order was — the sale still happened and the parcel still went out —
	// which is why there is no returned status and no flag on the order itself.
	Returns  []OrderReturn `json:"returns"`
	Metadata Metadata      `json:"metadata"`

	// AccessToken is how a guest reads their own order back. It is returned
	// once, at checkout, and never included in an admin listing.
	AccessToken string `json:"access_token,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// OrderLine is a frozen snapshot of what was bought.
type OrderLine struct {
	ID        int64  `json:"id"`
	ProductID *int64 `json:"product_id,omitempty"`
	VariantID *int64 `json:"variant_id,omitempty"`
	SKU       string `json:"sku"`
	Title     string `json:"title"`
	// ImageURL is what the product looks like *now*, filled in on the way out
	// and never stored. The rest of this row is a snapshot taken at checkout
	// and must not move; a picture is not part of what was agreed, it is how
	// somebody recognises the thing in the box. Empty when the product has been
	// deleted or never had a picture.
	ImageURL     string `json:"image_url,omitempty"`
	VariantLabel string `json:"variant_label,omitempty"`
	// Tax is what this line was charged, snapshotted like its price. An invoice
	// has to print the same figures next year as it does today, and a rate that
	// has since changed must not be able to rewrite them.
	Tax      LineTax `json:"tax"`
	Quantity int     `json:"quantity"`
	// ShippedQuantity is how many of this line have actually gone out, summed
	// on the way out from the shipments in the same response and never stored:
	// two parcels carrying the same line have to add up, and a column would be
	// a second place for that sum to be wrong.
	//
	// Filled only by loadChildren. A transition reading its lines through
	// loadOrderLinesTx sees zero here, which is why the shipping path asks
	// shippedByLine under the row lock instead.
	ShippedQuantity int `json:"shipped_quantity"`
	// ReturnedQuantity is how many of this line have come back and not been
	// withdrawn, summed on the way out from the returns in the same response
	// and never stored — ShippedQuantity's reason, for the same sum. Filled
	// only by loadChildren; a transition asks the returns table under the row
	// lock instead.
	ReturnedQuantity int   `json:"returned_quantity,omitempty"`
	UnitPrice        Money `json:"unit_price"`
	Total            Money `json:"total"`
	// LocationID is the place these units were taken from. Recorded at
	// checkout so that cancelling puts them back where they were, and nil for
	// a line placed before M17 or one whose location has since been closed —
	// in which case the movement falls back to the default.
	LocationID *int64   `json:"location_id,omitempty"`
	Metadata   Metadata `json:"metadata"`
}

// Fulfillment is a shipment against an order.
type Fulfillment struct {
	ID       int64  `json:"id"`
	Provider string `json:"provider"`
	Tracking string `json:"tracking,omitempty"`
	// Carrier is who is actually carrying the parcel, which is a different
	// fact from Provider: a store that packs its own boxes books every
	// shipment as "manual" and hands them to whichever courier serves the
	// pincode. Empty when nobody has said.
	Carrier string `json:"carrier,omitempty"`
	// CarrierName and TrackingURL are derived from Carrier and Tracking on the
	// way out, never stored. A stored URL is a URL that goes stale the day the
	// carrier changes its site.
	CarrierName string    `json:"carrier_name,omitempty"`
	TrackingURL string    `json:"tracking_url,omitempty"`
	LabelURL    string    `json:"label_url,omitempty"`
	Status      string    `json:"status"`
	Metadata    Metadata  `json:"metadata"`
	CreatedAt   time.Time `json:"created_at"`
	// Lines is what went in this parcel. Empty on a shipment recorded before
	// M22 that the backfill could not credit.
	Lines []FulfillmentLine `json:"lines"`
}

// FulfillmentLine is how much of one order line went in one parcel.
//
// The sku, title and label are joined in on the way out for the same reason
// CarrierName and ImageURL are: the row stores an id and a count, and an
// operator on the phone needs to know which parcel holds the mug.
type FulfillmentLine struct {
	OrderLineID  int64  `json:"order_line_id"`
	SKU          string `json:"sku"`
	Title        string `json:"title"`
	VariantLabel string `json:"variant_label,omitempty"`
	Quantity     int    `json:"quantity"`
}

// decorate fills in the carrier's name and the link to follow the parcel.
func (f *Fulfillment) decorate() {
	if f.Carrier == "" {
		return
	}
	if c, ok := CarrierByCode(f.Carrier, f.Tracking); ok {
		f.CarrierName, f.TrackingURL = c.Name, c.TrackURL
	}
}

// OrderRefund is one movement of money back to the customer.
//
// Amount is a Money rather than a bare amount_minor because a refund is read in
// a list where the order's currency is not to hand; it is filled from the
// owning order on the way out and never stored (D14).
type OrderRefund struct {
	ID     int64  `json:"id"`
	Amount Money  `json:"amount"`
	Reason string `json:"reason,omitempty"`
	Status string `json:"status"`
	// Provider is the method the money went out through, snapshotted at the
	// time: an order's payment_provider can still be corrected while nothing
	// has been refunded, and this row must keep saying which gateway saw it.
	Provider string `json:"provider"`
	// ProviderReference is the gateway's own id for the refund — what somebody
	// reconciles against a bank statement. Empty when the provider does not
	// implement [ReferencedRefunder].
	ProviderReference string `json:"provider_reference,omitempty"`
	// Error is what the provider said when it refused. Kept because an operator
	// looking at a refund that did not happen needs to know it was tried.
	Error string `json:"error,omitempty"`
	// By is the operator's email as it was then, empty when a script holding the
	// static admin token did it — a credential is not a person.
	By        string    `json:"by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// OrderNoteKey is the one metadata key core reserves on an order: the
// operator's note to the shop. It is refused on the way in at checkout and
// removed by [Order.Redact] on the way out, so it is the shop's and not the
// shopper's. A module attaching its own data uses its own key.
const OrderNoteKey = "notes"

// Redact drops what belongs to the operator, leaving a copy safe to hand the
// person who placed the order.
//
// It is exported and it is one method on purpose: [Orders.GetForGuest] is not
// the only customer-facing read — the identity module serves a signed-in
// shopper their own order history straight off [Orders.Get] — so a redaction
// living inside the guest path would be a rule only one of the two obeyed.
// Anything later added to an order that an operator writes for themselves
// belongs in here, rather than in a second stripping beside it.
//
// Today that is the operator's note, the access token, the returns and the
// refunds. How much came back and the gateway's reference are the customer's
// business — they are what reconciles their own statement. Who inside the store
// authorised it, the note they typed to justify it, and an attempt that failed
// are not, and AGENTS rule 8 keeps operator identities out of the commerce path.
//
// Deliberately not called on the checkout response, which is the one reply that
// must carry the access token: it is returned once, to the shopper who just
// placed the order, and clearing it there would destroy their only handle on it
// (D22).
func (o *Order) Redact() {
	// The note is the shop writing to itself about this order — "customer
	// sounded unhappy", "do not ship until the transfer clears". It is at a
	// reserved key rather than under the whole object, because metadata is
	// documented as where a module attaches its own data and a storefront may
	// be reading its own keys off the guest response.
	delete(o.Metadata, OrderNoteKey)

	// For callers that populated it themselves, the way checkout does.
	// orderColumns does not select access_token, so an order from Get or
	// GetByNumber already has none — which is not something a future caller
	// should have to know before handing one to a shopper.
	o.AccessToken = ""

	// A return is the store's record of goods it took back and what it judged
	// them to be worth: which shelf they went on, and whether it decided a line
	// was unsellable. The shopper's view of their order is what they bought,
	// and they are told a return was received by the order.returned
	// notification, which is the channel for it.
	o.Returns = []OrderReturn{}

	kept := make([]OrderRefund, 0, len(o.Refunds))
	for _, r := range o.Refunds {
		if r.Status != RefundSucceeded {
			continue
		}
		r.Reason, r.By, r.Error = "", "", ""
		kept = append(kept, r)
	}
	o.Refunds = kept
}

// stockCommitted reports whether the order's inventory has left the shelf, as
// opposed to merely being reserved. It is derived from status rather than
// stored, so the two can never disagree.
func stockCommitted(status string) bool {
	switch status {
	// A partly shipped order reached that state from confirmed, so its units
	// are off the shelf — every one of them, including the ones still in the
	// stockroom waiting for the second parcel.
	case OrderConfirmed, OrderPartial, OrderShipped, OrderDelivered:
		return true
	}
	return false
}

// OrderQuery filters an order listing.
type OrderQuery struct {
	Status        string
	PaymentStatus string
	Email         string
	// Search is the operator's search box — an order number by prefix, or an
	// email or a name containing it. Email stays exact beside it, because the
	// Customers screen links here with a whole address and means that person.
	//
	// List and Transfer.ExportOrders both honour it. The MCP list_orders tool
	// and the export HTTP handler do not yet: both build this struct by named
	// fields, so they compile and quietly ignore it.
	Search string
	// Sort is an operator-chosen ordering; zero keeps the listing's own,
	// newest first.
	Sort          Sort
	From, To      *time.Time
	Limit, Offset int
}

// Orders owns the order state machine. Every transition lives here — no
// integration gets to invent its own version of confirming or cancelling.
type Orders struct {
	app *App
}

// Order returns the order service.
func (a *App) Order() *Orders { return a.orders }

// ------------------------------------------------------------------ reading

const orderColumns = `o.id, o.number, o.status, o.payment_status, o.payment_provider,
	coalesce(o.payment_reference, ''), o.currency, o.subtotal_minor, o.shipping_minor,
	o.discount_minor, o.tax_minor, o.tax_inclusive, o.total_minor, o.refunded_minor,
	o.email, coalesce(o.phone, ''), coalesce(o.name, ''),
	o.address, o.lang, o.metadata, o.shipping_method,
	coalesce((SELECT ch.code FROM channels ch WHERE ch.id = o.channel_id), ''),
	o.created_at, o.updated_at`

func (s *Orders) scanOrder(row interface{ Scan(...any) error }) (*Order, error) {
	o := &Order{}
	var addr, meta []byte
	var subtotal, shipping, discount, tax, total, refunded int64
	if err := row.Scan(&o.ID, &o.Number, &o.Status, &o.PaymentStatus, &o.PaymentProvider,
		&o.PaymentReference, &o.Currency, &subtotal, &shipping, &discount,
		&tax, &o.TaxInclusive, &total, &refunded,
		&o.Email, &o.Phone, &o.Name, &addr, &o.Language, &meta, &o.ShippingMethod,
		&o.ChannelCode, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return nil, err
	}
	o.Subtotal = money(subtotal, o.Currency)
	o.Shipping = money(shipping, o.Currency)
	o.Discount = money(discount, o.Currency)
	o.Tax = money(tax, o.Currency)
	o.Total = money(total, o.Currency)
	o.Refunded = money(refunded, o.Currency)
	if len(addr) > 0 {
		if err := json.Unmarshal(addr, &o.Address); err != nil {
			return nil, err
		}
	}
	if err := scanMetadata(meta, &o.Metadata); err != nil {
		return nil, err
	}
	o.Lines = []OrderLine{}
	o.Fulfillments = []Fulfillment{}
	o.Refunds = []OrderRefund{}
	o.Returns = []OrderReturn{}
	return o, nil
}

// Get loads an order by id.
func (s *Orders) Get(ctx context.Context, id int64) (*Order, error) {
	return s.getWhere(ctx, "o.id = $1", id)
}

// GetByNumber loads an order by its human number.
func (s *Orders) GetByNumber(ctx context.Context, number string) (*Order, error) {
	return s.getWhere(ctx, "o.number = $1", number)
}

// GetForGuest loads an order for a shopper holding its access token. The
// token is compared in constant time and a mismatch is reported as not-found,
// so the endpoint cannot be used to discover which order numbers exist.
func (s *Orders) GetForGuest(ctx context.Context, number, accessToken string) (*Order, error) {
	var stored string
	err := s.app.db.QueryRowContext(ctx,
		`SELECT access_token FROM orders WHERE number = $1`, number).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("order not found")
	}
	if err != nil {
		return nil, err
	}
	if !constantTimeEqual(stored, accessToken) {
		return nil, NotFoundf("order not found")
	}
	o, err := s.GetByNumber(ctx, number)
	if err != nil {
		return nil, err
	}
	o.Redact()
	return o, nil
}

// OrderAccessToken is one order's guest credential, handed back to an operator
// who has to give a customer their link again.
type OrderAccessToken struct {
	Number      string `json:"number"`
	AccessToken string `json:"access_token"`
}

// RevealAccessToken reads an existing order's access token back out, and
// records that it was read.
//
// The gap this closes: the token is returned exactly once, in the checkout
// reply, and orderColumns has never selected it since — so a shopper who lost
// their "view your order" mail could not be helped by anybody. It is their only
// credential (D22: there is no account to sign into), which is precisely why
// there was no way to get it and precisely why there has to be one.
//
// Reveal rather than re-issue, deliberately. Rotating the token would answer
// the same question — the customer gets a working link — while silently
// breaking the link in the confirmation mail they already have, in the mail
// their storefront sent, and in any bookmark: an operator helping with a lost
// link would be destroying the copy that was merely mislaid. Reveal costs
// nothing that rotate does not also cost, because the operator is going to read
// the token out either way.
//
// What makes that safe is the row this writes. It is a disclosure of a bearer
// credential, so it needs a right (orders.write, in commerce_http.go — reading
// an order is the default for every role, handing out the key to it is not) and
// it needs a record. The record is written in the same transaction as the read,
// which is what stops the two coming apart: there is no path here that returns
// a token without leaving the row behind, because a failed audit rolls the
// whole thing back.
//
// The token itself is NOT in the record. An append-only table that nothing can
// delete from is the last place a live credential should be copied into, and
// the question an auditor brings — who asked for this, and when — is answered
// without it.
func (s *Orders) RevealAccessToken(ctx context.Context, id int64) (*OrderAccessToken, error) {
	out := &OrderAccessToken{}
	err := InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx,
			`SELECT number, access_token FROM orders WHERE id = $1`, id).
			Scan(&out.Number, &out.AccessToken)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("order not found")
		}
		if err != nil {
			return err
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditOrderTokenReveal, Entity: AuditEntityOrder,
			ID: id, Label: out.Number,
			Summary: fmt.Sprintf("Read back the access token for order %s", out.Number),
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Orders) getWhere(ctx context.Context, where string, arg any) (*Order, error) {
	o, err := s.scanOrder(s.app.db.QueryRowContext(ctx,
		`SELECT `+orderColumns+` FROM orders o WHERE `+where, arg))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, NotFoundf("order not found")
		}
		return nil, err
	}
	if err := s.loadChildren(ctx, []*Order{o}); err != nil {
		return nil, err
	}
	return o, nil
}

// List returns a page of orders and the total matching count.
// orderSorts is the order listing's allow-list.
var orderSorts = SortSpec{
	Tiebreak: "o.id",
	Columns: map[string]sortField{
		// The number is the id under a prefix and %06d, so ordering by the id IS
		// ordering by the number — and stays true past six digits, where a text
		// sort puts P1000000 before P999999.
		"number":         {"o.id ASC", "o.id DESC"},
		"status":         {"o.status ASC", "o.status DESC"},
		"payment_status": {"o.payment_status ASC", "o.payment_status DESC"},
		"email":          {"lower(o.email) ASC", "lower(o.email) DESC"},
		// orders.name is nullable AND can be the empty string. An empty string
		// is not a value, and sorting it as one puts a block of blank rows at
		// the top of the ascending listing, which reads as the sort failing.
		"name": {"lower(nullif(o.name, '')) ASC NULLS LAST", "lower(nullif(o.name, '')) DESC NULLS LAST"},
		// The JSON field is `total`; the column it means is total_minor. The key
		// follows the payload rather than the schema.
		"total":      {"o.total_minor ASC", "o.total_minor DESC"},
		"created_at": {"o.created_at ASC", "o.created_at DESC"},
		"id":         {"o.id ASC", "o.id DESC"},
	},
}

// orderSearchClause turns the operator's search box into one predicate over the
// three columns somebody actually searches an order by, appending its two
// arguments to the caller's list. An empty needle is no filter at all.
//
// One function rather than the same six lines in two places: List and
// ExportOrders are the two consumers that build a WHERE from an OrderQuery, and
// an export taken from a filtered screen has to be the rows on that screen. Two
// copies of this would drift the first time either was widened. Both statements
// alias orders as `o`, which is what lets the predicate transfer verbatim.
//
// The number is anchored and the rest contained because that is how each is
// read: an order number comes off a receipt from the left, while a name or an
// address is remembered from the middle. LIKE metacharacters are not escaped,
// following every other search in the engine — which is precisely why ?email=
// stays exact equality, since `_` is a wildcard and is common in real addresses.
func orderSearchClause(search string, args *[]any) string {
	needle := strings.ToLower(strings.TrimSpace(search))
	if needle == "" {
		return ""
	}
	*args = append(*args, needle+"%", "%"+needle+"%")
	prefix, contains := len(*args)-1, len(*args)
	return fmt.Sprintf("(lower(o.number) LIKE $%d OR lower(o.email) LIKE $%d"+
		" OR lower(coalesce(o.name, '')) LIKE $%d)", prefix, contains, contains)
}

func (s *Orders) List(ctx context.Context, q OrderQuery) ([]*Order, int, error) {
	where, args := []string{"1 = 1"}, []any{}
	add := func(expr string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(expr, len(args)))
	}
	if q.Status != "" {
		add("o.status = $%d", q.Status)
	}
	if q.PaymentStatus != "" {
		add("o.payment_status = $%d", q.PaymentStatus)
	}
	if q.Email != "" {
		add("lower(o.email) = $%d", strings.ToLower(q.Email))
	}
	if clause := orderSearchClause(q.Search, &args); clause != "" {
		where = append(where, clause)
	}
	if q.From != nil {
		add("o.created_at >= $%d", *q.From)
	}
	if q.To != nil {
		add("o.created_at < $%d", *q.To)
	}
	clause := strings.Join(where, " AND ")

	order, err := orderSorts.Clause(q.Sort, "o.id DESC")
	if err != nil {
		return nil, 0, err
	}

	var total int
	if err := s.app.db.QueryRowContext(ctx,
		`SELECT count(*) FROM orders o WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	args = append(args, limit, q.Offset)
	// The built clause is an argument and never the format string: it is SQL we
	// wrote, and the only %s here is the one that says so.
	rows, err := s.app.db.QueryContext(ctx,
		`SELECT `+orderColumns+` FROM orders o WHERE `+clause+
			fmt.Sprintf(" ORDER BY %s LIMIT $%d OFFSET $%d", order, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var orders []*Order
	for rows.Next() {
		o, err := s.scanOrder(rows)
		if err != nil {
			return nil, 0, err
		}
		orders = append(orders, o)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := s.loadChildren(ctx, orders); err != nil {
		return nil, 0, err
	}
	return orders, total, nil
}

func (s *Orders) loadChildren(ctx context.Context, orders []*Order) error {
	if len(orders) == 0 {
		return nil
	}
	byID := make(map[int64]*Order, len(orders))
	ids := make([]int64, 0, len(orders))
	for _, o := range orders {
		byID[o.ID] = o
		ids = append(ids, o.ID)
	}

	lineRows, err := s.app.db.QueryContext(ctx, `
		SELECT id, order_id, product_id, variant_id, sku, title, variant_label,
		       quantity, unit_price_minor, total_minor, tax_minor, tax_rate_bp, tax_name,
		       location_id, metadata
		FROM order_lines WHERE order_id = ANY($1::bigint[]) ORDER BY id`, int64Array(ids))
	if err != nil {
		return err
	}
	defer lineRows.Close()
	for lineRows.Next() {
		var l OrderLine
		var orderID int64
		var productID, variantID, locationID sql.NullInt64
		var meta []byte
		if err := lineRows.Scan(&l.ID, &orderID, &productID, &variantID, &l.SKU, &l.Title,
			&l.VariantLabel, &l.Quantity, &l.UnitPrice.AmountMinor, &l.Total.AmountMinor,
			&l.Tax.AmountMinor, &l.Tax.RateBP, &l.Tax.Name, &locationID, &meta); err != nil {
			return err
		}
		if locationID.Valid {
			l.LocationID = &locationID.Int64
		}
		if productID.Valid {
			l.ProductID = &productID.Int64
		}
		if variantID.Valid {
			l.VariantID = &variantID.Int64
		}
		if err := scanMetadata(meta, &l.Metadata); err != nil {
			return err
		}
		if o := byID[orderID]; o != nil {
			l.UnitPrice.Currency = o.Currency
			l.Total.Currency = o.Currency
			o.Lines = append(o.Lines, l)
		}
	}
	if err := lineRows.Err(); err != nil {
		return err
	}

	if err := s.loadLineImages(ctx, orders); err != nil {
		return err
	}
	if err := s.loadOrderDiscounts(ctx, byID, ids); err != nil {
		return err
	}
	if err := s.loadOrderRefunds(ctx, byID, ids); err != nil {
		return err
	}
	if err := s.loadOrderReturns(ctx, byID, ids); err != nil {
		return err
	}

	fRows, err := s.app.db.QueryContext(ctx, `
		SELECT id, order_id, provider, tracking, carrier, label_url, status, metadata, created_at
		FROM fulfillments WHERE order_id = ANY($1::bigint[]) ORDER BY id`, int64Array(ids))
	if err != nil {
		return err
	}
	defer fRows.Close()
	// Where each parcel landed, so its contents can be attached by one more
	// query for the whole page rather than one per parcel.
	fOwner, fAt := map[int64]*Order{}, map[int64]int{}
	var fIDs []int64
	for fRows.Next() {
		var f Fulfillment
		var orderID int64
		var meta []byte
		if err := fRows.Scan(&f.ID, &orderID, &f.Provider, &f.Tracking, &f.Carrier,
			&f.LabelURL, &f.Status, &meta, &f.CreatedAt); err != nil {
			return err
		}
		if err := scanMetadata(meta, &f.Metadata); err != nil {
			return err
		}
		f.Lines = []FulfillmentLine{}
		f.decorate()
		if o := byID[orderID]; o != nil {
			fOwner[f.ID], fAt[f.ID] = o, len(o.Fulfillments)
			fIDs = append(fIDs, f.ID)
			o.Fulfillments = append(o.Fulfillments, f)
		}
	}
	if err := fRows.Err(); err != nil {
		return err
	}
	return s.loadFulfillmentLines(ctx, orders, fOwner, fAt, fIDs)
}

// loadFulfillmentLines fills in what each parcel held, and sums it back onto
// the order lines as ShippedQuantity.
//
// One query for a whole page, the same trade loadLineImages makes. The per-line
// figure is added up here rather than asked of the database a second time, so
// "2 of 3 shipped" on a line and the parcels listed beside it are the same rows
// counted once and cannot contradict each other.
func (s *Orders) loadFulfillmentLines(ctx context.Context, orders []*Order,
	owner map[int64]*Order, at map[int64]int, ids []int64) error {

	if len(ids) == 0 {
		return nil
	}
	// An order line id is unique across orders, so one map serves the page.
	lineByID := map[int64]*OrderLine{}
	for _, o := range orders {
		for i := range o.Lines {
			lineByID[o.Lines[i].ID] = &o.Lines[i]
		}
	}

	rows, err := s.app.db.QueryContext(ctx, `
		SELECT fl.fulfillment_id, fl.order_line_id, fl.quantity, ol.sku, ol.title, ol.variant_label
		FROM fulfillment_lines fl
		JOIN order_lines ol ON ol.id = fl.order_line_id
		WHERE fl.fulfillment_id = ANY($1::bigint[])
		ORDER BY fl.fulfillment_id, fl.order_line_id`, int64Array(ids))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var fulfillmentID int64
		var fl FulfillmentLine
		if err := rows.Scan(&fulfillmentID, &fl.OrderLineID, &fl.Quantity,
			&fl.SKU, &fl.Title, &fl.VariantLabel); err != nil {
			return err
		}
		o := owner[fulfillmentID]
		if o == nil {
			continue
		}
		f := &o.Fulfillments[at[fulfillmentID]]
		f.Lines = append(f.Lines, fl)
		// A cancelled parcel still shows what it held — it is a record of what
		// was in the box — but its units are owed again, which is the rule the
		// whole derivation counts by.
		if f.Status == "cancelled" {
			continue
		}
		if l := lineByID[fl.OrderLineID]; l != nil {
			l.ShippedQuantity += fl.Quantity
		}
	}
	return rows.Err()
}

// -------------------------------------------------------------- transitions

// Confirm moves a pending order to confirmed and turns its inventory
// reservation into a committed sale. It is idempotent: confirming an
// already-confirmed order is a no-op, because a webhook may well arrive twice.
func (s *Orders) Confirm(ctx context.Context, id int64) (*Order, error) {
	return s.transition(ctx, id, func(ctx context.Context, tx *sql.Tx, o *Order) (transitionResult, error) {
		switch o.Status {
		// Partial belongs here for the same reason shipped does, and its absence
		// would be silent: the fall-through commits the shelf a second time and
		// then rewinds a partly shipped order to confirmed.
		case OrderConfirmed, OrderPartial, OrderShipped, OrderDelivered:
			return transitionResult{}, nil // already done
		case OrderCancelled:
			return transitionResult{}, Conflictf("order %s has been cancelled", o.Number)
		}
		if err := commitOrderStock(ctx, tx, o, ""); err != nil {
			return transitionResult{}, err
		}
		if err := setOrderStatus(ctx, tx, o.ID, OrderConfirmed); err != nil {
			return transitionResult{}, err
		}
		o.Status = OrderConfirmed
		// No event and no action: order.paid or order.created already told the
		// story, and a shopper's cash-on-delivery confirmation is not somebody's
		// act. Both callers — checkout and MarkPaid — record their own row.
		return transitionResult{}, nil
	})
}

// Cancel voids an order and returns its inventory. Which movement is correct
// depends on how far the order got: a pending order only ever reserved stock,
// while a confirmed one has already taken it off the shelf.
func (s *Orders) Cancel(ctx context.Context, id int64, reason string) (*Order, error) {
	return s.transition(ctx, id, func(ctx context.Context, tx *sql.Tx, o *Order) (transitionResult, error) {
		switch o.Status {
		case OrderCancelled:
			return transitionResult{}, nil
		case OrderShipped, OrderDelivered:
			return transitionResult{}, Conflictf("order %s has already shipped; cancelling it is a return, not a cancellation", o.Number)
		case OrderPartial:
			// stockCommitted is true here, so an unguarded cancel would restock
			// every line — inventing inventory for the units already in a van.
			return transitionResult{}, Conflictf(
				"order %s has already shipped in part; cancelling it is a return, not a cancellation. "+
					"Remove the shipment first if nothing actually went out.", o.Number)
		}
		// Reachable in three clicks: undeliver a returned order, delete its last
		// shipment — which sets it back to confirmed — and restockOrder below
		// then walks every line at its FULL quantity, so an order of five with
		// two already returned ends up seven units richer. Inside the callback,
		// under the same FOR UPDATE, so it cannot race a concurrent return.
		if returned, err := hasActiveReturn(ctx, tx, o.ID); err != nil {
			return transitionResult{}, err
		} else if returned {
			return transitionResult{}, Conflictf(
				"order %s has goods recorded as returned; cancelling now would put them back "+
					"on the shelf a second time — withdraw the return first", o.Number)
		}
		if stockCommitted(o.Status) {
			if err := restockOrder(ctx, tx, o, reason); err != nil {
				return transitionResult{}, err
			}
		} else if err := releaseOrderStock(ctx, tx, o, reason); err != nil {
			return transitionResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE orders SET status = $2, reservation_expires_at = NULL, updated_at = now()
			WHERE id = $1`, o.ID, OrderCancelled); err != nil {
			return transitionResult{}, err
		}
		o.Status = OrderCancelled
		payload := s.eventPayload(o)
		payload.Reason = reason
		summary := "Cancelled order " + o.Number
		if reason != "" {
			summary += " — " + reason
		}
		return transitionResult{
			Event: EventOrderCancelled, Payload: payload,
			Action: AuditOrderCancel, Summary: summary,
		}, nil
	})
}

// MarkDelivered records that the customer received the order.
func (s *Orders) MarkDelivered(ctx context.Context, id int64) (*Order, error) {
	return s.transition(ctx, id, func(ctx context.Context, tx *sql.Tx, o *Order) (transitionResult, error) {
		if o.Status == OrderDelivered {
			return transitionResult{}, nil
		}
		// Said before the general refusal, which would otherwise render as
		// "(it is partial)" — true, and no help at all.
		if o.Status == OrderPartial {
			return transitionResult{}, Conflictf(
				"order %s is only partly shipped; the rest has to go out before it can be delivered", o.Number)
		}
		if o.Status != OrderShipped {
			return transitionResult{}, Conflictf("order %s must be shipped before it can be delivered (it is %s)", o.Number, o.Status)
		}
		if err := setOrderStatus(ctx, tx, o.ID, OrderDelivered); err != nil {
			return transitionResult{}, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE fulfillments SET status = 'delivered', updated_at = now()
			 WHERE order_id = $1 AND status = 'shipped'`, o.ID); err != nil {
			return transitionResult{}, err
		}
		o.Status = OrderDelivered
		return transitionResult{
			Event: EventOrderDelivered, Payload: s.eventPayload(o),
			Action: AuditOrderDeliver, Summary: "Marked order " + o.Number + " delivered",
		}, nil
	})
}

// MarkUndelivered takes back a delivery recorded by mistake: the order goes
// back to shipped, and so do the fulfillments that were marked with it.
//
// The simplest of the corrections, because delivery is only ever a record of
// something somebody observed. Nothing moved when it was set — no stock, no
// money — so nothing has to move back. It refuses from anywhere but delivered:
// there is no other state a delivery can be undone from.
func (s *Orders) MarkUndelivered(ctx context.Context, id int64) (*Order, error) {
	return s.transition(ctx, id, func(ctx context.Context, tx *sql.Tx, o *Order) (transitionResult, error) {
		if o.Status == OrderShipped {
			return transitionResult{}, nil
		}
		if o.Status != OrderDelivered {
			return transitionResult{}, Conflictf("order %s is not delivered (it is %s)", o.Number, o.Status)
		}
		if err := setOrderStatus(ctx, tx, o.ID, OrderShipped); err != nil {
			return transitionResult{}, err
		}
		// Only the ones this delivery moved. A fulfillment cancelled separately
		// stays cancelled — it was not part of what is being undone.
		if _, err := tx.ExecContext(ctx,
			`UPDATE fulfillments SET status = 'shipped', updated_at = now()
			 WHERE order_id = $1 AND status = 'delivered'`, o.ID); err != nil {
			return transitionResult{}, err
		}
		o.Status = OrderShipped
		return transitionResult{
			Event: EventOrderUndelivered, Payload: s.eventPayload(o),
			Action:  AuditOrderUndeliver,
			Summary: "Undid the delivery of order " + o.Number,
		}, nil
	})
}

// transitionResult is what a state change reports about itself.
//
// Event and Action are independent because they always were, and the old
// (string, any, error) triple hid it: Confirm commits stock and sets status and
// names no event, and so do MarkFailed and Refund. So an empty event could
// never have meant "nothing happened". An empty Event publishes nothing; an
// empty Action records nothing; and "" no longer has to mean both at once.
//
// Action is deliberately not the event name. EditLines and Update both emit
// order.edited, so deriving one from the other would collapse "who changed the
// lines" and "who changed the shipping address" into one indistinguishable row.
// A returned struct beats an added parameter for the same reason: a zero-valued
// parameter silently writes an empty action, while a callback that returns a
// result names its action on the line where it already names its event.
type transitionResult struct {
	Event   string
	Payload any

	Action        string
	Summary       string
	Before, After map[string]any
}

// transition runs a state change, its event and its audit record in one
// transaction, which is what makes all three inseparable.
//
// A callback that names no event emits nothing and one that names no action
// records nothing, so an idempotent no-op transition stays quiet in both logs
// instead of announcing a change that did not happen.
func (s *Orders) transition(ctx context.Context, id int64,
	fn func(context.Context, *sql.Tx, *Order) (transitionResult, error)) (*Order, error) {

	// Every stock movement a transition causes is on the order path. One line
	// covers Confirm, Cancel, EditLines, the delivery verbs and all four
	// payment methods; because withStockSource is first-label-wins, it leaves
	// SweepUnpaid's more specific label alone.
	ctx = withStockSource(ctx, sourceOrder)

	err := InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		o, err := lockOrder(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := loadOrderLinesTx(ctx, tx, o); err != nil {
			return err
		}
		// lockOrder has already read both, so the status half of every order
		// audit row costs nothing.
		beforeStatus, beforePayment := o.Status, o.PaymentStatus

		res, err := fn(ctx, tx, o)
		if err != nil {
			return err
		}
		if res.Event != "" {
			if err := s.app.outbox.write(ctx, tx, res.Event, AggregateOrder, o.ID, res.Payload); err != nil {
				return err
			}
		}
		if res.Action == "" {
			return nil
		}
		// Both rows are written by this one InTx, so the order between them
		// carries no atomicity meaning: a rollback removes both, and the
		// dispatcher claims only committed rows.
		before, after := res.Before, res.After
		if o.Status != beforeStatus {
			before = withField(before, "status", beforeStatus)
			after = withField(after, "status", o.Status)
		}
		if o.PaymentStatus != beforePayment {
			before = withField(before, "payment_status", beforePayment)
			after = withField(after, "payment_status", o.PaymentStatus)
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: res.Action, Entity: AuditEntityOrder,
			ID: o.ID, Label: o.Number, Summary: res.Summary, Event: res.Event,
			Before: before, After: after,
		})
	})
	if err != nil {
		return nil, err
	}
	s.app.nudgeOutbox()
	return s.Get(ctx, id)
}

// lockOrder reads an order FOR UPDATE so concurrent transitions serialize.
func lockOrder(ctx context.Context, tx *sql.Tx, id int64) (*Order, error) {
	o := &Order{}
	// refunded_minor comes off the row this statement is already holding, which
	// is the whole point of storing it: every guard below sees a figure that
	// cannot be one commit behind, where a scalar sub-select in this target list
	// would be evaluated against the statement's pre-block snapshot under READ
	// COMMITTED — exactly how a second serialised refund misses the first.
	var refunded int64
	// metadata comes off the same row for the same reason, and pays for itself
	// once: the locked row is the only place a transition can tell an operator
	// typing a note apart from a save that wiped a module's key, and those two
	// have to end differently — one silent, one announced. Nothing starts
	// riding event payloads by reading it here, and nothing may: eventPayload
	// leaves OrderEvent.Metadata unset, which is what keeps the note off every
	// notifier's template data.
	var meta []byte
	err := tx.QueryRowContext(ctx, `
		SELECT id, number, status, payment_status, payment_provider, currency,
		       total_minor, refunded_minor, email, coalesce(phone,''), coalesce(name,''),
		       lang, metadata
		FROM orders WHERE id = $1 FOR UPDATE`, id,
	).Scan(&o.ID, &o.Number, &o.Status, &o.PaymentStatus, &o.PaymentProvider, &o.Currency,
		&o.Total.AmountMinor, &refunded, &o.Email, &o.Phone, &o.Name, &o.Language, &meta)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("order %d does not exist", id)
	}
	if err != nil {
		return nil, err
	}
	if err := scanMetadata(meta, &o.Metadata); err != nil {
		return nil, err
	}
	o.Total.Currency = o.Currency
	o.Refunded = money(refunded, o.Currency)
	return o, nil
}

func loadOrderLinesTx(ctx context.Context, tx *sql.Tx, o *Order) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, variant_id, sku, title, variant_label, quantity, unit_price_minor,
		       total_minor, tax_minor, tax_rate_bp, tax_name, location_id
		FROM order_lines WHERE order_id = $1 ORDER BY id`, o.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	o.Lines = nil
	for rows.Next() {
		var l OrderLine
		var variantID, locationID sql.NullInt64
		if err := rows.Scan(&l.ID, &variantID, &l.SKU, &l.Title, &l.VariantLabel,
			&l.Quantity, &l.UnitPrice.AmountMinor, &l.Total.AmountMinor,
			&l.Tax.AmountMinor, &l.Tax.RateBP, &l.Tax.Name, &locationID); err != nil {
			return err
		}
		if variantID.Valid {
			l.VariantID = &variantID.Int64
		}
		if locationID.Valid {
			l.LocationID = &locationID.Int64
		}
		l.UnitPrice.Currency = o.Currency
		l.Total.Currency = o.Currency
		o.Lines = append(o.Lines, l)
	}
	return rows.Err()
}

func setOrderStatus(ctx context.Context, tx *sql.Tx, id int64, status string) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE orders SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	return err
}

func commitOrderStock(ctx context.Context, tx *sql.Tx, o *Order, reason string) error {
	for _, l := range o.Lines {
		if l.VariantID == nil {
			continue
		}
		loc, err := lineLocation(ctx, tx, l)
		if err != nil {
			return err
		}
		if err := commitStock(ctx, tx, *l.VariantID, loc, l.Quantity,
			stockRef{Reason: reason, OrderID: o.ID, OrderNumber: o.Number}); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE orders SET reservation_expires_at = NULL WHERE id = $1`, o.ID)
	return err
}

func releaseOrderStock(ctx context.Context, tx *sql.Tx, o *Order, reason string) error {
	for _, l := range o.Lines {
		if l.VariantID == nil {
			continue
		}
		loc, err := lineLocation(ctx, tx, l)
		if err != nil {
			return err
		}
		if err := releaseStock(ctx, tx, *l.VariantID, loc, l.Quantity,
			stockRef{Reason: reason, OrderID: o.ID, OrderNumber: o.Number}); err != nil {
			return err
		}
	}
	return nil
}

// restockOrder puts a whole order back on the shelf, every line at its full
// quantity. It is the cancellation movement and nothing else: goods coming back
// off a sale that stands are per line and per quantity, which is Orders.Return.
func restockOrder(ctx context.Context, tx *sql.Tx, o *Order, reason string) error {
	for _, l := range o.Lines {
		if l.VariantID == nil {
			continue
		}
		loc, err := lineLocation(ctx, tx, l)
		if err != nil {
			return err
		}
		if err := restockStock(ctx, tx, *l.VariantID, loc, l.Quantity,
			stockRef{Reason: reason, OrderID: o.ID, OrderNumber: o.Number}); err != nil {
			return err
		}
	}
	return nil
}

// eventPayload builds the public shape of an order event.
func (s *Orders) eventPayload(o *Order) *OrderEvent {
	ev := &OrderEvent{
		OrderID: o.ID, Number: o.Number, Status: o.Status,
		PaymentStatus: o.PaymentStatus, Provider: o.PaymentProvider,
		Currency: o.Currency, TotalMinor: o.Total.AmountMinor,
		RefundedMinor: o.Refunded.AmountMinor,
		Email:         o.Email, Phone: o.Phone, Name: o.Name, Language: o.Language,
	}
	for _, l := range o.Lines {
		ev.Lines = append(ev.Lines, OrderEventLine{
			SKU: l.SKU, Title: l.Title, VariantLabel: l.VariantLabel,
			Quantity: l.Quantity, UnitPriceMinor: l.UnitPrice.AmountMinor,
			TotalMinor: l.Total.AmountMinor,
		})
	}
	return ev
}

// SweepUnpaid cancels pending orders whose payment never settled, returning
// their stock to the shelf. It is the pass without the bookkeeping, which is
// all the five-minute ticker needs.
func (s *Orders) SweepUnpaid(ctx context.Context) (int, error) {
	n, _, err := s.SweepUnpaidPass(ctx)
	return n, err
}

// sweepUnpaidBatch bounds one pass. Each cancellation is its own transaction
// with its own event, so an unbounded pass would hold the ticker's goroutine —
// or an HTTP request — open for as long as the backlog takes. A var rather than
// a const so a test can lower it and reach the capped path without building a
// two-hundred-order backlog.
var sweepUnpaidBatch = 200

// SweepUnpaidPass cancels pending orders whose payment never settled, returning
// their stock to the shelf. Without it an abandoned redirect holds inventory
// out of sale forever, which is invisible until the day it sells out a product
// that is actually in stock.
//
// Unsettled is wider than unpaid: a declined card holds exactly the same stock
// as an abandoned one, and a payment recorded as failed is the one nobody is
// coming back for. Before M23 this scanned payment_status = 'pending' alone, so
// [Payments.MarkFailed] removed an order from the sweep permanently — a stock
// leak with no cleanup path, which mattered little while two webhooks were the
// only callers and matters a great deal now there is a button.
//
// It reports both halves of what happened. scanned is how many expired orders
// it found, cancelled how many it could actually cancel, and a caller with a
// button to draw needs both: a full batch means "come back", and a full batch
// that cancelled fewer means something else is wrong as well. Deriving "was
// there more" from the cancellation count alone would report a clear queue in
// exactly the backlogged-and-partly-broken state an operator presses the button
// in — an order that cannot be cancelled is logged and skipped just below.
func (s *Orders) SweepUnpaidPass(ctx context.Context) (cancelled, scanned int, err error) {
	// Named once, before the loop, so two hundred cancellations at 3am read as
	// the store doing maintenance rather than as somebody's night's work.
	ctx = WithActorLabel(ctx, "unpaid sweeper")

	// The status constants are inline rather than bound, which is the one thing
	// about this query worth a comment. orders_unsettled_idx (M23) is a partial
	// index, and the planner uses one only when it can prove the query's WHERE
	// implies the index predicate. This runs every five minutes through a
	// cached statement, so a generic plan over $1/$2 proves nothing about
	// 'pending' — the index would be ignored and every pass would seq-scan
	// orders. They are compile-time constants and never caller input, so
	// nothing is lost.
	//
	// The LIMIT is bound, and safely: it takes no part in the index predicate,
	// so a generic plan is as good as a specific one. It is a parameter because
	// the bound has to be one value — the caller that derives "was there more"
	// compares against sweepUnpaidBatch, and two copies of 200 are two things
	// that can drift.
	rows, err := s.app.db.QueryContext(ctx, `
		SELECT id FROM orders
		WHERE status = 'pending' AND payment_status IN ('pending', 'failed')
		  AND reservation_expires_at IS NOT NULL AND reservation_expires_at < now()
		LIMIT $1`, sweepUnpaidBatch)
	if err != nil {
		return 0, 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	var swept int
	for _, id := range ids {
		// Labelled before Cancel's own transition can label it 'order', which
		// is what makes a night's sweep distinguishable in the ledger from an
		// operator cancelling by hand.
		if _, err := s.Cancel(withStockSource(ctx, sourceSweeper), id,
			"payment not completed in time"); err != nil {
			s.app.log.Warn("could not cancel expired order", "order_id", id, "error", err)
			continue
		}
		swept++
	}
	if swept > 0 {
		s.app.log.Info("released inventory from expired unpaid orders", "orders", swept)
	}
	return swept, len(ids), nil
}

// ------------------------------------------------------------- editing lines

// OrderEdit is the desired set of lines on an order.
//
// The whole set, not a diff: the panel holds the lines it is showing, and
// "here is what the order should be" is the request it can actually make
// without racing another edit. A line the order already has is named by its id;
// one being added names a variant instead. A line the request leaves out is
// removed.
type OrderEdit struct {
	Lines []OrderLineEdit `json:"lines"`
}

// OrderLineEdit is one line of a desired order.
type OrderLineEdit struct {
	// ID names a line the order already has. Zero means this is a new line,
	// and VariantID says what to add.
	ID        int64 `json:"id"`
	VariantID int64 `json:"variant_id"`
	Quantity  int   `json:"quantity"`
}

// OrderChange reports what an edit did, so the operator can be told rather
// than left to compare two screens.
type OrderChange struct {
	LinesAdded   []string `json:"lines_added"`
	LinesRemoved []string `json:"lines_removed"`
	LinesChanged []string `json:"lines_changed"`
	TotalBefore  Money    `json:"total_before"`
	TotalAfter   Money    `json:"total_after"`
	// BalanceMinor is what the edit moved the total by: positive is owed by the
	// customer, negative is owed to them. It is reported rather than settled —
	// taking a payment or making a refund is its own operation, with its own
	// provider and its own record.
	BalanceMinor int64 `json:"balance_minor"`
}

// EditLines changes what an order is for, and moves the stock the change
// implies.
//
// PLAN §10.3 calls an order line an immutable snapshot, and the reason it gives
// is that a historical order must stay readable when the product behind it
// changes or is deleted. That reason is about the catalog moving underneath an
// order; it is not about the operator and the customer agreeing to something
// different. So the snapshot stays a snapshot — a line still holds its own sku,
// title and price, and still survives its variant being deleted — and the
// amendment is recorded as order.edited, carrying the totals either side of it.
// The order says what is now agreed; the event stream says how it got there.
//
// The stock movement is the same fork Cancel makes: an order that only reserved
// its stock has its reservation adjusted, and one that has already taken the
// units off the shelf gives them back or takes more.
//
// What it will not do is settle the money. The new total can be under what was
// paid or over it, and both are reported as a balance for the operator to act
// on — a refund is a provider operation with its own record, and inventing one
// here would hide it.
func (s *Orders) EditLines(ctx context.Context, id int64, in OrderEdit) (*Order, *OrderChange, error) {
	change := &OrderChange{}
	_, err := s.transition(ctx, id, func(ctx context.Context, tx *sql.Tx, o *Order) (transitionResult, error) {
		switch o.Status {
		case OrderCancelled:
			return transitionResult{}, Conflictf("order %s has been cancelled", o.Number)
		case OrderShipped, OrderDelivered:
			return transitionResult{}, Conflictf(
				"order %s has already shipped; changing what is in it is a return, not an edit", o.Number)
		case OrderPartial:
			// Also what keeps the order_lines rows alive underneath
			// fulfillment_lines: a parcel names lines, and half an order cannot
			// have its lines deleted from under it.
			return transitionResult{}, Conflictf(
				"order %s has already shipped in part; changing what is in it is a return, not an edit", o.Number)
		}
		// Any refund at all, not only a full one. This is the only path that
		// lowers total_minor, so it is the only thing that could push the total
		// under refunded_minor and reach orders_refunded_within_total — and
		// under D36 a partly refunded order still reads `paid`, so the status
		// form of this guard would let it through.
		if o.Refunded.AmountMinor > 0 {
			return transitionResult{}, Conflictf(
				"order %s has had %d %s refunded; changing what is in it would move a total money has already come off",
				o.Number, o.Refunded.AmountMinor, o.Currency)
		}
		// Guarding Cancel alone would not be enough. A returned order walked
		// back to confirmed is editable again, and moveOrderStock's committed
		// branch restocks a reduced line — taking off the shelf what the return
		// just put on it, by a second route to the same double movement.
		if returned, err := hasActiveReturn(ctx, tx, o.ID); err != nil {
			return transitionResult{}, err
		} else if returned {
			return transitionResult{}, Conflictf(
				"order %s has goods recorded as returned; its lines no longer describe what left "+
					"the store — withdraw the return first", o.Number)
		}

		existing := map[int64]*OrderLine{}
		for i := range o.Lines {
			existing[o.Lines[i].ID] = &o.Lines[i]
		}

		// The whole request is checked before anything moves: a half-applied
		// edit is worse than a refused one.
		seen := map[int64]bool{}
		for _, l := range in.Lines {
			if l.Quantity < 0 {
				return transitionResult{}, Validationf("a quantity cannot be negative")
			}
			if l.ID == 0 {
				if l.VariantID == 0 {
					return transitionResult{}, Validationf("a new line needs a variant_id")
				}
				continue
			}
			if _, ok := existing[l.ID]; !ok {
				return transitionResult{}, Validationf("order %s has no line %d", o.Number, l.ID)
			}
			if seen[l.ID] {
				return transitionResult{}, Validationf("line %d is named twice", l.ID)
			}
			seen[l.ID] = true
		}

		committed := stockCommitted(o.Status)

		// 1. The lines already there: requantified, or gone.
		for _, line := range o.Lines {
			want := 0
			for _, l := range in.Lines {
				if l.ID == line.ID {
					want = l.Quantity
				}
			}
			if want == line.Quantity {
				continue
			}
			if err := moveOrderStock(ctx, tx, o, line, want-line.Quantity, committed); err != nil {
				return transitionResult{}, err
			}
			if want == 0 {
				if _, err := tx.ExecContext(ctx,
					`DELETE FROM order_lines WHERE id = $1`, line.ID); err != nil {
					return transitionResult{}, err
				}
				change.LinesRemoved = append(change.LinesRemoved, line.SKU)
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE order_lines
				SET quantity = $2::integer, total_minor = unit_price_minor * $2::integer
				WHERE id = $1`, line.ID, want); err != nil {
				return transitionResult{}, err
			}
			change.LinesChanged = append(change.LinesChanged,
				fmt.Sprintf("%s: %d to %d", line.SKU, line.Quantity, want))
		}

		// 2. New lines, priced as they are today: an amendment is agreed now,
		//    so today's price is the one that was agreed — not the price the
		//    rest of the order was placed at.
		for _, l := range in.Lines {
			if l.ID != 0 || l.Quantity == 0 {
				continue
			}
			sku, err := addOrderLine(ctx, tx, o, l.VariantID, l.Quantity, committed)
			if err != nil {
				return transitionResult{}, err
			}
			change.LinesAdded = append(change.LinesAdded, sku)
		}

		// 3. Retotal from the lines that are actually there.
		var subtotal int64
		var lines int
		if err := tx.QueryRowContext(ctx, `
			SELECT coalesce(sum(total_minor), 0), count(*)
			FROM order_lines WHERE order_id = $1`, o.ID).Scan(&subtotal, &lines); err != nil {
			return transitionResult{}, err
		}
		if lines == 0 {
			return transitionResult{}, Validationf(
				"an order must keep at least one line; cancel order %s instead", o.Number)
		}

		var shipping, discount int64
		if err := tx.QueryRowContext(ctx,
			`SELECT shipping_minor, discount_minor FROM orders WHERE id = $1`,
			o.ID).Scan(&shipping, &discount); err != nil {
			return transitionResult{}, err
		}
		// The discount follows the basket it came off (D27, amended by D39). A
		// fixed amount is a fixed amount whatever is left; a percentage was a
		// percentage of a basket that no longer exists, so it is taken again. A
		// scoped rule is judged against the lines it still covers, which clamps
		// even a fixed amount. An order that has dropped below the minimum its
		// discount required, or that no longer holds anything its rule covers,
		// is refused rather than quietly kept — the promotion it qualified for
		// is one it no longer qualifies for, and only an operator can decide
		// what to do.
		discount, err := recomputeOrderDiscount(ctx, tx, o.ID, subtotal, discount)
		if err != nil {
			return transitionResult{}, err
		}
		total := subtotal + shipping - discount
		if total < 0 {
			// A discount larger than what is left of the order. The floor is the
			// database's own CHECK; saying so beats a constraint violation.
			return transitionResult{}, Conflictf(
				"the discount on order %s is larger than the lines that would be left", o.Number)
		}

		change.TotalBefore = money(o.Total.AmountMinor, o.Currency)
		change.TotalAfter = money(total, o.Currency)
		change.BalanceMinor = total - o.Total.AmountMinor

		if _, err := tx.ExecContext(ctx, `
			UPDATE orders SET subtotal_minor = $2, total_minor = $3,
			                  discount_minor = $4, updated_at = now()
			WHERE id = $1`, o.ID, subtotal, total, discount); err != nil {
			return transitionResult{}, err
		}
		if len(change.LinesAdded) == 0 && len(change.LinesRemoved) == 0 &&
			len(change.LinesChanged) == 0 {
			return transitionResult{}, nil // nothing happened, so there is nothing to announce
		}

		o.Total = change.TotalAfter
		payload := s.eventPayload(o)
		payload.Change = change
		return transitionResult{
			Event: EventOrderEdited, Payload: payload,
			Action:  AuditOrderEditLines,
			Summary: "Changed what is on order " + o.Number,
			// Built from the OrderChange this method already filled. Money is
			// minor units plus the order's currency and never a formatted
			// amount (rule 6).
			After: map[string]any{
				"lines_added":        change.LinesAdded,
				"lines_removed":      change.LinesRemoved,
				"lines_changed":      change.LinesChanged,
				"total_before_minor": change.TotalBefore.AmountMinor,
				"total_after_minor":  change.TotalAfter.AmountMinor,
				"balance_minor":      change.BalanceMinor,
				"currency":           o.Currency,
			},
		}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	o, err := s.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return o, change, nil
}

// moveOrderStock applies a quantity delta to a variant, in whichever direction
// and against whichever pool the order's own state says is right.
//
// A line whose variant is gone — the product was deleted — moves nothing. The
// snapshot is still readable history; there is simply no shelf to put it back
// on.
func moveOrderStock(ctx context.Context, tx *sql.Tx, o *Order, line OrderLine, delta int, committed bool) error {
	if line.VariantID == nil || delta == 0 {
		return nil
	}
	loc, err := lineLocation(ctx, tx, line)
	if err != nil {
		return err
	}
	// The ledger row says which order was edited, because "why did this shelf
	// lose two units on Tuesday" has no answer in an order-line diff.
	ref := stockRef{Reason: "order edited", OrderID: o.ID, OrderNumber: o.Number}
	switch {
	case delta > 0 && committed:
		return sellStock(ctx, tx, *line.VariantID, loc, delta, ref)
	case delta > 0:
		_, err := reserveStock(ctx, tx, *line.VariantID, loc, delta, ref)
		return err
	case committed:
		return restockStock(ctx, tx, *line.VariantID, loc, -delta, ref)
	default:
		return releaseStock(ctx, tx, *line.VariantID, loc, -delta, ref)
	}
}

// lineLocation is where this line's units live. A line from before M17, or one
// whose location has since been closed, has none recorded; the default is the
// only honest answer left, and it is at least somewhere the store can count.
//
// It also makes sure the row exists, because a movement against a missing
// (variant, location) pair matches nothing and would silently lose the units.
func lineLocation(ctx context.Context, tx *sql.Tx, line OrderLine) (int64, error) {
	var loc int64
	if line.LocationID != nil {
		loc = *line.LocationID
	}
	loc, err := resolveLocation(ctx, tx, loc)
	if err != nil {
		return 0, err
	}
	if line.VariantID == nil {
		return loc, nil
	}
	return loc, ensureStockRow(ctx, tx, *line.VariantID, loc)
}

// addOrderLine snapshots a variant onto an order and takes its stock.
func addOrderLine(ctx context.Context, tx *sql.Tx, o *Order, variantID int64, qty int, committed bool) (string, error) {
	if qty <= 0 {
		return "", Validationf("a new line needs a quantity")
	}
	var (
		productID int64
		sku       string
		title     string
		price     int64
		sellable  bool
	)
	err := tx.QueryRowContext(ctx, `
		SELECT v.product_id, v.sku, p.title, v.price_minor, v.active AND p.status = 'active'
		FROM variants v JOIN products p ON p.id = v.product_id
		WHERE v.id = $1`, variantID).Scan(&productID, &sku, &title, &price, &sellable)
	if errors.Is(err, sql.ErrNoRows) {
		return "", NotFoundf("variant %d does not exist", variantID)
	}
	if err != nil {
		return "", err
	}
	if !sellable {
		return "", Conflictf("variant %s is not on sale", sku)
	}

	var label string
	if err := tx.QueryRowContext(ctx, `
		SELECT coalesce(string_agg(pov.value, ' / ' ORDER BY po.position, po.id), '')
		FROM variant_option_values vov
		JOIN product_option_values pov ON pov.id = vov.option_value_id
		JOIN product_options po ON po.id = pov.option_id
		WHERE vov.variant_id = $1`, variantID).Scan(&label); err != nil {
		return "", err
	}

	// A line added by hand takes its stock the same way checkout does: from
	// wherever can cover it, in priority order.
	locationID, err := pickLocation(ctx, tx, variantID, qty)
	if err != nil {
		return "", err
	}
	line := OrderLine{VariantID: &variantID, LocationID: &locationID}
	if err := moveOrderStock(ctx, tx, o, line, qty, committed); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO order_lines (order_id, product_id, variant_id, sku, title, variant_label,
		                         quantity, unit_price_minor, total_minor, location_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		o.ID, productID, variantID, sku, title, label, qty, price, price*int64(qty),
		locationID); err != nil {
		return "", err
	}
	return sku, nil
}

// OrderPatch corrects what was recorded about an order: who it is for, and how
// it was paid for. Pointers throughout, because "" is a real value for most of
// these — clearing a phone number nobody can reach is a correction.
//
// It deliberately cannot reach status, payment status, totals or lines. Each of
// those is a state change with consequences — stock, events, money — and each
// already has an operation that performs them properly. This is for the fields
// somebody typed.
type OrderPatch struct {
	Email   *string  `json:"email"`
	Phone   *string  `json:"phone"`
	Name    *string  `json:"name"`
	Address *Address `json:"address"`

	PaymentProvider  *string `json:"payment_provider"`
	PaymentReference *string `json:"payment_reference"`

	// Metadata replaces the whole object, like every other patch that carries
	// one, so the read-modify-write is the caller's. OrderNoteKey is the
	// operator's note; a patch that changes nothing else stays quiet, because a
	// note is not a state change.
	//
	// A pointer rather than a bare map, which is what separates "the patch did
	// not mention metadata" from "the patch cleared it".
	Metadata *Metadata `json:"metadata"`
}

// onlyTheNoteMoved reports whether two metadata objects agree everywhere except
// at OrderNoteKey. It is what separates an operator typing a note — which
// nothing downstream can act on, and which therefore stays quiet — from a save
// that also rewrote or dropped a module's key, which is a real change and
// announces itself like any other.
//
// Both sides arrived through encoding/json, so their numbers are both float64
// and DeepEqual compares like with like.
func onlyTheNoteMoved(stored, incoming Metadata) bool {
	a, b := maps.Clone(stored), maps.Clone(incoming)
	if a == nil {
		a = Metadata{}
	}
	if b == nil {
		b = Metadata{}
	}
	delete(a, OrderNoteKey)
	delete(b, OrderNoteKey)
	return reflect.DeepEqual(a, b)
}

// Update corrects an order's contact details and payment record.
//
// Both halves are the same kind of fix. An email with a typo in it means the
// customer never hears from the shop again; an address with the wrong house
// number means the parcel goes to the wrong door; a payment reference typed off
// a screen is the thing somebody reconciles against a bank statement. None of
// them is a state change, and until now the only way to correct any of them was
// a row in the database.
//
// Metadata is the third half, and it behaves differently on purpose. It is
// replaced whole, like every other patch that carries one, and its reserved
// OrderNoteKey is the operator's note on this order. A patch carrying nothing
// but metadata is accepted on a cancelled order — a note is never a fact of the
// order, and "refunded manually by bank transfer" is written precisely on the
// one that went wrong — and emits no event when it leaves every other key as it
// found them, because a note changes nothing anybody downstream can act on. A
// save that also rewrote or dropped a module's key is an ordinary edit again
// and announces itself like one.
//
// The one that needs care is the provider, because Refund books through it. It
// may be changed while nothing has been refunded — an order taken as cash on
// delivery and actually settled by transfer should say so — but not after any
// refund, full or partial, where the money went out through the provider that
// is on the order now, and rewriting it would leave the record pointing at a
// gateway that never saw it.
func (s *Orders) Update(ctx context.Context, id int64, patch OrderPatch) (*Order, error) {
	if patch.Email != nil {
		if strings.TrimSpace(*patch.Email) == "" {
			return nil, Validationf("an order needs an email address; it is how the customer hears about it")
		}
	}
	if patch.Address != nil {
		if err := patch.Address.Validate(); err != nil {
			return nil, err
		}
	}
	if patch.PaymentProvider != nil {
		if _, ok := s.app.payments.provider(*patch.PaymentProvider); !ok {
			return nil, Validationf("payment method %q is not installed in this build", *patch.PaymentProvider)
		}
	}

	return s.transition(ctx, id, func(ctx context.Context, tx *sql.Tx, o *Order) (transitionResult, error) {
		// The cancellation guard is no longer the first thing here: whether a
		// dead order may be written to depends on what the patch turned out to
		// carry, which is only known once the apply loop below has run.
		if patch.PaymentProvider != nil && *patch.PaymentProvider != o.PaymentProvider &&
			o.Refunded.AmountMinor > 0 {
			return transitionResult{}, Conflictf(
				"order %s has had money refunded through %s; the method it was settled by is part of that record",
				o.Number, o.PaymentProvider)
		}

		set, args := []string{}, []any{id}
		// before and after are filled by the same closure that builds the
		// UPDATE, so the audit record can never name a field the statement did
		// not set. The `from` value is read off o on the line above the one
		// that overwrites it, which is why both sides are free here.
		before, after := map[string]any{}, map[string]any{}
		add := func(col string, v any, from, to any) {
			args = append(args, v)
			set = append(set, fmt.Sprintf("%s = $%d", col, len(args)))
			before[col], after[col] = from, to
		}
		if patch.Email != nil {
			was := o.Email
			o.Email = strings.TrimSpace(*patch.Email)
			add("email", o.Email, was, o.Email)
		}
		if patch.Phone != nil {
			was := o.Phone
			o.Phone = strings.TrimSpace(*patch.Phone)
			add("phone", nullString(o.Phone), was, o.Phone)
		}
		if patch.Name != nil {
			was := o.Name
			o.Name = strings.TrimSpace(*patch.Name)
			add("name", nullString(o.Name), was, o.Name)
		}
		if patch.Address != nil {
			encoded, err := json.Marshal(*patch.Address)
			if err != nil {
				return transitionResult{}, Internalf(err, "encode the address")
			}
			was := o.Address
			o.Address = *patch.Address
			add("address", encoded, was, o.Address)
		}
		if patch.PaymentProvider != nil {
			was := o.PaymentProvider
			o.PaymentProvider = *patch.PaymentProvider
			add("payment_provider", o.PaymentProvider, was, o.PaymentProvider)
		}
		if patch.PaymentReference != nil {
			was := o.PaymentReference
			o.PaymentReference = strings.TrimSpace(*patch.PaymentReference)
			add("payment_reference", o.PaymentReference, was, o.PaymentReference)
		}

		// Two questions, two answers, both derived from what the apply loop
		// actually produced rather than from a list of field names — a list
		// would silently misclassify the next field somebody adds above.
		metadataOnly := patch.Metadata != nil && len(set) == 0
		// A note is never a fact of the order, so it is the one thing still
		// worth writing on a cancelled one: "refunded manually by bank
		// transfer, ref 88213" belongs on the order that went wrong. The guard
		// protects facts that can no longer matter on a dead order — an
		// address, a payment method — and a note is not one of them.
		if o.Status == OrderCancelled && !metadataOnly {
			return transitionResult{}, Conflictf("order %s has been cancelled", o.Number)
		}
		// Silence has to be earned: the patch replaces the object whole, so a
		// save that also dropped a module's key is a real change and says so.
		noteOnly := metadataOnly && onlyTheNoteMoved(o.Metadata, *patch.Metadata)

		if patch.Metadata != nil {
			if v, ok := (*patch.Metadata)[OrderNoteKey]; ok {
				if _, isString := v.(string); !isString {
					// The panel edits this in a textarea and trims it. jsonb
					// would take an object here quite happily, and the operator
					// would meet it as a crash on their first click.
					return transitionResult{}, Validationf("metadata.%s must be a string", OrderNoteKey)
				}
			}
			encoded, err := patch.Metadata.value()
			if err != nil {
				return transitionResult{}, Validationf("metadata is not valid JSON: %v", err)
			}
			was := o.Metadata
			o.Metadata = *patch.Metadata
			add("metadata", encoded, was, o.Metadata)
		}

		if len(set) == 0 {
			return transitionResult{}, Validationf("nothing to change")
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE orders SET `+strings.Join(set, ", ")+`, updated_at = now() WHERE id = $1`,
			args...); err != nil {
			return transitionResult{}, err
		}
		if noteOnly {
			// transition's documented quiet path. Every order.* event reaches
			// the notifier bridge and fans out to the customer's email and
			// phone, and a note is not a state change — so announcing one would
			// put "your order has been updated" in the shopper's inbox for each
			// line an operator wrote to themselves.
			//
			// Quiet in the outbox is not quiet in the trail: who wrote a note,
			// and what it said before, is exactly what somebody reading the
			// order back later is asking, and an audit row announces nothing to
			// anybody outside the store.
			return transitionResult{
				Action:  AuditOrderNote,
				Summary: "Wrote a note on order " + o.Number,
				Before:  before, After: after,
			}, nil
		}
		// order.edited, because that is what happened and a notifier keyed to
		// this order needs to know the address it holds has changed. The action
		// is order.update rather than the shared event name: EditLines emits
		// order.edited too, and "who changed the shipping address" has to stay
		// a different question from "who changed the lines".
		return transitionResult{
			Event: EventOrderEdited, Payload: s.eventPayload(o),
			Action:  AuditOrderUpdate,
			Summary: "Corrected the details on order " + o.Number,
			Before:  before, After: after,
		}, nil
	})
}

// loadLineImages attaches a picture to every line that still has a product.
//
// One query for a whole page of orders, like the categories: a line at a time
// would be one round trip per item, and the orders list is the busiest screen
// in the panel.
//
// A variant's own picture wins over the product's first, because that is what
// was bought — a red shirt should not show the blue one. Lines whose product
// has been deleted simply come back without one; the order is a record of a
// sale and does not stop being true because the catalog moved on.
func (s *Orders) loadLineImages(ctx context.Context, orders []*Order) error {
	products := map[int64]bool{}
	for _, o := range orders {
		for _, l := range o.Lines {
			if l.ProductID != nil {
				products[*l.ProductID] = true
			}
		}
	}
	if len(products) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(products))
	for id := range products {
		ids = append(ids, id)
	}

	rows, err := s.app.db.QueryContext(ctx, `
		SELECT pm.product_id, pm.variant_id, m.url
		FROM product_media pm
		JOIN media m ON m.id = pm.media_id
		WHERE pm.product_id = ANY($1::bigint[]) AND m.kind = 'image'
		ORDER BY pm.product_id, pm.position, pm.media_id`, int64Array(ids))
	if err != nil {
		return err
	}
	defer rows.Close()

	byProduct := map[int64]string{}
	byVariant := map[int64]string{}
	for rows.Next() {
		var productID int64
		var variantID *int64
		var url string
		if err := rows.Scan(&productID, &variantID, &url); err != nil {
			return err
		}
		if variantID != nil {
			byVariant[*variantID] = url
		}
		// Position order, so the first row for a product is its lead image.
		if _, seen := byProduct[productID]; !seen {
			byProduct[productID] = url
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, o := range orders {
		for i := range o.Lines {
			l := &o.Lines[i]
			if l.VariantID != nil {
				if url, ok := byVariant[*l.VariantID]; ok {
					l.ImageURL = url
					continue
				}
			}
			if l.ProductID != nil {
				l.ImageURL = byProduct[*l.ProductID]
			}
		}
	}
	return nil
}

// ----------------------------------------------------------- the timeline

// OrderTimelineEntry is one thing that happened to an order.
//
// It is a projection of two tables, not either one of them: outbox_events says
// what the order announced and whether anybody heard it, admin_audit says who
// did it and which fields moved, and the two are written by the same
// transaction whenever an operator causes a transition. Rendering both raw
// would show every such moment twice.
//
// The payload half is a reading rather than the payload: OrderEvent is a public
// contract for consumers, and this is an operator looking at one order.
type OrderTimelineEntry struct {
	At time.Time `json:"at"`
	// Kind is `action` when a person, a token or the engine's own background
	// work was recorded doing this, and `event` when all that survives is the
	// announcement — a transition from before the trail existed, or one whose
	// audit row names no event.
	Kind string `json:"kind"`
	// Name is the event this announced, empty where it announced nothing. An
	// operator's note is the one order write that is deliberately quiet.
	Name string `json:"name,omitempty"`
	// Action is the operator's verb — order.mark_paid, order.note — and is
	// deliberately not the event name: EditLines and Update both emit
	// order.edited, and "who changed the lines" must stay a different question
	// from "who changed the address".
	Action  string `json:"action,omitempty"`
	Summary string `json:"summary,omitempty"`

	// Who. Empty on an entry that has no audit row behind it; ActorEmail is
	// empty for a token and for the engine's own work, because a credential is
	// not a person.
	ActorKind  string `json:"actor_kind,omitempty"`
	ActorEmail string `json:"actor_email,omitempty"`
	ActorRole  string `json:"actor_role,omitempty"`
	ActorLabel string `json:"actor_label,omitempty"`

	// Delivery, present only where there is an event. PublishedAt is set once
	// every subscribed handler accepted it; nil means it is still queued — or,
	// with Dead, that nobody could be told at all.
	EventID     string     `json:"event_id,omitempty"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
	Attempts    int        `json:"attempts,omitempty"`
	Dead        bool       `json:"dead,omitempty"`
	// LastError is the handler's own words, so it can carry an upstream URL or
	// a fragment of a key. The service returns it; the handler blanks it for a
	// caller without store.operate.
	LastError string `json:"last_error,omitempty"`

	// What the order looked like afterwards, read off the event payload.
	Status        string       `json:"status,omitempty"`
	PaymentStatus string       `json:"payment_status,omitempty"`
	Tracking      string       `json:"tracking,omitempty"`
	Reason        string       `json:"reason,omitempty"`
	Change        *OrderChange `json:"change,omitempty"`

	// Which fields moved, from the audit row. Money keys are the same *_minor
	// integers the API uses. An absent Before means the previous value was not
	// recorded, never that it was empty.
	Before map[string]any `json:"before,omitempty"`
	After  map[string]any `json:"after,omitempty"`
}

// orderTimelineSource merges the two tables an order's history is spread across.
//
// The join is changes.event plus an identical created_at. Both rows are written
// by one InTx and both take now(), which is transaction_timestamp() and so is
// the same instant to the microsecond for every statement in that transaction —
// there is no other correlation, because outbox_events carries no audit id and
// admin_audit carries no event id. An audit row that claims an event supersedes
// it and carries its delivery fields along, so one moment renders once.
//
// Reading rows the dispatcher may be claiming needs no lock and takes none:
// this is a plain SELECT, so it never blocks a delivery pass and is never
// blocked by one. What it can see is a row mid-flight — attempts one behind,
// published_at not yet set — which is the honest answer a moment earlier and
// the only one a reader could have had anyway.
const orderTimelineSource = `
WITH ev AS (
	SELECT id, event_id, event_name, created_at, published_at, attempts, dead,
	       coalesce(last_error, '') AS last_error, payload
	FROM outbox_events
	WHERE aggregate_type = $1 AND aggregate_id = $2
), au AS (
	SELECT id, created_at, actor_kind, actor_email, actor_role, actor_label,
	       action, summary, changes, nullif(changes->>'event', '') AS event_name
	FROM admin_audit
	WHERE entity_type = $3 AND entity_id = $4
), merged AS (
	SELECT au.created_at AS at, 'action' AS kind, au.id AS row_id,
	       au.action, au.summary, au.actor_kind, au.actor_email, au.actor_role,
	       au.actor_label, au.changes, coalesce(au.event_name, '') AS event_name,
	       e.event_id, e.published_at, e.attempts, e.dead, e.last_error, e.payload
	FROM au
	LEFT JOIN LATERAL (
		SELECT * FROM ev
		WHERE ev.event_name = au.event_name AND ev.created_at = au.created_at
		ORDER BY ev.id LIMIT 1
	) e ON true
	UNION ALL
	SELECT ev.created_at, 'event', ev.id,
	       '', '', '', '', '', '', NULL::jsonb, ev.event_name,
	       ev.event_id, ev.published_at, ev.attempts, ev.dead, ev.last_error, ev.payload
	FROM ev
	WHERE NOT EXISTS (
		SELECT 1 FROM au
		WHERE au.event_name = ev.event_name AND au.created_at = ev.created_at
	)
)`

// Timeline is the order's own history, oldest first: what happened, who did it,
// and whether what it announced actually went out.
//
// Ordered by the instant and then by the row, which is a total order, so paging
// never repeats or skips an entry. Ascending because a history is read
// top-down.
func (s *Orders) Timeline(ctx context.Context, id int64, limit, offset int) ([]OrderTimelineEntry, int, error) {
	// An id no order has is a 404 rather than an empty history: the two mean
	// very different things to somebody asking what happened to an order, and
	// an order that genuinely has no entries is a real answer.
	var exists bool
	if err := s.app.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM orders WHERE id = $1)`, id).Scan(&exists); err != nil {
		return nil, 0, err
	}
	if !exists {
		return nil, 0, NotFoundf("order not found")
	}

	args := []any{AggregateOrder, id, AuditEntityOrder, strconv.FormatInt(id, 10)}

	var total int
	if err := s.app.db.QueryRowContext(ctx,
		orderTimelineSource+` SELECT count(*) FROM merged`, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	if limit <= 0 {
		limit = DefaultLimit
	}
	rows, err := s.app.db.QueryContext(ctx, orderTimelineSource+`
		SELECT at, kind, action, summary, actor_kind, actor_email, actor_role,
		       actor_label, changes, event_name, event_id, published_at,
		       attempts, dead, last_error, payload
		FROM merged ORDER BY at, kind, row_id LIMIT $5 OFFSET $6`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	// Never nil: an order with nothing recorded serializes as [], not null.
	out := []OrderTimelineEntry{}
	for rows.Next() {
		var e OrderTimelineEntry
		var changes, payload []byte
		var eventID, lastError sql.NullString
		var published sql.NullTime
		var attempts sql.NullInt64
		var dead sql.NullBool
		if err := rows.Scan(&e.At, &e.Kind, &e.Action, &e.Summary, &e.ActorKind,
			&e.ActorEmail, &e.ActorRole, &e.ActorLabel, &changes, &e.Name,
			&eventID, &published, &attempts, &dead, &lastError, &payload); err != nil {
			return nil, 0, err
		}
		// The event columns are NULL on an audit row that claimed nothing,
		// which is every act the taxonomy has no name for — a refund settled by
		// hand, a shipment corrected, a note.
		e.EventID, e.LastError = eventID.String, lastError.String
		e.Attempts, e.Dead = int(attempts.Int64), dead.Bool
		if published.Valid {
			at := published.Time
			e.PublishedAt = &at
		}
		if len(changes) > 0 {
			c, err := scanAuditChanges(changes)
			if err != nil {
				return nil, 0, err
			}
			e.Before, e.After = c.Before, c.After
		}
		// A payload that will not decode still yields its row, with what the
		// delivery columns say and no projection. A history with a hole in it
		// is worse than a history with a quiet entry in it.
		if len(payload) > 0 {
			var ev OrderEvent
			if json.Unmarshal(payload, &ev) == nil {
				e.Status, e.PaymentStatus = ev.Status, ev.PaymentStatus
				e.Tracking, e.Reason, e.Change = ev.Tracking, ev.Reason, ev.Change
			}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}
