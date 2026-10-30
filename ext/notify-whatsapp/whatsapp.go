// Package whatsapp sends a store's order updates and basket reminders as
// WhatsApp messages, through Meta's WhatsApp Business Platform Cloud API
// (D165). Each store connects its own WhatsApp Business account: a phone
// number ID, the account (WABA) ID and a system user's permanent access token,
// sealed like every plugin secret (D110). Meta, not this module, is paid for
// what is sent.
//
// Outside a conversation the shopper opened, WhatsApp delivers only templates
// Meta approved, so every message here is a template the store chose on
// Notifications › Setup WhatsApp for one of a few moments ("slots"):
//
//	order_confirmation   order.created
//	cod_confirmation     order.created, cash on delivery: Confirm / Cancel buttons
//	shipped              order.shipped
//	out_for_delivery     order.shipment_updated, the carrier saying out for delivery
//	delivered            order.delivered
//	cart_reminder        a cart-recovery step that sends WhatsApp (marketing)
//
// Nothing is sent to a number without its owner's consent, recorded by the
// storefront's opt-in boxes (consent.go): order updates and offers separately,
// as Meta advises, and a STOP reply ends both. The engine offers each order
// message on ChannelWhatsApp; Serves answers whether this one may go, and
// Notify queues it. A worker sends (deliver.go), as ext/webhooks does, so Meta
// being down never holds up a sale or the outbox. Meta's webhooks (webhook.go)
// report each message's fate and the shopper's replies onto the order's
// history.
package whatsapp

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	// PluginKey is where the panel keeps the account's settings.
	PluginKey = "whatsapp-cloud"

	defaultBaseURL = "https://graph.facebook.com"
	// graphVersion is the Graph API version this module was written against,
	// the one ext/meta-capi uses.
	graphVersion = "v26.0"
)

// Config is what a store's main() can set; everything a store chooses is on
// its Setup WhatsApp screen.
type Config struct {
	// BaseURL overrides Meta's Graph API, for tests.
	BaseURL string
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the WhatsApp channel.
type Module struct {
	cfg    Config
	app    *gocommerce.App
	db     *sql.DB
	log    *slog.Logger
	client *http.Client
	poll   time.Duration
	wake   chan struct{}
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg, wake: make(chan struct{}, 1)} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "notify-whatsapp" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "WhatsApp" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_whatsapp",
		SQL: `
-- One row per number the store may write to, and what its owner said yes to.
-- The two kinds are separate because Meta prices and polices them separately:
-- order updates are utility messages, basket reminders and offers marketing.
-- opted_out_at is a STOP reply, which silences both until START or a fresh
-- answer at checkout. last_inbound_at opens WhatsApp's 24-hour window, the
-- only time a message that is not a template may go.
CREATE TABLE whatsapp_contacts (
    id              bigserial PRIMARY KEY,
    phone           text NOT NULL UNIQUE,
    order_updates   boolean NOT NULL DEFAULT false,
    marketing       boolean NOT NULL DEFAULT false,
    consent_at      timestamptz,
    opted_out_at    timestamptz,
    last_inbound_at timestamptz,
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- Which approved template each moment sends, and which value fills each of
-- its variables.
CREATE TABLE whatsapp_templates (
    id         bigserial PRIMARY KEY,
    slot       text NOT NULL UNIQUE,
    name       text NOT NULL,
    language   text NOT NULL,
    category   text NOT NULL,
    variables  jsonb NOT NULL DEFAULT '[]',
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by text NOT NULL DEFAULT ''
);

-- Every message, from queued to read. payload is the request Meta is sent;
-- the worker's columns are the outbox's: claim a batch, back off, give up
-- visibly. dedupe keeps a redelivered event from sending twice.
CREATE TABLE whatsapp_messages (
    id           bigserial PRIMARY KEY,
    dedupe       text NOT NULL UNIQUE,
    event        text NOT NULL,
    slot         text NOT NULL,
    phone        text NOT NULL,
    order_id     bigint,
    order_number text,
    payload      jsonb NOT NULL,
    status       text NOT NULL DEFAULT 'queued'
                 CHECK (status IN ('queued', 'sent', 'delivered', 'read', 'failed')),
    wamid        text UNIQUE,
    attempts     integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    last_error   text,
    -- A cash-on-delivery confirmation's answer: confirmed, cancelled, or
    -- cancel_requested where the store left cancelling to a person.
    reply        text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX whatsapp_messages_claim ON whatsapp_messages (available_at) WHERE status = 'queued';
CREATE INDEX whatsapp_messages_recent ON whatsapp_messages (id DESC);`,
	},
		gocommerce.ScopeMigration("0002_store_scope", "whatsapp_contacts", "whatsapp_templates", "whatsapp_messages"),
	}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	m.db = app.DB()
	m.log = app.Log()
	if m.cfg.BaseURL == "" {
		m.cfg.BaseURL = defaultBaseURL
	}
	m.cfg.BaseURL = strings.TrimRight(m.cfg.BaseURL, "/")
	// Meta's address is this module's, not one a store typed, so the plain
	// client is right (rule 16).
	m.client = m.cfg.Client
	if m.client == nil {
		m.client = &http.Client{Timeout: 15 * time.Second}
	}
	m.poll = 5 * time.Second

	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "WhatsApp Business", Icon: "ri-whatsapp-line", Category: "notifications",
		Home: "/dash/notifications/whatsapp",
		Description: "Sends order updates, cash-on-delivery confirmations and basket reminders on WhatsApp through Meta's Cloud API, " +
			"from the store's own WhatsApp Business account, to shoppers who opted in. Meta bills the store directly for what is sent.",
		Docs: "https://developers.facebook.com/documentation/business-messaging/whatsapp",
		Fields: []gocommerce.PluginField{
			{Key: "phone_number_id", Label: "Phone number ID", Kind: "text", Required: true,
				Help: "WhatsApp Manager › Phone numbers, or the app's WhatsApp › API Setup page."},
			{Key: "waba_id", Label: "WhatsApp Business Account ID", Kind: "text", Required: true,
				Help: "The account the number belongs to; its approved templates are read from here."},
			{Key: "access_token", Label: "Access token", Kind: "secret", Required: true,
				Help: "A system user's permanent token with whatsapp_business_messaging and whatsapp_business_management."},
			{Key: "app_secret", Label: "App secret", Kind: "secret", Required: true,
				Help: "Your Meta app › App settings › Basic. Proves a webhook came from Meta."},
			{Key: "verify_token", Label: "Webhook verify token", Kind: "secret", Required: true,
				Help: "Any long random text; type the same into the Meta app's webhook setup."},
			{Key: "cod_cancel", Label: "Cancel when the shopper taps Cancel", Kind: "bool", Default: false,
				Help: "Off, a cancel reply flags the order for you to call the shopper; on, it cancels the order."},
		},
	})
	app.RegisterNotifier(gocommerce.ChannelWhatsApp, m)

	app.HandleFunc("GET /x/notify-whatsapp/consent", m.handleConsentState)
	app.HandleFunc("PUT /x/notify-whatsapp/consent", m.handleConsent)
	app.HandleFunc("GET /x/notify-whatsapp/webhook", m.handleVerify)
	app.HandleFunc("POST /x/notify-whatsapp/webhook", m.handleWebhook)

	app.HandleAdminFunc("GET /api/admin/x/notify-whatsapp/status", m.handleStatus, gocommerce.RightNotificationsRead)
	app.HandleAdminFunc("GET /api/admin/x/notify-whatsapp/templates", m.handleTemplates, gocommerce.RightNotificationsRead)
	app.HandleAdminFunc("PUT /api/admin/x/notify-whatsapp/slots/{slot}", m.handleMapSlot, gocommerce.RightNotificationsWrite)
	app.HandleAdminFunc("DELETE /api/admin/x/notify-whatsapp/slots/{slot}", m.handleUnmapSlot, gocommerce.RightNotificationsWrite)
	app.HandleAdminFunc("POST /api/admin/x/notify-whatsapp/slots/{slot}/suggest", m.handleSuggest, gocommerce.RightNotificationsWrite)
	app.HandleAdminFunc("GET /api/admin/x/notify-whatsapp/messages", m.handleMessages, gocommerce.RightNotificationsRead)

	app.OnStart(m.start)
	return nil
}

