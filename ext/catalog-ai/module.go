// Package catalogai is the catalogue's AI work, every model call made through
// the store's AI gateway. Its first piece is "Improve with AI" on a product's
// page (D152); bulk enrichment and quality checks belong here too.
//
// The panel asks for a proposal for one product. This module reads the
// product's own words and up to six of its pictures, and asks the gateway's
// catalog.improve task for a better title, description, highlights, tags,
// type, category, SEO copy and alt text. The answer comes back field by field
// beside what the product holds now, with a reason for each change and a list
// of statements the shop must check. Nothing is written here: the shop picks
// what to keep and saves the product as usual, through the catalogue's own
// update, its rights and its audit.
//
//	gw := aigateway.New(aigateway.Config{})
//	app, err := gocommerce.New(cfg, gw, catalogai.New(catalogai.Config{Gateway: gw}))
package catalogai

import (
	"html"
	"strings"
	"sync"

	gocommerce "github.com/itswadesh/gocommerce/core"
	aigateway "github.com/itswadesh/gocommerce/ext/ai-gateway"
)

// Config configures the module.
type Config struct {
	// Gateway is the store's AI gateway. Required: it is the only way this
	// module calls a model.
	Gateway *aigateway.Module
}

// Module is the catalogue's AI work.
type Module struct {
	cfg Config
	app *gocommerce.App
	// tasks registers the gateway task once: a test boots the same module
	// twice, once to migrate the template database.
	tasks sync.Once
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "catalog-ai" }

// Migrations implements gocommerce.Module. A proposal is answered, not kept:
// the gateway's own record of the run is the trace of it.
func (m *Module) Migrations() []gocommerce.Migration { return nil }

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	if m.cfg.Gateway == nil {
		// The screen says so instead of the binary refusing to start.
		app.Log().Warn("catalog-ai is installed without the AI gateway, so nothing can be improved")
	} else {
		var err error
		m.tasks.Do(func() { err = m.cfg.Gateway.RegisterTask(improveTask(m)) })
		if err != nil {
			return err
		}
	}
	w := gocommerce.RightCatalogWrite
	app.HandleAdminFunc("GET /api/admin/x/catalog-ai/status", m.handleStatus, w)
	app.HandleAdminFunc("POST /api/admin/x/catalog-ai/products/{id}/improve", m.handleImprove, w)
	return nil
}

// Honesty is the rule every task that writes a product's words follows. A
// product page is a claim the shop makes to a customer, so a model says only
// what the shop's own words or its photos show.
const Honesty = `Say only what the shop's own words or the photos show. Never write a price, discount, stock level, ` +
	`delivery promise, rating, review, sales figure or "bestseller". Never state a material, metal purity, gemstone, ` +
	`origin, brand, weight, size, dimension, fit, care instruction, certification, warranty, or health or performance ` +
	`claim unless the shop's words state it or it is plainly visible, such as printed on the item or its label. ` +
	`When the material is not evident, describe what is visible instead: the colour, the finish (shiny, matte, ` +
	`textured), the shape and the pattern. No superlatives and no claims about quality.`

// Paragraphs turns plain text into the HTML a product description is, every
// character escaped: the words are a model's, and a model's words are data.
func Paragraphs(text string) string {
	var b strings.Builder
	for _, p := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			b.WriteString("<p>" + strings.ReplaceAll(html.EscapeString(p), "\n", "<br>") + "</p>")
		}
	}
	return b.String()
}
