package cartrecovery

import (
	"net/http"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// The store's notification log is open to anybody who may read orders. A
// reminder's link and the basket's token open the shopper's basket: the log
// shows the reminder with both hidden, and the email still carries them.
func TestTheNotificationLogHidesARemindersLinkAndToken(t *testing.T) {
	for name, appCfg := range map[string]gocommerce.Config{
		"straight to the basket":   testAppConfig(),
		"through the counted link": {PanelURL: "https://admin.example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, Config{StorefrontURL: "https://shop.example.com"}, appCfg)
			token, cartID, _ := h.basket("R-LOG", "logged@example.com", 1)
			h.idle(cartID, 20)
			h.pass()
			h.due(cartID)
			if res := h.pass(); res.Sent != 1 {
				t.Fatalf("sent = %d, want 1 (%+v)", res.Sent, res)
			}
			sent := h.rec.All()
			link := sent[len(sent)-1].Data["recovery_url"]
			if link == "" || sent[len(sent)-1].Data["cart_token"] != token {
				t.Fatalf("the reminder carries %v; want the basket's token and a link", sent[len(sent)-1].Data)
			}

			gctest.AssertNotificationLogHides(t, h.app, templateFirst, token, link)
		})
	}
}

// An operator's own send carries the same link and token, and the log hides
// them the same way.
func TestTheNotificationLogHidesAManualSendsLinkAndToken(t *testing.T) {
	h := newHarness(t, Config{StorefrontURL: "https://shop.example.com"}, testAppConfig())
	token, cartID, _ := h.basket("R-LOG-MANUAL", "manual-log@example.com", 1)
	h.idle(cartID, 20)
	h.pass()
	rec := gctest.AdminRequest(t, h.app, http.MethodPost,
		"/api/admin/x/cart-recovery/abandonments/"+itoa(h.record(cartID).ID)+"/send",
		map[string]any{"channel": "email", "template": templateSecond})
	if rec.Code != http.StatusOK {
		t.Fatalf("send = %d %s", rec.Code, rec.Body)
	}
	sent := h.rec.All()
	link := sent[len(sent)-1].Data["recovery_url"]
	if link == "" || sent[len(sent)-1].Data["cart_token"] != token {
		t.Fatalf("the send carries %v; want the basket's token and a link", sent[len(sent)-1].Data)
	}
	gctest.AssertNotificationLogHides(t, h.app, templateSecond, token, link)
}
