// Package clover takes payments through Clover's Hosted Checkout.
//
//	app, err := gocommerce.New(cfg,
//	    clover.New(clover.Config{
//	        MerchantID:    os.Getenv("CLOVER_MERCHANT_ID"),
//	        PrivateToken:  os.Getenv("CLOVER_PRIVATE_TOKEN"),
//	        WebhookSecret: os.Getenv("CLOVER_WEBHOOK_SECRET"),
//	    }),
//	)
//
// Clover then calls POST /api/checkout/clover/webhook, a route the engine owns
// and hands to this module with the body untouched. Its URL and signing
// secret are set in the Clover dashboard, under Settings, Ecommerce, Hosted
// Checkout.
//
// REST over net/http and no SDK, per rule 2.
//
// # A redirect to Clover's page
//
// Initiate creates a Hosted Checkout session and returns its URL. The shopper
// types their card on Clover's page, so card numbers never reach the store,
// and comes back to the return URL. Coming back proves nothing: the signed
// webhook is what settles the order. Hosted Checkout is offered in the United
// States and Canada only.
//
// # One line for the whole order
//
// The order goes to Clover as one line, for its total. The engine has already
// worked out the tax, shipping and discounts, and Clover would otherwise apply
// its own. Clover also refunds part of a payment only when the payment was one
// line with no tax or tip, so this is what makes a partial refund possible.
// Tips are switched off, so the payment is always the session's amount.
//
// # No currency, no reference
//
// A Hosted Checkout session has no currency: Clover charges in the merchant
// account's own. So the module is told that currency and refuses an order in
// any other, rather than charge 2500 Canadian cents for 2500 US ones. Nor
// does a session carry a reference of the store's: the webhook names only the
// session and the payment. So this module keeps its own table from session to
// order, with the amount each session asked for.
package clover

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
	"sync"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	defaultBaseURL = "https://api.clover.com"
	sandboxBaseURL = "https://apisandbox.dev.clover.com"
	// Clover signs a timestamp. Its documentation names no tolerance; five
	// minutes allows for clock skew and keeps a captured delivery from being a
	// standing key to the store.
	signatureTolerance = 5 * time.Minute
	maxBodyBytes       = 1 << 20
	userAgent          = "gocommerce-payments-clover"
)

