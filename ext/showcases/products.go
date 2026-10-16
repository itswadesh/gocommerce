package showcases

import (
	"context"
	"errors"
	"net/http"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// ProductSummary is a product as a showcase lists it: enough for a card and a
// quick add to the cart, and nothing a buyer should not see — no cost, no
// stock count, no metadata. The product's own page has the rest.
type ProductSummary struct {
	ID    int64  `json:"id"`
	Slug  string `json:"slug"`
	Title string `json:"title"`
	// Status is the editor's: a draft product is attached but not shown.
	Status   string `json:"status,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	// Price and CompareAtPrice are the default variant's. A price list
	// applies in the cart, against an address the buyer has proven (D66),
	// so a showcase, which knows nobody, shows the store's own price.
	Price          *gocommerce.Money `json:"price"`
	CompareAtPrice *gocommerce.Money `json:"compare_at_price,omitempty"`
	// Available is whether any variant can be bought now, by the same rule
	// the cart applies.
	Available    bool  `json:"available"`
	VariantCount int   `json:"variant_count"`
	VariantID    int64 `json:"variant_id,omitempty"`
}

func summarize(p *gocommerce.Product, withStatus bool) ProductSummary {
	out := ProductSummary{ID: p.ID, Slug: p.Slug, Title: p.Title, ImageURL: p.ImageURL}
	if withStatus {
		out.Status = p.Status
	}
	for _, v := range p.Variants {
		if !v.Active {
			continue
		}
		out.VariantCount++
		if !v.TrackInventory || v.ContinueSelling || v.Available > 0 {
			out.Available = true
		}
	}
	if dv := p.DefaultVariant(); dv != nil {
		price := dv.Price
		out.Price, out.CompareAtPrice, out.VariantID = &price, dv.CompareAtPrice, dv.ID
	}
	return out
}

// shown is what a showcase shows a buyer: its collection's products or its
// own list's, in the store's order, only those active and carried by the
// request's channel, in the request's language.
func (m *Module) shown(r *http.Request, s *Showcase) ([]*gocommerce.Product, error) {
	ctx := r.Context()
	channel, err := m.app.Channels().Resolve(ctx, r.URL.Query().Get("channel"))
	if err != nil {
		return nil, err
	}
	var channelID int64
	if channel != nil {
		channelID = channel.ID
	}
	var out []*gocommerce.Product
	if s.CollectionID != nil {
		out, _, err = m.app.Collections().ProductsInCollection(ctx, *s.CollectionID, gocommerce.ProductQuery{
			Status: gocommerce.ProductActive, ChannelID: channelID, Limit: MaxProducts,
		})
		if err != nil {
			return nil, err
		}
	} else {
		ids, err := m.sellable(ctx, s.ProductIDs, channelID)
		if err != nil {
			return nil, err
		}
		if out, err = m.load(ctx, ids); err != nil {
			return nil, err
		}
	}
	if out == nil {
		out = []*gocommerce.Product{}
	}
	m.app.TranslateProducts(r, out)
	return out, nil
}

// sellable is the ids, in their order, of the products that are active and,
// when there is a channel, published to it or to none in particular — the
// rule core's own catalogue reads by.
func (m *Module) sellable(ctx context.Context, ids []int64, channel int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT p.id FROM products p
		WHERE p.id = ANY($1::bigint[]) AND p.status = 'active'
		  AND ($2 = 0
		       OR EXISTS (SELECT 1 FROM product_channels pc WHERE pc.product_id = p.id AND pc.channel_id = $2)
		       OR NOT EXISTS (SELECT 1 FROM product_channels pc WHERE pc.product_id = p.id))`,
		int64Set(ids), channel)
	if err != nil {
		return nil, gocommerce.Internalf(err, "read the showcase's products")
	}
	defer rows.Close()
	ok := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, gocommerce.Internalf(err, "read the showcase's products")
		}
		ok[id] = true
	}
	out := make([]int64, 0, len(ok))
	for _, id := range ids {
		if ok[id] {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

// load reads products in the order given, skipping any deleted since.
func (m *Module) load(ctx context.Context, ids []int64) ([]*gocommerce.Product, error) {
	out := make([]*gocommerce.Product, 0, len(ids))
	for _, id := range ids {
		p, err := m.app.Products().GetProduct(ctx, id)
		if errors.Is(err, gocommerce.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// forEditor fills what the editor shows beside the showcase: what it holds,
// and what was picked before a collection replaced the picks.
func (m *Module) forEditor(ctx context.Context, s *Showcase) error {
	var err error
	if s.Products, err = m.attached(ctx, s); err != nil || s.CollectionID == nil || len(s.ProductIDs) == 0 {
		return err
	}
	picked, err := m.load(ctx, s.ProductIDs)
	for _, p := range picked {
		s.Picked = append(s.Picked, summarize(p, true))
	}
	return err
}

// attached is what the editor lists: everything the showcase holds, whatever
// its status, so a draft product can be seen and published.
func (m *Module) attached(ctx context.Context, s *Showcase) ([]ProductSummary, error) {
	var products []*gocommerce.Product
	var err error
	if s.CollectionID != nil {
		products, _, err = m.app.Collections().ProductsInCollection(ctx, *s.CollectionID,
			gocommerce.ProductQuery{Limit: MaxProducts})
	} else {
		products, err = m.load(ctx, s.ProductIDs)
	}
	if err != nil {
		return nil, err
	}
	out := make([]ProductSummary, 0, len(products))
	for _, p := range products {
		out = append(out, summarize(p, true))
	}
	return out, nil
}
