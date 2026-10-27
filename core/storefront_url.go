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

// orderTrackingURL is the storefront page where a customer sees an order,
// the number filled in and the order's access key beside it, so the link in
// their email opens the order without asking for anything (owner, 2026-10-27).
// The key is what the storefront already reads a guest's order with; an order
// with none gets the page alone, where the number and email still open it.
func (a *App) orderTrackingURL(ctx context.Context, orderID int64, number string) string {
	base := a.StorefrontURL(ctx)
	if base == "" || number == "" {
		return ""
	}
	link := base + "/order-tracking?order=" + url.QueryEscape(number)
	var key string
	if err := a.db.QueryRowContext(ctx, `SELECT access_token FROM orders WHERE id = $1`, orderID).Scan(&key); err != nil && !errors.Is(err, sql.ErrNoRows) {
		a.log.Warn("read the order's key for its link", "order", number, "err", err)
	}
	if key != "" {
		link += "&key=" + url.QueryEscape(key)
	}
	return link
}
