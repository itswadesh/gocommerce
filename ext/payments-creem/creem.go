// Package creem takes payments through Creem.
//
//	app, err := gocommerce.New(cfg,
//	    creem.New(creem.Config{
//	        APIKey:        os.Getenv("CREEM_API_KEY"),
//	        WebhookSecret: os.Getenv("CREEM_WEBHOOK_SECRET"),
//	        ProductID:     os.Getenv("CREEM_PRODUCT_ID"),
//	    }),
//	)
//
// Creem then calls POST /api/checkout/creem/webhook, a route the engine owns
// and hands to this module with the body untouched.
//
// REST over net/http and no SDK, per rule 2: creating a checkout is one JSON
// POST and verifying a webhook is one HMAC.
//
// # Merchant of record, like Paddle and Lemon Squeezy
//
// Creem sells to the shopper, not the store, and works out the tax on the sale
// itself. A store using it should leave GoCommerce's tax rates empty and let
// the total it sends be what the shopper pays; configuring both charges tax
// twice. Documented rather than enforced, for the reason ext/payments-paddle
// gives: the engine cannot tell which of the two a store intends, and silently
// zeroing somebody's configured tax is a worse surprise than a paragraph.
//
// # Why this needs a product id
//
// The same reason Lemon Squeezy needs a variant id and Paddle needs neither:
// Creem sells items from its own catalogue, so a checkout must name a product
// that already exists there. `custom_price` then overrides what that product
// costs for this one checkout.
//
// So a store using this module creates one placeholder product in Creem — any
// name, any price, never shown to a shopper — and names it here. Every order is
// that product at the order's own price. It is a configuration step stated up
// front because the alternative is discovering it from a 400 at the first real
// checkout.
//
// The product must be a ONE-TIME product. Creem refuses `custom_price` on a
// subscription product, and this module only knows how to sell an order once.
//
// # Refunds are all or nothing
//
// POST /v1/refunds takes a TRANSACTION id — not the order id — and refunds
// "the full remaining refundable amount", with no way to name a smaller one.
//
// Both halves of that shape the module. The transaction id is recorded from
// the checkout.completed event, because it is not knowable when the checkout
// is created; and a request for anything less than the whole order is refused
// rather than sent, because the failure mode otherwise is the worst one
// available — an operator asking for a five pound goodwill refund and Creem
// returning fifty.
//
// A refund made in Creem's own dashboard still reaches the store, as a
// refund.created event this module records.
package creem

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
	defaultBaseURL = "https://api.creem.io"
	// TestBaseURL is Creem's sandbox, for a store trying this out. It is a
	// separate host rather than a flag, so a test key cannot be pointed at
	// production by forgetting to unset something.
	TestBaseURL     = "https://test-api.creem.io"
	maxWebhookBytes = 1 << 20

	// What Creem will accept as a custom price, in minor units. Checked here so
	// an operator reads what is wrong with the order rather than a 400 from a
	// gateway naming a field they never filled in.
	minCustomPrice = 100
	maxCustomPrice = 99_999_999
)

// Config configures the module.
type Config struct {
	// APIKey is a Creem API key, from the dashboard's developers section.
	// Required.
	APIKey string `plugin:"api_key"`
	// WebhookSecret is the signing secret of the webhook. Required: without it
	// any caller could mark orders paid.
	WebhookSecret string `plugin:"webhook_secret"`
	// ProductID names the placeholder catalogue item every checkout is created
	// against. Required — see the package comment for why it exists at all. It
	// must be a one-time product; Creem refuses a custom price on a
	// subscription.
	ProductID string `plugin:"product_id"`
	// SuccessURL is where Creem sends the shopper after paying. Optional:
	// Creem shows its own confirmation when it is empty.
	SuccessURL string `plugin:"success_url"`
	// BaseURL overrides the endpoint — TestBaseURL for the sandbox, or a stub
	// in tests.
	BaseURL string `plugin:"base_url"`
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the Creem payment provider.
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
const PluginKey = "payments-creem"

var errNotConfigured = errors.New("creem: not set up — activate and configure it under Settings")

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
// on, with every required field filled. Called before each use, so a key typed
// into the panel a moment ago counts without a restart.
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
		strings.TrimSpace(c.ProductID) != ""
}

// finish fills a configuration's defaults.
func (m *Module) finish(c *Config) {
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "payments-creem" }

