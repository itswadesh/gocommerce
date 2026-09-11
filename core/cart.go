package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Cart statuses.
const (
	CartOpen      = "open"
	CartConverted = "converted"
	CartAbandoned = "abandoned"
)

// Cart states, as an operator sees them. A state is what a cart IS, which is
// not always what the status column says: an 'open' cart past expires_at has
// been given up on and the five-minute sweeper has simply not reached it.
// Reporting the column instead would give a different answer depending on where
// the ticker happens to be.
const (
	CartStateLive      = "live"
	CartStateAbandoned = "abandoned"
	CartStateConverted = "converted"
)

// cartSweepBatch bounds one abandon or purge pass. It paces the first sweep
// after M28 on a store carrying a backlog, instead of one transaction holding
// fifty thousand rows.
const cartSweepBatch = 500

// Cart is a guest's basket. Its token is the only credential involved —
// there is no account, and there never has to be, because guest checkout is a
// permanent guarantee rather than a stage this project grows out of.
type Cart struct {
	ID        int64      `json:"-"`
	Token     string     `json:"id"`
	Status    string     `json:"status"`
	Currency  string     `json:"currency"`
	Email     string     `json:"email,omitempty"`
	Lines     []CartLine `json:"line_items"`
	ItemCount int        `json:"item_count"`
	Subtotal  Money      `json:"subtotal"`
	Metadata  Metadata   `json:"metadata"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ExpiresAt time.Time  `json:"expires_at"`
}

// CartLine is one variant in a cart, with the price as it was when added.
type CartLine struct {
	ID           int64  `json:"id"`
	VariantID    int64  `json:"variant_id"`
	ProductID    int64  `json:"product_id"`
	SKU          string `json:"sku"`
	Title        string `json:"title"`
	VariantLabel string `json:"variant_label,omitempty"`
	Quantity     int    `json:"quantity"`
	UnitPrice    Money  `json:"unit_price"`
	Total        Money  `json:"total"`

	// The live view of the line, so a storefront can warn before checkout
	// rather than surprising the shopper at the end.
	CurrentPrice Money `json:"current_price"`
	Available    int   `json:"available"`
	InStock      bool  `json:"in_stock"`
	PriceChanged bool  `json:"price_changed"`
}

// CartSummary is a cart as an operator sees it in a list: enough to judge
// whether it is worth chasing, and never the token.
//
// The token is a live credential — possessing it authorises adding to, emptying
// and checking out the basket — so the admin API withholds it exactly as
// orderColumns withholds an order's access_token. A recovery flow gets it from
// the cart.abandoned payload, which reaches a consumer in-process rather than a
// browser tab.
//
// It is also not []*Cart: loadLines costs two queries per cart, so a page of
// fifty would be a hundred round trips for figures a table does not show.
type CartSummary struct {
	ID int64 `json:"id"`
	// State is derived; Status is the column. They differ for a cart the
	// sweeper has not reached yet, and the drawer says so rather than looking
	// like a bug.
	State        string     `json:"state"`
	Status       string     `json:"status"`
	Currency     string     `json:"currency"`
	Email        string     `json:"email,omitempty"`
	DiscountCode string     `json:"discount_code,omitempty"`
	LineCount    int        `json:"line_count"`
	ItemCount    int        `json:"item_count"`
	Subtotal     Money      `json:"subtotal"`
	Metadata     Metadata   `json:"metadata"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
	AbandonedAt  *time.Time `json:"abandoned_at,omitempty"`
}

// CartDetail is a summary with what is in the basket.
type CartDetail struct {
	CartSummary
	Lines []CartLine `json:"line_items"`
}

// CartQuery filters an admin cart listing.
type CartQuery struct {
	State         string // live | abandoned | converted; empty means all
	HasEmail      *bool
	HasLines      *bool
	MinValueMinor int64
	From, To      *time.Time // bounds on updated_at — the shopper's last touch
	Limit, Offset int
}

// Carts owns baskets.
type Carts struct {
	app *App
}

// Cart returns the cart service.
func (a *App) Cart() *Carts { return a.carts }

