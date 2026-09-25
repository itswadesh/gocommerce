// Package easyship books shipments through Easyship.
//
// Registering it adds "easyship" as a fulfillment provider, so an operator
// ships by posting to the engine's own /api/admin/create-fulfillment with
// `"provider": "easyship"` — the engine still owns the order's state and its
// events, and this module only talks to the carrier.
//
//	app, err := gocommerce.New(cfg,
//	    easyship.New(easyship.Config{
//	        Token:           os.Getenv("EASYSHIP_TOKEN"),
//	        CourierServiceID: os.Getenv("EASYSHIP_COURIER_SERVICE_ID"),
//	        From: easyship.Address{
//	            ContactName: "Acme", Line1: "Kennedy Town", City: "Hong Kong",
//	            PostalCode: "0000", CountryAlpha2: "HK",
//	            ContactPhone: "+852-3008-5678", ContactEmail: "ship@acme.test",
//	        },
//	    }),
//	)
//
// REST over net/http and no SDK, per rule 2.
//
// # One call, with the label
//
// Easyship can create a shipment and leave it unlabelled, then rate-shop, then
// buy. This module sends buy_label with the shipment instead, so one call
// either produces a tracking number or fails — the engine has no state for a
// shipment that exists but cannot be tracked, and inventing one would make
// "booked" and "half booked" look identical on the order.
//
// Which courier is a configured choice for the same reason ext/fulfill-shippo
// gives: picking the cheapest rate automatically would quietly put somebody's
// express order on a slow service. A shipment can name a different courier
// with Meta["courier_service_id"].
//
// # Customs items carry no weight
//
// The items sent for customs describe what is in the box — title, quantity,
// declared value, SKU, origin country — and the parcel's weight is sent once,
// at the parcel level, because that is the only weight the engine actually
// knows. It sums the variants' weights for the whole parcel; it does not hold
// a per-line figure, and splitting the total across the lines would be
// inventing numbers for a customs declaration.
//
// # Units
//
// Easyship reads weights and sizes in the unit system configured on the
// account: metric by default, which is kilograms and centimetres. The engine
// keeps grams and millimetres, so both are divided by a thousand and by ten.
// Set Imperial when the Easyship account is set to pounds and inches, or every
// parcel will be declared about 2.2 times too light.
package easyship

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
	defaultBaseURL = "https://public-api.easyship.com"
	// The dated API version. Unpinned clients get whatever is current, which is
	// how an integration breaks on a day nobody deployed anything.
	apiVersion  = "2024-09"
	maxBodyRead = 1 << 20

	gramsPerPound      = 453.59237
	millimetresPerInch = 25.4
)

// Address is a postal address as Easyship takes one. It is exported because
// the store's own ship-from address is configuration, not something the engine
// holds: an order knows where it is going, never where it came from.
type Address struct {
	ContactName   string `plugin:"from_contact_name"`
	ContactPhone  string `plugin:"from_contact_phone"`
	ContactEmail  string
	CompanyName   string
	Line1         string `plugin:"from_line1"`
	Line2         string `plugin:"from_line2"`
	City          string `plugin:"from_city"`
	State         string `plugin:"from_state"`
	PostalCode    string `plugin:"from_postal_code"`
	CountryAlpha2 string `plugin:"from_country"`
}

