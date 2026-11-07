package gocommerce

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// GiftCards are money a shopper holds with the store (D250): a code with a
// balance, spent at checkout as a way of paying.
//
// A gift card pays; it does not discount. The order's lines, discount and tax
// are worked out exactly as if the card were not there, because GST falls on
// what was sold, not on how it was paid for, and the card's share is then
// taken off what is left to pay. orders.total_minor stays what the payment
// method collects, so every gateway charges and checks the same field it
// always has; orders.gift_card_minor is the card's share beside it, and the
// order's full value is the two added together.
//
// A card comes from one of two places: a store issues one by hand (a present,
// an apology, credit for a return), or a shopper buys a variant marked as a
// gift card and each unit becomes a card once the order is paid.
type GiftCards struct {
	app *App
}

// GiftCards returns the gift card service.
func (a *App) GiftCards() *GiftCards { return a.giftCards }

// M108 — gift cards, what they were spent on, and the three places that say
// one is involved: a variant sold as one, a cart a code was entered on and an
// order part-paid by one. The balance is kept on the card and every change to
// it is a row in gift_card_uses, so "where did this money go" has an answer
// that the balance alone cannot give.
const migration0108GiftCards = `
CREATE TABLE gift_cards (
    id            bigserial   PRIMARY KEY,
    code          text        NOT NULL CHECK (code <> ''),
    currency      text        NOT NULL,
    initial_minor bigint      NOT NULL CHECK (initial_minor > 0),
    balance_minor bigint      NOT NULL CHECK (balance_minor >= 0 AND balance_minor <= initial_minor),
    active        boolean     NOT NULL DEFAULT true,
    email         text        NOT NULL DEFAULT '',
    note          text        NOT NULL DEFAULT '',
    order_id      bigint      REFERENCES orders (id) ON DELETE SET NULL,
    expires_at    timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE gift_card_uses (
    id           bigserial   PRIMARY KEY,
    gift_card_id bigint      NOT NULL REFERENCES gift_cards (id) ON DELETE CASCADE,
    order_id     bigint      REFERENCES orders (id) ON DELETE SET NULL,
    amount_minor bigint      NOT NULL CHECK (amount_minor <> 0),
    reason       text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE variants ADD COLUMN gift_card boolean NOT NULL DEFAULT false;
ALTER TABLE carts ADD COLUMN gift_card_code text NOT NULL DEFAULT '';
ALTER TABLE orders ADD COLUMN gift_card_minor bigint NOT NULL DEFAULT 0 CHECK (gift_card_minor >= 0);
`

// M110 — a code is unique within its store. After the scope migration, which
// is what gives the table its store_id.
const migration0110GiftCardCodes = `
CREATE UNIQUE INDEX gift_cards_code ON gift_cards (store_id, code);
CREATE INDEX gift_cards_order ON gift_cards (order_id) WHERE order_id IS NOT NULL;
CREATE INDEX gift_card_uses_card ON gift_card_uses (gift_card_id);
CREATE INDEX gift_card_uses_order ON gift_card_uses (order_id) WHERE order_id IS NOT NULL;
`

