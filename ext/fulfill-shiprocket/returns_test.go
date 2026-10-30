package shiprocket

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

func returnPath(orderID int64) string {
	return "/api/admin/x/fulfill-shiprocket/orders/" + itoa(orderID) + "/returns"
}

func returnBody(order *gocommerce.Order, qty int) map[string]any {
	return map[string]any{
		"lines":  []map[string]any{{"order_line_id": order.Lines[0].ID, "quantity": qty}},
		"reason": "Size not as expected",
	}
}

func (f *fakeShiprocket) returns(email string) []*fakeOrder {
	var out []*fakeOrder
	for _, o := range f.orders(email) {
		if o.Return {
			out = append(out, o)
		}
	}
	return out
}

// A reverse pickup collects from the customer and comes back to the store's
// pickup address, with a waybill of its own.
func TestAReversePickupIsBookedFromTheCustomer(t *testing.T) {
	app, fake := newApp(t, Config{})
	variant := measuredProduct(t, app, "SR-RET")
	order := placeAndShip(t, app, variant.ID, 2, nil)

	rec := gctest.AdminRequest(t, app, http.MethodPost, returnPath(order.ID), returnBody(order, 1))
	if rec.Code != http.StatusOK {
		t.Fatalf("reverse pickup = %d %s", rec.Code, rec.Body)
	}
	var row shipment
	gctest.DecodeData(t, rec, &row)
	if !row.Return || row.AWB == "" || !strings.HasPrefix(row.ChannelOrderID, order.Number+"_") || !strings.HasSuffix(row.ChannelOrderID, "-R1") {
		t.Errorf("row = %+v, want a return with its own waybill", row)
	}
	rets := fake.returns(testEmail)
	if len(rets) != 1 {
		t.Fatalf("Shiprocket has %d return orders, want 1", len(rets))
	}
	p := rets[0].Payload
	if p["pickup_pincode"] != "560001" || p["pickup_customer_name"] != "Asha" || p["shipping_pincode"] != "560001" ||
		p["payment_method"] != "Prepaid" || p["shipping_customer_name"] == "" {
		t.Errorf("return payload = %v", p)
	}
	items, _ := json.Marshal(p["order_items"])
	if !strings.Contains(string(items), `"units":1`) || !strings.Contains(string(items), "SR-RET") {
		t.Errorf("items = %s, want one unit of SR-RET", items)
	}
	// The order itself is untouched: goods come back on the order's own return.
	after, _ := app.Order().Get(context.Background(), order.ID)
	if after.Status != gocommerce.OrderShipped || len(after.Fulfillments) != 1 {
		t.Errorf("order = %s with %d parcels after booking a reverse pickup", after.Status, len(after.Fulfillments))
	}
}

// A retry resumes the same return order; the same items twice is refused.
func TestAReversePickupRetriesOnceAndRefusesADuplicate(t *testing.T) {
	app, fake := newApp(t, Config{})
	variant := measuredProduct(t, app, "SR-RET2")
	order := placeAndShip(t, app, variant.ID, 1, nil)

	fake.refuseAWB = "Insufficient balance"
	if rec := gctest.AdminRequest(t, app, http.MethodPost, returnPath(order.ID), returnBody(order, 1)); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("first attempt = %d %s", rec.Code, rec.Body)
	}
	fake.refuseAWB = ""
	if rec := gctest.AdminRequest(t, app, http.MethodPost, returnPath(order.ID), returnBody(order, 1)); rec.Code != http.StatusOK {
		t.Fatalf("retry = %d %s", rec.Code, rec.Body)
	}
	if n := len(fake.returns(testEmail)); n != 1 {
		t.Fatalf("Shiprocket has %d return orders after a retry, want 1", n)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodPost, returnPath(order.ID), returnBody(order, 1)); rec.Code != http.StatusConflict {
		t.Errorf("the same items again = %d, want 409", rec.Code)
	}
}

func TestMoreBackThanWentOutIsRefused(t *testing.T) {
	app, fake := newApp(t, Config{})
	variant := measuredProduct(t, app, "SR-RET3")
	order := placeAndShip(t, app, variant.ID, 1, nil)
	if rec := gctest.AdminRequest(t, app, http.MethodPost, returnPath(order.ID), returnBody(order, 2)); rec.Code != http.StatusBadRequest {
		t.Errorf("two back of one shipped = %d %s", rec.Code, rec.Body)
	}
	if n := len(fake.returns(testEmail)); n != 0 {
		t.Errorf("%d return orders reached Shiprocket", n)
	}
}

// The return's waybill reaching the store is the return parcel delivered,
// not the order: tracking a return never moves the order.
func TestAReturnsTrackingLeavesTheOrderAlone(t *testing.T) {
	app, _ := newApp(t, Config{})
	_, token := webhookToken(t, app, "")
	variant := measuredProduct(t, app, "SR-RET4")
	order := placeAndShip(t, app, variant.ID, 1, nil)
	var row shipment
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodPost, returnPath(order.ID), returnBody(order, 1)), &row)

	ev := event(row.AWB, 7, "DELIVERED", "03 11 2026 10:00:00")
	ev["is_return"] = 1
	post(t, app, token, ev)
	status, tracked := trackingOf(t, app, context.Background(), order.ID)
	if status != gocommerce.OrderShipped || tracked != "" {
		t.Errorf("after the return arrived: order %s, forward parcel %q", status, tracked)
	}
	var rows []shipment
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet,
		"/api/admin/x/fulfill-shiprocket/orders/"+itoa(order.ID)+"/shipments", nil), &rows)
	for _, r := range rows {
		if r.Return && r.StatusCode != 7 {
			t.Errorf("the return's row = %+v, want Shiprocket's delivered status on it", r)
		}
	}
}
