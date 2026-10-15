package gocommerce

import (
	"context"
	"os"
	"testing"
)

func TestASingleStoreInstallUpgradesInPlace(t *testing.T) {
	dsn := requireDB(t)
	db, err := OpenDB(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Bring the schema to 0052, as a 1.2 install has it, and load fixtures.
	all := coreMigrations()
	var upTo []Migration
	for _, m := range all {
		upTo = append(upTo, m)
		if m.ID == "0052_custom_roles" {
			break
		}
	}
	sets := []migrationSet{{Owner: coreMigrationOwner, Migrations: upTo}}
	if err := runMigrations(context.Background(), db, quietLogger(), sets); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/upgrade_0052.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), string(fixture)); err != nil {
		t.Fatal(err)
	}

	app, err := New(testConfig(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	ctx := context.Background() // DefaultStore "first" binds it

	var orders, elsewhere int
	if err := app.db.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE store_id <> $1) FROM orders`,
		app.DefaultStore()).Scan(&orders, &elsewhere); err != nil {
		t.Fatal(err)
	}
	if orders != 2 || elsewhere != 0 {
		t.Fatalf("orders=%d, not in store #1=%d", orders, elsewhere)
	}
	var number string
	if err := app.db.QueryRowContext(ctx, `SELECT number FROM orders WHERE id = 7`).Scan(&number); err != nil {
		t.Fatal(err)
	}
	if number != "GC-000007" {
		t.Fatalf("order 7 is now %q; ids and numbers must not change", number)
	}
	var counter int64
	if err := app.db.QueryRowContext(ctx, `SELECT value FROM store_counters
		WHERE store_id = $1 AND name = 'order'`, app.DefaultStore()).Scan(&counter); err != nil {
		t.Fatal(err)
	}
	if counter != 7 {
		t.Fatalf("store #1's order counter = %d, want 7 so the next order cannot reuse a number", counter)
	}
	if got := placeOrderIn(t, app, ctx, "AFTER-UPGRADE").Number; got != "GC-000008" {
		t.Fatalf("the first order after the upgrade is %s, want GC-000008", got)
	}
	var role string
	if err := app.db.QueryRowContext(ctx, `SELECT m.role FROM store_members m JOIN superusers s ON s.id = m.superuser_id
		WHERE s.email = 'owner@example.com'`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if role != "owner" {
		t.Fatalf("the owner's membership role = %q", role)
	}
	var slug string
	if err := app.db.QueryRowContext(ctx, `SELECT slug FROM stores WHERE id = $1`, app.DefaultStore()).Scan(&slug); err != nil {
		t.Fatal(err)
	}
	if slug != "old-shop" {
		t.Fatalf("store #1's slug = %q, want old-shop from the store's name", slug)
	}
	var indexed string
	if err := app.db.QueryRowContext(ctx, `SELECT store_id FROM api_key_index WHERE prefix = 'gck_old1'`).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if indexed != app.DefaultStore() {
		t.Fatalf("the old API key routes to %q", indexed)
	}
}
