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
// password-reset mail branches on reset_url.
var templateVariables = []string{
	"recovery_url", "cart_token", "customer_email", "item_count", "summary",
	"subtotal_minor", "currency", "discount_code", "step",
}

func (m *Module) registerTemplates(app *gocommerce.App) {
	link := `{{if .recovery_url}}Pick up where you left off:
{{.recovery_url}}{{else}}Your basket reference is {{.cart_token}}.{{end}}`

	app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
		Channel: gocommerce.ChannelEmail, Event: templateFirst,
		Title:       "Checkout recovery #1",
		Description: "The first reminder about a basket somebody left, sent by the recovery automation.",
		Variables:   templateVariables,
		Subject:     "You left something in your basket",
		Body: `Hi,

You were nearly there — {{.summary}} is still waiting in your basket.

` + link + `
{{if .discount_code}}
Your code {{.discount_code}} is still applied.
{{end}}
If you have changed your mind, there is nothing to do; we won't keep writing for long.`,
	})
	app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
		Channel: gocommerce.ChannelEmail, Event: templateSecond,
		Title:       "Checkout recovery #2",
		Description: "A second reminder, for a basket the first one did not bring back.",
		Variables:   templateVariables,
		Subject:     "Still thinking it over?",
		Body: `Hi,

Your basket is saved: {{.summary}}.

If something stopped you — a question about sizing, delivery or payment — just reply to this email and we'll help.

` + link,
	})
	app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
		Channel: gocommerce.ChannelEmail, Event: templateLast,
		Title:       "Checkout recovery #3",
		Description: "The last reminder in the sequence. Nothing is sent about this basket after it.",
		Variables:   templateVariables,
		Subject:     "Last chance for the items in your basket",
		Body: `Hi,

This is our last note about your basket: {{.summary}}.

Stock is not held for a basket, so we can't promise it will all still be there later.

` + link,
	})
}
