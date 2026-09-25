// Package shiprocket books shipments through Shiprocket.
//
// Registering it adds "shiprocket" as a fulfillment provider, so an operator
// ships by posting to the engine's own /api/admin/create-fulfillment with
// `"provider": "shiprocket"` â€” the engine still owns the order's state and its
// events, and this module only talks to the carrier.
//
//	app, err := gocommerce.New(cfg,
//	    shiprocket.New(shiprocket.Config{
//	        Email:    os.Getenv("SHIPROCKET_EMAIL"),
//	        Password: os.Getenv("SHIPROCKET_PASSWORD"),
//	        PickupLocation: "Primary",
//	    }),
//	)
package shiprocket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/itswadesh/gocommerce/core"
)

const (
	defaultBaseURL = "https://apiv2.shiprocket.in"
	// Shiprocket tokens last ten days; refreshing a day early avoids the
	// awkward case of a token expiring between two calls of one shipment.
	tokenLifetime = 9 * 24 * time.Hour
)

// Config configures the module.
type Config struct {
	// Email and Password are the Shiprocket API user's credentials. Required.
	Email    string `plugin:"email"`
	Password string `plugin:"password"`
	// PickupLocation is the nickname of the pickup address registered in
	// Shiprocket. Required â€” the carrier has to collect the parcel somewhere.
	PickupLocation string `plugin:"pickup_location"`
	// DefaultWeightKg is the last resort, per unit, for a parcel the engine
	// could not weigh â€” one holding a variant with no weight recorded. A
	// catalogue that is fully weighed never reaches it.
	DefaultWeightKg float64 `plugin:"default_weight_kg"`
	// DefaultLengthCm and friends are the last resort for a parcel the engine
	// has no unambiguous size for, which is any parcel holding more than a
	// single unit. See gocommerce.Parcel.
	DefaultLengthCm  float64
	DefaultBreadthCm float64
	DefaultHeightCm  float64
	// BaseURL overrides the endpoint, for tests.
	BaseURL string `plugin:"base_url"`
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the Shiprocket fulfillment provider.
type Module struct {
	cfg    Config
	live   atomic.Pointer[Config]
	app    *gocommerce.App
	client *http.Client
	log    *slog.Logger

	mu        sync.Mutex
	token     string
	tokenTime time.Time
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// PluginKey is the plugin this module registers, where the panel keeps its
// credentials. Config is the environment's fallback for each field; a value
// typed into the panel wins.
const PluginKey = "shipping-shiprocket"

var errNotConfigured = errors.New("shiprocket: not set up — activate and configure it under Settings")

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
	return strings.TrimSpace(c.Email) != "" &&
		strings.TrimSpace(c.Password) != "" &&
		strings.TrimSpace(c.PickupLocation) != ""
}

// finish fills a configuration's defaults.
func (m *Module) finish(c *Config) {
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	if c.DefaultWeightKg <= 0 {
		c.DefaultWeightKg = 0.5
	}
	if c.DefaultLengthCm <= 0 {
		c.DefaultLengthCm, c.DefaultBreadthCm, c.DefaultHeightCm = 15, 15, 10
	}
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "fulfill-shiprocket" }

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
		Key: PluginKey, Title: "Shiprocket", Category: "shipping", DefaultEnabled: inCode,
		Description: "Books shipments and waybills through Shiprocket, India's aggregator: one login across its couriers.",
		Docs:        "https://apidocs.shiprocket.in/",
		Fields: []gocommerce.PluginField{
			{Key: "email", Label: "API user email", Kind: "text", Required: !inCode, Help: "The API user's login, not the account owner's."},
			{Key: "password", Label: "API user password", Kind: "secret", Required: !inCode},
			{Key: "pickup_location", Label: "Pickup location", Kind: "text", Required: !inCode, Help: "The pickup address nickname registered in Shiprocket."},
			{Key: "default_weight_kg", Label: "Default weight (kg)", Kind: "number", Help: "Per unit, for a variant with no weight recorded.", Default: 0.5},
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
func (m *Module) Code() string { return "shiprocket" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "Shiprocket" }

// Ship creates the order in Shiprocket and assigns a waybill.
//
// It runs before the engine opens its transaction, so a carrier having a slow
// minute never holds a lock on the orders table.
func (m *Module) Ship(ctx context.Context, order *gocommerce.Order, req gocommerce.ShipRequest) (gocommerce.Shipment, error) {
	if !m.refresh(ctx) {
		return gocommerce.Shipment{}, errNotConfigured
	}
	shipmentID, err := m.createOrder(ctx, order, req)
	if err != nil {
		return gocommerce.Shipment{}, err
	}

	awb, label, err := m.assignAWB(ctx, shipmentID, req.Meta["courier_id"])
	if err != nil {
		// The order exists in Shiprocket but has no waybill. Say so precisely:
		// the operator needs to know the parcel is half-booked rather than
		// not booked at all.
		return gocommerce.Shipment{}, fmt.Errorf(
			"shiprocket: order %s was created (shipment %d) but no waybill could be assigned: %w",
			order.Number, shipmentID, err)
	}

	return gocommerce.Shipment{
		Provider: m.Code(),
		Tracking: awb,
		LabelURL: label,
	}, nil
}

func (m *Module) createOrder(ctx context.Context, order *gocommerce.Order, req gocommerce.ShipRequest) (int64, error) {
	addr := order.Address
	name := strings.TrimSpace(order.Name)
	if name == "" {
		name = strings.TrimSpace(addr.Name)
	}
	if name == "" {
		name = "Customer"
	}
	first, last := splitName(name)

	// What is declared is what is in this box, not what is on the order. The
	// engine has already resolved req.Lines to explicit quantities â€” on a
	// whole-order shipment that is every line at its full count â€” so a parcel
	// holding one of three lines does not tell the carrier it holds three, and
	// the declared value is not the whole order's.
	byLine := make(map[int64]gocommerce.OrderLine, len(order.Lines))
	for _, line := range order.Lines {
		byLine[line.ID] = line
	}
	items := make([]map[string]any, 0, len(req.Lines))
	var units int
	var subtotalMinor int64
	for _, ship := range req.Lines {
		line, ok := byLine[ship.OrderLineID]
		if !ok {
			continue
		}
		items = append(items, map[string]any{
			"name":          line.Title,
			"sku":           line.SKU,
			"units":         ship.Quantity,
			"selling_price": float64(line.UnitPrice.AmountMinor) / 100,
		})
		units += ship.Quantity
		subtotalMinor += line.UnitPrice.AmountMinor * int64(ship.Quantity)
	}

	// Shiprocket rejects a duplicate external order id, and a second parcel
	// against the same order would be exactly that. len is 0 on the first call â€”
	// the order was read before this shipment's row exists â€” so the first
	// payload is byte-identical to the one this module always sent.
	externalID := order.Number
	if n := len(order.Fulfillments); n > 0 {
		externalID = fmt.Sprintf("%s-%d", order.Number, n+1)
	}

	payload := map[string]any{
		"order_id":              externalID,
		"order_date":            order.CreatedAt.UTC().Format("2006-01-02 15:04"),
		"pickup_location":       m.conf().PickupLocation,
		"billing_customer_name": first,
		"billing_last_name":     last,
		"billing_address":       addr.Line1,
		"billing_address_2":     addr.Line2,
		"billing_city":          addr.City,
		"billing_pincode":       addr.PostalCode,
		"billing_state":         addr.State,
		"billing_country":       addr.Country,
		"billing_email":         order.Email,
		"billing_phone":         firstNonEmpty(order.Phone, addr.Phone),
		"shipping_is_billing":   true,
		"order_items":           items,
		"payment_method":        paymentMethodFor(order),
		"sub_total":             float64(subtotalMinor) / 100,
		// Three sources, in this order: what the operator typed, what the
		// engine measured from the variants, and the configured fallback. The
		// operator comes first because they are holding the box; the measured
		// figure comes before the default because a catalogue somebody took
		// the trouble to weigh should not be overruled by a guess â€” which is
		// exactly what this module used to do.
		"length":  m.dimension(req.Meta, "length_cm", centimetres(req.Parcel.Dimensions.Length, m.conf().DefaultLengthCm)),
		"breadth": m.dimension(req.Meta, "breadth_cm", centimetres(req.Parcel.Dimensions.Width, m.conf().DefaultBreadthCm)),
		"height":  m.dimension(req.Meta, "height_cm", centimetres(req.Parcel.Dimensions.Height, m.conf().DefaultHeightCm)),
		"weight":  m.dimension(req.Meta, "weight_kg", m.weight(req.Parcel, units)),
	}

	var created struct {
		OrderID    int64  `json:"order_id"`
		ShipmentID int64  `json:"shipment_id"`
		Status     string `json:"status"`
		Message    string `json:"message"`
	}
	if err := m.post(ctx, "/v1/external/orders/create/adhoc", payload, &created); err != nil {
		return 0, err
	}
	if created.ShipmentID == 0 {
		return 0, fmt.Errorf("shiprocket: no shipment id returned (%s)",
			firstNonEmpty(created.Message, created.Status, "unknown reason"))
	}
	return created.ShipmentID, nil
}

func (m *Module) assignAWB(ctx context.Context, shipmentID int64, courierID string) (awb, label string, err error) {
	payload := map[string]any{"shipment_id": shipmentID}
	if courierID != "" {
		// The operator asked for a specific courier via the ship request's
		// meta, which is exactly what that pass-through is for.
		if id, convErr := strconv.Atoi(courierID); convErr == nil {
			payload["courier_id"] = id
		}
	}

	var assigned struct {
		Response struct {
			Data struct {
				AWBCode string `json:"awb_code"`
				Courier string `json:"courier_name"`
			} `json:"data"`
		} `json:"response"`
		Message string `json:"message"`
	}
	if err := m.post(ctx, "/v1/external/courier/assign/awb", payload, &assigned); err != nil {
		return "", "", err
	}
	if assigned.Response.Data.AWBCode == "" {
		return "", "", fmt.Errorf("shiprocket: no waybill assigned (%s)",
			firstNonEmpty(assigned.Message, "unknown reason"))
	}

	var labelResp struct {
		LabelURL string `json:"label_url"`
	}
	if err := m.post(ctx, "/v1/external/courier/generate/label",
		map[string]any{"shipment_id": []int64{shipmentID}}, &labelResp); err != nil {
		// A missing label is inconvenient, not fatal: the parcel is booked and
		// the label can be printed from Shiprocket's dashboard.
		m.log.Warn("could not generate a Shiprocket label", "shipment_id", shipmentID, "error", err)
	}
	return assigned.Response.Data.AWBCode, labelResp.LabelURL, nil
}

// paymentMethodFor tells the carrier whether to collect money on delivery.
// Getting this wrong means either a courier who does not ask for payment, or
// one who asks a customer who has already paid.
func paymentMethodFor(order *gocommerce.Order) string {
	if order.PaymentStatus == gocommerce.PaymentPaid {
		return "Prepaid"
	}
	return "COD"
}

// weight is what to declare, in kilograms.
//
// The engine's figure is used only when it measured every line: a Parcel whose
// Measured is false has a weight that is a floor, not the parcel's, and
// declaring a floor to a carrier is how a shipment comes back with a
// reweighing charge. In that case the configured default per unit is the more
// honest guess, because it is at least a guess about a whole parcel.
func (m *Module) weight(parcel gocommerce.Parcel, units int) float64 {
	if parcel.Measured && parcel.WeightGrams > 0 {
		return float64(parcel.WeightGrams) / 1000
	}
	// Rounded to the gram, which is all the precision a weight ever has here.
	// Without it, 0.4 kg three times is 1.2000000000000002 on the wire â€” not
	// wrong, but not something to put in front of a carrier either.
	return math.Round(m.conf().DefaultWeightKg*float64(max(units, 1))*1000) / 1000
}

// centimetres converts one of the engine's millimetre sides, falling back when
// the engine had no unambiguous answer â€” which it does not for any parcel
// holding more than a single unit. See gocommerce.Parcel for why.
func centimetres(mm *int, fallback float64) float64 {
	if mm == nil || *mm <= 0 {
		return fallback
	}
	return float64(*mm) / 10
}

func (m *Module) dimension(meta map[string]string, key string, fallback float64) float64 {
	if v, ok := meta[key]; ok {
		if parsed, err := strconv.ParseFloat(v, 64); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

// ------------------------------------------------------------------- transport

// authToken returns a valid bearer token, logging in when necessary.
func (m *Module) authToken(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token != "" && time.Since(m.tokenTime) < tokenLifetime {
		return m.token, nil
	}

	payload := map[string]string{"email": m.conf().Email, "password": m.conf().Password}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		m.conf().BaseURL+"/v1/external/auth/login", bytes.NewReader(encoded))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("shiprocket: log in: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("shiprocket: log in returned %s", resp.Status)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Token == "" {
		return "", errors.New("shiprocket: log in returned no token")
	}
	m.token, m.tokenTime = out.Token, time.Now()
	return m.token, nil
}

func (m *Module) post(ctx context.Context, path string, payload any, out any) error {
	token, err := m.authToken(ctx)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.conf().BaseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("shiprocket: %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("shiprocket: %s: read response: %w", path, err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		// The cached token went stale; drop it so the next attempt logs in.
		m.mu.Lock()
		m.token = ""
		m.mu.Unlock()
		return fmt.Errorf("shiprocket: %s: authentication rejected", path)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &apiErr)
		if apiErr.Message != "" {
			return fmt.Errorf("shiprocket: %s: %s", path, apiErr.Message)
		}
		return fmt.Errorf("shiprocket: %s returned %s", path, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func splitName(full string) (first, last string) {
	parts := strings.Fields(full)
	switch len(parts) {
	case 0:
		return "Customer", ""
	case 1:
		return parts[0], ""
	default:
		return parts[0], strings.Join(parts[1:], " ")
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
