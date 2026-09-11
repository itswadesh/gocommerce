package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The stock ledger: one append-only row for every change to a variant_stock
// balance, written inside the transaction that made the change (M26).
//
// Why a table and not an event. core/events.go calls its taxonomy a public
// contract, added to only when something real produces a name, and
// AggregateOrder is still the only aggregate; nothing consumes a stock event
// today, order.* already announces every order-driven movement, and a
// twenty-line order would put forty rows through the dispatcher's claim loop
// into a table nothing prunes, for nobody. AGENTS rule 4 requires that an event
// commit with its change, not that every change have an event — and its purpose
// is met here anyway, because the ledger row is written inside the same InTx as
// the balance it explains, so a rolled-back checkout leaves no phantom
// movement. The seam stays open at the cost of one constant: an
// AggregateVariant beside AggregateOrder and one stock.moved name emitted from
// recordMovement, which is already the single funnel, with no schema change —
// StockMovement is the payload. See D38.
//
// Why the delta is what the statement APPLIED and never what the caller asked
// for: five of the writers wrap their quantity in CASE WHEN v.track_inventory
// and report success while moving zero, one floors a release at zero, and two
// write absolutely. A ledger that stored the request would not reconcile to the
// shelf, and a ledger that does not reconcile cannot settle the dispute it
// exists for.

// The movement kinds, which are also the table's CHECK. A closed vocabulary is
// what lets a reader trust the column — the trade orders.status and
// role_rights.role already make — and the price is that a new kind is a new
// migration (rule 7). A module can never add one, because modules do not write
// core tables.
const (
	// MovementOpening is a balance that existed before it was explained: the
	// M26 seed, and a variant created with stock.
	MovementOpening = "opening"
	// MovementAdjust is a delta an operator applied — a delivery, a write-off.
	MovementAdjust = "adjust"
	// MovementStockTake is a count that replaced the count. It is the one kind
	// that may record a movement of zero, because an operator confirming the
	// number already on the shelf is evidence rather than noise.
	MovementStockTake = "stock_take"
	// MovementImport is a count a CSV set.
	MovementImport = "import"
	// MovementTransferOut and MovementTransferIn are the two ends of one
	// transfer, written by one transaction and naming each other.
	MovementTransferOut = "transfer_out"
	MovementTransferIn  = "transfer_in"
	// The order-driven five. Reserve holds units for an order; commit turns a
	// reservation into a sale and moves both counters; release drops a
	// reservation without selling; restock puts sold units back; sell takes
	// units that were never reserved.
	MovementReserve = "reserve"
	MovementCommit  = "commit"
	MovementRelease = "release"
	MovementRestock = "restock"
	MovementSell    = "sell"
)

// MovementKinds is the catalogue, in the order an operator reads them: the ones
// a person causes first, then the ones an order does. It is what the query
// validator, the OpenAPI enum and the panel's filter chips all read, so the
// vocabulary is written once.
var MovementKinds = []string{
	MovementOpening, MovementAdjust, MovementStockTake, MovementImport,
	MovementTransferOut, MovementTransferIn,
	MovementReserve, MovementCommit, MovementRelease, MovementRestock, MovementSell,
}

// The paths stock moves along. This is not the actor: whether a person or a
// script holding a static token was behind an 'admin' movement is
// superuser_id's job, and storing that twice is the stored duplicate this
// codebase refuses elsewhere. What it answers is the question a null actor
// otherwise leaves open — a checkout and the unpaid sweeper have no person by
// definition, and 'system' would say so three different ways.
//
// 'migration' belongs to the M26 seed alone and has no Go constant: nothing in
// the engine may write it.
const (
	sourceAdmin    = "admin"
	sourceCheckout = "checkout"
	sourceOrder    = "order"
	sourceSweeper  = "sweeper"
	sourceImport   = "import"
)

// stockBalance is a pair of counters — used both for the two deltas a movement
// applied and for the two balances it left behind. Which one a parameter means
// is named at every call site by its position: delta first, after second.
type stockBalance struct {
	OnHand   int
	Reserved int
}

// stockRef is why a movement happened, travelling with the call because by the
// time restockStock runs the only thing that still knows this is a cancellation
// is the caller.
//
// Kind is deliberately not a field: the movement helper stamps its own, so a
// caller cannot pass the wrong one.
type stockRef struct {
	// Reason is an operator's words, recorded verbatim. Length is validated at
	// the HTTP boundary rather than here, so an over-long cancellation reason
	// can never fail the cancellation.
	Reason string
	// OrderID and OrderNumber name the order that caused the movement. Zero
	// means an operator moved it directly — or, for checkout's reservations,
	// that the order does not exist yet; see attachMovementsToOrder.
	OrderID     int64
	OrderNumber string
	// Counterpart is the other end of a transfer.
	Counterpart int64
}

