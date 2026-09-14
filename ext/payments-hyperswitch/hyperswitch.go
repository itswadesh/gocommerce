// Package hyperswitch takes payments through Hyperswitch.
//
//	app, err := gocommerce.New(cfg,
//	    hyperswitch.New(hyperswitch.Config{
//	        APIKey:          os.Getenv("HYPERSWITCH_API_KEY"),
//	        ResponseHashKey: os.Getenv("HYPERSWITCH_RESPONSE_HASH_KEY"),
//	        BaseURL:         "https://sandbox.hyperswitch.io",
//	    }),
//	)
//
// Hyperswitch then calls POST /api/checkout/hyperswitch/webhook, a route the
// engine owns and hands to this module with the body untouched.
//
// REST over net/http and no SDK, per rule 2.
//
// # What Hyperswitch is, and why that matters here
//
// Hyperswitch is not a gateway. It is a router in front of gateways — Stripe,
// Adyen, Checkout.com, a local acquirer — and which one takes a given payment
// is decided by rules in the Hyperswitch dashboard, not by this module. That is
// the whole point of installing it, and it is also why this module is thinner
// than ext/payments-stripe: connector choice, retries and 3DS behaviour are
// configuration over there, and nothing here should try to second-guess them.
//
// The engine still owns the order. Hyperswitch says money arrived; the engine
// decides what that does to the order, in one place, as it does for every other
// provider.
//
// # Payment links rather than the SDK
//
// A payment is created with `payment_link: true`, which makes Hyperswitch host
// the page and hands back a URL to send the shopper to. The alternative is
// Hyperswitch's web SDK driven from a client_secret, which is a better checkout
// and a worse fit for a module that may add no JavaScript to anybody's
// storefront. A store that wants the SDK has the client_secret in the intent's
// client data and can use it.
//
// # Self-hosted deployments
//
// BaseURL defaults to Hyperswitch's hosted production API. A self-hosted
// deployment sets it to its own router, and the sandbox is
// https://sandbox.hyperswitch.io — there is no test/live switch in this module
// because in Hyperswitch that distinction is which host you talk to.
package hyperswitch

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
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
	"time"

	gocommerce "github.com/misiki/gocommerce/core"
)

const (
	defaultBaseURL  = "https://api.hyperswitch.io"
	maxWebhookBytes = 1 << 20
)

// Config configures the module.
type Config struct {
	// APIKey is the merchant API key, sent as the `api-key` header. Required.
	APIKey string
	// ResponseHashKey is the business profile's payment response hash key,
	// which is what signs outgoing webhooks. Required: without it any caller
	// could mark orders paid.
	ResponseHashKey string
	// ProfileID names the business profile to charge against. Optional, and
	// only mandatory in Hyperswitch itself when the merchant has more than one
	// profile — in which case leaving it empty is a 400 at the first checkout.
	ProfileID string
	// ReturnURL is where the shopper lands after paying, when the checkout
	// request did not carry one of its own. Hyperswitch requires a return URL
	// for a payment link, so one of the two has to be set.
	ReturnURL string
	// SessionExpirySeconds bounds how long the hosted link stays payable.
	// Zero leaves Hyperswitch's own default alone.
	SessionExpirySeconds int
	// BaseURL overrides the endpoint: the sandbox, or a self-hosted router.
	BaseURL string
	// Client overrides the HTTP client.
	Client *http.Client
}

// Module is the Hyperswitch payment provider.
type Module struct {
	cfg    Config
	client *http.Client
	log    *slog.Logger
	db     *sql.DB
	pay    *gocommerce.Payments
}

// New builds the module. Register it with gocommerce.New.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "payments-hyperswitch" }

// Migrations implements gocommerce.Module.
//
// One table, to make webhook handling idempotent: Hyperswitch retries with the
// same event_id, and settling an order twice must be impossible rather than
// merely unlikely.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_events",
		SQL: `
			CREATE TABLE payments_hyperswitch_events (
			    id          text        PRIMARY KEY,
			    type        text        NOT NULL,
			    order_id    bigint,
			    received_at timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX payments_hyperswitch_events_received_idx
			    ON payments_hyperswitch_events (received_at);`,
	}}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	if strings.TrimSpace(m.cfg.APIKey) == "" {
		return errors.New("hyperswitch: APIKey is required")
	}
	if strings.TrimSpace(m.cfg.ResponseHashKey) == "" {
		return errors.New("hyperswitch: ResponseHashKey is required — without it, anyone could mark orders paid")
	}
	if m.cfg.BaseURL == "" {
		m.cfg.BaseURL = defaultBaseURL
	}
	m.cfg.BaseURL = strings.TrimRight(m.cfg.BaseURL, "/")
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
func (m *Module) Code() string { return "hyperswitch" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "Hyperswitch" }

