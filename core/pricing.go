package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Pricing decides what a variant costs when the answer depends on who is buying
// and how many.
//
// Everything else on the money path is unchanged by it. AddLine still snapshots
// a price into the cart line, checkout still re-prices under the lock, and the
// difference between the two is still what makes a shopper re-confirm — this
// only changes what "the current price" resolves to. There is still exactly one
// function that answers that question, which is the property worth keeping.
type Pricing struct {
	app *App
}

// Pricing returns the price-list service.
func (a *App) Pricing() *Pricing { return &Pricing{app: a} }

// CustomerGroup is a set of shoppers who share a price list.
//
// Membership is by email address because this engine has no customers table: a
// customer is an address that has ordered, which is what customers.go reads and
// the only handle a group can hold.
type CustomerGroup struct {
	ID        int64  `json:"id"`
	Code      string `json:"code"`
	Name      string `json:"name"`
	Members   int    `json:"members"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// CustomerGroupInput creates a group.
type CustomerGroupInput struct {
	// Code is the stable handle an import or a script names the group by, so a
	// renamed group stays the same group.
	Code string `json:"code"`
	Name string `json:"name"`
}

// CustomerGroupPatch updates one. A nil field is left alone.
type CustomerGroupPatch struct {
	Code *string `json:"code"`
	Name *string `json:"name"`
}

// PriceList is a set of prices that applies to a group, over a window.
type PriceList struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// GroupID nil is everybody — a launch price, a seasonal one.
	GroupID   *int64  `json:"group_id,omitempty"`
	GroupCode *string `json:"group_code,omitempty"`
	// ChannelID nil is every channel, for the same reason: the empty state has
	// to mean "no restriction", or adopting channels would silently retire
	// every list written before them.
	ChannelID   *int64  `json:"channel_id,omitempty"`
	ChannelCode *string `json:"channel_code,omitempty"`
	StartsAt    *string `json:"starts_at,omitempty"`
	EndsAt      *string `json:"ends_at,omitempty"`
	Active      bool    `json:"active"`
	// Priority breaks a tie when two lists both cover a line. Higher wins.
	Priority  int    `json:"priority"`
	Prices    int    `json:"prices"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// PriceListInput creates a list.
type PriceListInput struct {
	Name      string     `json:"name"`
	GroupID   *int64     `json:"group_id"`
	ChannelID *int64     `json:"channel_id"`
	StartsAt  *time.Time `json:"starts_at"`
	EndsAt    *time.Time `json:"ends_at"`
	Active    *bool      `json:"active"`
	Priority  int        `json:"priority"`
}

// PriceListPatch updates one. A nil field is left alone; GroupID uses the
// nullable form because clearing it — making the list everybody's — is a real
// intention that a nil pointer could not express.
type PriceListPatch struct {
	Name      *string    `json:"name"`
	GroupID   NullableID `json:"group_id"`
	ChannelID NullableID `json:"channel_id"`
	StartsAt  *time.Time `json:"starts_at"`
	EndsAt    *time.Time `json:"ends_at"`
	Active    *bool      `json:"active"`
	Priority  *int       `json:"priority"`
}

// PriceRow is one variant's price on one list, at one quantity break.
type PriceRow struct {
	VariantID int64 `json:"variant_id"`
	// MinQuantity 1 is "any quantity".
	MinQuantity int   `json:"min_quantity"`
	AmountMinor int64 `json:"amount_minor"`
}

// ---------------------------------------------------------------- resolution

// PriceFor returns what one unit of a variant costs, for this buyer at this
// quantity, in the store's currency's minor units.
//
// The rules, in the order they break ties:
//
//  1. the most specific quantity break wins — 50-up beats 10-up beats 1-up;
//  2. then the higher-priority list;
//  3. then the cheaper price.
//
// Cheapest last rather than dearest last is deliberate. Two live lists covering
// one line is an operator mistake either way, and charging the lower of the two
// is the one that cannot turn into a complaint.
//
// An email of "" is an anonymous cart, which reaches only the ungrouped lists.
// That is the property that stops a trade price leaking onto the storefront, and
// it is why the group test is written as "the list has no group OR this address
// is in it" rather than as a join that would quietly drop the ungrouped ones.
//
// No rows means no list covers this line, and the caller falls back to
// variants.price_minor — which is why this answers the base price itself rather
// than a sentinel: every caller wants a price, and none of them wants to
// remember the fallback.
func (p *Pricing) PriceFor(ctx context.Context, variantID int64, quantity int, email string) (int64, error) {
	return p.priceFor(ctx, p.app.db, variantID, quantity, email, nil)
}

// PriceInChannel is PriceFor on one storefront.
//
// A list that names a channel applies only there; one that names none applies
// everywhere, which is why a store that has not adopted channels is unaffected.
// Passing no channel reaches only the unscoped lists — a channel-scoped price
// is not something to charge somebody who is not on that channel.
func (p *Pricing) PriceInChannel(ctx context.Context, variantID int64, quantity int, email string, channelID int64) (int64, error) {
	var ch *int64
	if channelID > 0 {
		ch = &channelID
	}
	return p.priceFor(ctx, p.app.db, variantID, quantity, email, ch)
}

// priceFor is PriceFor against any querier, so checkout can call it inside the
// transaction that holds the row locks.
func (p *Pricing) priceFor(ctx context.Context, q rowQuerier, variantID int64, quantity int, email string, channelID *int64) (int64, error) {
	if quantity < 1 {
		quantity = 1
	}
	folded := strings.ToLower(strings.TrimSpace(email))

	var listed sql.NullInt64
	err := q.QueryRowContext(ctx, `
		SELECT p.amount_minor
		FROM price_list_prices p
		JOIN price_lists l ON l.id = p.price_list_id
		WHERE p.variant_id = $1
		  AND p.min_quantity <= $2
		  AND l.active
		  AND (l.starts_at IS NULL OR l.starts_at <= now())
		  AND (l.ends_at   IS NULL OR l.ends_at   >  now())
		  AND (
		        l.group_id IS NULL
		     OR ($3 <> '' AND EXISTS (
		            SELECT 1 FROM customer_group_members m
		            WHERE m.group_id = l.group_id AND m.email = $3))
		      )
		  -- A channel-scoped list reaches only that channel. With no channel in
		  -- play the comparison is NULL rather than true, so only the unscoped
		  -- lists survive — which is what a store that has not adopted channels
		  -- should see, and what stops a wholesale price reaching a web cart.
		  AND (l.channel_id IS NULL OR l.channel_id = $4)
		ORDER BY p.min_quantity DESC, l.priority DESC, p.amount_minor ASC
		LIMIT 1`, variantID, quantity, folded, channelID).Scan(&listed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, Internalf(err, "resolve the price for variant %d", variantID)
	}
	if listed.Valid {
		return listed.Int64, nil
	}

	var base int64
	if err := q.QueryRowContext(ctx,
		`SELECT price_minor FROM variants WHERE id = $1`, variantID).Scan(&base); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, NotFoundf("variant %d does not exist", variantID)
		}
		return 0, Internalf(err, "read the price of variant %d", variantID)
	}
	return base, nil
}

