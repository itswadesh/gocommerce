// Package shippo buys shipping labels through Shippo.
//
// Registering it adds "shippo" as a fulfillment provider, so an operator ships
// by posting to the engine's own /api/admin/create-fulfillment with
// `"provider": "shippo"` — the engine still owns the order's state and its
// events, and this module only talks to the carrier.
//
//	app, err := gocommerce.New(cfg,
//	    shippo.New(shippo.Config{
//	        APIKey:            os.Getenv("SHIPPO_API_KEY"),
//	        CarrierAccount:    os.Getenv("SHIPPO_CARRIER_ACCOUNT"),
//	        ServicelevelToken: "usps_priority",
//	        From: shippo.Address{
//	            Name: "Acme", Street1: "215 Clayton St", City: "San Francisco",
//	            State: "CA", Zip: "94117", Country: "US", Phone: "+15553334444",
//	        },
//	    }),
//	)
//
// REST over net/http and no SDK, per rule 2.
//
// # One call, not three
//
// Shippo's usual flow is shipment → rates → buy the rate you liked. This module
// uses the instant form of POST /transactions instead: a carrier account and a
// service level chosen in configuration, and the label bought in a single call.
//
// That is the right trade for an engine. Rate shopping is a decision about
// price and delivery date that belongs to whoever is packing the parcel, and
// the engine has no opinion to offer — picking "the cheapest" automatically
// would quietly choose a slower service on somebody's express order. A store
// that wants rate shopping should do it in front of the operator and pass the
// chosen service level per shipment, which the ship request's meta allows.
//
// # Units
//
// The engine records weight in grams and sizes in millimetres, and Shippo
// accepts both — so nothing is converted and nothing is rounded on the way out.
package shippo

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
	"time"

	"github.com/misiki/gocommerce/core"
)

const (
	defaultBaseURL = "https://api.goshippo.com"
	// Shippo pins behaviour to a dated version, and an unpinned client gets
	// whatever is current — which is how an integration breaks on a day nobody
	// deployed anything.
	apiVersion  = "2018-02-08"
	maxBodyRead = 1 << 20
)

// Address is a postal address as Shippo takes one. It is exported because the
// store's own ship-from address is configuration, not something the engine
// holds: an order knows where it is going, never where it came from.
type Address struct {
	Name    string
	Company string
	Street1 string
	Street2 string
	City    string
	State   string
	Zip     string
	Country string
	Phone   string
	Email   string
}

