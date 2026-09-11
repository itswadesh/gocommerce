package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The permission model is data, so the first thing to pin is the data: every
// role's rights are drawn from the one enumerated list, and the list is what
// custom roles will later be stored against.
func TestRolesDrawFromTheOneList(t *testing.T) {
	known := map[Right]bool{}
	for _, r := range AllRights {
		if known[r] {
			t.Errorf("%s appears twice in AllRights", r)
		}
		known[r] = true
	}

	for _, role := range Roles {
		if !ValidRole(role) {
			t.Errorf("%q is listed in Roles but ValidRole says no", role)
		}
		rights := DefaultRightsOf(role)
		if len(rights) == 0 {
			t.Errorf("role %q carries nothing", role)
		}
		for _, r := range rights {
			if !known[r] {
				t.Errorf("role %q carries %q, which is not in AllRights", role, r)
			}
		}
	}

	// An owner can do everything there is; that is what makes them the role
	// that can grant roles.
	if len(DefaultRightsOf(RoleOwner)) != len(AllRights) {
		t.Errorf("owner has %d rights, want all %d", len(DefaultRightsOf(RoleOwner)), len(AllRights))
	}
	// And the ones that separate the roles, spelled out so a change to the
	// table has to be deliberate.
	cases := []struct {
		role  string
		right Right
		want  bool
	}{
		{RoleManager, RightCatalogWrite, true},
		{RoleManager, RightOrdersRefund, true},
		{RoleManager, RightTeamWrite, false},
		{RoleManager, RightRolesWrite, false},
		{RoleManager, RightTaxesWrite, false},
		{RoleManager, RightDataExport, false},
		// Growing the catalogue must not have narrowed anybody: these are
		// what catalog.read and orders.write used to cover.
		{RoleManager, RightDiscountsWrite, true},
		{RoleStaff, RightTaxesRead, true},
		{RoleStaff, RightLocationsRead, true},
		{RoleStaff, RightInventoryRead, true},
		{RoleStaff, RightOrdersFulfill, true},
		{RoleStaff, RightDiscountsWrite, false},
		{RoleStaff, RightOrdersWrite, true},
		{RoleStaff, RightOrdersRefund, false},
		{RoleStaff, RightCatalogWrite, false},
		{RoleStaff, RightInventoryWrite, false},
		// The twenty-first right is in nobody's default set: an owner reaches
		// it through AllRights, and a store that wants a manager operating it
		// grants it in the matrix rather than receiving it from an upgrade.
		{RoleManager, RightStoreOperate, false},
		{RoleStaff, RightStoreOperate, false},
		{"nonsense", RightCatalogRead, false},
	}
	for _, c := range cases {
		if got := DefaultCan(c.role, c.right); got != c.want {
			t.Errorf("DefaultCan(%q, %q) = %v, want %v", c.role, c.right, got, c.want)
		}
	}
}

