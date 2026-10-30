// Package metacapi tells Meta about this store's purchases from the server,
// through the Conversions API, so a sale the browser pixel missed — the tab
// closed on the payment page, the pixel's request dropped — still reaches
// the store's ad account (D160).
//
// Each purchase goes with the order number as its event_id. The storefront's
// pixel fires its own Purchase with the same number as its eventID, and Meta
// keeps one of the two, so a sale both saw is counted once.
//
// Only a website purchase is sent: an order whose metadata carries the
// browser block the storefront's connector attaches when Meta's pixel ran in
// the shopper's browser — its _fbp and _fbc cookies, the user agent and the
// page. The pixel runs only where the shopper allowed ad cookies, so the
// server sends exactly the sales the browser was allowed to; an order placed
// by staff, through the API, or with the pixel refused or blocked is not a
// website event Meta could take anyway (the user agent and the page are
// required for one).
//
// The shopper's address and number are hashed here, as Meta requires, before
// anything is stored. Delivery is the outbox's shape one layer out, like
// ext/webhooks (D51): the subscriber writes a row and returns, and a worker
// sends, backs off and gives up visibly, so Meta being down never holds up a
// sale or the outbox.
package metacapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	pluginKey   = "meta-capi"
	pixelPlugin = "meta-pixel"
	// MetadataKey is where on an order the storefront leaves the browser
	// block: {fbp, fbc, client_user_agent, event_source_url}.
	MetadataKey = "meta_capi"

	defaultBaseURL = "https://graph.facebook.com"
	// graphVersion is the Graph API this module was written against; Meta
	// keeps a version working for two years after the next one ships.
	graphVersion = "v26.0"

	maxAttempts    = 12
	maxBackoff     = 15 * time.Minute
	visibility     = 60 * time.Second
	deliverBatch   = 20
	pollInterval   = 5 * time.Second
	defaultTimeout = 10 * time.Second
)

// Config is what the store's main() can set; everything a store chooses is on
// the Plugins screen.
type Config struct {
	// BaseURL overrides Meta's Graph API, for tests.
	BaseURL string
	// Client overrides the HTTP client.
	Client *http.Client
}

// Status is what the admin route reports.
type Status struct {
	Enabled    bool `json:"enabled"`
	Configured bool `json:"configured"`
	// TestMode is a test event code being set: Meta shows the events under
	// Test events and leaves them out of the ad account's reports.
	TestMode  bool       `json:"test_mode"`
	Pending   int        `json:"pending"`
	Delivered int        `json:"delivered"`
	Dead      int        `json:"dead"`
	LastSent  *time.Time `json:"last_sent,omitempty"`
	LastError string     `json:"last_error,omitempty"`
}

