package layouts

import (
	"bytes"
	"context"
	"embed"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

func (m *Module) handleRegistry(w http.ResponseWriter, r *http.Request) {
	gocommerce.Respond(w, http.StatusOK, map[string]any{
		"sections": m.offered(r.Context()), "themes": Themes, "pages": Pages, "theme_settings": ThemeSettings,
	})
}

// offered is the section library: every type, less those whose plugin is off
// or not in this binary, which a shop could add but a shopper could not use —
// and, the same way, each type less its settings whose plugin is off.
func (m *Module) offered(ctx context.Context) []SectionType {
	out := make([]SectionType, 0, len(Sections))
	for _, s := range Sections {
		if s.Requires != "" && !m.pluginOn(ctx, s.Requires) {
			continue
		}
		fields := make([]Field, 0, len(s.Fields))
		for _, f := range s.Fields {
			if f.Requires == "" || m.pluginOn(ctx, f.Requires) {
				fields = append(fields, f)
			}
		}
		s.Fields = fields
		out = append(out, s)
	}
	return out
}

func (m *Module) pluginOn(ctx context.Context, key string) bool {
	on, err := m.app.Plugins().Enabled(ctx, key)
	return err == nil && on
}

func (m *Module) handleGet(w http.ResponseWriter, r *http.Request) {
	page, err := pageKey(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	st, err := m.state(r.Context(), m.db, page)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, st)
}

func (m *Module) handleSave(w http.ResponseWriter, r *http.Request) {
	page, err := pageKey(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in struct {
		Version  int      `json:"version"`
		Document Document `json:"document"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2*MaxDocBytes)
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	st, err := m.Save(r.Context(), page, in.Version, in.Document)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, st)
}

func (m *Module) handlePublish(w http.ResponseWriter, r *http.Request) {
	page, err := pageKey(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in struct {
		Version int `json:"version"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	st, err := m.Publish(r.Context(), page, in.Version)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, st)
}

func (m *Module) handleRevisions(w http.ResponseWriter, r *http.Request) {
	page, err := pageKey(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	list, err := m.Revisions(r.Context(), page)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, list)
}

func (m *Module) handleRollback(w http.ResponseWriter, r *http.Request) {
	page, err := pageKey(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in struct {
		Revision int `json:"revision"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	st, err := m.Rollback(r.Context(), page, in.Revision)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, st)
}

func (m *Module) handleReset(w http.ResponseWriter, r *http.Request) {
	page, err := pageKey(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in struct {
		Version int `json:"version"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	st, err := m.Reset(r.Context(), page, in.Version)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, st)
}

func (m *Module) handlePreview(w http.ResponseWriter, r *http.Request) {
	page, err := pageKey(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	token, expires, err := m.Preview(r.Context(), page)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// The storefront's address comes with the key, so the editor can frame
	// the store's own home page (D126); empty when the store has none yet.
	gocommerce.Respond(w, http.StatusCreated, map[string]any{
		"token": token, "expires_at": expires.UTC(), "storefront": m.app.StorefrontURL(r.Context()),
	})
}

// PublicPage is what the storefront draws: the shown sections in order, each
// product grid with its products resolved.
type PublicPage struct {
	Page     string `json:"page"`
	Theme    string `json:"theme"`
	Revision int    `json:"revision,omitempty"`
	Preview  bool   `json:"preview,omitempty"`
	// ThemeSettings are the design tokens published with the page (D127).
	ThemeSettings map[string]any  `json:"theme_settings,omitempty"`
	Sections      []PublicSection `json:"sections"`
}

// PublicSection is a shown section. Products is set on a product grid,
// Collections on a collection list.
type PublicSection struct {
	ID          string                `json:"id"`
	Type        string                `json:"type"`
	Settings    map[string]any        `json:"settings"`
	Products    []*gocommerce.Product `json:"products,omitempty"`
	Collections []PublicCollection    `json:"collections,omitempty"`
}

// PublicCollection is what a collection list's card shows and links to.
type PublicCollection struct {
	ID    int64  `json:"id"`
	Slug  string `json:"slug"`
	Title string `json:"title"`
	// Image is the collection's picture (its metadata's image), or empty.
	Image string `json:"image,omitempty"`
}

// PublicTheme is the design tokens every page of the storefront applies:
// the published ones, or a preview key's draft.
type PublicTheme struct {
	Revision int            `json:"revision,omitempty"`
	Preview  bool           `json:"preview,omitempty"`
	Settings map[string]any `json:"settings"`
}

// handleTheme answers the theme settings alone, so a page other than the
// home page gets them without the home page's products. It reads one value
// of one row; a 30-second cache keeps a busy storefront from asking on
// every request without holding a publish back for long.
func (m *Module) handleTheme(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !m.pluginOn(ctx, pluginKey) {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("the homepage editor is switched off"))
		return
	}
	out := PublicTheme{}
	var doc *Document
	var err error
	if token := strings.TrimSpace(r.URL.Query().Get("preview")); token != "" {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Robots-Tag", "noindex")
		if doc, err = m.draftFor(ctx, "home", token); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		out.Preview = true
	} else {
		var rev *Revision
		if rev, doc, err = m.shown(ctx, "home"); err != nil {
			gocommerce.RespondError(w, r, gocommerce.NotFoundf("no theme has been published"))
			return
		}
		out.Revision = rev.Number
		w.Header().Set("Cache-Control", "public, max-age=30")
		w.Header().Add("Vary", "X-Store")
	}
	out.Settings = doc.ThemeSettings
	if out.Settings == nil {
		out.Settings = map[string]any{}
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

func (m *Module) handlePublic(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page, err := pageKey(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// Switched off, the storefront is told there is no arranged page, and
	// draws its built-in one: the switch is how a shop tries this safely.
	if on, err := m.app.Plugins().Enabled(ctx, pluginKey); err != nil || !on {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("the homepage editor is switched off"))
		return
	}
	out := PublicPage{Page: page}
	var doc *Document
	if token := strings.TrimSpace(r.URL.Query().Get("preview")); token != "" {
		// A draft is for the person previewing it: never stored by a cache,
		// never indexed.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Robots-Tag", "noindex")
		if doc, err = m.draftFor(ctx, page, token); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		out.Preview = true
	} else {
		var rev *Revision
		if rev, doc, err = m.shown(ctx, page); err != nil {
			gocommerce.RespondError(w, r, gocommerce.NotFoundf("the %s has not been published", pageInfo(page).Label))
			return
		}
		out.Revision = rev.Number
		// The header and footer are read for every page the storefront
		// draws; like the theme settings, a short cache keeps that cheap
		// without holding a publish back for long.
		if pageInfo(page).CoreOnly {
			w.Header().Set("Cache-Control", "public, max-age=30")
			w.Header().Add("Vary", "X-Store")
		}
	}
	out.Theme = doc.Theme
	out.ThemeSettings = doc.ThemeSettings
	out.Sections, err = m.resolve(r, doc.Sections)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

// resolve drops hidden sections and fills each product grid with what it
// shows now. Products named by id are read once for the whole page, however
// many grids name them.
func (m *Module) resolve(r *http.Request, sections []Section) ([]PublicSection, error) {
	ctx := r.Context()
	channel, err := m.app.Channels().Resolve(ctx, r.URL.Query().Get("channel"))
	if err != nil {
		return nil, err
	}
	var channelID int64
	if channel != nil {
		channelID = channel.ID
	}
	var named []int64
	for _, s := range sections {
		if s.Enabled && s.Type == "product-grid" && setting(s, "source") == "products" {
			named = append(named, ints(s.Settings["products"])...)
		}
	}
	byID, err := m.sellable(ctx, named, channelID)
	if err != nil {
		return nil, err
	}

	out := []PublicSection{}
	var shown []*gocommerce.Product
	for _, s := range sections {
		if !s.Enabled {
			continue
		}
		// A section whose plugin is off would be a box that does nothing.
		if st := sectionType(s.Type); st != nil && st.Requires != "" && !m.pluginOn(ctx, st.Requires) {
			continue
		}
		ps := PublicSection{ID: s.ID, Type: s.Type, Settings: m.usable(ctx, s)}
		if s.Type == "collection-list" {
			ps.Collections = m.collectionCards(ctx, ints(s.Settings["collections"]))
		}
		if s.Type == "product-grid" {
			limit := number(s.Settings["limit"], 8)
			switch setting(s, "source") {
			case "newest":
				// What a new shop's grid shows before it has a collection:
				// the newest on sale, by the catalogue's own channel rule.
				if ps.Products, _, err = m.app.Products().ListProducts(ctx, gocommerce.ProductQuery{
					Status: gocommerce.ProductActive, ChannelID: channelID, Limit: limit,
				}); err != nil {
					return nil, err
				}
			case "products":
				for _, id := range ints(s.Settings["products"]) {
					if p := byID[id]; p != nil && len(ps.Products) < limit {
						ps.Products = append(ps.Products, p)
					}
				}
			default:
				if id := number(s.Settings["collection"], 0); id > 0 {
					if ps.Products, err = m.inCollection(ctx, int64(id), channelID, limit); err != nil {
						return nil, err
					}
				}
			}
			if ps.Products == nil {
				// A grid whose products are gone shows nothing rather than
				// breaking the page; the storefront skips an empty grid.
				ps.Products = []*gocommerce.Product{}
			}
			shown = append(shown, ps.Products...)
		}
		out = append(out, ps)
	}
	m.app.TranslateProducts(r, shown)
	return out, nil
}

// usable is a section's settings less any whose plugin is off: a newsletter
// box with no newsletter, or a menu with no menus, would be a promise the
// storefront cannot keep.
func (m *Module) usable(ctx context.Context, s Section) map[string]any {
	st := sectionType(s.Type)
	if st == nil {
		return s.Settings
	}
	var out map[string]any
	for _, f := range st.Fields {
		if f.Requires == "" || m.pluginOn(ctx, f.Requires) {
			continue
		}
		if _, held := s.Settings[f.Key]; !held {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(s.Settings))
			for k, v := range s.Settings {
				out[k] = v
			}
		}
		delete(out, f.Key)
	}
	if out == nil {
		return s.Settings
	}
	return out
}

// collectionCards reads the picked collections, in the order picked; one
// deleted since is left out rather than breaking the list.
func (m *Module) collectionCards(ctx context.Context, ids []int64) []PublicCollection {
	out := []PublicCollection{}
	for _, id := range ids {
		c, err := m.app.Collections().Get(ctx, id)
		if err != nil {
			continue
		}
		image, _ := c.Metadata["image"].(string)
		if !safeURL(image) {
			image = ""
		}
		out = append(out, PublicCollection{ID: c.ID, Slug: c.Slug, Title: c.Title, Image: image})
	}
	return out
}

func (m *Module) inCollection(ctx context.Context, id, channelID int64, limit int) ([]*gocommerce.Product, error) {
	c, err := m.app.Collections().Get(ctx, id)
	if err != nil {
		// Deleted since the page was published: an empty grid, not an error.
		return nil, nil
	}
	products, _, err := m.app.Collections().ProductsInCollection(ctx, c.ID, gocommerce.ProductQuery{
		Status: gocommerce.ProductActive, ChannelID: channelID, Limit: limit,
	})
	return products, err
}

// sellable reads the named products that a shopper may see: active and, when
// there is a channel, published to it or to none in particular — the rule the
// catalogue reads by.
func (m *Module) sellable(ctx context.Context, ids []int64, channel int64) (map[int64]*gocommerce.Product, error) {
	out := map[int64]*gocommerce.Product{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT p.id FROM products p
		WHERE p.id = ANY($1::bigint[]) AND p.status = 'active'
		  AND ($2 = 0
		       OR EXISTS (SELECT 1 FROM product_channels pc WHERE pc.product_id = p.id AND pc.channel_id = $2)
		       OR NOT EXISTS (SELECT 1 FROM product_channels pc WHERE pc.product_id = p.id))`,
		int64Array(ids), channel)
	if err != nil {
		return nil, gocommerce.Internalf(err, "read the page's products")
	}
	var ok []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, gocommerce.Internalf(err, "read the page's products")
		}
		ok = append(ok, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, gocommerce.Internalf(err, "read the page's products")
	}
	for _, id := range ok {
		p, err := m.app.Products().GetProduct(ctx, id)
		if err != nil {
			continue
		}
		out[id] = p
	}
	return out, nil
}

func setting(s Section, key string) string {
	v, _ := s.Settings[key].(string)
	if v == "" && key == "source" {
		return "collection"
	}
	return v
}

// ints reads a stored id list, which is []int64 fresh from Validate and
// []any of float64 once it has been through the database.
func ints(v any) []int64 {
	switch l := v.(type) {
	case []int64:
		return l
	case []any:
		out := make([]int64, 0, len(l))
		for _, x := range l {
			if f, ok := x.(float64); ok {
				out = append(out, int64(f))
			}
		}
		return out
	}
	return nil
}

func number(v any, fallback int) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return fallback
}

// int64Array is a Postgres array literal of ids, which the bigint[] cast
// reads without a driver-specific array type.
func int64Array(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// Starter is the page a shop starts from before its first save. The
// templates and the header and footer are the storefront's own built-in
// pages, so publishing them unchanged changes nothing a shopper sees (D131).
// The home page is the "Bold and bright" look (D140), under the header's own
// announcement strip rather than a second one: a split hero on a colour
// block, four tiles, the newest products, a picture beside words and the
// benefits strip, with theme settings for the colours and the rounding, and
// explicit values for the type, spacing, cards, lists and product page (D155).
// Every word is true of any shop and every link goes to a page every
// storefront has; the benefits are promises the shop must make its own.
// Its pictures are the engine's own (starter/: Unsplash photographs for the
// hero and the lookbook, Google Gemini pictures for the tiles; credits in
// starter/README.md), served at a path on the engine, which every storefront
// and the editor's preview already resolve against the engine as they do the
// Files library's. Until a shop publishes its home page, this is what its
// storefront shows (shown).
func Starter(page string) Document {
	switch page {
	case "header":
		return Document{SchemaVersion: schemaVersion, Theme: Themes[0].ID, Sections: []Section{
			{ID: "header", Type: "header", Enabled: true, Settings: map[string]any{
				"show_announcement": true, "announcement_text": "Free shipping on your first order · Easy 7-day returns",
				"announcement_link": "/products", "announcement_pages": "home", "menu": "header"}},
		}}
	case "footer":
		return Document{SchemaVersion: schemaVersion, Theme: Themes[0].ID, Sections: []Section{
			{ID: "footer", Type: "footer", Enabled: true, Settings: map[string]any{"menu": "footer", "newsletter": true}},
		}}
	case "product":
		return Document{SchemaVersion: schemaVersion, Theme: Themes[0].ID, Sections: []Section{
			{ID: "product", Type: "main-product", Enabled: true, Settings: map[string]any{}},
			{ID: "cross-sells", Type: "cross-sells", Enabled: true, Settings: map[string]any{}},
			{ID: "reviews", Type: "product-reviews", Enabled: true, Settings: map[string]any{}},
			{ID: "related", Type: "related-products", Enabled: true, Settings: map[string]any{}},
		}}
	case "collection":
		return Document{SchemaVersion: schemaVersion, Theme: Themes[0].ID, Sections: []Section{
			{ID: "products", Type: "main-collection", Enabled: true, Settings: map[string]any{}},
		}}
	}
	return Document{SchemaVersion: schemaVersion, Theme: Themes[0].ID, ThemeSettings: map[string]any{
		// Violet carries white words at 5.7:1; the pale yellow carries the
		// storefront's own text colour at well over 4.5:1.
		"accent": "#7c3aed", "accent_text": "#ffffff", "muted": "#fef3c7",
		// No heading font: a web font repainted the hero's headline seconds
		// after the page drew on a phone, which Lighthouse counts against it;
		// the storefront's own font at a heavy weight carries the look.
		"radius": "large",
		// The Bewakoof-style green, #1a8f3a, reads at 4.2:1 on the
		// storefront's white; this is the nearest of its hue at 4.5:1.
		"sale":       "#198837",
		"type_scale": "standard", "heading_weight": "extrabold", "density": "compact",
		"card_badge": "product_badge", "card_vendor": true, "card_title_lines": "one", "card_rating": true,
		"card_discount": true, "card_wishlist": true, "card_add_to_cart": false, "card_second_image": true,
		"list_columns_mobile": "two", "list_columns_desktop": "five", "list_subcategories": true,
		"list_quick_filters": true, "list_filters_desktop": "sidebar", "list_mobile_bar": true,
		"pdp_sticky_buy": true, "pdp_gallery": "carousel", "pdp_highlights_grid": true,
		"pdp_delivery_check": true, "pdp_low_stock_at": 5,
	}, Sections: []Section{
		{ID: "hero", Type: "hero", Enabled: true, Settings: map[string]any{
			"eyebrow": "New in", "heading": "Find your next favourite thing",
			"body":        "Browse the range and pick what you love.",
			"button_text": "Shop now", "button_link": "/products", "align": "left",
			"image": starterPicturePrefix + "hero.webp", "mobile_image": starterPicturePrefix + "hero-phone.webp",
			"image_alt": "A smiling woman in a turquoise kurta, her hand painted with henna, holding up a sheer dupatta"}},
		{ID: "tiles", Type: "multi-column", Enabled: true, Settings: map[string]any{
			"heading": "Start shopping", "items": []any{
				map[string]any{"image": starterPicturePrefix + "tile-shop-all.webp", "heading": "Shop all", "link_text": "See everything", "link": "/products"},
				map[string]any{"image": starterPicturePrefix + "tile-collections.webp", "heading": "Collections", "link_text": "See collections", "link": "/collections"},
				map[string]any{"image": starterPicturePrefix + "tile-categories.webp", "heading": "Categories", "link_text": "See categories", "link": "/categories"},
				map[string]any{"image": starterPicturePrefix + "tile-track.webp", "heading": "Track an order", "link_text": "Find your order", "link": "/order-tracking"},
			}}},
		{ID: "featured", Type: "product-grid", Enabled: true, Settings: map[string]any{
			"heading": "New in", "source": "newest", "limit": 8, "columns": 4, "view_all_link": "/products"}},
		{ID: "lookbook", Type: "image-with-text", Enabled: true, Settings: map[string]any{
			"eyebrow": "Made for every day", "heading": "Good things, picked for you", "image_side": "right",
			"image":       starterPicturePrefix + "lookbook.webp",
			"image_alt":   "A smiling woman in an embroidered red kurta looking into a white handbag",
			"body":        "Have a look around the shop and find what suits you.",
			"button_text": "Explore the shop", "button_link": "/products"}},
		{ID: "assurances", Type: "benefits", Enabled: true, Settings: map[string]any{"items": []any{
			map[string]any{"title": "Free shipping", "body": "On your qualifying orders", "icon": "truck"},
			map[string]any{"title": "Easy returns", "body": "7-day, hassle-free", "icon": "return"},
			map[string]any{"title": "Secure checkout", "body": "Encrypted & protected", "icon": "shield"},
			map[string]any{"title": "Here to help", "body": "Support when you need it", "icon": "headset"},
		}}},
	}}
}

//go:embed starter/*.webp
var starterPictures embed.FS

// starterPicturePrefix is where the engine serves the starter's pictures.
// They are cached as immutable and named in pages shops have saved, so a
// file here never changes or goes: a changed picture is a new file name, and
// the old file stays embedded for the pages that still name it.
const starterPicturePrefix = "/x/layouts/starter/1/"

// handleStarterPicture serves one of the starter's pictures from the binary.
// Public, like a store's own media: a storefront shows it to anybody, and it
// stays up while the editor is off, since a page published with it may still
// be cached somewhere.
func (m *Module) handleStarterPicture(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	body, err := starterPictures.ReadFile("starter/" + name)
	if err != nil || path.Ext(name) != ".webp" {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no such picture"))
		return
	}
	w.Header().Set("Content-Type", "image/webp")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
}
