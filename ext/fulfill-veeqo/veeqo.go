// Package veeqo records shipments against a Veeqo order.
//
// Registering it adds "veeqo" as a fulfillment provider, so an operator ships
// by posting to the engine's own /api/admin/create-fulfillment with
// `"provider": "veeqo"` and the tracking number of the parcel.
//
//	app, err := gocommerce.New(cfg,
//	    veeqo.New(veeqo.Config{APIKey: os.Getenv("VEEQO_API_KEY")}),
//	)
//
// REST over net/http and no SDK, per rule 2.
//
// # Which way the data flows
//
// Veeqo is a warehouse and inventory system, not a carrier. A store that uses
// it already has its orders there — Veeqo pulls them from the sales channel —
// and what it does not have is the fact that a parcel went out. So this module
// pushes that fact back: it finds the Veeqo order, takes the allocation the
// warehouse picked against, and records a shipment with the tracking number.
//
// The engine stays the system of record. Veeqo learns what happened; it does
// not decide it.
//
// # Why it does not buy the label
//
// Veeqo can buy a label, and this module deliberately does not. Doing so needs
// a rate quote first, six fields copied out of a chosen quote, and a carrier
// fixed to Amazon's shipping — and the purchase response carries a label URL
// and a tracking *URL* but no tracking number, which is the one thing the
// engine needs to record a shipment. Choosing a rate is also a decision about
// price and delivery date that belongs to whoever is packing the parcel.
//
// A store that wants Veeqo to buy labels should do it in Veeqo, where the rates
// are in front of a person, and let this module record the result.
//
// # Finding the allocation
//
// An operator who knows can pass Meta["allocation_id"] and Meta["order_id"],
// and this module uses them. Otherwise it searches Veeqo for an order whose
// number matches the store's and takes its first allocation — which is right
// for the ordinary case of one warehouse and one parcel, and which refuses
// rather than guesses when the search finds nothing or finds several.
package veeqo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/misiki/gocommerce/core"
)

const (
	defaultBaseURL = "https://api.veeqo.com"
	maxBodyRead    = 1 << 20
	// Veeqo's own id for "Other", which is what a parcel booked outside Veeqo
	// is until somebody says otherwise.
	carrierOther = 3
)

