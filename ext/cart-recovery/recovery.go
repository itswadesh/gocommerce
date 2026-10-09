// Package cartrecovery chases a basket somebody gave up on.
//
// The engine records abandonment and announces it, and deliberately stops
// there: `core/notify.go` subscribes `order.*` to delivery and nothing else,
// because when and how often to write to a shopper about a stale basket is a
// marketing decision with opt-out obligations attached, and because the first
// sweep after the abandonment migration would otherwise mail every stale cart
// in the table at once. This module is the other half of that decision — it
// owns the schedule, and the engine owns the channels.
//
// Abandonment here is not core's. Core calls a cart abandoned when its TTL
// runs out, thirty days by default, which is the right clock for retention and
// the wrong one for a reminder: a shopper who left an hour ago is the one worth
// writing to. So the module keeps its own record of each basket that has sat
// idle past a threshold the store sets — minutes, not days — and runs a
// sequence of messages against it, stops the moment the basket becomes an
// order, and credits that order to the record. Core's TTL is untouched; a cart
// goes on being alive, and revivable, for exactly as long as before.
//
// It adds no third-party dependency: the message goes out through whatever
// notifier the store installed, over `App.Notify`, in the wording the store
// edits on Notifications › Setup Email.
package cartrecovery

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Config is what the store tells this module in code. Everything here can be
// changed from the panel's automation screen as well, and the screen wins
// where both say something — the D57 rule, because a value an operator typed
// today is newer than one in a deployment file.
type Config struct {
	// StorefrontURL is where the shopper's basket lives, as an absolute base
	// URL — "https://shop.example.com". The recovery link lands on that plus
	// "/cart/<token>".
	//
	// Optional, and the absence is meaningful: with it unset (here and on the
	// screen) a message carries `cart_token` and no `recovery_url`, and a
	// template branches on that, exactly as the password-reset mail does with
	// `reset_url`. A storefront whose basket lives at some other path does the
	// same thing on purpose — it builds its own link from the token.
	StorefrontURL string
}

// Module is the recovery flow.
type Module struct {
	cfg Config
	app *gocommerce.App
	log *slog.Logger

	// tick is how often the background pass runs. A minute, because the
	// smallest threshold a store can set is a minute and a reminder that
	// arrives five minutes late reads as nothing worse than "later".
	tick time.Duration
}

// New builds the module.
func New(cfg Config) *Module {
	return &Module{cfg: cfg, tick: time.Minute}
}

func (m *Module) Name() string { return "cart-recovery" }

