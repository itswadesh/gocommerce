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

// personMailBrand is the brand a person's own email goes out in: the request's
// when one asked for it, else the brand they arrived through, else the first.
func (a *App) personMailBrand(ctx context.Context, superuserID int64) Brand {
	if b, ok := BrandFrom(ctx); ok {
		return b
	}
	var host sql.NullString
	if err := a.db.QueryRowContext(ctx, `SELECT brand_host FROM superusers WHERE id = $1`, superuserID).Scan(&host); err == nil && host.Valid {
		if b, ok := a.brandByPanelHost(host.String); ok {
			return b
		}
	}
	return a.defaultBrand()
}
