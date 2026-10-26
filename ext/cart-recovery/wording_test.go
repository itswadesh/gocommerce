package cartrecovery

import (
	"context"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// The reminders read like the ones jws sends: the shopper by first name when
// an earlier order gave one, each item with its price, the total, a way back
// and somebody to ask.
func TestTheReminderNamesTheShopperAndPricesEachItem(t *testing.T) {
	h := newHarness(t, Config{StorefrontURL: "https://shop.example.com"}, testAppConfig())
	ctx := context.Background()
	p, err := h.app.Profile().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.Name, p.Email = "Chillish", "hello@chillish.in"
	if _, err := h.app.Profile().Set(ctx, *p, nil); err != nil {
		t.Fatal(err)
	}
	// An earlier order is where her name comes from: a basket has none.
	earlier, _, _ := h.basket("WORD-0", "Asha@example.com", 1)
	if _, err := h.app.Order().Checkout(ctx, "cod", gocommerce.CheckoutInput{
		CartID: earlier, Email: "Asha@example.com", Name: "Asha Rao",
		Address: gocommerce.Address{Line1: "1 Test Street", City: "Testville", State: "CA",
			PostalCode: "94117", Country: "US", Phone: "+15555550123"},
	}, ""); err != nil {
		t.Fatalf("earlier order: %v", err)
	}
	_, cartID, _ := h.basket("WORD-1", "asha@example.com", 2)
	h.idle(cartID, 20)
	h.pass()
	h.due(cartID)
	h.pass()

	var sent []gocommerce.Notification
	for _, n := range h.rec.All() {
		if n.Event == templateFirst {
			sent = append(sent, n)
		}
	}
	if len(sent) != 1 {
		t.Fatalf("sent %d reminders, want 1", len(sent))
	}
	d := sent[0].Data
	for k, want := range map[string]string{
		"customer_first_name": "Asha",
		"cart_total":          "$50.00",
		"store_name":          "Chillish",
		"store_email":         "hello@chillish.in",
	} {
		if d[k] != want {
			t.Errorf("%s = %q, want %q", k, d[k], want)
		}
	}
	if !strings.Contains(d["items"], "2 × gctest WORD-1") || !strings.Contains(d["items"], "$50.00") {
		t.Errorf("items = %q, want the line with its quantity and price", d["items"])
	}

	tpl, ok, err := h.app.NotifyTemplates().GetIn(ctx, gocommerce.ChannelEmail, templateFirst, "")
	if err != nil || !ok {
		t.Fatalf("template: %v %v", ok, err)
	}
	subject, _ := gocommerce.RenderNotifyText(tpl.Subject, d)
	body, err := gocommerce.RenderNotifyText(tpl.Body, d)
	if err != nil {
		t.Fatal(err)
	}
	if subject != "You left something in your cart" {
		t.Errorf("subject = %q", subject)
	}
	for _, want := range []string{"Hi Asha,", "They're saved", "2 × gctest WORD-1", "Total: $50.00", "Complete your order: https://", "hello@chillish.in"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}
