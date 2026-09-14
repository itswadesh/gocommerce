package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Fulfillments books shipments. The engine keeps the order's state machine and
// its events; a provider only talks to a carrier and reports what came back.
type Fulfillments struct {
	app       *App
	providers map[string]FulfillmentProvider
}

// Ship returns the fulfillment service.
func (a *App) Ship() *Fulfillments { return a.fulfillment }

// Providers lists the installed fulfillment codes.
func (f *Fulfillments) Providers() []string {
	codes := make([]string, 0, len(f.providers))
	for code := range f.providers {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

// Create books a shipment and lets the order's status follow what has gone out:
// all of it is shipped, some of it is partial.
//
// The carrier call happens before the transaction opens, never inside it: a
// carrier API having a bad minute must not hold write locks on the orders
// table. The transaction then re-checks the order's state, because between the
// two the order could have been cancelled by someone else — or another parcel
// could have taken the units this one was booked for.
//
// What goes in the parcel is resolved before the provider is called, so a
// carrier module declares the box's real contents rather than the whole
// order's.
func (f *Fulfillments) Create(ctx context.Context, orderID int64, providerCode string, req ShipRequest) (*Order, error) {
	if providerCode == "" {
		providerCode = ProviderManual
	}
	provider, ok := f.providers[providerCode]
	if !ok {
		return nil, NotFoundf("no fulfillment provider named %q", providerCode)
	}

	order, err := f.app.orders.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if err := shippableOrder(order); err != nil {
		return nil, err
	}
	// Resolved here rather than inside the transaction so the provider is told
	// what it is booking. Asking for more than the order owes is a Validation
	// at this point; losing the race for those units under the lock below is a
	// Conflict, because by then a label exists.
	lines, err := resolveShipLines(order, req.Lines)
	if err != nil {
		return nil, err
	}
	req.Lines = lines

	// What the parcel physically is, worked out once here rather than guessed
	// separately by every carrier module.
	parcel, err := f.parcelFor(ctx, order, lines)
	if err != nil {
		return nil, err
	}
	req.Parcel = parcel

	shipment, err := provider.Ship(ctx, order, req)
	if err != nil {
		return nil, Internalf(err, "%s could not create the shipment", providerCode)
	}
	if shipment.Provider == "" {
		shipment.Provider = providerCode
	}

	return f.app.orders.transition(ctx, orderID, func(ctx context.Context, tx *sql.Tx, o *Order) (transitionResult, error) {
		// Re-check under the row lock: the world may have moved while the
		// carrier was thinking.
		if err := shippableOrder(o); err != nil {
			return transitionResult{}, err
		}
		shipped, err := shippedByLine(ctx, tx, o.ID)
		if err != nil {
			return transitionResult{}, err
		}
		if err := checkShipLines(o, shipped, lines); err != nil {
			return transitionResult{}, err
		}
		meta, err := json.Marshal(req.Meta)
		if err != nil {
			return transitionResult{}, err
		}
		// Who is carrying it, worked out from the number, unless the provider
		// already knows — an integration that booked the shipment has been told
		// by the carrier and does not have to guess.
		carrier := shipment.Carrier
		if carrier == "" {
			if c, ok := DetectCarrier(shipment.Tracking); ok {
				carrier = c.Code
			}
		}
		// The fulfillment's own status stays the literal 'shipped'. That is the
		// parcel's state, which is a different fact from the order's.
		var fulfillmentID int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO fulfillments (order_id, provider, tracking, carrier, label_url, status, metadata)
			VALUES ($1, $2, $3, $4, $5, 'shipped', $6)
			RETURNING id`,
			o.ID, shipment.Provider, shipment.Tracking, carrier, shipment.LabelURL, meta).
			Scan(&fulfillmentID); err != nil {
			return transitionResult{}, err
		}
		lineIDs := make([]int64, len(lines))
		quantities := make([]int64, len(lines))
		units := 0
		for i, l := range lines {
			lineIDs[i], quantities[i] = l.OrderLineID, int64(l.Quantity)
			units += l.Quantity
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO fulfillment_lines (fulfillment_id, order_line_id, quantity)
			SELECT $1, unnest($2::bigint[]), unnest($3::bigint[])::integer`,
			fulfillmentID, int64Array(lineIDs), int64Array(quantities)); err != nil {
			return transitionResult{}, err
		}
		remaining, err := settleOrderShipping(ctx, tx, o)
		if err != nil {
			return transitionResult{}, err
		}

		payload := f.app.orders.eventPayload(o)
		payload.Tracking = shipment.Tracking
		payload.Extra = map[string]string{"provider": shipment.Provider}
		if shipment.LabelURL != "" {
			payload.Extra["label_url"] = shipment.LabelURL
		}
		payload.Shipment = &ShipmentEvent{
			FulfillmentID: fulfillmentID, Carrier: carrier,
			RemainingUnits: remaining, Lines: shipmentEventLines(o, lines),
		}
		summary := "Shipped order " + o.Number + " — " + shipment.Provider + " " + shipment.Tracking
		if remaining > 0 {
			summary = fmt.Sprintf("Shipped %d of order %s's units, %d still owed — %s %s",
				units, o.Number, remaining, shipment.Provider, shipment.Tracking)
		}
		return transitionResult{
			Event: EventOrderShipped, Payload: payload,
			Action:  AuditOrderShip,
			Summary: summary,
			After: map[string]any{
				"provider":  shipment.Provider,
				"tracking":  shipment.Tracking,
				"carrier":   carrier,
				"label_url": shipment.LabelURL,
				"units":     units,
			},
		}, nil
	})
}

// FulfillmentPatch changes what was recorded about a shipment.
//
// Both fields are pointers because "" is a real value for each: clearing a
// tracking number that was typed against the wrong order is exactly the
// correction this exists for.
type FulfillmentPatch struct {
	Tracking *string `json:"tracking"`
	Carrier  *string `json:"carrier"`
}

// Update corrects a shipment's tracking number, and with it the carrier.
//
// Typing a tracking number is the one step of shipping a parcel that nobody
// else checks, so it is the one that gets mistyped, and until now the only fix
// was a row in the database. It does not touch the order's state: the parcel
// left either way, and correcting the number is not un-shipping it.
//
// Changing the number re-identifies the carrier, because the old one described
// the old number. An explicit carrier in the same patch wins — the operator can
// see the parcel and the engine is pattern-matching.
func (f *Fulfillments) Update(ctx context.Context, id int64, patch FulfillmentPatch) (*Fulfillment, error) {
	if patch.Tracking == nil && patch.Carrier == nil {
		return nil, Validationf("nothing to change")
	}
	if patch.Carrier != nil && *patch.Carrier != "" {
		if _, ok := CarrierByCode(*patch.Carrier, ""); !ok {
			return nil, Validationf("no carrier named %q", *patch.Carrier)
		}
	}

	var out Fulfillment
	err := InTx(ctx, f.app.db, func(tx *sql.Tx) error {
		var current Fulfillment
		var meta []byte
		if err := tx.QueryRowContext(ctx, `
			SELECT id, provider, tracking, carrier, label_url, status, metadata, created_at
			FROM fulfillments WHERE id = $1 FOR UPDATE`, id).Scan(
			&current.ID, &current.Provider, &current.Tracking, &current.Carrier,
			&current.LabelURL, &current.Status, &meta, &current.CreatedAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return NotFoundf("fulfillment not found")
			}
			return err
		}

		tracking, carrier := current.Tracking, current.Carrier
		if patch.Tracking != nil {
			tracking = strings.TrimSpace(*patch.Tracking)
			if tracking != current.Tracking {
				// The stored carrier described the old number. Re-read it, and
				// leave it empty when the new number identifies nobody rather
				// than keeping an answer to a question that has changed.
				carrier = ""
				if c, ok := DetectCarrier(tracking); ok {
					carrier = c.Code
				}
			}
		}
		if patch.Carrier != nil {
			carrier = *patch.Carrier
		}

		if err := tx.QueryRowContext(ctx, `
			UPDATE fulfillments SET tracking = $2, carrier = $3, updated_at = now()
			WHERE id = $1
			RETURNING id, provider, tracking, carrier, label_url, status, metadata, created_at`,
			id, tracking, carrier).Scan(
			&out.ID, &out.Provider, &out.Tracking, &out.Carrier,
			&out.LabelURL, &out.Status, &meta, &out.CreatedAt); err != nil {
			return err
		}
		return scanMetadata(meta, &out.Metadata)
	})
	if err != nil {
		return nil, err
	}
	out.decorate()
	return &out, nil
}