// GiftCard is one card. Code is the whole code, which is the money: only the
// screens that need it carry it, and a list shows Last4.
type GiftCard struct {
	ID          int64         `json:"id"`
	Code        string        `json:"code,omitempty"`
	Last4       string        `json:"last4"`
	Initial     Money         `json:"initial"`
	Balance     Money         `json:"balance"`
	Active      bool          `json:"active"`
	Email       string        `json:"email"`
	Note        string        `json:"note"`
	OrderID     *int64        `json:"order_id,omitempty"`
	OrderNumber string        `json:"order_number,omitempty"`
	ExpiresAt   *time.Time    `json:"expires_at"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
	Uses        []GiftCardUse `json:"uses,omitempty"`
}

// GiftCardUse is one change to a card's balance: negative when it paid for
// something, positive when an order it paid for was cancelled.
type GiftCardUse struct {
	Amount      Money     `json:"amount"`
	Reason      string    `json:"reason"`
	OrderID     *int64    `json:"order_id,omitempty"`
	OrderNumber string    `json:"order_number,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// GiftCardBalance is what a shopper is told about a code they typed: enough
// to show "₹500 of ₹2,000 left", and nothing that identifies whose it is.
type GiftCardBalance struct {
	Last4     string     `json:"last4"`
	Balance   Money      `json:"balance"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// IssuedGiftCard is a card bought on an order, shown to the buyer on that
// order with its code, since the order page is where they look for it.
type IssuedGiftCard struct {
	Code    string `json:"code"`
	Last4   string `json:"last4"`
	Initial Money  `json:"initial"`
	Balance Money  `json:"balance"`
	Active  bool   `json:"active"`
}

// GiftCardInput issues a card by hand.
type GiftCardInput struct {
	AmountMinor int64      `json:"amount_minor"`
	Email       string     `json:"email"`
	Note        string     `json:"note"`
	ExpiresAt   *time.Time `json:"expires_at"`
}

// GiftCardPatch changes what may change after issue. The amount is not one of
// them: more money is a new card, and less is the card being spent.
type GiftCardPatch struct {
	Active    *bool       `json:"active"`
	Note      *string     `json:"note"`
	ExpiresAt **time.Time `json:"expires_at"`
}

// GiftCardQuery filters the list.
type GiftCardQuery struct {
	Search string
	Limit  int
	Offset int
}

// giftCardAlphabet leaves out 0, O, 1 and I, which a person reading a code
// off a card or a phone screen confuses. Sixteen characters of thirty-two is
// eighty bits, which nobody guesses through a rate-limited route.
const giftCardAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func newGiftCardCode() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = giftCardAlphabet[int(b[i])%len(giftCardAlphabet)]
	}
	return string(b), nil
}

// NormalizeGiftCardCode is a code as stored: capitals, without the spaces and
// dashes a person types it with.
func NormalizeGiftCardCode(code string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return r
	}, strings.ToUpper(strings.TrimSpace(code)))
}

func last4(code string) string {
	if len(code) <= 4 {
		return code
	}
	return code[len(code)-4:]
}

const giftCardColumns = `g.id, g.code, g.currency, g.initial_minor, g.balance_minor, g.active,
	g.email, g.note, g.order_id, coalesce(o.number, ''), g.expires_at, g.created_at, g.updated_at`

const giftCardFrom = ` FROM gift_cards g LEFT JOIN orders o ON o.id = g.order_id`

func scanGiftCard(row interface{ Scan(...any) error }) (*GiftCard, error) {
	g := &GiftCard{}
	var currency string
	var initial, balance int64
	var orderID sql.NullInt64
	var expires sql.NullTime
	if err := row.Scan(&g.ID, &g.Code, &currency, &initial, &balance, &g.Active,
		&g.Email, &g.Note, &orderID, &g.OrderNumber, &expires, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return nil, err
	}
	g.Last4 = last4(g.Code)
	g.Initial = money(initial, currency)
	g.Balance = money(balance, currency)
	if orderID.Valid {
		g.OrderID = &orderID.Int64
	}
	if expires.Valid {
		g.ExpiresAt = &expires.Time
	}
	return g, nil
}

// insertGiftCard writes a new card, drawing another code on the vanishing
// chance of a collision within the store.
func insertGiftCard(ctx context.Context, tx *sql.Tx, currency string, amount int64,
	email, note string, orderID *int64, expires *time.Time) (int64, string, error) {
	for attempt := 0; ; attempt++ {
		code, err := newGiftCardCode()
		if err != nil {
			return 0, "", err
		}
		var id int64
		// A savepoint, because a unique violation aborts the transaction it
		// happens in, and this one is usually an order being paid.
		if _, err := tx.ExecContext(ctx, `SAVEPOINT gift_card_code`); err != nil {
			return 0, "", err
		}
		err = tx.QueryRowContext(ctx, `
			INSERT INTO gift_cards (code, currency, initial_minor, balance_minor, email, note, order_id, expires_at)
			VALUES ($1, $2, $3, $3, $4, $5, $6, $7) RETURNING id`,
			code, currency, amount, strings.ToLower(strings.TrimSpace(email)), strings.TrimSpace(note),
			orderID, expires).Scan(&id)
		if err == nil {
			_, err = tx.ExecContext(ctx, `RELEASE SAVEPOINT gift_card_code`)
			return id, code, err
		}
		if !isUniqueViolation(err) || attempt >= 3 {
			return 0, "", err
		}
		if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT gift_card_code`); err != nil {
			return 0, "", err
		}
	}
}

