package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Discounts are the one place a store takes money off a basket.
//
// A discount is a rule; what an order got is a snapshot of applying it. The two
// are separate tables and separate types here for the same reason an order line
// keeps its own price: a promotion that ended may be deleted, and every order it
// touched must still say what it was given.
//
// The arithmetic is deliberately dull. One rounding for the whole basket rather
// than one per line, integers throughout, and a discount that can never exceed
// what there is to discount — the interesting part of a promotion is the rule,
// and an operator reconciling a day's takings should never have to think about
// how it was computed.
type Discounts struct {
	app *App
}

// Discounts returns the discount service.
func (a *App) Discounts() *Discounts { return a.discounts }

// Discount kinds.
const (
	DiscountPercentage   = "percentage"
	DiscountFixed        = "fixed"
	DiscountFreeShipping = "free_shipping"
)

// Discount scopes. Every one of them is evaluated: a scope names the kind of
// thing in discount_targets a rule points at, and the value comes off only the
// lines those targets reach.
//
// The scope is plural and the target kind it selects is singular — `products`
// picks out rows of kind `product` — because a scope describes a rule and a
// kind describes one row. targetKindFor is the only place that mapping lives;
// a scope added without a matching evaluator branch is refused at the first
// call rather than quietly applying to the whole basket.
const (
	DiscountScopeOrder       = "order"
	DiscountScopeProducts    = "products"
	DiscountScopeCollections = "collections"
	DiscountScopeCategories  = "categories"
)

// Target kinds, as stored in discount_targets.kind. Singular, because a
// discount points at one kind of thing and its scope says which.
const (
	DiscountTargetProduct    = "product"
	DiscountTargetCollection = "collection"
	DiscountTargetCategory   = "category"
)

// targetKindFor maps a scope to the one kind of thing it may point at. Order
// scope and an unknown scope both map to "", which every caller reads as
// "cannot be targeted" and never as "targets everything". This is the single
// place a new scope const has to be taught about, so one added without an
// evaluator branch fails at the first call instead of falling through.
func targetKindFor(scope string) string {
	switch scope {
	case DiscountScopeProducts:
		return DiscountTargetProduct
	case DiscountScopeCollections:
		return DiscountTargetCollection
	case DiscountScopeCategories:
		return DiscountTargetCategory
	}
	return ""
}

// DiscountTarget is one thing a scoped rule points at, resolved for a reader.
//
// Title is filled on the way out and ignored on the way in — the ids are the
// record. For a category it is the full ancestry ("Apparel / Shirts"), because
// "Shirts" alone does not say which shirts. Missing is true when the catalog
// row is gone: the target row is kept rather than cleaned up, so a promotion
// that has narrowed is shown to an operator instead of narrowing in silence.
type DiscountTarget struct {
	Kind    string `json:"kind"`
	ID      int64  `json:"id"`
	Title   string `json:"title,omitempty"`
	Missing bool   `json:"missing,omitempty"`
}

// rowQuerier is *sql.DB or *sql.Tx. A preview reads outside a transaction and
// checkout reads inside the one creating the order; the questions are
// identical, so the handle is the parameter and there is one implementation of
// the matching rule.
type rowQuerier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// discountLine is everything the eligibility question needs about one basket
// line: which product it is, and what it is worth. The product and not the
// variant, because a promotion targets merchandising and all three scopes
// resolve through the product.
type discountLine struct {
	ProductID int64
	Total     int64
}

func totalOf(lines []discountLine) int64 {
	var sum int64
	for _, l := range lines {
		sum += l.Total
	}
	return sum
}

