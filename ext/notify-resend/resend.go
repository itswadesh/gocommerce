// Package resend delivers gocommerce notifications as email through Resend.
//
// It is the email provider a store should reach for first, and the module is
// shaped so that reaching for it is one field: paste an API key and mail goes
// out. Resend lets any account send from `onboarding@resend.dev` without
// verifying a domain, so a store that has configured nothing but the key sends
// from there — which is what makes the first order confirmation arrive on the
// day the store is set up rather than the week the DNS is finished. Point the
// From field at a verified domain when there is one; the default is for
// getting started, and every message says where it came from.
//
// It talks to Resend's REST API over net/http rather than through the vendor
// SDK: sending one email is a single JSON POST, and taking a dependency for it
// would put Resend's transitive tree into the dependency graph of every store
// that installs this module (AGENTS.md rule 2).
//
// Whatever happens, the message is written to the log. That is the other half
// of the ask and it is not decoration: an email that was accepted, rejected or
// never attempted otherwise leaves no trace an operator can read, and the one
// failure mode this engine already documents at length is a store that looks
// healthy while nobody receives anything.
//
//	app, err := gocommerce.New(cfg,
//	    resend.New(resend.Config{APIKey: os.Getenv("RESEND_API_KEY")}),
//	)
package resend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"text/template"
	"time"

	gocommerce "github.com/misiki/gocommerce/core"
)

const defaultBaseURL = "https://api.resend.com"

// DefaultFrom is the sender Resend gives every account before a domain is
// verified. It is deliberately the fallback rather than a hidden default: a
// store with nothing but an API key sends from here and its mail arrives,
// which is the difference between a working store on day one and a silent one.
//
// It is not a shared account and carries no credentials of ours — the store's
// own key is what authorises the send, and Resend attributes the message to
// the account that key belongs to.
const DefaultFrom = "onboarding@resend.dev"

// PluginKey is the plugin the module registers, where the panel keeps the key
// and the sender.
const PluginKey = "email-resend"

// Config configures the module. Every field is optional: what it does not
// carry, the plugin's settings can, and what neither carries has a default
// that still sends.
type Config struct {
	// APIKey is a Resend API key. Without one nothing can be sent, by this
	// module or by anything else — a key is the one thing that cannot have a
	// default, because it belongs to the store rather than to the engine.
	APIKey string
	// From is the sender address. Empty means DefaultFrom.
	From string
	// FromName is the display name beside it.
	FromName string
	// ReplyTo is optional.
	ReplyTo string
	// Subjects and Bodies override the engine's wording for an event, for a
	// store that keeps its copy in code. What the operator edits in the panel
	// wins over either.
	Subjects map[string]string
	Bodies   map[string]string
	// BaseURL overrides the Resend endpoint, for tests.
	BaseURL string
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the Resend notifier.
type Module struct {
	cfg    Config
	app    *gocommerce.App
	client *http.Client
	log    *slog.Logger
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "notify-resend" }

// DisplayName implements gocommerce.Named: what the settings and the Setup
// Email screen call this backend.
func (m *Module) DisplayName() string { return "Resend" }

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
			return fmt.Errorf("resend: subject template for %s: %w", event, err)
		}
	}
	for event, text := range m.cfg.Bodies {
		if _, err := template.New("body:" + event).Parse(text); err != nil {
			return fmt.Errorf("resend: body template for %s: %w", event, err)
		}
	}

	// A key in Config starts the module switched on. Unlike the other
	// notifiers that is the only required field: From has a default that
	// works, so a store needs one setting rather than two before mail sends.
	inCode := strings.TrimSpace(m.cfg.APIKey) != ""
	help := ""
	if inCode {
		help = " Set in code for this store; leave empty to keep that."
	}
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "Resend", Category: "notifications", DefaultEnabled: inCode,
		Description: "Sends the store's emails — order confirmations, shipping notices, password resets — through Resend. Paste an API key and mail goes out; until a domain is verified it is sent from " + DefaultFrom + ". The wording of each message is edited under Notifications › Setup Email.",
		Docs:        "https://resend.com/docs/api-reference/emails/send-email",
		Fields: []gocommerce.PluginField{
			{Key: "api_key", Label: "API key", Kind: "secret", Required: !inCode, Help: "A Resend API key." + help},
			{
				Key: "from", Label: "From address", Kind: "text", Default: DefaultFrom,
				Help: "A sender on a domain Resend has verified. Left as " + DefaultFrom +
					" it still sends, which is what lets a new store work before its DNS does.",
			},
			{Key: "from_name", Label: "From name", Kind: "text", Help: "The name beside the address in the inbox."},
			{Key: "reply_to", Label: "Reply-to address", Kind: "text", Help: "Where a shopper's reply lands; empty means the from address."},
		},
	})
	app.RegisterNotifier(gocommerce.ChannelEmail, m)
	return nil
}

