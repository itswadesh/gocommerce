// Package ads is ads intelligence (D168): which sources, mediums and
// campaigns bring the store's orders, what those orders earned after product
// cost, and, against the ad spend the shop enters or imports, whether the ads
// pay for themselves.
//
// It reads metadata.attribution, which the storefront records when a shopper
// arrives (the link's UTM parameters, an ad platform's click ID, the referring
// domain) and the connector hands to checkout. It writes nothing of core's:
// its own tables are the spend, the summaries and one settings row. A summary
// in plain words is written by a model through the AI gateway, from the
// report's own figures and nothing else.
//
//	gw := aigateway.New(aigateway.Config{})
//	app, err := gocommerce.New(cfg, gw, ads.New(ads.Config{Gateway: gw}))
package ads

import (
	"net/http"
	"sync"

	gocommerce "github.com/itswadesh/gocommerce/core"
	aigateway "github.com/itswadesh/gocommerce/ext/ai-gateway"
)

// Config configures the module.
type Config struct {
	// Gateway is the store's AI gateway, which the summary runs through.
	// Without it the report and spend work and the summary is refused.
	Gateway *aigateway.Module
}

// Module is ads intelligence.
type Module struct {
	cfg   Config
	app   *gocommerce.App
	tasks sync.Once
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "ads" }

// The rights the screens are gated on (D65). New rights, so they name the
// manager, as the AI gateway's do; a seller has neither, since the report
// carries the shop's product costs.
const (
	RightRead  gocommerce.Right = "ads.read"
	RightWrite gocommerce.Right = "ads.write"
)

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_ads",
		SQL: `
-- A day's spend for one campaign. Source and medium are lower-cased as the
-- report lower-cases the orders' own, so the two meet.
CREATE TABLE ads_spend (
    id           bigserial PRIMARY KEY,
    store_id     text NOT NULL,
    day          date NOT NULL,
    source       text NOT NULL CHECK (source <> '' AND source = lower(source)),
    medium       text NOT NULL DEFAULT '' CHECK (medium = lower(medium)),
    campaign     text NOT NULL CHECK (campaign <> ''),
    currency     text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    amount_minor bigint NOT NULL CHECK (amount_minor >= 0),
    origin       text NOT NULL CHECK (origin IN ('manual', 'google', 'meta')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ads_spend_day ON ads_spend (store_id, day, source, medium, lower(campaign), currency);

CREATE TABLE ads_summaries (
    id           bigserial PRIMARY KEY,
    store_id     text NOT NULL,
    kind         text NOT NULL CHECK (kind IN ('manual', 'weekly')),
    period_from  date NOT NULL,
    period_to    date NOT NULL,
    touch        text NOT NULL CHECK (touch IN ('first', 'last')),
    status       text NOT NULL CHECK (status IN ('running', 'done', 'failed')),
    headline     text NOT NULL DEFAULT '',
    points       jsonb NOT NULL DEFAULT '[]',
    figures      jsonb NOT NULL DEFAULT '[]',
    execution_id bigint,
    error        text,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ads_summaries_latest ON ads_summaries (store_id, created_at DESC) WHERE status = 'done';
-- One weekly summary a week, claimed before the model is called.
CREATE UNIQUE INDEX ads_summaries_week ON ads_summaries (store_id, period_from) WHERE kind = 'weekly';

CREATE TABLE ads_settings (
    store_id       text PRIMARY KEY,
    weekly_summary boolean NOT NULL DEFAULT false,
    updated_at     timestamptz NOT NULL DEFAULT now()
);`,
	},
		gocommerce.ScopeMigration("0002_store_scope", "ads_spend", "ads_summaries", "ads_settings"),
	}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	if m.cfg.Gateway != nil {
		var err error
		m.tasks.Do(func() { err = m.cfg.Gateway.RegisterTask(summaryTask()) })
		if err != nil {
			return err
		}
	}
	app.RegisterRight(gocommerce.RightSpec{
		Right: RightRead, Label: "See the ads report",
		Scope:   "Orders, sales, profit after product cost and ad spend by source, medium and campaign, and the AI summary",
		Default: []string{gocommerce.RoleManager},
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right: RightWrite, Label: "Enter ad spend",
		Scope:   "Entering and importing ad spend, asking for an AI summary (which draws AI credit) and the weekly summary",
		Default: []string{gocommerce.RoleManager},
	})
	app.HandleAdminFunc("GET /api/admin/x/ads/report", noSellers(m.handleReport), RightRead)
	app.HandleAdminFunc("GET /api/admin/x/ads/orders", noSellers(m.handleOrders), RightRead)
	app.HandleAdminFunc("GET /api/admin/x/ads/spend", noSellers(m.handleListSpend), RightRead)
	app.HandleAdminFunc("POST /api/admin/x/ads/spend", noSellers(m.handleAddSpend), RightWrite)
	app.HandleAdminFunc("POST /api/admin/x/ads/spend/import", noSellers(m.handleImport), RightWrite)
	app.HandleAdminFunc("DELETE /api/admin/x/ads/spend/{id}", noSellers(m.handleDeleteSpend), RightWrite)
	app.HandleAdminFunc("GET /api/admin/x/ads/summary", noSellers(m.handleLatest), RightRead)
	app.HandleAdminFunc("POST /api/admin/x/ads/summary", noSellers(m.handleSummarise), RightWrite)
	app.HandleAdminFunc("GET /api/admin/x/ads/settings", noSellers(m.handleGetSettings), RightRead)
	app.HandleAdminFunc("PUT /api/admin/x/ads/settings", noSellers(m.handlePutSettings), RightWrite)
	app.OnStart(m.start)
	return nil
}

// noSellers refuses a seller whatever their role was granted: the report is
// the shop's margins, which no seller sees (see core's costVisible).
func noSellers(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if gocommerce.IsVendorAccount(r.Context()) {
			gocommerce.RespondError(w, r, gocommerce.Forbiddenf("the ads report shows the shop's costs, which a seller account does not see"))
			return
		}
		h(w, r)
	}
}
