package gocommerce

import (
	"context"
	"database/sql"
	"errors"
)

// Inventory owns stock movements. Every change goes through here rather than
// through arbitrary SQL, because "set the stock to 7" is not a safe operation
// when another request may have sold one in between — the operations below are
// deltas evaluated under a row lock, not overwrites.
//
// A variant's sellable quantity is on_hand - reserved, summed over its
// locations. Checkout reserves; confirming a sale converts the reservation
// into a decrement of on_hand; cancelling releases or restocks depending on
// how far the order got.
//
// Since M17 those two numbers live per (variant, location) and the variant's
// totals are sums taken on the way out. Every movement therefore names a
// place; 0 means the default one, which is what a single-location store
// always passes and never has to think about.
type Inventory struct {
	app *App
}

// Stock returns the inventory service.
func (a *App) Stock() *Inventory { return a.inventory }

// The four stock movements, each against one location.
//
// Every one of them is a single statement, so the check and the change happen
// under the same row lock. That is what makes two concurrent checkouts for the
// last unit resolve rather than race: the second re-evaluates its condition
// after the first commits.
//
// `track_inventory` is on the variant and the movement is on the stock row, so
// each of these joins back to ask whether counting applies at all. A variant
// that does not track inventory succeeds and moves nothing.
//
// Since M26 every one of them also appends a row to stock_movements inside the
// same transaction, recording what the statement APPLIED rather than what the
// caller asked for — which for the five that carry the CASE above is zero
// whenever counting does not apply. The applied amount and the balances after
// come back on the RETURNING of the statement that was going to run anyway, so
// the hot paths gain no round trip; only the three writers that clamp read
// first, and their reads are arithmetic, never a decision.

