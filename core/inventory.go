package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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

// How a location's shelves are cut. Two questions, two orderings: "what do I
// have to move before I can close this" wants the biggest holdings first, and
// "what is this shop short of" wants the emptiest shelf first.
const (
	StockOrderHolding   = "holding"
	StockOrderAvailable = "available"
)

// LocationStockQuery cuts one location's shelves.
type LocationStockQuery struct {
	// NonZero keeps only rows the place is actually holding — on hand or
	// reserved. It is the same test Locations refuses a close on, so a listing
	// and a refusal can never disagree about what is there.
	NonZero bool
	// Threshold, when set, keeps only what is at or below it *here*. That is
	// the low-stock report for one shop, and unlike the store-wide one it is
	// answerable: a variant with one unit in each of five shops is low in all
	// five.
	Threshold *int
	Order     string
	// Sort is the operator's chosen ordering, and it OVERRIDES Order when it
	// names a field. Order stays because it is not a column choice: it is which
	// of the two questions the listing is being asked — "what must I move before
	// I can close this" or "what is this shop short of" — and a screen that has
	// not offered a sort still means one of them.
	Sort          Sort
	Limit, Offset int
}

// locationStockSorts is the per-location listing's allow-list.
//
// Its keys are lowStockSorts' keys, deliberately: one report, one set of column
// headers, and the location filter must not change which of them can be
// clicked. The EXPRESSIONS differ because the numbers differ — here they are the
// one shelf's own columns, and there they are the store-wide sums — which is
// also why this cannot be one spec. A test pins the two key sets together.
var locationStockSorts = SortSpec{
	Tiebreak: "vs.variant_id",
	Columns: map[string]sortField{
		"sku":       {"lower(v.sku) ASC", "lower(v.sku) DESC"},
		"price":     {"v.price_minor ASC", "v.price_minor DESC"},
		"on_hand":   {"vs.on_hand ASC", "vs.on_hand DESC"},
		"reserved":  {"vs.reserved ASC", "vs.reserved DESC"},
		"available": {"(vs.on_hand - vs.reserved) ASC", "(vs.on_hand - vs.reserved) DESC"},
	},
}

// lowStockSorts is the store-wide report's allow-list.
//
// The three stock keys are the same correlated subqueries the SELECT and the
// threshold already use, so the ordering and the number on screen are the same
// arithmetic. None of them can be NULL — every one is wrapped in coalesce — so
// none carries a NULLS clause, which is what keeps an ascending index serving
// the descending scan.
var lowStockSorts = SortSpec{
	Tiebreak: "v.id",
	Columns: map[string]sortField{
		"sku":       {"lower(v.sku) ASC", "lower(v.sku) DESC"},
		"price":     {"v.price_minor ASC", "v.price_minor DESC"},
		"on_hand":   {variantOnHand + " ASC", variantOnHand + " DESC"},
		"reserved":  {variantReserved + " ASC", variantReserved + " DESC"},
		"available": {variantAvailable + " ASC", variantAvailable + " DESC"},
	},
}

// LowStockQuery cuts the store-wide low-stock report.
//
// A struct rather than four positional arguments, for the reason
// LocationStockQuery is one: the fifth thing a report can be asked is not a
// number, and a signature that grows one parameter per release is a signature
// every caller has to be edited for.
type LowStockQuery struct {
	// Threshold is compared against the store's total availability.
	Threshold     int
	Sort          Sort
	Limit, Offset int
}

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

// shelf is a resolved location with the two facts an arrival has to check.
type shelf struct {
	id     int64
	active bool
	name   string
}

// resolveShelf is resolveLocation plus those two facts.
//
// The row is read FOR SHARE, and read BEFORE any variant_stock row is touched.
// Both halves matter. Locations.Update takes this row FOR UPDATE and then reads
// variant_stock; a movement that touched variant_stock first and locked this
// row afterwards would take the same two tables in the opposite order, which is
// a deadlock. And without the lock, a deactivation committing between the check
// and the UPDATE that follows strands stock at a location that is closed a
// millisecond later — the exact state both guards exist to prevent.
func resolveShelf(ctx context.Context, tx *sql.Tx, locationID int64) (shelf, error) {
	if locationID == 0 {
		id, err := defaultLocationID(ctx, tx)
		if err != nil {
			return shelf{}, err
		}
		locationID = id
	}
	var s shelf
	err := tx.QueryRowContext(ctx,
		`SELECT id, active, name FROM locations WHERE id = $1 FOR SHARE`,
		locationID).Scan(&s.id, &s.active, &s.name)
	if errors.Is(err, sql.ErrNoRows) {
		return shelf{}, NotFoundf("location %d does not exist", locationID)
	}
	return s, err
}

