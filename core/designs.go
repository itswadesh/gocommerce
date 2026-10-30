package gocommerce

import (
	"context"
	"database/sql"
	"errors"
)

// LineDesign is a design as a basket line and an order line keep it.
type LineDesign struct {
	// PreviewURL is the picture of the finished product, shown on the line.
	PreviewURL string
	// Snapshot is stored on the order line as metadata.design, so a placed
	// order keeps the design as it was bought whatever later happens to it.
	Snapshot Metadata
}

// LineDesigner vouches for a design going on a line of a variant. It runs
// inside the transaction that adds the line, and again inside checkout's,
// before the order number is drawn: an error refuses the line or the whole
// checkout. Return an *APIError for a refusal the shopper should read.
//
// It may lock and update the module's own rows through tx, which is how a
// design being bought is kept from a sweep and from a second order. It never
// writes a core table and never does network I/O, for a checkout guard's
// reasons (guards.go).
type LineDesigner func(ctx context.Context, tx *sql.Tx, designID string, variantID int64) (*LineDesign, error)

// RegisterLineDesigner installs the module that makes designs. A store has at
// most one: two would each have to vouch for designs the other made.
func (a *App) RegisterLineDesigner(fn LineDesigner) {
	switch {
	case fn == nil:
		a.regErrf("module %q: a nil line designer", a.ownerName())
	case a.designer != nil:
		a.regErrf("module %q: module %q already registered the line designer", a.ownerName(), a.designerOwner)
	default:
		a.designer, a.designerOwner = fn, a.ownerName()
	}
}

func (a *App) lineDesign(ctx context.Context, tx *sql.Tx, designID string, variantID int64) (*LineDesign, error) {
	if a.designer == nil {
		return nil, Conflictf("this store does not take designs")
	}
	d, err := a.designer(ctx, tx, designID, variantID)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			return nil, err
		}
		return nil, Internalf(err, "line designer from module %q failed", a.designerOwner)
	}
	return d, nil
}

// AddDesignLine puts a variant made to a shopper's design in the basket, or
// adds to the line that already carries that design. The designer decides
// whether the design may be bought as this variant; the line is priced as the
// variant is, at the quantity the line will hold.
func (c *Carts) AddDesignLine(ctx context.Context, tok string, variantID int64, designID string, qty int) (*Cart, error) {
	if qty <= 0 {
		return nil, Validationf("quantity must be at least 1")
	}
	err := InTx(ctx, c.app.db, func(tx *sql.Tx) error {
		cartID, err := c.openCartID(ctx, tx, tok)
		if err != nil {
			return err
		}
		design, err := c.app.lineDesign(ctx, tx, designID, variantID)
		if err != nil {
			return err
		}
		var active bool
		err = tx.QueryRowContext(ctx, `SELECT active FROM variants WHERE id = $1`, variantID).Scan(&active)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("variant %d does not exist", variantID)
		}
		if err != nil {
			return err
		}
		if !active {
			return Conflictf("that variant is not available")
		}
		var existing int
		var stored sql.NullInt64
		if err := tx.QueryRowContext(ctx, `
			SELECT coalesce((SELECT quantity FROM cart_line_items WHERE cart_id = $1 AND design_id = $2), 0),
			       (SELECT unit_price_minor FROM cart_line_items WHERE cart_id = $1 AND design_id = $2)`,
			cartID, designID).Scan(&existing, &stored); err != nil {
			return err
		}
		if err := checkVariantStock(ctx, tx, cartID, variantID, qty, 0); err != nil {
			return err
		}
		price, err := c.lineBuyerPrice(ctx, tx, cartID, variantID, existing+qty)
		if err != nil {
			return err
		}
		// addOrdinaryTx's rule: adding more takes a new price only when the
		// quantity moved it. A price the shop changed since stays the line's
		// snapshot, for checkout to stop and ask about.
		if existing > 0 && stored.Valid {
			atOld, err := c.lineBuyerPrice(ctx, tx, cartID, variantID, existing)
			if err != nil {
				return err
			}
			if atOld != stored.Int64 {
				price = stored.Int64
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO cart_line_items (cart_id, variant_id, quantity, unit_price_minor, design_id, design_preview_url)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (store_id, cart_id, design_id) WHERE design_id IS NOT NULL DO UPDATE
			SET quantity = cart_line_items.quantity + EXCLUDED.quantity,
			    unit_price_minor = EXCLUDED.unit_price_minor, updated_at = now()`,
			cartID, variantID, qty, price, designID, design.PreviewURL); err != nil {
			return err
		}
		return touchCart(ctx, tx, cartID, c.app.cfg.CartTTL)
	})
	if err != nil {
		return nil, err
	}
	return c.GetByToken(ctx, tok)
}

// MediaStore is where this install keeps uploaded files, nil when it keeps
// none. A module that stores files which are not the shop's library (a
// shopper's photo for their order) writes them here, under the store the
// context is bound to, and deletes them itself.
func (a *App) MediaStore() MediaStore { return a.mediaStore }

// M96 — a basket line made to a shopper's design (D174).
//
// design_id is not a foreign key, for add_on_id's reason: the module owns the
// design and core must not.
//
// The order-line index is what lets the module ask whether an order holds a
// design (to refuse a second order of it, and to keep its photos for as long
// as the order exists) without reading every order line.
const migration0096LineDesigns = `
ALTER TABLE cart_line_items
    ADD COLUMN design_id text,
    ADD COLUMN design_preview_url text;
DROP INDEX cart_line_items_ordinary;
CREATE UNIQUE INDEX cart_line_items_ordinary
    ON cart_line_items (store_id, cart_id, variant_id) WHERE add_on_id IS NULL AND design_id IS NULL;
CREATE UNIQUE INDEX cart_line_items_design
    ON cart_line_items (store_id, cart_id, design_id) WHERE design_id IS NOT NULL;
CREATE INDEX cart_line_items_design_idx
    ON cart_line_items (store_id, design_id) WHERE design_id IS NOT NULL;
CREATE INDEX order_lines_design_idx
    ON order_lines (store_id, (metadata->'design'->>'id')) WHERE metadata ? 'design';
`
