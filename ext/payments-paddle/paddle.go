// Package paddle takes payments through Paddle Billing.
//
// It speaks Paddle's REST API over net/http rather than through paddle-go, for
// the reason ext/payments-stripe gives: creating a transaction is one JSON POST
// and verifying a webhook is one HMAC, so the SDK would buy little and would
// put its whole dependency tree into the graph of every store that installs
// this module.
//
//	app, err := gocommerce.New(cfg,
//	    paddle.New(paddle.Config{
//	        APIKey:        os.Getenv("PADDLE_API_KEY"),
//	        WebhookSecret: os.Getenv("PADDLE_NOTIFICATION_SECRET"),
//	        Sandbox:       true,
//	    }),
//	)
//
// Paddle then calls POST /api/checkout/paddle/webhook, a route the engine owns
// and hands to this module with the body untouched.
//
// # Paddle is the merchant of record
//
// That is the difference from Stripe and it is not a detail. Paddle sells to
// the shopper, not the store, so Paddle decides the tax on the sale from the
// product's tax category and the buyer's location — whatever the engine's own
// computeTax already worked out. A store on Paddle should therefore leave
// GoCommerce's tax rates empty and let the total it sends be the price the
// shopper pays; configuring both means charging tax twice, once in the total
// and once again on top of it.
//
// This is documented rather than enforced, because the engine has no way to
// know which of the two a particular store intends — and silently zeroing a
// store's configured tax would be a worse surprise than a sentence here.
package paddle

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
	liveBaseURL    = "https://api.paddle.com"
	sandboxBaseURL = "https://sandbox-api.paddle.com"

	// webhookTolerance rejects replays of an old signed payload.
	//
	// Paddle's own SDK helpers default to five *seconds*, which is right for a
	// library running beside a synchronised clock and wrong for a store that
	// may be a second or two out — a webhook rejected for clock skew is an
	// order that never settles, and the retry arrives later still. Five minutes
	// is Stripe's recommendation for the identical construction and is the
	// number used here; a store that wants Paddle's stricter default can set
	// Config.WebhookTolerance.
	defaultTolerance = 5 * time.Minute
	maxWebhookBytes  = 1 << 20
)

// Config configures the module.
type Config struct {
	// APIKey is the Paddle API key. Required. It needs transaction.write to
	// create a transaction and adjustment.write to refund one.
	APIKey string `plugin:"api_key"`
	// WebhookSecret is the notification destination's signing secret.
	// Required: without it any caller could mark orders paid.
	WebhookSecret string `plugin:"webhook_secret"`
	// Sandbox points the module at sandbox-api.paddle.com. Paddle issues
	// separate keys for the two, so this is a deliberate flag rather than
	// something inferred from the key.
	Sandbox bool `plugin:"sandbox"`
	// BaseURL overrides the endpoint outright, for tests.
	BaseURL string `plugin:"base_url"`
	// WebhookTolerance overrides how old a signed payload may be. Zero takes
	// defaultTolerance.
	WebhookTolerance time.Duration
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the Paddle payment provider.
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
const PluginKey = "payments-paddle"

var errNotConfigured = errors.New("paddle: not set up — activate and configure it under Settings")

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
		strings.TrimSpace(c.WebhookSecret) != ""
}

// finish fills a configuration's defaults.
func (m *Module) finish(c *Config) {
	if c.BaseURL == "" {
		c.BaseURL = liveBaseURL
		if c.Sandbox {
			c.BaseURL = sandboxBaseURL
		}
	}
	if c.WebhookTolerance <= 0 {
		c.WebhookTolerance = defaultTolerance
	}
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "payments-paddle" }