// Discount is a rule an operator maintains.
type Discount struct {
	ID int64 `json:"id"`
	// Code is what a shopper types. Empty means automatic — reserved, and not
	// evaluated at checkout yet.
	Code  string `json:"code,omitempty"`
	Title string `json:"title"`
	Kind  string `json:"kind"`
	// ValueBP is basis points for a percentage: 1000 is 10.00%.
	ValueBP int `json:"value_bp,omitempty"`
	// ValueMinor is the amount for a fixed discount, in minor units.
	ValueMinor int64 `json:"value_minor,omitempty"`

	Scope string `json:"scope"`
	// TargetIDs and Targets are what a scoped rule points at. Get and List fill
	// TargetIDs; only Get fills Targets, because a rule aimed at two hundred
	// products would otherwise put two hundred titles into a page of fifty
	// rows. GetByCode and applyTx's FOR UPDATE scan leave both zero on purpose:
	// the shopper path never needs them and the evaluator asks the database
	// directly, so filling them there would be a query bought for nothing.
	TargetIDs []int64          `json:"target_ids,omitempty"`
	Targets   []DiscountTarget `json:"targets,omitempty"`
	// MinSubtotalMinor is the basket a discount needs before it applies. Nil is
	// no minimum, which is not the same as zero. It is measured against the
	// whole basket even for a scoped rule: it is the price of entry to a
	// promotion, not the thing being discounted.
	MinSubtotalMinor *int64 `json:"min_subtotal_minor,omitempty"`

	StartsAt *time.Time `json:"starts_at,omitempty"`
	EndsAt   *time.Time `json:"ends_at,omitempty"`

	UsageLimit   *int `json:"usage_limit,omitempty"`
	UsedCount    int  `json:"used_count"`
	OncePerEmail bool `json:"once_per_email"`

	Active    bool      `json:"active"`
	Metadata  Metadata  `json:"metadata"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AppliedDiscount is what a basket actually got: the snapshot, before it is
// written to an order.
type AppliedDiscount struct {
	DiscountID  int64  `json:"discount_id"`
	Code        string `json:"code,omitempty"`
	Title       string `json:"title"`
	Kind        string `json:"kind"`
	AmountMinor int64  `json:"amount_minor"`
	// FreeShipping is set instead of an amount, because shipping is not part of
	// the subtotal a discount comes off.
	FreeShipping bool `json:"free_shipping,omitempty"`
}

// DiscountDetail is the rule plus what it has actually cost. The cost is a join
// and so is not part of the rule: the listing would need one aggregate per row,
// and the checkout read takes this row under a lock and has no use for it.
type DiscountDetail struct {
	*Discount
	// RedeemedTotal is the money this promotion has given away, cancelled orders
	// excluded — the same exclusion emailHasUsed makes — and in the store's
	// settlement currency. order_discounts has no currency column; the order does
	// (D14), so a store that has changed currency would otherwise get one integer
	// that is the sum of two different kinds of money.
	//
	// It is not used_count, and the two are allowed to disagree: used_count is
	// the claim made under the checkout lock and is never given back, because a
	// code limited to 100 uses was claimed 100 times whatever happened next.
	RedeemedTotal  Money `json:"redeemed_total"`
	RedeemedOrders int   `json:"redeemed_orders"`
	// RedeemedOtherCurrencyOrders counts what the currency constraint left out,
	// so the number above is narrow rather than quietly wrong.
	RedeemedOtherCurrencyOrders int `json:"redeemed_other_currency_orders"`
}

// DiscountRedemption is one order that used a rule: the snapshot row joined to
// the order it sits on. The code and title are the snapshot's, not the rule's —
// a promotion that has since been renamed must still read as what it was.
type DiscountRedemption struct {
	OrderID       int64  `json:"order_id"`
	Number        string `json:"number"`
	Status        string `json:"status"`
	PaymentStatus string `json:"payment_status"`
	Email         string `json:"email"`
	Code          string `json:"code,omitempty"`
	Title         string `json:"title"`
	Kind          string `json:"kind"`
	// Amount and OrderTotal carry the order's own snapshotted currency, never
	// Config.Currency: an order placed before the store changed currency still
	// has to read correctly (D14).
	Amount     Money     `json:"amount"`
	OrderTotal Money     `json:"order_total"`
	CreatedAt  time.Time `json:"created_at"`
}

// DiscountPreview is a dry run against a basket. A rule that would not apply is
// an answer here rather than an error: the operator asked whether it works, and
// "no, it expired on Friday" is the answer. See D43.
type DiscountPreview struct {
	Applies bool             `json:"applies"`
	Applied *AppliedDiscount `json:"applied,omitempty"`
	Reason  string           `json:"reason,omitempty"`
}

// DiscountInput creates one.
type DiscountInput struct {
	Code       string `json:"code"`
	Title      string `json:"title"`
	Kind       string `json:"kind"`
	ValueBP    int    `json:"value_bp"`
	ValueMinor int64  `json:"value_minor"`
	Scope      string `json:"scope"`
	// TargetIDs are products, collections or categories according to Scope. The
	// kind is never sent: the scope decides it one-to-one, and letting a client
	// send both would make a scope/kind disagreement representable.
	TargetIDs        []int64    `json:"target_ids"`
	MinSubtotalMinor *int64     `json:"min_subtotal_minor"`
	StartsAt         *time.Time `json:"starts_at"`
	EndsAt           *time.Time `json:"ends_at"`
	UsageLimit       *int       `json:"usage_limit"`
	OncePerEmail     bool       `json:"once_per_email"`
	Active           *bool      `json:"active"`
	Metadata         Metadata   `json:"metadata"`
}

// DiscountPatch updates one. Only the keys present are written.
//
// `used_count` is absent on purpose: it is a fact about what happened, not a
// setting, and letting an operator type over it would let a limited promotion be
// silently reopened.
type DiscountPatch struct {
	Code       *string `json:"code"`
	Title      *string `json:"title"`
	Kind       *string `json:"kind"`
	ValueBP    *int    `json:"value_bp"`
	ValueMinor *int64  `json:"value_minor"`
	Scope      *string `json:"scope"`
	// TargetIDs is a pointer for the reason ProductPatch.Tags is one: a patched
	// list replaces the whole set, so absent has to differ from empty.
	TargetIDs        *[]int64      `json:"target_ids"`
	MinSubtotalMinor NullableInt64 `json:"min_subtotal_minor"`
	StartsAt         *time.Time    `json:"starts_at"`
	EndsAt           *time.Time    `json:"ends_at"`
	UsageLimit       *int          `json:"usage_limit"`
	OncePerEmail     *bool         `json:"once_per_email"`
	Active           *bool         `json:"active"`
	Metadata         *Metadata     `json:"metadata"`
}

// DiscountQuery filters a listing.
type DiscountQuery struct {
	Search string
	Active *bool
	// Sort is an operator-chosen ordering; zero keeps the listing's own,
	// newest first.
	Sort   Sort
	Limit  int
	Offset int
}

const discountColumns = `id, coalesce(code, ''), title, kind,
	coalesce(value_bp, 0), coalesce(value_minor, 0), scope, min_subtotal_minor,
	starts_at, ends_at, usage_limit, used_count, once_per_email, active,
	metadata, created_at, updated_at`

func scanDiscount(row interface{ Scan(...any) error }) (*Discount, error) {
	var d Discount
	var meta []byte
	if err := row.Scan(&d.ID, &d.Code, &d.Title, &d.Kind, &d.ValueBP, &d.ValueMinor,
		&d.Scope, &d.MinSubtotalMinor, &d.StartsAt, &d.EndsAt, &d.UsageLimit,
		&d.UsedCount, &d.OncePerEmail, &d.Active, &meta, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	if err := scanMetadata(meta, &d.Metadata); err != nil {
		return nil, err
	}
	return &d, nil
}

// ------------------------------------------------------------------ validation

// validate checks a rule against itself: the value has to match the kind, and a
// window has to be a window. The database enforces all of this too — these
// messages exist so an operator is told what is wrong rather than shown a
// constraint name.
func validateDiscount(kind, scope string, valueBP int, valueMinor int64, targets []int64) error {
	switch kind {
	case DiscountPercentage:
		if valueBP <= 0 || valueBP > 10000 {
			return Validationf("a percentage discount is between 1 and 10000 basis points (100 is 1%%)")
		}
		if valueMinor != 0 {
			return Validationf("a percentage discount has no fixed amount")
		}
	case DiscountFixed:
		if valueMinor <= 0 {
			return Validationf("a fixed discount needs an amount above zero")
		}
		if valueBP != 0 {
			return Validationf("a fixed discount has no percentage")
		}
	case DiscountFreeShipping:
		if valueBP != 0 || valueMinor != 0 {
			return Validationf("free shipping carries no value of its own")
		}
	default:
		return Validationf("kind must be percentage, fixed or free_shipping")
	}

	switch scope {
	case "", DiscountScopeOrder, DiscountScopeProducts,
		DiscountScopeCollections, DiscountScopeCategories:
	default:
		return Validationf("scope must be order, products, collections or categories")
	}

	// The loud refusal at creation this table exists for. Until now the API
	// accepted a scope, offered no way to say which products, and let the
	// customer find out at the till.
	if kind == DiscountFreeShipping && scope != "" && scope != DiscountScopeOrder {
		return Validationf(
			"free shipping comes off the shipping, not off any line, so it cannot be scoped to %s; use a minimum basket instead",
			scope)
	}
	if scope == "" || scope == DiscountScopeOrder {
		if len(targets) > 0 {
			return Validationf("a discount that applies to the whole basket points at nothing in particular; choose a scope first, or leave target_ids out")
		}
	} else if len(targets) == 0 {
		return Validationf("a discount scoped to %s has to name at least one %s — send target_ids",
			scope, targetKindFor(scope))
	}
	return nil
}

// ------------------------------------------------------------------- targets

// requireTargets rejects the whole request when any id is unknown, so a typo in
// one of five does not silently store the other four.
//
// There is no foreign key to lean on — one polymorphic column cannot carry
// three — so this is check-then-act, and a product deleted between the check
// and the insert leaves a target that names nothing. That row is reported
// (`missing` on the way out, a count in doctor) rather than prevented, which is
// the trade the schema decision records.
func requireTargets(ctx context.Context, tx *sql.Tx, scope string, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	var q, noun string
	switch scope {
	case DiscountScopeProducts:
		q, noun = `SELECT id FROM products WHERE id = ANY($1::bigint[])`, "product"
	case DiscountScopeCollections:
		q, noun = `SELECT id FROM collections WHERE id = ANY($1::bigint[])`, "collection"
	case DiscountScopeCategories:
		q, noun = `SELECT id FROM categories WHERE id = ANY($1::bigint[])`, "category"
	default:
		return nil
	}
	rows, err := tx.QueryContext(ctx, q, int64Array(ids))
	if err != nil {
		return err
	}
	defer rows.Close()

	found := make(map[int64]bool, len(ids))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		found[id] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if !found[id] {
			return NotFoundf("%s %d does not exist", noun, id)
		}
	}
	return nil
}

// replaceTargets writes the whole list after clearing what was there.
//
// Replace rather than merge, for the reason SetProductCollections gives: the
// caller holds the whole list, and an "add" endpoint quietly re-creates what
// somebody removed in another tab. When the scope cannot be targeted this
// deletes and inserts nothing, which is how moving a rule back to order scope
// clears what it used to point at.
func replaceTargets(ctx context.Context, tx *sql.Tx, discountID int64, scope string, ids []int64) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM discount_targets WHERE discount_id = $1`, discountID); err != nil {
		return err
	}
	kind := targetKindFor(scope)
	if kind == "" || len(ids) == 0 {
		return nil
	}
	// One statement rather than a loop: there is no position to preserve here,
	// unlike product_collections.
	_, err := tx.ExecContext(ctx, `
		INSERT INTO discount_targets (discount_id, kind, target_id)
		SELECT $1, $2, unnest($3::bigint[])`, discountID, kind, int64Array(ids))
	return err
}

