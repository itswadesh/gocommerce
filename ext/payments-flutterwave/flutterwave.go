// Package flutterwave takes payments through Flutterwave Standard, its hosted
// checkout, on the v3 API.
//
//	app, err := gocommerce.New(cfg,
//	    flutterwave.New(flutterwave.Config{
//	        SecretKey:  os.Getenv("FLUTTERWAVE_SECRET_KEY"),
//	        SecretHash: os.Getenv("FLUTTERWAVE_SECRET_HASH"),
//	    }),
//	)
//
// Flutterwave then calls POST /api/checkout/flutterwave/webhook, a route the
// engine owns. Its URL and secret hash are set in the Flutterwave dashboard,
// under Settings, Webhooks.
//
// REST over net/http and no SDK, per rule 2.
//
// # The webhook is not signed
//
// v3 sends the secret hash the merchant chose in a verif-hash header, as it is,
// with nothing computed over the body. So the header only says who is knocking,
// and it would say so for a body somebody else wrote. A delivery therefore
// contributes one fact, a transaction id, and everything that decides the order
// comes from GET /v3/transactions/{id}/verify.
//
// # Test and live
//
// The key decides, on one host: a key with _TEST in it reaches only test
// transactions, and a live key cannot see them. There is no sandbox switch to
// disagree with it.
//
// # Amounts
//
// Flutterwave takes and reports major units, 7500 for 7,500 naira, where the
// engine holds minor ones. The two are converted as strings of digits, using
// the engine's own count of decimals, never through a float (rule 6).
package flutterwave

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
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
	"strconv"
	"strings"
	"sync"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	// PluginKey is the plugin this module registers, where the panel keeps its
	// credentials. Config is the environment's fallback for each field; a value
	// typed into the panel wins.
	PluginKey = "payments-flutterwave"

	baseURL      = "https://api.flutterwave.com"
	maxBodyBytes = 1 << 20
	// maxAttempts caps the transactions one order may collect through retried
	// checkouts: each is one more call whenever the order is checked.
	maxAttempts = 10
	// verifyQuiet is how long the public verify route answers from the store
	// after asking Flutterwave, so a page called in a loop cannot spend the
	// merchant's API allowance.
	verifyQuiet = 15 * time.Second
)

var errNotConfigured = errors.New("flutterwave: not set up — activate and configure it under Settings")

// errNoTransaction is Flutterwave saying it has no such transaction, which is
// also how a transaction from the other mode looks.
var errNoTransaction = errors.New("flutterwave: no such transaction")

// currencies are those Flutterwave's support pages list for cards; which of them
// an account can take depends on its country. Its list also prints "IRN" for the
// Indian rupee, left out here until that is confirmed to be a working code.
// https://flutterwave.com/mu/support/general/what-are-the-currencies-accepted-on-flutterwave
var currencies = map[string]bool{
	"CAD": true, "COP": true, "EGP": true, "EUR": true, "GBP": true, "GHS": true,
	"KES": true, "NGN": true, "RWF": true, "SLL": true, "TZS": true, "UGX": true,
	"USD": true, "XAF": true, "XOF": true, "ZAR": true, "ZMW": true,
}

func supported(currency string) bool { return currencies[strings.ToUpper(currency)] }

