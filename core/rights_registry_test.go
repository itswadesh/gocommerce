package gocommerce

import (
	"context"
	"net/http"
	"testing"
)

// rightsModule is a module that brings a right, the way ext/reviews and the
// rest now do.
type rightsModule struct{ app *App }

func (m *rightsModule) Name() string            { return "rightsfixture" }
func (m *rightsModule) Migrations() []Migration { return nil }
func (m *rightsModule) Register(app *App) error {
	m.app = app
	app.RegisterRight(RightSpec{
		Right:   "fixture.read",
		Label:   "See the fixture",
		Scope:   "Nothing real; this module exists for the test",
		Default: []string{RoleManager},
	})
	app.RegisterRight(RightSpec{
		Right: "fixture.write",
		Label: "Change the fixture",
		Scope: "Owner only, like a right lifted off store.operate",
	})
	app.HandleAdminFunc("GET /api/admin/x/rightsfixture/thing", m.handle, "fixture.read")
	app.HandleAdminFunc("POST /api/admin/x/rightsfixture/thing", m.handle, "fixture.write")
	return nil
}
func (m *rightsModule) handle(w http.ResponseWriter, r *http.Request) {
	Respond(w, http.StatusOK, map[string]string{"ok": "yes"})
}

// A module's rights join the catalogue, reach the roles it named, and gate its
// own routes — which is the whole point: before this, a module screen had no
// right of its own, so it could not be granted or withheld at all.
func TestAModuleBringsItsOwnRights(t *testing.T) {
	app := newTestApp(t, &rightsModule{})
	ctx := context.Background()

	if !app.hasRight("fixture.read") || !app.hasRight("fixture.write") {
		t.Fatal("the module's rights are not in this build")
	}
	// Core's come first and keep their order; the module's are appended.
	all := app.Rights()
	if len(all) != len(AllRights)+2 {
		t.Fatalf("rights = %d, want core's %d plus two", len(all), len(AllRights))
	}
	for i, right := range AllRights {
		if all[i] != right {
			t.Fatalf("core's order moved at %d: %q", i, all[i])
		}
	}

	// The words travel with the right, because the panel cannot have a table
	// for a module it was never built beside.
	var found bool
	for _, entry := range app.RightsCatalogue() {
		if entry.Right == "fixture.read" {
			found = true
			if entry.Label == "" || entry.Module != "rightsfixture" {
				t.Errorf("catalogue entry = %+v", entry)
			}
		}
	}
	if !found {
		t.Error("the module's right is not in the catalogue")
	}

	// Defaults land where the declaration said, and nowhere else.
	if !DefaultCanIn(app, RoleManager, "fixture.read") {
		t.Error("manager did not get the right the module gave it")
	}
	if DefaultCanIn(app, RoleStaff, "fixture.read") {
		t.Error("staff got a right the module did not name")
	}
	if DefaultCanIn(app, RoleManager, "fixture.write") {
		t.Error("manager got an owner-only right")
	}
	// Owner carries everything, which now means everything this binary has.
	if !DefaultCanIn(app, RoleOwner, "fixture.write") {
		t.Error("owner is refused a right from its own store's module")
	}

	// And the right actually gates the route.
	manager := signInAs(t, app, "manager@example.com", RoleManager)
	if rec := do(t, app, http.MethodGet, "/api/admin/x/rightsfixture/thing", bearer(manager)); rec.Code != http.StatusOK {
		t.Errorf("manager on fixture.read = %d, want 200", rec.Code)
	}
	if rec := do(t, app, http.MethodPost, "/api/admin/x/rightsfixture/thing", bearer(manager)); rec.Code != http.StatusForbidden {
		t.Errorf("manager on fixture.write = %d, want 403", rec.Code)
	}

	// The matrix carries them, so the Roles screen can draw a row.
	matrix, err := app.Roles().Matrix(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(matrix.AllRights) != len(all) {
		t.Errorf("matrix lists %d rights, the build has %d", len(matrix.AllRights), len(all))
	}
	if len(matrix.Catalogue) != 2 {
		t.Errorf("matrix catalogue = %d entries, want the module's two", len(matrix.Catalogue))
	}

	// A store may grant one, which it could not do if the right were unknown
	// to the validator.
	set, err := app.Roles().Set(ctx, RoleStaff, append(DefaultRightsOf(RoleStaff), "fixture.read"), nil)
	if err != nil {
		t.Fatalf("granting a module right: %v", err)
	}
	if !containsRight(set.Rights, "fixture.read") {
		t.Errorf("stored set = %v", set.Rights)
	}
	staff := signInAs(t, app, "staff@example.com", RoleStaff)
	if rec := do(t, app, http.MethodGet, "/api/admin/x/rightsfixture/thing", bearer(staff)); rec.Code != http.StatusOK {
		t.Errorf("staff after the grant = %d, want 200", rec.Code)
	}
}

// A right from a module this binary was not built with is refused rather than
// stored: the row would gate nothing and would come back as a checkbox for a
// screen that does not exist.
func TestARightFromAModuleThisBuildLacksIsRefused(t *testing.T) {
	app := newTestApp(t)
	_, err := app.Roles().Set(context.Background(), RoleStaff,
		append(DefaultRightsOf(RoleStaff), "fixture.read"), nil)
	if err == nil {
		t.Fatal("a right no module declared was accepted")
	}
}

// DefaultCanIn is DefaultCan against one build, module rights included.
func DefaultCanIn(app *App, role string, right Right) bool {
	return containsRight(app.defaultRightsOf(role), right)
}

func containsRight(list []Right, want Right) bool {
	for _, r := range list {
		if r == want {
			return true
		}
	}
	return false
}
