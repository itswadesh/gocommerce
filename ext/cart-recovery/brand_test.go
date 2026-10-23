package cartrecovery

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// On an install with several brands (D114), a reminder's counted link goes
// through the panel address of the store's own brand: a shopper of one brand's
// store never sees another brand's domain.
func TestARemindersLinkGoesThroughItsStoresBrand(t *testing.T) {
	h := newHarness(t, Config{StorefrontURL: "https://shop.example.com"}, gocommerce.Config{Brands: []gocommerce.Brand{
		{Name: "Alpha", PanelURL: "https://admin.alpha.test", BaseDomain: "alpha.test"},
		{Name: "Beta", PanelURL: "https://admin.beta.test", BaseDomain: "beta.test"},
	}})
	h.exec(`UPDATE stores SET brand_host = 'admin.beta.test'`)
	_, cartID, _ := h.basket("R-BRAND", "branded@example.com", 1)
	h.idle(cartID, 20)
	h.pass()
	h.due(cartID)
	if res := h.pass(); res.Sent != 1 {
		t.Fatalf("sent = %d, want 1 (%+v)", res.Sent, res)
	}
	sent := h.rec.All()
	if link := sent[len(sent)-1].Data["recovery_url"]; !strings.HasPrefix(link, "https://admin.beta.test/x/cart-recovery/r/") {
		t.Fatalf("recovery_url = %q, want it through Beta's panel address", link)
	}
}

// A reminder's link goes through a panel address, which names no store; on an
// install of many stores the click could not tell whose basket it was. The
// link names its store, so the click lands wherever it is followed.
func TestARemindersLinkNamesItsStore(t *testing.T) {
	h := newHarness(t, Config{StorefrontURL: "https://shop.example.com"}, gocommerce.Config{PanelURL: "https://admin.example.com"})
	_, cartID, _ := h.basket("R-STORE", "named@example.com", 1)
	h.idle(cartID, 20)
	h.pass()
	h.due(cartID)
	if res := h.pass(); res.Sent != 1 {
		t.Fatalf("sent = %d, want 1 (%+v)", res.Sent, res)
	}
	sent := h.rec.All()
	link := sent[len(sent)-1].Data["recovery_url"]
	u, err := url.Parse(link)
	if err != nil || u.Query().Get("store") != h.app.DefaultStore() {
		t.Fatalf("recovery_url = %q, want it to name store %s", link, h.app.DefaultStore())
	}
	req := httptest.NewRequest(http.MethodGet, u.RequestURI(), nil)
	req.Host = u.Host
	rec := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "https://shop.example.com/cart/") {
		t.Fatalf("click = %d %s, want a redirect to the basket", rec.Code, rec.Body)
	}
}
