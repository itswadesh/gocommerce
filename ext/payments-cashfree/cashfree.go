// Package cashfree takes payments through Cashfree Payments, India: UPI,
// cards, netbanking and wallets on Cashfree's own checkout.
//
//	app, err := gocommerce.New(cfg,
//	    cashfree.New(cashfree.Config{
//	        AppID:     os.Getenv("CASHFREE_APP_ID"),
//	        SecretKey: os.Getenv("CASHFREE_SECRET_KEY"),
//	    }),
//	)
//
// Cashfree then calls POST /api/checkout/cashfree/webhook, a route the engine
// owns and hands to this module with the body untouched. Its URL is set in the
// Cashfree dashboard, under Developers, Webhooks, for the payment events.
//
// REST over net/http and no SDK, per rule 2.
//
// # The drop-in
//
// Initiate creates a Cashfree order and hands the storefront its
// payment_session_id, which Cashfree's JavaScript checkout opens. The shopper
// pays on Cashfree's page, so card numbers never reach the store. Coming back
// proves nothing: the signed webhook settles the order, and the storefront's
// return page can ask POST /x/payments-cashfree/orders/{cf_order_id}/verify to
// have Cashfree asked at once rather than wait for it.
//
// # One key, two jobs
//
// Cashfree signs its webhooks with the API secret key, so there is no separate
// webhook secret to configure: the key that opens orders also verifies what
// Cashfree says about them.
//
// # Rupees on the wire, paise in the engine
//
// Cashfree takes and reports amounts as rupees with two decimals. The engine
// holds paise. The two are converted as strings of digits, never through a
// float (rule 6), and an order in any currency but INR is refused before a
// Cashfree order is made.
//
// # The module's own order ids
//
// Cashfree orders carry an id the merchant chooses, unique across the
// account. Two stores can share an account and a checkout can be retried, so
// the id is the order number with a random suffix, and this module keeps its
// own table from that id to the store's order, with the amount it asked for.
package cashfree

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
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
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	// Fixed in code, so not an operator's choice of address (D109); the
	// base_url setting that overrides them is, and is marked Outbound.
	productionBaseURL = "https://api.cashfree.com/pg"
	sandboxBaseURL    = "https://sandbox.cashfree.com/pg"
	// apiVersion pins the request and response shapes, and the webhook
	// version the dashboard should be set to.
	apiVersion = "2023-08-01"
	// Cashfree signs a timestamp and names no tolerance; five minutes allows
	// for clock skew and keeps a captured delivery from being a standing key
	// to the store.
	signatureTolerance = 5 * time.Minute
	maxBodyBytes       = 1 << 20
	currency           = "INR"
)

// Config configures the module.
type Config struct {
	// AppID is the API key's client id (x-client-id). Required.
	AppID string `plugin:"app_id"`
	// SecretKey is the API secret (x-client-secret). Required: it also signs
	// the webhooks, so without it any caller could mark orders paid.
	SecretKey string `plugin:"secret_key"`
	// Environment is "production" (the default) or "sandbox", which picks
	// Cashfree's host. Each has its own keys.
	Environment string `plugin:"environment"`
	// BaseURL overrides the environment's host, for tests.
	BaseURL string `plugin:"base_url"`
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the Cashfree payment provider.
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
const PluginKey = "payments-cashfree"

var errNotConfigured = errors.New("cashfree: not set up — activate and configure it under Settings")

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
	return strings.TrimSpace(c.AppID) != "" && strings.TrimSpace(c.SecretKey) != ""
}

// finish fills a configuration's defaults.
func (m *Module) finish(c *Config) {
	c.Environment = strings.ToLower(strings.TrimSpace(c.Environment))
	if c.Environment != "sandbox" {
		c.Environment = "production"
	}
	switch {
	case strings.TrimSpace(c.BaseURL) != "":
	case c.Environment == "sandbox":
		c.BaseURL = sandboxBaseURL
	default:
		c.BaseURL = productionBaseURL
	}
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "payments-cashfree" }