// Create opens an empty cart and mints its token.
func (c *Carts) Create(ctx context.Context, email string) (*Cart, error) {
	tok, err := token()
	if err != nil {
		return nil, err
	}
	var id int64
	err = c.app.db.QueryRowContext(ctx, `
		INSERT INTO carts (token, currency, email, expires_at)
		VALUES ($1, $2, $3, now() + make_interval(secs => $4))
		RETURNING id`,
		tok, c.app.cfg.Currency, nullString(email), c.app.cfg.CartTTL.Seconds()).Scan(&id)
	if err != nil {
		return nil, err
	}
	return c.GetByToken(ctx, tok)
}

// GetByToken loads a cart. The token is unguessable, so possessing it is the
// authorisation.
func (c *Carts) GetByToken(ctx context.Context, tok string) (*Cart, error) {
	if tok == "" {
		return nil, Validationf("a cart id is required")
	}
	cart := &Cart{}
	var meta []byte
	var email sql.NullString
	err := c.app.db.QueryRowContext(ctx, `
		SELECT id, token, status, currency, email, metadata, created_at, updated_at, expires_at
		FROM carts WHERE token = $1`, tok,
	).Scan(&cart.ID, &cart.Token, &cart.Status, &cart.Currency, &email,
		&meta, &cart.CreatedAt, &cart.UpdatedAt, &cart.ExpiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, NotFoundf("cart not found")
		}
		return nil, err
	}
	cart.Email = email.String
	if err := scanMetadata(meta, &cart.Metadata); err != nil {
		return nil, err
	}
	if err := c.loadLines(ctx, cart); err != nil {
		return nil, err
	}
	return cart, nil
}

// loadLines fills in the cart's lines with both the snapshot price and the
// live one, in a single join.
func (c *Carts) loadLines(ctx context.Context, cart *Cart) error {
	lines, itemCount, subtotal, err := c.linesOf(ctx, cart.ID, cart.Currency)
	if err != nil {
		return err
	}
	cart.Lines = lines
	cart.ItemCount = itemCount
	cart.Subtotal = money(subtotal, cart.Currency)
	return nil
}

// linesOf reads one cart's lines for whoever asks — the shopper's own cart and
// the operator's read model alike.
//
// The currency is the cart's own rather than cfg.Currency, which is the one
// behaviour change in the refactor and a correction: a cart snapshots its
// currency at Create, so a store that changed Config.Currency was re-labelling
// baskets opened before the change with a code their prices were never in.
func (c *Carts) linesOf(ctx context.Context, cartID int64, currency string) (lines []CartLine, itemCount int, subtotalMinor int64, err error) {
	rows, err := c.app.db.QueryContext(ctx, `
		SELECT l.id, l.variant_id, v.product_id, v.sku, p.title, l.quantity,
		       l.unit_price_minor, v.price_minor, v.track_inventory,
		       coalesce((SELECT sum(vs.on_hand - vs.reserved) FROM variant_stock vs WHERE vs.variant_id = v.id), 0), v.active
		FROM cart_line_items l
		JOIN variants v ON v.id = l.variant_id
		JOIN products p ON p.id = v.product_id
		WHERE l.cart_id = $1
		ORDER BY l.id`, cartID)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()

	lines = []CartLine{}
	var variantIDs []int64
	for rows.Next() {
		var l CartLine
		var currentPrice int64
		var tracks, active bool
		var available int
		if err := rows.Scan(&l.ID, &l.VariantID, &l.ProductID, &l.SKU, &l.Title,
			&l.Quantity, &l.UnitPrice.AmountMinor, &currentPrice, &tracks,
			&available, &active); err != nil {
			return nil, 0, 0, err
		}
		l.UnitPrice.Currency = currency
		l.Total = money(l.UnitPrice.AmountMinor*int64(l.Quantity), currency)
		l.CurrentPrice = money(currentPrice, currency)
		l.PriceChanged = currentPrice != l.UnitPrice.AmountMinor
		l.Available = available
		if !tracks {
			l.Available = -1 // not tracked
		}
		l.InStock = active && (!tracks || available >= l.Quantity)
		subtotalMinor += l.Total.AmountMinor
		itemCount += l.Quantity
		lines = append(lines, l)
		variantIDs = append(variantIDs, l.VariantID)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, err
	}

	// Attach each line's variant label ("M / Black") in one extra query.
	if len(variantIDs) > 0 {
		labels, err := c.variantLabels(ctx, variantIDs)
		if err != nil {
			return nil, 0, 0, err
		}
		for i := range lines {
			lines[i].VariantLabel = labels[lines[i].VariantID]
		}
	}
	return lines, itemCount, subtotalMinor, nil
}

