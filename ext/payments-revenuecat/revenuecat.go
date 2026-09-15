// Package revenuecat takes payments through RevenueCat Web Billing.
//
//	app, err := gocommerce.New(cfg,
//	    revenuecat.New(revenuecat.Config{
//	        LinkToken:            os.Getenv("REVENUECAT_LINK_TOKEN"),
//	        WebhookAuthorization: os.Getenv("REVENUECAT_WEBHOOK_AUTHORIZATION"),
//	    }),
//	)
//
// RevenueCat then calls POST /api/checkout/revenuecat/webhook, a route the
// engine owns and hands to this module with the body untouched.
//
// No SDK and, for a checkout, no API call at all: a Web Purchase Link is a URL
// this module builds, and everything that follows arrives as a webhook.
//
// # Digital goods only, and RevenueCat says so
//
// RevenueCat's own documentation is explicit that Web Billing does not collect
// a shipping address, does not support B2B sales, and is not available to
// merchants in India. So this provider is for downloads, licences and
// memberships. Registering it on a store that ships boxes will work right up
// until somebody needs an address, and then it will not.
//
// # The price is RevenueCat's, not the order's
//
// This is the constraint to understand before installing it. Stripe, Adyen and
// the rest are told what to charge. RevenueCat sells a product from its own
// catalogue at the price configured there, and a purchase link carries no
// amount — so the store does not set the price at checkout, RevenueCat does.
//
// What this module can do, and does, is refuse to settle an order when the two
// disagree: the webhook carries what was actually charged, and an order is
// marked paid only if that matches its total to the minor unit. The practical
// consequence is that the RevenueCat product's price must equal the order
// total, which in turn means leaving GoCommerce's tax rates empty — the same
// arrangement ext/payments-paddle documents for a merchant of record.
//
// # One RevenueCat customer per order
//
// A purchase link identifies the buyer with an app user id in its path, and
// that is the only thing this module can put into a purchase and read back out
// of the webhook. So it uses the order number as the app user id.
//
// That is right for one-off digital goods and wrong if the same RevenueCat
// project also sells subscriptions to the same people: entitlements would
// accumulate against a new customer per order rather than one customer. A store
// in that position should alias the order-shaped id to its real customer id in
// RevenueCat after the sale; this module deliberately does not, because
// aliasing is a decision about identity and not about this order.
//
// # No refunds through this module
//
// RevenueCat has no API to refund a Web Billing purchase — refunds are issued
// in the RevenueCat dashboard or the underlying processor. So this provider
// does not implement Refunder, for the same reason cash on delivery does not:
// claiming the capability and failing would be worse than not claiming it. The
// engine will report that the provider does not support refunds, which is true.
package revenuecat

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"context"

	gocommerce "github.com/misiki/gocommerce/core"
)

const (
	defaultBaseURL = "https://pay.rev.cat"
	// Matches Stripe's and Paddle's window: long enough for clock skew,
	// short enough that a recorded delivery is not a standing key.
	signatureTolerance = 5 * time.Minute
	maxWebhookBytes    = 1 << 20
)

// Config configures the module.
type Config struct {
	// LinkToken is the Web Purchase Link's token — the path segment in
	// https://pay.rev.cat/<token>. Required.
	LinkToken string `plugin:"link_token"`
	// WebhookAuthorization is the exact value RevenueCat is configured to send
	// in the Authorization header. Required: without it any caller could mark
	// orders paid.
	WebhookAuthorization string `plugin:"webhook_authorization"`
	// SigningSecret enables HMAC verification on top of the shared header, if
	// the webhook has signing switched on in RevenueCat. Optional, and worth
	// switching on: the header alone is a bearer secret that every delivery
	// repeats.
	SigningSecret string `plugin:"signing_secret"`
	// BaseURL overrides the purchase-link host, for tests.
	BaseURL string `plugin:"base_url"`
}

// Module is the RevenueCat payment provider.
type Module struct {
	cfg    Config
	live   atomic.Pointer[Config]
	app    *gocommerce.App
	log    *slog.Logger
	db     *sql.DB
	pay    *gocommerce.Payments
	orders *gocommerce.Orders
}