// Module is the sender.
type Module struct {
	cfg    Config
	app    *gocommerce.App
	db     *sql.DB
	log    *slog.Logger
	client *http.Client
	poll   time.Duration
	wake   chan struct{}
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg, wake: make(chan struct{}, 1)} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "meta-capi" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_events",
		SQL: `
-- One row per event, holding it exactly as Meta will be sent it, the shopper's
-- address and number already hashed. The columns after payload are the
-- worker's, by the outbox's names: claim a batch, back off, give up visibly.
CREATE TABLE meta_capi_events (
    id           bigserial PRIMARY KEY,
    event_name   text NOT NULL,
    event_id     text NOT NULL,
    order_id     bigint NOT NULL,
    payload      jsonb NOT NULL,
    attempts     integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    dead         boolean NOT NULL DEFAULT false,
    last_status  integer,
    last_error   text,
    created_at   timestamptz NOT NULL DEFAULT now(),

    -- What Meta deduplicates on. A cash order paid on the doorstep, or the
    -- outbox delivering an event twice, must not be a second purchase.
    UNIQUE (event_name, event_id)
);

CREATE INDEX meta_capi_events_claim
    ON meta_capi_events (available_at)
    WHERE delivered_at IS NULL AND NOT dead;`,
	},
		gocommerce.ScopeMigration("0002_store_scope", "meta_capi_events"),
		// The browser block is the shopper's own: their browser and the ad
		// they came from.
		gocommerce.WithholdFromReports("0003_withhold_from_reports", "meta_capi_events.payload"),
	}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	m.db = app.DB()
	m.log = app.Log()
	if m.cfg.BaseURL == "" {
		m.cfg.BaseURL = defaultBaseURL
	}
	m.cfg.BaseURL = strings.TrimRight(m.cfg.BaseURL, "/")
	// Meta's address is this module's, not one a store typed, so the plain
	// client is right (rule 16).
	m.client = m.cfg.Client
	if m.client == nil {
		m.client = &http.Client{Timeout: defaultTimeout}
	}
	m.poll = pollInterval

	app.RegisterPlugin(gocommerce.PluginDef{
		Key: pluginKey, Title: "Meta Conversions API", Icon: "ri-meta-line", Category: "analytics",
		Description: "Sends each purchase to Meta from the server too, so a sale the browser pixel missed still reaches your ad account. Uses the pixel ID set on Meta pixel; Meta counts a sale both saw once.",
		Docs:        "https://developers.facebook.com/docs/marketing-api/conversions-api/get-started",
		Fields: []gocommerce.PluginField{
			{Key: "access_token", Label: "Access token", Kind: "secret", Required: true,
				Help: "Events Manager › your pixel › Settings › Conversions API › Generate access token."},
			{Key: "test_event_code", Label: "Test event code", Kind: "text",
				Help: "From Events Manager › Test events, while checking the setup. Clear it afterwards: test events are left out of your reports."},
		},
	})
	app.Subscribe(gocommerce.EventOrderCreated, m.onOrder)
	app.Subscribe(gocommerce.EventOrderPaid, m.onOrder)
	app.HandleAdminFunc("GET /api/admin/x/meta-capi/status", m.handleStatus, gocommerce.RightStoreOperate)
	app.OnStart(m.start)
	return nil
}

func (m *Module) enabled(ctx context.Context) bool {
	on, err := m.app.Plugins().Enabled(ctx, pluginKey)
	return err == nil && on
}

// account is where a pass sends: the pixel's ID is Meta pixel's, so the two
// can never name different pixels and fail to deduplicate. With Meta pixel
// switched off nothing is sent: the server sends what the browser was
// allowed to, and a store that turned the pixel off has said not to.
type account struct {
	pixel, token, testCode string
	pixelOn                bool
}

func (m *Module) account(ctx context.Context) account {
	p := m.app.Plugins()
	settings, _ := p.Settings(ctx, pluginKey)
	str := func(k string) string { s, _ := settings[k].(string); return strings.TrimSpace(s) }
	on, err := p.Enabled(ctx, pixelPlugin)
	return account{
		pixel:    strings.TrimSpace(p.String(ctx, pixelPlugin, "pixel_id")),
		token:    str("access_token"),
		testCode: str("test_event_code"),
		pixelOn:  err == nil && on,
	}
}

func (a account) ready() bool { return a.pixelOn && a.pixel != "" && a.token != "" }

// ------------------------------------------------------------------ events

