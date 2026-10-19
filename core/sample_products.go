package gocommerce

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"net/http"
	"path"
	"slices"
	"time"
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

// sampleProduct is one entry of the sample catalogue.
type sampleProduct struct {
	slug, title, productType, description string
	grams                                 int
	options                               []OptionInput
	variants                              []sampleVariant
	photos                                []samplePhoto
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

// samplePhoto is one picture of a sample product: a photograph under the
// Unsplash License, which allows this use without asking or crediting, and is
// credited here anyway (D96). colour names the option value it shows, so
// that colour's variants nominate it; empty shows the product as a whole.
type samplePhoto struct {
	file, colour, alt string
	// credit and source are the photographer and the photograph's page.
	credit, source string
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
		photos: []samplePhoto{
			{"cotton-crew-tee-black", "Black", "A man wearing a plain black crew-neck t-shirt",
				"Or Hakim", "https://unsplash.com/photos/XaPmwu1npkQ"},
			{"cotton-crew-tee-white", "White", "A plain white crew-neck t-shirt laid flat",
				"mockupbee", "https://unsplash.com/photos/4rUYuwJ2vGw"},
		},
	},
	{
		slug: "hooded-sweatshirt", title: "Hooded sweatshirt", productType: "Apparel", grams: 550,
		description: "A pullover hoodie in brushed cotton fleece, with a front pocket and a drawstring hood.",
		options:     []OptionInput{{Name: "Size", Values: []string{"S", "M", "L", "XL"}}},
		variants: []sampleVariant{
			{[]string{"S"}, 55, 6}, {[]string{"M"}, 55, 10}, {[]string{"L"}, 55, 10}, {[]string{"XL"}, 55, 4},
		},
		photos: []samplePhoto{{"hooded-sweatshirt", "", "A white hooded sweatshirt hanging on a rope",
			"Mediamodifier", "https://unsplash.com/photos/kJXGTOY1wLQ"}},
	},
	{
		slug: "canvas-tote-bag", title: "Canvas tote bag", productType: "Accessories", grams: 200,
		description: "A cotton canvas tote, 38 by 42 cm, with two shoulder straps and an inside pocket.",
		variants:    []sampleVariant{{nil, 18, 30}},
		photos: []samplePhoto{{"canvas-tote-bag", "", "A plain canvas tote bag on a grey background",
			"Brando Makes Branding", "https://unsplash.com/photos/smTDI-z1rlY"}},
	},
	{
		slug: "ceramic-mug", title: "Ceramic mug", productType: "Home", grams: 350,
		description: "A 350 ml ceramic mug with a white glaze.",
		variants:    []sampleVariant{{nil, 14, 34}},
		photos: []samplePhoto{{"ceramic-mug", "", "A white ceramic mug on a white table",
			"Vanesa Giaconi", "https://unsplash.com/photos/VVcggzlkYgE"}},
	},
	{
		slug: "wool-beanie", title: "Wool beanie", productType: "Accessories", grams: 90,
		description: "A warm knit beanie in a wool blend.",
		options:     []OptionInput{{Name: "Colour", Values: []string{"Grey", "Yellow"}}},
		variants:    []sampleVariant{{[]string{"Grey"}, 22, 15}, {[]string{"Yellow"}, 22, 9}},
		photos: []samplePhoto{
			{"wool-beanie-grey", "Grey", "A grey knit beanie with a turned-up cuff",
				"Rainier Watch", "https://unsplash.com/photos/2MMaMLOECjA"},
			{"wool-beanie-yellow", "Yellow", "A yellow cable-knit beanie with a bobble",
				"Kerim Ayar", "https://unsplash.com/photos/WWpJViNdatc"},
		},
	},
	{
		slug: "linen-napkins", title: "Linen napkins, set of four", productType: "Home", grams: 220,
		description: "Four 45 by 45 cm napkins in washed linen, hemmed on all sides.",
		variants:    []sampleVariant{{nil, 30, 12}},
		photos: []samplePhoto{{"linen-napkins", "", "Folded linen napkins stacked on a wooden stool",
			"Svitlana", "https://unsplash.com/photos/HbiRi1Owk9k"}},
	},
	{
		slug: "leather-card-holder", title: "Leather card holder", productType: "Accessories", grams: 40,
		description: "A slim leather card holder with four card slots and a centre pocket.",
		variants:    []sampleVariant{{nil, 35, 18}},
		photos: []samplePhoto{{"leather-card-holder", "", "Two black leather card holders on a grey surface",
			"Mason Supply", "https://unsplash.com/photos/-lN0HnySy7w"}},
	},
	{
		slug: "a5-notebook", title: "A5 notebook", productType: "Stationery", grams: 300,
		description: "An A5 notebook with 192 plain pages, a card cover and a binding that lies flat.",
		variants:    []sampleVariant{{nil, 12, 40}},
		photos: []samplePhoto{{"a5-notebook", "", "An open notebook with blank pages",
			"Brando Makes Branding", "https://unsplash.com/photos/TPoARluSmYs"}},
	},
}

