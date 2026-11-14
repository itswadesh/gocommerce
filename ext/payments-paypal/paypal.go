// Package paypal takes payments through PayPal's Orders API.
//
// Like the other gateways it speaks REST over net/http: an order, a capture
// and a signature check are three JSON POSTs, so the SDK would add a dependency
// without adding capability.
//
// A PayPal order takes two steps where most gateways take one. The shopper
// approves it on PayPal's page, which moves no money, and then the store must
// capture it. Either of two things does that: the storefront's return page
// through POST /x/payments-paypal/orders/{paypal_order_id}/capture, or the
// CHECKOUT.ORDER.APPROVED webhook for the shopper who closed the tab.
//
// PayPal does not handle Indian rupees, so a store selling in a currency PayPal
// lacks never offers this method.
//
//	app, err := gocommerce.New(cfg,
//	    paypal.New(paypal.Config{
//	        ClientID:     os.Getenv("PAYPAL_CLIENT_ID"),
//	        ClientSecret: os.Getenv("PAYPAL_CLIENT_SECRET"),
//	        WebhookID:    os.Getenv("PAYPAL_WEBHOOK_ID"),
//	    }),
//	)
package paypal

import (
	"bytes"
	"context"
	"database/sql"
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

	gocommerce "github.com/misiki-in/gocommerce/core"
)

const (
	liveBaseURL    = "https://api-m.paypal.com"
	sandboxBaseURL = "https://api-m.sandbox.paypal.com"

	maxWebhookBytes = 1 << 20
	// How often the public capture route may reach PayPal for one order.
	captureQuiet = 10 * time.Second
)

