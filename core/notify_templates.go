package gocommerce

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"text/template"
	"time"
)

// The wording of what the store sends.
//
// Until now every notifier module carried its own subjects and bodies, and
// changing "Thanks for your order" meant a Go map and a redeploy. The words
// are the store's, not the transport's: the same "your order has shipped"
// goes out whether SendGrid or Postmark carries it, and the person who wants
// to change it is the one who runs the shop. So the catalogue of events and
// their default wording lives here, a module registers the events it adds,
// an operator edits any of them from the panel, and a notifier asks for the
// effective text at send time (D58).
//
// The engine's own events are registered at boot; a module adds its own from
// Register, the way it adds a plugin. A stored row overrides the default and
// a delete restores it — the default is code, so it can never be lost.
//
// Each language a store sells in has its own wording (D87). A message in a
// language nobody has worded goes out in the main language's wording, then in
// the default: a Hindi-speaking customer of a store that has not written its
// Hindi yet gets what every other customer gets, not nothing. A row stored
// before languages existed has an empty language and stands for the main
// language until the main language's own wording replaces it.

// NotifyTemplate is the wording of one message on one channel.
type NotifyTemplate struct {
	Channel string `json:"channel"`
	Event   string `json:"event"`
	// Title is what the message is called on the screen ("Order shipped");
	// Description says when it goes out.
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	// Subject is an email's subject line; SMS ignores it. Both are Go
	// text/template over the event's flat string data.
	Subject string `json:"subject"`
	Body    string `json:"body"`
	// Variables names the data keys the event carries, for the editor's
	// hint. Documentation only — a template may name any key.
	Variables []string `json:"variables,omitempty"`
	// Language is what this wording was asked for in: one of the store's
	// languages.
	Language string `json:"language"`
	// Source says whose wording this is: "language" when Language has its
	// own, "main" when it borrows the main language's, "default" when nobody
	// has worded it and the engine's own stands.
	Source string `json:"source"`
	// Customized is whether Language has wording of its own stored, which is
	// what a reset removes.
	Customized bool       `json:"customized"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
}

// Where a message's wording comes from.
const (
	WordingOwn     = "language"
	WordingMain    = "main"
	WordingDefault = "default"
)

// migration0066NotifyTemplateLanguages gives each stored wording its language.
// The rows already there keep an empty language — written before languages,
// read as the main language's — rather than being stamped with a language a
// migration cannot know: the main language is a store's own, and the install
// default is only what a store that never chose one has.
const migration0066NotifyTemplateLanguages = `
ALTER TABLE notification_templates ADD COLUMN language text NOT NULL DEFAULT '';
ALTER TABLE notification_templates DROP CONSTRAINT notification_templates_pkey,
    ADD CONSTRAINT notification_templates_pkey PRIMARY KEY (store_id, channel, event, language);
`

func templateKey(channel, event string) string { return channel + "/" + event }

// NotifyTemplates is the catalogue and the overrides.
type NotifyTemplates struct {
	app   *App
	mu    sync.RWMutex
	defs  map[string]NotifyTemplate
	order []string
}

// NotifyTemplates returns the wording of the store's messages.
func (a *App) NotifyTemplates() *NotifyTemplates { return a.notifyTemplates }

func newNotifyTemplates(a *App) *NotifyTemplates {
	t := &NotifyTemplates{app: a, defs: map[string]NotifyTemplate{}}
	for _, def := range coreNotifyTemplates {
		if err := t.register(def); err != nil {
			// The core catalogue is a literal in this file; a bad entry is a
			// programming error, and the first test to build an App says so.
			panic("core notification template: " + err.Error())
		}
	}
	return t
}

// register adds a default. The key must be new: two owners of one message
// would have no obviously right winner, so the second is refused.
func (t *NotifyTemplates) register(def NotifyTemplate) error {
	switch def.Channel {
	case ChannelEmail, ChannelSMS:
	default:
		return fmt.Errorf("template %q: unknown channel %q", def.Event, def.Channel)
	}
	if strings.TrimSpace(def.Event) == "" || strings.TrimSpace(def.Title) == "" {
		return fmt.Errorf("a template needs an event and a title")
	}
	if err := validateNotifyText(def.Channel, def.Subject, def.Body); err != nil {
		return fmt.Errorf("template %s/%s: %w", def.Channel, def.Event, err)
	}
	def.Customized = false
	def.UpdatedAt = nil
	key := templateKey(def.Channel, def.Event)
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.defs[key]; ok {
		return fmt.Errorf("template %s/%s is already registered", def.Channel, def.Event)
	}
	t.defs[key] = def
	t.order = append(t.order, key)
	return nil
}

func (t *NotifyTemplates) def(channel, event string) (NotifyTemplate, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	def, ok := t.defs[templateKey(channel, event)]
	return def, ok
}

// title is what the notification log calls an event, "" for one nobody
// registered wording for.
func (t *NotifyTemplates) title(channel, event string) string {
	def, ok := t.def(channel, event)
	if !ok {
		return ""
	}
	return def.Title
}

// validateNotifyText parses both texts, so a typo in {{.order_nmber}} is
// refused at save time rather than discovered on the first sale.
func validateNotifyText(channel, subject, body string) error {
	if strings.TrimSpace(body) == "" {
		return Validationf("the body cannot be empty")
	}
	if channel == ChannelEmail && strings.TrimSpace(subject) == "" {
		return Validationf("an email needs a subject")
	}
	if _, err := template.New("subject").Parse(subject); err != nil {
		return Validationf("subject: %v", err)
	}
	if _, err := template.New("body").Parse(body); err != nil {
		return Validationf("body: %v", err)
	}
	return nil
}

// RenderNotifyText fills one template with an event's data. Exported for
// notifier modules, which own the sending and not the wording.
func RenderNotifyText(text string, data map[string]string) (string, error) {
	tpl, err := template.New("t").Parse(text)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

type notifyTemplateRow struct {
	subject, body string
	updatedAt     time.Time
	source        string
}

// rows is the stored wording that answers for lang, per message: lang's own,
// else the main language's, else a row from before languages — read as the
// main language's own when lang is the main language.
func (t *NotifyTemplates) rows(ctx context.Context, channel, event, lang, main string) (map[string]notifyTemplateRow, error) {
	rows, err := t.app.db.QueryContext(ctx, `
		SELECT channel, event, language, subject, body, updated_at FROM notification_templates
		WHERE ($1 = '' OR channel = $1) AND ($2 = '' OR event = $2) AND language IN ($3, $4, '')`,
		channel, event, lang, main)
	if err != nil {
		return nil, Internalf(err, "read notification templates")
	}
	defer rows.Close()
	rank := func(language string) int {
		switch language {
		case lang:
			return 0
		case main:
			return 1
		}
		return 2
	}
	best := map[string]int{}
	out := map[string]notifyTemplateRow{}
	for rows.Next() {
		var ch, ev, language string
		var r notifyTemplateRow
		if err := rows.Scan(&ch, &ev, &language, &r.subject, &r.body, &r.updatedAt); err != nil {
			return nil, Internalf(err, "scan notification template")
		}
		key := templateKey(ch, ev)
		if have, ok := best[key]; ok && have <= rank(language) {
			continue
		}
		best[key] = rank(language)
		r.source = WordingMain
		if language == lang || (language == "" && lang == main) {
			r.source = WordingOwn
		}
		out[key] = r
	}
	return out, rows.Err()
}

func overlay(def NotifyTemplate, lang string, r notifyTemplateRow, ok bool) NotifyTemplate {
	def.Language, def.Source = lang, WordingDefault
	if !ok {
		return def
	}
	def.Subject, def.Body, def.Source = r.subject, r.body, r.source
	def.Customized = r.source == WordingOwn
	at := r.updatedAt
	def.UpdatedAt = &at
	return def
}

// languageOf is tag as the store spells it, or its main language for "" —
// refused when the store does not sell in it, since wording in a language no
// customer is written to in is never sent.
func (t *NotifyTemplates) languageOf(ctx context.Context, tag string) (lang, main string, err error) {
	langs := t.app.Languages(ctx)
	if strings.TrimSpace(tag) == "" {
		return langs[0], langs[0], nil
	}
	for _, l := range langs {
		if strings.EqualFold(l, strings.TrimSpace(tag)) {
			return l, langs[0], nil
		}
	}
	return "", "", Validationf("this store does not sell in %q; add it on Store settings first", tag)
}

// List is every message on a channel ("" for all), in registration order,
// each with its effective wording in the store's main language.
func (t *NotifyTemplates) List(ctx context.Context, channel string) ([]NotifyTemplate, error) {
	return t.ListIn(ctx, channel, "")
}

// ListIn is List in one of the store's languages ("" for the main one).
func (t *NotifyTemplates) ListIn(ctx context.Context, channel, language string) ([]NotifyTemplate, error) {
	lang, main, err := t.languageOf(ctx, language)
	if err != nil {
		return nil, err
	}
	stored, err := t.rows(ctx, channel, "", lang, main)
	if err != nil {
		return nil, err
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := []NotifyTemplate{}
	for _, key := range t.order {
		def := t.defs[key]
		if channel != "" && def.Channel != channel {
			continue
		}
		r, ok := stored[key]
		out = append(out, overlay(def, lang, r, ok))
	}
	return out, nil
}

// Get is one message's effective wording in the store's main language; false
// when nobody registered the event on that channel, which a notifier reads as
// "nothing to say".
func (t *NotifyTemplates) Get(ctx context.Context, channel, event string) (NotifyTemplate, bool, error) {
	return t.GetIn(ctx, channel, event, "")
}

// GetIn is Get in a language — the one a notification is for. A language the
// store does not sell in, which an old order may carry, is written to in the
// main language rather than refused: the message still has to go.
func (t *NotifyTemplates) GetIn(ctx context.Context, channel, event, language string) (NotifyTemplate, bool, error) {
	def, ok := t.def(channel, event)
	if !ok {
		return NotifyTemplate{}, false, nil
	}
	lang, main, err := t.languageOf(ctx, language)
	if err != nil {
		if lang, main, err = t.languageOf(ctx, ""); err != nil {
			return NotifyTemplate{}, false, err
		}
	}
	stored, err := t.rows(ctx, channel, event, lang, main)
	if err != nil {
		return NotifyTemplate{}, false, err
	}
	r, found := stored[templateKey(channel, event)]
	return overlay(def, lang, r, found), true, nil
}

// Render is the effective wording, in the store's main language, filled with
// an event's data. ok is false for an event with no wording.
func (t *NotifyTemplates) Render(ctx context.Context, channel, event string, data map[string]string) (subject, body string, ok bool, err error) {
	tpl, ok, err := t.Get(ctx, channel, event)
	if err != nil || !ok {
		return "", "", ok, err
	}
	if subject, err = RenderNotifyText(tpl.Subject, data); err != nil {
		return "", "", true, fmt.Errorf("render %s/%s subject: %w", channel, event, err)
	}
	if body, err = RenderNotifyText(tpl.Body, data); err != nil {
		return "", "", true, fmt.Errorf("render %s/%s body: %w", channel, event, err)
	}
	return subject, body, true, nil
}

// Set stores an operator's wording for one message in the main language.
func (t *NotifyTemplates) Set(ctx context.Context, channel, event, subject, body string) (NotifyTemplate, error) {
	return t.SetIn(ctx, channel, event, "", subject, body)
}

// SetIn stores an operator's wording for one message in one of the store's
// languages ("" for the main one). The main language's wording replaces a row
// from before languages, so one wording is stored for it.
func (t *NotifyTemplates) SetIn(ctx context.Context, channel, event, language, subject, body string) (NotifyTemplate, error) {
	def, ok := t.def(channel, event)
	if !ok {
		return NotifyTemplate{}, NotFoundf("no %s message is sent for %q", channel, event)
	}
	if err := validateNotifyText(channel, subject, body); err != nil {
		return NotifyTemplate{}, err
	}
	lang, main, err := t.languageOf(ctx, language)
	if err != nil {
		return NotifyTemplate{}, err
	}
	var out NotifyTemplate
	err = InTx(ctx, t.app.db, func(tx *sql.Tx) error {
		var at time.Time
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO notification_templates (channel, event, language, subject, body, updated_at)
			VALUES ($1, $2, $3, $4, $5, now())
			ON CONFLICT (store_id, channel, event, language) DO UPDATE SET subject = EXCLUDED.subject, body = EXCLUDED.body, updated_at = now()
			RETURNING updated_at`, channel, event, lang, subject, body).Scan(&at); err != nil {
			return Internalf(err, "store notification template")
		}
		if lang == main {
			if _, err := tx.ExecContext(ctx, `DELETE FROM notification_templates
				WHERE channel = $1 AND event = $2 AND language = ''`, channel, event); err != nil {
				return Internalf(err, "replace notification template")
			}
		}
		out = overlay(def, lang, notifyTemplateRow{subject: subject, body: body, updatedAt: at, source: WordingOwn}, true)
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditNotificationTemplateUpdate, Entity: AuditEntityNotificationTemplate,
			Key: templateKey(channel, event), Label: def.Title,
			Summary: "Edited the " + def.Title + " " + channel + inLanguage(lang, main),
			After:   map[string]any{"language": lang, "subject": subject, "body": body},
		})
	})
	return out, err
}

