// Package nimbuspost books shipments through NimbusPost.
//
// Registering it adds "nimbuspost" as a fulfillment provider, so an operator
// ships by posting to the engine's own /api/admin/create-fulfillment with
// `"provider": "nimbuspost"` — the engine still owns the order's state and its
// events, and this module only talks to the carrier.
//
//	app, err := gocommerce.New(cfg,
//	    nimbuspost.New(nimbuspost.Config{
//	        Email:         os.Getenv("NIMBUSPOST_EMAIL"),
//	        Password:      os.Getenv("NIMBUSPOST_PASSWORD"),
//	        WarehouseName: "Primary",
//	    }),
//	)
//
// REST over net/http and no SDK, per rule 2.
//
// # An aggregator, not a carrier
//
// NimbusPost picks a courier — Delhivery, XpressBees, Blue Dart and the rest —
// and the shipment comes back naming whichever one took it. So the carrier code
// on the resulting shipment is read from the response rather than assumed, and
// a tracking number belongs to that courier, not to NimbusPost.
//
// # status: false with HTTP 200
//
// NimbusPost answers a refused shipment with 200 and `"status": false`, so the
// HTTP code alone would report a refusal as a shipped order. Every response
// here is checked for the flag before anything is believed.
//
// # Units
//
// NimbusPost takes grams for weight and centimetres for the three sides. The
// engine keeps grams and millimetres, so only the sides are converted.
package nimbuspost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/misiki/gocommerce/core"
)

const (
	defaultBaseURL = "https://api.nimbuspost.com/v1"
	// NimbusPost's token is a JWT with no expiry field the client is told
	// about. Refreshing daily keeps a stale one from outliving a long-running
	// process, and a 401 drops it early anyway.
	tokenLifetime = 24 * time.Hour
	maxBodyRead   = 1 << 20
)

// Config configures the module.
type Config struct {
	// Email and Password are the NimbusPost account's credentials. Required:
	// the API has no long-lived key, only a login that mints a token.
	Email    string `plugin:"email"`
	Password string `plugin:"password"`
	// WarehouseName is the nickname of the pickup warehouse registered with
	// NimbusPost. Required — the carrier has to collect the parcel somewhere.
	WarehouseName string `plugin:"warehouse_name"`
	// Pickup is the pickup address sent with each shipment. NimbusPost matches
	// the warehouse by name; these fields are what gets printed, and leaving
	// them empty leans on whatever the dashboard holds.
	PickupName    string `plugin:"pickup_name"`
	PickupAddress string `plugin:"pickup_address"`
	PickupCity    string `plugin:"pickup_city"`
	PickupState   string `plugin:"pickup_state"`
	PickupPincode string `plugin:"pickup_pincode"`
	PickupPhone   string `plugin:"pickup_phone"`
	// AutoPickup asks NimbusPost to raise the pickup request with the courier
	// as part of booking. Off by default, because scheduling a van is a
	// decision about somebody's afternoon.
	AutoPickup bool `plugin:"auto_pickup"`
	// DefaultWeightGrams is the last resort, per unit, for a parcel the engine
	// could not weigh. A fully weighed catalogue never reaches it.
	DefaultWeightGrams int `plugin:"default_weight_grams"`
	// DefaultLengthCM and friends are the box used when the engine has no
	// unambiguous size — which is any parcel holding more than a single unit.
	// See gocommerce.Parcel.
	DefaultLengthCM  float64
	DefaultBreadthCM float64
	DefaultHeightCM  float64
	// BaseURL overrides the endpoint, for tests.
	BaseURL string `plugin:"base_url"`
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the NimbusPost fulfillment provider.
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
const PluginKey = "shipping-nimbuspost"

var errNotConfigured = errors.New("nimbuspost: not set up — activate and configure it under Settings")

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
		strings.TrimSpace(c.WarehouseName) != ""
}

