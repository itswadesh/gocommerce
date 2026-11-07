package banners

import (
	"context"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// The sample shop's banners (D96, D99): three slides on the home page, each
// leading to a part of the sample shop, and a grid of its collections. Not of
// its categories: a storefront's home page has a category section of its own,
// which the sample categories' pictures fill, and the same tiles twice would
// read as a mistake. The pictures are placeholders the engine serves from its
// own binary (gocommerce.SamplePlaceholderPrefix, D242), which say what size to
// replace them with, because a banner is the first thing a store makes its own.

var sampleSlides = []struct{ title, kind, slug, picture string }{
	{"Festive edit", "collections", "festive-edit", "banner-1"},
	{"Women", "categories", "women", "banner-2"},
	{"Men", "categories", "men", "banner-3"},
}

// Each sample collection's tile has its own placeholder.
var sampleTiles = map[string]string{
	"new-arrivals": "tile-1",
	"festive-edit": "tile-2",
}

const sampleGrid = "Shop by collection"

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
			ImageURL:       gocommerce.SamplePlaceholderPrefix + s.picture + "-wide.webp",
			MobileImageURL: gocommerce.SamplePlaceholderPrefix + s.picture + "-phone.webp",
		})
	}
	for _, c := range shop.Collections {
		picture, ok := sampleTiles[c.Slug]
		if !ok {
			continue
		}
		ins = append(ins, BannerInput{
			Placement: PlacementGrid, GroupTitle: sampleGrid, Title: c.Title, Link: "/collections/" + c.Slug,
			ImageURL: gocommerce.SamplePlaceholderPrefix + picture + ".webp",
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
