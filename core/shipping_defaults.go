package gocommerce

import (
	"context"
	"database/sql"
)

// migration0062ShippingLikeKitCommerce gives zones and rates what KitCommerce's
// shipping screen has: a zone that can be switched off and described, and a
// rate with a description, a handling fee, a delivery estimate, and a band
// measured on weight as well as on the order's value.
const migration0062ShippingLikeKitCommerce = `
ALTER TABLE shipping_zones
    ADD COLUMN description text    NOT NULL DEFAULT '',
    ADD COLUMN active      boolean NOT NULL DEFAULT true;
ALTER TABLE shipping_rates
    ADD COLUMN description        text    NOT NULL DEFAULT '',
    ADD COLUMN handling_fee_minor bigint  NOT NULL DEFAULT 0 CHECK (handling_fee_minor >= 0),
    ADD COLUMN min_days           integer CHECK (min_days IS NULL OR min_days >= 0),
    ADD COLUMN max_days           integer CHECK (max_days IS NULL OR max_days >= 0),
    ADD COLUMN basis              text    NOT NULL DEFAULT 'price' CHECK (basis IN ('price', 'weight')),
    ADD COLUMN min_weight_grams   bigint  NOT NULL DEFAULT 0 CHECK (min_weight_grams >= 0),
    ADD COLUMN max_weight_grams   bigint  CHECK (max_weight_grams IS NULL OR max_weight_grams > min_weight_grams),
    ADD CONSTRAINT shipping_rates_days CHECK (min_days IS NULL OR max_days IS NULL OR max_days >= min_days);
`

// cartWeightGrams is what a cart's parcel weighs: each physical line's
// variant weight times its quantity. A variant with no weight counts as
// nothing, which is what a store that has not weighed its products expects.
func cartWeightGrams(ctx context.Context, q rowQuerier, cartID int64) (int64, error) {
	var grams int64
	err := q.QueryRowContext(ctx, `
		SELECT coalesce(sum(coalesce(v.weight_grams, 0)::bigint * l.quantity), 0)
		FROM cart_line_items l JOIN variants v ON v.id = l.variant_id
		WHERE l.cart_id = $1 AND v.requires_shipping`, cartID).Scan(&grams)
	return grams, err
}

// defaultShippingRates are the rates Varni Jewels runs, which the owner asked
// every new store to start from: one zone, "Local", the United States, priced
// on the order's value. Amounts are minor units of the store's currency.
// "below $100" is the band ending at 100, "above $100" the one starting there.
var defaultShippingRates = []ShippingRateInput{
	{Name: "USPS priority", PriceMinor: 0, MinSubtotalMinor: 10000},
	{Name: "USPS ground advantage", PriceMinor: 0, MaxSubtotalMinor: ptr64(10000)},
	{Name: "USPS priority", PriceMinor: 700, MaxSubtotalMinor: ptr64(10000)},
	{Name: "Fedex Overnight", PriceMinor: 2000, MinSubtotalMinor: 50000, MaxSubtotalMinor: ptr64(100000)},
	{Name: "Fedex Overnight", PriceMinor: 3000, MinSubtotalMinor: 10000, MaxSubtotalMinor: ptr64(50000)},
	{Name: "FedEx 2-Day", PriceMinor: 700, MaxSubtotalMinor: ptr64(10000)},
	{Name: "FedEx 2-Day", PriceMinor: 0, MinSubtotalMinor: 10000, MaxSubtotalMinor: ptr64(100000)},
	{Name: "Fedex Overnight", PriceMinor: 4500, MaxSubtotalMinor: ptr64(10000)},
	{Name: "Fedex Overnight", PriceMinor: 0, MinSubtotalMinor: 100000},
}

func ptr64(v int64) *int64 { return &v }

// SeedDefaults gives the store ctx is bound to the default zone and its rates,
// in one transaction, positioned in the order they are listed. It is for a
// store just opened from "Create a store"; a store that already has zones is
// left as it is.
func (s *Shipping) SeedDefaults(ctx context.Context) error {
	return InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		var any bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM shipping_zones)`).Scan(&any); err != nil || any {
			return err
		}
		var zoneID int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO shipping_zones (name, countries) VALUES ('Local', ARRAY['US'])
			RETURNING id`).Scan(&zoneID); err != nil {
			return err
		}
		for i, r := range defaultShippingRates {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO shipping_rates (zone_id, name, price_minor, min_subtotal_minor, max_subtotal_minor, position)
				VALUES ($1, $2, $3, $4, $5, $6)`,
				zoneID, r.Name, r.PriceMinor, r.MinSubtotalMinor, r.MaxSubtotalMinor, i); err != nil {
				return err
			}
		}
		return nil
	})
}
