package gocommerce

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// A vendor account is an operator account that belongs to a seller. The tests
// here are mostly about the thing that must never be true: an account with the
// vendor role and no vendor attached, which is the row every scoping rule fails
// open on.

func TestAVendorAccountBelongsToItsVendor(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	seller, err := app.Vendors().Create(ctx, VendorInput{Name: "Harbour Supply", Status: VendorApproved})
	if err != nil {
		t.Fatalf("create vendor: %v", err)
	}

	su, err := app.Superusers().CreateForVendor(ctx, seller.ID, "sales@harbour.example", "correct horse battery")
	if err != nil {
		t.Fatalf("create vendor account: %v", err)
	}
	if su.Role != RoleVendor {
		t.Errorf("role = %q, want vendor", su.Role)
	}
	if su.VendorID == nil || *su.VendorID != seller.ID {
		t.Errorf("vendor_id = %v, want %d", su.VendorID, seller.ID)
	}

	// It reads back the same way, because the scope depends on it and a field
	// that is only right at creation is a field that leaks on the next login.
	again, err := app.Superusers().Get(ctx, su.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if again.VendorID == nil || *again.VendorID != seller.ID {
		t.Errorf("re-read vendor_id = %v, want %d", again.VendorID, seller.ID)
	}

	// And an ordinary operator has none.
	staff, err := app.Superusers().Create(ctx, "staff@example.com", "correct horse battery", RoleStaff)
	if err != nil {
		t.Fatalf("create staff: %v", err)
	}
	if staff.VendorID != nil {
		t.Errorf("a staff account carries vendor_id %v", staff.VendorID)
	}
}

// The role and the attachment travel together or not at all. This is the
// invariant the whole of the scoping rests on.
func TestAVendorRoleWithoutAVendorIsImpossible(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	// Through the service.
	if _, err := app.Superusers().Create(ctx, "rogue@example.com", "correct horse battery", RoleVendor); err == nil {
		t.Error("the vendor role was granted without a vendor to belong to")
	}

	seller, err := app.Vendors().Create(ctx, VendorInput{Name: "Harbour Supply"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Superusers().CreateForVendor(ctx, seller.ID+9999, "ghost@example.com", "correct horse battery"); err == nil {
		t.Error("an account was attached to a vendor that does not exist")
	}

	// And underneath it, in the database, whatever the service does.
	_, err = app.DB().ExecContext(ctx, `
		INSERT INTO superusers (email, password_hash, role, vendor_id)
		VALUES ('direct@example.com', 'x', 'vendor', NULL)`)
	if err == nil {
		t.Error("the database accepted a vendor account with no vendor")
	}
	_, err = app.DB().ExecContext(ctx, `
		INSERT INTO superusers (email, password_hash, role, vendor_id)
		VALUES ('direct2@example.com', 'x', 'staff', $1)`, seller.ID)
	if err == nil {
		t.Error("the database accepted a staff account attached to a vendor")
	}
}

// Deleting a seller takes their logins with them: an account that can still
// sign in and belongs to nobody is the worst leftover this table could have.
func TestDeletingAVendorTakesItsAccounts(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	seller, err := app.Vendors().Create(ctx, VendorInput{Name: "Harbour Supply"})
	if err != nil {
		t.Fatal(err)
	}
	su, err := app.Superusers().CreateForVendor(ctx, seller.ID, "sales@harbour.example", "correct horse battery")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	if err := app.Vendors().Delete(ctx, seller.ID); err != nil {
		t.Fatalf("delete vendor: %v", err)
	}
	if _, err := app.Superusers().Get(ctx, su.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the account outlived its vendor: %v", err)
	}
}

// Until a store grants it something, a vendor can sign in and reach nothing.
// The default is empty on purpose — see roleRights — because every right in
// this engine answers "may you" and none of them answers "to whose rows".
func TestAVendorStartsWithNoRights(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	if got := DefaultRightsOf(RoleVendor); len(got) != 0 {
		t.Errorf("a new vendor role carries %v, want nothing", got)
	}

	seller, err := app.Vendors().Create(ctx, VendorInput{Name: "Harbour Supply", Status: VendorApproved})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Superusers().CreateForVendor(ctx, seller.ID, "sales@harbour.example", "correct horse battery"); err != nil {
		t.Fatalf("create account: %v", err)
	}
	sess := signIn(t, app, "sales@harbour.example", "correct horse battery", "10.0.0.9")

	// Every admin route refuses them, by right rather than by accident.
	for _, path := range []string{"/api/admin/products", "/api/admin/orders", "/api/admin/vendors"} {
		if got := do(t, app, http.MethodGet, path, bearer(sess.Token)).Code; got != http.StatusForbidden {
			t.Errorf("GET %s as a fresh vendor = %d, want 403", path, got)
		}
	}
}

// The scope is read off the account, and it is the one thing stage 3 keys on.
func TestTheVendorScopeComesOffTheAccount(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	seller, err := app.Vendors().Create(ctx, VendorInput{Name: "Harbour Supply"})
	if err != nil {
		t.Fatal(err)
	}
	vendorUser, err := app.Superusers().CreateForVendor(ctx, seller.ID, "sales@harbour.example", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	staff, err := app.Superusers().Create(ctx, "staff@example.com", "correct horse battery", RoleStaff)
	if err != nil {
		t.Fatal(err)
	}

	if got := VendorScopeOf(WithSuperuser(ctx, vendorUser)); got == nil || *got != seller.ID {
		t.Errorf("a vendor account scopes to %v, want %d", got, seller.ID)
	}
	if got := VendorScopeOf(WithSuperuser(ctx, staff)); got != nil {
		t.Errorf("a staff account scopes to %v, want the whole store", got)
	}
	// No operator at all is the static admin token, which is the store itself.
	if got := VendorScopeOf(ctx); got != nil {
		t.Errorf("an unattributed caller scopes to %v, want the whole store", got)
	}
}
