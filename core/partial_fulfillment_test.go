package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Half an order can go out. These are the ways that can quietly go wrong: a
// parcel recording more than was ordered, a status that disagrees with the
// parcels behind it, and the four readers of the status enum that would invent
// or destroy inventory if they had not learned the sixth value.

type orderSpec struct {
	sku string
	qty int
}

// confirmedOrder places a cash-on-delivery order, one line per spec, and
// returns it confirmed — COD confirms at checkout, which is what makes it
// shippable without a payment step.
func confirmedOrder(t *testing.T, app *App, specs ...orderSpec) *Order {
	t.Helper()
	cart := newCart(t, app)
	for _, s := range specs {
		p := simpleProduct(t, app, s.sku, 1000, s.qty+5)
		addToCart(t, app, cart.Token, p.DefaultVariant().ID, s.qty)
	}
	result, err := app.Order().Checkout(context.Background(), CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if result.Order.Status != OrderConfirmed {
		t.Fatalf("a cash-on-delivery order came back %s, not confirmed", result.Order.Status)
	}
	return result.Order
}

func lineBySKU(t *testing.T, o *Order, sku string) OrderLine {
	t.Helper()
	for _, l := range o.Lines {
		if l.SKU == sku {
			return l
		}
	}
	t.Fatalf("order %s has no line for %s", o.Number, sku)
	return OrderLine{}
}

// shippedUnits is the sum the whole feature turns on, read straight from the
// table rather than through the code that derives the status from it.
func shippedUnits(t *testing.T, app *App, orderID int64) int {
	t.Helper()
	var units int
	if err := app.DB().QueryRowContext(context.Background(), `
		SELECT coalesce(sum(fl.quantity), 0)::int
		FROM fulfillment_lines fl
		JOIN fulfillments f ON f.id = fl.fulfillment_id
		WHERE f.order_id = $1 AND f.status <> 'cancelled'`, orderID).Scan(&units); err != nil {
		t.Fatalf("read shipped units: %v", err)
	}
	return units
}

func countFulfillments(t *testing.T, app *App, orderID int64) int {
	t.Helper()
	var n int
	if err := app.DB().QueryRowContext(context.Background(),
		`SELECT count(*) FROM fulfillments WHERE order_id = $1`, orderID).Scan(&n); err != nil {
		t.Fatalf("count fulfillments: %v", err)
	}
	return n
}

// newestOrderEvent decodes the most recent payload of one event name for one
// order, which is how a consumer sees it.
func newestOrderEvent(t *testing.T, app *App, name string, orderID int64) OrderEvent {
	t.Helper()
	var raw []byte
	if err := app.DB().QueryRowContext(context.Background(), `
		SELECT payload FROM outbox_events
		WHERE event_name = $1 AND aggregate_type = 'order' AND aggregate_id = $2
		ORDER BY id DESC LIMIT 1`, name, orderID).Scan(&raw); err != nil {
		t.Fatalf("read %s payload: %v", name, err)
	}
	var ev OrderEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("decode %s payload: %v", name, err)
	}
	return ev
}

func countOrderEvents(t *testing.T, app *App, orderID int64) int {
	t.Helper()
	var n int
	if err := app.DB().QueryRowContext(context.Background(),
		`SELECT count(*) FROM outbox_events WHERE aggregate_type = 'order' AND aggregate_id = $1`,
		orderID).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return n
}

// TestShipWithNoLinesShipsTheWholeOrder is the compatibility proof: the body
// every client sent before parcels existed still ships the whole order, and
// still writes down what was in it.
func TestShipWithNoLinesShipsTheWholeOrder(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	order := confirmedOrder(t, app, orderSpec{"WHOLE-1", 2}, orderSpec{"WHOLE-2", 3})
	shipped, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{Tracking: "TRACK-1"})
	if err != nil {
		t.Fatalf("ship: %v", err)
	}

	if shipped.Status != OrderShipped {
		t.Errorf("status = %q, want shipped", shipped.Status)
	}
	if len(shipped.Fulfillments) != 1 {
		t.Fatalf("fulfillments = %d, want 1", len(shipped.Fulfillments))
	}
	if got := len(shipped.Fulfillments[0].Lines); got != 2 {
		t.Fatalf("the parcel names %d lines, want both", got)
	}
	for _, l := range shipped.Lines {
		if l.ShippedQuantity != l.Quantity {
			t.Errorf("%s: shipped %d of %d, want all of it", l.SKU, l.ShippedQuantity, l.Quantity)
		}
	}
	// The rows themselves, not just what the loader reported: the Items column
	// and Delete both read them.
	if got := shippedUnits(t, app, order.ID); got != 5 {
		t.Errorf("fulfillment_lines sum to %d units, want 5", got)
	}
}