// Migrations implements gocommerce.Module.
//
// Two tables. Orders is how a Cashfree order id finds the store's order, and
// what it asked for. Events makes webhook handling idempotent: Cashfree
// retries, and settling an order twice must be impossible rather than merely
// unlikely. Neither key is called id, so scoping puts the store in front of
// both (D83).
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_orders_and_events",
		SQL: `
			CREATE TABLE payments_cashfree_orders (
			    cf_order_id  text        PRIMARY KEY,
			    order_id     bigint      NOT NULL,
			    amount_minor bigint      NOT NULL,
			    currency     text        NOT NULL,
			    created_at   timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX payments_cashfree_orders_order_idx ON payments_cashfree_orders (order_id);
			CREATE TABLE payments_cashfree_events (
			    event_id    text        PRIMARY KEY,
			    order_id    bigint,
			    received_at timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX payments_cashfree_events_received_idx ON payments_cashfree_events (received_at);`,
	},
		gocommerce.ScopeMigration("0002_store_scope", "payments_cashfree_orders", "payments_cashfree_events"),
	}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	// Config-configured stores start switched on; a store with nothing in
	// Config starts idle and waits for the panel (D59).
	inCode := m.complete(&m.cfg)
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "Cashfree", Category: "payments", DefaultEnabled: inCode,
		Description: "UPI, cards, netbanking and wallets on Cashfree's checkout, India, with its signed webhook marking orders paid. Rupees only.",
		Docs:        "https://www.cashfree.com/docs/payments/online/intro",
		Fields: []gocommerce.PluginField{
			{Key: "app_id", Label: "App ID", Kind: "text", Required: !inCode, Help: "Developers, API Keys, in the Cashfree dashboard."},
			{Key: "secret_key", Label: "Secret key", Kind: "secret", Required: !inCode, Help: "The same page. It also signs the webhooks: set the webhook URL to this store's /api/checkout/cashfree/webhook, version 2023-08-01."},
			{Key: "environment", Label: "Environment", Kind: "select", Options: []string{"production", "sandbox"}, Default: "production", Help: "Sandbox is Cashfree's test mode, with its own keys."},
			{Key: "base_url", Label: "API base URL", Kind: "url", Outbound: true, Help: "Empty for the environment's own host."},
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
	app.HandleFunc("POST /x/payments-cashfree/orders/{cf_order_id}/verify", m.handleVerify)
	return nil
}

// Code implements gocommerce.PaymentProvider.
func (m *Module) Code() string { return "cashfree" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "Cashfree" }

