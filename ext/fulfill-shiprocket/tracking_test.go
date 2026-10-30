package shiprocket

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

const hookPath = "/api/tracking-updates/courier-aggregator"

// webhookToken asks the panel's route for a token, as the operator would.
func webhookToken(t *testing.T, app *gocommerce.App, store string) (path, token string) {
	t.Helper()
	target := "/api/admin/x/fulfill-shiprocket/webhook"
	if store != "" {
		target += "?store=" + store
	}
	rec := gctest.AdminRequest(t, app, http.MethodPost, target, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("webhook token = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Path  string `json:"path"`
		Token string `json:"token"`
	}
	gctest.DecodeData(t, rec, &out)
	return out.Path, out.Token
}

// event is Shiprocket's documented webhook body, trimmed to what matters.
func event(awb string, statusID int, label, at string) map[string]any {
	return map[string]any{
		"awb": awb, "courier_name": "Delhivery Surface",
		"current_status": label, "current_status_id": 20,
		"shipment_status": label, "shipment_status_id": statusID,
		"current_timestamp": at, "order_id": "1373900_150876814", "sr_order_id": 348456385,
		"etd": "2026-11-03 15:40:19", "is_return": 0,
		"scans": []map[string]any{{
			"date": "2026-11-01 11:59:16", "status": "X-PPOM", "activity": "In Transit - Shipment picked up",
			"location": "Chomu_SamodRd_D (Rajasthan)", "sr-status": "42", "sr-status-label": "PICKED UP",
		}},
	}
}

func post(t *testing.T, app *gocommerce.App, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, hookPath, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("x-api-key", token)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

func trackingOf(t *testing.T, app *gocommerce.App, ctx context.Context, orderID int64) (string, string) {
	t.Helper()
	o, err := app.Order().Get(ctx, orderID)
	if err != nil {
		t.Fatal(err)
	}
	return o.Status, o.Fulfillments[len(o.Fulfillments)-1].TrackingStatus
}

// The address Shiprocket is given must not hold the words it refuses.
func TestTheWebhookAddressAvoidsShiprocketsForbiddenWords(t *testing.T) {
	app, _ := newApp(t, Config{})
	path, token := webhookToken(t, app, "")
	if path != hookPath {
		t.Errorf("path = %q", path)
	}
	for _, word := range []string{"shiprocket", "kartrocket", "sr", "kr"} {
		if strings.Contains(strings.ToLower(path), word) {
			t.Errorf("the webhook path %q contains %q", path, word)
		}
	}
	store, secret, _ := strings.Cut(token, ".")
	if !gocommerce.ValidStoreID(store) || len(secret) < 40 {
		t.Errorf("token = %q, want the store's id, a dot and a long secret", token)
	}
}

// Shiprocket sends the store's token in x-api-key; anything else changes
// nothing, and is still answered 200, the only answer Shiprocket accepts.
func TestTheWebhookTrustsOnlyTheStoresToken(t *testing.T) {
	app, _ := newApp(t, Config{})
	_, token := webhookToken(t, app, "")
	variant := measuredProduct(t, app, "SR-HOOK")
	order := placeAndShip(t, app, variant.ID, 1, nil)
	awb := order.Fulfillments[0].Tracking
	ctx := context.Background()

	store, _, _ := strings.Cut(token, ".")
	for _, bad := range []string{"", store + ".wrong-secret", store + "." + strings.Repeat("x", 43), token + "x"} {
		rec := post(t, app, bad, event(awb, 18, "IN TRANSIT", "02 11 2026 10:00:00"))
		if rec.Code != http.StatusOK {
			t.Errorf("token %q answered %d, want 200", bad, rec.Code)
		}
		if _, tracked := trackingOf(t, app, ctx, order.ID); tracked != "" {
			t.Fatalf("token %q moved the parcel to %q", bad, tracked)
		}
	}
	post(t, app, token, event(awb, 18, "IN TRANSIT", "02 11 2026 10:00:00"))
	if _, tracked := trackingOf(t, app, ctx, order.ID); tracked != gocommerce.TrackingInTransit {
		t.Errorf("with the token, tracking = %q", tracked)
	}
}

// Shiprocket's documented shipment statuses reach the order as the engine's.
func TestShiprocketStatusesBecomeTheEngines(t *testing.T) {
	app, _ := newApp(t, Config{})
	_, token := webhookToken(t, app, "")
	variant := measuredProduct(t, app, "SR-MAP")
	ctx := context.Background()
	for _, c := range []struct {
		id      int
		label   string
		tracked string
		order   string
	}{
		{17, "OUT FOR DELIVERY", gocommerce.TrackingOutForDelivery, gocommerce.OrderShipped},
		{21, "UNDELIVERED", gocommerce.TrackingDeliveryFailed, gocommerce.OrderShipped},
		{9, "RTO INITIATED", gocommerce.TrackingReturning, gocommerce.OrderShipped},
		{10, "RTO DELIVERED", gocommerce.TrackingReturned, gocommerce.OrderShipped},
		{12, "LOST", gocommerce.TrackingLost, gocommerce.OrderShipped},
		{7, "DELIVERED", gocommerce.TrackingDelivered, gocommerce.OrderDelivered},
		// Booked and waiting for the courier: Shiprocket's business, not the order's.
		{3, "PICKUP SCHEDULED", "", gocommerce.OrderShipped},
	} {
		order := placeAndShip(t, app, variant.ID, 1, nil)
		post(t, app, token, event(order.Fulfillments[0].Tracking, c.id, c.label, "02 11 2026 10:00:00"))
		status, tracked := trackingOf(t, app, ctx, order.ID)
		if tracked != c.tracked || status != c.order {
			t.Errorf("status %d (%s): parcel %q, order %s; want %q and %s", c.id, c.label, tracked, status, c.tracked, c.order)
		}
	}
}

// A status Shiprocket has not documented is kept as in transit, whatever its
// label says, and never taken for a delivery.
func TestAnUnknownStatusIsNeverDelivered(t *testing.T) {
	h := newHarness(t, Config{}, gocommerce.Config{})
	_, token := webhookToken(t, h.app, "")
	variant := measuredProduct(t, h.app, "SR-UNKNOWN")
	order := placeAndShip(t, h.app, variant.ID, 1, nil)
	post(t, h.app, token, event(order.Fulfillments[0].Tracking, 999, "DELIVERED", "02 11 2026 10:00:00"))
	status, tracked := trackingOf(t, h.app, context.Background(), order.ID)
	if status != gocommerce.OrderShipped || tracked != gocommerce.TrackingInTransit {
		t.Errorf("unknown status: order %s, parcel %q; want shipped, in transit", status, tracked)
	}
	if !strings.Contains(h.logs.String(), "999") {
		t.Error("the unknown status was not logged")
	}
}

// Shiprocket may send an event twice, or an old one late.
func TestRepeatedAndLateEventsChangeNothing(t *testing.T) {
	app, _ := newApp(t, Config{})
	_, token := webhookToken(t, app, "")
	variant := measuredProduct(t, app, "SR-DUP")
	order := placeAndShip(t, app, variant.ID, 1, nil)
	awb := order.Fulfillments[0].Tracking
	ctx := context.Background()

	post(t, app, token, event(awb, 17, "OUT FOR DELIVERY", "02 11 2026 10:00:00"))
	post(t, app, token, event(awb, 17, "OUT FOR DELIVERY", "02 11 2026 10:00:00"))
	post(t, app, token, event(awb, 18, "IN TRANSIT", "01 11 2026 09:00:00"))
	if _, tracked := trackingOf(t, app, ctx, order.ID); tracked != gocommerce.TrackingOutForDelivery {
		t.Errorf("after a late in-transit, tracking = %q; want out for delivery", tracked)
	}
	var n int
	_ = app.DB().QueryRowContext(ctx, `SELECT count(*) FROM outbox_events WHERE event_name = $1 AND aggregate_id = $2`,
		gocommerce.EventOrderShipmentUpdated, order.ID).Scan(&n)
	if n != 1 {
		t.Errorf("%d shipment updates for one fact, want 1", n)
	}
}

// One store's token cannot move another store's parcel, even one with the
// same waybill.
func TestAStoresTokenOnlyReachesItsOwnParcels(t *testing.T) {
	h := newHarness(t, Config{}, gocommerce.Config{})
	second, err := h.app.Stores().Create(context.Background(), "Second", "second")
	if err != nil {
		t.Fatal(err)
	}
	b := gocommerce.WithStore(context.Background(), second.ID)
	on := true
	if _, err := h.app.Plugins().Update(b, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{
		"email": testEmail, "password": testPassword, "pickup_location": "Primary"}}); err != nil {
		t.Fatal(err)
	}
	_, tokenA := webhookToken(t, h.app, "")
	_, tokenB := webhookToken(t, h.app, second.ID)
	storeA, _, _ := strings.Cut(tokenA, ".")

	variant := measuredProduct(t, h.app, "SR-ISO")
	order := placeAndShip(t, h.app, variant.ID, 1, nil)
	awb := order.Fulfillments[0].Tracking

	post(t, h.app, tokenB, event(awb, 7, "DELIVERED", "02 11 2026 10:00:00"))
	// Store B's token, with store A's id in front of its secret.
	_, secretB, _ := strings.Cut(tokenB, ".")
	post(t, h.app, storeA+"."+secretB, event(awb, 7, "DELIVERED", "02 11 2026 10:00:00"))
	if status, _ := trackingOf(t, h.app, context.Background(), order.ID); status != gocommerce.OrderShipped {
		t.Errorf("store A's order is %s after store B's calls", status)
	}
}

// A missed webhook is caught by asking.
func TestPollingCatchesAMissedUpdate(t *testing.T) {
	h := newHarness(t, Config{}, gocommerce.Config{})
	app, fake := h.app, h.fake
	variant := measuredProduct(t, app, "SR-POLL")
	order := placeAndShip(t, app, variant.ID, 1, nil)
	awb := order.Fulfillments[0].Tracking
	fake.track(testEmail, awb, 7, "Delivered")

	if _, err := h.mod.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, tracked := trackingOf(t, app, context.Background(), order.ID)
	if status != gocommerce.OrderDelivered || tracked != gocommerce.TrackingDelivered {
		t.Errorf("after polling: order %s, parcel %q", status, tracked)
	}
	// What the panel shows: Shiprocket's own scans and status.
	var rows []shipment
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet,
		"/api/admin/x/fulfill-shiprocket/orders/"+itoa(order.ID)+"/shipments", nil), &rows)
	if len(rows) != 1 || rows[0].StatusCode != 7 || len(rows[0].Scans) != 1 || rows[0].Scans[0].Activity != "Delivered" {
		t.Errorf("row = %+v", rows)
	}
}

