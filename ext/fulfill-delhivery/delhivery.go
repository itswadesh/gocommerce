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
	"time"

	"github.com/misiki/gocommerce/core"
)

const (
	defaultBaseURL = "https://track.delhivery.com"
	maxBodyRead    = 1 << 20
)

// Config configures the module.
type Config struct {
	// Token is the Delhivery API token, sent as `Authorization: Token <token>`.
	// Required.
	Token string
	// PickupLocation is the nickname of the warehouse registered with
	// Delhivery, and it is matched case-sensitively on their side. Required —
	// the carrier has to collect the parcel somewhere.
	PickupLocation string
	// SellerName and SellerAddress are printed on the label. SellerName is
	// required; an unnamed seller is a parcel nobody can return.
	SellerName    string
	SellerAddress string
	// SellerGSTIN and HSNCode are the tax fields Delhivery asks for on
	// domestic Indian shipments. Optional here, because whether they are
	// mandatory depends on what the store sells and Delhivery is the one
	// entitled to refuse.
	SellerGSTIN string
	HSNCode     string
	// DefaultWeightGrams is the last resort, per unit, for a parcel the engine
	// could not weigh. A fully weighed catalogue never reaches it.
	DefaultWeightGrams int
	// DefaultLengthCM and friends are the box used when the engine has no
	// unambiguous size — which is any parcel holding more than a single unit.
	// See gocommerce.Parcel.
	DefaultLengthCM, DefaultWidthCM, DefaultHeightCM float64
	// BaseURL overrides the endpoint: staging, for instance.
	BaseURL string
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the Delhivery fulfillment provider.
type Module struct {
	cfg    Config
	client *http.Client
	log    *slog.Logger
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "fulfill-delhivery" }

// Migrations implements gocommerce.Module. The engine already stores the
// shipment; this module keeps no state of its own.
func (m *Module) Migrations() []gocommerce.Migration { return nil }

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	switch {
	case strings.TrimSpace(m.cfg.Token) == "":
		return errors.New("delhivery: Token is required")
	case strings.TrimSpace(m.cfg.PickupLocation) == "":
		return errors.New("delhivery: PickupLocation is required — the carrier has to collect the parcel somewhere, and the name must match the warehouse registered with Delhivery")
	case strings.TrimSpace(m.cfg.SellerName) == "":
		return errors.New("delhivery: SellerName is required — it is printed on the label, and an unnamed parcel is one nobody can return")
	}
	if m.cfg.BaseURL == "" {
		m.cfg.BaseURL = defaultBaseURL
	}
	m.cfg.BaseURL = strings.TrimRight(m.cfg.BaseURL, "/")
	if m.cfg.DefaultWeightGrams <= 0 {
		m.cfg.DefaultWeightGrams = 500
	}
	if m.cfg.DefaultLengthCM <= 0 {
		m.cfg.DefaultLengthCM, m.cfg.DefaultWidthCM, m.cfg.DefaultHeightCM = 15, 15, 10
	}
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

// Ship manifests the shipment and returns the waybill Delhivery assigned.
//
// It runs before the engine opens its transaction, so a carrier having a slow
// minute never holds a lock on the orders table.
func (m *Module) Ship(ctx context.Context, order *gocommerce.Order, req gocommerce.ShipRequest) (gocommerce.Shipment, error) {
	payload := map[string]any{
		"pickup_location": map[string]any{"name": m.cfg.PickupLocation},
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
		"shipment_length": m.side(req.Parcel.Dimensions.Length, m.cfg.DefaultLengthCM),
		"shipment_width":  m.side(req.Parcel.Dimensions.Width, m.cfg.DefaultWidthCM),
		"shipment_height": m.side(req.Parcel.Dimensions.Height, m.cfg.DefaultHeightCM),
		"seller_name":     m.cfg.SellerName,
		"seller_add":      m.cfg.SellerAddress,
	}
	if m.cfg.SellerGSTIN != "" {
		body["seller_gst_tin"] = m.cfg.SellerGSTIN
	}
	if m.cfg.HSNCode != "" {
		body["hsn_code"] = m.cfg.HSNCode
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
	return m.cfg.DefaultWeightGrams * max(units, 1)
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
		m.cfg.BaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Token "+m.cfg.Token)
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
