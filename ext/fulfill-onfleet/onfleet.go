// Package onfleet dispatches deliveries through Onfleet.
//
// Registering it adds "onfleet" as a fulfillment provider, so an operator ships
// by posting to the engine's own /api/admin/create-fulfillment with
// `"provider": "onfleet"` — the engine still owns the order's state and its
// events, and this module only talks to Onfleet.
//
//	app, err := gocommerce.New(cfg,
//	    onfleet.New(onfleet.Config{APIKey: os.Getenv("ONFLEET_API_KEY")}),
//	)
//
// REST over net/http and no SDK, per rule 2.
//
// # This is not a carrier
//
// Every other fulfillment module here hands a parcel to a courier. Onfleet does
// not carry anything: it dispatches a task to the store's own drivers, and what
// comes back is a task, not a waybill. So:
//
//   - the tracking number recorded is Onfleet's shortId, which is what finds
//     the task in the dashboard;
//   - the carrier code is "onfleet", which has no tracking URL in the engine's
//     registry on purpose — the recipient gets Onfleet's own link by SMS from
//     Onfleet, and a link built from an upper-cased task id would not resolve;
//   - there is no label, because a driver going to an address does not need one.
//
// It suits same-day and local delivery, and it is the wrong module for anything
// that goes in the post.
//
// # Addresses
//
// Onfleet parses an address itself when given one line, and takes the parts
// when given them. This module sends the parts, because the engine has them as
// parts and re-joining them into a sentence for Onfleet to take apart again is
// a lossy round trip. The street line goes in `street`; Onfleet's `number` is
// left out rather than guessed at from the front of the line, since "Flat 2,
// 14 High Street" has two numbers in it and neither is reliably the one meant.
package onfleet

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/itswadesh/gocommerce/core"
)

const (
	defaultBaseURL = "https://onfleet.com/api/v2"
	maxBodyRead    = 1 << 20
)

// Config configures the module.
type Config struct {
	// APIKey is an Onfleet API key. It is sent as the username of an HTTP
	// Basic credential with an empty password, which is how Onfleet
	// authenticates. Required.
	APIKey string `plugin:"api_key"`
	// AutoAssign asks Onfleet to pick a driver as the task is created. Off by
	// default: assigning somebody's next two hours is a decision, and a store
	// that dispatches by hand would not want it made for them.
	AutoAssign bool `plugin:"auto_assign"`
	// TeamID scopes auto-assignment to one team. Only meaningful with
	// AutoAssign.
	TeamID string `plugin:"team_id"`
	// ServiceTimeMinutes is how long the driver is expected to be at the door.
	// Zero leaves Onfleet's own default alone.
	ServiceTimeMinutes int `plugin:"service_time_minutes"`
	// BaseURL overrides the endpoint, for tests.
	BaseURL string `plugin:"base_url"`
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the Onfleet fulfillment provider.
type Module struct {
	cfg    Config
	live   atomic.Pointer[Config]
	app    *gocommerce.App
	client *http.Client
	log    *slog.Logger
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// PluginKey is the plugin this module registers, where the panel keeps its
// credentials. Config is the environment's fallback for each field; a value
// typed into the panel wins.
const PluginKey = "shipping-onfleet"

var errNotConfigured = errors.New("onfleet: not set up — activate and configure it under Settings")

// conf is the effective configuration: the last one refresh computed, or
// Config alone before the first use.
func (m *Module) conf() *Config {
	if c := m.live.Load(); c != nil {
		return c
	}
	return &m.cfg
}

// refresh recomputes the effective configuration — Config with the plugin's
// settings laid over it — and reports whether the provider can work: switched
// on, with every required field filled. Called before each use, so a key
// typed into the panel a moment ago counts without a restart.
func (m *Module) refresh(ctx context.Context) bool {
	c := m.cfg
	on := false
	if m.app != nil {
		on, _ = m.app.Plugins().Enabled(ctx, PluginKey)
		if on {
			_ = m.app.Plugins().Fill(ctx, PluginKey, &c)
		}
	}
	m.finish(&c)
	m.live.Store(&c)
	return on && m.complete(&c)
}

// Configured implements gocommerce.Configurable.
func (m *Module) Configured(ctx context.Context) bool { return m.refresh(ctx) }

// complete is whether a configuration has everything the provider needs.
func (m *Module) complete(c *Config) bool {
	return strings.TrimSpace(c.APIKey) != ""
}

// finish fills a configuration's defaults.
func (m *Module) finish(c *Config) {
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "fulfill-onfleet" }

// Migrations implements gocommerce.Module. The engine already stores the
// shipment; this module keeps no state of its own.
func (m *Module) Migrations() []gocommerce.Migration { return nil }

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	// Config-configured stores start switched on; a store with nothing in
	// Config starts idle and waits for the panel.
	inCode := m.complete(&m.cfg)
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "Onfleet", Category: "shipping", DefaultEnabled: inCode,
		Description: "Dispatches your own drivers through Onfleet: every shipment becomes a delivery task.",
		Docs:        "https://docs.onfleet.com/",
		Fields: []gocommerce.PluginField{
			{Key: "api_key", Label: "API key", Kind: "secret", Required: !inCode, Help: "From Onfleet's dashboard, under API & Webhooks."},
			{Key: "auto_assign", Label: "Auto-assign a driver", Kind: "bool", Help: "Assigning somebody's next two hours is a decision; off by default."},
			{Key: "team_id", Label: "Team ID", Kind: "text", Help: "Scopes auto-assignment to one team."},
			{Key: "service_time_minutes", Label: "Service time (minutes)", Kind: "number", Help: "How long the driver is expected at the door; empty leaves Onfleet's default."},
			{Key: "base_url", Label: "API base URL", Kind: "url", Help: "Empty for production."},
		},
	})
	m.finish(&m.cfg)
	m.client = m.cfg.Client
	if m.client == nil {
		m.client = &http.Client{Timeout: 30 * time.Second}
	}
	m.log = app.Log()

	app.RegisterFulfillment(m)
	return nil
}

