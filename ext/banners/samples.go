package banners

import (
	"context"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// The sample shop's banners (D96, D99): three slides on the home page, each
// leading to a part of the sample shop, and a grid of its categories. The
// pictures are the sample shop's photographs, which the engine serves from
// its own binary at /sample-media/1/ and credits in docs/admin-panel.md.

const sampleMedia = "/sample-media/1/"

var sampleSlides = []struct{ title, kind, slug, picture string }{
	{"New in", "collections", "new-in", "banner-new-in"},
	{"Apparel", "categories", "apparel", "banner-apparel"},
	{"For the home", "categories", "home", "banner-home"},
}

// The tile for each sample category is a photograph of one of its products.
var sampleTiles = map[string]string{
	"apparel":     "cotton-crew-tee-white",
	"accessories": "canvas-tote-bag",
	"home":        "ceramic-mug",
	"stationery":  "a5-notebook",
}

const sampleGrid = "Shop by category"

// SampleRights implements gocommerce.SampleSeeder.
func (m *Module) SampleRights() []gocommerce.Right { return []gocommerce.Right{rightBannersWrite} }

// AddSamples implements gocommerce.SampleSeeder. A page that already has
// sample banners gets no more; a slide or tile whose collection or category
// the store does not have as a sample is left out rather than linking
// nowhere.
func (m *Module) AddSamples(ctx context.Context, shop *gocommerce.SampleShop) (gocommerce.SampleCounts, error) {
	var held int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM banners WHERE sample`).Scan(&held); err != nil {
		return nil, gocommerce.Internalf(err, "count the sample banners")
	}
	if held > 0 {
		return gocommerce.SampleCounts{"banners": 0}, nil
	}
	has := func(items []gocommerce.SampleItem, slug string) bool {
		for _, it := range items {
			if it.Slug == slug {
				return true
			}
		}
		return false
	}
	var ins []BannerInput
	for _, s := range sampleSlides {
		items := shop.Categories
		if s.kind == "collections" {
			items = shop.Collections
		}
		if !has(items, s.slug) {
			continue
		}
		ins = append(ins, BannerInput{
			Placement: PlacementSlide, Title: s.title, Link: "/" + s.kind + "/" + s.slug,
			ImageURL: sampleMedia + s.picture + "-wide.webp", MobileImageURL: sampleMedia + s.picture + "-phone.webp",
		})
	}
	for _, c := range shop.Categories {
		picture, ok := sampleTiles[c.Slug]
		if !ok {
			continue
		}
		ins = append(ins, BannerInput{
			Placement: PlacementGrid, GroupTitle: sampleGrid, Title: c.Title, Link: "/categories/" + c.Slug,
			ImageURL: sampleMedia + picture + ".webp",
		})
	}
	added := 0
	for _, in := range ins {
		in.Page = "home"
		if _, err := m.insert(ctx, in, true); err != nil {
			return gocommerce.SampleCounts{"banners": added}, err
		}
		added++
	}
	return gocommerce.SampleCounts{"banners": added}, nil
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
