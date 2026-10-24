package gocommerce

import (
	"context"
	"net/http"
	"slices"
	"testing"
)

// The address form at checkout offers only the countries the store delivers
// to (D117), read from its shipping zones.

func shipsTo(t *testing.T, app *App) ShipsTo {
	t.Helper()
	rec := do(t, app, http.MethodGet, "/api/checkout/countries")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/checkout/countries = %d %s", rec.Code, rec.Body)
	}
	var out ShipsTo
	decodeData(t, rec, &out)
	return out
}

// A store with no rates charges its flat shipping anywhere, so nothing is
// narrowed.
func TestAStoreWithNoRatesShipsEverywhere(t *testing.T) {
	app := newTestApp(t)
	zone(t, app, "Unpriced", []string{"IN"}, nil)
	if got := shipsTo(t, app); !got.Everywhere || len(got.Countries) != 0 {
		t.Fatalf("ships to %+v, want everywhere", got)
	}
}

func TestOnlyZonesWithARateCount(t *testing.T) {
	app := newTestApp(t)
	in := zone(t, app, "India", []string{"IN"}, nil)
	rate(t, app, in.ID, "Standard", 4900, 0, nil)
	zone(t, app, "Nepal, unpriced", []string{"NP"}, nil)
	off := zone(t, app, "Bhutan, off", []string{"BT"}, nil)
	rate(t, app, off.ID, "Standard", 9900, 0, nil)
	no := false
	if _, err := app.Shipping().UpdateZone(context.Background(), off.ID, ShippingZoneInput{
		Name: off.Name, Countries: off.Countries, Active: &no,
	}); err != nil {
		t.Fatal(err)
	}
	us := zone(t, app, "US and Canada", []string{"US", "CA"}, nil)
	rate(t, app, us.ID, "Air", 99900, 0, nil)

	got := shipsTo(t, app)
	if got.Everywhere || !slices.Equal(got.Countries, []string{"CA", "IN", "US"}) {
		t.Fatalf("ships to %+v, want CA, IN and US only", got)
	}
}

// A zone that names no country is the rest of the world.
func TestACatchAllZoneShipsEverywhere(t *testing.T) {
	app := newTestApp(t)
	in := zone(t, app, "India", []string{"IN"}, nil)
	rate(t, app, in.ID, "Standard", 4900, 0, nil)
	rest := zone(t, app, "Rest of the world", nil, nil)
	rate(t, app, rest.ID, "Air", 99900, 0, nil)
	if got := shipsTo(t, app); !got.Everywhere {
		t.Fatalf("ships to %+v, want everywhere", got)
	}
}