// TestShipPartOfAnOrder: one line, part of it, and the order says so.
func TestShipPartOfAnOrder(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	order := confirmedOrder(t, app, orderSpec{"PART-1", 3}, orderSpec{"PART-2", 1})
	first := lineBySKU(t, order, "PART-1")

	out, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{
		Tracking: "TRACK-1",
		Lines:    []ShipLine{{OrderLineID: first.ID, Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("ship one unit: %v", err)
	}

	if out.Status != OrderPartial {
		t.Errorf("status = %q, want partial", out.Status)
	}
	if len(out.Fulfillments) != 1 {
		t.Fatalf("fulfillments = %d, want 1", len(out.Fulfillments))
	}
	lines := out.Fulfillments[0].Lines
	if len(lines) != 1 {
		t.Fatalf("the parcel names %d lines, want only the one that went in it", len(lines))
	}
	if lines[0].Quantity != 1 || lines[0].SKU != "PART-1" {
		t.Errorf("parcel line = %d x %q, want 1 x PART-1", lines[0].Quantity, lines[0].SKU)
	}
	if got := lineBySKU(t, out, "PART-1").ShippedQuantity; got != 1 {
		t.Errorf("PART-1 shipped_quantity = %d, want 1", got)
	}
	if got := lineBySKU(t, out, "PART-2").ShippedQuantity; got != 0 {
		t.Errorf("PART-2 shipped_quantity = %d, want none of it gone", got)
	}
}

// TestShippingTheRemainderCompletesTheOrder: the same empty request, sent
// again, ships exactly what is left rather than the original quantities.
func TestShippingTheRemainderCompletesTheOrder(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	order := confirmedOrder(t, app, orderSpec{"REST-1", 3}, orderSpec{"REST-2", 1})
	first := lineBySKU(t, order, "REST-1")
	if _, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{
		Tracking: "TRACK-1",
		Lines:    []ShipLine{{OrderLineID: first.ID, Quantity: 1}},
	}); err != nil {
		t.Fatalf("first parcel: %v", err)
	}

	done, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{Tracking: "TRACK-2"})
	if err != nil {
		t.Fatalf("ship the rest: %v", err)
	}
	if done.Status != OrderShipped {
		t.Errorf("status = %q, want shipped", done.Status)
	}
	if len(done.Fulfillments) != 2 {
		t.Errorf("fulfillments = %d, want two parcels", len(done.Fulfillments))
	}
	if got := shippedUnits(t, app, order.ID); got != 4 {
		t.Errorf("shipped units = %d, want exactly what was ordered", got)
	}

	if _, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{Tracking: "TRACK-3"}); err == nil {
		t.Error("a third shipment was accepted against an order with nothing left to ship")
	} else if !Conflictf("").Is(err) {
		t.Errorf("error = %v, want a conflict", err)
	}
}

// TestShipRefusesMoreThanIsLeft: four wrong requests, each a 400 rather than a
// 409 — a conflict would tell the panel to stop retrying a request that is
// simply wrong — and each leaving no parcel behind.
func TestShipRefusesMoreThanIsLeft(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	order := confirmedOrder(t, app, orderSpec{"LEFT-1", 2})
	line := lineBySKU(t, order, "LEFT-1")
	other := confirmedOrder(t, app, orderSpec{"LEFT-2", 1})
	foreign := lineBySKU(t, other, "LEFT-2")

	cases := []struct {
		name  string
		lines []ShipLine
	}{
		{"more than the order owes", []ShipLine{{OrderLineID: line.ID, Quantity: 3}}},
		{"a line from another order", []ShipLine{{OrderLineID: foreign.ID, Quantity: 1}}},
		{"the same line twice", []ShipLine{
			{OrderLineID: line.ID, Quantity: 1}, {OrderLineID: line.ID, Quantity: 1},
		}},
		{"a quantity of zero", []ShipLine{{OrderLineID: line.ID, Quantity: 0}}},
	}
	for _, c := range cases {
		_, err := app.Ship().Create(ctx, order.ID, ProviderManual,
			ShipRequest{Tracking: "TRACK-1", Lines: c.lines})
		if err == nil {
			t.Errorf("%s was accepted", c.name)
			continue
		}
		if !Validationf("").Is(err) {
			t.Errorf("%s: error = %v, want a validation failure", c.name, err)
		}
	}
	if got := countFulfillments(t, app, order.ID); got != 0 {
		t.Errorf("%d parcel(s) survived a refused request", got)
	}
}

