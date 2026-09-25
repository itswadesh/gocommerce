// Package lemonsqueezy takes payments through Lemon Squeezy.
//
//	app, err := gocommerce.New(cfg,
//	    lemonsqueezy.New(lemonsqueezy.Config{
//	        APIKey:        os.Getenv("LEMONSQUEEZY_API_KEY"),
//	        WebhookSecret: os.Getenv("LEMONSQUEEZY_WEBHOOK_SECRET"),
//	        StoreID:       os.Getenv("LEMONSQUEEZY_STORE_ID"),
//	        VariantID:     os.Getenv("LEMONSQUEEZY_VARIANT_ID"),
//	    }),
//	)
//
// Lemon Squeezy then calls POST /api/checkout/lemonsqueezy/webhook, a route the
// engine owns and hands to this module with the body untouched.
//
// REST over net/http and no SDK, per rule 2: creating a checkout is one JSON
// POST and verifying a webhook is one HMAC.
//
// # Merchant of record, like Paddle
//
// Lemon Squeezy sells to the shopper, not the store, and works out the tax on
// the sale itself. A store using it should leave GoCommerce's tax rates empty
// and let the total it sends be what the shopper pays; configuring both charges
// tax twice. Documented rather than enforced, for the reason ext/payments-paddle
// gives: the engine cannot tell which of the two a store intends, and silently
// zeroing somebody's configured tax is a worse surprise than a paragraph.
//
// # Why this needs a variant id, and Paddle did not
//
// Paddle takes a fully ad-hoc price: a name, an amount, done. Lemon Squeezy
// sells items from its own catalogue, so a checkout must name a store and a
// variant that already exist there — `custom_price` then overrides what that
// variant costs.
//
// So a store using this module creates one placeholder product in Lemon Squeezy
// — any name, any price, never shown to a shopper — and names its variant here.
// Every order is that variant at the order's own price. It is a configuration
// step Paddle does not need, and it is stated here because the alternative is
// discovering it from a 422 at the first real checkout.
package lemonsqueezy

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	defaultBaseURL = "https://api.lemonsqueezy.com"
	// The JSON:API content type Lemon Squeezy speaks. Sending plain
	// application/json is refused, which is a confusing 415 to debug.
	mediaType       = "application/vnd.api+json"
	maxWebhookBytes = 1 << 20
)

// Config configures the module.
type Config struct {
	// APIKey is a Lemon Squeezy API key. Required.
	APIKey string `plugin:"api_key"`
	// WebhookSecret is the signing secret of the webhook. Required: without it
	// any caller could mark orders paid.
	WebhookSecret string `plugin:"webhook_secret"`
	// StoreID and VariantID name the placeholder catalogue item every checkout
	// is created against. Both required — see the package comment for why they
	// exist at all.
	StoreID   string `plugin:"store_id"`
	VariantID string `plugin:"variant_id"`
	// BaseURL overrides the endpoint, for tests.
	BaseURL string `plugin:"base_url"`
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the Lemon Squeezy payment provider.
type Module struct {
	cfg    Config
	live   atomic.Pointer[Config]
	app    *gocommerce.App
	client *http.Client
	log    *slog.Logger
	db     *sql.DB
	pay    *gocommerce.Payments
}

// New builds the module. Register it with gocommerce.New.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// PluginKey is the plugin this module registers, where the panel keeps its
// credentials. Config is the environment's fallback for each field; a value
// typed into the panel wins.
const PluginKey = "payments-lemonsqueezy"

var errNotConfigured = errors.New("lemonsqueezy: not set up — activate and configure it under Settings")

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
		strings.TrimSpace(c.WebhookSecret) != "" &&
		strings.TrimSpace(c.StoreID) != "" &&
		strings.TrimSpace(c.VariantID) != ""
}

// finish fills a configuration's defaults.
func (m *Module) finish(c *Config) {
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "payments-lemonsqueezy" }

// Migrations implements gocommerce.Module.
//
// One table, to make webhook handling idempotent: Lemon Squeezy retries, and
// settling an order twice must be impossible rather than merely unlikely.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_events",
		SQL: `
			CREATE TABLE payments_lemonsqueezy_events (
			    id          text        PRIMARY KEY,
			    type        text        NOT NULL,
			    order_id    bigint,
			    received_at timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX payments_lemonsqueezy_events_received_idx
			    ON payments_lemonsqueezy_events (received_at);`,
	}}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	// Config-configured stores start switched on; a store with nothing in
	// Config starts idle and waits for the panel.
	inCode := m.complete(&m.cfg)
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "Lemon Squeezy", Category: "payments", DefaultEnabled: inCode,
		Description: "Checkouts through Lemon Squeezy as merchant of record, each created against one placeholder variant.",
		Docs:        "https://docs.lemonsqueezy.com/api",
		Fields: []gocommerce.PluginField{
			{Key: "api_key", Label: "API key", Kind: "secret", Required: !inCode},
			{Key: "webhook_secret", Label: "Webhook signing secret", Kind: "secret", Required: !inCode},
			{Key: "store_id", Label: "Store ID", Kind: "text", Required: !inCode},
			{Key: "variant_id", Label: "Placeholder variant ID", Kind: "text", Required: !inCode, Help: "The catalogue item every checkout is created against."},
			{Key: "base_url", Label: "API base URL", Kind: "url", Help: "Empty for production."},
		},
	})
	m.finish(&m.cfg)
	m.client = m.cfg.Client
	if m.client == nil {
		m.client = &http.Client{Timeout: 20 * time.Second}
	}
	m.log = app.Log()
	m.db = app.DB()
	m.pay = app.Pay()

	app.RegisterPayment(m)
	return nil
}

