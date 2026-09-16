// Package feeds publishes the catalogue in the shapes advertising platforms
// read: Google Merchant Center's RSS feed and Meta's catalogue CSV.
//
// A feed is the storefront's products, one item per variant, with the
// fields those platforms require and nothing they do not: an id, a title,
// a price in the store's currency, a link to the product page, a picture,
// availability, a brand, and the group that ties a product's variants
// together. Both are generated on request from the live catalogue, so a
// platform that fetches daily sees yesterday's prices only for a day.
//
// The storefront's address is a setting, because the engine does not know
// where its storefront lives; pictures the store holds itself are given
// the request's own host so the platform can fetch them.
package feeds

import (
	"context"
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	gocommerce "github.com/misiki/gocommerce/core"
)

const (
	pluginKey = "product-feeds"
	pageSize  = 200
)

// Config configures the module. Every field can also be set from the
// Plugins screen, which wins.
type Config struct {
	// StorefrontURL is where product pages live: https://shop.example.
	StorefrontURL string
	// ProductPath is the product page's path with {slug} in it. Defaults to
	// /products/{slug}.
	ProductPath string
	// Brand is what an item says when its product has no vendor.
	Brand string
}

// Module is the two feeds.
type Module struct {
	cfg Config
	app *gocommerce.App
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "feeds" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration { return nil }

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: pluginKey, Title: "Product feeds", Category: "marketing",
		Description:    "Google Merchant Center and Meta catalogue feeds, generated from the live catalogue at /x/feeds/google.xml and /x/feeds/meta.csv — one item per variant, in the store's currency.",
		DefaultEnabled: m.cfg.StorefrontURL != "",
		Docs:           "https://support.google.com/merchants/answer/7052112",
		Fields: []gocommerce.PluginField{
			{Key: "storefront_url", Label: "Storefront URL", Kind: "url", Required: m.cfg.StorefrontURL == "", Public: true, Help: "Where product pages live: https://shop.example"},
			{Key: "product_path", Label: "Product page path", Kind: "text", Default: "/products/{slug}", Help: "{slug} is replaced by the product's slug."},
			{Key: "brand", Label: "Brand when a product has no vendor", Kind: "text"},
			{Key: "google_product_category", Label: "Default Google product category", Kind: "text", Help: "Used when a product has no category of its own."},
		},
	})
	app.HandleFunc("GET /x/feeds/google.xml", m.handleGoogle)
	app.HandleFunc("GET /x/feeds/meta.csv", m.handleMeta)
	// The screen's own read, gated on the right that already governs it: the
	// Feeds page is a plugin page, and this says nothing a plugin page does not
	// already show.
	app.HandleAdminFunc("GET /api/admin/x/feeds/status", m.handleStatus, gocommerce.RightPluginsRead)
	return nil
}

// settings is the plugin's values over Config.
type settings struct {
	enabled     bool
	storefront  string
	productPath string
	brand       string
	category    string
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
	str := func(key, fallback string) string {
		if s, _ := values[key].(string); strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
		return fallback
	}
	s := settings{
		enabled:     enabled,
		storefront:  strings.TrimRight(str("storefront_url", m.cfg.StorefrontURL), "/"),
		productPath: str("product_path", firstNonEmpty(m.cfg.ProductPath, "/products/{slug}")),
		brand:       str("brand", m.cfg.Brand),
		category:    str("google_product_category", ""),
	}
	return s, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// item is one variant as both feeds see it.
type item struct {
	ID, Title, Description, Link, Image, Brand, GroupID, GTIN, MPN string
	Price, SalePrice, Availability, Condition, Category, Weight    string
	Quantity                                                       int
}

// items walks the active catalogue. mediaBase makes a site-relative picture
// absolute: the platform fetches it, and "/media/x.jpg" is not an address.
func (m *Module) items(ctx context.Context, s settings, mediaBase string) ([]item, error) {
	exp := exponent(m.app.Settings().Currency)
	currency := strings.ToUpper(m.app.Settings().Currency)
	var out []item
	for offset := 0; ; offset += pageSize {
		products, _, err := m.app.Products().ListProducts(ctx, gocommerce.ProductQuery{Status: gocommerce.ProductActive, Limit: pageSize, Offset: offset})
		if err != nil {
			return nil, err
		}
		if len(products) == 0 {
			break
		}
		for _, p := range products {
			if len(p.Variants) == 0 {
				if p, err = m.app.Products().GetProduct(ctx, p.ID); err != nil {
					return nil, err
				}
			}
			link := s.storefront + strings.ReplaceAll(s.productPath, "{slug}", p.Slug)
			brand := firstNonEmpty(p.Vendor, s.brand)
			category := s.category
			if p.Category != nil {
				category = strings.ReplaceAll(p.Category.FullName, " / ", " > ")
			}
			description := plainText(p.Description)
			if len(description) > 5000 {
				description = description[:5000]
			}
			for _, v := range p.Variants {
				if !v.Active {
					continue
				}
				it := item{
					ID: v.SKU, MPN: v.SKU, GTIN: v.Barcode, GroupID: "product-" + strconv.FormatInt(p.ID, 10),
					Title: p.Title, Description: description, Link: link, Brand: brand, Category: category,
					Condition: "new", Availability: "out of stock", Image: absolute(mediaBase, p.ImageURL),
				}
				if v.Label != "" && v.Label != "Default" {
					it.Title += " - " + v.Label
					it.Link += "?variant=" + v.SKU
				}
				if v.Image != nil {
					it.Image = absolute(mediaBase, v.Image.URL)
				}
				if v.InStock(1) {
					it.Availability = "in stock"
				}
				if v.TrackInventory {
					it.Quantity = v.Available
				} else {
					it.Quantity = 999
				}
				it.Price = money(v.Price.AmountMinor, exp) + " " + currency
				if v.CompareAtPrice != nil && v.CompareAtPrice.AmountMinor > v.Price.AmountMinor {
					it.SalePrice = it.Price
					it.Price = money(v.CompareAtPrice.AmountMinor, exp) + " " + currency
				}
				if v.WeightGrams != nil && *v.WeightGrams > 0 {
					it.Weight = strconv.Itoa(*v.WeightGrams) + " g"
				}
				out = append(out, it)
			}
		}
		if len(products) < pageSize {
			break
		}
	}
	return out, nil
}

func absolute(base, u string) string {
	if u == "" || base == "" || !strings.HasPrefix(u, "/") {
		return u
	}
	return strings.TrimRight(base, "/") + u
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

func plainText(html string) string {
	text := tagRE.ReplaceAllString(html, " ")
	text = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'").Replace(text)
	return strings.Join(strings.Fields(text), " ")
}

func exponent(code string) int {
	switch strings.ToUpper(code) {
	case "BIF", "CLP", "DJF", "GNF", "ISK", "JPY", "KMF", "KRW", "PYG", "RWF", "UGX", "VND", "VUV", "XAF", "XOF", "XPF":
		return 0
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND":
		return 3
	}
	return 2
}

func money(minor int64, exp int) string {
	if exp == 0 {
		return strconv.FormatInt(minor, 10)
	}
	scale := int64(1)
	for i := 0; i < exp; i++ {
		scale *= 10
	}
	return fmt.Sprintf("%d.%0*d", minor/scale, exp, minor%scale)
}

// requestBase is the address the request came to, for pictures the store
// serves itself.
func requestBase(r *http.Request) string {
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	if host == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + host
}

// ready is the settings, or the refusal a feed answers with when it cannot
// be built: off is a 404, on but unconfigured a 409 that says what to set.
func (m *Module) ready(w http.ResponseWriter, r *http.Request) (settings, bool) {
	s, err := m.settings(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return s, false
	}
	if !s.enabled {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("product feeds are not enabled on this store"))
		return s, false
	}
	if s.storefront == "" {
		gocommerce.RespondError(w, r, gocommerce.Conflictf("product feeds need the storefront URL: set it on the Plugins screen"))
		return s, false
	}
	return s, true
}

// ------------------------------------------------------------------ google

// The RSS 2.0 shape Merchant Center reads, with the g: namespace.
type rss struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	NS      string   `xml:"xmlns:g,attr"`
	Channel channel  `xml:"channel"`
}

