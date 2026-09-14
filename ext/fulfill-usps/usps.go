// Package usps buys shipping labels directly from the United States Postal
// Service.
//
// Registering it adds "usps" as a fulfillment provider, so an operator ships by
// posting to the engine's own /api/admin/create-fulfillment with
// `"provider": "usps"` — the engine still owns the order's state and its
// events, and this module only talks to the carrier.
//
//	app, err := gocommerce.New(cfg,
//	    usps.New(usps.Config{
//	        ClientID:      os.Getenv("USPS_CLIENT_ID"),
//	        ClientSecret:  os.Getenv("USPS_CLIENT_SECRET"),
//	        CRID:          os.Getenv("USPS_CRID"),
//	        MID:           os.Getenv("USPS_MID"),
//	        AccountNumber: os.Getenv("USPS_EPS_ACCOUNT"),
//	        From: usps.Address{
//	            FirstName: "Acme", StreetAddress: "4120 Bingham Ave",
//	            City: "St. Louis", State: "MO", ZIPCode: "63116",
//	        },
//	    }),
//	)
//
// REST over net/http and no SDK, per rule 2.
//
// # Three calls, not one
//
// USPS is the only carrier here that needs two credentials to buy one label:
//
//  1. POST /oauth2/v3/token — the ordinary OAuth client-credentials grant,
//     which produces the bearer token every USPS API takes.
//  2. POST /payments/v3/payment-authorization — a *second* token, tied to the
//     CRID, MID and EPS account that will be charged. Label creation is the
//     only endpoint that needs it, and without it every call returns 401 with a
//     perfectly valid bearer token, which is a confusing morning.
//  3. POST /labels/v3/label — the label itself.
//
// Both tokens are cached, so a store buying a hundred labels makes a hundred
// and two calls rather than three hundred.
//
// # Where the label goes
//
// USPS does not host the label. It returns the PDF inline, base64 encoded, and
// a base64 PDF is not a URL — putting one in a data: URI would put a quarter of
// a megabyte into every order response. So this module stores the bytes in its
// own table and serves them at
//
//	GET /api/admin/x/fulfill-usps/labels/{tracking}
//
// which is what the shipment's label URL points at. An operator with
// orders.read prints it; nobody else can read it, because a label carries the
// buyer's name and address.
//
// # Units
//
// USPS takes pounds and inches. The engine keeps grams and millimetres, so both
// are converted, and the weight is rounded *up* to the hundredth of a pound —
// a parcel declared lighter than it is comes back with postage due, and a
// parcel declared heavier only costs a little more.
package usps

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/misiki/gocommerce/core"
)

const (
	defaultBaseURL = "https://apis.usps.com"
	maxBodyRead    = 8 << 20 // a label PDF, base64, with room to spare

	gramsPerPound      = 453.59237
	millimetresPerInch = 25.4

	// USPS says a payment authorization lasts eight hours. Four is half of
	// that, which is short enough that a clock difference never lands on the
	// wrong side of the boundary.
	paymentTokenLifetime = 4 * time.Hour
)

// Address is a postal address as the USPS label API takes one. It is exported
// because the store's own ship-from address is configuration, not something the
// engine holds: an order knows where it is going, never where it came from.
type Address struct {
	FirstName        string
	LastName         string
	Firm             string
	StreetAddress    string
	SecondaryAddress string
	City             string
	State            string
	ZIPCode          string
	ZIPPlus4         string
	Phone            string
}

