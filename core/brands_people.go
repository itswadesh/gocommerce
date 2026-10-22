package gocommerce

import (
	"context"
	"database/sql"
)

// Emails to staff carry a brand (D111): its name, and links to its panel. A
// store's emails go out in the store's brand. A person's own — a password
// reset — go out in the brand of the admin domain they were asked from, and
// with no request behind them in the brand the person arrived through: the
// admin domain they signed up on, or the brand of the store whose invitation
// they accepted.

// migration0074SuperuserBrand records the brand a person arrived through, by
// its panel host, which outlives a change of name. NULL for everyone made
// before brands, or on an install without them; they get the first brand.
const migration0074SuperuserBrand = `
ALTER TABLE superusers ADD COLUMN brand_host text;
`

// arrivalBrandHost is what a person made by this request records as their
// brand: the request's, where the install configures brands.
func (a *App) arrivalBrandHost(ctx context.Context) sql.NullString {
	b, ok := BrandFrom(ctx)
	if !ok || len(a.cfg.Brands) == 0 {
		return sql.NullString{}
	}
	return sql.NullString{String: panelHost(b.PanelURL), Valid: true}
}

// storeBrandHost is what a person who joins the store ctx is bound to records
// as their brand: the store's, where the install configures brands.
func (a *App) storeBrandHost(ctx context.Context) sql.NullString {
	if len(a.cfg.Brands) == 0 {
		return sql.NullString{}
	}
	return sql.NullString{String: panelHost(a.StoreBrand(ctx).PanelURL), Valid: true}
}

// personMailBrand is the brand a person's own email goes out in: the request's,
// when the person belongs to that brand — a store of theirs is in it, or they
// arrived through it; else the brand they arrived through; else the brand of
// their first store; else the default brand. A request may not pick a brand the
// person has nothing to do with: anybody can ask for a reset for anybody, and
// would otherwise choose whose name a real person's email carries.
func (a *App) personMailBrand(ctx context.Context, superuserID int64) Brand {
	var arrived sql.NullString
	_ = a.db.QueryRowContext(ctx, `SELECT brand_host FROM superusers WHERE id = $1`, superuserID).Scan(&arrived)
	var stores []Brand
	rows, err := a.db.QueryContext(ctx, `
		SELECT coalesce(st.brand_host, '') FROM store_members m JOIN stores st ON st.id = m.store_id
		WHERE m.superuser_id = $1 ORDER BY st.created_at, st.id`, superuserID)
	if err == nil {
		for rows.Next() {
			var host string
			if rows.Scan(&host) == nil {
				stores = append(stores, a.storeBrand(host))
			}
		}
		rows.Close()
	}
	recorded, hasRecord := Brand{}, false
	if arrived.Valid {
		recorded, hasRecord = a.brandByPanelHost(arrived.String)
	}

	if asked, ok := BrandFrom(ctx); ok {
		if hasRecord && sameBrand(recorded, asked) {
			return asked
		}
		for _, b := range stores {
			if sameBrand(b, asked) {
				return asked
			}
		}
	}
	switch {
	case hasRecord:
		return recorded
	case len(stores) > 0:
		return stores[0]
	}
	return a.defaultBrand()
}
