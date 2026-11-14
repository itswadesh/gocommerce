// Package gokwik asks GoKwik how likely a cash-on-delivery order is to come
// back undelivered, and refuses cash on delivery for the ones the store chooses
// not to risk. The shopper can still pay online.
//
// This is GoKwik's RTO Predict API, and only that. Its other products, such as
// one-click checkout, are not here.
//
// GoKwik asks to be called before the payment methods are shown, and to be
// waited on for no more than 500 ms. This module asks when cash on delivery is
// chosen, through a checkout precheck (D190), because a checkout guard must not
// call a network and a storefront needs no change for the refusal to hold.
//
// Whatever goes wrong on GoKwik's side, the order goes ahead: a late or broken
// risk score must not cost a store its sales. So a timeout, an error or an
// answer it cannot read is logged and cash on delivery stays open.
//
// REST over net/http and no SDK, per rule 2.
package gokwik

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/misiki-in/gocommerce/core"
)

// PluginKey is the plugin a store switches on and gives its keys.
const PluginKey = "gokwik"

// predictTimeout is the longest GoKwik's document allows a checkout to wait.
const predictTimeout = 500 * time.Millisecond

// What the Refuse setting can say.
const (
	refuseHigh       = "High risk"
	refuseHighMedium = "High and medium risk"
)

// CodeCODUnavailable lets a storefront tell this refusal from any other.
const CodeCODUnavailable = "cod_unavailable"

// Config is what a store's main() may set; everything a store chooses is on
// the Plugins screen.
type Config struct {
	// Client replaces the HTTP client, for tests.
	Client *http.Client
}

// Module is the precheck.
type Module struct {
	cfg    Config
	app    *gocommerce.App
	log    *slog.Logger
	client *http.Client
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "gokwik" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration { return nil }

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	m.log = app.Log()
	m.client = m.cfg.Client
	if m.client == nil {
		m.client = app.OutboundClient(predictTimeout)
	}
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "GoKwik COD risk", Icon: "ri-shield-check-line", Category: "integration",
		Description: "Asks GoKwik how likely each cash-on-delivery order in India is to come back undelivered, and refuses cash on delivery for the risky ones, so those shoppers pay online. Sends GoKwik the shopper's name, phone, email and address and the order's lines. If GoKwik is slow or down, cash on delivery stays open.",
		Docs:        "https://cdn.gokwik.co/rto-doc/rto-predict-api.pdf",
		Fields: []gocommerce.PluginField{
			{Key: "app_id", Label: "App ID", Kind: "text", Required: true,
				Help: "On your GoKwik dashboard, under Account."},
			{Key: "app_secret", Label: "App secret", Kind: "secret", Required: true,
				Help: "On your GoKwik dashboard, under Account."},
			{Key: "api_url", Label: "API address", Kind: "url", Outbound: true, Required: true,
				Help: "The address GoKwik gives you for going live. To try it first, https://sandbox.gokwik.co with its test keys."},
			{Key: "refuse", Label: "Refuse cash on delivery for", Kind: "select",
				Options: []string{refuseHigh, refuseHighMedium}, Default: refuseHigh,
				Help: "High risk is what GoKwik thinks is highly likely to come back. Medium is less likely, and refusing it also turns away more genuine buyers."},
		},
	})
	app.RegisterCheckoutPrecheck(m.precheck)
	return nil
}

func (m *Module) precheck(ctx context.Context, c *gocommerce.CheckoutIntent) error {
	// GoKwik scores Indian orders.
	if c.Method != gocommerce.CodeCOD || !strings.EqualFold(strings.TrimSpace(c.Input.Address.Country), "IN") {
		return nil
	}
	if on, err := m.app.Plugins().Enabled(ctx, PluginKey); err != nil || !on {
		return nil
	}
	plugins := m.app.Plugins()
	appID, secret := plugins.String(ctx, PluginKey, "app_id"), plugins.String(ctx, PluginKey, "app_secret")
	base := plugins.String(ctx, PluginKey, "api_url")
	if appID == "" || secret == "" || base == "" {
		return nil
	}
	body, err := m.request(ctx, c)
	if err != nil {
		// The checkout that follows finds the same problem and says it better:
		// a moved price, an empty cart. Anything else is GoKwik not being asked.
		m.log.Info("gokwik: cash on delivery not scored", "reason", err)
		return nil
	}
	flag, err := m.predict(ctx, base, appID, secret, body)
	if err != nil {
		m.log.Warn("gokwik: no risk score, cash on delivery stays open", "error", err)
		return nil
	}
	if !refuses(plugins.String(ctx, PluginKey, "refuse"), flag) {
		return nil
	}
	m.log.Info("gokwik: cash on delivery refused", "risk", flag)
	return &gocommerce.APIError{
		Status:  http.StatusForbidden,
		Code:    CodeCODUnavailable,
		Message: "Cash on delivery isn't available for this order. Please choose another way to pay.",
	}
}

