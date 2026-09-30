// Package klaviyo tells Klaviyo what happens in this store, so its flows —
// abandoned cart, post-purchase, win-back — have something to run on.
//
// Every order event and every abandoned cart becomes a Klaviyo event on the
// shopper's profile, under the metric names Klaviyo's own Shopify
// integration uses (Placed Order, Ordered Product, Fulfilled Order,
// Cancelled Order, Refunded Order, Started Checkout), so a flow built
// against those names works unchanged. The public key is published to the
// storefront for onsite tracking and signup forms.
//
// Over net/http, no SDK (rule 2): one POST per event, JSON:API shaped as
// Klaviyo's 2024 API wants it. Configured from the Plugins screen or from
// Config; the screen wins.
//
// Delivery is best effort, on purpose. The outbox re-runs a whole event
// when any subscriber fails (D51), which would re-issue invoices and re-send
// notifications for the sake of one marketing ping — so a call that fails
// twice is logged and counted, never returned. Klaviyo de-duplicates on the
// event id, so the retry that does happen is harmless.
package klaviyo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	pluginKey      = "klaviyo"
	defaultBaseURL = "https://a.klaviyo.com"
	revision       = "2024-10-15"
)

// Config configures the module. Every field can also be set from the
// Plugins screen, which wins.
type Config struct {
	// PrivateKey is a private API key with events:write and profiles:write.
	PrivateKey string
	// PublicKey is the six-character public key the storefront's onsite
	// tracking script uses. Optional.
	PublicKey string
	// BaseURL overrides Klaviyo's endpoint, for tests.
	BaseURL string
	// Client overrides the HTTP client.
	Client *http.Client
}

