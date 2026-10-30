package gocommerce

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"slices"
	"strings"
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

// The sample catalogue (D159) is data, not code: core/sample/catalogue.json,
// which scripts/seed-fashion.ps1 and the photo scripts read as well. Eleven
// garments, each in three or four colours and every size, filed in a tree of
// categories and two collections.
//
//go:embed sample/catalogue.json
var sampleCatalogueFile []byte

var sampleCatalogue = func() (c struct {
	// Currency is what PriceMinor and CompareAtMinor are in.
	Currency    string
	Categories  []struct{ Slug, Title, Parent, Picture string }
	Collections []struct{ Slug, Title, Description string }
	Products    []sampleProduct
}) {
	if err := json.Unmarshal(sampleCatalogueFile, &c); err != nil {
		panic("core/sample/catalogue.json: " + err.Error())
	}
	return c
}()

// sampleProduct is one garment of the catalogue. The file carries more than
// this — SKU codes, colour swatches, where each photograph came from — for
// the scripts that read it.
type sampleProduct struct {
	Slug, Title, Category, Description, Badge string
	Collections, Highlights, Sizes            []string
	HSCode                                    string `json:"hs_code"`
	WeightGrams                               int    `json:"weight_grams"`
	// In the catalogue's currency (minor units), and a face value in whole
	// dollars for every other currency (sampleProduct.prices).
	PriceMinor     int64 `json:"price_minor"`
	CompareAtMinor int64 `json:"compare_at_minor"`
	PriceUSD       int64 `json:"price_usd"`
	CompareAtUSD   int64 `json:"compare_at_usd"`
	Colours        []struct{ Name string }
	// Photos are the shots each colour has, by name: "front", "side", "detail".
	Photos []struct{ Shot string }
	// SoldOut and Low name a colour and size, "Mustard/XL".
	SoldOut []string `json:"sold_out"`
	Low     []string
}

// prices is a variant's price and compare-at price (nil for none) in the
// store's currency. A store selling in the catalogue's currency gets its
// prices. Any other gets the dollar figure as a face value in its own major
// units, as D94's table did: 45 is 45.00 dollars or euros, 45 yen and 45.000
// dinars, which is not a market price anywhere but never reads 4,500 yen for
// a kurta priced in rupees.
func (s sampleProduct) prices(currency string) (int64, *int64) {
	price, compare := s.PriceMinor, s.CompareAtMinor
	if currency != sampleCatalogue.Currency {
		scale := currencyScale(currency)
		price, compare = s.PriceUSD*scale, s.CompareAtUSD*scale
	}
	if compare == 0 {
		return price, nil
	}
	return price, &compare
}

// sampleSizeStock is how many of a size each colour starts with: the middle
// sizes deepest, as a shop would buy them.
var sampleSizeStock = map[string]int{"XS": 6, "S": 12, "M": 16, "L": 14, "XL": 9, "XXL": 5, "Free size": 18}

// stock is what a colour and size start with: none for the ones the
// catalogue names sold out and two for the ones running low, so a storefront
// has both states to show.
func (s sampleProduct) stock(colour, size string) int {
	switch key := colour + "/" + size; {
	case slices.Contains(s.SoldOut, key):
		return 0
	case slices.Contains(s.Low, key):
		return 2
	}
	return sampleSizeStock[size]
}

// samplePhotoName is the file of one colour's shot in the current set, named
// as scripts/fashion-images/build-sample.mjs writes it.
func samplePhotoName(product, colour, shot string) string {
	return product + "-" + slugify(colour) + "-" + shot + ".webp"
}

// samplePhotoAlt describes a shot as seed-fashion.ps1 does: "Printed anarkali
// kurta in rose, front view".
func samplePhotoAlt(title, colour, shot string) string {
	view := shot + " view"
	if shot == "detail" {
		view = "close-up"
	}
	return title + " in " + strings.ToLower(colour) + ", " + view
}

