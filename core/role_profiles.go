package gocommerce

import (
	"context"
	"database/sql"
	"strings"
)

// What a store calls its roles.
//
// The role *key* is fixed and stays fixed: `owner`, `manager` and `staff` are
// written on every superuser row and in role_rights, so the string is an
// identifier rather than a label. Renaming one would orphan accounts.
//
// What a store may change is the display title and the description — "Fulfilment"
// reads better than "Staff" in a warehouse, and the description is where a store
// writes down the rule it actually operates: who is allowed to press refund, who
// covers the phone at the weekend. The engine ships a sentence for each
// (rights.go) and a row exists only where a store has said otherwise, which is
// the shape role_rights uses and for the same reason: improving a default then
// reaches every store that never edited it (M41).
//
// Unlike rights, all three roles are editable here, owner included. That is not
// an inconsistency: owner's *rights* are unstorable because a store that could
// narrow them could lock everybody out, and a title cannot lock anybody out of
// anything.

// RoleProfile is what a role is called and what it is for.
type RoleProfile struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	// Customized is whether this store has said anything, as opposed to
	// tracking the engine's own words.
	Customized bool `json:"customized"`
}

// MaxRoleTitle and MaxRoleDescription bound what a store may store. Generous
// rather than tight: the point is to stop a paste of a whole document, not to
// have an opinion about how a shop describes its own staff.
const (
	MaxRoleTitle       = 60
	MaxRoleDescription = 600
)

// profiles reads every stored label, keyed by role.
func (r *RoleRights) profiles(ctx context.Context) (map[string]RoleProfile, error) {
	rows, err := r.app.db.QueryContext(ctx,
		`SELECT role, title, description FROM role_profiles`)
	if err != nil {
		return nil, Internalf(err, "read role profiles")
	}
	defer rows.Close()

	out := map[string]RoleProfile{}
	for rows.Next() {
		var role string
		var p RoleProfile
		if err := rows.Scan(&role, &p.Title, &p.Description); err != nil {
			return nil, Internalf(err, "scan role profiles")
		}
		p.Customized = true
		out[role] = p
	}
	if err := rows.Err(); err != nil {
		return nil, Internalf(err, "read role profiles")
	}
	return out, nil
}

// profileOf fills a role's label from what the store stored, falling back to
// the engine's own words field by field: a store that renamed a role without
// describing it keeps the engine's description rather than losing it.
func profileOf(role string, stored map[string]RoleProfile) RoleProfile {
	p := RoleProfile{
		Title:       DefaultTitleOf(role),
		Description: DefaultDescriptionOf(role),
	}
	saved, ok := stored[role]
	if !ok {
		return p
	}
	if saved.Title != "" {
		p.Title = saved.Title
		p.Customized = true
	}
	if saved.Description != "" {
		p.Description = saved.Description
		p.Customized = true
	}
	return p
}

// SetProfile renames a role and describes it.
//
// Blank means "back to the engine's words" rather than "store an empty label",
// because a role with no name at all is not something a screen can render and
// not something an operator meant to ask for. Clearing both drops the row, so
// the role goes back to tracking the defaults the way a reset set does.
func (r *RoleRights) SetProfile(ctx context.Context, role, title, description string, by *Superuser) (*RoleProfile, error) {
	if !ValidRole(role) {
		return nil, Validationf("%q is not a role; the roles are %s", role, strings.Join(Roles, ", "))
	}
	title = strings.TrimSpace(title)
	description = strings.TrimSpace(description)
	if len([]rune(title)) > MaxRoleTitle {
		return nil, Validationf("a role's name is at most %d characters", MaxRoleTitle)
	}
	if len([]rune(description)) > MaxRoleDescription {
		return nil, Validationf("a role's description is at most %d characters", MaxRoleDescription)
	}

	var updatedBy *int64
	if by != nil {
		updatedBy = &by.ID
	}
	err := InTx(ctx, r.app.db, func(tx *sql.Tx) error {
		if title == "" && description == "" {
			_, err := tx.ExecContext(ctx, `DELETE FROM role_profiles WHERE role = $1`, role)
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO role_profiles (role, title, description, updated_by)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (role) DO UPDATE
			SET title = EXCLUDED.title,
			    description = EXCLUDED.description,
			    updated_at = now(),
			    updated_by = EXCLUDED.updated_by`,
			role, title, description, updatedBy)
		return err
	})
	if err != nil {
		return nil, Internalf(err, "save the role profile")
	}

	stored, err := r.profiles(ctx)
	if err != nil {
		return nil, err
	}
	p := profileOf(role, stored)
	return &p, nil
}
