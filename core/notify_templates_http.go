package gocommerce

import "net/http"

// The wording is store configuration, so it takes store.operate in both
// directions — reading it is not reading orders, and nothing on it is a
// secret, but the page it lives on is the one where credentials are pasted.
func (a *App) mountNotifyTemplateRoutes() {
	a.HandleAdminFunc("GET /api/admin/notifications/templates", a.handleListNotifyTemplates, RightNotificationsRead)
	a.HandleAdminFunc("PUT /api/admin/notifications/templates/{channel}/{event}", a.handleSetNotifyTemplate, RightNotificationsWrite)
	a.HandleAdminFunc("DELETE /api/admin/notifications/templates/{channel}/{event}", a.handleResetNotifyTemplate, RightNotificationsWrite)
	// Sending a test is setting up the channel, so it asks what rewording a
	// message asks.
	a.HandleAdminFunc("POST /api/admin/notifications/test-email", a.handleSendTestEmail, RightNotificationsWrite)
}

func (a *App) handleListNotifyTemplates(w http.ResponseWriter, r *http.Request) {
	channel := r.URL.Query().Get("channel")
	switch channel {
	case "", ChannelEmail, ChannelSMS:
	default:
		RespondError(w, r, Validationf("channel must be email or sms"))
		return
	}
	out, err := a.notifyTemplates.ListIn(r.Context(), channel, r.URL.Query().Get("language"))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, out)
}

func (a *App) handleSetNotifyTemplate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	out, err := a.notifyTemplates.SetIn(r.Context(), r.PathValue("channel"), r.PathValue("event"),
		r.URL.Query().Get("language"), in.Subject, in.Body)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, out)
}

func (a *App) handleResetNotifyTemplate(w http.ResponseWriter, r *http.Request) {
	out, err := a.notifyTemplates.ResetIn(r.Context(), r.PathValue("channel"), r.PathValue("event"), r.URL.Query().Get("language"))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, out)
}
