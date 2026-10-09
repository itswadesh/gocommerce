package gocommerce

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// What a role may do, as this store decided it.
//
// rights.go declares the rights and the default set each role carries. This is
// the store's departure from those defaults: a table of grants, one row per
// right, and a resolution step every authenticated request goes through.
//
// The shape is deliberate in three ways.
//
// A role with no rows tracks the defaults rather than freezing a copy of them,
// so a right added to `manager` in a later release reaches every store that
// never touched the matrix. Saving a set that matches the default therefore
// stores nothing at all.
//
// Owner is not storable. It is the recovery path — a store configured into a
// corner still has somebody who can configure it back out — and the way back
// must not depend on configuration being sane.
//
// Nothing is cached. Rights are read on each authentication, which is one small
// query on a table holding at most a dozen rows, and in exchange a change takes
// effect on the affected operator's next request without anybody being signed
// out and without a second server holding a stale copy of who may spend money.
type RoleRights struct {
	app *App
}

// Roles returns the role-rights service.
func (a *App) Roles() *RoleRights { return a.roles }

// roleRightsLockKey is the first half of the two-int advisory lock key that
// serialises writes to one role ("role" in ASCII). The second half is the role
// name, so two roles are edited independently.
const roleRightsLockKey int32 = 0x726F6C65

// RoleSet is one row of the matrix: what a role may do here, what it would do
// untouched, and whether those differ.
type RoleSet struct {
	Role   string  `json:"role"`
	Rights []Right `json:"rights"`
	// Default is what the engine ships for this role. The screen that edits a
	// set needs to show what "reset" would go back to.
	Default []Right `json:"default"`
	// Customized is whether this store has departed from Default — which is not
	// the same as having a row, since a set equal to the default is stored as
	// nothing at all.
	Customized   bool `json:"customized"`
	Configurable bool `json:"configurable"`
	// Title and Description are what this store calls the role. The key in
	// Role is the identifier and never changes; these are the words a screen
	// shows, and a store may set both (role_profiles, M41).
	Title       string `json:"title"`
	Description string `json:"description"`
	// TitleCustomized is whether those words are the store's own rather than
	// the engine's, so a screen can offer to put them back.
	TitleCustomized bool `json:"title_customized"`
	// Builtin is one of the engine's four, which have defaults to reset to; a
	// store's own role has none (D80).
	Builtin bool `json:"builtin"`
	// Holders counts the operators in the role and the open invitations into
	// it, which is what decides whether it can be deleted.
	Holders int `json:"holders"`
}

// RoleMatrix is the whole model in one response: every role, the closed list of
// rights, and the floor. A client renders the grid without keeping its own copy
// of either axis — the same reason a superuser record carries its rights.
type RoleMatrix struct {
	Roles []RoleSet `json:"roles"`
	// AllRights is the catalogue, in the engine's display order.
	AllRights []Right `json:"all_rights"`
	// Required is what no role can be stripped of; the panel renders these
	// checked and locked rather than letting somebody save a set the API will
	// only refuse.
	Required []Right `json:"required"`
	// Catalogue is the module-declared half with its words. Core's labels
	// live in the panel, which can have a table for rights it was built
	// beside; it cannot have one for a module it has never heard of.
	Catalogue []RightCatalogue `json:"catalogue,omitempty"`
}

// Matrix returns the store's effective role/right matrix.
func (r *RoleRights) Matrix(ctx context.Context) (*RoleMatrix, error) {
	stored, err := r.overrides(ctx)
	if err != nil {
		return nil, err
	}
	labels, err := r.profiles(ctx)
	if err != nil {
		return nil, err
	}
	keys, err := r.Keys(ctx)
	if err != nil {
		return nil, err
	}
	holders, err := r.holders(ctx)
	if err != nil {
		return nil, err
	}
	out := &RoleMatrix{
		// This build's rights, not core's: a module brings its own, and a grid
		// drawn from core alone leaves that module's screens with no row and so
		// no way to grant or withhold them.
		AllRights: r.app.Rights(),
		Required:  append([]Right(nil), RequiredRights...),
		Catalogue: r.app.RightsCatalogue(),
	}
	for _, role := range keys {
		label := profileOf(role, labels)
		row := RoleSet{
			Role:            role,
			Configurable:    role != RoleOwner,
			Title:           label.Title,
			Description:     label.Description,
			TitleCustomized: label.Customized,
			Builtin:         ValidRole(role),
			Holders:         holders[role],
		}
		if row.Builtin {
			def := r.app.defaultRightsOf(role)
			row.Rights, row.Default = def, def
			if set, ok := stored[role]; ok && row.Configurable {
				row.Rights = set
				row.Customized = !sameRights(set, def)
			}
		} else {
			// A store's own role has no default to track or depart from; its
			// grants are the whole of it.
			row.Rights = withFloor(stored[role])
			row.Default = []Right{}
		}
		out.Roles = append(out.Roles, row)
	}
	return out, nil
}