// Config configures the module.
type Config struct {
	// ClientID and ClientSecret are the consumer key and secret of a USPS
	// developer application. Required.
	ClientID, ClientSecret string
	// CRID, MID and AccountNumber identify who is paying. All three come from
	// the Business Customer Gateway, and all three are required: the payment
	// authorization call is what mints the second token, and it will not mint
	// one for an incomplete set.
	CRID, MID, AccountNumber string
	// ManifestMID is the MID that manifests the shipment. Defaults to MID,
	// which is right for a store with one mailer id.
	ManifestMID string
	// AccountType is the payment account behind the label. "EPS" — Enterprise
	// Payment System — is the only one most stores have, and the default.
	AccountType string
	// From is where parcels are posted from. Required.
	From Address
	// MailClass is the service to buy: USPS_GROUND_ADVANTAGE, PRIORITY_MAIL,
	// PRIORITY_MAIL_EXPRESS and the rest. Required as the default; a shipment
	// can override it with Meta["mail_class"].
	MailClass string
	// RateIndicator is the rate band. "SP" — single piece — is what an
	// ordinary retail parcel is, and the default.
	RateIndicator string
	// ProcessingCategory is how the item runs through the network:
	// MACHINABLE, IRREGULAR, NON_MACHINABLE, LETTERS, FLATS. Defaults to
	// MACHINABLE.
	ProcessingCategory string
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

// Module is the USPS fulfillment provider.
type Module struct {
	cfg    Config
	client *http.Client
	log    *slog.Logger
	db     *sql.DB

	mu           sync.Mutex
	bearer       string
	bearerUntil  time.Time
	payment      string
	paymentUntil time.Time
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "fulfill-usps" }

// Migrations implements gocommerce.Module.
//
// One table, because USPS hands back the label rather than hosting it — see
// the package comment.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_labels",
		SQL: `
			CREATE TABLE fulfill_usps_labels (
			    tracking     text        PRIMARY KEY,
			    order_id     bigint      NOT NULL,
			    content_type text        NOT NULL,
			    image        bytea       NOT NULL,
			    created_at   timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX fulfill_usps_labels_order_idx
			    ON fulfill_usps_labels (order_id);`,
	}}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	for name, value := range map[string]string{
		"ClientID":      m.cfg.ClientID,
		"ClientSecret":  m.cfg.ClientSecret,
		"CRID":          m.cfg.CRID,
		"MID":           m.cfg.MID,
		"AccountNumber": m.cfg.AccountNumber,
		"MailClass":     m.cfg.MailClass,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("usps: %s is required", name)
		}
	}
	if strings.TrimSpace(m.cfg.From.StreetAddress) == "" || strings.TrimSpace(m.cfg.From.ZIPCode) == "" {
		return errors.New("usps: From needs at least a street address and a ZIP code — an order knows where it is going, never where it came from")
	}

	if m.cfg.BaseURL == "" {
		m.cfg.BaseURL = defaultBaseURL
	}
	m.cfg.BaseURL = strings.TrimRight(m.cfg.BaseURL, "/")
	if m.cfg.ManifestMID == "" {
		m.cfg.ManifestMID = m.cfg.MID
	}
	if m.cfg.AccountType == "" {
		m.cfg.AccountType = "EPS"
	}
	if m.cfg.RateIndicator == "" {
		m.cfg.RateIndicator = "SP"
	}
	if m.cfg.ProcessingCategory == "" {
		m.cfg.ProcessingCategory = "MACHINABLE"
	}
	if m.cfg.DefaultWeightGrams <= 0 {
		m.cfg.DefaultWeightGrams = 500
	}
	if m.cfg.DefaultLengthMM <= 0 {
		m.cfg.DefaultLengthMM, m.cfg.DefaultWidthMM, m.cfg.DefaultHeightMM = 230, 150, 50
	}
	m.client = m.cfg.Client
	if m.client == nil {
		m.client = &http.Client{Timeout: 45 * time.Second}
	}
	m.log = app.Log()
	m.db = app.DB()

	// orders.read, because a label carries the buyer's name and address: the
	// same people who may read the order may print it, and nobody else.
	app.HandleAdminFunc("GET /api/admin/x/fulfill-usps/labels/{tracking}",
		m.handleLabel, gocommerce.RightOrdersRead)

	app.RegisterFulfillment(m)
	return nil
}

// Code implements gocommerce.FulfillmentProvider.
func (m *Module) Code() string { return "usps" }

