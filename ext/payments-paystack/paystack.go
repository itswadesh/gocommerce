// Package paystack takes payments through Paystack's hosted checkout, for
// stores selling in naira, cedis, rand, Kenyan shillings or US dollars.
//
//	app, err := gocommerce.New(cfg,
//	    paystack.New(paystack.Config{SecretKey: os.Getenv("PAYSTACK_SECRET_KEY")}),
//	)
//
// Paystack then calls POST /api/checkout/paystack/webhook, a route the engine
// owns and hands to this module with the body untouched. Its URL is set in the
// Paystack dashboard, once for test mode and once for live mode.
//
// REST over net/http and no SDK, per rule 2.
//
// # One key, three jobs
//
// The secret key opens transactions, signs the webhooks, and says which mode
// the store is in: sk_test_ moves no real money, sk_live_ does, and both talk
// to the same host. So there is no sandbox switch to disagree with the key, and
// a transaction whose domain is not the key's mode settles nothing.
//
// # A webhook is a prompt, not proof
//
// A good signature says Paystack sent a delivery, and a captured one stays good
// for ever, because Paystack signs no timestamp. So a charge.success only makes
// this module ask GET /transaction/verify/{reference}, and that answer is what
// is compared with the order.
//
// # Amounts
//
// Paystack takes every currency it supports in hundredths, which is what the
// engine holds for those five, so the number passes through untouched. Any other
// currency is refused, because a hundredfold mistake is the failure a multiplier
// or a guess would invite.
package paystack

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha512"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	gocommerce "github.com/misiki-in/gocommerce/core"
)

const (
	// PluginKey is the plugin this module registers, where the panel keeps its
	// credentials. Config is the environment's fallback for each field; a value
	// typed into the panel wins.
	PluginKey = "payments-paystack"

	baseURL      = "https://api.paystack.co"
	maxBodyBytes = 1 << 20
	// maxAttempts caps the transactions one order may collect through retried
	// checkouts: each is one more call whenever the order is checked.
	maxAttempts = 10
	// verifyQuiet is how long the public verify route answers from the store
	// after asking Paystack, so a page called in a loop cannot spend the
	// merchant's API allowance.
	verifyQuiet = 15 * time.Second
)

var errNotConfigured = errors.New("paystack: not set up — activate and configure it under Settings")

// errNoTransaction is Paystack saying it has never heard of a reference.
var errNoTransaction = errors.New("paystack: no such transaction")

// currencies are those Paystack's API reference lists, each sent in hundredths.
// https://paystack.com/docs/api/#supported-currency
var currencies = map[string]bool{"NGN": true, "GHS": true, "ZAR": true, "KES": true, "USD": true}

func supported(currency string) bool { return currencies[strings.ToUpper(currency)] }