// ------------------------------------------------------------------- groups

func (p *Pricing) CreateGroup(ctx context.Context, in CustomerGroupInput) (*CustomerGroup, error) {
	code, err := normalizeHandle(in.Code)
	if err != nil {
		return nil, Validationf("a group needs a code: %v", err)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, Validationf("a group needs a name, which is what an operator sees")
	}
	var id int64
	if err := p.app.db.QueryRowContext(ctx,
		`INSERT INTO customer_groups (code, name) VALUES ($1, $2) RETURNING id`,
		code, name).Scan(&id); err != nil {
		return nil, translatePricingErr(err)
	}
	return p.Group(ctx, id)
}

func (p *Pricing) Group(ctx context.Context, id int64) (*CustomerGroup, error) {
	g := &CustomerGroup{}
	err := p.app.db.QueryRowContext(ctx, `
		SELECT g.id, g.code, g.name,
		       (SELECT count(*) FROM customer_group_members m WHERE m.group_id = g.id),
		       g.created_at, g.updated_at
		FROM customer_groups g WHERE g.id = $1`, id,
	).Scan(&g.ID, &g.Code, &g.Name, &g.Members, &g.CreatedAt, &g.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("customer group %d does not exist", id)
	}
	if err != nil {
		return nil, err
	}
	return g, nil
}