// Migrations implements gocommerce.Module.
//
// One table, to make webhook handling idempotent: Creem retries, and settling
// an order twice must be impossible rather than merely unlikely.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_events",
		SQL: `
			CREATE TABLE payments_creem_events (
			    id          text        PRIMARY KEY,
			    type        text        NOT NULL,
			    order_id    bigint,
			    -- Creem refunds against a transaction, and a transaction id is
			    -- only knowable once somebody has paid. Kept here rather than
			    -- as the order's payment reference so that the reference stays
			    -- the order id, which is what Creem's dashboard shows and what
			    -- a person reconciling the two is reading.
			    transaction_id text     NOT NULL DEFAULT '',
			    received_at timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX payments_creem_events_received_idx
			    ON payments_creem_events (received_at);`,
	}}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	// Config-configured stores start switched on; a store with nothing in
	// Config starts idle and waits for the panel.
	inCode := m.complete(&m.cfg)
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "Creem", Category: "payments", DefaultEnabled: inCode,
		Description: "Checkouts through Creem as merchant of record, each created against one placeholder one-time product. Creem refunds in full only, so a partial refund has to be made in its dashboard.",
		Docs:        "https://docs.creem.io/api-reference/introduction",
		Fields: []gocommerce.PluginField{
			{Key: "api_key", Label: "API key", Kind: "secret", Required: !inCode},
			{Key: "webhook_secret", Label: "Webhook signing secret", Kind: "secret", Required: !inCode},
			{Key: "product_id", Label: "Placeholder product ID", Kind: "text", Required: !inCode, Help: "A one-time product every checkout is created against; its price is overridden per order."},
			{Key: "success_url", Label: "Where to send the shopper afterwards", Kind: "url", Help: "Empty shows Creem's own confirmation."},
			{Key: "base_url", Label: "API base URL", Kind: "url", Help: "Empty for production; https://test-api.creem.io for the sandbox."},
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
func (m *Module) Code() string { return "creem" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "Creem" }

// checkAmount is what Creem will take as a custom price.
//
// Its own limits, checked before the call rather than after: a store selling a
// 50-cent item would otherwise get a 400 about `custom_price`, a field nobody
// filled in, on a gateway they had just set up correctly.
func checkAmount(minor int64) error {
	if minor < minCustomPrice {
		return fmt.Errorf(
			"creem: will not take a payment under %d in minor units, and this order is %d",
			minCustomPrice, minor)
	}
	if minor > maxCustomPrice {
		return fmt.Errorf(
			"creem: will not take a payment over %d in minor units, and this order is %d",
			maxCustomPrice, minor)
	}
	return nil
}

// Initiate creates a checkout and sends the shopper to its hosted page.
func (m *Module) Initiate(ctx context.Context, order *gocommerce.Order, opts gocommerce.PayOptions) (gocommerce.PaymentIntent, error) {
	if !m.refresh(ctx) {
		return gocommerce.PaymentIntent{}, errNotConfigured
	}
	if err := checkAmount(order.Total.AmountMinor); err != nil {
		return gocommerce.PaymentIntent{}, err
	}

	metadata := map[string]string{
		"order_id":     strconv.FormatInt(order.ID, 10),
		"order_number": order.Number,
	}
	for k, v := range opts.Data {
		// Namespaced, so client-supplied extras cannot overwrite the two fields
		// the webhook depends on to find the order.
		metadata["client_"+k] = v
	}

	body := map[string]any{
		"product_id": m.conf().ProductID,
		// Minor units, which is what the engine holds and what Creem takes —
		// no conversion, so nothing to round wrongly.
		"custom_price": order.Total.AmountMinor,
		// The same id in two places on purpose. `metadata` is what comes back
		// on the webhook and is how the handler finds the order; `request_id`
		// is what a person reading Creem's dashboard sees, so a row there can
		// be tied to this store without opening the payload.
		"request_id": strconv.FormatInt(order.ID, 10),
		"metadata":   metadata,
	}
	if order.Email != "" {
		body["customer"] = map[string]string{"email": order.Email}
	}
	if url := strings.TrimSpace(m.conf().SuccessURL); url != "" {
		body["success_url"] = url
	}

	var out struct {
		ID          string `json:"id"`
		Status      string `json:"status"`
		CheckoutURL string `json:"checkout_url"`
	}
	if err := m.do(ctx, http.MethodPost, "/v1/checkouts", body, &out); err != nil {
		return gocommerce.PaymentIntent{}, err
	}
	if out.CheckoutURL == "" {
		return gocommerce.PaymentIntent{}, fmt.Errorf(
			"creem: checkout %s came back without a URL", out.ID)
	}

	return gocommerce.PaymentIntent{
		Kind:     gocommerce.IntentRedirect,
		Provider: m.Code(),
		// The checkout id, not an order id: Creem only mints its own order id
		// once somebody pays, and the webhook is what supplies it.
		Reference:  out.ID,
		ClientData: map[string]string{"url": out.CheckoutURL},
	}, nil
}

// Refund implements gocommerce.Refunder.
func (m *Module) Refund(ctx context.Context, order *gocommerce.Order, amountMinor int64) error {
	_, err := m.RefundWithReference(ctx, order, amountMinor)
	return err
}

// RefundWithReference implements gocommerce.ReferencedRefunder.
//
// Creem refunds a TRANSACTION, in full. Two consequences, and both are
// refusals rather than attempts:
//
// A transaction id is not knowable when the checkout is created — Creem mints
// one when somebody pays and sends it on checkout.completed — so a refund
// before that event has arrived has nothing to refund against and says so.
//
// And a request for part of the order is refused. Creem resolves "the full
// remaining refundable amount" itself and takes no amount from the caller, so
// sending a partial request would return the whole order to the shopper while
// the store's books recorded a fraction. A refusal an operator can read beats
// a silent forty-five pound difference.
func (m *Module) RefundWithReference(ctx context.Context, order *gocommerce.Order, amountMinor int64) (string, error) {
	if !m.refresh(ctx) {
		return "", errNotConfigured
	}
	if amountMinor != order.Total.AmountMinor {
		return "", fmt.Errorf(
			"creem: refunds the whole order or nothing, and this asks for %d of %d — refund it in full here, or make the partial refund in Creem's dashboard",
			amountMinor, order.Total.AmountMinor)
	}

	var transaction string
	err := m.db.QueryRowContext(ctx, `
		SELECT transaction_id FROM payments_creem_events
		WHERE order_id = $1 AND transaction_id <> ''
		ORDER BY received_at DESC LIMIT 1`, order.ID).Scan(&transaction)
	if errors.Is(err, sql.ErrNoRows) || transaction == "" {
		return "", errors.New(
			"creem: no transaction recorded for this order yet — Creem sends one with the checkout.completed event, and a refund needs it")
	}
	if err != nil {
		return "", fmt.Errorf("creem: could not read the transaction for order %d: %w", order.ID, err)
	}

	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := m.do(ctx, http.MethodPost, "/v1/refunds",
		map[string]any{"transaction_id": transaction}, &out); err != nil {
		return "", err
	}
	// `pending` is a real answer here rather than a failure: Creem returns it
	// when the card network confirms asynchronously. The engine has already
	// recorded the refund, and refund.created arrives when it settles.
	return out.ID, nil
}

// Webhook implements gocommerce.WebhookProvider.
func (m *Module) Webhook() http.Handler { return http.HandlerFunc(m.handleWebhook) }

// event is the shape this module reads. Creem sends a great deal more —
// subscriptions, credits, disputes — and the fields below are the ones a
// one-time sale turns on.
type event struct {
	ID        string `json:"id"`
	EventType string `json:"eventType"`
	Object    struct {
		ID    string `json:"id"`
		Order struct {
			ID     string `json:"id"`
			Amount int64  `json:"amount"`
			Status string `json:"status"`
			// What a refund is issued against. Nullable in Creem's schema, so
			// a refund checks for it rather than assuming.
			Transaction string `json:"transaction"`
		} `json:"order"`
		RefundAmount int64             `json:"refund_amount"`
		Metadata     map[string]string `json:"metadata"`
	} `json:"object"`
}

func (m *Module) handleWebhook(w http.ResponseWriter, r *http.Request) {
	// A notification for a provider nobody has set up cannot be verified, and
	// an unverifiable notification is refused.
	if !m.refresh(r.Context()) {
		http.Error(w, "payment method not set up", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBytes))
	if err != nil {
		http.Error(w, "could not read body", http.StatusBadRequest)
		return
	}
	if err := verifySignature(body, r.Header.Get("creem-signature"), m.conf().WebhookSecret); err != nil {
		m.log.Warn("rejected a Creem webhook", "error", err)
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	var ev event
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, "malformed event", http.StatusBadRequest)
		return
	}

	orderID, _ := strconv.ParseInt(ev.Object.Metadata["order_id"], 10, 64)

	// Creem sends an event id of its own, so the claim is that id directly —
	// no need for the composite key Lemon Squeezy's handler builds. A retry of
	// one delivery repeats the id; two events about one order do not.
	claim := ev.ID
	if claim == "" {
		// An event with no id cannot be de-duplicated, and de-duplication is
		// the only thing standing between a captured delivery and settling an
		// order twice. Refused rather than guessed at.
		http.Error(w, "event has no id", http.StatusBadRequest)
		return
	}
	res, err := m.db.ExecContext(r.Context(), `
		INSERT INTO payments_creem_events (id, type, order_id, transaction_id)
		VALUES ($1, $2, nullif($3, 0), $4)
		ON CONFLICT (id) DO NOTHING`,
		claim, ev.EventType, orderID, ev.Object.Order.Transaction)
	if err != nil {
		m.log.Error("could not record the Creem event", "claim", claim, "error", err)
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
		// renewal, a payment link used elsewhere. Acknowledged rather than
		// retried forever.
		w.WriteHeader(http.StatusOK)
		return
	}

	switch ev.EventType {
	case "checkout.completed":
		// `paid` is the status of a settled order; anything else is a sale that
		// has not completed, and settling on it would mark orders paid that
		// Creem is still collecting or has refused.
		if !strings.EqualFold(ev.Object.Order.Status, "paid") {
			w.WriteHeader(http.StatusOK)
			return
		}
		// The engine performs the transition and writes the event. This module
		// never touches an order row itself. The reference stored is Creem's
		// order id, which is what a dashboard refund is issued against and what
		// somebody reconciling the two will be reading.
		if _, err := m.pay.MarkPaid(r.Context(), orderID, ev.Object.Order.ID); err != nil {
			m.log.Error("could not mark the order paid", "order_id", orderID, "error", err)
			m.releaseClaim(r.Context(), claim)
			http.Error(w, "could not apply payment", http.StatusInternalServerError)
			return
		}
	case "refund.created":
		// Recorded rather than applied: the refund happened in Creem's
		// dashboard, and the engine's own refund is an operator's decision
		// about this store's books. Logging it is what keeps them from being
		// the last to know.
		m.log.Info("Creem reported a refund",
			"order_id", orderID, "creem_order", ev.Object.Order.ID,
			"amount_minor", ev.Object.RefundAmount)
	case "dispute.created":
		m.log.Warn("Creem reported a dispute",
			"order_id", orderID, "creem_order", ev.Object.Order.ID)
	}

	w.WriteHeader(http.StatusOK)
}