// Config configures the module.
type Config struct {
	// Token is an Easyship API access token, sent as a bearer. It needs the
	// public.shipment:write and public.label:write scopes. Required.
	Token string `plugin:"token"`
	// CourierServiceID is the courier service to book. Required as the
	// default; a shipment can override it with Meta["courier_service_id"].
	CourierServiceID string `plugin:"courier_service_id"`
	// From is where parcels are sent from. Required.
	From Address
	// Incoterms is who pays duty on a cross-border parcel: "DDU" (the
	// recipient, Easyship's default) or "DDP" (the store). Empty leaves
	// Easyship's own default alone.
	Incoterms string `plugin:"incoterms"`
	// Imperial says the Easyship account reads pounds and inches rather than
	// kilograms and centimetres. Get it wrong and every parcel is declared at
	// about 2.2 times the wrong weight.
	Imperial bool `plugin:"imperial"`
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

// Module is the Easyship fulfillment provider.
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
const PluginKey = "shipping-easyship"

var errNotConfigured = errors.New("easyship: not set up — activate and configure it under Settings")

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
	return strings.TrimSpace(c.Token) != "" &&
		strings.TrimSpace(c.CourierServiceID) != "" &&
		strings.TrimSpace(c.From.Line1) != "" &&
		strings.TrimSpace(c.From.CountryAlpha2) != ""
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
func (m *Module) Name() string { return "fulfill-easyship" }

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
		Key: PluginKey, Title: "Easyship", Category: "shipping", DefaultEnabled: inCode,
		Description: "Cross-border labels and customs paperwork through Easyship, booked on a courier service you choose.",
		Docs:        "https://developers.easyship.com/",
		Fields: []gocommerce.PluginField{
			{Key: "token", Label: "Access token", Kind: "secret", Required: !inCode, Help: "Needs the public.shipment:write and public.label:write scopes."},
			{Key: "courier_service_id", Label: "Courier service ID", Kind: "text", Required: !inCode, Help: "The service to book; a shipment can override it."},
			{Key: "from_line1", Label: "From: address line 1", Kind: "text", Required: !inCode, Help: "Where parcels are sent from."},
			{Key: "from_line2", Label: "From: address line 2", Kind: "text"},
			{Key: "from_city", Label: "From: city", Kind: "text"},
			{Key: "from_state", Label: "From: state", Kind: "text"},
			{Key: "from_postal_code", Label: "From: postal code", Kind: "text"},
			{Key: "from_country", Label: "From: country (ISO 2)", Kind: "text", Required: !inCode, Help: "Two letters: SG, US, IN."},
			{Key: "from_contact_name", Label: "From: contact name", Kind: "text"},
			{Key: "from_contact_phone", Label: "From: contact phone", Kind: "text"},
			{Key: "incoterms", Label: "Incoterms", Kind: "select", Help: "Who pays duty on a cross-border parcel.", Options: []string{"DDU", "DDP"}},
			{Key: "imperial", Label: "Account uses pounds and inches", Kind: "bool", Help: "Get it wrong and every parcel is declared at about 2.2 times the wrong weight."},
			{Key: "default_weight_grams", Label: "Default weight (grams)", Kind: "number", Help: "Per unit, for a variant with no weight recorded.", Default: 500},
			{Key: "base_url", Label: "API base URL", Kind: "url", Help: "Empty for production."},
		},
	})
	m.finish(&m.cfg)
	m.client = m.cfg.Client
	if m.client == nil {
		m.client = &http.Client{Timeout: 45 * time.Second}
	}
	m.log = app.Log()

	app.RegisterFulfillment(m)
	return nil
}

// Code implements gocommerce.FulfillmentProvider.
func (m *Module) Code() string { return "easyship" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "Easyship" }

