package gocommerce

import (
	"context"
	"testing"
	"time"
)

// What a variant costs, when the answer depends on who is buying and how many.
//
// Until now there was exactly one answer — variants.price_minor — and the whole
// money path leaned on that: AddLine snapshots it, checkout re-prices to it
// under the lock, and the difference between the two is what makes a shopper
// re-confirm. Price lists change what "the current price" means and nothing
// else, which is why the resolution rules below are worth pinning precisely.
//
// The rules, in the order they break ties:
//
//  1. the most specific quantity break wins — 50-up beats 10-up beats 1-up;
//  2. then the higher-priority list;
//  3. then the cheaper price.
//
// Cheapest-last rather than dearest-last is deliberate: two lists that both
// cover a line is an operator mistake either way, and charging the lower of the
// two prices is the one that cannot turn into a complaint.
func TestPriceResolution(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	price := int64(10000) // 100.00 base

	p, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Trade widget", SKU: "PL-1", PriceMinor: &price, Status: "active",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	variant := p.Variants[0].ID

	trade, err := app.Pricing().CreateGroup(ctx, CustomerGroupInput{Code: "trade", Name: "Trade"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := app.Pricing().AddMember(ctx, trade.ID, "Buyer@Example.com"); err != nil {
		t.Fatalf("add member: %v", err)
	}

	// A trade list with two quantity breaks.
	list, err := app.Pricing().CreateList(ctx, PriceListInput{
		Name: "Trade 2026", GroupID: &trade.ID,
	})
	if err != nil {
		t.Fatalf("create list: %v", err)
	}
	for _, row := range []PriceRow{
		{VariantID: variant, MinQuantity: 1, AmountMinor: 9000},
		{VariantID: variant, MinQuantity: 10, AmountMinor: 8000},
	} {
		if err := app.Pricing().SetPrice(ctx, list.ID, row); err != nil {
			t.Fatalf("set price: %v", err)
		}
	}

	at := func(email string, qty int) int64 {
		t.Helper()
		got, err := app.Pricing().PriceFor(ctx, variant, qty, email)
		if err != nil {
			t.Fatalf("price for %q x%d: %v", email, qty, err)
		}
		return got
	}

	// Nobody in particular pays the catalogue price, whatever the quantity:
	// a price list that leaked to anonymous carts would be a trade price on
	// the public storefront.
	if got := at("", 1); got != 10000 {
		t.Errorf("anonymous x1 = %d, want the base 10000", got)
	}
	if got := at("", 50); got != 10000 {
		t.Errorf("anonymous x50 = %d, want the base 10000", got)
	}
	if got := at("someone@else.com", 50); got != 10000 {
		t.Errorf("a non-member x50 = %d, want the base 10000", got)
	}

	// A member gets the list, and the break applies at the threshold.
	if got := at("buyer@example.com", 1); got != 9000 {
		t.Errorf("member x1 = %d, want 9000", got)
	}
	if got := at("buyer@example.com", 9); got != 9000 {
		t.Errorf("member x9 = %d, want 9000 — the 10-up break must not apply yet", got)
	}
	if got := at("buyer@example.com", 10); got != 8000 {
		t.Errorf("member x10 = %d, want 8000 at the threshold", got)
	}
	if got := at("buyer@example.com", 99); got != 8000 {
		t.Errorf("member x99 = %d, want 8000", got)
	}

	// Membership is matched case-insensitively: the address was added with
	// capitals and carts carry whatever the shopper typed.
	if got := at("BUYER@EXAMPLE.COM", 1); got != 9000 {
		t.Errorf("member in capitals = %d, want 9000", got)
	}
}

// A list with no group is everybody's — a launch price, a seasonal one — and it
// must reach anonymous carts, which is the case the group check could easily
// exclude by accident.
func TestUngroupedListAppliesToEveryone(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	price := int64(5000)

	p, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Seasonal", SKU: "PL-2", PriceMinor: &price, Status: "active",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	variant := p.Variants[0].ID

	list, err := app.Pricing().CreateList(ctx, PriceListInput{Name: "Launch"})
	if err != nil {
		t.Fatalf("create list: %v", err)
	}
	if err := app.Pricing().SetPrice(ctx, list.ID,
		PriceRow{VariantID: variant, MinQuantity: 1, AmountMinor: 4000}); err != nil {
		t.Fatalf("set price: %v", err)
	}

	for _, email := range []string{"", "anyone@example.com"} {
		got, err := app.Pricing().PriceFor(ctx, variant, 1, email)
		if err != nil {
			t.Fatalf("price for %q: %v", email, err)
		}
		if got != 4000 {
			t.Errorf("price for %q = %d, want 4000", email, got)
		}
	}
}

// A window that has not opened, or has closed, is not a price.
func TestPriceListWindow(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	price := int64(5000)

	p, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Windowed", SKU: "PL-3", PriceMinor: &price, Status: "active",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	variant := p.Variants[0].ID

	future := time.Now().Add(48 * time.Hour)
	past := time.Now().Add(-48 * time.Hour)

	notYet, err := app.Pricing().CreateList(ctx, PriceListInput{Name: "Next week", StartsAt: &future})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := app.Pricing().SetPrice(ctx, notYet.ID,
		PriceRow{VariantID: variant, MinQuantity: 1, AmountMinor: 100}); err != nil {
		t.Fatalf("set price: %v", err)
	}
	if got, _ := app.Pricing().PriceFor(ctx, variant, 1, ""); got != 5000 {
		t.Errorf("a list that has not opened priced at %d, want the base 5000", got)
	}

	over, err := app.Pricing().CreateList(ctx, PriceListInput{Name: "Last week", EndsAt: &past})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := app.Pricing().SetPrice(ctx, over.ID,
		PriceRow{VariantID: variant, MinQuantity: 1, AmountMinor: 200}); err != nil {
		t.Fatalf("set price: %v", err)
	}
	if got, _ := app.Pricing().PriceFor(ctx, variant, 1, ""); got != 5000 {
		t.Errorf("a closed list priced at %d, want the base 5000", got)
	}

	// And an inactive list is ignored whatever its window says.
	off := false
	live, err := app.Pricing().CreateList(ctx, PriceListInput{Name: "Paused"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := app.Pricing().SetPrice(ctx, live.ID,
		PriceRow{VariantID: variant, MinQuantity: 1, AmountMinor: 300}); err != nil {
		t.Fatalf("set price: %v", err)
	}
	if _, err := app.Pricing().UpdateList(ctx, live.ID, PriceListPatch{Active: &off}); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if got, _ := app.Pricing().PriceFor(ctx, variant, 1, ""); got != 5000 {
		t.Errorf("a paused list priced at %d, want the base 5000", got)
	}
}

// Two lists covering one line is an operator mistake either way; the cheaper
// one is the one that cannot turn into a complaint.
func TestOverlappingListsTakeThePriorityThenTheCheaper(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	price := int64(5000)

	p, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Contested", SKU: "PL-4", PriceMinor: &price, Status: "active",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	variant := p.Variants[0].ID

	low, err := app.Pricing().CreateList(ctx, PriceListInput{Name: "Everyday", Priority: 1})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	high, err := app.Pricing().CreateList(ctx, PriceListInput{Name: "Clearance", Priority: 5})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for id, amount := range map[int64]int64{low.ID: 4500, high.ID: 4000} {
		if err := app.Pricing().SetPrice(ctx, id,
			PriceRow{VariantID: variant, MinQuantity: 1, AmountMinor: amount}); err != nil {
			t.Fatalf("set price: %v", err)
		}
	}
	if got, _ := app.Pricing().PriceFor(ctx, variant, 1, ""); got != 4000 {
		t.Errorf("price = %d, want the higher-priority list's 4000", got)
	}

	// A quantity break beats priority: it is the more specific statement about
	// this particular line.
	if err := app.Pricing().SetPrice(ctx, low.ID,
		PriceRow{VariantID: variant, MinQuantity: 10, AmountMinor: 4300}); err != nil {
		t.Fatalf("set price: %v", err)
	}
	if got, _ := app.Pricing().PriceFor(ctx, variant, 10, ""); got != 4300 {
		t.Errorf("x10 = %d, want the 10-up break at 4300 even from the lower-priority list", got)
	}
}

// Groups are the other half, and they are keyed on the email because this
// engine has no customers table — a customer is an address that has ordered.
func TestCustomerGroupMembership(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	g, err := app.Pricing().CreateGroup(ctx, CustomerGroupInput{Code: "wholesale", Name: "Wholesale"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := app.Pricing().CreateGroup(ctx,
		CustomerGroupInput{Code: "wholesale", Name: "Again"}); err == nil {
		t.Error("a duplicate code was accepted; the code is the stable handle")
	}

	if err := app.Pricing().AddMember(ctx, g.ID, "A@Example.com"); err != nil {
		t.Fatalf("add: %v", err)
	}
	// Adding the same address twice is not an error: the caller asked for it to
	// be a member and it is.
	if err := app.Pricing().AddMember(ctx, g.ID, "a@example.com"); err != nil {
		t.Fatalf("re-add: %v", err)
	}

	members, total, err := app.Pricing().Members(ctx, g.ID, 50, 0)
	if err != nil {
		t.Fatalf("members: %v", err)
	}
	if total != 1 || len(members) != 1 {
		t.Fatalf("members = %d (total %d), want exactly 1 — the address differed only by case", len(members), total)
	}
	if members[0] != "a@example.com" {
		t.Errorf("stored as %q, want it folded to lower case", members[0])
	}

	if err := app.Pricing().RemoveMember(ctx, g.ID, "A@EXAMPLE.COM"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, total, _ := app.Pricing().Members(ctx, g.ID, 50, 0); total != 0 {
		t.Errorf("after removing, total = %d, want 0", total)
	}
}

// Deleting a group takes its lists' audience with it rather than leaving a list
// priced for nobody, and deleting a list takes its prices.
func TestDeleteCascades(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	price := int64(5000)

	p, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Cascade", SKU: "PL-5", PriceMinor: &price, Status: "active",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	variant := p.Variants[0].ID

	g, err := app.Pricing().CreateGroup(ctx, CustomerGroupInput{Code: "gone", Name: "Gone"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := app.Pricing().AddMember(ctx, g.ID, "x@example.com"); err != nil {
		t.Fatalf("add member: %v", err)
	}
	list, err := app.Pricing().CreateList(ctx, PriceListInput{Name: "Theirs", GroupID: &g.ID})
	if err != nil {
		t.Fatalf("create list: %v", err)
	}
	if err := app.Pricing().SetPrice(ctx, list.ID,
		PriceRow{VariantID: variant, MinQuantity: 1, AmountMinor: 1000}); err != nil {
		t.Fatalf("set price: %v", err)
	}
	if got, _ := app.Pricing().PriceFor(ctx, variant, 1, "x@example.com"); got != 1000 {
		t.Fatalf("price = %d, want 1000 before the delete", got)
	}

	if err := app.Pricing().DeleteGroup(ctx, g.ID); err != nil {
		t.Fatalf("delete group: %v", err)
	}
	if got, _ := app.Pricing().PriceFor(ctx, variant, 1, "x@example.com"); got != 5000 {
		t.Errorf("price = %d after the group went, want the base 5000", got)
	}
}

// The money path itself: a cart and a checkout must charge the resolved price,
// not the catalogue one.
//
// This is the test that matters. PriceFor above proves the rules; this proves
// they reach the three places that decide what somebody actually pays — the
// snapshot AddLine takes, the re-price checkout does under the lock, and the
// figure the order line keeps.
func TestCartAndCheckoutUsePriceLists(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "PL-CART", 10000, 100)
	variant := product.DefaultVariant().ID

	trade, err := app.Pricing().CreateGroup(ctx, CustomerGroupInput{Code: "trade2", Name: "Trade"})
	if err != nil {
		t.Fatalf("group: %v", err)
	}
	if err := app.Pricing().AddMember(ctx, trade.ID, "shopper@example.com"); err != nil {
		t.Fatalf("member: %v", err)
	}
	list, err := app.Pricing().CreateList(ctx, PriceListInput{Name: "Trade", GroupID: &trade.ID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, row := range []PriceRow{
		{VariantID: variant, MinQuantity: 1, AmountMinor: 9000},
		{VariantID: variant, MinQuantity: 10, AmountMinor: 8000},
	} {
		if err := app.Pricing().SetPrice(ctx, list.ID, row); err != nil {
			t.Fatalf("price: %v", err)
		}
	}

	// An anonymous cart pays the catalogue price: a cart with no email has no
	// claim on a trade list, and this is the leak that would matter most.
	anon, err := app.Cart().Create(ctx, "")
	if err != nil {
		t.Fatalf("anon cart: %v", err)
	}
	if _, err := app.Cart().AddLine(ctx, anon.Token, variant, 1); err != nil {
		t.Fatalf("add: %v", err)
	}
	got, err := app.Cart().GetByToken(ctx, anon.Token)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Lines[0].UnitPrice.AmountMinor != 10000 {
		t.Errorf("anonymous line = %d, want the catalogue 10000",
			got.Lines[0].UnitPrice.AmountMinor)
	}

	// A member's cart takes the list price at the quantity it holds.
	cart, err := app.Cart().Create(ctx, "shopper@example.com")
	if err != nil {
		t.Fatalf("cart: %v", err)
	}
	if _, err := app.Cart().AddLine(ctx, cart.Token, variant, 2); err != nil {
		t.Fatalf("add: %v", err)
	}
	got, err = app.Cart().GetByToken(ctx, cart.Token)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Lines[0].UnitPrice.AmountMinor != 9000 {
		t.Fatalf("member line = %d, want the list's 9000", got.Lines[0].UnitPrice.AmountMinor)
	}

	// Crossing the break moves the whole line, which is the point of a break:
	// the tenth unit does not cost less than the ninth while the first nine
	// stay dear.
	if _, err := app.Cart().AddLine(ctx, cart.Token, variant, 8); err != nil {
		t.Fatalf("add more: %v", err)
	}
	got, err = app.Cart().GetByToken(ctx, cart.Token)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Lines[0].Quantity != 10 {
		t.Fatalf("quantity = %d, want 10", got.Lines[0].Quantity)
	}
	if got.Lines[0].UnitPrice.AmountMinor != 8000 {
		t.Errorf("line at 10 = %d, want the 10-up break at 8000",
			got.Lines[0].UnitPrice.AmountMinor)
	}

	// And the order keeps that price, which is the figure the shopper pays.
	result, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if n := len(result.Order.Lines); n != 1 {
		t.Fatalf("order lines = %d, want 1", n)
	}
	if got := result.Order.Lines[0].UnitPrice.AmountMinor; got != 8000 {
		t.Errorf("order line = %d, want 8000", got)
	}
	if got := result.Order.Subtotal.AmountMinor; got != 80000 {
		t.Errorf("subtotal = %d, want 80000 (10 x 8000)", got)
	}
}

// A listed line must not read as "the price changed".
//
// The cart carries both the snapshot and the current price, and the difference
// is what puts a re-confirm banner in front of the shopper. Comparing the
// snapshot against the catalogue price would fire that banner on every trade
// cart every time, which makes it mean nothing the first time it matters.
func TestListedLineIsNotFlaggedAsChanged(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "PL-FLAG", 10000, 50)
	variant := product.DefaultVariant().ID

	g, err := app.Pricing().CreateGroup(ctx, CustomerGroupInput{Code: "flag", Name: "Flagged"})
	if err != nil {
		t.Fatalf("group: %v", err)
	}
	if err := app.Pricing().AddMember(ctx, g.ID, "buyer@example.com"); err != nil {
		t.Fatalf("member: %v", err)
	}
	list, err := app.Pricing().CreateList(ctx, PriceListInput{Name: "Trade", GroupID: &g.ID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := app.Pricing().SetPrice(ctx, list.ID,
		PriceRow{VariantID: variant, MinQuantity: 1, AmountMinor: 6000}); err != nil {
		t.Fatalf("price: %v", err)
	}

	cart, err := app.Cart().Create(ctx, "buyer@example.com")
	if err != nil {
		t.Fatalf("cart: %v", err)
	}
	if _, err := app.Cart().AddLine(ctx, cart.Token, variant, 1); err != nil {
		t.Fatalf("add: %v", err)
	}
	got, err := app.Cart().GetByToken(ctx, cart.Token)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	line := got.Lines[0]
	if line.UnitPrice.AmountMinor != 6000 {
		t.Errorf("unit price = %d, want the list's 6000", line.UnitPrice.AmountMinor)
	}
	if line.CurrentPrice.AmountMinor != 6000 {
		t.Errorf("current price = %d, want the resolved 6000 rather than the catalogue price",
			line.CurrentPrice.AmountMinor)
	}
	if line.PriceChanged {
		t.Error("a line at its list price was flagged as changed")
	}
}