// Config configures the module.
type Config struct {
	// ClientID and ClientSecret are the REST app's credentials. Required.
	ClientID     string `plugin:"client_id"`
	ClientSecret string `plugin:"client_secret"`
	// WebhookID is the id PayPal gave the webhook. Required: PayPal signs with
	// a certificate rather than a shared secret, and this id is what its check
	// binds a signature to. Without it any caller could mark orders paid.
	WebhookID string `plugin:"webhook_id"`
	// Sandbox talks to PayPal's test environment.
	Sandbox bool `plugin:"sandbox"`
	// BaseURL overrides the endpoint, for tests.
	BaseURL string `plugin:"base_url"`
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the PayPal payment provider.
type Module struct {
	cfg    Config
	live   sync.Map // store id → *Config: each store has its own account
	tokens sync.Map // base URL + client id → token
	asked  sync.Map // store id + PayPal order id → time of the last capture attempt
	app    *gocommerce.App
	client *http.Client
	log    *slog.Logger
	db     *sql.DB
	pay    *gocommerce.Payments
	orders *gocommerce.Orders
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// PluginKey is the plugin this module registers, where the panel keeps its
// credentials. Config is the environment's fallback for each field; a value
// typed into the panel wins.
const PluginKey = "payments-paypal"

var errNotConfigured = errors.New("paypal: not set up — activate and configure it under Settings")

// currencies are those PayPal's REST API accepts, with its decimal places.
// https://developer.paypal.com/reference/currency-codes/
var currencies = map[string]int{
	"AUD": 2, "BRL": 2, "CAD": 2, "CNY": 2, "CZK": 2, "DKK": 2, "EUR": 2, "HKD": 2,
	"HUF": 0, "ILS": 2, "JPY": 0, "MYR": 2, "MXN": 2, "TWD": 0, "NZD": 2, "NOK": 2,
	"PHP": 2, "PLN": 2, "GBP": 2, "SGD": 2, "SEK": 2, "CHF": 2, "THB": 2, "USD": 2,
}

func supported(currency string) bool {
	_, ok := currencies[strings.ToUpper(currency)]
	return ok
}

func (m *Module) conf(ctx context.Context) *Config {
	store, _ := gocommerce.StoreFrom(ctx)
	if c, ok := m.live.Load(store); ok {
		return c.(*Config)
	}
	return &m.cfg
}

// refresh recomputes the effective configuration and reports whether the
// provider can work. Called before each use, so a key typed into the panel a
// moment ago counts without a restart.
func (m *Module) refresh(ctx context.Context) bool {
	c := m.cfg
	on := false
	if m.app != nil {
		on, _ = m.app.Plugins().Enabled(ctx, PluginKey)
		if on {
			_ = m.app.Plugins().Fill(ctx, PluginKey, &c)
		}
	}
	finish(&c)
	store, _ := gocommerce.StoreFrom(ctx)
	m.live.Store(store, &c)
	return on && complete(&c)
}

// Configured implements gocommerce.Configurable. The store's currency counts,
// so a rupee store never shows a shopper a method that could only refuse them.
func (m *Module) Configured(ctx context.Context) bool {
	return m.refresh(ctx) && supported(m.app.Currency(ctx))
}

func complete(c *Config) bool {
	return strings.TrimSpace(c.ClientID) != "" &&
		strings.TrimSpace(c.ClientSecret) != "" &&
		strings.TrimSpace(c.WebhookID) != ""
}

func finish(c *Config) {
	if c.BaseURL != "" {
		return
	}
	c.BaseURL = liveBaseURL
	if c.Sandbox {
		c.BaseURL = sandboxBaseURL
	}
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return PluginKey }

// Migrations implements gocommerce.Module: one table, for webhook idempotency.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{
		{
			ID: "0001_events",
			SQL: `
				CREATE TABLE payments_paypal_events (
				    id          text        NOT NULL,
				    event       text        NOT NULL,
				    order_id    bigint,
				    received_at timestamptz NOT NULL DEFAULT now()
				);`,
		},
		gocommerce.ScopeMigration("0002_store_scope", "payments_paypal_events"),
		{
			// Two stores on one PayPal account each receive every event, so the
			// claim on an event id is each store's own (D83).
			ID:  "0003_events_key",
			SQL: `ALTER TABLE payments_paypal_events ADD PRIMARY KEY (store_id, id);`,
		},
	}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	inCode := complete(&m.cfg)
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "PayPal", Icon: "ri-paypal-line", Category: "payments", DefaultEnabled: inCode,
		Home:        "/dash/settings/payments",
		Description: "PayPal balance and cards, for stores selling in a currency PayPal handles. PayPal does not handle Indian rupees, so a rupee store does not offer it.",
		Docs:        "https://developer.paypal.com/docs/api/orders/v2/",
		Fields: []gocommerce.PluginField{
			{Key: "client_id", Label: "Client ID", Kind: "text", Required: !inCode},
			{Key: "client_secret", Label: "Client secret", Kind: "secret", Required: !inCode},
			{Key: "webhook_id", Label: "Webhook ID", Kind: "text", Required: !inCode,
				Help: "Add a webhook in your PayPal app for CHECKOUT.ORDER.APPROVED, PAYMENT.CAPTURE.COMPLETED and PAYMENT.CAPTURE.DENIED, then copy its ID here."},
			{Key: "sandbox", Label: "Use PayPal's sandbox", Kind: "bool", Help: "Test payments with sandbox credentials. No real money moves."},
			{Key: "base_url", Label: "API base URL", Kind: "url", Outbound: true, Help: "Empty for PayPal itself."},
		},
	})
	finish(&m.cfg)
	m.client = m.cfg.Client
	if m.client == nil {
		m.client = app.OutboundClient(20 * time.Second)
	}
	m.log, m.db, m.pay, m.orders = app.Log(), app.DB(), app.Pay(), app.Order()
	app.RegisterPayment(m)
	app.HandleFunc("POST /x/payments-paypal/orders/{paypal_order_id}/capture", m.handleCapture)
	return nil
}

// Code implements gocommerce.PaymentProvider.
func (m *Module) Code() string { return "paypal" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "PayPal" }

