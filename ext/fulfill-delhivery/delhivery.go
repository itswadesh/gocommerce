// Package delhivery books shipments through Delhivery.
//
// Registering it adds "delhivery" as a fulfillment provider, so an operator
// ships by posting to the engine's own /api/admin/create-fulfillment with
// `"provider": "delhivery"` — the engine still owns the order's state and its
// events, and this module only talks to the carrier.
//
//	app, err := gocommerce.New(cfg,
//	    delhivery.New(delhivery.Config{
//	        Token:          os.Getenv("DELHIVERY_TOKEN"),
//	        PickupLocation: "Primary",
//	        SellerName:     "Acme Retail",
//	    }),
//	)
//
// REST over net/http and no SDK, per rule 2.
//
// # A JSON body inside a form field
//
// Delhivery's manifestation endpoint is not a JSON API. It takes
// application/x-www-form-urlencoded with two fields — format=json and data=
// holding the whole payload as a JSON string. Sending the JSON as the body,
// which is what every other module here does, returns a bare 400 with no
// explanation. That is why this module encodes twice, and why it does not look
// like its neighbours.
//
// # Units
//
// Delhivery's own documentation does not state the units for weight or the
// three sides. Every working integration sends **grams** and **centimetres**,
// and that is what this module sends. It is written down here because it is an
// assumption rather than something the vendor documents, and because a store
// that finds otherwise will want to know where the number came from.
//
// # Staging
//
// BaseURL defaults to production. The staging host is
// https://staging-express.delhivery.com and takes a different token.
package delhivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/itswadesh/gocommerce/core"
)

const (
	defaultBaseURL = "https://track.delhivery.com"
	maxBodyRead    = 1 << 20
)

