package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"math"
)

// The sold count (D134): "30+ sold" on a product page, the social proof a
// shopper in India expects to see. It is a count and never an estimate: units
// on this store's orders that were confirmed and were not cancelled or
// refunded, the sample shop's orders (D96) left out. A shop turns it on, it is
// off until then, and a product under the shop's threshold says nothing at
// all rather than "2 sold".
//
// Counted by the sweeper into product_sales_counts rather than on each page
// view: a product that has sold forty thousand is forty thousand order lines
// to add up for every shopper who opens it. A count five minutes old is still
// a count.

// SoldCountPluginKey is the switch, on Themes › Storefront extras.
const SoldCountPluginKey = "sold-count"

// defaultSoldThreshold is the fewest sales a product shows, until the shop
// sets its own.
const defaultSoldThreshold = 25

// M85 — what each product has sold, kept by the sweeper.
const migration0085ProductSalesCounts = `
CREATE TABLE product_sales_counts (
    product_id bigint      PRIMARY KEY REFERENCES products (id) ON DELETE CASCADE,
    units      bigint      NOT NULL CHECK (units > 0),
    counted_at timestamptz NOT NULL DEFAULT now()
);
`

// soldStatuses are the orders whose units count as sold: taken and not
// undone. Pending is an order not yet paid for, and cancelled is undone.
const soldStatusesSQL = `('confirmed', 'partial', 'shipped', 'delivered')`

// RefreshSalesCounts recounts this store's sales, when the store shows them.
// A store with the count off keeps whatever was there, unread, and is
// recounted the first pass after it is switched on.
func (c *Catalog) RefreshSalesCounts(ctx context.Context) error {
	on, err := c.app.plugins.Enabled(ctx, SoldCountPluginKey)
	if err != nil || !on {
		return err
	}
	return InTx(ctx, c.app.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM product_sales_counts`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO product_sales_counts (product_id, units)
			SELECT l.product_id, sum(l.quantity)
			FROM order_lines l
			JOIN orders o ON o.id = l.order_id
			JOIN products p ON p.id = l.product_id
			WHERE o.status IN `+soldStatusesSQL+`
			  AND o.payment_status <> 'refunded'
			  AND NOT o.sample
			GROUP BY l.product_id`)
		return err
	})
}

// soldAtLeast is what a product page may say it has sold: the count rounded
// down to a band, and nil when the store does not show counts or the product
// has sold fewer than the threshold.
func (c *Catalog) soldAtLeast(ctx context.Context, productID int64) (*int64, error) {
	on, err := c.app.plugins.Enabled(ctx, SoldCountPluginKey)
	if err != nil || !on {
		return nil, err
	}
	settings, err := c.app.plugins.Settings(ctx, SoldCountPluginKey)
	if err != nil {
		return nil, err
	}
	threshold := int64(defaultSoldThreshold)
	if v, ok := settings["threshold"].(float64); ok && v >= 1 && v <= math.MaxInt32 {
		threshold = int64(v)
	}
	var units int64
	err = c.app.db.QueryRowContext(ctx,
		`SELECT units FROM product_sales_counts WHERE product_id = $1`, productID).Scan(&units)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	band := soldBand(units)
	if units < threshold || band == 0 {
		return nil, nil
	}
	return &band, nil
}

// soldBand rounds a count down to what a page says: tens under a hundred,
// hundreds under a thousand, then whole thousands — "30+", "400+", "45k+".
// Never up, so the claim is always true; and nothing under ten, where "5+"
// says more about an empty shop than a popular product.
func soldBand(n int64) int64 {
	switch {
	case n < 10:
		return 0
	case n < 100:
		return n / 10 * 10
	case n < 1000:
		return n / 100 * 100
	default:
		return n / 1000 * 1000
	}
}
