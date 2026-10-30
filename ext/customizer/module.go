// Package customizer lets a shopper design their own print on a product: a
// photo, words, shapes, stickers and a background colour on a phone case's
// printable area, bought as a line of its own (D159).
//
// The shop sets each product up on its page in the panel: the picture of the
// product the editor draws on (the mockup), an optional transparent picture
// drawn above the design (a camera cut-out, the case's edges), the printable
// area on the mockup, the print's real size and resolution, and which tools,
// fonts and stickers the editor offers. The storefront's editor reads that
// setup, uploads the shopper's photos and the finished preview and print
// file here, saves the design, and adds it to the basket with the variant.
// Checkout copies the design onto the order line, where the print operator
// finds it.
//
// Nothing a shopper uploads is in the shop's media library or anywhere else
// public: each file sits in the store's media storage under a random name
// that only the design and the order line carry. Uploads and designs nobody
// ordered are deleted after RetentionDays.
//
//	app, err := gocommerce.New(cfg, customizer.New(customizer.Config{}))
package customizer

import (
	"context"
	"database/sql"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	pluginKey = "customizer"

	rightRead  = "customizer.read"
	rightWrite = "customizer.write"

	// RetentionDays is how long an upload or a design is kept when no order
	// holds it. A shopper who leaves a design in a basket for a month has
	// left it; one who orders it keeps it as long as the order exists.
	RetentionDays = 30

	// DefaultConsent is the line the storefront shows before a photo is
	// chosen, until the shop writes its own.
	DefaultConsent = "Your photo is used only to print your order. Only upload photos you have the right to print."

	sweepEvery = time.Hour
)

// Config configures the module. Nothing here is required.
type Config struct {
	// UploadsPerHour is how many files one address may upload in an hour.
	// Zero means 30; a negative number turns the limit off. It counts by the
	// connection's peer address, never by X-Forwarded-For, which anybody can
	// write, so behind a reverse proxy every shopper shares the proxy's
	// allowance and the store should raise it.
	UploadsPerHour int
	// DesignsPerHour is the same for saved designs. Zero means 30.
	DesignsPerHour int
}

// Module is the routes, the tables and the sweep.
type Module struct {
	cfg     Config
	app     *gocommerce.App
	db      *sql.DB
	uploads *throttle
	designs *throttle
	decodes chan struct{}
}

// New constructs the module.
func New(cfg Config) *Module {
	if cfg.UploadsPerHour == 0 {
		cfg.UploadsPerHour = 30
	}
	if cfg.DesignsPerHour == 0 {
		cfg.DesignsPerHour = 30
	}
	return &Module{
		cfg:     cfg,
		uploads: newThrottle(cfg.UploadsPerHour, time.Hour),
		designs: newThrottle(cfg.DesignsPerHour, time.Hour),
		decodes: make(chan struct{}, decodeSlots),
	}
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "customizer" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{
		{
			ID: "0001_customizer",
			SQL: `
CREATE TABLE customizer_products (
    product_id bigint      PRIMARY KEY REFERENCES products (id) ON DELETE CASCADE,
    enabled    boolean     NOT NULL DEFAULT false,
    setup      jsonb       NOT NULL,
    stickers   jsonb       NOT NULL DEFAULT '[]',
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE customizer_uploads (
    id          text        PRIMARY KEY,
    storage_key text        NOT NULL,
    url         text        NOT NULL,
    mime        text        NOT NULL,
    width       integer     NOT NULL,
    height      integer     NOT NULL,
    size_bytes  bigint      NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX customizer_uploads_created_idx ON customizer_uploads (created_at);

-- product_id and variant_id are not foreign keys: a placed order's design
-- must keep its photos after the product is deleted, and a cascade from the
-- product would take the design and then the photos with it.
CREATE TABLE customizer_designs (
    id                text        PRIMARY KEY,
    product_id        bigint      NOT NULL,
    variant_id        bigint      NOT NULL,
    document          jsonb       NOT NULL,
    preview_upload_id text        NOT NULL REFERENCES customizer_uploads (id),
    print_upload_id   text        NOT NULL REFERENCES customizer_uploads (id),
    created_at        timestamptz NOT NULL DEFAULT now(),
    vouched_at        timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE customizer_design_uploads (
    design_id text NOT NULL REFERENCES customizer_designs (id) ON DELETE CASCADE,
    upload_id text NOT NULL REFERENCES customizer_uploads (id),
    PRIMARY KEY (design_id, upload_id)
);
CREATE INDEX customizer_design_uploads_upload_idx ON customizer_design_uploads (upload_id);
`,
		},
		gocommerce.ScopeMigration("0002_store_scope",
			"customizer_products", "customizer_uploads", "customizer_designs", "customizer_design_uploads"),
		gocommerce.WithholdFromReports("0003_withhold_from_reports",
			"customizer_uploads", "customizer_designs", "customizer_design_uploads"),
	}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	m.db = app.DB()
	app.RegisterRight(gocommerce.RightSpec{
		Right: rightRead, Label: "See product customizers",
		Scope:   "How each product's design editor is set up: its mockup, print area, print size, tools, fonts and stickers",
		Default: []string{gocommerce.RoleManager, gocommerce.RoleStaff},
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right: rightWrite, Label: "Set up product customizers",
		Scope:   "Switching a product's design editor on or off and changing its setup",
		Default: []string{gocommerce.RoleManager},
	})
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: pluginKey, Title: "Product customizer", Icon: "ri-brush-line", Category: "storefront",
		Description: "Shoppers design their own print on a product, such as a phone case with their photo, words, shapes and stickers, " +
			"and buy it as a line of its own. Each product is set up on its page under Customizer; the print file and the photos " +
			"are on the order. Off until switched on.",
		Fields: []gocommerce.PluginField{{
			Key: "consent", Label: "Before a photo is uploaded", Kind: "text", Public: true, Default: DefaultConsent,
			Help: "Shown beside the upload button. Say what the photo is used for.",
		}},
	})
	app.RegisterLineDesigner(m.lineDesign)

	app.HandleFunc("GET /x/customizer/products/{product_id}", m.handlePublicSetup)
	app.HandleFunc("POST /x/customizer/uploads", m.handleUpload)
	app.HandleFunc("POST /x/customizer/designs", m.handleCreateDesign)

	app.HandleAdminFunc("GET /api/admin/x/customizer/fonts", m.handleFonts, rightRead)
	app.HandleAdminFunc("GET /api/admin/x/customizer/products/{product_id}", m.handleGetSetup, rightRead)
	app.HandleAdminFunc("PUT /api/admin/x/customizer/products/{product_id}", m.handlePutSetup, rightWrite)

	app.OnStart(m.start)
	return nil
}

func (m *Module) enabled(ctx context.Context) bool {
	on, _ := m.app.Plugins().Enabled(ctx, pluginKey)
	return on
}

func (m *Module) start(ctx context.Context) error {
	go func() {
		t := time.NewTicker(sweepEvery)
		defer t.Stop()
		for {
			err := m.app.ForEachStore(ctx, func(ctx context.Context) error {
				_, _, err := m.Sweep(ctx)
				return err
			})
			if err != nil && ctx.Err() == nil {
				m.app.Log().Error("customizer: sweep failed", "error", err)
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
