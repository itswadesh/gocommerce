package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A storefront serving many stores from one deployment talks to the engine
// from its own address, so a store's own domain never reaches the engine as
// the Host. It names the store by that domain instead.
func TestAStoreIsNamedByItsVerifiedDomain(t *testing.T) {
	app := newTestApp(t)
	records := map[string][]string{}
	fakeTXT(app, records)
	verifyDomain(t, app, records, "shop.example.com")
	if code, _ := addDomain(t, app, "unproven.example.com"); code != http.StatusCreated {
		t.Fatalf("add unproven = %d", code)
	}
	if rec := doBody(t, app, http.MethodPatch, "/api/admin/store", `{"name":"Domain Shop"}`, withAdmin); rec.Code != http.StatusOK {
		t.Fatalf("name the store = %d %s", rec.Code, rec.Body)
	}
	want := do(t, app, http.MethodGet, "/api/store", asStore(app.defaultStore)).Body.String()
	if !strings.Contains(want, "Domain Shop") {
		t.Fatalf("store = %s, want its name", want)
	}

	// As `gocommerce serve` runs: no store answers for a request that names none.
	app.defaultStore = ""
	if _, err := app.Stores().Create(context.Background(), "Second Shop", "second-shop"); err != nil {
		t.Fatal(err)
	}

	for _, h := range []string{"X-Store", "x-litekart-store"} {
		rec := do(t, app, http.MethodGet, "/api/store", header(h, "Shop.Example.com"))
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Fatalf("%s: shop.example.com = %d %s, want %s", h, rec.Code, rec.Body, want)
		}
	}

	// A domain nobody proved names no store, and naming no store is a 404
	// rather than some other store's answer.
	for _, host := range []string{"unproven.example.com", "nobody.example.com"} {
		if rec := do(t, app, http.MethodGet, "/api/store", header("X-Store", host)); rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d %s, want 404", host, rec.Code, rec.Body)
		}
	}
}