func (c *Carts) variantLabels(ctx context.Context, ids []int64) (map[int64]string, error) {
	rows, err := c.app.db.QueryContext(ctx, `
		SELECT vov.variant_id, string_agg(pov.value, ' / ' ORDER BY o.position, o.id)
		FROM variant_option_values vov
		JOIN product_option_values pov ON pov.id = vov.option_value_id
		JOIN product_options o ON o.id = pov.option_id
		WHERE vov.variant_id = ANY($1::bigint[])
		GROUP BY vov.variant_id`, int64Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	labels := map[int64]string{}
	for rows.Next() {
		var id int64
		var label string
		if err := rows.Scan(&id, &label); err != nil {
			return nil, err
		}
		labels[id] = label
	}
	return labels, rows.Err()
}

// ------------------------------------------------------- the operator's view

// cartSummaryColumns is the admin read model's projection. The state comes out
// of the CASE rather than out of the status column, so a cart the sweeper has
// not reached yet reads as what it is.
const cartSummaryColumns = `c.id,
	CASE WHEN c.status = 'converted' THEN 'converted'
	     WHEN c.status = 'abandoned' THEN 'abandoned'
	     WHEN c.expires_at < now()   THEN 'abandoned'
	     ELSE 'live' END,
	c.status, c.currency, coalesce(c.email, ''), c.discount_code, c.metadata,
	c.created_at, c.updated_at, c.expires_at, c.abandoned_at,
	v.line_count, v.item_count, v.subtotal_minor`

// cartSummaryFrom aggregates the basket on read. Two of the filters are on the
// aggregate, so the count query and the page query have to share this FROM or
// they would count different sets. Carrying the figures as columns on carts
// instead is a second source of truth for a number that already exists, and the
// set is bounded by Config.CartRetention, which is what makes reading it
// affordable.
const cartSummaryFrom = `
	FROM carts c
	LEFT JOIN LATERAL (
	    SELECT count(*)::int                                              AS line_count,
	           coalesce(sum(l.quantity), 0)::int                          AS item_count,
	           coalesce(sum(l.quantity * l.unit_price_minor), 0)::bigint   AS subtotal_minor
	    FROM cart_line_items l WHERE l.cart_id = c.id
	) v ON true`

func scanCartSummary(row interface{ Scan(...any) error }) (*CartSummary, error) {
	s := &CartSummary{}
	var meta []byte
	var abandonedAt sql.NullTime
	var subtotalMinor int64
	if err := row.Scan(&s.ID, &s.State, &s.Status, &s.Currency, &s.Email,
		&s.DiscountCode, &meta, &s.CreatedAt, &s.UpdatedAt, &s.ExpiresAt,
		&abandonedAt, &s.LineCount, &s.ItemCount, &subtotalMinor); err != nil {
		return nil, err
	}
	// The row's own currency, never cfg.Currency: a basket filled before the
	// store changed currency holds prices in the code it snapshotted.
	s.Subtotal = money(subtotalMinor, s.Currency)
	if abandonedAt.Valid {
		at := abandonedAt.Time
		s.AbandonedAt = &at
	}
	if err := scanMetadata(meta, &s.Metadata); err != nil {
		return nil, err
	}
	return s, nil
}

// List is the admin cart listing: read-only, tokenless, newest first.
//
// The state filter uses the same derived predicate the projection reports, so
// the filter and the column beside it cannot disagree — an operator must not
// get a different answer depending on where the five-minute ticker happens to
// be.
func (c *Carts) List(ctx context.Context, q CartQuery) ([]*CartSummary, int, error) {
	where, args := []string{"1 = 1"}, []any{}
	add := func(expr string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(expr, len(args)))
	}
	switch q.State {
	case "":
	case CartStateLive:
		where = append(where, "(c.status = 'open' AND c.expires_at >= now())")
	case CartStateAbandoned:
		where = append(where, "(c.status = 'abandoned' OR (c.status = 'open' AND c.expires_at < now()))")
	case CartStateConverted:
		where = append(where, "c.status = 'converted'")
	default:
		// Loudly, rather than an empty page: a filter nobody can see is wrong is
		// worse than one that says so.
		return nil, 0, Validationf("state must be live, abandoned or converted")
	}
	if q.HasEmail != nil {
		if *q.HasEmail {
			where = append(where, "(c.email IS NOT NULL AND c.email <> '')")
		} else {
			where = append(where, "(c.email IS NULL OR c.email = '')")
		}
	}
	if q.HasLines != nil {
		if *q.HasLines {
			where = append(where, "v.line_count > 0")
		} else {
			where = append(where, "v.line_count = 0")
		}
	}
	if q.MinValueMinor > 0 {
		add("v.subtotal_minor >= $%d", q.MinValueMinor)
	}
	if q.From != nil {
		add("c.updated_at >= $%d", *q.From)
	}
	if q.To != nil {
		add("c.updated_at < $%d", *q.To)
	}
	clause := strings.Join(where, " AND ")

	var total int
	if err := c.app.db.QueryRowContext(ctx,
		`SELECT count(*)`+cartSummaryFrom+` WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	args = append(args, limit, q.Offset)
	rows, err := c.app.db.QueryContext(ctx,
		`SELECT `+cartSummaryColumns+cartSummaryFrom+` WHERE `+clause+
			fmt.Sprintf(" ORDER BY c.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var carts []*CartSummary
	for rows.Next() {
		s, err := scanCartSummary(rows)
		if err != nil {
			return nil, 0, err
		}
		carts = append(carts, s)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return carts, total, nil
}

// Get loads one cart for the admin screen, by its row id. By id and not by
// token: the token is the shopper's credential and the admin API does not carry
// it, so an operator has no handle with which to act on a stranger's basket.
func (c *Carts) Get(ctx context.Context, id int64) (*CartDetail, error) {
	summary, err := scanCartSummary(c.app.db.QueryRowContext(ctx,
		`SELECT `+cartSummaryColumns+cartSummaryFrom+` WHERE c.id = $1`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, NotFoundf("cart not found")
		}
		return nil, err
	}
	lines, _, _, err := c.linesOf(ctx, id, summary.Currency)
	if err != nil {
		return nil, err
	}
	return &CartDetail{CartSummary: *summary, Lines: lines}, nil
}

// AddLine adds a variant, or increases its quantity if it is already there.
// Adding records the price now; checkout is what makes a price authoritative.
func (c *Carts) AddLine(ctx context.Context, tok string, variantID int64, qty int) (*Cart, error) {
	if qty <= 0 {
		return nil, Validationf("quantity must be at least 1")
	}
	err := InTx(ctx, c.app.db, func(tx *sql.Tx) error {
		cartID, err := c.openCartID(ctx, tx, tok)
		if err != nil {
			return err
		}

		var price int64
		var active, tracks, oversell bool
		var available int
		err = tx.QueryRowContext(ctx, `
			SELECT price_minor, active, track_inventory, continue_selling,
			       coalesce((SELECT sum(on_hand - reserved) FROM variant_stock WHERE variant_id = $1), 0)
			FROM variants WHERE id = $1`, variantID,
		).Scan(&price, &active, &tracks, &oversell, &available)
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
		err = tx.QueryRowContext(ctx,
			`SELECT coalesce((SELECT quantity FROM cart_line_items WHERE cart_id = $1 AND variant_id = $2), 0)`,
			cartID, variantID).Scan(&existing)
		if err != nil {
			return err
		}
		if tracks && !oversell && available < existing+qty {
			return Conflictf("only %d left in stock", available)
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO cart_line_items (cart_id, variant_id, quantity, unit_price_minor)
			VALUES ($1, $2, $3, $4)
			-- The price is set on insert and never touched again. Re-adding a
			-- variant to bump its quantity must not silently move the units
			-- already in the cart to today's price: the snapshot is what lets
			-- checkout notice a price change and make the shopper re-confirm,
			-- and overwriting it here destroys that evidence.
			ON CONFLICT (cart_id, variant_id) DO UPDATE
			SET quantity = cart_line_items.quantity + EXCLUDED.quantity,
			    updated_at = now()`,
			cartID, variantID, qty, price)
		if err != nil {
			return err
		}
		return touchCart(ctx, tx, cartID, c.app.cfg.CartTTL)
	})
	if err != nil {
		return nil, err
	}
	return c.GetByToken(ctx, tok)
}