// New builds the module. Register it with gocommerce.New.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// PluginKey is the plugin this module registers, where the panel keeps its
// credentials. Config is the environment's fallback for each field; a value
// typed into the panel wins.
const PluginKey = "payments-revenuecat"

var errNotConfigured = errors.New("revenuecat: not set up — activate and configure it under Settings")

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
	return strings.TrimSpace(c.LinkToken) != "" &&
		strings.TrimSpace(c.WebhookAuthorization) != ""
}

// finish fills a configuration's defaults.
func (m *Module) finish(c *Config) {
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "payments-revenuecat" }

// Migrations implements gocommerce.Module.
//
// One table, to make webhook handling idempotent: RevenueCat retries with the
// same event id, and settling an order twice must be impossible rather than
// merely unlikely.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_events",
		SQL: `
			CREATE TABLE payments_revenuecat_events (
			    id          text        PRIMARY KEY,
			    type        text        NOT NULL,
			    order_id    bigint,
			    received_at timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX payments_revenuecat_events_received_idx
			    ON payments_revenuecat_events (received_at);`,
	}}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	// Config-configured stores start switched on; a store with nothing in
	// Config starts idle and waits for the panel.
	inCode := m.complete(&m.cfg)
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "RevenueCat", Category: "payments", DefaultEnabled: inCode,
		Description: "A RevenueCat Web Purchase Link as the checkout, with its webhook marking orders paid.",
		Docs:        "https://www.revenuecat.com/docs/",
		Fields: []gocommerce.PluginField{
			{Key: "link_token", Label: "Web Purchase Link token", Kind: "text", Required: !inCode, Help: "The path segment in https://pay.rev.cat/<token>."},
			{Key: "webhook_authorization", Label: "Webhook Authorization header", Kind: "secret", Required: !inCode, Help: "Exactly what RevenueCat is configured to send."},
			{Key: "signing_secret", Label: "Webhook signing secret", Kind: "secret", Help: "Adds HMAC verification when the webhook has signing on. Worth it."},
			{Key: "base_url", Label: "Purchase link host", Kind: "url", Help: "Empty for production."},
		},
	})
	m.finish(&m.cfg)
	m.log = app.Log()
	m.db = app.DB()
	m.pay = app.Pay()
	m.orders = app.Order()

	app.RegisterPayment(m)
	return nil
}

// Code implements gocommerce.PaymentProvider.
func (m *Module) Code() string { return "revenuecat" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "RevenueCat" }

// Initiate builds the shopper's purchase link. No network call: the link is a
// URL, and RevenueCat learns about the order when the webhook arrives.
func (m *Module) Initiate(ctx context.Context, order *gocommerce.Order, opts gocommerce.PayOptions) (gocommerce.PaymentIntent, error) {
	if !m.refresh(ctx) {
		return gocommerce.PaymentIntent{}, errNotConfigured
	}
	if order.Address.Line1 != "" || order.Address.PostalCode != "" {
		// Not fatal — a store may collect an address for its own records — but
		// worth saying once per order, because RevenueCat will not carry it and
		// nobody should discover that from a fulfilment queue.
		m.log.Warn("an order paid through RevenueCat carries a shipping address, which Web Billing does not collect or verify",
			"order_id", order.ID, "order_number", order.Number)
	}

	link := m.conf().BaseURL + "/" + url.PathEscape(m.conf().LinkToken) + "/" + url.PathEscape(order.Number)
	query := url.Values{}
	if order.Email != "" {
		query.Set("email", order.Email)
	}
	if order.Total.Currency != "" {
		query.Set("currency", strings.ToUpper(order.Total.Currency))
	}
	if packageID := opts.Data["package_id"]; packageID != "" {
		// Which catalogue item to preselect. The one piece of per-checkout
		// choice a purchase link accepts.
		query.Set("package_id", packageID)
	}
	if encoded := query.Encode(); encoded != "" {
		link += "?" + encoded
	}

	return gocommerce.PaymentIntent{
		Kind:     gocommerce.IntentRedirect,
		Provider: m.Code(),
		// The app user id, which is the order number: it is what the webhook
		// will carry back, and the only handle this integration has.
		Reference:  order.Number,
		ClientData: map[string]string{"url": link},
	}, nil
}

