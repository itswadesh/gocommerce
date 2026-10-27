package cartrecovery

import (
	"context"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// A shop that never typed a storefront address on the recovery screen still
// has one: its own domain, or its address under the base domain. The reminder
// links there rather than handing the shopper a cart reference they cannot use.
func TestAReminderLinksToTheStoresOwnAddressWhenNoneIsSet(t *testing.T) {
	h := newHarness(t, Config{}, gocommerce.Config{BaseDomain: "shops.test"})
	st, err := h.app.Stores().Get(context.Background(), h.app.DefaultStore())
	if err != nil {
		t.Fatal(err)
	}
	token, cartID, _ := h.basket("R-OWN", "own@example.com", 1)
	h.idle(cartID, 20)
	h.pass()
	h.due(cartID)
	if res := h.pass(); res.Sent != 1 {
		t.Fatalf("sent = %d, want 1 (%+v)", res.Sent, res)
	}
	sent := h.rec.All()
	last := sent[len(sent)-1]
	want := "https://" + st.Slug + ".shops.test/cart/" + token
	if got := last.Data["recovery_url"]; got != want {
		t.Fatalf("recovery_url = %q, want %q", got, want)
	}
}

// With no address at all there is no link, and the message says nothing about
// a cart reference: a token is not something a shopper can act on.
func TestTheDefaultReminderNeverShowsTheCartToken(t *testing.T) {
	for _, tpl := range []string{recoveryBody("Intro.", "Go")} {
		if strings.Contains(tpl, "cart_token") || strings.Contains(tpl, "cart reference") {
			t.Fatalf("the default wording still shows the token:\n%s", tpl)
		}
	}
}