func (p *Pricing) Groups(ctx context.Context) ([]*CustomerGroup, error) {
	rows, err := p.app.db.QueryContext(ctx, `
		SELECT g.id, g.code, g.name,
		       (SELECT count(*) FROM customer_group_members m WHERE m.group_id = g.id),
		       g.created_at, g.updated_at
		FROM customer_groups g ORDER BY g.name, g.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*CustomerGroup{}
	for rows.Next() {
		g := &CustomerGroup{}
		if err := rows.Scan(&g.ID, &g.Code, &g.Name, &g.Members, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (p *Pricing) UpdateGroup(ctx context.Context, id int64, patch CustomerGroupPatch) (*CustomerGroup, error) {
	sets, args := []string{}, []any{}
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, col+" = $"+strconv.Itoa(len(args)))
	}
	if patch.Code != nil {
		code, err := normalizeHandle(*patch.Code)
		if err != nil {
			return nil, Validationf("a group needs a code: %v", err)
		}
		add("code", code)
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			return nil, Validationf("a group needs a name")
		}
		add("name", name)
	}
	if len(sets) == 0 {
		return p.Group(ctx, id)
	}
	add("updated_at", time.Now())
	args = append(args, id)
	res, err := p.app.db.ExecContext(ctx,
		`UPDATE customer_groups SET `+strings.Join(sets, ", ")+` WHERE id = $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, translatePricingErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, NotFoundf("customer group %d does not exist", id)
	}
	return p.Group(ctx, id)
}

// DeleteGroup removes a group, its membership and the lists priced for it.
//
// Cascading rather than refusing, which is the opposite of what a category does
// with its products, and for a reason that is not laziness: a product orphaned
// by a deleted category is still a valuable thing that has lost its filing,
// while a price list whose audience has gone is not orphaned but meaningless —
// it would price for nobody and could never be reached again.
func (p *Pricing) DeleteGroup(ctx context.Context, id int64) error {
	res, err := p.app.db.ExecContext(ctx, `DELETE FROM customer_groups WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return NotFoundf("customer group %d does not exist", id)
	}
	return nil
}

// AddMember puts an address in a group. Adding one twice is not an error: the
// caller asked for it to be a member, and it is.
func (p *Pricing) AddMember(ctx context.Context, groupID int64, email string) error {
	folded, err := foldEmail(email)
	if err != nil {
		return err
	}
	res, err := p.app.db.ExecContext(ctx, `
		INSERT INTO customer_group_members (group_id, email) VALUES ($1, $2)
		ON CONFLICT (group_id, email) DO NOTHING`, groupID, folded)
	if err != nil {
		return translatePricingErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Either already a member, or the group does not exist. Only the second
		// is worth an error, so it is checked rather than assumed.
		if _, err := p.Group(ctx, groupID); err != nil {
			return err
		}
	}
	return nil
}

// RemoveMember takes an address out. Removing one that is not in leaves the
// world in the state the caller asked for, so it is not an error.
func (p *Pricing) RemoveMember(ctx context.Context, groupID int64, email string) error {
	folded, err := foldEmail(email)
	if err != nil {
		return err
	}
	_, err = p.app.db.ExecContext(ctx,
		`DELETE FROM customer_group_members WHERE group_id = $1 AND email = $2`, groupID, folded)
	return err
}

func (p *Pricing) Members(ctx context.Context, groupID int64, limit, offset int) ([]string, int, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	var total int
	if err := p.app.db.QueryRowContext(ctx,
		`SELECT count(*) FROM customer_group_members WHERE group_id = $1`, groupID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := p.app.db.QueryContext(ctx,
		`SELECT email FROM customer_group_members WHERE group_id = $1
		 ORDER BY email LIMIT $2 OFFSET $3`, groupID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, 0, err
		}
		out = append(out, email)
	}
	return out, total, rows.Err()
}

// -------------------------------------------------------------------- lists

func (p *Pricing) CreateList(ctx context.Context, in PriceListInput) (*PriceList, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, Validationf("a price list needs a name")
	}
	if in.StartsAt != nil && in.EndsAt != nil && !in.EndsAt.After(*in.StartsAt) {
		return nil, Validationf("a window that ends at or before it starts covers nothing")
	}
	var id int64
	if err := p.app.db.QueryRowContext(ctx, `
		INSERT INTO price_lists (name, group_id, channel_id, starts_at, ends_at, active, priority)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		name, in.GroupID, in.ChannelID, in.StartsAt, in.EndsAt, boolOr(in.Active, true), in.Priority,
	).Scan(&id); err != nil {
		return nil, translatePricingErr(err)
	}
	return p.List(ctx, id)
}

