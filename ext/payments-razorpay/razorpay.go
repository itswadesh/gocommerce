// Package razorpay takes payments through Razorpay.
//
// Like the Stripe module, it speaks the vendor's REST API over net/http: an
// order is one JSON POST and a webhook is one HMAC, so the SDK would add a
// dependency without adding capability.
//
//	app, err := gocommerce.New(cfg,
//	    razorpay.New(razorpay.Config{
//	        KeyID:         os.Getenv("RAZORPAY_KEY_ID"),
//	        KeySecret:     os.Getenv("RAZORPAY_KEY_SECRET"),
//	        WebhookSecret: os.Getenv("RAZORPAY_WEBHOOK_SECRET"),
//	    }),
//	)
package razorpay

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

	"github.com/itswadesh/gocommerce/core"
)

const (
	defaultBaseURL  = "https://api.razorpay.com"
	maxWebhookBytes = 1 << 20
)

// Config configures the module.
type Config struct {
	// KeyID and KeySecret are the API credentials. Required.
	KeyID     string `plugin:"key_id"`
	KeySecret string `plugin:"key_secret"`
	// WebhookSecret signs the webhook. Required: without it any caller could
	// mark orders paid.
	WebhookSecret string `plugin:"webhook_secret"`
	// Hosted uses Razorpay's hosted checkout page and returns a redirect
	// intent instead of data for the in-page widget.
	Hosted bool `plugin:"hosted"`
	// BaseURL overrides the endpoint, for tests.
	BaseURL string `plugin:"base_url"`
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the Razorpay payment provider.
type Module struct {
	cfg    Config
	live   atomic.Pointer[Config]
	app    *gocommerce.App
	client *http.Client
	log    *slog.Logger
	db     *sql.DB
	pay    *gocommerce.Payments
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// PluginKey is the plugin this module registers, where the panel keeps its
// credentials. Config is the environment's fallback for each field; a value
// typed into the panel wins.
const PluginKey = "payments-razorpay"

var errNotConfigured = errors.New("razorpay: not set up — activate and configure it under Settings")

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
	return strings.TrimSpace(c.KeyID) != "" &&
		strings.TrimSpace(c.KeySecret) != "" &&
		strings.TrimSpace(c.WebhookSecret) != ""
}

// finish fills a configuration's defaults.
func (m *Module) finish(c *Config) {
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "payments-razorpay" }

// Migrations implements gocommerce.Module: one table, for webhook idempotency.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_events",
		SQL: `
			CREATE TABLE payments_razorpay_events (
			    id          text        PRIMARY KEY,
			    event       text        NOT NULL,
			    order_id    bigint,
			    received_at timestamptz NOT NULL DEFAULT now()
			);`,
	}}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	// Config-configured stores start switched on; a store with nothing in
	// Config starts idle and waits for the panel.
	inCode := m.complete(&m.cfg)
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "Razorpay", Category: "payments", DefaultEnabled: inCode,
		Description: "Cards, UPI and netbanking through Razorpay, India, with its signed webhook marking orders paid.",
		Docs:        "https://razorpay.com/docs/api/",
		Fields: []gocommerce.PluginField{
			{Key: "key_id", Label: "Key ID", Kind: "text", Required: !inCode},
			{Key: "key_secret", Label: "Key secret", Kind: "secret", Required: !inCode},
			{Key: "webhook_secret", Label: "Webhook secret", Kind: "secret", Required: !inCode},
			{Key: "hosted", Label: "Use the hosted checkout page", Kind: "bool", Help: "A redirect instead of the in-page widget."},
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
func (m *Module) Code() string { return "razorpay" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "Razorpay" }

// Initiate creates a Razorpay order for the checkout to be completed against.
func (m *Module) Initiate(ctx context.Context, order *gocommerce.Order, opts gocommerce.PayOptions) (gocommerce.PaymentIntent, error) {
	if !m.refresh(ctx) {
		return gocommerce.PaymentIntent{}, errNotConfigured
	}
	payload := map[string]any{
		"amount":   order.Total.AmountMinor,
		"currency": strings.ToUpper(order.Total.Currency),
		"receipt":  order.Number,
		"notes": map[string]string{
			"order_id":     strconv.FormatInt(order.ID, 10),
			"order_number": order.Number,
		},
	}

	var created struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
	}
	if err := m.post(ctx, "/v1/orders", payload, &created); err != nil {
		return gocommerce.PaymentIntent{}, err
	}

	intent := gocommerce.PaymentIntent{
		Provider:  m.Code(),
		Reference: created.ID,
		ClientData: map[string]string{
			"razorpay_order_id": created.ID,
			"key_id":            m.conf().KeyID,
			"amount":            strconv.FormatInt(created.Amount, 10),
			"currency":          created.Currency,
		},
	}
	if m.conf().Hosted {
		// The hosted page needs somewhere to send the shopper back to.
		intent.Kind = gocommerce.IntentRedirect
		if opts.ReturnURL != "" {
			intent.ClientData["callback_url"] = opts.ReturnURL
		}
	} else {
		intent.Kind = gocommerce.IntentClientAction
	}
	return intent, nil
}