// Migrations owns three tables and no foreign key onto core's: a cart is
// deleted by core's retention purge on core's schedule, and a record that
// outlives it is how "this basket expired" stays answerable.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_abandonments",
		SQL: `
-- One row, ever. installed_at is the install guard: a basket that went idle
-- before this module existed is recorded, so the screen can show it, and is
-- never written to. Mailing a backlog of stale baskets the day the module is
-- switched on is the single worst thing it could do.
--
-- settings is NULL until an operator saves the automation screen, and NULL
-- reads as the defaults in code, so a default changed in a release reaches
-- every store that never touched it.
CREATE TABLE cart_recovery_settings (
    id           smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    installed_at timestamptz NOT NULL DEFAULT now(),
    settings     jsonb,
    updated_at   timestamptz,
    updated_by   text
);
INSERT INTO cart_recovery_settings (id) VALUES (1);

CREATE TABLE cart_recovery_abandonments (
    id             bigserial   PRIMARY KEY,
    -- One record per basket for its whole life. A shopper who comes back and
    -- leaves again is the same abandonment resumed, not a second one, so the
    -- list counts baskets rather than visits.
    cart_id        bigint      NOT NULL UNIQUE,
    -- checkout: an address was typed onto the basket, which on a guest
    -- storefront happens at checkout's first step. cart: no address, so the
    -- shopper never got that far — reachable only through a signed-in
    -- account's verified address, if at all.
    kind           text        NOT NULL CHECK (kind IN ('cart', 'checkout')),
    status         text        NOT NULL DEFAULT 'abandoned'
                   CHECK (status IN ('abandoned', 'scheduled', 'contacted',
                                     'recovered', 'suppressed', 'expired')),
    -- The address a message goes to: the cart's email, else its verified one.
    email          text,
    currency       text        NOT NULL,
    item_count     integer     NOT NULL,
    subtotal_minor bigint      NOT NULL,
    -- What was in the basket when it was given up on, at the prices it held
    -- then. The detail screen sets it beside the basket as it is now, which
    -- is how an operator sees a price that moved or a line that sold out.
    lines          jsonb       NOT NULL DEFAULT '[]',
    discount_code  text,
    cart_created_at timestamptz NOT NULL,
    last_active_at timestamptz NOT NULL,
    abandoned_at   timestamptz NOT NULL DEFAULT now(),
    history        boolean     NOT NULL DEFAULT false,

    -- The sequence. steps_sent is how far the automation got; messages_sent
    -- also counts the ones an operator sent by hand.
    steps_sent     integer     NOT NULL DEFAULT 0,
    messages_sent  integer     NOT NULL DEFAULT 0,
    next_step_at   timestamptz,
    -- Set while a sender holds the step, so a save that re-plans next_step_at
    -- mid-send cannot hand the same step to a second sender.
    claimed_until  timestamptz,
    send_attempts  integer     NOT NULL DEFAULT 0,
    last_sent_at   timestamptz,
    -- Why nothing is scheduled, when nothing is: no_contact, automation_off,
    -- no_steps, before_install, nothing_purchasable, send_failed, sequence_done.
    hold_reason    text,

    -- The link a message carries. It redirects to the basket, which is what
    -- makes a click countable; it is as much a credential as the cart token it
    -- leads to, and is shown to an operator only by a POST that is recorded.
    link_token     text        NOT NULL UNIQUE,
    clicks         integer     NOT NULL DEFAULT 0,
    first_clicked_at timestamptz,

    recovered_at           timestamptz,
    recovered_order_id     bigint,
    recovered_order_number text,
    recovered_total_minor  bigint,
    -- Whether a message had gone out before the order. A shopper who came back
    -- on their own is recovered all the same; crediting the sequence with that
    -- order is how a recovery rate comes to flatter itself.
    recovered_after_message boolean,

    suppressed_at      timestamptz,
    suppression_reason text CHECK (suppression_reason IN
                       ('contacted_manually', 'requested_no_contact', 'invalid_customer',
                        'fraud_or_test', 'other')),
    suppression_note   text,
    suppressed_by      text,
    expired_at         timestamptz,
    updated_at         timestamptz NOT NULL DEFAULT now(),

    -- Recovered is terminal, and the order it names is the proof.
    CONSTRAINT cart_recovery_recovered_has_time
        CHECK ((status = 'recovered') = (recovered_at IS NOT NULL)),
    -- One-way only: a suppressed basket that is bought anyway becomes
    -- recovered and keeps the record of having been suppressed.
    CONSTRAINT cart_recovery_suppressed_has_reason
        CHECK (status <> 'suppressed' OR (suppressed_at IS NOT NULL AND suppression_reason IS NOT NULL)),
    CONSTRAINT cart_recovery_scheduled_has_time
        CHECK (status <> 'scheduled' OR next_step_at IS NOT NULL)
);

-- The sender's claim: due now, oldest first.
CREATE INDEX cart_recovery_due ON cart_recovery_abandonments (next_step_at)
    WHERE next_step_at IS NOT NULL;
CREATE INDEX cart_recovery_listing ON cart_recovery_abandonments (abandoned_at DESC, id DESC);
CREATE INDEX cart_recovery_open ON cart_recovery_abandonments (status)
    WHERE status IN ('abandoned', 'scheduled', 'contacted', 'suppressed');

-- The timeline, written by the same transaction as the change it describes.
-- actor is an operator's email, "token" for a script, or "system".
CREATE TABLE cart_recovery_events (
    id             bigserial   PRIMARY KEY,
    abandonment_id bigint      NOT NULL REFERENCES cart_recovery_abandonments (id) ON DELETE CASCADE,
    at             timestamptz NOT NULL DEFAULT now(),
    kind           text        NOT NULL,
    detail         jsonb       NOT NULL DEFAULT '{}',
    actor          text        NOT NULL DEFAULT 'system'
);
CREATE INDEX cart_recovery_events_of ON cart_recovery_events (abandonment_id, at, id);`,
	}}
}

