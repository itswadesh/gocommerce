package shiprocket

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/itswadesh/gocommerce/core"
)

// What this module keeps: one row per order it has asked Shiprocket to
// create, written before the request goes out and filled in as each step
// answers. It is what makes booking idempotent. A retry — after a refusal, a
// timeout, or an engine that could not record the parcel — finds the row and
// carries on from the step that did not finish, rather than creating a second
// Shiprocket order or a second waybill.

var migrations = []gocommerce.Migration{
	{
		ID: "0001_shipments",
		SQL: `
CREATE TABLE shiprocket_shipments (
    id               bigserial   PRIMARY KEY,
    order_id         bigint      NOT NULL,
    -- The order_id this module sent Shiprocket: the order's number for its
    -- first parcel, then number-2, number-3. Shiprocket refuses an id it has
    -- seen, cancelled ones included, so a cancelled row keeps its id.
    channel_order_id text        NOT NULL,
    -- Which order lines, and how many of each, the Shiprocket order holds.
    contents         text        NOT NULL,
    -- Set before the create request is sent, so a retry after a lost answer
    -- looks the order up instead of creating it again.
    create_sent      boolean     NOT NULL DEFAULT false,
    sr_order_id      bigint,
    shipment_id      bigint,
    awb              text        NOT NULL DEFAULT '',
    courier_id       integer,
    courier_name     text        NOT NULL DEFAULT '',
    -- Set before a waybill is asked for, for the same reason.
    awb_sent         boolean     NOT NULL DEFAULT false,
    pickup_requested boolean     NOT NULL DEFAULT false,
    pickup_scheduled text        NOT NULL DEFAULT '',
    label_url        text        NOT NULL DEFAULT '',
    manifest_url     text        NOT NULL DEFAULT '',
    invoice_url      text        NOT NULL DEFAULT '',
    -- A short lease, so two people pressing Ship at once book one parcel.
    booking_until    timestamptz,
    cancelled_at     timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (channel_order_id)
);
CREATE INDEX shiprocket_shipments_order_idx ON shiprocket_shipments (order_id, id);
`,
	},
	gocommerce.ScopeMigration("0002_store_scope", "shiprocket_shipments"),
}

