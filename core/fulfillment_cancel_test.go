package gocommerce

import (
	"context"
	"encoding/json"
	"testing"
)

func unshippedEvents(t *testing.T, app *App, orderID int64) []OrderEvent {
	t.Helper()
	rows, err := app.DB().QueryContext(context.Background(), `
		SELECT payload FROM outbox_events
		WHERE event_name = $1 AND aggregate_id = $2 ORDER BY id`, EventOrderUnshipped, orderID)
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

// A carrier cancelling a booking is not a record entered in error, so the
// parcel stays on the order, marked cancelled, and what it held is owed again.
func TestCancelledShipmentStaysOnTheOrderAndItsUnitsAreOwedAgain(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := confirmedOrder(t, app, orderSpec{"CANCEL-A", 2})

	shipped, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{Tracking: "EE123456789IN"})
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	parcel := shipped.Fulfillments[0].ID

	back, err := app.Ship().Cancel(ctx, parcel, "the courier never came")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if back.Status != OrderConfirmed {
		t.Errorf("order = %s, want confirmed: nothing is carrying it now", back.Status)
	}
	if len(back.Fulfillments) != 1 || back.Fulfillments[0].Status != "cancelled" {
		t.Fatalf("fulfillments = %+v, want the one parcel, cancelled", back.Fulfillments)
	}
	events := unshippedEvents(t, app, order.ID)
	if len(events) != 1 || events[0].Shipment == nil || events[0].Shipment.FulfillmentID != parcel {
		t.Fatalf("order.unshipped events = %+v, want one naming parcel %d", events, parcel)
	}
	if events[0].Reason != "the courier never came" || events[0].Shipment.RemainingUnits != 2 {
		t.Errorf("event = reason %q, remaining %d; want the reason and both units owed",
			events[0].Reason, events[0].Shipment.RemainingUnits)
	}

	// A webhook and a person can both report it: the second says nothing new.
	if _, err := app.Ship().Cancel(ctx, parcel, ""); err != nil {
		t.Fatalf("cancel again: %v", err)
	}
	if n := len(unshippedEvents(t, app, order.ID)); n != 1 {
		t.Errorf("cancelling twice wrote %d events, want 1", n)
	}

	if _, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{Tracking: "EE987654321IN"}); err != nil {
		t.Fatalf("ship again after the cancellation: %v", err)
	}
	again, _ := app.Order().Get(ctx, order.ID)
	if again.Status != OrderShipped {
		t.Errorf("after the second parcel the order is %s, want shipped", again.Status)
	}
}

// A parcel somebody received cannot be cancelled after the fact.
func TestADeliveredShipmentCannotBeCancelled(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := confirmedOrder(t, app, orderSpec{"CANCEL-B", 1})
	shipped, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{})
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	if _, err := app.Order().MarkDelivered(ctx, order.ID); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if _, err := app.Ship().Cancel(ctx, shipped.Fulfillments[0].ID, ""); err == nil {
		t.Error("a delivered parcel was cancelled")
	}
	if _, err := app.Ship().Cancel(ctx, 987654, ""); err == nil {
		t.Error("cancelling a fulfillment that does not exist was allowed")
	}
}

// Parcel is what a carrier module quotes on before anything is booked: the
// same figure Create hands the provider.
func TestParcelIsWhatTheShipmentWouldWeigh(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := confirmedOrder(t, app, orderSpec{"PARCEL-A", 3})
	grams := 400
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE variants SET weight_grams = $1 WHERE sku = 'PARCEL-A'`, grams); err != nil {
		t.Fatal(err)
	}
	parcel, err := app.Ship().Parcel(ctx, order, nil)
	if err != nil {
		t.Fatalf("parcel: %v", err)
	}
	if parcel.WeightGrams != 1200 || !parcel.Measured {
		t.Errorf("parcel = %+v, want 1200 g measured: three units of 400 g", parcel)
	}
}

// Removing a parcel already cancelled says nothing new: its units were
// announced as owed again when it was cancelled.
func TestRemovingACancelledParcelAnnouncesNothingTwice(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := confirmedOrder(t, app, orderSpec{"CANCEL-C", 1})
	shipped, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Ship().Cancel(ctx, shipped.Fulfillments[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Ship().Delete(ctx, shipped.Fulfillments[0].ID); err != nil {
		t.Fatal(err)
	}
	if n := len(unshippedEvents(t, app, order.ID)); n != 1 {
		t.Errorf("%d order.unshipped events, want 1", n)
	}
}
