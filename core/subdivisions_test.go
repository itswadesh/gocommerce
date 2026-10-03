package gocommerce

import (
	"context"
	"slices"
	"testing"
)

// A state is read by its code or its name in the countries the engine knows,
// in the spellings people actually send, and as written everywhere else.
func TestAStateIsReadByItsCodeOrItsName(t *testing.T) {
	for _, c := range []struct{ country, state, want string }{
		{"US", "California", "CA"},
		{"us", "ca", "CA"},
		{"US", "US-CA", "CA"},
		{"US", "  new   york ", "NY"},
		{"US", "Washington, D.C.", "DC"},
		{"IN", "Jammu & Kashmir", "JK"},
		{"IN", "Orissa", "OD"},
		{"IN", "OR", "OD"},
		{"IN", "IN-MH", "MH"},
		{"CA", "Québec", "QC"},
		{"AU", "new south wales", "NSW"},
		{"DE", "Bayern", "BAYERN"},
		{"US", "Calif.", "CALIF."},
		{"US", "", ""},
	} {
		if got := StateCode(c.country, c.state); got != c.want {
			t.Errorf("StateCode(%q, %q) = %q, want %q", c.country, c.state, got, c.want)
		}
	}
	for _, in := range []string{"CA", "California", "us-ca"} {
		got := StateSpellings("US", in)
		if !slices.Contains(got, "CA") || !slices.Contains(got, "CALIFORNIA") {
			t.Errorf("StateSpellings(US, %q) = %v; want both CA and CALIFORNIA", in, got)
		}
	}
	if got := StateSpellings("US", ""); len(got) != 0 {
		t.Errorf("no state has spellings %v, want none", got)
	}
}

// A tax rate and a shipping zone written with a state's name are stored as its
// code, and an address meets them whichever way it spells the state — as does
// a rule saved under a name before codes were stored, which is matched as it
// is rather than rewritten.
func TestARuleMeetsAnAddressByTheStatesCodeOrName(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	product := simpleProduct(t, app, "STATE-1", 1000, 20)
	variant := product.DefaultVariant().ID

	newTaxRate(t, app, TaxRateInput{Name: "US", RateBP: 500, Country: "US"})
	ca := newTaxRate(t, app, TaxRateInput{Name: "California", RateBP: 725, Country: "US", State: "California"})
	if ca.State != "CA" {
		t.Errorf("a rate written for California is stored as %q, want CA", ca.State)
	}
	if _, err := app.db.ExecContext(ctx, `
		INSERT INTO tax_rates (name, rate_bp, country, state, active, metadata)
		VALUES ('New York', 400, 'US', 'NEW YORK', true, '{}')`); err != nil {
		t.Fatalf("a rate saved under a name: %v", err)
	}
	for _, c := range []struct{ state, want string }{
		{"CA", "California"},
		{"california", "California"},
		{"US-CA", "California"},
		{"NY", "New York"},
		{"New York", "New York"},
		{"TX", "US"},
	} {
		order, err := checkoutTo(t, app, variant, 1, "US", c.state)
		if err != nil {
			t.Fatalf("checkout to US/%s: %v", c.state, err)
		}
		if order.Lines[0].Tax.Name != c.want {
			t.Errorf("an address in %q paid %q, want %q", c.state, order.Lines[0].Tax.Name, c.want)
		}
	}

	// A patch naming only the state reads it against the rate's own country.
	moved, err := app.Taxes().Update(ctx, ca.ID, TaxRatePatch{State: ptr("Texas")})
	if err != nil || moved.State != "TX" {
		t.Errorf("patched to Texas = %+v (%v), want state TX", moved, err)
	}

	// A zone naming states of two countries stores each as its own country's code.
	west := zone(t, app, "West", []string{"US", "CA"}, []string{"California", "British Columbia"})
	if !slices.Equal(west.States, []string{"CA", "BC"}) {
		t.Errorf("zone states = %v, want [CA BC]", west.States)
	}
	rate(t, app, west.ID, "West coast", 1500, 0, nil)
	var legacy int64
	if err := app.db.QueryRowContext(ctx, `
		INSERT INTO shipping_zones (name, countries, states) VALUES ('Old New York', '{US}', '{"NEW YORK"}')
		RETURNING id`).Scan(&legacy); err != nil {
		t.Fatalf("a zone saved under a name: %v", err)
	}
	rate(t, app, legacy, "Empire", 900, 0, nil)
	for _, c := range []struct{ country, state, want string }{
		{"US", "ca", "West coast"},
		{"CA", "British Columbia", "West coast"},
		{"US", "NY", "Empire"},
	} {
		quotes, err := app.Shipping().Quote(ctx, ShippingQuery{Country: c.country, State: c.state, SubtotalMinor: 1000})
		if err != nil || len(quotes) != 1 || quotes[0].Name != c.want {
			t.Errorf("quote %s/%s = %+v (%v), want %q", c.country, c.state, quotes, err, c.want)
		}
	}
}