// shipment is one row, as the panel reads it.
type shipment struct {
	ID              int64  `json:"id"`
	OrderID         int64  `json:"order_id"`
	ChannelOrderID  string `json:"channel_order_id"`
	contents        string // order line id:quantity, for matching a retry
	createSent      bool
	awbSent         bool
	SROrderID       int64      `json:"sr_order_id,omitempty"`
	ShipmentID      int64      `json:"shipment_id,omitempty"`
	AWB             string     `json:"awb,omitempty"`
	CourierID       int        `json:"courier_id,omitempty"`
	CourierName     string     `json:"courier_name,omitempty"`
	PickupRequested bool       `json:"pickup_requested"`
	PickupScheduled string     `json:"pickup_scheduled,omitempty"`
	LabelURL        string     `json:"label_url,omitempty"`
	ManifestURL     string     `json:"manifest_url,omitempty"`
	InvoiceURL      string     `json:"invoice_url,omitempty"`
	CancelledAt     *time.Time `json:"cancelled_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	// FulfillmentID is the engine's record of this parcel, once it has one:
	// the order's fulfillment carrying this waybill. Zero means booked at
	// Shiprocket and not (yet) recorded on the order.
	FulfillmentID int64 `json:"fulfillment_id,omitempty"`
}

const shipmentColumns = `id, order_id, channel_order_id, contents, create_sent, awb_sent,
	coalesce(sr_order_id, 0), coalesce(shipment_id, 0), awb, coalesce(courier_id, 0), courier_name,
	pickup_requested, pickup_scheduled, label_url, manifest_url, invoice_url, cancelled_at, created_at`

func scanShipment(row interface{ Scan(...any) error }) (*shipment, error) {
	var s shipment
	err := row.Scan(&s.ID, &s.OrderID, &s.ChannelOrderID, &s.contents, &s.createSent, &s.awbSent,
		&s.SROrderID, &s.ShipmentID, &s.AWB, &s.CourierID, &s.CourierName,
		&s.PickupRequested, &s.PickupScheduled, &s.LabelURL, &s.ManifestURL, &s.InvoiceURL,
		&s.CancelledAt, &s.CreatedAt)
	return &s, err
}

func (m *Module) shipmentsOf(ctx context.Context, orderID int64) ([]*shipment, error) {
	rows, err := m.app.DB().QueryContext(ctx,
		`SELECT `+shipmentColumns+` FROM shiprocket_shipments WHERE order_id = $1 ORDER BY id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*shipment
	for rows.Next() {
		s, err := scanShipment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (m *Module) shipmentByID(ctx context.Context, id int64) (*shipment, error) {
	s, err := scanShipment(m.app.DB().QueryRowContext(ctx,
		`SELECT `+shipmentColumns+` FROM shiprocket_shipments WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("no Shiprocket shipment %d", id)
	}
	return s, err
}

// contentsOf is a parcel's lines as one comparable string.
func contentsOf(lines []gocommerce.ShipLine) string {
	sorted := append([]gocommerce.ShipLine(nil), lines...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].OrderLineID < sorted[j].OrderLineID })
	parts := make([]string, len(sorted))
	for i, l := range sorted {
		parts[i] = strconv.FormatInt(l.OrderLineID, 10) + ":" + strconv.Itoa(l.Quantity)
	}
	return strings.Join(parts, ",")
}

// recordedAs is the engine's fulfillment for a booked parcel: the order's
// shipment through this provider carrying its waybill.
func recordedAs(order *gocommerce.Order, s *shipment) int64 {
	if s.AWB == "" {
		return 0
	}
	for _, f := range order.Fulfillments {
		if f.Provider == "shiprocket" && f.Tracking == s.AWB {
			return f.ID
		}
	}
	return 0
}

// claim finds or makes the row a booking of these contents works on, and takes
// its lease, returning the row as the lease found it and the lease's token.
//
// Contents are the parcel's lines and what the courier is to collect. An
// earlier row the engine never recorded — booked, then refused under the
// order's lock, or a step that failed — with the same contents is the same
// parcel being tried again, so it is reused. One with different contents is
// refused rather than silently reused or abandoned: it is a live Shiprocket
// order, holding other items or telling the courier to collect another
// amount, and somebody should cancel it before a different parcel is booked.
func (m *Module) claim(ctx context.Context, order *gocommerce.Order, contents string) (*shipment, time.Time, error) {
	rows, err := m.shipmentsOf(ctx, order.ID)
	if err != nil {
		return nil, time.Time{}, err
	}
	var pending *shipment
	for _, s := range rows {
		if s.CancelledAt == nil && recordedAs(order, s) == 0 {
			pending = s
		}
	}
	if pending != nil && pending.contents != contents {
		return nil, time.Time{}, gocommerce.Conflictf("Shiprocket order %s was made for different items, or to collect a different amount, "+
			"and was never recorded here. Cancel it under Shiprocket on this order, then ship again", pending.ChannelOrderID)
	}
	if pending == nil {
		// The order's number for people to find it by, and its id, which no
		// other store's order shares — two stores on one Shiprocket account
		// both have a GC-000001. The underscore keeps these apart from the
		// ids this module gave before (the number, then number-2), and the
		// sequence counts cancelled rows, because Shiprocket refuses an id it
		// has seen.
		channel := fmt.Sprintf("%s_%d", order.Number, order.ID)
		if seq := len(rows) + 1; seq > 1 {
			channel = fmt.Sprintf("%s-%d", channel, seq)
		}
		pending, err = scanShipment(m.app.DB().QueryRowContext(ctx, `
			INSERT INTO shiprocket_shipments (order_id, channel_order_id, contents)
			VALUES ($1, $2, $3)
			ON CONFLICT (store_id, channel_order_id) DO UPDATE SET updated_at = shiprocket_shipments.updated_at
			RETURNING `+shipmentColumns, order.ID, channel, contents))
		if err != nil {
			return nil, time.Time{}, err
		}
		if pending.OrderID != order.ID || pending.contents != contents {
			return nil, time.Time{}, gocommerce.Conflictf("Shiprocket order id %s is already taken by another booking", channel)
		}
	}
	return m.lease(ctx, pending.ID)
}

// lease takes a row for one booking or cancellation at a time, and returns the
// row as it is now — a cancellation that finished a moment ago shows — with
// the lease's token. It is a column and not a transaction because what it
// guards is calls to Shiprocket, and rule 5 keeps those outside transactions.
// Ten minutes outlasts the slowest booking (seven calls at thirty seconds,
// and a login); it lapses on its own if the process dies holding it.
func (m *Module) lease(ctx context.Context, id int64) (*shipment, time.Time, error) {
	var until time.Time
	row, err := scanShipment(scanFirst(m.app.DB().QueryRowContext(ctx, `
		UPDATE shiprocket_shipments SET booking_until = now() + interval '10 minutes', updated_at = now()
		WHERE id = $1 AND cancelled_at IS NULL AND (booking_until IS NULL OR booking_until < now())
		RETURNING booking_until, `+shipmentColumns, id), &until))
	if errors.Is(err, sql.ErrNoRows) {
		if s, lerr := m.shipmentByID(ctx, id); lerr == nil && s.CancelledAt != nil {
			return nil, time.Time{}, gocommerce.Conflictf("Shiprocket order %s was cancelled", s.ChannelOrderID)
		}
		return nil, time.Time{}, gocommerce.Conflictf("this parcel is being booked or cancelled with Shiprocket right now; wait a moment and look again")
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	return row, until, nil
}

// release gives a lease back, and only the one this caller took: a lease that
// lapsed and was taken by another request is that request's to give back.
func (m *Module) release(ctx context.Context, id int64, token time.Time) {
	// A fresh context: the lease must be given back even when the request
	// that took it was cancelled.
	_, _ = m.app.DB().ExecContext(detached(ctx),
		`UPDATE shiprocket_shipments SET booking_until = NULL WHERE id = $1 AND booking_until = $2`, id, token)
}

// detached is ctx's store without ctx's cancellation, for writes that must
// land whatever became of the request.
func detached(ctx context.Context) context.Context {
	store, _ := gocommerce.StoreFrom(ctx)
	return gocommerce.WithStore(context.Background(), store)
}

// scanFirst lets a row with one leading column be read by scanShipment.
type firstScanner struct {
	row   interface{ Scan(...any) error }
	first any
}

func scanFirst(row interface{ Scan(...any) error }, first any) firstScanner {
	return firstScanner{row: row, first: first}
}

func (f firstScanner) Scan(dest ...any) error {
	return f.row.Scan(append([]any{f.first}, dest...)...)
}

// save writes the columns a step filled in.
func (m *Module) save(ctx context.Context, id int64, set map[string]any) error {
	cols := make([]string, 0, len(set))
	for k := range set {
		cols = append(cols, k)
	}
	sort.Strings(cols)
	args := []any{id}
	assign := make([]string, 0, len(cols)+1)
	for _, k := range cols {
		args = append(args, set[k])
		assign = append(assign, fmt.Sprintf("%s = $%d", k, len(args)))
	}
	assign = append(assign, "updated_at = now()")
	_, err := m.app.DB().ExecContext(ctx,
		`UPDATE shiprocket_shipments SET `+strings.Join(assign, ", ")+` WHERE id = $1`, args...)
	return err
}
