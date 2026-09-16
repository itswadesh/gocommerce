package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A store renames its roles without renaming the thing accounts are filed
// under: "Fulfilment" reads better than "Staff" in a warehouse, and the key
// stays `staff` because every superuser row and every role_rights row spells
// it that way.
func TestAStoreRenamesARoleWithoutMovingAnybody(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	// Untouched, a role wears the engine's own words.
	matrix, err := app.Roles().Matrix(ctx)
	if err != nil {
		t.Fatalf("matrix: %v", err)
	}
	staff := roleRow(t, matrix, RoleStaff)
	if staff.Title != "Staff" || staff.Description == "" {
		t.Fatalf("default label = %+v, want the engine's words", staff)
	}
	if staff.TitleCustomized {
		t.Error("an untouched role reports itself renamed")
	}

	set, err := app.Roles().SetProfile(ctx, RoleStaff, "Fulfilment",
		"Packs and ships. Cannot refund.", nil)
	if err != nil {
		t.Fatalf("SetProfile: %v", err)
	}
	if set.Title != "Fulfilment" || !set.Customized {
		t.Errorf("set = %+v", set)
	}

	matrix, err = app.Roles().Matrix(ctx)
	if err != nil {
		t.Fatal(err)
	}
	staff = roleRow(t, matrix, RoleStaff)
	if staff.Title != "Fulfilment" || staff.Description != "Packs and ships. Cannot refund." {
		t.Errorf("after renaming = %+v", staff)
	}
	// The key did not move, which is the whole point: accounts are filed under it.
	if staff.Role != RoleStaff {
		t.Errorf("role key = %q, want %q", staff.Role, RoleStaff)
	}
	// And the rights are untouched by a rename.
	if len(staff.Rights) != len(DefaultRightsOf(RoleStaff)) {
		t.Errorf("renaming changed the rights: %v", staff.Rights)
	}

	// An operator signed in as staff still resolves, still carries staff's
	// rights, and reads the role by its key.
	who := signInAs(t, app, "packer@example.com", RoleStaff)
	rec := do(t, app, http.MethodGet, "/api/admin/products", bearer(who))
	if rec.Code != http.StatusOK {
		t.Errorf("a renamed role stopped working: %d", rec.Code)
	}
}

// Blank is the reset, and it is per field: a store that renamed a role without
// describing it keeps the engine's description rather than losing it.
func TestBlankingARoleLabelPutsTheEnginesWordsBack(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	if _, err := app.Roles().SetProfile(ctx, RoleManager, "Shopkeeper", "", nil); err != nil {
		t.Fatalf("rename: %v", err)
	}
	matrix, _ := app.Roles().Matrix(ctx)
	row := roleRow(t, matrix, RoleManager)
	if row.Title != "Shopkeeper" {
		t.Errorf("title = %q", row.Title)
	}
	if row.Description != DefaultDescriptionOf(RoleManager) {
		t.Errorf("a blank description lost the engine's: %q", row.Description)
	}

	// Clearing both drops the override entirely.
	if _, err := app.Roles().SetProfile(ctx, RoleManager, "", "", nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	matrix, _ = app.Roles().Matrix(ctx)
	row = roleRow(t, matrix, RoleManager)
	if row.Title != DefaultTitleOf(RoleManager) || row.TitleCustomized {
		t.Errorf("after clearing = %+v, want the engine's words back", row)
	}
}

// Owner's rights are unstorable; owner's name is not. A title locks nobody out
// of anything, so the rule that protects the way back into a store does not
// need to reach it.
func TestOwnerCanBeRenamedEvenThoughItsRightsCannotChange(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	if _, err := app.Roles().SetProfile(ctx, RoleOwner, "Proprietor", "", nil); err != nil {
		t.Fatalf("renaming owner: %v", err)
	}
	matrix, _ := app.Roles().Matrix(ctx)
	owner := roleRow(t, matrix, RoleOwner)
	if owner.Title != "Proprietor" {
		t.Errorf("owner title = %q", owner.Title)
	}
	if len(owner.Rights) != len(AllRights) {
		t.Errorf("renaming owner changed its rights: %d of %d", len(owner.Rights), len(AllRights))
	}
	// And its rights are still refused.
	if _, err := app.Roles().Set(ctx, RoleOwner, []Right{RightCatalogRead}, nil); err == nil {
		t.Error("owner's rights became storable")
	}
}

func TestRoleProfileRoute(t *testing.T) {
	app := newTestApp(t)

	rec := do(t, app, http.MethodPatch, "/api/admin/roles/staff", withAdmin,
		jsonBody(t, map[string]string{"title": "Fulfilment", "description": "Packs and ships."}))
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Data RoleSet `json:"data"`
	}
	decodeJSONBody(t, rec.Body.Bytes(), &body)
	if body.Data.Title != "Fulfilment" || body.Data.Role != RoleStaff {
		t.Errorf("data = %+v", body.Data)
	}
	// The whole row comes back, rights included, because the screen shows both.
	if len(body.Data.Rights) == 0 {
		t.Error("the answer carried no rights")
	}

	// An unknown role is refused rather than silently stored.
	if bad := do(t, app, http.MethodPatch, "/api/admin/roles/wizard", withAdmin,
		jsonBody(t, map[string]string{"title": "Wizard"})); bad.Code != http.StatusBadRequest {
		t.Errorf("unknown role = %d, want 400", bad.Code)
	}
	// And a name nobody could render is refused by length.
	long := strings.Repeat("x", MaxRoleTitle+1)
	if bad := do(t, app, http.MethodPatch, "/api/admin/roles/staff", withAdmin,
		jsonBody(t, map[string]string{"title": long})); bad.Code != http.StatusBadRequest {
		t.Errorf("an over-long name = %d, want 400", bad.Code)
	}
	if denied := do(t, app, http.MethodPatch, "/api/admin/roles/staff",
		jsonBody(t, map[string]string{"title": "X"})); denied.Code != http.StatusUnauthorized {
		t.Errorf("with no token = %d, want 401", denied.Code)
	}
}

func roleRow(t *testing.T, matrix *RoleMatrix, role string) RoleSet {
	t.Helper()
	for _, r := range matrix.Roles {
		if r.Role == role {
			return r
		}
	}
	t.Fatalf("no row for %q", role)
	return RoleSet{}
}
