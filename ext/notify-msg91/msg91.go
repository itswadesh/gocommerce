// Package msg91 delivers gocommerce notifications as SMS through MSG91.
//
// India requires SMS content to be pre-registered as a DLT template, so this
// module sends a template id and its variables rather than free text — the
// wording lives in your MSG91 account, which is where the regulator expects
// to find it. The engine's SMS templates are therefore not what this module
// sends; the template id for each event is.
//
// The key and the ids come from Config, or — when Config has none — from the
// "sms-msg91" plugin, which the panel edits under Notifications › Setup SMS.
// Installed with neither, the module is idle and the settings say so.
//
//	app, err := gocommerce.New(cfg,
//	    msg91.New(msg91.Config{
//	        AuthKey:   os.Getenv("MSG91_AUTH_KEY"),
//	        Templates: map[string]string{
//	            gocommerce.EventOrderShipped: "65a1b2c3d4e5f6",
//	        },
//	    }),
//	)
package msg91

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/itswadesh/gocommerce/core"
)

const defaultBaseURL = "https://control.msg91.com"

// PluginKey is the plugin the module registers, where the panel keeps the key
// and the template ids.
const PluginKey = "sms-msg91"

// Config configures the module. Every field is optional: what it does not
// carry, the plugin's settings can.
type Config struct {
	// AuthKey is the MSG91 authentication key.
	AuthKey string
	// Templates maps an event name to a DLT-approved flow template id. An
	// event with no template is not sent — which is the usual case, since
	// most stores text about shipping and nothing else.
	Templates map[string]string
	// DefaultCountryCode is prefixed to recipient numbers that have none,
	// e.g. "91". MSG91 requires the country code.
	DefaultCountryCode string
	// BaseURL overrides the endpoint, for tests.
	BaseURL string
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the MSG91 notifier.
type Module struct {
	cfg    Config
	app    *gocommerce.App
	client *http.Client
	log    *slog.Logger
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "notify-msg91" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "MSG91" }

// Migrations implements gocommerce.Module. Sending SMS owns no state.
func (m *Module) Migrations() []gocommerce.Migration { return nil }

// eventFields is the plugin field for each event's template id, in the
// order the Setup SMS screen lists them.
var eventFields = []struct {
	event, key, label string
}{
	{gocommerce.EventOrderCreated, "template_order_placed", "Order placed"},
	{gocommerce.EventOrderPaid, "template_payment_received", "Payment received"},
	{gocommerce.EventOrderShipped, "template_order_shipped", "Order shipped"},
	{gocommerce.EventOrderDelivered, "template_order_delivered", "Order delivered"},
	{gocommerce.EventOrderCancelled, "template_order_cancelled", "Order cancelled"},
	{gocommerce.EventOrderRefunded, "template_refund_issued", "Refund issued"},
}

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

	inCode := strings.TrimSpace(m.cfg.AuthKey) != ""
	fields := []gocommerce.PluginField{
		{Key: "auth_key", Label: "Auth key", Kind: "secret", Required: !inCode, Help: "From the MSG91 dashboard, under Authkey."},
		{Key: "country_code", Label: "Default country code", Kind: "text", Default: "91", Help: "Prefixed to a number that has none; MSG91 needs it."},
	}
	for _, f := range eventFields {
		fields = append(fields, gocommerce.PluginField{
			Key: f.key, Label: f.label + " template ID", Kind: "text",
			Help: "The DLT-approved flow template for this event. Empty sends nothing for it.",
		})
	}
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "MSG91", Category: "notifications", DefaultEnabled: inCode,
		Description: "Texts shoppers about their orders through MSG91's DLT flow templates. The wording is registered with MSG91; each event below names the template that carries it.",
		Docs:        "https://docs.msg91.com/sms/send-sms",
		Fields:      fields,
	})
	app.RegisterNotifier(gocommerce.ChannelSMS, m)
	return nil
}

func (m *Module) enabled(ctx context.Context) bool {
	on, err := m.app.Plugins().Enabled(ctx, PluginKey)
	return err == nil && on
}

func (m *Module) setting(ctx context.Context, field, fallback string) string {
	if v := strings.TrimSpace(m.app.Plugins().String(ctx, PluginKey, field)); v != "" {
		return v
	}
	return strings.TrimSpace(fallback)
}

func (m *Module) authKey(ctx context.Context) string {
	if !m.enabled(ctx) {
		return ""
	}
	return m.setting(ctx, "auth_key", m.cfg.AuthKey)
}

func (m *Module) templateID(ctx context.Context, event string) string {
	for _, f := range eventFields {
		if f.event == event {
			return m.setting(ctx, f.key, m.cfg.Templates[event])
		}
	}
	return strings.TrimSpace(m.cfg.Templates[event])
}

// Configured implements gocommerce.ConfigurableNotifier: switched on, with a
// key. Which events have a template is a separate question, answered per
// message.
func (m *Module) Configured(ctx context.Context) bool {
	return m.authKey(ctx) != ""
}

// Notify implements gocommerce.Notifier.
func (m *Module) Notify(ctx context.Context, n gocommerce.Notification) error {
	if n.Channel != gocommerce.ChannelSMS || n.To == "" {
		return nil
	}
	key := m.authKey(ctx)
	if key == "" {
		return nil
	}
	templateID := m.templateID(ctx, n.Event)
	if templateID == "" {
		return nil
	}

	recipient := map[string]string{"mobiles": m.normalizeNumber(ctx, n.To)}
	for k, v := range n.Data {
		recipient[k] = v
	}

	payload := map[string]any{
		"template_id": templateID,
		"recipients":  []map[string]string{recipient},
	}
	return m.post(ctx, key, "/api/v5/flow/", payload)
}

func (m *Module) normalizeNumber(ctx context.Context, raw string) string {
	var digits strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	number := digits.String()
	cc := m.setting(ctx, "country_code", m.cfg.DefaultCountryCode)
	if cc == "" || strings.HasPrefix(number, cc) {
		return number
	}
	if len(number) == 10 {
		return cc + number
	}
	return number
}

func (m *Module) post(ctx context.Context, key, path string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.cfg.BaseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("authkey", key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("msg91: %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// MSG91 answers 200 to a rejected message too, with type "error".
		var result struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &result)
		if strings.EqualFold(result.Type, "error") {
			m.log.Error("MSG91 rejected the message", "detail", result.Message)
			return nil // a rejected message will be rejected again; do not retry
		}
		return nil
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
		m.log.Error("MSG91 rejected the request", "status", resp.Status, "detail", string(body))
		return nil
	}
	return fmt.Errorf("msg91: %s returned %s", path, resp.Status)
}
