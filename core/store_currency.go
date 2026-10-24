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
// settings, until its first order: from then on its orders are amounts in that
// currency, and changing it would change what they mean. A store that never
// chooses sells in Config.Currency, as before.
//
// Before the first order a change relabels what the store has priced so far —
// its products and its open carts — rather than leaving a catalogue in one
// currency and a store in another. Amounts are not converted: the store is
// being set up, and a price typed as 2500 meant 2500 in the currency the owner
// had in mind. Rows a module owns (a B2B quote, a recovery record) keep the
// currency they were made in, as every order does (D14).

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

// hasOrders is whether the store ctx is bound to has an order whose amounts a
// new currency would change, which is what fixes its currency. An order
// cancelled before any money moved has none; a paid one keeps the old
// currency in its payment and refund.
func (a *App) hasOrders(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (bool, error) {
	var any bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM orders WHERE status <> 'cancelled' OR payment_status IN ('paid', 'refunded'))`).Scan(&any)
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
// relabels what it has priced so far. The caller has checked that the store has
// no order.
func (a *App) setCurrency(ctx context.Context, tx *sql.Tx, code string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE stores SET currency = $1 WHERE id = $2`, code, a.storeOf(ctx)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE products SET currency = $1 WHERE currency <> $1`, code); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE carts SET currency = $1 WHERE status = 'open' AND currency <> $1`, code)
	return err
}
