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
	"cotton-crew-tee":     {5, "My new everyday tee", "Soft, holds its shape after washing, and the fit is just as described."},
	"hooded-sweatshirt":   {4, "Warm and comfortable", "Thick, cosy fleece. The sleeves run a little long, so I would size down."},
	"canvas-tote-bag":     {5, "Fits everything", "Strong straps and the inside pocket keeps my keys from getting lost."},
	"ceramic-mug":         {5, "Lovely mug", "A good weight in the hand and it keeps tea hot for a while."},
	"wool-beanie":         {4, "Warm, not itchy", "Keeps my ears warm on cold mornings and does not itch at all."},
	"linen-napkins":       {5, "Better with every wash", "They soften up nicely and look good on the table."},
	"leather-card-holder": {4, "Slim and well made", "Fits my cards without bulk. The leather was stiff at first but has softened."},
	"a5-notebook":         {5, "Lies flat", "The paper takes ink without bleeding through and the binding lies flat."},
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
