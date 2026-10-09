# Custom Roles and the Team Flow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A store creates, renames and deletes its own roles, each a set of the rights the engine already has, and adds team members the KitCommerce admin's way — email and role, then "User Account Not Found" → Create Account or Send Invite.

**Architecture:** A per-store `roles` table becomes the list of roles that exist; `owner`, `manager`, `staff` and `vendor` are seeded rows. Every place that asked the static `ValidRole` whether a role exists asks the store instead (`app.Roles().Exists`). A custom role has no engine default, so its rights are exactly its `role_rights` rows. The panel's Roles screens gain Add and Delete; the Team screen reads the store's roles and adopts the add-by-email flow.

**Tech Stack:** Go 1.27 + PostgreSQL (tests against the real database), SvelteKit 2 / Svelte 5 panel, `node --test` source-reading tests, Playwright for the browser walk.

**Spec:** `docs/superpowers/specs/2026-10-09-store-switcher-and-custom-roles-design.md`, section **Part 2 — custom roles, and the team flow**.

## Global Constraints

- `owner` is fixed: holds every right, cannot be edited, renamed-only (as today, D64), never deleted, never stored in `role_rights`.
- `manager`, `staff`, `vendor` become starting roles: editable and deletable; "reset to defaults" exists only for them.
- A role key matches `^[a-z][a-z0-9_]{0,39}$` and never changes; what a person reads is its title (`role_profiles`).
- Deleting a role is refused while any operator or open invitation holds it: 409 `role_in_use`, naming the counts.
- A new role with no rights gets `RequiredRights` (`catalog.read`).
- Every admin route keeps its server-side right check. Every new route names a right and appears in `core/openapi.json`.
- Migrations are append-only: the new one is `0052_custom_roles`. Never edit a shipped migration.
- Panel: do not hand-edit `admin/src/lib/styles/*.css`; rebuild with `.\scripts\build.ps1` and commit `admin/build`. Dialogs and drawers open/close in 150–300 ms on `transform`/`opacity` with a `prefers-reduced-motion` path (user's CLAUDE.md); reuse the panel's existing Drawer/Confirm components, which already do.
- Environment: `$env:Path += ';C:\tools\go\bin'`; `GOCOMMERCE_TEST_DB=postgres://gocommerce@127.0.0.1:5460/gocommerce_test?sslmode=disable`; dev store `.\scripts\dev.ps1 -Port 8090 -PgPort 5460 -Database gocommerce_roles -Seed` (start it in the background; the `-Seed` path is the one that survives PowerShell redirection).
- Work in a worktree on a branch; stage only files this plan touches. Intermediate commits carry `Update: skip`; the last one is the public subject.

**Two narrowings of the spec, decided here:**
- **No custom vendor-scoped roles.** M48's `superusers_vendor_account` CHECK ties row-scoping to the role named `vendor`, and loosening it means moving a database guarantee into service code. `vendor` stays the one vendor role (editable, deletable when unheld). Cost: a store cannot make "Vendor (read-only)" beside "Vendor". Revisit if asked.
- **API keys keep the four built-in roles.** `api_keys.role` has its own validation (`isRole`) and kitcommerce-admin's keys use a fixed list too. A key in a custom role is a later change.

## Review Focus

1. A role deleted between the Team screen loading and the operator saving a member with it — the save must fail with a clear 4xx, not a 500 from the foreign key (Task 2 test).
2. Two owners deleting the same role, or one deleting while another assigns it — no orphaned superuser, no 500 (Task 2: FK `ON DELETE RESTRICT` + `role_in_use` check in one transaction; test assigns-then-deletes).
3. A role key that collides with a seeded key or differs only by case/space (`"Manager "`) — refused or normalised, never a second `manager` (Task 2 test).
4. A custom role whose every right was removed by a later release (module uninstalled) — still resolves to at least `catalog.read`, never to nothing that signs in and sees an error loop (Task 2 test: `Of` on a role whose only rows are unknown rights returns the floor).
5. The Team screen for an operator with `team.write` but not `roles.write` — the role picker must still list roles (Task 3 adds `GET /api/admin/roles/names` behind `team.read`; Task 5 test).

---

### Task 1: The `roles` table

**Files:**
- Modify: `core/schema.go` (append `migration0052CustomRoles` and its `{ID: "0052_custom_roles", ...}` entry after `0051_group_shipping_and_tax`)
- Test: `core/custom_roles_test.go` (new)

**Interfaces:**
- Produces: table `roles (key text PRIMARY KEY CHECK (key ~ '^[a-z][a-z0-9_]{0,39}$'), builtin boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now(), created_by bigint REFERENCES superusers(id) ON DELETE SET NULL)`, seeded with `owner, manager, staff, vendor` (`builtin = true`). Foreign keys: `superusers.role`, `superuser_invitations.role` → `roles(key) ON DELETE RESTRICT`; `role_rights.role` → `roles(key) ON DELETE CASCADE`. `role_rights` keeps `role <> 'owner'`.

- [ ] **Step 1: Write the failing test**

```go
// core/custom_roles_test.go
package gocommerce

import (
	"context"
	"testing"
)

// The roles a store has are rows now, so the database — not a Go list — is
// what refuses an operator in a role that does not exist.
func TestTheSeededRolesExistAndNoOthers(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	var keys []string
	rows, err := app.db.QueryContext(ctx, `SELECT key FROM roles WHERE builtin ORDER BY key`)
	if err != nil {
		t.Fatalf("query roles: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		keys = append(keys, k)
	}
	want := []string{"manager", "owner", "staff", "vendor"}
	if len(keys) != len(want) {
		t.Fatalf("builtin roles = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("builtin roles = %v, want %v", keys, want)
		}
	}
	if _, err := app.db.ExecContext(ctx,
		`INSERT INTO superusers (email, password_hash, role) VALUES ('x@example.com', 'h', 'nobody')`); err == nil {
		t.Fatal("a superuser in a role that does not exist was stored")
	}
	if _, err := app.db.ExecContext(ctx,
		`INSERT INTO role_rights (role, right_name) VALUES ('owner', 'catalog.read')`); err == nil {
		t.Fatal("a right was stored against owner, which always holds every right")
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./core -run TestTheSeededRolesExistAndNoOthers -count=1`
Expected: FAIL — `relation "roles" does not exist`.

- [ ] **Step 3: Write the migration**

```go
// migration0052CustomRoles makes the roles a store has a table rather than a
// list compiled into the engine (D81).
//
// The four the engine ships are seeded as rows so every existing superuser,
// invitation and grant has a role to point at the moment the foreign keys
// arrive. The CHECK lists M13, M19 and M48 wrote are dropped: the keys are
// the store's now, and the foreign key is the stronger form of the same
// guarantee. Owner stays out of role_rights, for the reason M19 gave.
//
// RESTRICT from superusers and invitations, so a role somebody holds cannot
// vanish under them; the service refuses first with a message that counts
// them, and this is the last line. CASCADE from role_rights, because a
// role's grants mean nothing once the role is gone.
const migration0052CustomRoles = `
CREATE TABLE roles (
    key        text        PRIMARY KEY CHECK (key ~ '^[a-z][a-z0-9_]{0,39}$'),
    builtin    boolean     NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by bigint      REFERENCES superusers (id) ON DELETE SET NULL
);
INSERT INTO roles (key, builtin) VALUES
    ('owner', true), ('manager', true), ('staff', true), ('vendor', true);

ALTER TABLE superusers DROP CONSTRAINT superusers_role_check;
ALTER TABLE superusers
    ADD CONSTRAINT superusers_role_fkey
        FOREIGN KEY (role) REFERENCES roles (key) ON DELETE RESTRICT;

ALTER TABLE superuser_invitations
    ADD CONSTRAINT superuser_invitations_role_fkey
        FOREIGN KEY (role) REFERENCES roles (key) ON DELETE RESTRICT;

ALTER TABLE role_rights DROP CONSTRAINT role_rights_role_check;
ALTER TABLE role_rights
    ADD CONSTRAINT role_rights_role_check CHECK (role <> 'owner');
ALTER TABLE role_rights
    ADD CONSTRAINT role_rights_role_fkey
        FOREIGN KEY (role) REFERENCES roles (key) ON DELETE CASCADE;
`
```

Before writing it, confirm the constraint names with
`Select-String -Path core\schema.go -Pattern 'superusers_role_check|role_rights_role_check|superuser_invitations'`
and check whether `superuser_invitations` has any open row with a role that is not one of the four (it cannot today — `Invite` validates — but the migration would fail on one; if the check finds a path that could, add `DELETE FROM superuser_invitations WHERE role NOT IN (...) AND accepted_at IS NULL` before the constraint and say why).

- [ ] **Step 4: Run it to make sure it passes, and that nothing else broke**

Run: `go test ./core -run 'TestTheSeededRolesExistAndNoOthers|Role|Invit|Superuser|Vendor' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add core/schema.go core/custom_roles_test.go
git commit -m "Keep a store's roles in a table" -m "Update: skip"
```

---

### Task 2: The role service — exists, create, delete, resolve

**Files:**
- Modify: `core/roles.go` (Matrix, Of, All, Set, Reset; new Exists, Create, Delete, Names), `core/role_profiles.go:100` (`SetProfile`), `core/superusers.go:272` (`Create`) and `:1156` (`SetRole`), `core/invitations.go:122` (`Invite`)
- Test: `core/custom_roles_test.go`

**Interfaces:**
- Consumes: the `roles` table from Task 1.
- Produces:
  - `func (r *RoleRights) Exists(ctx context.Context, role string) (bool, error)`
  - `func (r *RoleRights) Keys(ctx context.Context) ([]string, error)` — every role key, built-ins first in `Roles` order, then the store's by `created_at`.
  - `type NewRole struct { Key, Title, Description string; Rights []Right }`
  - `func (r *RoleRights) Create(ctx context.Context, in NewRole, by *Superuser) (*RoleSet, error)`
  - `func (r *RoleRights) Delete(ctx context.Context, role string) error`
  - `RoleSet` gains `Builtin bool \`json:"builtin"\`` and `Holders int \`json:"holders"\`` (operators plus open invitations).
  - `type RoleName struct { Role, Title, Description string \`json:...\`; Builtin bool }` and `func (r *RoleRights) Names(ctx context.Context) ([]RoleName, error)`.
  - Error for an unknown role, used everywhere `ValidRole` was: `Validationf("%q is not a role in this store", role)`.

- [ ] **Step 1: Write the failing tests** (append to `core/custom_roles_test.go`; add `"errors"` and `"slices"` to its imports)

```go
func createRole(t *testing.T, app *App, key, title string, rights ...Right) *RoleSet {
	t.Helper()
	set, err := app.Roles().Create(context.Background(), NewRole{Key: key, Title: title, Rights: rights}, nil)
	if err != nil {
		t.Fatalf("create role %s: %v", key, err)
	}
	return set
}

func TestAStoreMakesARoleAndItResolves(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	set := createRole(t, app, "packer", "Packer", RightOrdersRead, RightOrdersFulfill)
	if set.Builtin || !set.Configurable {
		t.Fatalf("new role = %+v, want configurable and not builtin", set)
	}
	// The floor is added, not demanded: a role made from a blank form still
	// signs in to something.
	got, err := app.Roles().Of(ctx, "packer")
	if err != nil {
		t.Fatalf("of: %v", err)
	}
	for _, want := range []Right{RightCatalogRead, RightOrdersRead, RightOrdersFulfill} {
		if !slices.Contains(got, want) {
			t.Errorf("packer rights = %v, missing %s", got, want)
		}
	}
	matrix, err := app.Roles().Matrix(ctx)
	if err != nil {
		t.Fatalf("matrix: %v", err)
	}
	if last := matrix.Roles[len(matrix.Roles)-1]; last.Role != "packer" || last.Title != "Packer" {
		t.Errorf("matrix ends with %+v, want the new role last with its title", last)
	}
	// An operator can be put in it, and invited into it.
	su, err := app.Superusers().Create(ctx, "p@example.com", "a-long-password", "packer")
	if err != nil || su.Role != "packer" {
		t.Fatalf("create in custom role: %v %+v", err, su)
	}
	if _, err := app.Team().Invite(ctx, "q@example.com", "packer", nil); err != nil {
		t.Fatalf("invite into custom role: %v", err)
	}
}

func TestARoleKeyIsTheStoresOnlyOnce(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	for _, key := range []string{"manager", "Manager ", "OWNER", "9lives", "has space", ""} {
		if _, err := app.Roles().Create(ctx, NewRole{Key: key, Title: "X"}, nil); err == nil {
			t.Errorf("created role %q; want it refused", key)
		}
	}
	createRole(t, app, "packer", "Packer")
	if _, err := app.Roles().Create(ctx, NewRole{Key: "packer", Title: "Again"}, nil); err == nil {
		t.Error("created packer twice")
	}
}

func TestARoleSomebodyHoldsCannotBeDeleted(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	createRole(t, app, "packer", "Packer")
	if _, err := app.Superusers().Create(ctx, "p@example.com", "a-long-password", "packer"); err != nil {
		t.Fatal(err)
	}
	err := app.Roles().Delete(ctx, "packer")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "role_in_use" {
		t.Fatalf("delete held role: %v, want role_in_use", err)
	}
	if err := app.Roles().Delete(ctx, RoleOwner); err == nil {
		t.Error("deleted owner")
	}
}

func TestDeletingAnUnheldRoleTakesItsGrantsWithIt(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	createRole(t, app, "packer", "Packer", RightOrdersRead)
	if err := app.Roles().Delete(ctx, "packer"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if ok, _ := app.Roles().Exists(ctx, "packer"); ok {
		t.Fatal("packer still exists")
	}
	var n int
	_ = app.db.QueryRowContext(ctx, `SELECT count(*) FROM role_rights WHERE role = 'packer'`).Scan(&n)
	if n != 0 {
		t.Errorf("%d grants outlived their role", n)
	}
	// A starting role can go too, once nobody holds it.
	if err := app.Roles().Delete(ctx, RoleStaff); err != nil {
		t.Errorf("delete unheld staff: %v", err)
	}
	// And assigning a role that is gone is a validation error, not a 500.
	_, err := app.Superusers().Create(ctx, "s@example.com", "a-long-password", RoleStaff)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != 422 {
		t.Errorf("create in deleted role: %v, want a validation error", err)
	}
}

func TestAStoresOwnRoleHasNoDefaultsToResetTo(t *testing.T) {
	app := newTestApp(t)
	createRole(t, app, "packer", "Packer")
	if _, err := app.Roles().Reset(context.Background(), "packer"); err == nil {
		t.Error("reset a role that has no defaults")
	}
}

// A module uninstalled later leaves grants nobody can name; the role still
// keeps the floor rather than resolving to nothing at all.
func TestACustomRoleNeverResolvesBelowTheFloor(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	createRole(t, app, "packer", "Packer")
	if _, err := app.db.ExecContext(ctx, `DELETE FROM role_rights WHERE role = 'packer'`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.ExecContext(ctx,
		`INSERT INTO role_rights (role, right_name) VALUES ('packer', 'gone.read')`); err != nil {
		t.Fatal(err)
	}
	got, err := app.Roles().Of(ctx, "packer")
	if err != nil || !slices.Contains(got, RightCatalogRead) {
		t.Errorf("packer = %v %v, want at least %s", got, err, RightCatalogRead)
	}
}
```

Check the error type's name and fields before running (`Select-String -Path core\errors.go -Pattern 'type .*Error struct' -Context 0,8`); use the real names (`Code`, `Status`) the package defines, and add a `Conflict`-style constructor that sets `Code: "role_in_use"` if none takes a code — following the pattern of an existing coded error in `core/errors.go`.

- [ ] **Step 2: Run them to make sure they fail**

Run: `go test ./core -run 'TestAStoreMakesARole|TestARoleKey|TestARoleSomebody|TestDeletingAnUnheld|TestAStoresOwnRole|TestACustomRoleNever' -count=1`
Expected: FAIL to compile — `NewRole`, `Create`, `Delete`, `Exists` undefined.

- [ ] **Step 3: Implement**

In `core/roles.go`:

```go
// roleKeyRE is the shape of a role's key: the identifier written on every
// superuser row, so it is lower-case, unspaced and permanent. What a person
// reads is the title.
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

// requireRole is the check every write that names a role makes first.
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
		`SELECT key FROM roles WHERE NOT builtin ORDER BY created_at, key`)
	if err != nil {
		return nil, Internalf(err, "list roles")
	}
	defer rows.Close()
	keys := append([]string(nil), Roles...)
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, Internalf(err, "scan role")
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}
```

Then:
- `Matrix`: iterate `Keys(ctx)` instead of `Roles`; set `Builtin: ValidRole(role)`; for a non-built-in role `def` is `nil` and `row.Rights` is its stored set with the floor added (`withFloor`); `Customized` is `false` for non-built-ins (there is no default to depart from). Fill `Holders` from one query: `SELECT role, count(*) FROM (SELECT role FROM superusers UNION ALL SELECT role FROM superuser_invitations WHERE accepted_at IS NULL AND expires_at > now()) h GROUP BY role`.
- `Of` / `All`: iterate `Keys`; a non-built-in role resolves to `withFloor(stored[role])`. Owner unchanged.
- `Set`: replace the `ValidRole` check with `requireRole`; `RoleConfigurable(role)` becomes `role != RoleOwner`; for a non-built-in role always store the rows (never "same as default").
- `Reset`: `requireRole`; refuse a non-built-in role with `Validationf("%q is this store's own role and has no defaults to go back to", role)`.
- `withFloor(rights []Right) []Right` adds each of `RequiredRights` that is missing and sorts.
- `Create(ctx, in, by)`: trim and validate `in.Key` against `roleKeyRE` (no lower-casing — a key that is not already in shape is refused, so the screen shows the key it will store); refuse an empty `Title`; in one `InTx`: insert into `roles (key, created_by)` (unique violation → `Conflictf("there is already a role called %q", key)`), upsert `role_profiles` with the title and description, then call the same insert path `Set` uses for `withFloor(in.Rights)` after the `hasRight` check. Return the row as `Matrix` would show it.
- `Delete(ctx, role)`: `role == RoleOwner` → `Forbiddenf("the owner role cannot be deleted")`; `requireRole`; in one `InTx` with the same advisory lock `Set` takes: count holders (`SELECT count(*) FROM superusers WHERE role = $1 FOR UPDATE` cannot lock a count — lock with `SELECT id FROM superusers WHERE role = $1 FOR UPDATE` and count the rows, then count open invitations), if any → the coded 409 `role_in_use` with message `"%d operator(s) and %d open invitation(s) hold %q; move them to another role first"`; else `DELETE FROM role_profiles WHERE role = $1` and `DELETE FROM roles WHERE key = $1` (grants cascade). A foreign-key violation from a race maps to the same 409.
- `Names(ctx)`: `Keys` joined with titles via `profiles`/`profileOf`, plus `Builtin`.

In `core/role_profiles.go` `SetProfile`, `core/superusers.go` `Create` and `SetRole`, and `core/invitations.go` `Invite`: replace `if !ValidRole(role)` with `if err := s.<roles>.requireRole(ctx, role); err != nil { return nil, err }` (each service reaches the role service through its app: `s.app.roles` or the existing `s.roles` field — use what the file already has). Keep the `role == RoleVendor` vendor rule in `Create` exactly as it is. `profileOf` for a non-built-in role with no profile row returns the key as the title.

- [ ] **Step 4: Run them to make sure they pass, then the role/team suites**

Run: `go test ./core -run 'Role|Invit|Superuser|Team|Vendor|Right' -count=1`
Expected: PASS, including every pre-existing role test (the four built-ins behave exactly as before).

- [ ] **Step 5: Commit**

```powershell
git add core/roles.go core/role_profiles.go core/superusers.go core/invitations.go core/errors.go core/custom_roles_test.go
git commit -m "Let a store make and delete its own roles" -m "Update: skip"
```

---

### Task 3: Routes

**Files:**
- Modify: `core/roles_http.go`, `core/openapi.json` (the `/api/admin/roles` and `/api/admin/roles/{role}` entries at ~8032 and ~8069; add `/api/admin/roles/{role}/reset` and `/api/admin/roles/names`)
- Test: `core/custom_roles_test.go`

**Interfaces:**
- Consumes: `Create`, `Delete`, `Reset`, `Names` from Task 2.
- Produces: `POST /api/admin/roles` `{key, title, description, rights}` → 201 RoleSet (`roles.write`); `DELETE /api/admin/roles/{role}` → 204 (`roles.write`; was reset); `POST /api/admin/roles/{role}/reset` → 200 RoleSet (`roles.write`); `GET /api/admin/roles/names` → 200 `[]RoleName` (`team.read`).

- [ ] **Step 1: Write the failing test**

```go
func TestRoleRoutesMakeResetAndDelete(t *testing.T) {
	app := newTestApp(t)
	rec := doBody(t, app, "POST", "/api/admin/roles",
		`{"key":"packer","title":"Packer","rights":["orders.read"]}`, withAdmin)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, app, "GET", "/api/admin/roles/names", withAdmin); rec.Code != 200 ||
		!strings.Contains(rec.Body.String(), `"packer"`) {
		t.Fatalf("names: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, app, "POST", "/api/admin/roles/manager/reset", withAdmin); rec.Code != 200 {
		t.Fatalf("reset manager: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, app, "DELETE", "/api/admin/roles/packer", withAdmin); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, app, "DELETE", "/api/admin/roles/owner", withAdmin); rec.Code != 403 {
		t.Fatalf("delete owner: %d, want 403", rec.Code)
	}
}
```

(`doBody` is defined in `core/roles_test.go`; add `"strings"` to the imports.) The existing `TestRoleRoutes` (roles_test.go:304) calls `DELETE /api/admin/roles/{role}` expecting a reset — change that call to `POST .../reset` in this same step; it is the one test that pins the old meaning.

- [ ] **Step 2: Run to make sure it fails**

Run: `go test ./core -run 'TestRoleRoutes' -count=1` → FAIL (405/404 on the new routes).

- [ ] **Step 3: Implement**

```go
func (a *App) mountRoleRoutes() {
	a.HandleAdminFunc("GET /api/admin/roles", a.handleListRoles, RightRolesWrite)
	a.HandleAdminFunc("POST /api/admin/roles", a.handleCreateRole, RightRolesWrite)
	// The names alone, for the screens that hand a role out. Behind team.read,
	// not roles.write: choosing somebody's role is staffing the shop, and a
	// store may let a manager staff it without letting them redraw the rules.
	a.HandleAdminFunc("GET /api/admin/roles/names", a.handleRoleNames, RightTeamRead)
	a.HandleAdminFunc("PUT /api/admin/roles/{role}", a.handleSetRoleRights, RightRolesWrite)
	a.HandleAdminFunc("PATCH /api/admin/roles/{role}", a.handleSetRoleProfile, RightRolesWrite)
	// DELETE removes the role now that a store can make one; going back to
	// the engine's defaults is its own verb, and only the starting roles have
	// defaults to go back to.
	a.HandleAdminFunc("DELETE /api/admin/roles/{role}", a.handleDeleteRole, RightRolesWrite)
	a.HandleAdminFunc("POST /api/admin/roles/{role}/reset", a.handleResetRoleRights, RightRolesWrite)
}
```

`handleCreateRole` decodes `NewRole` (`json:"key"`, `json:"title"`, `json:"description"`, `json:"rights"` tags on its fields), calls `Create` with `SuperuserFrom(ctx)`, logs `"role created"`, responds 201. `handleDeleteRole` calls `Delete`, logs `"role deleted"`, `w.WriteHeader(http.StatusNoContent)`. `handleRoleNames` responds with `Names`. Check that `GET /api/admin/roles/names` is not shadowed by `{role}` patterns (Go's mux prefers the literal segment; the route test proves it).

Add the four operations to `core/openapi.json` beside the existing role entries, in the same style (summary, the right in the description, request/response schemas; `role_in_use` 409 on DELETE). Run `go test ./core -run OpenAPI -count=1` to prove the contract test sees them.

- [ ] **Step 4: Run to make sure it passes**

Run: `go test ./core -run 'TestRoleRoutes|OpenAPI|EveryRightGates' -count=1` → PASS.

- [ ] **Step 5: Commit**

```powershell
git add core/roles_http.go core/openapi.json core/custom_roles_test.go core/roles_test.go
git commit -m "Add routes to make, delete and reset roles" -m "Update: skip"
```

---

### Task 4: Roles screens — Add role, Holders, Delete

**Files:**
- Modify: `admin/src/lib/api.js` (`roles`), `admin/src/routes/dash/settings/roles/+page.svelte`, `admin/src/routes/dash/settings/roles/[role]/+page.svelte`
- Test: `admin/tests/roles.test.js` (extend)

**Interfaces:**
- Consumes: Task 3 routes.
- Produces: `roles.create({key, title, description, rights})`, `roles.remove(role)`, `roles.reset(role)` (now `POST .../reset`), `roles.names()`.

- [ ] **Step 1: Write the failing tests** (append to `admin/tests/roles.test.js`, which already defines `list` and `detail` from the two screen files; read `admin/src/lib/api.js` the same way)

```js
const apiSource = readFileSync(new URL("../src/lib/api.js", import.meta.url), "utf8");

test("the roles client makes, deletes, resets and names roles", () => {
    assert.match(apiSource, /create: \(role\) => request\("POST", "\/api\/admin\/roles"/);
    assert.match(apiSource, /remove: \(role\) => api\.delete\(`\/api\/admin\/roles\/\$\{role\}`\)/);
    assert.match(apiSource, /reset: \(role\) => request\("POST", `\/api\/admin\/roles\/\$\{role\}\/reset`/);
    assert.match(apiSource, /names: \(\) => api\.get\("\/api\/admin\/roles\/names"\)/);
});

test("the roles list offers Add role and shows who holds each role", () => {
    assert.match(list, />\s*Add role\s*</);
    assert.match(list, /Holders/);
    assert.match(list, /roles\.create\(/);
});

test("a role's page deletes it only when nobody holds it", () => {
    assert.match(detail, /roles\.remove\(/);
    assert.match(detail, /holders === 0/);
    // The starting roles reset; a store's own role has nothing to reset to.
    assert.match(detail, /builtin/);
});
```

(Use whatever `readFileSync` import the file already has.)

- [ ] **Step 2: Run to make sure they fail**

Run (in `admin/`): `node --test tests/roles.test.js` → FAIL.

- [ ] **Step 3: Implement**

`api.js`:

```js
export const roles = {
    matrix: () => api.get("/api/admin/roles"),
    // Every role's key and title, for the screens that hand one out. Behind
    // team.read, so an operator who staffs the shop can see what to pick.
    names: () => api.get("/api/admin/roles/names"),
    create: (role) => request("POST", "/api/admin/roles", { body: role }),
    save: (role, rights) => request("PUT", `/api/admin/roles/${role}`, { body: { rights } }),
    reset: (role) => request("POST", `/api/admin/roles/${role}/reset`, {}),
    remove: (role) => api.delete(`/api/admin/roles/${role}`),
    rename: (role, title, description) =>
        request("PATCH", `/api/admin/roles/${role}`, { body: { title, description } }),
};
```

Roles list (`+page.svelte`): a primary **Add role** button in the page header (same markup as other screens' header actions) opens the panel's existing drawer component (find it: `Select-String -Path admin\src\lib\components\*.svelte -Pattern 'Drawer'`) with fields **Name** (required) and **Description**, and a read-only **Key** line derived from the name (`name.toLowerCase().trim().replace(/[^a-z0-9]+/g, "_").replace(/^[^a-z]+|_+$/g, "").slice(0, 40)`), shown so the operator sees the identifier that will be stored. Saving calls `roles.create({ key, title: name, description, rights: [] })`, toasts "Role created", and `goto` to `${base}/dash/settings/roles/${key}` where the rights matrix grants rights (kitcommerce-admin's two steps). Add a **Holders** column showing `set.holders`.

Role page (`[role]/+page.svelte`): for `set.builtin` keep **Reset to defaults** (now `roles.reset`); for a store's own role hide it. Show **Delete role** (danger, in the page's footer actions) only when `set.role !== "owner" && set.holders === 0`; when holders > 0 show the hint "Held by N — move them to another role to delete it." Delete confirms with the existing `Confirm` component ("Delete the {title} role? This cannot be undone."), calls `roles.remove`, toasts "Role deleted", and goes back to the list.

Every new control gets the panel's existing hover/press/focus classes (`btn`, `btn secondary`, `btn danger`); drawer and confirm already animate open/close and honour reduced motion — check that `Drawer`/`Confirm` contain a `prefers-reduced-motion` rule; if one does not, add it there (one CSS block in that component, `transition: none` for the transformed element, opacity kept).

- [ ] **Step 4: Run to make sure they pass**

Run (in `admin/`): `npm test` → all pass. `npx vite build` → exit 0.

- [ ] **Step 5: Commit**

```powershell
git add admin/src/lib/api.js admin/src/routes/dash/settings/roles admin/tests/roles.test.js
git commit -m "Add and delete roles from the Roles screen" -m "Update: skip"
```

---

### Task 5: Team screen — the store's roles, and add by email

**Files:**
- Modify: `admin/src/routes/dash/settings/teams/+page.svelte`
- Test: `admin/tests/team.test.js` (new)

**Interfaces:**
- Consumes: `roles.names()` (Task 4), existing `auth.create`, `auth.invite` (see `admin/src/lib/api.js` `auth` client).
- Produces: the KitCommerce admin's flow on the Team screen.

- [ ] **Step 1: Write the failing test**

```js
// admin/tests/team.test.js
/*
 * The Team screen hands out the store's roles, not a list compiled into the
 * panel, and adds people the KitCommerce admin's way: email and role first,
 * and only when the address has no account, a choice between making one and
 * sending an invitation. Source-reading, as tests/roles.test.js is.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const team = readFileSync(new URL("../src/routes/dash/settings/teams/+page.svelte", import.meta.url), "utf8");

test("the role picker comes from the store", () => {
    assert.doesNotMatch(team, /const ROLES = \[/);
    assert.match(team, /roles\.names\(\)/);
});

test("adding a member asks for email and role, then how to bring them in", () => {
    assert.match(team, />\s*Add team member\s*</);
    assert.match(team, /User Account Not Found/);
    assert.match(team, />\s*Create Account\s*</);
    assert.match(team, />\s*Send Invite\s*</);
    assert.match(team, /Confirm &amp; Proceed|Confirm & Proceed/);
});
```

- [ ] **Step 2: Run to make sure it fails**

Run (in `admin/`): `node --test tests/team.test.js` → FAIL.

- [ ] **Step 3: Implement**

- Replace the hard-coded `ROLES` (line ~69) and `roleName` with state loaded once from `roles.names()`: `options = names.map((n) => ({ value: n.role, short: n.title, label: n.description ? \`${n.title} — ${n.description}\` : n.title }))`. Leave out `vendor` from the picker (a vendor account is made from the vendor's page, as today). Every `options={ROLES}` and `ROLES.some(...)` uses the loaded list.
- Header button **Add team member** (replacing the separate "New superuser"/"Invite somebody" entry points; keep the edit drawer for an existing row). It opens a drawer with **Email** and **Role**. On **Continue**: if the email is already in the loaded team list (case-insensitive), show the field error "already on the team". Otherwise open a dialog titled **User Account Not Found** with the body "The email {email} does not have a registered account yet. Choose how you would like to proceed." and two option cards:
  - **Create Account** — "Enter a password" — reveals **Password** (min length as the engine's `validateCredentials` requires; read it from the existing create form's attribute) and calls the existing create call with `{email, password, role}`; toast "User created and added to team".
  - **Send Invite** — "Send a registration link" — "An invitation will be sent to {email} with a secure link to set a password and join the team." Calls the existing invite call; then shows the existing one-time link panel (copy + done) as today; toast "Invite sent".
  - Footer: **Cancel** and **Confirm & Proceed** (shows "Processing…" while saving).
- Single store: "already has an account" means "already an operator here" — the engine has no account outside the store until part 1. Write that in a comment above the check, so part 1's change is obvious.
- Motion: the dialog uses the panel's existing modal component and its open/close animation; option cards get the hover/press/focus feedback the panel's selectable cards use (find an existing `.card` selectable pattern, e.g. in the shipping providers screen, and reuse its class); the selected card shows a visible checked state within 150–300 ms.

- [ ] **Step 4: Run to make sure it passes**

Run (in `admin/`): `npm test` → all pass; `npx vite build` → exit 0.

- [ ] **Step 5: Commit**

```powershell
git add admin/src/routes/dash/settings/teams admin/tests/team.test.js
git commit -m "Add team members by email, as the KitCommerce admin does" -m "Update: skip"
```

---

### Task 6: Decision, docs, build, and the browser walk

**Files:**
- Modify: `PLAN.md` (§5 table: append **D81** after the last decision — read the table's last rows first; if D80/D81 are taken by then, take the next free numbers and update the spec's references), `skills/team.md` (roles section: custom roles, delete rule, the new routes; the Team flow), `docs/admin-panel.md` (Roles and Team screens, briefly), `docs/releases/` (if an unreleased notes file exists, add the breaking change: `DELETE /api/admin/roles/{role}` now deletes; reset is `POST .../reset`)
- Rebuild: `admin/build`
- Browser walk: `<scratchpad>/verify/roles-walk.mjs` (not committed)

- [ ] **Step 1: Write D81**

One row in the §5 table, in its voice: **Custom roles** — a store's roles are rows in `roles`; the engine's four are seeded; `owner` fixed; starting roles editable and deletable; a held role cannot be deleted; a custom role's rights are its grants plus the floor. Reason: D24 refused custom roles to keep role names stable for code and the matrix legible; D64 made titles the store's and keys identifiers, and D65 made rights extensible, so no code needs a role other than `owner` and `vendor`, and the KitCommerce admin's stores expect to name their own. Note the two narrowings (no custom vendor-scoped roles; API keys keep the built-in four) and the breaking DELETE change.

- [ ] **Step 2: Docs check, full build and suites**

```powershell
.\scripts\check-docs.ps1
.\scripts\build.ps1          # via: powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build.ps1
gofmt -l .
go vet ./...
go test ./... -count=1 -timeout 60m
go build -tags no_admin ./...
cd admin; npm test
```

All must pass.

- [ ] **Step 3: Walk it in a browser** (dev store on 8090, as in Global Constraints)

`roles-walk.mjs` (Playwright, launched with `executablePath: "C:/Users/admin/AppData/Local/ms-playwright/chromium-1243/chrome-win64/chrome.exe"`, waiting for `networkidle` after every navigation): sign in at `/admin/auth/login` as `admin@example.com` / `devpassword`; open `/dash/settings/roles`; **Add role** "Packer" → lands on `/dash/settings/roles/packer`; tick `orders.read`, save; back to the list — Packer shows Holders 0; open `/dash/settings/teams`; **Add team member** `packer1@example.com`, role Packer → **User Account Not Found** → **Create Account** with a password → row appears with role Packer; **Add team member** `packer2@example.com` → **Send Invite** → link shown; open Packer's page — no Delete, hint "Held by 2"; remove `packer1` and revoke the invitation; Packer's page now shows **Delete role** → confirm → gone from the list. Capture console errors, failed non-API requests and horizontal overflow throughout; screenshot the dialog in light, dark and at 390 px wide. Run it twice. Also emulate `prefers-reduced-motion: reduce` once and confirm the dialog still opens and shows its checked state.

- [ ] **Step 4: Smoke and doctor**

`.\scripts\smoke.ps1 -BaseUrl http://127.0.0.1:8090` → all pass; `.\gocommerce.exe -db <dev db> doctor` → healthy.

- [ ] **Step 5: Commit**

```powershell
git add PLAN.md skills/team.md docs/admin-panel.md docs/releases admin/build
git commit -m "Let stores make their own roles and add team members by email" -m "A store can now create, rename and delete roles of its own, each a set of the rights the engine already has; Owner stays fixed, and a role somebody holds cannot be deleted. The Team screen adds people by email and role, then offers to create their account or send an invitation. DELETE /api/admin/roles/{role} now deletes a role; resetting one to its defaults is POST /api/admin/roles/{role}/reset."
```

Do not push until asked.