// Config configures the module.
type Config struct {
	// MerchantID is the Clover merchant the payments go to, sent as the
	// X-Clover-Merchant-Id header. Required.
	MerchantID string `plugin:"merchant_id"`
	// PrivateToken is the merchant's Ecommerce API private token, from the
	// Clover dashboard under Settings, Ecommerce, Ecommerce API Tokens.
	// Required.
	PrivateToken string `plugin:"private_token"`
	// WebhookSecret is the Hosted Checkout signing secret. Required: without
	// it any caller could mark orders paid.
	WebhookSecret string `plugin:"webhook_secret"`
	// Currency is the merchant account's currency, USD or CAD. Defaults to
	// USD. Orders in any other currency are refused.
	Currency string `plugin:"currency"`
	// ReturnURL is where the shopper lands after paying, when the checkout
	// does not send its own. Clover only accepts https.
	ReturnURL string `plugin:"return_url"`
	// Sandbox points the module at apisandbox.dev.clover.com. Clover issues a
	// test merchant with its own token and secret for it.
	Sandbox bool `plugin:"sandbox"`
	// BaseURL overrides the endpoint, for tests.
	BaseURL string `plugin:"base_url"`
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the Clover payment provider.
type Module struct {
	cfg    Config
	live   sync.Map // store id → *Config: each store has its own account
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
const PluginKey = "payments-clover"

var errNotConfigured = errors.New("clover: not set up — activate and configure it under Settings")

// conf is the effective configuration: the last one refresh computed, or
// Config alone before the first use.
func (m *Module) conf(ctx context.Context) *Config {
	store, _ := gocommerce.StoreFrom(ctx)
	if c, ok := m.live.Load(store); ok {
		return c.(*Config)
	}
	return &m.cfg
}

// refresh recomputes the effective configuration (Config with the plugin's
// settings laid over it) and reports whether the provider can work: switched
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
	return strings.TrimSpace(c.MerchantID) != "" &&
		strings.TrimSpace(c.PrivateToken) != "" &&
		strings.TrimSpace(c.WebhookSecret) != ""
}

// finish fills a configuration's defaults.
func (m *Module) finish(c *Config) {
	c.Currency = strings.ToUpper(strings.TrimSpace(c.Currency))
	if c.Currency == "" {
		c.Currency = gocommerce.DefaultCurrency
	}
	switch {
	case strings.TrimSpace(c.BaseURL) != "":
	case c.Sandbox:
		c.BaseURL = sandboxBaseURL
	default:
		c.BaseURL = defaultBaseURL
	}
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "payments-clover" }

// Migrations implements gocommerce.Module.
//
// Two tables. Sessions is how the webhook finds its order, since Clover sends
// nothing else that names one. Events makes webhook handling idempotent:
// Clover may deliver twice, and settling an order twice must be impossible
// rather than merely unlikely. Neither key is called id, so scoping puts the
// store in front of both (D83): two stores on one Clover account each claim
// an event for themselves.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_sessions_and_events",
		SQL: `
			CREATE TABLE payments_clover_sessions (
			    session_id   text        PRIMARY KEY,
			    order_id     bigint      NOT NULL,
			    amount_minor bigint      NOT NULL,
			    currency     text        NOT NULL,
			    created_at   timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX payments_clover_sessions_order_idx ON payments_clover_sessions (order_id);
			CREATE TABLE payments_clover_events (
			    event_id    text        PRIMARY KEY,
			    order_id    bigint,
			    received_at timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX payments_clover_events_received_idx ON payments_clover_events (received_at);`,
	},
		gocommerce.ScopeMigration("0002_store_scope", "payments_clover_sessions", "payments_clover_events"),
		// A session id opens its order's hosted checkout page.
		gocommerce.WithholdFromReports("0003_withhold_from_reports", "payments_clover_sessions.session_id"),
	}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	// Config-configured stores start switched on; a store with nothing in
	// Config starts idle and waits for the panel.
	inCode := m.complete(&m.cfg)
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "Clover", Category: "payments", DefaultEnabled: inCode,
		Description: "Card payments on Clover's Hosted Checkout page, with the signed webhook marking orders paid. United States and Canada.",
		Docs:        "https://docs.clover.com/dev/docs/hosted-checkout-api",
		Fields: []gocommerce.PluginField{
			{Key: "merchant_id", Label: "Merchant ID", Kind: "text", Required: !inCode, Help: "From the Clover dashboard's address bar or Account & Setup."},
			{Key: "private_token", Label: "Ecommerce private token", Kind: "secret", Required: !inCode, Help: "Settings, Ecommerce, Ecommerce API Tokens."},
			{Key: "webhook_secret", Label: "Webhook signing secret", Kind: "secret", Required: !inCode, Help: "Settings, Ecommerce, Hosted Checkout: set the webhook URL to this store's /api/checkout/clover/webhook, then generate the secret."},
			{Key: "currency", Label: "Account currency", Kind: "select", Options: []string{"USD", "CAD"}, Default: "USD", Help: "Clover charges in the account's own currency; orders in any other are refused."},
			{Key: "return_url", Label: "Return URL", Kind: "url", Help: "Where the shopper lands after paying, if the storefront sends none. https only."},
			{Key: "sandbox", Label: "Sandbox", Kind: "bool", Help: "Points at apisandbox.dev.clover.com, with a test merchant's token and secret."},
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
	m.orders = app.Order()

	app.RegisterPayment(m)
	return nil
}

// Code implements gocommerce.PaymentProvider.
func (m *Module) Code() string { return "clover" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "Clover" }

