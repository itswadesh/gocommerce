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

// storefrontOrigins are every address shoppers may reach the store ctx is
// bound to by — each verified domain, then <slug>.<base domain> — as
// https://host origins. The panel lets these, and only these, be framed, so
// the theme editor can show the store's own home page (D126). An unproven
// domain is somebody's claim, not yet the store's, and is left out.
func (a *App) storefrontOrigins(ctx context.Context) []string {
	id := a.storeOf(ctx)
	if id == "" || id == installScope {
		return nil
	}
	var out []string
	rows, err := a.db.QueryContext(ctx, `SELECT host FROM store_domains WHERE store_id = $1 AND verified_at IS NOT NULL
		ORDER BY is_primary DESC, created_at, host LIMIT 20`, id)
	if err != nil {
		a.log.Warn("read the store's domains", "store", id, "err", err)
	} else {
		for rows.Next() {
			var host string
			if rows.Scan(&host) == nil && host != "" {
				out = append(out, "https://"+host)
			}
		}
		rows.Close()
	}
	if st, err := a.stores.Get(ctx, id); err == nil {
		if base := a.storeBrand(st.BrandHost).BaseDomain; base != "" {
			out = append(out, "https://"+st.Slug+"."+base)
		}
	}
	return out
}
