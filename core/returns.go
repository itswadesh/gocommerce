package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Goods coming back.
//
// A return is a fact about goods, and it is deliberately not a fact about
// money: nothing here moves a penny. Refunding is [Payments.Refund], with its
// own right, its own ledger and its own event, and the two are recorded
// separately because they really are separate — a parcel arrives on Tuesday,
// somebody looks at it, and the money goes out later, or as store credit, or
// not at all. The engine already draws this line in words: EditLines reports a
// balance and refuses to settle it.
//
// Everything a return does is an order transition, so [Orders.Return] and
// [Orders.WithdrawReturn] are methods on *Orders in their own file rather than
// a service of their own: they take the order's row lock, they write an
// `order.*` event on the order aggregate, and they are authorised by
// orders.write. There is no port, no registry and nothing to configure, which
// is what Fulfillments has and what earned it a service of its own.
//
// Both run entirely inside [Orders.transition], which means the rows, the stock
// movement and the outbox event commit together (rule 4) and there is no
// network call anywhere in the path — so rule 5 holds structurally rather than
// by everybody remembering it.

// The two states of a return. Not a workflow: `received` is the fact the table
// exists to record, and `withdrawn` is that fact taken back.
const (
	ReturnReceived  = "received"
	ReturnWithdrawn = "withdrawn"
)

// ReturnInput is what came back.
type ReturnInput struct {
	// Reason is customer-visible: it rides order.returned out to the notifiers,
	// exactly as a cancellation reason does.
	Reason   string            `json:"reason"`
	Lines    []ReturnLineInput `json:"lines"`
	Metadata Metadata          `json:"metadata"`
}

// ReturnLineInput is how much of one order line came back, and what happened to
// it.
type ReturnLineInput struct {
	LineID   int64 `json:"line_id"`
	Quantity int   `json:"quantity"`
	// Restock absent means false: a client that forgets the field then
	// under-counts stock, which is the status quo and the safe direction to be
	// wrong in. Defaulting it to true would invent inventory that is not on a
	// shelf. The panel ticks the box, because a person is looking at the goods.
	Restock bool `json:"restock"`
	// LocationID is which shelf the units go back on. 0 means the one the line
	// was picked from, so a return lands where the sale came from.
	LocationID int64 `json:"location_id"`
}