// Reset drops the operator's wording in the main language and restores the
// default.
func (t *NotifyTemplates) Reset(ctx context.Context, channel, event string) (NotifyTemplate, error) {
	return t.ResetIn(ctx, channel, event, "")
}

// ResetIn drops the operator's wording in one language and answers what that
// language now gets: the main language's wording, or the default.
func (t *NotifyTemplates) ResetIn(ctx context.Context, channel, event, language string) (NotifyTemplate, error) {
	def, ok := t.def(channel, event)
	if !ok {
		return NotifyTemplate{}, NotFoundf("no %s message is sent for %q", channel, event)
	}
	lang, main, err := t.languageOf(ctx, language)
	if err != nil {
		return NotifyTemplate{}, err
	}
	err = InTx(ctx, t.app.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM notification_templates
			WHERE channel = $1 AND event = $2 AND (language = $3 OR ($4 AND language = ''))`,
			channel, event, lang, lang == main)
		if err != nil {
			return Internalf(err, "reset notification template")
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Nothing was stored: the default already stands, and an audit
			// row saying it was restored would be recording a non-event.
			return nil
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditNotificationTemplateUpdate, Entity: AuditEntityNotificationTemplate,
			Key: templateKey(channel, event), Label: def.Title,
			Summary: "Restored the default " + def.Title + " " + channel + inLanguage(lang, main),
			After:   map[string]any{"language": lang, "subject": def.Subject, "body": def.Body},
		})
	})
	if err != nil {
		return NotifyTemplate{}, err
	}
	out, _, err := t.GetIn(ctx, channel, event, lang)
	return out, err
}

// keepOldWording labels wording stored before languages with the main language
// it was written in, before that language stops being the main one. An empty
// language reads as whichever language is main, so without this the old
// wording would follow the move into a language it is not written in — and
// wording the new main language would then replace it. A row the old main
// language has already superseded is dropped instead.
func (t *NotifyTemplates) keepOldWording(ctx context.Context, tx *sql.Tx, oldMain string) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM notification_templates l
		WHERE l.language = '' AND EXISTS (
		    SELECT 1 FROM notification_templates o
		    WHERE o.store_id = l.store_id AND o.channel = l.channel AND o.event = l.event AND o.language = $1)`,
		oldMain); err != nil {
		return Internalf(err, "keep the old wording")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notification_templates SET language = $1 WHERE language = ''`, oldMain); err != nil {
		return Internalf(err, "keep the old wording")
	}
	return nil
}

// inLanguage names a language other than the main one in an audit summary.
func inLanguage(lang, main string) string {
	if lang == main {
		return ""
	}
	return " in " + lang
}

// The engine's own messages. Order events carry the keys orderNotificationData
// builds; the operator reset carries its own vocabulary on purpose (see
// Superusers.deliverReset). Amounts are minor units, which is what the engine
// hands every notifier — how a currency is written is the reader's business,
// and a store that wants them formatted edits the template.
var orderVariables = []string{
	"order_number", "customer_name", "customer_email", "items_summary", "item_count",
	"total_minor", "currency", "order_status", "payment_status", "payment_method",
}

func withVariables(extra ...string) []string {
	return append(append([]string{}, orderVariables...), extra...)
}

var coreNotifyTemplates = []NotifyTemplate{
	{
		Channel: ChannelEmail, Event: EventOrderCreated, Title: "Order placed",
		Description: "To the shopper when their order is placed.",
		Variables:   orderVariables,
		Subject:     "Order {{.order_number}} confirmed",
		Body: `Hello {{.customer_name}},

Thanks for your order {{.order_number}}.

{{.items_summary}}

We'll email you again when it ships.`,
	},
	{
		Channel: ChannelEmail, Event: EventOrderPaid, Title: "Payment received",
		Description: "To the shopper when their payment is confirmed.",
		Variables:   orderVariables,
		Subject:     "Payment received for order {{.order_number}}",
		Body: `Hello {{.customer_name}},

We've received your payment for order {{.order_number}}.

{{.items_summary}}`,
	},
	{
		Channel: ChannelEmail, Event: EventOrderShipped, Title: "Order shipped",
		Description: "To the shopper when a parcel leaves, with its tracking number.",
		Variables:   withVariables("tracking", "shipment_items_summary", "shipment_is_partial"),
		Subject:     "Order {{.order_number}} is on its way",
		// The parcel's own contents when the event carries them, and the
		// whole order otherwise — which is what an event written before
		// shipments named their contents, and redelivered since, still says.
		Body: `Hello {{.customer_name}},

Order {{.order_number}} has shipped.
{{if .tracking}}Tracking number: {{.tracking}}{{end}}

{{if .shipment_items_summary}}{{.shipment_items_summary}}{{else}}{{.items_summary}}{{end}}
{{if .shipment_is_partial}}The rest of your order will follow separately.{{end}}`,
	},
	{
		Channel: ChannelEmail, Event: EventOrderDelivered, Title: "Order delivered",
		Description: "To the shopper when the order is marked delivered.",
		Variables:   orderVariables,
		Subject:     "Order {{.order_number}} was delivered",
		Body: `Hello {{.customer_name}},

Order {{.order_number}} has been delivered. We hope you like it.`,
	},
	{
		Channel: ChannelEmail, Event: EventOrderCancelled, Title: "Order cancelled",
		Description: "To the shopper when their order is cancelled.",
		Variables:   withVariables("reason"),
		Subject:     "Order {{.order_number}} was cancelled",
		Body: `Hello {{.customer_name}},

Order {{.order_number}} has been cancelled.
{{if .reason}}Reason: {{.reason}}{{end}}`,
	},
	{
		Channel: ChannelEmail, Event: EventOrderRefunded, Title: "Refund issued",
		Description: "To the shopper when money goes back, in full or in part.",
		Variables:   withVariables("refund_amount_minor", "refunded_minor", "refund_remaining_minor", "reason"),
		Subject:     "Refund issued for order {{.order_number}}",
		Body: `Hello {{.customer_name}},

We have refunded {{.refund_amount_minor}} ({{.currency}}) on order {{.order_number}}.
{{if .reason}}Reason: {{.reason}}{{end}}
{{if ne .refund_remaining_minor "0"}}The rest of the order stands.{{end}}`,
	},
	{
		Channel: ChannelEmail, Event: EventOrderReturned, Title: "Return received",
		Description: "To the shopper when their parcel comes back. The money is a separate message, because it is a separate decision.",
		Variables:   withVariables("return_units", "return_status", "reason"),
		Subject:     "We have your return from order {{.order_number}}",
		Body: `Hello {{.customer_name}},

We have received your return of {{.return_units}} item(s) from order {{.order_number}}.
{{if .reason}}Reason: {{.reason}}{{end}}

We will be in touch about anything owed to you.`,
	},
	{
		Channel: ChannelEmail, Event: EventSuperuserInvitation, Title: "Team invitation",
		Description: "To somebody invited to join the store's team. The link works for two days.",
		Variables:   []string{"invite_url", "store_name", "role", "invited_by", "expires_in_hours"},
		Subject:     "Join {{if .store_name}}{{.store_name}}{{else}}the team{{end}} on GoCommerce",
		Body: `{{if .invited_by}}{{.invited_by}} has invited you{{else}}You have been invited{{end}} to join {{if .store_name}}{{.store_name}}{{else}}the team{{end}} as {{.role}}.

Accept within {{.expires_in_hours}} hours:

{{.invite_url}}

If you were not expecting this, you can ignore it; nothing happens unless the link is opened.`,
	},
	{
		Channel: ChannelEmail, Event: EventSuperuserPasswordReset, Title: "Operator password reset",
		Description: "To a member of the team who asked to reset their panel password.",
		Variables:   []string{"operator_email", "reset_url", "reset_token", "expires_in_minutes"},
		Subject:     "Reset your GoCommerce password",
		// The absence of reset_url is meaningful: without a panel URL the
		// engine cannot build a link, and the code stands in for it.
		Body: `Somebody asked to reset the password for {{.operator_email}}. If that was you,
{{if .reset_url}}open this link within {{.expires_in_minutes}} minutes:

{{.reset_url}}{{else}}use this code within {{.expires_in_minutes}} minutes:

{{.reset_token}}{{end}}

If it was not you, nothing has changed and you can ignore this message.`,
	},

	{
		Channel: ChannelEmail, Event: EventNotificationTest, Title: "Test email",
		Description: "Sent from Setup Email to check that the provider delivers.",
		Variables:   []string{"store_name", "to"},
		Subject:     "Test email from {{if .store_name}}{{.store_name}}{{else}}your store{{end}}",
		Body: `This is a test email from {{if .store_name}}{{.store_name}}{{else}}your store{{end}}, sent to {{.to}}.

If you are reading it, the store's email provider is set up and order emails will arrive.`,
	},

	// SMS is the same news in one line. A provider that sends through
	// pre-approved templates of its own (MSG91's DLT flows) ignores these
	// bodies and takes its template ids from its plugin settings instead.
	{
		Channel: ChannelSMS, Event: EventOrderCreated, Title: "Order placed",
		Description: "To the shopper's phone when their order is placed.",
		Variables:   orderVariables,
		Body:        `Thanks for your order {{.order_number}}. We'll text you when it ships.`,
	},
	{
		Channel: ChannelSMS, Event: EventOrderShipped, Title: "Order shipped",
		Description: "To the shopper's phone when a parcel leaves.",
		Variables:   withVariables("tracking"),
		Body:        `Order {{.order_number}} has shipped.{{if .tracking}} Tracking: {{.tracking}}{{end}}`,
	},
	{
		Channel: ChannelSMS, Event: EventOrderDelivered, Title: "Order delivered",
		Description: "To the shopper's phone when the order is marked delivered.",
		Variables:   orderVariables,
		Body:        `Order {{.order_number}} has been delivered. Thank you!`,
	},
	{
		Channel: ChannelSMS, Event: EventOrderCancelled, Title: "Order cancelled",
		Description: "To the shopper's phone when their order is cancelled.",
		Variables:   withVariables("reason"),
		Body:        `Order {{.order_number}} has been cancelled.{{if .reason}} Reason: {{.reason}}{{end}}`,
	},
}
