package gocommerce

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func intp(v int) *int { return &v }

func mustZone(t *testing.T, app *App, in ShippingZoneInput) *ShippingZone {
	t.Helper()
	z, err := app.Shipping().CreateZone(context.Background(), in)
	if err != nil {
		t.Fatalf("zone: %v", err)
	}
	return z
}

func mustRate(t *testing.T, app *App, in ShippingRateInput) *ShippingRate {
	t.Helper()
	r, err := app.Shipping().CreateRate(context.Background(), in)
	if err != nil {
		t.Fatalf("rate %s: %v", in.Name, err)
	}
	return r
}

// A rate can be banded by the parcel's weight instead of the basket's value,
// as KitCommerce's "Based on item weight" is.
func TestARateCanBeBandedByWeight(t *testing.T) {
	app := newTestApp(t)
	z := mustZone(t, app, ShippingZoneInput{Name: "Everywhere"})
	mustRate(t, app, ShippingRateInput{ZoneID: z.ID, Name: "Courier", PriceMinor: 200, Basis: "weight", MaxWeightGrams: i64(1000)})
	mustRate(t, app, ShippingRateInput{ZoneID: z.ID, Name: "Courier", PriceMinor: 500, Basis: "weight", MinWeightGrams: 1000})
	for _, c := range []struct {
		grams int64
		want  int64
	}{{0, 200}, {999, 200}, {1000, 500}, {25000, 500}} {
		q, err := app.Shipping().Quote(context.Background(), ShippingQuery{Country: "IN", SubtotalMinor: 100000, WeightGrams: c.grams})
		if err != nil || len(q) != 1 || q[0].Price.AmountMinor != c.want {
			t.Fatalf("%dg: %+v %v, want one quote at %d", c.grams, q, err, c.want)
		}
	}
	// One method is priced one way: a price band beside a weight band of the
	// same name would offer a basket the method twice.
	if _, err := app.Shipping().CreateRate(context.Background(), ShippingRateInput{ZoneID: z.ID, Name: "courier", PriceMinor: 100}); err == nil {
		t.Fatal("a price band beside a weight band of the same method was accepted")
	}
}

// The handling fee is part of what the shopper pays, and the delivery estimate
// and description travel with the quote.
func TestHandlingIsChargedAndDaysAreQuoted(t *testing.T) {
	app := newTestApp(t)
	z := mustZone(t, app, ShippingZoneInput{Name: "Everywhere", Description: "All of it"})
	mustRate(t, app, ShippingRateInput{ZoneID: z.ID, Name: "Standard", Description: "Tracked", PriceMinor: 700,
		HandlingFeeMinor: 150, MinDays: intp(2), MaxDays: intp(4)})
	q, err := app.Shipping().Quote(context.Background(), ShippingQuery{Country: "US", SubtotalMinor: 5000})
	if err != nil || len(q) != 1 {
		t.Fatalf("quote: %+v %v", q, err)
	}
	got := q[0]
	if got.Price.AmountMinor != 850 || got.Description != "Tracked" || got.MinDays == nil || *got.MinDays != 2 || got.MaxDays == nil || *got.MaxDays != 4 {
		t.Fatalf("quote = %+v", got)
	}
	if _, err := app.Shipping().CreateRate(context.Background(), ShippingRateInput{ZoneID: z.ID, Name: "Odd", PriceMinor: 1, MinDays: intp(5), MaxDays: intp(2)}); err == nil {
		t.Fatal("a delivery window that ends before it starts was accepted")
	}
}

// A zone switched off is not there: nothing in it is quoted, and a store whose
// every zone is off is back on its flat rate rather than refusing every sale.
func TestAnInactiveZoneIsNotQuoted(t *testing.T) {
	app := newTestApp(t)
	off := false
	z := mustZone(t, app, ShippingZoneInput{Name: "Everywhere"})
	mustRate(t, app, ShippingRateInput{ZoneID: z.ID, Name: "Standard", PriceMinor: 700})
	if _, err := app.Shipping().UpdateZone(context.Background(), z.ID, ShippingZoneInput{Name: "Everywhere", Active: &off}); err != nil {
		t.Fatal(err)
	}
	quotes, rated, err := app.Shipping().options(context.Background(), app.db, ShippingQuery{Country: "US", SubtotalMinor: 5000})
	if err != nil || rated || len(quotes) != 0 {
		t.Fatalf("an inactive zone: quotes %+v rated %v err %v", quotes, rated, err)
	}
	zones, _, _ := app.Shipping().Zones(context.Background())
	if len(zones) != 1 || zones[0].Active {
		t.Fatalf("zones = %+v", zones)
	}
}