// OrderReturn is one lot of goods that came back off an order.
//
// Units, RestockedUnits and Refundable are summed on the way out and never
// stored: they are sums over the lines below, and a stored copy of a sum is a
// number that can be wrong (M17).
type OrderReturn struct {
	ID             int64             `json:"id"`
	OrderID        int64             `json:"order_id"`
	Status         string            `json:"status"`
	Reason         string            `json:"reason,omitempty"`
	Lines          []OrderReturnLine `json:"lines"`
	Units          int               `json:"units"`
	RestockedUnits int               `json:"restocked_units"`
	// Refundable is what the goods were worth, not what was refunded. It is a
	// suggestion for whoever decides about the money, and never a settlement.
	Refundable Money     `json:"refundable"`
	Metadata   Metadata  `json:"metadata"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// OrderReturnLine is how much of one order line came back.
//
// The sku, title and label are joined in on the way out, for the reason
// FulfillmentLine does the same: the row stores an id and a count, and the
// operator holding the parcel needs to know which thing it is.
type OrderReturnLine struct {
	ID           int64  `json:"id"`
	OrderLineID  int64  `json:"order_line_id"`
	SKU          string `json:"sku"`
	Title        string `json:"title"`
	VariantLabel string `json:"variant_label,omitempty"`
	Quantity     int    `json:"quantity"`
	Restocked    bool   `json:"restocked"`
	// LocationID is the shelf the units actually went back on, absent when
	// nothing moved.
	LocationID *int64 `json:"location_id,omitempty"`
	Refundable Money  `json:"refundable"`
}

// Return records goods coming back off an order, and puts back on the shelf the
// ones the operator says are sellable.
//
// It moves no money. What the goods were worth is reported per line as a
// suggestion — see lineRefundable — and sending it back is a separate call with
// a separate right, because a return and a refund are separate decisions and a
// provider call cannot share a transaction with a stock movement (rule 5).
//
// The whole request is checked before anything moves, following EditLines' rule:
// a half-applied return is worse than a refused one.
func (s *Orders) Return(ctx context.Context, id int64, in ReturnInput) (*Order, *OrderReturn, error) {
	if len(in.Lines) == 0 {
		return nil, nil, Validationf("a return needs at least one line")
	}
	rec := &OrderReturn{}
	order, err := s.transition(ctx, id, func(ctx context.Context, tx *sql.Tx, o *Order) (transitionResult, error) {
		if err := returnableOrder(o); err != nil {
			return transitionResult{}, err
		}

		at := map[int64]int{}
		for i := range o.Lines {
			at[o.Lines[i].ID] = i
		}
		seen := map[int64]bool{}
		for _, l := range in.Lines {
			if l.Quantity <= 0 {
				return transitionResult{}, Validationf("a returned quantity must be at least one")
			}
			if _, ok := at[l.LineID]; !ok {
				return transitionResult{}, Validationf("order %s has no line %d", o.Number, l.LineID)
			}
			if seen[l.LineID] {
				return transitionResult{}, Validationf("line %d is named twice", l.LineID)
			}
			seen[l.LineID] = true
		}

		// What has actually left the store. On a partly shipped order that is
		// less than what was sold, and the units still in the stockroom cannot
		// come back from a customer who never had them — restocking those would
		// invent inventory, which is the disease this feature treats. A shipped
		// or delivered order has all of it out by definition, and asking the
		// parcels there would wrongly refuse an order imported without them.
		var shipped map[int64]int
		if o.Status == OrderPartial {
			var err error
			if shipped, err = shippedByLine(ctx, tx, o.ID); err != nil {
				return transitionResult{}, err
			}
		}

		// The two figures lockOrder does not select. Reading the row again here
		// is what EditLines already does for the discount; computing a tax share
		// from o.TaxInclusive instead would silently read false on every order,
		// which is the quietest wrong number this feature could produce.
		var discountMinor int64
		var taxInclusive bool
		if err := tx.QueryRowContext(ctx,
			`SELECT discount_minor, tax_inclusive FROM orders WHERE id = $1`, o.ID).
			Scan(&discountMinor, &taxInclusive); err != nil {
			return transitionResult{}, err
		}
		shares := orderDiscountShares(o, discountMinor)

		meta, err := in.Metadata.value()
		if err != nil {
			return transitionResult{}, Validationf("metadata is not valid JSON: %v", err)
		}
		rec.OrderID, rec.Status, rec.Reason = o.ID, ReturnReceived, in.Reason
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO order_returns (order_id, reason, metadata)
			VALUES ($1, $2, $3) RETURNING id, created_at, updated_at`,
			o.ID, in.Reason, meta).Scan(&rec.ID, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
			return transitionResult{}, err
		}
		if err := scanMetadata(meta, &rec.Metadata); err != nil {
			return transitionResult{}, err
		}

		for _, want := range in.Lines {
			line := o.Lines[at[want.LineID]]
			// A line whose variant has been deleted records that nothing moved
			// rather than refusing: the snapshot is still readable history, and
			// there is simply no shelf left to put it back on. The same doctrine
			// moveOrderStock follows.
			restock := want.Restock && line.VariantID != nil
			var shelf *int64
			if restock {
				loc, err := returnShelf(ctx, tx, line, want.LocationID)
				if err != nil {
					return transitionResult{}, err
				}
				shelf = &loc
			}
			refundable := lineRefundable(line, shares[at[want.LineID]], want.Quantity, taxInclusive)

			// What may still come back from this line: what was sold, or on a
			// partly shipped order what has gone out, whichever is less.
			left := line.Quantity
			if shipped != nil {
				left = shipped[line.ID]
			}

			// The cap is in the statement rather than in a read before it, which
			// is how reserveStock and sellStock enforce theirs: no window between
			// the check and the change. The order's FOR UPDATE makes that
			// belt-and-braces rather than load-bearing, which is the right order
			// of defences — and no CHECK could express this one, because it is a
			// sum across rows.
			var lineID int64
			err := tx.QueryRowContext(ctx, `
				INSERT INTO order_return_lines
				    (return_id, order_line_id, quantity, restocked, location_id, refundable_minor)
				SELECT $1, ol.id, $3::integer, $4, $5, $6
				FROM order_lines ol
				WHERE ol.id = $2 AND ol.order_id = $7
				  AND $3::integer <= least(ol.quantity, $8::integer) - coalesce((
				        SELECT sum(rl.quantity) FROM order_return_lines rl
				        JOIN order_returns r ON r.id = rl.return_id
				        WHERE rl.order_line_id = ol.id AND r.status = $9), 0)::integer
				RETURNING id`,
				rec.ID, line.ID, want.Quantity, restock, shelf, refundable,
				o.ID, left, ReturnReceived).Scan(&lineID)
			if errors.Is(err, sql.ErrNoRows) {
				return transitionResult{}, explainReturnRefusal(ctx, tx, o, line, want.Quantity, left)
			}
			if err != nil {
				return transitionResult{}, err
			}
			if restock {
				if err := restockStock(ctx, tx, *line.VariantID, *shelf, want.Quantity); err != nil {
					return transitionResult{}, err
				}
			}
			rec.Lines = append(rec.Lines, OrderReturnLine{
				ID: lineID, OrderLineID: line.ID, SKU: line.SKU, Title: line.Title,
				VariantLabel: line.VariantLabel, Quantity: want.Quantity,
				Restocked: restock, LocationID: shelf,
				Refundable: money(refundable, o.Currency),
			})
		}
		rec.total(o.Currency)

		// The order's status and payment_status are untouched, which is the
		// point: `delivered` stays true, because the parcel really did arrive.
		// Whether goods have come back since is the return's own fact.
		summary := fmt.Sprintf("Recorded a return of %s on order %s",
			pluralUnits(rec.Units), o.Number)
		if rec.RestockedUnits > 0 {
			summary += fmt.Sprintf(", %d back on the shelf", rec.RestockedUnits)
		}
		if in.Reason != "" {
			summary += " — " + in.Reason
		}
		return transitionResult{
			Event: EventOrderReturned, Payload: s.returnPayload(o, rec),
			Action: AuditOrderReturn, Summary: summary,
			// Minor units and a currency, never a formatted amount (rule 6).
			After: map[string]any{
				"return_id":        rec.ID,
				"units":            rec.Units,
				"restocked_units":  rec.RestockedUnits,
				"refundable_minor": rec.Refundable.AmountMinor,
				"currency":         o.Currency,
				"reason":           in.Reason,
			},
		}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	return order, rec, nil
}

