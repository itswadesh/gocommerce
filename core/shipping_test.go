package gocommerce

import (
	"context"
	"testing"
)

// zone creates a shipping zone and fails the test if it cannot.
func zone(t *testing.T, app *App, name string, countries, states []string) *ShippingZone {
	t.Helper()
	z, err := app.Shipping().CreateZone(context.Background(), ShippingZoneInput{
		Name: name, Countries: countries, States: states,
	})
	if err != nil {
		t.Fatalf("create zone %q: %v", name, err)
	}
	return z
}

// rate prices a method inside a zone over a subtotal band. A nil max is "no
// ceiling".
func rate(t *testing.T, app *App, zoneID int64, name string, priceMinor, min int64, max *int64) *ShippingRate {
	t.Helper()
	r, err := app.Shipping().CreateRate(context.Background(), ShippingRateInput{
		ZoneID: zoneID, Name: name, PriceMinor: priceMinor,
		MinSubtotalMinor: min, MaxSubtotalMinor: max,
	})
	if err != nil {
		t.Fatalf("create rate %q: %v", name, err)
	}
	return r
}

func i64(v int64) *int64 { return &v }

// The everyday case: a destination, a basket, and what it costs to send.
func TestAZoneOffersItsRatesForADestinationItCovers(t *testing.T) {
	app := newTestApp(t)
	z := zone(t, app, "India", []string{"IN"}, nil)
	rate(t, app, z.ID, "Standard", 4900, 0, nil)
	rate(t, app, z.ID, "Express", 19900, 0, nil)

	quotes, err := app.Shipping().Quote(context.Background(), ShippingQuery{
		Country: "IN", SubtotalMinor: 100000,
	})
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if len(quotes) != 2 {
		t.Fatalf("quotes = %d, want 2", len(quotes))
	}
	if quotes[0].Name != "Standard" || quotes[0].Price.AmountMinor != 4900 {
		t.Errorf("first quote = %q at %d, want Standard at 4900",
			quotes[0].Name, quotes[0].Price.AmountMinor)
	}
}

// Nowhere this store ships to is not an error, it is an empty list — and the
// checkout is what decides whether that refuses the sale.
func TestADestinationNoZoneCoversGetsNothing(t *testing.T) {
	app := newTestApp(t)
	z := zone(t, app, "India", []string{"IN"}, nil)
	rate(t, app, z.ID, "Standard", 4900, 0, nil)

	quotes, err := app.Shipping().Quote(context.Background(), ShippingQuery{
		Country: "FR", SubtotalMinor: 100000,
	})
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if len(quotes) != 0 {
		t.Fatalf("quotes for an uncovered country = %d, want 0", len(quotes))
	}
}

// The same rule tax already applies to a rate's place: the most specific fit
// wins, and only that one's rates are offered.
func TestTheMostSpecificZoneWins(t *testing.T) {
	app := newTestApp(t)
	anywhere := zone(t, app, "Anywhere", nil, nil)
	rate(t, app, anywhere.ID, "Worldwide", 99900, 0, nil)

	india := zone(t, app, "India", []string{"IN"}, nil)
	rate(t, app, india.ID, "Standard", 4900, 0, nil)

	karnataka := zone(t, app, "Karnataka", []string{"IN"}, []string{"KA"})
	rate(t, app, karnataka.ID, "Local courier", 2900, 0, nil)

	for _, tc := range []struct {
		country, state string
		want           string
	}{
		{"IN", "KA", "Local courier"},
		{"IN", "MH", "Standard"},
		{"FR", "", "Worldwide"},
	} {
		quotes, err := app.Shipping().Quote(context.Background(), ShippingQuery{
			Country: tc.country, State: tc.state, SubtotalMinor: 100000,
		})
		if err != nil {
			t.Fatalf("quote %s/%s: %v", tc.country, tc.state, err)
		}
		if len(quotes) != 1 {
			t.Fatalf("%s/%s: quotes = %d, want 1", tc.country, tc.state, len(quotes))
		}
		if quotes[0].Name != tc.want {
			t.Errorf("%s/%s offered %q, want %q", tc.country, tc.state, quotes[0].Name, tc.want)
		}
	}
}

// "Standard 49, free over 2000" is one method and two bands, which is how a
// store actually words it.
func TestASubtotalBandIsWhatMakesShippingFreeOverAThreshold(t *testing.T) {
	app := newTestApp(t)
	z := zone(t, app, "India", []string{"IN"}, nil)
	rate(t, app, z.ID, "Standard", 4900, 0, i64(200000))
	rate(t, app, z.ID, "Standard", 0, 200000, nil)

	for _, tc := range []struct {
		subtotal int64
		want     int64
	}{
		{100000, 4900},
		{199999, 4900},
		{200000, 0},
		{500000, 0},
	} {
		quotes, err := app.Shipping().Quote(context.Background(), ShippingQuery{
			Country: "IN", SubtotalMinor: tc.subtotal,
		})
		if err != nil {
			t.Fatalf("quote at %d: %v", tc.subtotal, err)
		}
		if len(quotes) != 1 {
			t.Fatalf("subtotal %d: quotes = %d, want 1 (bands must not overlap)",
				tc.subtotal, len(quotes))
		}
		if quotes[0].Price.AmountMinor != tc.want {
			t.Errorf("subtotal %d: charged %d, want %d",
				tc.subtotal, quotes[0].Price.AmountMinor, tc.want)
		}
	}
}

// Two bands of one method that overlap would offer a shopper the same method at
// two prices, which is a configuration mistake worth refusing at the door.
func TestOverlappingBandsOfOneMethodAreRefused(t *testing.T) {
	app := newTestApp(t)
	z := zone(t, app, "India", []string{"IN"}, nil)
	rate(t, app, z.ID, "Standard", 4900, 0, i64(200000))

	_, err := app.Shipping().CreateRate(context.Background(), ShippingRateInput{
		ZoneID: z.ID, Name: "Standard", PriceMinor: 0,
		MinSubtotalMinor: 150000, MaxSubtotalMinor: nil,
	})
	if err == nil {
		t.Fatal("an overlapping band was accepted, want a refusal")
	}

	// A different method may overlap freely: that is two options, not two
	// prices for one.
	if _, err := app.Shipping().CreateRate(context.Background(), ShippingRateInput{
		ZoneID: z.ID, Name: "Express", PriceMinor: 19900, MinSubtotalMinor: 0,
	}); err != nil {
		t.Fatalf("a second method overlapping the first was refused: %v", err)
	}
}

// Money is minor units and a currency everywhere else; a rate is money.
func TestARateIsRefusedWithoutTheThingsThatMakeItOne(t *testing.T) {
	app := newTestApp(t)
	z := zone(t, app, "India", []string{"IN"}, nil)

	for _, tc := range []struct {
		what string
		in   ShippingRateInput
	}{
		{"no name", ShippingRateInput{ZoneID: z.ID, PriceMinor: 4900}},
		{"negative price", ShippingRateInput{ZoneID: z.ID, Name: "Standard", PriceMinor: -1}},
		{"no zone", ShippingRateInput{Name: "Standard", PriceMinor: 4900}},
		{"band ends before it starts", ShippingRateInput{
			ZoneID: z.ID, Name: "Standard", PriceMinor: 4900,
			MinSubtotalMinor: 200000, MaxSubtotalMinor: i64(100000),
		}},
	} {
		if _, err := app.Shipping().CreateRate(context.Background(), tc.in); err == nil {
			t.Errorf("%s was accepted, want a refusal", tc.what)
		}
	}
}