// AddSampleProducts gives the store ctx is bound to the sample catalogue,
// active and stocked at its default location, and reports how many products it
// added. Each goes through CreateProduct, so each has its audit row and its
// product.created event like any other. A slug the store already uses is
// skipped, so a second run adds nothing.
func (c *Catalog) AddSampleProducts(ctx context.Context) (int, error) {
	scale := currencyScale(c.app.Currency(ctx))
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
		p, err := c.CreateProduct(ctx, in)
		if errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			return added, err
		}
		if err := c.app.media.attachSamplePhotos(ctx, p, s.photos); err != nil {
			return added, err
		}
		added++
	}
	return added, nil
}

// attachSamplePhotos gives a sample product its pictures, and each colour's
// variants the picture of that colour.
//
// Linked rather than uploaded: the engine serves the files from its own
// binary (handleSamplePhoto), so every install has them whether or not it has
// somewhere to put uploads, and a store's samples cost no disk.
func (m *Media) attachSamplePhotos(ctx context.Context, p *Product, photos []samplePhoto) error {
	ids := make([]int64, 0, len(photos))
	for _, ph := range photos {
		url := samplePhotoPrefix + ph.file + ".webp"
		item, err := m.insert(ctx, &MediaItem{
			Kind: MediaImage, URL: url, Alt: ph.alt, Filename: filenameForURL(url), sample: true,
		})
		if err != nil {
			return err
		}
		ids = append(ids, item.ID)
	}
	if err := m.SetProductMedia(ctx, p.ID, ids); err != nil {
		return err
	}
	for i, ph := range photos {
		if ph.colour == "" {
			continue
		}
		for _, v := range p.Variants {
			if slices.Contains(v.Options, ph.colour) {
				if err := m.SetVariantMedia(ctx, v.ID, []int64{ids[i]}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

//go:embed sample/*.webp
var samplePhotos embed.FS

// samplePhotoPrefix is where the engine serves the sample pictures. The 1 is
// their version: they are cached as immutable, so a changed picture takes a
// new number rather than leaving the old one in a shopper's cache.
const samplePhotoPrefix = "/sample-media/1/"

// handleSamplePhoto serves one sample picture from the binary. Public, like a
// store's own media, because a storefront shows it to anybody.
func (a *App) handleSamplePhoto(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	body, err := samplePhotos.ReadFile("sample/" + name)
	if err != nil || path.Ext(name) != ".webp" {
		RespondError(w, r, NotFoundf("no such picture"))
		return
	}
	w.Header().Set("Content-Type", "image/webp")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
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
	ids, err := c.app.listIDs(ctx, `SELECT id FROM products WHERE sample ORDER BY id`, "sample products")
	if err != nil {
		return 0, err
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
	// Their pictures go too: left behind they would fill the media library
	// with pictures of products the store no longer has. A picture the
	// merchant has put on a product of their own stays, and is theirs from
	// now on, so it neither blocks this erase nor waits for the next. A
	// picture they linked themselves was never marked, whatever its address.
	pictures, err := c.app.listIDs(ctx, `SELECT id FROM media WHERE sample ORDER BY id`, "sample pictures")
	if err != nil {
		return deleted, err
	}
	for _, id := range pictures {
		err := c.app.media.Delete(ctx, id)
		if errors.Is(err, ErrConflict) {
			if _, err := c.app.db.ExecContext(ctx, `UPDATE media SET sample = false WHERE id = $1`, id); err != nil {
				return deleted, Internalf(err, "hand over a sample picture")
			}
			continue
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			return deleted, err
		}
	}
	return deleted, nil
}

// listIDs runs a query of one id column and returns them, what naming them in
// an error.
func (a *App) listIDs(ctx context.Context, query, what string, args ...any) ([]int64, error) {
	rows, err := a.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, Internalf(err, "list %s", what)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, Internalf(err, "list %s", what)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, Internalf(err, "list %s", what)
	}
	return ids, nil
}
