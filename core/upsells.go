package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Upsells are the two ways a shop puts one product beside another (D133).
//
// An add-on is an order bump: a variant offered with a product, optionally at
// a price of its own, ticked on the product page and bought with it. The price
// is the engine's to keep: it holds on a basket line only while the product it
// is offered with is in the same basket, for at most as many as there are of
// that product. The shop's checkbox on a storefront is a request; the basket
// decides.
//
// A cross-sell is a suggestion and nothing more: "you may also like", chosen
// per product, at the products' own prices. A basket's suggestions are its
// products' cross-sells, less what it already holds.
type Upsells struct {
	app *App
}

// Upsells returns the add-on and cross-sell service.
func (a *App) Upsells() *Upsells { return a.upsells }

// maxAddOns and maxCrossSells bound what a product carries. A buy box with
// thirty checkboxes is not an offer anybody reads, and the bound keeps every
// read of them one small query.
const (
	maxAddOns     = 10
	maxCrossSells = 24
)

// M83 — add-ons and cross-sells (D133), and the flag that lists a discount
// code among a basket's offers.
//
// A basket line now says which add-on it was bought as. That line is its own
// row beside any line of the same variant bought on its own, because the two
// are priced differently: three grips with one case is one at the offer price
// and two at the grip's own. So the one-line-per-variant key becomes two
// partial keys, one for ordinary lines and one per add-on.
//
// add_on_id is deliberately not a foreign key. An offer the shop withdraws must
// not take a shopper's line with it, and SET NULL would collide with the
// ordinary line of the same variant; a line whose add-on is gone is priced as
// the variant on its own, which the basket shows as a price change.
const migration0083AddOnsAndCrossSells = `
CREATE TABLE product_add_ons (
    id          bigserial   PRIMARY KEY,
    product_id  bigint      NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    variant_id  bigint      NOT NULL REFERENCES variants (id) ON DELETE CASCADE,
    price_minor bigint      CHECK (price_minor >= 0),
    position    integer     NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (product_id, variant_id)
);
CREATE TABLE product_cross_sells (
    product_id         bigint  NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    related_product_id bigint  NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    position           integer NOT NULL DEFAULT 0,
    PRIMARY KEY (product_id, related_product_id),
    CHECK (product_id <> related_product_id)
);
ALTER TABLE cart_line_items ADD COLUMN add_on_id bigint;
ALTER TABLE cart_line_items DROP CONSTRAINT cart_line_items_cart_id_variant_id_key;
CREATE UNIQUE INDEX cart_line_items_ordinary
    ON cart_line_items (store_id, cart_id, variant_id) WHERE add_on_id IS NULL;
CREATE UNIQUE INDEX cart_line_items_add_on
    ON cart_line_items (store_id, cart_id, add_on_id) WHERE add_on_id IS NOT NULL;
ALTER TABLE discounts ADD COLUMN listed boolean NOT NULL DEFAULT false;
`

// AddOn is one variant offered with a product.
type AddOn struct {
	ID int64 `json:"id"`
	// ProductID is the product it is offered with.
	ProductID int64 `json:"product_id"`
	VariantID int64 `json:"variant_id"`
	Position  int   `json:"position"`
	// OfferPrice is what it costs bought with the product. Nil is its own
	// price: the shop is suggesting it, not discounting it.
	OfferPrice *Money `json:"offer_price"`
	// Price and CompareAtPrice are the variant's own, so a storefront can
	// show what the offer saves without a second request.
	Price          Money     `json:"price"`
	CompareAtPrice *Money    `json:"compare_at_price,omitempty"`
	Item           AddOnItem `json:"item"`
}

// AddOnItem is what a storefront draws for an add-on.
type AddOnItem struct {
	ProductID    int64  `json:"product_id"`
	Slug         string `json:"slug"`
	Title        string `json:"title"`
	VariantLabel string `json:"variant_label,omitempty"`
	SKU          string `json:"sku"`
	ImageURL     string `json:"image_url,omitempty"`
	// Live is whether it can be bought at all: the variant on and its
	// product published. The storefront is only ever sent live ones.
	Live    bool `json:"live"`
	InStock bool `json:"in_stock"`
}

// AddOnInput is one add-on as the panel saves it.
type AddOnInput struct {
	VariantID  int64  `json:"variant_id"`
	PriceMinor *int64 `json:"price_minor"`
}

