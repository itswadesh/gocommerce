package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// Scoped discounts: the value comes off the part of the basket the rule points
// at, and a rule that points at nothing is refused where it was written rather
// than at the till.

// basketItem is one line of a mixed basket.
type basketItem struct {
	variantID int64
	qty       int
}

// checkoutMixed buys a basket of several variants with a code attached. The
// single-variant checkoutWithCode cannot express the case every test here needs:
// a basket holding both the targeted thing and something else.
func checkoutMixed(t *testing.T, app *App, code string, items []basketItem, email string) (*Order, error) {
	t.Helper()
	ctx := context.Background()
	cart := newCart(t, app)
	for _, it := range items {
		addToCart(t, app, cart.Token, it.variantID, it.qty)
	}
	if code != "" {
		if _, err := app.db.ExecContext(ctx,
			`UPDATE carts SET discount_code = $2 WHERE token = $1`, cart.Token, code); err != nil {
			t.Fatalf("attach code: %v", err)
		}
	}
	in := checkoutInput(cart.Token)
	if email != "" {
		in.Email = email
	}
	result, err := app.Order().Checkout(ctx, CodeCOD, in, "")
	if err != nil {
		return nil, err
	}
	return result.Order, nil
}

// targetsOf reads the stored rows straight, so a test asserting what was written
// cannot be satisfied by the same code that wrote it.
func targetsOf(t *testing.T, app *App, discountID int64) map[string]int64 {
	t.Helper()
	rows, err := app.db.QueryContext(context.Background(),
		`SELECT kind, target_id FROM discount_targets WHERE discount_id = $1 ORDER BY kind, target_id`,
		discountID)
	if err != nil {
		t.Fatalf("read targets: %v", err)
	}
	defer rows.Close()

	out := map[string]int64{}
	for rows.Next() {
		var kind string
		var id int64
		if err := rows.Scan(&kind, &id); err != nil {
			t.Fatalf("scan target: %v", err)
		}
		out[kind+":"+strconv.FormatInt(id, 10)] = id
	}
	return out
}

