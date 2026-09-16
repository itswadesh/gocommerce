// Package sitemaps publishes the storefront's sitemap: every active product,
// every active collection, and every published page when the cms module is
// installed, each with the time it last changed.
//
// The engine does not know where its storefront lives, so the address is a
// setting; the paths are settings too, because storefronts differ on
// whether a product lives at /products/{slug} or /p/{slug}. A storefront
// serves /sitemap.xml by proxying or redirecting to /x/sitemaps/sitemap.xml,
// which is the index; search engines follow it to the three files.
//
// Generated on request from the live catalogue, and cacheable for an hour.
// The one limit worth knowing: a sitemap file holds 50,000 URLs, and a
// catalogue past that needs the products file split, which this does not
// do yet.
package sitemaps

import (
	"context"
	"encoding/xml"
	"net/http"
	"strings"
	"time"

	gocommerce "github.com/misiki/gocommerce/core"
)

const (
	pluginKey = "sitemap"
	pageSize  = 500
)

// Config configures the module. Every field can also be set from the
// Plugins screen, which wins.
type Config struct {
	// StorefrontURL is where the pages live: https://shop.example.
	StorefrontURL string
	// ProductPath, CollectionPath and PagePath are the storefront's paths
	// with {slug} in them. Defaults: /products/{slug}, /collections/{slug},
	// /pages/{slug}.
	ProductPath, CollectionPath, PagePath string
}

// Module is the four routes.
type Module struct {
	cfg Config
	app *gocommerce.App
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "sitemaps" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration { return nil }

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: pluginKey, Title: "Sitemap", Category: "marketing",
		Description:    "A sitemap of every active product and collection — and every published page when the CMS is installed — at /x/sitemaps/sitemap.xml, for search engines. Point the storefront's /sitemap.xml at it.",
		DefaultEnabled: m.cfg.StorefrontURL != "",
		Docs:           "https://www.sitemaps.org/protocol.html",
		Fields: []gocommerce.PluginField{
			// No Help: it was a bare example URL, which sat under the real one an
			// operator had already typed and read as a second, contradictory
			// address. The label and the url kind say what the box takes.
			{Key: "storefront_url", Label: "Storefront URL", Kind: "url", Required: m.cfg.StorefrontURL == "", Public: true},
			{Key: "product_path", Label: "Product page path", Kind: "text", Default: "/products/{slug}"},
			{Key: "collection_path", Label: "Collection page path", Kind: "text", Default: "/collections/{slug}"},
			{Key: "page_path", Label: "Content page path", Kind: "text", Default: "/pages/{slug}"},
		},
	})
	app.HandleFunc("GET /x/sitemaps/sitemap.xml", m.handleIndex)
	app.HandleFunc("GET /x/sitemaps/products.xml", m.handleProducts)
	app.HandleFunc("GET /x/sitemaps/collections.xml", m.handleCollections)
	app.HandleFunc("GET /x/sitemaps/pages.xml", m.handlePages)
	// The sheet every sitemap above points at, so a browser shows a table
	// rather than a wall of angle brackets (stylesheet.go).
	app.HandleFunc("GET "+stylesheetPath, m.handleStylesheet)
	return nil
}

type settings struct {
	enabled                                  bool
	storefront, products, collections, pages string
}

func (m *Module) settings(ctx context.Context) (settings, error) {
	enabled, err := m.app.Plugins().Enabled(ctx, pluginKey)
	if err != nil {
		return settings{}, err
	}
	values, err := m.app.Plugins().Settings(ctx, pluginKey)
	if err != nil {
		return settings{}, err
	}
	str := func(key string, fallbacks ...string) string {
		if s, _ := values[key].(string); strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
		for _, f := range fallbacks {
			if f != "" {
				return f
			}
		}
		return ""
	}
	return settings{
		enabled:     enabled,
		storefront:  strings.TrimRight(str("storefront_url", m.cfg.StorefrontURL), "/"),
		products:    str("product_path", m.cfg.ProductPath, "/products/{slug}"),
		collections: str("collection_path", m.cfg.CollectionPath, "/collections/{slug}"),
		pages:       str("page_path", m.cfg.PagePath, "/pages/{slug}"),
	}, nil
}

func (m *Module) hasCMS() bool {
	for _, name := range m.app.Modules() {
		if name == "cms" {
			return true
		}
	}
	return false
}

func (m *Module) ready(w http.ResponseWriter, r *http.Request) (settings, bool) {
	s, err := m.settings(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return s, false
	}
	if !s.enabled {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("the sitemap is not enabled on this store"))
		return s, false
	}
	if s.storefront == "" {
		gocommerce.RespondError(w, r, gocommerce.Conflictf("the sitemap needs the storefront URL: set it on the Plugins screen"))
		return s, false
	}
	return s, true
}