// discountTargetIDs reads one rule's target ids through whichever handle the
// caller already holds — Update reads them inside the lock it is about to
// write under, and nothing else needs them one rule at a time.
func discountTargetIDs(ctx context.Context, q rowQuerier, discountID int64) ([]int64, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT target_id FROM discount_targets WHERE discount_id = $1 ORDER BY target_id`,
		discountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// loadDiscountTargetIDs attaches the bare ids for a page of discounts in one
// query, the way loadOrderDiscounts does for a page of orders.
//
// Ids and not titles: a rule aimed at two hundred products would otherwise put
// two hundred names into a page of fifty rows, and a listing only wants to know
// *that* a rule is scoped and whether it points at anything. It is also what
// lets the panel's drawer open with the targets already in hand, so a save
// landing before a background fetch cannot clear a live promotion.
func (s *Discounts) loadDiscountTargetIDs(ctx context.Context, byID map[int64]*Discount, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.app.db.QueryContext(ctx, `
		SELECT discount_id, target_id FROM discount_targets
		WHERE discount_id = ANY($1::bigint[]) ORDER BY discount_id, target_id`, int64Array(ids))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var discountID, targetID int64
		if err := rows.Scan(&discountID, &targetID); err != nil {
			return err
		}
		if d := byID[discountID]; d != nil {
			d.TargetIDs = append(d.TargetIDs, targetID)
		}
	}
	return rows.Err()
}

// loadDiscountTargets resolves one discount's targets for a reader. A row whose
// catalog entry is gone comes back Missing with no title, because an operator
// has to be told a promotion has narrowed rather than discovering it at the
// till.
func (s *Discounts) loadDiscountTargets(ctx context.Context, discountID int64) ([]DiscountTarget, error) {
	rows, err := s.app.db.QueryContext(ctx, `
		SELECT t.kind, t.target_id,
		       coalesce(p.title, c.title, cat.title, ''),
		       (p.id IS NULL AND c.id IS NULL AND cat.id IS NULL)
		FROM discount_targets t
		LEFT JOIN products    p   ON t.kind = 'product'    AND p.id   = t.target_id
		LEFT JOIN collections c   ON t.kind = 'collection' AND c.id   = t.target_id
		LEFT JOIN categories  cat ON t.kind = 'category'   AND cat.id = t.target_id
		WHERE t.discount_id = $1
		ORDER BY t.kind, t.target_id`, discountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []DiscountTarget{}
	categoryIDs := []int64{}
	for rows.Next() {
		var t DiscountTarget
		if err := rows.Scan(&t.Kind, &t.ID, &t.Title, &t.Missing); err != nil {
			return nil, err
		}
		if t.Kind == DiscountTargetCategory && !t.Missing {
			categoryIDs = append(categoryIDs, t.ID)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// A category's own title does not say which category it is, so it is shown
	// by its ancestry — the same answer the product form gives.
	if len(categoryIDs) > 0 {
		paths, err := categoryPaths(ctx, s.app.db, categoryIDs)
		if err != nil {
			return nil, err
		}
		for i := range out {
			if out[i].Kind != DiscountTargetCategory {
				continue
			}
			if p, ok := paths[out[i].ID]; ok {
				out[i].Title = p.fullName
			}
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------- CRUD

// Create adds a discount.
func (s *Discounts) Create(ctx context.Context, in DiscountInput) (*Discount, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return nil, Validationf("title is required")
	}
	targets := dedupeIDs(in.TargetIDs)
	if err := validateDiscount(in.Kind, in.Scope, in.ValueBP, in.ValueMinor, targets); err != nil {
		return nil, err
	}
	if in.Scope == "" {
		in.Scope = DiscountScopeOrder
	}
	if in.EndsAt != nil && in.StartsAt != nil && !in.EndsAt.After(*in.StartsAt) {
		return nil, Validationf("a discount cannot end before it starts")
	}
	if in.UsageLimit != nil && *in.UsageLimit <= 0 {
		return nil, Validationf("a usage limit is a count above zero; leave it out for no limit")
	}
	meta, err := in.Metadata.value()
	if err != nil {
		return nil, Validationf("metadata is not valid JSON: %v", err)
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}

	// The statement gains a transaction so the rule and the record of who wrote
	// it commit together. "Who invented a hundred-percent-off code" is the
	// reason discounts.write was split out of catalog.write in the first place,
	// and until now the answer was nowhere.
	//
	// What the rule points at rides in the same transaction for a harder reason:
	// a scoped discount that exists pointing at nothing IS the defect this
	// feature closes, and it must not be constructible even for the width of a
	// failed second statement.
	var d *Discount
	err = InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		var derr error
		d, derr = scanDiscount(tx.QueryRowContext(ctx, `
			INSERT INTO discounts (code, title, kind, value_bp, value_minor, scope,
			                       min_subtotal_minor, starts_at, ends_at, usage_limit,
			                       once_per_email, active, metadata)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
			RETURNING `+discountColumns,
			nullString(strings.TrimSpace(in.Code)), in.Title, in.Kind,
			nullInt(in.ValueBP), nullInt64(in.ValueMinor), in.Scope,
			in.MinSubtotalMinor, in.StartsAt, in.EndsAt, in.UsageLimit,
			in.OncePerEmail, active, meta))
		if derr != nil {
			return translateDiscountErr(derr)
		}
		if err := requireTargets(ctx, tx, in.Scope, targets); err != nil {
			return err
		}
		if err := replaceTargets(ctx, tx, d.ID, in.Scope, targets); err != nil {
			return err
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditDiscountCreate, Entity: AuditEntityDiscount,
			ID: d.ID, Label: discountLabel(d), Summary: "Created the discount " + discountLabel(d),
			// Basis points and minor units on the wire, never a percentage
			// string and never a formatted amount (rule 6).
			After: map[string]any{
				"code": d.Code, "kind": d.Kind, "value_bp": d.ValueBP,
				"value_minor": d.ValueMinor, "scope": d.Scope, "active": d.Active,
				"target_ids": targets,
			},
		})
	})
	if err != nil {
		return nil, err
	}
	// Through Get so the response is byte-identical to a subsequent read of the
	// same id, targets resolved and all.
	return s.Get(ctx, d.ID)
}

// discountLabel is what to call a discount in a log a person reads: the code
// where there is one, and the title for an automatic discount, which has none.
func discountLabel(d *Discount) string {
	if d.Code != "" {
		return d.Code
	}
	return d.Title
}

// Get loads one by id.
func (s *Discounts) Get(ctx context.Context, id int64) (*Discount, error) {
	d, err := scanDiscount(s.app.db.QueryRowContext(ctx,
		`SELECT `+discountColumns+` FROM discounts WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("discount not found")
	}
	if err != nil {
		return nil, err
	}
	// Both shapes, so a client gets the same fields whichever route it used: the
	// ids are the record, and the resolved targets are what a drawer draws.
	targets, err := s.loadDiscountTargets(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	if len(targets) > 0 {
		d.Targets = targets
		d.TargetIDs = make([]int64, len(targets))
		for i, t := range targets {
			d.TargetIDs[i] = t.ID
		}
	}
	return d, nil
}