// Of resolves what a role may actually do in this store.
//
// Every path that hands an operator their rights goes through here rather than
// through DefaultRightsOf, which is what makes a change to the matrix take
// effect on the next request.
func (r *RoleRights) Of(ctx context.Context, role string) ([]Right, error) {
	// The one lookup that needs no table at all. Owner is not storable, and
	// resolving it without reading anything means a store whose role_rights is
	// unreadable can still be signed into by the person who can fix it.
	if role == RoleOwner {
		return r.app.defaultRightsOf(role), nil
	}
	all, err := r.All(ctx)
	if err != nil {
		return nil, err
	}
	return all[role], nil
}

// All resolves every role in one query, for the paths that scan more than one
// operator and would otherwise ask the same question per row.
func (r *RoleRights) All(ctx context.Context) (map[string][]Right, error) {
	stored, err := r.overrides(ctx)
	if err != nil {
		return nil, err
	}
	keys, err := r.Keys(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]Right, len(keys))
	for _, role := range keys {
		if !ValidRole(role) {
			out[role] = withFloor(stored[role])
			continue
		}
		if set, ok := stored[role]; ok && RoleConfigurable(role) {
			out[role] = set
			continue
		}
		// The build's defaults, not core's: a module brings rights of its own,
		// and resolving a role without them refuses that module's screens to
		// everybody the module meant to give them to.
		out[role] = r.app.defaultRightsOf(role)
	}
	return out, nil
}