// Config configures the module.
type Config struct {
	// SecretKey is the sk_test_ or sk_live_ key. Required: it also signs the
	// webhooks, so without it any caller could mark orders paid.
	SecretKey string `plugin:"secret_key"`
	// BaseURL overrides Paystack's host, for tests. It is not a plugin setting:
	// the key is sent wherever this points, and a store has no need to move it.
	BaseURL string
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the Paystack payment provider.
type Module struct {
	cfg    Config
	live   sync.Map // store id → *Config: each store has its own account
	asked  sync.Map // store id + reference → when the verify route last asked
	app    *gocommerce.App
	client *http.Client
	log    *slog.Logger
	db     *sql.DB
	pay    *gocommerce.Payments
	orders *gocommerce.Orders
}

// New builds the module. Register it with gocommerce.New.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

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
// so a store selling in one Paystack lacks never shows a method that could only
// refuse the shopper.
func (m *Module) Configured(ctx context.Context) bool {
	return m.refresh(ctx) && supported(m.app.Currency(ctx))
}

// complete also turns away the public key, the likeliest thing to be pasted
// here: it can open no transaction and verify no signature.
func complete(c *Config) bool {
	return c.SecretKey != "" && !strings.HasPrefix(c.SecretKey, "pk_")
}

func finish(c *Config) {
	// A stray space from a paste would break the header and every signature.
	c.SecretKey = strings.TrimSpace(c.SecretKey)
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if c.BaseURL == "" {
		c.BaseURL = baseURL
	}
}

// keyMode is what the key says about the store: test, live, or nothing when its
// prefix is neither, in which case a transaction's domain is not compared.
func keyMode(key string) string {
	switch {
	case strings.HasPrefix(key, "sk_test_"):
		return "test"
	case strings.HasPrefix(key, "sk_live_"):
		return "live"
	}
	return ""
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return PluginKey }

// Migrations implements gocommerce.Module.
//
// Transactions is how a reference finds the store's order, and what it asked
// for. Events makes webhook handling idempotent. Neither key is called id, so
// scoping puts the store in front of both (D83).
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_transactions_and_events",
		SQL: `
			CREATE TABLE payments_paystack_transactions (
			    reference    text        PRIMARY KEY,
			    order_id     bigint      NOT NULL,
			    amount_minor bigint      NOT NULL,
			    currency     text        NOT NULL,
			    created_at   timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX payments_paystack_transactions_order_idx ON payments_paystack_transactions (order_id);
			CREATE TABLE payments_paystack_events (
			    event_id    text        PRIMARY KEY,
			    received_at timestamptz NOT NULL DEFAULT now()
			);`,
	},
		gocommerce.ScopeMigration("0002_store_scope", "payments_paystack_transactions", "payments_paystack_events"),
	}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	// Config-configured stores start switched on; a store with nothing in
	// Config starts idle and waits for the panel (D59).
	finish(&m.cfg)
	inCode := complete(&m.cfg)
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "Paystack", Icon: "ri-bank-card-line", Category: "payments", DefaultEnabled: inCode,
		Home:        "/dash/settings/payments",
		Description: "Cards, bank transfer and mobile money on Paystack's checkout, for stores selling in NGN, GHS, ZAR, KES or USD.",
		Docs:        "https://paystack.com/docs/payments/accept-payments/",
		Fields: []gocommerce.PluginField{
			{Key: "secret_key", Label: "Secret key", Kind: "secret", Required: !inCode,
				Help: "Settings, API Keys & Webhooks, in the Paystack dashboard. A sk_test_ key moves no real money, though orders paid with it are marked paid; use sk_live_ to take real payments. Set the webhook URL on the same page to this store's /api/checkout/paystack/webhook."},
		},
	})
	m.client = m.cfg.Client
	if m.client == nil {
		m.client = app.OutboundClient(20 * time.Second)
	}
	m.log, m.db, m.pay, m.orders = app.Log(), app.DB(), app.Pay(), app.Order()

	app.RegisterPayment(m)
	app.HandleFunc("POST /x/payments-paystack/transactions/{reference}/verify", m.handleVerify)
	return nil
}

// Code implements gocommerce.PaymentProvider.
func (m *Module) Code() string { return "paystack" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "Paystack" }

