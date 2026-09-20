package gocommerce

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// Scoping a vendor.
//
// The property these tests exist for is not "a vendor sees their own rows" —
// that is the easy half and any implementation gets it right on the day it is
// written. It is that **forgetting is safe**: a route added next year by
// somebody who has never heard of vendors must refuse them, rather than serve
// them the whole store because nobody remembered to narrow a query.

// twoSellers sets up a store with two vendors, an account for the first, and a
// variant each is offering, then grants the vendor role enough rights to be
// dangerous if the scope were not there.
func twoSellers(t *testing.T) (app *App, mine, theirs *Vendor, token string, variantID int64) {
	t.Helper()
	app = newTestApp(t)
	ctx := context.Background()

	var err error
	mine, err = app.Vendors().Create(ctx, VendorInput{Name: "Harbour Supply", Status: VendorApproved})
	if err != nil {
		t.Fatalf("create mine: %v", err)
	}
	theirs, err = app.Vendors().Create(ctx, VendorInput{Name: "Quay Goods", Status: VendorApproved})
	if err != nil {
		t.Fatalf("create theirs: %v", err)
	}

	product := simpleProduct(t, app, "SCOPE-1", 3000, 5)
	variantID = product.DefaultVariant().ID
	if _, err := app.Vendors().SetOffer(ctx, mine.ID, OfferInput{VariantID: variantID, PriceMinor: 2800}); err != nil {
		t.Fatalf("my offer: %v", err)
	}
	if _, err := app.Vendors().SetOffer(ctx, theirs.ID, OfferInput{VariantID: variantID, PriceMinor: 2650}); err != nil {
		t.Fatalf("their offer: %v", err)
	}

	if _, err := app.Superusers().CreateForVendor(ctx, mine.ID, "sales@harbour.example", "a-long-enough-password"); err != nil {
		t.Fatalf("create account: %v", err)
	}

	// The store grants the vendor role everything it could plausibly want. If
	// the scope is doing its job, this changes what they may *do* and not whose
	// rows they may do it to.
	if _, err := app.Roles().Set(ctx, RoleVendor, []Right{
		RightCatalogRead, RightVendorsRead, RightVendorsWrite, RightOrdersRead, RightReportsRead,
	}, nil); err != nil {
		t.Fatalf("grant rights: %v", err)
	}

	sess := signIn(t, app, "sales@harbour.example", "a-long-enough-password", "10.0.0.7")
	return app, mine, theirs, sess.Token, variantID
}

