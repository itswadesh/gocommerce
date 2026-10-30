package shiprocket

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/itswadesh/gocommerce/gctest"
)

// fakeShiprocket answers the documented requests with the documented shapes
// (https://apidocs.shiprocket.in/), keeping enough state to catch a module
// that books twice, calls as the wrong account or forgets a step. Nothing
// here was checked against the real service: there are no credentials.
type fakeShiprocket struct {
	t   *testing.T
	URL string

	mu       sync.Mutex
	accounts map[string]*fakeAccount // email → account
	tokens   map[string]string       // token → email
	logins   int
	calls    map[string]int // path → count
	nextID   int64

	// dropCreateAnswer makes the next create succeed at "Shiprocket" and then
	// fail on the wire, which is the case a retry must not double-book.
	dropCreateAnswer bool
	// refuseAWB makes assign/awb refuse with this message.
	refuseAWB string
	// dropAWB hangs up on assign/awb, after assigning when assignThenDrop.
	dropAWB, assignThenDrop bool
	// cancelledOnCreate makes the next order created arrive cancelled, as one
	// cancelled in Shiprocket's dashboard would.
	cancelledOnCreate bool
	// refuseOrderCancel makes orders/cancel fail.
	refuseOrderCancel bool
}

type fakeAccount struct {
	password string
	pickups  []map[string]any
	orders   map[string]*fakeOrder // channel order id → order
	couriers []map[string]any
	awbs     int
}

type fakeOrder struct {
	ID, ShipmentID int64
	Channel        string
	Payload        map[string]any
	AWB            string
	CourierID      int
	Cancelled      bool
	Pickup         bool
}

func newFake(t *testing.T) *fakeShiprocket {
	f := &fakeShiprocket{t: t, accounts: map[string]*fakeAccount{}, tokens: map[string]string{},
		calls: map[string]int{}, nextID: 1000}
	srv := gctest.StubHTTP(t, f.serve)
	f.URL = srv.URL
	return f
}

func (f *fakeShiprocket) account(email, password, pickup, pin string) *fakeAccount {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := &fakeAccount{
		password: password,
		orders:   map[string]*fakeOrder{},
		pickups: []map[string]any{{
			"id": 1, "pickup_location": pickup, "address": "1 Warehouse Road", "address_2": "",
			"city": "Bengaluru", "state": "Karnataka", "country": "India", "pin_code": pin,
			"email": "store@example.test", "phone": "9999999999", "name": "Store", "is_primary_location": 1,
		}},
		couriers: []map[string]any{
			{"courier_company_id": 43, "courier_name": "Delhivery Surface", "rate": 54, "cod": 1,
				"estimated_delivery_days": "4", "etd": "Jul 01, 2024", "rating": 4.9, "blocked": 0},
			{"courier_company_id": 1, "courier_name": "Blue Dart", "rate": "123.9", "cod": 1,
				"estimated_delivery_days": "2", "etd": "Jun 29, 2024", "rating": 4.4, "blocked": 0},
			{"courier_company_id": 225, "courier_name": "India Post-Business Parcel Surface Prepaid", "rate": 40, "cod": 0,
				"estimated_delivery_days": "8", "etd": "Jul 05, 2024", "rating": 4.4, "blocked": 0},
		},
	}
	f.accounts[email] = a
	return a
}

// expireTokens is Shiprocket ending every session early.
func (f *fakeShiprocket) expireTokens() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = map[string]string{}
}

func (f *fakeShiprocket) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

