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
