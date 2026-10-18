package gocommerce

import (
	"context"
	"errors"
	"net/http"
)

// migration0068ProductSample marks the products the engine added as a sample
// catalogue (D94). A column rather than a metadata key: metadata is the
// merchant's to edit, and a flag they could edit away would either strand a
// sample the erase cannot find or file their own product for erasing. The
// partial index answers the Products screen's count, asked on every visit,
// without reading the catalogue.
const migration0068ProductSample = `
ALTER TABLE products ADD COLUMN sample boolean NOT NULL DEFAULT false;
CREATE INDEX products_sample_idx ON products (store_id) WHERE sample;
`

// sampleProduct is one entry of the sample catalogue. None has a picture: a
// missing asset is left absent rather than faked.
type sampleProduct struct {
	slug, title, productType, description string
	grams                                 int
	options                               []OptionInput
	variants                              []sampleVariant
}

// sampleVariant is priced in major units. AddSampleProducts scales them by
// the store's currency, so one table reads 25.00 in dollars, 25 in yen and
// 25.000 in dinars, where a fixed minor amount would read 2,500 yen and 2.500
// dinars.
type sampleVariant struct {
	options []string
	price   int64
	stock   int
}

var sampleCatalogue = []sampleProduct{
	{
		slug: "cotton-crew-tee", title: "Cotton crew tee", productType: "Apparel", grams: 180,
		description: "A short-sleeved crew-neck t-shirt in midweight cotton, cut to a regular fit.",
		options: []OptionInput{
			{Name: "Size", Values: []string{"S", "M", "L"}},
			{Name: "Colour", Values: []string{"Black", "White"}},
		},
		variants: []sampleVariant{
			{[]string{"S", "Black"}, 25, 12}, {[]string{"M", "Black"}, 25, 15}, {[]string{"L", "Black"}, 25, 8},
			{[]string{"S", "White"}, 25, 10}, {[]string{"M", "White"}, 25, 12}, {[]string{"L", "White"}, 25, 6},
		},
	},
	{
		slug: "hooded-sweatshirt", title: "Hooded sweatshirt", productType: "Apparel", grams: 550,
		description: "A pullover hoodie in brushed cotton fleece, with a front pocket and a drawstring hood.",
		options:     []OptionInput{{Name: "Size", Values: []string{"S", "M", "L", "XL"}}},
		variants: []sampleVariant{
			{[]string{"S"}, 55, 6}, {[]string{"M"}, 55, 10}, {[]string{"L"}, 55, 10}, {[]string{"XL"}, 55, 4},
		},
	},
	{
		slug: "canvas-tote-bag", title: "Canvas tote bag", productType: "Accessories", grams: 200,
		description: "A cotton canvas tote, 38 by 42 cm, with two shoulder straps and an inside pocket.",
		variants:    []sampleVariant{{nil, 18, 30}},
	},
	{
		slug: "ceramic-mug", title: "Ceramic mug", productType: "Home", grams: 350,
		description: "A 350 ml stoneware mug with a glazed finish.",
		options:     []OptionInput{{Name: "Colour", Values: []string{"White", "Sage"}}},
		variants:    []sampleVariant{{[]string{"White"}, 14, 20}, {[]string{"Sage"}, 14, 14}},
	},
	{
		slug: "wool-beanie", title: "Wool beanie", productType: "Accessories", grams: 90,
		description: "A ribbed knit beanie in a wool blend, with a turned-up cuff.",
		options:     []OptionInput{{Name: "Colour", Values: []string{"Charcoal", "Mustard"}}},
		variants:    []sampleVariant{{[]string{"Charcoal"}, 22, 15}, {[]string{"Mustard"}, 22, 9}},
	},
	{
		slug: "linen-napkins", title: "Linen napkins, set of four", productType: "Home", grams: 220,
		description: "Four 45 by 45 cm napkins in washed linen, hemmed on all sides.",
		variants:    []sampleVariant{{nil, 30, 12}},
	},
	{
		slug: "leather-card-holder", title: "Leather card holder", productType: "Accessories", grams: 40,
		description: "A slim card holder with four card slots and a centre pocket.",
		variants:    []sampleVariant{{nil, 35, 18}},
	},
	{
		slug: "a5-dotted-notebook", title: "A5 dotted notebook", productType: "Stationery", grams: 300,
		description: "An A5 notebook with 192 dotted pages, a card cover and a binding that lies flat.",
		variants:    []sampleVariant{{nil, 12, 40}},
	},
}

// AddSampleProducts gives the store ctx is bound to the sample catalogue,
// active and stocked at its default location, and reports how many products it
// added. Each goes through CreateProduct, so each has its audit row and its
// product.created event like any other. A slug the store already uses is
// skipped, so a second run adds nothing.
func (c *Catalog) AddSampleProducts(ctx context.Context) (int, error) {
	scale := int64(1)
	for range currencyExponent(c.app.Currency(ctx)) {
		scale *= 10
	}
	added := 0
	for _, s := range sampleCatalogue {
		in := ProductInput{
			Slug: s.slug, Title: s.title, Description: s.description, Status: ProductActive,
			ProductType: s.productType, Options: s.options, sample: true,
		}
		for _, v := range s.variants {
			stock, grams := v.stock, s.grams
			in.Variants = append(in.Variants, VariantInput{
				Options: v.options, PriceMinor: v.price * scale, StockOnHand: &stock, WeightGrams: &grams,
			})
		}
		// SKUs are left to be derived, and a derived one walks past a taken
		// code, so the one conflict left is the slug.
		_, err := c.CreateProduct(ctx, in)
		if errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			return added, err
		}
		added++
	}
	return added, nil
}

// SampleProductCount is how many sample products the store still has, edited
// or not.
func (c *Catalog) SampleProductCount(ctx context.Context) (int, error) {
	var n int
	if err := c.app.db.QueryRowContext(ctx, `SELECT count(*) FROM products WHERE sample`).Scan(&n); err != nil {
		return 0, Internalf(err, "count sample products")
	}
	return n, nil
}

// DeleteSampleProducts erases every sample product, edited or not, and reports
// how many it removed. Each goes through DeleteProduct, so each has its audit
// row and its product.deleted event, and an order that sold one keeps its
// lines. A failure part way leaves the rest for the next run.
func (c *Catalog) DeleteSampleProducts(ctx context.Context) (int, error) {
	rows, err := c.app.db.QueryContext(ctx, `SELECT id FROM products WHERE sample ORDER BY id`)
	if err != nil {
		return 0, Internalf(err, "list sample products")
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, Internalf(err, "scan sample product")
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, Internalf(err, "list sample products")
	}
	deleted := 0
	for _, id := range ids {
		err := c.DeleteProduct(ctx, id)
		if errors.Is(err, ErrNotFound) {
			continue // somebody deleted it since the list was read
		}
		if err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

func (a *App) handleSampleProductCount(w http.ResponseWriter, r *http.Request) {
	n, err := a.catalog.SampleProductCount(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, map[string]int{"count": n})
}

func (a *App) handleDeleteSampleProducts(w http.ResponseWriter, r *http.Request) {
	n, err := a.catalog.DeleteSampleProducts(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, map[string]int{"deleted": n})
}