// blockingShipper holds a shipment inside the carrier call, where the engine
// deliberately has no lock, so a test can decide what happens in that window.
type blockingShipper struct {
	inside  chan struct{}
	release chan struct{}
}

func (blockingShipper) Code() string { return "blocking" }

func (b *blockingShipper) Ship(ctx context.Context, o *Order, req ShipRequest) (Shipment, error) {
	b.inside <- struct{}{}
	<-b.release
	return Shipment{Provider: "blocking", Tracking: req.Tracking}, nil
}

// TestShipRefusesWhenAnotherParcelTookTheUnits is the one test that tells
// refusing apart from silently re-resolving. The carrier call is held open
// while another shipment takes the units, so the transaction wakes to an order
// that is still shippable but can no longer carry the parcel that was booked.
func TestShipRefusesWhenAnotherParcelTookTheUnits(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	blocker := &blockingShipper{inside: make(chan struct{}), release: make(chan struct{})}
	app.RegisterFulfillment(blocker)

	order := confirmedOrder(t, app, orderSpec{"BOOKED-1", 2})
	line := lineBySKU(t, order, "BOOKED-1")

	booked := make(chan error, 1)
	go func() {
		_, err := app.Ship().Create(ctx, order.ID, "blocking", ShipRequest{
			Tracking: "TRACK-BOOKED",
			Lines:    []ShipLine{{OrderLineID: line.ID, Quantity: 2}},
		})
		booked <- err
	}()

	<-blocker.inside // the label exists at the carrier; the engine holds no lock
	if _, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{
		Tracking: "TRACK-1",
		Lines:    []ShipLine{{OrderLineID: line.ID, Quantity: 1}},
	}); err != nil {
		t.Fatalf("the competing parcel: %v", err)
	}
	close(blocker.release)

	err := <-booked
	if err == nil {
		t.Fatal("a parcel was recorded for units another shipment had already taken")
	}
	if !Conflictf("").Is(err) {
		t.Errorf("error = %v, want a conflict — a label may already exist for it", err)
	}
	if !strings.Contains(err.Error(), "label") {
		t.Errorf("error = %v, want it to warn that a label may already have been booked", err)
	}
	if got := shippedUnits(t, app, order.ID); got != 1 {
		t.Errorf("shipped units = %d, want only the parcel that won", got)
	}
	if got := countFulfillments(t, app, order.ID); got != 1 {
		t.Errorf("%d parcels survived, want only the one that was recorded", got)
	}
}

// TestConcurrentShipmentsCannotOversend is the invariant under load: however
// the refusals fall out — a stale read is a 400 before the lock, losing the
// race is a 409 under it — no more units leave than were ordered.
func TestConcurrentShipmentsCannotOversend(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	order := confirmedOrder(t, app, orderSpec{"RACE-1", 1})
	line := lineBySKU(t, order, "RACE-1")

	const packers = 8
	var succeeded, refused int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < packers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{
				Tracking: "TRACK-1",
				Lines:    []ShipLine{{OrderLineID: line.ID, Quantity: 1}},
			})
			switch {
			case err == nil:
				atomic.AddInt64(&succeeded, 1)
			case Conflictf("").Is(err), Validationf("").Is(err):
				atomic.AddInt64(&refused, 1)
			default:
				t.Errorf("unexpected shipping error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if succeeded != 1 {
		t.Errorf("%d shipments succeeded for 1 unit, want exactly 1", succeeded)
	}
	if refused != packers-1 {
		t.Errorf("%d shipments were refused, want %d", refused, packers-1)
	}
	if got := shippedUnits(t, app, order.ID); got != 1 {
		t.Errorf("shipped units = %d, want 1 — more went out than was ordered", got)
	}
}

