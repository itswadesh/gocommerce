package gocommerce

import "net/http"

func (a *App) mountPasswordResetRoutes() {
	// Unauthenticated, and necessarily so: a reset cannot require the credential
	// it replaces, for the same reason a login route cannot. The emailed token
	// is the credential — the arrangement the invitation accept pair uses.
	a.HandleFunc("POST /api/admin/password-reset", a.handleRequestPasswordReset)
	a.HandleFunc("GET /api/admin/password-reset/{token}", a.handleLookupPasswordReset)
	a.HandleFunc("POST /api/admin/password-reset/confirm", a.handleConfirmPasswordReset)

	// Mailing somebody a link instead of choosing a password for them. Not an
	// escalation: team.write can already set that operator's password outright
	// through PATCH /api/admin/superusers/{id}.
	a.HandleAdminFunc("POST /api/admin/superusers/{id}/password-reset",
		a.handleSendPasswordReset, RightTeamWrite)
}

// handleRequestPasswordReset always answers 202, and answers it identically for
// an address that belongs to an operator and one that does not. Nothing in the
// response, its status or its timing may vary with that fact — the login
// endpoint already refuses to be an account oracle, and this route would undo it.
func (a *App) handleRequestPasswordReset(w http.ResponseWriter, r *http.Request) {
	var in struct {
		// "identity" for PocketBase compatibility, "email" as a synonym, the
		// same pair handleAuthWithPassword accepts and for the same reason.
		Identity string `json:"identity"`
		Email    string `json:"email"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	identity := in.Identity
	if identity == "" {
		identity = in.Email
	}
	out, err := a.superusers.RequestReset(r.Context(), identity, clientIP(r))
	if err != nil {
		// Only a malformed address (400) and the throttle (429) reach here, and
		// neither depends on whether an account exists.
		RespondError(w, r, err)
		return
	}
	// Accepted rather than OK: the honest status for "this has been taken in",
	// when whether anything is on its way is exactly what the route will not say.
	Respond(w, http.StatusAccepted, out)
}

func (a *App) handleLookupPasswordReset(w http.ResponseWriter, r *http.Request) {
	target, err := a.superusers.LookupReset(r.Context(), r.PathValue("token"), clientIP(r))
	respondOr(w, r, target, err)
}

func (a *App) handleConfirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	var in struct {
		// The token travels in the body rather than the path, so the one
		// request that actually spends it is not repeated in an access log's
		// request line.
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	su, sess, err := a.superusers.ConfirmReset(r.Context(), in.Token, in.Password, clientIP(r))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// The log is where operator-account facts go: this flow writes no outbox
	// event, for the reason passwordreset.go gives.
	a.log.Info("superuser password reset", "email", su.Email)
	// 200 and not 201: unlike accepting an invitation, nothing was created.
	Respond(w, http.StatusOK, authResponse{
		Token:     sess.Token,
		ExpiresAt: sess.ExpiresAt.UTC(),
		Record:    su,
	})
}

func (a *App) handleSendPasswordReset(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	out, err := a.superusers.RequestResetFor(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// The same shape the public route returns, so the panel reads one field
	// either way.
	Respond(w, http.StatusAccepted, out)
}
