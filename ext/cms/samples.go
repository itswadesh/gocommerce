package cms

import (
	"context"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// The sample shop's pages (D96): the three a shopper looks for before buying,
// for the merchant to replace with their own — the erase takes them even when
// edited. They promise nothing a store has not decided, no number of days and
// no time to answer, since a sample left published would speak for the shop.

// migrationSample marks the pages the sample shop added, by a column no
// request sets, so erasing them never takes the shop's own.
const migrationSample = `ALTER TABLE cms_pages ADD COLUMN sample boolean NOT NULL DEFAULT false;`

var samplePages = []struct{ slug, title, excerpt, body string }{
	{"about", "About us", "Who we are and what we sell.",
		"We are a small shop selling everyday clothing, accessories and things for the home.\n\n" +
			"Questions about an order or a product? Write to us from the contact page."},
	{"shipping", "Shipping and delivery", "How your order reaches you.",
		"The checkout shows every delivery option for your address, with its price, before you pay.\n\n" +
			"Questions about a delivery? Write to us from the contact page with your order number."},
	{"returns", "Returns", "Sending something back.",
		"If something is not right, write to us from the contact page with your order number, and we will tell you what happens next."},
}

// SampleRights implements gocommerce.SampleSeeder.
func (m *Module) SampleRights() []gocommerce.Right { return []gocommerce.Right{rightPagesWrite} }

// AddSamples implements gocommerce.SampleSeeder. The pages are published, in
// the store's first language; a slug the store already uses is skipped, and
// the sample pages the store has are offered to the menus to link.
func (m *Module) AddSamples(ctx context.Context, shop *gocommerce.SampleShop) (gocommerce.SampleCounts, error) {
	language := m.app.Languages(ctx)[0]
	added := 0
	for _, p := range samplePages {
		res, err := m.db.ExecContext(ctx, `
			INSERT INTO cms_pages (slug, language, title, body, excerpt, status, published_at, sample)
			SELECT $1, $2, $3, $4, $5, 'published', now(), true
			WHERE NOT EXISTS (SELECT 1 FROM cms_pages WHERE slug = $1 AND language = $2)`,
			p.slug, language, p.title, p.body, p.excerpt)
		if err != nil {
			return gocommerce.SampleCounts{"pages": added}, gocommerce.Internalf(err, "add the sample pages")
		}
		n, _ := res.RowsAffected()
		added += int(n)

		var id int64
		var sample bool
		if err := m.db.QueryRowContext(ctx,
			`SELECT id, sample FROM cms_pages WHERE slug = $1 AND language = $2`, p.slug, language).Scan(&id, &sample); err != nil {
			return gocommerce.SampleCounts{"pages": added}, gocommerce.Internalf(err, "add the sample pages")
		}
		if sample {
			shop.Pages = append(shop.Pages, gocommerce.SampleItem{ID: id, Slug: p.slug, Title: p.title})
		}
	}
	return gocommerce.SampleCounts{"pages": added}, nil
}

// CountSamples implements gocommerce.SampleSeeder.
func (m *Module) CountSamples(ctx context.Context) (gocommerce.SampleCounts, error) {
	var n int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM cms_pages WHERE sample`).Scan(&n); err != nil {
		return nil, gocommerce.Internalf(err, "count the sample pages")
	}
	return gocommerce.SampleCounts{"pages": n}, nil
}

// EraseSamples implements gocommerce.SampleSeeder.
func (m *Module) EraseSamples(ctx context.Context) (gocommerce.SampleCounts, error) {
	res, err := m.db.ExecContext(ctx, `DELETE FROM cms_pages WHERE sample`)
	if err != nil {
		return nil, gocommerce.Internalf(err, "erase the sample pages")
	}
	n, _ := res.RowsAffected()
	return gocommerce.SampleCounts{"pages": int(n)}, nil
}