// TestPartlyShippedOrderRefusesWhatItShould covers the three refusals, and the
// stock assertion is the load-bearing one: stockCommitted now answers true for
// partial, so a missing case would restock units that are in a van.
func TestPartlyShippedOrderRefusesWhatItShould(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	order := confirmedOrder(t, app, orderSpec{"REFUSE-1", 3})
	line := lineBySKU(t, order, "REFUSE-1")
	variantID := *line.VariantID
	if _, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{
		Tracking: "TRACK-1",
		Lines:    []ShipLine{{OrderLineID: line.ID, Quantity: 1}},
	}); err != nil {
		t.Fatalf("ship one unit: %v", err)
	}
	onHand, reserved := variantStock(t, app, variantID)

	if _, err := app.Order().Cancel(ctx, order.ID, "changed their mind"); err == nil {
		t.Error("a partly shipped order was cancelled")
	} else if !Conflictf("").Is(err) || !strings.Contains(err.Error(), "return") {
		t.Errorf("cancel error = %v, want a conflict that says it is a return", err)
	}
	if _, _, err := app.Order().EditLines(ctx, order.ID, OrderEdit{
		Lines: []OrderLineEdit{{ID: line.ID, Quantity: 1}},
	}); err == nil {
		t.Error("a partly shipped order had its lines edited")
	} else if !Conflictf("").Is(err) {
		t.Errorf("edit error = %v, want a conflict", err)
	}
	if _, err := app.Order().MarkDelivered(ctx, order.ID); err == nil {
		t.Error("a partly shipped order was marked delivered")
	} else if !strings.Contains(err.Error(), "partly shipped") {
		t.Errorf("deliver error = %v, want it to say the rest has to go out first", err)
	}

	after, err := app.Order().Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Status != OrderPartial {
		t.Errorf("status = %q, want it still partial", after.Status)
	}
	nowOnHand, nowReserved := variantStock(t, app, variantID)
	if nowOnHand != onHand || nowReserved != reserved {
		t.Errorf("stock moved from (%d, %d) to (%d, %d) on refused operations",
			onHand, reserved, nowOnHand, nowReserved)
	}
}

// TestConfirmingAPartlyShippedOrderIsSilent: Confirm called directly, which is
// the path MarkPaid would not exercise. Without partial in the no-op arm this
// commits the shelf a second time and rewinds the status.
func TestConfirmingAPartlyShippedOrderIsSilent(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	order := confirmedOrder(t, app, orderSpec{"RECONF-1", 2})
	line := lineBySKU(t, order, "RECONF-1")
	if _, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{
		Tracking: "TRACK-1",
		Lines:    []ShipLine{{OrderLineID: line.ID, Quantity: 1}},
	}); err != nil {
		t.Fatalf("ship one unit: %v", err)
	}
	onHand, reserved := variantStock(t, app, *line.VariantID)
	events := countOrderEvents(t, app, order.ID)

	again, err := app.Order().Confirm(ctx, order.ID)
	if err != nil {
		t.Fatalf("confirming a partly shipped order: %v", err)
	}
	if again.Status != OrderPartial {
		t.Errorf("status = %q, want it still partial", again.Status)
	}
	if nowOnHand, nowReserved := variantStock(t, app, *line.VariantID); nowOnHand != onHand || nowReserved != reserved {
		t.Errorf("stock moved from (%d, %d) to (%d, %d) on a replayed confirmation",
			onHand, reserved, nowOnHand, nowReserved)
	}
	if got := countOrderEvents(t, app, order.ID); got != events {
		t.Errorf("%d event(s) were written by a transition that did nothing", got-events)
	}
}

