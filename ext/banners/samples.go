package banners

import (
	"context"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// The sample shop adds no banners any more (D257). It used to add three
// slides and a grid of its collections (D96, D99, with D242's placeholder
// pictures), but a storefront puts a store's slides above its home page's
// first section, and the starting home page (D256) opens with a picture of
// its own, so a new store showed two banners, one on top of the other. A store
// that added the sample shop before keeps its sample banners until it erases
// the sample shop, so they are still counted and erased here.

// SampleRights implements gocommerce.SampleSeeder.
func (m *Module) SampleRights() []gocommerce.Right { return []gocommerce.Right{rightBannersWrite} }

// AddSamples implements gocommerce.SampleSeeder: there is nothing to add.
func (m *Module) AddSamples(context.Context, *gocommerce.SampleShop) (gocommerce.SampleCounts, error) {
	return gocommerce.SampleCounts{"banners": 0}, nil
}

// CountSamples implements gocommerce.SampleSeeder.
func (m *Module) CountSamples(ctx context.Context) (gocommerce.SampleCounts, error) {
	var n int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM banners WHERE sample`).Scan(&n); err != nil {
		return nil, gocommerce.Internalf(err, "count the sample banners")
	}
	return gocommerce.SampleCounts{"banners": n}, nil
}

// EraseSamples implements gocommerce.SampleSeeder.
func (m *Module) EraseSamples(ctx context.Context) (gocommerce.SampleCounts, error) {
	res, err := m.db.ExecContext(ctx, `DELETE FROM banners WHERE sample`)
	if err != nil {
		return nil, gocommerce.Internalf(err, "erase the sample banners")
	}
	n, _ := res.RowsAffected()
	return gocommerce.SampleCounts{"banners": int(n)}, nil
}
