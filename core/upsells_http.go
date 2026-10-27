package gocommerce

import (
	"net/http"
	"strconv"
	"strings"
)

// mountUpsellRoutes serves add-ons, cross-sells and what a basket is offered
// (D133). The product's two are read by query rather than under
// /api/products/{id}/…, which /api/products/slug/{slug} would make ambiguous,
// and as /api/variants?product_id= already is.
func (a *App) mountUpsellRoutes() {
	a.HandleFunc("GET /api/add-ons", a.handlePublicAddOns)
	a.HandleFunc("GET /api/cross-sells", a.handlePublicCrossSells)
	a.HandleFunc("GET /api/carts/{cartId}/cross-sells", a.handleCartCrossSells)
	a.HandleFunc("GET /api/carts/{cartId}/offers", a.handleCartOffers)
	a.HandleFunc("GET /api/carts/{cartId}/free-shipping", a.handleCartFreeShipping)

	// Choosing what sits beside a product, and at what price, is
	// merchandising: the same pair that edits the product.
	a.HandleAdminFunc("GET /api/admin/products/{id}/add-ons", a.handleAdminAddOns, RightCatalogRead)
	a.HandleAdminFunc("PUT /api/admin/products/{id}/add-ons", a.handleSetAddOns, RightCatalogWrite)
	a.HandleAdminFunc("GET /api/admin/products/{id}/cross-sells", a.handleAdminCrossSells, RightCatalogRead)
	a.HandleAdminFunc("PUT /api/admin/products/{id}/cross-sells", a.handleSetCrossSells, RightCatalogWrite)
}

// publishedProductID reads ?product_id= and answers 404 for a product a shopper
// cannot see, so a draft's add-ons are as hidden as the draft.
func (a *App) publishedProductID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.URL.Query().Get("product_id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, Validationf("product_id must be a positive integer")
	}
	p, err := a.catalog.GetProduct(r.Context(), id)
	if err != nil {
		return 0, err
	}
	if p.Status != ProductActive {
		return 0, NotFoundf("product not found")
	}
	return id, nil
}

func limitParam(r *http.Request, fallback int) int {
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		return n
	}
	return fallback
}

func (a *App) handlePublicAddOns(w http.ResponseWriter, r *http.Request) {
	id, err := a.publishedProductID(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	out, err := a.upsells.AddOns(r.Context(), id, true)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, out)
}

func (a *App) handlePublicCrossSells(w http.ResponseWriter, r *http.Request) {
	id, err := a.publishedProductID(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	out, err := a.upsells.CrossSells(r.Context(), id, limitParam(r, maxCrossSells))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	a.translateProducts(r, out)
	Respond(w, http.StatusOK, out)
}

func (a *App) handleCartCrossSells(w http.ResponseWriter, r *http.Request) {
	cart, err := a.carts.GetByToken(r.Context(), r.PathValue("cartId"))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	out, err := a.upsells.CartCrossSells(r.Context(), cart, limitParam(r, 12))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	a.translateProducts(r, out)
	Respond(w, http.StatusOK, out)
}

func (a *App) handleCartOffers(w http.ResponseWriter, r *http.Request) {
	cart, err := a.carts.GetByToken(r.Context(), r.PathValue("cartId"))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	out, err := a.discounts.CartOffers(r.Context(), cart)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, out)
}

// handleCartFreeShipping answers how far the basket is from free delivery.
// With no country it reads the store's own, which is where a basket is
// usually going before the shopper has typed an address.
func (a *App) handleCartFreeShipping(w http.ResponseWriter, r *http.Request) {
	cart, err := a.carts.GetByToken(r.Context(), r.PathValue("cartId"))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	country := strings.TrimSpace(r.URL.Query().Get("country"))
	if country == "" {
		if p, err := a.profile.Get(r.Context()); err == nil {
			country = p.Country
		}
	}
	weight, err := cartWeightGrams(r.Context(), a.db, cart.ID)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	out, err := a.shipping.FreeShipping(r.Context(), ShippingQuery{
		Country:       country,
		State:         r.URL.Query().Get("state"),
		SubtotalMinor: cart.Subtotal.AmountMinor,
		WeightGrams:   weight,
		Email:         cart.VerifiedEmail,
	})
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, out)
}

func (a *App) handleAdminAddOns(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if err := requireProduct(r.Context(), a.db, id); err != nil {
		RespondError(w, r, err)
		return
	}
	out, err := a.upsells.AddOns(r.Context(), id, false)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, out)
}

func (a *App) handleSetAddOns(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var in struct {
		AddOns []AddOnInput `json:"add_ons"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	out, err := a.upsells.SetAddOns(r.Context(), id, in.AddOns)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, out)
}

func (a *App) handleAdminCrossSells(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if err := requireProduct(r.Context(), a.db, id); err != nil {
		RespondError(w, r, err)
		return
	}
	a.respondCrossSells(w, r, id)
}

func (a *App) handleSetCrossSells(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var in struct {
		ProductIDs []int64 `json:"product_ids"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.upsells.SetCrossSells(r.Context(), id, in.ProductIDs); err != nil {
		RespondError(w, r, err)
		return
	}
	a.respondCrossSells(w, r, id)
}

// respondCrossSells answers the panel with the ids in order and, beside them,
// enough of each product to draw its chip, drafts included.
func (a *App) respondCrossSells(w http.ResponseWriter, r *http.Request, id int64) {
	ids, err := a.upsells.CrossSellIDs(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	type chip struct {
		ID       int64  `json:"id"`
		Title    string `json:"title"`
		Status   string `json:"status"`
		ImageURL string `json:"image_url,omitempty"`
	}
	chips := []chip{}
	if len(ids) > 0 {
		products, _, err := a.catalog.ListProducts(r.Context(), ProductQuery{IDs: ids, Limit: len(ids)})
		if err != nil {
			RespondError(w, r, err)
			return
		}
		for _, p := range products {
			chips = append(chips, chip{ID: p.ID, Title: p.Title, Status: p.Status, ImageURL: p.ImageURL})
		}
	}
	Respond(w, http.StatusOK, map[string]any{"product_ids": ids, "products": chips})
}
