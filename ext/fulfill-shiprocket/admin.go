package shiprocket

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/itswadesh/gocommerce/core"
)

// The panel's side: connecting an account, quoting couriers for an order, and
// what an operator does with a booked parcel — its papers, its pickup, and
// calling it off.
func (m *Module) routes(app *gocommerce.App) {
	const base = "/api/admin/x/fulfill-shiprocket"
	app.HandleAdminFunc("POST "+base+"/connect", m.handleConnect, gocommerce.RightPluginsWrite)
	app.HandleAdminFunc("GET "+base+"/orders/{id}/couriers", m.handleCouriers, gocommerce.RightOrdersFulfill)
	app.HandleAdminFunc("GET "+base+"/orders/{id}/shipments", m.handleShipments, gocommerce.RightOrdersRead)
	app.HandleAdminFunc("POST "+base+"/shipments/{id}/documents/{kind}", m.handleDocument, gocommerce.RightOrdersFulfill)
	app.HandleAdminFunc("POST "+base+"/shipments/{id}/pickup", m.handlePickup, gocommerce.RightOrdersFulfill)
	app.HandleAdminFunc("POST "+base+"/shipments/{id}/cancel", m.handleCancel, gocommerce.RightOrdersFulfill)
}

// fail answers with what went wrong, in the words an operator can act on. A
// Shiprocket that could not be reached is said plainly and the detail kept in
// the log: it names an address on the server's network.
func (m *Module) fail(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *gocommerce.APIError
	var netErr net.Error
	switch {
	case errors.As(err, &apiErr):
	case errors.Is(err, gocommerce.ErrNotPublic):
		err = &gocommerce.APIError{Status: http.StatusUnprocessableEntity, Code: "provider_refused",
			Message: "Shiprocket was not called: " + err.Error()}
	case errors.As(err, &netErr):
		m.log.Error("could not reach Shiprocket", "error", err)
		err = &gocommerce.APIError{Status: http.StatusUnprocessableEntity, Code: "provider_unreachable",
			Message: "Could not reach Shiprocket. Try again in a minute; if it keeps failing, the server log says why."}
	case errors.Is(err, errNotConfigured):
		err = gocommerce.Conflictf("Shiprocket is not set up for this store — connect it under Settings › Shipping providers")
	}
	gocommerce.RespondError(w, r, err)
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, gocommerce.Validationf("id must be a positive number")
	}
	return id, nil
}

