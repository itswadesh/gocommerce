package gocommerce

import (
	"context"
	"errors"
	"slices"
	"strings"
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
	matrix, _ = app.Roles().Matrix(ctx)
	if last := matrix.Roles[len(matrix.Roles)-1]; last.Holders != 2 {
		t.Errorf("packer holders = %d, want 2 (one operator, one open invitation)", last.Holders)
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
	if _, err := app.Roles().Create(ctx, NewRole{Key: "picker", Title: " "}, nil); err == nil {
		t.Error("created a role with no name")
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
	var apiErr *APIError
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
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
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
	// The names are for whoever staffs the shop, not only whoever redraws
	// the rules: team.read is enough.
	staff := signInAs(t, app, "staff@example.com", RoleStaff)
	if rec := do(t, app, "GET", "/api/admin/roles/names", bearer(staff)); rec.Code != 403 {
		t.Fatalf("names as staff (no team.read) = %d, want 403", rec.Code)
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
	if rec := do(t, app, "DELETE", "/api/admin/roles/staff", withAdmin); rec.Code != 409 ||
		!strings.Contains(rec.Body.String(), "role_in_use") {
		t.Fatalf("delete held staff: %d %s, want 409 role_in_use", rec.Code, rec.Body)
	}
}
