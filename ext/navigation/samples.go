package navigation

import (
	"context"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// The sample shop's menu links (D96): the sample categories in the header,
// so a shopper can browse by them, and the sample pages in the footer.

// migrationSample marks the links the sample shop added, by a column no
// request sets. Saving a menu rewrites its tree; the save carries the mark
// over to a link that still points where a sample link did (handleSetItems).
const migrationSample = `ALTER TABLE navigation_items ADD COLUMN sample boolean NOT NULL DEFAULT false;`

// sampleLink is where a sample link points, which is what a save matches.
type sampleLink struct{ kind, target string }

// SampleRights implements gocommerce.SampleSeeder.
func (m *Module) SampleRights() []gocommerce.Right { return []gocommerce.Right{rightMenusWrite} }

// LinksSamples implements gocommerce.SampleLinker: the links point at the
// pages the other modules add, so this module is asked after them.
func (m *Module) LinksSamples() {}

// AddSamples implements gocommerce.SampleSeeder. A link the menu already has
// is skipped, and so is a menu the store has deleted.
func (m *Module) AddSamples(ctx context.Context, shop *gocommerce.SampleShop) (gocommerce.SampleCounts, error) {
	added := 0
	add := func(menu, kind string, items []gocommerce.SampleItem) error {
		for _, it := range items {
			res, err := m.db.ExecContext(ctx, `
				INSERT INTO navigation_items (menu_id, title, kind, target, position, sample)
				SELECT m.id, $2, $3, $4,
				       coalesce((SELECT max(position) + 1 FROM navigation_items
				                 WHERE menu_id = m.id AND parent_id IS NULL), 0), true
				FROM navigation_menus m
				WHERE m.handle = $1
				  AND NOT EXISTS (SELECT 1 FROM navigation_items i
				                  WHERE i.menu_id = m.id AND i.kind = $3 AND i.target = $4)`,
				menu, it.Title, kind, it.Slug)
			if err != nil {
				return gocommerce.Internalf(err, "add the sample menu links")
			}
			n, _ := res.RowsAffected()
			added += int(n)
		}
		return nil
	}
	if err := add("header", "category", shop.Categories); err != nil {
		return gocommerce.SampleCounts{"menu_links": added}, err
	}
	if err := add("footer", "page", shop.Pages); err != nil {
		return gocommerce.SampleCounts{"menu_links": added}, err
	}
	return gocommerce.SampleCounts{"menu_links": added}, nil
}

// CountSamples implements gocommerce.SampleSeeder.
func (m *Module) CountSamples(ctx context.Context) (gocommerce.SampleCounts, error) {
	var n int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM navigation_items WHERE sample`).Scan(&n); err != nil {
		return nil, gocommerce.Internalf(err, "count the sample menu links")
	}
	return gocommerce.SampleCounts{"menu_links": n}, nil
}

// EraseSamples implements gocommerce.SampleSeeder.
func (m *Module) EraseSamples(ctx context.Context) (gocommerce.SampleCounts, error) {
	res, err := m.db.ExecContext(ctx, `DELETE FROM navigation_items WHERE sample`)
	if err != nil {
		return nil, gocommerce.Internalf(err, "erase the sample menu links")
	}
	n, _ := res.RowsAffected()
	return gocommerce.SampleCounts{"menu_links": int(n)}, nil
}