// Issue creates a card by hand, in the store's currency. The response carries
// the whole code, once, for the operator to pass on.
func (s *GiftCards) Issue(ctx context.Context, in GiftCardInput) (*GiftCard, error) {
	if in.AmountMinor <= 0 {
		return nil, Validationf("a gift card needs an amount above zero")
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(time.Now()) {
		return nil, Validationf("the expiry date has already passed")
	}
	currency := s.app.Currency(ctx)
	var id int64
	err := InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		var code string
		var err error
		id, code, err = insertGiftCard(ctx, tx, currency, in.AmountMinor, in.Email, in.Note, nil, in.ExpiresAt)
		if err != nil {
			return err
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditGiftCardIssue, Entity: AuditEntityGiftCard, ID: id,
			Label:   "…" + last4(code),
			Summary: "Issued gift card …" + last4(code),
			// The code itself stays out of the history: the history is read by
			// more people than may hand out money.
			After: map[string]any{"amount_minor": in.AmountMinor, "currency": currency, "email": in.Email},
		})
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Get loads one card with its history.
func (s *GiftCards) Get(ctx context.Context, id int64) (*GiftCard, error) {
	g, err := scanGiftCard(s.app.db.QueryRowContext(ctx,
		`SELECT `+giftCardColumns+giftCardFrom+` WHERE g.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("gift card not found")
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.app.db.QueryContext(ctx, `
		SELECT u.amount_minor, u.reason, u.order_id, coalesce(o.number, ''), u.created_at
		FROM gift_card_uses u LEFT JOIN orders o ON o.id = u.order_id
		WHERE u.gift_card_id = $1 ORDER BY u.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	g.Uses = []GiftCardUse{}
	for rows.Next() {
		var u GiftCardUse
		var amount int64
		var orderID sql.NullInt64
		if err := rows.Scan(&amount, &u.Reason, &orderID, &u.OrderNumber, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.Amount = money(amount, g.Balance.Currency)
		if orderID.Valid {
			u.OrderID = &orderID.Int64
		}
		g.Uses = append(g.Uses, u)
	}
	return g, rows.Err()
}

// List returns a page of cards, newest first, without their codes. The search
// matches the last four characters or the email.
func (s *GiftCards) List(ctx context.Context, q GiftCardQuery) ([]*GiftCard, int, error) {
	where, args := "true", []any{}
	if needle := strings.TrimSpace(q.Search); needle != "" {
		args = append(args, "%"+strings.ToLower(needle)+"%", NormalizeGiftCardCode(needle))
		where = `(lower(g.email) LIKE $1 OR right(g.code, 4) = $2 OR o.number = $2)`
	}
	var total int
	if err := s.app.db.QueryRowContext(ctx,
		`SELECT count(*)`+giftCardFrom+` WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, q.Limit, q.Offset)
	rows, err := s.app.db.QueryContext(ctx, `SELECT `+giftCardColumns+giftCardFrom+` WHERE `+where+
		` ORDER BY g.id DESC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	list := []*GiftCard{}
	for rows.Next() {
		g, err := scanGiftCard(rows)
		if err != nil {
			return nil, 0, err
		}
		g.Code = ""
		list = append(list, g)
	}
	return list, total, rows.Err()
}

// Update switches a card on or off, or changes its note or expiry.
func (s *GiftCards) Update(ctx context.Context, id int64, p GiftCardPatch) (*GiftCard, error) {
	err := InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		var code string
		var active bool
		err := tx.QueryRowContext(ctx,
			`SELECT code, active FROM gift_cards WHERE id = $1 FOR UPDATE`, id).Scan(&code, &active)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("gift card not found")
		}
		if err != nil {
			return err
		}
		after := map[string]any{}
		if p.Active != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE gift_cards SET active = $2 WHERE id = $1`, id, *p.Active); err != nil {
				return err
			}
			after["active"] = *p.Active
		}
		if p.Note != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE gift_cards SET note = $2 WHERE id = $1`, id, strings.TrimSpace(*p.Note)); err != nil {
				return err
			}
			after["note"] = strings.TrimSpace(*p.Note)
		}
		if p.ExpiresAt != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE gift_cards SET expires_at = $2 WHERE id = $1`, id, *p.ExpiresAt); err != nil {
				return err
			}
			after["expires_at"] = *p.ExpiresAt
		}
		if len(after) == 0 {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE gift_cards SET updated_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		summary := "Changed gift card …" + last4(code)
		if p.Active != nil && *p.Active != active {
			summary = map[bool]string{true: "Switched on", false: "Switched off"}[*p.Active] + " gift card …" + last4(code)
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditGiftCardUpdate, Entity: AuditEntityGiftCard, ID: id,
			Label: "…" + last4(code), Summary: summary, After: after,
		})
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// usable reads a card by code for spending, locked when tx is given, and
// says in plain words why it cannot be spent if it cannot.
func (s *GiftCards) usable(ctx context.Context, q rowQuerier, code, currency string, lock bool) (id int64, balance int64, expires *time.Time, err error) {
	var cardCurrency string
	var active bool
	var exp sql.NullTime
	query := `SELECT id, currency, balance_minor, active, expires_at FROM gift_cards WHERE code = $1`
	if lock {
		query += ` FOR UPDATE`
	}
	err = q.QueryRowContext(ctx, query, NormalizeGiftCardCode(code)).Scan(&id, &cardCurrency, &balance, &active, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil, Validationf("that gift card code was not found; check it and try again")
	}
	if err != nil {
		return 0, 0, nil, err
	}
	if exp.Valid {
		expires = &exp.Time
	}
	switch {
	case !active:
		return 0, 0, nil, Validationf("that gift card can no longer be used")
	case expires != nil && !expires.After(time.Now()):
		return 0, 0, nil, Validationf("that gift card has expired")
	case !strings.EqualFold(cardCurrency, currency):
		return 0, 0, nil, Validationf("that gift card is in %s and this basket is in %s",
			strings.ToUpper(cardCurrency), strings.ToUpper(currency))
	case balance <= 0:
		return 0, 0, nil, Validationf("that gift card has nothing left on it")
	}
	return id, balance, expires, nil
}

// Check answers a shopper who typed a code: what it has left. Nothing is held;
// checkout decides again under its own lock.
func (s *GiftCards) Check(ctx context.Context, code, currency string) (*GiftCardBalance, error) {
	if NormalizeGiftCardCode(code) == "" {
		return nil, Validationf("a gift card code is required")
	}
	_, balance, expires, err := s.usable(ctx, s.app.db, code, currency, false)
	if err != nil {
		return nil, err
	}
	return &GiftCardBalance{Last4: last4(NormalizeGiftCardCode(code)),
		Balance: money(balance, currency), ExpiresAt: expires}, nil
}

// SetCartGiftCard puts a code on a cart after checking it, or clears it when
// the code is empty.
func (s *GiftCards) SetCartGiftCard(ctx context.Context, tok, code string) (*GiftCardBalance, error) {
	code = NormalizeGiftCardCode(code)
	var out *GiftCardBalance
	err := InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		cartID, err := s.app.carts.openCartID(ctx, tx, tok)
		if err != nil {
			return err
		}
		if code != "" {
			var currency string
			if err := tx.QueryRowContext(ctx, `SELECT currency FROM carts WHERE id = $1`, cartID).Scan(&currency); err != nil {
				return err
			}
			_, balance, expires, err := s.usable(ctx, tx, code, currency, false)
			if err != nil {
				return err
			}
			out = &GiftCardBalance{Last4: last4(code), Balance: money(balance, currency), ExpiresAt: expires}
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE carts SET gift_card_code = $2, updated_at = now() WHERE id = $1`, cartID, code)
		return err
	})
	return out, err
}

