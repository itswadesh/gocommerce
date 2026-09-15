// Package adyen takes payments through Adyen.
//
//	app, err := gocommerce.New(cfg,
//	    adyen.New(adyen.Config{
//	        APIKey:          os.Getenv("ADYEN_API_KEY"),
//	        MerchantAccount: os.Getenv("ADYEN_MERCHANT_ACCOUNT"),
//	        HMACKey:         os.Getenv("ADYEN_HMAC_KEY"),
//	        BaseURL:         "https://checkout-test.adyen.com/v71",
//	    }),
//	)
//
// Adyen then calls POST /api/checkout/adyen/webhook, a route the engine owns
// and hands to this module with the body untouched.
//
// REST over net/http and no SDK, per rule 2.
//
// # Why BaseURL has no default
//
// Every other module in this repository defaults its endpoint to the vendor's
// production API. Adyen cannot: a live endpoint is prefixed with the merchant's
// own identifier —
//
//	https://{prefix}-checkout-live.adyenpayments.com/checkout/v71
//
// — so there is no live URL this package could know. The two candidates were a
// default of the test endpoint, which turns a misconfigured production store
// into one quietly taking pretend money, and no default at all, which stops the
// store booting until somebody states which environment they mean. This module
// takes the second.
//
// # Payment links rather than Drop-in
//
// A checkout creates an Adyen payment link and sends the shopper to it. The
// alternative is Drop-in or Components, which is a better checkout and needs
// Adyen's JavaScript on the storefront, a /sessions call and a client key — a
// reasonable thing for a store to build, and not something a module can do on
// its behalf without dictating the storefront.
//
// # Notifications are the source of truth
//
// Adyen answers a refund with "received" and tells you what actually happened
// in a later notification. The same is true of the payment itself. So nothing
// here treats an API response as settlement: the engine marks an order paid
// when an AUTHORISATION notification with success=true arrives, verified by
// HMAC, and not before.
package adyen

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
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

	gocommerce "github.com/misiki/gocommerce/core"
)

const maxWebhookBytes = 1 << 20

// Config configures the module.
type Config struct {
	// APIKey is an Adyen API credential's key, sent as X-API-Key. Required.
	APIKey string `plugin:"api_key"`
	// MerchantAccount is the account to charge against. Required.
	MerchantAccount string `plugin:"merchant_account"`
	// HMACKey is the hex HMAC key generated for the webhook in Adyen's
	// Customer Area. Required: without it any caller could mark orders paid.
	HMACKey string `plugin:"hmac_key"`
	// hmacBytes is HMACKey decoded, filled by finish.
	hmacBytes []byte
	// BaseURL is the Checkout API endpoint, including the version segment —
	// "https://checkout-test.adyen.com/v71" for test, and the merchant's own
	// prefixed host for live. Required; see the package comment.
	BaseURL string `plugin:"base_url"`
	// ReturnURL is where the shopper lands after paying, when the checkout
	// request did not carry one of its own.
	ReturnURL string `plugin:"return_url"`
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the Adyen payment provider.
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
const PluginKey = "payments-adyen"

var errNotConfigured = errors.New("adyen: not set up — activate and configure it under Settings")

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
		strings.TrimSpace(c.MerchantAccount) != "" &&
		strings.TrimSpace(c.HMACKey) != "" &&
		strings.TrimSpace(c.BaseURL) != ""
}

// finish fills a configuration's defaults.
func (m *Module) finish(c *Config) {
	// The key is hex in the Customer Area and bytes in the HMAC; a key that
	// does not decode leaves the provider unconfigured rather than failing
	// every notification in production.
	c.hmacBytes, _ = hex.DecodeString(strings.TrimSpace(c.HMACKey))
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "payments-adyen" }