// onOrder queues the purchase an order event is, if it is one, and returns.
//
// A purchase is a sale the shop will be paid for: an order when it is paid,
// or a cash-on-delivery order when it is placed, since that is confirmed
// then. A cash order's payment on the doorstep is never one: it would be a
// second purchase days after the pixel's, outside the two days Meta matches
// the two in, and for an order placed before this was switched on it would
// be a sale counted on the wrong day. The only error returned is a database
// one, where a second try is right; anything else would ask the outbox to run
// every other consumer of the event again (D51).
func (m *Module) onOrder(ctx context.Context, e gocommerce.Event) error {
	var ev gocommerce.OrderEvent
	if err := e.Decode(&ev); err != nil {
		m.log.Error("meta capi: the event cannot be read", "event", e.Name, "event_id", e.ID, "error", err)
		return nil
	}
	if (e.Name == gocommerce.EventOrderCreated) != (ev.Provider == gocommerce.CodeCOD) {
		return nil
	}
	if !m.enabled(ctx) {
		return nil
	}
	order, err := m.app.Order().Get(ctx, ev.OrderID)
	if errors.Is(err, gocommerce.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	b, ok := browserOf(order.Metadata)
	if !ok {
		return nil
	}
	payload, err := json.Marshal(purchase(ev, e.At, b))
	if err != nil {
		return nil
	}
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO meta_capi_events (event_name, event_id, order_id, payload)
		VALUES ('Purchase', $1, $2, $3)
		ON CONFLICT (store_id, event_name, event_id) DO NOTHING`,
		ev.Number, ev.OrderID, payload); err != nil {
		return err
	}
	m.nudge()
	return nil
}

func (m *Module) nudge() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// browser is the block the storefront's connector puts on an order.
type browser struct {
	fbp, fbc, userAgent, sourceURL string
}

// browserOf reads the block, or says there is none worth sending. Every value
// arrived from a shopper's browser, so each is bounded and the page is
// reduced to its address without the query, which can carry anything.
func browserOf(meta gocommerce.Metadata) (browser, bool) {
	raw, _ := meta[MetadataKey].(map[string]any)
	str := func(k string) string {
		s, _ := raw[k].(string)
		s = strings.TrimSpace(s)
		if len(s) > 512 {
			return ""
		}
		return s
	}
	b := browser{fbp: str("fbp"), fbc: str("fbc"), userAgent: str("client_user_agent")}
	if !strings.HasPrefix(b.fbp, "fb.") || b.userAgent == "" {
		return browser{}, false
	}
	if b.fbc != "" && !strings.HasPrefix(b.fbc, "fb.") {
		b.fbc = ""
	}
	u, err := url.Parse(str("event_source_url"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return browser{}, false
	}
	b.sourceURL = (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
	return b, true
}

// The event, by the names in Meta's server event and customer information
// parameters.
type metaEvent struct {
	EventName      string     `json:"event_name"`
	EventTime      int64      `json:"event_time"`
	EventID        string     `json:"event_id"`
	ActionSource   string     `json:"action_source"`
	EventSourceURL string     `json:"event_source_url"`
	UserData       userData   `json:"user_data"`
	CustomData     customData `json:"custom_data"`
}

type userData struct {
	Email           []string `json:"em,omitempty"`
	Phone           []string `json:"ph,omitempty"`
	ClientUserAgent string   `json:"client_user_agent"`
	FBP             string   `json:"fbp"`
	FBC             string   `json:"fbc,omitempty"`
}

type customData struct {
	Currency    string      `json:"currency"`
	Value       json.Number `json:"value"`
	OrderID     string      `json:"order_id"`
	ContentIDs  []string    `json:"content_ids,omitempty"`
	ContentType string      `json:"content_type,omitempty"`
	Contents    []content   `json:"contents,omitempty"`
	NumItems    int         `json:"num_items"`
}

type content struct {
	ID        string      `json:"id"`
	Quantity  int         `json:"quantity"`
	ItemPrice json.Number `json:"item_price"`
}

// purchase is the order as Meta's Purchase. The value is the order's total
// in the currency's decimals, written exactly: minor units become Meta's
// decimal here and nowhere earlier (rule 6). Content ids are SKUs, which is
// what ext/feeds gives the catalogue as each item's id.
func purchase(ev gocommerce.OrderEvent, at time.Time, b browser) metaEvent {
	exp := gocommerce.CurrencyExponent(ev.Currency)
	out := metaEvent{
		EventName: "Purchase", EventTime: at.Unix(), EventID: ev.Number,
		ActionSource: "website", EventSourceURL: b.sourceURL,
		UserData: userData{ClientUserAgent: b.userAgent, FBP: b.fbp, FBC: b.fbc},
		CustomData: customData{
			Currency: ev.Currency, Value: json.Number(decimal(ev.TotalMinor, exp)), OrderID: ev.Number,
		},
	}
	if h := hashedEmail(ev.Email); h != "" {
		out.UserData.Email = []string{h}
	}
	if h := hashedPhone(ev.Phone); h != "" {
		out.UserData.Phone = []string{h}
	}
	for _, l := range ev.Lines {
		out.CustomData.NumItems += l.Quantity
		if l.SKU == "" {
			continue
		}
		out.CustomData.ContentIDs = append(out.CustomData.ContentIDs, l.SKU)
		out.CustomData.Contents = append(out.CustomData.Contents, content{
			ID: l.SKU, Quantity: l.Quantity, ItemPrice: json.Number(decimal(l.UnitPriceMinor, exp)),
		})
	}
	if len(out.CustomData.ContentIDs) > 0 {
		out.CustomData.ContentType = "product"
	}
	return out
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// hashedEmail is Meta's em: trimmed, lower-cased, SHA-256.
func hashedEmail(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	return sha256Hex(s)
}

// hashedPhone is Meta's ph: the digits with the country code, SHA-256. Only a
// number written with its country code (+ or 00) is sent: without one, the
// same digits are somebody else's number in another country, and a guess
// would match a stranger. The "(0)" some write between the country code and
// the number is a trunk prefix, not a digit of it.
func hashedPhone(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "(0)", "")
	switch {
	case strings.HasPrefix(s, "+"):
	case strings.HasPrefix(s, "00"):
		s = s[2:]
	default:
		return ""
	}
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	// E.164 is at most fifteen digits; under eight is not a whole number.
	if len(digits) < 8 || len(digits) > 15 {
		return ""
	}
	return sha256Hex(digits)
}

// decimal writes minor units as the currency's decimal: 249900 at two places
// is "2499.00".
func decimal(minor int64, exp int) string {
	if exp <= 0 {
		return strconv.FormatInt(minor, 10)
	}
	s := fmt.Sprintf("%0*d", exp+1, minor)
	return s[:len(s)-exp] + "." + s[len(s)-exp:]
}

// ---------------------------------------------------------------- delivery

type pending struct {
	id       int64
	payload  []byte
	attempts int
}

// staleAfter is the oldest event Meta takes; an older one fails the whole
// request it is in.
const staleAfter = 7 * 24 * time.Hour

// DeliverPass sends one batch of this store's events and returns how many it
// attempted. Exported for the reason the engine exports DrainOutbox: a test,
// and an operator in a hurry, can run a pass rather than wait for the ticker.
// Switched off, or missing the pixel or the token, it sends nothing, and what
// is queued waits.
func (m *Module) DeliverPass(ctx context.Context) (int, error) {
	// The cheap question first: the ticker asks it of every store every few
	// seconds, and most have nothing waiting.
	var due bool
	if err := m.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM meta_capi_events
		               WHERE delivered_at IS NULL AND NOT dead AND available_at <= now())`).Scan(&due); err != nil || !due {
		return 0, err
	}
	if !m.enabled(ctx) {
		return 0, nil
	}
	acct := m.account(ctx)
	if !acct.ready() {
		return 0, nil
	}
	// One that waited out a switched-off plugin or a broken token that long
	// is given up here rather than sent to be refused.
	if _, err := m.db.ExecContext(ctx, `
		UPDATE meta_capi_events
		SET dead = true, payload = '{}', last_error = $1
		WHERE delivered_at IS NULL AND NOT dead AND created_at < now() - make_interval(secs => $2)`,
		"older than the seven days Meta accepts an event for", staleAfter.Seconds()); err != nil {
		return 0, err
	}
	rows, err := m.db.QueryContext(ctx, `
		WITH claimed AS MATERIALIZED (
			SELECT id FROM meta_capi_events
			WHERE delivered_at IS NULL AND NOT dead AND available_at <= now()
			ORDER BY id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE meta_capi_events e
		SET attempts = e.attempts + 1,
		    available_at = now() + make_interval(secs => $2)
		FROM claimed
		WHERE e.id = claimed.id
		RETURNING e.id, e.payload, e.attempts`,
		deliverBatch, visibility.Seconds())
	if err != nil {
		return 0, fmt.Errorf("claim meta capi events: %w", err)
	}
	var batch []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.payload, &p.attempts); err != nil {
			rows.Close()
			return 0, err
		}
		batch = append(batch, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	for i, p := range batch {
		status, err := m.post(ctx, acct, p.payload)
		if err == nil {
			// Sent, the shopper's browser and hashed keys have done their job;
			// the row stays as what keeps the purchase from going twice.
			if _, err := m.db.ExecContext(ctx, `
				UPDATE meta_capi_events
				SET delivered_at = now(), payload = '{}', last_status = $2, last_error = NULL
				WHERE id = $1`, p.id, status); err != nil {
				return len(batch), err
			}
			continue
		}
		var me *metaError
		if errors.As(err, &me) && me.account() {
			return len(batch), m.holdForAccount(ctx, batch[i:], status, err)
		}
		if err := m.markFailed(ctx, p, status, err); err != nil {
			return len(batch), err
		}
	}
	return len(batch), nil
}