// ------------------------------------------------------------- the parcel

// orderLinesByID indexes an order's lines for the three places below that have
// to answer "which line is this id".
func orderLinesByID(o *Order) map[int64]*OrderLine {
	byID := make(map[int64]*OrderLine, len(o.Lines))
	for i := range o.Lines {
		byID[o.Lines[i].ID] = &o.Lines[i]
	}
	return byID
}

// resolveShipLines turns what was asked for into what will be written.
//
// It is pure because Create reads the order through orders.Get, which fills
// ShippedQuantity — so the pre-flight check needs no database, and no interface
// over both *sql.DB and *sql.Tx has to be invented to share code with the
// re-check that runs under the row lock.
//
// An empty request means everything the order still owes, which on a first
// shipment is the whole order: that is what keeps a client written before
// parcels existed shipping whole orders with the body it always sent.
func resolveShipLines(o *Order, want []ShipLine) ([]ShipLine, error) {
	var out []ShipLine
	if len(want) == 0 {
		for _, l := range o.Lines {
			if remaining := l.Quantity - l.ShippedQuantity; remaining > 0 {
				out = append(out, ShipLine{OrderLineID: l.ID, Quantity: remaining})
			}
		}
	} else {
		byID := orderLinesByID(o)
		// The whole request is checked before any of it is accepted, the same
		// discipline EditLines states: a half-taken parcel is worse than a
		// refused one.
		seen := map[int64]bool{}
		for _, w := range want {
			l, ok := byID[w.OrderLineID]
			if !ok {
				return nil, Validationf("order %s has no line %d", o.Number, w.OrderLineID)
			}
			if seen[w.OrderLineID] {
				return nil, Validationf("line %d is named twice", w.OrderLineID)
			}
			seen[w.OrderLineID] = true
			if w.Quantity < 1 {
				return nil, Validationf(
					"a shipped quantity must be at least 1; leave a line out to hold it back")
			}
			if remaining := l.Quantity - l.ShippedQuantity; w.Quantity > remaining {
				return nil, Validationf("%s has %d left to ship, not %d", l.SKU, remaining, w.Quantity)
			}
			out = append(out, ShipLine{OrderLineID: l.ID, Quantity: w.Quantity})
		}
	}
	if len(out) == 0 {
		return nil, Conflictf("order %s has nothing left to ship", o.Number)
	}
	return out, nil
}

