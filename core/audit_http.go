package gocommerce

import (
	"net/http"
	"strconv"
)

// Reading the trail. There is no write route and there never will be one: the
// table is append-only because nothing outside writeAudit can reach it, and a
// route that could would be the one way to un-write history.
//
// Six per-record routes rather than one generic
// /api/admin/audit/{entity_type}/{entity_id}, because rights on a route are
// static and enforced by the router. A right resolved inside a handler would be
// a second place where permissions are decided, which rights.go's header
// forbids, and TestEveryAdminRouteDeclaresRights rejects an ungated admin route
// outright.
//
// Each record's history follows that record's own read right rather than
// store.operate, so the manager who has to ask "who refunded this" can answer
// it. Filtering the whole store by person is a different question — that is
// surveillance of the team, and it is what store.operate gates.
func (a *App) mountAuditRoutes() {
	a.HandleAdminFunc("GET /api/admin/audit", a.handleAuditFeed, RightStoreOperate)
	a.HandleAdminFunc("GET /api/admin/audit/actors", a.handleAuditActors, RightStoreOperate)
	a.HandleAdminFunc("GET /api/admin/products/{id}/history", a.entityHistory(AuditEntityProduct), RightCatalogRead)
	// A variant's history is its stock movements, which are counts rather than
	// listings — the line catalog.read and inventory.read are already drawn
	// along. Catalog edits to a variant are filed against its product instead,
	// which is what removes the need for an any-of check requireRights cannot
	// express.
	a.HandleAdminFunc("GET /api/admin/variants/{id}/history", a.entityHistory(AuditEntityStock), RightInventoryRead)
	a.HandleAdminFunc("GET /api/admin/categories/{id}/history", a.entityHistory(AuditEntityCategory), RightCatalogRead)
	a.HandleAdminFunc("GET /api/admin/discounts/{id}/history", a.entityHistory(AuditEntityDiscount), RightDiscountsRead)
	a.HandleAdminFunc("GET /api/admin/tax-rates/{id}/history", a.entityHistory(AuditEntityTaxRate), RightTaxesRead)
	a.HandleAdminFunc("GET /api/admin/locations/{id}/history", a.entityHistory(AuditEntityLocation), RightLocationsRead)
}

// entityHistory serves one record's own history. One closure for every
// per-record route, because the only thing that differs between them is which
// entity type to ask for and which right the router already checked.
func (a *App) entityHistory(entityType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		// Never 404 on an unknown id: the log knowing nothing about a record is
		// not the record being absent, and every record that existed before
		// migration 0020 ran has an honestly empty history.
		rows, total, err := a.audit.OfEntity(r.Context(), entityType, strconv.FormatInt(id, 10), limit, offset)
		if err != nil {
			RespondError(w, r, err)
			return
		}
		RespondList(w, rows, ListMeta{Total: total, Limit: limit, Offset: offset})
	}
}

func (a *App) handleAuditFeed(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := Page(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	q := r.URL.Query()
	// An unknown actor_kind, action or entity_type returns an empty page rather
	// than 400. A feed that refuses a stale filter is a screen an operator
	// cannot get out of.
	query := AuditQuery{
		ActorKind:  q.Get("actor_kind"),
		Action:     q.Get("action"),
		EntityType: q.Get("entity_type"),
		EntityID:   q.Get("entity_id"),
		Limit:      limit,
		Offset:     offset,
	}
	if s := q.Get("actor_id"); s != "" {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil || id <= 0 {
			RespondError(w, r, Validationf("actor_id must be a positive integer"))
			return
		}
		query.ActorID = &id
	}
	if query.From, err = parseDate(q.Get("from")); err != nil {
		RespondError(w, r, err)
		return
	}
	if query.To, err = parseDate(q.Get("to")); err != nil {
		RespondError(w, r, err)
		return
	}
	rows, total, err := a.audit.List(r.Context(), query)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	RespondList(w, rows, ListMeta{Total: total, Limit: limit, Offset: offset})
}

// handleAuditActors has no meta block: it is a short bounded list, not a page
// of one.
func (a *App) handleAuditActors(w http.ResponseWriter, r *http.Request) {
	actors, err := a.audit.Actors(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if actors == nil {
		actors = []AuditActor{}
	}
	Respond(w, http.StatusOK, actors)
}