// Code implements gocommerce.PaymentProvider.
func (m *Module) Code() string { return "lemonsqueezy" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "Lemon Squeezy" }

// Initiate creates a checkout and sends the shopper to its hosted page.
func (m *Module) Initiate(ctx context.Context, order *gocommerce.Order, opts gocommerce.PayOptions) (gocommerce.PaymentIntent, error) {
	if !m.refresh(ctx) {
		return gocommerce.PaymentIntent{}, errNotConfigured
	}
	custom := map[string]string{
		"order_id":     strconv.FormatInt(order.ID, 10),
		"order_number": order.Number,
	}
	for k, v := range opts.Data {
		// Namespaced, so client-supplied extras cannot overwrite the two fields
		// the webhook depends on to find the order.
		custom["client_"+k] = v
	}

	checkoutData := map[string]any{"custom": custom}
	if order.Email != "" {
		checkoutData["email"] = order.Email
	}
	if order.Name != "" {
		checkoutData["name"] = order.Name
	}

	body := map[string]any{"data": map[string]any{
		"type": "checkouts",
		"attributes": map[string]any{
			// Minor units, which is what the engine holds and what Lemon
			// Squeezy takes — no conversion, so nothing to round wrongly.
			"custom_price":  order.Total.AmountMinor,
			"checkout_data": checkoutData,
		},
		"relationships": map[string]any{
			"store":   resource("stores", m.conf().StoreID),
			"variant": resource("variants", m.conf().VariantID),
		},
	}}

	var out struct {
		Data struct {
			ID         string `json:"id"`
			Attributes struct {
				URL string `json:"url"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := m.do(ctx, http.MethodPost, "/v1/checkouts", body, &out); err != nil {
		return gocommerce.PaymentIntent{}, err
	}
	if out.Data.Attributes.URL == "" {
		return gocommerce.PaymentIntent{}, fmt.Errorf(
			"lemonsqueezy: checkout %s came back without a URL", out.Data.ID)
	}

	return gocommerce.PaymentIntent{
		Kind:     gocommerce.IntentRedirect,
		Provider: m.Code(),
		// The checkout id, not an order id: Lemon Squeezy only mints its own
		// order id once somebody pays, and the webhook is what supplies it.
		Reference:  out.Data.ID,
		ClientData: map[string]string{"url": out.Data.Attributes.URL},
	}, nil
}

func resource(kind, id string) map[string]any {
	return map[string]any{"data": map[string]any{"type": kind, "id": id}}
}

// Refund implements gocommerce.Refunder.
func (m *Module) Refund(ctx context.Context, order *gocommerce.Order, amountMinor int64) error {
	if !m.refresh(ctx) {
		return errNotConfigured
	}
	_, err := m.RefundWithReference(ctx, order, amountMinor)
	return err
}

// RefundWithReference implements gocommerce.ReferencedRefunder.
//
// The payment reference here is Lemon Squeezy's *order* id, which the webhook
// wrote when the sale completed — not the checkout id Initiate returned. A
// refund before that webhook has arrived has nothing to refund against, and
// says so rather than posting to a URL built from a checkout id.
func (m *Module) RefundWithReference(ctx context.Context, order *gocommerce.Order, amountMinor int64) (string, error) {
	if !m.refresh(ctx) {
		return "", errNotConfigured
	}
	if order.PaymentReference == "" {
		return "", errors.New("lemonsqueezy: this order has no payment reference to refund")
	}

	body := map[string]any{"data": map[string]any{
		"type":       "orders",
		"id":         order.PaymentReference,
		"attributes": map[string]any{"amount": amountMinor},
	}}

	var out struct {
		Data struct {
			ID         string `json:"id"`
			Attributes struct {
				Status string `json:"status"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := m.do(ctx, http.MethodPost,
		"/v1/orders/"+order.PaymentReference+"/refund", body, &out); err != nil {
		return "", err
	}
	return out.Data.ID, nil
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
	if err := verifySignature(body, r.Header.Get("X-Signature"), m.conf().WebhookSecret); err != nil {
		m.log.Warn("rejected a Lemon Squeezy webhook", "error", err)
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	var event struct {
		Meta struct {
			EventName  string            `json:"event_name"`
			CustomData map[string]string `json:"custom_data"`
		} `json:"meta"`
		Data struct {
			ID         string `json:"id"`
			Attributes struct {
				Status string `json:"status"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "malformed event", http.StatusBadRequest)
		return
	}

	orderID, _ := strconv.ParseInt(event.Meta.CustomData["order_id"], 10, 64)

	// Lemon Squeezy sends no event id of its own, so the claim is keyed on the
	// event name and the object it concerns — which is what makes a retry of
	// the same delivery a duplicate, and two different events about one order
	// still distinct.
	claim := event.Meta.EventName + ":" + event.Data.ID
	res, err := m.db.ExecContext(r.Context(), `
		INSERT INTO payments_lemonsqueezy_events (id, type, order_id)
		VALUES ($1, $2, nullif($3, 0))
		ON CONFLICT (id) DO NOTHING`, claim, event.Meta.EventName, orderID)
	if err != nil {
		m.log.Error("could not record the Lemon Squeezy event", "claim", claim, "error", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Already handled. 200 stops the retries.
		w.WriteHeader(http.StatusOK)
		return
	}

	if orderID == 0 {
		// An event for something this store did not start — a subscription
		// renewal, a dashboard sale. Acknowledged rather than retried forever.
		w.WriteHeader(http.StatusOK)
		return
	}

	switch event.Meta.EventName {
	case "order_created":
		// `paid` is the status of a completed sale; anything else is a sale
		// that has not settled, and settling on it would mark orders paid that
		// Lemon Squeezy is still collecting or has refused.
		if !strings.EqualFold(event.Data.Attributes.Status, "paid") {
			w.WriteHeader(http.StatusOK)
			return
		}
		// The engine performs the transition and writes the event. This module
		// never touches an order row itself. The reference stored is Lemon
		// Squeezy's order id, which is what a refund is issued against.
		if _, err := m.pay.MarkPaid(r.Context(), orderID, event.Data.ID); err != nil {
			m.log.Error("could not mark the order paid", "order_id", orderID, "error", err)
			m.releaseClaim(r.Context(), claim)
			http.Error(w, "could not apply payment", http.StatusInternalServerError)
			return
		}
	case "order_refunded":
		m.log.Info("Lemon Squeezy reported a refund", "order_id", orderID)
	}

	w.WriteHeader(http.StatusOK)
}

// releaseClaim un-claims an event whose handling failed, so the retry finds
// work to do instead of being told it was already handled.
func (m *Module) releaseClaim(ctx context.Context, claim string) {
	if _, err := m.db.ExecContext(ctx,
		`DELETE FROM payments_lemonsqueezy_events WHERE id = $1`, claim); err != nil {
		m.log.Error("could not release the event claim", "claim", claim, "error", err)
	}
}

// verifySignature checks the X-Signature header.
//
// A plain HMAC-SHA256 hex digest of the raw body, with no timestamp — which is
// the one real difference from Stripe and Paddle. There is nothing to bound a
// replay with: a signature Lemon Squeezy produced stays valid for that body for
// ever, so the idempotency claim above is not a nicety here, it is the only
// thing standing between a captured delivery and settling an order twice.
func verifySignature(body []byte, header, secret string) error {
	header = strings.TrimSpace(header)
	if header == "" {
		return errors.New("missing X-Signature header")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	// Constant time, so a wrong signature cannot be found a byte at a time.
	if !hmac.Equal([]byte(header), []byte(want)) {
		return errors.New("X-Signature did not match")
	}
	return nil
}

// do performs one JSON:API request.
func (m *Module) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("lemonsqueezy: could not encode the request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, m.conf().BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.conf().APIKey)
	req.Header.Set("Accept", mediaType)
	if body != nil {
		req.Header.Set("Content-Type", mediaType)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("lemonsqueezy: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxWebhookBytes))
	if err != nil {
		return fmt.Errorf("lemonsqueezy: reading the response to %s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 300 {
		// JSON:API returns an errors array; the detail is the sentence worth
		// keeping, because "422" alone sends an operator to the dashboard.
		var fail struct {
			Errors []struct {
				Detail string `json:"detail"`
				Title  string `json:"title"`
			} `json:"errors"`
		}
		if json.Unmarshal(payload, &fail) == nil && len(fail.Errors) > 0 {
			first := fail.Errors[0]
			return fmt.Errorf("lemonsqueezy: %s %s: %s (%s)", method, path, first.Detail, first.Title)
		}
		return fmt.Errorf("lemonsqueezy: %s %s: %s", method, path, strings.TrimSpace(string(payload)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("lemonsqueezy: could not read the response to %s %s: %w", method, path, err)
	}
	return nil
}