// Config configures the module.
type Config struct {
	// SecretKey is the API secret key. Required.
	SecretKey string `plugin:"secret_key"`
	// SecretHash is the value set beside the webhook URL in the dashboard.
	// Required: without it, deliveries could not be told from anybody's.
	SecretHash string `plugin:"secret_hash"`
	// BaseURL overrides Flutterwave's host, for tests. It is not a plugin
	// setting: the key is sent wherever this points, and a store has no need to
	// move it.
	BaseURL string
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the Flutterwave payment provider.
type Module struct {
	cfg    Config
	live   sync.Map // store id → *Config: each store has its own account
	asked  sync.Map // store id + tx_ref → when the verify route last asked
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
// so a store selling in one Flutterwave lacks never shows a method that could
// only refuse the shopper.
func (m *Module) Configured(ctx context.Context) bool {
	return m.refresh(ctx) && supported(m.app.Currency(ctx))
}

// complete also turns away the public key, the likeliest thing to be pasted
// here: it can open no checkout and verify no transaction.
func complete(c *Config) bool {
	return c.SecretKey != "" && c.SecretHash != "" && !strings.HasPrefix(c.SecretKey, "FLWPUBK")
}

func finish(c *Config) {
	// A stray space from a paste would break the header and every delivery.
	c.SecretKey = strings.TrimSpace(c.SecretKey)
	c.SecretHash = strings.TrimSpace(c.SecretHash)
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if c.BaseURL == "" {
		c.BaseURL = baseURL
	}
}

// keyMode is what the key says about the store.
func keyMode(key string) string {
	if strings.Contains(key, "_TEST") {
		return "test"
	}
	return "live"
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return PluginKey }

// Migrations implements gocommerce.Module.
//
// Transactions is how a tx_ref finds the store's order, and what it asked for.
// Events makes webhook handling idempotent. Neither key is called id, so scoping
// puts the store in front of both (D83).
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_transactions_and_events",
		SQL: `
			CREATE TABLE payments_flutterwave_transactions (
			    tx_ref       text        PRIMARY KEY,
			    order_id     bigint      NOT NULL,
			    amount_minor bigint      NOT NULL,
			    currency     text        NOT NULL,
			    created_at   timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX payments_flutterwave_transactions_order_idx ON payments_flutterwave_transactions (order_id);
			CREATE TABLE payments_flutterwave_events (
			    event_id    text        PRIMARY KEY,
			    received_at timestamptz NOT NULL DEFAULT now()
			);`,
	},
		gocommerce.ScopeMigration("0002_store_scope", "payments_flutterwave_transactions", "payments_flutterwave_events"),
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
		Key: PluginKey, Title: "Flutterwave", Icon: "ri-bank-card-line", Category: "payments", DefaultEnabled: inCode,
		Home:        "/dash/settings/payments",
		Description: "Cards, bank transfer and mobile money on Flutterwave's checkout, across Africa and beyond. What an account can take depends on its country.",
		Docs:        "https://developer.flutterwave.com/docs/flutterwave-standard-1",
		Fields: []gocommerce.PluginField{
			{Key: "secret_key", Label: "Secret key", Kind: "secret", Required: !inCode,
				Help: "Settings, API Keys, in the Flutterwave dashboard. A key with _TEST in it moves no real money, though orders paid with it are marked paid; use the live key to take real payments."},
			{Key: "secret_hash", Label: "Secret hash", Kind: "secret", Required: !inCode,
				Help: "Settings, Webhooks: set the URL to this store's /api/checkout/flutterwave/webhook, choose a long random secret hash, and enter the same value here."},
		},
	})
	m.client = m.cfg.Client
	if m.client == nil {
		m.client = app.OutboundClient(20 * time.Second)
	}
	m.log, m.db, m.pay, m.orders = app.Log(), app.DB(), app.Pay(), app.Order()

	app.RegisterPayment(m)
	app.HandleFunc("POST /x/payments-flutterwave/transactions/{tx_ref}/verify", m.handleVerify)
	return nil
}

// Code implements gocommerce.PaymentProvider.
func (m *Module) Code() string { return "flutterwave" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "Flutterwave" }

