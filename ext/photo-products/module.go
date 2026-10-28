// Package photoproducts creates draft products from a shop's own photographs.
//
// The shop drops a batch of photos on the products screen. The panel stores
// each one in the media library as it is, and sends two small copies here:
// a thumbnail for sorting and a larger picture for describing. Claude, through
// the store's AI gateway, then does two kinds of call:
//
//   - one call with every thumbnail, which sorts the photos into products:
//     several angles of one item are one variant's pictures, the same item in
//     other colours or finishes are that product's variants, and unrelated
//     items are separate products;
//   - one call per product, with up to six of its larger pictures, which
//     writes the title, description, highlights, type, tags, category, SEO
//     copy and alt text from what is visible.
//
// The shop reviews the proposal, corrects it, and presses Create. Every
// product is created through the catalogue service as a draft with every
// variant at price 0 and nothing in stock, so nothing an AI wrote reaches a
// shopper until the shop has priced and published it.
//
// # The key, the cap and the data
//
// The calls are the gateway's tasks, so they are held to the store's monthly
// cap, recorded with their cost, and sent only if the store lets documents
// (pictures) leave it. The key is the gateway's: Settings › Plugins › AI
// gateway. This module holds no key of its own.
//
//	gw := aigateway.New(aigateway.Config{})
//	app, err := gocommerce.New(cfg, gw, photoproducts.New(photoproducts.Config{Gateway: gw}))
package photoproducts

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"

	gocommerce "github.com/itswadesh/gocommerce/core"
	aigateway "github.com/itswadesh/gocommerce/ext/ai-gateway"
)

// Limits. A batch is bounded so its cost is: at most MaxPhotos thumbnails in
// one call and at most one call per product, so 1 + MaxPhotos calls.
const (
	// MaxPhotos is the most photos one batch takes.
	MaxPhotos = 60
	// photosPerProduct is how many pictures the describing call sees. Six
	// angles say what an item looks like; more only cost more.
	photosPerProduct = 6
	// describers is how many products are described at once: enough that 60
	// photos finish in a few minutes, few enough to stay under a provider's
	// rate limit on a new key.
	describers = 3
	// The bytes accepted for each copy. The panel sends a 512 px thumbnail and
	// a 1024 px picture as JPEG, which come to far less; the bound keeps the
	// sorting call, which carries every thumbnail, under the provider's
	// request size whatever a client sends.
	maxThumbBytes = 150 << 10
	maxImageBytes = 600 << 10
)

// Config configures the module.
type Config struct {
	// Gateway is the store's AI gateway. Required: it is the only way this
	// module calls a model.
	Gateway *aigateway.Module
}

// Module creates draft products from photos.
type Module struct {
	cfg Config
	app *gocommerce.App
	db  *sql.DB
	log *slog.Logger

	// ctx outlives requests: a batch keeps working after the request that
	// started it has answered.
	ctx    context.Context
	cancel context.CancelFunc
	// running is the batches this process is working on, so a second start
	// of the same batch does not run it twice.
	mu      sync.Mutex
	running map[int64]bool
	wg      sync.WaitGroup

	tasks sync.Once
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg, running: map[int64]bool{}} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "photo-products" }

// Migrations implements gocommerce.Module.
//
// Three tables: the batch, its photos with the two small copies the model is
// shown, and the proposed products. The copies are the job's input, so a
// batch survives a restart and picks up where it stopped; they are cleared
// once every product is created.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_batches",
		SQL: `
CREATE TABLE photo_products_batches (
    id               bigserial PRIMARY KEY,
    status           text NOT NULL DEFAULT 'uploading'
                     CHECK (status IN ('uploading', 'grouping', 'describing', 'review', 'creating', 'done', 'failed')),
    -- The sizes the shop said these come in. Sizes are not visible in a
    -- photo, so the model is never asked for them.
    sizes            jsonb NOT NULL DEFAULT '[]',
    step_done        integer NOT NULL DEFAULT 0,
    step_total       integer NOT NULL DEFAULT 0,
    message          text NOT NULL DEFAULT '',
    -- What the model calls cost, in millionths of a minor unit, as the
    -- gateway reports it.
    cost_micro_minor bigint NOT NULL DEFAULT 0,
    currency         text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX photo_products_batches_created ON photo_products_batches (id DESC);

CREATE TABLE photo_products_photos (
    id         bigserial PRIMARY KEY,
    batch_id   bigint NOT NULL REFERENCES photo_products_batches (id) ON DELETE CASCADE,
    media_id   bigint NOT NULL,
    url        text NOT NULL DEFAULT '',
    thumb      bytea,
    image      bytea,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (batch_id, media_id)
);

CREATE TABLE photo_products_items (
    id         bigserial PRIMARY KEY,
    batch_id   bigint NOT NULL REFERENCES photo_products_batches (id) ON DELETE CASCADE,
    position   integer NOT NULL DEFAULT 0,
    draft      jsonb NOT NULL,
    described  boolean NOT NULL DEFAULT false,
    error      text NOT NULL DEFAULT '',
    product_id bigint
);
CREATE INDEX photo_products_items_batch ON photo_products_items (batch_id, position);`,
	},
		gocommerce.ScopeMigration("0002_store_scope", "photo_products_batches", "photo_products_photos", "photo_products_items"),
	}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app, m.db, m.log = app, app.DB(), app.Log()
	if m.cfg.Gateway == nil {
		// A binary composed without the gateway has no way to call a model;
		// the screen says so instead of the binary refusing to start.
		m.log.Warn("photo-products is installed without the AI gateway, so batches cannot run")
	} else {
		var err error
		m.tasks.Do(func() {
			if err = m.cfg.Gateway.RegisterTask(groupTask(m)); err == nil {
				err = m.cfg.Gateway.RegisterTask(describeTask(m))
			}
		})
		if err != nil {
			return err
		}
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.mountRoutes(app)
	app.OnStart(func(ctx context.Context) error {
		return app.ForEachStore(ctx, m.resume)
	})
	app.OnStop(func(context.Context) error {
		m.cancel()
		m.wg.Wait()
		return nil
	})
	return nil
}
