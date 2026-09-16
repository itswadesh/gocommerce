// Package twilio carries the store's SMS through Twilio's REST API.
//
// It is the plain-text counterpart to notify-msg91. MSG91 sends DLT flow
// templates, because India requires the wording to be registered with the
// carrier before it may be sent; Twilio sends free text, so the words come
// from the engine's own SMS templates and an operator can change them on the
// Setup SMS screen without asking anybody for approval. That is the whole
// difference between the two modules, and it is why this one has three
// settings where MSG91 has eight.
//
// Three settings, and one of them is a choice: Twilio addresses a message
// either from a phone number you own or through a messaging service that owns
// several. Sending both is an error on Twilio's side, so the service wins when
// it is set — a store that has moved to a service has a number left in the box
// and should not be punished for it.
//
// No SDK, per the rule that ext/ adds no third-party dependencies. Twilio's
// API is form posts with basic auth, which net/http does without help.
package twilio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	gocommerce "github.com/misiki/gocommerce/core"
)

const defaultBaseURL = "https://api.twilio.com"

// PluginKey is the plugin an operator switches on and configures.
const PluginKey = "sms-twilio"

// Config configures the module. Every field can also be set from the panel,
// which wins.
type Config struct {
	// AccountSID is the AC... identifier from the Twilio console.
	AccountSID string
	// AuthToken is the account's auth token, or an API key secret.
	AuthToken string
	// From is a Twilio number you own, in E.164: +15550000000.
	From string
	// MessagingServiceSID is the MG... identifier of a messaging service, used
	// instead of From when set.
	MessagingServiceSID string

	// BaseURL overrides Twilio's host. Tests set it; nothing else should.
	BaseURL string
	Client  *http.Client
}

// Module is the SMS backend.
type Module struct {
	cfg    Config
	app    *gocommerce.App
	client *http.Client
	log    *slog.Logger
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "notify-twilio" }

// DisplayName is what the Notifications screen calls this backend.
func (m *Module) DisplayName() string { return "Twilio" }

// Migrations implements gocommerce.Module.
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

	inCode := strings.TrimSpace(m.cfg.AccountSID) != "" && strings.TrimSpace(m.cfg.AuthToken) != ""
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "Twilio", Category: "notifications", DefaultEnabled: inCode,
		Description: "Texts shoppers about their orders through Twilio. The wording is the store's own — edit it under Notifications › Setup SMS — so nothing has to be registered with a carrier first.",
		Docs:        "https://www.twilio.com/docs/messaging/api/message-resource",
		Fields: []gocommerce.PluginField{
			{Key: "account_sid", Label: "Account SID", Kind: "text", Required: !inCode, Help: "Starts with AC. On the Twilio console home page."},
			{Key: "auth_token", Label: "Auth token", Kind: "secret", Required: !inCode, Help: "The account's auth token, or an API key secret."},
			{Key: "from", Label: "From number", Kind: "text", Help: "A Twilio number you own, in international form: +15550000000."},
			{Key: "messaging_service_sid", Label: "Messaging service SID", Kind: "text", Help: "Starts with MG. Set this instead of a from number to send through a messaging service; it wins over the number above."},
		},
	})
	app.RegisterNotifier(gocommerce.ChannelSMS, m)
	return nil
}

// sender is everything one message needs, resolved from the panel over Config.
type sender struct {
	accountSID, authToken string
	from, serviceSID      string
}

// resolve reads the settings. The bool is whether anything can be sent at all.
func (m *Module) resolve(ctx context.Context) (sender, bool) {
	on, err := m.app.Plugins().Enabled(ctx, PluginKey)
	if err != nil || !on {
		return sender{}, false
	}
	str := func(key, fallback string) string {
		if v := strings.TrimSpace(m.app.Plugins().String(ctx, PluginKey, key)); v != "" {
			return v
		}
		return strings.TrimSpace(fallback)
	}
	s := sender{
		accountSID: str("account_sid", m.cfg.AccountSID),
		authToken:  str("auth_token", m.cfg.AuthToken),
		from:       str("from", m.cfg.From),
		serviceSID: str("messaging_service_sid", m.cfg.MessagingServiceSID),
	}
	// An account with no way to address a message sends nothing, so it does
	// not count as configured — the Notifications screen would otherwise show
	// a green channel that silently drops every text.
	return s, s.accountSID != "" && s.authToken != "" && (s.from != "" || s.serviceSID != "")
}

// Configured implements gocommerce.ConfigurableNotifier.
func (m *Module) Configured(ctx context.Context) bool {
	_, ok := m.resolve(ctx)
	return ok
}

// Notify implements gocommerce.Notifier.
func (m *Module) Notify(ctx context.Context, n gocommerce.Notification) error {
	if n.Channel != gocommerce.ChannelSMS || n.To == "" {
		return nil
	}
	s, ok := m.resolve(ctx)
	if !ok {
		// Nothing can be sent, and the message is still written down. A text
		// that was never attempted and one that was delivered look identical
		// without this line, and a store can run for months in the first state
		// believing it is in the second.
		m.log.Info("text not sent: Twilio is not configured",
			"event", n.Event, "to", n.To, "channel", n.Channel)
		return nil
	}

	tpl, found, err := m.app.NotifyTemplates().Get(ctx, gocommerce.ChannelSMS, n.Event)
	if err != nil {
		return err
	}
	if !found {
		// An event with no wording is not an error: a store may add events
		// this module has never heard of.
		return nil
	}
	body, err := gocommerce.RenderNotifyText(tpl.Body, n.Data)
	if err != nil {
		return fmt.Errorf("twilio: %s body: %w", n.Event, err)
	}
	if strings.TrimSpace(body) == "" {
		return nil
	}
	return m.send(ctx, s, n, body)
}

func (m *Module) send(ctx context.Context, s sender, n gocommerce.Notification, body string) error {
	form := url.Values{}
	form.Set("To", n.To)
	form.Set("Body", body)
	// One or the other, never both: Twilio rejects a request carrying a from
	// number and a messaging service together.
	if s.serviceSID != "" {
		form.Set("MessagingServiceSid", s.serviceSID)
	} else {
		form.Set("From", s.from)
	}

	endpoint := m.cfg.BaseURL + "/2010-04-01/Accounts/" + url.PathEscape(s.accountSID) + "/Messages.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.SetBasicAuth(s.accountSID, s.authToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("twilio: send: %w", err)
	}
	defer resp.Body.Close()
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		var out struct {
			SID    string `json:"sid"`
			Status string `json:"status"`
		}
		_ = json.Unmarshal(detail, &out)
		m.log.Info("text sent", "event", n.Event, "to", n.To, "sid", out.SID, "status", out.Status)
		return nil

	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		// Twilio being down is worth trying again; Twilio saying no is not.
		return fmt.Errorf("twilio: send returned %s", resp.Status)

	default:
		var out struct {
			Code     int    `json:"code"`
			Message  string `json:"message"`
			MoreInfo string `json:"more_info"`
		}
		_ = json.Unmarshal(detail, &out)
		// Settled, not retried: a number Twilio will not accept will not be
		// accepted on the twelfth attempt either, and the reason belongs in
		// the log rather than in a stack trace.
		m.log.Error("Twilio refused the message",
			"event", n.Event, "to", n.To, "status", resp.Status,
			"code", out.Code, "detail", out.Message, "more_info", out.MoreInfo)
		return nil
	}
}