// The rights are enforced where they are declared: on the route.
func TestRightsAreEnforcedOnAdminRoutes(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	signIn := func(email, role string) string {
		t.Helper()
		if _, err := app.Superusers().Create(ctx, email, "a-long-enough-password", role); err != nil {
			t.Fatalf("create %s: %v", role, err)
		}
		_, session, err := app.Superusers().Authenticate(ctx, email, "a-long-enough-password", "127.0.0.1")
		if err != nil {
			t.Fatalf("sign in %s: %v", role, err)
		}
		return session.Token
	}
	as := func(token string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
	}

	staff := signIn("staff@example.com", RoleStaff)
	manager := signIn("manager@example.com", RoleManager)
	owner := signIn("owner@example.com", RoleOwner)

	cases := []struct {
		name   string
		token  string
		method string
		path   string
		want   int
	}{
		// A 400 here means the right was carried and the empty body was not: the
		// request reached the handler, which is what these cases are asking.
		{"staff may read the catalog", staff, "GET", "/api/admin/products", 200},
		{"staff may read orders", staff, "GET", "/api/admin/orders", 200},
		{"staff may not write the catalog", staff, "POST", "/api/admin/products", 403},
		{"staff may not see the team", staff, "GET", "/api/admin/superusers", 403},
		{"manager may write the catalog", manager, "POST", "/api/admin/products", 400},
		{"manager may not see the team", manager, "GET", "/api/admin/superusers", 403},
		{"owner may see the team", owner, "GET", "/api/admin/superusers", 200},
		{"a static token may do anything", testAdminToken, "GET", "/api/admin/superusers", 200},

		// The lines the eight-right version could not draw. Each of these was
		// impossible to express before: discounts rode on catalog.write, tax
		// rates on settings.write, and the roles matrix on the same right as
		// the team.
		{"staff may see discounts", staff, "GET", "/api/admin/discounts", 200},
		{"staff may not write discounts", staff, "POST", "/api/admin/discounts", 403},
		{"manager may write discounts", manager, "POST", "/api/admin/discounts", 400},
		{"staff may see tax rates", staff, "GET", "/api/admin/tax-rates", 200},
		{"manager may not write tax rates", manager, "POST", "/api/admin/tax-rates", 403},
		{"owner may write tax rates", owner, "POST", "/api/admin/tax-rates", 400},
		{"manager may not write locations", manager, "POST", "/api/admin/locations", 403},
		{"staff may fulfil", staff, "POST", "/api/admin/create-fulfillment", 400},
		{"staff may not refund", staff, "POST", "/api/admin/orders/1/refund", 403},
		// The counter and the till on opposite sides of one line: staff take the
		// goods back, a manager sends the money.
		{"staff may record a return", staff, "POST", "/api/admin/orders/1/returns", 400},
		{"staff may withdraw a return", staff, "DELETE", "/api/admin/orders/1/returns/1", 404},
		{"manager may not read the roles matrix", manager, "GET", "/api/admin/roles", 403},
		{"owner may read the roles matrix", owner, "GET", "/api/admin/roles", 200},
		{"manager may not export the catalog", manager, "GET", "/api/admin/export/admin-products", 403},
		{"manager may not import", manager, "POST", "/api/admin/import/products", 403},
		{"owner may export", owner, "GET", "/api/admin/export/admin-products", 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(t, app, c.method, c.path, as(c.token))
			if rec.Code != c.want {
				t.Errorf("%s %s = %d, want %d: %s", c.method, c.path, rec.Code, c.want, rec.Body)
			}
			if c.want == 403 && !strings.Contains(rec.Body.String(), "does not carry") {
				t.Errorf("the refusal does not name the missing right: %s", rec.Body)
			}
		})
	}
}

// A store keeps at least one owner, because owner is the only role that can
// hand out roles.
func TestTheLastOwnerCannotBeDemoted(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	only, err := app.Superusers().Create(ctx, "solo@example.com", "a-long-enough-password", RoleOwner)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := app.Superusers().SetRole(ctx, only.ID, RoleStaff); err == nil {
		t.Fatal("demoted the only owner, locking the store out of its own team screen")
	} else if !strings.Contains(err.Error(), "only owner") {
		t.Errorf("refusal = %q, which does not explain why", err.Error())
	}

	// With a second owner, the first may step down.
	second, err := app.Superusers().Create(ctx, "second@example.com", "a-long-enough-password", RoleOwner)
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	demoted, err := app.Superusers().SetRole(ctx, only.ID, RoleManager)
	if err != nil {
		t.Fatalf("demote with a second owner present: %v", err)
	}
	if demoted.Role != RoleManager {
		t.Errorf("role = %q, want manager", demoted.Role)
	}
	if len(demoted.Rights) != len(DefaultRightsOf(RoleManager)) {
		t.Errorf("the record's rights did not follow the role: %v", demoted.Rights)
	}

	// And now the second one is the last.
	if _, err := app.Superusers().SetRole(ctx, second.ID, RoleStaff); err == nil {
		t.Error("demoted the last remaining owner")
	}

	// An unknown role is refused rather than stored, since DefaultRightsOf gives
	// one nothing and the row would lock somebody out silently.
	if _, err := app.Superusers().SetRole(ctx, second.ID, "wizard"); err == nil {
		t.Error("stored a role the engine has never heard of")
	}
}