// Migrations implements gocommerce.Module.
//
// The one table this module owns exists to make webhook handling idempotent:
// Paddle will deliver the same event more than once, and settling an order
// twice must be impossible rather than merely unlikely.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_events",
		SQL: `
			CREATE TABLE payments_paddle_events (
			    id          text        PRIMARY KEY,
			    type        text        NOT NULL,
			    order_id    bigint,
			    received_at timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX payments_paddle_events_received_idx
			    ON payments_paddle_events (received_at);`,
	}}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	// Config-configured stores start switched on; a store with nothing in
	// Config starts idle and waits for the panel.
	inCode := m.complete(&m.cfg)
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "Paddle", Category: "payments", DefaultEnabled: inCode,
		Description: "Transactions through Paddle Billing as merchant of record, with its signed notification marking orders paid.",
		Docs:        "https://developer.paddle.com/",
		Fields: []gocommerce.PluginField{
			{Key: "api_key", Label: "API key", Kind: "secret", Required: !inCode, Help: "Needs transaction.write and adjustment.write."},
			{Key: "webhook_secret", Label: "Notification signing secret", Kind: "secret", Required: !inCode},
			{Key: "sandbox", Label: "Sandbox", Kind: "bool", Help: "Points at sandbox-api.paddle.com; Paddle issues separate keys for the two."},
			{Key: "base_url", Label: "API base URL", Kind: "url", Help: "Overrides both hosts; for tests."},
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
func (m *Module) Code() string { return "paddle" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "Paddle" }

// Initiate creates a Paddle transaction and sends the shopper to its checkout.
//
// One ad-hoc line rather than one per order line. Paddle's items are its own
// catalogue's, and mirroring an order into them would mean either creating a
// Paddle product per variant — a second catalogue to keep in step — or sending
// line prices that Paddle would then tax individually under one shared tax
// category, which is not what the order's own tax worked out either. One line
// carrying the order total is the honest summary: Paddle collects what the
// engine says the order costs, and the order keeps the breakdown.
func (m *Module) Initiate(ctx context.Context, order *gocommerce.Order, opts gocommerce.PayOptions) (gocommerce.PaymentIntent, error) {
	if !m.refresh(ctx) {
		return gocommerce.PaymentIntent{}, errNotConfigured
	}
	custom := map[string]string{
		"order_id":     strconv.FormatInt(order.ID, 10),
		"order_number": order.Number,
	}
	for k, v := range opts.Data {
		// Client-supplied extras are namespaced so they cannot overwrite the
		// two fields the webhook depends on to find the order.
		custom["client_"+k] = v
	}

	body := map[string]any{
		"currency_code": strings.ToUpper(order.Total.Currency),
		"custom_data":   custom,
		"items": []map[string]any{{
			"quantity": 1,
			"price": map[string]any{
				"name": "Order " + order.Number,
				"unit_price": map[string]string{
					// Paddle takes minor units as a string, which is the same
					// decision the engine made for a different reason: an
					// amount that is never a float cannot pick up a rounding
					// error on the way through JSON.
					"amount":        strconv.FormatInt(order.Total.AmountMinor, 10),
					"currency_code": strings.ToUpper(order.Total.Currency),
				},
				"product": map[string]any{
					"name":         "Order " + order.Number,
					"tax_category": "standard",
				},
			},
		}},
	}

	var out struct {
		Data struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			Checkout struct {
				URL string `json:"url"`
			} `json:"checkout"`
		} `json:"data"`
	}
	if err := m.do(ctx, http.MethodPost, "/transactions", body, &out); err != nil {
		return gocommerce.PaymentIntent{}, err
	}
	if out.Data.Checkout.URL == "" {
		// A transaction with no checkout URL is one the shopper cannot pay.
		// Saying so here beats handing the storefront an intent it cannot act
		// on and letting it fail silently in the browser.
		return gocommerce.PaymentIntent{}, fmt.Errorf(
			"paddle: transaction %s came back without a checkout URL — "+
				"check that the store has an approved default payment link", out.Data.ID)
	}

	return gocommerce.PaymentIntent{
		Kind:      gocommerce.IntentRedirect,
		Provider:  m.Code(),
		Reference: out.Data.ID,
		ClientData: map[string]string{
			"url":    out.Data.Checkout.URL,
			"status": out.Data.Status,
		},
	}, nil
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
// Paddle refunds are adjustments against a transaction's *line items*, not
// against the transaction, so this reads the transaction back to learn the id
// of the single line Initiate wrote. Two calls rather than one, and the read is
// not cacheable: the engine refunds against whatever the order says its payment
// reference is, which may have been taken by an earlier release of this module.
//
// Always a partial adjustment carrying an explicit amount, even when it happens
// to be the whole transaction. A "full" adjustment refunds what Paddle thinks
// the transaction is worth, and the engine has already decided what it is
// refunding (D36) — letting the two disagree is how a partial refund becomes a
// full one.
func (m *Module) RefundWithReference(ctx context.Context, order *gocommerce.Order, amountMinor int64) (string, error) {
	if !m.refresh(ctx) {
		return "", errNotConfigured
	}
	if order.PaymentReference == "" {
		return "", errors.New("paddle: this order has no payment reference to refund")
	}

	var txn struct {
		Data struct {
			Details struct {
				LineItems []struct {
					ID string `json:"id"`
				} `json:"line_items"`
			} `json:"details"`
		} `json:"data"`
	}
	if err := m.do(ctx, http.MethodGet, "/transactions/"+order.PaymentReference, nil, &txn); err != nil {
		return "", err
	}
	if len(txn.Data.Details.LineItems) == 0 {
		return "", fmt.Errorf("paddle: transaction %s has no line items to refund", order.PaymentReference)
	}

	body := map[string]any{
		"action":         "refund",
		"transaction_id": order.PaymentReference,
		"reason":         "refund issued from the store",
		"items": []map[string]any{{
			"item_id": txn.Data.Details.LineItems[0].ID,
			"type":    "partial",
			"amount":  strconv.FormatInt(amountMinor, 10),
		}},
	}

	var adj struct {
		Data struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := m.do(ctx, http.MethodPost, "/adjustments", body, &adj); err != nil {
		return "", err
	}
	if adj.Data.Status == "rejected" {
		// The id goes back with the error: a refund Paddle refused still exists
		// on their side, and it is what somebody would quote asking why.
		return adj.Data.ID, fmt.Errorf("paddle: refund %s was rejected", adj.Data.ID)
	}
	return adj.Data.ID, nil
}

// Webhook implements gocommerce.WebhookProvider.
func (m *Module) Webhook() http.Handler {
	return http.HandlerFunc(m.handleWebhook)
}

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
	if err := verifySignature(body, r.Header.Get("Paddle-Signature"),
		m.conf().WebhookSecret, time.Now(), m.conf().WebhookTolerance); err != nil {
		m.log.Warn("rejected a Paddle webhook", "error", err)
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	var event struct {
		EventID   string `json:"event_id"`
		EventType string `json:"event_type"`
		Data      struct {
			ID         string            `json:"id"`
			Status     string            `json:"status"`
			CustomData map[string]string `json:"custom_data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "malformed event", http.StatusBadRequest)
		return
	}

	orderID, _ := strconv.ParseInt(event.Data.CustomData["order_id"], 10, 64)

	// Claim the event id. Paddle retries, and a second delivery of the same
	// event must not settle the order a second time. INSERT ... ON CONFLICT DO
	// NOTHING makes the claim atomic even across application instances.
	res, err := m.db.ExecContext(r.Context(), `
		INSERT INTO payments_paddle_events (id, type, order_id)
		VALUES ($1, $2, nullif($3, 0))
		ON CONFLICT (id) DO NOTHING`, event.EventID, event.EventType, orderID)
	if err != nil {
		m.log.Error("could not record the Paddle event", "event_id", event.EventID, "error", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Already handled. Answering 200 stops Paddle retrying it forever.
		w.WriteHeader(http.StatusOK)
		return
	}

	if orderID == 0 {
		// An event for something we did not start — a subscription renewal
		// billed in Paddle's own dashboard, say. Acknowledge it rather than
		// making Paddle retry what we will never act on.
		w.WriteHeader(http.StatusOK)
		return
	}

	switch event.EventType {
	// transaction.completed is the one that means the money arrived.
	// transaction.paid fires first and can still be reversed, so settling on
	// it would mark orders paid that Paddle has not finished collecting.
	case "transaction.completed":
		// The engine performs the transition and writes the event. This module
		// never touches an order row itself.
		if _, err := m.pay.MarkPaid(r.Context(), orderID, event.Data.ID); err != nil {
			m.log.Error("could not mark the order paid", "order_id", orderID, "error", err)
			m.releaseClaim(r.Context(), event.EventID)
			http.Error(w, "could not apply payment", http.StatusInternalServerError)
			return
		}
	case "transaction.payment_failed":
		if _, err := m.pay.MarkFailed(r.Context(), orderID, "paddle reported a failed payment"); err != nil {
			m.log.Error("could not mark the payment failed", "order_id", orderID, "error", err)
			m.releaseClaim(r.Context(), event.EventID)
			http.Error(w, "could not apply failure", http.StatusInternalServerError)
			return
		}
	case "adjustment.created", "adjustment.updated":
		m.log.Info("Paddle reported an adjustment", "order_id", orderID, "status", event.Data.Status)
	}

	w.WriteHeader(http.StatusOK)
}

// releaseClaim un-claims an event whose handling failed, so Paddle's retry
// finds work to do instead of being told the event was already handled.
func (m *Module) releaseClaim(ctx context.Context, eventID string) {
	if _, err := m.db.ExecContext(ctx,
		`DELETE FROM payments_paddle_events WHERE id = $1`, eventID); err != nil {
		m.log.Error("could not release the event claim", "event_id", eventID, "error", err)
	}
}

// verifySignature checks Paddle's `ts=...;h1=...` header.
//
// The signed payload is the timestamp, a colon, and the raw body — which is why
// the engine hands webhook routes their bytes untouched. Semicolons separate
// the parts and there may be several h1 values during a secret rotation, so any
// one matching is a pass.
//
// The timestamp is checked too: a valid signature over an old body is a replay.
func verifySignature(body []byte, header, secret string, now time.Time, tolerance time.Duration) error {
	if header == "" {
		return errors.New("missing Paddle-Signature header")
	}
	var (
		tsRaw      string
		signatures []string
	)
	for _, part := range strings.Split(header, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch key {
		case "ts":
			tsRaw = value
		case "h1":
			signatures = append(signatures, value)
		}
	}
	if tsRaw == "" || len(signatures) == 0 {
		return errors.New("Paddle-Signature is missing ts or h1")
	}

	seconds, err := strconv.ParseInt(tsRaw, 10, 64)
	if err != nil {
		return fmt.Errorf("Paddle-Signature has an unreadable timestamp: %w", err)
	}
	// Absolute difference: a payload from the future is as suspect as an old
	// one, and it is what a clock set wrong looks like.
	age := now.Sub(time.Unix(seconds, 0))
	if age < 0 {
		age = -age
	}
	if age > tolerance {
		return fmt.Errorf("Paddle-Signature is %s old, outside the %s tolerance", age.Round(time.Second), tolerance)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(tsRaw))
	mac.Write([]byte(":"))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))

	for _, got := range signatures {
		// Constant time, so a wrong signature cannot be found a byte at a time.
		if hmac.Equal([]byte(got), []byte(want)) {
			return nil
		}
	}
	return errors.New("no Paddle-Signature value matched")
}

// do performs one JSON request against the Paddle API.
func (m *Module) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("paddle: could not encode the request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, m.conf().BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.conf().APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("paddle: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxWebhookBytes))
	if err != nil {
		return fmt.Errorf("paddle: reading the response to %s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 300 {
		// Paddle's errors carry a code and a human sentence; both are worth
		// keeping, because "400" alone sends an operator to the dashboard.
		var fail struct {
			Error struct {
				Code   string `json:"code"`
				Detail string `json:"detail"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &fail) == nil && fail.Error.Detail != "" {
			return fmt.Errorf("paddle: %s %s: %s (%s)", method, path, fail.Error.Detail, fail.Error.Code)
		}
		return fmt.Errorf("paddle: %s %s: %s", method, path, strings.TrimSpace(string(payload)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("paddle: could not read the response to %s %s: %w", method, path, err)
	}
	return nil
}