// GetByCode looks one up the way a shopper does: case-insensitively.
//
// The folding is PostgreSQL's, matching the unique index, and only
// PostgreSQL's. See the migration for why there is exactly one implementation.
func (s *Discounts) GetByCode(ctx context.Context, code string) (*Discount, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, NotFoundf("discount not found")
	}
	d, err := scanDiscount(s.app.db.QueryRowContext(ctx,
		`SELECT `+discountColumns+` FROM discounts WHERE lower(code) = lower($1)`, code))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("no discount with that code")
	}
	return d, err
}

// discountSorts is the discount listing's allow-list.
//
// `active` and `ends_at` are deliberately absent. The screen's State column
// renders a phase derived from active, starts_at, ends_at, usage_limit and
// used_count together, so ordering by `active` would group the off rows and
// leave scheduled, live, expired and used-up unordered among themselves — a
// header that half-lies. Nor is there a key for what a discount takes off:
// coalesce(value_bp, value_minor) orders 10% next to $10 as if 1000 and 1000
// meant the same thing.
var discountSorts = SortSpec{
	Tiebreak: "id",
	Columns: map[string]sortField{
		// code is NULL for an automatic discount and can never be '' (a CHECK
		// forbids it), so an automatic discount sorts last in both directions:
		// no code is not a code that sorts first.
		"code":       {"lower(code) ASC NULLS LAST", "lower(code) DESC NULLS LAST"},
		"title":      {"lower(title) ASC", "lower(title) DESC"},
		"used_count": {"used_count ASC", "used_count DESC"},
		"created_at": {"created_at ASC", "created_at DESC"},
		"id":         {"id ASC", "id DESC"},
	},
}

