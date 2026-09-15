package gocommerce

import "net/http"

func (a *App) mountNotificationRoutes() {
	// orders.read to look, orders.write to send again: a notification is an
	// order's communication, and the person who may read the order may see
	// what it was told; sending is acting on it.
	a.HandleAdminFunc("GET /api/admin/notifications", a.handleListNotifications, RightOrdersRead)
	a.HandleAdminFunc("GET /api/admin/notifications/{id}", a.handleGetNotification, RightOrdersRead)
	a.HandleAdminFunc("POST /api/admin/notifications/{id}/resend", a.handleResendNotification, RightOrdersWrite)
}

func (a *App) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := Page(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	q := r.URL.Query()
	query := NotificationQuery{
		Channel: q.Get("channel"), Status: q.Get("status"), Event: q.Get("event"), Search: q.Get("q"),
		Limit: limit, Offset: offset,
	}
	switch query.Status {
	case "", NotificationSent, NotificationLogged, NotificationFailed:
	default:
		RespondError(w, r, Validationf("status must be sent, logged or failed"))
		return
	}
	rows, total, err := a.notifications.List(r.Context(), query)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	RespondList(w, rows, ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (a *App) handleGetNotification(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	rec, err := a.notifications.Get(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, rec)
}

func (a *App) handleResendNotification(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	rec, err := a.notifications.Resend(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, rec)
}