// value renders minor units as PayPal's decimal string, with integers only. A
// currency PayPal treats as whole units refuses a fractional amount, because
// rounding it would charge the shopper something other than the order's total.
func value(minor int64, currency string) (string, error) {
	code := strings.ToUpper(currency)
	places, ok := currencies[code]
	if !ok {
		return "", fmt.Errorf("paypal does not handle %s", code)
	}
	scale := int64(1)
	for range gocommerce.CurrencyExponent(code) {
		scale *= 10
	}
	whole, frac := minor/scale, minor%scale
	if places == 0 {
		if frac != 0 {
			return "", fmt.Errorf("paypal takes %s in whole units; %d minor units is not one", code, minor)
		}
		return strconv.FormatInt(whole, 10), nil
	}
	return fmt.Sprintf("%d.%02d", whole, frac), nil
}

// money reads a PayPal amount back into minor units.
func money(a amount) (gocommerce.Money, error) {
	code := strings.ToUpper(a.Currency)
	wholeText, fracText, _ := strings.Cut(a.Value, ".")
	digits := gocommerce.CurrencyExponent(code)
	if len(fracText) > digits {
		return gocommerce.Money{}, fmt.Errorf("paypal amount %q has more decimals than %s", a.Value, code)
	}
	minor, err := strconv.ParseInt(wholeText+fracText+strings.Repeat("0", digits-len(fracText)), 10, 64)
	if err != nil {
		return gocommerce.Money{}, fmt.Errorf("paypal amount %q: %w", a.Value, err)
	}
	return gocommerce.Money{AmountMinor: minor, Currency: code}, nil
}

type amount struct {
	Currency string `json:"currency_code"`
	Value    string `json:"value"`
}

type capture struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Amount   amount `json:"amount"`
	CustomID string `json:"custom_id"`
}

type paypalOrder struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	PurchaseUnits []struct {
		CustomID string `json:"custom_id"`
		Payments struct {
			Captures []capture `json:"captures"`
		} `json:"payments"`
	} `json:"purchase_units"`
	Links []struct {
		Href string `json:"href"`
		Rel  string `json:"rel"`
	} `json:"links"`
}

func (o paypalOrder) madeFor(orderID int64) bool {
	return len(o.PurchaseUnits) > 0 && o.PurchaseUnits[0].CustomID == strconv.FormatInt(orderID, 10)
}

func (o paypalOrder) captures() []capture {
	if len(o.PurchaseUnits) == 0 {
		return nil
	}
	return o.PurchaseUnits[0].Payments.Captures
}

// Initiate implements gocommerce.PaymentProvider: it creates the PayPal order
// and sends the shopper to approve it.
func (m *Module) Initiate(ctx context.Context, order *gocommerce.Order, opts gocommerce.PayOptions) (gocommerce.PaymentIntent, error) {
	if !m.refresh(ctx) {
		return gocommerce.PaymentIntent{}, errNotConfigured
	}
	if opts.ReturnURL == "" {
		return gocommerce.PaymentIntent{}, gocommerce.Validationf("paypal needs a return_url to send the shopper back to")
	}
	total, err := value(order.Total.AmountMinor, order.Total.Currency)
	if err != nil {
		return gocommerce.PaymentIntent{}, gocommerce.Validationf("%s", err.Error())
	}
	payload := map[string]any{
		"intent": "CAPTURE",
		"purchase_units": []map[string]any{{
			"reference_id": order.Number,
			"custom_id":    strconv.FormatInt(order.ID, 10),
			"invoice_id":   order.Number,
			"amount":       amount{Currency: strings.ToUpper(order.Total.Currency), Value: total},
		}},
		"payment_source": map[string]any{"paypal": map[string]any{"experience_context": map[string]any{
			"user_action": "PAY_NOW",
			"return_url":  opts.ReturnURL,
			"cancel_url":  opts.ReturnURL,
		}}},
	}
	var created paypalOrder
	// The key lets a checkout retry get the same PayPal order, not a second one.
	if err := m.post(ctx, "/v2/checkout/orders", "order-"+strconv.FormatInt(order.ID, 10)+"-"+total, payload, &created); err != nil {
		return gocommerce.PaymentIntent{}, err
	}
	approve := ""
	for _, l := range created.Links {
		if l.Rel == "payer-action" || l.Rel == "approve" {
			approve = l.Href
			break
		}
	}
	if approve == "" {
		return gocommerce.PaymentIntent{}, fmt.Errorf("paypal: order %s came back with no approval link", created.ID)
	}
	return gocommerce.PaymentIntent{
		Kind:      gocommerce.IntentRedirect,
		Provider:  m.Code(),
		Reference: created.ID,
		ClientData: map[string]string{
			"url":             approve,
			"paypal_order_id": created.ID,
		},
	}, nil
}

