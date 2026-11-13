// Package sitemaps publishes the storefront's sitemap: the home page, the
// all-products page, every category with a live product in it or under it,
// every active product, every active collection, and every published page
// when the cms module is installed, each with the time it last changed.
//
// The engine does not know where its storefront lives, so the address is a
// setting; the paths are settings too, because storefronts differ on
// whether a product lives at /products/{slug} or /p/{slug}. A storefront
// serves /sitemap.xml by proxying or redirecting to /x/sitemaps/sitemap.xml,
// which is the index; search engines follow it to the files.
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

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	pluginKey = "sitemap"
	pageSize  = 500
)

// Config configures the module. Every field can also be set from the
// panel, which wins.
type Config struct {
	// StorefrontURL is where the pages live: https://shop.example.
	StorefrontURL string
	// ProductPath, CollectionPath, CategoryPath and PagePath are the
	// storefront's paths with {slug} in them. Defaults: /products/{slug},
	// /collections/{slug}, /{slug} and /pages/{slug}.
	ProductPath, CollectionPath, CategoryPath, PagePath string
	// ListingPath is the page listing every product. Default: /products.
	ListingPath string
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
		Key: pluginKey, Title: "Sitemap", Icon: "ri-compass-3-line", Category: "marketing",
		Home:        "/dash/sitemap",
		Description: "A sitemap of the home page, the product listing, every category, product and collection with something live in it — and every published page when the CMS is installed — at /x/sitemaps/sitemap.xml, for search engines. Point the storefront's /sitemap.xml at it.",
		// On for every store: a sitemap only lists what is already public, and a
		// store should not have to know it needs one before search engines find it
		// (D232).
		DefaultEnabled: true,
		Docs:           "https://www.sitemaps.org/protocol.html",
		Fields: []gocommerce.PluginField{
			// No Help: it was a bare example URL, which sat under the real one an
			// operator had already typed and read as a second, contradictory
			// address. The label and the url kind say what the box takes.
			{Key: "storefront_url", Label: "Storefront URL", Kind: "url", Public: true},
			{Key: "product_path", Label: "Product page path", Kind: "text", Default: "/products/{slug}"},
			{Key: "collection_path", Label: "Collection page path", Kind: "text", Default: "/collections/{slug}"},
			// The bundled storefront lists a category at its bare slug, /women,
			// and redirects /categories/women there.
			{Key: "category_path", Label: "Category page path", Kind: "text", Default: "/{slug}"},
			{Key: "listing_path", Label: "All-products page path", Kind: "text", Default: "/products"},
			{Key: "page_path", Label: "Content page path", Kind: "text", Default: "/pages/{slug}"},
			// The storefront's own pages that are not a record: each is listed
			// only when it has something on it.
			{Key: "contact_path", Label: "Contact page path", Kind: "text", Default: "/contact-us"},
			{Key: "faq_path", Label: "FAQ page path", Kind: "text", Default: "/faqs"},
			{Key: "collections_index_path", Label: "All-collections page path", Kind: "text", Default: "/collections"},
			{Key: "categories_index_path", Label: "All-categories page path", Kind: "text", Default: "/categories"},
		},
	})
	app.HandleFunc("GET /x/sitemaps/sitemap.xml", m.handleIndex)
	app.HandleFunc("GET /x/sitemaps/listings.xml", m.handleListings)
	app.HandleFunc("GET /x/sitemaps/products.xml", m.handleProducts)
	app.HandleFunc("GET /x/sitemaps/collections.xml", m.handleCollections)
	app.HandleFunc("GET /x/sitemaps/pages.xml", m.handlePages)
	// The sheet every sitemap above points at, so a browser shows a table
	// rather than a wall of angle brackets (stylesheet.go).
	app.HandleFunc("GET "+stylesheetPath, m.handleStylesheet)
	return nil
}

