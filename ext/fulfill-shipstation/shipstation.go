// Package shipstation buys shipping labels through ShipStation.
//
// Registering it adds "shipstation" as a fulfillment provider, so an operator
// ships by posting to the engine's own /api/admin/create-fulfillment with
// `"provider": "shipstation"` — the engine still owns the order's state and
// its events, and this module only talks to the carrier.
//
//	app, err := gocommerce.New(cfg,
//	    shipstation.New(shipstation.Config{
//	        APIKey:      os.Getenv("SHIPSTATION_API_KEY"),
//	        ServiceCode: "usps_priority_mail",
//	        From: shipstation.Address{
//	            Name: "Acme", AddressLine1: "215 Clayton St",
//	            CityLocality: "San Francisco", StateProvince: "CA",
//	            PostalCode: "94117", CountryCode: "US", Phone: "+15553334444",
//	        },
//	    }),
//	)
//
// REST over net/http and no SDK, per rule 2.
//
// # V2, not the older ssapi
//
// ShipStation has two APIs. The older one lives at ssapi.shipstation.com,
// authenticates with a key and a secret over HTTP Basic, and is organised
// around orders you first push into ShipStation. This module speaks the newer
// one at api.shipstation.com/v2 — a single API-Key header and a label bought
// from a shipment described in the request, with no order to create first.
//
// That matters for more than convenience: pushing the order into ShipStation
// would make ShipStation a second place an order exists, and then a second
// place it can change. The engine is the system of record, and buying a label
// against a described parcel keeps it that way.
//
// # Units
//
// ShipStation takes grams for weight, which is what the engine stores, and
// centimetres for size, which it does not — the engine keeps millimetres. So
// sides are divided by ten on the way out and the result is sent as a decimal
// rather than rounded to a whole centimetre, because rounding 305 mm down to
// 30 cm is how a parcel gets refused at the counter for being over its band.
package shipstation

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
	"sync/atomic"
	"time"

	"github.com/itswadesh/gocommerce/core"
)

const (
	defaultBaseURL = "https://api.shipstation.com"
	maxBodyRead    = 1 << 20
)

// Address is a postal address as ShipStation V2 takes one. It is exported
// because the store's own ship-from address is configuration, not something
// the engine holds: an order knows where it is going, never where it came from.
type Address struct {
	Name          string `plugin:"from_name"`
	CompanyName   string
	Phone         string `plugin:"from_phone"`
	AddressLine1  string `plugin:"from_address_line1"`
	AddressLine2  string
	CityLocality  string `plugin:"from_city_locality"`
	StateProvince string `plugin:"from_state_province"`
	PostalCode    string `plugin:"from_postal_code"`
	CountryCode   string `plugin:"from_country_code"`
	// Residential is ShipStation's address_residential_indicator. Leave it
	// alone and the carrier decides, which is usually right and occasionally
	// expensive.
	Residential string
}

