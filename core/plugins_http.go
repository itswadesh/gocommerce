package gocommerce

import "net/http"

func (a *App) mountPluginRoutes() {
	// store.operate for both halves: switching a storefront feature on and
	// pasting an analytics key are the same act as registering a webhook,
	// and reading the masked settings is the page that does it.
	a.HandleAdminFunc("GET /api/admin/plugins", a.handleListPlugins, RightPluginsRead)
	a.HandleAdminFunc("GET /api/admin/plugins/{key}", a.handleGetPlugin, RightPluginsRead)
	a.HandleAdminFunc("PATCH /api/admin/plugins/{key}", a.handleUpdatePlugin, RightPluginsWrite)
	// The storefront's half: what is on, and the settings meant for it.
	a.HandleFunc("GET /api/plugins", a.handlePublicPlugins)
}

func (a *App) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	list, err := a.plugins.List(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, list)
}

func (a *App) handleGetPlugin(w http.ResponseWriter, r *http.Request) {
	p, err := a.plugins.Get(r.Context(), r.PathValue("key"))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, p)
}

func (a *App) handleUpdatePlugin(w http.ResponseWriter, r *http.Request) {
	var patch PluginPatch
	if err := DecodeJSON(w, r, &patch); err != nil {
		RespondError(w, r, err)
		return
	}
	p, err := a.plugins.Update(r.Context(), r.PathValue("key"), patch)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, p)
}

func (a *App) handlePublicPlugins(w http.ResponseWriter, r *http.Request) {
	list, err := a.plugins.Public(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, list)
}
