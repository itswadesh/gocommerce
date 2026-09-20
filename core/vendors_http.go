package gocommerce

import (
	"net/http"
	"strings"
)

// The vendors' HTTP surface.
//
// Two audiences with different answers. An operator sees every seller and every
// offer, including the pending applications and the paused offers, because
// running a marketplace is mostly looking at those. A shopper sees approved
// sellers and active offers and nothing else — a pending application is not a
// shop somebody should be able to browse to, and the difference between the two
// listings is the whole of the moderation this table exists for.
func (a *App) mountVendorRoutes() {
	// Open to sellers, every one of them narrowed by VendorScopeOf inside the
	// handler. Creating a vendor is not on the list: a seller signing up
	// another seller is not a thing a marketplace wants.
	a.HandleVendorFunc("GET /api/admin/vendors", a.handleListVendors, RightVendorsRead)
	a.HandleAdminFunc("POST /api/admin/vendors", a.handleCreateVendor, RightVendorsWrite)
	a.HandleVendorFunc("GET /api/admin/vendors/{id}", a.handleGetVendor, RightVendorsRead)
	a.HandleVendorFunc("PATCH /api/admin/vendors/{id}", a.handleUpdateVendor, RightVendorsWrite)
	a.HandleVendorFunc("DELETE /api/admin/vendors/{id}", a.handleDeleteVendor, RightVendorsWrite)

	a.HandleVendorFunc("GET /api/admin/vendors/{id}/offers", a.handleVendorOffers, RightVendorsRead)
	a.HandleVendorFunc("PUT /api/admin/vendors/{id}/offers", a.handleSetOffer, RightVendorsWrite)
	a.HandleVendorFunc("DELETE /api/admin/vendors/{id}/offers/{variantId}", a.handleDeleteOffer, RightVendorsWrite)
	// Filed under the variant because "who sells this" is the question a
	// product screen asks, and it is not a question about one vendor.
	a.HandleVendorFunc("GET /api/admin/variants/{id}/offers", a.handleVariantOffers, RightVendorsRead)

	// Opening a login for a seller is giving somebody access to this store, so
	// it is gated on the team right rather than on vendors.write: a marketplace
	// manager who may edit a seller's commission is not thereby somebody who may
	// hand out accounts.
	a.HandleAdminFunc("POST /api/admin/vendors/{id}/users", a.handleCreateVendorUser, RightTeamWrite)

	a.HandleFunc("GET /api/vendors", a.handlePublicVendors)
	a.HandleFunc("GET /api/vendors/{slug}", a.handlePublicVendor)
	a.HandleFunc("GET /api/variants/{id}/offers", a.handlePublicVariantOffers)
}