// TestDeletingOneOfTwoShipmentsGoesBackToPartial is the case the old count of
// live fulfillments could not express.
func TestDeletingOneOfTwoShipmentsGoesBackToPartial(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	order := confirmedOrder(t, app, orderSpec{"UNSHIP-1", 3})
	line := lineBySKU(t, order, "UNSHIP-1")
	if _, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{
		Tracking: "TRACK-1",
		Lines:    []ShipLine{{OrderLineID: line.ID, Quantity: 1}},
	}); err != nil {
		t.Fatalf("first parcel: %v", err)
	}
	full, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{Tracking: "TRACK-2"})
	if err != nil {
		t.Fatalf("second parcel: %v", err)
	}
	if full.Status != OrderShipped || len(full.Fulfillments) != 2 {
		t.Fatalf("after two parcels = %s with %d shipments", full.Status, len(full.Fulfillments))
	}

	back, err := app.Ship().Delete(ctx, full.Fulfillments[1].ID)
	if err != nil {
		t.Fatalf("delete the second parcel: %v", err)
	}
	if back.Status != OrderPartial {
		t.Errorf("status = %q, want partial — one parcel is still out there", back.Status)
	}
	if got := lineBySKU(t, back, "UNSHIP-1").ShippedQuantity; got != 1 {
		t.Errorf("shipped_quantity = %d, want only what the first parcel held", got)
	}

	empty, err := app.Ship().Delete(ctx, full.Fulfillments[0].ID)
	if err != nil {
		t.Fatalf("delete the first parcel: %v", err)
	}
	if empty.Status != OrderConfirmed {
		t.Errorf("status = %q, want confirmed — nothing is shipping it any more", empty.Status)
	}
	if _, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{Tracking: "TRACK-3"}); err != nil {
		t.Fatalf("ship again after removing every parcel: %v", err)
	}
}