// WithdrawReturn takes back a return recorded in error: the row survives with
// status `withdrawn`, and the units it put on the shelf come off again.
//
// A withdrawal rather than a delete, because how much is still returnable is
// counted from these rows and a return whose row is gone is a stock movement
// nobody can account for. Keeping the row is also what makes this idempotent —
// a second withdrawal is a silent no-op, the house rule for a transition with
// nothing left to do — and it preserves the returned-then-withdrawn history the
// panel shows.
//
// The units come off through sellStock rather than a bare decrement: it refuses,
// in the same statement and under the same lock, to push on_hand below what
// other orders have reserved.
func (s *Orders) WithdrawReturn(ctx context.Context, orderID, returnID int64) (*Order, error) {
	return s.transition(ctx, orderID, func(ctx context.Context, tx *sql.Tx, o *Order) (transitionResult, error) {
		var status string
		err := tx.QueryRowContext(ctx,
			`SELECT status FROM order_returns WHERE id = $1 AND order_id = $2 FOR UPDATE`,
			returnID, o.ID).Scan(&status)
		// A return belonging to another order is reported as not found rather
		// than as a mismatch: which order holds which return is not something to
		// be learned by probing this route.
		if errors.Is(err, sql.ErrNoRows) {
			return transitionResult{}, NotFoundf("order %s has no return %d", o.Number, returnID)
		}
		if err != nil {
			return transitionResult{}, err
		}
		if status == ReturnWithdrawn {
			return transitionResult{}, nil
		}

		type putBack struct {
			variantID  int64
			locationID int64
			quantity   int
			sku        string
		}
		var back []putBack
		rows, err := tx.QueryContext(ctx, `
			SELECT rl.quantity, rl.location_id, ol.variant_id, ol.sku
			FROM order_return_lines rl
			JOIN order_lines ol ON ol.id = rl.order_line_id
			WHERE rl.return_id = $1 AND rl.restocked
			ORDER BY rl.id`, returnID)
		if err != nil {
			return transitionResult{}, err
		}
		for rows.Next() {
			var qty int
			var loc, variant sql.NullInt64
			var sku string
			if err := rows.Scan(&qty, &loc, &variant, &sku); err != nil {
				rows.Close()
				return transitionResult{}, err
			}
			// variant_stock cascades when a variant is deleted, so there is no
			// row left to take the units off; location_id is NULL for the same
			// kind of reason, a shelf closed since. Both are skipped rather than
			// refused — the record of what came back still stands, and the
			// engine cannot un-restock units that have nowhere to come from.
			if !variant.Valid || !loc.Valid {
				continue
			}
			back = append(back, putBack{variant.Int64, loc.Int64, qty, sku})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return transitionResult{}, err
		}

		for _, b := range back {
			if err := sellStock(ctx, tx, b.variantID, b.locationID, b.quantity); err != nil {
				// The bare sentinel must not escape as a 500, and the operator
				// needs a next step rather than a refusal.
				if errors.Is(err, errInsufficientStock) {
					return transitionResult{}, Conflictf(
						"the %d unit(s) of %s this return put back have since gone out again; "+
							"adjust the stock instead of withdrawing the record", b.quantity, b.sku)
				}
				return transitionResult{}, err
			}
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE order_returns SET status = $2, updated_at = now() WHERE id = $1`,
			returnID, ReturnWithdrawn); err != nil {
			return transitionResult{}, err
		}

		// Read back rather than built from what was intended, so a consumer can
		// reverse exactly what order.returned told it to do.
		rec, err := loadReturnTx(ctx, tx, o, returnID)
		if err != nil {
			return transitionResult{}, err
		}
		return transitionResult{
			Event: EventOrderUnreturned, Payload: s.returnPayload(o, rec),
			Action: AuditOrderReturnWithdraw,
			Summary: fmt.Sprintf("Withdrew the return of %s on order %s",
				pluralUnits(rec.Units), o.Number),
			After: map[string]any{
				"return_id":       rec.ID,
				"units":           rec.Units,
				"lines_taken_off": len(back),
			},
		}, nil
	})
}

// returnableOrder says which orders goods can come back from, and it is the
// exact complement of the two refusals that already name this operation:
// Cancel's "cancelling it is a return, not a cancellation" and EditLines'
// "changing what is in it is a return, not an edit". Every order state therefore
// has exactly one operation that moves its stock.
//
// There is deliberately no payment guard: a delivered cash-on-delivery order
// that was never paid for can still have goods come back, because a return is a
// fact about goods.
func returnableOrder(o *Order) error {
	switch o.Status {
	// Partial belongs here: units have left the store, and what went out can
	// come back. What may come back is then capped at what actually shipped
	// rather than at what was sold.
	case OrderPartial, OrderShipped, OrderDelivered:
		return nil
	case OrderPending, OrderConfirmed:
		return Conflictf(
			"order %s has not shipped, so nothing has come back from it; "+
				"change what is on it or cancel it instead", o.Number)
	case OrderCancelled:
		return Conflictf("order %s was cancelled; nothing went out to come back", o.Number)
	default:
		return Conflictf("order %s is %s; nothing can be returned against it", o.Number, o.Status)
	}
}

// hasActiveReturn is what Cancel and EditLines ask before they move stock an
// order has already had back. Those two callers are the whole of its purpose.
func hasActiveReturn(ctx context.Context, tx *sql.Tx, orderID int64) (bool, error) {
	var yes bool
	err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM order_returns WHERE order_id = $1 AND status = $2)`,
		orderID, ReturnReceived).Scan(&yes)
	return yes, err
}

// returnShelf is where the units go back.
//
// Zero means the shelf the line was picked from, so a return lands where the
// sale came from — lineLocation, which also makes sure the stock row exists, so
// that a movement cannot silently match nothing.
//
// A named location is checked for being active, which resolveLocation does not
// do: it verifies existence only, and quietly falling back to the default for a
// shelf the operator explicitly named would put goods somewhere nobody said.
func returnShelf(ctx context.Context, tx *sql.Tx, line OrderLine, locationID int64) (int64, error) {
	if locationID == 0 {
		return lineLocation(ctx, tx, line)
	}
	var active bool
	err := tx.QueryRowContext(ctx,
		`SELECT active FROM locations WHERE id = $1`, locationID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, NotFoundf("location %d does not exist", locationID)
	}
	if err != nil {
		return 0, err
	}
	if !active {
		return 0, Conflictf("location %d is closed; returning stock into it would hide the units", locationID)
	}
	if line.VariantID == nil {
		return locationID, nil
	}
	return locationID, ensureStockRow(ctx, tx, *line.VariantID, locationID)
}

// orderDiscountShares splits the order-level discount across the lines it came
// off, index-aligned with o.Lines.
//
// It reuses allocateDiscount — the same largest-remainder split checkout used to
// reach the total, which is what makes the parts sum to the whole — and the
// synthesised slice is enough because that function reads only Total.
func orderDiscountShares(o *Order, discountMinor int64) []int64 {
	lines := make([]taxableLine, len(o.Lines))
	for i, l := range o.Lines {
		lines[i] = taxableLine{Total: l.Total.AmountMinor}
	}
	return allocateDiscount(lines, discountMinor)
}

// lineRefundable is what these units were worth: what was charged for them, less
// their share of the order-level discount, plus their share of the line's tax
// when prices are exclusive — which is the fork checkout itself makes.
//
// Both shares are prorated by quantity and round down, so the figure is exact
// when a whole line comes back and can under-state a partial return by a minor
// unit; the remainder stays with the store rather than being invented for the
// customer. It is snapshotted at record time rather than derived on read,
// because the discount behind it is editable and last year's return has to keep
// saying what happened.
func lineRefundable(l OrderLine, discountShare int64, qty int, taxInclusive bool) int64 {
	if l.Quantity <= 0 {
		return 0
	}
	gross := l.UnitPrice.AmountMinor * int64(qty)
	share := discountShare * int64(qty) / int64(l.Quantity)
	tax := int64(0)
	if !taxInclusive {
		tax = l.Tax.AmountMinor * int64(qty) / int64(l.Quantity)
	}
	// Non-negative by construction — allocateDiscount caps a line's share at its
	// own total and gross scales identically — and floored anyway, because the
	// column carries a CHECK and a raw constraint error tells an operator
	// nothing.
	if v := gross - share + tax; v > 0 {
		return v
	}
	return 0
}

// explainReturnRefusal turns "the statement matched no rows" into a sentence
// naming the reason, the way explainStockFailure does for a stock movement.
func explainReturnRefusal(ctx context.Context, tx *sql.Tx, o *Order, line OrderLine, want, left int) error {
	var already int
	if err := tx.QueryRowContext(ctx, `
		SELECT coalesce(sum(rl.quantity), 0)::int
		FROM order_return_lines rl
		JOIN order_returns r ON r.id = rl.return_id
		WHERE rl.order_line_id = $1 AND r.status = $2`,
		line.ID, ReturnReceived).Scan(&already); err != nil {
		return err
	}
	if left < line.Quantity {
		return Validationf(
			"only %d of %s has gone out on order %s and %d of it has already come back, so %d cannot",
			left, line.SKU, o.Number, already, want)
	}
	return Validationf(
		"order %s sold %d of %s and %d has already come back, so %d cannot",
		o.Number, line.Quantity, line.SKU, already, want)
}

// returnPayload keeps the exact OrderEvent shape, because subscribeNotifications
// subscribes the literal pattern order.* and decodes every match as one: a
// differently shaped payload under that prefix would fail to decode, error the
// handler, retry and dead-letter.
func (s *Orders) returnPayload(o *Order, rec *OrderReturn) *OrderEvent {
	ev := s.eventPayload(o)
	// The same field order.cancelled and order.refunded already use for the same
	// question, so a notifier keyed on data["reason"] works with no change.
	ev.Reason = rec.Reason
	ret := &ReturnEvent{
		ReturnID: rec.ID, Status: rec.Status, Reason: rec.Reason,
		Units: rec.Units, RestockedUnits: rec.RestockedUnits,
		RefundableMinor: rec.Refundable.AmountMinor,
	}
	for _, l := range rec.Lines {
		ret.Lines = append(ret.Lines, ReturnEventLine{
			SKU: l.SKU, Title: l.Title, Quantity: l.Quantity,
			Restocked: l.Restocked, RefundableMinor: l.Refundable.AmountMinor,
		})
	}
	ev.Return = ret
	return ev
}

// total fills in the three figures that are sums over the lines, and is the only
// place they are ever computed.
func (r *OrderReturn) total(currency string) {
	if r.Lines == nil {
		r.Lines = []OrderReturnLine{}
	}
	r.Units, r.RestockedUnits = 0, 0
	var refundable int64
	for _, l := range r.Lines {
		r.Units += l.Quantity
		if l.Restocked {
			r.RestockedUnits += l.Quantity
		}
		refundable += l.Refundable.AmountMinor
	}
	r.Refundable = money(refundable, currency)
}

// pluralUnits reads the way an operator would say it, for an audit summary.
func pluralUnits(n int) string {
	if n == 1 {
		return "1 unit"
	}
	return strconv.Itoa(n) + " units"
}

// loadReturnTx reads one return inside the transaction that just changed it, so
// the event describes what was committed rather than what was intended.
func loadReturnTx(ctx context.Context, tx *sql.Tx, o *Order, returnID int64) (*OrderReturn, error) {
	rec := &OrderReturn{ID: returnID, OrderID: o.ID}
	var meta []byte
	if err := tx.QueryRowContext(ctx, `
		SELECT status, reason, metadata, created_at, updated_at
		FROM order_returns WHERE id = $1`, returnID).
		Scan(&rec.Status, &rec.Reason, &meta, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		return nil, err
	}
	if err := scanMetadata(meta, &rec.Metadata); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT rl.id, rl.order_line_id, ol.sku, ol.title, ol.variant_label,
		       rl.quantity, rl.restocked, rl.location_id, rl.refundable_minor
		FROM order_return_lines rl
		JOIN order_lines ol ON ol.id = rl.order_line_id
		WHERE rl.return_id = $1 ORDER BY rl.id`, returnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var l OrderReturnLine
		var loc sql.NullInt64
		var refundable int64
		if err := rows.Scan(&l.ID, &l.OrderLineID, &l.SKU, &l.Title, &l.VariantLabel,
			&l.Quantity, &l.Restocked, &loc, &refundable); err != nil {
			return nil, err
		}
		if loc.Valid {
			l.LocationID = &loc.Int64
		}
		l.Refundable = money(refundable, o.Currency)
		rec.Lines = append(rec.Lines, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rec.total(o.Currency)
	return rec, nil
}

// loadOrderReturns attaches what has come back to a page of orders, in the two
// queries loadChildren already spends on fulfillments.
//
// The second result set does double duty: it also fills in each order line's
// ReturnedQuantity, so the panel can show what is still returnable without a
// third query and without re-deriving a figure the engine would then have to
// agree with.
func (s *Orders) loadOrderReturns(ctx context.Context, byID map[int64]*Order, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.app.db.QueryContext(ctx, `
		SELECT id, order_id, status, reason, metadata, created_at, updated_at
		FROM order_returns WHERE order_id = ANY($1::bigint[]) ORDER BY order_id, id`,
		int64Array(ids))
	if err != nil {
		return err
	}
	defer rows.Close()

	// Where each return landed, so its lines can be attached by one more query
	// for the whole page rather than one per return.
	owner, at := map[int64]*Order{}, map[int64]int{}
	for rows.Next() {
		var r OrderReturn
		var meta []byte
		if err := rows.Scan(&r.ID, &r.OrderID, &r.Status, &r.Reason, &meta,
			&r.CreatedAt, &r.UpdatedAt); err != nil {
			return err
		}
		if err := scanMetadata(meta, &r.Metadata); err != nil {
			return err
		}
		o := byID[r.OrderID]
		if o == nil {
			continue
		}
		r.Lines = []OrderReturnLine{}
		r.Refundable = money(0, o.Currency)
		owner[r.ID], at[r.ID] = o, len(o.Returns)
		o.Returns = append(o.Returns, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(owner) == 0 {
		return nil
	}

	// An order line id is unique across orders, so one map serves the page.
	lineByID := map[int64]*OrderLine{}
	for _, o := range byID {
		for i := range o.Lines {
			lineByID[o.Lines[i].ID] = &o.Lines[i]
		}
	}

	lineRows, err := s.app.db.QueryContext(ctx, `
		SELECT rl.id, rl.return_id, rl.order_line_id, ol.sku, ol.title, ol.variant_label,
		       rl.quantity, rl.restocked, rl.location_id, rl.refundable_minor
		FROM order_return_lines rl
		JOIN order_returns r ON r.id = rl.return_id
		JOIN order_lines ol ON ol.id = rl.order_line_id
		WHERE r.order_id = ANY($1::bigint[]) ORDER BY rl.id`, int64Array(ids))
	if err != nil {
		return err
	}
	defer lineRows.Close()
	for lineRows.Next() {
		var returnID int64
		var l OrderReturnLine
		var loc sql.NullInt64
		var refundable int64
		if err := lineRows.Scan(&l.ID, &returnID, &l.OrderLineID, &l.SKU, &l.Title,
			&l.VariantLabel, &l.Quantity, &l.Restocked, &loc, &refundable); err != nil {
			return err
		}
		o := owner[returnID]
		if o == nil {
			continue
		}
		if loc.Valid {
			l.LocationID = &loc.Int64
		}
		l.Refundable = money(refundable, o.Currency)
		r := &o.Returns[at[returnID]]
		r.Lines = append(r.Lines, l)
		// Only a return that still stands counts against what may come back,
		// which is the filter the cap in Return uses.
		if r.Status == ReturnReceived {
			if ol := lineByID[l.OrderLineID]; ol != nil {
				ol.ReturnedQuantity += l.Quantity
			}
		}
	}
	if err := lineRows.Err(); err != nil {
		return err
	}
	for _, o := range byID {
		for i := range o.Returns {
			o.Returns[i].total(o.Currency)
		}
	}
	return nil
}