// Config configures the module.
type Config struct {
	// APIKey is a Veeqo API key, sent as the x-api-key header. Required.
	APIKey string `plugin:"api_key"`
	// CarrierID is Veeqo's numeric id for the carrier that took the parcel.
	// Defaults to 3, which is Veeqo's "Other" — right for a parcel booked
	// outside Veeqo, and worth setting when the store always uses one carrier.
	CarrierID int `plugin:"carrier_id"`
	// NotifyCustomer lets Veeqo send its own shipping email. Off by default:
	// the engine already notifies on order.shipped, and two emails about one
	// parcel is how a shop looks disorganised.
	NotifyCustomer bool `plugin:"notify_customer"`
	// UpdateRemoteOrder asks Veeqo to push the shipment on to whichever sales
	// channel the order came from. Off by default, because this store is that
	// channel — and a round trip back into the engine is the loop nobody wants.
	UpdateRemoteOrder bool `plugin:"update_remote_order"`
	// BaseURL overrides the endpoint, for tests.
	BaseURL string `plugin:"base_url"`
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the Veeqo fulfillment provider.
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
const PluginKey = "shipping-veeqo"

var errNotConfigured = errors.New("veeqo: not set up — activate and configure it under Settings")

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
	if c.CarrierID <= 0 {
		c.CarrierID = carrierOther
	}
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "fulfill-veeqo" }

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
		Key: PluginKey, Title: "Veeqo", Category: "shipping", DefaultEnabled: inCode,
		Description: "Tells Veeqo a parcel went out, so its stock and channels follow. Labels are bought elsewhere.",
		Docs:        "https://developers.veeqo.com/",
		Fields: []gocommerce.PluginField{
			{Key: "api_key", Label: "API key", Kind: "secret", Required: !inCode, Help: "Sent as the x-api-key header."},
			{Key: "carrier_id", Label: "Carrier ID", Kind: "number", Help: "Veeqo's numeric id for the carrier; 3 is Other.", Default: 3},
			{Key: "notify_customer", Label: "Let Veeqo email the customer", Kind: "bool", Help: "The engine already writes on order.shipped; two emails about one parcel reads as disorganised."},
			{Key: "update_remote_order", Label: "Push the shipment to the sales channel", Kind: "bool", Help: "This store is that channel; off by default."},
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
func (m *Module) Code() string { return "veeqo" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "Veeqo" }

// Ship records the shipment against the Veeqo order.
//
// It runs before the engine opens its transaction, so Veeqo having a slow
// minute never holds a lock on the orders table.
func (m *Module) Ship(ctx context.Context, order *gocommerce.Order, req gocommerce.ShipRequest) (gocommerce.Shipment, error) {
	if !m.refresh(ctx) {
		return gocommerce.Shipment{}, errNotConfigured
	}
	tracking := strings.TrimSpace(req.Tracking)
	if tracking == "" {
		return gocommerce.Shipment{}, errors.New(
			"veeqo: a tracking number is required — this provider records a parcel that has gone out, it does not buy a label")
	}

	orderID, allocationID, err := m.locate(ctx, order, req)
	if err != nil {
		return gocommerce.Shipment{}, err
	}

	carrierID := m.conf().CarrierID
	if raw := req.Meta["carrier_id"]; raw != "" {
		if parsed, convErr := strconv.Atoi(raw); convErr == nil && parsed > 0 {
			carrierID = parsed
		}
	}

	body := map[string]any{
		"allocation_id": allocationID,
		"order_id":      orderID,
		"shipment": map[string]any{
			"tracking_number_attributes": map[string]any{"tracking_number": tracking},
			"carrier_id":                 carrierID,
			"notify_customer":            m.conf().NotifyCustomer,
			"update_remote_order":        m.conf().UpdateRemoteOrder,
		},
	}

	var out struct {
		ID      int64 `json:"id"`
		Carrier struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"carrier"`
		TrackingNumber struct {
			TrackingNumber string `json:"tracking_number"`
		} `json:"tracking_number"`
	}
	if err := m.do(ctx, http.MethodPost, "/shipments", body, &out); err != nil {
		return gocommerce.Shipment{}, err
	}
	m.log.Info("recorded a shipment in Veeqo", "order_id", order.ID,
		"veeqo_order_id", orderID, "veeqo_shipment_id", out.ID)

	return gocommerce.Shipment{
		Provider: m.Code(),
		// The number the operator gave, not the one Veeqo echoed: they are the
		// same, and if they are not, what actually went out is the truth.
		Tracking: tracking,
		// Read from what Veeqo called the carrier, so a store that set a real
		// carrier id gets a tracking link. Unknown is left empty and the engine
		// works it out from the number, as it does for a manual shipment.
		Carrier: carrierFor(out.Carrier.Name, req.Carrier),
	}, nil
}

// locate works out which Veeqo order and allocation this parcel belongs to.
func (m *Module) locate(ctx context.Context, order *gocommerce.Order, req gocommerce.ShipRequest) (orderID, allocationID int64, err error) {
	// The operator knows, which beats anything this module could work out.
	if raw := req.Meta["allocation_id"]; raw != "" {
		allocationID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("veeqo: allocation_id %q is not a number", raw)
		}
		if raw := req.Meta["order_id"]; raw != "" {
			orderID, err = strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return 0, 0, fmt.Errorf("veeqo: order_id %q is not a number", raw)
			}
			return orderID, allocationID, nil
		}
	}

	var found []struct {
		ID          int64  `json:"id"`
		Number      string `json:"number"`
		Allocations []struct {
			ID int64 `json:"id"`
		} `json:"allocations"`
	}
	query := "/orders?" + url.Values{
		"query":     {order.Number},
		"page_size": {"25"},
	}.Encode()
	if err := m.do(ctx, http.MethodGet, query, nil, &found); err != nil {
		return 0, 0, err
	}

	var matches []int
	for i, candidate := range found {
		// Veeqo's search is fuzzy, so "GC-100" also returns "GC-1001". Only an
		// exact number is this order.
		if strings.EqualFold(strings.TrimSpace(candidate.Number), order.Number) {
			matches = append(matches, i)
		}
	}
	switch {
	case len(matches) == 0:
		return 0, 0, fmt.Errorf(
			"veeqo: no Veeqo order is numbered %s — either it has not synced yet, or pass Meta[\"allocation_id\"] and Meta[\"order_id\"]",
			order.Number)
	case len(matches) > 1:
		return 0, 0, fmt.Errorf(
			"veeqo: %d Veeqo orders are numbered %s — pass Meta[\"allocation_id\"] and Meta[\"order_id\"] to say which",
			len(matches), order.Number)
	}

	match := found[matches[0]]
	if len(match.Allocations) == 0 {
		return 0, 0, fmt.Errorf(
			"veeqo: Veeqo order %d has no allocation to ship against — allocate it to a warehouse first", match.ID)
	}
	if len(match.Allocations) > 1 {
		// Several warehouses are each sending part of it, and picking the
		// first would record the whole parcel against one of them.
		return 0, 0, fmt.Errorf(
			"veeqo: Veeqo order %d has %d allocations — pass Meta[\"allocation_id\"] to say which one this parcel is",
			match.ID, len(match.Allocations))
	}
	if allocationID == 0 {
		allocationID = match.Allocations[0].ID
	}
	return match.ID, allocationID, nil
}

// carrierFor turns what Veeqo calls the carrier into one of the engine's
// codes, falling back to whatever the operator said on the ship request.
//
// Unknown returns "", which is not a failure: the engine then works the carrier
// out from the tracking number, which is what it does for a manual shipment.
func carrierFor(names ...string) string {
	for _, name := range names {
		switch n := strings.ToLower(name); {
		case n == "", n == "other":
			continue
		case strings.Contains(n, "usps"):
			return "usps"
		case strings.Contains(n, "fedex"):
			return "fedex"
		case strings.Contains(n, "ups"):
			return "ups"
		case strings.Contains(n, "dhl"):
			return "dhl"
		case strings.Contains(n, "royal mail"):
			return "royal-mail"
		case strings.Contains(n, "dpd"):
			return "dpd"
		case strings.Contains(n, "australia post"):
			return "australia-post"
		case strings.Contains(n, "canada post"):
			return "canada-post"
		default:
			// An operator who typed a carrier code on the ship request meant
			// it, so long as it is one the engine knows.
			if _, ok := gocommerce.CarrierByCode(name, ""); ok {
				return name
			}
		}
	}
	return ""
}

func (m *Module) do(ctx context.Context, method, path string, payload any, out any) error {
	var reader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("veeqo: could not encode the request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.conf().BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", m.conf().APIKey)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("veeqo: %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyRead))
	if err != nil {
		return fmt.Errorf("veeqo: %s: read response: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Error   string   `json:"error"`
			Message string   `json:"message"`
			Errors  []string `json:"errors"`
		}
		if json.Unmarshal(body, &apiErr) == nil {
			if detail := firstNonEmpty(apiErr.Message, apiErr.Error, strings.Join(apiErr.Errors, "; ")); detail != "" {
				return fmt.Errorf("veeqo: %s: %s", path, detail)
			}
		}
		return fmt.Errorf("veeqo: %s returned %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
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