// TestDeleteFulfillmentLeavesAClosedOrderAlone proves the guard was widened
// rather than replaced: an order that is neither shipped nor partial keeps
// whatever status it has.
func TestDeleteFulfillmentLeavesAClosedOrderAlone(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	order := confirmedOrder(t, app, orderSpec{"CLOSED-1", 1})
	shipped, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{Tracking: "TRACK-1"})
	if err != nil {
		t.Fatalf("ship: %v", err)
	}

	// A cancelled order carrying a fulfillment row is not reachable through the
	// engine — which is exactly why the guard exists, and why the only way to
	// set this up is the way somebody would have caused it.
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE orders SET status = 'cancelled' WHERE id = $1`, order.ID); err != nil {
		t.Fatalf("close the order by hand: %v", err)
	}
	after, err := app.Ship().Delete(ctx, shipped.Fulfillments[0].ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if after.Status != OrderCancelled {
		t.Errorf("status = %q, want the cancelled order left where it was", after.Status)
	}
}

// TestShippedEventCarriesTheParcel: every existing field of order.shipped is
// unchanged, and the new block says which parcel this was.
func TestShippedEventCarriesTheParcel(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	order := confirmedOrder(t, app, orderSpec{"EVENT-1", 3}, orderSpec{"EVENT-2", 1})
	first := lineBySKU(t, order, "EVENT-1")
	if _, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{
		Tracking: "1Z999AA10123456784",
		Lines:    []ShipLine{{OrderLineID: first.ID, Quantity: 2}},
	}); err != nil {
		t.Fatalf("ship two units: %v", err)
	}

	ev := newestOrderEvent(t, app, EventOrderShipped, order.ID)
	if ev.Status != OrderPartial {
		t.Errorf("payload status = %q, want partial", ev.Status)
	}
	if ev.Tracking != "1Z999AA10123456784" {
		t.Errorf("tracking = %q, want what was typed", ev.Tracking)
	}
	if ev.Extra["provider"] != ProviderManual {
		t.Errorf("extra.provider = %q, want the provider that booked it", ev.Extra["provider"])
	}
	// The contract that must not move: top-level lines is still the order.
	if len(ev.Lines) != 2 {
		t.Errorf("payload lines = %d, want the whole order as before", len(ev.Lines))
	}
	if ev.Shipment == nil {
		t.Fatal("order.shipped carries no shipment block")
	}
	if ev.Shipment.RemainingUnits != 2 {
		t.Errorf("remaining_units = %d, want 2 still owed", ev.Shipment.RemainingUnits)
	}
	if len(ev.Shipment.Lines) != 1 || ev.Shipment.Lines[0].SKU != "EVENT-1" ||
		ev.Shipment.Lines[0].Quantity != 2 {
		t.Errorf("shipment lines = %+v, want 2 x EVENT-1", ev.Shipment.Lines)
	}
	if ev.Shipment.Carrier != "ups" {
		t.Errorf("shipment carrier = %q, want the one the number names", ev.Shipment.Carrier)
	}

	if _, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{Tracking: "TRACK-2"}); err != nil {
		t.Fatalf("ship the rest: %v", err)
	}
	done := newestOrderEvent(t, app, EventOrderShipped, order.ID)
	if done.Status != OrderShipped {
		t.Errorf("completing payload status = %q, want shipped", done.Status)
	}
	if done.Shipment == nil || done.Shipment.RemainingUnits != 0 {
		t.Errorf("completing shipment = %+v, want nothing still owed", done.Shipment)
	}
}

// TestUnshippedEventNamesTheParcelThatCameBack: without the block a consumer
// cannot tell which of two parcels was removed.
func TestUnshippedEventNamesTheParcelThatCameBack(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	order := confirmedOrder(t, app, orderSpec{"CAMEBACK-1", 3})
	line := lineBySKU(t, order, "CAMEBACK-1")
	if _, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{
		Tracking: "TRACK-1",
		Lines:    []ShipLine{{OrderLineID: line.ID, Quantity: 1}},
	}); err != nil {
		t.Fatalf("first parcel: %v", err)
	}
	full, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{Tracking: "TRACK-2"})
	if err != nil {
		t.Fatalf("second parcel: %v", err)
	}
	if _, err := app.Ship().Delete(ctx, full.Fulfillments[1].ID); err != nil {
		t.Fatalf("delete the second parcel: %v", err)
	}

	ev := newestOrderEvent(t, app, EventOrderUnshipped, order.ID)
	if ev.Status != OrderPartial {
		t.Errorf("payload status = %q, want partial", ev.Status)
	}
	if ev.Shipment == nil {
		t.Fatal("order.unshipped carries no shipment block")
	}
	if ev.Shipment.FulfillmentID != full.Fulfillments[1].ID {
		t.Errorf("fulfillment_id = %d, want the parcel that was removed", ev.Shipment.FulfillmentID)
	}
	if len(ev.Shipment.Lines) != 1 || ev.Shipment.Lines[0].Quantity != 2 {
		t.Errorf("shipment lines = %+v, want the 2 units that came back", ev.Shipment.Lines)
	}
	if ev.Shipment.RemainingUnits != 2 {
		t.Errorf("remaining_units = %d, want what the order owes now", ev.Shipment.RemainingUnits)
	}
}

// TestCreateFulfillmentAcceptsLinesOverHTTP is the only thing that exercises
// the DTO wiring, including the JSON error envelope on a bad line.
func TestCreateFulfillmentAcceptsLinesOverHTTP(t *testing.T) {
	app := newTestApp(t)

	order := confirmedOrder(t, app, orderSpec{"HTTP-1", 2}, orderSpec{"HTTP-2", 1})
	first := lineBySKU(t, order, "HTTP-1")

	rec := do(t, app, "POST", "/api/admin/create-fulfillment", withAdmin, jsonBody(t, map[string]any{
		"order_id": order.ID,
		"provider": ProviderManual,
		"tracking": "TRACK-1",
		"lines":    []map[string]any{{"order_line_id": first.ID, "quantity": 1}},
	}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body)
	}
	var partial Order
	decodeData(t, rec, &partial)
	if partial.Status != OrderPartial {
		t.Errorf("status = %q, want partial", partial.Status)
	}
	if len(partial.Fulfillments) != 1 || len(partial.Fulfillments[0].Lines) != 1 {
		t.Fatalf("the parcel's lines did not round-trip: %+v", partial.Fulfillments)
	}
	if partial.Fulfillments[0].Lines[0].SKU != "HTTP-1" {
		t.Errorf("parcel line sku = %q, want HTTP-1", partial.Fulfillments[0].Lines[0].SKU)
	}

	// An unknown line is a 400 in the envelope, not a Go default.
	bad := do(t, app, "POST", "/api/admin/create-fulfillment", withAdmin, jsonBody(t, map[string]any{
		"order_id": order.ID,
		"lines":    []map[string]any{{"order_line_id": 999999, "quantity": 1}},
	}))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", bad.Code, bad.Body)
	}
	if err := decodeError(t, bad); err.Code != "validation_failed" {
		t.Errorf("error code = %q, want validation_failed", err.Code)
	}

	// No lines at all ships the remainder, and a further one has nothing left.
	rest := do(t, app, "POST", "/api/admin/create-fulfillment", withAdmin, jsonBody(t, map[string]any{
		"order_id": order.ID, "tracking": "TRACK-2",
	}))
	if rest.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rest.Code, rest.Body)
	}
	var done Order
	decodeData(t, rest, &done)
	if done.Status != OrderShipped {
		t.Errorf("status = %q, want shipped", done.Status)
	}
	again := do(t, app, "POST", "/api/admin/create-fulfillment", withAdmin, jsonBody(t, map[string]any{
		"order_id": order.ID, "tracking": "TRACK-3",
	}))
	if again.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (body %s)", again.Code, again.Body)
	}
}

// TestOrderLinesReportWhatHasShipped: all three read paths go through
// loadChildren, so all three have to agree.
func TestOrderLinesReportWhatHasShipped(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	order := confirmedOrder(t, app, orderSpec{"REPORT-1", 4})
	line := lineBySKU(t, order, "REPORT-1")
	if got := lineBySKU(t, order, "REPORT-1").ShippedQuantity; got != 0 {
		t.Errorf("an unshipped order reports %d units gone", got)
	}

	shipped, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{
		Tracking: "TRACK-1",
		Lines:    []ShipLine{{OrderLineID: line.ID, Quantity: 3}},
	})
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	if got := lineBySKU(t, shipped, "REPORT-1").ShippedQuantity; got != 3 {
		t.Errorf("Get reports %d units gone, want 3", got)
	}

	listed, _, err := app.Order().List(ctx, OrderQuery{Status: OrderPartial})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("the partial filter returned %d orders, want 1", len(listed))
	}
	if got := lineBySKU(t, listed[0], "REPORT-1").ShippedQuantity; got != 3 {
		t.Errorf("List reports %d units gone, want 3", got)
	}

	guest, err := app.Order().GetForGuest(ctx, order.Number, order.AccessToken)
	if err != nil {
		t.Fatalf("guest lookup: %v", err)
	}
	if got := lineBySKU(t, guest, "REPORT-1").ShippedQuantity; got != 3 {
		t.Errorf("the guest lookup reports %d units gone, want 3", got)
	}

	if _, err := app.Ship().Delete(ctx, shipped.Fulfillments[0].ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	back, err := app.Order().Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := lineBySKU(t, back, "REPORT-1").ShippedQuantity; got != 0 {
		t.Errorf("after removing the parcel the line still reports %d units gone", got)
	}
}

// TestOrderStatusPartialIsStorable is the cheap check that the widened CHECK
// actually took. A mis-guessed constraint name would otherwise fail silently at
// migration time on somebody else's database.
func TestOrderStatusPartialIsStorable(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	order := confirmedOrder(t, app, orderSpec{"CHECK-1", 1})
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE orders SET status = 'partial' WHERE id = $1`, order.ID); err != nil {
		t.Fatalf("the orders CHECK does not admit partial: %v", err)
	}
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE orders SET status = 'nonsense' WHERE id = $1`, order.ID); err == nil {
		t.Error("the orders CHECK admits anything at all")
	}
}

// TestFulfillmentLinesCoverEveryShipment is the standing invariant the backfill
// exists to establish for rows written before the table did.
func TestFulfillmentLinesCoverEveryShipment(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	order := confirmedOrder(t, app, orderSpec{"COVER-1", 2}, orderSpec{"COVER-2", 2})
	if _, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{Tracking: "TRACK-1"}); err != nil {
		t.Fatalf("ship: %v", err)
	}

	var uncovered int
	if err := app.DB().QueryRowContext(ctx, `
		SELECT count(*)
		FROM fulfillments f
		WHERE f.status <> 'cancelled'
		  AND coalesce((SELECT sum(fl.quantity) FROM fulfillment_lines fl
		                 WHERE fl.fulfillment_id = f.id), 0)
		      <> (SELECT sum(ol.quantity) FROM order_lines ol WHERE ol.order_id = f.order_id)`,
	).Scan(&uncovered); err != nil {
		t.Fatalf("check coverage: %v", err)
	}
	if uncovered != 0 {
		t.Errorf("%d whole-order shipment(s) do not say what was in them", uncovered)
	}
}
