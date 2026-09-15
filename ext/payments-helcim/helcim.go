// Package helcim takes payments through Helcim.
//
//	app, err := gocommerce.New(cfg,
//	    helcim.New(helcim.Config{
//	        APIToken:      os.Getenv("HELCIM_API_TOKEN"),
//	        VerifierToken: os.Getenv("HELCIM_VERIFIER_TOKEN"),
//	    }),
//	)
//
// Helcim then calls POST /api/checkout/helcim/webhook, a route the engine owns
// and hands to this module with the body untouched.
//
// REST over net/http and no SDK, per rule 2.
//
// # An in-page modal, not a redirect
//
// Helcim's hosted checkout is HelcimPay.js: the server asks for a checkout
// token, the storefront calls `appendHelcimPayIframe(checkoutToken)` and the
// shopper pays in a modal without leaving the page. There is no hosted URL to
// redirect to, so this provider returns a client_action intent carrying the
// token rather than a redirect. A storefront using it loads Helcim's script
// itself; that is the one thing this module cannot do for it.
//
// # The webhook does not carry the payment
//
// Helcim's webhook body is two fields — an id and a type — and nothing else.
// No amount, no invoice, no status. So this module reads the transaction back
// from the Card Transactions API before deciding anything, which is the only
// way to know what the notification was actually about. That second call is
// not optional and not an optimisation to remove: a webhook body that says only
// "transaction 25764674 happened" cannot settle an order on its own.
//
// # Amounts
//
// Helcim's API takes a decimal amount, not minor units. Helcim settles in CAD
// and USD, both of which have two decimal places, so the conversion is an exact
// division by 100 and the reverse is exact too. A currency with a different
// exponent would need more care, and Helcim does not offer one.
package helcim

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	gocommerce "github.com/misiki/gocommerce/core"
)

const (
	defaultBaseURL = "https://api.helcim.com/v2"
	// Helcim signs a timestamp, so unlike Lemon Squeezy a captured delivery
	// does go stale. Five minutes is generous for clock skew and short enough
	// that a recorded request is not a standing key to the store.
	signatureTolerance = 5 * time.Minute
	maxWebhookBytes    = 1 << 20
)

// Config configures the module.
type Config struct {
	// APIToken is a Helcim API access token, sent as the `api-token` header.
	// Required.
	APIToken string `plugin:"api_token"`
	// VerifierToken is the webhook verifier token from Helcim's settings,
	// base64 as Helcim shows it. Required: without it any caller could mark
	// orders paid.
	VerifierToken string `plugin:"verifier_token"`
	// verifierBytes is VerifierToken decoded, filled by finish.
	verifierBytes []byte
	// ServerIP is sent as the `ipAddress` of a refund. Helcim requires the
	// field on every payment call; on a refund there is no shopper making the
	// request, so this is the machine that is. Defaults to 127.0.0.1.
	ServerIP string `plugin:"server_ip"`
	// BaseURL overrides the endpoint, for tests.
	BaseURL string `plugin:"base_url"`
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the Helcim payment provider.
type Module struct {
	cfg    Config
	live   atomic.Pointer[Config]
	app    *gocommerce.App
	client *http.Client
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
const PluginKey = "payments-helcim"

var errNotConfigured = errors.New("helcim: not set up — activate and configure it under Settings")

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
	return strings.TrimSpace(c.APIToken) != "" &&
		strings.TrimSpace(c.VerifierToken) != ""
}

// finish fills a configuration's defaults.
func (m *Module) finish(c *Config) {
	// Base64 as Helcim shows it, bytes in the HMAC; one that does not decode
	// leaves the provider unconfigured.
	c.verifierBytes, _ = base64.StdEncoding.DecodeString(strings.TrimSpace(c.VerifierToken))
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "payments-helcim" }

// Migrations implements gocommerce.Module.
//
// One table, to make webhook handling idempotent: Helcim retries, and settling
// an order twice must be impossible rather than merely unlikely.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_events",
		SQL: `
			CREATE TABLE payments_helcim_events (
			    id          text        PRIMARY KEY,
			    type        text        NOT NULL,
			    order_id    bigint,
			    received_at timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX payments_helcim_events_received_idx
			    ON payments_helcim_events (received_at);`,
	}}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	// Config-configured stores start switched on; a store with nothing in
	// Config starts idle and waits for the panel.
	inCode := m.complete(&m.cfg)
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "Helcim", Category: "payments", DefaultEnabled: inCode,
		Description: "Card payments through Helcim's HelcimPay.js, with the verified webhook marking orders paid.",
		Docs:        "https://devdocs.helcim.com/",
		Fields: []gocommerce.PluginField{
			{Key: "api_token", Label: "API token", Kind: "secret", Required: !inCode, Help: "Sent as the api-token header."},
			{Key: "verifier_token", Label: "Webhook verifier token", Kind: "secret", Required: !inCode, Help: "Base64, as Helcim shows it."},
			{Key: "server_ip", Label: "Server IP for refunds", Kind: "text", Help: "Helcim wants an ipAddress on every payment call; a refund has no shopper, so this is the machine's.", Default: "127.0.0.1"},
			{Key: "base_url", Label: "API base URL", Kind: "url", Help: "Empty for production."},
		},
	})
	m.finish(&m.cfg)
	if strings.TrimSpace(m.cfg.ServerIP) == "" {
		m.cfg.ServerIP = "127.0.0.1"
	}
	m.client = m.cfg.Client
	if m.client == nil {
		m.client = &http.Client{Timeout: 20 * time.Second}
	}
	m.log = app.Log()
	m.db = app.DB()
	m.pay = app.Pay()
	m.orders = app.Order()

	app.RegisterPayment(m)
	return nil
}