// releaseClaim un-claims an event whose handling failed, so the retry finds
// work to do instead of being told it was already handled.
func (m *Module) releaseClaim(ctx context.Context, claim string) {
	if _, err := m.db.ExecContext(ctx,
		`DELETE FROM payments_creem_events WHERE id = $1`, claim); err != nil {
		m.log.Error("could not release the event claim", "claim", claim, "error", err)
	}
}

// verifySignature checks the creem-signature header.
//
// A plain HMAC-SHA256 hex digest of the raw body, with no timestamp — the same
// shape Lemon Squeezy uses and the same consequence: a signature Creem produced
// stays valid for that body for ever, so the idempotency claim above is not a
// nicety, it is the only thing standing between a captured delivery and
// settling an order twice.
func verifySignature(body []byte, header, secret string) error {
	header = strings.TrimSpace(header)
	if header == "" {
		return errors.New("missing creem-signature header")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	// Constant time, so a wrong signature cannot be found a byte at a time.
	if !hmac.Equal([]byte(header), []byte(want)) {
		return errors.New("creem-signature did not match")
	}
	return nil
}

// do performs one JSON request.
func (m *Module) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("creem: could not encode the request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, m.conf().BaseURL+path, reader)
	if err != nil {
		return err
	}
	// Creem's own header, not a bearer token. Sending Authorization instead is
	// a 401 that looks like a bad key.
	req.Header.Set("x-api-key", m.conf().APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("creem: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxWebhookBytes))
	if err != nil {
		return fmt.Errorf("creem: reading the response to %s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 300 {
		// The message is the sentence worth keeping, because "400" alone sends
		// an operator to the dashboard.
		var fail struct {
			Message any    `json:"message"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(payload, &fail) == nil {
			if text := messageOf(fail.Message); text != "" {
				return fmt.Errorf("creem: %s %s: %s", method, path, text)
			}
			if fail.Error != "" {
				return fmt.Errorf("creem: %s %s: %s", method, path, fail.Error)
			}
		}
		return fmt.Errorf("creem: %s %s: %s", method, path, strings.TrimSpace(string(payload)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("creem: could not read the response to %s %s: %w", method, path, err)
	}
	return nil
}

// messageOf reads Creem's `message`, which is a string for one problem and an
// array of them for a rejected body. Both become one sentence, because the
// caller is putting it in a log line or a toast.
func messageOf(v any) string {
	switch got := v.(type) {
	case string:
		return got
	case []any:
		parts := make([]string, 0, len(got))
		for _, item := range got {
			if s, ok := item.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "; ")
	}
	return ""
}
