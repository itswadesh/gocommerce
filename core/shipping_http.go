package gocommerce

import (
	"net/http"
	"strconv"
)

func (a *App) mountShippingRoutes() {
	// Public: what will it cost to send this basket there. A quote rather than
	// a price list, because the answer depends on what is in the cart — which
	// is also why it takes the cart's own token rather than a subtotal the
	// caller could make up.
	a.HandleFunc("GET /api/checkout/rates", a.handleShippingRates)

	// Operator: where this store delivers and what it charges, under the
	// shipping pair D63 gave it. Naming a customer group on a rate needs
	// nothing more (D76): it offers that group a delivery price, which is what
	// shipping.write already decides for everybody, and it reaches no address
	// the group did not already hold.
	a.HandleAdminFunc("GET /api/admin/shipping/zones", a.handleListShippingZones, RightShippingRead)
	a.HandleAdminFunc("POST /api/admin/shipping/zones", a.handleCreateShippingZone, RightShippingWrite)
	a.HandleAdminFunc("PATCH /api/admin/shipping/zones/{id}", a.handleUpdateShippingZone, RightShippingWrite)
	a.HandleAdminFunc("DELETE /api/admin/shipping/zones/{id}", a.handleDeleteShippingZone, RightShippingWrite)
	a.HandleAdminFunc("POST /api/admin/shipping/rates", a.handleCreateShippingRate, RightShippingWrite)
	a.HandleAdminFunc("PATCH /api/admin/shipping/rates/{id}", a.handleUpdateShippingRate, RightShippingWrite)
	a.HandleAdminFunc("DELETE /api/admin/shipping/rates/{id}", a.handleDeleteShippingRate, RightShippingWrite)
}

// handleShippingRates answers what a storefront needs to render the delivery
// step: the options, by name, with prices.
//
// An empty list is a 200 with no options rather than a 404. "We do not deliver
// there" is an answer about the destination, not a missing resource, and a
// storefront has to render it either way.
func (a *App) handleShippingRates(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	token := q.Get("cart")
	if token == "" {
		RespondError(w, r, Validationf("cart is required"))
		return
	}
	cart, err := a.carts.GetByToken(r.Context(), token)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	country := q.Get("country")
	if country == "" {
		RespondError(w, r, Validationf("country is required"))
		return
	}

	// Quoted as whoever the cart is proven to belong to, so a dealer's basket
	// is offered the dealer's freight — and as nobody in particular when it is
	// not, whatever address was typed onto it (D66, D76).
	quotes, err := a.shipping.Quote(r.Context(), ShippingQuery{
		Country:       country,
		State:         q.Get("state"),
		SubtotalMinor: cart.Subtotal.AmountMinor,
		Email:         cart.VerifiedEmail,
	})
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if quotes == nil {
		quotes = []ShippingQuote{}
	}
	Respond(w, http.StatusOK, map[string]any{
		"rates":    quotes,
		"currency": a.cfg.Currency,
	})
}

func (a *App) handleListShippingZones(w http.ResponseWriter, r *http.Request) {
	zones, rates, err := a.shipping.Zones(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// Zones carry their rates rather than being listed beside them: a zone with
	// no prices is not a thing an operator can use, so showing the two apart
	// invites reading half the configuration.
	out := make([]map[string]any, 0, len(zones))
	for _, z := range zones {
		rs := rates[z.ID]
		if rs == nil {
			rs = []*ShippingRate{}
		}
		out = append(out, map[string]any{"zone": z, "rates": rs})
	}
	Respond(w, http.StatusOK, out)
}

func (a *App) handleCreateShippingZone(w http.ResponseWriter, r *http.Request) {
	var in ShippingZoneInput
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	z, err := a.shipping.CreateZone(r.Context(), in)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusCreated, z)
}

func (a *App) handleUpdateShippingZone(w http.ResponseWriter, r *http.Request) {
	id, err := shippingPathID(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var in ShippingZoneInput
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	z, err := a.shipping.UpdateZone(r.Context(), id, in)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, z)
}

func (a *App) handleDeleteShippingZone(w http.ResponseWriter, r *http.Request) {
	id, err := shippingPathID(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.shipping.DeleteZone(r.Context(), id); err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, map[string]any{"deleted": true})
}

func (a *App) handleCreateShippingRate(w http.ResponseWriter, r *http.Request) {
	var in ShippingRateInput
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	rate, err := a.shipping.CreateRate(r.Context(), in)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusCreated, rate)
}

func (a *App) handleUpdateShippingRate(w http.ResponseWriter, r *http.Request) {
	id, err := shippingPathID(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var in ShippingRateInput
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	rate, err := a.shipping.UpdateRate(r.Context(), id, in)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, rate)
}

func (a *App) handleDeleteShippingRate(w http.ResponseWriter, r *http.Request) {
	id, err := shippingPathID(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.shipping.DeleteRate(r.Context(), id); err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, map[string]any{"deleted": true})
}

func shippingPathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, Validationf("id must be a positive integer")
	}
	return id, nil
}
