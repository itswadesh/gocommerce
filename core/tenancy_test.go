package gocommerce

import (
	"context"
	"testing"
)

func TestTheEngineCannotBypassRowLevelSecurity(t *testing.T) {
	app := newTestApp(t)
	var super, bypass bool
	if err := app.db.QueryRowContext(context.Background(),
		`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&super, &bypass); err != nil {
		t.Fatal(err)
	}
	if super || bypass {
		t.Fatalf("the test engine logs in as superuser=%v bypassrls=%v; RLS tests would prove nothing", super, bypass)
	}
}

func TestAFreshInstallHasOneStore(t *testing.T) {
	app := newTestApp(t)
	stores, err := app.Stores().Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(stores) != 1 || !ValidStoreID(stores[0].ID) || stores[0].Slug == "" {
		t.Fatalf("stores = %+v, want exactly one with a valid id and slug", stores)
	}
	if app.DefaultStore() != stores[0].ID {
		t.Fatalf("DefaultStore() = %q, want %q", app.DefaultStore(), stores[0].ID)
	}
}

func TestAnOperatorsRoleLivesOnTheirMembership(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	su, err := app.Superusers().Create(ctx, "m@example.com", "a-long-password", RoleManager)
	if err != nil {
		t.Fatal(err)
	}
	if su.Role != RoleManager {
		t.Fatalf("Create returned role %q", su.Role)
	}
	ms, err := app.Stores().MembershipsOf(ctx, su.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].StoreID != app.DefaultStore() || ms[0].Role != RoleManager {
		t.Fatalf("memberships = %+v", ms)
	}
	var cols int
	if err := app.db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'superusers' AND column_name IN ('role','vendor_id')`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if cols != 0 {
		t.Fatal("superusers still carries role/vendor_id; a person's role belongs to a store")
	}
}