func (f *fakeShiprocket) orders(email string) []*fakeOrder {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*fakeOrder
	for _, o := range f.accounts[email].orders {
		out = append(out, o)
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeShiprocket) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.Path
	f.calls[path]++
	var body map[string]any
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			writeJSON(w, 422, map[string]any{"message": "Oops! Invalid Data.", "status_code": 422})
			return
		}
	}

	if path == "/v1/external/auth/login" {
		f.logins++
		a := f.accounts[fmt.Sprint(body["email"])]
		if a == nil || a.password != body["password"] {
			writeJSON(w, 400, map[string]any{"message": "Invalid email and password combination", "status_code": 400})
			return
		}
		tok := fmt.Sprintf("tok-%d", f.logins)
		f.tokens[tok] = fmt.Sprint(body["email"])
		writeJSON(w, 200, map[string]any{"token": tok, "email": body["email"]})
		return
	}
	email, ok := f.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if !ok {
		writeJSON(w, 401, map[string]any{"message": "Token has expired", "status_code": 401})
		return
	}
	a := f.accounts[email]

	switch {
	case path == "/v1/external/settings/company/pickup":
		writeJSON(w, 200, map[string]any{"data": map[string]any{"shipping_address": a.pickups}})

	case path == "/v1/external/courier/serviceability/":
		if r.URL.Query().Get("delivery_postcode") == "999999" {
			writeJSON(w, 200, map[string]any{"status": 404, "message": "Delivery postcode not serviceable"})
			return
		}
		writeJSON(w, 200, map[string]any{"status": 200, "currency": "INR", "data": map[string]any{
			"available_courier_companies":    a.couriers,
			"recommended_courier_company_id": 43,
		}})

	case path == "/v1/external/orders/create/adhoc":
		channel := fmt.Sprint(body["order_id"])
		if _, taken := a.orders[channel]; taken {
			writeJSON(w, 422, map[string]any{"message": "Oops! Invalid Data.",
				"errors": map[string]any{"order_id": []string{"The order id has already been taken."}}, "status_code": 422})
			return
		}
		f.nextID++
		o := &fakeOrder{ID: f.nextID, ShipmentID: f.nextID + 500000, Channel: channel, Payload: body}
		a.orders[channel] = o
		if f.cancelledOnCreate {
			f.cancelledOnCreate = false
			o.Cancelled = true
		}
		if f.dropCreateAnswer {
			f.dropCreateAnswer = false
			// Shiprocket made the order; the answer never arrived.
			f.hangUp(w)
			return
		}
		writeJSON(w, 200, map[string]any{"order_id": o.ID, "shipment_id": o.ShipmentID, "status": "NEW", "status_code": 1})

	case path == "/v1/external/orders":
		search := r.URL.Query().Get("search")
		data := []any{}
		for _, o := range a.orders {
			if o.Channel == search || (o.AWB != "" && o.AWB == search) {
				status := "NEW"
				if o.Cancelled {
					status = "CANCELED"
				}
				data = append(data, map[string]any{"id": o.ID, "channel_order_id": o.Channel, "status": status,
					"shipments": []any{map[string]any{"id": o.ShipmentID, "awb": o.AWB, "courier": ""}}})
			}
		}
		writeJSON(w, 200, map[string]any{"data": data})

	case path == "/v1/external/courier/assign/awb":
		if f.dropAWB && !f.assignThenDrop {
			f.dropAWB = false
			f.hangUp(w)
			return
		}
		if f.refuseAWB != "" {
			writeJSON(w, 400, map[string]any{"message": f.refuseAWB, "status_code": 400})
			return
		}
		o := a.byShipment(int64(body["shipment_id"].(float64)))
		if o == nil {
			writeJSON(w, 400, map[string]any{"message": "Invalid shipment id", "status_code": 400})
			return
		}
		if o.AWB != "" {
			writeJSON(w, 400, map[string]any{"message": "AWB is already assigned", "status_code": 400})
			return
		}
		a.awbs++
		o.CourierID = 43
		if c, ok := body["courier_id"].(float64); ok {
			o.CourierID = int(c)
		}
		o.AWB = fmt.Sprintf("1904%010d", o.ShipmentID)
		if f.dropAWB {
			f.dropAWB = false
			f.hangUp(w)
			return
		}
		writeJSON(w, 200, map[string]any{"awb_assign_status": 1, "response": map[string]any{"data": map[string]any{
			"courier_company_id": o.CourierID, "awb_code": o.AWB, "shipment_id": o.ShipmentID,
			"courier_name":          map[int]string{43: "Delhivery Surface", 1: "Blue Dart"}[o.CourierID],
			"pickup_scheduled_date": "2024-06-27 14:00:00",
		}}})

	case path == "/v1/external/courier/generate/pickup":
		o := a.byShipment(firstID(body["shipment_id"]))
		if o == nil || o.AWB == "" {
			writeJSON(w, 400, map[string]any{"message": "Invalid shipment id", "status_code": 400})
			return
		}
		o.Pickup = true
		writeJSON(w, 200, map[string]any{"pickup_status": 1, "response": map[string]any{
			"pickup_scheduled_date": "2024-06-27 14:00:00", "data": "Pickup is confirmed"}})

	case path == "/v1/external/courier/generate/label":
		o := a.byShipment(firstID(body["shipment_id"]))
		if o == nil || o.AWB == "" {
			writeJSON(w, 200, map[string]any{"label_created": 0, "not_created": []any{}, "response": "No valid shipment ids to print label"})
			return
		}
		writeJSON(w, 200, map[string]any{"label_created": 1, "label_url": "https://labels.example.test/" + o.AWB + ".pdf",
			"response": "Label has been created and uploaded successfully!", "not_created": []any{}})

	case path == "/v1/external/manifests/generate":
		o := a.byShipment(firstID(body["shipment_id"]))
		if o == nil || !o.Pickup {
			writeJSON(w, 200, map[string]any{"status": 1, "manifest_url": ""})
			return
		}
		writeJSON(w, 200, map[string]any{"status": 1, "manifest_url": "https://manifests.example.test/" + o.AWB + ".pdf"})

	case path == "/v1/external/orders/print/invoice":
		writeJSON(w, 200, map[string]any{"is_invoice_created": true,
			"invoice_url": fmt.Sprintf("https://invoices.example.test/%d.pdf", firstID(body["ids"])), "not_created": []any{}})

	case path == "/v1/external/orders/cancel/shipment/awbs":
		for _, o := range a.orders {
			for _, awb := range body["awbs"].([]any) {
				if o.AWB == awb {
					o.Cancelled = true
				}
			}
		}
		writeJSON(w, 200, map[string]any{"message": "Bulk Shipment cancellation is in progress. Please wait for some time."})

	case path == "/v1/external/orders/cancel":
		if f.refuseOrderCancel {
			writeJSON(w, 500, map[string]any{"message": "Trying to get property of non-object", "status_code": 500})
			return
		}
		for _, o := range a.orders {
			if o.ID == firstID(body["ids"]) {
				o.Cancelled = true
			}
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		writeJSON(w, 404, map[string]any{"message": "404 Not Found", "status_code": 404})
	}
}

func (a *fakeAccount) byShipment(id int64) *fakeOrder {
	for _, o := range a.orders {
		if o.ShipmentID == id {
			return o
		}
	}
	return nil
}

func firstID(v any) int64 {
	if list, ok := v.([]any); ok && len(list) > 0 {
		if f, ok := list[0].(float64); ok {
			return int64(f)
		}
	}
	return 0
}

// hangUp is the answer that never arrives.
func (f *fakeShiprocket) hangUp(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		f.t.Fatal("cannot drop the connection")
	}
	conn, _, _ := hj.Hijack()
	_ = conn.Close()
}
