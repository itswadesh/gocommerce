// Package shippit books shipments through Shippit.
//
// Registering it adds "shippit" as a fulfillment provider, so an operator ships
// by posting to the engine's own /api/admin/create-fulfillment with
// `"provider": "shippit"` — the engine still owns the order's state and its
// events, and this module only talks to the carrier.
//
//	app, err := gocommerce.New(cfg,
//	    shippit.New(shippit.Config{
//	        APIKey:      os.Getenv("SHIPPIT_API_KEY"),
//	        CourierType: "standard",
//	    }),
//	)
//
// REST over net/http and no SDK, per rule 2.
//
// # Two calls, and the second is allowed to fail
//
// Shippit books the order and issues the label separately: POST /orders comes
// back with a tracking number, and GET /orders/{tracking}/label comes back with
// a pre-signed URL to print. The booking is what matters — once it succeeds the
// parcel is Shippit's problem — so a label that cannot be fetched is logged and
// the shipment still stands. Failing the shipment over a missing PDF would
// leave a booked parcel against an order the engine says is unshipped, which is
// the worse of the two wrong answers.
//
// The label URL Shippit returns is pre-signed and expires after seven days. It
// is stored as it comes; a store that needs it later re-fetches it from
// Shippit rather than expecting the engine to have kept a live link.
//
// # Service, not price
//
// CourierType picks the service band — "standard", "express", "priority", or
// "plain_label" for a store that books its own carrier and only wants the
// paperwork. Which carrier fills that band is Shippit's allocation logic, and
// the response says who took it. Naming a specific carrier is possible with
// Meta["courier_allocation"], for the parcel an operator has already promised
// to somebody.
//
// # Units
//
// Shippit reads **kilograms** and **metres**. The engine keeps grams and
// millimetres, so both are divided by a thousand — which makes metres the one
// unit in this repository where a typical parcel is a number smaller than one,
// and worth reading twice.
package shippit

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
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/misiki/gocommerce/core"
)

const (
	defaultBaseURL = "https://app.shippit.com/api/3"
	maxBodyRead    = 1 << 20
)