// giftCardCharge is a card's share of one checkout, decided before the order
// exists and written once it does.
type giftCardCharge struct {
	cardID int64
	last4  string
	amount int64
}

// chargeTx locks the cart's card and works out how much of due it pays. A code
// that cannot be spent refuses the checkout, the way an expired discount code
// does: the shopper takes it off or fixes it, and is never charged a total
// they were not shown.
func (s *GiftCards) chargeTx(ctx context.Context, tx *sql.Tx, code, currency string, due int64) (*giftCardCharge, error) {
	if code == "" || due <= 0 {
		return nil, nil
	}
	id, balance, _, err := s.usable(ctx, tx, code, currency, true)
	if err != nil {
		return nil, err
	}
	return &giftCardCharge{cardID: id, last4: last4(code), amount: min(balance, due)}, nil
}

// spendTx takes a charge off its card, against the order it paid for.
func (c *giftCardCharge) spendTx(ctx context.Context, tx *sql.Tx, orderID int64, number string) error {
	if c == nil || c.amount <= 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE gift_cards SET balance_minor = balance_minor - $2, updated_at = now() WHERE id = $1`,
		c.cardID, c.amount); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO gift_card_uses (gift_card_id, order_id, amount_minor, reason)
		VALUES ($1, $2, $3, $4)`, c.cardID, orderID, -c.amount, "Paid for order "+number)
	return err
}