// Initiate opens a Hosted Checkout session for the order's total.
//
// PayOptions.Data is not sent: a session has nowhere to carry it.
func (m *Module) Initiate(ctx context.Context, order *gocommerce.Order, opts gocommerce.PayOptions) (gocommerce.PaymentIntent, error) {
	if !m.refresh(ctx) {
		return gocommerce.PaymentIntent{}, errNotConfigured
	}
	c := m.conf(ctx)
	if !strings.EqualFold(order.Total.Currency, c.Currency) {
		return gocommerce.PaymentIntent{}, fmt.Errorf(
			"clover: this Clover account takes %s and the order is in %s", c.Currency, order.Total.Currency)
	}
	if order.Total.AmountMinor <= 0 {
		return gocommerce.PaymentIntent{}, errors.New("clover: there is nothing to pay")
	}
	returnURL := strings.TrimSpace(opts.ReturnURL)
	if returnURL == "" {
		returnURL = strings.TrimSpace(c.ReturnURL)
	}
	if !strings.HasPrefix(returnURL, "https://") {
		return gocommerce.PaymentIntent{}, fmt.Errorf(
			"clover: Clover sends the shopper back only to an https address, and the return URL is %q — set it in the plugin or send return_url with the checkout", returnURL)
	}

	customer := map[string]string{}
	if order.Email != "" {
		customer["email"] = order.Email
	}
	body := map[string]any{
		"customer": customer,
		"shoppingCart": map[string]any{
			"lineItems": []map[string]any{{
				"name": "Order " + order.Number,
				// Cents, which is what the engine holds and Clover takes: no
				// conversion, so nothing to round wrongly.
				"price":   order.Total.AmountMinor,
				"unitQty": 1,
			}},
		},
		"tips": map[string]any{"enabled": false},
		// One address for all three. The storefront reads the order to say
		// how it went, so it does not need Clover to tell it which.
		"redirectUrls": map[string]string{"success": returnURL, "failure": returnURL, "cancel": returnURL},
	}

	var out struct {
		Href              string `json:"href"`
		CheckoutSessionID string `json:"checkoutSessionId"`
	}
	if err := m.do(ctx, http.MethodPost, "/invoicingcheckoutservice/v1/checkouts", body, &out); err != nil {
		return gocommerce.PaymentIntent{}, err
	}
	if out.Href == "" || out.CheckoutSessionID == "" {
		return gocommerce.PaymentIntent{}, errors.New("clover: the checkout session came back without its id or URL")
	}
	// Without this row the webhook could not find the order, so a session
	// that cannot be recorded is not handed to the shopper.
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO payments_clover_sessions (session_id, order_id, amount_minor, currency)
		VALUES ($1, $2, $3, $4)`,
		out.CheckoutSessionID, order.ID, order.Total.AmountMinor, strings.ToUpper(order.Total.Currency)); err != nil {
		return gocommerce.PaymentIntent{}, fmt.Errorf("clover: could not record the checkout session: %w", err)
	}

	return gocommerce.PaymentIntent{
		Kind:     gocommerce.IntentRedirect,
		Provider: m.Code(),
		// The session, until the webhook replaces it with the payment.
		Reference:  out.CheckoutSessionID,
		ClientData: map[string]string{"url": out.Href},
	}, nil
}

// Refund implements gocommerce.Refunder.
func (m *Module) Refund(ctx context.Context, order *gocommerce.Order, amountMinor int64) error {
	_, err := m.RefundWithReference(ctx, order, amountMinor)
	return err
}

// RefundWithReference implements gocommerce.ReferencedRefunder.
func (m *Module) RefundWithReference(ctx context.Context, order *gocommerce.Order, amountMinor int64) (string, error) {
	if !m.refresh(ctx) {
		return "", errNotConfigured
	}
	// The payment reference is Clover's payment id, which the webhook wrote
	// when the order settled. Before that it is the checkout session, which
	// Clover cannot refund.
	payment := strings.TrimSpace(order.PaymentReference)
	var session bool
	if payment != "" {
		if err := m.db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM payments_clover_sessions WHERE session_id = $1)`, payment).Scan(&session); err != nil {
			return "", fmt.Errorf("clover: could not check the payment reference: %w", err)
		}
	}
	if payment == "" || session {
		return "", fmt.Errorf("clover: this order has no Clover payment to refund (payment reference %q)", order.PaymentReference)
	}

	var out struct {
		ID            string `json:"id"`
		Status        string `json:"status"`
		FailureReason string `json:"failure_reason"`
	}
	if err := m.do(ctx, http.MethodPost, "/v1/refunds", map[string]any{"charge": payment, "amount": amountMinor}, &out); err != nil {
		return "", err
	}
	switch strings.ToLower(out.Status) {
	case "failed", "canceled":
		return "", fmt.Errorf("clover: refund %s was %s: %s", out.ID, out.Status, firstNonEmpty(out.FailureReason, "no reason given"))
	}
	return out.ID, nil
}

