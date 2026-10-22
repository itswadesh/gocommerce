package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A store's own domain names the store, and so its brand (D111 review): staff
// at the domain of a store that is not in the first brand work there as on
// that brand's panel, and its emails are that brand's.
func TestAStoresOwnDomainIsItsBrand(t *testing.T) {
	app, rec := brandedResetApp(t)
	dark := addTestStore(t, app, "dark-shop")
	inBrand(t, app, dark, "admin.beta.test")
	if _, err := app.db.Exec(`INSERT INTO store_domains (host, store_id, verified_at) VALUES ('shop.dark.example', $1, now())`, dark); err != nil {
		t.Fatal(err)
	}
	tok := sessionIn(t, app, dark, "dark@example.com", RoleOwner)
	at := onHost("shop.dark.example")

	if rec := do(t, app, http.MethodGet, "/api/admin/products", bearer(tok), at); rec.Code != http.StatusOK {
		t.Fatalf("the store's staff at its own domain: %d %s", rec.Code, rec.Body)
	}
	var mine []MyStore
	decodeData(t, do(t, app, http.MethodGet, "/api/stores/my", bearer(tok), at), &mine)
	if len(mine) != 1 || mine[0].ID != dark {
		t.Fatalf("store list at its own domain = %+v, want dark-shop", mine)
	}
	var st struct {
		Brand struct{ Name string } `json:"brand"`
	}
	decodeData(t, do(t, app, http.MethodGet, "/api/admin/auth-state", at), &st)
	if st.Brand.Name != "Beta" {
		t.Fatalf("auth-state brand at its own domain = %q, want Beta", st.Brand.Name)
	}
	do(t, app, http.MethodPost, "/api/admin/password-reset",
		jsonBody(t, map[string]string{"identity": "dark@example.com"}), at)
	if note := lastReset(t, app, rec); !strings.HasPrefix(note.Data["reset_url"], "https://admin.beta.test/") || note.Data["brand_name"] != "Beta" {
		t.Fatalf("reset at the store's own domain = %q, %q; want Beta's", note.Data["brand_name"], note.Data["reset_url"])
	}
}

// A host no brand and no store knows is the default brand: the one
// GOCOMMERCE_BASE_DOMAIN names, where stores answered before brands.
func TestAnUnknownHostIsTheDefaultBrand(t *testing.T) {
	app := newTestApp(t)
	app.cfg.Brands = twoBrands()
	app.cfg.BaseDomain = "beta.test"
	var st struct {
		Brand struct{ Name string } `json:"brand"`
	}
	decodeData(t, do(t, app, http.MethodGet, "/api/admin/auth-state", onHost("203.0.113.9:8080")), &st)
	if st.Brand.Name != "Beta" {
		t.Fatalf("unknown host = %q, want the default brand Beta", st.Brand.Name)
	}
	if b := app.Brand(context.Background()); b.Name != "Beta" {
		t.Fatalf("no request = %q, want the default brand Beta", b.Name)
	}
}