// The storefront's quote weighs the cart: each physical line's weight times
// its quantity.
func TestTheCartIsWeighedForAQuote(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	z := mustZone(t, app, ShippingZoneInput{Name: "Everywhere"})
	mustRate(t, app, ShippingRateInput{ZoneID: z.ID, Name: "Courier", PriceMinor: 200, Basis: "weight", MaxWeightGrams: i64(1000)})
	mustRate(t, app, ShippingRateInput{ZoneID: z.ID, Name: "Courier", PriceMinor: 500, Basis: "weight", MinWeightGrams: 1000})
	p := simpleProduct(t, app, "HEAVY-1", 1000, 10)
	if _, err := app.db.ExecContext(ctx, `UPDATE variants SET weight_grams = 400 WHERE id = $1`, p.DefaultVariant().ID); err != nil {
		t.Fatal(err)
	}
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, p.DefaultVariant().ID, 3) // 1200 g
	rec := do(t, app, http.MethodGet, "/api/checkout/rates?cart="+cart.Token+"&country=US")
	var out struct {
		Rates []ShippingQuote `json:"rates"`
	}
	decodeData(t, rec, &out)
	if len(out.Rates) != 1 || out.Rates[0].Price.AmountMinor != 500 {
		t.Fatalf("a 1200 g cart was quoted %+v, want the 1 kg-and-over band", out.Rates)
	}
}

// A store opened from "Create a store" starts with the zone Varni Jewels runs:
// Local, the United States, priced by the order's value.
func TestANewStoreStartsWithTheDefaultZone(t *testing.T) {
	app := newTestApp(t)
	tok := signInAs(t, app, "zones@example.com", RoleStaff)
	rec := doBody(t, app, http.MethodPost, "/api/stores/create", `{"name":"Zoned","slug":"zoned"}`, bearer(tok))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body)
	}
	st, err := app.Stores().BySlug(context.Background(), "zoned")
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithStore(context.Background(), st.ID)
	zones, rates, err := app.Shipping().Zones(ctx)
	if err != nil || len(zones) != 1 {
		t.Fatalf("zones = %+v %v", zones, err)
	}
	z := zones[0]
	if z.Name != "Local" || len(z.Countries) != 1 || z.Countries[0] != "US" || !z.Active {
		t.Fatalf("zone = %+v", z)
	}
	if n := len(rates[z.ID]); n != len(defaultShippingRates) {
		t.Fatalf("rates = %d, want %d", n, len(defaultShippingRates))
	}
	// $150 of jewellery to the US: free USPS Priority, Overnight at $30, free
	// 2-Day — and no Ground Advantage, which is only under $100.
	q, err := app.Shipping().Quote(ctx, ShippingQuery{Country: "US", SubtotalMinor: 15000})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, x := range q {
		got[x.Name] = x.Price.AmountMinor
	}
	want := map[string]int64{"USPS priority": 0, "Fedex Overnight": 3000, "FedEx 2-Day": 0}
	if len(got) != len(want) {
		t.Fatalf("$150 to the US = %v, want %v", got, want)
	}
	for k, v := range want {
		if p, ok := got[k]; !ok || p != v {
			t.Fatalf("$150 to the US = %v, want %v", got, want)
		}
	}
	// A store that already exists is left as it is.
	var n int
	if err := app.db.QueryRowContext(context.Background(), `SELECT count(*) FROM shipping_zones`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("the existing store gained %d zones", n)
	}
}

func TestAZoneNeedsAName(t *testing.T) {
	app := newTestApp(t)
	var api *APIError
	if _, err := app.Shipping().CreateZone(context.Background(), ShippingZoneInput{Name: "  "}); !errors.As(err, &api) {
		t.Fatalf("a nameless zone: %v", err)
	}
}
