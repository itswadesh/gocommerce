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
	"net/url"
	"strconv"
	"strings"
	"sync"
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
	live   sync.Map // store id → *Config: each store has its own account
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
func (m *Module) conf(ctx context.Context) *Config {
	store, _ := gocommerce.StoreFrom(ctx)
	if c, ok := m.live.Load(store); ok {
		return c.(*Config)
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
	store, _ := gocommerce.StoreFrom(ctx)
	m.live.Store(store, &c)
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
	},
		gocommerce.ScopeMigration("0002_store_scope", "payments_razorpay_events"),
		{
			// Two stores on one provider account each receive every event, so
			// the claim on an event id is each store's own (D83).
			ID:  "0003_events_per_store",
			SQL: `ALTER TABLE payments_razorpay_events DROP CONSTRAINT payments_razorpay_events_pkey, ADD PRIMARY KEY (store_id, id);`,
		},
	}
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
			{Key: "base_url", Label: "API base URL", Kind: "url", Outbound: true, Help: "Empty for production."},
		},
	})
	m.finish(&m.cfg)
	m.client = m.cfg.Client
	if m.client == nil {
		m.client = app.OutboundClient(20 * time.Second)
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
			"key_id":            m.conf(ctx).KeyID,
			"amount":            strconv.FormatInt(created.Amount, 10),
			"currency":          created.Currency,
		},
	}
	if m.conf(ctx).Hosted {
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
	paymentID, err := m.paymentIDFor(ctx, order)
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
//
// A payment recorded by asking Razorpay (Status) has no webhook row; its id is
// the order's payment reference instead.
func (m *Module) paymentIDFor(ctx context.Context, order *gocommerce.Order) (string, error) {
	var paymentID sql.NullString
	err := m.db.QueryRowContext(ctx, `
		SELECT id FROM payments_razorpay_events
		WHERE order_id = $1 AND event = 'payment.captured'
		ORDER BY received_at DESC LIMIT 1`, order.ID).Scan(&paymentID)
	if errors.Is(err, sql.ErrNoRows) || !paymentID.Valid {
		if ref := strings.TrimSpace(order.PaymentReference); strings.HasPrefix(ref, "pay_") {
			return ref, nil
		}
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

// razorpayPayment is one payment as Razorpay describes it.
type razorpayPayment struct {
	ID               string `json:"id"`
	Amount           int64  `json:"amount"`
	Currency         string `json:"currency"`
	Status           string `json:"status"`
	ErrorDescription string `json:"error_description"`
}

// Status implements gocommerce.PaymentStatusReader: it lists the payments on
// the order's Razorpay order, for the order whose webhook never came. Before
// the order is settled its payment reference is that Razorpay order; after, it
// is the payment, which is read on its own.
//
// Paid means captured (or since refunded, which it was first) for the order's
// total in its currency. An authorised payment is not money yet: Razorpay
// releases it if nobody captures it.
func (m *Module) Status(ctx context.Context, order *gocommerce.Order) (gocommerce.PaymentState, error) {
	if !m.refresh(ctx) {
		return gocommerce.PaymentState{}, errNotConfigured
	}
	ref := strings.TrimSpace(order.PaymentReference)
	var payments []razorpayPayment
	switch {
	case strings.HasPrefix(ref, "order_"):
		var list struct {
			Items []razorpayPayment `json:"items"`
		}
		if err := m.get(ctx, "/v1/orders/"+url.PathEscape(ref)+"/payments", &list); err != nil {
			return gocommerce.PaymentState{}, err
		}
		payments = list.Items
	case strings.HasPrefix(ref, "pay_"):
		var one razorpayPayment
		if err := m.get(ctx, "/v1/payments/"+url.PathEscape(ref), &one); err != nil {
			return gocommerce.PaymentState{}, err
		}
		payments = []razorpayPayment{one}
	default:
		return gocommerce.PaymentState{}, fmt.Errorf("razorpay: order %s has no Razorpay order to ask about (payment reference %q)", order.Number, ref)
	}
	if len(payments) == 0 {
		return gocommerce.PaymentState{Detail: "no payment attempted yet"}, nil
	}

	var state gocommerce.PaymentState
	for _, p := range payments {
		if p.Status != "captured" && p.Status != "refunded" {
			continue
		}
		currency := strings.ToUpper(p.Currency)
		if p.Amount == order.Total.AmountMinor && strings.EqualFold(currency, order.Total.Currency) {
			return gocommerce.PaymentState{Paid: true, Reference: p.ID, Detail: p.Status}, nil
		}
		state.Detail = fmt.Sprintf("%s for %d %s, not the order's %d %s",
			p.Status, p.Amount, currency, order.Total.AmountMinor, strings.ToUpper(order.Total.Currency))
	}
	if state.Detail != "" {
		return state, nil
	}
	// Razorpay lists the newest attempt first.
	latest := payments[0]
	state.Detail = latest.Status
	if latest.Status == "failed" {
		state.Failed = true
		if latest.ErrorDescription != "" {
			state.Detail += ": " + latest.ErrorDescription
		}
	}
	return state, nil
}

// Webhook implements gocommerce.WebhookProvider.
func (m *Module) Webhook() http.Handler { return http.HandlerFunc(m.handleWebhook) }

func (m *Module) handleWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
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
	if !validSignature(body, r.Header.Get("X-Razorpay-Signature"), m.conf(ctx).WebhookSecret) {
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
		ON CONFLICT (store_id, id) DO NOTHING`, eventKey, event.Event, orderID)
	if err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		w.WriteHeader(http.StatusOK) // already handled
		return
	}
	// An event for another store's order: two stores on one provider account
	// each receive every event. Acknowledged, and the claim given back, so it
	// is left for the store whose order it is.
	if orderID != 0 {
		if _, err := m.app.Order().Get(r.Context(), orderID); errors.Is(err, gocommerce.ErrNotFound) {
			m.releaseClaim(r.Context(), eventKey)
			w.WriteHeader(http.StatusOK)
			return
		}
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.conf(ctx).BaseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return m.do(ctx, req, path, out)
}

func (m *Module) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.conf(ctx).BaseURL+path, nil)
	if err != nil {
		return err
	}
	return m.do(ctx, req, path, out)
}

// do sends an authenticated request and decodes the JSON reply, or
// Razorpay's error.
func (m *Module) do(ctx context.Context, req *http.Request, path string, out any) error {
	req.SetBasicAuth(m.conf(ctx).KeyID, m.conf(ctx).KeySecret)
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
