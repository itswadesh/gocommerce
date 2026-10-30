package gocommerce

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func eventsNamed(t *testing.T, app *App, orderID int64, name string) []OrderEvent {
	t.Helper()
	rows, err := app.DB().QueryContext(context.Background(), `
		SELECT payload FROM outbox_events
		WHERE event_name = $1 AND aggregate_id = $2 ORDER BY id`, name, orderID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []OrderEvent
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var p OrderEvent
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func parcelOf(t *testing.T, app *App, orderID, id int64) Fulfillment {
	t.Helper()
	o, err := app.Order().Get(context.Background(), orderID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range o.Fulfillments {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("order %d has no fulfillment %d", orderID, id)
	return Fulfillment{}
}

// A carrier's progress reaches the order: each new state once, delivery as
// the order's own delivery, and nothing after it moves the parcel back.
func TestCarrierUpdatesMoveTheParcelAndAnnounceIt(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := confirmedOrder(t, app, orderSpec{"TRACK-A", 1})
	shipped, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{Tracking: "EE123456789IN"})
	if err != nil {
		t.Fatal(err)
	}
	parcel := shipped.Fulfillments[0].ID
	at := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)

	for _, status := range []string{TrackingInTransit, TrackingInTransit, TrackingOutForDelivery} {
		if _, err := app.Ship().Track(ctx, parcel, TrackingUpdate{Status: status, Detail: "Bag received", At: at}); err != nil {
			t.Fatalf("track %s: %v", status, err)
		}
	}
	updates := eventsNamed(t, app, order.ID, EventOrderShipmentUpdated)
	if len(updates) != 2 {
		t.Fatalf("%d shipment updates, want 2: the repeated in-transit says nothing new", len(updates))
	}
	if s := updates[1].Shipment; s == nil || s.FulfillmentID != parcel || s.TrackingStatus != TrackingOutForDelivery {
		t.Errorf("second update = %+v, want parcel %d out for delivery", updates[1].Shipment, parcel)
	}
	if got := parcelOf(t, app, order.ID, parcel).TrackingStatus; got != TrackingOutForDelivery {
		t.Errorf("tracking status = %q", got)
	}
	// The order's history says what the carrier said.
	history, _, err := app.Order().TimelineNewestFirst(ctx, order.ID, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if h := history[0]; h.Name != EventOrderShipmentUpdated || h.TrackingStatus != TrackingOutForDelivery || h.Reason != "Bag received" {
		t.Errorf("newest history entry = %+v", h)
	}

	delivered, err := app.Ship().Track(ctx, parcel, TrackingUpdate{Status: TrackingDelivered, At: at})
	if err != nil {
		t.Fatal(err)
	}
	if delivered.Status != OrderDelivered || len(eventsNamed(t, app, order.ID, EventOrderDelivered)) != 1 {
		t.Errorf("order = %s with %d order.delivered; want delivered, announced once",
			delivered.Status, len(eventsNamed(t, app, order.ID, EventOrderDelivered)))
	}
	// A late scan from the van does not undo the delivery.
	again, err := app.Ship().Track(ctx, parcel, TrackingUpdate{Status: TrackingInTransit, At: at})
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != OrderDelivered || parcelOf(t, app, order.ID, parcel).TrackingStatus != TrackingDelivered {
		t.Errorf("after a late scan: order %s, parcel %s", again.Status, parcelOf(t, app, order.ID, parcel).TrackingStatus)
	}
}

// One parcel of two arriving is that parcel delivered, not the order.
func TestOneParcelOfTwoDeliveredLeavesTheOrderShipped(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := confirmedOrder(t, app, orderSpec{"TRACK-B1", 1}, orderSpec{"TRACK-B2", 1})
	first, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{
		Lines: []ShipLine{{OrderLineID: lineBySKU(t, order, "TRACK-B1").ID, Quantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{})
	if err != nil {
		t.Fatal(err)
	}
	p1, p2 := first.Fulfillments[0].ID, second.Fulfillments[1].ID

	after, err := app.Ship().Track(ctx, p1, TrackingUpdate{Status: TrackingDelivered})
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != OrderShipped || parcelOf(t, app, order.ID, p1).Status != "delivered" {
		t.Errorf("one of two delivered: order %s, parcel %s", after.Status, parcelOf(t, app, order.ID, p1).Status)
	}
	after, err = app.Ship().Track(ctx, p2, TrackingUpdate{Status: TrackingDelivered})
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != OrderDelivered {
		t.Errorf("both delivered: order %s, want delivered", after.Status)
	}
}

// Failed attempts and a parcel on its way back are recorded and announced;
// neither is a delivery.
func TestFailedDeliveryAndReturnToOriginAreNotDelivery(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := confirmedOrder(t, app, orderSpec{"TRACK-C", 1})
	shipped, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{})
	if err != nil {
		t.Fatal(err)
	}
	parcel := shipped.Fulfillments[0].ID
	for _, status := range []string{TrackingDeliveryFailed, TrackingReturning, TrackingReturned} {
		o, err := app.Ship().Track(ctx, parcel, TrackingUpdate{Status: status, Detail: "Customer not available"})
		if err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		if o.Status != OrderShipped {
			t.Errorf("%s left the order %s, want shipped", status, o.Status)
		}
	}
	if n := len(eventsNamed(t, app, order.ID, EventOrderShipmentUpdated)); n != 3 {
		t.Errorf("%d updates, want 3", n)
	}
	if got := parcelOf(t, app, order.ID, parcel); got.TrackingStatus != TrackingReturned || got.Status != "shipped" {
		t.Errorf("parcel = %s / %s, want returned to the store and still the order's shipment", got.TrackingStatus, got.Status)
	}
}

// A carrier calling the parcel off is the same thing as cancelling it here.
func TestACarrierCancellationCancelsTheParcel(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := confirmedOrder(t, app, orderSpec{"TRACK-D", 1})
	shipped, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{})
	if err != nil {
		t.Fatal(err)
	}
	o, err := app.Ship().Track(ctx, shipped.Fulfillments[0].ID, TrackingUpdate{Status: TrackingCancelled, Detail: "Cancelled by the courier"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != OrderConfirmed || o.Fulfillments[0].Status != "cancelled" {
		t.Errorf("order %s, parcel %s; want confirmed and cancelled", o.Status, o.Fulfillments[0].Status)
	}
	if n := len(eventsNamed(t, app, order.ID, EventOrderUnshipped)); n != 1 {
		t.Errorf("%d order.unshipped, want 1", n)
	}
}

func TestATrackingStatusTheEngineDoesNotKnowIsRefused(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := confirmedOrder(t, app, orderSpec{"TRACK-E", 1})
	shipped, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Ship().Track(ctx, shipped.Fulfillments[0].ID, TrackingUpdate{Status: "teleported"}); err == nil {
		t.Error("an unknown status was recorded")
	}
}

// trackingCarrier is a provider whose carrier calls back.
type trackingCarrier struct {
	manualFulfillment
	hits []string
}

func (c *trackingCarrier) Code() string         { return "calls-back" }
func (c *trackingCarrier) TrackingHook() string { return "parcel-updates" }
func (c *trackingCarrier) Tracking() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.hits = append(c.hits, string(body))
		Respond(w, http.StatusOK, map[string]bool{"ok": true})
	})
}

type carrierModule struct{ c *trackingCarrier }

func (m carrierModule) Name() string            { return "calls-back" }
func (m carrierModule) Migrations() []Migration { return nil }
func (m carrierModule) Register(app *App) error { app.RegisterFulfillment(m.c); return nil }

// A carrier names no store a resolver understands, so its callback is served
// before one is known, and the provider binds the store itself.
func TestACarrierCallbackIsServedWithoutAStore(t *testing.T) {
	c := &trackingCarrier{}
	app := newTestApp(t, carrierModule{c})
	addTestStore(t, app, "second-shop")

	req := httptest.NewRequest(http.MethodPost, "/api/tracking-updates/parcel-updates", strings.NewReader(`{"awb":"1"}`))
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || len(c.hits) != 1 {
		t.Fatalf("callback = %d %s, hits %d", rec.Code, rec.Body, len(c.hits))
	}
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/tracking-updates/nobody", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("an unknown hook = %d, want 404", rec.Code)
	}
}

// Carriers' reports arrive out of order. One older than what the parcel
// already shows changes nothing.
func TestALateUpdateDoesNotUndoANewerOne(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := confirmedOrder(t, app, orderSpec{"TRACK-LATE", 1})
	shipped, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{})
	if err != nil {
		t.Fatal(err)
	}
	parcel := shipped.Fulfillments[0].ID
	newer := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	if _, err := app.Ship().Track(ctx, parcel, TrackingUpdate{Status: TrackingOutForDelivery, At: newer}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Ship().Track(ctx, parcel, TrackingUpdate{Status: TrackingInTransit, At: newer.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got := parcelOf(t, app, order.ID, parcel).TrackingStatus; got != TrackingOutForDelivery {
		t.Errorf("after a late report the parcel is %q, want out for delivery", got)
	}
	if n := len(eventsNamed(t, app, order.ID, EventOrderShipmentUpdated)); n != 1 {
		t.Errorf("%d updates, want 1: the late report said nothing new", n)
	}
}

// A delivery taken back — the customer says it never came — leaves the parcel
// open for the carrier's real delivery later.
func TestAnUndoneDeliveryCanBeDeliveredAgain(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := confirmedOrder(t, app, orderSpec{"TRACK-UNDO", 1})
	shipped, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{})
	if err != nil {
		t.Fatal(err)
	}
	parcel := shipped.Fulfillments[0].ID
	if _, err := app.Ship().Track(ctx, parcel, TrackingUpdate{Status: TrackingDelivered}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Order().MarkUndelivered(ctx, order.ID); err != nil {
		t.Fatal(err)
	}
	o, err := app.Ship().Track(ctx, parcel, TrackingUpdate{Status: TrackingDelivered})
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != OrderDelivered {
		t.Errorf("the carrier's second delivery left the order %s", o.Status)
	}
}