// pickLocation chooses where a reservation comes from: the first active
// location, in priority order, that can cover the quantity — falling back to the
// default when nothing can, so that a variant which sells past zero still has a
// place to be short in.
//
// One query, because "which location" and "is there enough there" are the same
// question and answering them separately invites the stock to move in between.
func pickLocation(ctx context.Context, tx *sql.Tx, variantID int64, qty int) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
		SELECT coalesce(
		    (SELECT vs.location_id
		       FROM variant_stock vs
		       JOIN locations l ON l.id = vs.location_id
		       JOIN variants v ON v.id = vs.variant_id
		      WHERE vs.variant_id = $1 AND l.active
		        AND (NOT v.track_inventory OR v.continue_selling
		             OR vs.on_hand - vs.reserved >= $2)
		      ORDER BY l.priority, l.id
		      LIMIT 1),
		    (SELECT id FROM locations WHERE is_default))`,
		variantID, qty).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) || id == 0 {
		return 0, errInsufficientStock
	}
	return id, err
}

// reserveStock holds qty units at one location.
//
// It returns the ledger row's id, because checkout reserves before the order it
// belongs to exists and has to come back and name it; every other caller
// ignores the id.
func reserveStock(ctx context.Context, tx *sql.Tx, variantID, locationID int64, qty int, ref stockRef) (int64, error) {
	var after stockBalance
	var applied int
	err := tx.QueryRowContext(ctx, `
		UPDATE variant_stock vs
		SET reserved = vs.reserved + CASE WHEN v.track_inventory THEN $3 ELSE 0 END,
		    updated_at = now()
		FROM variants v
		WHERE v.id = vs.variant_id
		  AND vs.variant_id = $1 AND vs.location_id = $2
		  AND (NOT v.track_inventory OR v.continue_selling
		       OR vs.on_hand - vs.reserved >= $3)
		RETURNING vs.on_hand, vs.reserved, CASE WHEN v.track_inventory THEN $3 ELSE 0 END`,
		variantID, locationID, qty).Scan(&after.OnHand, &after.Reserved, &applied)
	if errors.Is(err, sql.ErrNoRows) {
		// Either there is no stock row for this pair or there is not enough
		// left. The caller distinguishes them; from here both mean "cannot
		// sell this". ErrNoRows is exactly what RowsAffected() == 0 meant
		// before the RETURNING: the guard is still the statement's own WHERE.
		return 0, errInsufficientStock
	}
	if err != nil {
		return 0, translateCatalogErr(err)
	}
	return recordMovement(ctx, tx, variantID, locationID, MovementReserve,
		stockBalance{Reserved: applied}, after, ref)
}

// commitStock turns a reservation into a sale: the units leave both the
// reservation and the shelf they were held on.
func commitStock(ctx context.Context, tx *sql.Tx, variantID, locationID int64, qty int, ref stockRef) error {
	ctx = withStockSource(ctx, sourceOrder)
	var after stockBalance
	var applied int
	err := tx.QueryRowContext(ctx, `
		UPDATE variant_stock vs
		SET reserved = vs.reserved - CASE WHEN v.track_inventory THEN $3 ELSE 0 END,
		    on_hand  = vs.on_hand  - CASE WHEN v.track_inventory THEN $3 ELSE 0 END,
		    updated_at = now()
		FROM variants v
		WHERE v.id = vs.variant_id AND vs.variant_id = $1 AND vs.location_id = $2
		RETURNING vs.on_hand, vs.reserved, CASE WHEN v.track_inventory THEN $3 ELSE 0 END`,
		variantID, locationID, qty).Scan(&after.OnHand, &after.Reserved, &applied)
	if errors.Is(err, sql.ErrNoRows) {
		// A missing pair stays silently nothing, which is what ignoring
		// RowsAffected meant here before M26. lineLocation's ensureStockRow
		// makes it unreachable from the order path, and turning it into an
		// error would put a live 500 on the confirm path for a case that has
		// never failed.
		return nil
	}
	if err != nil {
		return translateCatalogErr(err)
	}
	_, err = recordMovement(ctx, tx, variantID, locationID, MovementCommit,
		stockBalance{OnHand: -applied, Reserved: -applied}, after, ref)
	return err
}

// releaseStock drops a reservation without selling: the units go back on sale
// where they were held.
func releaseStock(ctx context.Context, tx *sql.Tx, variantID, locationID int64, qty int, ref stockRef) error {
	ctx = withStockSource(ctx, sourceOrder)
	// The one pre-read on the order path, and it is arithmetic rather than a
	// decision: greatest(0, …) below means releasing 3 against 2 reserved moves
	// 2, and the statement cannot report which it did. Nothing is tested here —
	// every condition stays in the UPDATE's own WHERE — and the SELECT takes
	// the same row lock the UPDATE would take a moment later.
	var before stockBalance
	err := tx.QueryRowContext(ctx,
		`SELECT on_hand, reserved FROM variant_stock
		 WHERE variant_id = $1 AND location_id = $2 FOR UPDATE`,
		variantID, locationID).Scan(&before.OnHand, &before.Reserved)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // commitStock's silence, for commitStock's reason.
	}
	if err != nil {
		return err
	}
	var after stockBalance
	err = tx.QueryRowContext(ctx, `
		UPDATE variant_stock vs
		SET reserved = greatest(0, vs.reserved - CASE WHEN v.track_inventory THEN $3 ELSE 0 END),
		    updated_at = now()
		FROM variants v
		WHERE v.id = vs.variant_id AND vs.variant_id = $1 AND vs.location_id = $2
		RETURNING vs.on_hand, vs.reserved`,
		variantID, locationID, qty).Scan(&after.OnHand, &after.Reserved)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return translateCatalogErr(err)
	}
	_, err = recordMovement(ctx, tx, variantID, locationID, MovementRelease,
		stockBalance{Reserved: after.Reserved - before.Reserved}, after, ref)
	return err
}

// restockStock returns already-sold units to the shelf they came off, for a
// cancellation after the sale was committed.
func restockStock(ctx context.Context, tx *sql.Tx, variantID, locationID int64, qty int, ref stockRef) error {
	ctx = withStockSource(ctx, sourceOrder)
	var after stockBalance
	var applied int
	err := tx.QueryRowContext(ctx, `
		UPDATE variant_stock vs
		SET on_hand = vs.on_hand + CASE WHEN v.track_inventory THEN $3 ELSE 0 END,
		    updated_at = now()
		FROM variants v
		WHERE v.id = vs.variant_id AND vs.variant_id = $1 AND vs.location_id = $2
		RETURNING vs.on_hand, vs.reserved, CASE WHEN v.track_inventory THEN $3 ELSE 0 END`,
		variantID, locationID, qty).Scan(&after.OnHand, &after.Reserved, &applied)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return translateCatalogErr(err)
	}
	_, err = recordMovement(ctx, tx, variantID, locationID, MovementRestock,
		stockBalance{OnHand: applied}, after, ref)
	return err
}

// ensureStockRow makes sure a (variant, location) pair exists before it is
// moved. A variant created before a location existed has no row for it, and a
// movement against a missing row is silently nothing.
func ensureStockRow(ctx context.Context, tx *sql.Tx, variantID, locationID int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO variant_stock (variant_id, location_id) VALUES ($1, $2)
		ON CONFLICT (variant_id, location_id) DO NOTHING`, variantID, locationID)
	return translateCatalogErr(err)
}

