package gocommerce

import "net/http"

func (a *App) mountChannelRoutes() {
	// Storefronts are how the store is wired to the world — the same kind of
	// thing as shipping zones and the outbox — so they take `store.operate`
	// rather than a right of their own, and core/rights.go stays closed (D24).
	a.HandleAdminFunc("GET /api/admin/channels", a.handleListChannels, RightStoreOperate)
	a.HandleAdminFunc("POST /api/admin/channels", a.handleCreateChannel, RightStoreOperate)
	a.HandleAdminFunc("GET /api/admin/channels/{id}", a.handleGetChannel, RightStoreOperate)
	a.HandleAdminFunc("PATCH /api/admin/channels/{id}", a.handleUpdateChannel, RightStoreOperate)
	a.HandleAdminFunc("DELETE /api/admin/channels/{id}", a.handleDeleteChannel, RightStoreOperate)

	// Which channels a product is narrowed to is a merchandising decision, so
	// it takes the catalogue's rights rather than store.operate: the person who
	// decides a product exists decides where it sells.
	a.HandleAdminFunc("GET /api/admin/products/{id}/channels", a.handleProductChannels, RightCatalogRead)
	a.HandleAdminFunc("PUT /api/admin/products/{id}/channels", a.handleSetProductChannels, RightCatalogWrite)
}

func (a *App) handleListChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := a.Channels().List(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, channels)
}

func (a *App) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	var in ChannelInput
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	c, err := a.Channels().Create(r.Context(), in)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusCreated, c)
}

func (a *App) handleGetChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	c, err := a.Channels().Get(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, c)
}

func (a *App) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var patch ChannelPatch
	if err := DecodeJSON(w, r, &patch); err != nil {
		RespondError(w, r, err)
		return
	}
	c, err := a.Channels().Update(r.Context(), id, patch)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, c)
}

func (a *App) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.Channels().Delete(r.Context(), id); err != nil {
		RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleProductChannels(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	ids, err := a.Channels().ChannelsOf(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, ids)
}

// handleSetProductChannels replaces the narrowing whole. An empty list clears
// it, which puts the product back in every channel rather than in none — see
// M35 for why that is the only safe reading of an empty set.
func (a *App) handleSetProductChannels(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var in struct {
		ChannelIDs []int64 `json:"channel_ids"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.Channels().Publish(r.Context(), id, in.ChannelIDs); err != nil {
		RespondError(w, r, err)
		return
	}
	ids, err := a.Channels().ChannelsOf(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, ids)
}