// Webhook implements gocommerce.WebhookProvider.
func (m *Module) Webhook() http.Handler { return http.HandlerFunc(m.handleWebhook) }

// event is a Hosted Checkout notification. Clover's documentation lists the
// fields without their exact spelling; encoding/json matches names in any
// case, so "Type" and "type" both land here.
type event struct {
	Type       string `json:"type"`
	Status     string `json:"status"`
	Message    string `json:"message"`
	ID         string `json:"id"`         // the payment
	MerchantID string `json:"merchantId"` // the merchant it was paid to
	Data       string `json:"data"`       // the checkout session
}

func (m *Module) handleWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// A notification for a provider nobody has set up cannot be verified, and
	// an unverifiable notification is refused.
	if !m.refresh(ctx) {
		http.Error(w, "payment method not set up", http.StatusServiceUnavailable)
		return
	}
	c := m.conf(ctx)
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "could not read body", http.StatusBadRequest)
		return
	}
	if err := verifySignature(body, r.Header.Get("Clover-Signature"), []byte(c.WebhookSecret), time.Now()); err != nil {
		m.log.Warn("rejected a Clover webhook", "error", err)
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	var ev event
	if err := json.Unmarshal(body, &ev); err != nil || ev.ID == "" {
		http.Error(w, "malformed event", http.StatusBadRequest)
		return
	}
	// Acknowledged and left alone: something other than a payment, or a
	// payment to a merchant this store is not set up for.
	if !strings.EqualFold(ev.Type, "PAYMENT") ||
		(ev.MerchantID != "" && !strings.EqualFold(ev.MerchantID, c.MerchantID)) {
		w.WriteHeader(http.StatusOK)
		return
	}

	// A decline and then an approval can arrive for one session, so the claim
	// is on the payment and its outcome.
	claim := "PAYMENT:" + ev.ID + ":" + strings.ToUpper(ev.Status)
	res, err := m.db.ExecContext(ctx, `
		INSERT INTO payments_clover_events (event_id) VALUES ($1)
		ON CONFLICT (store_id, event_id) DO NOTHING`, claim)
	if err != nil {
		m.log.Error("could not record the Clover event", "claim", claim, "error", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Already handled. 200 stops the retries.
		w.WriteHeader(http.StatusOK)
		return
	}

	var orderID, amount int64
	var currency string
	err = m.db.QueryRowContext(ctx, `
		SELECT order_id, amount_minor, currency FROM payments_clover_sessions WHERE session_id = $1`,
		ev.Data).Scan(&orderID, &amount, &currency)
	if errors.Is(err, sql.ErrNoRows) {
		// A payment through a session this store did not make: real money,
		// but not this store's order. Acknowledged rather than retried for ever.
		m.log.Info("a Clover payment named no checkout session this store made", "payment", ev.ID, "session", ev.Data)
		w.WriteHeader(http.StatusOK)
		return
	}
	if err != nil {
		m.log.Error("could not find the Clover checkout session", "session", ev.Data, "error", err)
		m.releaseClaim(ctx, claim)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if _, err := m.db.ExecContext(ctx,
		`UPDATE payments_clover_events SET order_id = $2 WHERE event_id = $1`, claim, orderID); err != nil {
		m.log.Warn("could not record which order a Clover event was for", "claim", claim, "order_id", orderID, "error", err)
	}

	status := strings.ToUpper(ev.Status)
	if status == "DECLINED" {
		if _, err := m.pay.MarkFailed(ctx, orderID, "clover declined the card: "+firstNonEmpty(ev.Message, "no reason given")); err != nil {
			m.log.Error("could not mark the payment failed", "order_id", orderID, "error", err)
			m.releaseClaim(ctx, claim)
			http.Error(w, "could not apply the failure", http.StatusInternalServerError)
			return
		}
	}
	if status != "APPROVED" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// The webhook carries no amount, so the session's is the check. An order
	// whose total has changed since its session was made is not settled by
	// it: that would put the store's books and Clover's out of step with
	// nobody the wiser, so it is left for an operator who can see both.
	order, err := m.orders.Get(ctx, orderID)
	if err != nil {
		m.log.Error("could not read the order a Clover payment is for", "order_id", orderID, "error", err)
		m.releaseClaim(ctx, claim)
		http.Error(w, "could not read the order", http.StatusInternalServerError)
		return
	}
	if amount != order.Total.AmountMinor || !strings.EqualFold(currency, order.Total.Currency) {
		m.log.Error("a Clover payment does not match the order it is for",
			"order_id", orderID, "order_total_minor", order.Total.AmountMinor, "order_currency", order.Total.Currency,
			"session_minor", amount, "session_currency", currency, "payment", ev.ID)
		w.WriteHeader(http.StatusOK)
		return
	}

	// The engine performs the transition and writes the event. This module
	// never touches an order row itself.
	if _, err := m.pay.MarkPaid(ctx, orderID, ev.ID); err != nil {
		m.log.Error("could not mark the order paid", "order_id", orderID, "error", err)
		m.releaseClaim(ctx, claim)
		http.Error(w, "could not apply payment", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// releaseClaim un-claims an event whose handling failed, so the retry finds
// work to do instead of being told it was already handled.
func (m *Module) releaseClaim(ctx context.Context, claim string) {
	if _, err := m.db.ExecContext(ctx,
		`DELETE FROM payments_clover_events WHERE event_id = $1`, claim); err != nil {
		m.log.Error("could not release the event claim", "claim", claim, "error", err)
	}
}

// verifySignature checks the Clover-Signature header, `t=<unix>,v1=<hex>`:
// an HMAC-SHA256, keyed on the signing secret, of the timestamp, a full stop
// and the raw body. Any v1 that matches is a match.
func verifySignature(body []byte, header string, secret []byte, now time.Time) error {
	if strings.TrimSpace(header) == "" {
		return errors.New("missing Clover-Signature header")
	}
	var stamp string
	var signatures []string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			stamp = v
		case "v1":
			signatures = append(signatures, strings.ToLower(v))
		}
	}
	if stamp == "" {
		return errors.New("Clover-Signature has no timestamp")
	}
	if len(signatures) == 0 {
		return errors.New("Clover-Signature has no v1 signature")
	}
	seconds, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return fmt.Errorf("Clover-Signature timestamp %q is not a unix time", stamp)
	}
	// The timestamp is inside the signed string, so an attacker replaying a
	// captured delivery cannot move it, which is what makes this bound real.
	if drift := now.Sub(time.Unix(seconds, 0)); drift > signatureTolerance || drift < -signatureTolerance {
		return fmt.Errorf("Clover-Signature timestamp is %s away from now", drift.Round(time.Second))
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(stamp + "."))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	for _, s := range signatures {
		// Constant time, so a wrong signature cannot be found a byte at a time.
		if hmac.Equal([]byte(s), []byte(want)) {
			return nil
		}
	}
	return errors.New("Clover-Signature did not match")
}

// do performs one JSON request.
func (m *Module) do(ctx context.Context, method, path string, body any, out any) error {
	c := m.conf(ctx)
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("clover: could not encode the request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.PrivateToken)
	req.Header.Set("X-Clover-Merchant-Id", c.MerchantID)
	req.Header.Set("Accept", "application/json")
	// Clover asks every call to name the application making it.
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("clover: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return fmt.Errorf("clover: reading the response to %s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 300 {
		var fail struct {
			Message string `json:"message"`
			Error   any    `json:"error"`
		}
		if json.Unmarshal(payload, &fail) == nil {
			if fail.Message != "" {
				return fmt.Errorf("clover: %s %s: %d %s", method, path, resp.StatusCode, fail.Message)
			}
			if fail.Error != nil {
				return fmt.Errorf("clover: %s %s: %d %v", method, path, resp.StatusCode, fail.Error)
			}
		}
		return fmt.Errorf("clover: %s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("clover: could not read the response to %s %s: %w", method, path, err)
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