// Config configures the module.
type Config struct {
	// APIKey is a ShipStation V2 API key, sent as the API-Key header.
	// Required.
	APIKey string `plugin:"api_key"`
	// ServiceCode is the service to buy — "usps_priority_mail",
	// "ups_ground", and so on. Required as the default; a shipment can
	// override it with Meta["service_code"].
	ServiceCode string `plugin:"service_code"`
	// From is where parcels are sent from. Required.
	From Address
	// DefaultWeightGrams is the last resort, per unit, for a parcel the engine
	// could not weigh. A fully weighed catalogue never reaches it.
	DefaultWeightGrams int `plugin:"default_weight_grams"`
	// DefaultLengthMM and friends are the box used when the engine has no
	// unambiguous size — which is any parcel holding more than a single unit.
	// See gocommerce.Parcel.
	DefaultLengthMM int
	DefaultWidthMM  int
	DefaultHeightMM int
	// BaseURL overrides the endpoint, for tests.
	BaseURL string `plugin:"base_url"`
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the ShipStation fulfillment provider.
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
const PluginKey = "shipping-shipstation"

var errNotConfigured = errors.New("shipstation: not set up — activate and configure it under Settings")

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
		strings.TrimSpace(c.ServiceCode) != "" &&
		strings.TrimSpace(c.From.AddressLine1) != "" &&
		strings.TrimSpace(c.From.CountryCode) != ""
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
func (m *Module) Name() string { return "fulfill-shipstation" }

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
		Key: PluginKey, Title: "ShipStation", Category: "shipping", DefaultEnabled: inCode,
		Description: "Multi-carrier labels through ShipStation's V2 API, on the service you name.",
		Docs:        "https://docs.shipstation.com/",
		Fields: []gocommerce.PluginField{
			{Key: "api_key", Label: "API key", Kind: "secret", Required: !inCode, Help: "A V2 API key, sent as the API-Key header."},
			{Key: "service_code", Label: "Service code", Kind: "text", Required: !inCode, Help: "usps_priority_mail, ups_ground and the rest; a shipment can override it."},
			{Key: "from_name", Label: "From: name", Kind: "text"},
			{Key: "from_address_line1", Label: "From: address line 1", Kind: "text", Required: !inCode, Help: "Where parcels are sent from."},
			{Key: "from_city_locality", Label: "From: city", Kind: "text"},
			{Key: "from_state_province", Label: "From: state", Kind: "text"},
			{Key: "from_postal_code", Label: "From: postal code", Kind: "text"},
			{Key: "from_country_code", Label: "From: country (ISO 2)", Kind: "text", Required: !inCode},
			{Key: "from_phone", Label: "From: phone", Kind: "text"},
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
func (m *Module) Code() string { return "shipstation" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "ShipStation" }

// Ship buys a label and returns its tracking number.
//
// It runs before the engine opens its transaction, so a carrier having a slow
// minute never holds a lock on the orders table.
func (m *Module) Ship(ctx context.Context, order *gocommerce.Order, req gocommerce.ShipRequest) (gocommerce.Shipment, error) {
	if !m.refresh(ctx) {
		return gocommerce.Shipment{}, errNotConfigured
	}
	service := firstNonEmpty(req.Meta["service_code"], m.conf().ServiceCode)

	shipment := map[string]any{
		"service_code": service,
		"ship_from":    addressBody(m.conf().From),
		"ship_to":      m.toAddress(order),
		"packages":     []any{m.packageBody(req)},
		// ShipStation's own handle on this parcel. A second parcel for the
		// same order needs a different one, so it carries the count.
		"external_shipment_id": externalID(order),
	}
	if code := req.Meta["carrier_id"]; code != "" {
		shipment["carrier_id"] = code
	}

	var out struct {
		LabelID        string `json:"label_id"`
		Status         string `json:"status"`
		ShipmentID     string `json:"shipment_id"`
		TrackingNumber string `json:"tracking_number"`
		CarrierCode    string `json:"carrier_code"`
		LabelDownload  struct {
			PDF  string `json:"pdf"`
			PNG  string `json:"png"`
			Href string `json:"href"`
		} `json:"label_download"`
	}
	if err := m.post(ctx, "/v2/labels", map[string]any{"shipment": shipment}, &out); err != nil {
		return gocommerce.Shipment{}, err
	}
	if out.TrackingNumber == "" {
		return gocommerce.Shipment{}, fmt.Errorf(
			"shipstation: label %s came back without a tracking number (status %s)",
			out.LabelID, firstNonEmpty(out.Status, "unknown"))
	}

	return gocommerce.Shipment{
		Provider: m.Code(),
		Tracking: out.TrackingNumber,
		Carrier:  carrierFor(firstNonEmpty(out.CarrierCode, service)),
		LabelURL: firstNonEmpty(out.LabelDownload.PDF, out.LabelDownload.Href, out.LabelDownload.PNG),
	}, nil
}

// externalID is what ShipStation is told this parcel is. It has to be unique
// per label, and a second parcel against one order would otherwise repeat the
// first one's. len is 0 on a first shipment — the order was read before this
// shipment's row exists — so a store that only ever ships whole orders sees
// exactly the order number, which is what it would want to search on.
func externalID(order *gocommerce.Order) string {
	if n := len(order.Fulfillments); n > 0 {
		return fmt.Sprintf("%s-%d", order.Number, n+1)
	}
	return order.Number
}

func addressBody(a Address) map[string]any {
	body := map[string]any{
		"name":           a.Name,
		"phone":          a.Phone,
		"address_line1":  a.AddressLine1,
		"address_line2":  a.AddressLine2,
		"city_locality":  a.CityLocality,
		"state_province": a.StateProvince,
		"postal_code":    a.PostalCode,
		"country_code":   a.CountryCode,
	}
	if a.CompanyName != "" {
		body["company_name"] = a.CompanyName
	}
	if a.Residential != "" {
		body["address_residential_indicator"] = a.Residential
	}
	return body
}

func (m *Module) toAddress(order *gocommerce.Order) map[string]any {
	addr := order.Address
	return addressBody(Address{
		Name:          firstNonEmpty(addr.Name, order.Name, "Customer"),
		Phone:         firstNonEmpty(addr.Phone, order.Phone),
		AddressLine1:  addr.Line1,
		AddressLine2:  addr.Line2,
		CityLocality:  addr.City,
		StateProvince: addr.State,
		PostalCode:    addr.PostalCode,
		CountryCode:   strings.ToUpper(addr.Country),
	})
}

func (m *Module) packageBody(req gocommerce.ShipRequest) map[string]any {
	var units int
	for _, line := range req.Lines {
		units += line.Quantity
	}

	grams := m.conf().DefaultWeightGrams * max(units, 1)
	if req.Parcel.Measured && req.Parcel.WeightGrams > 0 {
		grams = req.Parcel.WeightGrams
	}

	return map[string]any{
		"weight": map[string]any{"value": grams, "unit": "gram"},
		"dimensions": map[string]any{
			"length": centimetres(req.Parcel.Dimensions.Length, m.conf().DefaultLengthMM),
			"width":  centimetres(req.Parcel.Dimensions.Width, m.conf().DefaultWidthMM),
			"height": centimetres(req.Parcel.Dimensions.Height, m.conf().DefaultHeightMM),
			"unit":   "centimeter",
		},
	}
}

// centimetres converts a millimetre side, keeping the tenth: 305 mm is 30.5 cm
// and not 30.
func centimetres(mm *int, fallback int) float64 {
	if mm == nil || *mm <= 0 {
		return float64(fallback) / 10
	}
	return float64(*mm) / 10
}

// carrierFor turns ShipStation's carrier code, or the service code it was
// bought with, into one of the engine's carrier codes.
//
// Unknown returns "", which is not a failure: the engine then works the carrier
// out from the tracking number, which is what it does for a manual shipment.
func carrierFor(code string) string {
	code = strings.ToLower(code)
	switch {
	// stamps_com and endicia are USPS resellers: the parcel is carried by USPS
	// and tracks on USPS, whatever the account says.
	case strings.Contains(code, "usps"), strings.Contains(code, "stamps"), strings.Contains(code, "endicia"):
		return "usps"
	case strings.Contains(code, "fedex"):
		return "fedex"
	case strings.Contains(code, "ups"):
		return "ups"
	case strings.Contains(code, "dhl"):
		return "dhl"
	case strings.Contains(code, "canada_post"), strings.Contains(code, "canadapost"):
		return "canada-post"
	case strings.Contains(code, "australia_post"):
		return "australia-post"
	}
	return ""
}

func (m *Module) post(ctx context.Context, path string, payload any, out any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("shipstation: could not encode the request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.conf().BaseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("API-Key", m.conf().APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("shipstation: %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyRead))
	if err != nil {
		return fmt.Errorf("shipstation: %s: read response: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Errors []struct {
				Message   string `json:"message"`
				ErrorCode string `json:"error_code"`
			} `json:"errors"`
		}
		if json.Unmarshal(body, &apiErr) == nil && len(apiErr.Errors) > 0 {
			parts := make([]string, 0, len(apiErr.Errors))
			for _, e := range apiErr.Errors {
				parts = append(parts, strings.TrimSpace(e.Message+" ("+e.ErrorCode+")"))
			}
			return fmt.Errorf("shipstation: %s: %s", path, strings.Join(parts, "; "))
		}
		return fmt.Errorf("shipstation: %s returned %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
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