// shippedByLine is how many units of each order line have already gone out,
// read under the caller's lock. A cancelled shipment's units are owed again,
// which is the same rule Delete has always counted by.
func shippedByLine(ctx context.Context, tx *sql.Tx, orderID int64) (map[int64]int, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT fl.order_line_id, sum(fl.quantity)::int
		FROM fulfillment_lines fl
		JOIN fulfillments f ON f.id = fl.fulfillment_id
		WHERE f.order_id = $1 AND f.status <> 'cancelled'
		GROUP BY fl.order_line_id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var lineID int64
		var qty int
		if err := rows.Scan(&lineID, &qty); err != nil {
			return nil, err
		}
		out[lineID] = qty
	}
	return out, rows.Err()
}

// checkShipLines re-checks a resolved parcel against the locked order.
//
// It refuses rather than re-resolving: provider.Ship has already booked a
// parcel with these exact contents, and quietly recording a smaller one would
// leave the engine and the carrier disagreeing with nobody told.
func checkShipLines(o *Order, shipped map[int64]int, lines []ShipLine) error {
	byID := orderLinesByID(o)
	for _, s := range lines {
		l, ok := byID[s.OrderLineID]
		if !ok {
			return Conflictf(
				"order %s no longer has the line this parcel was booked against", o.Number)
		}
		if remaining := l.Quantity - shipped[s.OrderLineID]; s.Quantity > remaining {
			return Conflictf("another shipment took those units while this one was being booked; "+
				"%s has %d left, not %d — a label may already exist for it",
				l.SKU, remaining, s.Quantity)
		}
	}
	return nil
}

// shippingState is what the parcels say the order's status should be, and how
// many units it still owes.
func shippingState(ctx context.Context, tx *sql.Tx, orderID int64) (want string, remaining int, err error) {
	var ordered, shipped int
	if err := tx.QueryRowContext(ctx, `
		SELECT coalesce(sum(ol.quantity), 0)::int, coalesce(sum(s.shipped), 0)::int
		FROM order_lines ol
		LEFT JOIN (
		    SELECT fl.order_line_id, sum(fl.quantity) AS shipped
		    FROM fulfillment_lines fl
		    JOIN fulfillments f ON f.id = fl.fulfillment_id
		    WHERE f.status <> 'cancelled'
		    GROUP BY fl.order_line_id
		) s ON s.order_line_id = ol.id
		WHERE ol.order_id = $1`, orderID).Scan(&ordered, &shipped); err != nil {
		return "", 0, err
	}
	switch {
	case shipped == 0:
		want = OrderConfirmed
	case ordered > 0 && shipped >= ordered:
		want = OrderShipped
	default:
		want = OrderPartial
	}
	if remaining = ordered - shipped; remaining < 0 {
		remaining = 0
	}
	return want, remaining, nil
}