// Config configures the module.
type Config struct {
	// Token is the Delhivery API token, sent as `Authorization: Token <token>`.
	// Required.
	Token string `plugin:"token"`
	// PickupLocation is the nickname of the warehouse registered with
	// Delhivery, and it is matched case-sensitively on their side. Required —
	// the carrier has to collect the parcel somewhere.
	PickupLocation string `plugin:"pickup_location"`
	// SellerName and SellerAddress are printed on the label. SellerName is
	// required; an unnamed seller is a parcel nobody can return.
	SellerName    string `plugin:"seller_name"`
	SellerAddress string `plugin:"seller_address"`
	// SellerGSTIN and HSNCode are the tax fields Delhivery asks for on
	// domestic Indian shipments. Optional here, because whether they are
	// mandatory depends on what the store sells and Delhivery is the one
	// entitled to refuse.
	SellerGSTIN string `plugin:"seller_gstin"`
	HSNCode     string `plugin:"hsn_code"`
	// DefaultWeightGrams is the last resort, per unit, for a parcel the engine
	// could not weigh. A fully weighed catalogue never reaches it.
	DefaultWeightGrams int `plugin:"default_weight_grams"`
	// DefaultLengthCM and friends are the box used when the engine has no
	// unambiguous size — which is any parcel holding more than a single unit.
	// See gocommerce.Parcel.
	DefaultLengthCM float64
	DefaultWidthCM  float64
	DefaultHeightCM float64
	// BaseURL overrides the endpoint: staging, for instance.
	BaseURL string `plugin:"base_url"`
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the Delhivery fulfillment provider.
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
const PluginKey = "shipping-delhivery"

var errNotConfigured = errors.New("delhivery: not set up — activate and configure it under Settings")

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
		strings.TrimSpace(c.PickupLocation) != "" &&
		strings.TrimSpace(c.SellerName) != ""
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
		c.DefaultLengthCM, c.DefaultWidthCM, c.DefaultHeightCM = 15, 15, 10
	}
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "fulfill-delhivery" }

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
		Key: PluginKey, Title: "Delhivery", Category: "shipping", DefaultEnabled: inCode,
		Description: "Manifests parcels with Delhivery and takes the waybill it assigns. India's largest carrier.",
		Docs:        "https://delhivery-express-api-doc.readme.io/",
		Fields: []gocommerce.PluginField{
			{Key: "token", Label: "API token", Kind: "secret", Required: !inCode, Help: "From the Delhivery dashboard; sent as Authorization: Token."},
			{Key: "pickup_location", Label: "Pickup location", Kind: "text", Required: !inCode, Help: "The warehouse nickname registered with Delhivery, matched exactly."},
			{Key: "seller_name", Label: "Seller name", Kind: "text", Required: !inCode, Help: "Printed on the label."},
			{Key: "seller_address", Label: "Seller address", Kind: "text", Help: "Printed on the label."},
			{Key: "seller_gstin", Label: "Seller GSTIN", Kind: "text", Help: "For domestic Indian shipments, where Delhivery asks for it."},
			{Key: "hsn_code", Label: "HSN code", Kind: "text"},
			{Key: "default_weight_grams", Label: "Default weight (grams)", Kind: "number", Help: "Per unit, for a variant with no weight recorded.", Default: 500},
			{Key: "base_url", Label: "API base URL", Kind: "url", Help: "Empty for production; the staging host otherwise."},
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
func (m *Module) Code() string { return "delhivery" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "Delhivery" }

// Ship manifests the shipment and returns the waybill Delhivery assigned.
//
// It runs before the engine opens its transaction, so a carrier having a slow
// minute never holds a lock on the orders table.
func (m *Module) Ship(ctx context.Context, order *gocommerce.Order, req gocommerce.ShipRequest) (gocommerce.Shipment, error) {
	if !m.refresh(ctx) {
		return gocommerce.Shipment{}, errNotConfigured
	}
	payload := map[string]any{
		"pickup_location": map[string]any{"name": m.conf().PickupLocation},
		"shipments":       []any{m.shipmentBody(order, req)},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return gocommerce.Shipment{}, fmt.Errorf("delhivery: could not encode the shipment: %w", err)
	}

	form := url.Values{"format": {"json"}, "data": {string(encoded)}}

	var out struct {
		Success  bool `json:"success"`
		Packages []struct {
			Waybill string          `json:"waybill"`
			RefNum  string          `json:"refnum"`
			Status  string          `json:"status"`
			Remarks json.RawMessage `json:"remarks"`
		} `json:"packages"`
		Rmk          string `json:"rmk"`
		Error        string `json:"error"`
		PackageCount int    `json:"package_count"`
	}
	if err := m.post(ctx, "/api/cmu/create.json", form, &out); err != nil {
		return gocommerce.Shipment{}, err
	}

	if len(out.Packages) == 0 {
		return gocommerce.Shipment{}, fmt.Errorf("delhivery: no package was manifested: %s",
			firstNonEmpty(out.Rmk, out.Error, "Delhivery gave no reason"))
	}
	pkg := out.Packages[0]
	if pkg.Waybill == "" {
		// Delhivery answers 200 with success=false and the reason per package,
		// so the HTTP code alone would report a refusal as a shipment — and the
		// engine would move the order to shipped.
		return gocommerce.Shipment{}, fmt.Errorf("delhivery: the shipment was refused (%s): %s",
			firstNonEmpty(pkg.Status, "no status"), remarks(pkg.Remarks, out.Rmk))
	}

	return gocommerce.Shipment{
		Provider: m.Code(),
		Tracking: pkg.Waybill,
		Carrier:  "delhivery",
	}, nil
}

// remarks renders Delhivery's per-package reason, which arrives as a list of
// strings on some responses and a bare string on others.
func remarks(raw json.RawMessage, fallback string) string {
	if len(raw) > 0 {
		var list []string
		if json.Unmarshal(raw, &list) == nil {
			if joined := strings.TrimSpace(strings.Join(list, "; ")); joined != "" {
				return joined
			}
		}
		var single string
		if json.Unmarshal(raw, &single) == nil && strings.TrimSpace(single) != "" {
			return single
		}
	}
	return firstNonEmpty(fallback, "Delhivery gave no reason")
}

func (m *Module) shipmentBody(order *gocommerce.Order, req gocommerce.ShipRequest) map[string]any {
	addr := order.Address

	byLine := make(map[int64]gocommerce.OrderLine, len(order.Lines))
	for _, line := range order.Lines {
		byLine[line.ID] = line
	}
	// What is declared is what is in this box, not what is on the order: the
	// engine has already resolved req.Lines to explicit quantities, so a parcel
	// holding one of three lines does not tell the carrier it holds three, and
	// the amount to collect on delivery is this parcel's, not the order's.
	var units int
	var valueMinor int64
	descriptions := make([]string, 0, len(req.Lines))
	for _, ship := range req.Lines {
		line, ok := byLine[ship.OrderLineID]
		if !ok {
			continue
		}
		units += ship.Quantity
		valueMinor += line.UnitPrice.AmountMinor * int64(ship.Quantity)
		descriptions = append(descriptions, line.Title)
	}

	// The amount the courier collects. A paid order collects nothing — getting
	// this wrong means either a courier who does not ask, or one who asks a
	// customer who has already paid.
	var codMinor int64
	paymentMode := "Prepaid"
	if order.PaymentStatus != gocommerce.PaymentPaid {
		paymentMode = "COD"
		codMinor = valueMinor
	}

	// Delhivery rejects a duplicate order id, and a second parcel against the
	// same order would be exactly that. len is 0 on the first call — the order
	// was read before this shipment's row exists — so a store that ships whole
	// orders sends exactly the order number.
	externalID := order.Number
	if n := len(order.Fulfillments); n > 0 {
		externalID = fmt.Sprintf("%s-%d", order.Number, n+1)
	}

	body := map[string]any{
		"name":            firstNonEmpty(addr.Name, order.Name, "Customer"),
		"add":             strings.TrimSpace(addr.Line1 + " " + addr.Line2),
		"pin":             addr.PostalCode,
		"city":            addr.City,
		"state":           addr.State,
		"country":         firstNonEmpty(addr.Country, "India"),
		"phone":           firstNonEmpty(addr.Phone, order.Phone),
		"order":           externalID,
		"payment_mode":    paymentMode,
		"cod_amount":      minorToRupees(codMinor),
		"total_amount":    minorToRupees(valueMinor),
		"products_desc":   strings.Join(descriptions, ", "),
		"quantity":        units,
		"weight":          m.weight(req, units),
		"shipment_length": m.side(req.Parcel.Dimensions.Length, m.conf().DefaultLengthCM),
		"shipment_width":  m.side(req.Parcel.Dimensions.Width, m.conf().DefaultWidthCM),
		"shipment_height": m.side(req.Parcel.Dimensions.Height, m.conf().DefaultHeightCM),
		"seller_name":     m.conf().SellerName,
		"seller_add":      m.conf().SellerAddress,
	}
	if m.conf().SellerGSTIN != "" {
		body["seller_gst_tin"] = m.conf().SellerGSTIN
	}
	if m.conf().HSNCode != "" {
		body["hsn_code"] = m.conf().HSNCode
	}
	// A waybill the operator has already pulled from Delhivery's pool. Passing
	// one is how a store prints labels ahead of manifesting them.
	if waybill := req.Meta["waybill"]; waybill != "" {
		body["waybill"] = waybill
	}
	return body
}

// weight is what to declare, in grams — see the package comment on units.
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

// side converts one of the engine's millimetre sides to centimetres, keeping
// the tenth: 305 mm is 30.5 cm and not 30.
func (m *Module) side(mm *int, fallback float64) float64 {
	if mm == nil || *mm <= 0 {
		return fallback
	}
	return float64(*mm) / 10
}

// minorToRupees turns the engine's minor units into the decimal Delhivery
// takes. Exact for INR, which has two decimal places and is the only currency
// Delhivery settles COD in.
func minorToRupees(minor int64) float64 { return float64(minor) / 100 }

func (m *Module) post(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		m.conf().BaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Token "+m.conf().Token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("delhivery: %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyRead))
	if err != nil {
		return fmt.Errorf("delhivery: %s: read response: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("delhivery: %s returned %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("delhivery: %s: could not read the response: %w", path, err)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
