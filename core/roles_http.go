package gocommerce

import "net/http"

// Role routes. Reading the matrix and writing it sit behind the same right,
// roles.write, because they are the same act at different speeds: an operator
// who can see exactly which rights each role is missing is holding the map of
// the store's access, and the only people with a use for it are the people who
// may redraw it. An operator's own rights reach them on their record, so nobody
// needs this endpoint to find out what they themselves may do.
//
// Apart from team.write on purpose: staffing the shop and rewriting the rules
// it is staffed under are different powers, and a store may want to hand out
// the first without the second.
func (a *App) mountRoleRoutes() {
	a.HandleAdminFunc("GET /api/admin/roles", a.handleListRoles, RightRolesWrite)
	a.HandleAdminFunc("POST /api/admin/roles", a.handleCreateRole, RightRolesWrite)
	// The names alone, for the screens that hand a role out. Behind team.read,
	// not roles.write: choosing somebody's role is staffing the shop, and a
	// store may let a manager staff it without letting them redraw the rules.
	a.HandleAdminFunc("GET /api/admin/roles/names", a.handleRoleNames, RightTeamRead)
	a.HandleAdminFunc("PUT /api/admin/roles/{role}", a.handleSetRoleRights, RightRolesWrite)
	// DELETE removes the role now that a store can make one (D80); going back
	// to the engine's defaults is its own verb, and only the starting roles
	// have defaults to go back to.
	a.HandleAdminFunc("DELETE /api/admin/roles/{role}", a.handleDeleteRole, RightRolesWrite)
	a.HandleAdminFunc("POST /api/admin/roles/{role}/reset", a.handleResetRoleRights, RightRolesWrite)
	// What the store calls the role, apart from what the role may do. PATCH
	// rather than folding it into the PUT above: the two are edited at
	// different moments and by different intentions, and a screen that saved a
	// rename by sending the whole right set would overwrite a colleague's
	// grant made a second earlier.
	a.HandleAdminFunc("PATCH /api/admin/roles/{role}", a.handleSetRoleProfile, RightRolesWrite)
}

// handleListRoles returns the whole matrix: every role, the closed list of
// rights, and the floor. One response rather than three, because a client
// rendering a grid needs all of it and a client rendering half of it draws
// something wrong.
func (a *App) handleListRoles(w http.ResponseWriter, r *http.Request) {
	matrix, err := a.roles.Matrix(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, matrix)
}

// handleSetRoleRights replaces one role's set.
//
// PUT and not PATCH: the body is the whole set the operator means the role to
// have, which is what the screen has in front of them. A partial update would
// have to invent a way to say "and take this one away", and two clients each
// adding one right would silently both win.
func (a *App) handleSetRoleRights(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Rights []Right `json:"rights"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	// SuperuserFrom is nil for a static admin token, which is the case Set
	// reads as "nobody's own role", and correctly: a script has no role to lock
	// itself out of, and it is the credential an operator who *has* locked
	// themselves out would use to undo it.
	set, err := a.roles.Set(r.Context(), r.PathValue("role"), in.Rights, SuperuserFrom(r.Context()))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	a.log.Info("role rights changed", "role", set.Role,
		"rights", set.Rights, "customized", set.Customized)
	a.respondRoleRow(w, r, set.Role)
}

// respondRoleRow answers with the role as the matrix shows it: holders, name
// and all. The screen that saved a role swaps this into its list, and half a
// row there forgot who held the role and offered to delete it.
func (a *App) respondRoleRow(w http.ResponseWriter, r *http.Request, role string) {
	matrix, err := a.roles.Matrix(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	for _, set := range matrix.Roles {
		if set.Role == role {
			Respond(w, http.StatusOK, set)
			return
		}
	}
	RespondError(w, r, NotFoundf("role %q", role))
}

// handleResetRoleRights drops the store's override so the role tracks the
// engine's defaults again.
func (a *App) handleResetRoleRights(w http.ResponseWriter, r *http.Request) {
	set, err := a.roles.Reset(r.Context(), r.PathValue("role"))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	a.log.Info("role rights reset to defaults", "role", set.Role)
	a.respondRoleRow(w, r, set.Role)
}

// handleSetRoleProfile renames a role and describes it.
//
// The role's key is not in the body and cannot be: `owner`, `manager` and
// `staff` are written on every superuser row, so the string is an identifier.
// What changes is what a store calls it.
//
// Both fields are optional and a blank one means "back to the engine's own
// words", which is why there is no separate reset verb: clearing the name is
// the reset, and it reads the same way on the screen that does it.
func (a *App) handleSetRoleProfile(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	role := r.PathValue("role")
	if _, err := a.roles.SetProfile(r.Context(), role, in.Title, in.Description,
		SuperuserFrom(r.Context())); err != nil {
		RespondError(w, r, err)
		return
	}
	// The whole matrix back, not just the one profile: the screen that renamed
	// a role is showing its rights beside the name, and answering with half of
	// what it displays is how the two come to disagree.
	matrix, err := a.roles.Matrix(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	for _, set := range matrix.Roles {
		if set.Role == role {
			a.log.Info("role renamed", "role", role, "title", set.Title,
				"customized", set.TitleCustomized)
			Respond(w, http.StatusOK, set)
			return
		}
	}
	RespondError(w, r, NotFoundf("role %q", role))
}

// handleCreateRole makes a role of the store's own.
func (a *App) handleCreateRole(w http.ResponseWriter, r *http.Request) {
	var in NewRole
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	set, err := a.roles.Create(r.Context(), in, SuperuserFrom(r.Context()))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	a.log.Info("role created", "role", set.Role, "rights", set.Rights)
	Respond(w, http.StatusCreated, set)
}

// handleDeleteRole removes a role nobody holds.
func (a *App) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	role := r.PathValue("role")
	if err := a.roles.Delete(r.Context(), role); err != nil {
		RespondError(w, r, err)
		return
	}
	a.log.Info("role deleted", "role", role)
	w.WriteHeader(http.StatusNoContent)
}

// handleRoleNames lists every role with what the store calls it.
func (a *App) handleRoleNames(w http.ResponseWriter, r *http.Request) {
	names, err := a.roles.Names(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, names)
}
