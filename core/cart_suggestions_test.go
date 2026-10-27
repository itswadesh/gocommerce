package gocommerce

import (
	"context"
	"testing"
)

// The store-wide basket suggestions (D134): one collection, after what the
// basket's own products suggest.

func TestBasketSuggestsTheStoreCollectionAfterCuratedOnes(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	phoneCase := simpleProduct(t, app, "SUG-CASE", 17900, 10)
	grip := simpleProduct(t, app, "SUG-GRIP", 12900, 10)
	cable := simpleProduct(t, app, "SUG-CABLE", 9900, 10)
	charger := simpleProduct(t, app, "SUG-CHARGER", 29900, 10)
	if err := app.Upsells().SetCrossSells(ctx, phoneCase.ID, []int64{grip.ID}); err != nil {
		t.Fatal(err)
	}
	col, err := app.Collections().Create(ctx, CollectionInput{Slug: "under-199", Title: "Under 199"})
	if err != nil {
		t.Fatal(err)
	}
	// The case is in the collection too, and the basket holds it: not suggested.
	if err := app.Collections().SetCollectionProducts(ctx, col.ID, []int64{cable.ID, grip.ID, phoneCase.ID, charger.ID}); err != nil {
		t.Fatal(err)
	}
	cart := newCart(t, app)
	cart = addToCart(t, app, cart.Token, phoneCase.DefaultVariant().ID, 1)

	ids := func() []int64 {
		t.Helper()
		got, err := app.Upsells().CartCrossSells(ctx, cart, 10)
		if err != nil {
			t.Fatal(err)
		}
		out := []int64{}
		for _, p := range got {
			out = append(out, p.ID)
		}
		return out
	}
	if got := ids(); len(got) != 1 || got[0] != grip.ID {
		t.Fatalf("with the feature off: %v, want only the curated grip", got)
	}

	switchPlugin(t, app, CartSuggestionsPluginKey, true, map[string]any{"collection": "under-199"})
	want := []int64{grip.ID, cable.ID, charger.ID}
	got := ids()
	if len(got) != len(want) {
		t.Fatalf("suggestions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("suggestions = %v, want %v", got, want)
		}
	}

	// A collection that has gone is no suggestion, not an error.
	switchPlugin(t, app, CartSuggestionsPluginKey, true, map[string]any{"collection": "gone"})
	if got := ids(); len(got) != 1 {
		t.Errorf("with an unknown collection: %v, want the curated grip alone", got)
	}
}
