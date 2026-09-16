package gocommerce

import (
	"net/http"
	"strings"
)

// The operator's window onto baskets that have not become orders yet.
//
// Read-only, and by row id. The cart token is a live credential — possessing it
// authorises adding to, emptying, repricing and checking out someone's basket —
// so the admin API withholds it exactly as orderColumns withholds an order's
// access_token, and addressing a cart by its numeric id leaves an operator with
// no handle to act on a stranger's basket with. An operator who wants to act
// already has POST /api/admin/orders.
//
// Gated on orders.read rather than a right of its own: a cart is an order that
// has not happened yet, and orders.read already exposes every order's email,
// phone and address, which is strictly more than a cart carries.
func (a *App) mountAdminCartRoutes() {
	a.HandleAdminFunc("GET /api/admin/carts", a.handleAdminListCarts, RightCartsRead)
	a.HandleAdminFunc("GET /api/admin/carts/{id}", a.handleAdminGetCart, RightCartsRead)
}

func (a *App) handleAdminListCarts(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := Page(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	q := r.URL.Query()
	// The state is passed through unvalidated on purpose: Carts.List is what
	// knows the vocabulary, and it refuses an unrecognised one rather than
	// quietly returning an empty page.
	query := CartQuery{State: q.Get("state"), Limit: limit, Offset: offset}
	if v := q.Get("has_email"); v != "" {
		has := v == "1" || strings.EqualFold(v, "true")
		query.HasEmail = &has
	}
	if v := q.Get("has_lines"); v != "" {
		has := v == "1" || strings.EqualFold(v, "true")
		query.HasLines = &has
	}
	if v := q.Get("min_value_minor"); v != "" {
		n, err := parseInt(v)
		if err != nil || n < 0 {
			RespondError(w, r, Validationf(
				"min_value_minor must be a non-negative integer number of minor units"))
			return
		}
		query.MinValueMinor = int64(n)
	}
	if query.From, err = parseDate(q.Get("from")); err != nil {
		RespondError(w, r, err)
		return
	}
	if query.To, err = parseDate(q.Get("to")); err != nil {
		RespondError(w, r, err)
		return
	}
	carts, total, err := a.carts.List(r.Context(), query)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	RespondList(w, carts, ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (a *App) handleAdminGetCart(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	cart, err := a.carts.Get(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, cart)
}
