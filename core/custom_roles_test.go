package gocommerce

import (
	"context"
	"testing"
)

// The roles a store has are rows now, so the database — not a Go list — is
// what refuses an operator in a role that does not exist.
func TestTheSeededRolesExistAndNoOthers(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	var keys []string
	rows, err := app.db.QueryContext(ctx, `SELECT key FROM roles WHERE builtin ORDER BY key`)
	if err != nil {
		t.Fatalf("query roles: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		keys = append(keys, k)
	}
	want := []string{"manager", "owner", "staff", "vendor"}
	if len(keys) != len(want) {
		t.Fatalf("builtin roles = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("builtin roles = %v, want %v", keys, want)
		}
	}
	if _, err := app.db.ExecContext(ctx,
		`INSERT INTO superusers (email, password_hash, role) VALUES ('x@example.com', 'h', 'nobody')`); err == nil {
		t.Fatal("a superuser in a role that does not exist was stored")
	}
	if _, err := app.db.ExecContext(ctx,
		`INSERT INTO role_rights (role, right_name) VALUES ('owner', 'catalog.read')`); err == nil {
		t.Fatal("a right was stored against owner, which always holds every right")
	}
}