// settle captures the PayPal order if the shopper has approved it and records
// the payment. PayPal answers the capture with the captures themselves, and a
// repeat under the same request id gets the same answer, so two callers racing
// here take the money once; MarkPaid is idempotent for the rest.
func (m *Module) settle(ctx context.Context, order *gocommerce.Order, paypalOrderID string) error {
	var pp paypalOrder
	if err := m.get(ctx, "/v2/checkout/orders/"+url.PathEscape(paypalOrderID), &pp); err != nil {
		return err
	}
	if !pp.madeFor(order.ID) {
		return fmt.Errorf("paypal: order %s was not made for store order %s", paypalOrderID, order.Number)
	}
	if pp.Status == "APPROVED" {
		pp = paypalOrder{}
		if err := m.post(ctx, "/v2/checkout/orders/"+url.PathEscape(paypalOrderID)+"/capture",
			"capture-"+paypalOrderID, struct{}{}, &pp); err != nil {
			return err
		}
	}
	for _, c := range pp.captures() {
		if c.Status != "COMPLETED" {
			continue
		}
		if err := matches(c, order); err != nil {
			return err
		}
		_, err := m.pay.MarkPaid(ctx, order.ID, c.ID)
		return err
	}
	return nil
}

// matches says a capture is for the order's total in its currency.
func matches(c capture, order *gocommerce.Order) error {
	got, err := money(c.Amount)
	if err != nil {
		return err
	}
	if got.AmountMinor != order.Total.AmountMinor || !strings.EqualFold(got.Currency, order.Total.Currency) {
		return fmt.Errorf("paypal: capture %s is for %d %s, not order %s's %d %s", c.ID, got.AmountMinor, got.Currency,
			order.Number, order.Total.AmountMinor, strings.ToUpper(order.Total.Currency))
	}
	return nil
}

func (m *Module) handleCapture(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !m.refresh(ctx) {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("PayPal is not set up"))
		return
	}
	id := r.PathValue("paypal_order_id")
	var orderID int64
	err := m.db.QueryRowContext(ctx,
		`SELECT id FROM orders WHERE payment_reference = $1 ORDER BY id DESC LIMIT 1`, id).Scan(&orderID)
	if errors.Is(err, sql.ErrNoRows) {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no such PayPal order"))
		return
	}
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	order, err := m.orders.Get(ctx, orderID)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if order.PaymentProvider != m.Code() {
		gocommerce.RespondError(w, r, gocommerce.Conflictf("order %s is no longer paid through PayPal", order.Number))
		return
	}
	if (order.PaymentStatus == gocommerce.PaymentPending || order.PaymentStatus == gocommerce.PaymentFailed) && m.mayAsk(ctx, id) {
		if err := m.settle(ctx, order, id); err != nil {
			m.log.Error("could not capture a PayPal order", "order_id", orderID, "error", err)
		}
		if order, err = m.orders.Get(ctx, orderID); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
	}
	gocommerce.Respond(w, http.StatusOK, map[string]string{
		"order":          order.Number,
		"payment_status": order.PaymentStatus,
	})
}

// mayAsk caps a public route's calls to PayPal, per process, which is all a cap
// on a reloading page needs.
func (m *Module) mayAsk(ctx context.Context, paypalOrderID string) bool {
	store, _ := gocommerce.StoreFrom(ctx)
	key := store + "/" + paypalOrderID
	now := time.Now()
	for {
		last, loaded := m.asked.LoadOrStore(key, now)
		if !loaded {
			return true
		}
		if now.Sub(last.(time.Time)) < captureQuiet {
			return false
		}
		if m.asked.CompareAndSwap(key, last, now) {
			return true
		}
	}
}