// The headline behaviour: ten percent off the shoes is ten percent of the shoes,
// not ten percent of the basket.
func TestScopedDiscountAppliesOnlyToItsProducts(t *testing.T) {
	app := newTestApp(t)

	shoes := simpleProduct(t, app, "SCOPE-SHOES", 1000, 10)
	hats := simpleProduct(t, app, "SCOPE-HATS", 500, 10)
	newDiscount(t, app, DiscountInput{
		Code: "SHOES10", Title: "Ten off shoes", Kind: DiscountPercentage, ValueBP: 1000,
		Scope: DiscountScopeProducts, TargetIDs: []int64{shoes.ID},
	})

	order, err := checkoutMixed(t, app, "SHOES10", []basketItem{
		{shoes.DefaultVariant().ID, 2}, {hats.DefaultVariant().ID, 1},
	}, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if order.Subtotal.AmountMinor != 2500 {
		t.Fatalf("subtotal = %d, want 2500", order.Subtotal.AmountMinor)
	}
	// 10% of the two shoe lines' 2000, not of the basket's 2500.
	if order.Discount.AmountMinor != 200 {
		t.Errorf("discount = %d, want 200 — ten percent of the shoes alone",
			order.Discount.AmountMinor)
	}
	want := order.Subtotal.AmountMinor + order.Shipping.AmountMinor - order.Discount.AmountMinor
	if order.Total.AmountMinor != want {
		t.Errorf("total = %d, want %d — the figures must add up", order.Total.AmountMinor, want)
	}
	if len(order.Discounts) != 1 || order.Discounts[0].AmountMinor != 200 {
		t.Errorf("snapshot = %+v, want the smaller amount recorded", order.Discounts)
	}

	// And the order-wide case is untouched by construction: the same basket, an
	// order-scoped rule, ten percent of the whole 2500.
	newDiscount(t, app, DiscountInput{
		Code: "ALL10", Title: "Ten off everything", Kind: DiscountPercentage, ValueBP: 1000,
	})
	whole, err := checkoutMixed(t, app, "ALL10", []basketItem{
		{shoes.DefaultVariant().ID, 2}, {hats.DefaultVariant().ID, 1},
	}, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if whole.Discount.AmountMinor != 250 {
		t.Errorf("order-wide discount = %d, want 250", whole.Discount.AmountMinor)
	}
}

// A fixed amount is clamped to the lines it covers, not to the basket. Without
// that it would take money off lines it was never aimed at.
func TestScopedFixedDiscountIsCappedByItsOwnLines(t *testing.T) {
	app := newTestApp(t)

	small := simpleProduct(t, app, "SCOPE-SMALL", 200, 10)
	big := simpleProduct(t, app, "SCOPE-BIG", 900, 10)
	newDiscount(t, app, DiscountInput{
		Code: "FIVE", Title: "Five hundred off the small one", Kind: DiscountFixed,
		ValueMinor: 500, Scope: DiscountScopeProducts, TargetIDs: []int64{small.ID},
	})

	order, err := checkoutMixed(t, app, "FIVE", []basketItem{
		{small.DefaultVariant().ID, 1}, {big.DefaultVariant().ID, 1},
	}, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if order.Subtotal.AmountMinor != 1100 {
		t.Fatalf("subtotal = %d, want 1100", order.Subtotal.AmountMinor)
	}
	if order.Discount.AmountMinor != 200 {
		t.Errorf("discount = %d, want 200 — the line it came off is all it can take",
			order.Discount.AmountMinor)
	}
}

// Collection membership is read at checkout, not copied when the rule is
// written: adding a product to the collection tomorrow puts it in the promotion.
func TestScopedDiscountByCollectionFollowsMembership(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	inside := simpleProduct(t, app, "SCOPE-IN", 1000, 10)
	outside := simpleProduct(t, app, "SCOPE-OUT", 1000, 10)
	sale, err := app.Collections().Create(ctx, CollectionInput{Title: "Sale"})
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	if err := app.Collections().SetProductCollections(ctx, inside.ID, []int64{sale.ID}); err != nil {
		t.Fatalf("set collections: %v", err)
	}
	newDiscount(t, app, DiscountInput{
		Code: "SALE10", Title: "Ten off the sale", Kind: DiscountPercentage, ValueBP: 1000,
		Scope: DiscountScopeCollections, TargetIDs: []int64{sale.ID},
	})

	order, err := checkoutMixed(t, app, "SALE10", []basketItem{
		{inside.DefaultVariant().ID, 1}, {outside.DefaultVariant().ID, 1},
	}, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if order.Discount.AmountMinor != 100 {
		t.Fatalf("discount = %d, want 100 — the member line alone", order.Discount.AmountMinor)
	}

	// Now the second product joins, and a fresh basket gets both discounted.
	if err := app.Collections().SetProductCollections(ctx, outside.ID, []int64{sale.ID}); err != nil {
		t.Fatalf("set collections: %v", err)
	}
	order2, err := checkoutMixed(t, app, "SALE10", []basketItem{
		{inside.DefaultVariant().ID, 1}, {outside.DefaultVariant().ID, 1},
	}, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if order2.Discount.AmountMinor != 200 {
		t.Errorf("discount = %d, want 200 — membership is read live", order2.Discount.AmountMinor)
	}
}

// A category target reaches everything filed beneath it, the rule tax already
// follows. Exact-node matching would make every branch category a promotion
// covering nothing.
func TestScopedDiscountByCategoryReachesDescendants(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	apparel, err := app.Categories().Create(ctx, CategoryInput{Title: "Apparel"})
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	shirts, err := app.Categories().Create(ctx, CategoryInput{Title: "Shirts", ParentID: &apparel.ID})
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	other, err := app.Categories().Create(ctx, CategoryInput{Title: "Homeware"})
	if err != nil {
		t.Fatalf("create category: %v", err)
	}

	shirt := simpleProduct(t, app, "SCOPE-SHIRT", 1000, 10)
	mug := simpleProduct(t, app, "SCOPE-MUG", 1000, 10)
	filed := func(p *Product, categoryID int64) {
		t.Helper()
		if _, err := app.Products().UpdateProduct(ctx, p.ID, ProductPatch{
			CategoryID: NullableID{Present: true, Value: &categoryID},
		}); err != nil {
			t.Fatalf("file %s: %v", p.Title, err)
		}
	}
	filed(shirt, shirts.ID)
	filed(mug, other.ID)

	newDiscount(t, app, DiscountInput{
		Code: "APPAREL10", Title: "Ten off apparel", Kind: DiscountPercentage, ValueBP: 1000,
		Scope: DiscountScopeCategories, TargetIDs: []int64{apparel.ID},
	})

	order, err := checkoutMixed(t, app, "APPAREL10", []basketItem{
		{shirt.DefaultVariant().ID, 1}, {mug.DefaultVariant().ID, 1},
	}, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if order.Discount.AmountMinor != 100 {
		t.Errorf("discount = %d, want 100 — Apparel reaches Apparel / Shirts and stops there",
			order.Discount.AmountMinor)
	}
}

// A basket holding none of what the rule points at is refused, and the refusal
// happens before the usage claim, so a rule that matched nothing burns no use.
func TestScopedDiscountRefusesABasketItDoesNotReach(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	targeted := simpleProduct(t, app, "SCOPE-YES", 1000, 10)
	other := simpleProduct(t, app, "SCOPE-NO", 1000, 10)
	d := newDiscount(t, app, DiscountInput{
		Code: "ONLYTHAT", Title: "Only that one", Kind: DiscountPercentage, ValueBP: 1000,
		Scope: DiscountScopeProducts, TargetIDs: []int64{targeted.ID},
	})

	_, err := checkoutMixed(t, app, "ONLYTHAT", []basketItem{{other.DefaultVariant().ID, 1}}, "")
	if err == nil {
		t.Fatal("a basket with none of the targeted products was given the discount")
	}
	if !strings.Contains(err.Error(), "does not apply to anything in this basket") {
		t.Errorf("error = %q, want it to blame the basket, not the rule", err)
	}

	var orders int
	if err := app.db.QueryRowContext(ctx, `SELECT count(*) FROM orders`).Scan(&orders); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if orders != 0 {
		t.Errorf("%d order(s) written for a refused checkout", orders)
	}
	after, err := app.Discounts().Get(ctx, d.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.UsedCount != 0 {
		t.Errorf("used_count = %d, want 0 — a rule matching nothing must not burn a use", after.UsedCount)
	}
}

// The scope guard on the zero-base refusal, as a regression test: an order-wide
// code against a basket worth nothing still applies, at zero.
func TestOrderWideDiscountStillAppliesToAFreeBasket(t *testing.T) {
	app := newTestApp(t)

	free := simpleProduct(t, app, "SCOPE-FREE", 0, 10)
	newDiscount(t, app, DiscountInput{
		Code: "TENALL", Title: "Ten off", Kind: DiscountPercentage, ValueBP: 1000,
	})

	order, err := checkoutMixed(t, app, "TENALL", []basketItem{{free.DefaultVariant().ID, 1}}, "")
	if err != nil {
		t.Fatalf("an order-wide code was refused a free basket: %v", err)
	}
	if order.Discount.AmountMinor != 0 {
		t.Errorf("discount = %d, want 0", order.Discount.AmountMinor)
	}
}

// Every door into the broken row, closed with its own message.
func TestScopedDiscountNeedsTargetsAtEveryDoor(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	product := simpleProduct(t, app, "DOORS", 1000, 5)
	collection, err := app.Collections().Create(ctx, CollectionInput{Title: "Doors"})
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}

	creates := []struct {
		name string
		in   DiscountInput
	}{
		{"scoped with no targets", DiscountInput{
			Title: "x", Kind: DiscountFixed, ValueMinor: 100, Scope: DiscountScopeProducts}},
		{"order scope with targets", DiscountInput{
			Title: "x", Kind: DiscountFixed, ValueMinor: 100,
			TargetIDs: []int64{product.ID}}},
		{"free shipping with a scope", DiscountInput{
			Title: "x", Kind: DiscountFreeShipping, Scope: DiscountScopeProducts,
			TargetIDs: []int64{product.ID}}},
	}
	for _, c := range creates {
		if _, err := app.Discounts().Create(ctx, c.in); err == nil {
			t.Errorf("%s was accepted", c.name)
		}
	}

	live := newDiscount(t, app, DiscountInput{
		Title: "Live", Kind: DiscountPercentage, ValueBP: 1000,
		Scope: DiscountScopeProducts, TargetIDs: []int64{product.ID},
	})
	order := DiscountScopeOrder
	products := DiscountScopeProducts
	collections := DiscountScopeCollections
	empty := []int64{}

	plain := newDiscount(t, app, DiscountInput{Title: "Plain", Kind: DiscountPercentage, ValueBP: 500})
	if _, err := app.Discounts().Update(ctx, plain.ID, DiscountPatch{Scope: &products}); err == nil {
		t.Error("a rule was moved to product scope with no targets")
	}
	if _, err := app.Discounts().Update(ctx, live.ID, DiscountPatch{TargetIDs: &empty}); err == nil {
		t.Error("a scoped rule was emptied of its targets")
	}
	if _, err := app.Discounts().Update(ctx, live.ID, DiscountPatch{Scope: &collections}); err == nil {
		t.Error("a rule changed scope while keeping targets of the old kind")
	}

	// And the ways through are open: moving back to the whole basket clears the
	// targets without needing them sent, and re-aiming with a list works.
	back, err := app.Discounts().Update(ctx, live.ID, DiscountPatch{Scope: &order})
	if err != nil {
		t.Fatalf("moving to order scope: %v", err)
	}
	if len(back.TargetIDs) != 0 {
		t.Errorf("target_ids = %v, want them cleared with the scope", back.TargetIDs)
	}
	if len(targetsOf(t, app, live.ID)) != 0 {
		t.Error("the target rows survived a move to order scope")
	}
	ids := []int64{collection.ID}
	again, err := app.Discounts().Update(ctx, live.ID, DiscountPatch{
		Scope: &collections, TargetIDs: &ids,
	})
	if err != nil {
		t.Fatalf("re-aiming: %v", err)
	}
	if len(again.Targets) != 1 || again.Targets[0].Kind != DiscountTargetCollection {
		t.Errorf("targets = %+v, want one collection", again.Targets)
	}
}

// One unknown id rejects the whole request, and nothing at all is stored —
// because the rule and its targets share a transaction.
func TestUnknownTargetRejectsTheWholeRule(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	real := simpleProduct(t, app, "REAL", 1000, 5)

	_, err := app.Discounts().Create(ctx, DiscountInput{
		Code: "PARTIAL", Title: "Half real", Kind: DiscountPercentage, ValueBP: 1000,
		Scope: DiscountScopeProducts, TargetIDs: []int64{real.ID, 999999},
	})
	if err == nil {
		t.Fatal("a rule was stored naming a product that does not exist")
	}
	if !strings.Contains(err.Error(), "product 999999 does not exist") {
		t.Errorf("error = %q, want it to name the id", err)
	}

	var discounts, targets int
	if err := app.db.QueryRowContext(ctx, `SELECT count(*) FROM discounts`).Scan(&discounts); err != nil {
		t.Fatalf("count discounts: %v", err)
	}
	if err := app.db.QueryRowContext(ctx, `SELECT count(*) FROM discount_targets`).Scan(&targets); err != nil {
		t.Fatalf("count targets: %v", err)
	}
	if discounts != 0 || targets != 0 {
		t.Errorf("%d discount(s) and %d target(s) survived a rejected create", discounts, targets)
	}
}

// The scope and what it points at move together, so a rule can never hold
// targets of the wrong kind.
func TestScopeAndTargetsCommitTogether(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	apparel, err := app.Categories().Create(ctx, CategoryInput{Title: "Apparel"})
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	product := simpleProduct(t, app, "TOGETHER", 1000, 5)
	d := newDiscount(t, app, DiscountInput{
		Title: "Together", Kind: DiscountPercentage, ValueBP: 1000,
		Scope: DiscountScopeProducts, TargetIDs: []int64{product.ID},
	})

	categories := DiscountScopeCategories
	ids := []int64{apparel.ID}
	moved, err := app.Discounts().Update(ctx, d.ID, DiscountPatch{Scope: &categories, TargetIDs: &ids})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if moved.Scope != DiscountScopeCategories || len(moved.TargetIDs) != 1 || moved.TargetIDs[0] != apparel.ID {
		t.Fatalf("after = %+v, want the category scope and its one target", moved)
	}
	rows := targetsOf(t, app, d.ID)
	if len(rows) != 1 {
		t.Fatalf("stored targets = %v, want only the new category row", rows)
	}
	if _, ok := rows["category:"+strconv.FormatInt(apparel.ID, 10)]; !ok {
		t.Errorf("stored targets = %v, want the category row", rows)
	}

	order := DiscountScopeOrder
	cleared, err := app.Discounts().Update(ctx, d.ID, DiscountPatch{Scope: &order})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(cleared.Targets) != 0 || len(cleared.TargetIDs) != 0 {
		t.Errorf("after = %+v, want no targets in the response the drawer redraws from", cleared)
	}
	if len(targetsOf(t, app, d.ID)) != 0 {
		t.Error("target rows survived the move to order scope")
	}
}

// Re-aiming a rule and nothing else is a real edit.
func TestTargetsOnlyPatchIsARealEdit(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	one := simpleProduct(t, app, "AIM-1", 1000, 5)
	two := simpleProduct(t, app, "AIM-2", 1000, 5)
	d := newDiscount(t, app, DiscountInput{
		Title: "Aim", Kind: DiscountPercentage, ValueBP: 1000,
		Scope: DiscountScopeProducts, TargetIDs: []int64{one.ID},
	})

	ids := []int64{two.ID}
	after, err := app.Discounts().Update(ctx, d.ID, DiscountPatch{TargetIDs: &ids})
	if err != nil {
		t.Fatalf("targets-only patch: %v", err)
	}
	if len(after.TargetIDs) != 1 || after.TargetIDs[0] != two.ID {
		t.Errorf("target_ids = %v, want just the new one", after.TargetIDs)
	}
	if !after.UpdatedAt.After(d.UpdatedAt) {
		t.Error("updated_at did not move; a re-aimed promotion must read as changed")
	}
}

// The Go consts and the schema CHECK can drift apart. When they do, the
// evaluator refuses rather than taking the discount off everything.
func TestUnsupportedScopeIsRefusedNotApplied(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	// The CHECK forbids storing this, so a hand-built rule is the only way to
	// reach the branch that protects whoever adds the next scope.
	d := &Discount{ID: 1, Scope: "everything", Kind: DiscountPercentage, ValueBP: 1000}
	_, _, err := app.Discounts().eligibleFor(ctx, app.db, d, []discountLine{{ProductID: 7, Total: 100}})
	if err == nil {
		t.Fatal("an unknown scope was evaluated")
	}
	if !strings.Contains(err.Error(), "scope this store does not understand") {
		t.Errorf("error = %q, want it to name the unknown scope", err)
	}
}

// Free shipping has no line to come off, so it cannot be scoped — refused when
// written, and refused by name for a row that predates the rule.
func TestFreeShippingCannotBeScoped(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	product := simpleProduct(t, app, "SHIPFREE", 1000, 5)

	_, err := app.Discounts().Create(ctx, DiscountInput{
		Code: "FREESHIP", Title: "Free delivery on sofas", Kind: DiscountFreeShipping,
		Scope: DiscountScopeProducts, TargetIDs: []int64{product.ID},
	})
	if err == nil {
		t.Fatal("free shipping was given a scope")
	}
	if !strings.Contains(err.Error(), "minimum basket") {
		t.Errorf("error = %q, want it to point at the minimum instead", err)
	}

	// A row from before the rule: inert today, and it must stay inert rather
	// than quietly becoming an order-wide giveaway.
	var id int64
	if err := app.db.QueryRowContext(ctx, `
		INSERT INTO discounts (code, title, kind, scope)
		VALUES ('OLDSHIP', 'Old free shipping', 'free_shipping', 'products')
		RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := app.db.ExecContext(ctx, `
		INSERT INTO discount_targets (discount_id, kind, target_id)
		VALUES ($1, 'product', $2)`, id, product.ID); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	_, err = checkoutMixed(t, app, "OLDSHIP", []basketItem{{product.DefaultVariant().ID, 1}}, "")
	if err == nil {
		t.Fatal("a scoped free-shipping rule was applied")
	}
	if !strings.Contains(err.Error(), "free shipping cannot be scoped") {
		t.Errorf("error = %q, want it to name the rule's problem", err)
	}
}

// D39 amending D27: a scoped discount follows the lines it covers through an
// edit, for a fixed amount as well as a percentage.
func TestEditedScopedOrderFollowsD39(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	// Percentage: 2 targeted @1000 plus an untargeted 500. Ten percent of 2000
	// is 200; dropping the targeted line to one must give 100, not a tenth of
	// the new 1500 subtotal.
	shoes := simpleProduct(t, app, "D39-SHOES", 1000, 10)
	hats := simpleProduct(t, app, "D39-HATS", 500, 10)
	newDiscount(t, app, DiscountInput{
		Code: "D39PCT", Title: "Percent on shoes", Kind: DiscountPercentage, ValueBP: 1000,
		Scope: DiscountScopeProducts, TargetIDs: []int64{shoes.ID},
	})
	order, err := checkoutMixed(t, app, "D39PCT", []basketItem{
		{shoes.DefaultVariant().ID, 2}, {hats.DefaultVariant().ID, 1},
	}, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if order.Discount.AmountMinor != 200 {
		t.Fatalf("discount = %d, want 200", order.Discount.AmountMinor)
	}
	// OrderEdit.Lines is the whole desired order, so the untargeted line is
	// listed too — and it is what makes this assertion discriminating: the new
	// subtotal is 1500, so a whole-basket recompute would say 150.
	shoeLine := lineFor(t, order, shoes.ID)
	hatLine := lineFor(t, order, hats.ID)
	edited, _, err := app.Order().EditLines(ctx, order.ID, OrderEdit{
		Lines: []OrderLineEdit{
			{ID: shoeLine.ID, Quantity: 1},
			{ID: hatLine.ID, Quantity: 1},
		},
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if edited.Subtotal.AmountMinor != 1500 {
		t.Fatalf("subtotal = %d, want 1500", edited.Subtotal.AmountMinor)
	}
	if edited.Discount.AmountMinor != 100 {
		t.Errorf("discount = %d, want 100 — a percentage follows the lines it covers, not the order",
			edited.Discount.AmountMinor)
	}

	// Fixed: 500 off a targeted line worth 2000 in a bigger basket. Shrink the
	// targeted line to 300 and the amount has to come down with it — this is the
	// case that fails if scope is read inside the percentage-only branch.
	tools := simpleProduct(t, app, "D39-TOOLS", 300, 10)
	bags := simpleProduct(t, app, "D39-BAGS", 900, 10)
	newDiscount(t, app, DiscountInput{
		Code: "D39FIX", Title: "Fixed on tools", Kind: DiscountFixed, ValueMinor: 500,
		Scope: DiscountScopeProducts, TargetIDs: []int64{tools.ID},
	})
	order2, err := checkoutMixed(t, app, "D39FIX", []basketItem{
		{tools.DefaultVariant().ID, 3}, {bags.DefaultVariant().ID, 1},
	}, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if order2.Discount.AmountMinor != 500 {
		t.Fatalf("discount = %d, want the whole 500", order2.Discount.AmountMinor)
	}
	toolLine := lineFor(t, order2, tools.ID)
	bagLine := lineFor(t, order2, bags.ID)
	edited2, _, err := app.Order().EditLines(ctx, order2.ID, OrderEdit{
		Lines: []OrderLineEdit{
			{ID: toolLine.ID, Quantity: 1},
			{ID: bagLine.ID, Quantity: 1},
		},
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if edited2.Subtotal.AmountMinor != 1200 {
		t.Fatalf("subtotal = %d, want 1200", edited2.Subtotal.AmountMinor)
	}
	// D27 alone would leave this at 500, which the 1200 subtotal does not clamp.
	if edited2.Discount.AmountMinor != 300 {
		t.Errorf("discount = %d, want 300 — a scoped fixed amount is clamped to what is left of its lines",
			edited2.Discount.AmountMinor)
	}

	// Removing the last targeted line refuses the edit rather than silently
	// zeroing the discount: that is an operator's call.
	_, _, err = app.Order().EditLines(ctx, order2.ID, OrderEdit{
		Lines: []OrderLineEdit{{ID: lineFor(t, edited2, bags.ID).ID, Quantity: 1}},
	})
	if err == nil {
		t.Fatal("an order was edited until its discount covered nothing")
	}
	if !strings.Contains(err.Error(), "remove the discount first") {
		t.Errorf("error = %q, want it to say what to do", err)
	}
}

// lineFor finds an order's line for a product, since a mixed basket is sorted by
// variant id and the position is not the test's to guess.
func lineFor(t *testing.T, o *Order, productID int64) OrderLine {
	t.Helper()
	for _, l := range o.Lines {
		if l.ProductID != nil && *l.ProductID == productID {
			return l
		}
	}
	t.Fatalf("order %d has no line for product %d", o.ID, productID)
	return OrderLine{}
}

// The promise the preview makes: the figure a shopper is shown is the figure
// they are charged, which for a scoped rule depends on both sides deriving the
// same lines.
func TestScopedPreviewMatchesWhatCheckoutCharges(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	shoes := simpleProduct(t, app, "PREVIEW-SHOES", 999, 10)
	hats := simpleProduct(t, app, "PREVIEW-HATS", 555, 10)
	newDiscount(t, app, DiscountInput{
		Code: "PREV10", Title: "Ten off shoes", Kind: DiscountPercentage, ValueBP: 1000,
		Scope: DiscountScopeProducts, TargetIDs: []int64{shoes.ID},
	})

	cart := newCart(t, app)
	addToCart(t, app, cart.Token, shoes.DefaultVariant().ID, 3)
	addToCart(t, app, cart.Token, hats.DefaultVariant().ID, 1)

	rec := doBody(t, app, "PUT", "/api/carts/"+cart.Token+"/discount",
		`{"code":"PREV10","email":"shopper@example.com"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview = %d: %s", rec.Code, rec.Body.String())
	}
	var previewed struct {
		Data AppliedDiscount `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &previewed); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	// 999 * 3 = 2997; ten percent, rounded once, is 300. Not a tenth of 3552.
	if previewed.Data.AmountMinor != 300 {
		t.Errorf("previewed = %d, want 300 — the shoes alone", previewed.Data.AmountMinor)
	}

	result, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if result.Order.Discount.AmountMinor != previewed.Data.AmountMinor {
		t.Errorf("charged %d after previewing %d — the two paths disagree",
			result.Order.Discount.AmountMinor, previewed.Data.AmountMinor)
	}
}

// The wire contract the panel is written against. The listing carries ids and no
// titles, and dropping them later would silently reintroduce the drawer's
// data-loss race.
func TestDiscountTargetsRoundTripOverHTTP(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	apparel, err := app.Categories().Create(ctx, CategoryInput{Title: "Apparel"})
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	shirts, err := app.Categories().Create(ctx, CategoryInput{Title: "Shirts", ParentID: &apparel.ID})
	if err != nil {
		t.Fatalf("create category: %v", err)
	}

	body := `{"code":"WIRE","title":"Wire","kind":"percentage","value_bp":1000,` +
		`"scope":"categories","target_ids":[` + strconv.FormatInt(shirts.ID, 10) + `]}`
	rec := doBody(t, app, "POST", "/api/admin/discounts", body, withAdmin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Data Discount `json:"data"`
	}
	decodeCreated(t, rec, &created)
	if len(created.Data.TargetIDs) != 1 || created.Data.TargetIDs[0] != shirts.ID {
		t.Fatalf("created target_ids = %v, want the one category", created.Data.TargetIDs)
	}

	id := strconv.FormatInt(created.Data.ID, 10)
	var single struct {
		Data Discount `json:"data"`
	}
	decodeInto(t, do(t, app, "GET", "/api/admin/discounts/"+id, withAdmin), &single)
	if len(single.Data.Targets) != 1 {
		t.Fatalf("targets = %+v, want one", single.Data.Targets)
	}
	got := single.Data.Targets[0]
	if got.Kind != DiscountTargetCategory {
		t.Errorf("kind = %q, want the singular %q", got.Kind, DiscountTargetCategory)
	}
	if got.Title != "Apparel / Shirts" {
		t.Errorf("title = %q, want the full ancestry", got.Title)
	}
	if got.Missing {
		t.Error("a live category came back missing")
	}

	var listed struct {
		Data []Discount `json:"data"`
	}
	decodeInto(t, do(t, app, "GET", "/api/admin/discounts", withAdmin), &listed)
	if len(listed.Data) != 1 {
		t.Fatalf("listing has %d rows, want 1", len(listed.Data))
	}
	if len(listed.Data[0].TargetIDs) != 1 {
		t.Errorf("listing target_ids = %v, want the drawer's ids in hand", listed.Data[0].TargetIDs)
	}
	if len(listed.Data[0].Targets) != 0 {
		t.Errorf("listing carries resolved targets = %+v; a page of fifty rules must not",
			listed.Data[0].Targets)
	}

	// A patch of something else leaves the targets alone.
	rec = doBody(t, app, "PATCH", "/api/admin/discounts/"+id, `{"title":"Renamed"}`, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rec.Code, rec.Body.String())
	}
	if len(targetsOf(t, app, created.Data.ID)) != 1 {
		t.Error("a title patch moved the targets")
	}
}

func decodeCreated(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

// A deleted target is kept and reported, not cascaded away: an operator has to
// be told a promotion has narrowed.
func TestDeletedTargetIsReportedNotHidden(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	gone := simpleProduct(t, app, "GONE", 1000, 5)
	d := newDiscount(t, app, DiscountInput{
		Code: "NARROW", Title: "Narrowing", Kind: DiscountPercentage, ValueBP: 1000,
		Scope: DiscountScopeProducts, TargetIDs: []int64{gone.ID},
	})
	if err := app.Products().DeleteProduct(ctx, gone.ID); err != nil {
		t.Fatalf("delete product: %v", err)
	}

	after, err := app.Discounts().Get(ctx, d.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(after.Targets) != 1 {
		t.Fatalf("targets = %+v, want the row kept", after.Targets)
	}
	if !after.Targets[0].Missing {
		t.Error("a target naming a deleted product is not reported missing")
	}
	if after.Targets[0].Title != "" {
		t.Errorf("title = %q, want it empty so the panel draws the chip red", after.Targets[0].Title)
	}

	other := simpleProduct(t, app, "STILL-HERE", 1000, 5)
	if _, err := checkoutMixed(t, app, "NARROW",
		[]basketItem{{other.DefaultVariant().ID, 1}}, ""); err == nil {
		t.Error("a rule whose only target is gone still discounted a basket")
	}
}

// What a returned line is worth has to know which lines the discount came off,
// for the same reason the tax does. The untargeted line was charged in full, so
// it comes back in full; the targeted one comes back less its own discount.
func TestScopedDiscountSharesOnlyItsOwnLinesOnAReturn(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	shoes := simpleProduct(t, app, "RET-SHOES", 1000, 10)
	hats := simpleProduct(t, app, "RET-HATS", 1000, 10)
	newDiscount(t, app, DiscountInput{
		Code: "RETSHOES", Title: "Ten off shoes", Kind: DiscountPercentage, ValueBP: 1000,
		Scope: DiscountScopeProducts, TargetIDs: []int64{shoes.ID},
	})

	order, err := checkoutMixed(t, app, "RETSHOES", []basketItem{
		{shoes.DefaultVariant().ID, 1}, {hats.DefaultVariant().ID, 1},
	}, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if order.Discount.AmountMinor != 100 {
		t.Fatalf("discount = %d, want 100", order.Discount.AmountMinor)
	}
	if _, err := app.Ship().Create(ctx, order.ID, "", ShipRequest{Tracking: "TRK-SCOPE"}); err != nil {
		t.Fatalf("ship: %v", err)
	}
	if _, err := app.Order().MarkDelivered(ctx, order.ID); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	hatLine := lineFor(t, order, hats.ID)
	shoeLine := lineFor(t, order, shoes.ID)
	back, _, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Reason: "changed mind",
		Lines: []ReturnLineInput{
			{LineID: hatLine.ID, Quantity: 1},
			{LineID: shoeLine.ID, Quantity: 1},
		},
	})
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if len(back.Returns) != 1 {
		t.Fatalf("returns = %+v, want one", back.Returns)
	}

	byLine := map[int64]int64{}
	for _, l := range back.Returns[0].Lines {
		byLine[l.OrderLineID] = l.Refundable.AmountMinor
	}
	// The hat was charged 1000 and taxed nothing, so all of it comes back.
	if byLine[hatLine.ID] != 1000 {
		t.Errorf("untargeted line refundable = %d, want 1000 — it was charged in full",
			byLine[hatLine.ID])
	}
	// The shoe was charged 1000 less the 100 that came off it.
	if byLine[shoeLine.ID] != 900 {
		t.Errorf("targeted line refundable = %d, want 900 — less its own discount",
			byLine[shoeLine.ID])
	}
}