func (m *Module) Register(app *gocommerce.App) error {
	if m.cfg.StorefrontURL != "" {
		clean, err := cleanStorefrontURL(m.cfg.StorefrontURL)
		if err != nil {
			return errors.New("cart-recovery: StorefrontURL must be an absolute base URL like https://shop.example.com")
		}
		m.cfg.StorefrontURL = clean
	}

	m.app = app
	m.log = app.Log()
	m.registerRights(app)
	m.registerTemplates(app)

	// order.created names the basket it was checked out from, which is the
	// one fact that turns "the shopper bought something" into "this basket
	// came back".
	app.Subscribe(gocommerce.EventOrderCreated, m.onOrderCreated)

	app.HandleFunc("GET /x/cart-recovery/r/{token}", m.handleClick)

	app.HandleAdminFunc("GET /api/admin/x/cart-recovery/abandonments", m.handleList, rightRead)
	app.HandleAdminFunc("GET /api/admin/x/cart-recovery/abandonments/{id}", m.handleGet, rightRead)
	app.HandleAdminFunc("POST /api/admin/x/cart-recovery/abandonments/{id}/send", m.handleSend, rightContact)
	app.HandleAdminFunc("POST /api/admin/x/cart-recovery/abandonments/{id}/link", m.handleLink, rightContact)
	app.HandleAdminFunc("POST /api/admin/x/cart-recovery/abandonments/{id}/suppress", m.handleSuppress, rightSuppress)
	app.HandleAdminFunc("GET /api/admin/x/cart-recovery/summary", m.handleSummary, rightRead)
	app.HandleAdminFunc("GET /api/admin/x/cart-recovery/analytics", m.handleAnalytics, rightRead)
	app.HandleAdminFunc("GET /api/admin/x/cart-recovery/settings", m.handleGetSettings, rightRead)
	app.HandleAdminFunc("PUT /api/admin/x/cart-recovery/settings", m.handlePutSettings, rightAutomate)

	app.OnStart(m.start)
	return nil
}

// start runs the pass on a ticker until shutdown. One pass at boot, so a store
// that was down over a step's due time catches up without waiting a minute.
func (m *Module) start(ctx context.Context) error {
	go func() {
		t := time.NewTicker(m.tick)
		defer t.Stop()
		for {
			if _, err := m.Pass(ctx); err != nil && ctx.Err() == nil {
				m.log.Error("cart-recovery: pass failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return nil
}

func cleanStorefrontURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", gocommerce.Validationf("storefront_url must be an absolute base URL like https://shop.example.com")
	}
	return strings.TrimRight(raw, "/"), nil
}

// The rights the screens are gated on (D65). Read sits with the roles that
// read carts, because a recovery record is a cart seen a step later. Writing to
// a shopper and suppressing are the actions support takes; changing what the
// store sends to everybody, unasked, is a manager's.
const (
	rightRead     gocommerce.Right = "abandonment.read"
	rightContact  gocommerce.Right = "abandonment.contact"
	rightSuppress gocommerce.Right = "abandonment.suppress"
	rightAutomate gocommerce.Right = "abandonment.automate"
)

func (m *Module) registerRights(app *gocommerce.App) {
	both := []string{gocommerce.RoleManager, gocommerce.RoleStaff}
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightRead,
		Label:   "See abandoned checkouts",
		Scope:   "Baskets left behind, what was sent about them, and the recovery figures",
		Default: both,
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightContact,
		Label:   "Send recovery messages",
		Scope:   "Sending a reminder by hand and copying a basket's recovery link",
		Default: both,
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightSuppress,
		Label:   "Suppress recovery",
		Scope:   "Stopping the reminders for one basket, with a reason",
		Default: both,
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightAutomate,
		Label:   "Change the recovery automation",
		Scope:   "Whether reminders go out, when a basket counts as abandoned, and the sequence",
		Default: []string{gocommerce.RoleManager},
	})
}