// Initiate opens a hosted checkout for the order's total and sends the shopper
// to it.
func (m *Module) Initiate(ctx context.Context, order *gocommerce.Order, opts gocommerce.PayOptions) (gocommerce.PaymentIntent, error) {
	if !m.refresh(ctx) {
		return gocommerce.PaymentIntent{}, errNotConfigured
	}
	code := strings.ToUpper(order.Total.Currency)
	if !supported(code) {
		return gocommerce.PaymentIntent{}, gocommerce.Validationf("flutterwave does not handle %s", code)
	}
	if order.Total.AmountMinor <= 0 {
		return gocommerce.PaymentIntent{}, errors.New("flutterwave: there is nothing to pay")
	}
	if order.Email == "" {
		return gocommerce.PaymentIntent{}, gocommerce.Validationf("flutterwave needs the shopper's email address")
	}
	if strings.TrimSpace(opts.ReturnURL) == "" {
		return gocommerce.PaymentIntent{}, gocommerce.Validationf("flutterwave needs a return_url to send the shopper back to")
	}
	var attempts int
	if err := m.db.QueryRowContext(ctx,
		`SELECT count(*) FROM payments_flutterwave_transactions WHERE order_id = $1`, order.ID).Scan(&attempts); err != nil {
		return gocommerce.PaymentIntent{}, fmt.Errorf("flutterwave: could not count the payment attempts: %w", err)
	}
	if attempts >= maxAttempts {
		return gocommerce.PaymentIntent{}, fmt.Errorf("flutterwave: order %s has had %d payment attempts; place a new order to try again", order.Number, attempts)
	}
	txRef, err := newTxRef(order.Number)
	if err != nil {
		return gocommerce.PaymentIntent{}, err
	}
	customer := map[string]string{"email": order.Email}
	if order.Name != "" {
		customer["name"] = order.Name
	}
	var out struct {
		Link string `json:"link"`
	}
	if err := m.call(ctx, http.MethodPost, "/v3/payments", map[string]any{
		"tx_ref":       txRef,
		"amount":       major(order.Total.AmountMinor, code),
		"currency":     code,
		"redirect_url": opts.ReturnURL,
		"customer":     customer,
		// For the merchant reading Flutterwave's dashboard; nothing relies on it.
		"meta": map[string]string{"order_number": order.Number},
	}, &out); err != nil {
		return gocommerce.PaymentIntent{}, err
	}
	if out.Link == "" {
		return gocommerce.PaymentIntent{}, errors.New("flutterwave: the checkout came back without a link")
	}
	// Without this row the webhook could not find the order, so a link that
	// cannot be recorded is not handed to the shopper.
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO payments_flutterwave_transactions (tx_ref, order_id, amount_minor, currency)
		VALUES ($1, $2, $3, $4)`, txRef, order.ID, order.Total.AmountMinor, code); err != nil {
		return gocommerce.PaymentIntent{}, fmt.Errorf("flutterwave: could not record the transaction: %w", err)
	}
	return gocommerce.PaymentIntent{
		Kind: gocommerce.IntentRedirect,
		// The tx_ref, until a payment on it replaces it with the transaction id
		// a refund needs.
		Reference: txRef,
		Provider:  m.Code(),
		ClientData: map[string]string{
			"url":    out.Link,
			"tx_ref": txRef,
			"mode":   keyMode(m.conf(ctx).SecretKey),
		},
	}, nil
}

// ResumePayment implements gocommerce.PaymentResumer. A shopper coming back pays
// on the same checkout wherever it can still be paid, because a new tx_ref would
// let both be charged; a new one is made only once the first has failed, which
// moved no money. Nothing yet on Flutterwave's side means the shopper never
// finished, and the link stands.
func (m *Module) ResumePayment(ctx context.Context, order *gocommerce.Order, prior gocommerce.PaymentIntent, opts gocommerce.PayOptions) (gocommerce.PaymentIntent, error) {
	if !m.refresh(ctx) {
		return gocommerce.PaymentIntent{}, errNotConfigured
	}
	if prior.Reference == "" {
		return prior, nil
	}
	tx, err := m.byRef(ctx, prior.Reference)
	if errors.Is(err, errNoTransaction) {
		return prior, nil
	}
	if err != nil {
		return gocommerce.PaymentIntent{}, err
	}
	switch tx.Status {
	case "successful":
		// The webhook has not landed. Recording it now tells the shopper, and
		// the empty reference leaves the id this records in place.
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

// RefundWithReference implements gocommerce.ReferencedRefunder. A reference can
// be typed by hand, so the transaction is read back and must carry a tx_ref this
// module opened for this order before any money is sent.
func (m *Module) RefundWithReference(ctx context.Context, order *gocommerce.Order, amountMinor int64) (string, error) {
	if !m.refresh(ctx) {
		return "", errNotConfigured
	}
	id := strings.TrimSpace(order.PaymentReference)
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return "", fmt.Errorf("flutterwave: order %s was not settled by a Flutterwave transaction (reference %q); refund it from Flutterwave's dashboard", order.Number, id)
	}
	tx, err := m.byID(ctx, id)
	if err != nil {
		return "", err
	}
	var known bool
	if err := m.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM payments_flutterwave_transactions WHERE tx_ref = $1 AND order_id = $2)`,
		tx.TxRef, order.ID).Scan(&known); err != nil {
		return "", fmt.Errorf("flutterwave: could not read the transaction: %w", err)
	}
	if !known {
		return "", fmt.Errorf("flutterwave: transaction %s belongs to another order; refund it from Flutterwave's dashboard", id)
	}
	var out struct {
		ID     json.Number `json:"id"`
		Status string      `json:"status"`
	}
	if err := m.call(ctx, http.MethodPost, "/v3/transactions/"+url.PathEscape(id)+"/refund", map[string]any{
		"amount":   major(amountMinor, order.Total.Currency),
		"comments": "Refund for order " + order.Number,
	}, &out); err != nil {
		return "", err
	}
	if out.Status == "failed" {
		return "", fmt.Errorf("flutterwave: refund %s for order %s failed", out.ID, order.Number)
	}
	return out.ID.String(), nil
}

