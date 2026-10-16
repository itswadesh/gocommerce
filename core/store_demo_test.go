package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// One store of many marked as a demo: its admin routes mask personal data and
// freeze who can sign in, exactly as a whole-install demo does, while every
// other store on the same engine shows its own data in full.

func TestAStoreMarkedDemoMasksItsCustomers(t *testing.T) {
	app := newTestApp(t)
	demoOrder(t, app, "STORE-DEMO-1", "jane.doe@gmail.com", "+31 20 555 1242")
	if err := app.Stores().SetDemo(context.Background(), app.DefaultStore(), true); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/api/admin/orders", "/api/admin/customers"} {
		rec := do(t, app, http.MethodGet, target, withAdmin)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", target, rec.Code, rec.Body)
		}
		body := rec.Body.String()
		if strings.Contains(body, "jane.doe@gmail.com") || strings.Contains(body, "555 1242") {
			t.Fatalf("%s on a demo store shows the real contact details: %s", target, body)
		}
		if !strings.Contains(body, "j•••@g•••.com") {
			t.Fatalf("%s on a demo store does not show the mask: %s", target, body)
		}
	}

	// Switched off, the same store shows its data again: the flag is read
	// on every request, not once at start.
	if err := app.Stores().SetDemo(context.Background(), app.DefaultStore(), false); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, app, http.MethodGet, "/api/admin/orders", withAdmin); !strings.Contains(rec.Body.String(), "jane.doe@gmail.com") {
		t.Fatalf("a store no longer a demo still masks: %s", rec.Body)
	}
}

func TestAnotherStoresDemoFlagMasksNothingHere(t *testing.T) {
	app := newTestApp(t)
	demoOrder(t, app, "STORE-DEMO-2", "sam.real@example.com", "+44 20 7946 0018")
	other := addTestStore(t, app, "showcase")
	if err := app.Stores().SetDemo(context.Background(), other, true); err != nil {
		t.Fatal(err)
	}
	rec := do(t, app, http.MethodGet, "/api/admin/orders", withAdmin,
		header("x-litekart-store", app.DefaultStore()))
	if !strings.Contains(rec.Body.String(), "sam.real@example.com") {
		t.Fatalf("a live store was masked because another store is a demo: %s", rec.Body)
	}
}

func TestADemoStoreFreezesWhoCanSignIn(t *testing.T) {
	app := newTestApp(t)
	other := addTestStore(t, app, "showcase")
	if err := app.Stores().SetDemo(context.Background(), other, true); err != nil {
		t.Fatal(err)
	}
	body := `{"email":"intruder@example.com","password":"a-long-enough-password","role":"owner"}`
	rec := doBody(t, app, http.MethodPost, "/api/admin/superusers", body, withAdmin, header("x-litekart-store", other))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "demo_frozen") {
		t.Fatalf("adding an owner to a demo store = %d: %s", rec.Code, rec.Body)
	}
	// The live store next door is not frozen.
	rec = doBody(t, app, http.MethodPost, "/api/admin/superusers", body, withAdmin, header("x-litekart-store", app.DefaultStore()))
	if rec.Code != http.StatusCreated {
		t.Fatalf("adding an owner to a live store = %d: %s", rec.Code, rec.Body)
	}
}

func TestTheStoreListSaysWhichStoresAreDemos(t *testing.T) {
	app := newTestApp(t)
	other := addTestStore(t, app, "showcase")
	if err := app.Stores().SetDemo(context.Background(), other, true); err != nil {
		t.Fatal(err)
	}
	rec := do(t, app, http.MethodGet, "/api/stores/my", withAdmin)
	var mine []MyStore
	decodeData(t, rec, &mine)
	if len(mine) != 2 || mine[0].Demo || !mine[1].Demo {
		t.Fatalf("stores = %+v, want only the second marked demo", mine)
	}
}