// Config configures the module.
type Config struct {
	// APIKey is a Shippit API key, sent as a bearer token. Required.
	APIKey string `plugin:"api_key"`
	// CourierType is the service band to book: "standard", "express",
	// "priority", or "plain_label" for label-only. Required as the default; a
	// shipment can override it with Meta["courier_type"].
	CourierType string `plugin:"courier_type"`
	// AuthorityToLeave says whether a courier may leave a parcel at the door
	// when nobody answers. Off by default: authorising it is the shopper's
	// call, and a store that collects the answer should pass it per shipment
	// with Meta["authority_to_leave"].
	AuthorityToLeave bool `plugin:"authority_to_leave"`
	// DefaultWeightGrams is the last resort, per unit, for a parcel the engine
	// could not weigh. A fully weighed catalogue never reaches it.
	DefaultWeightGrams int `plugin:"default_weight_grams"`
	// DefaultLengthMM and friends are the box used when the engine has no
	// unambiguous size — which is any parcel holding more than a single unit.
	// See gocommerce.Parcel.
	DefaultLengthMM int
	DefaultWidthMM  int
	DefaultHeightMM int
	// BaseURL overrides the endpoint: the staging host is
	// https://app.staging.shippit.com/api/3 and takes a different key.
	BaseURL string `plugin:"base_url"`
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the Shippit fulfillment provider.
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
const PluginKey = "shipping-shippit"

var errNotConfigured = errors.New("shippit: not set up — activate and configure it under Settings")

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
	return strings.TrimSpace(c.APIKey) != "" &&
		strings.TrimSpace(c.CourierType) != ""
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
	if c.DefaultLengthMM <= 0 {
		c.DefaultLengthMM, c.DefaultWidthMM, c.DefaultHeightMM = 150, 150, 100
	}
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "fulfill-shippit" }

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
		Key: PluginKey, Title: "Shippit", Category: "shipping", DefaultEnabled: inCode,
		Description: "Shippit's carrier allocation, Australia and New Zealand: pick a service band and Shippit picks the courier.",
		Docs:        "https://developer.shippit.com/",
		Fields: []gocommerce.PluginField{
			{Key: "api_key", Label: "API key", Kind: "secret", Required: !inCode, Help: "Sent as a bearer token. Staging takes a different key."},
			{Key: "courier_type", Label: "Service band", Kind: "select", Required: !inCode, Help: "The default; a shipment can override it.", Options: []string{"standard", "express", "priority", "plain_label"}},
			{Key: "authority_to_leave", Label: "Authority to leave", Kind: "bool", Help: "Whether a courier may leave a parcel at the door; the shopper's call, really."},
			{Key: "default_weight_grams", Label: "Default weight (grams)", Kind: "number", Help: "Per unit, for a variant with no weight recorded.", Default: 500},
			{Key: "base_url", Label: "API base URL", Kind: "url", Help: "Empty for production; https://app.staging.shippit.com/api/3 for staging."},
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
func (m *Module) Code() string { return "shippit" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "Shippit" }

// Ship books the order and fetches its label.
//
// It runs before the engine opens its transaction, so a carrier having a slow
// minute never holds a lock on the orders table.
func (m *Module) Ship(ctx context.Context, order *gocommerce.Order, req gocommerce.ShipRequest) (gocommerce.Shipment, error) {
	if !m.refresh(ctx) {
		return gocommerce.Shipment{}, errNotConfigured
	}
	var booked struct {
		Response struct {
			ID             int64  `json:"id"`
			TrackingNumber string `json:"tracking_number"`
			CourierJobID   string `json:"courier_job_id"`
			CourierName    string `json:"courier_name"`
			State          string `json:"state"`
		} `json:"response"`
	}
	if err := m.do(ctx, http.MethodPost, "/orders",
		map[string]any{"order": m.orderBody(order, req)}, &booked); err != nil {
		return gocommerce.Shipment{}, err
	}
	tracking := strings.TrimSpace(booked.Response.TrackingNumber)
	if tracking == "" {
		return gocommerce.Shipment{}, fmt.Errorf(
			"shippit: order %d was created but carries no tracking number (state %q)",
			booked.Response.ID, booked.Response.State)
	}

	return gocommerce.Shipment{
		Provider: m.Code(),
		Tracking: tracking,
		Carrier:  carrierFor(booked.Response.CourierName),
		LabelURL: m.label(ctx, tracking),
	}, nil
}

// label fetches the printable label. The booking already succeeded by the time
// this runs, so a failure here is logged rather than returned: an order left
// unshipped against a parcel Shippit has accepted is the worse wrong answer.
func (m *Module) label(ctx context.Context, tracking string) string {
	var out struct {
		Response struct {
			QualifiedURL string `json:"qualified_url"`
		} `json:"response"`
	}
	if err := m.do(ctx, http.MethodGet,
		"/orders/"+url.PathEscape(tracking)+"/label", nil, &out); err != nil {
		m.log.Warn("could not fetch a Shippit label", "tracking", tracking, "error", err)
		return ""
	}
	return out.Response.QualifiedURL
}

func (m *Module) orderBody(order *gocommerce.Order, req gocommerce.ShipRequest) map[string]any {
	addr := order.Address
	first, last := splitName(firstNonEmpty(addr.Name, order.Name, "Customer"))

	authority := "No"
	if m.conf().AuthorityToLeave {
		authority = "Yes"
	}
	// The shopper's answer, if the storefront collected one, beats the store's
	// default — leaving a parcel at the door is their risk, not the store's.
	if raw := req.Meta["authority_to_leave"]; raw != "" {
		if strings.EqualFold(raw, "yes") || raw == "true" {
			authority = "Yes"
		} else {
			authority = "No"
		}
	}

	body := map[string]any{
		"courier_type":            firstNonEmpty(req.Meta["courier_type"], m.conf().CourierType),
		"delivery_address":        strings.TrimSpace(addr.Line1 + " " + addr.Line2),
		"delivery_suburb":         addr.City,
		"delivery_state":          addr.State,
		"delivery_postcode":       addr.PostalCode,
		"delivery_country_code":   strings.ToUpper(addr.Country),
		"receiver_name":           firstNonEmpty(addr.Name, order.Name, "Customer"),
		"receiver_contact_number": firstNonEmpty(addr.Phone, order.Phone),
		"authority_to_leave":      authority,
		// Shippit's own handle on the order, and what an operator searches on
		// when reconciling its dashboard against the store.
		"retailer_invoice": order.Number,
		"user_attributes": map[string]any{
			"email":      order.Email,
			"first_name": first,
			"last_name":  last,
			"mobile":     firstNonEmpty(addr.Phone, order.Phone),
		},
		"parcel_attributes": []any{m.parcelBody(req)},
	}
	if instructions := req.Meta["delivery_instructions"]; instructions != "" {
		body["delivery_instructions"] = instructions
	}
	// A parcel an operator has already promised to one carrier.
	if allocation := req.Meta["courier_allocation"]; allocation != "" {
		body["courier_allocation"] = allocation
	}
	return body
}

func (m *Module) parcelBody(req gocommerce.ShipRequest) map[string]any {
	var units int
	for _, line := range req.Lines {
		units += line.Quantity
	}

	grams := m.conf().DefaultWeightGrams * max(units, 1)
	if req.Parcel.Measured && req.Parcel.WeightGrams > 0 {
		grams = req.Parcel.WeightGrams
	}

	return map[string]any{
		// One parcel, whatever is in it: the engine has already resolved the
		// lines, and telling Shippit "three" would book three boxes.
		"qty":    1,
		"weight": kilograms(grams),
		"length": metres(req.Parcel.Dimensions.Length, m.conf().DefaultLengthMM),
		"width":  metres(req.Parcel.Dimensions.Width, m.conf().DefaultWidthMM),
		"depth":  metres(req.Parcel.Dimensions.Height, m.conf().DefaultHeightMM),
	}
}

// kilograms converts grams, keeping the gram: 750 g is 0.75 and not 0.8.
func kilograms(grams int) float64 { return float64(grams) / 1000 }

// metres converts a millimetre side. Shippit is the one integration here that
// reads metres, so a typical parcel is a number smaller than one — 0.305, not
// 30.5.
func metres(mm *int, fallback int) float64 {
	value := fallback
	if mm != nil && *mm > 0 {
		value = *mm
	}
	return float64(value) / 1000
}

// carrierFor turns the carrier Shippit allocated into one of the engine's
// carrier codes.
//
// Unknown returns "", which is not a failure: the engine then works the carrier
// out from the tracking number, which is what it does for a manual shipment.
func carrierFor(courier string) string {
	switch c := strings.ToLower(courier); {
	case c == "":
		return ""
	// eParcel and StarTrack are Australia Post's parcel services, and a parcel
	// on either tracks on Australia Post.
	case strings.Contains(c, "eparcel"), strings.Contains(c, "startrack"),
		strings.Contains(c, "australia post"), strings.Contains(c, "auspost"):
		return "australia-post"
	case strings.Contains(c, "fedex"):
		return "fedex"
	case strings.Contains(c, "dhl"):
		return "dhl"
	case strings.Contains(c, "tnt"):
		return "tnt"
	case strings.Contains(c, "aramex"):
		return "aramex"
	case strings.Contains(c, "ups"):
		return "ups"
	}
	return ""
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

func (m *Module) do(ctx context.Context, method, path string, payload any, out any) error {
	var reader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("shippit: could not encode the request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.conf().BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.conf().APIKey)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("shippit: %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyRead))
	if err != nil {
		return fmt.Errorf("shippit: %s: read response: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Error  string          `json:"error"`
			Errors json.RawMessage `json:"errors"`
		}
		if json.Unmarshal(body, &apiErr) == nil {
			if detail := firstNonEmpty(apiErr.Error, errorList(apiErr.Errors)); detail != "" {
				return fmt.Errorf("shippit: %s: %s", path, detail)
			}
		}
		return fmt.Errorf("shippit: %s returned %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// errorList renders Shippit's validation errors, which arrive as a list of
// strings on some responses and as a field-to-messages object on others.
func errorList(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil && len(list) > 0 {
		return strings.Join(list, "; ")
	}
	var byField map[string][]string
	if json.Unmarshal(raw, &byField) == nil && len(byField) > 0 {
		parts := make([]string, 0, len(byField))
		for field, messages := range byField {
			parts = append(parts, field+" "+strings.Join(messages, ", "))
		}
		// Sorted, so the same failure reads the same way twice — map iteration
		// order would otherwise shuffle the sentence between attempts.
		sort.Strings(parts)
		return strings.Join(parts, "; ")
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
