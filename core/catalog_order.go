package gocommerce

import (
	"context"
	"database/sql"
	"strconv"
)

// ReorderProducts puts products in the order given, among the places they
// already hold in the list (D272). The places are the list's own: the panel
// sends the rows it shows, dragged, and only those rows trade places, so a
// page, a filtered list or a search reorders what it shows and nothing else.
//
// The first reorder numbers the whole catalogue in the order it is listed in,
// newest first, so the places exist; after that only the rows named move.
func (c *Catalog) ReorderProducts(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return Validationf("ids is required")
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			return Validationf("product %d is listed twice", id)
		}
		seen[id] = true
	}
	return InTx(ctx, c.app.db, func(tx *sql.Tx) error {
		// One reorder at a time per store, so two operators dragging at once
		// cannot interleave their numberings.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('products.position:' || coalesce(current_setting('gocommerce.store_id', true), '')))`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE products p SET position = r.n
			FROM (SELECT id, row_number() OVER (ORDER BY position ASC NULLS FIRST, id DESC) AS n FROM products) r
			WHERE p.id = r.id AND EXISTS (SELECT 1 FROM products WHERE position IS NULL)`); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx,
			`SELECT position FROM products WHERE id = ANY($1) ORDER BY position, id DESC`, int64Array(ids))
		if err != nil {
			return err
		}
		var places []int
		for rows.Next() {
			var p int
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return err
			}
			places = append(places, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(places) != len(ids) {
			return NotFoundf("a product in the list does not exist")
		}
		for i, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE products SET position = $1 WHERE id = $2`, places[i], id); err != nil {
				return err
			}
		}
		return nil
	})
}

// DuplicateProduct makes a draft copy of a product: its words, organisation,
// options, variants and prices, its pictures and its collections, with no stock
// and SKUs of its own (D272). Made through CreateProduct, so it is announced and
// audited as any new product is.
func (c *Catalog) DuplicateProduct(ctx context.Context, id int64) (*Product, error) {
	p, err := c.GetProduct(ctx, id)
	if err != nil {
		return nil, err
	}
	in := ProductInput{
		Title: p.Title + " (copy)", Description: p.Description, Status: ProductDraft,
		ProductType: p.ProductType, Vendor: p.Vendor, Tags: p.Tags,
		SEOTitle: p.SEOTitle, SEODescription: p.SEODescription, Metadata: p.Metadata,
	}
	if p.Category != nil {
		in.CategoryID = &p.Category.ID
	}
	if in.Slug, err = c.freeSlug(ctx, slugify(in.Title)); err != nil {
		return nil, err
	}
	for _, o := range p.Options {
		opt := OptionInput{Name: o.Name}
		for _, v := range o.Values {
			opt.Values = append(opt.Values, v.Value)
		}
		in.Options = append(in.Options, opt)
	}
	for _, v := range p.Variants {
		cost := (*int64)(nil)
		if v.Cost != nil {
			cost = &v.Cost.AmountMinor
		}
		compare := (*int64)(nil)
		if v.CompareAtPrice != nil {
			compare = &v.CompareAtPrice.AmountMinor
		}
		// SKU and barcode left empty: each names one sellable thing, and the
		// engine derives a free SKU from the new slug.
		in.Variants = append(in.Variants, VariantInput{
			PriceMinor: v.Price.AmountMinor, CompareAtPriceMinor: compare, CostMinor: cost,
			Taxable: &v.Taxable, RequiresShipping: &v.RequiresShipping, GiftCard: &v.GiftCard,
			Options: v.Options, TrackInventory: &v.TrackInventory, ContinueSelling: &v.ContinueSelling,
			Active: &v.Active, OriginCountry: v.OriginCountry, HSCode: v.HSCode,
			WeightGrams: v.WeightGrams, WeightUnit: v.WeightUnit,
			Dimensions: v.Dimensions, DimensionUnit: v.DimensionUnit, Metadata: v.Metadata,
		})
	}
	dup, err := c.CreateProduct(ctx, in)
	if err != nil {
		return nil, err
	}
	media, err := c.app.media.ForProduct(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(media) > 0 {
		ids := make([]int64, len(media))
		for i, m := range media {
			ids[i] = m.ID
		}
		if err := c.app.media.SetProductMedia(ctx, dup.ID, ids); err != nil {
			return nil, err
		}
	}
	if len(p.Collections) > 0 {
		ids := make([]int64, len(p.Collections))
		for i, col := range p.Collections {
			ids[i] = col.ID
		}
		if err := c.app.Collections().SetProductCollections(ctx, dup.ID, ids); err != nil {
			return nil, err
		}
	}
	return c.GetProduct(ctx, dup.ID)
}

// freeSlug is base, or base-2, base-3 … whichever no product has yet.
func (c *Catalog) freeSlug(ctx context.Context, base string) (string, error) {
	if base == "" {
		base = "product"
	}
	for n := 1; ; n++ {
		slug := base
		if n > 1 {
			slug += "-" + strconv.Itoa(n)
		}
		var taken bool
		if err := c.app.db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM products WHERE slug = $1)`, slug).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			return slug, nil
		}
	}
}
