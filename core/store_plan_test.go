package gocommerce

import (
	"context"
	"testing"
)

// A store starts on the free plan; the install's operator moves it.
func TestAStoreStartsFreeAndCanBePro(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if got := app.StorePlan(ctx); got != PlanFree {
		t.Fatalf("a new store's plan = %q, want free", got)
	}
	if err := app.Stores().SetPlan(ctx, app.defaultStore, PlanPro); err != nil {
		t.Fatal(err)
	}
	if got := app.StorePlan(ctx); got != PlanPro {
		t.Fatalf("plan after the change = %q, want pro", got)
	}
	if err := app.Stores().SetPlan(ctx, app.defaultStore, "gold"); err == nil {
		t.Fatal("an unknown plan was accepted")
	}
}