// The headline: a vendor sees themselves and nobody else.
func TestAVendorSeesOnlyItsOwnRecord(t *testing.T) {
	app, mine, theirs, token, _ := twoSellers(t)

	rec := do(t, app, http.MethodGet, "/api/admin/vendors", bearer(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body)
	}
	var list struct {
		Data []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	decodeJSONBody(t, rec.Body.Bytes(), &list)
	if len(list.Data) != 1 || list.Data[0].ID != mine.ID {
		t.Errorf("a vendor listing vendors saw %+v, want only themselves", list.Data)
	}
	// The count has to be narrowed too. A total of 2 over a page of 1 tells a
	// seller exactly how many competitors they have.
	if list.Meta.Total != 1 {
		t.Errorf("total = %d, want 1 — the count leaks what the page hides", list.Meta.Total)
	}

	if got := do(t, app, http.MethodGet, "/api/admin/vendors/"+strconv.FormatInt(mine.ID, 10), bearer(token)).Code; got != http.StatusOK {
		t.Errorf("reading their own record = %d, want 200", got)
	}
	// Somebody else's is not found rather than forbidden: that a vendor with
	// that id exists is itself something a competitor should not learn.
	if got := do(t, app, http.MethodGet, "/api/admin/vendors/"+strconv.FormatInt(theirs.ID, 10), bearer(token)).Code; got != http.StatusNotFound {
		t.Errorf("reading a competitor's record = %d, want 404", got)
	}
}

func TestAVendorCannotEditAnother(t *testing.T) {
	app, mine, theirs, token, variantID := twoSellers(t)

	// Their own record: allowed.
	if got := doBody(t, app, http.MethodPatch, "/api/admin/vendors/"+strconv.FormatInt(mine.ID, 10),
		`{"about":"Chandlery since 1974"}`, bearer(token)).Code; got != http.StatusOK {
		t.Errorf("editing their own record = %d, want 200", got)
	}

	// A competitor's: refused.
	if got := doBody(t, app, http.MethodPatch, "/api/admin/vendors/"+strconv.FormatInt(theirs.ID, 10),
		`{"about":"Closed"}`, bearer(token)).Code; got != http.StatusNotFound {
		t.Errorf("editing a competitor = %d, want 404", got)
	}
	if got := do(t, app, http.MethodDelete, "/api/admin/vendors/"+strconv.FormatInt(theirs.ID, 10), bearer(token)).Code; got != http.StatusNotFound {
		t.Errorf("deleting a competitor = %d, want 404", got)
	}

	// Their offers, likewise.
	if got := doBody(t, app, http.MethodPut, "/api/admin/vendors/"+strconv.FormatInt(theirs.ID, 10)+"/offers",
		fmt.Sprintf(`{"variant_id":%d,"price_minor":1}`, variantID), bearer(token)).Code; got != http.StatusNotFound {
		t.Errorf("undercutting a competitor through their own offer = %d, want 404", got)
	}
	if got := do(t, app, http.MethodGet, "/api/admin/vendors/"+strconv.FormatInt(theirs.ID, 10)+"/offers", bearer(token)).Code; got != http.StatusNotFound {
		t.Errorf("reading a competitor's offers = %d, want 404", got)
	}
}

// Two sellers offer the same variant. Each may see their own price and must not
// see the other's — undercutting is a thing a marketplace has to make somebody
// work for.
func TestAVendorSeesOnlyItsOwnOffersOnASharedVariant(t *testing.T) {
	app, _, _, token, variantID := twoSellers(t)

	rec := do(t, app, http.MethodGet, "/api/admin/variants/"+strconv.FormatInt(variantID, 10)+"/offers", bearer(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("variant offers = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Data []struct {
			VendorID int64 `json:"vendor_id"`
		} `json:"data"`
	}
	decodeJSONBody(t, rec.Body.Bytes(), &body)
	if len(body.Data) != 1 {
		t.Errorf("a seller saw %d offers on a shared variant, want only their own", len(body.Data))
	}
}

// The important one. Every admin route is closed to a vendor unless somebody
// deliberately opened it, so a route added later — by somebody who has never
// thought about vendors — refuses them instead of serving the whole store.
func TestEveryAdminRouteIsClosedToVendorsUnlessOpened(t *testing.T) {
	app, _, _, token, _ := twoSellers(t)

	// These are the ones deliberately opened. Everything else must refuse.
	opened := map[string]bool{
		"GET /api/admin/vendors":                            true,
		"GET /api/admin/vendors/{id}":                       true,
		"PATCH /api/admin/vendors/{id}":                     true,
		"DELETE /api/admin/vendors/{id}":                    true,
		"GET /api/admin/vendors/{id}/offers":                true,
		"PUT /api/admin/vendors/{id}/offers":                true,
		"DELETE /api/admin/vendors/{id}/offers/{variantId}": true,
		"GET /api/admin/variants/{id}/offers":               true,
		"GET /api/admin/products":                           true,
		"GET /api/admin/products/{id}":                      true,
	}

	for _, route := range app.Routes() {
		if !route.Admin || route.UI {
			continue
		}
		if opened[route.Pattern] {
			if !route.VendorReachable {
				t.Errorf("%s is meant to be open to vendors but is not marked reachable", route.Pattern)
			}
			continue
		}
		if route.VendorReachable {
			t.Errorf("%s is reachable by vendors and is not in this test's list. "+
				"If that is deliberate, add it here and say why; if not, it is a leak.", route.Pattern)
		}
	}

	// And the refusal is real, not just a flag: a GET a vendor has the right
	// for, on a route nobody opened to them.
	rec := do(t, app, http.MethodGet, "/api/admin/orders", bearer(token))
	if rec.Code != http.StatusForbidden {
		t.Errorf("a vendor reading orders = %d, want 403", rec.Code)
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "vendor") {
		t.Errorf("the refusal does not explain itself: %s", rec.Body)
	}
}

// An operator is not narrowed by any of this.
func TestTheStoreItselfSeesEverything(t *testing.T) {
	app, _, _, _, variantID := twoSellers(t)

	rec := do(t, app, http.MethodGet, "/api/admin/vendors", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner listing = %d: %s", rec.Code, rec.Body)
	}
	var list struct {
		Data []struct{} `json:"data"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	decodeJSONBody(t, rec.Body.Bytes(), &list)
	if list.Meta.Total != 2 {
		t.Errorf("the store sees %d vendors, want both", list.Meta.Total)
	}

	rec = do(t, app, http.MethodGet, "/api/admin/variants/"+strconv.FormatInt(variantID, 10)+"/offers", withAdmin)
	var offers struct {
		Data []struct{} `json:"data"`
	}
	decodeJSONBody(t, rec.Body.Bytes(), &offers)
	if len(offers.Data) != 2 {
		t.Errorf("the store sees %d offers on the shared variant, want both", len(offers.Data))
	}
}

// The shop's own margin is not a seller's business. A vendor reading the
// catalogue to find something to offer must not come away with what it cost.
func TestAVendorCannotReadTheShopsCost(t *testing.T) {
	app, _, _, token, _ := twoSellers(t)

	rec := do(t, app, http.MethodGet, "/api/admin/products", bearer(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("catalogue = %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), `"cost"`) {
		t.Errorf("a vendor can read the shop's cost price: %s", rec.Body.String()[:400])
	}
}