// Code implements gocommerce.PaymentProvider.
func (m *Module) Code() string { return "helcim" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "Helcim" }

// Initiate opens a HelcimPay.js checkout session.
func (m *Module) Initiate(ctx context.Context, order *gocommerce.Order, opts gocommerce.PayOptions) (gocommerce.PaymentIntent, error) {
	if !m.refresh(ctx) {
		return gocommerce.PaymentIntent{}, errNotConfigured
	}
	body := map[string]any{
		"paymentType": "purchase",
		"amount":      decimal(order.Total.AmountMinor),
		"currency":    strings.ToUpper(order.Total.Currency),
		// The only field that comes back on the transaction and identifies the
		// order — see handleWebhook, which has nothing else to go on.
		"invoiceNumber": order.Number,
	}

	var out struct {
		CheckoutToken string `json:"checkoutToken"`
		SecretToken   string `json:"secretToken"`
	}
	if err := m.do(ctx, http.MethodPost, "/helcim-pay/initialize", body, "", &out); err != nil {
		return gocommerce.PaymentIntent{}, err
	}
	if out.CheckoutToken == "" {
		return gocommerce.PaymentIntent{}, errors.New("helcim: the checkout session came back without a token")
	}

	// secretToken is deliberately dropped rather than stored. It exists so a
	// page can verify the hash HelcimPay.js posts back to it, and this module
	// settles from the webhook instead — so keeping it would mean holding a
	// secret nothing reads, which is a liability with no benefit.
	return gocommerce.PaymentIntent{
		Kind:      gocommerce.IntentClientAction,
		Provider:  m.Code(),
		Reference: out.CheckoutToken,
		ClientData: map[string]string{
			"checkout_token": out.CheckoutToken,
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
func (m *Module) RefundWithReference(ctx context.Context, order *gocommerce.Order, amountMinor int64) (string, error) {
	if !m.refresh(ctx) {
		return "", errNotConfigured
	}
	// The payment reference is Helcim's transaction id, which the webhook wrote
	// when the sale settled — not the checkout token Initiate returned.
	original, err := strconv.ParseInt(strings.TrimSpace(order.PaymentReference), 10, 64)
	if err != nil || original <= 0 {
		return "", fmt.Errorf(
			"helcim: this order has no Helcim transaction to refund (payment reference %q)",
			order.PaymentReference)
	}

	body := map[string]any{
		"originalTransactionId": original,
		"amount":                decimal(amountMinor),
		"ipAddress":             m.conf().ServerIP,
		"ecommerce":             true,
	}

	var out struct {
		TransactionID json.Number `json:"transactionId"`
		Status        string      `json:"status"`
	}
	// Helcim requires an idempotency key on every payment call, and keying it
	// on the order and the amount means a retried refund of the same amount is
	// the same request rather than a second one.
	idempotency := fmt.Sprintf("gc-refund-%d-%d", order.ID, amountMinor)
	if err := m.do(ctx, http.MethodPost, "/payment/refund", body, idempotency, &out); err != nil {
		return "", err
	}
	if out.Status != "" && !strings.EqualFold(out.Status, "APPROVED") {
		return "", fmt.Errorf("helcim: refund %s was %s, not approved", out.TransactionID, out.Status)
	}
	return out.TransactionID.String(), nil
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
	if err := verifySignature(body,
		r.Header.Get("webhook-id"),
		r.Header.Get("webhook-timestamp"),
		r.Header.Get("webhook-signature"),
		m.conf().verifierBytes, time.Now()); err != nil {
		m.log.Warn("rejected a Helcim webhook", "error", err)
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	var event struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &event); err != nil || event.ID == "" {
		http.Error(w, "malformed event", http.StatusBadRequest)
		return
	}

	claim := event.Type + ":" + event.ID
	res, err := m.db.ExecContext(r.Context(), `
		INSERT INTO payments_helcim_events (id, type)
		VALUES ($1, $2)
		ON CONFLICT (id) DO NOTHING`, claim, event.Type)
	if err != nil {
		m.log.Error("could not record the Helcim event", "claim", claim, "error", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Already handled. 200 stops the retries.
		w.WriteHeader(http.StatusOK)
		return
	}

	if event.Type != "cardTransaction" {
		// An invoice or a customer event: nothing this provider acts on.
		w.WriteHeader(http.StatusOK)
		return
	}

	// The body carried an id and a type. Everything that decides what happens
	// to an order — which order, how much, whether it was approved — has to be
	// read back.
	transaction, err := m.transaction(r.Context(), event.ID)
	if err != nil {
		m.log.Error("could not read the Helcim transaction", "id", event.ID, "error", err)
		m.releaseClaim(r.Context(), claim)
		http.Error(w, "could not read the transaction", http.StatusInternalServerError)
		return
	}

	order, err := m.orders.GetByNumber(r.Context(), transaction.InvoiceNumber)
	if err != nil {
		// A transaction taken at a terminal, or through Helcim's own invoicing:
		// real money, but not this store's order. Acknowledged rather than
		// retried for ever.
		m.log.Info("a Helcim transaction named no order this store knows",
			"id", event.ID, "invoice_number", transaction.InvoiceNumber)
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, err := m.db.ExecContext(r.Context(),
		`UPDATE payments_helcim_events SET order_id = $2 WHERE id = $1`, claim, order.ID); err != nil {
		m.log.Warn("could not record which order a Helcim event was for",
			"claim", claim, "order_id", order.ID, "error", err)
	}

	// Only a purchase settles an order. A preauth is authorised and not taken,
	// a verify moves no money at all, and a refund is the store's own doing
	// coming back round.
	if !strings.EqualFold(transaction.Type, "purchase") {
		w.WriteHeader(http.StatusOK)
		return
	}
	if !strings.EqualFold(transaction.Status, "APPROVED") {
		if _, err := m.pay.MarkFailed(r.Context(), order.ID,
			firstNonEmpty(transaction.Status, "declined")); err != nil {
			m.log.Error("could not mark the payment failed", "order_id", order.ID, "error", err)
			m.releaseClaim(r.Context(), claim)
			http.Error(w, "could not apply the failure", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	// The webhook is what reveals the amount, so this is the first chance to
	// check it — and the last. Settling an order on a transaction for a
	// different sum would put the store's books and Helcim's out of step with
	// nobody the wiser, so a mismatch stops here and is left for an operator
	// who can see both sides.
	if paid := minorUnits(transaction.Amount); paid != order.Total.AmountMinor ||
		!strings.EqualFold(transaction.Currency, order.Total.Currency) {
		m.log.Error("a Helcim transaction does not match the order it names",
			"order_id", order.ID, "order_total_minor", order.Total.AmountMinor,
			"order_currency", order.Total.Currency,
			"transaction_minor", paid, "transaction_currency", transaction.Currency,
			"transaction_id", transaction.TransactionID.String())
		w.WriteHeader(http.StatusOK)
		return
	}

	// The engine performs the transition and writes the event. This module
	// never touches an order row itself.
	if _, err := m.pay.MarkPaid(r.Context(), order.ID, transaction.TransactionID.String()); err != nil {
		m.log.Error("could not mark the order paid", "order_id", order.ID, "error", err)
		m.releaseClaim(r.Context(), claim)
		http.Error(w, "could not apply payment", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// cardTransaction is the part of Helcim's transaction this module reads.
type cardTransaction struct {
	TransactionID json.Number `json:"transactionId"`
	Status        string      `json:"status"`
	Type          string      `json:"type"`
	Amount        float64     `json:"amount"`
	Currency      string      `json:"currency"`
	InvoiceNumber string      `json:"invoiceNumber"`
}

func (m *Module) transaction(ctx context.Context, id string) (cardTransaction, error) {
	var out cardTransaction
	err := m.do(ctx, http.MethodGet, "/card-transactions/"+id, nil, "", &out)
	return out, err
}

// releaseClaim un-claims an event whose handling failed, so the retry finds
// work to do instead of being told it was already handled.
func (m *Module) releaseClaim(ctx context.Context, claim string) {
	if _, err := m.db.ExecContext(ctx,
		`DELETE FROM payments_helcim_events WHERE id = $1`, claim); err != nil {
		m.log.Error("could not release the event claim", "claim", claim, "error", err)
	}
}

// verifySignature checks Helcim's three webhook headers.
//
// The signed string is `id.timestamp.body`, the key is the verifier token after
// base64 decoding, and the header holds one or more space-separated
// `v1,<base64>` signatures — more than one while a token is being rotated, so
// any match is a match.
func verifySignature(body []byte, id, timestamp, header string, verifier []byte, now time.Time) error {
	switch {
	case strings.TrimSpace(id) == "":
		return errors.New("missing webhook-id header")
	case strings.TrimSpace(timestamp) == "":
		return errors.New("missing webhook-timestamp header")
	case strings.TrimSpace(header) == "":
		return errors.New("missing webhook-signature header")
	}

	seconds, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return fmt.Errorf("webhook-timestamp %q is not a unix time", timestamp)
	}
	// The timestamp is inside the signed string, so an attacker replaying a
	// captured delivery cannot move it — which is what makes this bound real.
	if drift := now.Sub(time.Unix(seconds, 0)); drift > signatureTolerance || drift < -signatureTolerance {
		return fmt.Errorf("webhook-timestamp is %s away from now", drift.Round(time.Second))
	}

	mac := hmac.New(sha256.New, verifier)
	mac.Write([]byte(id + "." + timestamp + "."))
	mac.Write(body)
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	for _, candidate := range strings.Fields(header) {
		version, signature, ok := strings.Cut(candidate, ",")
		if !ok || version != "v1" {
			continue
		}
		// Constant time, so a wrong signature cannot be found a byte at a time.
		if hmac.Equal([]byte(signature), []byte(want)) {
			return nil
		}
	}
	return errors.New("webhook-signature did not match")
}

// decimal turns minor units into the decimal amount Helcim's API takes. Exact
// for CAD and USD, which are the currencies Helcim settles in.
func decimal(minor int64) float64 {
	return float64(minor) / 100
}

// minorUnits is decimal's inverse, rounded rather than truncated so that a
// float that arrived as 25.099999 is still 2510.
func minorUnits(amount float64) int64 {
	return int64(math.Round(amount * 100))
}

// do performs one JSON request.
func (m *Module) do(ctx context.Context, method, path string, body any, idempotency string, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("helcim: could not encode the request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, m.conf().BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("api-token", m.conf().APIToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotency != "" {
		req.Header.Set("idempotency-key", idempotency)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("helcim: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxWebhookBytes))
	if err != nil {
		return fmt.Errorf("helcim: reading the response to %s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 300 {
		var fail struct {
			Errors any    `json:"errors"`
			Error  string `json:"error"`
		}
		if json.Unmarshal(payload, &fail) == nil {
			if fail.Error != "" {
				return fmt.Errorf("helcim: %s %s: %s", method, path, fail.Error)
			}
			if fail.Errors != nil {
				return fmt.Errorf("helcim: %s %s: %v", method, path, fail.Errors)
			}
		}
		return fmt.Errorf("helcim: %s %s: %s", method, path, strings.TrimSpace(string(payload)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("helcim: could not read the response to %s %s: %w", method, path, err)
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
