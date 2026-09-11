package gocommerce

import (
	"context"
	"net/http"
	"regexp"
	"strings"
)

// The outbox, as an operator reaches it.
//
// Silent notification failure has no remedy an operator can reach: a row that
// exhausted its twelve attempts is parked with the failure text and read by
// nothing but the doctor's own check, whose hint used to send them to psql.
// These four routes are that hint made reachable — read the row, read why, and
// make it go.
//
// One right for all four. No domain right fits: the table is a delivery ledger
// rather than an order, so orders.read would be wrong in kind, and it would
// simultaneously be too wide, because a payload carries the buyer's email,
// phone and name, every line they bought and what they paid — none of which
// customers.read gates on this surface. Requeueing a delivery is an operator's
// act, not a merchandiser's.
func (a *App) mountEventRoutes() {
	a.HandleAdminFunc("GET /api/admin/events", a.handleListEvents, RightStoreOperate)
	a.HandleAdminFunc("GET /api/admin/events/{id}", a.handleGetEvent, RightStoreOperate)
	a.HandleAdminFunc("POST /api/admin/events/{id}/retry", a.handleRetryEvent, RightStoreOperate)
	a.HandleAdminFunc("POST /api/admin/events/retry-dead", a.handleRetryDeadEvents, RightStoreOperate)
}

func (a *App) handleListEvents(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := Page(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	q := r.URL.Query()

	// The state is refused rather than ignored, unlike the audit feed's
	// filters: there are exactly three states, they partition the table, and a
	// fourth word is a typo whose honest answer is "no such thing" rather than
	// an empty page that reads as "nothing has failed".
	state := strings.TrimSpace(q.Get("state"))
	switch state {
	case "", EventStatePending, EventStateDead, EventStatePublished:
	default:
		RespondError(w, r, Validationf("state must be pending, dead or published"))
		return
	}

	aggregateType := strings.TrimSpace(q.Get("aggregate_type"))
	aggregateID, err := queryInt64(q, "aggregate_id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if aggregateID != 0 && aggregateType == "" {
		// outbox_aggregate_idx is on the pair (aggregate_type, aggregate_id,
		// id). The second column alone cannot use it, so this filter would be a
		// sequential scan of every event the store has ever delivered — one
		// screen occasionally taking thirty seconds for no visible reason.
		RespondError(w, r, Validationf("aggregate_id needs aggregate_type: the index is on the pair"))
		return
	}

	events, total, err := a.ListEvents(r.Context(), EventQuery{
		Name:          strings.TrimSpace(q.Get("name")),
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		State:         state,
		Limit:         limit,
		Offset:        offset,
	})
	if err != nil {
		RespondError(w, r, err)
		return
	}
	RespondList(w, events, ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (a *App) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	id, err := pathEventID(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	event, err := a.GetEvent(r.Context(), id)
	respondOr(w, r, event, err)
}

func (a *App) handleRetryEvent(w http.ResponseWriter, r *http.Request) {
	id, err := pathEventID(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// No body is read at all, so an empty POST and `{}` both work. DecodeJSON's
	// DisallowUnknownFields would otherwise force an empty-body dance on a
	// route that has nothing to say.
	event, err := a.RequeueEvent(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// The repair writes no event of its own — announcing a fix to the outbox
	// through the outbox would put it inside the thing being repaired, and a
	// broken handler would dead-letter the announcement too. This log line is
	// the trail: the state it comes back in says which act it was, since an
	// un-parked row reads attempts 0 and an expedited one keeps its budget.
	logFrom(r).Info("outbox event requeued",
		"event_id", id, "event", event.Name, "state", event.State,
		"attempts", event.Attempts, "by", eventActor(r.Context()))
	Respond(w, http.StatusOK, event)
}

func (a *App) handleRetryDeadEvents(w http.ResponseWriter, r *http.Request) {
	// The filter rides in the query string, as ?dry_run= already does on the
	// import routes, rather than in a body a bodyless POST would have to fake.
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	requeued, remaining, err := a.RequeueDeadEvents(r.Context(), name, requeueDeadMax)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	logFrom(r).Info("outbox dead letters requeued",
		"requeued", requeued, "remaining", remaining, "name", name, "by", eventActor(r.Context()))
	// Not a 404 when nothing moved: requeueing nothing is a truthful zero, not
	// a missing resource.
	Respond(w, http.StatusOK, map[string]any{"requeued": requeued, "remaining": remaining})
}

// ------------------------------------------------------------------ helpers

var eventIDRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// pathEventID reads the event's uuid.
//
// The uuid and not the bigserial: it is the stable identity docs/events.md
// promises, what outbox.write generates, what every log line prints and what a
// handler dedupes on — so it is the value an operator actually has in hand when
// chasing a failure. The bigserial is still visible as `seq`, because it is the
// dispatcher's ordering key and the list sorts on it.
//
// Validated here rather than left to PostgreSQL: `WHERE event_id = 'nonsense'`
// raises a cast error, which RespondError would render as a 500 for what is
// plainly a bad request.
func pathEventID(r *http.Request) (string, error) {
	raw := strings.TrimSpace(r.PathValue("id"))
	if !eventIDRE.MatchString(raw) {
		return "", Validationf("id must be an event uuid")
	}
	return raw, nil
}

// eventActor names whoever is acting, for a log line.
//
// Through auditActor rather than by reading SuperuserFrom here, because that is
// the engine's one actor seam and a second reader of the context is how the two
// come to disagree about who did something. A static admin token carries no
// superuser — requireRights lets it straight through — so it comes back as its
// kind, which is more useful than an empty field.
func eventActor(ctx context.Context) string {
	kind, _, email, _, label := auditActor(ctx)
	switch {
	case email != "":
		return email
	case label != "":
		return label
	default:
		return kind
	}
}
