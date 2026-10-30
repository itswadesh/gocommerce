package shiprocket

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/itswadesh/gocommerce/core"
)

// Returns: a reverse pickup booked with Shiprocket, collecting goods from the
// customer and bringing them to the store's pickup address.
//
// It is a parcel, not a return in the engine's sense. What came back, whether
// it goes on the shelf and whether money goes back are decided when it
// arrives, with the order's own return and refund — so booking one changes
// nothing on the order, and its tracking never does either.

// returnReasons are the return_reason values Shiprocket's docs list for a
// return order ("Return & Exchange Orders › Create a Return Order").
var returnReasons = []string{
	"Bought by Mistake", "Better price available", "Performance or quality not adequate",
	"Incompatible or not useful", "Product damaged, but shipping box OK", "Item arrived too late",
	"Missing parts or accessories", "Both product and shipping box damaged", "Wrong item was sent",
	"Item defective or doesn't work", "No longer needed", "Didn't approve purchase",
	"Inaccurate website description", "Return against replacement", "Delay Refund", "Delivered Late",
	"Product does not Match Description on Website", "Both Product & Outer Box Damaged",
	"Defective or does not work", "Product damaged, but outer Box OK", "Missing Parts or Accessories",
	"Incorrect Item Delivered", "Product Defective or Doesn't Work",
	"Product performance/quality is not up to my expectations", "Other", "Changed my mind",
	"Does not fit", "Size not as expected", "Item is damaged", "Received wrong item",
	"Parcel damaged on arrival", "Quality not as expected", "Missing Item or accessories",
	"Performance not adequate", "Not as described", "Arrived too late", "Order Not Received",
	"Empty Package", "Wrong item or Wrong colour was sent", "Item defective, expired, spoilt or does not work",
	"Items or parts missing", "Size or Quantity issues", "Status as delivered but order not received", "N/A",
}

type returnRequest struct {
	Lines     []gocommerce.ShipLine `json:"lines"`
	Reason    string                `json:"reason"`
	CourierID int                   `json:"courier_id"`
	WeightKg  float64               `json:"weight_kg"`
}

// handleReturn books a reverse pickup for some of what an order shipped. Like
// booking a parcel, each step is written down before it is asked for, so a
// retry resumes rather than doubling; the same items asked for again while a
// pickup for them is live is refused.
func (m *Module) handleReturn(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !m.refresh(ctx) {
		m.fail(w, r, errNotConfigured)
		return
	}
	var in returnRequest
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	order, err := m.order(ctx, r)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	if err := domestic(order); err != nil {
		m.fail(w, r, err)
		return
	}
	rows, err := m.shipmentsOf(ctx, order.ID)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	if err := returnable(order, rows, in); err != nil {
		m.fail(w, r, err)
		return
	}
	row, lease, err := m.claimReturn(ctx, order, contentsOf(in.Lines))
	if err != nil {
		m.fail(w, r, err)
		return
	}
	defer m.release(ctx, row.ID, lease)
	if row.ShipmentID == 0 {
		if err := m.createReturn(ctx, row, order, in); err != nil {
			m.fail(w, r, err)
			return
		}
	}
	if row.AWB == "" {
		courier := ""
		if in.CourierID > 0 {
			courier = strconv.Itoa(in.CourierID)
		}
		if err := m.assignAWB(ctx, row, courier); err != nil {
			m.fail(w, r, m.halfBooked(row, err))
			return
		}
	}
	m.respondRow(w, r, row)
}

// returnable checks a request against what the customer actually has: units
// in parcels delivered to them, less what has been recorded back and what
// another live pickup is already fetching. A courier sent for more is a
// courier sent for nothing.
func returnable(order *gocommerce.Order, rows []*shipment, in returnRequest) error {
	if len(in.Lines) == 0 {
		return gocommerce.Validationf("name what is coming back: at least one line")
	}
	if in.Reason != "" && !slices.Contains(returnReasons, in.Reason) {
		return gocommerce.Validationf("%q is not one of Shiprocket's return reasons", in.Reason)
	}
	byID := map[int64]gocommerce.OrderLine{}
	for _, l := range order.Lines {
		byID[l.ID] = l
	}
	delivered := map[int64]int{}
	for _, f := range order.Fulfillments {
		if f.Status == "delivered" {
			for _, fl := range f.Lines {
				delivered[fl.OrderLineID] += fl.Quantity
			}
		}
	}
	coming := map[int64]int{}
	for _, s := range rows {
		// Booked, not cancelled, not yet back: a row with no waybill is a
		// request being retried, which is this one or is refused as another.
		if s.Return && s.AWB != "" && s.CancelledAt == nil && !slices.Contains(terminal, s.StatusCode) {
			for id, q := range parseContents(s.contents) {
				coming[id] += q
			}
		}
	}
	seen := map[int64]bool{}
	for _, want := range in.Lines {
		l, ok := byID[want.OrderLineID]
		if !ok {
			return gocommerce.Validationf("order %s has no line %d", order.Number, want.OrderLineID)
		}
		if seen[l.ID] {
			return gocommerce.Validationf("line %d is named twice", l.ID)
		}
		seen[l.ID] = true
		left := delivered[l.ID] - l.ReturnedQuantity - coming[l.ID]
		if want.Quantity < 1 || want.Quantity > left {
			return gocommerce.Validationf("%s: %d delivered, %d recorded back and %d on a pickup already, so %d can be collected",
				l.SKU, delivered[l.ID], l.ReturnedQuantity, coming[l.ID], max(left, 0))
		}
	}
	return nil
}

