// Package shopassistant is a shopping assistant on the storefront (D244): an
// anonymous shopper asks in their own words, and gets product cards and the
// store's own FAQ answers back.
//
// It is the one place a person nobody knows can spend a store's AI credit, so
// it is off for every store until the store turns it on, on the platform's key
// too, and each turn is counted against the store's day and the visitor's ten
// minutes. It reads and never writes: the model sees the store's facet list and
// FAQs, never a product, and every card it leads to comes from the core
// catalogue. The browser holds the conversation; the server keeps none of it.
//
//	gw := aigateway.New(aigateway.Config{})
//	fq := faq.New(faq.Config{})
//	app, err := gocommerce.New(cfg, gw, fq, shopassistant.New(shopassistant.Config{Gateway: gw, FAQ: fq}))
package shopassistant

import (
	"context"
	"database/sql"
	"net/netip"
	"sync"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	aigateway "github.com/itswadesh/gocommerce/ext/ai-gateway"
	"github.com/itswadesh/gocommerce/ext/faq"
)

// PluginKey is the switch on the panel's Plugins screen.
const PluginKey = "shop-assistant"

// The owner's limits, 2026-11-07 (D244).
const (
	turnsPerStorePerDay = 200
	turnsPerVisitor     = 30
	visitorPeriod       = 10 * time.Minute
	// maxVisitors bounds the limiter's memory. Past it a new address is refused
	// until a sweep frees room: the store's day is 200 turns, so a crowd this
	// size has spent it long before anybody real is turned away by this.
	maxVisitors = 100_000
)

// Config configures the module.
type Config struct {
	// Gateway is the store's AI gateway. Required: it is the only way this
	// module calls a model.
	Gateway *aigateway.Module
	// FAQ is where the answers come from. Nil means the assistant has none.
	FAQ *faq.Module
}

// Module is the storefront's assistant.
type Module struct {
	cfg  Config
	app  *gocommerce.App
	db   *sql.DB
	once sync.Once

	visitors *visitorLimiter
}

// New constructs the module.
func New(cfg Config) *Module {
	return &Module{cfg: cfg, visitors: newVisitorLimiter()}
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "shop-assistant" }

// Migrations implements gocommerce.Module. One row per store, overwritten when
// the day changes, as api_key_rates does for a minute: the count is only ever
// asked about today, so nothing piles up and nothing needs sweeping.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{
		{ID: "0001_daily", SQL: `
CREATE TABLE shop_assistant_daily (
    store_id text    NOT NULL PRIMARY KEY,
    day      date    NOT NULL,
    turns    integer NOT NULL CHECK (turns >= 0)
);`},
		gocommerce.ScopeMigration("0002_store_scope", "shop_assistant_daily"),
	}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	m.db = app.DB()
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "Shop assistant", Icon: "ri-chat-smile-2-line", Category: "ai",
		Description: "A chat on your storefront that finds products and answers from your FAQs. Shoppers do not sign in, and each answer spends this store's AI credit, " +
			"up to 200 a day. It replies without anybody checking first. What shoppers type is sent to the AI provider, so it also needs Share message bodies on under AI gateway.",
	})
	if m.cfg.Gateway == nil {
		app.Log().Warn("shop-assistant is installed without the AI gateway, so it can answer nothing")
	} else {
		var err error
		m.once.Do(func() { err = m.cfg.Gateway.RegisterTask(m.task()) })
		if err != nil {
			return err
		}
	}
	app.HandleFunc("GET /x/shop-assistant/status", m.handleStatus)
	app.HandleFunc("POST /x/shop-assistant/turn", m.handleTurn)
	return nil
}

// countTurn takes one of the store's turns for today, in one statement so that
// two engines cannot both take the last. The day is UTC's, so a store's day does
// not move with a setting; it turns over at 05:30 in India.
func (m *Module) countTurn(ctx context.Context) (bool, error) {
	var turns int
	err := m.db.QueryRowContext(ctx, `
		INSERT INTO shop_assistant_daily AS d (day, turns)
		VALUES ((now() AT TIME ZONE 'UTC')::date, 1)
		ON CONFLICT (store_id) DO UPDATE SET
		    day   = EXCLUDED.day,
		    turns = CASE WHEN d.day = EXCLUDED.day THEN d.turns + 1 ELSE 1 END
		WHERE d.day <> EXCLUDED.day OR d.turns < $1
		RETURNING turns`, turnsPerStorePerDay).Scan(&turns)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// visitorLimiter is a fixed window per store and address.
//
// ponytail: in memory, so a second engine doubles it and a restart resets it.
// That is its size of problem: the store's daily count is what bounds the money,
// and this only stops one visitor taking the whole day's turns. Behind a proxy
// the install has not named in GOCOMMERCE_TRUSTED_PROXIES, every shopper is one
// address and shares thirty turns.
type visitorLimiter struct {
	mu    sync.Mutex
	seen  map[visitorKey]*visitorWindow
	swept time.Time
}

type visitorKey struct {
	store string
	addr  netip.Prefix
}

type visitorWindow struct {
	start time.Time
	used  int
}

func newVisitorLimiter() *visitorLimiter {
	return &visitorLimiter{seen: map[visitorKey]*visitorWindow{}}
}

// allow counts one turn and says how long to wait when there is none left.
func (l *visitorLimiter) allow(key visitorKey, now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// A shopper's address is a stranger's to choose, unlike the signed-in person
	// Ask AI counts, so a window that has ended cannot be left in the map.
	if now.Sub(l.swept) >= time.Minute {
		for k, w := range l.seen {
			if now.Sub(w.start) >= visitorPeriod {
				delete(l.seen, k)
			}
		}
		l.swept = now
	}
	w := l.seen[key]
	if w == nil || now.Sub(w.start) >= visitorPeriod {
		if w == nil && len(l.seen) >= maxVisitors {
			return l.swept.Add(time.Minute).Sub(now), false
		}
		w = &visitorWindow{start: now}
		l.seen[key] = w
	}
	if w.used >= turnsPerVisitor {
		return visitorPeriod - now.Sub(w.start), false
	}
	w.used++
	return 0, true
}

// visitorOf is the address a turn counts against: a host, or an IPv6 address's
// /64, which is what one household or phone is given and can roam within freely.
// An unreadable address is one bucket rather than a free pass.
func visitorOf(store, remoteAddr string) visitorKey {
	addr := netip.Addr{}
	if ap, err := netip.ParseAddrPort(remoteAddr); err == nil {
		addr = ap.Addr().Unmap()
	} else if a, err := netip.ParseAddr(remoteAddr); err == nil {
		addr = a.Unmap()
	}
	bits := addr.BitLen()
	if addr.Is6() {
		bits = 64
	}
	prefix, _ := addr.Prefix(bits)
	return visitorKey{store, prefix}
}
