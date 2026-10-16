// Package showcases is Video Showcases (D88): a store's own people showing the
// products it sells, in a video on YouTube or Facebook, beside those products.
//
// A buyer trusts a shop more when they can see it — somebody holding the ring
// they are about to buy — and a shop already films that for its social pages.
// So a showcase is a link to that video, the products it shows, and which one
// is being shown now; the storefront plays the platform's own player and sells
// the products through the ordinary cart.
//
// What it never does is host video. There is no upload, no transcoding and no
// stream: the platform carries the video, and a pasted address becomes that
// platform's own embed on the server (providers.go). Nor does it claim more
// than it knows. A livestream is "live" only because the store said so, and
// only for twelve hours after; there are no viewer counts and no badges.
package showcases

import (
	"context"
	"database/sql"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	pluginKey = "showcases"
	// MaxProducts bounds a showcase's own list: a video shows a handful of
	// things, and a page of fifty product cards beside it is a catalogue.
	MaxProducts    = 50
	maxTitle       = 200
	maxDescription = 2000
	// liveFor is how long "we're live" stands before it reads as a replay:
	// a store that forgets to say the broadcast ended must not stay live
	// for a week.
	liveFor = 12 * time.Hour
)

// Config is the module's wiring. The zero value is what a store runs.
type Config struct {
	// EventsPerMinute bounds the storefront events a store takes from one
	// address. Zero is 600.
	EventsPerMinute int
	// YouTubeOEmbedURL and HTTPClient replace YouTube's oEmbed endpoint and
	// the client that asks it, for tests.
	YouTubeOEmbedURL string
	HTTPClient       *http.Client
}

// Module is Video Showcases.
type Module struct {
	cfg    Config
	app    *gocommerce.App
	db     *sql.DB
	events *throttle
}

