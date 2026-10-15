package gocommerce

import (
	"context"
	"database/sql"
	"fmt"
)

// coreStoreTables is every core table that belongs to one store: all of
// them except the people (superusers and their sessions and resets) and the
// store directory itself.
var coreStoreTables = []string{
	"products", "product_options", "product_option_values", "variants", "variant_option_values",
	"carts", "cart_line_items", "orders", "order_lines", "idempotency_keys", "outbox_events",
	"fulfillments", "collections", "product_collections", "media", "product_media", "categories",
	"taxonomy_attributes", "discounts", "discount_targets", "order_discounts", "tax_rates",
	"locations", "variant_stock", "superuser_invitations", "role_rights", "admin_audit",
	"fulfillment_lines", "order_refunds", "order_returns", "order_return_lines", "stock_movements",
	"shipping_zones", "shipping_rates", "customer_groups", "customer_group_members", "price_lists",
	"price_list_prices", "channels", "product_channels", "variant_media", "plugins",
	"notifications", "notification_templates", "role_profiles", "store_profile", "api_keys",
	"custom_reports", "vendors", "vendor_offers", "roles",
}

// migrate0054StoreScope puts every core table under its store (scope.go)
// and finishes what 0053 could not do before the tables had a store: the
// membership keys become per-store, and each store's order counter starts
// after its highest order.
func migrate0054StoreScope(ctx context.Context, tx *sql.Tx) error {
	// store_members points at roles(key) and vendors(id). Those keys are about
	// to lead with store_id, so the global table's keys come off first and go
	// back on per-store below.
	if _, err := tx.ExecContext(ctx, `
		ALTER TABLE store_members DROP CONSTRAINT store_members_role_fkey;
		ALTER TABLE store_members DROP CONSTRAINT store_members_vendor_fkey;`); err != nil {
		return err
	}
	// One row per store instead of one row per install: the id = 1 check and
	// the key on it go, and store_id becomes the key once it exists.
	var check string
	if err := tx.QueryRowContext(ctx, `SELECT conname FROM pg_constraint
		WHERE conrelid = 'store_profile'::regclass AND contype = 'c'
		  AND pg_get_constraintdef(oid) LIKE '%id = 1%'`).Scan(&check); err != nil {
		return fmt.Errorf("find the store_profile check: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		ALTER TABLE store_profile DROP CONSTRAINT %s;
		ALTER TABLE store_profile DROP CONSTRAINT store_profile_pkey;`, quoteIdent(check))); err != nil {
		return err
	}
	if err := ScopeTables(ctx, tx, coreStoreTables...); err != nil {
		return err
	}
	// roles, vendors and orders are behind their policies now, and adding a
	// foreign key checks the existing rows under them: what follows runs bound
	// to store #1, the only store an install has at this point.
	var first string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM stores ORDER BY created_at, id LIMIT 1`).Scan(&first); err != nil {
		return err
	}
	bound := WithStore(ctx, first)
	if _, err := tx.ExecContext(bound, `
		ALTER TABLE store_profile ADD PRIMARY KEY (store_id);
		ALTER TABLE store_members
		    ADD CONSTRAINT store_members_role_fkey FOREIGN KEY (store_id, role)
		        REFERENCES roles (store_id, key) ON DELETE RESTRICT,
		    ADD CONSTRAINT store_members_vendor_fkey FOREIGN KEY (store_id, vendor_id)
		        REFERENCES vendors (store_id, id) ON DELETE CASCADE;`); err != nil {
		return err
	}
	_, err := tx.ExecContext(bound, `
		INSERT INTO store_counters (store_id, name, value)
		SELECT $1, 'order', COALESCE(max(id), 0) FROM orders WHERE store_id = $1`, first)
	return err
}