// AddOns lists what is offered with a product, in the shop's order. live
// limits it to what a shopper can buy.
func (u *Upsells) AddOns(ctx context.Context, productID int64, live bool) ([]AddOn, error) {
	q := `
		SELECT a.id, a.product_id, a.variant_id, a.position, a.price_minor,
		       v.price_minor, v.compare_at_price_minor, v.sku, v.active AND p.status = 'active',
		       NOT v.track_inventory OR v.continue_selling
		           OR coalesce((SELECT sum(vs.on_hand - vs.reserved) FROM variant_stock vs WHERE vs.variant_id = v.id), 0) > 0,
		       p.id, p.slug, p.title
		FROM product_add_ons a
		JOIN variants v ON v.id = a.variant_id
		JOIN products p ON p.id = v.product_id
		WHERE a.product_id = $1`
	if live {
		q += ` AND v.active AND p.status = 'active'`
	}
	rows, err := u.app.db.QueryContext(ctx, q+` ORDER BY a.position, a.id`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	currency := u.app.Currency(ctx)
	out := []AddOn{}
	var variantIDs, productIDs []int64
	for rows.Next() {
		var a AddOn
		var offer, compare sql.NullInt64
		if err := rows.Scan(&a.ID, &a.ProductID, &a.VariantID, &a.Position, &offer,
			&a.Price.AmountMinor, &compare, &a.Item.SKU, &a.Item.Live, &a.Item.InStock,
			&a.Item.ProductID, &a.Item.Slug, &a.Item.Title); err != nil {
			return nil, err
		}
		a.Price.Currency = currency
		if offer.Valid {
			m := money(offer.Int64, currency)
			a.OfferPrice = &m
		}
		if compare.Valid {
			m := money(compare.Int64, currency)
			a.CompareAtPrice = &m
		}
		out = append(out, a)
		variantIDs = append(variantIDs, a.VariantID)
		productIDs = append(productIDs, a.Item.ProductID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	labels, err := u.app.carts.variantLabels(ctx, variantIDs)
	if err != nil {
		return nil, err
	}
	products, _, err := u.app.catalog.ListProducts(ctx, ProductQuery{IDs: productIDs, Limit: len(productIDs)})
	if err != nil {
		return nil, err
	}
	images := map[int64]string{}
	for _, p := range products {
		images[p.ID] = p.ImageURL
	}
	for i := range out {
		out[i].Item.VariantLabel = labels[out[i].VariantID]
		out[i].Item.ImageURL = images[out[i].Item.ProductID]
	}
	return out, nil
}

// SetAddOns replaces what a product offers, in the order given. An add-on
// that stays keeps its id, so a basket holding it keeps its offer; one that
// goes is withdrawn from every basket at once, as a price change.
func (u *Upsells) SetAddOns(ctx context.Context, productID int64, in []AddOnInput) ([]AddOn, error) {
	if len(in) > maxAddOns {
		return nil, Validationf("a product can offer at most %d add-ons", maxAddOns)
	}
	seen := map[int64]bool{}
	ids := make([]int64, 0, len(in))
	for _, a := range in {
		if a.VariantID <= 0 {
			return nil, Validationf("each add-on needs a variant")
		}
		if seen[a.VariantID] {
			return nil, Validationf("an add-on is listed twice")
		}
		seen[a.VariantID] = true
		if a.PriceMinor != nil && *a.PriceMinor < 0 {
			return nil, Validationf("an add-on's price must not be negative")
		}
		ids = append(ids, a.VariantID)
	}
	err := InTx(ctx, u.app.db, func(tx *sql.Tx) error {
		if err := requireProduct(ctx, tx, productID); err != nil {
			return err
		}
		var found, own int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*), count(*) FILTER (WHERE product_id = $2)
			FROM variants WHERE id = ANY($1::bigint[])`, int64Array(ids), productID).Scan(&found, &own); err != nil {
			return err
		}
		if found != len(ids) {
			return Validationf("an add-on names a variant that does not exist")
		}
		if own > 0 {
			return Validationf("a product cannot be offered as its own add-on")
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM product_add_ons WHERE product_id = $1 AND NOT (variant_id = ANY($2::bigint[]))`,
			productID, int64Array(ids)); err != nil {
			return err
		}
		for i, a := range in {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO product_add_ons (product_id, variant_id, price_minor, position)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (store_id, product_id, variant_id) DO UPDATE
				SET price_minor = EXCLUDED.price_minor, position = EXCLUDED.position, updated_at = now()`,
				productID, a.VariantID, a.PriceMinor, i); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return u.AddOns(ctx, productID, false)
}

func requireProduct(ctx context.Context, q rowQuerier, id int64) error {
	var ok bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM products WHERE id = $1)`, id).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return NotFoundf("product %d does not exist", id)
	}
	return nil
}

// CrossSellIDs are the products a product suggests, in the shop's order,
// published or not: the panel's view.
func (u *Upsells) CrossSellIDs(ctx context.Context, productID int64) ([]int64, error) {
	rows, err := u.app.db.QueryContext(ctx, `
		SELECT related_product_id FROM product_cross_sells
		WHERE product_id = $1 ORDER BY position, related_product_id`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SetCrossSells replaces what a product suggests, in the order given.
func (u *Upsells) SetCrossSells(ctx context.Context, productID int64, ids []int64) error {
	ids = dedupeIDs(ids)
	if len(ids) > maxCrossSells {
		return Validationf("a product can suggest at most %d others", maxCrossSells)
	}
	for _, id := range ids {
		if id == productID {
			return Validationf("a product cannot suggest itself")
		}
	}
	return InTx(ctx, u.app.db, func(tx *sql.Tx) error {
		if err := requireProduct(ctx, tx, productID); err != nil {
			return err
		}
		var found int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM products WHERE id = ANY($1::bigint[])`,
			int64Array(ids)).Scan(&found); err != nil {
			return err
		}
		if found != len(ids) {
			return Validationf("a suggestion names a product that does not exist")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM product_cross_sells WHERE product_id = $1`, productID); err != nil {
			return err
		}
		for i, id := range ids {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO product_cross_sells (product_id, related_product_id, position) VALUES ($1, $2, $3)`,
				productID, id, i); err != nil {
				return err
			}
		}
		return nil
	})
}

// CrossSells are the published products a product suggests, in order.
func (u *Upsells) CrossSells(ctx context.Context, productID int64, limit int) ([]*Product, error) {
	ids, err := u.CrossSellIDs(ctx, productID)
	if err != nil {
		return nil, err
	}
	return u.liveProducts(ctx, ids, limit)
}

// CartCrossSells are what a basket's products suggest, in the order the
// products went in and each one's own order after that, less anything the
// basket already holds. Empty when nothing is curated; a storefront then falls
// back to its own idea of related products.
func (u *Upsells) CartCrossSells(ctx context.Context, cart *Cart, limit int) ([]*Product, error) {
	var inCart []int64
	held := map[int64]bool{}
	for _, l := range cart.Lines {
		if !held[l.ProductID] {
			held[l.ProductID] = true
			inCart = append(inCart, l.ProductID)
		}
	}
	if len(inCart) == 0 {
		return []*Product{}, nil
	}
	rows, err := u.app.db.QueryContext(ctx, `
		SELECT related_product_id FROM product_cross_sells
		WHERE product_id = ANY($1::bigint[]) AND NOT (related_product_id = ANY($1::bigint[]))
		ORDER BY array_position($1::bigint[], product_id), position`, int64Array(inCart))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return u.liveProducts(ctx, dedupeIDs(ids), limit)
}

// liveProducts reads the published ones of ids, keeping their order.
func (u *Upsells) liveProducts(ctx context.Context, ids []int64, limit int) ([]*Product, error) {
	if limit <= 0 || limit > maxCrossSells {
		limit = maxCrossSells
	}
	if len(ids) == 0 {
		return []*Product{}, nil
	}
	found, _, err := u.app.catalog.ListProducts(ctx, ProductQuery{IDs: ids, Status: ProductActive, Limit: len(ids)})
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*Product, len(found))
	for _, p := range found {
		byID[p.ID] = p
	}
	out := make([]*Product, 0, min(limit, len(found)))
	for _, id := range ids {
		if p := byID[id]; p != nil && len(out) < limit {
			out = append(out, p)
		}
	}
	return out, nil
}

// addOnPriceSQL is an add-on line's offer price while the offer holds, and
// NULL when it does not: the add-on withdrawn, changed to another variant, left
// without an offer price, or outnumbering the product it is offered with. The
// line is then priced as the variant on its own. Never above what the variant
// costs this buyer anyway: a trade price lower than the offer still wins.
func addOnPriceSQL(effective string) string {
	return `(SELECT least(a.price_minor, ` + effective + `)
		FROM product_add_ons a
		WHERE a.id = l.add_on_id AND a.variant_id = l.variant_id AND a.price_minor IS NOT NULL
		  AND l.quantity <= (SELECT coalesce(sum(m.quantity), 0)
		                     FROM cart_line_items m JOIN variants mv ON mv.id = m.variant_id
		                     WHERE m.cart_id = l.cart_id AND m.add_on_id IS NULL
		                       AND mv.product_id = a.product_id))`
}

// AddAddOn puts an add-on in a basket at its offer price. The product it is
// offered with has to be there already, and there cannot be more of the
// add-on at its offer price than of that product. An add-on with no offer
// price is added as an ordinary line.
func (c *Carts) AddAddOn(ctx context.Context, tok string, addOnID int64, qty int) (*Cart, error) {
	if qty <= 0 {
		return nil, Validationf("quantity must be at least 1")
	}
	err := InTx(ctx, c.app.db, func(tx *sql.Tx) error {
		cartID, err := c.openCartID(ctx, tx, tok)
		if err != nil {
			return err
		}
		var productID, variantID int64
		var offer sql.NullInt64
		var live bool
		var title, addOnTitle string
		err = tx.QueryRowContext(ctx, `
			SELECT a.product_id, a.variant_id, a.price_minor, v.active AND ap.status = 'active', p.title, ap.title
			FROM product_add_ons a
			JOIN products p ON p.id = a.product_id
			JOIN variants v ON v.id = a.variant_id
			JOIN products ap ON ap.id = v.product_id
			WHERE a.id = $1`, addOnID).Scan(&productID, &variantID, &offer, &live, &title, &addOnTitle)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("add-on %d does not exist", addOnID)
		}
		if err != nil {
			return err
		}
		if !live {
			return Conflictf("%s is not available", addOnTitle)
		}
		if !offer.Valid {
			if err := c.addOrdinaryTx(ctx, tx, cartID, variantID, qty); err != nil {
				return err
			}
			return touchCart(ctx, tx, cartID, c.app.cfg.CartTTL)
		}

		var withProduct, existing int
		if err := tx.QueryRowContext(ctx, `
			SELECT (SELECT coalesce(sum(m.quantity), 0) FROM cart_line_items m JOIN variants mv ON mv.id = m.variant_id
			        WHERE m.cart_id = $1 AND m.add_on_id IS NULL AND mv.product_id = $2),
			       coalesce((SELECT quantity FROM cart_line_items WHERE cart_id = $1 AND add_on_id = $3), 0)`,
			cartID, productID, addOnID).Scan(&withProduct, &existing); err != nil {
			return err
		}
		if withProduct == 0 {
			return Conflictf("%s is offered with %s; add that first", addOnTitle, title)
		}
		if existing+qty > withProduct {
			return Conflictf("the offer on %s is for one with each %s in the basket", addOnTitle, title)
		}
		if err := checkVariantStock(ctx, tx, cartID, variantID, qty, 0); err != nil {
			return err
		}
		price, err := c.lineBuyerPrice(ctx, tx, cartID, variantID, existing+qty)
		if err != nil {
			return err
		}
		price = min(price, offer.Int64)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO cart_line_items (cart_id, variant_id, quantity, unit_price_minor, add_on_id)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (store_id, cart_id, add_on_id) WHERE add_on_id IS NOT NULL DO UPDATE
			SET quantity = cart_line_items.quantity + EXCLUDED.quantity,
			    unit_price_minor = EXCLUDED.unit_price_minor, updated_at = now()`,
			cartID, variantID, qty, price, addOnID); err != nil {
			return err
		}
		return touchCart(ctx, tx, cartID, c.app.cfg.CartTTL)
	})
	if err != nil {
		return nil, err
	}
	return c.GetByToken(ctx, tok)
}

// lineBuyerPrice is what one unit of a variant costs this basket's buyer at a
// quantity: the price lists for its verified address and channel.
func (c *Carts) lineBuyerPrice(ctx context.Context, tx *sql.Tx, cartID, variantID int64, qty int) (int64, error) {
	var email string
	var channel sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT coalesce(verified_email, ''), channel_id FROM carts WHERE id = $1`, cartID).Scan(&email, &channel); err != nil {
		return 0, err
	}
	var channelID *int64
	if channel.Valid {
		channelID = &channel.Int64
	}
	return c.app.Pricing().priceFor(ctx, tx, variantID, qty, email, channelID)
}

// checkVariantStock refuses adding qty of a variant when the shelf cannot
// cover every line of it in the basket, except the line being changed
// (exceptLine, zero for none), plus qty.
func checkVariantStock(ctx context.Context, tx *sql.Tx, cartID, variantID int64, qty int, exceptLine int64) error {
	var tracks, oversell bool
	var available, held int
	if err := tx.QueryRowContext(ctx, `
		SELECT v.track_inventory, v.continue_selling,
		       coalesce((SELECT sum(on_hand - reserved) FROM variant_stock WHERE variant_id = v.id), 0),
		       coalesce((SELECT sum(quantity) FROM cart_line_items
		                 WHERE cart_id = $2 AND variant_id = v.id AND id <> $3), 0)
		FROM variants v WHERE v.id = $1`, variantID, cartID, exceptLine).Scan(&tracks, &oversell, &available, &held); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("variant %d does not exist", variantID)
		}
		return err
	}
	if tracks && !oversell && available < held+qty {
		return Conflictf("only %d left in stock", available)
	}
	return nil
}

// settleAddOns brings a basket's add-on lines back inside their offers after
// any change to it: an add-on line may hold no more than there are of the
// product it is offered with, and none when the product is gone or the offer
// withdrawn. What no longer fits moves to the variant's ordinary line, at the
// variant's own price. Nothing leaves the basket the shopper put in it.
func (c *Carts) settleAddOns(ctx context.Context, tx *sql.Tx, cartID int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT l.id, l.variant_id, l.quantity,
		       CASE WHEN a.id IS NULL OR a.price_minor IS NULL THEN 0
		            ELSE (SELECT coalesce(sum(m.quantity), 0) FROM cart_line_items m JOIN variants mv ON mv.id = m.variant_id
		                  WHERE m.cart_id = l.cart_id AND m.add_on_id IS NULL AND mv.product_id = a.product_id) END
		FROM cart_line_items l
		LEFT JOIN product_add_ons a ON a.id = l.add_on_id AND a.variant_id = l.variant_id
		WHERE l.cart_id = $1 AND l.add_on_id IS NOT NULL
		ORDER BY l.id`, cartID)
	if err != nil {
		return err
	}
	type over struct {
		line, variant int64
		keep, excess  int
	}
	var moves []over
	for rows.Next() {
		var o over
		var qty, allowed int
		if err := rows.Scan(&o.line, &o.variant, &qty, &allowed); err != nil {
			rows.Close()
			return err
		}
		if qty > allowed {
			o.keep, o.excess = allowed, qty-allowed
			moves = append(moves, o)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, o := range moves {
		if o.keep == 0 {
			_, err = tx.ExecContext(ctx, `DELETE FROM cart_line_items WHERE id = $1`, o.line)
		} else {
			_, err = tx.ExecContext(ctx,
				`UPDATE cart_line_items SET quantity = $2, updated_at = now() WHERE id = $1`, o.line, o.keep)
		}
		if err != nil {
			return err
		}
		if err := c.addOrdinaryTx(ctx, tx, cartID, o.variant, o.excess); err != nil {
			return err
		}
	}
	return nil
}

// CartOffer is a listed discount code as a basket sees it: what it is, and
// either what it would take off now or what is still missing.
type CartOffer struct {
	Code             string `json:"code"`
	Title            string `json:"title"`
	Kind             string `json:"kind"`
	ValueBP          int    `json:"value_bp,omitempty"`
	ValueMinor       int64  `json:"value_minor,omitempty"`
	MinSubtotalMinor *int64 `json:"min_subtotal_minor,omitempty"`
	// Applies is whether the code would be accepted for this basket now.
	Applies bool `json:"applies"`
	// Amount is what it takes off when it applies (free shipping has none).
	Amount       *Money `json:"amount,omitempty"`
	FreeShipping bool   `json:"free_shipping,omitempty"`
	// Remaining is how much more the basket needs before a minimum is met.
	Remaining *Money `json:"remaining,omitempty"`
	// Reason is the engine's sentence when it does not apply for another
	// reason: a scoped code with none of its products in the basket.
	Reason string `json:"reason,omitempty"`
}

// maxCartOffers bounds the list a basket is shown.
const maxCartOffers = 20

// CartOffers are the codes the shop listed (Discount.Listed) that are live
// now, judged against this basket without claiming anything. A code with a
// usage limit already reached, or not started, or ended, is not listed.
func (s *Discounts) CartOffers(ctx context.Context, cart *Cart) ([]CartOffer, error) {
	rows, err := s.app.db.QueryContext(ctx, `
		SELECT `+discountColumns+` FROM discounts
		WHERE listed AND active AND code IS NOT NULL
		  AND (starts_at IS NULL OR starts_at <= now())
		  AND (ends_at IS NULL OR ends_at > now())
		  AND (usage_limit IS NULL OR used_count < usage_limit)
		ORDER BY coalesce(min_subtotal_minor, 0), id
		LIMIT $1`, maxCartOffers)
	if err != nil {
		return nil, err
	}
	var ds []*Discount
	for rows.Next() {
		d, err := scanDiscount(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		ds = append(ds, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	lines := cartDiscountLines(cart)
	out := make([]CartOffer, 0, len(ds))
	for _, d := range ds {
		o := CartOffer{Code: d.Code, Title: d.Title, Kind: d.Kind, ValueBP: d.ValueBP, ValueMinor: d.ValueMinor,
			MinSubtotalMinor: d.MinSubtotalMinor}
		if d.MinSubtotalMinor != nil && cart.Subtotal.AmountMinor < *d.MinSubtotalMinor {
			m := money(*d.MinSubtotalMinor-cart.Subtotal.AmountMinor, cart.Currency)
			o.Remaining = &m
			out = append(out, o)
			continue
		}
		applied, err := s.preview(ctx, d, cart.VerifiedEmail, lines)
		if err != nil {
			var api *APIError
			if !errors.As(err, &api) || api.Code != ErrValidation.Code {
				return nil, err
			}
			o.Reason = api.Message
			out = append(out, o)
			continue
		}
		o.Applies = true
		o.FreeShipping = applied.FreeShipping
		if !applied.FreeShipping {
			m := money(applied.AmountMinor, cart.Currency)
			o.Amount = &m
		}
		out = append(out, o)
	}
	return out, nil
}

// FreeShipping is how far a basket is from free delivery to a place, read
// from the shop's own rates.
type FreeShipping struct {
	// Qualifies is true when a delivery option for this basket there is free.
	Qualifies bool `json:"qualifies"`
	// Threshold is the smallest basket at which a free option starts, when
	// the basket is below it. Nil when there is none to reach: nothing is
	// free there, or the price is not decided by the basket's value.
	Threshold *Money `json:"threshold,omitempty"`
	// Remaining is Threshold less the basket's subtotal.
	Remaining *Money `json:"remaining,omitempty"`
	Subtotal  Money  `json:"subtotal"`
}

// FreeShipping answers for a basket and a destination. A basket priced as a
// customer group's is told only whether it qualifies: a group's rates are its
// own arrangement, and a threshold read from everybody's would be a promise
// the checkout might not keep.
func (s *Shipping) FreeShipping(ctx context.Context, q ShippingQuery) (*FreeShipping, error) {
	currency := s.app.Currency(ctx)
	out := &FreeShipping{Subtotal: money(q.SubtotalMinor, currency)}
	quotes, rated, err := s.options(ctx, s.app.db, q)
	if err != nil {
		return nil, err
	}
	if !rated {
		out.Qualifies = s.app.cfg.FlatShippingMinor == 0
		return out, nil
	}
	for _, quote := range quotes {
		if quote.Price.AmountMinor == 0 {
			out.Qualifies = true
			return out, nil
		}
	}
	if email := strings.ToLower(strings.TrimSpace(q.Email)); email != "" {
		country := strings.ToUpper(strings.TrimSpace(q.Country))
		mine, err := s.groupQuotes(ctx, s.app.db, country, stringArray(StateSpellings(country, q.State)),
			q.SubtotalMinor, q.WeightGrams, email)
		if err != nil || len(mine) > 0 {
			return out, err
		}
	}
	country := strings.ToUpper(strings.TrimSpace(q.Country))
	zoneID, found, err := s.publicZone(ctx, s.app.db, country, stringArray(StateSpellings(country, q.State)))
	if err != nil || !found {
		return out, err
	}
	var threshold sql.NullInt64
	if err := s.app.db.QueryRowContext(ctx, `
		SELECT min(min_subtotal_minor) FROM shipping_rates
		WHERE zone_id = $1 AND active AND customer_group_id IS NULL AND basis = 'price'
		  AND price_minor + handling_fee_minor = 0 AND min_subtotal_minor > $2`,
		zoneID, q.SubtotalMinor).Scan(&threshold); err != nil {
		return nil, err
	}
	if threshold.Valid {
		t := money(threshold.Int64, currency)
		r := money(threshold.Int64-q.SubtotalMinor, currency)
		out.Threshold, out.Remaining = &t, &r
	}
	return out, nil
}