// parseContents reads back what contentsOf wrote: order line id:quantity.
func parseContents(contents string) map[int64]int {
	out := map[int64]int{}
	lines, _, _ := strings.Cut(contents, "|")
	for _, part := range strings.Split(lines, ",") {
		id, q, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		lineID, err1 := strconv.ParseInt(id, 10, 64)
		n, err2 := strconv.Atoi(q)
		if err1 == nil && err2 == nil {
			out[lineID] += n
		}
	}
	return out
}

// createReturn creates the return order. "Return & Exchange Orders › Create a
// Return Order": POST /v1/external/orders/create/return with pickup_* the
// customer, shipping_* where it goes back to, order_items, payment_method
// (always Prepaid), sub_total and the parcel's size → {order_id,
// shipment_id}.
func (m *Module) createReturn(ctx context.Context, row *shipment, order *gocommerce.Order, in returnRequest) error {
	if row.createSent {
		if found, err := m.findOrder(ctx, row.ChannelOrderID); err == nil && found != nil {
			return m.adopt(ctx, row, found)
		}
	}
	to, err := m.pickup(ctx)
	if err != nil {
		return err
	}
	if err := m.save(ctx, row.ID, map[string]any{"create_sent": true}); err != nil {
		return err
	}
	row.createSent = true

	addr := order.Address
	first, last := splitName(firstNonEmpty(order.Name, addr.Name, "Customer"))
	byLine := map[int64]gocommerce.OrderLine{}
	for _, l := range order.Lines {
		byLine[l.ID] = l
	}
	items := []map[string]any{}
	var valueMinor int64
	units := 0
	for _, want := range in.Lines {
		l := byLine[want.OrderLineID]
		item := map[string]any{"name": l.Title, "sku": l.SKU, "units": want.Quantity, "selling_price": rupees(l.UnitPrice.AmountMinor)}
		if in.Reason != "" {
			item["return_reason"] = in.Reason
		}
		items = append(items, item)
		valueMinor += l.UnitPrice.AmountMinor * int64(want.Quantity)
		units += want.Quantity
	}
	conf := m.conf(ctx)
	weight := in.WeightKg
	if weight <= 0 {
		weight = m.weight(ctx, gocommerce.Parcel{}, units)
	}
	payload := map[string]any{
		"order_id":               row.ChannelOrderID,
		"order_date":             time.Now().In(ist).Format("2006-01-02"),
		"pickup_customer_name":   first,
		"pickup_last_name":       last,
		"pickup_address":         addr.Line1,
		"pickup_address_2":       addr.Line2,
		"pickup_city":            addr.City,
		"pickup_state":           stateName(addr.Country, addr.State),
		"pickup_country":         "India",
		"pickup_pincode":         addr.PostalCode,
		"pickup_email":           order.Email,
		"pickup_phone":           indianPhone(firstNonEmpty(addr.Phone, order.Phone)),
		"shipping_customer_name": firstNonEmpty(to.Contact, to.Name),
		"shipping_address":       to.Address,
		"shipping_address_2":     to.Address2,
		"shipping_city":          to.City,
		"shipping_state":         to.State,
		"shipping_country":       firstNonEmpty(to.Country, "India"),
		"shipping_pincode":       to.Pincode,
		"shipping_email":         to.Email,
		"shipping_phone":         indianPhone(to.Phone),
		"order_items":            items,
		"payment_method":         "Prepaid",
		"sub_total":              rupees(valueMinor),
		"length":                 conf.DefaultLengthCm,
		"breadth":                conf.DefaultBreadthCm,
		"height":                 conf.DefaultHeightCm,
		"weight":                 weight,
	}
	var created struct {
		OrderID    int64  `json:"order_id"`
		ShipmentID int64  `json:"shipment_id"`
		Status     string `json:"status"`
		Message    string `json:"message"`
	}
	if err := m.call(ctx, http.MethodPost, "/v1/external/orders/create/return", nil, payload, &created); err != nil {
		if found, ferr := m.findOrder(ctx, row.ChannelOrderID); ferr == nil && found != nil {
			return m.adopt(ctx, row, found)
		}
		return err
	}
	if created.ShipmentID == 0 {
		return refused(firstNonEmpty(created.Message, created.Status, "no return shipment was created"))
	}
	row.SROrderID, row.ShipmentID = created.OrderID, created.ShipmentID
	return m.save(ctx, row.ID, map[string]any{"sr_order_id": created.OrderID, "shipment_id": created.ShipmentID})
}

// claimReturn is claim for a reverse pickup. A return row without a waybill is
// the same request being tried again; one with a waybill and the same items,
// still on its way, means this pickup is already booked, and booking it again
// would send a second courier to the customer's door.
func (m *Module) claimReturn(ctx context.Context, order *gocommerce.Order, contents string) (*shipment, time.Time, error) {
	rows, err := m.shipmentsOf(ctx, order.ID)
	if err != nil {
		return nil, time.Time{}, err
	}
	var pending *shipment
	returns := 0
	for _, s := range rows {
		if !s.Return {
			continue
		}
		returns++
		switch {
		case s.CancelledAt != nil:
		case s.AWB == "":
			pending = s
		case s.contents == contents && !slices.Contains(terminal, s.StatusCode):
			return nil, time.Time{}, gocommerce.Conflictf("a reverse pickup for these items is already booked (waybill %s); "+
				"cancel it before booking another", s.AWB)
		}
	}
	if pending != nil && pending.contents != contents {
		return nil, time.Time{}, gocommerce.Conflictf("reverse pickup %s was started for different items and never finished. "+
			"Cancel it, then book this one", pending.ChannelOrderID)
	}
	if pending == nil {
		pending, err = m.insertRow(ctx, order, fmt.Sprintf("%s_%d-R%d", order.Number, order.ID, returns+1), contents, true)
		if err != nil {
			return nil, time.Time{}, err
		}
	}
	return m.lease(ctx, pending.ID)
}