// Initiate opens a Paystack transaction for the order's total and sends the
// shopper to its page.
func (m *Module) Initiate(ctx context.Context, order *gocommerce.Order, opts gocommerce.PayOptions) (gocommerce.PaymentIntent, error) {
	if !m.refresh(ctx) {
		return gocommerce.PaymentIntent{}, errNotConfigured
	}
	code := strings.ToUpper(order.Total.Currency)
	if !supported(code) {
		return gocommerce.PaymentIntent{}, gocommerce.Validationf("paystack does not handle %s", code)
	}
	if order.Total.AmountMinor <= 0 {
		return gocommerce.PaymentIntent{}, errors.New("paystack: there is nothing to pay")
	}
	if order.Email == "" {
		return gocommerce.PaymentIntent{}, gocommerce.Validationf("paystack needs the shopper's email address")
	}
	if strings.TrimSpace(opts.ReturnURL) == "" {
		return gocommerce.PaymentIntent{}, gocommerce.Validationf("paystack needs a return_url to send the shopper back to")
	}
	var attempts int
	if err := m.db.QueryRowContext(ctx,
		`SELECT count(*) FROM payments_paystack_transactions WHERE order_id = $1`, order.ID).Scan(&attempts); err != nil {
		return gocommerce.PaymentIntent{}, fmt.Errorf("paystack: could not count the payment attempts: %w", err)
	}
	if attempts >= maxAttempts {
		return gocommerce.PaymentIntent{}, fmt.Errorf("paystack: order %s has had %d payment attempts; place a new order to try again", order.Number, attempts)
	}
	reference, err := newReference(order.Number)
	if err != nil {
		return gocommerce.PaymentIntent{}, err
	}
	body := map[string]any{
		"email":        order.Email,
		"amount":       order.Total.AmountMinor,
		"currency":     code,
		"reference":    reference,
		"callback_url": opts.ReturnURL,
		// For the merchant reading Paystack's dashboard; nothing relies on it.
		"metadata": map[string]string{"order_number": order.Number},
	}
	var out struct {
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := m.call(ctx, http.MethodPost, "/transaction/initialize", body, &out); err != nil {
		return gocommerce.PaymentIntent{}, err
	}
	if out.AuthorizationURL == "" {
		return gocommerce.PaymentIntent{}, errors.New("paystack: the transaction came back without a checkout link")
	}
	// Without this row the webhook could not find the order, so a link that
	// cannot be recorded is not handed to the shopper.
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO payments_paystack_transactions (reference, order_id, amount_minor, currency)
		VALUES ($1, $2, $3, $4)`, reference, order.ID, order.Total.AmountMinor, code); err != nil {
		return gocommerce.PaymentIntent{}, fmt.Errorf("paystack: could not record the transaction: %w", err)
	}
	return gocommerce.PaymentIntent{
		Kind:      gocommerce.IntentRedirect,
		Provider:  m.Code(),
		Reference: reference,
		ClientData: map[string]string{
			"url":       out.AuthorizationURL,
			"reference": reference,
			"mode":      keyMode(m.conf(ctx).SecretKey),
		},
	}, nil
}

// ResumePayment implements gocommerce.PaymentResumer. A shopper coming back pays
// on the same transaction wherever it can still be paid, because Paystack stops
// a second payment on one reference and not across two; a new one is made only
// once the first has failed, which moved no money.
func (m *Module) ResumePayment(ctx context.Context, order *gocommerce.Order, prior gocommerce.PaymentIntent, opts gocommerce.PayOptions) (gocommerce.PaymentIntent, error) {
	if !m.refresh(ctx) {
		return gocommerce.PaymentIntent{}, errNotConfigured
	}
	if prior.Reference == "" {
		return prior, nil
	}
	tx, err := m.verify(ctx, prior.Reference)
	if errors.Is(err, errNoTransaction) {
		return prior, nil
	}
	if err != nil {
		return gocommerce.PaymentIntent{}, err
	}
	switch tx.Status {
	case "success":
		// The webhook has not landed. Recording it now tells the shopper, and
		// the empty reference leaves the one this records in place.
		if _, err := m.pay.CheckPayment(ctx, order.ID); err != nil {
			return gocommerce.PaymentIntent{}, err
		}
		prior.Reference = ""
		return prior, nil
	case "failed":
		return m.Initiate(ctx, order, opts)
	}
	return prior, nil
}

// Refund implements gocommerce.Refunder.
func (m *Module) Refund(ctx context.Context, order *gocommerce.Order, amountMinor int64) error {
	_, err := m.RefundWithReference(ctx, order, amountMinor)
	return err
}

// RefundWithReference implements gocommerce.ReferencedRefunder. Paystack accepts
// the refund and processes it later, so the id it answers with means accepted,
// not paid out. A reference can be typed by hand, so only one this module opened
// for this order is sent.
func (m *Module) RefundWithReference(ctx context.Context, order *gocommerce.Order, amountMinor int64) (string, error) {
	if !m.refresh(ctx) {
		return "", errNotConfigured
	}
	reference := strings.TrimSpace(order.PaymentReference)
	var known bool
	if err := m.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM payments_paystack_transactions WHERE reference = $1 AND order_id = $2)`,
		reference, order.ID).Scan(&known); err != nil {
		return "", fmt.Errorf("paystack: could not read the transaction: %w", err)
	}
	if !known {
		return "", fmt.Errorf("paystack: order %s was not settled by a Paystack transaction this store opened (reference %q); refund it from Paystack's dashboard", order.Number, reference)
	}
	var out struct {
		ID     json.Number `json:"id"`
		Status string      `json:"status"`
	}
	if err := m.call(ctx, http.MethodPost, "/refund", map[string]any{
		"transaction":   reference,
		"amount":        amountMinor,
		"currency":      strings.ToUpper(order.Total.Currency),
		"merchant_note": "Refund for order " + order.Number,
	}, &out); err != nil {
		return "", err
	}
	if out.Status == "failed" {
		return "", fmt.Errorf("paystack: refund %s for order %s failed", out.ID, order.Number)
	}
	return out.ID.String(), nil
}