// ------------------------------------------------------------------- xml

const ns = "http://www.sitemaps.org/schemas/sitemap/0.9"

type sitemapIndex struct {
	XMLName  xml.Name `xml:"sitemapindex"`
	NS       string   `xml:"xmlns,attr"`
	Sitemaps []entry  `xml:"sitemap"`
}

type urlset struct {
	XMLName xml.Name `xml:"urlset"`
	NS      string   `xml:"xmlns,attr"`
	URLs    []entry  `xml:"url"`
}

type entry struct {
	Loc        string `xml:"loc"`
	LastMod    string `xml:"lastmod,omitempty"`
	ChangeFreq string `xml:"changefreq,omitempty"`
}

func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	// The declaration, then the stylesheet instruction, then the document.
	// xml.Header ends in a newline, so the instruction sits on its own line
	// where a processing instruction belongs — before the root element and
	// after the declaration, which is the only place it is legal.
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write([]byte(stylesheetPI))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	_ = enc.Encode(v)
}

func lastmod(t time.Time) string { return t.UTC().Format("2006-01-02") }

func requestBase(r *http.Request) string {
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + host
}

// ---------------------------------------------------------------- routes

func (m *Module) handleIndex(w http.ResponseWriter, r *http.Request) {
	if _, ok := m.ready(w, r); !ok {
		return
	}
	base := requestBase(r) + "/x/sitemaps/"
	index := sitemapIndex{NS: ns, Sitemaps: []entry{{Loc: base + "products.xml"}, {Loc: base + "collections.xml"}}}
	if m.hasCMS() {
		index.Sitemaps = append(index.Sitemaps, entry{Loc: base + "pages.xml"})
	}
	write(w, index)
}

func (m *Module) handleProducts(w http.ResponseWriter, r *http.Request) {
	s, ok := m.ready(w, r)
	if !ok {
		return
	}
	set := urlset{NS: ns, URLs: []entry{}}
	for offset := 0; ; offset += pageSize {
		products, _, err := m.app.Products().ListProducts(r.Context(), gocommerce.ProductQuery{Status: gocommerce.ProductActive, Limit: pageSize, Offset: offset})
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		for _, p := range products {
			set.URLs = append(set.URLs, entry{
				Loc: s.storefront + strings.ReplaceAll(s.products, "{slug}", p.Slug), LastMod: lastmod(p.UpdatedAt), ChangeFreq: "weekly",
			})
		}
		if len(products) < pageSize {
			break
		}
	}
	write(w, set)
}

func (m *Module) handleCollections(w http.ResponseWriter, r *http.Request) {
	s, ok := m.ready(w, r)
	if !ok {
		return
	}
	set := urlset{NS: ns, URLs: []entry{}}
	for offset := 0; ; offset += pageSize {
		collections, _, err := m.app.Collections().ListActive(r.Context(), pageSize, offset)
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		for _, c := range collections {
			set.URLs = append(set.URLs, entry{
				Loc: s.storefront + strings.ReplaceAll(s.collections, "{slug}", c.Slug), LastMod: lastmod(c.UpdatedAt), ChangeFreq: "weekly",
			})
		}
		if len(collections) < pageSize {
			break
		}
	}
	write(w, set)
}

// handlePages reads the cms module's table directly, which is the one
// place this module looks past its own door: the cms module has no Go API
// a sibling can import without a dependency cycle through the store, and
// the two columns read here are the ones its public route publishes anyway.
func (m *Module) handlePages(w http.ResponseWriter, r *http.Request) {
	s, ok := m.ready(w, r)
	if !ok {
		return
	}
	if !m.hasCMS() {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("this store has no content pages: the cms module is not installed"))
		return
	}
	rows, err := m.app.DB().QueryContext(r.Context(),
		`SELECT slug, updated_at FROM cms_pages WHERE status = 'published' ORDER BY slug`)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read pages"))
		return
	}
	defer rows.Close()
	set := urlset{NS: ns, URLs: []entry{}}
	seen := map[string]bool{}
	for rows.Next() {
		var slug string
		var at time.Time
		if err := rows.Scan(&slug, &at); err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "scan page"))
			return
		}
		// A page in several languages is one address.
		if seen[slug] {
			continue
		}
		seen[slug] = true
		set.URLs = append(set.URLs, entry{Loc: s.storefront + strings.ReplaceAll(s.pages, "{slug}", slug), LastMod: lastmod(at), ChangeFreq: "monthly"})
	}
	write(w, set)
}