// account is the store's connection, read fresh each time so a saved setting
// takes effect on the next message.
type account struct {
	on                                bool
	phoneID, wabaID, token, appSecret string
	verifyToken                       string
	codCancel                         bool
}

func (m *Module) account(ctx context.Context) account {
	p := m.app.Plugins()
	on, err := p.Enabled(ctx, PluginKey)
	settings, _ := p.Settings(ctx, PluginKey)
	str := func(k string) string { s, _ := settings[k].(string); return strings.TrimSpace(s) }
	cancel, _ := settings["cod_cancel"].(bool)
	return account{
		on: err == nil && on, phoneID: str("phone_number_id"), wabaID: str("waba_id"),
		token: str("access_token"), appSecret: str("app_secret"), verifyToken: str("verify_token"),
		codCancel: cancel,
	}
}

func (a account) ready() bool { return a.on && a.phoneID != "" && a.wabaID != "" && a.token != "" }

// Configured implements gocommerce.ConfigurableNotifier.
func (m *Module) Configured(ctx context.Context) bool { return m.account(ctx).ready() }

// Serves implements gocommerce.ScopedNotifier: this message has a template
// mapped for its moment, and its recipient said yes to its kind and has not
// said STOP since. Core asks before it offers an order's message, so a store
// whose shoppers never opted in writes no rows for the channel.
func (m *Module) Serves(ctx context.Context, n gocommerce.Notification) bool {
	_, _, ok := m.plan(ctx, n)
	return ok
}

// Notify implements gocommerce.Notifier. It queues and returns: the worker
// talks to Meta, so a slow or failing Meta is never the outbox's problem.
func (m *Module) Notify(ctx context.Context, n gocommerce.Notification) error {
	tpl, phone, ok := m.plan(ctx, n)
	if !ok {
		return nil
	}
	data := m.enrich(ctx, n.Data)
	body := templateMessage(phone, tpl, data)
	if err := m.queue(ctx, n.Event, tpl.Slot, phone, data, body); err != nil {
		return err
	}
	m.nudge()
	return nil
}

// plan is the whole decision to send: the slot, its template, and the
// recipient as E.164, or false.
func (m *Module) plan(ctx context.Context, n gocommerce.Notification) (*mapping, string, bool) {
	if n.Channel != gocommerce.ChannelWhatsApp || !m.account(ctx).ready() {
		return nil, "", false
	}
	phone, ok := m.normalize(ctx, n.To)
	if !ok {
		return nil, "", false
	}
	slot := m.slotFor(ctx, n)
	if slot == nil {
		return nil, "", false
	}
	tpl, err := m.mapping(ctx, slot.Key)
	if err != nil || tpl == nil {
		return nil, "", false
	}
	if !m.consented(ctx, phone, slot.Marketing) {
		return nil, "", false
	}
	return tpl, phone, true
}

func (m *Module) nudge() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}