// finish fills a configuration's defaults.
func (m *Module) finish(c *Config) {
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.DefaultWeightGrams <= 0 {
		c.DefaultWeightGrams = 500
	}
	if c.DefaultLengthCM <= 0 {
		c.DefaultLengthCM, c.DefaultBreadthCM, c.DefaultHeightCM = 15, 15, 10
	}
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "fulfill-nimbuspost" }

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
		Key: PluginKey, Title: "NimbusPost", Category: "shipping", DefaultEnabled: inCode,
		Description: "NimbusPost's courier aggregation, India: one login books across the couriers it holds.",
		Docs:        "https://documentation.nimbuspost.com/",
		Fields: []gocommerce.PluginField{
			{Key: "email", Label: "Account email", Kind: "text", Required: !inCode, Help: "The API user's login."},
			{Key: "password", Label: "Account password", Kind: "secret", Required: !inCode, Help: "The API has no long-lived key, only a login that mints a token."},
			{Key: "warehouse_name", Label: "Warehouse name", Kind: "text", Required: !inCode, Help: "The pickup warehouse registered with NimbusPost, matched exactly."},
			{Key: "pickup_name", Label: "Pickup: name", Kind: "text"},
			{Key: "pickup_address", Label: "Pickup: address", Kind: "text"},
			{Key: "pickup_city", Label: "Pickup: city", Kind: "text"},
			{Key: "pickup_state", Label: "Pickup: state", Kind: "text"},
			{Key: "pickup_pincode", Label: "Pickup: pincode", Kind: "text"},
			{Key: "pickup_phone", Label: "Pickup: phone", Kind: "text"},
			{Key: "auto_pickup", Label: "Raise the pickup request while booking", Kind: "bool", Help: "Scheduling a van is a decision about somebody's afternoon; off by default."},
			{Key: "default_weight_grams", Label: "Default weight (grams)", Kind: "number", Help: "Per unit, for a variant with no weight recorded.", Default: 500},
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
func (m *Module) Code() string { return "nimbuspost" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "NimbusPost" }

// Ship books the shipment and returns the waybill the chosen courier assigned.
//
// It runs before the engine opens its transaction, so a carrier having a slow
// minute never holds a lock on the orders table.
func (m *Module) Ship(ctx context.Context, order *gocommerce.Order, req gocommerce.ShipRequest) (gocommerce.Shipment, error) {
	if !m.refresh(ctx) {
		return gocommerce.Shipment{}, errNotConfigured
	}
	var out struct {
		Data struct {
			OrderID     json.Number `json:"order_id"`
			ShipmentID  json.Number `json:"shipment_id"`
			AWBNumber   string      `json:"awb_number"`
			CourierID   json.Number `json:"courier_id"`
			CourierName string      `json:"courier_name"`
			Label       string      `json:"label"`
		} `json:"data"`
	}
	if err := m.post(ctx, "/shipments", m.shipmentBody(order, req), &out); err != nil {
		return gocommerce.Shipment{}, err
	}
	if out.Data.AWBNumber == "" {
		return gocommerce.Shipment{}, fmt.Errorf(
			"nimbuspost: shipment %s was created but no waybill was assigned — cancel it in NimbusPost before retrying",
			out.Data.ShipmentID)
	}

	return gocommerce.Shipment{
		Provider: m.Code(),
		Tracking: out.Data.AWBNumber,
		Carrier:  carrierFor(out.Data.CourierName),
		LabelURL: out.Data.Label,
	}, nil
}

func (m *Module) shipmentBody(order *gocommerce.Order, req gocommerce.ShipRequest) map[string]any {
	addr := order.Address

	byLine := make(map[int64]gocommerce.OrderLine, len(order.Lines))
	for _, line := range order.Lines {
		byLine[line.ID] = line
	}
	// What is declared is what is in this box, not what is on the order: the
	// engine has already resolved req.Lines to explicit quantities, so a parcel
	// holding one of three lines does not tell the courier it holds three, and
	// the amount collected on delivery is this parcel's.
	items := make([]map[string]any, 0, len(req.Lines))
	var units int
	var valueMinor int64
	for _, ship := range req.Lines {
		line, ok := byLine[ship.OrderLineID]
		if !ok {
			continue
		}
		units += ship.Quantity
		valueMinor += line.UnitPrice.AmountMinor * int64(ship.Quantity)
		items = append(items, map[string]any{
			"name":  line.Title,
			"qty":   ship.Quantity,
			"price": float64(line.UnitPrice.AmountMinor) / 100,
			"sku":   line.SKU,
		})
	}

	// A paid order collects nothing. Getting this wrong means either a courier
	// who does not ask for money, or one who asks a customer who has paid.
	paymentType := "cod"
	if order.PaymentStatus == gocommerce.PaymentPaid {
		paymentType = "prepaid"
	}

	// NimbusPost rejects a duplicate order number, and a second parcel against
	// the same order would be exactly that. len is 0 on the first call, so a
	// store that ships whole orders sends exactly the order number.
	externalID := order.Number
	if n := len(order.Fulfillments); n > 0 {
		externalID = fmt.Sprintf("%s-%d", order.Number, n+1)
	}

	autoPickup := "no"
	if m.conf().AutoPickup {
		autoPickup = "yes"
	}

	body := map[string]any{
		"order_number":        externalID,
		"payment_type":        paymentType,
		"order_amount":        float64(valueMinor) / 100,
		"package_weight":      m.weight(req, units),
		"package_length":      m.side(req.Parcel.Dimensions.Length, m.conf().DefaultLengthCM),
		"package_breadth":     m.side(req.Parcel.Dimensions.Width, m.conf().DefaultBreadthCM),
		"package_height":      m.side(req.Parcel.Dimensions.Height, m.conf().DefaultHeightCM),
		"request_auto_pickup": autoPickup,
		"consignee": map[string]any{
			"name":      firstNonEmpty(addr.Name, order.Name, "Customer"),
			"address":   addr.Line1,
			"address_2": addr.Line2,
			"city":      addr.City,
			"state":     addr.State,
			"pincode":   addr.PostalCode,
			"phone":     firstNonEmpty(addr.Phone, order.Phone),
		},
		"pickup": map[string]any{
			"warehouse_name": m.conf().WarehouseName,
			"name":           firstNonEmpty(m.conf().PickupName, m.conf().WarehouseName),
			"address":        m.conf().PickupAddress,
			"city":           m.conf().PickupCity,
			"state":          m.conf().PickupState,
			"pincode":        m.conf().PickupPincode,
			"phone":          m.conf().PickupPhone,
		},
		"order_items": items,
	}
	// The operator asked for a specific courier, which is exactly what the
	// ship request's pass-through is for.
	if courier := req.Meta["courier_id"]; courier != "" {
		body["courier_id"] = courier
	}
	return body
}

// weight is what to declare, in grams.
//
// The engine's figure is used only when it measured every line: a Parcel whose
// Measured is false has a weight that is a floor, not the parcel's, and
// declaring a floor to a carrier is how a shipment comes back with a
// reweighing charge.
func (m *Module) weight(req gocommerce.ShipRequest, units int) int {
	if req.Parcel.Measured && req.Parcel.WeightGrams > 0 {
		return req.Parcel.WeightGrams
	}
	return m.conf().DefaultWeightGrams * max(units, 1)
}

// side converts a millimetre side to centimetres, keeping the tenth.
func (m *Module) side(mm *int, fallback float64) float64 {
	if mm == nil || *mm <= 0 {
		return fallback
	}
	return float64(*mm) / 10
}

// carrierFor turns the courier NimbusPost chose into one of the engine's
// carrier codes, so the tracking number gets the right tracking URL.
//
// Unknown returns "", which is not a failure: the engine then works the carrier
// out from the tracking number, which is what it does for a manual shipment.
func carrierFor(courier string) string {
	switch c := strings.ToLower(courier); {
	case c == "":
		return ""
	case strings.Contains(c, "delhivery"):
		return "delhivery"
	case strings.Contains(c, "xpressbees"):
		return "xpressbees"
	case strings.Contains(c, "bluedart"), strings.Contains(c, "blue dart"):
		return "bluedart"
	case strings.Contains(c, "ecom"):
		return "ecom-express"
	case strings.Contains(c, "dtdc"):
		return "dtdc"
	case strings.Contains(c, "shadowfax"):
		return "shadowfax"
	case strings.Contains(c, "ekart"):
		return "ekart"
	case strings.Contains(c, "amazon"):
		return "amazon-shipping"
	case strings.Contains(c, "india post"), strings.Contains(c, "indiapost"):
		return "india-post"
	case strings.Contains(c, "dhl"):
		return "dhl"
	case strings.Contains(c, "fedex"):
		return "fedex"
	}
	return ""
}

// ------------------------------------------------------------------- transport

// authToken returns a valid bearer token, logging in when necessary.
func (m *Module) authToken(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token != "" && time.Since(m.tokenTime) < tokenLifetime {
		return m.token, nil
	}

	encoded, err := json.Marshal(map[string]string{
		"email": m.conf().Email, "password": m.conf().Password,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		m.conf().BaseURL+"/users/login", bytes.NewReader(encoded))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("nimbuspost: log in: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyRead))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("nimbuspost: log in returned %s", resp.Status)
	}
	var out struct {
		Status  bool   `json:"status"`
		Data    string `json:"data"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("nimbuspost: could not read the login response: %w", err)
	}
	if !out.Status || out.Data == "" {
		return "", fmt.Errorf("nimbuspost: log in refused: %s",
			firstNonEmpty(out.Message, "no reason given"))
	}
	m.token, m.tokenTime = out.Data, time.Now()
	return m.token, nil
}

func (m *Module) post(ctx context.Context, path string, payload any, out any) error {
	token, err := m.authToken(ctx)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("nimbuspost: could not encode the request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		m.conf().BaseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("nimbuspost: %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyRead))
	if err != nil {
		return fmt.Errorf("nimbuspost: %s: read response: %w", path, err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		// The cached token went stale; drop it so the next attempt logs in.
		m.mu.Lock()
		m.token = ""
		m.mu.Unlock()
		return fmt.Errorf("nimbuspost: %s: authentication rejected", path)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("nimbuspost: %s returned %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}

	// The flag, not the status code, is what says whether NimbusPost did
	// anything — see the package comment.
	var envelope struct {
		Status  bool            `json:"status"`
		Message json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("nimbuspost: %s: could not read the response: %w", path, err)
	}
	if !envelope.Status {
		return fmt.Errorf("nimbuspost: %s was refused: %s", path, message(envelope.Message))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// message renders NimbusPost's reason, which is a string on some responses and
// a list of them on others.
func message(raw json.RawMessage) string {
	if len(raw) > 0 {
		var single string
		if json.Unmarshal(raw, &single) == nil && strings.TrimSpace(single) != "" {
			return single
		}
		var list []string
		if json.Unmarshal(raw, &list) == nil && len(list) > 0 {
			return strings.Join(list, "; ")
		}
	}
	return "NimbusPost gave no reason"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