// withStockSource labels the path a request is moving stock along.
//
// First label wins. SweepUnpaid marks the context before it calls Cancel, and
// Cancel's own transition then labels it 'order'; without this rule the more
// specific answer would be overwritten by the generic one and two hundred
// nightly releases would read as somebody cancelling by hand.
func withStockSource(ctx context.Context, src string) context.Context {
	if _, ok := ctx.Value(ctxKeyStockSource).(string); ok {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyStockSource, src)
}

// stockSource is the path this movement is on, defaulting to the admin one: an
// unlabelled movement reached the engine through an admin service call, which
// is the only way in that nothing else labels.
func stockSource(ctx context.Context) string {
	if src, ok := ctx.Value(ctxKeyStockSource).(string); ok {
		return src
	}
	return sourceAdmin
}

// recordMovement writes one ledger row, and is the only thing that writes one.
//
// It takes a *sql.Tx and no service handle for writeAudit's reason: there is no
// correct way to call it outside the transaction that moved the stock, and a
// signature that cannot express the wrong call is better than a comment asking
// for the right one.
//
// A movement that changed neither counter is not recorded, because an untracked
// variant's reservation moved nothing by design and a re-run of an unchanged
// import changed nothing — rows saying nothing happened hide the rows that say
// something did. The exception is a stock take, and it is derived from the kind
// here rather than carried as a flag by the caller, so it cannot be set
// inconsistently at one call site. The table's CHECK says the same thing again,
// so a future writer that forgets to suppress a zero fails loudly.
//
// The actor comes from auditActor, which is the engine's one actor seam: the
// audit trail and this ledger read the operator off the context in the same
// place, so the two records can never name different people.
func recordMovement(ctx context.Context, tx *sql.Tx, variantID, locationID int64,
	kind string, delta, after stockBalance, ref stockRef) (int64, error) {

	if delta.OnHand == 0 && delta.Reserved == 0 && kind != MovementStockTake {
		return 0, nil
	}

	_, actorID, actorEmail, _, _ := auditActor(ctx)

	// One statement: the sku, the location code and the counterpart's code are
	// snapshotted by the same SELECT that supplies the ids, so a row that still
	// reads after its SKU is deleted costs no extra round trip to write.
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO stock_movements (
		    variant_id, sku, location_id, location_code, kind, source,
		    on_hand_delta, reserved_delta, on_hand_after, reserved_after,
		    reason, counterpart_location_id, counterpart_code,
		    order_id, order_number, superuser_id, actor_email)
		SELECT v.id, v.sku, l.id, l.code, $3, $4,
		       $5, $6, $7, $8,
		       $9, cl.id, coalesce(cl.code, ''),
		       $11, $12, $13, $14
		FROM variants v
		JOIN locations l ON l.id = $2
		LEFT JOIN locations cl ON cl.id = $10
		WHERE v.id = $1
		RETURNING id`,
		variantID, locationID, kind, stockSource(ctx),
		delta.OnHand, delta.Reserved, after.OnHand, after.Reserved,
		strings.TrimSpace(ref.Reason), nullInt64(ref.Counterpart),
		nullInt64(ref.OrderID), ref.OrderNumber,
		actorID, actorEmail,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		// The caller has just moved this pair's balance under a row lock, so
		// both parents existed a statement ago. Loud rather than silent: a
		// movement that is not recorded is the one thing this table cannot
		// survive.
		return 0, fmt.Errorf("stock movement: variant %d or location %d is gone mid-transaction",
			variantID, locationID)
	}
	if err != nil {
		return 0, translateCatalogErr(err)
	}
	return id, nil
}

// attachMovementsToOrder names the order on rows that were written before it
// existed.
//
// This is the ONLY statement in the engine that updates stock_movements, and it
// is legal for exactly one reason: checkout reserves stock eighty lines before
// it inserts the order — the order id does not exist yet, and hoisting the
// nextval would burn an order number on every conflicted checkout — so these
// rows are completed by the same transaction that created them, before commit.
// Under MVCC no reader can ever observe the NULL, and the table stays
// append-only to everyone outside.
//
// It is unexported, has one caller, and carries `order_id IS NULL` so it can
// never overwrite an attribution. If a future author reaches for it to backfill
// or to correct, the table stops being an append and the audit trail stops
// being evidence.
func attachMovementsToOrder(ctx context.Context, tx *sql.Tx, orderID int64, number string, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE stock_movements SET order_id = $1, order_number = $2
		WHERE id = ANY($3::bigint[]) AND order_id IS NULL`,
		orderID, number, int64Array(ids))
	return err
}