const priceListColumns = `l.id, l.name, l.group_id, g.code, l.channel_id, ch.code,
	l.starts_at, l.ends_at, l.active, l.priority,
	(SELECT count(*) FROM price_list_prices pp WHERE pp.price_list_id = l.id),
	l.created_at, l.updated_at`

// priceListJoins is the two optional owners a list can name.
const priceListJoins = ` FROM price_lists l
	LEFT JOIN customer_groups g ON g.id = l.group_id
	LEFT JOIN channels ch ON ch.id = l.channel_id`

func scanPriceList(row interface{ Scan(...any) error }) (*PriceList, error) {
	l := &PriceList{}
	var groupID, channelID sql.NullInt64
	var code, channelCode, starts, ends sql.NullString
	if err := row.Scan(&l.ID, &l.Name, &groupID, &code, &channelID, &channelCode,
		&starts, &ends, &l.Active, &l.Priority, &l.Prices, &l.CreatedAt, &l.UpdatedAt); err != nil {
		return nil, err
	}
	if groupID.Valid {
		l.GroupID = &groupID.Int64
	}
	if code.Valid {
		l.GroupCode = &code.String
	}
	if channelID.Valid {
		l.ChannelID = &channelID.Int64
	}
	if channelCode.Valid {
		l.ChannelCode = &channelCode.String
	}
	if starts.Valid {
		l.StartsAt = &starts.String
	}
	if ends.Valid {
		l.EndsAt = &ends.String
	}
	return l, nil
}