// Initiate creates a payment with a hosted link and sends the shopper to it.
func (m *Module) Initiate(ctx context.Context, order *gocommerce.Order, opts gocommerce.PayOptions) (gocommerce.PaymentIntent, error) {
	returnURL := opts.ReturnURL
	if returnURL == "" {
		returnURL = m.cfg.ReturnURL
	}
	if returnURL == "" {
		return gocommerce.PaymentIntent{}, errors.New(
			"hyperswitch: a return URL is required for a payment link — set Config.ReturnURL or send return_url with the checkout")
	}

	metadata := map[string]string{
		"order_id":     strconv.FormatInt(order.ID, 10),
		"order_number": order.Number,
	}
	for k, v := range opts.Data {
		// Namespaced, so client-supplied extras cannot overwrite the field the
		// webhook depends on to find the order.
		metadata["client_"+k] = v
	}

	body := map[string]any{
		// Minor units both sides, so there is nothing to round.
		"amount":       order.Total.AmountMinor,
		"currency":     strings.ToUpper(order.Total.Currency),
		"payment_link": true,
		"return_url":   returnURL,
		"description":  "Order " + order.Number,
		"metadata":     metadata,
		// Hyperswitch's own idempotency handle for the order, and what an
		// operator reconciling the dashboard against the store will search on.
		"merchant_order_reference_id": order.Number,
	}
	if order.Email != "" {
		body["email"] = order.Email
	}
	if order.Name != "" {
		body["name"] = order.Name
	}
	if m.cfg.ProfileID != "" {
		body["profile_id"] = m.cfg.ProfileID
	}
	if m.cfg.SessionExpirySeconds > 0 {
		body["session_expiry"] = m.cfg.SessionExpirySeconds
	}

	var out struct {
		PaymentID    string `json:"payment_id"`
		Status       string `json:"status"`
		ClientSecret string `json:"client_secret"`
		PaymentLink  struct {
			Link          string `json:"link"`
			PaymentLinkID string `json:"payment_link_id"`
		} `json:"payment_link"`
	}
	if err := m.do(ctx, http.MethodPost, "/payments", body, &out); err != nil {
		return gocommerce.PaymentIntent{}, err
	}
	if out.PaymentLink.Link == "" {
		return gocommerce.PaymentIntent{}, fmt.Errorf(
			"hyperswitch: payment %s came back without a link (status %s) — check that payment links are enabled on the business profile",
			out.PaymentID, out.Status)
	}

	client := map[string]string{"url": out.PaymentLink.Link}
	if out.ClientSecret != "" {
		// Carried so a storefront that would rather mount Hyperswitch's own
		// SDK than redirect has what it needs; the redirect stays the default.
		client["client_secret"] = out.ClientSecret
	}
	return gocommerce.PaymentIntent{
		Kind:       gocommerce.IntentRedirect,
		Provider:   m.Code(),
		Reference:  out.PaymentID,
		ClientData: client,
	}, nil
}

// Refund implements gocommerce.Refunder.
func (m *Module) Refund(ctx context.Context, order *gocommerce.Order, amountMinor int64) error {
	_, err := m.RefundWithReference(ctx, order, amountMinor)
	return err
}

// RefundWithReference implements gocommerce.ReferencedRefunder.
func (m *Module) RefundWithReference(ctx context.Context, order *gocommerce.Order, amountMinor int64) (string, error) {
	if order.PaymentReference == "" {
		return "", errors.New("hyperswitch: this order has no payment reference to refund")
	}

	body := map[string]any{
		"payment_id": order.PaymentReference,
		"amount":     amountMinor,
		// Instant, so the call either refunds or fails now. Scheduled refunds
		// answer "accepted" and settle later, which would have the engine write
		// a refund the gateway may yet decline.
		"refund_type": "instant",
	}

	var out struct {
		RefundID string `json:"refund_id"`
		Status   string `json:"status"`
		Error    string `json:"error_message"`
	}
	if err := m.do(ctx, http.MethodPost, "/refunds", body, &out); err != nil {
		return "", err
	}
	if strings.EqualFold(out.Status, "failed") {
		return "", fmt.Errorf("hyperswitch: refund %s failed: %s",
			out.RefundID, firstNonEmpty(out.Error, "no reason given"))
	}
	return out.RefundID, nil
}

// Webhook implements gocommerce.WebhookProvider.
func (m *Module) Webhook() http.Handler { return http.HandlerFunc(m.handleWebhook) }