// Ship creates the shipment, buys its label, and returns the tracking number.
//
// It runs before the engine opens its transaction, so a carrier having a slow
// minute never holds a lock on the orders table.
func (m *Module) Ship(ctx context.Context, order *gocommerce.Order, req gocommerce.ShipRequest) (gocommerce.Shipment, error) {
	if !m.refresh(ctx) {
		return gocommerce.Shipment{}, errNotConfigured
	}
	body := map[string]any{
		"origin_address":      addressBody(m.conf().From),
		"destination_address": m.toAddress(order),
		"courier_service_id":  firstNonEmpty(req.Meta["courier_service_id"], m.conf().CourierServiceID),
		"parcels":             []any{m.parcelBody(order, req)},
		"buy_label":           true,
		"order_data": map[string]any{
			"platform_name":         "gocommerce",
			"platform_order_number": order.Number,
			"order_created_at":      order.CreatedAt.UTC().Format(time.RFC3339),
		},
	}
	if m.conf().Incoterms != "" {
		body["incoterms"] = m.conf().Incoterms
	}

	var out struct {
		Shipment struct {
			ID             string `json:"easyship_shipment_id"`
			LabelState     string `json:"label_state"`
			CourierService struct {
				Name         string `json:"name"`
				UmbrellaName string `json:"umbrella_name"`
			} `json:"courier_service"`
			Trackings []struct {
				TrackingNumber string `json:"tracking_number"`
				Handler        string `json:"handler"`
			} `json:"trackings"`
			ShippingDocuments []document `json:"shipping_documents"`
		} `json:"shipment"`
	}
	if err := m.post(ctx, "/"+apiVersion+"/shipments", body, &out); err != nil {
		return gocommerce.Shipment{}, err
	}

	var tracking, handler string
	if len(out.Shipment.Trackings) > 0 {
		tracking = out.Shipment.Trackings[0].TrackingNumber
		handler = out.Shipment.Trackings[0].Handler
	}
	if tracking == "" {
		// A shipment that exists with no tracking number is the half-booked
		// state the engine has no room for: say so precisely, and name the
		// Easyship id so an operator can find it and cancel it.
		return gocommerce.Shipment{}, fmt.Errorf(
			"easyship: shipment %s was created but no label was issued (label state %q) — cancel it in Easyship before retrying",
			out.Shipment.ID, firstNonEmpty(out.Shipment.LabelState, "unknown"))
	}

	return gocommerce.Shipment{
		Provider: m.Code(),
		Tracking: tracking,
		Carrier:  carrierFor(handler, out.Shipment.CourierService.UmbrellaName),
		LabelURL: labelURL(out.Shipment.ShippingDocuments),
	}, nil
}

// document is one of the papers Easyship issues for a shipment.
type document struct {
	Category string `json:"category"`
	Format   string `json:"format"`
	URL      string `json:"url"`
}

func labelURL(documents []document) string {
	for _, d := range documents {
		// "label" is the printable one; the list also carries commercial
		// invoices and packing slips, which are not what an operator prints to
		// put on the box.
		if strings.EqualFold(d.Category, "label") && d.URL != "" {
			return d.URL
		}
	}
	return ""
}

func addressBody(a Address) map[string]any {
	return map[string]any{
		"contact_name":   a.ContactName,
		"contact_phone":  a.ContactPhone,
		"contact_email":  a.ContactEmail,
		"company_name":   a.CompanyName,
		"line_1":         a.Line1,
		"line_2":         a.Line2,
		"city":           a.City,
		"state":          a.State,
		"postal_code":    a.PostalCode,
		"country_alpha2": strings.ToUpper(a.CountryAlpha2),
	}
}

func (m *Module) toAddress(order *gocommerce.Order) map[string]any {
	addr := order.Address
	return addressBody(Address{
		ContactName:   firstNonEmpty(addr.Name, order.Name, "Customer"),
		ContactPhone:  firstNonEmpty(addr.Phone, order.Phone),
		ContactEmail:  order.Email,
		Line1:         addr.Line1,
		Line2:         addr.Line2,
		City:          addr.City,
		State:         addr.State,
		PostalCode:    addr.PostalCode,
		CountryAlpha2: addr.Country,
	})
}