// New builds the module. Register it with gocommerce.New.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "showcases" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_showcases",
		SQL: `
CREATE TABLE showcases (
    id                  bigserial   PRIMARY KEY,
    slug                text        NOT NULL CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    title               text        NOT NULL CHECK (title <> ''),
    description         text        NOT NULL DEFAULT '',
    provider            text        NOT NULL CHECK (provider IN ('youtube', 'facebook')),
    -- What the store pasted, and what it is known by: YouTube's video id, or
    -- Facebook's canonical video address. The player's address is built from
    -- the reference when read, never stored.
    video_url           text        NOT NULL,
    video_ref           text        NOT NULL,
    kind                text        NOT NULL DEFAULT 'video' CHECK (kind IN ('video', 'live')),
    live_state          text        NOT NULL DEFAULT 'none'
                                    CHECK (live_state IN ('none', 'scheduled', 'live', 'ended')),
    starts_at           timestamptz,
    live_since          timestamptz,
    status              text        NOT NULL DEFAULT 'draft'
                                    CHECK (status IN ('draft', 'published', 'archived')),
    -- A collection's products, read when shown; otherwise showcase_products.
    collection_id       bigint,
    featured_product_id bigint,
    on_home             boolean     NOT NULL DEFAULT false,
    on_collection       boolean     NOT NULL DEFAULT true,
    on_products         boolean     NOT NULL DEFAULT true,
    position            integer     NOT NULL DEFAULT 0,
    published_at        timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (slug)
);

CREATE TABLE showcase_products (
    showcase_id bigint  NOT NULL REFERENCES showcases (id) ON DELETE CASCADE,
    -- Not a foreign key: a product deleted later drops out when the showcase
    -- is read, rather than taking the showcase's order with it.
    product_id  bigint  NOT NULL,
    position    integer NOT NULL,
    PRIMARY KEY (showcase_id, product_id)
);
CREATE INDEX showcase_products_product_idx ON showcase_products (product_id);

-- Counts by day, so the panel can say how a showcase did this week without
-- keeping a row per click.
CREATE TABLE showcase_stats (
    showcase_id   bigint NOT NULL REFERENCES showcases (id) ON DELETE CASCADE,
    day           date   NOT NULL,
    views         bigint NOT NULL DEFAULT 0,
    clicks        bigint NOT NULL DEFAULT 0,
    adds          bigint NOT NULL DEFAULT 0,
    orders        bigint NOT NULL DEFAULT 0,
    revenue_minor bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (showcase_id, day)
);

-- Which showcase a basket was filled from, the last one to add to it, so the
-- order it becomes can be counted there.
CREATE TABLE showcase_carts (
    cart_id     bigint      PRIMARY KEY,
    showcase_id bigint      NOT NULL REFERENCES showcases (id) ON DELETE CASCADE,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- An order counted once: order.created is delivered at least once.
CREATE TABLE showcase_orders (
    order_id    bigint      PRIMARY KEY,
    showcase_id bigint      NOT NULL REFERENCES showcases (id) ON DELETE CASCADE,
    created_at  timestamptz NOT NULL DEFAULT now()
);
`,
	},
		gocommerce.ScopeMigration("0002_store_scope",
			"showcases", "showcase_products", "showcase_stats", "showcase_carts", "showcase_orders"),
	}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.registerRights(app)
	m.app = app
	m.db = app.DB()
	m.events = newThrottle(m.cfg.EventsPerMinute)

	app.RegisterPlugin(gocommerce.PluginDef{
		Key: pluginKey, Title: "Video showcases", Category: "storefront", DefaultEnabled: true,
		Description: "Videos of the store showing what it sells — from YouTube or Facebook, never uploaded here — " +
			"beside the products they show, served at /x/showcases for the storefront to play.",
	})

	app.Subscribe(gocommerce.EventOrderCreated, m.onOrderCreated)

	app.HandleFunc("GET /x/showcases", m.handlePublicList)
	app.HandleFunc("GET /x/showcases/{slug}", m.handlePublicGet)
	app.HandleFunc("GET /x/showcases/{slug}/now", m.handlePublicNow)
	app.HandleFunc("POST /x/showcases/{slug}/events", m.handleEvent)

	app.HandleAdminFunc("GET /api/admin/x/showcases", m.handleList, rightRead)
	app.HandleAdminFunc("POST /api/admin/x/showcases", m.handleCreate, rightWrite)
	app.HandleAdminFunc("POST /api/admin/x/showcases/check", m.handleCheck, rightWrite)
	app.HandleAdminFunc("GET /api/admin/x/showcases/{id}", m.handleGet, rightRead)
	app.HandleAdminFunc("PATCH /api/admin/x/showcases/{id}", m.handleUpdate, rightWrite)
	app.HandleAdminFunc("DELETE /api/admin/x/showcases/{id}", m.handleDelete, rightWrite)
	// Making a collection is the collections screen's right as well: a role
	// that cannot create one at /api/admin/collections cannot create one
	// here either.
	app.HandleAdminFunc("POST /api/admin/x/showcases/{id}/collection", m.handleMakeCollection,
		rightWrite, gocommerce.RightCollectionsWrite)
	return nil
}

func (m *Module) enabled(ctx context.Context) bool {
	on, _ := m.app.Plugins().Enabled(ctx, pluginKey)
	return on
}

// The rights the screens are gated on. A showcase is storefront content, so
// those who keep the store's pages keep these: managers write, staff read.
const (
	rightRead  gocommerce.Right = "showcases.read"
	rightWrite gocommerce.Right = "showcases.write"
)

func (m *Module) registerRights(app *gocommerce.App) {
	app.RegisterRight(gocommerce.RightSpec{
		Right: rightRead, Label: "See video showcases",
		Scope:   "The videos the storefront plays, their products and how they did",
		Default: []string{gocommerce.RoleManager, gocommerce.RoleStaff},
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right: rightWrite, Label: "Edit video showcases",
		Scope:   "Making, publishing and running showcases, and which product is shown now",
		Default: []string{gocommerce.RoleManager},
	})
}

// slugOf is a title as an address: "Diwali Gold, 2026!" is "diwali-gold-2026".
// Letters outside ASCII are dropped rather than transliterated — a store that
// titles a showcase in Hindi gets "showcase" and can type its own.
func slugOf(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	if s == "" {
		return "showcase"
	}
	return s
}

var slugRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
