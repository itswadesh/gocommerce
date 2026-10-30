package gocommerce

import (
	"net/http"
	"strings"
)

// India's GST reference lists (D172): the state codes and the HSN/SAC master.
// Both are compiled into the binary and the same for every store, so they
// are read with catalog.read — the right every role holds — like the
// taxonomy master is read with the catalog.
func (a *App) mountMasterListRoutes() {
	a.HandleAdminFunc("GET /api/admin/gst-states", a.handleGSTStates, RightCatalogRead)
	a.HandleAdminFunc("GET /api/admin/hsn", a.handleSearchHSN, RightCatalogRead)
	a.HandleAdminFunc("GET /api/admin/hsn/{code}", a.handleGetHSN, RightCatalogRead)
}

func (a *App) handleGSTStates(w http.ResponseWriter, r *http.Request) {
	RespondList(w, gstStates, ListMeta{Total: len(gstStates), Limit: len(gstStates)})
}

// handleSearchHSN answers ?q= with a page of matches. No q is an empty page
// rather than 22,000 codes: the list is searched, not scrolled.
func (a *App) handleSearchHSN(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := Page(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	list, total := SearchHSN(strings.TrimSpace(r.URL.Query().Get("q")), limit, offset)
	RespondList(w, list, ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (a *App) handleGetHSN(w http.ResponseWriter, r *http.Request) {
	c, ok := LookupHSN(r.PathValue("code"))
	if !ok {
		RespondError(w, r, NotFoundf("%q is not in the HSN/SAC master", r.PathValue("code")))
		return
	}
	Respond(w, http.StatusOK, c)
}