// StockMovement is one row of the ledger.
//
// Two deltas rather than one signed number, because a commit takes units out of
// the reservation and off the shelf in the same movement and one column cannot
// say which happened. The balances after sit beside them so a row read in the
// middle of a paged history makes sense on its own.
//
// VariantID and LocationID go null when their parent is deleted, while SKU and
// LocationCode keep what they were called — which is what makes "who deleted
// the SKU that held forty units" answerable. A null SuperuserID means no person
// was behind it; Source then names which path was.
type StockMovement struct {
	ID                    int64     `json:"id"`
	VariantID             *int64    `json:"variant_id"`
	SKU                   string    `json:"sku"`
	LocationID            *int64    `json:"location_id"`
	LocationCode          string    `json:"location_code"`
	Kind                  string    `json:"kind"`
	Source                string    `json:"source"`
	OnHandDelta           int       `json:"on_hand_delta"`
	ReservedDelta         int       `json:"reserved_delta"`
	OnHandAfter           int       `json:"on_hand_after"`
	ReservedAfter         int       `json:"reserved_after"`
	Reason                string    `json:"reason,omitempty"`
	CounterpartLocationID *int64    `json:"counterpart_location_id,omitempty"`
	CounterpartCode       string    `json:"counterpart_code,omitempty"`
	OrderID               *int64    `json:"order_id,omitempty"`
	OrderNumber           string    `json:"order_number,omitempty"`
	SuperuserID           *int64    `json:"superuser_id,omitempty"`
	ActorEmail            string    `json:"actor_email,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
}

// MovementQuery narrows the ledger. Every field is optional; the zero value
// reads the newest movements in the store.
type MovementQuery struct {
	VariantID  int64
	LocationID int64
	OrderID    int64
	Kinds      []string
	From, To   *time.Time
	Limit      int
	Offset     int
}

// Movements is how a count got to be what it is, newest first.
//
// Ordered by id rather than by created_at, because a transfer writes both of
// its rows in one transaction and they share now() exactly — the serial is the
// only total ordering there is. No joins: every name the reader needs was
// snapshotted when the row was written.
func (i *Inventory) Movements(ctx context.Context, q MovementQuery) ([]StockMovement, int, error) {
	where := []string{}
	args := []any{}
	add := func(clause string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if q.VariantID != 0 {
		add("variant_id = $%d", q.VariantID)
	}
	if q.LocationID != 0 {
		add("location_id = $%d", q.LocationID)
	}
	if q.OrderID != 0 {
		add("order_id = $%d", q.OrderID)
	}
	if len(q.Kinds) > 0 {
		for _, k := range q.Kinds {
			// A typo is a 400 rather than an empty page: "no movements" and
			// "you asked for a kind that does not exist" are different answers
			// and only one of them is the caller's to fix.
			if !containsMovementKind(k) {
				return nil, 0, Validationf("%q is not a stock movement kind", k)
			}
		}
		add("kind = ANY($%d::text[])", stringArray(q.Kinds))
	}
	if q.From != nil {
		add("created_at >= $%d", *q.From)
	}
	if q.To != nil {
		add("created_at < $%d", *q.To)
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := i.app.db.QueryRowContext(ctx,
		`SELECT count(*) FROM stock_movements`+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit, offset := q.Limit, q.Offset
	if limit <= 0 {
		limit = DefaultLimit
	}
	rows, err := i.app.db.QueryContext(ctx, `
		SELECT id, variant_id, sku, location_id, location_code, kind, source,
		       on_hand_delta, reserved_delta, on_hand_after, reserved_after,
		       reason, counterpart_location_id, counterpart_code,
		       order_id, order_number, superuser_id, actor_email, created_at
		FROM stock_movements`+clause+
		fmt.Sprintf(" ORDER BY id DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2),
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []StockMovement{}
	for rows.Next() {
		var m StockMovement
		var variantID, locationID, counterpart, orderID, superuserID sql.NullInt64
		if err := rows.Scan(&m.ID, &variantID, &m.SKU, &locationID, &m.LocationCode,
			&m.Kind, &m.Source, &m.OnHandDelta, &m.ReservedDelta,
			&m.OnHandAfter, &m.ReservedAfter, &m.Reason, &counterpart, &m.CounterpartCode,
			&orderID, &m.OrderNumber, &superuserID, &m.ActorEmail, &m.CreatedAt); err != nil {
			return nil, 0, err
		}
		m.VariantID = nullableID(variantID)
		m.LocationID = nullableID(locationID)
		m.CounterpartLocationID = nullableID(counterpart)
		m.OrderID = nullableID(orderID)
		m.SuperuserID = nullableID(superuserID)
		out = append(out, m)
	}
	return out, total, rows.Err()
}

func nullableID(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	id := v.Int64
	return &id
}

func containsMovementKind(kind string) bool {
	for _, k := range MovementKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// stringArray renders a text[] literal, int64Array's sibling, for `= ANY($n)`.
func stringArray(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = `"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
	}
	return "{" + strings.Join(quoted, ",") + "}"
}