// metaError is Meta's refusal, in its words.
type metaError struct {
	status, code, subcode int
	message               string
	transient             bool
}

func (e *metaError) Error() string { return fmt.Sprintf("meta answered %d: %s", e.status, e.message) }

// final is a refusal the same request would get again: a 4xx Meta does not
// call transient, such as a parameter it will not take.
func (e *metaError) final() bool {
	return e.status >= 400 && e.status < 500 && e.status != http.StatusTooManyRequests && !e.transient
}

// account is a refusal of the store's setup rather than of the event: a token
// that is wrong, expired or lacks the permission (codes 190, 10 and 200 to
// 299), or a pixel that does not exist (subcode 33). Every event would be
// refused the same way until the owner fixes it, so none is given up over it.
func (e *metaError) account() bool {
	return e.status == http.StatusUnauthorized || e.status == http.StatusForbidden ||
		e.code == 190 || e.code == 10 || (e.code >= 200 && e.code <= 299) || e.subcode == 33
}

// post sends one event, as Meta's own example does: form fields in the body.
// The token goes there and never in the address, which a proxy or an error
// message would repeat.
func (m *Module) post(ctx context.Context, acct account, event []byte) (int, error) {
	form := url.Values{
		"data":         {"[" + string(event) + "]"},
		"access_token": {acct.token},
	}
	if acct.testCode != "" {
		form.Set("test_event_code", acct.testCode)
	}
	endpoint := m.cfg.BaseURL + "/" + graphVersion + "/" + url.PathEscape(acct.pixel) + "/events"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = fmt.Errorf("%s: %w", ue.Op, ue.Err)
		}
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, nil
	}
	var fail struct {
		Error struct {
			Message   string `json:"message"`
			Code      int    `json:"code"`
			Subcode   int    `json:"error_subcode"`
			Transient bool   `json:"is_transient"`
		} `json:"error"`
	}
	msg := resp.Status
	if json.Unmarshal(body, &fail) == nil && fail.Error.Message != "" {
		msg = fail.Error.Message
	}
	// Meta's words are kept and logged, and its refusal of a malformed token
	// can quote the token back.
	msg = strings.ReplaceAll(msg, acct.token, "[access token]")
	return resp.StatusCode, &metaError{status: resp.StatusCode, code: fail.Error.Code, subcode: fail.Error.Subcode,
		message: msg, transient: fail.Error.Transient}
}

