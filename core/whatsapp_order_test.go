package gocommerce

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// Order on WhatsApp (D255): off until an owner switches it on and says where
// orders go; an order placed with it is confirmed like cash on delivery, and
// the shopper is handed a chat with the order already written.

func setWhatsAppOrders(t *testing.T, app *App, on bool, phone string) {
	t.Helper()
	rec := do(t, app, http.MethodPatch, "/api/admin/plugins/"+WhatsAppPluginKey, withAdmin,
		jsonBody(t, PluginPatch{Enabled: &on, Settings: map[string]any{"phone": phone}}))
	if rec.Code != http.StatusOK {
		t.Fatalf("switch Order on WhatsApp to %v: %d %s", on, rec.Code, rec.Body)
	}
}

func TestOrderOnWhatsAppIsOfferedOnlyOnWithANumber(t *testing.T) {
	app := newTestApp(t)
	if codes, _ := checkoutMethods(t, app); slices.Contains(codes, CodeWhatsApp) {
		t.Fatalf("a new store offers %v, want Order on WhatsApp off", codes)
	}
	setWhatsAppOrders(t, app, true, "")
	if codes, _ := checkoutMethods(t, app); slices.Contains(codes, CodeWhatsApp) {
		t.Fatal("switched on with no number anywhere, it is offered with nobody to send to")
	}
	setWhatsAppOrders(t, app, true, "+91 98765 43210")
	if codes, named := checkoutMethods(t, app); !slices.Contains(codes, CodeWhatsApp) || !slices.Contains(named, CodeWhatsApp) {
		t.Fatalf("on with a number, offered %v / %v", codes, named)
	}
	setWhatsAppOrders(t, app, false, "+91 98765 43210")
	if codes, _ := checkoutMethods(t, app); slices.Contains(codes, CodeWhatsApp) {
		t.Fatal("switched off, still offered")
	}
}

// With no number of its own the method sends to the shop's phone, read the
// way the shop wrote it; the order is confirmed, unpaid, and the link carries
// it in the order's own figures.
func TestOrderOnWhatsAppConfirmsTheOrderAndWritesItIntoTheChat(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if _, err := app.Profile().Set(ctx, StoreProfile{Name: "Rasoi Bakery", Phone: "098765 43210", Country: "IN"}, nil); err != nil {
		t.Fatal(err)
	}
	setWhatsAppOrders(t, app, true, "")
	product := simpleProduct(t, app, "WA-CAKE", 45000, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 2)

	res, err := app.Order().Checkout(ctx, CodeWhatsApp, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatal(err)
	}
	o := res.Order
	if res.Payment.Kind != IntentNone || o.Status != OrderConfirmed || o.PaymentStatus != PaymentPending || o.PaymentProvider != CodeWhatsApp {
		t.Fatalf("payment %+v, order %s / %s / %s; want confirmed, unpaid, by WhatsApp", res.Payment, o.Status, o.PaymentStatus, o.PaymentProvider)
	}
	link := res.Payment.ClientData["whatsapp_url"]
	const prefix = "https://wa.me/919876543210?text="
	if !strings.HasPrefix(link, prefix) || strings.Contains(link, "+") {
		t.Fatalf("whatsapp_url = %q, want the shop's number with %%20 for spaces", link)
	}
	text, err := url.QueryUnescape(strings.TrimPrefix(link, prefix))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Hello Rasoi Bakery, I have placed order " + o.Number + ".",
		"2 × Test WA-CAKE: " + messageMoney(o.Lines[0].Total),
		"Total: " + messageMoney(o.Total),
		"Name: A Shopper",
		"Deliver to: 1 Test Street, Testville 12345",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the message lacks %q:\n%s", want, text)
		}
	}
}