// Status implements gocommerce.PaymentStatusReader. An order's reference is the
// PayPal order until it is paid and the capture after, and the two ids look
// alike, so the order is asked for first. It only reads: an approved order
// waiting for capture is reported, not captured.
func (m *Module) Status(ctx context.Context, order *gocommerce.Order) (gocommerce.PaymentState, error) {
	if !m.refresh(ctx) {
		return gocommerce.PaymentState{}, errNotConfigured
	}
	ref := strings.TrimSpace(order.PaymentReference)
	if ref == "" {
		return gocommerce.PaymentState{}, fmt.Errorf("paypal: order %s has no PayPal payment to ask about", order.Number)
	}
	var pp paypalOrder
	err := m.get(ctx, "/v2/checkout/orders/"+url.PathEscape(ref), &pp)
	var missing *notFound
	switch {
	case errors.As(err, &missing):
		c, why, err := m.boundCapture(ctx, order, ref)
		if err != nil {
			return gocommerce.PaymentState{}, err
		}
		if why != "" {
			return gocommerce.PaymentState{Detail: why}, nil
		}
		return captureState(c, order), nil
	case err != nil:
		return gocommerce.PaymentState{}, err
	}
	if !pp.madeFor(order.ID) {
		return gocommerce.PaymentState{Detail: "PayPal order " + ref + " was made for another order"}, nil
	}
	state := gocommerce.PaymentState{Detail: pp.Status}
	for _, c := range pp.captures() {
		state = captureState(c, order)
		if state.Paid {
			break
		}
	}
	return state, nil
}

func captureState(c capture, order *gocommerce.Order) gocommerce.PaymentState {
	state := gocommerce.PaymentState{Detail: c.Status, Failed: c.Status == "DECLINED" || c.Status == "FAILED"}
	if c.Status != "COMPLETED" && c.Status != "REFUNDED" && c.Status != "PARTIALLY_REFUNDED" {
		return state
	}
	if err := matches(c, order); err != nil {
		state.Detail = err.Error()
		return state
	}
	got, _ := money(c.Amount)
	state.Paid, state.Reference, state.Amount = true, c.ID, &got
	return state
}

// boundCapture reads a capture back and checks it is this order's. A reference
// can be typed by hand, so nothing is refunded or settled on its word alone.
func (m *Module) boundCapture(ctx context.Context, order *gocommerce.Order, id string) (capture, string, error) {
	var c capture
	if err := m.get(ctx, "/v2/payments/captures/"+url.PathEscape(id), &c); err != nil {
		return c, "", err
	}
	if c.CustomID != strconv.FormatInt(order.ID, 10) {
		return c, "PayPal capture " + id + " belongs to another order", nil
	}
	return c, "", nil
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
	captureID := strings.TrimSpace(order.PaymentReference)
	if captureID == "" {
		return "", errors.New("paypal: this order has no payment to refund")
	}
	_, why, err := m.boundCapture(ctx, order, captureID)
	if err != nil {
		return "", err
	}
	if why != "" {
		return "", errors.New("paypal: " + why)
	}
	v, err := value(amountMinor, order.Total.Currency)
	if err != nil {
		return "", err
	}
	var refund struct {
		ID string `json:"id"`
	}
	payload := map[string]any{"amount": amount{Currency: strings.ToUpper(order.Total.Currency), Value: v}}
	if err := m.post(ctx, "/v2/payments/captures/"+url.PathEscape(captureID)+"/refund", "", payload, &refund); err != nil {
		return "", err
	}
	return refund.ID, nil
}

// Webhook implements gocommerce.WebhookProvider.
func (m *Module) Webhook() http.Handler { return http.HandlerFunc(m.handleWebhook) }

