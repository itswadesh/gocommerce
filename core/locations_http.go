package gocommerce

import (
	"net/http"
	"strings"
)

func (a *App) mountLocationRoutes() {
	// Admin only. A storefront is told what it can have, not where it is: a
	// shopper who learns which warehouse is short has learned something about
	// the business, not about their order.
	//
	// Reading comes with the catalog, because "where is this" is a question
	// about a product. Writing is locations.write — opening and closing places
	// redirects every future reservation in the store, which is a different
	// kind of act from adjusting a count.
	a.HandleAdminFunc("GET /api/admin/locations", a.handleListLocations, RightLocationsRead)
	a.HandleAdminFunc("POST /api/admin/locations", a.handleCreateLocation, RightLocationsWrite)
	a.HandleAdminFunc("GET /api/admin/locations/{id}", a.handleGetLocation, RightLocationsRead)
	a.HandleAdminFunc("PATCH /api/admin/locations/{id}", a.handleUpdateLocation, RightLocationsWrite)
	a.HandleAdminFunc("DELETE /api/admin/locations/{id}", a.handleDeleteLocation, RightLocationsWrite)
	a.HandleAdminFunc("POST /api/admin/locations/{id}/default", a.handleSetDefaultLocation, RightLocationsWrite)

	// Where one variant's stock is, and moving it. Both are inventory.write
	// rather than locations.write: a transfer changes counts, which is exactly
	// what the person doing the stock take is trusted to do.
	//
	// Reading how the counts got there is inventory.read for the same reason
	// reading the counts is: a movement is stock, not location configuration.
	// The two ledger routes are the same query from the two ends an operator
	// asks it from — one SKU across the store, or one shelf across the catalog.
	a.HandleAdminFunc("GET /api/admin/variants/{id}/stock", a.handleVariantStock, RightInventoryRead)
	a.HandleAdminFunc("POST /api/admin/variants/{id}/stock/transfer", a.handleTransferStock, RightInventoryWrite)
	a.HandleAdminFunc("GET /api/admin/variants/{id}/movements", a.handleVariantMovements, RightInventoryRead)
	a.HandleAdminFunc("GET /api/admin/locations/{id}/movements", a.handleLocationMovements, RightInventoryRead)
}

func (a *App) handleListLocations(w http.ResponseWriter, r *http.Request) {
	list, err := a.locations.List(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	RespondList(w, list, ListMeta{Total: len(list), Limit: len(list), Offset: 0})
}

func (a *App) handleGetLocation(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	l, err := a.locations.Get(r.Context(), id)
	respondOr(w, r, l, err)
}

func (a *App) handleCreateLocation(w http.ResponseWriter, r *http.Request) {
	var in LocationInput
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	l, err := a.locations.Create(r.Context(), in)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusCreated, l)
}

func (a *App) handleUpdateLocation(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var patch LocationPatch
	if err := DecodeJSON(w, r, &patch); err != nil {
		RespondError(w, r, err)
		return
	}
	l, err := a.locations.Update(r.Context(), id, patch)
	respondOr(w, r, l, err)
}

func (a *App) handleDeleteLocation(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.locations.Delete(r.Context(), id); err != nil {
		RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleSetDefaultLocation(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	l, err := a.locations.SetDefault(r.Context(), id)
	respondOr(w, r, l, err)
}

func (a *App) handleVariantStock(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	rows, err := a.inventory.ByLocation(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	RespondList(w, rows, ListMeta{Total: len(rows), Limit: len(rows), Offset: 0})
}

// handleVariantMovements is how one SKU's count got to be what it is.
//
// GetVariant runs first so a mistyped id is a 404 rather than an empty page —
// the shape ByLocation already sets, and the difference between "nothing has
// moved" and "there is no such variant" is worth a status code.
func (a *App) handleVariantMovements(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if _, err := a.catalog.GetVariant(r.Context(), id); err != nil {
		RespondError(w, r, err)
		return
	}
	limit, offset, err := Page(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	q, err := movementFiltersFrom(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	q.VariantID, q.Limit, q.Offset = id, limit, offset
	rows, total, err := a.inventory.Movements(r.Context(), q)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	RespondList(w, rows, ListMeta{Total: total, Limit: limit, Offset: offset})
}

// handleLocationMovements is everything that moved at one place.
//
// A deleted location's own history is unreachable here by construction — the
// location is gone, so this 404s — and those rows stay readable on the variant
// route with their location_code snapshot intact.
func (a *App) handleLocationMovements(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if _, err := a.locations.Get(r.Context(), id); err != nil {
		RespondError(w, r, err)
		return
	}
	limit, offset, err := Page(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	q, err := movementFiltersFrom(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	q.LocationID, q.Limit, q.Offset = id, limit, offset
	rows, total, err := a.inventory.Movements(r.Context(), q)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	RespondList(w, rows, ListMeta{Total: total, Limit: limit, Offset: offset})
}

// movementFiltersFrom reads the filters both ledger routes share. The parent id
// is not among them: each handler sets its own afterwards, so neither route can
// be talked out of its own scope by a query parameter.
//
// `kind` is not validated here — Movements checks it against MovementKinds, so
// the vocabulary is policed in one place whether the caller is a route or a
// module.
func movementFiltersFrom(r *http.Request) (MovementQuery, error) {
	q := r.URL.Query()
	out := MovementQuery{}

	for param, dst := range map[string]*int64{
		"variant_id": &out.VariantID, "location_id": &out.LocationID, "order_id": &out.OrderID,
	} {
		s := q.Get(param)
		if s == "" {
			continue
		}
		n, err := parseInt(s)
		if err != nil || n < 0 {
			return out, Validationf("%s must be a non-negative integer", param)
		}
		*dst = int64(n)
	}

	// Repeated and comma-separated both, because a filter row of chips sends
	// one and a hand-written URL sends the other.
	for _, v := range q["kind"] {
		for _, k := range strings.Split(v, ",") {
			if k = strings.TrimSpace(k); k != "" {
				out.Kinds = append(out.Kinds, k)
			}
		}
	}

	var err error
	if out.From, err = parseDate(q.Get("from")); err != nil {
		return out, err
	}
	if out.To, err = parseDate(q.Get("to")); err != nil {
		return out, err
	}
	return out, nil
}

func (a *App) handleTransferStock(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var in struct {
		// From has no default: moving stock out of "wherever" is not a thing
		// anyone means to do, and guessing would take units off a shelf the
		// operator never named. To may be omitted for the default location.
		From     int64 `json:"from_location_id"`
		To       int64 `json:"to_location_id"`
		Quantity int   `json:"quantity"`
		// Reason is optional and goes into the ledger verbatim, on both ends of
		// the transfer: the row that says the units left and the row that says
		// they arrived carry the same text, because they are one act.
		Reason string `json:"reason"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	if in.From == 0 {
		RespondError(w, r, Validationf("from_location_id is required"))
		return
	}
	if len(strings.TrimSpace(in.Reason)) > 200 {
		RespondError(w, r, Validationf("reason must be under 200 characters"))
		return
	}
	v, err := a.inventory.Move(r.Context(), id, in.From, in.To, in.Quantity, in.Reason)
	respondOr(w, r, v, err)
}
