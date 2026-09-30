// Package sendgrid delivers gocommerce notifications as email through
// SendGrid.
//
// It talks to SendGrid's v3 REST API over net/http rather than through the
// vendor SDK: sending one email is a single JSON POST, and taking a dependency
// for it would put SendGrid's transitive tree into the dependency graph of
// every store that installs this module.
//
// The key and the sender come from Config, or — when Config has none — from
// the "email-sendgrid" plugin, which the panel edits under Notifications ›
// Setup Email. Installed with neither, the module is idle: the engine keeps
// writing every message to the log until an operator fills the form in, and
// the settings and the doctor say so. The wording is not the module's at all:
// it asks the engine's notification templates for the effective subject and
// body at send time, so what the operator edits in the panel is what goes out.
//
//	app, err := gocommerce.New(cfg,
//	    sendgrid.New(sendgrid.Config{
//	        APIKey: os.Getenv("SENDGRID_API_KEY"),
//	        From:   "orders@example.com",
//	    }),
//	)
package sendgrid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/template"
	"time"

	"github.com/itswadesh/gocommerce/core"
)

const defaultBaseURL = "https://api.sendgrid.com"

// PluginKey is the plugin the module registers, where the panel keeps the key
// and the sender.
const PluginKey = "email-sendgrid"

// Config configures the module. Every field is optional: what it does not
// carry, the plugin's settings can.
type Config struct {
	// APIKey is a SendGrid API key with Mail Send permission.
	APIKey string
	// From is the sender address.
	From string
	// FromName is the sender's display name.
	FromName string
	// ReplyTo is optional.
	ReplyTo string
	// Subjects overrides the engine's default subject line for an event,
	// keyed by event name, for a store that keeps its wording in code. A
	// subject the operator edited in the panel wins over it.
	Subjects map[string]string
	// Bodies overrides the engine's default plain-text body for an event.
	Bodies map[string]string
	// BaseURL overrides the SendGrid endpoint, for tests.
	BaseURL string
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the SendGrid notifier.
type Module struct {
	cfg    Config
	app    *gocommerce.App
	client *http.Client
	log    interface{ Error(string, ...any) }
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "notify-sendgrid" }

// DisplayName implements gocommerce.Named: what the settings and the Setup
// Email screen call this backend.
func (m *Module) DisplayName() string { return "SendGrid" }

// Migrations implements gocommerce.Module. Sending email owns no state.
func (m *Module) Migrations() []gocommerce.Migration { return nil }

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	if m.cfg.BaseURL == "" {
		m.cfg.BaseURL = defaultBaseURL
	}
	m.client = m.cfg.Client
	if m.client == nil {
		m.client = &http.Client{Timeout: 15 * time.Second}
	}
	m.log = app.Log()

	// An override that does not parse is refused at boot, not on the first
	// sale: the wording is code here, and code fails early.
	for event, text := range m.cfg.Subjects {
		if _, err := template.New("subject:" + event).Parse(text); err != nil {
			return fmt.Errorf("sendgrid: subject template for %s: %w", event, err)
		}
	}
	for event, text := range m.cfg.Bodies {
		if _, err := template.New("body:" + event).Parse(text); err != nil {
			return fmt.Errorf("sendgrid: body template for %s: %w", event, err)
		}
	}

	// Config-configured stores start switched on, so installing the module
	// with a key in the environment sends mail the way it always has; the
	// switch is still theirs to turn off. A store with nothing in Config
	// starts off and needs both required fields before anything goes out.
	inCode := strings.TrimSpace(m.cfg.APIKey) != "" && strings.TrimSpace(m.cfg.From) != ""
	help := ""
	if inCode {
		help = " Set in code for this store; leave empty to keep that."
	}
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "SendGrid", Category: "notifications", DefaultEnabled: inCode,
		Description: "Sends the store's emails — order confirmations, shipping notices, password resets — through SendGrid. The wording of each message is edited under Notifications › Setup Email.",
		Docs:        "https://www.twilio.com/docs/sendgrid/for-developers/sending-email/api-getting-started",
		Fields: []gocommerce.PluginField{
			{Key: "api_key", Label: "API key", Kind: "secret", Required: !inCode, Help: "A SendGrid API key with Mail Send permission." + help},
			{Key: "from", Label: "From address", Kind: "text", Required: !inCode, Help: "A sender SendGrid has verified." + help},
			{Key: "from_name", Label: "From name", Kind: "text", Help: "The name beside the address in the inbox."},
			{Key: "reply_to", Label: "Reply-to address", Kind: "text", Help: "Where a shopper's reply lands; empty means the from address."},
		},
	})
	app.RegisterNotifier(gocommerce.ChannelEmail, m)
	return nil
}