// defaultLocationID is where stock goes when nobody has said otherwise.
func defaultLocationID(ctx context.Context, tx *sql.Tx) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM locations WHERE is_default`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, Conflictf("this store has no default location")
	}
	return id, err
}

var errInsufficientStock = errors.New("insufficient stock")

// resolveLocation turns the caller's location — or 0, meaning "wherever the
// store puts things" — into a real id, and refuses one that does not exist
// rather than moving stock into a row the operator did not mean.
func resolveLocation(ctx context.Context, tx *sql.Tx, locationID int64) (int64, error) {
	if locationID == 0 {
		return defaultLocationID(ctx, tx)
	}
	var ok bool
	err := tx.QueryRowContext(ctx,
		`SELECT true FROM locations WHERE id = $1`, locationID).Scan(&ok)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, NotFoundf("location %d does not exist", locationID)
	}
	return locationID, err
}

// Adjust moves a variant's on-hand quantity at one location by delta — a
// receipt of new stock, or a correction after a stock count. Pass 0 for the
// location to mean the default. The result may not go below what is already
// reserved *there*: those units are promised to orders that will be picked from
// that shelf, and no other shelf can answer for them.
// The reason is the operator's own words and nothing else knows it, so it is an
// argument rather than something read off the context. Who is acting is the
// opposite: it never changes the outcome, so it is read from the context inside
// the ledger writer and no signature grows an actor parameter.
func (i *Inventory) Adjust(ctx context.Context, variantID, locationID int64, delta int, reason string) (*Variant, error) {
	if delta == 0 {
		return i.app.catalog.GetVariant(ctx, variantID)
	}
	err := InTx(ctx, i.app.db, func(tx *sql.Tx) error {
		loc, err := i.prepare(ctx, tx, variantID, locationID)
		if err != nil {
			return err
		}
		// The conditional UPDATE is still the guard: the RETURNING reports what
		// it did and never decides anything, and its not-matched signal is
		// sql.ErrNoRows where it used to be RowsAffected() == 0 — the same
		// branch, reached the same way, still ending in explainStockFailure.
		//
		// No CASE WHEN track_inventory here, deliberately: an operator counting
		// a digital product's shelf means the number they typed. So the applied
		// amount is exactly delta and nothing has to report it back.
		var after stockBalance
		err = tx.QueryRowContext(ctx, `
			UPDATE variant_stock
			SET on_hand = on_hand + $3, updated_at = now()
			WHERE variant_id = $1 AND location_id = $2 AND on_hand + $3 >= reserved
			RETURNING on_hand, reserved`,
			variantID, loc, delta).Scan(&after.OnHand, &after.Reserved)
		if errors.Is(err, sql.ErrNoRows) {
			return i.explainStockFailure(ctx, tx, variantID, loc)
		}
		if err != nil {
			return translateCatalogErr(err)
		}
		_, err = recordMovement(ctx, tx, variantID, loc, MovementAdjust,
			stockBalance{OnHand: delta}, after, stockRef{Reason: reason})
		return err
	})
	if err != nil {
		return nil, err
	}
	return i.app.catalog.GetVariant(ctx, variantID)
}

// SetOnHand sets the absolute on-hand quantity at one location, for a stock
// take. It refuses to drop below the quantity reserved there, for Adjust's
// reason.
func (i *Inventory) SetOnHand(ctx context.Context, variantID, locationID int64, qty int, reason string) (*Variant, error) {
	if qty < 0 {
		return nil, Validationf("stock_on_hand must not be negative")
	}
	err := InTx(ctx, i.app.db, func(tx *sql.Tx) error {
		loc, err := i.prepare(ctx, tx, variantID, locationID)
		if err != nil {
			return err
		}
		// prepare has already made sure the row exists, so this reads the count
		// that is about to be replaced. An absolute write has no parameter
		// equal to its delta — a count of 4 set to 18 is a movement of +14, and
		// only the before-image says so — and a stock take without the previous
		// count is half a record either way. The FOR UPDATE takes the lock the
		// guarded statement below takes anyway.
		var was int
		if err := tx.QueryRowContext(ctx,
			`SELECT on_hand FROM variant_stock WHERE variant_id = $1 AND location_id = $2 FOR UPDATE`,
			variantID, loc).Scan(&was); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return i.explainStockFailure(ctx, tx, variantID, loc)
			}
			return err
		}
		// Still the guard: see Adjust.
		var after stockBalance
		err = tx.QueryRowContext(ctx, `
			UPDATE variant_stock SET on_hand = $3, updated_at = now()
			WHERE variant_id = $1 AND location_id = $2 AND $3 >= reserved
			RETURNING on_hand, reserved`,
			variantID, loc, qty).Scan(&after.OnHand, &after.Reserved)
		if errors.Is(err, sql.ErrNoRows) {
			return i.explainStockFailure(ctx, tx, variantID, loc)
		}
		if err != nil {
			return translateCatalogErr(err)
		}
		// A count that confirms the count writes a row of zeroes, and a stock
		// take is the one kind the ledger's CHECK lets say nothing moved: an
		// operator who walked to the shelf and found the number already there
		// has produced the evidence a stock-take dispute turns on.
		_, err = recordMovement(ctx, tx, variantID, loc, MovementStockTake,
			stockBalance{OnHand: qty - was}, after, stockRef{Reason: reason})
		return err
	})
	if err != nil {
		return nil, err
	}
	return i.app.catalog.GetVariant(ctx, variantID)
}

// Move transfers units between two locations: one statement out, one in, in a
// single transaction, so the store's total never changes even for an instant.
//
// Reserved units do not travel. They are promised to orders that will be picked
// from where they are, and moving them would send a picker to the wrong shelf.
func (i *Inventory) Move(ctx context.Context, variantID, fromID, toID int64, qty int, reason string) (*Variant, error) {
	if qty <= 0 {
		return nil, Validationf("quantity must be positive")
	}
	err := InTx(ctx, i.app.db, func(tx *sql.Tx) error {
		from, err := resolveLocation(ctx, tx, fromID)
		if err != nil {
			return err
		}
		to, err := i.prepare(ctx, tx, variantID, toID)
		if err != nil {
			return err
		}
		if from == to {
			return Validationf("a transfer needs two different locations")
		}
		// Both row locks in ascending location order before either UPDATE, for
		// the reason checkout sorts its reservation loop: two operators
		// transferring in opposite directions between the same pair would
		// otherwise take the two locks in opposite orders and deadlock. It is
		// not a read — the rows are drained and discarded — and FOR UPDATE
		// locks rows as they are fetched, so draining is what takes them.
		lock, err := tx.QueryContext(ctx, `
			SELECT location_id FROM variant_stock
			WHERE variant_id = $1 AND location_id IN ($2, $3)
			ORDER BY location_id FOR UPDATE`, variantID, from, to)
		if err != nil {
			return err
		}
		for lock.Next() {
			var ignored int64
			if err := lock.Scan(&ignored); err != nil {
				lock.Close()
				return err
			}
		}
		lock.Close()
		if err := lock.Err(); err != nil {
			return err
		}

		// Neither statement carries a track_inventory CASE, so both deltas are
		// exactly qty: a transfer is a move between two shelves of this store,
		// and whether the variant is counted is not the question.
		var out stockBalance
		err = tx.QueryRowContext(ctx, `
			UPDATE variant_stock SET on_hand = on_hand - $3, updated_at = now()
			WHERE variant_id = $1 AND location_id = $2 AND on_hand - $3 >= reserved
			RETURNING on_hand, reserved`,
			variantID, from, qty).Scan(&out.OnHand, &out.Reserved)
		if errors.Is(err, sql.ErrNoRows) {
			return i.explainStockFailure(ctx, tx, variantID, from)
		}
		if err != nil {
			return translateCatalogErr(err)
		}
		var in stockBalance
		if err := tx.QueryRowContext(ctx, `
			UPDATE variant_stock SET on_hand = on_hand + $3, updated_at = now()
			WHERE variant_id = $1 AND location_id = $2
			RETURNING on_hand, reserved`,
			variantID, to, qty).Scan(&in.OnHand, &in.Reserved); err != nil {
			return translateCatalogErr(err)
		}
		// Two rows, each naming the other end. "3 units left here" is half an
		// answer without "and went to the shop", and the store's total is
		// unchanged because the two deltas cancel.
		if _, err := recordMovement(ctx, tx, variantID, from, MovementTransferOut,
			stockBalance{OnHand: -qty}, out,
			stockRef{Reason: reason, Counterpart: to}); err != nil {
			return err
		}
		_, err = recordMovement(ctx, tx, variantID, to, MovementTransferIn,
			stockBalance{OnHand: qty}, in,
			stockRef{Reason: reason, Counterpart: from})
		return err
	})
	if err != nil {
		return nil, err
	}
	return i.app.catalog.GetVariant(ctx, variantID)
}

// ByLocation is where a variant's stock actually is. The variant's own totals
// answer "how many"; this answers "where", which is the question a picker, a
// shipping estimate and a stock take all really ask.
func (i *Inventory) ByLocation(ctx context.Context, variantID int64) ([]VariantStock, error) {
	if _, err := i.app.catalog.GetVariant(ctx, variantID); err != nil {
		return nil, err
	}
	// A left join from locations, not from variant_stock: a location holding
	// none of this variant is a real and useful answer — it is where an
	// operator would send a transfer — and an inner join would hide it.
	rows, err := i.app.db.QueryContext(ctx, `
		SELECT l.id, l.code, l.name, l.active,
		       coalesce(vs.on_hand, 0), coalesce(vs.reserved, 0)
		FROM locations l
		LEFT JOIN variant_stock vs ON vs.location_id = l.id AND vs.variant_id = $1
		ORDER BY l.priority, l.id`, variantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VariantStock{}
	for rows.Next() {
		s := VariantStock{VariantID: variantID}
		if err := rows.Scan(&s.LocationID, &s.LocationCode, &s.LocationName,
			&s.Active, &s.OnHand, &s.Reserved); err != nil {
			return nil, err
		}
		s.Available = s.OnHand - s.Reserved
		out = append(out, s)
	}
	return out, rows.Err()
}

// prepare resolves the location and makes sure the variant has a row there, so
// that receiving stock at a location opened after the variant existed works
// without the operator having to create anything.
func (i *Inventory) prepare(ctx context.Context, tx *sql.Tx, variantID, locationID int64) (int64, error) {
	loc, err := resolveLocation(ctx, tx, locationID)
	if err != nil {
		return 0, err
	}
	var exists bool
	err = tx.QueryRowContext(ctx,
		`SELECT true FROM variants WHERE id = $1`, variantID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, NotFoundf("variant %d does not exist", variantID)
	}
	if err != nil {
		return 0, err
	}
	return loc, ensureStockRow(ctx, tx, variantID, loc)
}

// explainStockFailure turns "the update matched no rows" into a sentence naming
// the reservation that blocked it. The variant and the row are known to exist
// by the time this is reached — prepare made sure — so there is only one reason
// left.
func (i *Inventory) explainStockFailure(ctx context.Context, tx *sql.Tx, variantID, locationID int64) error {
	var reserved int
	err := tx.QueryRowContext(ctx,
		`SELECT reserved FROM variant_stock WHERE variant_id = $1 AND location_id = $2`,
		variantID, locationID).Scan(&reserved)
	if errors.Is(err, sql.ErrNoRows) {
		return NotFoundf("variant %d holds no stock at location %d", variantID, locationID)
	}
	if err != nil {
		return err
	}
	return Conflictf("stock cannot go below the %d unit(s) already reserved for open orders", reserved)
}

// LowStock lists variants at or below a threshold, so an operator — or an
// agent — can find what needs reordering. The threshold is against the store's
// total: a variant with one unit in each of five shops is not low, even though
// every individual shelf looks it.
func (i *Inventory) LowStock(ctx context.Context, threshold, limit, offset int) ([]*Variant, int, error) {
	if threshold < 0 {
		return nil, 0, Validationf("threshold must not be negative")
	}
	var total int
	if err := i.app.db.QueryRowContext(ctx, `
		SELECT count(*) FROM variants v
		WHERE v.track_inventory AND `+variantAvailable+` <= $1`, threshold).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	variants, err := i.app.catalog.queryVariantsOrdered(ctx,
		`v.track_inventory AND `+variantAvailable+` <= $1`,
		variantAvailable+` ASC, v.id`, limit, offset, threshold)
	if err != nil {
		return nil, 0, err
	}
	return variants, total, nil
}

// sellStock takes units off a shelf that were never reserved.
//
// The case is an order that has already committed its stock being edited
// upward: its reservation is long gone, so there is nothing to convert and the
// units come straight off on-hand. The guard is reserveStock's, for the same
// reason — the check and the decrement have to happen under one row lock, or
// two operators editing two orders can both pass it.
func sellStock(ctx context.Context, tx *sql.Tx, variantID, locationID int64, qty int, ref stockRef) error {
	ctx = withStockSource(ctx, sourceOrder)
	var after stockBalance
	var applied int
	err := tx.QueryRowContext(ctx, `
		UPDATE variant_stock vs
		SET on_hand = vs.on_hand - CASE WHEN v.track_inventory THEN $3 ELSE 0 END,
		    updated_at = now()
		FROM variants v
		WHERE v.id = vs.variant_id
		  AND vs.variant_id = $1 AND vs.location_id = $2
		  AND (NOT v.track_inventory OR v.continue_selling
		       OR vs.on_hand - vs.reserved >= $3)
		RETURNING vs.on_hand, vs.reserved, CASE WHEN v.track_inventory THEN $3 ELSE 0 END`,
		variantID, locationID, qty).Scan(&after.OnHand, &after.Reserved, &applied)
	if errors.Is(err, sql.ErrNoRows) {
		return errInsufficientStock
	}
	if err != nil {
		return translateCatalogErr(err)
	}
	_, err = recordMovement(ctx, tx, variantID, locationID, MovementSell,
		stockBalance{OnHand: -applied}, after, ref)
	return err
}