// refuseIfClosed blocks units arriving at a location that is not open (D44).
//
// Stock may always leave a closed place — that is the only way one is ever
// emptied, and a store that already has stranded units needs that path. It may
// never be *received* at one: an inactive location's units are counted by the
// variant's totals and skipped by pickLocation, so they would be for sale and
// unsellable at once, and Locations.Update and Delete would then refuse to
// finish closing the place that is holding them.
func refuseIfClosed(s shelf) error {
	if s.active {
		return nil
	}
	return Conflictf("%s is closed; stock moves out of a closed location, never into it", s.name)
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
		// Receiving is arrival; writing off is departure, and a closed shelf has
		// to keep the second or it can never be emptied (D44).
		if delta > 0 {
			if err := refuseIfClosed(loc); err != nil {
				return err
			}
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
			variantID, loc.id, delta).Scan(&after.OnHand, &after.Reserved)
		if errors.Is(err, sql.ErrNoRows) {
			return i.explainStockFailure(ctx, tx, variantID, loc.id)
		}
		if err != nil {
			return translateCatalogErr(err)
		}
		_, err = recordMovement(ctx, tx, variantID, loc.id, MovementAdjust,
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
			variantID, loc.id).Scan(&was); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return i.explainStockFailure(ctx, tx, variantID, loc.id)
			}
			return err
		}
		// A stock take that counts down is how a closed shelf is cleared, so only
		// a count above what is already there is an arrival (D44). The comparison
		// is against the figure read under the FOR UPDATE above, so a concurrent
		// lowering cannot turn a decrease into an increase between the two.
		if qty > was {
			if err := refuseIfClosed(loc); err != nil {
				return err
			}
		}
		// Still the guard: see Adjust.
		var after stockBalance
		err = tx.QueryRowContext(ctx, `
			UPDATE variant_stock SET on_hand = $3, updated_at = now()
			WHERE variant_id = $1 AND location_id = $2 AND $3 >= reserved
			RETURNING on_hand, reserved`,
			variantID, loc.id, qty).Scan(&after.OnHand, &after.Reserved)
		if errors.Is(err, sql.ErrNoRows) {
			return i.explainStockFailure(ctx, tx, variantID, loc.id)
		}
		if err != nil {
			return translateCatalogErr(err)
		}
		// A count that confirms the count writes a row of zeroes, and a stock
		// take is the one kind the ledger's CHECK lets say nothing moved: an
		// operator who walked to the shelf and found the number already there
		// has produced the evidence a stock-take dispute turns on.
		_, err = recordMovement(ctx, tx, variantID, loc.id, MovementStockTake,
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
		// The source stays on resolveLocation, which asks nothing about `active`:
		// moving units off a closed shelf is the whole of the recovery path for a
		// store that already has some stranded there, and a check here would make
		// exactly those units permanently unmovable (D44).
		from, err := resolveLocation(ctx, tx, fromID)
		if err != nil {
			return err
		}
		to, err := i.prepare(ctx, tx, variantID, toID)
		if err != nil {
			return err
		}
		if err := refuseIfClosed(to); err != nil {
			return err
		}
		if from == to.id {
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
			ORDER BY location_id FOR UPDATE`, variantID, from, to.id)
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
			variantID, to.id, qty).Scan(&in.OnHand, &in.Reserved); err != nil {
			return translateCatalogErr(err)
		}
		// Two rows, each naming the other end. "3 units left here" is half an
		// answer without "and went to the shop", and the store's total is
		// unchanged because the two deltas cancel.
		if _, err := recordMovement(ctx, tx, variantID, from, MovementTransferOut,
			stockBalance{OnHand: -qty}, out,
			stockRef{Reason: reason, Counterpart: to.id}); err != nil {
			return err
		}
		_, err = recordMovement(ctx, tx, variantID, to.id, MovementTransferIn,
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

// AtLocation is every variant at one place, where ByLocation is one variant
// across every place. It is the read a shop does before it restocks, and the one
// an operator arrives at from a refusal that named units they now have to find.
//
// A location's report is its own variant_stock rows, joined rather than left
// joined: a row exists from the moment anything moves there and survives going
// to zero, so a shop that has sold out of something it carries still appears —
// which is the restocking case — while the catalog a newly opened shop has never
// carried does not bury it.
func (i *Inventory) AtLocation(ctx context.Context, locationID int64, q LocationStockQuery) ([]*Variant, int, error) {
	// The location first, so a mistyped id is a 404 rather than an empty page
	// that reads as a fully stocked shop. ByLocation makes the same probe.
	loc, err := i.app.locations.Get(ctx, locationID)
	if err != nil {
		return nil, 0, err
	}
	if q.Threshold != nil && *q.Threshold < 0 {
		return nil, 0, Validationf("threshold must not be negative")
	}

	where := []string{"vs.location_id = $1"}
	args := []any{locationID}
	if q.NonZero {
		// refuseIfHolding counts a SKU as held when either number is non-zero,
		// and untracked variants are counted there too — so this cut does not
		// ask about track_inventory either, or a listing would not add up to the
		// refusal that sent the operator here.
		where = append(where, "(vs.on_hand <> 0 OR vs.reserved <> 0)")
	}
	if q.Threshold != nil {
		// The threshold cut, unlike the holdings cut, does exclude untracked
		// variants: an unlimited variant is never low. That is LowStock's rule,
		// kept here so the two reports mean the same thing.
		args = append(args, *q.Threshold)
		where = append(where, fmt.Sprintf("v.track_inventory AND vs.on_hand - vs.reserved <= $%d", len(args)))
	}
	clause := strings.Join(where, " AND ")

	fallback := `(vs.on_hand + vs.reserved) DESC, vs.variant_id`
	if q.Order == StockOrderAvailable {
		fallback = `(vs.on_hand - vs.reserved) ASC, vs.variant_id`
	}
	// Resolved before any database work, so a rejected sort costs none — the
	// count below runs after this. It is read after the two fixed orderings
	// rather than instead of them: Order says which question the listing is
	// being asked, and an explicit Sort overrides the answer's shape.
	order, err := locationStockSorts.Clause(q.Sort, fallback)
	if err != nil {
		return nil, 0, err
	}

	var total int
	if err := i.app.db.QueryRowContext(ctx, `
		SELECT count(*) FROM variant_stock vs
		JOIN variants v ON v.id = vs.variant_id
		WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	if q.Limit <= 0 {
		q.Limit = DefaultLimit
	}
	args = append(args, q.Limit, q.Offset)
	rows, err := i.app.db.QueryContext(ctx, `
		SELECT vs.variant_id, vs.on_hand, vs.reserved
		FROM variant_stock vs
		JOIN variants v ON v.id = vs.variant_id
		WHERE `+clause+`
		ORDER BY `+order+fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	type held struct{ onHand, reserved int }
	ids := []int64{}
	at := map[int64]held{}
	for rows.Next() {
		var id int64
		var h held
		if err := rows.Scan(&id, &h.onHand, &h.reserved); err != nil {
			return nil, 0, err
		}
		ids = append(ids, id)
		at[id] = h
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// Never nil: selectVariants returns a nil slice for an empty page, which
	// marshals as "data": null and breaks the envelope every other listing keeps.
	out := []*Variant{}
	if len(ids) == 0 {
		return out, total, nil
	}

	// Hydrated through the shared scanner rather than a widened column list, so
	// a row here is the same Variant every other catalog read returns and the
	// per-location figures ride beside it rather than overwriting it.
	variants, err := i.app.catalog.queryVariants(ctx, `v.id = ANY($1::bigint[])`, int64Array(ids))
	if err != nil {
		return nil, 0, err
	}
	byID := make(map[int64]*Variant, len(variants))
	for _, v := range variants {
		byID[v.ID] = v
	}
	// Walked in page order: queryVariants orders by v.position, v.id, which is
	// not the ordering this page was selected in.
	for _, id := range ids {
		v := byID[id]
		if v == nil {
			continue
		}
		h := at[id]
		v.AtLocation = &VariantStock{
			VariantID: v.ID, LocationID: loc.ID, LocationCode: loc.Code,
			LocationName: loc.Name, Active: loc.Active,
			OnHand: h.onHand, Reserved: h.reserved, Available: h.onHand - h.reserved,
		}
		out = append(out, v)
	}
	return out, total, nil
}

// prepare resolves the location and makes sure the variant has a row there, so
// that receiving stock at a location opened after the variant existed works
// without the operator having to create anything.
//
// The shelf comes back pinned rather than just its id, so the caller can decide
// whether this particular movement is an arrival and refuse it at a closed
// place. Resolving through resolveShelf also takes the locations lock before
// ensureStockRow touches variant_stock, which is the order Locations.Update
// takes the same two tables in.
func (i *Inventory) prepare(ctx context.Context, tx *sql.Tx, variantID, locationID int64) (shelf, error) {
	loc, err := resolveShelf(ctx, tx, locationID)
	if err != nil {
		return shelf{}, err
	}
	var exists bool
	err = tx.QueryRowContext(ctx,
		`SELECT true FROM variants WHERE id = $1`, variantID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return shelf{}, NotFoundf("variant %d does not exist", variantID)
	}
	if err != nil {
		return shelf{}, err
	}
	return loc, ensureStockRow(ctx, tx, variantID, loc.id)
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
func (i *Inventory) LowStock(ctx context.Context, q LowStockQuery) ([]*Variant, int, error) {
	if q.Threshold < 0 {
		return nil, 0, Validationf("threshold must not be negative")
	}
	// Before the count, so a rejected sort costs no database work — and the
	// emptiest first stays the fallback, because "what do I have to reorder" is
	// what the report is for when nobody has clicked a column.
	order, err := lowStockSorts.Clause(q.Sort, variantAvailable+` ASC, v.id`)
	if err != nil {
		return nil, 0, err
	}
	var total int
	if err := i.app.db.QueryRowContext(ctx, `
		SELECT count(*) FROM variants v
		WHERE v.track_inventory AND `+variantAvailable+` <= $1`, q.Threshold).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	variants, err := i.app.catalog.queryVariantsOrdered(ctx,
		`v.track_inventory AND `+variantAvailable+` <= $1`,
		order, limit, q.Offset, q.Threshold)
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