// Set replaces what a role may do.
//
// The guards, in order: owner is fixed; only rights this engine has are
// accepted; the floor is kept; and nobody removes roles.write from their own
// role. The last one is the same rule the panel draws locked — an operator who
// saves their way out of the roles screen has no way back in short of a
// database client, and the mistake is one keystroke away from being made by
// somebody who was only tidying up.
//
// It is *not* an escalation guard: a role holding roles.write can widen itself,
// and one holding team.write can hand out the owner role outright. Those two
// rights are what granting access *is*, and a check here that reads like a
// boundary without being one would be worse than none. Handing them out is the
// decision; this is not the place to second-guess it.
//
// by is the operator making the change, or nil for a static admin token, which
// has no role to lock itself out of.
func (r *RoleRights) Set(ctx context.Context, role string, rights []Right, by *Superuser) (*RoleSet, error) {
	if err := r.requireRole(ctx, role); err != nil {
		return nil, err
	}
	if role == RoleOwner {
		return nil, Forbiddenf("the owner role always carries every right and cannot be changed")
	}

	clean := make([]Right, 0, len(rights))
	for _, right := range rights {
		if !r.app.hasRight(right) {
			return nil, Validationf("%q is not a right this build has", right)
		}
		if !slices.Contains(clean, right) {
			clean = append(clean, right)
		}
	}
	for _, required := range RequiredRights {
		if !slices.Contains(clean, required) {
			return nil, Validationf(
				"every role keeps %s: a role without it can sign in and see nothing, "+
					"which is a removed operator rather than a narrower one", required)
		}
	}
	if by != nil && by.Role == role && !slices.Contains(clean, RightRolesWrite) {
		return nil, Forbiddenf(
			"this is your own role, and removing %s from it would lock you out of this screen",
			RightRolesWrite)
	}
	sort.Slice(clean, func(i, j int) bool { return clean[i] < clean[j] })

	// Stored as the store's departure from the defaults, so a set that matches
	// them is stored as no rows and the role goes back to tracking. A store's
	// own role has no defaults, so its set is always written down.
	builtin := ValidRole(role)
	def := r.app.defaultRightsOf(role)
	if !builtin {
		def = []Right{}
	}
	var grantedBy *int64
	if by != nil {
		grantedBy = &by.ID
	}
	err := InTx(ctx, r.app.db, func(tx *sql.Tx) error {
		// Delete-then-insert is not serialisable on its own: two owners saving
		// the same role at once each delete the rows they can see and then
		// insert, and the store ends up with the union of two sets that nobody
		// chose. There is no row to lock when a role has no override yet, so
		// the lock is on the role's name. It is released at commit.
		if _, err := tx.ExecContext(ctx,
			`SELECT pg_advisory_xact_lock($1, hashtext($2))`,
			roleRightsLockKey, role); err != nil {
			return Internalf(err, "lock the role")
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM role_rights WHERE role = $1`, role); err != nil {
			return Internalf(err, "clear role rights")
		}
		if builtin && sameRights(clean, def) {
			return nil
		}
		return grant(ctx, tx, role, clean, grantedBy)
	})
	if err != nil {
		return nil, err
	}
	return &RoleSet{
		Role: role, Rights: clean, Default: def,
		Customized: builtin && !sameRights(clean, def), Configurable: true, Builtin: builtin,
	}, nil
}

// Reset returns a role to the rights the engine ships for it, by removing the
// store's override rather than by writing the defaults down — so the role
// tracks them again from here on.
func (r *RoleRights) Reset(ctx context.Context, role string) (*RoleSet, error) {
	if err := r.requireRole(ctx, role); err != nil {
		return nil, err
	}
	if !ValidRole(role) {
		return nil, Validationf("%q is this store's own role and has no defaults to go back to", role)
	}
	if !RoleConfigurable(role) {
		return nil, Forbiddenf("the owner role always carries every right and cannot be changed")
	}
	err := InTx(ctx, r.app.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`SELECT pg_advisory_xact_lock($1, hashtext($2))`,
			roleRightsLockKey, role); err != nil {
			return Internalf(err, "lock the role")
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM role_rights WHERE role = $1`, role); err != nil {
			return Internalf(err, "reset role rights")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	def := r.app.defaultRightsOf(role)
	return &RoleSet{Role: role, Rights: def, Default: def, Configurable: true, Builtin: true}, nil
}

// overrides loads every stored set, keyed by role and sorted.
//
// Rights the engine no longer has are dropped on the way out. The table has no
// foreign key to hold them to the list — the rights live in Go — so a right
// removed in a later release leaves rows behind, and the intersection is what
// keeps them from resolving into something nobody can name.
func (r *RoleRights) overrides(ctx context.Context) (map[string][]Right, error) {
	rows, err := r.app.db.QueryContext(ctx,
		`SELECT role, right_name FROM role_rights ORDER BY role, right_name`)
	if err != nil {
		return nil, Internalf(err, "read role rights")
	}
	defer rows.Close()

	out := map[string][]Right{}
	for rows.Next() {
		var role, name string
		if err := rows.Scan(&role, &name); err != nil {
			return nil, Internalf(err, "scan role right")
		}
		right := Right(name)
		// Against this BUILD, not against core: a stored grant of a module's
		// right was being dropped on the way out, so the store saved the
		// permission, reported it saved, and refused the screen anyway.
		if !r.app.hasRight(right) {
			continue
		}
		out[role] = append(out[role], right)
	}
	if err := rows.Err(); err != nil {
		return nil, Internalf(err, "read role rights")
	}
	return out, nil
}

// sameRights compares two sets. Both sides are sorted by the time they get
// here, so this is an equality test and not a set comparison.
func sameRights(a, b []Right) bool {
	return slices.Equal(a, b)
}

// grant writes a role's set inside the caller's transaction. The caller has
// cleared the old rows and holds the role's lock.
func grant(ctx context.Context, tx *sql.Tx, role string, rights []Right, grantedBy *int64) error {
	for _, right := range rights {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO role_rights (role, right_name, granted_by)
			VALUES ($1, $2, $3)`, role, string(right), grantedBy); err != nil {
			return Internalf(err, "grant %s to %s", right, role)
		}
	}
	return nil
}

// withFloor is a store's own role as it resolves: its grants with the floor
// added. Added rather than demanded, so a role made from a blank form signs in
// to something, and a role whose every grant named a module since removed
// still does.
func withFloor(rights []Right) []Right {
	out := append([]Right(nil), rights...)
	for _, required := range RequiredRights {
		if !slices.Contains(out, required) {
			out = append(out, required)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ------------------------------------------------------------ custom roles

// roleKeyRE is the shape of a role's key: the identifier written on every
// superuser row, so it is lower-case, unspaced and permanent. What a person
// reads is the title. The same pattern is the table's CHECK.
var roleKeyRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// Exists reports whether this store has a role, built in or its own.
func (r *RoleRights) Exists(ctx context.Context, role string) (bool, error) {
	var ok bool
	err := r.app.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM roles WHERE key = $1)`, role).Scan(&ok)
	if err != nil {
		return false, Internalf(err, "look up role")
	}
	return ok, nil
}

// requireRole is the check every write that names a role makes first. It used
// to be a lookup in a list compiled into the engine; the store's roles are its
// own now (D80), so the answer is the store's.
func (r *RoleRights) requireRole(ctx context.Context, role string) error {
	ok, err := r.Exists(ctx, role)
	if err != nil {
		return err
	}
	if !ok {
		return Validationf("%q is not a role in this store", role)
	}
	return nil
}

// Keys lists every role: the engine's four in their fixed order, then the
// store's own in the order they were made, which is the order a screen shows.
func (r *RoleRights) Keys(ctx context.Context) ([]string, error) {
	rows, err := r.app.db.QueryContext(ctx,
		`SELECT key, builtin FROM roles ORDER BY created_at, key`)
	if err != nil {
		return nil, Internalf(err, "list roles")
	}
	defer rows.Close()
	var builtin, own []string
	for rows.Next() {
		var k string
		var b bool
		if err := rows.Scan(&k, &b); err != nil {
			return nil, Internalf(err, "scan role")
		}
		if b {
			builtin = append(builtin, k)
		} else {
			own = append(own, k)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, Internalf(err, "list roles")
	}
	// The engine's own in Roles order, and only those this store still has:
	// a starting role it deleted stays deleted.
	keys := make([]string, 0, len(builtin)+len(own))
	for _, role := range Roles {
		if slices.Contains(builtin, role) {
			keys = append(keys, role)
		}
	}
	return append(keys, own...), nil
}

// holders counts, per role, the operators in it, the open invitations into it
// and the API keys acting in it: everything that stops it being deleted.
func (r *RoleRights) holders(ctx context.Context) (map[string]int, error) {
	rows, err := r.app.db.QueryContext(ctx, `
		SELECT role, count(*) FROM (
			SELECT role FROM superusers
			UNION ALL
			SELECT role FROM superuser_invitations
			 WHERE accepted_at IS NULL AND expires_at > now()
			UNION ALL
			SELECT role FROM api_keys WHERE revoked_at IS NULL
		) h GROUP BY role`)
	if err != nil {
		return nil, Internalf(err, "count role holders")
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var role string
		var n int
		if err := rows.Scan(&role, &n); err != nil {
			return nil, Internalf(err, "scan role holders")
		}
		out[role] = n
	}
	return out, rows.Err()
}

// NewRole is what a store says when it makes a role.
type NewRole struct {
	Key         string  `json:"key"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Rights      []Right `json:"rights"`
}

// Create makes a role of the store's own (D80). The key is refused rather than
// tidied when it is not already in shape, so the screen that shows the key
// before saving shows exactly the identifier that will be stored.
func (r *RoleRights) Create(ctx context.Context, in NewRole, by *Superuser) (*RoleSet, error) {
	if !roleKeyRE.MatchString(in.Key) {
		return nil, Validationf("a role's key is lower-case letters, digits and underscores, starting with a letter, at most 40 characters; got %q", in.Key)
	}
	title := strings.TrimSpace(in.Title)
	description := strings.TrimSpace(in.Description)
	if title == "" {
		return nil, Validationf("a role needs a name")
	}
	if len([]rune(title)) > MaxRoleTitle {
		return nil, Validationf("a role's name is at most %d characters", MaxRoleTitle)
	}
	if len([]rune(description)) > MaxRoleDescription {
		return nil, Validationf("a role's description is at most %d characters", MaxRoleDescription)
	}
	clean := []Right{}
	for _, right := range in.Rights {
		if !r.app.hasRight(right) {
			return nil, Validationf("%q is not a right this build has", right)
		}
		if !slices.Contains(clean, right) {
			clean = append(clean, right)
		}
	}
	clean = withFloor(clean)

	var byID *int64
	if by != nil {
		byID = &by.ID
	}
	err := InTx(ctx, r.app.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO roles (key, created_by) VALUES ($1, $2)`, in.Key, byID); err != nil {
			if isUniqueViolation(err) {
				return Conflictf("there is already a role called %q", in.Key)
			}
			return Internalf(err, "create role")
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO role_profiles (role, title, description, updated_by)
			VALUES ($1, $2, $3, $4)`, in.Key, title, description, byID); err != nil {
			return Internalf(err, "name the role")
		}
		return grant(ctx, tx, in.Key, clean, byID)
	})
	if err != nil {
		return nil, err
	}
	return &RoleSet{
		Role: in.Key, Rights: clean, Default: []Right{}, Configurable: true,
		Title: title, Description: description, TitleCustomized: true,
	}, nil
}

// Delete removes a role nobody holds. Owner cannot go: it is the way back in.
// The starting roles can, once empty — a store that does not want "Staff"
// should not have to keep it.
//
// The holders are counted under the role's lock and with the operators' rows
// locked, so an assignment racing the delete either lands first and is counted
// or arrives after and meets the foreign key.
func (r *RoleRights) Delete(ctx context.Context, role string) error {
	if role == RoleOwner {
		return Forbiddenf("the owner role cannot be deleted")
	}
	// Vendor logins are filed under this key (M48's CHECK ties row-scoping
	// to it), so a store without one would have no way to make them.
	if role == RoleVendor {
		return Forbiddenf("the vendor role belongs to vendor accounts and cannot be deleted")
	}
	if err := r.requireRole(ctx, role); err != nil {
		return err
	}
	return InTx(ctx, r.app.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`SELECT pg_advisory_xact_lock($1, hashtext($2))`,
			roleRightsLockKey, role); err != nil {
			return Internalf(err, "lock the role")
		}
		var operators, invitations, keys int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM (SELECT id FROM superusers WHERE role = $1 FOR UPDATE) s`,
			role).Scan(&operators); err != nil {
			return Internalf(err, "count the role's operators")
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM superuser_invitations
			 WHERE role = $1 AND accepted_at IS NULL AND expires_at > now()`,
			role).Scan(&invitations); err != nil {
			return Internalf(err, "count the role's invitations")
		}
		// An API key acts in a role as well. Deleting the role under it would
		// leave an integration refused everywhere, and a later role of the
		// same key would quietly hand it new rights.
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM api_keys WHERE role = $1 AND revoked_at IS NULL`,
			role).Scan(&keys); err != nil {
			return Internalf(err, "count the role's API keys")
		}
		if operators+invitations+keys > 0 {
			return roleInUse(role, operators, invitations, keys)
		}
		// Expired and accepted invitations still name the role. They are
		// history, and the foreign key would otherwise keep the role alive
		// for a link nobody can use.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM superuser_invitations WHERE role = $1`, role); err != nil {
			return Internalf(err, "clear the role's old invitations")
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM role_profiles WHERE role = $1`, role); err != nil {
			return Internalf(err, "clear the role's name")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM roles WHERE key = $1`, role); err != nil {
			if isForeignKeyViolation(err) {
				return roleInUse(role, 1, 0, 0)
			}
			return Internalf(err, "delete role")
		}
		return nil
	})
}

