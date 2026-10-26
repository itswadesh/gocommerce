package cartrecovery

import gocommerce "github.com/itswadesh/gocommerce/core"

// The messages a step can send, registered with the engine (D58) so their
// wording is the store's to edit on Notifications › Setup Email, and what that
// screen shows is what goes out. Three, escalating, because that is the shape
// of every recovery sequence worth copying: a reminder, a nudge, a last call.
// A step names one by its event; two steps may name the same one.
const (
	templateFirst  = "cart.recovery.1"
	templateSecond = "cart.recovery.2"
	templateLast   = "cart.recovery.3"
)

var templateEvents = []string{templateFirst, templateSecond, templateLast}

// templateVariables are the keys data() fills. recovery_url is absent when no
// storefront address is configured, and the defaults branch on it the way the
// password-reset mail branches on reset_url; customer_first_name, store_name
// and store_email are absent when nobody has given them.
var templateVariables = []string{
	"customer_first_name", "items", "cart_total", "recovery_url", "cart_token",
	"customer_email", "item_count", "summary", "subtotal_minor", "currency",
	"discount_code", "store_name", "store_email", "step",
}

// recoveryBody is the shape the three share, taken from reminders a live
// store already sends: the shopper by name, why we are writing, the items
// with their prices and the total, one way back, and somebody to ask.
func recoveryBody(intro, action string) string {
	return `Hi{{with .customer_first_name}} {{.}}{{end}},

` + intro + `

{{.items}}

Total: {{.cart_total}}

{{if .recovery_url}}` + action + `: {{.recovery_url}}{{else}}Your cart reference is {{.cart_token}}.{{end}}
{{if .discount_code}}
Your code {{.discount_code}} is still applied.
{{end}}
Questions? Reply to this email{{with .store_email}} or contact us at {{.}}{{end}}.
{{with .store_name}}
{{.}}{{end}}`
}

func (m *Module) registerTemplates(app *gocommerce.App) {
	app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
		Channel: gocommerce.ChannelEmail, Event: templateFirst,
		Title:       "Checkout recovery #1",
		Description: "The first reminder about a cart somebody left, sent by the recovery automation.",
		Variables:   templateVariables,
		Subject:     "You left something in your cart",
		Body: recoveryBody(
			"You left these in your cart. They're saved, and you can pick up right where you left off.",
			"Complete your order"),
	})
	app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
		Channel: gocommerce.ChannelEmail, Event: templateSecond,
		Title:       "Checkout recovery #2",
		Description: "A second reminder, for a cart the first one did not bring back.",
		Variables:   templateVariables,
		Subject:     "Still thinking it over?",
		Body: recoveryBody(
			"Your cart is still saved. If anything is holding you back, a question about sizing, delivery or one of these items, just reply to this email and we'll help.",
			"Return to your cart"),
	})
	app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
		Channel: gocommerce.ChannelEmail, Event: templateLast,
		Title:       "Checkout recovery #3",
		Description: "The last reminder in the sequence. Nothing is sent about this cart after it.",
		Variables:   templateVariables,
		Subject:     "Last chance to complete your order",
		Body: recoveryBody(
			"This is our last reminder about the items in your cart. Stock and prices change, so we can't promise they'll be waiting much longer.",
			"Complete your order"),
	})
}