// restoreTx gives back what cards paid towards an order being cancelled. What
// each card has on net for the order is returned, so a second call returns
// nothing and an order that was never part-paid by a card is untouched.
func (s *GiftCards) restoreTx(ctx context.Context, tx *sql.Tx, o *Order) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT gift_card_id, -sum(amount_minor)::bigint FROM gift_card_uses
		WHERE order_id = $1 GROUP BY gift_card_id HAVING sum(amount_minor) < 0
		ORDER BY gift_card_id`, o.ID)
	if err != nil {
		return err
	}
	type owed struct{ card, amount int64 }
	var back []owed
	for rows.Next() {
		var b owed
		if err := rows.Scan(&b.card, &b.amount); err != nil {
			rows.Close()
			return err
		}
		back = append(back, b)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, b := range back {
		if _, err := tx.ExecContext(ctx,
			`UPDATE gift_cards SET balance_minor = balance_minor + $2, updated_at = now() WHERE id = $1`,
			b.card, b.amount); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gift_card_uses (gift_card_id, order_id, amount_minor, reason)
			VALUES ($1, $2, $3, $4)`, b.card, o.ID, b.amount, "Order "+o.Number+" was cancelled"); err != nil {
			return err
		}
	}
	// Cards this order bought are switched off: the money for them is being
	// handed back, so the card must not also be spendable. One already part
	// spent is an operator's call, and they can see its history.
	_, err = tx.ExecContext(ctx,
		`UPDATE gift_cards SET active = false, updated_at = now() WHERE order_id = $1 AND active`, o.ID)
	return err
}

// issueForOrderTx makes a card for each unit of each gift card line on an order
// that has just been paid, worth what the unit was sold at. An order that
// already has cards gets none more, so paying, unpaying and paying again does
// not mint money twice.
func (s *GiftCards) issueForOrderTx(ctx context.Context, tx *sql.Tx, o *Order) error {
	var already bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM gift_cards WHERE order_id = $1)`, o.ID).Scan(&already); err != nil {
		return err
	}
	if already {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT l.quantity, l.unit_price_minor FROM order_lines l
		JOIN variants v ON v.id = l.variant_id
		WHERE l.order_id = $1 AND v.gift_card AND l.unit_price_minor > 0
		ORDER BY l.id`, o.ID)
	if err != nil {
		return err
	}
	type unit struct {
		qty   int
		price int64
	}
	var units []unit
	for rows.Next() {
		var u unit
		if err := rows.Scan(&u.qty, &u.price); err != nil {
			rows.Close()
			return err
		}
		units = append(units, u)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	orderID := o.ID
	for _, u := range units {
		for i := 0; i < u.qty; i++ {
			if _, _, err := insertGiftCard(ctx, tx, o.Currency, u.price, o.Email,
				"Bought on order "+o.Number, &orderID, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// issuedFor is the cards an order bought, for its page.
func (s *GiftCards) issuedFor(ctx context.Context, orderID int64) ([]IssuedGiftCard, error) {
	rows, err := s.app.db.QueryContext(ctx, `
		SELECT code, currency, initial_minor, balance_minor, active FROM gift_cards
		WHERE order_id = $1 ORDER BY id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IssuedGiftCard
	for rows.Next() {
		var c IssuedGiftCard
		var currency string
		var initial, balance int64
		if err := rows.Scan(&c.Code, &currency, &initial, &balance, &c.Active); err != nil {
			return nil, err
		}
		c.Last4, c.Initial, c.Balance = last4(c.Code), money(initial, currency), money(balance, currency)
		out = append(out, c)
	}
	return out, rows.Err()
}
