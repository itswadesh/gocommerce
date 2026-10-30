package gocommerce

import (
	"context"
	"slices"
	"testing"
)

// Background work that serves shoppers visits the stores that are serving;
// upkeep a store owes either way visits a suspended one too.
func TestForEveryStoreVisitsSuspendedStoresToo(t *testing.T) {
	app := newTestApp(t)
	held := addTestStore(t, app, "held-shop")
	if _, err := app.db.Exec(`UPDATE stores SET status = 'suspended' WHERE id = $1`, held); err != nil {
		t.Fatal(err)
	}
	visited := func(each func(context.Context, func(context.Context) error) error) []string {
		var ids []string
		if err := each(context.Background(), func(ctx context.Context) error {
			id, _ := StoreFrom(ctx)
			ids = append(ids, id)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return ids
	}
	serving, every := visited(app.ForEachStore), visited(app.ForEveryStore)
	if slices.Contains(serving, held) || len(serving) == 0 {
		t.Errorf("ForEachStore visited %v, want the serving stores and not the suspended %s", serving, held)
	}
	if want := append(slices.Clone(serving), held); !slices.Equal(every, want) {
		t.Errorf("ForEveryStore visited %v, want %v, oldest first", every, want)
	}
}