// handleConnect tries an API user before it is saved: it logs in and lists
// the account's pickup locations, which is both the test and the list the
// pickup location is chosen from. A password left as the mask means the one
// already saved; nothing typed here is stored by this route.
func (m *Module) handleConnect(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		BaseURL  string `json:"base_url"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	m.refresh(r.Context())
	c := *m.conf(r.Context())
	if e := strings.TrimSpace(in.Email); e != "" {
		c.Email = e
	}
	if p := in.Password; p != "" && p != gocommerce.SecretMask {
		c.Password = p
	}
	if b := strings.TrimSpace(in.BaseURL); b != "" {
		c.BaseURL = b
	}
	if strings.TrimSpace(c.Email) == "" || strings.TrimSpace(c.Password) == "" {
		gocommerce.RespondError(w, r, gocommerce.Validationf("type the API user's email and password first"))
		return
	}
	locations, err := m.pickupLocations(r.Context(), &c)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]any{"connected": true, "pickup_locations": locations})
}

// handleCouriers quotes every courier for what the order still owes, to its
// address: rate, Shiprocket's delivery estimate and whether it collects cash.
// The weight and size are the engine's, unless the operator weighed the box.
func (m *Module) handleCouriers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !m.refresh(ctx) {
		m.fail(w, r, errNotConfigured)
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
	parcel, err := m.app.Ship().Parcel(ctx, order, nil)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	owed := 0
	var valueMinor int64
	for _, l := range order.Lines {
		left := l.Quantity - l.ShippedQuantity
		owed += left
		valueMinor += l.UnitPrice.AmountMinor * int64(left)
	}
	meta := map[string]string{}
	for _, k := range []string{"weight_kg", "length_cm", "breadth_cm", "height_cm"} {
		if v := r.URL.Query().Get(k); v != "" {
			meta[k] = v
		}
	}
	cod := paymentMethodFor(order) == "COD"
	declared := valueMinor
	if cod {
		declared = order.Total.AmountMinor
	}
	conf := m.conf(ctx)
	q, err := m.quote(ctx, parcelSpec{
		deliveryPin:   strings.TrimSpace(order.Address.PostalCode),
		cod:           cod,
		weightKg:      m.dimension(meta, "weight_kg", m.weight(ctx, parcel, owed)),
		lengthCm:      m.dimension(meta, "length_cm", centimetres(parcel.Dimensions.Length, conf.DefaultLengthCm)),
		breadthCm:     m.dimension(meta, "breadth_cm", centimetres(parcel.Dimensions.Width, conf.DefaultBreadthCm)),
		heightCm:      m.dimension(meta, "height_cm", centimetres(parcel.Dimensions.Height, conf.DefaultHeightCm)),
		declaredMinor: declared,
	})
	if err != nil {
		m.fail(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, q)
}

func (m *Module) order(ctx context.Context, r *http.Request) (*gocommerce.Order, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	return m.app.Order().Get(ctx, id)
}

// handleShipments lists what this order has booked through Shiprocket,
// including a booking the engine never recorded, so it can be seen and
// cancelled rather than left live at Shiprocket.
func (m *Module) handleShipments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	order, err := m.order(ctx, r)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	rows, err := m.shipmentsOf(ctx, order.ID)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	out := make([]*shipment, 0, len(rows))
	for _, s := range rows {
		s.FulfillmentID = recordedAs(order, s)
		out = append(out, s)
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

// booked is a row this store's operator may act on, ready for kind of act.
func (m *Module) booked(w http.ResponseWriter, r *http.Request, needAWB bool) (*shipment, bool) {
	ctx := r.Context()
	if !m.refresh(ctx) {
		m.fail(w, r, errNotConfigured)
		return nil, false
	}
	id, err := pathID(r)
	if err != nil {
		m.fail(w, r, err)
		return nil, false
	}
	row, err := m.shipmentByID(ctx, id)
	if err != nil {
		m.fail(w, r, err)
		return nil, false
	}
	if row.CancelledAt != nil {
		m.fail(w, r, gocommerce.Conflictf("Shiprocket order %s was cancelled", row.ChannelOrderID))
		return nil, false
	}
	if row.ShipmentID == 0 || (needAWB && row.AWB == "") {
		m.fail(w, r, gocommerce.Conflictf("Shiprocket order %s has no waybill yet; ship it again to finish booking", row.ChannelOrderID))
		return nil, false
	}
	return row, true
}

func (m *Module) handleDocument(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	row, ok := m.booked(w, r, kind != "invoice")
	if !ok {
		return
	}
	if kind == "manifest" && !row.PickupRequested {
		// Shiprocket generates a manifest only for a parcel whose pickup has
		// been asked for.
		if err := m.requestPickup(r.Context(), row); err != nil {
			m.fail(w, r, err)
			return
		}
	}
	if err := m.document(r.Context(), row, kind); err != nil {
		m.fail(w, r, err)
		return
	}
	m.respondRow(w, r, row)
}

func (m *Module) handlePickup(w http.ResponseWriter, r *http.Request) {
	row, ok := m.booked(w, r, true)
	if !ok {
		return
	}
	if err := m.requestPickup(r.Context(), row); err != nil {
		m.fail(w, r, err)
		return
	}
	m.respondRow(w, r, row)
}

// handleCancel calls the parcel off at Shiprocket and then on the order, so
// what it held is owed again. Shiprocket goes first: if it refuses — the
// parcel is already on its way — the order still says what is true.
func (m *Module) handleCancel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !m.refresh(ctx) {
		m.fail(w, r, errNotConfigured)
		return
	}
	id, err := pathID(r)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	row, err := m.shipmentByID(ctx, id)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	order, err := m.app.Order().Get(ctx, row.OrderID)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	fid := recordedAs(order, row)
	for _, f := range order.Fulfillments {
		if f.ID == fid && f.Status == "delivered" {
			m.fail(w, r, gocommerce.Conflictf("that parcel was delivered; it cannot be cancelled"))
			return
		}
	}
	if row.CancelledAt == nil {
		if err := m.lease(ctx, row.ID); err != nil {
			m.fail(w, r, err)
			return
		}
		err := m.cancel(ctx, row)
		m.release(ctx, row.ID)
		if err != nil {
			m.fail(w, r, err)
			return
		}
	}
	if fid != 0 {
		if _, err := m.app.Ship().Cancel(ctx, fid, "Cancelled on Shiprocket"); err != nil {
			m.fail(w, r, err)
			return
		}
	}
	m.respondRow(w, r, row)
}

func (m *Module) respondRow(w http.ResponseWriter, r *http.Request, row *shipment) {
	if order, err := m.app.Order().Get(r.Context(), row.OrderID); err == nil {
		row.FulfillmentID = recordedAs(order, row)
	}
	gocommerce.Respond(w, http.StatusOK, row)
}