// Migrations implements gocommerce.Module.
//
// One table, to make notification handling idempotent: Adyen retries until it
// is told "[accepted]", and settling an order twice must be impossible rather
// than merely unlikely.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_events",
		SQL: `
			CREATE TABLE payments_adyen_events (
			    id          text        PRIMARY KEY,
			    type        text        NOT NULL,
			    order_id    bigint,
			    received_at timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX payments_adyen_events_received_idx
			    ON payments_adyen_events (received_at);`,
	}}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	// Config-configured stores start switched on; a store with nothing in
	// Config starts idle and waits for the panel.
	inCode := m.complete(&m.cfg)
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "Adyen", Category: "payments", DefaultEnabled: inCode,
		Description: "Card and local payments through Adyen's Checkout API, with the HMAC-signed notification marking orders paid.",
		Docs:        "https://docs.adyen.com/online-payments/",
		Fields: []gocommerce.PluginField{
			{Key: "api_key", Label: "API key", Kind: "secret", Required: !inCode, Help: "An API credential's key, sent as X-API-Key."},
			{Key: "merchant_account", Label: "Merchant account", Kind: "text", Required: !inCode},
			{Key: "hmac_key", Label: "Webhook HMAC key", Kind: "secret", Required: !inCode, Help: "Hex, as generated for the webhook in the Customer Area."},
			{Key: "base_url", Label: "Checkout API base URL", Kind: "url", Required: !inCode, Help: "Including the version segment: https://checkout-test.adyen.com/v71 for test, your own prefixed host for live."},
			{Key: "return_url", Label: "Return URL", Kind: "url", Help: "Where the shopper lands after paying, when the checkout did not say."},
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
	m.orders = app.Order()

	app.RegisterPayment(m)
	return nil
}

// Code implements gocommerce.PaymentProvider.
func (m *Module) Code() string { return "adyen" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "Adyen" }