func (m *Module) handleWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !m.refresh(ctx) {
		http.Error(w, "payment method not set up", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBytes))
	if err != nil {
		http.Error(w, "could not read body", http.StatusBadRequest)
		return
	}
	ok, err := m.verify(ctx, r.Header, body)
	if err != nil {
		// PayPal being unreachable says nothing about the event: let it retry.
		m.log.Error("could not check a PayPal webhook's signature", "error", err)
		http.Error(w, "could not verify", http.StatusBadGateway)
		return
	}
	if !ok {
		m.log.Warn("rejected a PayPal webhook with an invalid signature")
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	var event struct {
		ID       string `json:"id"`
		Type     string `json:"event_type"`
		Resource struct {
			paypalOrder
			Amount            amount `json:"amount"`
			CustomID          string `json:"custom_id"`
			SupplementaryData struct {
				RelatedIDs struct {
					OrderID string `json:"order_id"`
				} `json:"related_ids"`
			} `json:"supplementary_data"`
		} `json:"resource"`
	}
	if err := json.Unmarshal(body, &event); err != nil || event.ID == "" {
		http.Error(w, "malformed event", http.StatusBadRequest)
		return
	}
	res := event.Resource

	var custom, paypalOrderID string
	switch event.Type {
	case "CHECKOUT.ORDER.APPROVED":
		paypalOrderID = res.ID
		if len(res.PurchaseUnits) > 0 {
			custom = res.PurchaseUnits[0].CustomID
		}
	case "PAYMENT.CAPTURE.COMPLETED", "PAYMENT.CAPTURE.DENIED":
		custom = res.CustomID
		paypalOrderID = res.SupplementaryData.RelatedIDs.OrderID
	default:
		w.WriteHeader(http.StatusOK)
		return
	}
	orderID, _ := strconv.ParseInt(custom, 10, 64)
	if orderID == 0 {
		m.log.Info("acknowledged a PayPal event that names no order", "event", event.Type, "resource", res.ID)
		w.WriteHeader(http.StatusOK)
		return
	}

	result, err := m.db.ExecContext(ctx, `
		INSERT INTO payments_paypal_events (id, event, order_id) VALUES ($1, $2, $3)
		ON CONFLICT (store_id, id) DO NOTHING`, event.ID, event.Type, orderID)
	if err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}
	fail := func(what string, err error) {
		m.log.Error(what, "order_id", orderID, "event", event.Type, "error", err)
		m.releaseClaim(ctx, event.ID)
		http.Error(w, what, http.StatusInternalServerError)
	}

	order, err := m.orders.Get(ctx, orderID)
	if errors.Is(err, gocommerce.ErrNotFound) {
		// Another store's order, on a PayPal account the two share.
		m.releaseClaim(ctx, event.ID)
		w.WriteHeader(http.StatusOK)
		return
	}
	if err != nil {
		fail("could not read the order", err)
		return
	}

	switch event.Type {
	case "CHECKOUT.ORDER.APPROVED":
		if err := m.settle(ctx, order, paypalOrderID); err != nil {
			fail("could not capture the payment", err)
			return
		}
	case "PAYMENT.CAPTURE.COMPLETED":
		c := capture{ID: res.ID, Status: "COMPLETED", Amount: res.Amount}
		if err := matches(c, order); err != nil {
			// Settled nowhere and not retried: money for the wrong amount is for
			// a person to look at, and a retry would say the same thing.
			m.log.Error("ignored a PayPal capture that does not match its order", "order_id", orderID, "error", err)
			break
		}
		if _, err := m.pay.MarkPaid(ctx, orderID, res.ID); err != nil {
			fail("could not apply payment", err)
			return
		}
	case "PAYMENT.CAPTURE.DENIED":
		if order.PaymentStatus == gocommerce.PaymentPaid || order.PaymentStatus == gocommerce.PaymentRefunded {
			m.log.Info("ignored a PayPal denial for a settled order", "order_id", orderID, "capture", res.ID)
			break
		}
		if _, err := m.pay.MarkFailed(ctx, orderID, "paypal denied the capture"); err != nil {
			fail("could not apply failure", err)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (m *Module) releaseClaim(ctx context.Context, id string) {
	if _, err := m.db.ExecContext(ctx, `DELETE FROM payments_paypal_events WHERE id = $1`, id); err != nil {
		m.log.Error("could not release the event claim", "id", id, "error", err)
	}
}

// verify asks PayPal whether a webhook's signature is good. The event goes back
// as the bytes it came in: a re-encoded body would change what was signed.
func (m *Module) verify(ctx context.Context, h http.Header, body []byte) (bool, error) {
	if h.Get("Paypal-Transmission-Sig") == "" || !json.Valid(body) {
		return false, nil
	}
	head, err := json.Marshal(map[string]string{
		"auth_algo":         h.Get("Paypal-Auth-Algo"),
		"cert_url":          h.Get("Paypal-Cert-Url"),
		"transmission_id":   h.Get("Paypal-Transmission-Id"),
		"transmission_sig":  h.Get("Paypal-Transmission-Sig"),
		"transmission_time": h.Get("Paypal-Transmission-Time"),
		"webhook_id":        m.conf(ctx).WebhookID,
	})
	if err != nil {
		return false, err
	}
	raw := append(append(head[:len(head)-1:len(head)-1], `,"webhook_event":`...), body...)
	raw = append(raw, '}')
	var out struct {
		Status string `json:"verification_status"`
	}
	if err := m.send(ctx, http.MethodPost, "/v1/notifications/verify-webhook-signature", "", raw, &out); err != nil {
		return false, err
	}
	return out.Status == "SUCCESS", nil
}

// notFound is PayPal answering 404.
type notFound struct{ path string }

func (e *notFound) Error() string { return "paypal: " + e.path + " not found" }

func (m *Module) post(ctx context.Context, path, requestID string, payload, out any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return m.send(ctx, http.MethodPost, path, requestID, encoded, out)
}

func (m *Module) get(ctx context.Context, path string, out any) error {
	return m.send(ctx, http.MethodGet, path, "", nil, out)
}

func (m *Module) send(ctx context.Context, method, path, requestID string, body []byte, out any) error {
	token, err := m.token(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, m.conf(ctx).BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if requestID != "" {
		req.Header.Set("PayPal-Request-Id", requestID)
	}
	// Without this, a capture may answer with a bare order and no captures.
	req.Header.Set("Prefer", "return=representation")
	return m.do(req, path, out)
}

func (m *Module) do(req *http.Request, path string, out any) error {
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("paypal: %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("paypal: %s: read response: %w", path, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return &notFound{path}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Name    string `json:"name"`
			Message string `json:"message"`
			Details []struct {
				Issue string `json:"issue"`
			} `json:"details"`
		}
		_ = json.Unmarshal(body, &apiErr)
		if apiErr.Message != "" {
			issue := ""
			if len(apiErr.Details) > 0 {
				issue = " (" + apiErr.Details[0].Issue + ")"
			}
			return fmt.Errorf("paypal: %s: %s%s", path, apiErr.Message, issue)
		}
		return fmt.Errorf("paypal: %s returned %s", path, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

type cachedToken struct {
	value   string
	expires time.Time
}

// token is a bearer token for the store's credentials, reused until shortly
// before it expires because PayPal rate-limits asking for new ones.
func (m *Module) token(ctx context.Context) (string, error) {
	c := m.conf(ctx)
	key := c.BaseURL + "|" + c.ClientID + "|" + c.ClientSecret
	if t, ok := m.tokens.Load(key); ok && time.Now().Before(t.(cachedToken).expires) {
		return t.(cachedToken).value, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/oauth2/token",
		strings.NewReader("grant_type=client_credentials"))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.ClientID, c.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := m.do(req, "/v1/oauth2/token", &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", errors.New("paypal: no access token in the answer")
	}
	m.tokens.Store(key, cachedToken{out.AccessToken, time.Now().Add(time.Duration(out.ExpiresIn-60) * time.Second)})
	return out.AccessToken, nil
}