// A failed delivery shows why, and Shiprocket's two documented answers to it
// can be sent from the order.
func TestAFailedDeliveryCanBeTriedAgainOrSentBack(t *testing.T) {
	app, fake := newApp(t, Config{})
	variant := measuredProduct(t, app, "SR-NDR")
	order := placeAndShip(t, app, variant.ID, 1, nil)
	awb := order.Fulfillments[0].Tracking
	var rows []shipment
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet,
		"/api/admin/x/fulfill-shiprocket/orders/"+itoa(order.ID)+"/shipments", nil), &rows)
	base := "/api/admin/x/fulfill-shiprocket/shipments/" + itoa(rows[0].ID) + "/ndr"

	var ndr NDR
	rec := gctest.AdminRequest(t, app, http.MethodGet, base, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("ndr = %d %s", rec.Code, rec.Body)
	}
	gctest.DecodeData(t, rec, &ndr)
	if ndr.Reason != "Customer not available" || ndr.Attempts != 1 || len(ndr.History) != 1 {
		t.Errorf("ndr = %+v", ndr)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodPost, base, map[string]string{"action": "fake-attempt", "comments": "x"}); rec.Code != http.StatusBadRequest {
		t.Errorf("an action Shiprocket asks proof for = %d, want 400", rec.Code)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodPost, base, map[string]string{"action": "re-attempt"}); rec.Code != http.StatusBadRequest {
		t.Errorf("no comment = %d, want 400: Shiprocket requires one", rec.Code)
	}
	rec = gctest.AdminRequest(t, app, http.MethodPost, base, map[string]string{
		"action": "re-attempt", "comments": "Customer home after 6 pm", "deferred_date": "2026-11-04"})
	if rec.Code != http.StatusOK {
		t.Fatalf("re-attempt = %d %s", rec.Code, rec.Body)
	}
	acts := fake.ndrActions(testEmail, awb)
	if len(acts) != 1 || acts[0]["action"] != "re-attempt" || acts[0]["deferred_date"] != "2026-11-04" {
		t.Errorf("Shiprocket was sent %+v", acts)
	}
}