func (m *Module) handleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBytes))
	if err != nil {
		http.Error(w, "could not read body", http.StatusBadRequest)
		return
	}
	if err := verifySignature(body, r.Header.Get(signatureHeader), m.cfg.ResponseHashKey); err != nil {
		m.log.Warn("rejected a Hyperswitch webhook", "error", err)
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	var event struct {
		EventID   string `json:"event_id"`
		EventType string `json:"event_type"`
		Content   struct {
			Type   string `json:"type"`
			Object struct {
				PaymentID   string            `json:"payment_id"`
				Status      string            `json:"status"`
				Amount      int64             `json:"amount"`
				Currency    string            `json:"currency"`
				Metadata    map[string]string `json:"metadata"`
				ErrorReason string            `json:"error_message"`
			} `json:"object"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "malformed event", http.StatusBadRequest)
		return
	}

	orderID, _ := strconv.ParseInt(event.Content.Object.Metadata["order_id"], 10, 64)

	// event_id is stable across Hyperswitch's retries, which is exactly what
	// makes it the right claim.
	claim := event.EventID
	if claim == "" {
		// Older routers, and any deployment with events disabled, send no id.
		// Fall back to the type plus the object so a retry is still a duplicate.
		claim = event.EventType + ":" + event.Content.Object.PaymentID
	}
	res, err := m.db.ExecContext(r.Context(), `
		INSERT INTO payments_hyperswitch_events (id, type, order_id)
		VALUES ($1, $2, nullif($3, 0))
		ON CONFLICT (id) DO NOTHING`, claim, event.EventType, orderID)
	if err != nil {
		m.log.Error("could not record the Hyperswitch event", "claim", claim, "error", err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Already handled. 200 stops the retries.
		w.WriteHeader(http.StatusOK)
		return
	}

	if orderID == 0 {
		// An event for a payment this store did not start — another merchant
		// app on the same profile, or a dashboard test. Acknowledged rather
		// than retried forever.
		w.WriteHeader(http.StatusOK)
		return
	}

	switch event.EventType {
	case "payment_succeeded", "payment_captured":
		// The engine performs the transition and writes the event. This module
		// never touches an order row itself.
		if _, err := m.pay.MarkPaid(r.Context(), orderID, event.Content.Object.PaymentID); err != nil {
			m.log.Error("could not mark the order paid", "order_id", orderID, "error", err)
			m.releaseClaim(r.Context(), claim)
			http.Error(w, "could not apply payment", http.StatusInternalServerError)
			return
		}
	case "payment_failed", "payment_cancelled", "payment_expired":
		reason := firstNonEmpty(event.Content.Object.ErrorReason, event.EventType)
		if _, err := m.pay.MarkFailed(r.Context(), orderID, reason); err != nil {
			m.log.Error("could not mark the payment failed", "order_id", orderID, "error", err)
			m.releaseClaim(r.Context(), claim)
			http.Error(w, "could not apply the failure", http.StatusInternalServerError)
			return
		}
	case "refund_succeeded":
		// Recorded by the engine when the refund was issued through it; a
		// refund started in the Hyperswitch dashboard is a reconciliation
		// problem an operator has to see, not one to paper over here.
		m.log.Info("Hyperswitch reported a refund", "order_id", orderID)
	}

	w.WriteHeader(http.StatusOK)
}

// releaseClaim un-claims an event whose handling failed, so the retry finds
// work to do instead of being told it was already handled.
func (m *Module) releaseClaim(ctx context.Context, claim string) {
	if _, err := m.db.ExecContext(ctx,
		`DELETE FROM payments_hyperswitch_events WHERE id = $1`, claim); err != nil {
		m.log.Error("could not release the event claim", "claim", claim, "error", err)
	}
}

// signatureHeader is what Hyperswitch signs its outgoing webhooks with. The
// name carries the digest size because the router will also send
// X-Webhook-Signature-256 for recipients that cannot do SHA-512; this module
// verifies the 512 header it is given rather than offering the weaker one.
const signatureHeader = "X-Webhook-Signature-512"

// verifySignature checks the X-Webhook-Signature-512 header: a hex HMAC-SHA512
// of the raw body, keyed on the business profile's payment response hash key.
//
// Like Lemon Squeezy and unlike Stripe, there is no timestamp in the signature,
// so a captured delivery stays valid for ever and the idempotency claim above
// is the only bound on a replay.
func verifySignature(body []byte, header, key string) error {
	header = strings.TrimSpace(header)
	if header == "" {
		return errors.New("missing X-Webhook-Signature-512 header")
	}
	mac := hmac.New(sha512.New, []byte(key))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	// Constant time, so a wrong signature cannot be found a byte at a time.
	// Lower-cased first: the digest is hex either way, and rejecting a correct
	// signature over letter case would be a very confusing outage.
	if !hmac.Equal([]byte(strings.ToLower(header)), []byte(want)) {
		return errors.New("X-Webhook-Signature-512 did not match")
	}
	return nil
}

// do performs one JSON request.
func (m *Module) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("hyperswitch: could not encode the request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, m.cfg.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("api-key", m.cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("hyperswitch: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxWebhookBytes))
	if err != nil {
		return fmt.Errorf("hyperswitch: reading the response to %s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 300 {
		var fail struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &fail) == nil && fail.Error.Message != "" {
			return fmt.Errorf("hyperswitch: %s %s: %s (%s)",
				method, path, fail.Error.Message, firstNonEmpty(fail.Error.Code, fail.Error.Type))
		}
		return fmt.Errorf("hyperswitch: %s %s: %s", method, path, strings.TrimSpace(string(payload)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("hyperswitch: could not read the response to %s %s: %w", method, path, err)
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