// sender is the effective credentials: the plugin's value for a field where
// the operator typed one, Config's otherwise.
type sender struct {
	apiKey, from, fromName, replyTo string
}

func (m *Module) sender(ctx context.Context) (sender, bool) {
	on, err := m.app.Plugins().Enabled(ctx, PluginKey)
	if err != nil || !on {
		return sender{}, false
	}
	pick := func(field, fallback string) string {
		if v := strings.TrimSpace(m.app.Plugins().String(ctx, PluginKey, field)); v != "" {
			return v
		}
		return strings.TrimSpace(fallback)
	}
	s := sender{
		apiKey:   pick("api_key", m.cfg.APIKey),
		from:     pick("from", m.cfg.From),
		fromName: pick("from_name", m.cfg.FromName),
		replyTo:  pick("reply_to", m.cfg.ReplyTo),
	}
	return s, s.apiKey != "" && s.from != ""
}

// Configured implements gocommerce.ConfigurableNotifier: switched on, with a
// key and a sender to send from.
func (m *Module) Configured(ctx context.Context) bool {
	_, ok := m.sender(ctx)
	return ok
}

// Notify implements gocommerce.Notifier.
func (m *Module) Notify(ctx context.Context, n gocommerce.Notification) error {
	if n.Channel != gocommerce.ChannelEmail || n.To == "" {
		return nil
	}
	s, ok := m.sender(ctx)
	if !ok {
		// The funnel skips an unconfigured backend before it gets here;
		// this is what keeps a direct call honest too.
		return nil
	}
	tpl, ok, err := m.app.NotifyTemplates().Get(ctx, gocommerce.ChannelEmail, n.Event)
	if err != nil {
		return err
	}
	if !ok {
		// An event with no wording is not an error: a store may add events
		// this module has never heard of.
		return nil
	}
	subjectText, bodyText := tpl.Subject, tpl.Body
	if !tpl.Customized {
		// Code's override applies only where the operator has not spoken:
		// the panel is the nearer hand.
		if text, ok := m.cfg.Subjects[n.Event]; ok {
			subjectText = text
		}
		if text, ok := m.cfg.Bodies[n.Event]; ok {
			bodyText = text
		}
	}
	subject, err := gocommerce.RenderNotifyText(subjectText, n.Data)
	if err != nil {
		return fmt.Errorf("sendgrid: %s subject: %w", n.Event, err)
	}
	body, err := gocommerce.RenderNotifyText(bodyText, n.Data)
	if err != nil {
		return fmt.Errorf("sendgrid: %s body: %w", n.Event, err)
	}
	return m.send(ctx, s, n.To, subject, body)
}

type mailPayload struct {
	Personalizations []personalization `json:"personalizations"`
	From             emailAddress      `json:"from"`
	ReplyTo          *emailAddress     `json:"reply_to,omitempty"`
	Subject          string            `json:"subject"`
	Content          []mailContent     `json:"content"`
}

type personalization struct {
	To []emailAddress `json:"to"`
}

type emailAddress struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type mailContent struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

func (m *Module) send(ctx context.Context, s sender, to, subject, body string) error {
	payload := mailPayload{
		Personalizations: []personalization{{To: []emailAddress{{Email: to}}}},
		From:             emailAddress{Email: s.from, Name: s.fromName},
		Subject:          subject,
		Content:          []mailContent{{Type: "text/plain", Value: body}},
	}
	if s.replyTo != "" {
		payload.ReplyTo = &emailAddress{Email: s.replyTo}
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		m.cfg.BaseURL+"/v3/mail/send", bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		// Returning the error asks the outbox to retry with backoff, which is
		// the right answer to a vendor being briefly unreachable.
		return fmt.Errorf("sendgrid: send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	err = fmt.Errorf("sendgrid: send returned %s: %s", resp.Status, strings.TrimSpace(string(detail)))
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
		// A rejected message will be rejected again. Log it and let the event
		// settle, rather than retrying a bad address twelve times.
		m.log.Error("sendgrid rejected the message", "status", resp.Status, "to", to, "detail", string(detail))
		return nil
	}
	return err
}