func (m *Module) parcelBody(order *gocommerce.Order, req gocommerce.ShipRequest) map[string]any {
	byLine := make(map[int64]gocommerce.OrderLine, len(order.Lines))
	for _, line := range order.Lines {
		byLine[line.ID] = line
	}

	// What is declared is what is in this box, not what is on the order: the
	// engine has already resolved req.Lines to explicit quantities, so a parcel
	// holding one of three lines does not tell customs it holds three.
	items := make([]map[string]any, 0, len(req.Lines))
	var units int
	for _, ship := range req.Lines {
		line, ok := byLine[ship.OrderLineID]
		if !ok {
			continue
		}
		units += ship.Quantity
		items = append(items, map[string]any{
			"description":            line.Title,
			"sku":                    line.SKU,
			"quantity":               ship.Quantity,
			"declared_customs_value": float64(line.UnitPrice.AmountMinor) / 100,
			"declared_currency":      strings.ToUpper(line.UnitPrice.Currency),
		})
	}

	grams := m.conf().DefaultWeightGrams * max(units, 1)
	if req.Parcel.Measured && req.Parcel.WeightGrams > 0 {
		grams = req.Parcel.WeightGrams
	}

	return map[string]any{
		"items":               items,
		"total_actual_weight": m.weight(grams),
		"box": map[string]any{
			"type": "box",
			"outer_dimensions": map[string]any{
				"length": m.length(req.Parcel.Dimensions.Length, m.conf().DefaultLengthMM),
				"width":  m.length(req.Parcel.Dimensions.Width, m.conf().DefaultWidthMM),
				"height": m.length(req.Parcel.Dimensions.Height, m.conf().DefaultHeightMM),
			},
		},
	}
}

// weight converts grams to the unit the Easyship account reads.
func (m *Module) weight(grams int) float64 {
	if m.conf().Imperial {
		return round(float64(grams)/gramsPerPound, 3)
	}
	return round(float64(grams)/1000, 3)
}

// length converts a millimetre side to the unit the Easyship account reads,
// falling back when the engine had no unambiguous answer.
func (m *Module) length(mm *int, fallback int) float64 {
	value := fallback
	if mm != nil && *mm > 0 {
		value = *mm
	}
	if m.conf().Imperial {
		return round(float64(value)/millimetresPerInch, 2)
	}
	return round(float64(value)/10, 2)
}

// round keeps a conversion to the precision the source actually had, so a
// parcel is not declared as 0.7500000000000001 kg.
func round(value float64, places int) float64 {
	scale := 1.0
	for range places {
		scale *= 10
	}
	// A half up the middle rounds away from zero, which is the direction that
	// over-declares rather than under-declares a parcel.
	if value < 0 {
		return -float64(int64(-value*scale+0.5)) / scale
	}
	return float64(int64(value*scale+0.5)) / scale
}

// carrierFor turns Easyship's handler or umbrella courier name into one of the
// engine's carrier codes.
//
// Unknown returns "", which is not a failure: the engine then works the carrier
// out from the tracking number, which is what it does for a manual shipment.
func carrierFor(names ...string) string {
	for _, name := range names {
		switch n := strings.ToLower(name); {
		case n == "":
			continue
		case strings.Contains(n, "usps"):
			return "usps"
		case strings.Contains(n, "fedex"):
			return "fedex"
		case strings.Contains(n, "ups"):
			return "ups"
		case strings.Contains(n, "dhl"):
			return "dhl"
		case strings.Contains(n, "aramex"):
			return "aramex"
		case strings.Contains(n, "delhivery"):
			return "delhivery"
		case strings.Contains(n, "royal mail"), strings.Contains(n, "royalmail"):
			return "royal-mail"
		}
	}
	return ""
}

func (m *Module) post(ctx context.Context, path string, payload any, out any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("easyship: could not encode the request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.conf().BaseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.conf().Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("easyship: %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyRead))
	if err != nil {
		return fmt.Errorf("easyship: %s: read response: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Error struct {
				Code    string   `json:"code"`
				Message string   `json:"message"`
				Details []string `json:"details"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Error.Message != "" {
			detail := apiErr.Error.Message
			if len(apiErr.Error.Details) > 0 {
				detail += ": " + strings.Join(apiErr.Error.Details, "; ")
			}
			return fmt.Errorf("easyship: %s: %s", path, detail)
		}
		return fmt.Errorf("easyship: %s returned %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
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