// ------------------------------------------------------------------ status

// transaction is the data of GET /transaction/verify/{reference}.
type transaction struct {
	Domain          string `json:"domain"`
	Status          string `json:"status"`
	Reference       string `json:"reference"`
	Amount          int64  `json:"amount"`
	Currency        string `json:"currency"`
	GatewayResponse string `json:"gateway_response"`
}

func (m *Module) verify(ctx context.Context, reference string) (transaction, error) {
	var tx transaction
	err := m.call(ctx, http.MethodGet, "/transaction/verify/"+url.PathEscape(reference), nil, &tx)
	return tx, err
}

// attempt is a row of payments_paystack_transactions.
type attempt struct {
	reference string
	amount    int64
	currency  string
}

// stateOf compares what Paystack reports with what was asked for and what the
// order totals now. Paid and a "success" status differing is how a caller tells
// a payment that does not match from one that is not finished.
func stateOf(tx transaction, a attempt, order *gocommerce.Order, mode string) gocommerce.PaymentState {
	switch tx.Status {
	case "success":
		switch {
		case mode != "" && tx.Domain != mode:
			return gocommerce.PaymentState{Detail: fmt.Sprintf("a %s-mode payment, and this store's key is %s mode", tx.Domain, mode)}
		case tx.Amount != a.amount || tx.Amount != order.Total.AmountMinor ||
			!strings.EqualFold(tx.Currency, a.currency) || !strings.EqualFold(tx.Currency, order.Total.Currency):
			return gocommerce.PaymentState{Detail: fmt.Sprintf("success for %d %s, not the order's %d %s",
				tx.Amount, tx.Currency, order.Total.AmountMinor, strings.ToUpper(order.Total.Currency))}
		}
		return gocommerce.PaymentState{Paid: true, Reference: a.reference, Detail: tx.Status,
			Amount: &gocommerce.Money{AmountMinor: tx.Amount, Currency: strings.ToUpper(tx.Currency)}}
	case "failed":
		return gocommerce.PaymentState{Failed: true, Detail: strings.TrimRight("failed: "+tx.GatewayResponse, ": ")}
	}
	return gocommerce.PaymentState{Detail: tx.Status}
}

