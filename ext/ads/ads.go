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
	// Client reads the ad platforms' APIs; nil uses the store's outbound
	// client. Tests point it at stub servers.
	Client *http.Client
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
		{ID: "0003_budgets", SQL: `
-- A source's monthly ad budget, typed in, which this month's spend is paced
-- against (D265). One per source: a platform bills in one currency.
CREATE TABLE ads_budgets (
    store_id     text NOT NULL,
    source       text NOT NULL CHECK (source <> '' AND source = lower(source)),
    currency     text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    amount_minor bigint NOT NULL CHECK (amount_minor > 0),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (store_id, source)
);`},
		gocommerce.ScopeMigration("0004_budgets_scope", "ads_budgets"),
		{ID: "0005_sync", SQL: `
-- The latest run of each spend connector (D267), shown on the ads screen.
CREATE TABLE ads_sync (
    store_id  text NOT NULL,
    platform  text NOT NULL CHECK (platform IN ('google', 'meta')),
    ran_at    timestamptz NOT NULL,
    ok        boolean NOT NULL,
    message   text NOT NULL DEFAULT '',
    days      int NOT NULL DEFAULT 0,
    campaigns int NOT NULL DEFAULT 0,
    PRIMARY KEY (store_id, platform)
);`},
		gocommerce.ScopeMigration("0006_sync_scope", "ads_sync"),
		{ID: "0007_changes", SQL: `
-- A change to an ad account, proposed from the report and made only once
-- somebody approves it (D268). A budget is minor units in the account's
-- currency; items are the keywords, placements or hours a change adds.
CREATE TABLE ads_changes (
    id           bigserial PRIMARY KEY,
    store_id     text NOT NULL,
    platform     text NOT NULL CHECK (platform IN ('google', 'meta')),
    action       text NOT NULL CHECK (action IN ('pause', 'budget', 'negative_keywords', 'exclude_placements', 'ad_schedule')),
    target_kind  text NOT NULL CHECK (target_kind IN ('campaign', 'ad_set')),
    target_id    text NOT NULL CHECK (target_id <> ''),
    target_name  text NOT NULL,
    campaign_id  text NOT NULL,
    summary      text NOT NULL,
    before_state text NOT NULL DEFAULT '',
    after_state  text NOT NULL DEFAULT '',
    currency     text CHECK (currency ~ '^[A-Z]{3}$'),
    before_minor bigint CHECK (before_minor >= 0),
    after_minor  bigint CHECK (after_minor >= 0),
    percent      int NOT NULL DEFAULT 0,
    items        jsonb NOT NULL DEFAULT '[]',
    figure       text NOT NULL,
    reason       text NOT NULL,
    detail       jsonb NOT NULL DEFAULT '{}',
    status       text NOT NULL CHECK (status IN ('proposed', 'approved', 'applied', 'failed', 'dismissed', 'undone')),
    decided_by   text NOT NULL DEFAULT '',
    decided_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK ((currency IS NULL) = (before_minor IS NULL) AND (currency IS NULL) = (after_minor IS NULL))
);
CREATE INDEX ads_changes_recent ON ads_changes (store_id, created_at DESC);

-- Every attempt to make or undo a change: what was asked, what the platform
-- answered, and who.
CREATE TABLE ads_change_log (
    id        bigserial PRIMARY KEY,
    store_id  text NOT NULL,
    change_id bigint NOT NULL REFERENCES ads_changes (id) ON DELETE CASCADE,
    kind      text NOT NULL CHECK (kind IN ('apply', 'undo')),
    ok        boolean NOT NULL,
    actor     text NOT NULL,
    request   text NOT NULL DEFAULT '',
    response  text NOT NULL DEFAULT '',
    error     text NOT NULL DEFAULT '',
    at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ads_change_log_change ON ads_change_log (change_id);

-- When the daily job last proposed changes.
ALTER TABLE ads_settings ADD COLUMN proposed_at timestamptz;`},
		gocommerce.ScopeMigration("0008_changes_scope", "ads_changes", "ads_change_log"),
	}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	if m.cfg.Gateway != nil {
		var err error
		m.tasks.Do(func() {
			if err = m.cfg.Gateway.RegisterTask(summaryTask()); err == nil {
				err = m.cfg.Gateway.RegisterTask(adviseTask())
			}
		})
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
		Scope:   "Entering and importing ad spend and monthly budgets, asking for an AI summary (which draws AI credit) and the weekly summary",
		Default: []string{gocommerce.RoleManager},
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right: RightApprove, Label: "Approve changes to the ad accounts",
		Scope: "Approving, dismissing and undoing proposed changes to Google Ads and Meta campaigns, budgets and keywords, which spend the shop's money",
	})
	app.HandleAdminFunc("GET /api/admin/x/ads/report", noSellers(m.handleReport), RightRead)
	app.HandleAdminFunc("GET /api/admin/x/ads/orders", noSellers(m.handleOrders), RightRead)
	app.HandleAdminFunc("GET /api/admin/x/ads/spend", noSellers(m.handleListSpend), RightRead)
	app.HandleAdminFunc("POST /api/admin/x/ads/spend", noSellers(m.handleAddSpend), RightWrite)
	app.HandleAdminFunc("POST /api/admin/x/ads/spend/import", noSellers(m.handleImport), RightWrite)
	app.HandleAdminFunc("DELETE /api/admin/x/ads/spend/{id}", noSellers(m.handleDeleteSpend), RightWrite)
	app.HandleAdminFunc("GET /api/admin/x/ads/summary", noSellers(m.handleLatest), RightRead)
	app.HandleAdminFunc("POST /api/admin/x/ads/summary", noSellers(m.handleSummarise), RightWrite)
	app.HandleAdminFunc("GET /api/admin/x/ads/breakdown", noSellers(m.handleBreakdown), RightRead)
	app.HandleAdminFunc("GET /api/admin/x/ads/pacing", noSellers(m.handlePacing), RightRead)
	app.HandleAdminFunc("PUT /api/admin/x/ads/budgets", noSellers(m.handlePutBudget), RightWrite)
	app.HandleAdminFunc("GET /api/admin/x/ads/advice", noSellers(m.handleAdvice), RightRead)
	app.HandleAdminFunc("POST /api/admin/x/ads/advice", noSellers(m.handleAdvice), RightWrite)
	app.HandleAdminFunc("GET /api/admin/x/ads/connectors", noSellers(m.handleConnectors), RightRead)
	app.HandleAdminFunc("POST /api/admin/x/ads/connectors/{platform}/sync", noSellers(m.handleSync), RightWrite)
	app.HandleAdminFunc("GET /api/admin/x/ads/settings", noSellers(m.handleGetSettings), RightRead)
	app.HandleAdminFunc("PUT /api/admin/x/ads/settings", noSellers(m.handlePutSettings), RightWrite)
	app.HandleAdminFunc("GET /api/admin/x/ads/changes", noSellers(m.handleChanges), RightRead)
	app.HandleAdminFunc("POST /api/admin/x/ads/changes/propose", noSellers(m.handlePropose), RightWrite)
	app.HandleAdminFunc("POST /api/admin/x/ads/changes/approve", noSellers(m.handleApproveAll), RightApprove)
	app.HandleAdminFunc("POST /api/admin/x/ads/changes/{id}/approve", noSellers(m.handleApprove), RightApprove)
	app.HandleAdminFunc("POST /api/admin/x/ads/changes/{id}/dismiss", noSellers(m.handleDismiss), RightApprove)
	app.HandleAdminFunc("POST /api/admin/x/ads/changes/{id}/undo", noSellers(m.handleUndo), RightApprove)
	m.registerConnectors(app)
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
