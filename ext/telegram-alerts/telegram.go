// Package telegramalerts tells a store's owner about its orders on Telegram.
//
// A shop that runs its day from a phone already has Telegram open, and a bot
// writing to its owner costs nothing: no account with a provider, no wording
// registered with a carrier, no price per message. The owner makes a bot with
// @BotFather, sends it a message, picks that chat under Notifications › Setup
// Telegram, and every new order, payment, cancellation and return arrives
// there as it happens.
//
// It is not a notifier. The engine's notifiers write to shoppers, on a channel
// the shopper gave; this writes to the owner about the shop. So it keeps its
// own wording — telegram_alerts holds a row for each event the owner changed —
// and renders it with the engine's RenderNotifyText over the same flat data the
// engine's own templates read, so {{.order_number}} means one thing everywhere.
//
// The sending is ext/webhooks' shape (D51), for the same two reasons. The
// subscriber only writes a row and returns, so a Telegram that takes ten
// seconds to answer never holds the outbox; and a refusal is recorded against
// the alert rather than returned to the outbox, which would re-run every other
// consumer of the event. A worker sends, with backoff and a dead state.
//
// Telegram's address is fixed in code, not typed by an operator, so it is
// called with an ordinary client: D109 is about addresses a store chooses. The
// bot token travels in the URL path, which is how the Bot API is built, so an
// error that quotes the URL — and Go's network errors do — is never logged,
// stored or answered as it stands.
//
//	app, err := gocommerce.New(cfg, telegramalerts.New(telegramalerts.Config{}))
package telegramalerts

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// PluginKey is the plugin the owner switches on and gives the bot and chat.
const PluginKey = "telegram-alerts"

const defaultBaseURL = "https://api.telegram.org"

// Delivery policy: the webhooks module's numbers, which are the outbox's. An
// afternoon of Telegram trouble is the same problem as an afternoon of a
// merchant's server being down, and a second answer would be a second thing
// to remember.
const (
	maxAttempts    = 12
	maxBackoff     = 15 * time.Minute
	visibility     = 60 * time.Second
	deliverBatch   = 20
	pollInterval   = 5 * time.Second
	defaultTimeout = 10 * time.Second
)

// Config is what a store's own main() may tell the module. Every field is
// optional, and the Setup Telegram screen's settings win over the first two.
type Config struct {
	// BotToken and ChatID configure the store in code. With both set the
	// plugin starts switched on (D59); with neither it installs idle.
	BotToken string
	ChatID   string
	// BaseURL replaces https://api.telegram.org. Tests set it; nothing else
	// should.
	BaseURL string
	// Client replaces the HTTP client, and Timeout bounds one call to
	// Telegram when it does not.
	Client  *http.Client
	Timeout time.Duration
}