// Code implements gocommerce.FulfillmentProvider.
func (m *Module) Code() string { return "onfleet" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "Onfleet" }

// Ship creates the delivery task.
//
// It runs before the engine opens its transaction, so Onfleet having a slow
// minute never holds a lock on the orders table.
func (m *Module) Ship(ctx context.Context, order *gocommerce.Order, req gocommerce.ShipRequest) (gocommerce.Shipment, error) {
	if !m.refresh(ctx) {
		return gocommerce.Shipment{}, errNotConfigured
	}
	var out struct {
		ID          string `json:"id"`
		ShortID     string `json:"shortId"`
		TrackingURL string `json:"trackingURL"`
		State       int    `json:"state"`
	}
	if err := m.post(ctx, "/tasks", m.taskBody(order, req), &out); err != nil {
		return gocommerce.Shipment{}, err
	}
	if out.ShortID == "" && out.ID == "" {
		return gocommerce.Shipment{}, errors.New("onfleet: the task came back with no id")
	}
	if out.TrackingURL != "" {
		// The recipient gets this from Onfleet, not from the store, so it is
		// logged rather than stored: the engine has nowhere truthful to put a
		// URL that is neither a label nor built from the tracking number.
		m.log.Info("Onfleet task created", "order_id", order.ID,
			"short_id", out.ShortID, "tracking_url", out.TrackingURL)
	}

	return gocommerce.Shipment{
		Provider: m.Code(),
		// The shortId, not the full id: it is what an operator types into the
		// Onfleet dashboard, and the full id is in the logs above.
		Tracking: firstNonEmpty(out.ShortID, out.ID),
		Carrier:  "onfleet",
	}, nil
}

func (m *Module) taskBody(order *gocommerce.Order, req gocommerce.ShipRequest) map[string]any {
	addr := order.Address

	byLine := make(map[int64]gocommerce.OrderLine, len(order.Lines))
	for _, line := range order.Lines {
		byLine[line.ID] = line
	}
	// What the driver is carrying is what is in this parcel, not what is on the
	// order: the engine has already resolved req.Lines to explicit quantities.
	contents := make([]string, 0, len(req.Lines))
	var units int
	for _, ship := range req.Lines {
		line, ok := byLine[ship.OrderLineID]
		if !ok {
			continue
		}
		units += ship.Quantity
		contents = append(contents, fmt.Sprintf("%d x %s", ship.Quantity, line.Title))
	}

	notes := "Order " + order.Number
	if len(contents) > 0 {
		notes += ": " + strings.Join(contents, ", ")
	}
	if order.PaymentStatus != gocommerce.PaymentPaid {
		// The driver is the one who will be asked for money, or will forget to
		// ask. It belongs at the top of what they read.
		notes = "COLLECT " + order.Total.Currency + " " +
			fmt.Sprintf("%.2f", float64(order.Total.AmountMinor)/100) + " — " + notes
	}
	if extra := req.Meta["notes"]; extra != "" {
		notes += "\n" + extra
	}

	body := map[string]any{
		"destination": map[string]any{
			"address": map[string]any{
				"street":     strings.TrimSpace(addr.Line1),
				"apartment":  addr.Line2,
				"city":       addr.City,
				"state":      addr.State,
				"postalCode": addr.PostalCode,
				"country":    addr.Country,
			},
		},
		"recipients": []any{map[string]any{
			"name":  firstNonEmpty(addr.Name, order.Name, "Customer"),
			"phone": firstNonEmpty(addr.Phone, order.Phone),
		}},
		"notes": notes,
		// Metadata is how the task is found again from the store's own
		// identifiers, and Onfleet takes it as a typed list rather than an
		// object.
		"metadata": []any{
			metadata("order_number", order.Number),
			metadata("order_id", fmt.Sprint(order.ID)),
			metadata("units", fmt.Sprint(units)),
		},
		"quantity": units,
	}
	if m.conf().AutoAssign {
		assign := map[string]any{"mode": "distance"}
		if m.conf().TeamID != "" {
			assign["team"] = m.conf().TeamID
		}
		body["autoAssign"] = assign
	}
	if m.conf().ServiceTimeMinutes > 0 {
		body["serviceTime"] = m.conf().ServiceTimeMinutes
	}
	return body
}

func metadata(name, value string) map[string]any {
	return map[string]any{
		"name": name, "type": "string", "value": value,
		// Visible to the store and its drivers, not to the recipient.
		"visibility": []string{"api", "dashboard"},
	}
}

func (m *Module) post(ctx context.Context, path string, payload any, out any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("onfleet: could not encode the request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.conf().BaseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	// The API key is the username and the password is empty, which is what
	// Onfleet documents — not a bearer token.
	req.Header.Set("Authorization", "Basic "+
		base64.StdEncoding.EncodeToString([]byte(m.conf().APIKey+":")))
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("onfleet: %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyRead))
	if err != nil {
		return fmt.Errorf("onfleet: %s: read response: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Message struct {
				Error   int    `json:"error"`
				Message string `json:"message"`
				Cause   any    `json:"cause"`
			} `json:"message"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Message.Message != "" {
			detail := apiErr.Message.Message
			if apiErr.Message.Cause != nil {
				detail += fmt.Sprintf(" (%v)", apiErr.Message.Cause)
			}
			return fmt.Errorf("onfleet: %s: %s", path, detail)
		}
		return fmt.Errorf("onfleet: %s returned %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