// List returns a page, newest first unless a sort says otherwise.
func (s *Discounts) List(ctx context.Context, q DiscountQuery) ([]*Discount, int, error) {
	where, args := []string{"true"}, []any{}
	if term := strings.TrimSpace(q.Search); term != "" {
		args = append(args, "%"+term+"%")
		where = append(where, fmt.Sprintf(
			"(title ILIKE $%d OR coalesce(code, '') ILIKE $%d)", len(args), len(args)))
	}
	if q.Active != nil {
		args = append(args, *q.Active)
		where = append(where, fmt.Sprintf("active = $%d", len(args)))
	}
	clause := strings.Join(where, " AND ")

	order, err := discountSorts.Clause(q.Sort, "id DESC")
	if err != nil {
		return nil, 0, err
	}

	var total int
	if err := s.app.db.QueryRowContext(ctx,
		`SELECT count(*) FROM discounts WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit, offset := q.Limit, q.Offset
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit, offset)
	rows, err := s.app.db.QueryContext(ctx,
		`SELECT `+discountColumns+` FROM discounts WHERE `+clause+
			fmt.Sprintf(" ORDER BY %s LIMIT $%d OFFSET $%d", order, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []*Discount{}
	for rows.Next() {
		d, err := scanDiscount(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	// One query for the whole page. This is what lets the panel tell a scoped
	// rule that points at nothing from a working one.
	byID := make(map[int64]*Discount, len(out))
	ids := make([]int64, 0, len(out))
	for _, d := range out {
		byID[d.ID] = d
		ids = append(ids, d.ID)
	}
	if err := s.loadDiscountTargetIDs(ctx, byID, ids); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// Update changes a rule.
//
// The whole of it runs inside one transaction, opened with the same FOR UPDATE
// on the discount row that applyTx takes. Scope and targets mean nothing apart
// from each other, so they are serialised against a checkout reading them: a
// checkout can never see the scope from one revision of a rule and the targets
// from another.
func (s *Discounts) Update(ctx context.Context, id int64, patch DiscountPatch) (*Discount, error) {
	var d *Discount
	err := InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		current, err := scanDiscount(tx.QueryRowContext(ctx,
			`SELECT `+discountColumns+` FROM discounts WHERE id = $1 FOR UPDATE`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("discount not found")
		}
		if err != nil {
			return err
		}
		return s.applyPatch(ctx, tx, current, patch, &d)
	})
	if err != nil {
		return nil, err
	}
	// Through Get, so a drawer that just moved a rule to order scope redraws
	// from the response and sees the cleared set rather than guessing.
	return s.Get(ctx, d.ID)
}

// applyPatch is Update's body, kept separate only so the lock above it reads as
// one thing. `out` receives the row the UPDATE returned.
func (s *Discounts) applyPatch(ctx context.Context, tx *sql.Tx, current *Discount, patch DiscountPatch, out **Discount) error {
	id := current.ID
	kind, scope := current.Kind, current.Scope
	valueBP, valueMinor := current.ValueBP, current.ValueMinor
	if patch.Kind != nil {
		kind = *patch.Kind
	}
	if patch.Scope != nil {
		scope = *patch.Scope
		if scope == "" {
			scope = DiscountScopeOrder
		}
	}
	if patch.ValueBP != nil {
		valueBP = *patch.ValueBP
	}
	if patch.ValueMinor != nil {
		valueMinor = *patch.ValueMinor
	}

	// Resolve the (scope, targets) pair the write will produce, then validate
	// that pair rather than either half of it.
	var targets []int64
	writeTargets := false
	switch {
	case patch.TargetIDs != nil:
		targets, writeTargets = dedupeIDs(*patch.TargetIDs), true
	case patch.Scope != nil && scope == DiscountScopeOrder:
		// Moving a rule back to the whole basket clears what it pointed at, in
		// the same transaction, so a target of the wrong kind cannot survive.
		targets, writeTargets = nil, true
	case patch.Scope != nil && scope != current.Scope:
		return Validationf("changing the scope changes what the targets mean; send target_ids with it")
	default:
		var err error
		if targets, err = discountTargetIDs(ctx, tx, id); err != nil {
			return err
		}
	}

	// Changing the kind without the value is the mistake this catches: a
	// percentage rule given a fixed kind would otherwise take one basis point
	// off the basket.
	if err := validateDiscount(kind, scope, valueBP, valueMinor, targets); err != nil {
		return err
	}

	set, args := []string{}, []any{id}
	after := map[string]any{}
	add := func(col string, v any) {
		args = append(args, v)
		set = append(set, fmt.Sprintf("%s = $%d", col, len(args)))
		after[col] = v
	}
	if patch.Code != nil {
		add("code", nullString(strings.TrimSpace(*patch.Code)))
	}
	if patch.Title != nil {
		title := strings.TrimSpace(*patch.Title)
		if title == "" {
			return Validationf("title is required")
		}
		add("title", title)
	}
	if patch.Kind != nil || patch.ValueBP != nil || patch.ValueMinor != nil {
		add("kind", kind)
		add("value_bp", nullInt(valueBP))
		add("value_minor", nullInt64(valueMinor))
	}
	if patch.Scope != nil {
		add("scope", scope)
	}
	if patch.MinSubtotalMinor.Present {
		add("min_subtotal_minor", patch.MinSubtotalMinor.Value)
	}
	if patch.StartsAt != nil {
		add("starts_at", *patch.StartsAt)
	}
	if patch.EndsAt != nil {
		add("ends_at", *patch.EndsAt)
	}
	if patch.UsageLimit != nil {
		if *patch.UsageLimit <= 0 {
			return Validationf("a usage limit is a count above zero")
		}
		add("usage_limit", *patch.UsageLimit)
	}
	if patch.OncePerEmail != nil {
		add("once_per_email", *patch.OncePerEmail)
	}
	if patch.Active != nil {
		add("active", *patch.Active)
	}
	if patch.Metadata != nil {
		meta, err := patch.Metadata.value()
		if err != nil {
			return Validationf("metadata is not valid JSON: %v", err)
		}
		add("metadata", meta)
	}
	// Re-aiming a promotion is a real edit, even when no column on the rule
	// moves.
	if len(set) == 0 && !writeTargets {
		return Validationf("nothing to change")
	}
	if writeTargets {
		after["target_ids"] = targets
		beforeIDs, err := discountTargetIDs(ctx, tx, id)
		if err != nil {
			return err
		}
		current.TargetIDs = beforeIDs
	}

	// `before` is free: the method already read `current` under the lock, before
	// touching anything.
	before := map[string]any{}
	for col, was := range map[string]any{
		"code": current.Code, "title": current.Title, "kind": current.Kind,
		"value_bp": current.ValueBP, "value_minor": current.ValueMinor,
		"scope": current.Scope, "active": current.Active,
		"once_per_email": current.OncePerEmail,
		"target_ids":     current.TargetIDs,
	} {
		if _, changed := after[col]; changed {
			before[col] = was
		}
	}

	// updated_at carries the row write even for a targets-only patch: it is what
	// holds the lock ordering, and it makes a re-aimed promotion read as changed.
	d, err := scanDiscount(tx.QueryRowContext(ctx,
		`UPDATE discounts SET `+strings.Join(append(append([]string{}, set...), "updated_at = now()"), ", ")+`
		 WHERE id = $1 RETURNING `+discountColumns, args...))
	if err != nil {
		return translateDiscountErr(err)
	}
	*out = d

	if writeTargets {
		if err := requireTargets(ctx, tx, scope, targets); err != nil {
			return err
		}
		if err := replaceTargets(ctx, tx, id, scope, targets); err != nil {
			return err
		}
	}
	return writeAudit(ctx, tx, auditRecord{
		Action: AuditDiscountUpdate, Entity: AuditEntityDiscount,
		ID: d.ID, Label: discountLabel(d), Summary: "Edited the discount " + discountLabel(d),
		Before: before, After: after,
	})
}

// Delete removes a discount. Orders that used it keep their snapshot.
func (s *Discounts) Delete(ctx context.Context, id int64) error {
	return InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		var code sql.NullString
		var title string
		err := tx.QueryRowContext(ctx,
			`DELETE FROM discounts WHERE id = $1 RETURNING code, title`, id).Scan(&code, &title)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("discount not found")
		}
		if err != nil {
			return translateDiscountErr(err)
		}
		label := code.String
		if label == "" {
			label = title
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditDiscountDelete, Entity: AuditEntityDiscount,
			ID: id, Label: label, Summary: "Deleted the discount " + label,
			Before: map[string]any{"code": code.String, "title": title},
		})
	})
}

// ------------------------------------------------------------------ applying

// discountRequest is what a basket knows about itself when it asks.
//
// The lines and not a subtotal: a rule scoped to chosen products cannot be
// answered from one number, and carrying both would make a request whose stated
// total disagrees with its own lines representable. The subtotal is totalOf the
// lines, which is what a cart's own subtotal already is.
type discountRequest struct {
	Code  string
	Email string
	Lines []discountLine
}

// cartDiscountLines is the evaluator's view of a cart.
//
// Total, not CurrentPrice: it is the figure cart.Subtotal is the sum of, so the
// preview answers about the basket the shopper is looking at. A price that has
// moved since is checkout's to refuse, under its own lock.
func cartDiscountLines(c *Cart) []discountLine {
	out := make([]discountLine, len(c.Lines))
	for i, l := range c.Lines {
		out[i] = discountLine{ProductID: l.ProductID, Total: l.Total.AmountMinor}
	}
	return out
}

// Preview reports what a basket would get, without consuming anything.
//
// The storefront calls this to show a figure before anybody commits. It is
// deliberately not the same code path as checkout — this one takes no locks and
// counts no usage — but it answers with the same arithmetic, so the number a
// shopper is shown is the number they are charged.
//
// It takes the cart rather than its subtotal because that promise now depends
// on preview and checkout deriving the same lines: a scoped rule comes off the
// lines its targets reach, and a bare total cannot say which those are.
func (s *Discounts) Preview(ctx context.Context, code, email string, cart *Cart) (*AppliedDiscount, error) {
	d, err := s.GetByCode(ctx, code)
	if err != nil {
		return nil, err
	}
	return s.preview(ctx, d, email, cartDiscountLines(cart))
}

// preview is the shared half of both dry runs: eligibility, then arithmetic. No
// lock, no usage claimed — applyTx is the consuming path and this must never
// become it.
func (s *Discounts) preview(ctx context.Context, d *Discount, email string, lines []discountLine) (*AppliedDiscount, error) {
	_, base, err := s.eligible(ctx, s.app.db, d, discountRequest{Email: email, Lines: lines})
	if err != nil {
		return nil, err
	}
	return applyDiscount(d, base), nil
}

// PreviewID is Preview for a rule somebody already has open: the admin dry run.
//
// Keyed by id rather than by code because an automatic discount has no code for
// GetByCode to find, and because the panel is testing a row it is looking at
// rather than a string a shopper typed.
//
// A refusal comes back as data — applies false and the engine's own sentence —
// where the public cart route answers 400 for the identical sentence. Two
// surfaces, two questions: a storefront is using a code and a refusal is a
// failed attempt; an operator is asking about one, and "no, it expired on
// Friday" is a successful answer. See D43. The request being wrong is still an
// error: an unknown id is 404 and a malformed basket is 400.
func (s *Discounts) PreviewID(ctx context.Context, id int64, email string, lines []discountLine) (*DiscountPreview, error) {
	d, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// A scoped rule comes off the lines its targets reach, so a bare subtotal
	// cannot answer for one — and letting it through would report "does not apply
	// to anything in this basket" about a basket the caller never sent. Say what
	// is missing instead.
	if targetKindFor(d.Scope) != "" && !hasProductLines(lines) {
		return &DiscountPreview{Reason: fmt.Sprintf(
			"this discount applies to chosen %s; send the lines to try it", d.Scope)}, nil
	}
	applied, err := s.preview(ctx, d, email, lines)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Code == ErrValidation.Code {
			return &DiscountPreview{Reason: apiErr.Message}, nil
		}
		// A database failure is still a failure: only the engine's own refusals
		// are findings.
		return nil, err
	}
	return &DiscountPreview{Applies: true, Applied: applied}, nil
}

// hasProductLines reports whether the basket says which products are in it. A
// bare subtotal arrives as one line with no product id, which is enough for an
// order-wide rule and not for any other kind.
func hasProductLines(lines []discountLine) bool {
	for _, l := range lines {
		if l.ProductID != 0 {
			return true
		}
	}
	return false
}

// Redemptions lists the orders this rule was spent on, newest first.
//
// Cancelled ones are listed and carry their status: hiding them would leave the
// operator unable to see why this list and redeemed_total differ. The rule is
// read first so an unknown or deleted one is a 404 rather than an empty page —
// and a deleted rule's redemptions are unreachable by design, because
// order_discounts.discount_id is ON DELETE SET NULL and the snapshots stay with
// their orders.
func (s *Discounts) Redemptions(ctx context.Context, discountID int64, limit, offset int) ([]DiscountRedemption, int, error) {
	if _, err := s.Get(ctx, discountID); err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.app.db.QueryRowContext(ctx,
		`SELECT count(*) FROM order_discounts od WHERE od.discount_id = $1`,
		discountID).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	rows, err := s.app.db.QueryContext(ctx, `
		SELECT o.id, o.number, o.status, o.payment_status, o.email, o.currency,
		       od.code, od.title, od.kind, od.amount_minor, o.total_minor, o.created_at
		FROM order_discounts od
		JOIN orders o ON o.id = od.order_id
		WHERE od.discount_id = $1
		ORDER BY o.id DESC
		LIMIT $2 OFFSET $3`, discountID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []DiscountRedemption{}
	for rows.Next() {
		var r DiscountRedemption
		var currency string
		var amount, orderTotal int64
		if err := rows.Scan(&r.OrderID, &r.Number, &r.Status, &r.PaymentStatus,
			&r.Email, &currency, &r.Code, &r.Title, &r.Kind,
			&amount, &orderTotal, &r.CreatedAt); err != nil {
			return nil, 0, err
		}
		r.Amount = money(amount, currency)
		r.OrderTotal = money(orderTotal, currency)
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// Redeemed is what the rule has cost: the money given away in the store's
// settlement currency, how many live orders carry a redemption, and how many
// were left out for being in another currency.
//
// Cancelled orders are excluded, following emailHasUsed's precedent, because
// nothing left the store on an order that was cancelled.
func (s *Discounts) Redeemed(ctx context.Context, discountID int64) (Money, int, int, error) {
	var sum int64
	var orders, other int
	err := s.app.db.QueryRowContext(ctx, `
		SELECT coalesce(sum(od.amount_minor) FILTER (WHERE o.currency = $2), 0),
		       count(*) FILTER (WHERE o.currency = $2),
		       count(*) FILTER (WHERE o.currency <> $2)
		FROM order_discounts od
		JOIN orders o ON o.id = od.order_id
		WHERE od.discount_id = $1 AND o.status <> 'cancelled'`,
		discountID, s.app.cfg.Currency).Scan(&sum, &orders, &other)
	if err != nil {
		return Money{}, 0, 0, err
	}
	return money(sum, s.app.cfg.Currency), orders, other, nil
}

// matchedProducts answers which of these products a scoped rule reaches.
//
// Three constant queries selected by scope rather than one query UNIONing them:
// a single query would evaluate the recursive category walk even for a
// product-scoped rule, because the third branch references it. Each branch
// costs the same one round trip and keeps the walk off the common path.
//
// The `default` is the loud gate. A scope const added later without an
// evaluator branch is refused at the first call instead of falling through to
// "take it off everything" — which the schema CHECK cannot help with, since a
// new const would be added to it in the same change.
func matchedProducts(ctx context.Context, q rowQuerier, discountID int64, scope string, productIDs []int64) (map[int64]bool, error) {
	arr := int64Array(productIDs)
	var qs string
	var args []any
	switch scope {
	case DiscountScopeProducts:
		qs = `
			SELECT target_id FROM discount_targets
			WHERE discount_id = $1 AND kind = 'product' AND target_id = ANY($2::bigint[])`
		args = []any{discountID, arr}
	case DiscountScopeCollections:
		qs = `
			SELECT DISTINCT pc.product_id
			FROM discount_targets t
			JOIN product_collections pc ON pc.collection_id = t.target_id
			WHERE t.discount_id = $1 AND t.kind = 'collection'
			  AND pc.product_id = ANY($2::bigint[])`
		args = []any{discountID, arr}
	case DiscountScopeCategories:
		qs = `
			WITH RECURSIVE up AS (
			    -- Every basket product's own category, then every ancestor above
			    -- it. Lifted from ratesForProducts so a discount on "Apparel"
			    -- reaches "Apparel / Shirts" by the same walk a tax rate does.
			    SELECT p.id AS product_id, c.id AS category_id, c.parent_id, 0 AS depth
			    FROM products p JOIN categories c ON c.id = p.category_id
			    WHERE p.id = ANY($2::bigint[])
			  UNION ALL
			    SELECT up.product_id, c.id, c.parent_id, up.depth + 1
			    FROM up JOIN categories c ON c.id = up.parent_id
			    WHERE up.depth < $3
			)
			SELECT DISTINCT up.product_id
			FROM up JOIN discount_targets t
			  ON t.discount_id = $1 AND t.kind = 'category' AND t.target_id = up.category_id`
		args = []any{discountID, arr, MaxCategoryDepth}
	default:
		return nil, Validationf("that discount has a scope this store does not understand: %q", scope)
	}

	out := map[int64]bool{}
	if len(productIDs) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, qs, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// eligibleFor reports which lines a rule covers and what they add up to.
//
// An order-wide rule covers everything and is answered with a nil mask, which
// every reader takes as "every line" — so an ordinary checkout exercises the
// same nil branch allocateDiscount relies on, and it cannot rot from disuse.
//
// A category target reaches its whole subtree, the rule tax already follows,
// bounded by MaxCategoryDepth so a cycle that got into the table cannot spin. A
// product with no category matches nothing, which narrows the rule rather than
// widening it — the safe direction to be wrong in.
func (s *Discounts) eligibleFor(ctx context.Context, q rowQuerier, d *Discount, lines []discountLine) ([]bool, int64, error) {
	if d.Scope == "" || d.Scope == DiscountScopeOrder {
		return nil, totalOf(lines), nil
	}

	ids := make([]int64, 0, len(lines))
	seen := make(map[int64]bool, len(lines))
	for _, l := range lines {
		if l.ProductID == 0 || seen[l.ProductID] {
			continue
		}
		seen[l.ProductID] = true
		ids = append(ids, l.ProductID)
	}
	matched, err := matchedProducts(ctx, q, d.ID, d.Scope, ids)
	if err != nil {
		return nil, 0, err
	}

	// Only the product ids crossed the wire; the totals are summed here, so a
	// product on two lines counts twice, correctly.
	mask := make([]bool, len(lines))
	var base int64
	for i, l := range lines {
		if matched[l.ProductID] {
			mask[i] = true
			base += l.Total
		}
	}
	return mask, base, nil
}

// countDiscountTargets answers the failure path's one question: is this a rule
// that points at nothing, or a basket holding none of what it points at?
func (s *Discounts) countDiscountTargets(ctx context.Context, q rowQuerier, discountID int64) (int, error) {
	var n int
	err := q.QueryRowContext(ctx,
		`SELECT count(*) FROM discount_targets WHERE discount_id = $1`, discountID).Scan(&n)
	return n, err
}

// eligible reports whether a discount may be used for this basket, or says why
// not, and hands back the part of the basket it comes off: a per-line mask and
// what those lines add up to. A nil mask means every line.
func (s *Discounts) eligible(ctx context.Context, q rowQuerier, d *Discount, req discountRequest) ([]bool, int64, error) {
	if !d.Active {
		return nil, 0, Validationf("that discount is not active")
	}
	now := time.Now()
	if d.StartsAt != nil && now.Before(*d.StartsAt) {
		return nil, 0, Validationf("that discount has not started yet")
	}
	if d.EndsAt != nil && !now.Before(*d.EndsAt) {
		return nil, 0, Validationf("that discount has expired")
	}
	if d.UsageLimit != nil && d.UsedCount >= *d.UsageLimit {
		return nil, 0, Validationf("that discount has been fully used")
	}
	// Measured against the whole basket even for a scoped rule: the minimum is
	// the price of entry to a promotion, not the thing being discounted.
	if d.MinSubtotalMinor != nil && totalOf(req.Lines) < *d.MinSubtotalMinor {
		return nil, 0, Validationf("that discount needs a basket of at least %d", *d.MinSubtotalMinor)
	}
	// The other rule that is stored and not evaluated yet (D29). applyTx never
	// reaches this — it short-circuits on an empty code before the row is looked
	// up — and neither does Preview, whose GetByCode rejects one. It exists for
	// PreviewID, which is the first caller that can hold a codeless rule, and
	// which would otherwise report a real amount for a promotion that can never
	// fire. The day automatic discounts do fire, this is the line to delete.
	if strings.TrimSpace(d.Code) == "" {
		return nil, 0, Validationf("automatic discounts are not applied at checkout yet")
	}
	if d.Kind == DiscountFreeShipping && d.Scope != "" && d.Scope != DiscountScopeOrder {
		// Shipping is not a line, so no scope can select it. validateDiscount
		// refuses this from here on; a row that predates that is refused here,
		// by name, rather than being treated as order-wide.
		return nil, 0, Validationf("free shipping cannot be scoped; this rule needs its scope set back to the whole basket")
	}

	mask, base, err := s.eligibleFor(ctx, q, d, req.Lines)
	if err != nil {
		return nil, 0, err
	}
	// Guarded by scope, and that guard is load-bearing: unguarded, a basket of
	// all-free items would start being refused an order-wide code with a message
	// about items it does not contain. The count runs only on the failure path,
	// so the happy path pays nothing for a message that names the missing half.
	if d.Scope != "" && d.Scope != DiscountScopeOrder && base == 0 {
		n, err := s.countDiscountTargets(ctx, q, d.ID)
		if err != nil {
			return nil, 0, err
		}
		if n == 0 {
			return nil, 0, Validationf(
				"that discount is not finished — it applies to chosen %s and none are chosen", d.Scope)
		}
		return nil, 0, Validationf("that discount does not apply to anything in this basket")
	}

	if d.OncePerEmail && strings.TrimSpace(req.Email) != "" {
		used, err := s.emailHasUsed(ctx, q, d.ID, req.Email)
		if err != nil {
			return nil, 0, err
		}
		if used {
			return nil, 0, Validationf("that discount has already been used with this email address")
		}
	}
	return mask, base, nil
}

// emailHasUsed reports whether this address already has an order carrying this
// discount. It is a deterrent, not a control — a second address defeats it —
// and D26 says so out loud rather than implying otherwise.
func (s *Discounts) emailHasUsed(ctx context.Context, q rowQuerier, discountID int64, email string) (bool, error) {
	var used bool
	err := q.QueryRowContext(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM order_discounts od
		    JOIN orders o ON o.id = od.order_id
		    WHERE od.discount_id = $1 AND lower(o.email) = lower($2)
		      AND o.status <> 'cancelled'
		)`, discountID, email).Scan(&used)
	return used, err
}