// settleOrderShipping writes what shippingState decided, and only when it
// differs from what the order already says.
//
// This is the only place the shipping axis of orders.status is written, which
// is what stops the status and the parcels disagreeing. MarkDelivered and
// MarkUndelivered are the deliberate exceptions: delivery is a fact somebody
// observed, and an order imported as delivered has no parcels behind it at all.
func settleOrderShipping(ctx context.Context, tx *sql.Tx, o *Order) (int, error) {
	want, remaining, err := shippingState(ctx, tx, o.ID)
	if err != nil {
		return 0, err
	}
	if want != o.Status {
		if err := setOrderStatus(ctx, tx, o.ID, want); err != nil {
			return 0, err
		}
		o.Status = want
	}
	return remaining, nil
}

// shipmentEventLines names a parcel's contents for the event, joining the
// resolved lines to the ones the transition already loaded.
func shipmentEventLines(o *Order, lines []ShipLine) []ShipmentEventLine {
	byID := orderLinesByID(o)
	out := make([]ShipmentEventLine, 0, len(lines))
	for _, s := range lines {
		l, ok := byID[s.OrderLineID]
		if !ok {
			continue
		}
		out = append(out, ShipmentEventLine{
			SKU: l.SKU, Title: l.Title, VariantLabel: l.VariantLabel, Quantity: s.Quantity,
		})
	}
	return out
}