// ------------------------------------------------------------------ status

// transaction is the data of Flutterwave's verify endpoints.
type transaction struct {
	ID                int64       `json:"id"`
	TxRef             string      `json:"tx_ref"`
	Amount            json.Number `json:"amount"`
	ChargedAmount     json.Number `json:"charged_amount"`
	Currency          string      `json:"currency"`
	Status            string      `json:"status"`
	ProcessorResponse string      `json:"processor_response"`
}

func (m *Module) byID(ctx context.Context, id string) (transaction, error) {
	var tx transaction
	err := m.call(ctx, http.MethodGet, "/v3/transactions/"+url.PathEscape(id)+"/verify", nil, &tx)
	return tx, err
}

func (m *Module) byRef(ctx context.Context, txRef string) (transaction, error) {
	var tx transaction
	err := m.call(ctx, http.MethodGet, "/v3/transactions/verify_by_reference?tx_ref="+url.QueryEscape(txRef), nil, &tx)
	return tx, err
}

// attempt is a row of payments_flutterwave_transactions.
type attempt struct {
	txRef    string
	amount   int64
	currency string
}

// stateOf compares what Flutterwave reports with what was asked for and what the
// order totals now. Flutterwave's guide accepts paying more than was asked, and
// this does not for amount: the engine records one figure for what an order
// took, so an overpayment is for a person to look at. charged_amount may be
// higher, since it can carry a fee the shopper bore, and may never be lower.
//
// Paid and a "successful" status differing is how a caller tells a payment that
// does not match from one that is not finished.
func stateOf(tx transaction, a attempt, order *gocommerce.Order) gocommerce.PaymentState {
	switch tx.Status {
	case "successful":
		code := strings.ToUpper(a.currency)
		amount, err := minor(tx.Amount, code)
		if err != nil {
			return gocommerce.PaymentState{Detail: "successful, but Flutterwave's amount could not be read: " + err.Error()}
		}
		charged, err := minor(tx.ChargedAmount, code)
		if err != nil {
			return gocommerce.PaymentState{Detail: "successful, but Flutterwave's charged amount could not be read: " + err.Error()}
		}
		switch {
		case tx.TxRef != a.txRef:
			return gocommerce.PaymentState{Detail: fmt.Sprintf("transaction %d was made for %s, not %s", tx.ID, tx.TxRef, a.txRef)}
		case amount != a.amount || amount != order.Total.AmountMinor || charged < amount ||
			!strings.EqualFold(tx.Currency, code) || !strings.EqualFold(tx.Currency, order.Total.Currency):
			return gocommerce.PaymentState{Detail: fmt.Sprintf("successful for %s %s (charged %s), not the order's %d minor units of %s",
				tx.Amount, tx.Currency, tx.ChargedAmount, order.Total.AmountMinor, strings.ToUpper(order.Total.Currency))}
		}
		return gocommerce.PaymentState{Paid: true, Reference: strconv.FormatInt(tx.ID, 10), Detail: tx.Status,
			Amount: &gocommerce.Money{AmountMinor: amount, Currency: code}}
	case "failed":
		return gocommerce.PaymentState{Failed: true, Detail: strings.TrimRight("failed: "+tx.ProcessorResponse, ": ")}
	}
	return gocommerce.PaymentState{Detail: tx.Status}
}