// Webhook implements gocommerce.WebhookProvider.
func (m *Module) Webhook() http.Handler { return http.HandlerFunc(m.handleWebhook) }

func (m *Module) handleWebhook(w http.ResponseWriter, r *http.Request) {
	// A notification for a provider nobody has set up cannot be verified,
	// and an unverifiable notification is refused.
	if !m.refresh(r.Context()) {
		http.Error(w, "payment method not set up", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBytes))
	if err != nil {
		http.Error(w, "could not read body", http.StatusBadRequest)
		return
	}

	// The shared header first: it is what RevenueCat always sends, and a
	// constant-time compare so a wrong value cannot be found a byte at a time.
	if subtle.ConstantTimeCompare(
		[]byte(r.Header.Get("Authorization")),
		[]byte(m.conf().WebhookAuthorization)) != 1 {
		m.log.Warn("rejected a RevenueCat webhook", "error", "Authorization did not match")
		http.Error(w, "invalid authorization", http.StatusBadRequest)
		return
	}
	if m.conf().SigningSecret != "" {
		if err := verifySignature(body,
			r.Header.Get(signatureHeader), m.conf().SigningSecret, time.Now()); err != nil {
			m.log.Warn("rejected a RevenueCat webhook", "error", err)
			http.Error(w, "invalid signature", http.StatusBadRequest)
			return
		}
	}

	var delivery struct {
		Event struct {
			ID                       string  `json:"id"`
			Type                     string  `json:"type"`
			AppUserID                string  `json:"app_user_id"`
			ProductID                string  `json:"product_id"`
			Currency                 string  `json:"currency"`
			PriceInPurchasedCurrency float64 `json:"price_in_purchased_currency"`
			TransactionID            string  `json:"transaction_id"`
			Store                    string  `json:"store"`
		} `json:"event"`
	}
	if err := json.Unmarshal(body, &delivery); err != nil {
		http.Error(w, "malformed event", http.StatusBadRequest)
		return
	}
	event := delivery.Event
	if event.ID == "" {
		http.Error(w, "event carries no id", http.StatusBadRequest)
		return
	}

	res, err := m.db.ExecContext(r.Context(), `
		INSERT INTO payments_revenuecat_events (id, type)
		VALUES ($1, $2)
		ON CONFLICT (id) DO NOTHING`, event.ID, event.Type)
	if err != nil {
		m.log.Error("could not record the RevenueCat event", "event_id", event.ID, "error", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Already handled. 200 stops the retries.
		w.WriteHeader(http.StatusOK)
		return
	}

	if event.Type == "TEST" {
		// The button in the dashboard. Acknowledged and nothing else.
		w.WriteHeader(http.StatusOK)
		return
	}

	order, err := m.orders.GetByNumber(r.Context(), event.AppUserID)
	if err != nil {
		// A purchase by somebody this store did not send — the same RevenueCat
		// project used by an app, or a customer id that is not an order number.
		// Acknowledged rather than retried for ever.
		m.log.Info("a RevenueCat event named no order this store knows",
			"event_id", event.ID, "app_user_id", event.AppUserID, "type", event.Type)
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, err := m.db.ExecContext(r.Context(),
		`UPDATE payments_revenuecat_events SET order_id = $2 WHERE id = $1`,
		event.ID, order.ID); err != nil {
		m.log.Warn("could not record which order a RevenueCat event was for",
			"event_id", event.ID, "order_id", order.ID, "error", err)
	}

	switch event.Type {
	case "INITIAL_PURCHASE", "NON_RENEWING_PURCHASE":
		// The one place the price can be checked. See the package comment for
		// why it has to be: RevenueCat, not the store, decided what to charge.
		paid, err := minorUnits(event.PriceInPurchasedCurrency, event.Currency)
		if err != nil || paid != order.Total.AmountMinor ||
			!strings.EqualFold(event.Currency, order.Total.Currency) {
			m.log.Error("a RevenueCat purchase does not match the order it names — the product's price and the order total have to agree",
				"order_id", order.ID, "order_total_minor", order.Total.AmountMinor,
				"order_currency", order.Total.Currency,
				"charged_minor", paid, "charged_currency", event.Currency,
				"product_id", event.ProductID)
			w.WriteHeader(http.StatusOK)
			return
		}
		// The engine performs the transition and writes the event. This module
		// never touches an order row itself.
		if _, err := m.pay.MarkPaid(r.Context(), order.ID, event.TransactionID); err != nil {
			m.log.Error("could not mark the order paid", "order_id", order.ID, "error", err)
			m.releaseClaim(r.Context(), event.ID)
			http.Error(w, "could not apply payment", http.StatusInternalServerError)
			return
		}
	case "CANCELLATION":
		// RevenueCat reports a refund as a cancellation. Nothing is undone
		// automatically — this module cannot issue refunds, so it cannot
		// pretend to have recorded one either.
		m.log.Warn("RevenueCat cancelled or refunded a purchase; the order still reads as paid",
			"order_id", order.ID, "transaction_id", event.TransactionID)
	}

	w.WriteHeader(http.StatusOK)
}

// releaseClaim un-claims an event whose handling failed, so the retry finds
// work to do instead of being told it was already handled.
func (m *Module) releaseClaim(ctx context.Context, id string) {
	if _, err := m.db.ExecContext(ctx,
		`DELETE FROM payments_revenuecat_events WHERE id = $1`, id); err != nil {
		m.log.Error("could not release the event claim", "event_id", id, "error", err)
	}
}

const signatureHeader = "X-RevenueCat-Webhook-Signature"

// verifySignature checks the optional HMAC header, which has Stripe's shape:
// `t=<unix seconds>,v1=<hex>`, over `<t>.<raw body>`.
func verifySignature(body []byte, header, secret string, now time.Time) error {
	header = strings.TrimSpace(header)
	if header == "" {
		return errors.New("missing " + signatureHeader + " header")
	}

	var timestamp, signature string
	for _, part := range strings.Split(header, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch key {
		case "t":
			timestamp = value
		case "v1":
			signature = value
		}
	}
	if timestamp == "" || signature == "" {
		return errors.New(signatureHeader + " is not t=…,v1=…")
	}

	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("signature timestamp %q is not a unix time", timestamp)
	}
	// The timestamp is inside the signed string, so it cannot be moved by
	// whoever captured the delivery — which is what makes this bound real.
	if drift := now.Sub(time.Unix(seconds, 0)); drift > signatureTolerance || drift < -signatureTolerance {
		return fmt.Errorf("signature timestamp is %s away from now", drift.Round(time.Second))
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(strings.ToLower(signature)), []byte(want)) {
		return errors.New(signatureHeader + " did not match")
	}
	return nil
}

// zeroDecimal and threeDecimal are the ISO 4217 currencies whose minor unit is
// not a hundredth. They are listed because RevenueCat reports a price as a
// float and this module has to turn it back into minor units to compare it
// with an order total — and getting JPY wrong by a factor of a hundred would
// mean every Japanese order silently failing to settle.
var zeroDecimal = map[string]bool{
	"BIF": true, "CLP": true, "DJF": true, "GNF": true, "ISK": true,
	"JPY": true, "KMF": true, "KRW": true, "PYG": true, "RWF": true,
	"UGX": true, "UYI": true, "VND": true, "VUV": true, "XAF": true,
	"XOF": true, "XPF": true,
}

var threeDecimal = map[string]bool{
	"BHD": true, "IQD": true, "JOD": true, "KWD": true,
	"LYD": true, "OMR": true, "TND": true,
}

// minorUnits converts a decimal amount to the currency's minor unit.
func minorUnits(amount float64, currency string) (int64, error) {
	code := strings.ToUpper(strings.TrimSpace(currency))
	if len(code) != 3 {
		return 0, fmt.Errorf("%q is not a currency code", currency)
	}
	scale := 100.0
	switch {
	case zeroDecimal[code]:
		scale = 1
	case threeDecimal[code]:
		scale = 1000
	}
	// Rounded rather than truncated, so a float that arrived as 9.989999 is
	// still 999.
	return int64(math.Round(amount * scale)), nil
}