func (a *App) handleListVendors(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := Page(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	list, total, err := a.Vendors().List(r.Context(), VendorQuery{
		Search: r.URL.Query().Get("q"),
		Status: VendorStatus(strings.TrimSpace(r.URL.Query().Get("status"))),
		Limit:  limit,
		Offset: offset,
		// A seller listing vendors sees one: themselves. The count is narrowed
		// with the page rather than only the rows, because a total of 40 over a
		// page of 1 tells them exactly how many competitors they have.
		OnlyID: VendorScopeOf(r.Context()),
	})
	if err != nil {
		RespondError(w, r, err)
		return
	}
	RespondList(w, list, ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (a *App) handleCreateVendor(w http.ResponseWriter, r *http.Request) {
	var in VendorInput
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	v, err := a.Vendors().Create(r.Context(), in)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusCreated, v)
}

func (a *App) handleGetVendor(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// A seller may only ever mean themselves here. Any other id is reported as
	// not found, so a competitor cannot confirm one exists by watching the
	// status code change.
	if err := requireOwnVendor(r, id); err != nil {
		RespondError(w, r, err)
		return
	}
	v, err := a.Vendors().Get(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, v)
}

func (a *App) handleUpdateVendor(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// A seller may only ever mean themselves here. Any other id is reported as
	// not found, so a competitor cannot confirm one exists by watching the
	// status code change.
	if err := requireOwnVendor(r, id); err != nil {
		RespondError(w, r, err)
		return
	}
	var patch VendorPatch
	if err := DecodeJSON(w, r, &patch); err != nil {
		RespondError(w, r, err)
		return
	}
	v, err := a.Vendors().Update(r.Context(), id, patch)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, v)
}

func (a *App) handleDeleteVendor(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// A seller may only ever mean themselves here. Any other id is reported as
	// not found, so a competitor cannot confirm one exists by watching the
	// status code change.
	if err := requireOwnVendor(r, id); err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.Vendors().Delete(r.Context(), id); err != nil {
		RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleVendorOffers(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// A seller may only ever mean themselves here. Any other id is reported as
	// not found, so a competitor cannot confirm one exists by watching the
	// status code change.
	if err := requireOwnVendor(r, id); err != nil {
		RespondError(w, r, err)
		return
	}
	offers, err := a.Vendors().OffersForVendor(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, offers)
}

func (a *App) handleSetOffer(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// A seller may only ever mean themselves here. Any other id is reported as
	// not found, so a competitor cannot confirm one exists by watching the
	// status code change.
	if err := requireOwnVendor(r, id); err != nil {
		RespondError(w, r, err)
		return
	}
	var in OfferInput
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	offer, err := a.Vendors().SetOffer(r.Context(), id, in)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, offer)
}

func (a *App) handleDeleteOffer(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// A seller may only ever mean themselves here. Any other id is reported as
	// not found, so a competitor cannot confirm one exists by watching the
	// status code change.
	if err := requireOwnVendor(r, id); err != nil {
		RespondError(w, r, err)
		return
	}
	variantID, err := pathInt64(r, "variantId")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.Vendors().DeleteOffer(r.Context(), id, variantID); err != nil {
		RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleVariantOffers(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	offers, err := a.Vendors().OffersForVariant(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// Filtered rather than refused: "who sells this variant" is a fair question
	// for a seller to ask, and their own offer is the part of the answer they
	// are entitled to. What is being kept from them is the competitor's price.
	if scope := VendorScopeOf(r.Context()); scope != nil {
		mine := []*Offer{}
		for _, o := range offers {
			if o.VendorID == *scope {
				mine = append(mine, o)
			}
		}
		offers = mine
	}
	Respond(w, http.StatusOK, offers)
}

// vendorUserInput opens a login for a seller.
type vendorUserInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (a *App) handleCreateVendorUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var in vendorUserInput
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	su, err := a.Superusers().CreateForVendor(r.Context(), id, in.Email, in.Password)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusCreated, su)
}

// ---------------------------------------------------------------- storefront

// publicVendor is the seller as a shopper may see them.
//
// A narrower struct rather than the whole row with fields blanked, because
// blanking is something a future edit forgets to do. Commission, the tax id and
// the email are the store's business with its seller and none of the shopper's.
type publicVendor struct {
	ID      int64         `json:"id"`
	Slug    string        `json:"slug"`
	Name    string        `json:"name"`
	About   string        `json:"about"`
	Website string        `json:"website"`
	LogoID  *int64        `json:"logo_media_id"`
	Address VendorAddress `json:"address"`
}

func asPublicVendor(v *Vendor) publicVendor {
	return publicVendor{
		ID: v.ID, Slug: v.Slug, Name: v.Name, About: v.About,
		Website: v.Website, LogoID: v.LogoMediaID,
		// The trading address is on the invoice anyway, so it is not a secret.
		Address: v.Address,
	}
}

func (a *App) handlePublicVendors(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := Page(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// Approved only, and not from a query parameter: a storefront must not be
	// able to ask for the pending ones by adding ?status=pending.
	list, total, err := a.Vendors().List(r.Context(), VendorQuery{
		Search: r.URL.Query().Get("q"),
		Status: VendorApproved,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		RespondError(w, r, err)
		return
	}
	out := make([]publicVendor, 0, len(list))
	for _, v := range list {
		out = append(out, asPublicVendor(v))
	}
	RespondList(w, out, ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (a *App) handlePublicVendor(w http.ResponseWriter, r *http.Request) {
	v, err := a.Vendors().GetBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// A pending or suspended seller is not found rather than forbidden: that a
	// slug is taken is itself something a shopper has no business learning.
	if v.Status != VendorApproved {
		RespondError(w, r, NotFoundf("vendor not found"))
		return
	}
	Respond(w, http.StatusOK, asPublicVendor(v))
}

// publicOffer is one seller's price, without their stock position.
//
// How many a seller is holding is commercial information about them, and a
// shopper needs one fact from it: whether they can buy one. So availability
// crosses as a boolean.
type publicOffer struct {
	VendorID  int64  `json:"vendor_id"`
	Vendor    string `json:"vendor"`
	Slug      string `json:"vendor_slug"`
	Price     Money  `json:"price"`
	InStock   bool   `json:"in_stock"`
	VariantID int64  `json:"variant_id"`
}

func (a *App) handlePublicVariantOffers(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	offers, err := a.Vendors().OffersForVariant(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}

	out := []publicOffer{}
	for _, o := range offers {
		if o.Status != OfferActive {
			continue
		}
		v, err := a.Vendors().Get(r.Context(), o.VendorID)
		if err != nil {
			return
		}
		if v.Status != VendorApproved {
			continue
		}
		out = append(out, publicOffer{
			VendorID: v.ID, Vendor: v.Name, Slug: v.Slug,
			Price:     o.Price,
			InStock:   !o.TrackInventory || o.Available > 0,
			VariantID: o.VariantID,
		})
	}
	Respond(w, http.StatusOK, out)
}
