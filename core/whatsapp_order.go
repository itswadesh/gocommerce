package gocommerce

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// whatsappProvider is "Order on WhatsApp" (D255): the shopper places the order
// and then sends it to the shop on WhatsApp, where the two settle payment the
// way a small shop does — UPI, cash at the door, cash at the counter.
//
// Like cash on delivery it needs no third party, so it lives in core, and the
// order is confirmed the same way: the sale is happening and the stock leaves
// the shelf, while payment stays pending until an operator marks it paid. The
// message is written here rather than by each storefront, so every storefront
// sends the shop the same order in the order's own figures.
type whatsappProvider struct{ app *App }

const (
	// CodeWhatsApp is the payment code of the built-in Order on WhatsApp method.
	CodeWhatsApp = "whatsapp"
	// WhatsAppPluginKey is the built-in plugin that switches it on for a store.
	WhatsAppPluginKey = "payments-whatsapp"
)

func (whatsappProvider) Code() string        { return CodeWhatsApp }
func (whatsappProvider) DisplayName() string { return "Order on WhatsApp" }
func (whatsappProvider) pluginKey() string   { return WhatsAppPluginKey }

// Configured is the switch and a number to send to. Without a number the
// shopper would land in WhatsApp with nobody to message, so a store that
// switched it on before saying where orders go offers it to nobody.
func (p whatsappProvider) Configured(ctx context.Context) bool {
	on, err := p.app.plugins.Enabled(ctx, WhatsAppPluginKey)
	if err != nil || !on {
		return false
	}
	number, _ := p.target(ctx)
	return number != ""
}

// Initiate answers with the wa.me link that opens the chat with the order
// already written; the shopper only taps Send. The order stands without it.
func (p whatsappProvider) Initiate(ctx context.Context, o *Order, _ PayOptions) (PaymentIntent, error) {
	intent := PaymentIntent{Kind: IntentNone, Provider: CodeWhatsApp}
	if number, shop := p.target(ctx); number != "" {
		intent.ClientData = map[string]string{"whatsapp_url": whatsappLink(number, whatsappOrderText(shop, o))}
	}
	return intent, nil
}

// target is the number orders go to, as wa.me takes it, and the shop's name to
// greet: the method's own number, else the phone in Store details.
func (p whatsappProvider) target(ctx context.Context) (number, shop string) {
	phone := p.app.plugins.String(ctx, WhatsAppPluginKey, "phone")
	country := ""
	if prof, err := p.app.profile.Get(ctx); err == nil {
		shop, country = prof.Name, prof.Country
		if strings.TrimSpace(phone) == "" {
			phone = prof.Phone
		}
	}
	return whatsappDigits(phone, country), shop
}

// whatsappDigits is a phone number as wa.me takes it: the country code and the
// number, digits only. A bare ten-digit number, or one with a trunk 0, in an
// Indian store gets India's code, which is how a shop in India writes its own
// number. Anything else has to carry its code (+44 …, 0044 …). Empty when
// what is left cannot be a phone number.
func whatsappDigits(phone, country string) string {
	phone = strings.TrimSpace(phone)
	var b strings.Builder
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	indian := strings.EqualFold(country, "IN")
	switch {
	case strings.HasPrefix(phone, "+"):
	case strings.HasPrefix(d, "00"):
		d = d[2:]
	case indian && len(d) == 11 && d[0] == '0':
		d = "91" + d[1:]
	case indian && len(d) == 10:
		d = "91" + d
	}
	if len(d) < 8 || len(d) > 15 || d[0] == '0' {
		return ""
	}
	return d
}

// whatsappLink opens a chat with number, text typed in. Spaces are %20, not +:
// WhatsApp reads a + in the text as a plus sign.
func whatsappLink(number, text string) string {
	return "https://wa.me/" + number + "?text=" + strings.ReplaceAll(url.QueryEscape(text), "+", "%20")
}

// whatsappOrderText is the message the shopper sends: what the shop needs to
// pack the order and get it to them, in the order's own figures.
func whatsappOrderText(shop string, o *Order) string {
	var b strings.Builder
	hello := "Hello"
	if shop != "" {
		hello += " " + shop
	}
	fmt.Fprintf(&b, "%s, I have placed order %s.\n\n", hello, o.Number)
	for _, l := range o.Lines {
		title := l.Title
		if l.VariantLabel != "" {
			title += " (" + l.VariantLabel + ")"
		}
		fmt.Fprintf(&b, "%d × %s: %s\n", l.Quantity, title, messageMoney(l.Total))
	}
	b.WriteString("\n")
	if o.Shipping.AmountMinor > 0 {
		fmt.Fprintf(&b, "Delivery: %s\n", messageMoney(o.Shipping))
	}
	if o.Discount.AmountMinor > 0 {
		fmt.Fprintf(&b, "Discount: −%s\n", messageMoney(o.Discount))
	}
	if o.Tax.AmountMinor > 0 && !o.TaxInclusive {
		fmt.Fprintf(&b, "Tax: %s\n", messageMoney(o.Tax))
	}
	if o.GiftCard.AmountMinor > 0 {
		fmt.Fprintf(&b, "Gift card: −%s\n", messageMoney(o.GiftCard))
	}
	fmt.Fprintf(&b, "Total: %s\n", messageMoney(o.Total))

	name, phone := firstOf(o.Name, o.Address.Name), firstOf(o.Phone, o.Address.Phone)
	if name != "" || phone != "" || o.Address.Line1 != "" {
		b.WriteString("\n")
	}
	if name != "" {
		fmt.Fprintf(&b, "Name: %s\n", name)
	}
	if phone != "" {
		fmt.Fprintf(&b, "Phone: %s\n", phone)
	}
	if o.Address.Line1 != "" {
		place := strings.TrimSpace(strings.Join(nonEmpty(o.Address.City, o.Address.State), ", ") + " " + o.Address.PostalCode)
		fmt.Fprintf(&b, "Deliver to: %s\n", strings.Join(nonEmpty(o.Address.Line1, o.Address.Line2, place), ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// messageMoney is FormatMoney without a whole amount's ".00": "₹450" reads as
// a price in a chat, "₹450.00" as an invoice.
func messageMoney(m Money) string {
	return strings.TrimSuffix(FormatMoney(m.AmountMinor, m.Currency), ".00")
}

func firstOf(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func nonEmpty(values ...string) []string {
	var out []string
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// ------------------------------------------------------------------- routes

// orderLink is the store's quick-order page and a QR code of it, for the
// counter and the WhatsApp status. URL and QR are empty while the store has
// no address shoppers can reach.
type orderLink struct {
	URL   string `json:"url"`
	QRSVG string `json:"qr_svg"`
}

func (a *App) mountOrderLinkRoutes() {
	// plugins.read, which the Payments screen it is shown on already needs.
	a.HandleAdminFunc("GET /api/admin/order-link", a.handleOrderLink, RightPluginsRead)
}

func (a *App) handleOrderLink(w http.ResponseWriter, r *http.Request) {
	out := orderLink{}
	if base := a.StorefrontURL(r.Context()); base != "" {
		out.URL = base + "/order"
		svg, err := QRCodeSVG(out.URL)
		if err != nil {
			RespondError(w, r, err)
			return
		}
		out.QRSVG = svg
	}
	Respond(w, http.StatusOK, out)
}