// Status implements gocommerce.PaymentStatusReader: it asks Paystack about each
// of the order's transactions, for the order whose webhook never came and for
// the shopper's return. A declined attempt is reported only when it is the latest,
// since the one after it may have been paid.
func (m *Module) Status(ctx context.Context, order *gocommerce.Order) (gocommerce.PaymentState, error) {
	if !m.refresh(ctx) {
		return gocommerce.PaymentState{}, errNotConfigured
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT reference, amount_minor, currency FROM payments_paystack_transactions
		WHERE order_id = $1 ORDER BY created_at DESC, reference`, order.ID)
	if err != nil {
		return gocommerce.PaymentState{}, fmt.Errorf("paystack: could not read the transactions: %w", err)
	}
	defer rows.Close()
	var attempts []attempt
	for rows.Next() {
		var a attempt
		if err := rows.Scan(&a.reference, &a.amount, &a.currency); err != nil {
			return gocommerce.PaymentState{}, err
		}
		attempts = append(attempts, a)
	}
	if err := rows.Err(); err != nil {
		return gocommerce.PaymentState{}, err
	}
	if len(attempts) == 0 {
		return gocommerce.PaymentState{}, fmt.Errorf("paystack: order %s has no Paystack transaction to ask about", order.Number)
	}
	mode := keyMode(m.conf(ctx).SecretKey)
	latest := gocommerce.PaymentState{Detail: "no payment attempted yet"}
	mismatch := ""
	for i, a := range attempts {
		tx, err := m.verify(ctx, a.reference)
		if errors.Is(err, errNoTransaction) {
			continue
		}
		if err != nil {
			return gocommerce.PaymentState{}, err
		}
		state := stateOf(tx, a, order, mode)
		if state.Paid {
			return state, nil
		}
		if tx.Status == "success" && mismatch == "" {
			mismatch = state.Detail
		}
		if i == 0 {
			latest = state
		}
	}
	if mismatch != "" {
		return gocommerce.PaymentState{Detail: mismatch}, nil
	}
	return latest, nil
}

// handleVerify is the storefront's return page asking for its transaction to be
// checked now rather than when the webhook arrives. It records only what
// Paystack reports, through the engine's own check, and answers with the order's
// payment status alone.
//
// The reference is the credential: only the shopper who was handed it has it,
// and its suffix is random.
func (m *Module) handleVerify(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !m.refresh(ctx) {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("Paystack is not set up"))
		return
	}
	reference := r.PathValue("reference")
	var orderID int64
	err := m.db.QueryRowContext(ctx,
		`SELECT order_id FROM payments_paystack_transactions WHERE reference = $1`, reference).Scan(&orderID)
	if errors.Is(err, sql.ErrNoRows) {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no such Paystack transaction"))
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
	// An operator may have moved the order to another method since; settling
	// it is then that method's business.
	if order.PaymentProvider != m.Code() {
		gocommerce.RespondError(w, r, gocommerce.Conflictf("order %s is no longer paid through Paystack", order.Number))
		return
	}
	if (order.PaymentStatus == gocommerce.PaymentPending || order.PaymentStatus == gocommerce.PaymentFailed) &&
		m.mayAsk(ctx, reference) {
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

// mayAsk notes that the verify route is asking, once per verifyQuiet. Kept in
// memory, per process, which is all a cap on a loop needs.
func (m *Module) mayAsk(ctx context.Context, reference string) bool {
	store, _ := gocommerce.StoreFrom(ctx)
	key := store + "/" + reference
	now := time.Now()
	for {
		last, loaded := m.asked.LoadOrStore(key, now)
		if !loaded {
			return true
		}
		if now.Sub(last.(time.Time)) < verifyQuiet {
			return false
		}
		if m.asked.CompareAndSwap(key, last, now) {
			return true
		}
	}
}

// ----------------------------------------------------------------- webhook

// Webhook implements gocommerce.WebhookProvider.
func (m *Module) Webhook() http.Handler { return http.HandlerFunc(m.handleWebhook) }

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
	if !validSignature(body, r.Header.Get("x-paystack-signature"), m.conf(ctx).SecretKey) {
		m.log.Warn("rejected a Paystack webhook with an invalid signature")
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}
	var ev struct {
		Event string `json:"event"`
		Data  struct {
			Reference string `json:"reference"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, "malformed event", http.StatusBadRequest)
		return
	}
	if ev.Event != "charge.success" {
		w.WriteHeader(http.StatusOK)
		return
	}
	reference := ev.Data.Reference
	if reference == "" {
		http.Error(w, "malformed event", http.StatusBadRequest)
		return
	}

	a := attempt{reference: reference}
	var orderID int64
	err = m.db.QueryRowContext(ctx, `
		SELECT order_id, amount_minor, currency FROM payments_paystack_transactions WHERE reference = $1`,
		reference).Scan(&orderID, &a.amount, &a.currency)
	if errors.Is(err, sql.ErrNoRows) {
		// Another store's on a shared account, or a payment page's. Acknowledged
		// rather than retried for days.
		m.log.Info("a Paystack payment named no transaction this store opened", "reference", reference)
		w.WriteHeader(http.StatusOK)
		return
	}
	if err != nil {
		m.log.Error("could not find the Paystack transaction", "reference", reference, "error", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	claim := ev.Event + ":" + reference
	res, err := m.db.ExecContext(ctx, `
		INSERT INTO payments_paystack_events (event_id) VALUES ($1)
		ON CONFLICT (store_id, event_id) DO NOTHING`, claim)
	if err != nil {
		m.log.Error("could not record the Paystack event", "claim", claim, "error", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		w.WriteHeader(http.StatusOK) // already handled; 200 stops the retries
		return
	}
	fail := func(status int, what string, err error) {
		m.log.Error(what, "order_id", orderID, "reference", reference, "error", err)
		m.releaseClaim(ctx, claim)
		http.Error(w, what, status)
	}

	order, err := m.orders.Get(ctx, orderID)
	if err != nil {
		fail(http.StatusInternalServerError, "could not read the order", err)
		return
	}
	if order.PaymentStatus == gocommerce.PaymentPaid || order.PaymentStatus == gocommerce.PaymentRefunded {
		w.WriteHeader(http.StatusOK)
		return
	}

	tx, err := m.verify(ctx, reference)
	if err != nil {
		fail(http.StatusBadGateway, "could not ask Paystack about the payment", err)
		return
	}
	if tx.Status != "success" {
		// The delivery said paid and Paystack does not say so yet. Its retries
		// are the way to find out which settles.
		fail(http.StatusBadGateway, "Paystack does not report this payment as successful", errors.New(tx.Status))
		return
	}
	state := stateOf(tx, a, order, keyMode(m.conf(ctx).SecretKey))
	switch {
	case !state.Paid:
		m.needsPerson(ctx, order, reference, "Paystack took a payment that does not match order "+order.Number+" ("+state.Detail+"); look at it in Paystack's dashboard")
	case order.Status == gocommerce.OrderCancelled:
		m.needsPerson(ctx, order, reference, "Paystack took a payment for order "+order.Number+" after it was cancelled; refund it from Paystack's dashboard")
	case order.PaymentProvider != m.Code():
		m.needsPerson(ctx, order, reference, "Paystack took a payment for order "+order.Number+" after it moved to "+order.PaymentProvider+"; refund it from Paystack's dashboard")
	default:
		// The engine performs the transition and writes the event. This module
		// never touches an order row itself.
		if _, err := m.pay.MarkPaid(ctx, orderID, reference); err != nil {
			fail(http.StatusInternalServerError, "could not apply payment", err)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// needsPerson puts money that cannot settle its order on the store's Errors
// screen. Retrying would change nothing, so the delivery is acknowledged.
func (m *Module) needsPerson(ctx context.Context, order *gocommerce.Order, reference, msg string) {
	m.log.Error(msg, "order_id", order.ID, "reference", reference)
	m.app.Errors().Capture(ctx, gocommerce.CapturedError{
		Source: "server", Message: msg, Culprit: "payments-paystack webhook",
		Context: map[string]any{"order": order.Number, "reference": reference},
	})
}

// releaseClaim un-claims an event whose handling failed, so the retry finds
// work to do instead of being told it was already handled.
func (m *Module) releaseClaim(ctx context.Context, claim string) {
	if _, err := m.db.ExecContext(ctx,
		`DELETE FROM payments_paystack_events WHERE event_id = $1`, claim); err != nil {
		m.log.Error("could not release the event claim", "claim", claim, "error", err)
	}
}

// validSignature checks x-paystack-signature: the hex HMAC-SHA512 of the raw
// body, keyed on the secret key. Constant time, so a wrong signature cannot be
// found a byte at a time.
func validSignature(body []byte, signature, secret string) bool {
	got, err := hex.DecodeString(strings.TrimSpace(signature))
	if err != nil || len(got) == 0 {
		return false
	}
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// ------------------------------------------------------------------- calls

// call performs one JSON request and unwraps Paystack's envelope into out.
func (m *Module) call(ctx context.Context, method, path string, body, out any) error {
	c := m.conf(ctx)
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("paystack: could not encode the request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.SecretKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("paystack: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return fmt.Errorf("paystack: reading the response to %s %s: %w", method, path, err)
	}
	var envelope struct {
		Status  bool            `json:"status"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	decodeErr := json.Unmarshal(payload, &envelope)
	// Asking about a reference Paystack does not have is a question it answered,
	// not a call that failed.
	if method == http.MethodGet && (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound) {
		return errNoTransaction
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || decodeErr != nil || !envelope.Status {
		if envelope.Message != "" {
			return fmt.Errorf("paystack: %s %s: %d %s", method, path, resp.StatusCode, envelope.Message)
		}
		return fmt.Errorf("paystack: %s %s: %s", method, path, resp.Status)
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("paystack: could not read the response to %s %s: %w", method, path, err)
	}
	return nil
}

// ------------------------------------------------------------------ values

var notReferenceChar = regexp.MustCompile(`[^A-Za-z0-9.=-]+`)

// newReference is the reference for one attempt at paying an order: the order
// number, readable in Paystack's dashboard, and a random suffix, so a retry and
// a second store on the same account never collide, and the public verify route
// cannot be walked. Paystack allows letters, digits, -, . and =.
func newReference(number string) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("paystack: could not make a reference: %w", err)
	}
	base := strings.Trim(notReferenceChar.ReplaceAllString(number, "-"), "-")
	if len(base) > 36 {
		base = base[:36]
	}
	if base == "" {
		base = "order"
	}
	return base + "-" + hex.EncodeToString(b), nil
}
