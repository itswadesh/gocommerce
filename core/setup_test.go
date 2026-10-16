package gocommerce

import (
	"context"
	"net/http"
	"testing"
)

// The setup guide: the steps a new store goes through, in order, read from
// what the store actually has — and each required step locks the ones after
// it until it is done.

func setupOf(t *testing.T, app *App) Setup {
	t.Helper()
	rec := do(t, app, http.MethodGet, "/api/admin/setup", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup = %d: %s", rec.Code, rec.Body)
	}
	var s Setup
	decodeData(t, rec, &s)
	return s
}

func stepOf(s Setup, key string) SetupStep {
	for _, st := range s.Steps {
		if st.Key == key {
			return st
		}
	}
	return SetupStep{}
}

func TestANewStoreStartsAtTheFirstStep(t *testing.T) {
	app := newTestApp(t)
	ctx := WithStore(context.Background(), app.DefaultStore())
	if _, err := app.db.ExecContext(ctx, `UPDATE store_profile SET name = '', email = '', country = ''`); err != nil {
		t.Fatal(err)
	}
	s := setupOf(t, app)
	if len(s.Steps) < 4 || s.Steps[0].Key != "details" {
		t.Fatalf("steps = %+v, want the store's details first", s.Steps)
	}
	if s.Next != "details" || s.Complete {
		t.Fatalf("next = %q complete = %v, want the first step", s.Next, s.Complete)
	}
	for _, st := range s.Steps[1:] {
		// A step already done (cash on delivery is on from the start) is
		// shown done, not locked; every other one waits for step one.
		if st.Done == st.Locked {
			t.Fatalf("step %q: done %v, locked %v", st.Key, st.Done, st.Locked)
		}
	}
	if s.Steps[0].Locked || s.Steps[0].Href == "" || s.Steps[0].Title == "" || s.Steps[0].Why == "" {
		t.Fatalf("the first step = %+v, want it open and saying what and why", s.Steps[0])
	}
}

func TestEachStepOpensTheNext(t *testing.T) {
	app := newTestApp(t)
	ctx := WithStore(context.Background(), app.DefaultStore())
	if _, err := app.db.ExecContext(ctx, `UPDATE store_profile SET name = 'Corner Shop', email = 'hi@corner.example', country = 'GB'`); err != nil {
		t.Fatal(err)
	}
	s := setupOf(t, app)
	if !stepOf(s, "details").Done || s.Next != "products" || stepOf(s, "products").Locked {
		t.Fatalf("after the details: next = %q, steps = %+v", s.Next, s.Steps)
	}
	if !stepOf(s, "shipping").Locked {
		t.Fatal("shipping opened before a product exists")
	}

	simpleProduct(t, app, "SETUP-1", 1000, 5)
	s = setupOf(t, app)
	if !stepOf(s, "products").Done || s.Next == "products" {
		t.Fatalf("after a product: next = %q", s.Next)
	}
	// Cash on delivery is on by default, so a store can take payment from
	// the start; shipping is what is left.
	if !stepOf(s, "payments").Done {
		t.Fatal("cash on delivery did not count as a way to pay")
	}
	if s.Next != "shipping" || stepOf(s, "shipping").Locked {
		t.Fatalf("next = %q, want shipping open", s.Next)
	}

	if _, err := app.db.ExecContext(ctx, `
		WITH z AS (INSERT INTO shipping_zones (name, countries) VALUES ('Home', '{GB}') RETURNING id)
		INSERT INTO shipping_rates (zone_id, name, price_minor) SELECT id, 'Standard', 400 FROM z`); err != nil {
		t.Fatal(err)
	}
	s = setupOf(t, app)
	if !stepOf(s, "shipping").Done {
		t.Fatal("a zone with a rate did not count as shipping")
	}
	if !s.RequiredDone {
		t.Fatalf("every required step is done but the guide says otherwise: %+v", s.Steps)
	}
	// What is left is recommended, never required.
	for _, st := range s.Steps {
		if !st.Done && st.Required {
			t.Fatalf("required step %q still open", st.Key)
		}
	}
}