// roleGone turns the foreign key a writer meets, when a role is deleted
// between the writer checking it and committing, into the answer requireRole
// would have given a moment earlier. Only the role keys: any other foreign key
// is left to the caller's own handling.
func roleGone(err error, role string) error {
	if err != nil && isForeignKeyViolation(err) && strings.Contains(err.Error(), "role_fkey") {
		return Validationf("%q is not a role in this store", role)
	}
	return err
}

func roleInUse(role string, operators, invitations, keys int) *APIError {
	return &APIError{
		Status: http.StatusConflict,
		Code:   "role_in_use",
		Message: fmt.Sprintf("%d operator(s), %d open invitation(s) and %d API key(s) hold %q; move or revoke them first",
			operators, invitations, keys, role),
	}
}

// RoleName is a role as the screens that hand one out need it.
type RoleName struct {
	Role        string `json:"role"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Builtin     bool   `json:"builtin"`
}

// Names lists every role with what the store calls it.
func (r *RoleRights) Names(ctx context.Context) ([]RoleName, error) {
	keys, err := r.Keys(ctx)
	if err != nil {
		return nil, err
	}
	labels, err := r.profiles(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RoleName, 0, len(keys))
	for _, role := range keys {
		p := profileOf(role, labels)
		out = append(out, RoleName{Role: role, Title: p.Title, Description: p.Description, Builtin: ValidRole(role)})
	}
	return out, nil
}
