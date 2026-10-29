package cartrecovery

import (
	"net/http"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// A shop that never typed an address on the automation screen still sends
// links: to its own domain, or its address under the base domain. The screen
// reports that same address, so it does not say "no storefront address is
// set" over reminders that carry a working link.
func TestTheScreenReportsTheAddressLinksActuallyUse(t *testing.T) {
	h := newHarness(t, Config{}, gocommerce.Config{Brands: []gocommerce.Brand{
		{Name: "Alpha", PanelURL: "https://admin.alpha.test", BaseDomain: "alpha.test"},
	}})
	h.exec(`UPDATE stores SET brand_host = 'admin.alpha.test'`)
	var slug string
	if err := h.app.DB().QueryRow(`SELECT slug FROM stores WHERE id = $1`, h.app.DefaultStore()).Scan(&slug); err != nil {
		t.Fatal(err)
	}
	want := "https://" + slug + ".alpha.test"

	rec := gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/cart-recovery/settings", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("settings = %d %s", rec.Code, rec.Body)
	}
	var view SettingsView
	gctest.DecodeData(t, rec, &view)
	if view.Settings.StorefrontURL != "" {
		t.Fatalf("the screen's own address = %q, want none typed", view.Settings.StorefrontURL)
	}
	if view.StorefrontFallback != want {
		t.Errorf("storefront_fallback = %q, want the store's own address %q", view.StorefrontFallback, want)
	}
}