// Initiate opens a Cashfree order for the order's total and hands its payment
// session to the storefront.
func (m *Module) Initiate(ctx context.Context, order *gocommerce.Order, opts gocommerce.PayOptions) (gocommerce.PaymentIntent, error) {
	if !m.refresh(ctx) {
		return gocommerce.PaymentIntent{}, errNotConfigured
	}
	c := m.conf(ctx)
	if !strings.EqualFold(order.Total.Currency, currency) {
		return gocommerce.PaymentIntent{}, fmt.Errorf("cashfree: Cashfree takes INR and the order is in %s", order.Total.Currency)
	}
	if order.Total.AmountMinor <= 0 {
		return gocommerce.PaymentIntent{}, errors.New("cashfree: there is nothing to pay")
	}
	phone := cashfreePhone(order.Phone)
	if phone == "" {
		phone = cashfreePhone(order.Address.Phone)
	}
	if phone == "" {
		return gocommerce.PaymentIntent{}, errors.New("cashfree: Cashfree needs the shopper's phone number, and the order has none")
	}
	cfOrderID, err := newOrderID(order.Number)
	if err != nil {
		return gocommerce.PaymentIntent{}, err
	}

	customer := map[string]string{"customer_id": customerID(order), "customer_phone": phone}
	if order.Email != "" {
		customer["customer_email"] = order.Email
	}
	if order.Name != "" {
		customer["customer_name"] = order.Name
	}
	body := map[string]any{
		"order_id":         cfOrderID,
		"order_amount":     rupees(order.Total.AmountMinor),
		"order_currency":   currency,
		"customer_details": customer,
		// For the merchant reading Cashfree's dashboard; nothing here relies
		// on them coming back.
		"order_tags": map[string]string{
			"order_id":     strconv.FormatInt(order.ID, 10),
			"order_number": order.Number,
		},
	}
	if ret := strings.TrimSpace(opts.ReturnURL); ret != "" {
		// Cashfree fills {order_id} in on the way back, so the storefront's
		// page knows which Cashfree order to have verified.
		sep := "?"
		if strings.Contains(ret, "?") {
			sep = "&"
		}
		body["order_meta"] = map[string]string{"return_url": ret + sep + "cf_order_id={order_id}"}
	}

	var out struct {
		OrderID          string `json:"order_id"`
		PaymentSessionID string `json:"payment_session_id"`
	}
	if err := m.do(ctx, http.MethodPost, "/orders", body, &out); err != nil {
		return gocommerce.PaymentIntent{}, err
	}
	if out.PaymentSessionID == "" {
		return gocommerce.PaymentIntent{}, errors.New("cashfree: the order came back without a payment session")
	}
	// Without this row the webhook could not find the order, so a session
	// that cannot be recorded is not handed to the shopper.
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO payments_cashfree_orders (cf_order_id, order_id, amount_minor, currency)
		VALUES ($1, $2, $3, $4)`,
		cfOrderID, order.ID, order.Total.AmountMinor, currency); err != nil {
		return gocommerce.PaymentIntent{}, fmt.Errorf("cashfree: could not record the Cashfree order: %w", err)
	}

	return gocommerce.PaymentIntent{
		Kind:     gocommerce.IntentClientAction,
		Provider: m.Code(),
		// The Cashfree order, until a payment on it replaces it.
		Reference: cfOrderID,
		ClientData: map[string]string{
			"payment_session_id": out.PaymentSessionID,
			"order_id":           cfOrderID,
			// Cashfree's JavaScript checkout is told which environment the
			// session belongs to; a session opened in the wrong one fails.
			"environment": c.Environment,
		},
	}, nil
}

// Refund implements gocommerce.Refunder.
func (m *Module) Refund(ctx context.Context, order *gocommerce.Order, amountMinor int64) error {
	_, err := m.RefundWithReference(ctx, order, amountMinor)
	return err
}

// RefundWithReference implements gocommerce.ReferencedRefunder. Cashfree
// refunds against its order, not the payment, so the Cashfree order the money
// was taken on is found first.
func (m *Module) RefundWithReference(ctx context.Context, order *gocommerce.Order, amountMinor int64) (string, error) {
	if !m.refresh(ctx) {
		return "", errNotConfigured
	}
	cfOrderID, err := m.paidCashfreeOrder(ctx, order)
	if err != nil {
		return "", err
	}
	refundID, err := randomHex(6)
	if err != nil {
		return "", err
	}
	body := map[string]any{
		"refund_amount": rupees(amountMinor),
		// Cashfree's refund ids are the merchant's and must be unique; the
		// order id keeps them findable in its dashboard.
		"refund_id":   "gc" + strconv.FormatInt(order.ID, 10) + "r" + refundID,
		"refund_note": clip("Refund for order "+order.Number, 100),
	}
	var out struct {
		CFRefundID        string `json:"cf_refund_id"`
		RefundID          string `json:"refund_id"`
		RefundStatus      string `json:"refund_status"`
		StatusDescription string `json:"status_description"`
	}
	if err := m.do(ctx, http.MethodPost, "/orders/"+url.PathEscape(cfOrderID)+"/refunds", body, &out); err != nil {
		return "", err
	}
	switch strings.ToUpper(out.RefundStatus) {
	case "CANCELLED", "REJECTED":
		return "", fmt.Errorf("cashfree: refund %s was %s: %s", out.RefundID, out.RefundStatus,
			firstNonEmpty(out.StatusDescription, "no reason given"))
	}
	// PENDING and ONHOLD are refunds Cashfree accepted and will process; the
	// money is on its way, which is what the engine records.
	return firstNonEmpty(out.CFRefundID, out.RefundID), nil
}

// paidCashfreeOrder is the Cashfree order an order's money was taken on. Most
// orders have one; a retried checkout leaves more, and then Cashfree is asked
// which of them holds the payment the order is settled by.
func (m *Module) paidCashfreeOrder(ctx context.Context, order *gocommerce.Order) (string, error) {
	rows, err := m.cashfreeOrders(ctx, order.ID)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("cashfree: order %s has no Cashfree order to refund", order.Number)
	}
	if len(rows) == 1 {
		return rows[0].id, nil
	}
	ref := strings.TrimSpace(order.PaymentReference)
	for _, row := range rows {
		if row.id == ref {
			return row.id, nil
		}
		payments, err := m.payments(ctx, row.id)
		if err != nil {
			return "", err
		}
		for _, p := range payments {
			if string(p.CFPaymentID) == ref && strings.EqualFold(p.PaymentStatus, "SUCCESS") {
				return row.id, nil
			}
		}
	}
	return "", fmt.Errorf("cashfree: none of order %s's Cashfree orders holds payment %q", order.Number, ref)
}

type cashfreeOrder struct {
	id     string
	amount int64
}

// cashfreeOrders lists an order's Cashfree orders, newest first.
func (m *Module) cashfreeOrders(ctx context.Context, orderID int64) ([]cashfreeOrder, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT cf_order_id, amount_minor FROM payments_cashfree_orders
		WHERE order_id = $1 ORDER BY created_at DESC, cf_order_id`, orderID)
	if err != nil {
		return nil, fmt.Errorf("cashfree: could not read the Cashfree orders: %w", err)
	}
	defer rows.Close()
	var out []cashfreeOrder
	for rows.Next() {
		var o cashfreeOrder
		if err := rows.Scan(&o.id, &o.amount); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ------------------------------------------------------------------ status

// payment is one entry of GET /orders/{order_id}/payments.
type payment struct {
	CFPaymentID    flexID      `json:"cf_payment_id"`
	PaymentStatus  string      `json:"payment_status"`
	OrderAmount    json.Number `json:"order_amount"`
	PaymentMessage string      `json:"payment_message"`
}

func (m *Module) payments(ctx context.Context, cfOrderID string) ([]payment, error) {
	var out []payment
	if err := m.do(ctx, http.MethodGet, "/orders/"+url.PathEscape(cfOrderID)+"/payments", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Status implements gocommerce.PaymentStatusReader: it asks Cashfree for the
// payments on each of the order's Cashfree orders, for the order whose webhook
// never came and for the shopper's return.
//
// Paid means a SUCCESS on a Cashfree order made for the order's present total,
// asked for in rupees. An offer can make the payment itself smaller, so the
// amount checked is what the Cashfree order was for.
func (m *Module) Status(ctx context.Context, order *gocommerce.Order) (gocommerce.PaymentState, error) {
	if !m.refresh(ctx) {
		return gocommerce.PaymentState{}, errNotConfigured
	}
	rows, err := m.cashfreeOrders(ctx, order.ID)
	if err != nil {
		return gocommerce.PaymentState{}, err
	}
	if len(rows) == 0 {
		return gocommerce.PaymentState{}, fmt.Errorf("cashfree: order %s has no Cashfree order to ask about", order.Number)
	}
	var state gocommerce.PaymentState
	var latest *payment
	for _, row := range rows {
		list, err := m.payments(ctx, row.id)
		if err != nil {
			return gocommerce.PaymentState{}, err
		}
		for i, p := range list {
			if latest == nil {
				latest = &list[i]
			}
			if !strings.EqualFold(p.PaymentStatus, "SUCCESS") {
				continue
			}
			asked, err := paise(p.OrderAmount)
			if err != nil {
				asked = row.amount
			}
			if asked == row.amount && row.amount == order.Total.AmountMinor && strings.EqualFold(order.Total.Currency, currency) {
				return gocommerce.PaymentState{Paid: true, Reference: string(p.CFPaymentID), Detail: "SUCCESS"}, nil
			}
			state.Detail = fmt.Sprintf("SUCCESS for %d paise, not the order's %d %s",
				asked, order.Total.AmountMinor, strings.ToUpper(order.Total.Currency))
		}
	}
	if state.Detail != "" {
		return state, nil
	}
	if latest == nil {
		return gocommerce.PaymentState{Detail: "no payment attempted yet"}, nil
	}
	state.Detail = latest.PaymentStatus
	if strings.EqualFold(latest.PaymentStatus, "FAILED") {
		state.Failed = true
		if latest.PaymentMessage != "" {
			state.Detail += ": " + latest.PaymentMessage
		}
	}
	return state, nil
}

// handleVerify is the storefront's return page asking for its Cashfree order
// to be checked now rather than when the webhook arrives. It records only what
// Cashfree reports, through the engine's own check, and answers with the
// order's payment status alone.
//
// The Cashfree order id is the credential, the way it is at Cashfree: only
// the shopper who was handed it has it, and its suffix is random. A paid order
// is answered from the store without asking Cashfree again, so the route
// cannot be used to spend the merchant's API allowance on settled orders.
func (m *Module) handleVerify(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !m.refresh(ctx) {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("Cashfree is not set up"))
		return
	}
	var orderID int64
	err := m.db.QueryRowContext(ctx,
		`SELECT order_id FROM payments_cashfree_orders WHERE cf_order_id = $1`,
		r.PathValue("cf_order_id")).Scan(&orderID)
	if errors.Is(err, sql.ErrNoRows) {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no such Cashfree order"))
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
	if order.PaymentStatus == gocommerce.PaymentPending || order.PaymentStatus == gocommerce.PaymentFailed {
		check, err := m.pay.CheckPayment(ctx, orderID)
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		order = check.Order
	}
	gocommerce.Respond(w, http.StatusOK, map[string]string{
		"order":          order.Number,
		"payment_status": order.PaymentStatus,
	})
}

// ----------------------------------------------------------------- webhook

// Webhook implements gocommerce.WebhookProvider.
func (m *Module) Webhook() http.Handler { return http.HandlerFunc(m.handleWebhook) }

// event is a payment webhook, version 2023-08-01.
type event struct {
	Type string `json:"type"`
	Data struct {
		Order struct {
			OrderID       string      `json:"order_id"`
			OrderAmount   json.Number `json:"order_amount"`
			OrderCurrency string      `json:"order_currency"`
		} `json:"order"`
		Payment struct {
			CFPaymentID    flexID `json:"cf_payment_id"`
			PaymentStatus  string `json:"payment_status"`
			PaymentMessage string `json:"payment_message"`
		} `json:"payment"`
	} `json:"data"`
}

func (m *Module) handleWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// A notification for a provider nobody has set up cannot be verified, and
	// an unverifiable notification is refused.
	if !m.refresh(ctx) {
		http.Error(w, "payment method not set up", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "could not read body", http.StatusBadRequest)
		return
	}
	if err := verifySignature(body, r.Header.Get("x-webhook-signature"), r.Header.Get("x-webhook-timestamp"),
		[]byte(m.conf(ctx).SecretKey), time.Now()); err != nil {
		m.log.Warn("rejected a Cashfree webhook", "error", err)
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	var ev event
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, "malformed event", http.StatusBadRequest)
		return
	}
	// Acknowledged and left alone: a user who dropped out (the order is still
	// payable and nothing happened), the charges report, and anything else.
	if ev.Type != "PAYMENT_SUCCESS_WEBHOOK" && ev.Type != "PAYMENT_FAILED_WEBHOOK" {
		w.WriteHeader(http.StatusOK)
		return
	}
	paymentID := string(ev.Data.Payment.CFPaymentID)
	if paymentID == "" || ev.Data.Order.OrderID == "" {
		http.Error(w, "malformed event", http.StatusBadRequest)
		return
	}

	// A failure and then a success can arrive for one Cashfree order, so the
	// claim is on the payment and what it says.
	claim := ev.Type + ":" + paymentID
	res, err := m.db.ExecContext(ctx, `
		INSERT INTO payments_cashfree_events (event_id) VALUES ($1)
		ON CONFLICT (store_id, event_id) DO NOTHING`, claim)
	if err != nil {
		m.log.Error("could not record the Cashfree event", "claim", claim, "error", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Already handled. 200 stops the retries.
		w.WriteHeader(http.StatusOK)
		return
	}

	var orderID, amount int64
	err = m.db.QueryRowContext(ctx, `
		SELECT order_id, amount_minor FROM payments_cashfree_orders WHERE cf_order_id = $1`,
		ev.Data.Order.OrderID).Scan(&orderID, &amount)
	if errors.Is(err, sql.ErrNoRows) {
		// A payment on a Cashfree order this store did not make: another
		// store's on a shared account, or a payment link. Acknowledged rather
		// than retried for ever.
		m.log.Info("a Cashfree payment named no order this store made", "cf_order_id", ev.Data.Order.OrderID, "payment", paymentID)
		w.WriteHeader(http.StatusOK)
		return
	}
	if err != nil {
		m.log.Error("could not find the Cashfree order", "cf_order_id", ev.Data.Order.OrderID, "error", err)
		m.releaseClaim(ctx, claim)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if _, err := m.db.ExecContext(ctx,
		`UPDATE payments_cashfree_events SET order_id = $2 WHERE event_id = $1`, claim, orderID); err != nil {
		m.log.Warn("could not record which order a Cashfree event was for", "claim", claim, "order_id", orderID, "error", err)
	}

	order, err := m.orders.Get(ctx, orderID)
	if err != nil {
		m.log.Error("could not read the order a Cashfree payment is for", "order_id", orderID, "error", err)
		m.releaseClaim(ctx, claim)
		http.Error(w, "could not read the order", http.StatusInternalServerError)
		return
	}
	// Settled already, by an earlier attempt or by hand: an attempt's failure
	// arriving late says nothing about the order, and recording it would be
	// refused and answered 500 for Cashfree to retry for ever.
	if order.PaymentStatus == gocommerce.PaymentPaid || order.PaymentStatus == gocommerce.PaymentRefunded {
		w.WriteHeader(http.StatusOK)
		return
	}

	if ev.Type == "PAYMENT_FAILED_WEBHOOK" {
		if _, err := m.pay.MarkFailed(ctx, orderID, "cashfree reported a failed payment: "+firstNonEmpty(ev.Data.Payment.PaymentMessage, "no reason given")); err != nil {
			m.log.Error("could not mark the payment failed", "order_id", orderID, "error", err)
			m.releaseClaim(ctx, claim)
			http.Error(w, "could not apply the failure", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	// The order an offer was applied to still asked for its whole amount, so
	// that is what is compared, not the payment. A Cashfree order made for a
	// total the order no longer has does not settle it: that would put the
	// store's books and Cashfree's out of step with nobody the wiser, so it is
	// left for an operator who can see both.
	asked, err := paise(ev.Data.Order.OrderAmount)
	if err != nil || asked != amount || amount != order.Total.AmountMinor ||
		!strings.EqualFold(ev.Data.Order.OrderCurrency, currency) || !strings.EqualFold(order.Total.Currency, currency) {
		m.log.Error("a Cashfree payment does not match the order it is for",
			"order_id", orderID, "order_total_minor", order.Total.AmountMinor, "order_currency", order.Total.Currency,
			"cashfree_order_amount", ev.Data.Order.OrderAmount.String(), "cashfree_currency", ev.Data.Order.OrderCurrency,
			"recorded_minor", amount, "payment", paymentID)
		w.WriteHeader(http.StatusOK)
		return
	}
	// The engine performs the transition and writes the event. This module
	// never touches an order row itself.
	if _, err := m.pay.MarkPaid(ctx, orderID, paymentID); err != nil {
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
		`DELETE FROM payments_cashfree_events WHERE event_id = $1`, claim); err != nil {
		m.log.Error("could not release the event claim", "claim", claim, "error", err)
	}
}

// verifySignature checks x-webhook-signature: the base64 HMAC-SHA256, keyed
// on the API secret, of x-webhook-timestamp followed directly by the raw body,
// as Cashfree's own SDKs compute it.
func verifySignature(body []byte, signature, timestamp string, secret []byte, now time.Time) error {
	signature, timestamp = strings.TrimSpace(signature), strings.TrimSpace(timestamp)
	if signature == "" {
		return errors.New("missing x-webhook-signature")
	}
	if timestamp == "" {
		return errors.New("missing x-webhook-timestamp")
	}
	stamp, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("x-webhook-timestamp %q is not a unix time", timestamp)
	}
	// Cashfree's samples stamp in milliseconds. A stamp too small to be one is
	// seconds, so a change of unit on their side is not read as 1970.
	at := time.UnixMilli(stamp)
	if stamp < 1e11 {
		at = time.Unix(stamp, 0)
	}
	// The timestamp is inside the signed string, so an attacker replaying a
	// captured delivery cannot move it, which is what makes this bound real.
	if drift := now.Sub(at); drift > signatureTolerance || drift < -signatureTolerance {
		return fmt.Errorf("x-webhook-timestamp is %s away from now", drift.Round(time.Second))
	}
	got, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return errors.New("x-webhook-signature is not base64")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	mac.Write(body)
	// Constant time, so a wrong signature cannot be found a byte at a time.
	if !hmac.Equal(got, mac.Sum(nil)) {
		return errors.New("x-webhook-signature did not match")
	}
	return nil
}

// ------------------------------------------------------------------- calls

// do performs one JSON request against the PG API.
func (m *Module) do(ctx context.Context, method, path string, body any, out any) error {
	c := m.conf(ctx)
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("cashfree: could not encode the request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("x-client-id", c.AppID)
	req.Header.Set("x-client-secret", c.SecretKey)
	req.Header.Set("x-api-version", apiVersion)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("cashfree: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return fmt.Errorf("cashfree: reading the response to %s %s: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var fail struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		}
		if json.Unmarshal(payload, &fail) == nil && fail.Message != "" {
			return fmt.Errorf("cashfree: %s %s: %d %s", method, path, resp.StatusCode, fail.Message)
		}
		return fmt.Errorf("cashfree: %s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if out == nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("cashfree: could not read the response to %s %s: %w", method, path, err)
	}
	return nil
}

// ------------------------------------------------------------------ values

// rupees writes paise as Cashfree's two-decimal rupee amount, as a JSON
// number built from the digits: 2500 is 25.00.
func rupees(minor int64) json.Number {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	return json.Number(fmt.Sprintf("%s%d.%02d", sign, minor/100, minor%100))
}

var decimalAmount = regexp.MustCompile(`^(\d+)(?:\.(\d+))?$`)

// paise reads a rupee amount Cashfree sent back without passing it through a
// float. More than two decimals is refused unless the extra digits are zeros,
// because a fraction of a paisa is not an amount the engine can hold.
func paise(n json.Number) (int64, error) {
	match := decimalAmount.FindStringSubmatch(strings.TrimSpace(string(n)))
	if match == nil {
		return 0, fmt.Errorf("%q is not a rupee amount", string(n))
	}
	fraction := strings.TrimRight(match[2], "0")
	if len(fraction) > 2 {
		return 0, fmt.Errorf("%q has a fraction of a paisa", string(n))
	}
	whole, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0, err
	}
	fraction += strings.Repeat("0", 2-len(fraction))
	part, _ := strconv.ParseInt(fraction, 10, 64)
	return whole*100 + part, nil
}

// flexID is cf_payment_id, which Cashfree's reference prints as a string in
// some samples and a number in others. Either way it is kept as its digits.
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*f = ""
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*f = flexID(v)
		return nil
	}
	*f = flexID(s)
	return nil
}

var notOrderIDChar = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// newOrderID is the Cashfree order id for one attempt at paying an order: the
// order number, readable in Cashfree's dashboard, and a random suffix, so a
// retry and a second store on the same account never collide. Cashfree takes
// 3 to 45 letters, digits, hyphens and underscores.
func newOrderID(number string) (string, error) {
	suffix, err := randomHex(4)
	if err != nil {
		return "", err
	}
	base := strings.Trim(notOrderIDChar.ReplaceAllString(number, "-"), "-")
	if len(base) > 36 {
		base = base[:36]
	}
	if base == "" {
		base = "order"
	}
	return base + "_" + suffix, nil
}

// customerID is the shopper as Cashfree's customer_id: letters and digits
// only, the same for one email address so Cashfree can offer its saved
// methods again, and saying nothing about the address itself.
func customerID(order *gocommerce.Order) string {
	email := strings.ToLower(strings.TrimSpace(order.Email))
	if email == "" {
		return "gcorder" + strconv.FormatInt(order.ID, 10)
	}
	sum := sha256.Sum256([]byte(email))
	return "gc" + hex.EncodeToString(sum[:12])
}

// cashfreePhone is a phone number as Cashfree takes it: an Indian number as
// its ten digits, any other with its plus and country code. Empty when there
// is nothing that could be one.
func cashfreePhone(raw string) string {
	raw = strings.TrimSpace(raw)
	var digits strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	d := digits.String()
	switch {
	case len(d) == 10:
		return d
	case len(d) == 12 && strings.HasPrefix(d, "91"):
		return d[2:]
	case len(d) == 11 && strings.HasPrefix(d, "0"):
		return d[1:]
	case strings.HasPrefix(raw, "+") && len(d) >= 8:
		return "+" + d
	}
	return ""
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("cashfree: could not make an id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