type channel struct {
	Title       string       `xml:"title"`
	Link        string       `xml:"link"`
	Description string       `xml:"description"`
	Items       []googleItem `xml:"item"`
}

type googleItem struct {
	ID           string `xml:"g:id"`
	Title        string `xml:"g:title"`
	Description  string `xml:"g:description"`
	Link         string `xml:"g:link"`
	ImageLink    string `xml:"g:image_link,omitempty"`
	Availability string `xml:"g:availability"`
	Price        string `xml:"g:price"`
	SalePrice    string `xml:"g:sale_price,omitempty"`
	Brand        string `xml:"g:brand,omitempty"`
	Condition    string `xml:"g:condition"`
	GTIN         string `xml:"g:gtin,omitempty"`
	MPN          string `xml:"g:mpn,omitempty"`
	ItemGroupID  string `xml:"g:item_group_id"`
	Category     string `xml:"g:google_product_category,omitempty"`
	Weight       string `xml:"g:shipping_weight,omitempty"`
}

func (m *Module) handleGoogle(w http.ResponseWriter, r *http.Request) {
	s, ok := m.ready(w, r)
	if !ok {
		return
	}
	items, err := m.items(r.Context(), s, requestBase(r))
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	feed := rss{Version: "2.0", NS: "http://base.google.com/ns/1.0", Channel: channel{
		Title: m.app.Settings().OrderPrefix, Link: s.storefront, Description: "Products",
	}}
	if feed.Channel.Title == "" {
		feed.Channel.Title = s.storefront
	}
	for _, it := range items {
		feed.Channel.Items = append(feed.Channel.Items, googleItem{
			ID: it.ID, Title: it.Title, Description: it.Description, Link: it.Link, ImageLink: it.Image,
			Availability: it.Availability, Price: it.Price, SalePrice: it.SalePrice, Brand: it.Brand,
			Condition: it.Condition, GTIN: it.GTIN, MPN: it.MPN, ItemGroupID: it.GroupID,
			Category: it.Category, Weight: it.Weight,
		})
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=900")
	_, _ = w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	_ = enc.Encode(feed)
}

// -------------------------------------------------------------------- meta

var metaHeader = []string{
	"id", "title", "description", "availability", "condition", "price", "sale_price", "link", "image_link",
	"brand", "item_group_id", "gtin", "mpn", "google_product_category", "quantity_to_sell_on_facebook",
}

func (m *Module) handleMeta(w http.ResponseWriter, r *http.Request) {
	s, ok := m.ready(w, r)
	if !ok {
		return
	}
	items, err := m.items(r.Context(), s, requestBase(r))
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=900")
	cw := csv.NewWriter(w)
	_ = cw.Write(metaHeader)
	for _, it := range items {
		_ = cw.Write([]string{
			it.ID, it.Title, it.Description, it.Availability, it.Condition, it.Price, it.SalePrice, it.Link, it.Image,
			it.Brand, it.GroupID, it.GTIN, it.MPN, it.Category, strconv.Itoa(it.Quantity),
		})
	}
	cw.Flush()
}