// AddSampleProducts gives the store ctx is bound to the sample catalogue,
// active and stocked at its default location, and reports how many products it
// added. Each goes through CreateProduct, so each has its audit row and its
// product.created event like any other. A slug the store already uses is
// skipped, so a second run adds nothing.
func (c *Catalog) AddSampleProducts(ctx context.Context) (int, error) {
	currency := c.app.Currency(ctx)
	added := 0
	for _, s := range sampleCatalogue.Products {
		metadata := Metadata{MetaHighlights: s.Highlights}
		if s.Badge != "" {
			metadata[MetaBadge] = s.Badge
		}
		colours := make([]string, len(s.Colours))
		for i, colour := range s.Colours {
			colours[i] = colour.Name
		}
		in := ProductInput{
			Slug: s.Slug, Title: s.Title, Description: s.Description, Status: ProductActive,
			ProductType: "Apparel", Metadata: metadata, sample: true,
			// Colour first: it is the choice that changes the pictures.
			Options: []OptionInput{{Name: "Colour", Values: colours}, {Name: "Size", Values: s.Sizes}},
		}
		price, compare := s.prices(currency)
		for _, colour := range colours {
			for _, size := range s.Sizes {
				stock, grams := s.stock(colour, size), s.WeightGrams
				in.Variants = append(in.Variants, VariantInput{
					Options: []string{colour, size}, PriceMinor: price, CompareAtPriceMinor: compare,
					StockOnHand: &stock, WeightGrams: &grams, HSCode: s.HSCode,
				})
			}
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
		if err := c.app.media.attachSamplePhotos(ctx, p, s); err != nil {
			return added, err
		}
		added++
	}
	return added, nil
}

// attachSamplePhotos gives a sample product every colour's photographs, and
// each variant its own colour's, which is what lets a storefront swap the
// gallery as a shopper picks a colour.
//
// Linked rather than uploaded: the engine serves the files from its own
// binary (handleSamplePhoto), so every install has them whether or not it has
// somewhere to put uploads, and a store's samples cost no disk.
func (m *Media) attachSamplePhotos(ctx context.Context, p *Product, s sampleProduct) error {
	var all []int64
	byColour := map[string][]int64{}
	for _, colour := range s.Colours {
		for _, photo := range s.Photos {
			url := SampleMediaPrefix + samplePhotoName(s.Slug, colour.Name, photo.Shot)
			item, err := m.insert(ctx, &MediaItem{
				Kind: MediaImage, URL: url, Alt: samplePhotoAlt(s.Title, colour.Name, photo.Shot),
				Filename: filenameForURL(url), sample: true,
			})
			if err != nil {
				return err
			}
			all = append(all, item.ID)
			byColour[colour.Name] = append(byColour[colour.Name], item.ID)
		}
	}
	if err := m.SetProductMedia(ctx, p.ID, all); err != nil {
		return err
	}
	// Every variant's in one statement, rather than SetVariantMedia's audited
	// transaction per variant: fifteen audit rows saying a product's pictures
	// changed, moments after the one saying it was made, and most of the time
	// it takes to add the sample shop.
	var variants, media, positions []int64
	for _, v := range p.Variants {
		for colour, ids := range byColour {
			if slices.Contains(v.Options, colour) {
				for i, id := range ids {
					variants, media, positions = append(variants, v.ID), append(media, id), append(positions, int64(i))
				}
			}
		}
	}
	if _, err := m.app.db.ExecContext(ctx, `
		INSERT INTO variant_media (variant_id, media_id, position)
		SELECT * FROM unnest($1::bigint[], $2::bigint[], $3::bigint[])`,
		int64Array(variants), int64Array(media), int64Array(positions)); err != nil {
		return Internalf(err, "show the sample variants their pictures")
	}
	return nil
}

//go:embed sample/1/*.webp sample/2/*.webp
var samplePhotos embed.FS

// SampleMediaPrefix is where the engine serves the sample shop's pictures,
// for a module whose samples show them. The number is the set. Its files are
// cached as immutable and linked from stores' sample shops, so a set is never
// changed or removed, and a different picture goes in a new set. Set 1 is
// D96's, which new stores no longer get; it stays for the stores that added
// the sample shop before D159, and for a merchant who put one of its photos
// on a product of their own, which D96 made theirs.
const SampleMediaPrefix = "/sample-media/2/"

// handleSamplePhoto serves one sample picture from the binary. Public, like a
// store's own media, because a storefront shows it to anybody.
func (a *App) handleSamplePhoto(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	body, err := samplePhotos.ReadFile("sample/" + r.PathValue("set") + "/" + name)
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
