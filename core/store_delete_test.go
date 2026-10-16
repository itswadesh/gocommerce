package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// Deleting a store: its owner, a typed confirmation, and every row it owned
// gone with it — while the store beside it keeps every one of its own.

func deleteStore(t *testing.T, app *App, token, id, confirm string) (int, string) {
	t.Helper()
	rec := doBody(t, app, http.MethodPost, "/api/stores/delete",
		`{"store_id":"`+id+`","confirm":"`+confirm+`"}`, bearer(token))
	return rec.Code, rec.Body.String()
}

func TestDeletingAStoreRemovesEverythingItOwned(t *testing.T) {
	app := newTestApp(t)
	tok := signInAs(t, app, "closer@example.com", RoleOwner)
	// The first store keeps a product and an order, to prove they survive.
	demoOrder(t, app, "KEEP-1", "keeper@example.com", "")

	doomed := addTestStore(t, app, "doomed")
	joinTestStore(t, app, doomed, "closer@example.com", RoleOwner)
	in := WithStore(context.Background(), doomed)
	if _, err := app.db.ExecContext(in, `INSERT INTO customer_groups (code, name) VALUES ('vip', 'VIP')`); err != nil {
		t.Fatal(err)
	}

	if code, body := deleteStore(t, app, tok, doomed, "doomed"); code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", code, body)
	}
	if _, err := app.Stores().Get(context.Background(), doomed); err == nil {
		t.Fatal("the store is still listed")
	}
	// Counted with row security out of the way: the engine's own login could
	// not see another store's rows even if they had survived.
	var left int
	if err := app.db.QueryRowContext(in, `SELECT count(*) FROM customer_groups WHERE store_id = $1`, doomed).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d of the store's rows outlived it", left)
	}
	var seq int
	if err := app.db.QueryRow(`SELECT count(*) FROM pg_class WHERE relkind = 'S' AND relname = $1`, orderSequence(doomed)).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if seq != 0 {
		t.Fatal("the store's order sequence outlived it")
	}
	// The other store is untouched.
	if rec := do(t, app, http.MethodGet, "/api/admin/orders", withAdmin,
		header("x-litekart-store", app.DefaultStore())); !strings.Contains(rec.Body.String(), "keeper@example.com") {
		t.Fatalf("the other store lost its orders: %s", rec.Body)
	}
}

func TestOnlyAnOwnerDeletesAStoreAndOnlyByName(t *testing.T) {
	app := newTestApp(t)
	tok := signInAs(t, app, "staffer@example.com", RoleOwner)
	b := addTestStore(t, app, "kept")
	joinTestStore(t, app, b, "staffer@example.com", RoleStaff)

	if code, _ := deleteStore(t, app, tok, b, "kept"); code != http.StatusForbidden {
		t.Fatalf("staff deleting a store = %d, want 403", code)
	}
	if _, err := app.db.Exec(`UPDATE store_members SET role = 'owner' WHERE store_id = $1`, b); err != nil {
		t.Fatal(err)
	}
	if code, _ := deleteStore(t, app, tok, b, "not-the-name"); code != http.StatusBadRequest {
		t.Fatalf("a wrong confirmation = %d, want 400", code)
	}
	if _, err := app.Stores().Get(context.Background(), b); err != nil {
		t.Fatal("a refused delete removed the store")
	}
	// A store they are not in reads as one they cannot touch.
	stranger := addTestStore(t, app, "stranger")
	if code, _ := deleteStore(t, app, tok, stranger, "stranger"); code != http.StatusForbidden {
		t.Fatalf("deleting another person's store = %d, want 403", code)
	}
}

func TestTheLastStoreAndADemoStoreCannotBeDeleted(t *testing.T) {
	app := newTestApp(t)
	tok := signInAs(t, app, "only@example.com", RoleOwner)
	if code, _ := deleteStore(t, app, tok, app.DefaultStore(), "store"); code != http.StatusConflict {
		t.Fatalf("deleting the install's only store = %d, want 409", code)
	}
	showcase := addTestStore(t, app, "showcase")
	joinTestStore(t, app, showcase, "only@example.com", RoleOwner)
	if err := app.Stores().SetDemo(context.Background(), showcase, true); err != nil {
		t.Fatal(err)
	}
	if code, body := deleteStore(t, app, tok, showcase, "showcase"); code != http.StatusForbidden || !strings.Contains(body, "demo") {
		t.Fatalf("deleting a demo store = %d: %s", code, body)
	}
}