// UpdateLine sets a line's quantity. Zero removes it, which is what a quantity
// stepper stepping down to nothing means.
func (c *Carts) UpdateLine(ctx context.Context, tok string, lineID int64, qty int) (*Cart, error) {
	if qty < 0 {
		return nil, Validationf("quantity must not be negative")
	}
	if qty == 0 {
		return c.RemoveLine(ctx, tok, lineID)
	}
	err := InTx(ctx, c.app.db, func(tx *sql.Tx) error {
		cartID, err := c.openCartID(ctx, tx, tok)
		if err != nil {
			return err
		}
		var variantID int64
		err = tx.QueryRowContext(ctx,
			`SELECT variant_id FROM cart_line_items WHERE id = $1 AND cart_id = $2`,
			lineID, cartID).Scan(&variantID)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("line item %d is not in this cart", lineID)
		}
		if err != nil {
			return err
		}

		var tracks, oversell bool
		var available int
		if err := tx.QueryRowContext(ctx,
			`SELECT track_inventory, continue_selling, coalesce((SELECT sum(on_hand - reserved) FROM variant_stock WHERE variant_id = $1), 0)
			 FROM variants WHERE id = $1`,
			variantID).Scan(&tracks, &oversell, &available); err != nil {
			return err
		}
		if tracks && !oversell && available < qty {
			return Conflictf("only %d left in stock", available)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE cart_line_items SET quantity = $2, updated_at = now() WHERE id = $1`,
			lineID, qty); err != nil {
			return err
		}
		return touchCart(ctx, tx, cartID, c.app.cfg.CartTTL)
	})
	if err != nil {
		return nil, err
	}
	return c.GetByToken(ctx, tok)
}

// RemoveLine drops a line from the cart.
func (c *Carts) RemoveLine(ctx context.Context, tok string, lineID int64) (*Cart, error) {
	err := InTx(ctx, c.app.db, func(tx *sql.Tx) error {
		cartID, err := c.openCartID(ctx, tx, tok)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			`DELETE FROM cart_line_items WHERE id = $1 AND cart_id = $2`, lineID, cartID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return NotFoundf("line item %d is not in this cart", lineID)
		}
		return touchCart(ctx, tx, cartID, c.app.cfg.CartTTL)
	})
	if err != nil {
		return nil, err
	}
	return c.GetByToken(ctx, tok)
}

// SetEmail records the shopper's email on the cart, so an abandoned-cart
// consumer has something to work with. An empty value clears it, which is the
// shopper's own way to withdraw the address.
func (c *Carts) SetEmail(ctx context.Context, tok, email string) (*Cart, error) {
	email = strings.TrimSpace(email)
	// Not the full checkout validation: an address that is merely wrong still
	// records that a shopper was here. One with no @ in it is not an address.
	if email != "" && !strings.Contains(email, "@") {
		return nil, Validationf("a valid email is required")
	}
	err := InTx(ctx, c.app.db, func(tx *sql.Tx) error {
		cartID, err := c.openCartID(ctx, tx, tok)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE carts SET email = $2 WHERE id = $1`, cartID, nullString(email))
		if err != nil {
			return err
		}
		// Every other mutation extends the cart's life, and a shopper who has
		// just typed their address is the most active they have been.
		return touchCart(ctx, tx, cartID, c.app.cfg.CartTTL)
	})
	if err != nil {
		return nil, err
	}
	return c.GetByToken(ctx, tok)
}