// parcelContents is what one shipment held, read for the event that says it
// came back. A parcel recorded before M22 that the backfill could not credit
// has no rows and reads as an empty one, which is what it can honestly say.
func parcelContents(ctx context.Context, tx *sql.Tx, fulfillmentID int64) ([]ShipmentEventLine, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT ol.sku, ol.title, ol.variant_label, fl.quantity
		FROM fulfillment_lines fl
		JOIN order_lines ol ON ol.id = fl.order_line_id
		WHERE fl.fulfillment_id = $1
		ORDER BY fl.order_line_id`, fulfillmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ShipmentEventLine{}
	for rows.Next() {
		var l ShipmentEventLine
		if err := rows.Scan(&l.SKU, &l.Title, &l.VariantLabel, &l.Quantity); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func shippableOrder(o *Order) error {
	switch o.Status {
	// Partial ships again: the remainder is exactly what it still owes.
	case OrderConfirmed, OrderPartial:
		return nil
	case OrderPending:
		return Conflictf("order %s is not confirmed yet, so it cannot ship", o.Number)
	case OrderShipped, OrderDelivered:
		return Conflictf("order %s has already shipped in full", o.Number)
	default:
		return Conflictf("order %s is %s and cannot ship", o.Number, o.Status)
	}
}

// ------------------------------------------------------------------ manual

// ProviderManual is the built-in fulfillment method: an operator packs the box
// themselves and types in the tracking number. It is in core because it needs
// no third party, and because a store must be able to ship before it has
// integrated a carrier.
const ProviderManual = "manual"

type manualFulfillment struct{}

func (manualFulfillment) Code() string { return ProviderManual }

func (manualFulfillment) DisplayName() string { return "Manual" }

func (manualFulfillment) Ship(ctx context.Context, o *Order, req ShipRequest) (Shipment, error) {
	// The carrier travels through: an operator holding the parcel is a better
	// source than a pattern, and an empty one still leaves the engine to read
	// it off the number.
	return Shipment{Provider: ProviderManual, Tracking: req.Tracking, Carrier: req.Carrier}, nil
}

// Delete removes a shipment recorded in error.
//
// The order follows it. "Shipped" was true because those parcels said so, so
// the status is re-derived from the ones still there rather than counted:
// removing one of two lands the order on partial, and removing the last puts it
// back to confirmed. Leaving it shipped with nothing shipping it would be a
// state no operation could explain and no operator could correct.
//
// It refuses on a delivered order. A parcel somebody received is not a record
// to erase, and undoing the delivery first is the operation that says so.
func (f *Fulfillments) Delete(ctx context.Context, id int64) (*Order, error) {
	var orderID int64
	if err := f.app.db.QueryRowContext(ctx,
		`SELECT order_id FROM fulfillments WHERE id = $1`, id).Scan(&orderID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, NotFoundf("fulfillment not found")
		}
		return nil, err
	}

	return f.app.orders.transition(ctx, orderID, func(ctx context.Context, tx *sql.Tx, o *Order) (transitionResult, error) {
		if o.Status == OrderDelivered {
			return transitionResult{}, Conflictf(
				"order %s is delivered; undo the delivery before removing what shipped it", o.Number)
		}

		// Read before the delete, because the rows go with it and the event has
		// to be able to say which parcel came back.
		var carrier string
		if err := tx.QueryRowContext(ctx,
			`SELECT carrier FROM fulfillments WHERE id = $1`, id).Scan(&carrier); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return transitionResult{}, NotFoundf("fulfillment not found")
			}
			return transitionResult{}, err
		}
		held, err := parcelContents(ctx, tx, id)
		if err != nil {
			return transitionResult{}, err
		}

		res, err := tx.ExecContext(ctx, `DELETE FROM fulfillments WHERE id = $1`, id)
		if err != nil {
			return transitionResult{}, err
		}
		if n, err := res.RowsAffected(); err != nil {
			return transitionResult{}, err
		} else if n == 0 {
			// Deleted by somebody else between the read and the lock.
			return transitionResult{}, NotFoundf("fulfillment not found")
		}

		want, remaining, err := shippingState(ctx, tx, o.ID)
		if err != nil {
			return transitionResult{}, err
		}
		// The guard the old count had, widened by one value and no further.
		// Delete refuses nothing but a delivered order, so an unconditional
		// re-derivation would walk a cancelled order carrying a stray
		// fulfillment row to confirmed — a state change nobody asked for and no
		// event explains.
		if (o.Status == OrderShipped || o.Status == OrderPartial) && want != o.Status {
			if err := setOrderStatus(ctx, tx, o.ID, want); err != nil {
				return transitionResult{}, err
			}
			o.Status = want
		}
		payload := f.app.orders.eventPayload(o)
		payload.Shipment = &ShipmentEvent{
			FulfillmentID: id, Carrier: carrier,
			RemainingUnits: remaining, Lines: held,
		}
		return transitionResult{
			Event: EventOrderUnshipped, Payload: payload,
			Action:  AuditOrderShipmentDelete,
			Summary: "Removed a shipment from order " + o.Number,
		}, nil
	})
}

// parcelFor works out what a shipment physically is, from the variants behind
// the lines going into it.
//
// See the Parcel type for why weight is summed and dimensions usually are not.
func (f *Fulfillments) parcelFor(ctx context.Context, o *Order, lines []ShipLine) (Parcel, error) {
	variantOf := make(map[int64]int64, len(o.Lines))
	for _, l := range o.Lines {
		// A line whose variant has since been deleted keeps its snapshot and
		// loses the reference, so there is nothing to weigh for it.
		if l.VariantID != nil {
			variantOf[l.ID] = *l.VariantID
		}
	}

	var (
		parcel   Parcel
		units    int
		onlyOne  int64
		weighed  = true
		distinct = map[int64]bool{}
	)
	for _, line := range lines {
		variantID, ok := variantOf[line.OrderLineID]
		if !ok || line.Quantity <= 0 {
			continue
		}
		distinct[variantID] = true
		onlyOne = variantID
		units += line.Quantity

		var grams sql.NullInt64
		if err := f.app.db.QueryRowContext(ctx,
			`SELECT weight_grams FROM variants WHERE id = $1`, variantID).Scan(&grams); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return Parcel{}, Internalf(err, "read the weight of variant %d", variantID)
		}
		if !grams.Valid {
			// One unweighed line makes the total a floor rather than the
			// weight, and a module quoting a carrier on it should know.
			weighed = false
			continue
		}
		parcel.WeightGrams += int(grams.Int64) * line.Quantity
	}
	parcel.Measured = weighed && parcel.WeightGrams > 0

	// One unit of one variant is the only case with an unambiguous size.
	if len(distinct) == 1 && units == 1 {
		var l, w, h sql.NullInt64
		if err := f.app.db.QueryRowContext(ctx,
			`SELECT length_mm, width_mm, height_mm FROM variants WHERE id = $1`, onlyOne,
		).Scan(&l, &w, &h); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Parcel{}, Internalf(err, "read the size of variant %d", onlyOne)
		}
		if l.Valid {
			mm := int(l.Int64)
			parcel.Dimensions.Length = &mm
		}
		if w.Valid {
			mm := int(w.Int64)
			parcel.Dimensions.Width = &mm
		}
		if h.Valid {
			mm := int(h.Int64)
			parcel.Dimensions.Height = &mm
		}
	}
	return parcel, nil
}
