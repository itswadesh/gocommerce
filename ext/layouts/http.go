package layouts

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

func (m *Module) handleRegistry(w http.ResponseWriter, r *http.Request) {
	gocommerce.Respond(w, http.StatusOK, map[string]any{"sections": Sections, "themes": Themes, "pages": Pages})
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
	gocommerce.Respond(w, http.StatusCreated, map[string]any{"token": token, "expires_at": expires.UTC()})
}

// PublicPage is what the storefront draws: the shown sections in order, each
// product grid with its products resolved.
type PublicPage struct {
	Page     string          `json:"page"`
	Theme    string          `json:"theme"`
	Revision int             `json:"revision,omitempty"`
	Preview  bool            `json:"preview,omitempty"`
	Sections []PublicSection `json:"sections"`
}

// PublicSection is a shown section. Products is set on a product grid.
type PublicSection struct {
	ID       string                `json:"id"`
	Type     string                `json:"type"`
	Settings map[string]any        `json:"settings"`
	Products []*gocommerce.Product `json:"products,omitempty"`
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
		if rev, doc, err = m.published(ctx, page); err != nil {
			gocommerce.RespondError(w, r, gocommerce.NotFoundf("the home page has not been published"))
			return
		}
		out.Revision = rev.Number
	}
	out.Theme = doc.Theme
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
		ps := PublicSection{ID: s.ID, Type: s.Type, Settings: s.Settings}
		if s.Type == "product-grid" {
			limit := number(s.Settings["limit"], 8)
			if setting(s, "source") == "products" {
				for _, id := range ints(s.Settings["products"]) {
					if p := byID[id]; p != nil && len(ps.Products) < limit {
						ps.Products = append(ps.Products, p)
					}
				}
			} else if id := number(s.Settings["collection"], 0); id > 0 {
				if ps.Products, err = m.inCollection(ctx, int64(id), channelID, limit); err != nil {
					return nil, err
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

// Starter is the page a shop starts from before its first save: the
// storefront's built-in home page, section for section and word for word, so
// switching the editor on and publishing changes nothing a shopper sees until
// the shop changes it. It has no pictures — the built-in page's are stock
// photographs, and a shop's pictures are its own to choose.
func Starter() Document {
	return Document{SchemaVersion: schemaVersion, Theme: Themes[0].ID, Sections: []Section{
		{ID: "announcement", Type: "announcement-bar", Enabled: true, Settings: map[string]any{
			"text": "Free shipping on your first order · Easy 7-day returns", "link": "/products"}},
		{ID: "hero", Type: "hero", Enabled: true, Settings: map[string]any{
			"eyebrow": "New Season", "heading": "Considered goods for modern living",
			"body":        "A curated edit of pieces made to be kept — thoughtfully sourced, honestly priced, and ready to ship.",
			"button_text": "Shop the collection", "button_link": "/products", "align": "left"}},
		{ID: "featured", Type: "product-grid", Enabled: true, Settings: map[string]any{
			"heading": "Featured pieces", "source": "collection", "limit": 8, "columns": 4, "view_all_link": "/products"}},
		{ID: "lookbook", Type: "image-with-text", Enabled: true, Settings: map[string]any{
			"eyebrow": "The Lookbook", "heading": "Made to be lived in", "image_side": "left",
			"body":        "Pieces designed for the everyday — versatile, durable, and quietly beautiful. Discover the season's edit.",
			"button_text": "Discover more", "button_link": "/products"}},
		{ID: "assurances", Type: "benefits", Enabled: true, Settings: map[string]any{"items": []any{
			map[string]any{"title": "Free shipping", "body": "On your qualifying orders"},
			map[string]any{"title": "Easy returns", "body": "7-day, hassle-free"},
			map[string]any{"title": "Secure checkout", "body": "Encrypted & protected"},
			map[string]any{"title": "Here to help", "body": "Support when you need it"},
		}}},
	}}
}
