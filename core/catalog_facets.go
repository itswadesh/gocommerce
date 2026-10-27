package gocommerce

import (
	"context"
	"fmt"
	"strings"
)

// ProductFacets counts what a listing could be narrowed by, over every product
// the listing matches rather than the page it returned: counts made from one
// page of hits would count that page, not the shop.
//
// Each group is counted with its own selection left out and every other filter
// kept (disjunctive counting). Ticking "Acme" must not collapse the vendor list
// to Acme alone, or the shopper could never tick a second vendor; but the
// vendor list must still narrow when they tick a tag. The price range is the
// same rule applied to the slider: it spans the listing without its own price
// bounds, so dragging it does not shrink the track under the shopper's hand.
type ProductFacets struct {
	// Price is nil when nothing matches, rather than a range from zero to zero.
	Price        *PriceRange             `json:"price"`
	Tags         []FacetValue            `json:"tags"`
	Vendors      []FacetValue            `json:"vendors"`
	ProductTypes []FacetValue            `json:"product_types"`
	Categories   []CategoryFacet         `json:"categories"`
	Attributes   map[string][]FacetValue `json:"attributes"`
}

// FacetValue is one value a group can take and how many products have it.
type FacetValue struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// CategoryFacet counts the products filed directly under one category.
type CategoryFacet struct {
	ID    int64  `json:"id"`
	Slug  string `json:"slug"`
	Title string `json:"title"`
	Count int    `json:"count"`
}

// PriceRange is the cheapest and dearest "from" price in a listing.
type PriceRange struct {
	Min Money `json:"min"`
	Max Money `json:"max"`
}

// facetLimit caps each group. ponytail: a shop with more distinct tags than
// this shows the most common ones; page through them if one ever needs all.
const facetLimit = 100

// ProductFacets counts the facets of the listing q describes. Paging and
// sorting in q are ignored.
func (c *Catalog) ProductFacets(ctx context.Context, q ProductQuery) (*ProductFacets, error) {
	out := &ProductFacets{Attributes: map[string][]FacetValue{}}
	var err error

	without := q
	without.Tags = nil
	if out.Tags, err = c.facetValues(ctx, without, " CROSS JOIN LATERAL unnest(p.tags) AS f(value)", "f.value", ""); err != nil {
		return nil, err
	}
	without = q
	without.Vendors = nil
	if out.Vendors, err = c.facetValues(ctx, without, "", "p.vendor", "p.vendor <> ''"); err != nil {
		return nil, err
	}
	without = q
	without.ProductTypes = nil
	if out.ProductTypes, err = c.facetValues(ctx, without, "", "p.product_type", "p.product_type <> ''"); err != nil {
		return nil, err
	}

	// Unselected attributes are counted in one pass under the whole query; each
	// selected one again without its own filter.
	if err := c.attributeFacets(ctx, q, "", out.Attributes); err != nil {
		return nil, err
	}
	for i, f := range q.Attributes {
		without = q
		without.Attributes = append(append([]AttributeFilter{}, q.Attributes[:i]...), q.Attributes[i+1:]...)
		delete(out.Attributes, f.Key)
		if err := c.attributeFacets(ctx, without, f.Key, out.Attributes); err != nil {
			return nil, err
		}
	}

	if out.Categories, err = c.categoryFacets(ctx, q); err != nil {
		return nil, err
	}
	without = q
	without.PriceMinMinor, without.PriceMaxMinor = nil, nil
	if out.Price, err = c.priceFacet(ctx, without); err != nil {
		return nil, err
	}
	return out, nil
}

// facetValues counts one text-valued group. lateral may add rows per product;
// value is the expression grouped on.
func (c *Catalog) facetValues(ctx context.Context, q ProductQuery, lateral, value, extra string) ([]FacetValue, error) {
	join, where, args := productFilters(q, nil)
	if extra != "" {
		where = append(where, extra)
	}
	rows, err := c.app.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT %s, count(*) FROM products p%s%s WHERE %s GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT %d`,
		value, join, lateral, strings.Join(where, " AND "), facetLimit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FacetValue{}
	for rows.Next() {
		var v FacetValue
		if err := rows.Scan(&v.Value, &v.Count); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// attributeFacets adds counts per attribute handle to into. A non-empty only
// restricts the pass to that one handle.
//
// The CASEs keep a hand-edited metadata document that is not the expected
// `{handle: [values]}` shape from failing the whole listing: jsonb_each and
// jsonb_array_elements_text raise on anything else.
func (c *Catalog) attributeFacets(ctx context.Context, q ProductQuery, only string, into map[string][]FacetValue) error {
	join, where, args := productFilters(q, nil)
	selected := map[string]bool{}
	for _, f := range q.Attributes {
		selected[f.Key] = true
	}
	if only != "" {
		args = append(args, only)
		where = append(where, fmt.Sprintf("a.key = $%d", len(args)))
	}
	rows, err := c.app.db.QueryContext(ctx, `SELECT a.key, v.value, count(*) FROM products p`+join+`
		CROSS JOIN LATERAL jsonb_each(CASE WHEN jsonb_typeof(p.metadata->'category') = 'object'
			THEN p.metadata->'category' ELSE '{}'::jsonb END) AS a(key, vals)
		CROSS JOIN LATERAL jsonb_array_elements_text(CASE WHEN jsonb_typeof(a.vals) = 'array'
			THEN a.vals ELSE '[]'::jsonb END) AS v(value)
		WHERE `+strings.Join(where, " AND ")+`
		GROUP BY 1, 2 ORDER BY 1, 3 DESC, 2`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var v FacetValue
		if err := rows.Scan(&key, &v.Value, &v.Count); err != nil {
			return err
		}
		// The whole-query pass skips selected handles; their own pass counts them.
		if only == "" && selected[key] {
			continue
		}
		if strings.TrimSpace(v.Value) != "" && len(into[key]) < facetLimit {
			into[key] = append(into[key], v)
		}
	}
	return rows.Err()
}

func (c *Catalog) categoryFacets(ctx context.Context, q ProductQuery) ([]CategoryFacet, error) {
	join, where, args := productFilters(q, nil)
	rows, err := c.app.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT c.id, c.slug, c.title, count(*) FROM products p%s JOIN categories c ON c.id = p.category_id
		WHERE %s GROUP BY 1, 2, 3 ORDER BY 4 DESC, 3 LIMIT %d`,
		join, strings.Join(where, " AND "), facetLimit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CategoryFacet{}
	for rows.Next() {
		var f CategoryFacet
		if err := rows.Scan(&f.ID, &f.Slug, &f.Title, &f.Count); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (c *Catalog) priceFacet(ctx context.Context, q ProductQuery) (*PriceRange, error) {
	join, where, args := productFilters(q, nil)
	var lo, hi *int64
	if err := c.app.db.QueryRowContext(ctx,
		`SELECT min(`+productMinPrice+`), max(`+productMinPrice+`) FROM products p`+join+
			` WHERE `+strings.Join(where, " AND "), args...).Scan(&lo, &hi); err != nil {
		return nil, err
	}
	if lo == nil || hi == nil {
		return nil, nil
	}
	currency := c.app.Currency(ctx)
	return &PriceRange{Min: Money{*lo, currency}, Max: Money{*hi, currency}}, nil
}