// Status implements gocommerce.PaymentStatusReader: it asks Flutterwave about
// each of the order's tx_refs, for the order whose webhook never came and for
// the shopper's return. A declined attempt is reported only when it is the
// latest, since the one after it may have been paid.
func (m *Module) Status(ctx context.Context, order *gocommerce.Order) (gocommerce.PaymentState, error) {
	if !m.refresh(ctx) {
		return gocommerce.PaymentState{}, errNotConfigured
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT tx_ref, amount_minor, currency FROM payments_flutterwave_transactions
		WHERE order_id = $1 ORDER BY created_at DESC, tx_ref`, order.ID)
	if err != nil {
		return gocommerce.PaymentState{}, fmt.Errorf("flutterwave: could not read the transactions: %w", err)
	}
	defer rows.Close()
	var attempts []attempt
	for rows.Next() {
		var a attempt
		if err := rows.Scan(&a.txRef, &a.amount, &a.currency); err != nil {
			return gocommerce.PaymentState{}, err
		}
		attempts = append(attempts, a)
	}
	if err := rows.Err(); err != nil {
		return gocommerce.PaymentState{}, err
	}
	if len(attempts) == 0 {
		return gocommerce.PaymentState{}, fmt.Errorf("flutterwave: order %s has no Flutterwave transaction to ask about", order.Number)
	}
	latest := gocommerce.PaymentState{Detail: "no payment attempted yet"}
	mismatch := ""
	for i, a := range attempts {
		tx, err := m.byRef(ctx, a.txRef)
		if errors.Is(err, errNoTransaction) {
			continue
		}
		if err != nil {
			return gocommerce.PaymentState{}, err
		}
		state := stateOf(tx, a, order)
		if state.Paid {
			return state, nil
		}
		if tx.Status == "successful" && mismatch == "" {
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

// handleVerify is the storefront's return page asking for its tx_ref to be
// checked now rather than when the webhook arrives. It records only what
// Flutterwave reports, through the engine's own check, and answers with the
// order's payment status alone. The status on the return URL is not read.
//
// The tx_ref is the credential: only the shopper who was handed it has it, and
// its suffix is random.
func (m *Module) handleVerify(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !m.refresh(ctx) {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("Flutterwave is not set up"))
		return
	}
	txRef := r.PathValue("tx_ref")
	var orderID int64
	err := m.db.QueryRowContext(ctx,
		`SELECT order_id FROM payments_flutterwave_transactions WHERE tx_ref = $1`, txRef).Scan(&orderID)
	if errors.Is(err, sql.ErrNoRows) {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no such Flutterwave transaction"))
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
		gocommerce.RespondError(w, r, gocommerce.Conflictf("order %s is no longer paid through Flutterwave", order.Number))
		return
	}
	if (order.PaymentStatus == gocommerce.PaymentPending || order.PaymentStatus == gocommerce.PaymentFailed) &&
		m.mayAsk(ctx, txRef) {
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
func (m *Module) mayAsk(ctx context.Context, txRef string) bool {
	store, _ := gocommerce.StoreFrom(ctx)
	key := store + "/" + txRef
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
	// Constant time, so the hash cannot be found a byte at a time.
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("verif-hash")), []byte(m.conf(ctx).SecretHash)) != 1 {
		m.log.Warn("rejected a Flutterwave webhook with a wrong secret hash")
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}
	var ev struct {
		Event string `json:"event"`
		Data  struct {
			ID     int64  `json:"id"`
			TxRef  string `json:"tx_ref"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, "malformed event", http.StatusBadRequest)
		return
	}
	if ev.Event != "charge.completed" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if ev.Data.ID == 0 || ev.Data.TxRef == "" {
		http.Error(w, "malformed event", http.StatusBadRequest)
		return
	}
	id := strconv.FormatInt(ev.Data.ID, 10)

	a := attempt{txRef: ev.Data.TxRef}
	var orderID int64
	err = m.db.QueryRowContext(ctx, `
		SELECT order_id, amount_minor, currency FROM payments_flutterwave_transactions WHERE tx_ref = $1`,
		a.txRef).Scan(&orderID, &a.amount, &a.currency)
	if errors.Is(err, sql.ErrNoRows) {
		// Another store's on a shared account, or a payment link's. Acknowledged
		// rather than retried.
		m.log.Info("a Flutterwave payment named no tx_ref this store opened", "tx_ref", a.txRef, "transaction", id)
		w.WriteHeader(http.StatusOK)
		return
	}
	if err != nil {
		m.log.Error("could not find the Flutterwave transaction", "tx_ref", a.txRef, "error", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	// A failure and then a success can arrive for one tx_ref, so the claim is on
	// the transaction and what it says.
	claim := ev.Event + ":" + id + ":" + ev.Data.Status
	res, err := m.db.ExecContext(ctx, `
		INSERT INTO payments_flutterwave_events (event_id) VALUES ($1)
		ON CONFLICT (store_id, event_id) DO NOTHING`, claim)
	if err != nil {
		m.log.Error("could not record the Flutterwave event", "claim", claim, "error", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		w.WriteHeader(http.StatusOK) // already handled; 200 stops the retries
		return
	}
	fail := func(status int, what string, err error) {
		m.log.Error(what, "order_id", orderID, "transaction", id, "error", err)
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

	tx, err := m.byID(ctx, id)
	if errors.Is(err, errNoTransaction) {
		// Permanent, and what a test delivery to a live store looks like. Without
		// a transaction to read there is nothing to settle and nothing to retry.
		fail(http.StatusBadRequest, "Flutterwave has no such transaction", err)
		return
	}
	if err != nil {
		fail(http.StatusBadGateway, "could not ask Flutterwave about the payment", err)
		return
	}
	switch tx.Status {
	case "successful":
	case "failed":
		// An order moved to another method since has no Flutterwave attempt to fail,
		// and a transaction made for another tx_ref says nothing about this one.
		if order.PaymentProvider == m.Code() && tx.TxRef == a.txRef {
			if _, err := m.pay.MarkFailed(ctx, orderID, "flutterwave reported a failed payment: "+tx.ProcessorResponse); err != nil {
				fail(http.StatusInternalServerError, "could not apply the failure", err)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		return
	default:
		// Not finished yet. Flutterwave's retries are the way to find out.
		fail(http.StatusBadGateway, "Flutterwave does not report this payment as finished", errors.New(tx.Status))
		return
	}

	state := stateOf(tx, a, order)
	switch {
	case !state.Paid:
		m.needsPerson(ctx, order, id, "Flutterwave took a payment that does not match order "+order.Number+" ("+state.Detail+"); look at it in Flutterwave's dashboard")
	case order.Status == gocommerce.OrderCancelled:
		m.needsPerson(ctx, order, id, "Flutterwave took a payment for order "+order.Number+" after it was cancelled; refund it from Flutterwave's dashboard")
	case order.PaymentProvider != m.Code():
		m.needsPerson(ctx, order, id, "Flutterwave took a payment for order "+order.Number+" after it moved to "+order.PaymentProvider+"; refund it from Flutterwave's dashboard")
	default:
		// The engine performs the transition and writes the event. This module
		// never touches an order row itself.
		if _, err := m.pay.MarkPaid(ctx, orderID, state.Reference); err != nil {
			fail(http.StatusInternalServerError, "could not apply payment", err)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// needsPerson puts money that cannot settle its order on the store's Errors
// screen. Retrying would change nothing, so the delivery is acknowledged.
func (m *Module) needsPerson(ctx context.Context, order *gocommerce.Order, transaction, msg string) {
	m.log.Error(msg, "order_id", order.ID, "transaction", transaction)
	m.app.Errors().Capture(ctx, gocommerce.CapturedError{
		Source: "server", Message: msg, Culprit: "payments-flutterwave webhook",
		Context: map[string]any{"order": order.Number, "transaction": transaction},
	})
}

// releaseClaim un-claims an event whose handling failed, so the retry finds
// work to do instead of being told it was already handled.
func (m *Module) releaseClaim(ctx context.Context, claim string) {
	if _, err := m.db.ExecContext(ctx,
		`DELETE FROM payments_flutterwave_events WHERE event_id = $1`, claim); err != nil {
		m.log.Error("could not release the event claim", "claim", claim, "error", err)
	}
}

// ------------------------------------------------------------------- calls

// call performs one JSON request and unwraps Flutterwave's envelope into out.
func (m *Module) call(ctx context.Context, method, path string, body, out any) error {
	c := m.conf(ctx)
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("flutterwave: could not encode the request: %w", err)
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
		return fmt.Errorf("flutterwave: %s: %w", method, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return fmt.Errorf("flutterwave: reading the response to %s: %w", method, err)
	}
	var envelope struct {
		Status  string          `json:"status"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	decodeErr := json.Unmarshal(payload, &envelope)
	// Asking about a transaction Flutterwave does not have is a question it
	// answered, not a call that failed.
	if method == http.MethodGet && (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound) {
		return errNoTransaction
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || decodeErr != nil || envelope.Status != "success" {
		if envelope.Message != "" {
			return fmt.Errorf("flutterwave: %s: %d %s", method, resp.StatusCode, envelope.Message)
		}
		return fmt.Errorf("flutterwave: %s: %s", method, resp.Status)
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("flutterwave: could not read the response to %s: %w", method, err)
	}
	return nil
}

// ------------------------------------------------------------------ values

// major writes minor units as Flutterwave's major-unit amount, as a JSON number
// built from the digits: 750000 kobo is 7500 and 2550 is 25.50. A whole amount
// has no fraction, the form its reference and samples use.
func major(amount int64, currency string) json.Number {
	places := gocommerce.CurrencyExponent(currency)
	if places == 0 {
		return json.Number(strconv.FormatInt(amount, 10))
	}
	scale := int64(1)
	for range places {
		scale *= 10
	}
	if amount%scale == 0 {
		return json.Number(strconv.FormatInt(amount/scale, 10))
	}
	return json.Number(fmt.Sprintf("%d.%0*d", amount/scale, places, amount%scale))
}

var decimalAmount = regexp.MustCompile(`^(\d+)(?:\.(\d+))?$`)

// minor reads a major-unit amount Flutterwave sent back without passing it
// through a float. Digits past the currency's decimals are refused unless they
// are zeros, because the engine cannot hold a fraction of its smallest unit.
func minor(n json.Number, currency string) (int64, error) {
	match := decimalAmount.FindStringSubmatch(strings.TrimSpace(string(n)))
	if match == nil {
		return 0, fmt.Errorf("%q is not an amount", string(n))
	}
	places := gocommerce.CurrencyExponent(currency)
	fraction := strings.TrimRight(match[2], "0")
	if len(fraction) > places {
		return 0, fmt.Errorf("%q has more decimals than %s", string(n), strings.ToUpper(currency))
	}
	digits := match[1] + fraction + strings.Repeat("0", places-len(fraction))
	return strconv.ParseInt(digits, 10, 64)
}

var notRefChar = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// newTxRef is the tx_ref for one attempt at paying an order: the order number,
// readable in Flutterwave's dashboard, and a random suffix, so a retry and a
// second store on the same account never collide, and the public verify route
// cannot be walked.
func newTxRef(number string) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("flutterwave: could not make a tx_ref: %w", err)
	}
	base := strings.Trim(notRefChar.ReplaceAllString(number, "-"), "-")
	if len(base) > 36 {
		base = base[:36]
	}
	if base == "" {
		base = "order"
	}
	return base + "_" + hex.EncodeToString(b), nil
}
