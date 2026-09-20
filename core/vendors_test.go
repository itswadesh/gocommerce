package gocommerce

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// A vendor is a seller on this store. The thing it is not is a brand: products
// carry a free-text `vendor` holding "Anker", the Google feed sends that as
// `brand`, and none of it moves here. These tests hold that line, and the one
// about what an offer is.

func vendorsApp(t *testing.T) *App {
	t.Helper()
	return newTestApp(t)
}

func TestAVendorIsCreatedAndRead(t *testing.T) {
	app := vendorsApp(t)
	ctx := context.Background()
	svc := app.Vendors()

	v, err := svc.Create(ctx, VendorInput{
		Name:         "Harbour Supply",
		Email:        "hello@harbour.example",
		CommissionBP: 250,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// A slug is derived when none is given, the way a product's is.
	if v.Slug != "harbour-supply" {
		t.Errorf("slug = %q, want harbour-supply", v.Slug)
	}
	// Nobody is approved by signing up. A marketplace that lists a seller the
	// moment they arrive has no moderation step at all.
	if v.Status != VendorPending {
		t.Errorf("status = %q, want pending", v.Status)
	}
	if v.CommissionBP != 250 {
		t.Errorf("commission = %d bp, want 250", v.CommissionBP)
	}

	got, err := svc.Get(ctx, v.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Harbour Supply" {
		t.Errorf("name = %q", got.Name)
	}

	bySlug, err := svc.GetBySlug(ctx, "harbour-supply")
	if err != nil {
		t.Fatalf("get by slug: %v", err)
	}
	if bySlug.ID != v.ID {
		t.Errorf("by slug returned %d, want %d", bySlug.ID, v.ID)
	}

	if _, err := svc.Get(ctx, v.ID+9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("a vendor that does not exist = %v, want not found", err)
	}
}

func TestVendorValidation(t *testing.T) {
	app := vendorsApp(t)
	ctx := context.Background()
	svc := app.Vendors()

	if _, err := svc.Create(ctx, VendorInput{}); err == nil {
		t.Error("a vendor with no name was accepted")
	}
	if _, err := svc.Create(ctx, VendorInput{Name: "  "}); err == nil {
		t.Error("a whitespace name was accepted")
	}

	// Commission is basis points, so it cannot exceed the whole sale. A
	// marketplace taking 150% is somebody who typed 150 meaning 1.5%.
	if _, err := svc.Create(ctx, VendorInput{Name: "Greedy", CommissionBP: 10001}); err == nil {
		t.Error("a commission over 100% was accepted")
	}
	if _, err := svc.Create(ctx, VendorInput{Name: "Negative", CommissionBP: -1}); err == nil {
		t.Error("a negative commission was accepted")
	}

	if _, err := svc.Create(ctx, VendorInput{Name: "Bad status", Status: "approved-ish"}); err == nil {
		t.Error("a status outside the vocabulary was accepted")
	}

	// Two sellers cannot share a handle: /vendor/{slug} has to resolve to one.
	if _, err := svc.Create(ctx, VendorInput{Name: "Harbour Supply"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	_, err := svc.Create(ctx, VendorInput{Name: "Harbour Supply"})
	if err == nil {
		t.Fatal("a duplicate slug was accepted")
	}
	if !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate slug = %v, want a conflict", err)
	}
}

func TestVendorUpdateAndDelete(t *testing.T) {
	app := vendorsApp(t)
	ctx := context.Background()
	svc := app.Vendors()

	v, err := svc.Create(ctx, VendorInput{Name: "Harbour Supply"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	approved := VendorApproved
	name := "Harbour Supply Co"
	updated, err := svc.Update(ctx, v.ID, VendorPatch{Name: &name, Status: &approved})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != name || updated.Status != VendorApproved {
		t.Errorf("update = %+v", updated)
	}
	// The slug does not follow the name. A handle in circulation is a URL
	// somebody has linked to, and renaming a shop should not break it.
	if updated.Slug != "harbour-supply" {
		t.Errorf("slug moved to %q when the name changed", updated.Slug)
	}

	if err := svc.Delete(ctx, v.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.Get(ctx, v.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete = %v, want not found", err)
	}
}

// An offer is one seller's price and stock for one variant. The product's own
// price and stock stay where they are — they are the store's own first-party
// offer, and every store that exists today sells only that way.
func TestAnOfferPricesOneVariantForOneSeller(t *testing.T) {
	app := vendorsApp(t)
	ctx := context.Background()
	svc := app.Vendors()

	product := simpleProduct(t, app, "OFFER-1", 3000, 5)
	variant := product.DefaultVariant()

	harbour, err := svc.Create(ctx, VendorInput{Name: "Harbour Supply"})
	if err != nil {
		t.Fatalf("create harbour: %v", err)
	}
	quay, err := svc.Create(ctx, VendorInput{Name: "Quay Goods"})
	if err != nil {
		t.Fatalf("create quay: %v", err)
	}

	first, err := svc.SetOffer(ctx, harbour.ID, OfferInput{
		VariantID: variant.ID, PriceMinor: 2800, StockOnHand: 4,
	})
	if err != nil {
		t.Fatalf("set offer: %v", err)
	}
	if first.PriceMinor != 2800 || first.StockOnHand != 4 {
		t.Errorf("offer = %+v", first)
	}

	// Two sellers, one variant: that is the whole point of the table.
	if _, err := svc.SetOffer(ctx, quay.ID, OfferInput{
		VariantID: variant.ID, PriceMinor: 2650, StockOnHand: 2,
	}); err != nil {
		t.Fatalf("second seller: %v", err)
	}

	offers, err := svc.OffersForVariant(ctx, variant.ID)
	if err != nil {
		t.Fatalf("offers for variant: %v", err)
	}
	if len(offers) != 2 {
		t.Fatalf("got %d offers, want 2", len(offers))
	}
	// Cheapest first, because that is the question a product page asks.
	if offers[0].PriceMinor > offers[1].PriceMinor {
		t.Errorf("offers are not cheapest-first: %d then %d", offers[0].PriceMinor, offers[1].PriceMinor)
	}

	// Setting again is an update, not a second row: one seller cannot hold two
	// prices for the same thing.
	again, err := svc.SetOffer(ctx, harbour.ID, OfferInput{
		VariantID: variant.ID, PriceMinor: 2750, StockOnHand: 9,
	})
	if err != nil {
		t.Fatalf("re-set offer: %v", err)
	}
	if again.ID != first.ID {
		t.Errorf("re-setting made a new row: %d then %d", first.ID, again.ID)
	}
	if again.PriceMinor != 2750 {
		t.Errorf("price = %d, want 2750", again.PriceMinor)
	}
	if offers, _ := svc.OffersForVariant(ctx, variant.ID); len(offers) != 2 {
		t.Errorf("re-setting left %d offers, want 2", len(offers))
	}

	// The variant's own price is untouched by any of it. This is the line that
	// keeps the migration additive.
	fresh, err := app.Products().GetProduct(ctx, product.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := fresh.DefaultVariant().Price.AmountMinor; got != 3000 {
		t.Errorf("the variant's own price moved to %d; offers must not touch it", got)
	}

	if err := svc.DeleteOffer(ctx, harbour.ID, variant.ID); err != nil {
		t.Fatalf("delete offer: %v", err)
	}
	if offers, _ := svc.OffersForVariant(ctx, variant.ID); len(offers) != 1 {
		t.Errorf("after withdrawing one offer, %d remain, want 1", len(offers))
	}
}

func TestOfferValidation(t *testing.T) {
	app := vendorsApp(t)
	ctx := context.Background()
	svc := app.Vendors()

	product := simpleProduct(t, app, "OFFER-2", 3000, 5)
	variant := product.DefaultVariant()
	v, err := svc.Create(ctx, VendorInput{Name: "Harbour Supply"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := svc.SetOffer(ctx, v.ID, OfferInput{VariantID: variant.ID, PriceMinor: -1}); err == nil {
		t.Error("a negative price was accepted")
	}
	if _, err := svc.SetOffer(ctx, v.ID, OfferInput{VariantID: variant.ID, PriceMinor: 100, StockOnHand: -1}); err == nil {
		t.Error("negative stock was accepted")
	}
	// A variant that does not exist is a foreign key away from a row nobody
	// could ever sell.
	if _, err := svc.SetOffer(ctx, v.ID, OfferInput{VariantID: variant.ID + 99999, PriceMinor: 100}); err == nil {
		t.Error("an offer against a variant that does not exist was accepted")
	}
	if _, err := svc.SetOffer(ctx, v.ID+99999, OfferInput{VariantID: variant.ID, PriceMinor: 100}); err == nil {
		t.Error("an offer from a vendor that does not exist was accepted")
	}
}

// Deleting a seller withdraws everything they were selling. The alternative —
// refusing while offers exist — leaves an operator unable to remove a seller
// without hand-deleting their catalogue first.
func TestDeletingAVendorWithdrawsItsOffers(t *testing.T) {
	app := vendorsApp(t)
	ctx := context.Background()
	svc := app.Vendors()

	product := simpleProduct(t, app, "OFFER-3", 3000, 5)
	variant := product.DefaultVariant()
	v, _ := svc.Create(ctx, VendorInput{Name: "Harbour Supply"})
	if _, err := svc.SetOffer(ctx, v.ID, OfferInput{VariantID: variant.ID, PriceMinor: 2800}); err != nil {
		t.Fatalf("set offer: %v", err)
	}

	if err := svc.Delete(ctx, v.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	offers, err := svc.OffersForVariant(ctx, variant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 0 {
		t.Errorf("%d offers survived their seller", len(offers))
	}
}

func TestVendorListFilters(t *testing.T) {
	app := vendorsApp(t)
	ctx := context.Background()
	svc := app.Vendors()

	approved, _ := svc.Create(ctx, VendorInput{Name: "Harbour Supply", Status: VendorApproved})
	svc.Create(ctx, VendorInput{Name: "Quay Goods"})
	svc.Create(ctx, VendorInput{Name: "Pier Trading", Status: VendorSuspended})

	all, total, err := svc.List(ctx, VendorQuery{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 3 || len(all) != 3 {
		t.Errorf("list returned %d of %d, want 3 of 3", len(all), total)
	}

	only, total, err := svc.List(ctx, VendorQuery{Status: VendorApproved})
	if err != nil {
		t.Fatalf("list approved: %v", err)
	}
	if total != 1 || len(only) != 1 || only[0].ID != approved.ID {
		t.Errorf("approved-only returned %+v (total %d)", only, total)
	}

	found, _, err := svc.List(ctx, VendorQuery{Search: "quay"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(found) != 1 || found[0].Name != "Quay Goods" {
		t.Errorf("searching for quay returned %+v", found)
	}
}

// Every write leaves a row, because "who approved this seller" and "who changed
// their commission" are the questions a marketplace gets asked.
func TestVendorWritesAreAudited(t *testing.T) {
	app := vendorsApp(t)
	ctx := context.Background()
	svc := app.Vendors()

	product := simpleProduct(t, app, "OFFER-4", 3000, 5)
	v, err := svc.Create(ctx, VendorInput{Name: "Harbour Supply"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	approved := VendorApproved
	if _, err := svc.Update(ctx, v.ID, VendorPatch{Status: &approved}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := svc.SetOffer(ctx, v.ID, OfferInput{
		VariantID: product.DefaultVariant().ID, PriceMinor: 2800,
	}); err != nil {
		t.Fatalf("offer: %v", err)
	}

	rows := auditRows(t, app, "entity_type = $1", AuditEntityVendor)
	actions := map[string]bool{}
	for _, row := range rows {
		actions[row.Action] = true
	}
	for _, want := range []string{AuditVendorCreate, AuditVendorUpdate, AuditVendorOfferSet} {
		if !actions[want] {
			t.Errorf("%q left no audit row; got %v", want, actions)
		}
	}
	// The approval is the one somebody will come looking for, so both sides of
	// it have to be on the row.
	for _, row := range rows {
		if row.Action != AuditVendorUpdate {
			continue
		}
		if row.Changes.Before["status"] != string(VendorPending) {
			t.Errorf("before.status = %v, want pending", row.Changes.Before["status"])
		}
		if row.Changes.After["status"] != string(VendorApproved) {
			t.Errorf("after.status = %v, want approved", row.Changes.After["status"])
		}
	}
}

func TestVendorRoutes(t *testing.T) {
	app := vendorsApp(t)

	rec := do(t, app, http.MethodPost, "/api/admin/vendors", withAdmin,
		jsonBody(t, map[string]any{"name": "Harbour Supply", "commission_bp": 250}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	var created struct {
		Data struct {
			ID   int64  `json:"id"`
			Slug string `json:"slug"`
		} `json:"data"`
	}
	decodeJSONBody(t, rec.Body.Bytes(), &created)
	if created.Data.Slug != "harbour-supply" {
		t.Errorf("slug = %q", created.Data.Slug)
	}

	if got := do(t, app, http.MethodGet, "/api/admin/vendors", withAdmin).Code; got != http.StatusOK {
		t.Errorf("GET list = %d", got)
	}

	// The storefront sees approved sellers only: a pending application is not
	// a shop somebody should be able to browse to.
	pub := do(t, app, http.MethodGet, "/api/vendors")
	if pub.Code != http.StatusOK {
		t.Fatalf("public GET = %d: %s", pub.Code, pub.Body)
	}
	if strings.Contains(pub.Body.String(), "harbour-supply") {
		t.Error("a pending vendor is visible on the storefront")
	}

	// And the rights gate holds.
	staff := signInAs(t, app, "staff@example.com", RoleStaff)
	if got := do(t, app, http.MethodPost, "/api/admin/vendors", bearer(staff),
		jsonBody(t, map[string]any{"name": "Sneaky"})).Code; got != http.StatusForbidden {
		t.Errorf("staff creating a vendor = %d, want 403", got)
	}
}