func refuses(setting, flag string) bool {
	flag = strings.ToLower(flag)
	return strings.Contains(flag, "high") ||
		(setting == refuseHighMedium && strings.Contains(flag, "medium"))
}

// predict makes the one call. Its errors carry no key: the keys travel in
// headers, which Go's errors do not quote.
func (m *Module) predict(ctx context.Context, base, appID, secret string, body []byte) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, predictTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/v2/rto/predict", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("appid", appID)
	req.Header.Set("appsecret", secret)
	resp, err := m.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GoKwik answered %d", resp.StatusCode)
	}
	var answer struct {
		Data struct {
			RiskFlag string `json:"risk_flag"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return "", fmt.Errorf("GoKwik's answer could not be read: %w", err)
	}
	if answer.Data.RiskFlag == "" {
		return "", fmt.Errorf("GoKwik's answer has no risk flag")
	}
	return answer.Data.RiskFlag, nil
}

// request builds the body from the figures an order placed now would get.
func (m *Module) request(ctx context.Context, c *gocommerce.CheckoutIntent) ([]byte, error) {
	quote, err := m.app.Order().Quote(ctx, c.Method, c.Input)
	if err != nil {
		return nil, fmt.Errorf("quote: %w", err)
	}
	if quote.Total.Currency != "INR" {
		return nil, fmt.Errorf("the order is in %s, not rupees", quote.Total.Currency)
	}
	cart, err := m.app.Cart().GetByToken(ctx, c.Input.CartID)
	if err != nil {
		return nil, fmt.Errorf("cart: %w", err)
	}
	addr := c.Input.Address
	postcode, err := strconv.Atoi(strings.ReplaceAll(addr.PostalCode, " ", ""))
	if err != nil {
		return nil, fmt.Errorf("postcode is not a number")
	}
	phone := addr.Phone
	if phone == "" {
		phone = c.Input.Phone
	}
	name := addr.Name
	if name == "" {
		name = c.Input.Name
	}
	first, last, _ := strings.Cut(strings.TrimSpace(name), " ")

	lines := make([]map[string]any, 0, len(cart.Lines))
	units := 0
	for _, l := range cart.Lines {
		units += l.Quantity
		lines = append(lines, map[string]any{
			"product_id":   strconv.FormatInt(l.ProductID, 10),
			"variant_id":   strconv.FormatInt(l.VariantID, 10),
			"line_item_id": strconv.FormatInt(l.ID, 10),
			"sku":          l.SKU,
			"name":         l.Title,
			"price":        rupees(l.UnitPrice.AmountMinor),
			"quantity":     l.Quantity,
			"total":        rupees(l.Total.AmountMinor),
		})
	}
	discount, promo := int64(0), ""
	if quote.Discount != nil {
		discount, promo = quote.Discount.Amount.AmountMinor, quote.Discount.Code
	}
	return json.Marshal(map[string]any{
		"order": map[string]any{
			"order_date":                time.Now().UTC().Format("2006-01-02 15:04:05.000000-07:00"),
			"subtotal":                  rupees(quote.Subtotal.AmountMinor),
			"total_tax":                 rupees(quote.Tax.AmountMinor),
			"total_shipping":            rupees(quote.Shipping.AmountMinor),
			"total_discount":            rupees(discount),
			"total":                     rupees(quote.Total.AmountMinor),
			"promo_code":                promo,
			"total_line_items":          len(lines),
			"total_line_items_quantity": units,
			"user_type":                 m.userType(ctx, c.Input.Email),
			"platform_type":             "custom",
			"line_items":                lines,
			"session":                   map[string]any{"source": "", "customer_user_agent": c.UserAgent},
			"shipping_address": map[string]any{
				"first_name": first, "last_name": last,
				"address_1": addr.Line1, "address_2": addr.Line2,
				"city": addr.City, "state": addr.State, "postcode": postcode,
				"email": c.Input.Email, "phone": indianPhone(phone),
			},
		},
	})
}

// userType says whether this address has ordered here before. A failed read
// reads as new, which is what a shopper with no history would be anyway.
func (m *Module) userType(ctx context.Context, email string) string {
	var known bool
	err := m.app.DB().QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM orders WHERE lower(email) = lower($1))`, email).Scan(&known)
	if err != nil || !known {
		return "new"
	}
	return "existing"
}

// rupees writes paise as GoKwik's documented examples do, as digits and never
// through a float (rule 6).
func rupees(minor int64) string {
	return fmt.Sprintf("%d.%02d", minor/100, minor%100)
}

// indianPhone is the ten digits GoKwik's examples show, from +91 98765 43210 or
// the like. Anything shorter goes as it came, and GoKwik refuses it, which
// leaves cash on delivery open.
func indianPhone(s string) string {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	if len(digits) > 10 {
		return digits[len(digits)-10:]
	}
	return digits
}