// Status is what the admin route reports.
type Status struct {
	Enabled    bool       `json:"enabled"`
	Configured bool       `json:"configured"`
	Sent       int        `json:"sent"`
	Failed     int        `json:"failed"`
	LastSent   *time.Time `json:"last_sent,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
}

// Module is the subscriber.
type Module struct {
	cfg    Config
	app    *gocommerce.App
	log    *slog.Logger
	client *http.Client

	mu       sync.Mutex
	sent     int
	failed   int
	lastSent time.Time
	lastErr  string
}

// New constructs the module.
func New(cfg Config) *Module {
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	return &Module{cfg: cfg, client: client}
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "klaviyo" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration { return nil }

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	m.log = app.Log().With("module", "klaviyo")
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: pluginKey, Title: "Klaviyo", Category: "marketing",
		Description:    "Email and SMS marketing: every order and abandoned cart becomes a Klaviyo event on the shopper's profile, under the metric names Klaviyo's flows already know. The public key powers onsite tracking and signup forms.",
		DefaultEnabled: m.cfg.PrivateKey != "",
		Docs:           "https://developers.klaviyo.com/en/reference/create_event",
		Fields: []gocommerce.PluginField{
			{Key: "private_key", Label: "Private API key", Kind: "secret", Required: m.cfg.PrivateKey == "", Help: "pk_… with events:write and profiles:write."},
			{Key: "public_key", Label: "Public API key", Kind: "text", Public: true, Help: "The six-character key the storefront's tracking script and signup forms use."},
			{Key: "track_carts", Label: "Send abandoned carts as Started Checkout", Kind: "bool", Default: true},
		},
	})
	app.Subscribe("order.*", m.onOrder)
	app.Subscribe(gocommerce.EventCartAbandoned, m.onCart)
	app.HandleAdminFunc("GET /api/admin/x/klaviyo/status", m.handleStatus, gocommerce.RightStoreOperate)
	return nil
}

type connection struct {
	key, base  string
	enabled    bool
	trackCarts bool
}

func (m *Module) connection(ctx context.Context) (connection, error) {
	enabled, err := m.app.Plugins().Enabled(ctx, pluginKey)
	if err != nil {
		return connection{}, err
	}
	settings, err := m.app.Plugins().Settings(ctx, pluginKey)
	if err != nil {
		return connection{}, err
	}
	key, _ := settings["private_key"].(string)
	if key == "" {
		key = m.cfg.PrivateKey
	}
	base := m.cfg.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	track := true
	if v, ok := settings["track_carts"].(bool); ok {
		track = v
	}
	return connection{key: key, base: strings.TrimRight(base, "/"), enabled: enabled, trackCarts: track}, nil
}

// ------------------------------------------------------------------ events

// metrics is the Klaviyo metric each order event lands under. Events not
// listed — edits, undeliveries, returns — are the store's business alone.
var metrics = map[string]string{
	gocommerce.EventOrderCreated:   "Placed Order",
	gocommerce.EventOrderShipped:   "Fulfilled Order",
	gocommerce.EventOrderCancelled: "Cancelled Order",
	gocommerce.EventOrderRefunded:  "Refunded Order",
}

func (m *Module) onOrder(ctx context.Context, e gocommerce.Event) error {
	metric, ok := metrics[e.Name]
	if !ok {
		return nil
	}
	conn, err := m.connection(ctx)
	if err != nil || !conn.enabled || conn.key == "" {
		return nil
	}
	var order gocommerce.OrderEvent
	if err := json.Unmarshal(e.Data, &order); err != nil || order.Email == "" {
		return nil
	}
	exp := exponent(order.Currency)
	items := make([]map[string]any, 0, len(order.Lines))
	names := make([]string, 0, len(order.Lines))
	for _, l := range order.Lines {
		items = append(items, map[string]any{
			"ProductName": l.Title, "SKU": l.SKU, "Variant": l.VariantLabel,
			"Quantity": l.Quantity, "ItemPrice": major(l.UnitPriceMinor, exp), "RowTotal": major(l.TotalMinor, exp),
		})
		names = append(names, l.Title)
	}
	props := map[string]any{
		"OrderId": order.Number, "Status": order.Status, "PaymentStatus": order.PaymentStatus,
		"Currency": order.Currency, "Items": items, "ItemNames": names, "ItemCount": len(items),
	}
	if order.Tracking != "" {
		props["TrackingNumber"] = order.Tracking
	}
	if order.Reason != "" {
		props["Reason"] = order.Reason
	}
	value := major(order.TotalMinor, exp)
	if e.Name == gocommerce.EventOrderRefunded && order.Refund != nil {
		value = major(order.Refund.AmountMinor, exp)
	}
	profile := profileOf(order.Email, order.Name, order.Phone)
	m.send(ctx, conn, metric, e.ID, e.At, value, order.Currency, props, profile)

	// One event per line as well, which is what a "bought X, recommend Y"
	// flow segments on.
	if e.Name == gocommerce.EventOrderCreated {
		for _, l := range order.Lines {
			m.send(ctx, conn, "Ordered Product", e.ID+":"+l.SKU, e.At, major(l.TotalMinor, exp), order.Currency, map[string]any{
				"OrderId": order.Number, "ProductName": l.Title, "SKU": l.SKU, "Variant": l.VariantLabel,
				"Quantity": l.Quantity, "ItemPrice": major(l.UnitPriceMinor, exp), "Currency": order.Currency,
			}, profile)
		}
	}
	return nil
}

func (m *Module) onCart(ctx context.Context, e gocommerce.Event) error {
	conn, err := m.connection(ctx)
	if err != nil || !conn.enabled || conn.key == "" || !conn.trackCarts {
		return nil
	}
	var cart gocommerce.CartEvent
	if err := json.Unmarshal(e.Data, &cart); err != nil || cart.Email == "" {
		return nil
	}
	exp := exponent(cart.Currency)
	items := make([]map[string]any, 0, len(cart.Lines))
	for _, l := range cart.Lines {
		items = append(items, map[string]any{
			"ProductName": l.Title, "SKU": l.SKU, "Variant": l.VariantLabel,
			"Quantity": l.Quantity, "ItemPrice": major(l.UnitPriceMinor, exp),
		})
	}
	m.send(ctx, conn, "Started Checkout", e.ID, e.At, major(cart.SubtotalMinor, exp), cart.Currency, map[string]any{
		"CartToken": cart.Token, "Items": items, "ItemCount": cart.ItemCount, "Currency": cart.Currency,
		"DiscountCode": cart.DiscountCode,
	}, profileOf(cart.Email, "", ""))
	return nil
}

func profileOf(email, name, phone string) map[string]any {
	p := map[string]any{"email": strings.ToLower(strings.TrimSpace(email))}
	if name = strings.TrimSpace(name); name != "" {
		first, last := name, ""
		if i := strings.LastIndex(name, " "); i > 0 {
			first, last = name[:i], name[i+1:]
		}
		p["first_name"] = first
		if last != "" {
			p["last_name"] = last
		}
	}
	if phone != "" {
		p["phone_number"] = phone
	}
	return p
}

// send is one event to Klaviyo, tried twice, never an error to the caller.
func (m *Module) send(ctx context.Context, conn connection, metric, uniqueID string, at time.Time, value float64, currency string, props map[string]any, profile map[string]any) {
	body := map[string]any{
		"data": map[string]any{
			"type": "event",
			"attributes": map[string]any{
				"properties":     props,
				"time":           at.UTC().Format(time.RFC3339),
				"value":          value,
				"value_currency": currency,
				"unique_id":      uniqueID,
				"metric":         map[string]any{"data": map[string]any{"type": "metric", "attributes": map[string]any{"name": metric}}},
				"profile":        map[string]any{"data": map[string]any{"type": "profile", "attributes": profile}},
			},
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return
	}
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
		if last = m.post(ctx, conn, encoded); last == nil {
			m.mu.Lock()
			m.sent++
			m.lastSent = time.Now()
			m.lastErr = ""
			m.mu.Unlock()
			return
		}
	}
	m.mu.Lock()
	m.failed++
	m.lastErr = last.Error()
	m.mu.Unlock()
	m.log.Warn("klaviyo event not delivered", "metric", metric, "error", last)
}

func (m *Module) post(ctx context.Context, conn connection, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, conn.base+"/api/events/", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Klaviyo-API-Key "+conn.key)
	req.Header.Set("revision", revision)
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("klaviyo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var fail struct {
			Errors []struct {
				Detail string `json:"detail"`
			} `json:"errors"`
		}
		if json.Unmarshal(payload, &fail) == nil && len(fail.Errors) > 0 {
			return fmt.Errorf("klaviyo: %s (%s)", fail.Errors[0].Detail, resp.Status)
		}
		return fmt.Errorf("klaviyo: %s", resp.Status)
	}
	return nil
}

// ----------------------------------------------------------------- money

// exponent is how many decimals a currency has; Klaviyo wants a major-unit
// value, and the engine holds minor units (rule 6).
func exponent(code string) int {
	switch strings.ToUpper(code) {
	case "BIF", "CLP", "DJF", "GNF", "ISK", "JPY", "KMF", "KRW", "PYG", "RWF", "UGX", "VND", "VUV", "XAF", "XOF", "XPF":
		return 0
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND":
		return 3
	}
	return 2
}

func major(minor int64, exp int) float64 {
	scale := 1.0
	for i := 0; i < exp; i++ {
		scale *= 10
	}
	return float64(minor) / scale
}

// ---------------------------------------------------------------- routes

func (m *Module) handleStatus(w http.ResponseWriter, r *http.Request) {
	conn, err := m.connection(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	m.mu.Lock()
	st := Status{Enabled: conn.enabled, Configured: conn.key != "", Sent: m.sent, Failed: m.failed, LastError: m.lastErr}
	if !m.lastSent.IsZero() {
		at := m.lastSent
		st.LastSent = &at
	}
	m.mu.Unlock()
	gocommerce.Respond(w, http.StatusOK, st)
}
