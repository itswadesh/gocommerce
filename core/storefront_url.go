package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
)

// StorefrontURL is where shoppers reach the store ctx is bound to, as
// https://host with no trailing slash: its primary verified domain, else its
// first verified one, else <slug>.<its brand's base domain>. Empty when the
// store has none of those, so a caller leaves its link out rather than
// pointing at nowhere.
func (a *App) StorefrontURL(ctx context.Context) string {
	id := a.storeOf(ctx)
	if id == "" || id == installScope {
		return ""
	}
	var host string
	err := a.db.QueryRowContext(ctx, `SELECT host FROM store_domains WHERE store_id = $1 AND verified_at IS NOT NULL
		ORDER BY is_primary DESC, created_at, host LIMIT 1`, id).Scan(&host)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		a.log.Warn("read the store's domain", "store", id, "err", err)
	}
	if host == "" {
		st, err := a.stores.Get(ctx, id)
		if err != nil {
			return ""
		}
		base := a.storeBrand(st.BrandHost).BaseDomain
		if base == "" {
			return ""
		}
		host = st.Slug + "." + base
	}
	return "https://" + host
}

// orderTrackingURL is the storefront page where a guest opens an order with
// a code sent to its contact (D115), the number filled in. The link is no
// credential: it names the order, and the code still has to come.
func (a *App) orderTrackingURL(ctx context.Context, number string) string {
	base := a.StorefrontURL(ctx)
	if base == "" || number == "" {
		return ""
	}
	return base + "/order-tracking?" + url.Values{"number": {number}}.Encode()
}
