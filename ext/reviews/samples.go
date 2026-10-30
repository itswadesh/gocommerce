package reviews

import (
	"context"
	"slices"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// The sample shop's reviews (D96): one on each sample product, written by a
// sample customer whose sample order bought it, so each is marked verified by
// the same test a real review passes. They are samples: the merchant erases
// them with the rest before opening, since a review nobody wrote is not one
// to show a real shopper.

// migrationSample marks the reviews the sample shop added, by a column no
// request sets, so erasing them never takes a real shopper's.
const migrationSample = `ALTER TABLE reviews ADD COLUMN sample boolean NOT NULL DEFAULT false;`

var sampleReviews = map[string]struct {
	rating      int
	title, body string
}{
	"printed-anarkali-kurta":   {5, "Beautiful flare", "The skirt flares nicely when I walk and the side ties sit neatly. The print is just as in the photos."},
	"printed-yoke-kurta":       {4, "Easy everyday kurta", "Comfortable, and the embroidery on the yoke is neat. The sleeves are a touch long on me."},
	"floral-co-ord-set":        {5, "Wore it all weekend", "Soft fabric, and the elasticated waist makes the trousers easy to wear. The lace on the placket is a nice touch."},
	"ikat-print-co-ord-set":    {5, "Love the print", "A bold print, wide trousers that move well, and the collar keeps it looking smart."},
	"paisley-print-jumpsuit":   {4, "Comfortable and smart", "The tie belt gives it shape. I would size down if you are between sizes."},
	"satin-saree":              {5, "Lovely sheen", "It drapes well and the border catches the light. The blouse piece matches perfectly."},
	"embroidered-lehenga-set":  {5, "Wore it to a wedding", "The embroidery is fine and even, the dupatta is light, and the skirt has a lovely flare."},
	"floral-tiered-maxi-dress": {4, "Pretty and light", "The tiers sit nicely and it is easy to wear on a warm day. A little long, so I wear it with heels."},
	"mens-embroidered-kurta":   {5, "Smart for festivals", "The tonal embroidery looks rich without being loud, and the straight fit is as described."},
	"mens-nehru-jacket":        {5, "Lifts a plain kurta", "Well finished, with a soft lining. It dresses up a plain kurta for an evening out."},
	"mens-kurta-pyjama-set":    {4, "Comfortable set", "Easy to wear, and the colour is just as shown. The pyjama waist is comfortable all day."},
}

// SampleRights implements gocommerce.SampleSeeder: the samples are published
// reviews, which is moderating.
func (m *Module) SampleRights() []gocommerce.Right {
	return []gocommerce.Right{rightReviewsModerate}
}

// AddSamples implements gocommerce.SampleSeeder. A product that already has a
// sample review is skipped.
func (m *Module) AddSamples(ctx context.Context, shop *gocommerce.SampleShop) (gocommerce.SampleCounts, error) {
	added := 0
	for i, p := range shop.Products {
		r, ok := sampleReviews[p.Slug]
		if !ok {
			continue
		}
		var author *gocommerce.SampleCustomer
		var bought time.Time
		for j := range shop.Customers {
			k := slices.IndexFunc(shop.Customers[j].Bought,
				func(b gocommerce.SamplePurchase) bool { return b.Slug == p.Slug })
			if k >= 0 {
				author, bought = &shop.Customers[j], shop.Customers[j].Bought[k].At
				break
			}
		}
		if author == nil {
			continue
		}
		email := strings.ToLower(author.Email)
		verified, err := m.bought(ctx, email, p.ID)
		if err != nil {
			return gocommerce.SampleCounts{"reviews": added}, gocommerce.Internalf(err, "add the sample reviews")
		}
		// Written a day or so after the order that bought it, and never in the
		// future: an order placed in the last day is reviewed an hour ago.
		at := bought.Add(time.Duration(20+i%4*6) * time.Hour)
		if latest := time.Now().UTC().Add(-time.Hour); at.After(latest) {
			at = latest
		}
		res, err := m.db.ExecContext(ctx, `
			INSERT INTO reviews (product_id, name, email, rating, title, body, status, verified, created_at, updated_at, sample)
			SELECT $1, $2, $3, $4, $5, $6, 'approved', $7, $8, $8, true
			WHERE NOT EXISTS (SELECT 1 FROM reviews WHERE product_id = $1 AND sample)`,
			p.ID, shortName(author.Name), email, r.rating, r.title, r.body, verified, at)
		if err != nil {
			return gocommerce.SampleCounts{"reviews": added}, gocommerce.Internalf(err, "add the sample reviews")
		}
		n, _ := res.RowsAffected()
		added += int(n)
	}
	return gocommerce.SampleCounts{"reviews": added}, nil
}

// shortName is a first name and an initial, as reviews are signed.
func shortName(full string) string {
	parts := strings.Fields(full)
	if len(parts) < 2 {
		return full
	}
	return parts[0] + " " + parts[len(parts)-1][:1] + "."
}

// CountSamples implements gocommerce.SampleSeeder.
func (m *Module) CountSamples(ctx context.Context) (gocommerce.SampleCounts, error) {
	var n int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM reviews WHERE sample`).Scan(&n); err != nil {
		return nil, gocommerce.Internalf(err, "count the sample reviews")
	}
	return gocommerce.SampleCounts{"reviews": n}, nil
}

// EraseSamples implements gocommerce.SampleSeeder.
func (m *Module) EraseSamples(ctx context.Context) (gocommerce.SampleCounts, error) {
	res, err := m.db.ExecContext(ctx, `DELETE FROM reviews WHERE sample`)
	if err != nil {
		return nil, gocommerce.Internalf(err, "erase the sample reviews")
	}
	n, _ := res.RowsAffected()
	return gocommerce.SampleCounts{"reviews": int(n)}, nil
}