type settings struct {
	enabled                                                       bool
	storefront, products, collections, categories, listing, pages string
	contact, faq, collectionsIndex, categoriesIndex               string
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
		storefront:  strings.TrimRight(str("storefront_url", m.cfg.StorefrontURL, m.app.StorefrontURL(ctx)), "/"),
		products:    str("product_path", m.cfg.ProductPath, "/products/{slug}"),
		collections: str("collection_path", m.cfg.CollectionPath, "/collections/{slug}"),
		categories:  str("category_path", m.cfg.CategoryPath, "/{slug}"),
		listing:     str("listing_path", m.cfg.ListingPath, "/products"),
		pages:       str("page_path", m.cfg.PagePath, "/pages/{slug}"),
		contact:     str("contact_path", "/contact-us"),
		faq:         str("faq_path", "/faqs"),
		// Named like the others so a storefront without them can point
		// elsewhere; nothing is listed for a page with nothing on it.
		collectionsIndex: str("collections_index_path", "/collections"),
		categoriesIndex:  str("categories_index_path", "/categories"),
	}, nil
}

func (m *Module) hasCMS() bool { return m.hasModule("cms") }

// hasModule is whether the install runs the named module, so a table it owns
// can be read.
func (m *Module) hasModule(want string) bool {
	for _, name := range m.app.Modules() {
		if name == want {
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
		gocommerce.RespondError(w, r, gocommerce.Conflictf("the sitemap needs the storefront URL: set it on the Sitemap screen"))
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
	index := sitemapIndex{NS: ns, Sitemaps: []entry{{Loc: base + "listings.xml"}, {Loc: base + "products.xml"}, {Loc: base + "collections.xml"}}}
	if m.hasCMS() {
		index.Sitemaps = append(index.Sitemaps, entry{Loc: base + "pages.xml"})
	}
	write(w, index)
}

// handleListings is the pages a shopper browses from: the home page, the
// all-products page and each category with a live product in it or in one
// under it, since its page lists those too and an empty one is a dead end.
func (m *Module) handleListings(w http.ResponseWriter, r *http.Request) {
	s, ok := m.ready(w, r)
	if !ok {
		return
	}
	set := urlset{NS: ns, URLs: []entry{
		{Loc: s.storefront + "/", ChangeFreq: "daily"},
		{Loc: s.storefront + s.listing, ChangeFreq: "daily"},
	}}
	rows, err := m.app.DB().QueryContext(r.Context(), `
		WITH RECURSIVE filled(id) AS (
		    SELECT DISTINCT category_id FROM products WHERE status = 'active' AND category_id IS NOT NULL
		    UNION
		    SELECT c.parent_id FROM categories c JOIN filled f ON f.id = c.id WHERE c.parent_id IS NOT NULL)
		SELECT c.slug, c.updated_at FROM categories c JOIN filled f ON f.id = c.id ORDER BY c.slug`)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read categories"))
		return
	}
	defer rows.Close()
	categories := 0
	for rows.Next() {
		var slug string
		var at time.Time
		if err := rows.Scan(&slug, &at); err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "scan category"))
			return
		}
		categories++
		set.URLs = append(set.URLs, entry{
			Loc: s.storefront + strings.ReplaceAll(s.categories, "{slug}", slug), LastMod: lastmod(at), ChangeFreq: "weekly",
		})
	}
	if err := rows.Err(); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read categories"))
		return
	}
	// The storefront's own pages, each only when it has something on it: an
	// empty FAQ or collections page in a sitemap is a thin page Google is
	// told to index.
	if categories > 0 {
		set.URLs = append(set.URLs, entry{Loc: s.storefront + s.categoriesIndex, ChangeFreq: "weekly"})
	}
	if collections, _, err := m.app.Collections().ListActive(r.Context(), 1, 0); err == nil && len(collections) > 0 {
		set.URLs = append(set.URLs, entry{Loc: s.storefront + s.collectionsIndex, ChangeFreq: "weekly"})
	}
	if m.hasModule("faq") {
		var any bool
		if err := m.app.DB().QueryRowContext(r.Context(),
			`SELECT EXISTS (SELECT 1 FROM faq_entries WHERE published)`).Scan(&any); err == nil && any {
			set.URLs = append(set.URLs, entry{Loc: s.storefront + s.faq, ChangeFreq: "monthly"})
		}
	}
	set.URLs = append(set.URLs, entry{Loc: s.storefront + s.contact, ChangeFreq: "monthly"})
	write(w, set)
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