// Module is the alerts, the queue and the routes.
type Module struct {
	cfg    Config
	app    *gocommerce.App
	db     *sql.DB
	log    *slog.Logger
	client *http.Client
	poll   time.Duration
	// wake asks the worker for a pass now, so an alert queued a moment ago
	// does not wait out the poll interval.
	wake chan struct{}
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg, wake: make(chan struct{}, 1)} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "telegram-alerts" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_alerts",
		SQL: `
-- Only what the owner changed: an event with no row is sent, with the wording
-- in code, so the defaults can never be lost and a new one reaches every store.
CREATE TABLE telegram_alerts (
    id         bigserial PRIMARY KEY,
    event      text NOT NULL UNIQUE,
    template   text NOT NULL,
    active     boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- One row per event, holding the message as it was worded when the event
-- happened. The columns after text are the worker's, and they are the outbox's
-- and webhook_deliveries' by the same names: claim a batch, back off, give up
-- visibly.
CREATE TABLE telegram_alerts_deliveries (
    id           bigserial PRIMARY KEY,
    event_id     text NOT NULL,
    event_name   text NOT NULL,
    order_id     bigint,
    order_number text NOT NULL DEFAULT '',
    text         text NOT NULL,
    attempts     integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    dead         boolean NOT NULL DEFAULT false,
    last_status  integer,
    last_error   text,
    created_at   timestamptz NOT NULL DEFAULT now(),

    -- The outbox delivers at least once; a second delivery of one event must
    -- not become a second message on the owner's phone.
    UNIQUE (event_id)
);

CREATE INDEX telegram_alerts_deliveries_claim
    ON telegram_alerts_deliveries (available_at)
    WHERE delivered_at IS NULL AND NOT dead;`,
	},
		gocommerce.ScopeMigration("0002_store_scope", "telegram_alerts", "telegram_alerts_deliveries"),
	}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	m.db = app.DB()
	m.log = app.Log()
	m.registerRights(app)
	if m.cfg.BaseURL == "" {
		m.cfg.BaseURL = defaultBaseURL
	}
	m.cfg.BaseURL = strings.TrimRight(m.cfg.BaseURL, "/")
	if m.cfg.Timeout <= 0 {
		m.cfg.Timeout = defaultTimeout
	}
	m.client = m.cfg.Client
	if m.client == nil {
		m.client = &http.Client{Timeout: m.cfg.Timeout}
	}
	m.poll = pollInterval

	inCode := strings.TrimSpace(m.cfg.BotToken) != "" && strings.TrimSpace(m.cfg.ChatID) != ""
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "Telegram alerts", Icon: "ri-telegram-line", Category: "notifications", DefaultEnabled: inCode,
		Description: "Sends the owner a Telegram message for every new order, payment, cancellation and return. Free: you make a bot with @BotFather and it writes to you. Set it up, and word the messages, under Notifications › Setup Telegram.",
		Docs:        "https://core.telegram.org/bots/features#botfather",
		Fields: []gocommerce.PluginField{
			{Key: "bot_token", Label: "Bot token", Kind: "secret", Required: !inCode,
				Help: "From @BotFather in Telegram: send it /newbot, and it answers with a token like 123456789:AAE…"},
			{Key: "chat_id", Label: "Chat ID", Kind: "text", Required: !inCode,
				Help: "Where the alerts go: your own chat with the bot, or a group it is in. Setup Telegram finds it after you send the bot a message."},
		},
	})

	for _, def := range catalogue {
		app.Subscribe(def.Event, m.onEvent)
	}

	const base = "/api/admin/x/telegram-alerts"
	app.HandleAdminFunc("GET "+base+"/alerts", m.handleList, rightAlertsRead)
	app.HandleAdminFunc("POST "+base+"/alerts", m.handleCreate, rightAlertsWrite)
	app.HandleAdminFunc("GET "+base+"/alerts/{event}", m.handleGet, rightAlertsRead)
	app.HandleAdminFunc("PATCH "+base+"/alerts/{event}", m.handleUpdate, rightAlertsWrite)
	app.HandleAdminFunc("DELETE "+base+"/alerts/{event}", m.handleReset, rightAlertsWrite)
	app.HandleAdminFunc("POST "+base+"/preview", m.handlePreview, rightAlertsRead)
	// Write, though one only reads and the other only sends: both are setting
	// the bot up, and the chats are the names of whoever has messaged it.
	app.HandleAdminFunc("GET "+base+"/chats", m.handleChats, rightAlertsWrite)
	app.HandleAdminFunc("POST "+base+"/test", m.handleTest, rightAlertsWrite)
	app.HandleAdminFunc("GET "+base+"/deliveries", m.handleDeliveries, rightAlertsRead)
	app.HandleAdminFunc("POST "+base+"/deliveries/{id}/retry", m.handleRetry, rightAlertsWrite)

	app.OnStart(m.start)
	return nil
}

// bot is where one message goes: the panel's settings over Config's.
type bot struct{ token, chat string }

func (m *Module) bot(ctx context.Context) bot {
	str := func(key, fallback string) string {
		if v := strings.TrimSpace(m.app.Plugins().String(ctx, PluginKey, key)); v != "" {
			return v
		}
		return strings.TrimSpace(fallback)
	}
	return bot{token: str("bot_token", m.cfg.BotToken), chat: str("chat_id", m.cfg.ChatID)}
}

func (m *Module) enabled(ctx context.Context) bool {
	on, err := m.app.Plugins().Enabled(ctx, PluginKey)
	return err == nil && on
}

// tokenRE is the shape @BotFather hands out: the bot's number, a colon, and a
// secret of letters, digits, dashes and underscores. Checked before the token
// is put in a URL, because a paste of the wrong thing would otherwise become a
// different path on Telegram's server and an error nobody could read.
var tokenRE = regexp.MustCompile(`^[0-9]{3,}:[A-Za-z0-9_-]{30,}$`)

// The rights this module's routes are gated on (D65). New rather than lifted
// off a core right, so Default widens nobody: the owner carries every right,
// and a manager may see what the owner is told without changing it.
const (
	rightAlertsRead  gocommerce.Right = "alerts.read"
	rightAlertsWrite gocommerce.Right = "alerts.write"
)

func (m *Module) registerRights(app *gocommerce.App) {
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightAlertsRead,
		Label:   "See the Telegram alerts",
		Scope:   "Which order events reach the owner's Telegram, their wording, and whether they arrived",
		Default: []string{gocommerce.RoleManager},
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightAlertsWrite,
		Label:   "Change the Telegram alerts",
		Scope:   "Switching and rewording them, finding the chat, sending a test and retrying one that failed",
		Default: nil,
	})
}