// SetDiscountCode puts a promotion code on a cart, or clears it when the code
// is empty. It is the only writer of carts.discount_code — the two public
// handlers and the operator-placed order path all come through here, so the
// open-cart check and the trimming happen once rather than three times.
//
// It returns an error rather than the cart because neither caller reads one
// back, and GetByToken would re-read every line and its live price to answer a
// question nobody asked. Nothing is validated here: applyTx judges and claims
// the code under the checkout's own lock, which is the only place the answer
// can still be true when the order is written.
func (c *Carts) SetDiscountCode(ctx context.Context, tok, code string) error {
	return InTx(ctx, c.app.db, func(tx *sql.Tx) error {
		cartID, err := c.openCartID(ctx, tx, tok)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE carts SET discount_code = $2, updated_at = now() WHERE id = $1`,
			cartID, strings.TrimSpace(code))
		return err
	})
}

// openCartID resolves a token to a cart that can still be modified.
func (c *Carts) openCartID(ctx context.Context, tx *sql.Tx, tok string) (int64, error) {
	var id int64
	var status string
	err := tx.QueryRowContext(ctx,
		`SELECT id, status FROM carts WHERE token = $1 FOR UPDATE`, tok).Scan(&id, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, NotFoundf("cart not found")
	}
	if err != nil {
		return 0, err
	}
	switch status {
	case CartOpen:
	case CartAbandoned:
		// A shopper who came back has not abandoned anything; abandonment was
		// the store's observation, not their decision.
		if err := revive(ctx, tx, id, c.app.cfg.CartTTL); err != nil {
			return 0, err
		}
	default:
		// Deliberately an allowlist with a refusing default, not a denylist of
		// one: a future fourth status must arrive refused, not allowed. Only
		// 'converted' can reach here, which makes this message true for the
		// first time.
		return 0, Conflictf("this cart has already been checked out")
	}
	return id, nil
}

func touchCart(ctx context.Context, tx *sql.Tx, cartID int64, ttl time.Duration) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE carts
		SET updated_at = now(), expires_at = now() + make_interval(secs => $2)
		WHERE id = $1`, cartID, ttl.Seconds())
	return err
}

// revive brings an abandoned cart back to open under the caller's lock, with a
// fresh TTL, so somebody following a recovery link finds what they left.
//
// Only a mutation revives. A read must not, or a GET becomes a write and anyone
// holding a leaked token keeps a basket alive forever.
//
// abandoned_at goes back to NULL because carts_abandoned_at_matches_status
// requires it — the constraint makes this mandatory rather than remembered.
func revive(ctx context.Context, tx *sql.Tx, cartID int64, ttl time.Duration) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE carts
		SET status = 'open', abandoned_at = NULL, updated_at = now(),
		    expires_at = now() + make_interval(secs => $2)
		WHERE id = $1`, cartID, ttl.Seconds())
	return err
}

// Abandon marks expired carts that still hold something as abandoned and
// announces each one, in one transaction: a cart abandoned without its event is
// a basket nobody will ever be told about (AGENTS rule 4).
//
// The claim is FOR UPDATE SKIP LOCKED because runSweepers starts in every
// process. Two instances take different rows rather than blocking, and the
// status predicate is re-evaluated under the lock, so a cart is abandoned — and
// announced — exactly once however many replicas are running.
func (c *Carts) Abandon(ctx context.Context) (int, error) {
	var claimed int
	err := InTx(ctx, c.app.db, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			UPDATE carts c
			SET status = 'abandoned', abandoned_at = now()
			FROM (
				SELECT id FROM carts
				WHERE status = 'open' AND expires_at < now()
				  AND EXISTS (SELECT 1 FROM cart_line_items l WHERE l.cart_id = carts.id)
				ORDER BY expires_at
				FOR UPDATE SKIP LOCKED
				LIMIT $1
			) AS claimed
			WHERE c.id = claimed.id
			-- updated_at is deliberately absent from the SET list: it means
			-- "when the shopper last changed this", and the report exists to
			-- say how long the basket sat before it was given up on.
			RETURNING c.id, c.token, c.currency, coalesce(c.email, ''), c.discount_code,
			          c.metadata, c.created_at, c.updated_at, c.abandoned_at`,
			cartSweepBatch)
		if err != nil {
			return err
		}

		events := map[int64]*CartEvent{}
		var ids []int64
		func() {
			defer rows.Close()
			for rows.Next() {
				ev := &CartEvent{}
				var email string
				var meta []byte
				if err = rows.Scan(&ev.CartID, &ev.Token, &ev.Currency, &email,
					&ev.DiscountCode, &meta, &ev.CreatedAt, &ev.LastActiveAt,
					&ev.AbandonedAt); err != nil {
					return
				}
				ev.Email = email
				if len(meta) > 0 {
					if err = json.Unmarshal(meta, &ev.Metadata); err != nil {
						return
					}
				}
				ev.Lines = []OrderEventLine{}
				events[ev.CartID] = ev
				ids = append(ids, ev.CartID)
			}
			err = rows.Err()
		}()
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}

		// One batched query for every claimed cart's lines, carrying the label
		// subquery checkout already uses so no second pass is needed.
		lineRows, err := tx.QueryContext(ctx, `
			SELECT l.cart_id, v.sku, p.title,
			       coalesce((SELECT string_agg(pov.value, ' / ' ORDER BY o.position, o.id)
			                 FROM variant_option_values vov
			                 JOIN product_option_values pov ON pov.id = vov.option_value_id
			                 JOIN product_options o ON o.id = pov.option_id
			                 WHERE vov.variant_id = v.id), ''),
			       l.quantity, l.unit_price_minor
			FROM cart_line_items l
			JOIN variants v ON v.id = l.variant_id
			JOIN products p ON p.id = v.product_id
			WHERE l.cart_id = ANY($1::bigint[])
			ORDER BY l.cart_id, l.id`, int64Array(ids))
		if err != nil {
			return err
		}
		func() {
			defer lineRows.Close()
			for lineRows.Next() {
				var cartID int64
				var line OrderEventLine
				if err = lineRows.Scan(&cartID, &line.SKU, &line.Title,
					&line.VariantLabel, &line.Quantity, &line.UnitPriceMinor); err != nil {
					return
				}
				line.TotalMinor = line.UnitPriceMinor * int64(line.Quantity)
				ev, ok := events[cartID]
				if !ok {
					continue
				}
				ev.Lines = append(ev.Lines, line)
				ev.ItemCount += line.Quantity
				ev.SubtotalMinor += line.TotalMinor
			}
			err = lineRows.Err()
		}()
		if err != nil {
			return err
		}

		for _, id := range ids {
			if err := c.app.outbox.write(ctx, tx, EventCartAbandoned,
				AggregateCart, id, events[id]); err != nil {
				return err
			}
		}
		claimed = len(ids)
		return nil
	})
	if err != nil {
		return 0, err
	}
	c.app.nudgeOutbox()
	return claimed, nil
}

