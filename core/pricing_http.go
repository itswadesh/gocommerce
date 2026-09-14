package gocommerce

import (
	"net/http"
	"strconv"
)

func (a *App) mountPricingRoutes() {
	// Behind the discounts rights, not the catalogue's.
	//
	// A price list is a rule that changes what somebody pays, which is what a
	// discount is; the catalogue right is for what the shop sells and what it
	// normally costs. It is also the same person's job — whoever runs the
	// promotions runs the trade pricing — and core/rights.go stays the closed
	// catalogue D24 made it rather than growing a twenty-second entry.
	a.HandleAdminFunc("GET /api/admin/customer-groups", a.handleListCustomerGroups, RightDiscountsRead)
	a.HandleAdminFunc("POST /api/admin/customer-groups", a.handleCreateCustomerGroup, RightDiscountsWrite)
	a.HandleAdminFunc("GET /api/admin/customer-groups/{id}", a.handleGetCustomerGroup, RightDiscountsRead)
	a.HandleAdminFunc("PATCH /api/admin/customer-groups/{id}", a.handleUpdateCustomerGroup, RightDiscountsWrite)
	a.HandleAdminFunc("DELETE /api/admin/customer-groups/{id}", a.handleDeleteCustomerGroup, RightDiscountsWrite)

	// The membership *list* is the exception, and it takes customers.read
	// instead: it answers with the shop's customer addresses, and somebody
	// trusted to write a promotion is not thereby trusted to page through the
	// customer list. Adding and removing stay a pricing decision, and need the
	// address already in hand to make.
	a.HandleAdminFunc("GET /api/admin/customer-groups/{id}/members", a.handleListGroupMembers, RightCustomersRead)
	a.HandleAdminFunc("POST /api/admin/customer-groups/{id}/members", a.handleAddGroupMember, RightDiscountsWrite)
	a.HandleAdminFunc("DELETE /api/admin/customer-groups/{id}/members", a.handleRemoveGroupMember, RightDiscountsWrite)

	a.HandleAdminFunc("GET /api/admin/price-lists", a.handleListPriceLists, RightDiscountsRead)
	a.HandleAdminFunc("POST /api/admin/price-lists", a.handleCreatePriceList, RightDiscountsWrite)
	a.HandleAdminFunc("GET /api/admin/price-lists/{id}", a.handleGetPriceList, RightDiscountsRead)
	a.HandleAdminFunc("PATCH /api/admin/price-lists/{id}", a.handleUpdatePriceList, RightDiscountsWrite)
	a.HandleAdminFunc("DELETE /api/admin/price-lists/{id}", a.handleDeletePriceList, RightDiscountsWrite)
	a.HandleAdminFunc("GET /api/admin/price-lists/{id}/prices", a.handleListPrices, RightDiscountsRead)
	a.HandleAdminFunc("PUT /api/admin/price-lists/{id}/prices", a.handleSetPrice, RightDiscountsWrite)
	a.HandleAdminFunc("DELETE /api/admin/price-lists/{id}/prices", a.handleRemovePrice, RightDiscountsWrite)
}

// ------------------------------------------------------------------- groups

func (a *App) handleListCustomerGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := a.Pricing().Groups(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, groups)
}

func (a *App) handleCreateCustomerGroup(w http.ResponseWriter, r *http.Request) {
	var in CustomerGroupInput
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	g, err := a.Pricing().CreateGroup(r.Context(), in)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusCreated, g)
}

func (a *App) handleGetCustomerGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	g, err := a.Pricing().Group(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, g)
}

func (a *App) handleUpdateCustomerGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var patch CustomerGroupPatch
	if err := DecodeJSON(w, r, &patch); err != nil {
		RespondError(w, r, err)
		return
	}
	g, err := a.Pricing().UpdateGroup(r.Context(), id, patch)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, g)
}

func (a *App) handleDeleteCustomerGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.Pricing().DeleteGroup(r.Context(), id); err != nil {
		RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleListGroupMembers(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	limit, offset, err := Page(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	members, total, err := a.Pricing().Members(r.Context(), id, limit, offset)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	RespondList(w, members, ListMeta{Total: total, Limit: limit, Offset: offset})
}

// memberBody is the address on its own, for the two routes that take one. A
// body rather than a path segment because an email in a URL is an email in an
// access log, and these addresses are the shop's customer list.
type memberBody struct {
	Email string `json:"email"`
}

func (a *App) handleAddGroupMember(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var in memberBody
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.Pricing().AddMember(r.Context(), id, in.Email); err != nil {
		RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleRemoveGroupMember(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var in memberBody
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.Pricing().RemoveMember(r.Context(), id, in.Email); err != nil {
		RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// -------------------------------------------------------------------- lists

func (a *App) handleListPriceLists(w http.ResponseWriter, r *http.Request) {
	lists, err := a.Pricing().Lists(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, lists)
}

func (a *App) handleCreatePriceList(w http.ResponseWriter, r *http.Request) {
	var in PriceListInput
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	l, err := a.Pricing().CreateList(r.Context(), in)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusCreated, l)
}

func (a *App) handleGetPriceList(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	l, err := a.Pricing().List(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, l)
}

func (a *App) handleUpdatePriceList(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var patch PriceListPatch
	if err := DecodeJSON(w, r, &patch); err != nil {
		RespondError(w, r, err)
		return
	}
	l, err := a.Pricing().UpdateList(r.Context(), id, patch)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, l)
}

func (a *App) handleDeletePriceList(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.Pricing().DeleteList(r.Context(), id); err != nil {
		RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleListPrices(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if _, err := a.Pricing().List(r.Context(), id); err != nil {
		RespondError(w, r, err)
		return
	}
	prices, err := a.Pricing().Prices(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, prices)
}

// handleSetPrice writes one row. PUT rather than POST because the row is
// identified by what the caller sends — list, variant and break — so sending it
// twice is the same fact stated twice, not two prices.
func (a *App) handleSetPrice(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var row PriceRow
	if err := DecodeJSON(w, r, &row); err != nil {
		RespondError(w, r, err)
		return
	}
	if row.MinQuantity == 0 {
		// Absent means "any quantity", which is the common case and a nicer
		// default than refusing a body that left the field out.
		row.MinQuantity = 1
	}
	if err := a.Pricing().SetPrice(r.Context(), id, row); err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, row)
}

func (a *App) handleRemovePrice(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	variantID, err := queryInt64(r.URL.Query(), "variant_id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if variantID == 0 {
		RespondError(w, r, Validationf("variant_id is required"))
		return
	}
	minQuantity := 1
	if raw := r.URL.Query().Get("min_quantity"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			RespondError(w, r, Validationf("min_quantity must be a whole number of 1 or more"))
			return
		}
		minQuantity = n
	}
	if err := a.Pricing().RemovePrice(r.Context(), id, variantID, minQuantity); err != nil {
		RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
