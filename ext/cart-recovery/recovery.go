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
// It adds no third-party dependency: the message goes out through whatever
// notifier the store installed, over `App.Notify`.
package cartrecovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/misiki/gocommerce/core"
)

// Config is what the store tells this module.
type Config struct {
	// StorefrontURL is where the shopper's basket lives, as an absolute base
	// URL — "https://shop.example.com". The recovery link is that plus
	// "/cart/<token>".
	//
	// Optional, and the absence is meaningful: with it unset the notification
	// carries `cart_token` and no `recovery_url`, and a template branches on
	// that, exactly as the password-reset mail does with `reset_url`. A
	// storefront whose basket lives at some other path does the same thing on
	// purpose — it builds its own link from the token.
	StorefrontURL string
}

// Module is the recovery flow.
type Module struct {
	cfg Config
	app *gocommerce.App
	log *slog.Logger

	// startedAt is the install guard, and it is the whole reason this module is
	// safe to add to a store that has been running for a year.
	//
	// `carts.abandoned_at` is the shopper's clock, not the sweeper's: a cart
	// that expired months ago is stamped with when it expired. So the first
	// sweep after installing this module can surface thousands of baskets that
	// were given up on long before anybody decided to chase them, and mailing
	// that backlog is the single worst thing this module could do. Anything
	// abandoned before the process came up is treated as history.
	startedAt time.Time
}

// New builds the module. It starts its own clock here rather than in Register,
// so a cart that expires during startup is still counted as history.
func New(cfg Config) *Module {
	return &Module{cfg: cfg, startedAt: time.Now()}
}

func (m *Module) Name() string { return "cart-recovery" }

// Migrations returns none: this module owns no tables. What it would store —
// "did we already chase this one" — the engine already answers, because a cart
// is abandoned once and `revive` clears the stamp when the shopper comes back.
func (m *Module) Migrations() []gocommerce.Migration { return nil }

func (m *Module) Register(app *gocommerce.App) error {
	if m.cfg.StorefrontURL != "" {
		u, err := url.Parse(m.cfg.StorefrontURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return errors.New(
				"cart-recovery: StorefrontURL must be an absolute base URL like https://shop.example.com")
		}
		m.cfg.StorefrontURL = strings.TrimRight(m.cfg.StorefrontURL, "/")
	}
	if m.startedAt.IsZero() {
		m.startedAt = time.Now()
	}

	m.app = app
	m.log = app.Log()
	app.Subscribe(gocommerce.EventCartAbandoned, m.onAbandoned)
	return nil
}

// onAbandoned turns one abandoned basket into one message.
//
// Delivery is at-least-once, so this runs again on a redelivery. That is
// tolerable here and not worth a table to prevent: the cost of a duplicate is
// one repeated email, the cost of getting the de-duplication wrong is a
// shopper who is never chased at all, and a cart is abandoned once — coming
// back clears the stamp.
func (m *Module) onAbandoned(ctx context.Context, e gocommerce.Event) error {
	var ev gocommerce.CartEvent
	if err := e.Decode(&ev); err != nil {
		return fmt.Errorf("cart-recovery: decode %s payload: %w", e.Name, err)
	}

	if ev.Email == "" {
		// Nowhere to send it. Most baskets never reach an address, so this is
		// the common path rather than an error.
		return nil
	}
	if ev.AbandonedAt.Before(m.startedAt) {
		m.log.Debug("cart-recovery: skipping a cart abandoned before this module started",
			"cart", ev.CartID, "abandoned_at", ev.AbandonedAt)
		return nil
	}

	return m.app.Notify(ctx, gocommerce.Notification{
		Event:   gocommerce.EventCartAbandoned,
		Channel: gocommerce.ChannelEmail,
		To:      ev.Email,
		Data:    m.data(&ev),
	})
}

// data flattens the event for a template, in the same flat strings
// `orderNotificationData` uses: the module hands a notifier values, and the
// notifier owns the wording.
func (m *Module) data(ev *gocommerce.CartEvent) map[string]string {
	d := map[string]string{
		"cart_token":     ev.Token,
		"currency":       ev.Currency,
		"item_count":     strconv.Itoa(ev.ItemCount),
		"subtotal_minor": strconv.FormatInt(ev.SubtotalMinor, 10),
		"customer_email": ev.Email,
		"summary":        summarise(ev.Lines),
	}
	if m.cfg.StorefrontURL != "" {
		d["recovery_url"] = m.cfg.StorefrontURL + "/cart/" + ev.Token
	}
	// Carried because a basket abandoned with a code on it is worth more of the
	// shopper's attention, and the template may want to say so.
	if ev.DiscountCode != "" {
		d["discount_code"] = ev.DiscountCode
	}
	return d
}

// summarise is the one-line "2 x Blue shirt (M)" a plain template needs without
// walking the line array, matching what the order notifications already carry.
func summarise(lines []gocommerce.OrderEventLine) string {
	var b strings.Builder
	for i, l := range lines {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Itoa(l.Quantity))
		b.WriteString(" x ")
		b.WriteString(l.Title)
		if l.VariantLabel != "" {
			b.WriteString(" (")
			b.WriteString(l.VariantLabel)
			b.WriteString(")")
		}
	}
	return b.String()
}