func TestAPrepaidOnlyCodeIsRefusedOnAWhatsAppOrder(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	setWhatsAppOrders(t, app, true, "+91 98765 43210")
	product := simpleProduct(t, app, "WA-PREPAID", 1000, 5)
	newDiscount(t, app, DiscountInput{Code: "PAYNOW", Title: "Pay online", Kind: DiscountFixed, ValueMinor: 100, PrepaidOnly: true})
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	if err := app.Cart().SetDiscountCode(ctx, cart.Token, "PAYNOW"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Order().Checkout(ctx, CodeWhatsApp, checkoutInput(cart.Token), ""); !isValidation(err) {
		t.Fatalf("a prepaid-only code on a WhatsApp order: err = %v, want refused", err)
	}
}

func TestWhatsAppDigits(t *testing.T) {
	for _, c := range []struct{ phone, country, want string }{
		{"+91 98765 43210", "", "919876543210"},
		{"919876543210", "IN", "919876543210"},
		{"98765 43210", "IN", "919876543210"},
		{"098765-43210", "in", "919876543210"},
		{"0044 20 7946 0958", "GB", "442079460958"},
		{"+44 (20) 7946 0958", "IN", "442079460958"},
		{"12345", "IN", ""},
		{"", "IN", ""},
	} {
		if got := whatsappDigits(c.phone, c.country); got != c.want {
			t.Errorf("whatsappDigits(%q, %q) = %q, want %q", c.phone, c.country, got, c.want)
		}
	}
}

func TestWhatsAppOrderTextIsTheOrderAsTheShopNeedsIt(t *testing.T) {
	inr := func(minor int64) Money { return Money{AmountMinor: minor, Currency: "INR"} }
	o := &Order{
		Number: "GC-000123",
		Lines: []OrderLine{
			{Title: "Chocolate cake", VariantLabel: "500 g", Quantity: 2, Total: inr(90000)},
			{Title: "Brown bread", Quantity: 1, Total: inr(4050)},
		},
		Shipping: inr(3000), Discount: inr(5000), Total: inr(92050),
		Name: "Asha Rao", Phone: "+91 98765 43210",
		Address: Address{Line1: "12 MG Road", Line2: "Saheed Nagar", City: "Bhubaneswar", State: "OD", PostalCode: "751007", Country: "IN"},
	}
	want := "Hello Rasoi Bakery, I have placed order GC-000123.\n\n" +
		"2 × Chocolate cake (500 g): ₹900\n" +
		"1 × Brown bread: ₹40.50\n\n" +
		"Delivery: ₹30\n" +
		"Discount: −₹50\n" +
		"Total: ₹920.50\n\n" +
		"Name: Asha Rao\n" +
		"Phone: +91 98765 43210\n" +
		"Deliver to: 12 MG Road, Saheed Nagar, Bhubaneswar, OD 751007"
	if got := whatsappOrderText("Rasoi Bakery", o); got != want {
		t.Fatalf("message:\n%s\n\nwant:\n%s", got, want)
	}
}

// The order link is the storefront's quick-order page, with a QR of it; a
// store nobody can reach yet has neither.
func TestOrderLinkIsTheStorefrontsOrderPageWithItsQRCode(t *testing.T) {
	app := newTestApp(t)
	var out orderLink
	decodeData(t, do(t, app, http.MethodGet, "/api/admin/order-link", withAdmin), &out)
	if out.URL != "" || out.QRSVG != "" {
		t.Fatalf("a store with no address answers %+v, want nothing", out)
	}
	if _, err := app.db.ExecContext(context.Background(),
		`INSERT INTO store_domains (host, store_id, verified_at, is_primary) VALUES ('rasoi.example.in', $1, now(), true)`, app.DefaultStore()); err != nil {
		t.Fatal(err)
	}
	decodeData(t, do(t, app, http.MethodGet, "/api/admin/order-link", withAdmin), &out)
	if out.URL != "https://rasoi.example.in/order" || !strings.HasPrefix(out.QRSVG, "<svg ") {
		t.Fatalf("order link = %q with a QR of %d bytes", out.URL, len(out.QRSVG))
	}
}
