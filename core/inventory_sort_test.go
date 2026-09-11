package gocommerce

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

// Ordering the low-stock report.
//
// The report is one route over two statements — the store's totals and one
// location's own shelf — so the interesting property is not that a sort works
// but that BOTH branches order by the numbers the row actually shows. Sorting
// a per-location page by the store-wide availability would draw a column of
// figures in an order that has nothing to do with them, which is worse than no
// sort at all: it looks correct.

// stockSKUs is the report reduced to the SKUs it returned, in order.
func stockSKUs(t *testing.T, app *App, target string) []string {
	t.Helper()
	rows, _, _ := listStock(t, app, target)
	out := make([]string, 0, len(rows))
	for _, v := range rows {
		out = append(out, v.SKU)
	}
	return out
}

func TestLowStockSortsByTheNumbersItShows(t *testing.T) {
	app := newTestApp(t)

	// Availability, SKU and price each put these three in a different order, so
	// no assertion below can pass by coinciding with another.
	simpleProduct(t, app, "STK-A", 1000, 9)
	simpleProduct(t, app, "STK-B", 3000, 3)
	simpleProduct(t, app, "STK-C", 2000, 6)

	const report = "/api/admin/inventory/low-stock?threshold=100"
	for _, tc := range []struct {
		query string
		want  []string
	}{
		// No sort at all is the report's own order, which is what it is for:
		// the emptiest shelf is the thing to reorder.
		{"", []string{"STK-B", "STK-C", "STK-A"}},
		{"&sort=available&order=asc", []string{"STK-B", "STK-C", "STK-A"}},
		{"&sort=available&order=desc", []string{"STK-A", "STK-C", "STK-B"}},
		{"&sort=on_hand&order=asc", []string{"STK-B", "STK-C", "STK-A"}},
		// Folded, and the fold is PostgreSQL's rather than a second copy here.
		{"&sort=sku&order=asc", []string{"STK-A", "STK-B", "STK-C"}},
		{"&sort=sku&order=desc", []string{"STK-C", "STK-B", "STK-A"}},
		{"&sort=price&order=asc", []string{"STK-A", "STK-C", "STK-B"}},
		{"&sort=price&order=desc", []string{"STK-B", "STK-C", "STK-A"}},
		// Nothing is reserved here, so every row ties — which is the case the
		// tiebreaker exists for, and it has to be stable rather than refused.
		{"&sort=reserved&order=asc", []string{"STK-A", "STK-B", "STK-C"}},
	} {
		got := stockSKUs(t, app, report+tc.query)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("GET %s%s = %v, want %v", report, tc.query, got, tc.want)
		}
	}
}

func TestLowStockAtALocationSortsByThatShelfNotTheStore(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	// Deliberately opposed: at the shop X is the emptiest, and across the store
	// it is the fullest. A per-location page ordered by the store-wide sum would
	// come back in exactly the wrong order, and would still look sorted.
	x := simpleProduct(t, app, "SHELF-X", 1000, 10).DefaultVariant().ID
	y := simpleProduct(t, app, "SHELF-Y", 1000, 1).DefaultVariant().ID
	shop := newLocation(t, app, "shop", "The shop", 1)
	if _, err := app.Stock().Adjust(ctx, x, shop.ID, 1, ""); err != nil {
		t.Fatalf("stock X at the shop: %v", err)
	}
	if _, err := app.Stock().Adjust(ctx, y, shop.ID, 5, ""); err != nil {
		t.Fatalf("stock Y at the shop: %v", err)
	}

	store := stockSKUs(t, app, "/api/admin/inventory/low-stock?threshold=100&sort=available&order=asc")
	if want := []string{"SHELF-Y", "SHELF-X"}; !reflect.DeepEqual(store, want) {
		t.Errorf("store-wide available ascending = %v, want %v — X holds 11 and Y holds 6", store, want)
	}

	here := stockSKUs(t, app,
		"/api/admin/inventory/low-stock?threshold=100&location_id="+id64(shop.ID)+
			"&sort=available&order=asc")
	if want := []string{"SHELF-X", "SHELF-Y"}; !reflect.DeepEqual(here, want) {
		t.Errorf("the shop's available ascending = %v, want %v — the shop holds 1 of X and 5 of Y", here, want)
	}

	// And the same request without a sort is still the shelf's own emptiest
	// first, which is what the location filter meant before a header existed.
	if got := stockSKUs(t, app,
		"/api/admin/inventory/low-stock?threshold=100&location_id="+id64(shop.ID)); !reflect.DeepEqual(got, here) {
		t.Errorf("unsorted per-location report = %v, want %v", got, here)
	}
}

// TestTheStockReportRefusesASortItDoesNotServe covers the branch the generic
// allow-list tests cannot reach: they drive the route without a location, and
// the per-location statement is a different ORDER BY built from a different
// spec. A key accepted at the door and unknown inside would be a 500.
func TestTheStockReportRefusesASortItDoesNotServe(t *testing.T) {
	app := newTestApp(t)
	simpleProduct(t, app, "REFUSE-1", 1000, 1)
	shop := newLocation(t, app, "shop", "The shop", 1)

	for _, target := range []string{
		"/api/admin/inventory/low-stock?sort=title",
		"/api/admin/inventory/low-stock?sort=available&order=sideways",
		"/api/admin/inventory/low-stock?order=desc",
		"/api/admin/inventory/low-stock?location_id=" + id64(shop.ID) + "&sort=title",
		"/api/admin/inventory/low-stock?location_id=" + id64(shop.ID) + "&order=asc",
	} {
		if rec := do(t, app, http.MethodGet, target, withAdmin); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400: %s", target, rec.Code, rec.Body)
		}
	}

	// Every key the door accepts is served by both statements. This is the
	// assertion that would have caught a spec added to one side only.
	for _, field := range lowStockSorts.Fields() {
		for _, order := range []string{"asc", "desc"} {
			for _, where := range []string{"", "&location_id=" + id64(shop.ID)} {
				target := "/api/admin/inventory/low-stock?threshold=100&sort=" +
					field + "&order=" + order + where
				if rec := do(t, app, http.MethodGet, target, withAdmin); rec.Code != http.StatusOK {
					t.Errorf("GET %s = %d: %s", target, rec.Code, rec.Body)
				}
			}
		}
	}
}
