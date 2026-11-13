package gocommerce

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
)

// Each store's own currency (D85).
//
// D14 made the settlement currency a start-up setting, one per install, and
// D83 then put many stores in one install — so a shop in India sold in the
// install's dollars. Now a store chooses its own, at creation or on Store
// settings, until an order is paid or sent out: from then on money or goods have
// moved at that currency, and changing it would change what they meant. A store
// that never chooses sells in Config.Currency, as before.
//
// Until then a change relabels what the store has priced so far — its products,
// its open carts and its sample orders — rather than leaving a catalogue in one
// currency and a store in another. Amounts are not converted: the store is
// being set up, and a price typed as 2500 meant 2500 in the currency the owner
// had in mind. The sample shop is the exception: nobody typed its prices, so
// it takes the ones it would have been added with (repriceSamples, D262).
// A real order that is still unpaid keeps the currency it was
// placed in, because that is what its shopper was shown and a gateway may be
// charging it; so do rows a module owns (a B2B quote, a recovery record), as
// every order does (D14).

// migration0061StoreCurrency gives a store its own currency. NULL is the
// install's.
const migration0061StoreCurrency = `
ALTER TABLE stores ADD COLUMN currency text CHECK (currency ~ '^[A-Z]{3}$');
`

var currencyRE = regexp.MustCompile(`^[A-Z]{3}$`)

type storeCurrencyKey struct{}

// withStoreCurrency records, beside the store, the currency storeMW already
// read with it, so every price on a request does not read the row again.
func withStoreCurrency(ctx context.Context, code string) context.Context {
	return context.WithValue(ctx, storeCurrencyKey{}, code)
}

// Currency is what the store ctx is bound to sells in: its own, else the
// install's Config.Currency (D85). Every price the engine writes or reports
// for a store is in it.
func (a *App) Currency(ctx context.Context) string {
	if code, ok := ctx.Value(storeCurrencyKey{}).(string); ok && code != "" {
		return code
	}
	if store := a.storeOf(ctx); store != "" {
		var code sql.NullString
		if err := a.db.QueryRowContext(ctx, `SELECT currency FROM stores WHERE id = $1`, store).Scan(&code); err == nil && code.Valid {
			return code.String
		}
	}
	return a.cfg.Currency
}

// currencyOf is a store row's currency, or the install's.
func (a *App) currencyOf(st *Store) string {
	if st != nil && st.Currency != nil && *st.Currency != "" {
		return *st.Currency
	}
	return a.cfg.Currency
}

// hasOrders is whether the store ctx is bound to has an order that fixes its
// currency: one that was paid, whose payment and refund are in the old currency,
// or one sent out, since cash on delivery may have been collected before anybody
// marks it paid. An unpaid order waiting to go out does not (D212), and nor does
// a sample order, which nobody placed.
func (a *App) hasOrders(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (bool, error) {
	var any bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM orders WHERE NOT sample AND
		(payment_status IN ('paid', 'refunded') OR status NOT IN ('pending', 'confirmed', 'cancelled')))`).Scan(&any)
	return any, err
}

// normalizeCurrency upper-cases a code and refuses anything that is not three
// letters. Which three letters is the store's business (D14): any ISO 4217 code
// works, and the panel offers the list.
func normalizeCurrency(code string) (string, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if !currencyRE.MatchString(code) {
		return "", Validationf("currency is a three-letter ISO code, such as %q", "INR")
	}
	return code, nil
}

// setCurrency moves the store ctx is bound to onto code, inside tx, and
// relabels what it has priced so far. The caller has checked hasOrders.
func (a *App) setCurrency(ctx context.Context, tx *sql.Tx, code string) error {
	if err := a.repriceSamples(ctx, tx, a.Currency(ctx), code); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE stores SET currency = $1 WHERE id = $2`, code, a.storeOf(ctx)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE products SET currency = $1 WHERE currency <> $1`, code); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE orders SET currency = $1 WHERE sample AND currency <> $1`, code); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE carts SET currency = $1 WHERE status = 'open' AND currency <> $1`, code)
	return err
}

// repriceSamples gives the sample shop the prices it would have been added
// with in the new currency, rather than relabelling them: the catalogue has
// its own rupee prices and only a dollar face value for every other currency
// (sampleProduct.prices), so a store opened in dollars and moved to rupees
// sold a ₹2,299 kurta at ₹29 (D262). A price the merchant has changed no
// longer matches the old figure and is theirs to keep.
//
// ponytail: sample orders are only relabelled; they are the panel's, never a
// shopper's, and erasing and re-adding the sample shop prices them too.
func (a *App) repriceSamples(ctx context.Context, tx *sql.Tx, from, to string) error {
	if from == to {
		return nil
	}
	for _, s := range sampleCatalogue.Products {
		wasPrice, wasCompare := s.prices(from)
		price, compare := s.prices(to)
		rows, err := tx.QueryContext(ctx, `
			WITH moved AS (
			    UPDATE variants v SET price_minor = $4, compare_at_price_minor = $5, updated_at = now()
			    FROM products p
			    WHERE p.id = v.product_id AND p.sample AND p.slug = $1
			      AND v.price_minor = $2 AND v.compare_at_price_minor IS NOT DISTINCT FROM $3
			    RETURNING v.product_id)
			SELECT p.id, p.slug, p.title, p.status FROM products p WHERE p.id IN (SELECT product_id FROM moved)`,
			s.Slug, wasPrice, wasCompare, price, compare)
		if err != nil {
			return err
		}
		var events []productEvent
		for rows.Next() {
			var e productEvent
			if err := rows.Scan(&e.ID, &e.Slug, &e.Title, &e.Status); err != nil {
				rows.Close()
				return err
			}
			events = append(events, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, e := range events {
			if err := a.outbox.write(ctx, tx, EventProductUpdated, AggregateProduct, e.ID, e); err != nil {
				return err
			}
		}
	}
	pick := func(currency string) int {
		if currency == sampleCatalogue.Currency {
			return 0
		}
		return 1
	}
	for _, d := range sampleDiscounts {
		if d.minimum[pick(from)] == 0 {
			continue
		}
		was := d.minimum[pick(from)] * currencyScale(from)
		now := d.minimum[pick(to)] * currencyScale(to)
		if _, err := tx.ExecContext(ctx, `
			UPDATE discounts SET min_subtotal_minor = $3, title = CASE WHEN title = $4 THEN $5 ELSE title END
			WHERE sample AND code = $1 AND min_subtotal_minor = $2`,
			d.code, was, now, d.title[pick(from)], d.title[pick(to)]); err != nil {
			return err
		}
	}
	return nil
}