// Refund implements gocommerce.Refunder.
func (m *Module) Refund(ctx context.Context, order *gocommerce.Order, amountMinor int64) error {
	if !m.refresh(ctx) {
		return errNotConfigured
	}
	_, err := m.RefundWithReference(ctx, order, amountMinor)
	return err
}

// RefundWithReference implements gocommerce.ReferencedRefunder: the same call,
// keeping the refund id Razorpay answers with instead of discarding the body.
// It is what somebody reconciles against a bank statement, and the engine
// records it on the refund row.
func (m *Module) RefundWithReference(ctx context.Context, order *gocommerce.Order, amountMinor int64) (string, error) {
	if !m.refresh(ctx) {
		return "", errNotConfigured
	}
	paymentID, err := m.paymentIDFor(ctx, order.ID)
	if err != nil {
		return "", err
	}
	var refund struct {
		ID string `json:"id"`
	}
	if err := m.post(ctx, "/v1/payments/"+paymentID+"/refund",
		map[string]any{"amount": amountMinor}, &refund); err != nil {
		return "", err
	}
	return refund.ID, nil
}

// paymentIDFor finds the captured payment behind an order. Razorpay refunds
// against the payment, not the order, and the two are different objects.
func (m *Module) paymentIDFor(ctx context.Context, orderID int64) (string, error) {
	var paymentID sql.NullString
	err := m.db.QueryRowContext(ctx, `
		SELECT id FROM payments_razorpay_events
		WHERE order_id = $1 AND event = 'payment.captured'
		ORDER BY received_at DESC LIMIT 1`, orderID).Scan(&paymentID)
	if errors.Is(err, sql.ErrNoRows) || !paymentID.Valid {
		return "", errors.New("razorpay: no captured payment recorded for this order")
	}
	if err != nil {
		return "", err
	}
	// The row id is the webhook event id; the payment id is stored alongside.
	var pid string
	if err := m.db.QueryRowContext(ctx, `
		SELECT coalesce(nullif(split_part(id, ':', 2), ''), id)
		FROM payments_razorpay_events WHERE id = $1`, paymentID.String).Scan(&pid); err != nil {
		return "", err
	}
	return pid, nil
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
	if !validSignature(body, r.Header.Get("X-Razorpay-Signature"), m.conf().WebhookSecret) {
		m.log.Warn("rejected a Razorpay webhook with an invalid signature")
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	var event struct {
		Event   string `json:"event"`
		Payload struct {
			Payment struct {
				Entity struct {
					ID    string            `json:"id"`
					Notes map[string]string `json:"notes"`
				} `json:"entity"`
			} `json:"payment"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "malformed event", http.StatusBadRequest)
		return
	}

	entity := event.Payload.Payment.Entity
	orderID, _ := strconv.ParseInt(entity.Notes["order_id"], 10, 64)
	// Razorpay has no per-delivery event id, so the payment id plus the event
	// name is the natural idempotency key: the same payment being captured is
	// the same fact however many times we are told about it.
	eventKey := event.Event + ":" + entity.ID

	res, err := m.db.ExecContext(r.Context(), `
		INSERT INTO payments_razorpay_events (id, event, order_id)
		VALUES ($1, $2, nullif($3, 0))
		ON CONFLICT (id) DO NOTHING`, eventKey, event.Event, orderID)
	if err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		w.WriteHeader(http.StatusOK) // already handled
		return
	}
	if orderID == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

	switch event.Event {
	case "payment.captured":
		if _, err := m.pay.MarkPaid(r.Context(), orderID, entity.ID); err != nil {
			m.log.Error("could not mark the order paid", "order_id", orderID, "error", err)
			m.releaseClaim(r.Context(), eventKey)
			http.Error(w, "could not apply payment", http.StatusInternalServerError)
			return
		}
	case "payment.failed":
		if _, err := m.pay.MarkFailed(r.Context(), orderID, "razorpay reported a failed payment"); err != nil {
			m.log.Error("could not mark the payment failed", "order_id", orderID, "error", err)
			m.releaseClaim(r.Context(), eventKey)
			http.Error(w, "could not apply failure", http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (m *Module) releaseClaim(ctx context.Context, id string) {
	if _, err := m.db.ExecContext(ctx,
		`DELETE FROM payments_razorpay_events WHERE id = $1`, id); err != nil {
		m.log.Error("could not release the event claim", "id", id, "error", err)
	}
}

// validSignature checks Razorpay's hex HMAC-SHA256 of the raw body.
func validSignature(body []byte, header, secret string) bool {
	if header == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	got, err := hex.DecodeString(strings.TrimSpace(header))
	if err != nil {
		return false
	}
	return hmac.Equal(got, mac.Sum(nil))
}

func (m *Module) post(ctx context.Context, path string, payload any, out any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.conf().BaseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.SetBasicAuth(m.conf().KeyID, m.conf().KeySecret)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("razorpay: %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("razorpay: %s: read response: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Error struct {
				Description string `json:"description"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &apiErr)
		if apiErr.Error.Description != "" {
			return fmt.Errorf("razorpay: %s: %s", path, apiErr.Error.Description)
		}
		return fmt.Errorf("razorpay: %s returned %s", path, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}