// Config configures the module.
type Config struct {
	// APIKey is a Shippo API token, live or test. Required.
	APIKey string
	// CarrierAccount is the object id of the carrier account to buy from.
	// Required: an instant purchase has to name the account being charged.
	CarrierAccount string
	// ServicelevelToken is the service to buy — "usps_priority",
	// "ups_ground", and so on. Required as the default; a shipment can
	// override it with Meta["servicelevel_token"].
	ServicelevelToken string
	// From is where parcels are sent from. Required.
	From Address
	// LabelFileType is Shippo's label format: PDF_4x6, PNG, ZPLII and the
	// rest. Empty leaves the account's own default alone.
	LabelFileType string
	// DefaultWeightGrams is the last resort, per unit, for a parcel the engine
	// could not weigh. A fully weighed catalogue never reaches it.
	DefaultWeightGrams int
	// DefaultLengthMM and friends are the box used when the engine has no
	// unambiguous size — which is any parcel holding more than a single unit.
	// See gocommerce.Parcel.
	DefaultLengthMM, DefaultWidthMM, DefaultHeightMM int
	// BaseURL overrides the endpoint, for tests.
	BaseURL string
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the Shippo fulfillment provider.
type Module struct {
	cfg    Config
	client *http.Client
	log    *slog.Logger
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "fulfill-shippo" }

// Migrations implements gocommerce.Module. The engine already stores the
// shipment; this module keeps no state of its own.
func (m *Module) Migrations() []gocommerce.Migration { return nil }

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	switch {
	case strings.TrimSpace(m.cfg.APIKey) == "":
		return errors.New("shippo: APIKey is required")
	case strings.TrimSpace(m.cfg.CarrierAccount) == "":
		return errors.New("shippo: CarrierAccount is required — an instant label purchase has to name the account being charged")
	case strings.TrimSpace(m.cfg.ServicelevelToken) == "":
		return errors.New("shippo: ServicelevelToken is required — the engine will not choose a service on your behalf")
	case strings.TrimSpace(m.cfg.From.Street1) == "", strings.TrimSpace(m.cfg.From.Country) == "":
		return errors.New("shippo: From needs at least a street and a country — an order knows where it is going, never where it came from")
	}
	if m.cfg.BaseURL == "" {
		m.cfg.BaseURL = defaultBaseURL
	}
	m.cfg.BaseURL = strings.TrimRight(m.cfg.BaseURL, "/")
	if m.cfg.DefaultWeightGrams <= 0 {
		m.cfg.DefaultWeightGrams = 500
	}
	if m.cfg.DefaultLengthMM <= 0 {
		m.cfg.DefaultLengthMM, m.cfg.DefaultWidthMM, m.cfg.DefaultHeightMM = 150, 150, 100
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
func (m *Module) Code() string { return "shippo" }

// Ship buys a label and returns its tracking number.
//
// It runs before the engine opens its transaction, so a carrier having a slow
// minute never holds a lock on the orders table.
func (m *Module) Ship(ctx context.Context, order *gocommerce.Order, req gocommerce.ShipRequest) (gocommerce.Shipment, error) {
	service := firstNonEmpty(req.Meta["servicelevel_token"], m.cfg.ServicelevelToken)
	account := firstNonEmpty(req.Meta["carrier_account"], m.cfg.CarrierAccount)

	body := map[string]any{
		"carrier_account":    account,
		"servicelevel_token": service,
		// Synchronous: an asynchronous purchase answers QUEUED and leaves the
		// engine holding a shipment with no tracking number, which is exactly
		// the half-booked state an operator cannot act on.
		"async":    false,
		"metadata": "Order " + order.Number,
		"shipment": map[string]any{
			"address_from": addressBody(m.cfg.From),
			"address_to":   m.toAddress(order),
			"parcels":      []any{m.parcelBody(req)},
		},
	}
	if m.cfg.LabelFileType != "" {
		body["label_file_type"] = m.cfg.LabelFileType
	}

	var out struct {
		ObjectID            string    `json:"object_id"`
		Status              string    `json:"status"`
		TrackingNumber      string    `json:"tracking_number"`
		LabelURL            string    `json:"label_url"`
		TrackingURLProvider string    `json:"tracking_url_provider"`
		Messages            []message `json:"messages"`
	}
	if err := m.post(ctx, "/transactions", body, &out); err != nil {
		return gocommerce.Shipment{}, err
	}
	if !strings.EqualFold(out.Status, "SUCCESS") {
		// Shippo answers 200 or 201 with status ERROR and the reason in
		// messages, so the HTTP code alone would report a failure as a
		// success — and the engine would move the order to shipped.
		return gocommerce.Shipment{}, fmt.Errorf("shippo: the label was not purchased (%s): %s",
			firstNonEmpty(out.Status, "no status"), describe(out.Messages))
	}
	if out.TrackingNumber == "" {
		return gocommerce.Shipment{}, fmt.Errorf(
			"shippo: transaction %s succeeded but carries no tracking number", out.ObjectID)
	}

	return gocommerce.Shipment{
		Provider: m.Code(),
		Tracking: out.TrackingNumber,
		Carrier:  carrierFor(service),
		LabelURL: out.LabelURL,
	}, nil
}

type message struct {
	Source string `json:"source"`
	Code   string `json:"code"`
	Text   string `json:"text"`
}

func describe(messages []message) string {
	if len(messages) == 0 {
		return "Shippo gave no reason"
	}
	parts := make([]string, 0, len(messages))
	for _, m := range messages {
		parts = append(parts, strings.TrimSpace(strings.Join([]string{m.Source, m.Code, m.Text}, " ")))
	}
	return strings.Join(parts, "; ")
}

func addressBody(a Address) map[string]any {
	return map[string]any{
		"name": a.Name, "company": a.Company,
		"street1": a.Street1, "street2": a.Street2,
		"city": a.City, "state": a.State, "zip": a.Zip, "country": a.Country,
		"phone": a.Phone, "email": a.Email,
	}
}

func (m *Module) toAddress(order *gocommerce.Order) map[string]any {
	addr := order.Address
	return addressBody(Address{
		Name:    firstNonEmpty(addr.Name, order.Name, "Customer"),
		Street1: addr.Line1,
		Street2: addr.Line2,
		City:    addr.City,
		State:   addr.State,
		Zip:     addr.PostalCode,
		Country: addr.Country,
		Phone:   firstNonEmpty(addr.Phone, order.Phone),
		Email:   order.Email,
	})
}

// parcelBody is the box, in the units the engine already keeps: grams and
// millimetres, both of which Shippo accepts, so nothing is converted here and
// nothing can be rounded wrongly.
func (m *Module) parcelBody(req gocommerce.ShipRequest) map[string]any {
	var units int
	for _, line := range req.Lines {
		units += line.Quantity
	}

	weight := m.cfg.DefaultWeightGrams * max(units, 1)
	if req.Parcel.Measured && req.Parcel.WeightGrams > 0 {
		weight = req.Parcel.WeightGrams
	}

	return map[string]any{
		"length":        side(req.Parcel.Dimensions.Length, m.cfg.DefaultLengthMM),
		"width":         side(req.Parcel.Dimensions.Width, m.cfg.DefaultWidthMM),
		"height":        side(req.Parcel.Dimensions.Height, m.cfg.DefaultHeightMM),
		"distance_unit": "mm",
		"weight":        weight,
		"mass_unit":     "g",
	}
}

func side(mm *int, fallback int) int {
	if mm == nil || *mm <= 0 {
		return fallback
	}
	return *mm
}

// carrierFor turns a Shippo service level token into one of the engine's
// carrier codes, so a tracking number gets the right tracking URL without the
// engine having to guess from the number's shape.
//
// Unknown tokens return "", which is not a failure: the engine then works the
// carrier out from the tracking number, which is what it does for a manual
// shipment too.
func carrierFor(servicelevelToken string) string {
	prefix, _, _ := strings.Cut(strings.ToLower(servicelevelToken), "_")
	switch prefix {
	case "usps", "ups", "fedex", "dhl", "aramex":
		return prefix
	case "canada":
		return "canada-post"
	case "australia":
		return "australia-post"
	}
	return ""
}

func (m *Module) post(ctx context.Context, path string, payload any, out any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("shippo: could not encode the request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.cfg.BaseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "ShippoToken "+m.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("SHIPPO-API-VERSION", apiVersion)

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("shippo: %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyRead))
	if err != nil {
		return fmt.Errorf("shippo: %s: read response: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(body, &apiErr)
		if apiErr.Detail != "" {
			return fmt.Errorf("shippo: %s: %s", path, apiErr.Detail)
		}
		return fmt.Errorf("shippo: %s returned %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
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