// Every operator that existed before roles did is an owner: the migration
// defaults the column, so an upgrade changes nobody's access.
func TestExistingOperatorsBecomeOwners(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	su, _, err := app.Superusers().Bootstrap(ctx, "first@example.com", "a-long-enough-password")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if su.Role != RoleOwner {
		t.Errorf("the first operator is %q, want owner", su.Role)
	}
	if !DefaultCan(su.Role, RightTeamWrite) {
		t.Error("the first operator cannot manage the team")
	}
}

// TestEveryAdminRouteDeclaresRights is the guard the roles system was missing.
//
// Rights are variadic on HandleAdminFunc, so forgetting them is silent: the
// route mounts, authentication still runs, and every signed-in operator can
// reach it whatever their role. That is exactly how the discount and tax routes
// shipped ungated — a staff account, deliberately denied catalog.write, could
// have created a hundred-percent-off code.
//
// The exemptions are named rather than pattern-matched, so adding one is a
// decision somebody writes down. They live beside requireRights rather than
// here, because doctor's "admin rights" check reads the same map: a test and a
// running store must never disagree about which routes may be open.
func TestEveryAdminRouteDeclaresRights(t *testing.T) {
	app := newTestApp(t)

	var ungated []string
	for _, r := range app.Routes() {
		if !r.Admin || len(r.Rights) > 0 {
			continue
		}
		if rightsExempt[r.Method+" "+r.Path] {
			continue
		}
		ungated = append(ungated, r.Method+" "+r.Path)
	}
	if len(ungated) > 0 {
		t.Errorf("admin routes with no rights, reachable by any role: %v", ungated)
	}
}

// TestAdminRightsCheckFindsAnUngatedRoute is the same rule as the test above,
// asked of a running store rather than of a test binary.
//
// It lives here rather than in doctor_test.go because it is about the rights
// rule and reads the same rightsExempt map. The distinction it proves is the
// one that mattered: the test above boots an app with no modules in it, so it
// was passing for the whole time twelve module admin routes were open to any
// authenticated operator. The check runs in everybody's binary.
func TestAdminRightsCheckFindsAnUngatedRoute(t *testing.T) {
	ctx := context.Background()

	clean := newTestApp(t)
	if d := diagnostic(t, clean.Diagnose(ctx), "admin rights"); d.Status != StatusOK {
		t.Errorf("a store with no modules: %s — %s", d.Status, d.Detail)
	}

	// testModule mounts its admin routes with no rights, which is exactly the
	// mistake a module author makes by omission.
	withModule := newTestApp(t, &testModule{
		name:  "openmod",
		admin: []string{"GET /api/admin/x/openmod/things"},
	})
	d := diagnostic(t, withModule.Diagnose(ctx), "admin rights")
	if d.Status != StatusWarn {
		t.Fatalf("an ungated module route: %s — %s", d.Status, d.Detail)
	}
	if !strings.Contains(d.Detail, "GET /api/admin/x/openmod/things") {
		t.Errorf("the detail does not name the route: %q", d.Detail)
	}
	if d.Hint == "" {
		t.Error("a finding with no next step just moves the puzzle")
	}
}

// A right a route asks for has to be one a role can actually hold, or the route
// is unreachable by anybody and nobody finds out until somebody tries.
func TestRouteRightsExist(t *testing.T) {
	app := newTestApp(t)

	known := map[Right]bool{}
	for _, r := range AllRights {
		known[r] = true
	}
	for _, route := range app.Routes() {
		for _, right := range route.Rights {
			if !known[right] {
				t.Errorf("%s %s wants %q, which is not a right this engine has",
					route.Method, route.Path, right)
			}
		}
	}
}
