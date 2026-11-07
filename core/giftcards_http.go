package gocommerce

import (
	"net/http"
	"strconv"
)

func (a *App) mountGiftCardRoutes() {
	// A shopper offers a code and is told what it has left. Nothing is held
	// until checkout, which decides again under its own lock.
	a.HandleFunc("PUT /api/carts/{token}/gift-card", a.handleSetCartGiftCard)
	a.HandleFunc("DELETE /api/carts/{token}/gift-card", a.handleClearCartGiftCard)

	// Under the discounts rights: a gift card is money a store gives away or
	// sells, decided by whoever runs its promotions.
	a.HandleAdminFunc("GET /api/admin/gift-cards", a.handleListGiftCards, RightDiscountsRead)
	a.HandleAdminFunc("POST /api/admin/gift-cards", a.handleIssueGiftCard, RightDiscountsWrite)
	a.HandleAdminFunc("GET /api/admin/gift-cards/{id}", a.handleGetGiftCard, RightDiscountsRead)
	a.HandleAdminFunc("PATCH /api/admin/gift-cards/{id}", a.handleUpdateGiftCard, RightDiscountsWrite)
}

func (a *App) handleSetCartGiftCard(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	if NormalizeGiftCardCode(in.Code) == "" {
		RespondError(w, r, Validationf("a gift card code is required"))
		return
	}
	balance, err := a.giftCards.SetCartGiftCard(r.Context(), r.PathValue("token"), in.Code)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, balance)
}

func (a *App) handleClearCartGiftCard(w http.ResponseWriter, r *http.Request) {
	if _, err := a.giftCards.SetCartGiftCard(r.Context(), r.PathValue("token"), ""); err != nil {
		RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleListGiftCards(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := Page(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	list, total, err := a.giftCards.List(r.Context(), GiftCardQuery{
		Search: r.URL.Query().Get("q"), Limit: limit, Offset: offset})
	if err != nil {
		RespondError(w, r, err)
		return
	}
	RespondList(w, list, ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (a *App) handleIssueGiftCard(w http.ResponseWriter, r *http.Request) {
	var in GiftCardInput
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	g, err := a.giftCards.Issue(r.Context(), in)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusCreated, g)
}

func (a *App) handleGetGiftCard(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		RespondError(w, r, NotFoundf("gift card not found"))
		return
	}
	g, err := a.giftCards.Get(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, g)
}

func (a *App) handleUpdateGiftCard(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		RespondError(w, r, NotFoundf("gift card not found"))
		return
	}
	var p GiftCardPatch
	if err := DecodeJSON(w, r, &p); err != nil {
		RespondError(w, r, err)
		return
	}
	g, err := a.giftCards.Update(r.Context(), id, p)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, g)
}