// applyDiscount is the arithmetic, and all of it.
//
// `base` is the part of the basket the rule covers: the whole subtotal for an
// order-wide rule, and the total of the lines its targets reach for a scoped
// one. Handing it the smaller basket IS the scoped behaviour — one rounding
// rule and one clamp rule in the engine rather than two that can drift apart.
//
// One rounding for the whole of it rather than one per line: per-line rounding
// drifts by a minor unit per line and leaves a total nobody can reconcile
// against the lines above it. Half up, because a shopper who is told "10% off"
// should not lose a paisa to banker's rounding.
//
// The result can never exceed the base, so a scoped fixed amount is capped at
// its own lines rather than at the basket. A fixed discount larger than what it
// covers takes all of it — the alternative is a negative total, which the
// order's own CHECK would refuse anyway, at a point far from the cause.
func applyDiscount(d *Discount, base int64) *AppliedDiscount {
	out := &AppliedDiscount{
		DiscountID: d.ID, Code: d.Code, Title: d.Title, Kind: d.Kind,
	}
	switch d.Kind {
	case DiscountPercentage:
		out.AmountMinor = (base*int64(d.ValueBP) + 5000) / 10000
	case DiscountFixed:
		out.AmountMinor = d.ValueMinor
	case DiscountFreeShipping:
		out.FreeShipping = true
	}
	if out.AmountMinor > base {
		out.AmountMinor = base
	}
	if out.AmountMinor < 0 {
		out.AmountMinor = 0
	}
	return out
}