// Ship buys a label and returns its tracking number.
//
// It runs before the engine opens its transaction, so USPS having a slow minute
// never holds a lock on the orders table.
func (m *Module) Ship(ctx context.Context, order *gocommerce.Order, req gocommerce.ShipRequest) (gocommerce.Shipment, error) {
	body := map[string]any{
		"imageInfo":          map[string]any{"imageType": "PDF", "labelType": "4X6LABEL"},
		"fromAddress":        addressBody(m.cfg.From),
		"toAddress":          toAddress(order),
		"packageDescription": m.packageBody(order, req),
	}

	var out struct {
		LabelMetadata struct {
			TrackingNumber string `json:"trackingNumber"`
			Postage        any    `json:"postage"`
		} `json:"labelMetadata"`
		LabelImage string `json:"labelImage"`
		Error      struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := m.post(ctx, "/labels/v3/label", body, true, &out); err != nil {
		return gocommerce.Shipment{}, err
	}
	tracking := strings.TrimSpace(out.LabelMetadata.TrackingNumber)
	if tracking == "" {
		return gocommerce.Shipment{}, errors.New("usps: the label came back with no tracking number")
	}

	labelURL := ""
	if image, err := base64.StdEncoding.DecodeString(out.LabelImage); err != nil || len(image) == 0 {
		// The parcel is bought and the number is real; only the paper is
		// missing. Refusing the shipment over it would leave the operator with
		// postage they have paid for and an order the engine says is unshipped.
		m.log.Warn("USPS returned a label this module could not decode",
			"order_id", order.ID, "tracking", tracking, "error", err)
	} else if err := m.store(ctx, tracking, order.ID, image); err != nil {
		m.log.Error("could not store the USPS label",
			"order_id", order.ID, "tracking", tracking, "error", err)
	} else {
		labelURL = "/api/admin/x/fulfill-usps/labels/" + tracking
	}

	return gocommerce.Shipment{
		Provider: m.Code(),
		Tracking: tracking,
		Carrier:  "usps",
		LabelURL: labelURL,
	}, nil
}

// store keeps the label bytes. A row written here whose shipment then fails to
// commit is an orphan, and deliberately harmless: it is keyed by a tracking
// number USPS has already issued, and the postage was spent either way.
func (m *Module) store(ctx context.Context, tracking string, orderID int64, image []byte) error {
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO fulfill_usps_labels (tracking, order_id, content_type, image)
		VALUES ($1, $2, 'application/pdf', $3)
		ON CONFLICT (tracking) DO UPDATE SET image = excluded.image`,
		tracking, orderID, image)
	return err
}

func (m *Module) handleLabel(w http.ResponseWriter, r *http.Request) {
	tracking := r.PathValue("tracking")

	var contentType string
	var image []byte
	err := m.db.QueryRowContext(r.Context(),
		`SELECT content_type, image FROM fulfill_usps_labels WHERE tracking = $1`,
		tracking).Scan(&contentType, &image)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, `{"error":{"code":"not_found","message":"no USPS label for that tracking number"}}`,
			http.StatusNotFound)
		return
	}
	if err != nil {
		m.log.Error("could not read a USPS label", "tracking", tracking, "error", err)
		http.Error(w, `{"error":{"code":"internal","message":"could not read the label"}}`,
			http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", contentType)
	// Inline, because the operator is looking at it to print it.
	w.Header().Set("Content-Disposition", `inline; filename="`+tracking+`.pdf"`)
	_, _ = w.Write(image)
}

func addressBody(a Address) map[string]any {
	body := map[string]any{
		"firstName":     a.FirstName,
		"lastName":      a.LastName,
		"streetAddress": a.StreetAddress,
		"city":          a.City,
		"state":         strings.ToUpper(a.State),
		"ZIPCode":       a.ZIPCode,
	}
	for key, value := range map[string]string{
		"firm":             a.Firm,
		"secondaryAddress": a.SecondaryAddress,
		"ZIPPlus4":         a.ZIPPlus4,
		"phone":            a.Phone,
	} {
		if strings.TrimSpace(value) != "" {
			body[key] = value
		}
	}
	return body
}

func toAddress(order *gocommerce.Order) map[string]any {
	addr := order.Address
	first, last := splitName(firstNonEmpty(addr.Name, order.Name, "Customer"))
	zip, plus4 := splitZIP(addr.PostalCode)
	return addressBody(Address{
		FirstName:        first,
		LastName:         last,
		StreetAddress:    addr.Line1,
		SecondaryAddress: addr.Line2,
		City:             addr.City,
		State:            addr.State,
		ZIPCode:          zip,
		ZIPPlus4:         plus4,
		Phone:            firstNonEmpty(addr.Phone, order.Phone),
	})
}

// splitZIP separates a ZIP+4 that arrived as one string. USPS takes the two
// halves in separate fields and rejects "94117-1234" in ZIPCode.
func splitZIP(postal string) (zip, plus4 string) {
	postal = strings.TrimSpace(postal)
	if base, rest, ok := strings.Cut(postal, "-"); ok {
		return base, rest
	}
	if len(postal) == 9 && isDigits(postal) {
		return postal[:5], postal[5:]
	}
	return postal, ""
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
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

func (m *Module) packageBody(order *gocommerce.Order, req gocommerce.ShipRequest) map[string]any {
	var units int
	for _, line := range req.Lines {
		units += line.Quantity
	}

	grams := m.cfg.DefaultWeightGrams * max(units, 1)
	if req.Parcel.Measured && req.Parcel.WeightGrams > 0 {
		grams = req.Parcel.WeightGrams
	}

	return map[string]any{
		"mailClass":                    firstNonEmpty(req.Meta["mail_class"], m.cfg.MailClass),
		"rateIndicator":                firstNonEmpty(req.Meta["rate_indicator"], m.cfg.RateIndicator),
		"processingCategory":           m.cfg.ProcessingCategory,
		"destinationEntryFacilityType": "NONE",
		"mailingDate":                  time.Now().UTC().Format("2006-01-02"),
		"weightUOM":                    "lb",
		"weight":                       pounds(grams),
		"dimensionsUOM":                "in",
		"length":                       inches(req.Parcel.Dimensions.Length, m.cfg.DefaultLengthMM),
		"width":                        inches(req.Parcel.Dimensions.Width, m.cfg.DefaultWidthMM),
		"height":                       inches(req.Parcel.Dimensions.Height, m.cfg.DefaultHeightMM),
	}
}

// pounds converts grams, rounding *up* to the hundredth. A parcel declared
// lighter than it is comes back with postage due; one declared a hundredth
// heavier costs a cent.
func pounds(grams int) float64 {
	return math.Ceil(float64(grams)/gramsPerPound*100) / 100
}

// inches converts a millimetre side, rounding up for the same reason: a box
// declared inside a band it is actually outside of is a re-rated parcel.
func inches(mm *int, fallback int) float64 {
	value := fallback
	if mm != nil && *mm > 0 {
		value = *mm
	}
	return math.Ceil(float64(value)/millimetresPerInch*100) / 100
}

// ------------------------------------------------------------------- transport

// bearerToken returns a valid OAuth token, minting one when necessary.
func (m *Module) bearerToken(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bearer != "" && time.Now().Before(m.bearerUntil) {
		return m.bearer, nil
	}

	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := m.call(ctx, http.MethodPost, "/oauth2/v3/token", map[string]any{
		"client_id":     m.cfg.ClientID,
		"client_secret": m.cfg.ClientSecret,
		"grant_type":    "client_credentials",
	}, nil, &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", errors.New("usps: the token endpoint returned no access token")
	}

	lifetime := time.Duration(out.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = time.Hour
	}
	// A minute early, so a token never expires between the two calls of one
	// label.
	m.bearer, m.bearerUntil = out.AccessToken, time.Now().Add(lifetime-time.Minute)
	return m.bearer, nil
}

// paymentToken returns a valid payment authorization, minting one when
// necessary. Label creation is the only USPS endpoint that needs it.
func (m *Module) paymentToken(ctx context.Context) (string, error) {
	bearer, err := m.bearerToken(ctx)
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.payment != "" && time.Now().Before(m.paymentUntil) {
		return m.payment, nil
	}

	role := func(name string) map[string]any {
		return map[string]any{
			"roleName":      name,
			"CRID":          m.cfg.CRID,
			"MID":           m.cfg.MID,
			"manifestMID":   m.cfg.ManifestMID,
			"accountType":   m.cfg.AccountType,
			"accountNumber": m.cfg.AccountNumber,
		}
	}

	var out struct {
		PaymentAuthorizationToken string `json:"paymentAuthorizationToken"`
	}
	if err := m.call(ctx, http.MethodPost, "/payments/v3/payment-authorization",
		map[string]any{"roles": []any{role("PAYER"), role("LABEL_OWNER")}},
		map[string]string{"Authorization": "Bearer " + bearer}, &out); err != nil {
		return "", err
	}
	if out.PaymentAuthorizationToken == "" {
		return "", errors.New("usps: the payment authorization returned no token — check the CRID, MID and EPS account")
	}
	m.payment, m.paymentUntil = out.PaymentAuthorizationToken, time.Now().Add(paymentTokenLifetime)
	return m.payment, nil
}

func (m *Module) post(ctx context.Context, path string, payload any, needsPayment bool, out any) error {
	bearer, err := m.bearerToken(ctx)
	if err != nil {
		return err
	}
	headers := map[string]string{"Authorization": "Bearer " + bearer}
	if needsPayment {
		payment, err := m.paymentToken(ctx)
		if err != nil {
			return err
		}
		headers["X-Payment-Authorization-Token"] = payment
	}
	return m.call(ctx, http.MethodPost, path, payload, headers, out)
}

func (m *Module) call(ctx context.Context, method, path string, payload any,
	headers map[string]string, out any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("usps: could not encode the request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.cfg.BaseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// Without this the label endpoint answers multipart/mixed, and the label
	// arrives as a MIME part rather than a base64 field.
	req.Header.Set("Accept", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("usps: %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyRead))
	if err != nil {
		return fmt.Errorf("usps: %s: read response: %w", path, err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		// Either token may have gone stale; drop both so the next attempt
		// mints them again rather than retrying with the same dead pair.
		m.mu.Lock()
		m.bearer, m.payment = "", ""
		m.mu.Unlock()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("usps: %s: %s", path, describe(body, resp.Status))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("usps: %s: could not read the response: %w", path, err)
	}
	return nil
}

// describe pulls the sentence out of a USPS error, which arrives in two
// different shapes depending on which service answered.
func describe(body []byte, status string) string {
	var structured struct {
		Error struct {
			Message string `json:"message"`
			Errors  []struct {
				Detail string `json:"detail"`
				Title  string `json:"title"`
			} `json:"errors"`
		} `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if json.Unmarshal(body, &structured) == nil {
		if structured.ErrorDescription != "" {
			return structured.ErrorDescription
		}
		if structured.Error.Message != "" {
			detail := structured.Error.Message
			for _, e := range structured.Error.Errors {
				detail += "; " + firstNonEmpty(e.Detail, e.Title)
			}
			return detail
		}
	}
	if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		return status + ": " + trimmed
	}
	return status
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