// sender is the effective credentials: the plugin's value for a field where
// the operator typed one, Config's otherwise, and the default under both.
type sender struct {
	apiKey, from, fromName, replyTo string
	// defaulted is whether From fell through to DefaultFrom, which the log
	// line says so that "why is my mail from resend.dev" has an answer in the
	// place somebody is already looking.
	defaulted bool
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
	if s.from == "" {
		s.from, s.defaulted = DefaultFrom, true
	}
	// The key is the only thing with no default. Everything else can be
	// nothing and the message still goes.
	return s, s.apiKey != ""
}

// Configured implements gocommerce.ConfigurableNotifier: switched on, with a
// key. There is no second required field, which is the point of this module.
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
		// Not configured, so nothing can be sent — but the message is still
		// written down. An order confirmation that was never attempted and one
		// that was delivered look identical without this line, and a store can
		// run for months in the first state believing it is in the second.
		m.log.Info("email not sent: Resend has no API key",
			"event", n.Event, "to", n.To, "channel", n.Channel)
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
		return fmt.Errorf("resend: %s subject: %w", n.Event, err)
	}
	body, err := gocommerce.RenderNotifyText(bodyText, n.Data)
	if err != nil {
		return fmt.Errorf("resend: %s body: %w", n.Event, err)
	}
	return m.send(ctx, s, n.To, n.Event, subject, body)
}

type mailPayload struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	ReplyTo string   `json:"reply_to,omitempty"`
	Subject string   `json:"subject"`
	Text    string   `json:"text"`
}

type mailResult struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	Name    string `json:"name"`
}

// fromHeader is "Name <address>" when there is a name, the bare address
// otherwise, which is the shape Resend's `from` takes.
func (s sender) fromHeader() string {
	if s.fromName == "" {
		return s.from
	}
	return s.fromName + " <" + s.from + ">"
}

func (m *Module) send(ctx context.Context, s sender, to, event, subject, body string) error {
	payload := mailPayload{
		From:    s.fromHeader(),
		To:      []string{to},
		ReplyTo: s.replyTo,
		Subject: subject,
		Text:    body,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		m.cfg.BaseURL+"/emails", bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		// Returning the error asks the outbox to retry with backoff, which is
		// the right answer to a vendor being briefly unreachable. Logged all
		// the same, because a retry that eventually succeeds still hid a
		// period where nothing was arriving.
		m.log.Error("email could not reach Resend", "event", event, "to", to, "error", err)
		return fmt.Errorf("resend: send: %w", err)
	}
	defer resp.Body.Close()

	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var out mailResult
		_ = json.Unmarshal(detail, &out)
		// The sent line, which is the record the ask is about: every message
		// that goes out says so, with the id Resend filed it under so a
		// question about one email can be answered from their dashboard.
		m.log.Info("email sent",
			"event", event, "to", to, "from", s.from, "id", out.ID,
			"default_sender", s.defaulted)
		return nil
	}

	var out mailResult
	_ = json.Unmarshal(detail, &out)
	reason := strings.TrimSpace(out.Message)
	if reason == "" {
		reason = strings.TrimSpace(string(detail))
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
		// A rejected message will be rejected again. Log it and let the event
		// settle, rather than retrying a bad address twelve times.
		m.log.Error("Resend rejected the message",
			"event", event, "status", resp.Status, "to", to, "from", s.from, "detail", reason)
		return nil
	}
	m.log.Error("Resend could not take the message",
		"event", event, "status", resp.Status, "to", to, "detail", reason)
	return fmt.Errorf("resend: send returned %s: %s", resp.Status, reason)
}