// Initiate creates a payment link and sends the shopper to it.
func (m *Module) Initiate(ctx context.Context, order *gocommerce.Order, opts gocommerce.PayOptions) (gocommerce.PaymentIntent, error) {
	if !m.refresh(ctx) {
		return gocommerce.PaymentIntent{}, errNotConfigured
	}
	body := map[string]any{
		"merchantAccount": m.conf().MerchantAccount,
		"amount": map[string]any{
			// Minor units both sides, so there is nothing to round.
			"currency": strings.ToUpper(order.Total.Currency),
			"value":    order.Total.AmountMinor,
		},
		// The order number, and the one field that both identifies the order
		// and is covered by the notification's HMAC — see handleWebhook.
		"reference":   order.Number,
		"description": "Order " + order.Number,
	}
	if returnURL := firstNonEmpty(opts.ReturnURL, m.conf().ReturnURL); returnURL != "" {
		body["returnUrl"] = returnURL
	}
	if order.Email != "" {
		body["shopperEmail"] = order.Email
	}
	if country := strings.ToUpper(strings.TrimSpace(order.Address.Country)); len(country) == 2 {
		body["countryCode"] = country
	}

	metadata := map[string]string{"order_id": strconv.FormatInt(order.ID, 10)}
	for k, v := range opts.Data {
		// Namespaced, so client-supplied extras cannot overwrite the id.
		metadata["client_"+k] = v
	}
	body["metadata"] = metadata

	var out struct {
		ID     string `json:"id"`
		URL    string `json:"url"`
		Status string `json:"status"`
	}
	if err := m.do(ctx, http.MethodPost, "/paymentLinks", body, &out); err != nil {
		return gocommerce.PaymentIntent{}, err
	}
	if out.URL == "" {
		return gocommerce.PaymentIntent{}, fmt.Errorf(
			"adyen: payment link %s came back without a URL (status %s)", out.ID, out.Status)
	}

	return gocommerce.PaymentIntent{
		Kind:     gocommerce.IntentRedirect,
		Provider: m.Code(),
		// The link id, not a PSP reference: Adyen mints that when somebody
		// pays, and the notification is what supplies it.
		Reference:  out.ID,
		ClientData: map[string]string{"url": out.URL},
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
// The payment reference here is the PSP reference of the authorisation, which
// the notification wrote when the sale settled — not the payment link id
// Initiate returned. A refund before that notification has arrived has nothing
// to refund against, and says so rather than posting to a URL built from a
// link id.
func (m *Module) RefundWithReference(ctx context.Context, order *gocommerce.Order, amountMinor int64) (string, error) {
	if !m.refresh(ctx) {
		return "", errNotConfigured
	}
	if order.PaymentReference == "" {
		return "", errors.New("adyen: this order has no payment reference to refund")
	}

	body := map[string]any{
		"merchantAccount": m.conf().MerchantAccount,
		"amount": map[string]any{
			"currency": strings.ToUpper(order.Total.Currency),
			"value":    amountMinor,
		},
		"reference": order.Number + "-refund",
	}

	var out struct {
		PSPReference string `json:"pspReference"`
		Status       string `json:"status"`
	}
	if err := m.do(ctx, http.MethodPost,
		"/payments/"+url(order.PaymentReference)+"/refunds", body, &out); err != nil {
		return "", err
	}
	// Adyen answers "received" and nothing else; whether the refund cleared
	// arrives later as a REFUND notification.
	return out.PSPReference, nil
}

// Webhook implements gocommerce.WebhookProvider.
func (m *Module) Webhook() http.Handler { return http.HandlerFunc(m.handleWebhook) }

// notificationItem is one event inside an Adyen notification batch.
type notificationItem struct {
	AdditionalData map[string]string `json:"additionalData"`
	Amount         struct {
		Currency string `json:"currency"`
		Value    int64  `json:"value"`
	} `json:"amount"`
	EventCode           string `json:"eventCode"`
	MerchantAccountCode string `json:"merchantAccountCode"`
	MerchantReference   string `json:"merchantReference"`
	OriginalReference   string `json:"originalReference"`
	PSPReference        string `json:"pspReference"`
	Reason              string `json:"reason"`
	// Success is a *string* in Adyen's JSON, not a boolean, and it is the
	// string that is signed.
	Success string `json:"success"`
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

	var batch struct {
		Live              string `json:"live"`
		NotificationItems []struct {
			Item notificationItem `json:"NotificationRequestItem"`
		} `json:"notificationItems"`
	}
	if err := json.Unmarshal(body, &batch); err != nil {
		http.Error(w, "malformed notification", http.StatusBadRequest)
		return
	}
	if len(batch.NotificationItems) == 0 {
		http.Error(w, "no notification items", http.StatusBadRequest)
		return
	}

	// Every item is verified before any of them is acted on. A batch with one
	// forged item in it is a forged batch.
	for _, wrapper := range batch.NotificationItems {
		if err := verifyItem(wrapper.Item, m.conf().hmacBytes); err != nil {
			m.log.Warn("rejected an Adyen notification", "error", err,
				"psp_reference", wrapper.Item.PSPReference)
			http.Error(w, "invalid signature", http.StatusBadRequest)
			return
		}
	}

	for _, wrapper := range batch.NotificationItems {
		if err := m.apply(r.Context(), wrapper.Item); err != nil {
			m.log.Error("could not apply an Adyen notification",
				"psp_reference", wrapper.Item.PSPReference, "error", err)
			http.Error(w, "could not apply the notification", http.StatusInternalServerError)
			return
		}
	}

	// Adyen keeps retrying until it reads exactly this.
	w.Header().Set("Content-Type", "text/plain")
	_, _ = io.WriteString(w, "[accepted]")
}

func (m *Module) apply(ctx context.Context, item notificationItem) error {
	// The PSP reference is unique per event, and pairing it with the event code
	// keeps an AUTHORISATION and a later CAPTURE about the same payment
	// distinct while making a retry of either a duplicate.
	claim := item.EventCode + ":" + item.PSPReference

	// Which order this is about comes from merchantReference, not from the
	// metadata Adyen echoes back: merchantReference is one of the eight fields
	// the HMAC covers, and the metadata is not signed at all. Trusting an
	// unsigned field to pick the order would let a forger with a valid
	// signature for their own payment settle somebody else's order.
	var orderID int64
	if item.MerchantReference != "" {
		order, err := m.orders.GetByNumber(ctx, item.MerchantReference)
		if err == nil {
			orderID = order.ID
		}
	}

	res, err := m.db.ExecContext(ctx, `
		INSERT INTO payments_adyen_events (id, type, order_id)
		VALUES ($1, $2, nullif($3, 0))
		ON CONFLICT (id) DO NOTHING`, claim, item.EventCode, orderID)
	if err != nil {
		return fmt.Errorf("record the event: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil // already handled
	}

	if orderID == 0 {
		// A notification for something this store did not start — another
		// integration on the same merchant account, or a test from the
		// Customer Area. Acknowledged rather than retried forever.
		return nil
	}

	succeeded := strings.EqualFold(item.Success, "true")
	switch item.EventCode {
	case "AUTHORISATION":
		if !succeeded {
			if _, err := m.pay.MarkFailed(ctx, orderID,
				firstNonEmpty(item.Reason, "authorisation refused")); err != nil {
				m.releaseClaim(ctx, claim)
				return err
			}
			return nil
		}
		// The engine performs the transition and writes the event. This module
		// never touches an order row itself.
		if _, err := m.pay.MarkPaid(ctx, orderID, item.PSPReference); err != nil {
			m.releaseClaim(ctx, claim)
			return err
		}
	case "REFUND", "CANCEL_OR_REFUND":
		if !succeeded {
			// A refund the engine already recorded has now been declined by
			// the acquirer. Nothing can be undone automatically — the store's
			// books and Adyen's disagree, and an operator has to reconcile.
			m.log.Error("an Adyen refund failed after the engine recorded it",
				"order_id", orderID, "psp_reference", item.PSPReference, "reason", item.Reason)
			return nil
		}
		m.log.Info("Adyen confirmed a refund", "order_id", orderID, "psp_reference", item.PSPReference)
	case "CHARGEBACK", "NOTIFICATION_OF_CHARGEBACK", "SECOND_CHARGEBACK":
		// Not a state this engine models. Logged loudly because money has left
		// and somebody has to know.
		m.log.Warn("Adyen reported a chargeback", "order_id", orderID,
			"psp_reference", item.PSPReference, "reason", item.Reason)
	}
	return nil
}

// releaseClaim un-claims an event whose handling failed, so the retry finds
// work to do instead of being told it was already handled.
func (m *Module) releaseClaim(ctx context.Context, claim string) {
	if _, err := m.db.ExecContext(ctx,
		`DELETE FROM payments_adyen_events WHERE id = $1`, claim); err != nil {
		m.log.Error("could not release the event claim", "claim", claim, "error", err)
	}
}

// verifyItem checks a notification item's HMAC.
//
// Adyen signs eight fields joined with colons rather than the request body, and
// puts the result in additionalData.hmacSignature rather than a header. That
// means the signature covers the identity and the money — pspReference,
// merchantReference, amount, eventCode, success — and nothing else in the
// payload. Anything outside those eight fields is unauthenticated and must not
// be trusted to decide what happens to an order.
func verifyItem(item notificationItem, key []byte) error {
	signature := item.AdditionalData["hmacSignature"]
	if signature == "" {
		return errors.New("notification carries no hmacSignature")
	}

	payload := strings.Join([]string{
		item.PSPReference,
		item.OriginalReference,
		item.MerchantAccountCode,
		item.MerchantReference,
		strconv.FormatInt(item.Amount.Value, 10),
		item.Amount.Currency,
		item.EventCode,
		item.Success,
	}, ":")

	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	// Constant time, so a wrong signature cannot be found a byte at a time.
	if !hmac.Equal([]byte(signature), []byte(want)) {
		return errors.New("hmacSignature did not match")
	}
	return nil
}

// do performs one JSON request.
func (m *Module) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("adyen: could not encode the request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, m.conf().BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", m.conf().APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("adyen: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxWebhookBytes))
	if err != nil {
		return fmt.Errorf("adyen: reading the response to %s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 300 {
		var fail struct {
			Message   string `json:"message"`
			ErrorCode string `json:"errorCode"`
		}
		if json.Unmarshal(payload, &fail) == nil && fail.Message != "" {
			return fmt.Errorf("adyen: %s %s: %s (%s)", method, path, fail.Message, fail.ErrorCode)
		}
		return fmt.Errorf("adyen: %s %s: %s", method, path, strings.TrimSpace(string(payload)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("adyen: could not read the response to %s %s: %w", method, path, err)
	}
	return nil
}

// url escapes a PSP reference for use as a path segment. They are alphanumeric
// in practice, which is exactly why escaping them costs nothing.
func url(segment string) string {
	var b strings.Builder
	for _, r := range segment {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == '~':
			b.WriteRune(r)
		default:
			b.WriteString(fmt.Sprintf("%%%02X", r))
		}
	}
	return b.String()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
