package cartrecovery

import (
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// On an install with several brands (D111), a reminder's counted link goes
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
