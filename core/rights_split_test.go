package gocommerce

import (
	"context"
	"net/http"
	"testing"
)

// Splitting four areas out of store.operate must not have moved anybody's
// access. Every one of the eight came off a right only owner held, so owner
// holds them and no other role gained anything.
//
// Written as a test rather than trusted to review because a default reaches
// every store that has never opened the Roles screen: widening one on upgrade
// would hand stores a permission they never granted, and narrowing one would
// take away a screen somebody uses every day.
func TestSplittingStoreOperateMovedNoDefaults(t *testing.T) {
	// What each role carried before the split, spelled out rather than
	// computed, so this fails if a later edit quietly adds to a default.
	want := map[string][]Right{
		RoleManager: {
			RightCatalogRead, RightCatalogWrite,
			RightInventoryRead, RightInventoryWrite,
			RightDiscountsRead, RightDiscountsWrite,
			RightTaxesRead,
			RightLocationsRead,
			RightOrdersRead, RightOrdersWrite, RightOrdersFulfill, RightOrdersRefund,
			RightCustomersRead,
			// Lifted off catalog.read / catalog.write, which manager held, so
			// these reach exactly the screens it already reached.
			RightCollectionsRead, RightCollectionsWrite,
			RightCategoriesRead, RightCategoriesWrite,
			RightMediaRead, RightMediaWrite,
			// Off discounts.read / discounts.write.
			RightPricingRead, RightPricingWrite,
			RightGroupsRead, RightGroupsWrite,
			// Off orders.read.
			RightReportsRead, RightCartsRead, RightPayoutsRead,
		},
		RoleStaff: {
			RightCatalogRead,
			RightInventoryRead,
			RightDiscountsRead,
			RightTaxesRead,
			RightLocationsRead,
			RightOrdersRead, RightOrdersWrite, RightOrdersFulfill,
			RightCustomersRead,
			// Staff held the read half of each and nothing more.
			RightCollectionsRead,
			RightCategoriesRead,
			RightMediaRead,
			RightPricingRead,
			RightGroupsRead,
			RightReportsRead, RightCartsRead, RightPayoutsRead,
		},
	}
	for role, expected := range want {
		held := map[Right]bool{}
		for _, r := range DefaultRightsOf(role) {
			held[r] = true
		}
		for _, r := range expected {
			if !held[r] {
				t.Errorf("%s lost %q", role, r)
			}
			delete(held, r)
		}
		for r := range held {
			t.Errorf("%s gained %q, which no upgrade should hand out", role, r)
		}
	}

	// Owner carries the new ones, because owner carries everything.
	for _, r := range []Right{
		RightShippingRead, RightShippingWrite,
		RightChannelsRead, RightChannelsWrite,
		RightPluginsRead, RightPluginsWrite,
		RightNotificationsRead, RightNotificationsWrite,
	} {
		if !DefaultCan(RoleOwner, r) {
			t.Errorf("owner does not carry %q", r)
		}
		if DefaultCan(RoleManager, r) || DefaultCan(RoleStaff, r) {
			t.Errorf("%q reached a role below owner by default", r)
		}
	}
}

// The four areas answer to their own rights now, and store.operate alone no
// longer opens them. A manager granted shipping.write can set what delivery
// costs without also being handed the outbox.
func TestTheSplitAreasAnswerToTheirOwnRights(t *testing.T) {
	app := newTestApp(t)

	byPath := map[string][]Right{}
	for _, route := range app.Routes() {
		byPath[route.Method+" "+route.Path] = route.Rights
	}
	for path, want := range map[string]Right{
		"GET /api/admin/shipping/zones":                               RightShippingRead,
		"POST /api/admin/shipping/rates":                              RightShippingWrite,
		"GET /api/admin/channels":                                     RightChannelsRead,
		"PATCH /api/admin/channels/{id}":                              RightChannelsWrite,
		"GET /api/admin/plugins":                                      RightPluginsRead,
		"PATCH /api/admin/plugins/{key}":                              RightPluginsWrite,
		"GET /api/admin/notifications/templates":                      RightNotificationsRead,
		"PUT /api/admin/notifications/templates/{channel}/{event}":    RightNotificationsWrite,
		"DELETE /api/admin/notifications/templates/{channel}/{event}": RightNotificationsWrite,
	} {
		rights, ok := byPath[path]
		if !ok {
			t.Errorf("%s is not served", path)
			continue
		}
		found := false
		for _, r := range rights {
			if r == want {
				found = true
			}
			if r == RightStoreOperate {
				t.Errorf("%s still asks for store.operate", path)
			}
		}
		if !found {
			t.Errorf("%s asks for %v, want %q", path, rights, want)
		}
	}

	// And the operational surfaces kept it: this was a split, not a rename.
	for _, path := range []string{
		"GET /api/admin/events",
		"GET /api/admin/audit",
		"GET /api/admin/diagnostics",
		"POST /api/admin/maintenance/drain-outbox",
	} {
		rights, ok := byPath[path]
		if !ok {
			t.Errorf("%s is not served", path)
			continue
		}
		operate := false
		for _, r := range rights {
			if r == RightStoreOperate {
				operate = true
			}
		}
		if !operate {
			t.Errorf("%s no longer asks for store.operate", path)
		}
	}
}

// A session proves the gate, which a static admin token cannot: it carries
// every right by design. A manager is refused the shipping zones until the
// store grants shipping.read, and then is not.
func TestAManagerReachesShippingOnlyWhenGranted(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	manager := signInAs(t, app, "manager@example.com", RoleManager)
	with := bearer(manager)
	if rec := do(t, app, http.MethodGet, "/api/admin/shipping/zones", with); rec.Code != http.StatusForbidden {
		t.Fatalf("manager on the default set = %d, want 403", rec.Code)
	}

	granted := append(DefaultRightsOf(RoleManager), RightShippingRead)
	if _, err := app.Roles().Set(ctx, RoleManager, granted, nil); err != nil {
		t.Fatalf("grant shipping.read: %v", err)
	}
	if rec := do(t, app, http.MethodGet, "/api/admin/shipping/zones", with); rec.Code != http.StatusOK {
		t.Fatalf("after granting shipping.read = %d, want 200", rec.Code)
	}
	// Reading the zones did not come with the outbox.
	if rec := do(t, app, http.MethodGet, "/api/admin/events", with); rec.Code != http.StatusForbidden {
		t.Errorf("shipping.read also opened the outbox: %d", rec.Code)
	}
}