func (p *Pricing) List(ctx context.Context, id int64) (*PriceList, error) {
	l, err := scanPriceList(p.app.db.QueryRowContext(ctx,
		`SELECT `+priceListColumns+priceListJoins+` WHERE l.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("price list %d does not exist", id)
	}
	if err != nil {
		return nil, err
	}
	return l, nil
}

func (p *Pricing) Lists(ctx context.Context) ([]*PriceList, error) {
	rows, err := p.app.db.QueryContext(ctx,
		`SELECT `+priceListColumns+priceListJoins+`
		 ORDER BY l.priority DESC, l.name, l.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PriceList{}
	for rows.Next() {
		l, err := scanPriceList(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (p *Pricing) UpdateList(ctx context.Context, id int64, patch PriceListPatch) (*PriceList, error) {
	sets, args := []string{}, []any{}
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, col+" = $"+strconv.Itoa(len(args)))
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			return nil, Validationf("a price list needs a name")
		}
		add("name", name)
	}
	if patch.GroupID.Present {
		// Cleared means "everybody", which is a real intention and the reason
		// this field is nullable rather than a plain pointer.
		add("group_id", patch.GroupID.Value)
	}
	if patch.ChannelID.Present {
		// And cleared here means "every channel", for the same reason.
		add("channel_id", patch.ChannelID.Value)
	}
	if patch.StartsAt != nil {
		add("starts_at", *patch.StartsAt)
	}
	if patch.EndsAt != nil {
		add("ends_at", *patch.EndsAt)
	}
	if patch.Active != nil {
		add("active", *patch.Active)
	}
	if patch.Priority != nil {
		add("priority", *patch.Priority)
	}
	if len(sets) == 0 {
		return p.List(ctx, id)
	}
	add("updated_at", time.Now())
	args = append(args, id)
	res, err := p.app.db.ExecContext(ctx,
		`UPDATE price_lists SET `+strings.Join(sets, ", ")+` WHERE id = $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, translatePricingErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, NotFoundf("price list %d does not exist", id)
	}
	return p.List(ctx, id)
}

func (p *Pricing) DeleteList(ctx context.Context, id int64) error {
	res, err := p.app.db.ExecContext(ctx, `DELETE FROM price_lists WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return NotFoundf("price list %d does not exist", id)
	}
	return nil
}

// SetPrice writes one variant's price on a list, at one quantity break.
//
// An upsert rather than an insert, because "this is the trade price" is what an
// operator means whether or not they set one last week, and making them delete
// first would turn a correction into two requests that can half-fail.
func (p *Pricing) SetPrice(ctx context.Context, listID int64, row PriceRow) error {
	if row.VariantID <= 0 {
		return Validationf("a price needs a variant")
	}
	if row.MinQuantity < 1 {
		return Validationf("a quantity break starts at 1; a break at zero units is not a thing anybody sells")
	}
	if row.AmountMinor < 0 {
		return Validationf("a price cannot be negative")
	}
	res, err := p.app.db.ExecContext(ctx, `
		INSERT INTO price_list_prices (price_list_id, variant_id, min_quantity, amount_minor)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (price_list_id, variant_id, min_quantity)
		DO UPDATE SET amount_minor = excluded.amount_minor`,
		listID, row.VariantID, row.MinQuantity, row.AmountMinor)
	if err != nil {
		return translatePricingErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return NotFoundf("price list %d does not exist", listID)
	}
	return nil
}

func (p *Pricing) RemovePrice(ctx context.Context, listID, variantID int64, minQuantity int) error {
	_, err := p.app.db.ExecContext(ctx, `
		DELETE FROM price_list_prices
		WHERE price_list_id = $1 AND variant_id = $2 AND min_quantity = $3`,
		listID, variantID, minQuantity)
	return err
}

func (p *Pricing) Prices(ctx context.Context, listID int64) ([]PriceRow, error) {
	rows, err := p.app.db.QueryContext(ctx, `
		SELECT variant_id, min_quantity, amount_minor
		FROM price_list_prices WHERE price_list_id = $1
		ORDER BY variant_id, min_quantity`, listID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PriceRow{}
	for rows.Next() {
		var r PriceRow
		if err := rows.Scan(&r.VariantID, &r.MinQuantity, &r.AmountMinor); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ------------------------------------------------------------------ helpers

func foldEmail(email string) (string, error) {
	folded := strings.ToLower(strings.TrimSpace(email))
	if folded == "" {
		return "", Validationf("an email address is required")
	}
	if !strings.Contains(folded, "@") {
		return "", Validationf("%q is not an email address", email)
	}
	return folded, nil
}

func translatePricingErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "customer_groups_code_key"):
		return Conflictf("a customer group with that code already exists")
	case strings.Contains(msg, "price_lists_group_id_fkey"):
		return Validationf("that customer group does not exist")
	case strings.Contains(msg, "price_list_prices_variant_id_fkey"):
		return Validationf("that variant does not exist")
	case strings.Contains(msg, "price_list_prices_price_list_id_fkey"):
		return NotFoundf("that price list does not exist")
	case strings.Contains(msg, "price_lists_window"):
		return Validationf("a window that ends at or before it starts covers nothing")
	}
	return err
}

// effectivePriceSQL is PriceFor's rule as a SQL scalar expression.
//
// The set-based paths need it per line without a round trip each: the checkout
// read walks every line of a cart inside the transaction that holds the row
// locks, and a Go call per line there would turn one query into N inside a lock.
//
// It must stay in step with priceFor above. TestCartAndCheckoutUsePriceLists is
// what proves the two agree — it exercises this expression through the cart and
// the order while TestPriceResolution exercises the Go one, and they assert the
// same numbers.
//
// The arguments are SQL expressions, not values: callers pass column references
// like "v.id" and "l.quantity", or a bound parameter like "$2". Nothing here is
// user input, so there is nothing to escape.
func effectivePriceSQL(variantExpr, qtyExpr, emailExpr, channelExpr, baseExpr string) string {
	return `coalesce((
		SELECT pp.amount_minor
		FROM price_list_prices pp
		JOIN price_lists pl ON pl.id = pp.price_list_id
		WHERE pp.variant_id = ` + variantExpr + `
		  AND pp.min_quantity <= ` + qtyExpr + `
		  AND pl.active
		  AND (pl.starts_at IS NULL OR pl.starts_at <= now())
		  AND (pl.ends_at   IS NULL OR pl.ends_at   >  now())
		  AND (
		        pl.group_id IS NULL
		     OR (coalesce(` + emailExpr + `, '') <> '' AND EXISTS (
		            SELECT 1 FROM customer_group_members m
		            WHERE m.group_id = pl.group_id
		              AND m.email = lower(` + emailExpr + `)))
		      )
		  AND (pl.channel_id IS NULL OR pl.channel_id = ` + channelExpr + `)
		ORDER BY pp.min_quantity DESC, pl.priority DESC, pp.amount_minor ASC
		LIMIT 1
	), ` + baseExpr + `)`
}