// SweepExpired deletes expired carts that hold nothing.
//
// It used to delete every expired cart, and the reason was good: POST
// /api/carts is unauthenticated, so unswept carts are an unbounded-growth
// vector rather than merely untidy. That reason is narrowed, not repealed. An
// empty basket has nothing to recover and is the shape probe traffic takes —
// minting one costs an anonymous POST with no body at all — while putting a
// line in one costs a valid variant id and passes AddLine's stock check, so it
// goes to Abandon instead.
//
// The NOT EXISTS is load-bearing: Abandon is capped at cartSweepBatch per pass,
// and without it this statement would delete the non-empty carts the cap has
// not reached yet.
func (c *Carts) SweepExpired(ctx context.Context) (int64, error) {
	res, err := c.app.db.ExecContext(ctx, `
		DELETE FROM carts
		WHERE status = 'open' AND expires_at < now()
		  AND NOT EXISTS (SELECT 1 FROM cart_line_items l WHERE l.cart_id = carts.id)`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PurgeAbandoned deletes abandoned carts older than Config.CartRetention. This
// is the second bound, and it is what makes keeping a cart safe now that expiry
// no longer deletes it — it is also the date the shopper's email on that row
// stops being kept.
//
// The clock is abandoned_at, not expires_at. Draining a backlog abandons carts
// whose expires_at is already weeks old, so an expires_at clock would abandon
// and purge them inside the same pass — announcing a live recovery token for a
// row that no longer exists.
//
// It emits nothing. A deletion after retention is a retention action, not a
// business transition: there is nothing downstream can do about evidence that
// has already gone.
func (c *Carts) PurgeAbandoned(ctx context.Context) (int64, error) {
	res, err := c.app.db.ExecContext(ctx, `
		DELETE FROM carts c
		USING (
			SELECT id FROM carts
			WHERE status = 'abandoned'
			  AND abandoned_at < now() - make_interval(secs => $1)
			ORDER BY abandoned_at
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		) AS aged
		WHERE c.id = aged.id`,
		c.app.cfg.CartRetention.Seconds(), cartSweepBatch)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