// holdForAccount puts the rest of a batch back, the attempts it was charged
// returned, to wait for the owner to fix the token or the pixel. The status
// route and the log say what Meta said; an event that waits seven days is
// given up then.
func (m *Module) holdForAccount(ctx context.Context, rest []pending, status int, cause error) error {
	for _, p := range rest {
		if _, err := m.db.ExecContext(ctx, `
			UPDATE meta_capi_events
			SET attempts = attempts - 1, available_at = now() + make_interval(secs => $2),
			    last_status = nullif($3, 0), last_error = $4
			WHERE id = $1`, p.id, maxBackoff.Seconds(), status, cause.Error()); err != nil {
			return err
		}
	}
	store, _ := gocommerce.StoreFrom(ctx)
	m.log.Warn("meta capi: Meta refused the store's token or pixel; purchases wait until it is fixed",
		"store", store, "waiting", len(rest), "reason", cause.Error())
	return nil
}

// markFailed schedules the next try, or gives up: at once when Meta refused
// the event for good, after maxAttempts when it could not be reached.
func (m *Module) markFailed(ctx context.Context, p pending, status int, cause error) error {
	var me *metaError
	final := errors.As(cause, &me) && me.final()
	if final || p.attempts >= maxAttempts {
		_, err := m.db.ExecContext(ctx, `
			UPDATE meta_capi_events
			SET dead = true, payload = '{}', last_status = nullif($2, 0), last_error = $3
			WHERE id = $1`, p.id, status, cause.Error())
		if err == nil {
			store, _ := gocommerce.StoreFrom(ctx)
			m.log.Warn("meta capi gave up on an event", "store", store, "event", p.id,
				"attempts", p.attempts, "reason", cause.Error())
		}
		return err
	}
	_, err := m.db.ExecContext(ctx, `
		UPDATE meta_capi_events
		SET available_at = now() + make_interval(secs => $2),
		    last_status = nullif($3, 0), last_error = $4
		WHERE id = $1`, p.id, backoff(p.attempts).Seconds(), status, cause.Error())
	return err
}

