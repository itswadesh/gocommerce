package gocommerce

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// A store whose brand's panel host the list no longer has is said loudly at
// start (D111 review): renaming a brand's panel_url otherwise moves every one
// of its stores to the default brand without a word.
func TestAPanelHostNoBrandHasIsLoudAtStart(t *testing.T) {
	before := newTestApp(t)
	if _, err := before.db.Exec(`UPDATE stores SET brand_host = 'admin.old.test'`); err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	cfg := testConfig(before.cfg.DBURL)
	cfg.Brands = twoBrands()
	cfg.Logger = slog.New(slog.NewTextHandler(&logged, nil))
	after := newApp(t, cfg)
	if !strings.Contains(logged.String(), "level=ERROR") || !strings.Contains(logged.String(), "admin.old.test (1)") {
		t.Fatalf("start-up log = %s; want an error naming admin.old.test and its one store", logged.String())
	}
	if st, _ := after.Stores().Get(context.Background(), after.DefaultStore()); st.BrandHost != "admin.old.test" {
		t.Fatalf("start-up rewrote the store's brand to %q; it must leave it for the operator", st.BrandHost)
	}
}

// Renaming a brand's panel host is one command that moves its stores and its
// people together, and says so in each store's trail.
func TestRenamingABrandsPanelHostMovesItsStoresAndPeople(t *testing.T) {
	app := newTestApp(t)
	app.cfg.Brands = twoBrands()
	ctx := context.Background()
	other := addTestStore(t, app, "old-shop")
	for _, q := range []string{
		`UPDATE stores SET brand_host = 'admin.old.test'`,
		`INSERT INTO superusers (email, password_hash, brand_host) VALUES ('old@example.com', 'x', 'admin.old.test')`,
	} {
		if _, err := app.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := app.RenameBrandHost(ctx, "admin.old.test", "admin.delta.test"); err == nil {
		t.Fatal("renamed to a host no brand has")
	}
	if _, _, err := app.RenameBrandHost(ctx, "admin.alpha.test", "admin.beta.test"); err == nil {
		t.Fatal("renamed a host a brand still has; that is a move, store by store")
	}
	stores, people, err := app.RenameBrandHost(WithActorLabel(ctx, "gocommerce brands rename-host"), "ADMIN.OLD.TEST", "admin.beta.test")
	if err != nil || stores != 2 || people != 1 {
		t.Fatalf("rename = %d stores, %d people, %v; want 2, 1", stores, people, err)
	}
	var left int
	if err := app.db.QueryRow(`SELECT (SELECT count(*) FROM stores WHERE brand_host = 'admin.old.test')
		+ (SELECT count(*) FROM superusers WHERE brand_host = 'admin.old.test')`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d rows still name the old host (%v)", left, err)
	}
	for _, id := range []string{app.DefaultStore(), other} {
		rows, _, err := app.Audit().List(WithStore(ctx, id), AuditQuery{Action: AuditStoreSetBrand})
		if err != nil || len(rows) != 1 || !strings.Contains(rows[0].Summary, "admin.old.test") {
			t.Errorf("store %s audit = %+v, %v", id, rows, err)
		}
	}
	if d := findCheck(t, app.Diagnose(ctx), "brands"); d.Status != StatusOK {
		t.Fatalf("doctor after the rename: %+v", d)
	}
}