// applyTx is the checkout path: eligibility, arithmetic, and the usage claim,
// all inside the transaction that is creating the order.
//
// The claim is a conditional UPDATE rather than a read followed by a write. Two
// checkouts racing for the last use of a code is the one contention that
// matters here, and it is the database's to resolve — zero rows affected means
// somebody else took it.
func (s *Discounts) applyTx(ctx context.Context, tx *sql.Tx, req discountRequest) (*AppliedDiscount, []bool, error) {
	code := strings.TrimSpace(req.Code)
	if code == "" {
		return nil, nil, nil
	}

	d, err := scanDiscount(tx.QueryRowContext(ctx,
		`SELECT `+discountColumns+` FROM discounts WHERE lower(code) = lower($1) FOR UPDATE`, code))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, Validationf("no discount with that code")
	}
	if err != nil {
		return nil, nil, err
	}
	// Strictly before the usage claim below: a rule that matches nothing in this
	// basket must never burn a use.
	mask, base, err := s.eligible(ctx, tx, d, req)
	if err != nil {
		return nil, nil, err
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE discounts SET used_count = used_count + 1, updated_at = now()
		WHERE id = $1 AND (usage_limit IS NULL OR used_count < usage_limit)`, d.ID)
	if err != nil {
		return nil, nil, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, nil, err
	} else if n == 0 {
		return nil, nil, Validationf("that discount has been fully used")
	}

	return applyDiscount(d, base), mask, nil
}

// recordOrderDiscount writes the snapshot beside the order that got it.
func recordOrderDiscount(ctx context.Context, tx *sql.Tx, orderID int64, a *AppliedDiscount) error {
	if a == nil {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO order_discounts (order_id, discount_id, code, title, kind, amount_minor)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		orderID, nullInt64(a.DiscountID), a.Code, a.Title, a.Kind, a.AmountMinor)
	return err
}

// loadOrderDiscounts attaches the snapshots to a page of orders in one query.
func (s *Orders) loadOrderDiscounts(ctx context.Context, byID map[int64]*Order, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.app.db.QueryContext(ctx, `
		SELECT order_id, coalesce(discount_id, 0), code, title, kind, amount_minor
		FROM order_discounts WHERE order_id = ANY($1::bigint[]) ORDER BY id`, int64Array(ids))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var orderID int64
		var a AppliedDiscount
		if err := rows.Scan(&orderID, &a.DiscountID, &a.Code, &a.Title, &a.Kind, &a.AmountMinor); err != nil {
			return err
		}
		if o := byID[orderID]; o != nil {
			o.Discounts = append(o.Discounts, a)
		}
	}
	return rows.Err()
}

func translateDiscountErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "discounts_code_key"):
		return Conflictf("that code is already in use")
	case strings.Contains(msg, "discounts_value_matches_kind"):
		return Validationf("the value does not match the kind of discount")
	case strings.Contains(msg, "discounts_window"):
		return Validationf("a discount cannot end before it starts")
	}
	return err
}

// nullInt and nullInt64 render a zero as SQL NULL, for the columns where zero
// is not a value the CHECK constraints permit.
func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// recomputeOrderDiscount is D27: what happens to a discount when the basket it
// came off changes, as amended by D39 for a scoped rule.
//
// A fixed amount survives — it was never a function of the basket. A percentage
// is taken again, because the basket it was a percentage of no longer exists
// and keeping the old figure would leave an order whose arithmetic does not add
// up. An order that has fallen below the minimum its discount required is
// refused: it no longer qualifies for the promotion, and choosing between
// removing the discount and refusing the edit is an operator's call, not a
// silent one.
//
// A scoped rule is re-judged against the lines it still covers, for a fixed
// amount as well as a percentage: without that, a product-scoped 10% would
// silently re-take 10% of the whole edited order. Scope is read in the outer
// query on purpose — the inner lookup only runs for a percentage with a live
// rule, so reading it there would leave a scoped fixed amount clamped to the
// whole subtotal, which is money out of the store with nobody told.
//
// The stored snapshot moves with it, so the order and its discount rows never
// disagree.
func recomputeOrderDiscount(ctx context.Context, tx *sql.Tx, orderID, subtotal, current int64) (int64, error) {
	var (
		rowID       int64
		kind        string
		discount    sql.NullInt64
		scope       string
		valueBP     int
		minSubtotal sql.NullInt64
	)
	err := tx.QueryRowContext(ctx, `
		SELECT od.id, od.kind, od.discount_id,
		       coalesce(d.scope, ''), coalesce(d.value_bp, 0), d.min_subtotal_minor
		FROM order_discounts od
		LEFT JOIN discounts d ON d.id = od.discount_id
		WHERE od.order_id = $1 ORDER BY od.id LIMIT 1`, orderID).
		Scan(&rowID, &kind, &discount, &scope, &valueBP, &minSubtotal)
	if errors.Is(err, sql.ErrNoRows) {
		// No snapshot: either the order predates discounts or it never had one.
		// Whatever is on the order stays, clamped to what there is to discount.
		if current > subtotal {
			return subtotal, nil
		}
		return current, nil
	}
	if err != nil {
		return 0, err
	}

	amount := current
	switch {
	case discount.Valid && scope != "" && scope != DiscountScopeOrder:
		// The rule still exists and covers part of the order. Today's targets,
		// not a copy taken when the order was placed — the same accepted drift
		// D27 already takes on value_bp.
		base, err := eligibleSubtotalForOrder(ctx, tx, orderID, discount.Int64, scope)
		if err != nil {
			return 0, err
		}
		if base == 0 {
			return 0, Conflictf(
				"this order's discount no longer applies to anything left on it; remove the discount first")
		}
		if minSubtotal.Valid && subtotal < minSubtotal.Int64 {
			return 0, Conflictf(
				"this order would fall below the %d its discount needs; remove the discount first",
				minSubtotal.Int64)
		}
		if kind == DiscountPercentage {
			amount = (base*int64(valueBP) + 5000) / 10000
		} else if amount > base {
			// Where D39 departs from D27: a scoped fixed amount is clamped to
			// what is left of the lines it covered, not to the whole basket.
			amount = base
		}
	case kind != DiscountPercentage:
		// A fixed amount was never a function of the basket; only the floor
		// below applies.
	case !discount.Valid:
		// The rule was deleted. Its percentage is unknowable, so the amount
		// stands as recorded rather than being guessed at.
		if current > subtotal {
			amount = subtotal
		}
	default:
		if minSubtotal.Valid && subtotal < minSubtotal.Int64 {
			return 0, Conflictf(
				"this order would fall below the %d its discount needs; remove the discount first",
				minSubtotal.Int64)
		}
		amount = (subtotal*int64(valueBP) + 5000) / 10000
	}
	if amount > subtotal {
		amount = subtotal
	}
	if amount != current {
		if _, err := tx.ExecContext(ctx,
			`UPDATE order_discounts SET amount_minor = $2 WHERE id = $1`, rowID, amount); err != nil {
			return 0, err
		}
	}
	return amount, nil
}

// orderDiscountMask says which of an order's lines its discount came off, given
// those lines' ids in the order the caller holds them. nil means every line.
//
// An order does not store that set — it is derived state that would go stale on
// the only occasions it is read — so it is answered from the rule's current
// targets, the same live read recomputeOrderDiscount takes. A deleted rule
// answers nil: what it covered is unknowable, and spreading the recorded amount
// over everything is what the order was written with.
//
// The line ids and not the products, because loadOrderLinesTx does not select
// product_id: a caller handing over what it loaded would hand over nothing, and
// an all-false mask reads as "the discount came off no line" — which would
// refund a customer more than they paid. Reading the column here costs one query
// and only on the scoped branch.
func orderDiscountMask(ctx context.Context, tx *sql.Tx, orderID int64, lineIDs []int64) ([]bool, error) {
	var discountID sql.NullInt64
	var scope string
	err := tx.QueryRowContext(ctx, `
		SELECT od.discount_id, coalesce(d.scope, '')
		FROM order_discounts od
		LEFT JOIN discounts d ON d.id = od.discount_id
		WHERE od.order_id = $1 ORDER BY od.id LIMIT 1`, orderID).Scan(&discountID, &scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !discountID.Valid || scope == "" || scope == DiscountScopeOrder {
		return nil, nil
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT id, coalesce(product_id, 0) FROM order_lines WHERE order_id = $1`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	productByLine := map[int64]int64{}
	ids := []int64{}
	seen := map[int64]bool{}
	for rows.Next() {
		var lineID, productID int64
		if err := rows.Scan(&lineID, &productID); err != nil {
			return nil, err
		}
		productByLine[lineID] = productID
		if productID != 0 && !seen[productID] {
			seen[productID] = true
			ids = append(ids, productID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	matched, err := matchedProducts(ctx, tx, discountID.Int64, scope, ids)
	if err != nil {
		return nil, err
	}
	mask := make([]bool, len(lineIDs))
	for i, lineID := range lineIDs {
		mask[i] = matched[productByLine[lineID]]
	}
	return mask, nil
}

// eligibleSubtotalForOrder is eligibleFor for an order that already exists.
//
// order_lines.product_id is nulled when a product is deleted, and such a line
// matches nothing — which is right: it is no longer the thing the promotion
// pointed at, and narrowing is the safe direction to be wrong in.
//
// Storing the covered set on order_discounts was declined: it is derived state
// that would go stale on the only occasion it is ever read, since an edit is
// exactly what changes the lines.
func eligibleSubtotalForOrder(ctx context.Context, tx *sql.Tx, orderID, discountID int64, scope string) (int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT product_id, total_minor FROM order_lines
		WHERE order_id = $1 AND product_id IS NOT NULL`, orderID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	lines := []discountLine{}
	for rows.Next() {
		var l discountLine
		if err := rows.Scan(&l.ProductID, &l.Total); err != nil {
			return 0, err
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	ids := make([]int64, 0, len(lines))
	seen := make(map[int64]bool, len(lines))
	for _, l := range lines {
		if seen[l.ProductID] {
			continue
		}
		seen[l.ProductID] = true
		ids = append(ids, l.ProductID)
	}
	matched, err := matchedProducts(ctx, tx, discountID, scope, ids)
	if err != nil {
		return 0, err
	}
	var base int64
	for _, l := range lines {
		if matched[l.ProductID] {
			base += l.Total
		}
	}
	return base, nil
}