// backoff doubles from two seconds, capped, as the outbox's does.
func backoff(attempts int) time.Duration {
	d := time.Duration(math.Pow(2, float64(attempts))) * time.Second
	if d > maxBackoff || d <= 0 {
		return maxBackoff
	}
	return d
}

// start launches the worker and returns: OnStart runs before the listener
// accepts traffic, so a hook that loops is a store that never serves.
func (m *Module) start(ctx context.Context) error {
	go m.run(ctx)
	return nil
}

func (m *Module) run(ctx context.Context) {
	t := time.NewTicker(m.poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.wake:
		}
		// One pass per store, each bound to its own (D83).
		err := m.app.ForEachStore(ctx, func(ctx context.Context) error {
			_, err := m.DeliverPass(ctx)
			return err
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			m.log.Error("meta capi pass failed", "error", err)
		}
	}
}

// ------------------------------------------------------------------ routes

func (m *Module) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	acct := m.account(ctx)
	st := Status{Enabled: m.enabled(ctx), Configured: acct.ready(), TestMode: acct.testCode != ""}
	var lastError sql.NullString
	if err := m.db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE delivered_at IS NULL AND NOT dead),
		       count(*) FILTER (WHERE delivered_at IS NOT NULL),
		       count(*) FILTER (WHERE dead),
		       max(delivered_at),
		       (SELECT last_error FROM meta_capi_events
		        WHERE last_error IS NOT NULL ORDER BY id DESC LIMIT 1)
		FROM meta_capi_events`).Scan(&st.Pending, &st.Delivered, &st.Dead, &st.LastSent, &lastError); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read the conversions api queue"))
		return
	}
	st.LastError = lastError.String
	gocommerce.Respond(w, http.StatusOK, st)
}
