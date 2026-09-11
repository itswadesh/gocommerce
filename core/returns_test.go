package gocommerce

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// What a return is, proved against what the engine could do before it: walk a
// delivered order backwards — undeliver, delete the shipment, cancel — which
// restocks every line at its full quantity and rewrites a completed sale as a
// cancellation.

// ------------------------------------------------------------------ fixtures

// delivered walks a cash-on-delivery sale all the way out of the door, which is
// the state a return starts from.
func delivered(t *testing.T, app *App, variantID int64, qty int) *Order {
	t.Helper()
	ctx := context.Background()
	order := buy(t, app, variantID, qty)
	if _, err := app.Ship().Create(ctx, order.ID, "", ShipRequest{Tracking: "TRK-RET"}); err != nil {
		t.Fatalf("ship: %v", err)
	}
	out, err := app.Order().MarkDelivered(ctx, order.ID)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	return out
}

// id64 puts an id in a path, which these tests do more than anything else.
func id64(id int64) string { return strconv.FormatInt(id, 10) }

func returnRows(t *testing.T, app *App, orderID int64) int {
	t.Helper()
	var n int
	if err := app.DB().QueryRowContext(context.Background(),
		`SELECT count(*) FROM order_returns WHERE order_id = $1`, orderID).Scan(&n); err != nil {
		t.Fatalf("count returns: %v", err)
	}
	return n
}

func returnStatus(t *testing.T, app *App, returnID int64) string {
	t.Helper()
	var status string
	if err := app.DB().QueryRowContext(context.Background(),
		`SELECT status FROM order_returns WHERE id = $1`, returnID).Scan(&status); err != nil {
		t.Fatalf("read the return: %v", err)
	}
	return status
}

// ------------------------------------------------------------- the stock half

// The classic bug this reuses lineLocation to avoid: goods going back to the
// default shelf rather than the one they were picked from.
func TestReturnRestocksToTheShelfTheLineLeftFrom(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	main := defaultLocation(t, app)
	shop := newLocation(t, app, "SHOP", "The shop floor", 1) // ahead of the default
	product := simpleProduct(t, app, "RET-SHELF", 1000, 0)
	variant := product.DefaultVariant()
	if _, err := app.Stock().Adjust(ctx, variant.ID, shop.ID, 5, ""); err != nil {
		t.Fatalf("stock the shop: %v", err)
	}

	order := delivered(t, app, variant.ID, 3)
	onHand, reserved := stockAt(t, app, variant.ID, shop.ID)
	if onHand != 2 || reserved != 0 {
		t.Fatalf("after the sale the shop holds (%d, %d), want (2, 0)", onHand, reserved)
	}

	_, rec, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Reason: "wrong size",
		Lines:  []ReturnLineInput{{LineID: order.Lines[0].ID, Quantity: 2, Restock: true}},
	})
	if err != nil {
		t.Fatalf("return: %v", err)
	}

	if onHand, reserved = stockAt(t, app, variant.ID, shop.ID); onHand != 4 || reserved != 0 {
		t.Errorf("the shop holds (%d, %d), want (4, 0) — the units went back where they left from",
			onHand, reserved)
	}
	if onHand, _ = stockAt(t, app, variant.ID, main.ID); onHand != 0 {
		t.Errorf("the default location holds %d, want 0 — a return is not a delivery to head office", onHand)
	}
	if rec.Units != 2 || rec.RestockedUnits != 2 {
		t.Errorf("return = %d units / %d restocked, want 2 / 2", rec.Units, rec.RestockedUnits)
	}
	if rec.Lines[0].LocationID == nil || *rec.Lines[0].LocationID != shop.ID {
		t.Errorf("the line records location %v, want the shop it went back on", rec.Lines[0].LocationID)
	}
}

// The whole point of the per-line flag: a damaged unit is recorded as having
// come back without being put back on sale.
func TestReturnWithoutRestockRecordsTheGoodsAndLeavesTheShelfAlone(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	main := defaultLocation(t, app)
	product := simpleProduct(t, app, "RET-DMG", 1000, 5)
	variant := product.DefaultVariant()
	order := delivered(t, app, variant.ID, 3)

	_, rec, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Reason: "arrived damaged",
		Lines:  []ReturnLineInput{{LineID: order.Lines[0].ID, Quantity: 2}},
	})
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if onHand, _ := stockAt(t, app, variant.ID, main.ID); onHand != 2 {
		t.Errorf("on_hand = %d, want 2 — nothing sellable came back", onHand)
	}
	if rec.RestockedUnits != 0 || rec.Lines[0].Restocked {
		t.Errorf("return says %d restocked, want none", rec.RestockedUnits)
	}
	if rec.Lines[0].LocationID != nil {
		t.Errorf("location = %v, want none: nothing moved", *rec.Lines[0].LocationID)
	}

	// And it still counts against what may come back, which is what makes the
	// record worth keeping.
	if _, _, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: order.Lines[0].ID, Quantity: 2}},
	}); err == nil {
		t.Fatal("a damaged return did not count against the cap")
	}
}

// The memory a restock flag on the refund route could not have had: the cap is
// cumulative across returns, not per request.
func TestReturnCannotExceedWhatWasSold(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	main := defaultLocation(t, app)
	product := simpleProduct(t, app, "RET-CAP", 500, 9)
	variant := product.DefaultVariant()
	order := delivered(t, app, variant.ID, 3)
	line := order.Lines[0].ID

	if _, _, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: line, Quantity: 4, Restock: true}},
	}); err == nil {
		t.Fatal("returned more than was sold")
	} else if !strings.Contains(err.Error(), "RET-CAP") || !strings.Contains(err.Error(), "sold 3") {
		t.Errorf("refusal = %q, want it to name the sku and what was sold", err.Error())
	}
	onHand, _ := stockAt(t, app, variant.ID, main.ID)
	if onHand != 6 {
		t.Fatalf("on_hand = %d after a refused return, want the post-sale 6", onHand)
	}

	if _, _, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: line, Quantity: 2, Restock: true}},
	}); err != nil {
		t.Fatalf("first return: %v", err)
	}
	if _, _, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: line, Quantity: 2, Restock: true}},
	}); err == nil {
		t.Fatal("two returns of two against a line of three both succeeded")
	} else if !strings.Contains(err.Error(), "already come back") {
		t.Errorf("refusal = %q, want it to say what has already come back", err.Error())
	}
	if onHand, _ = stockAt(t, app, variant.ID, main.ID); onHand != 8 {
		t.Errorf("on_hand = %d, want 8 — the refused return moved nothing", onHand)
	}

	if _, _, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: line, Quantity: 1, Restock: true}},
	}); err != nil {
		t.Fatalf("the last unit: %v", err)
	}
	if onHand, _ = stockAt(t, app, variant.ID, main.ID); onHand != 9 {
		t.Errorf("on_hand = %d, want the original 9 back", onHand)
	}
}

// A partly shipped order can only have back what actually went out. The rest is
// still in the stockroom, and restocking it would invent inventory.
func TestPartlyShippedOrderReturnsOnlyWhatWentOut(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	main := defaultLocation(t, app)
	product := simpleProduct(t, app, "RET-PART", 700, 5)
	variant := product.DefaultVariant()
	order := buy(t, app, variant.ID, 4)
	line := order.Lines[0].ID
	if _, err := app.Ship().Create(ctx, order.ID, "", ShipRequest{
		Lines: []ShipLine{{OrderLineID: line, Quantity: 1}},
	}); err != nil {
		t.Fatalf("ship one: %v", err)
	}

	if _, _, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: line, Quantity: 2, Restock: true}},
	}); err == nil {
		t.Fatal("returned two units of which only one has shipped")
	} else if !strings.Contains(err.Error(), "gone out") {
		t.Errorf("refusal = %q, want it to say only part of it has gone out", err.Error())
	}

	if _, _, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: line, Quantity: 1, Restock: true}},
	}); err != nil {
		t.Fatalf("return the one that shipped: %v", err)
	}
	if onHand, _ := stockAt(t, app, variant.ID, main.ID); onHand != 2 {
		t.Errorf("on_hand = %d, want 2 — one unit back off a sale of four", onHand)
	}
}

// returnableOrder is the exact complement of the two refusals that already call
// this operation a return, so every state has one operation that moves its
// stock.
func TestReturnRefusesAnOrderThatNeverShipped(t *testing.T) {
	app := newTestApp(t, &gatewayModule{})
	ctx := context.Background()

	product := simpleProduct(t, app, "RET-EARLY", 1000, 9)
	variant := product.DefaultVariant()

	confirmed := buy(t, app, variant.ID, 1)
	pending := hold(t, app, variant.ID, 1)
	cancelled := buy(t, app, variant.ID, 1)
	if _, err := app.Order().Cancel(ctx, cancelled.ID, "changed their mind"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	for _, c := range []struct {
		name  string
		order *Order
		want  string
	}{
		{"confirmed", confirmed, "has not shipped"},
		{"pending", pending, "has not shipped"},
		{"cancelled", cancelled, "nothing went out"},
	} {
		_, _, err := app.Order().Return(ctx, c.order.ID, ReturnInput{
			Lines: []ReturnLineInput{{LineID: c.order.Lines[0].ID, Quantity: 1, Restock: true}},
		})
		if err == nil {
			t.Fatalf("%s: a return was recorded against goods that never left", c.name)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s refusal = %q, want it to say %q", c.name, err.Error(), c.want)
		}
		if !errors.Is(err, ErrConflict) {
			t.Errorf("%s refusal is not a conflict: %v", c.name, err)
		}
	}
}

// The argument for the whole design, made as a test: the engine's own default
// payment method cannot refund at all, so a restock flag on the refund route
// could not have put a single cash-on-delivery return back on the shelf.
func TestReturnWorksOnCashOnDeliveryWhichCannotRefund(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	main := defaultLocation(t, app)
	product := simpleProduct(t, app, "RET-COD", 1500, 4)
	variant := product.DefaultVariant()
	order := delivered(t, app, variant.ID, 2)
	if _, err := app.Pay().MarkPaid(ctx, order.ID, "cash"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}

	if _, err := app.Pay().Refund(ctx, order.ID, RefundRequest{}, nil); err == nil {
		t.Fatal("cash on delivery refunded; the premise of this design has changed")
	} else if !strings.Contains(err.Error(), "does not support refunds") {
		t.Errorf("refusal = %q, want the method to say it cannot refund", err.Error())
	}

	if _, _, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: order.Lines[0].ID, Quantity: 2, Restock: true}},
	}); err != nil {
		t.Fatalf("return on a COD order: %v", err)
	}
	if onHand, _ := stockAt(t, app, variant.ID, main.ID); onHand != 4 {
		t.Errorf("on_hand = %d, want all 4 back", onHand)
	}
}

// A return moves no money, and the transactional path contains no provider call
// at all — which is what makes rule 5 hold structurally here.
func TestReturnLeavesTheMoneyAlone(t *testing.T) {
	app := newTestApp(t, refundableModule{})
	ctx := context.Background()

	product := simpleProduct(t, app, "RET-MONEY", 2000, 3)
	variant := product.DefaultVariant()
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, variant.ID, 2)
	result, err := app.Order().Checkout(ctx, "refundable", checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "charge-ref"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	if _, err := app.Ship().Create(ctx, result.Order.ID, "", ShipRequest{}); err != nil {
		t.Fatalf("ship: %v", err)
	}
	order, err := app.Order().MarkDelivered(ctx, result.Order.ID)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}

	after, _, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: order.Lines[0].ID, Quantity: 2, Restock: true}},
	})
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if after.PaymentStatus != PaymentPaid {
		t.Errorf("payment_status = %q, want it untouched at paid", after.PaymentStatus)
	}
	if after.Refunded.AmountMinor != 0 {
		t.Errorf("refunded = %d, want 0 — a return sends no money back", after.Refunded.AmountMinor)
	}
	if after.Total.AmountMinor != order.Total.AmountMinor {
		t.Errorf("total = %d, want the agreed %d", after.Total.AmountMinor, order.Total.AmountMinor)
	}
	if countRefundRows(t, app, order.ID) != 0 {
		t.Error("a return booked a refund; the two are separate records on purpose")
	}
	// And the order's status is the other thing a return must not rewrite: the
	// parcel really was delivered.
	if after.Status != OrderDelivered {
		t.Errorf("status = %q, want delivered — goods came back, the delivery still happened", after.Status)
	}
}

// ------------------------------------------------------------ what it is worth

// The suggestion is arithmetic an operator can check: what was charged, less the
// line's share of the order discount, plus its tax only where prices exclude it.
func TestReturnReportsWhatTheGoodsWereWorth(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	// 1000 x 3 = 3000, ten percent off is 300, and 18% tax on the discounted
	// 2700 is 486. A whole line back is worth 3000 - 300 + 486.
	product := simpleProduct(t, app, "RET-WORTH", 1000, 6)
	newTaxRate(t, app, TaxRateInput{Name: "GST 18%", RateBP: 1800, Country: "US"})
	newDiscount(t, app, DiscountInput{
		Code: "TENOFF", Title: "Ten percent", Kind: DiscountPercentage, ValueBP: 1000,
	})
	order, err := checkoutWithCode(t, app, "TENOFF", product.DefaultVariant().ID, 3, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if _, err := app.Ship().Create(ctx, order.ID, "", ShipRequest{}); err != nil {
		t.Fatalf("ship: %v", err)
	}
	if _, err := app.Order().MarkDelivered(ctx, order.ID); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	line := order.Lines[0]

	// One of three: a third of the discount and a third of the tax, both floored.
	_, part, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: line.ID, Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("partial return: %v", err)
	}
	wantPart := 1000 - 300/3 + line.Tax.AmountMinor/3
	if part.Refundable.AmountMinor != wantPart {
		t.Errorf("one unit is worth %d, want %d (price, less its share of the discount, plus its tax)",
			part.Refundable.AmountMinor, wantPart)
	}
	if part.Refundable.Currency != order.Currency {
		t.Errorf("currency = %q, want the order's %q", part.Refundable.Currency, order.Currency)
	}

	_, rest, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: line.ID, Quantity: 2}},
	})
	if err != nil {
		t.Fatalf("the rest: %v", err)
	}
	// Exact when the whole line has come back, which is the ordinary case; the
	// flooring can only ever leave a minor unit with the store.
	whole := part.Refundable.AmountMinor + rest.Refundable.AmountMinor
	if want := int64(3000 - 300 + line.Tax.AmountMinor); whole > want || want-whole > 2 {
		t.Errorf("the line came back in two parts worth %d, want about %d", whole, want)
	}
}

// The regression test for lockOrder not selecting tax_inclusive: an
// implementation reading o.TaxInclusive inside a transition sees false on every
// order and adds the tax to a price that already contains it.
func TestReturnReadsTheOrdersTaxFlagNotTheLockedRow(t *testing.T) {
	app := newTaxApp(t, func(c *Config) { c.PricesIncludeTax = true })
	ctx := context.Background()

	product := simpleProduct(t, app, "RET-INCL", 1180, 4)
	newTaxRate(t, app, TaxRateInput{Name: "GST 18%", RateBP: 1800, Country: "US"})
	order, err := checkoutTo(t, app, product.DefaultVariant().ID, 1, "US", "CA")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if order.Tax.AmountMinor == 0 {
		t.Fatal("the order carries no tax, so this proves nothing")
	}
	if _, err := app.Ship().Create(ctx, order.ID, "", ShipRequest{}); err != nil {
		t.Fatalf("ship: %v", err)
	}
	if _, err := app.Order().MarkDelivered(ctx, order.ID); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	_, rec, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: order.Lines[0].ID, Quantity: 1, Restock: true}},
	})
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if rec.Refundable.AmountMinor != 1180 {
		t.Errorf("refundable = %d, want the 1180 the customer paid — the tax is already in the price",
			rec.Refundable.AmountMinor)
	}
}

// ---------------------------------------------------------------- the shelves

func TestReturnToANamedLocationAndRefusesAClosedOne(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	main := defaultLocation(t, app)
	desk := newLocation(t, app, "DESK", "Returns desk", 50)
	product := simpleProduct(t, app, "RET-DESK", 400, 5)
	variant := product.DefaultVariant()
	order := delivered(t, app, variant.ID, 2)

	if _, _, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: order.Lines[0].ID, Quantity: 1, Restock: true, LocationID: desk.ID}},
	}); err != nil {
		t.Fatalf("return to the desk: %v", err)
	}
	if onHand, _ := stockAt(t, app, variant.ID, desk.ID); onHand != 1 {
		t.Errorf("the desk holds %d, want the returned unit", onHand)
	}
	if onHand, _ := stockAt(t, app, variant.ID, main.ID); onHand != 3 {
		t.Errorf("the default location holds %d, want the 3 left after the sale", onHand)
	}

	// A shelf nobody counts is not a shelf to return goods to. (A location has
	// to be emptied before it can be closed, so this one never held anything.)
	shut := newLocation(t, app, "SHUT", "The old depot", 90)
	inactive := false
	if _, err := app.Places().Update(ctx, shut.ID, LocationPatch{Active: &inactive}); err != nil {
		t.Fatalf("close the depot: %v", err)
	}
	_, _, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: order.Lines[0].ID, Quantity: 1, Restock: true, LocationID: shut.ID}},
	})
	if err == nil {
		t.Fatal("stock went into a closed location")
	}
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "closed") {
		t.Errorf("refusal = %v, want a conflict saying the location is closed", err)
	}
	// And it did not quietly fall back to the default instead.
	if onHand, _ := stockAt(t, app, variant.ID, main.ID); onHand != 3 {
		t.Errorf("the default location holds %d; the refused return moved units anyway", onHand)
	}
	if _, _, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: order.Lines[0].ID, Quantity: 1, Restock: true, LocationID: 9999}},
	}); err == nil || !errors.Is(err, ErrNotFound) {
		t.Errorf("a return into a location that does not exist = %v, want not found", err)
	}
}

// The row records what happened rather than what was asked: there is no shelf
// left to put a deleted variant back on, and the return still stands.
func TestReturnOfADeletedVariantRecordsThatNothingMoved(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "RET-GONE", 900, 3)
	order := delivered(t, app, product.DefaultVariant().ID, 1)
	if err := app.Products().DeleteProduct(ctx, product.ID); err != nil {
		t.Fatalf("delete the product: %v", err)
	}

	_, rec, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: order.Lines[0].ID, Quantity: 1, Restock: true}},
	})
	if err != nil {
		t.Fatalf("return a line whose variant is gone: %v", err)
	}
	if rec.Lines[0].Restocked || rec.Lines[0].LocationID != nil {
		t.Errorf("line = %+v, want restocked false and no location", rec.Lines[0])
	}
	if rec.RestockedUnits != 0 {
		t.Errorf("restocked_units = %d, want 0", rec.RestockedUnits)
	}
}

// ------------------------------------------------------------ one transaction

// The whole return is one transaction: a refused line leaves no row, no
// movement and no event, exactly as the checkout rollback does.
func TestRefusedReturnLeavesNoRowAndNoEvent(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	main := defaultLocation(t, app)
	first := simpleProduct(t, app, "RET-TX-A", 1000, 5)
	second := simpleProduct(t, app, "RET-TX-B", 1000, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, first.DefaultVariant().ID, 2)
	addToCart(t, app, cart.Token, second.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if _, err := app.Ship().Create(ctx, result.Order.ID, "", ShipRequest{}); err != nil {
		t.Fatalf("ship: %v", err)
	}
	order, err := app.Order().MarkDelivered(ctx, result.Order.ID)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}

	_, _, err = app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{
			{LineID: order.Lines[0].ID, Quantity: 2, Restock: true},
			{LineID: order.Lines[1].ID, Quantity: 5, Restock: true},
		},
	})
	if err == nil {
		t.Fatal("a return whose second line exceeds the cap was accepted")
	}
	if returnRows(t, app, order.ID) != 0 {
		t.Error("the refused return left a header behind")
	}
	if onHand, _ := stockAt(t, app, first.DefaultVariant().ID, main.ID); onHand != 3 {
		t.Errorf("on_hand = %d, want 3 — the first line was restocked and not rolled back", onHand)
	}
	if events := orderEvents(t, app, order.ID, EventOrderReturned); len(events) != 0 {
		t.Errorf("order.returned events = %d, want none", len(events))
	}
}

// The state change and its event commit together (rule 4), and the payload still
// decodes as an OrderEvent — which is what keeps notify.go's literal order.*
// subscription from dead-lettering it.
func TestReturnAnnouncesItselfOnce(t *testing.T) {
	rec := &recordingNotifier{}
	app := newTestApp(t, &notifyModule{rec: rec})
	ctx := context.Background()

	product := simpleProduct(t, app, "RET-EVENT", 1000, 5)
	order := delivered(t, app, product.DefaultVariant().ID, 3)

	var heard int
	app.Subscribe(EventOrderReturned, func(context.Context, Event) error {
		heard++
		return nil
	})

	if _, _, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Reason: "wrong colour",
		Lines:  []ReturnLineInput{{LineID: order.Lines[0].ID, Quantity: 2, Restock: true}},
	}); err != nil {
		t.Fatalf("return: %v", err)
	}

	events := orderEvents(t, app, order.ID, EventOrderReturned)
	if len(events) != 1 {
		t.Fatalf("order.returned events = %d, want exactly one", len(events))
	}
	ev := events[0]
	if ev.Return == nil {
		t.Fatal("the event carries no return block")
	}
	if ev.Return.Units != 2 || ev.Return.RestockedUnits != 2 {
		t.Errorf("return block = %+v, want 2 units and 2 restocked", *ev.Return)
	}
	if len(ev.Return.Lines) != 1 || !ev.Return.Lines[0].Restocked || ev.Return.Lines[0].Quantity != 2 {
		t.Errorf("return lines = %+v, want the one line that came back", ev.Return.Lines)
	}
	if ev.Return.RefundableMinor != 2000 {
		t.Errorf("refundable_minor = %d, want 2000", ev.Return.RefundableMinor)
	}
	if ev.Reason != "wrong colour" {
		t.Errorf("reason = %q, want the one the operator typed", ev.Reason)
	}
	// A return changes neither status, and the payload has to keep saying so.
	if ev.Status != OrderDelivered || ev.PaymentStatus != PaymentPending {
		t.Errorf("payload states = %q / %q, want them unchanged by the return",
			ev.Status, ev.PaymentStatus)
	}

	if _, err := app.DrainOutbox(ctx); err != nil {
		t.Fatalf("drain outbox: %v", err)
	}
	if heard != 1 {
		t.Errorf("subscribers heard %d order.returned, want 1", heard)
	}
	if n := rec.count(EventOrderReturned); n != 1 {
		t.Fatalf("order.returned notifications = %d, want 1", n)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, note := range rec.sent {
		if note.Event != EventOrderReturned {
			continue
		}
		if note.Data["return_units"] != "2" || note.Data["return_refundable_minor"] == "" {
			t.Errorf("notification data = %v, want the return figures", note.Data)
		}
	}
}

// ------------------------------------------------------------- the correction

func TestWithdrawnReturnTakesTheUnitsBackOff(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	main := defaultLocation(t, app)
	product := simpleProduct(t, app, "RET-UNDO", 1000, 5)
	variant := product.DefaultVariant()
	order := delivered(t, app, variant.ID, 3)

	_, rec, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: order.Lines[0].ID, Quantity: 2, Restock: true}},
	})
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if onHand, _ := stockAt(t, app, variant.ID, main.ID); onHand != 4 {
		t.Fatalf("on_hand = %d after the return, want 4", onHand)
	}

	if _, err := app.Order().WithdrawReturn(ctx, order.ID, rec.ID); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if onHand, _ := stockAt(t, app, variant.ID, main.ID); onHand != 2 {
		t.Errorf("on_hand = %d, want the post-sale 2 back", onHand)
	}
	if got := returnStatus(t, app, rec.ID); got != ReturnWithdrawn {
		t.Errorf("status = %q, want the row kept as withdrawn", got)
	}
	if returnRows(t, app, order.ID) != 1 {
		t.Error("the row was deleted; a return that holds units must stay accountable")
	}

	events := orderEvents(t, app, order.ID, EventOrderUnreturned)
	if len(events) != 1 || events[0].Return == nil {
		t.Fatalf("order.unreturned events = %d, want one carrying what was undone", len(events))
	}
	if events[0].Return.Status != ReturnWithdrawn || events[0].Return.Units != 2 {
		t.Errorf("event return block = %+v, want the withdrawn two units", *events[0].Return)
	}

	// Idempotent: a second withdrawal moves nothing and announces nothing.
	if _, err := app.Order().WithdrawReturn(ctx, order.ID, rec.ID); err != nil {
		t.Fatalf("second withdraw: %v", err)
	}
	if onHand, _ := stockAt(t, app, variant.ID, main.ID); onHand != 2 {
		t.Errorf("on_hand = %d after withdrawing twice, want 2", onHand)
	}
	if events := orderEvents(t, app, order.ID, EventOrderUnreturned); len(events) != 1 {
		t.Errorf("order.unreturned events = %d, want still one", len(events))
	}

	// And a return that belongs to another order is not found rather than
	// mismatched.
	other := delivered(t, app, variant.ID, 1)
	if _, err := app.Order().WithdrawReturn(ctx, other.ID, rec.ID); err == nil || !errors.Is(err, ErrNotFound) {
		t.Errorf("withdrawing another order's return = %v, want not found", err)
	}
}

// A correction really is a correction: the quantity it held becomes returnable
// again, which is what proves the cap counts only returns that still stand.
func TestWithdrawnReturnMakesTheQuantityReturnableAgain(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "RET-AGAIN", 1000, 5)
	order := delivered(t, app, product.DefaultVariant().ID, 3)

	_, rec, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: order.Lines[0].ID, Quantity: 3, Restock: true}},
	})
	if err != nil {
		t.Fatalf("return everything: %v", err)
	}
	if _, err := app.Order().WithdrawReturn(ctx, order.ID, rec.ID); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if _, _, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: order.Lines[0].ID, Quantity: 3, Restock: true}},
	}); err != nil {
		t.Fatalf("return the same units after withdrawing: %v", err)
	}
}

// The withdrawal goes through sellStock's guard, so it cannot push the count
// below what other orders have reserved — and the sentinel does not escape as a
// 500.
func TestWithdrawnReturnRefusesWhenTheUnitsHaveGoneAgain(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "RET-RESOLD", 1000, 1)
	variant := product.DefaultVariant()
	order := delivered(t, app, variant.ID, 1)

	_, rec, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: order.Lines[0].ID, Quantity: 1, Restock: true}},
	})
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	// Somebody else buys the unit that came back.
	buy(t, app, variant.ID, 1)

	_, err = app.Order().WithdrawReturn(ctx, order.ID, rec.ID)
	if err == nil {
		t.Fatal("withdrew a return whose units have been sold again")
	}
	if !errors.Is(err, ErrConflict) {
		t.Errorf("refusal = %v, want a conflict rather than a bare sentinel", err)
	}
	if !strings.Contains(err.Error(), "RET-RESOLD") || !strings.Contains(err.Error(), "adjust the stock") {
		t.Errorf("refusal = %q, want the sku and a next step", err.Error())
	}
	if got := returnStatus(t, app, rec.ID); got != ReturnReceived {
		t.Errorf("status = %q, want the return still standing after a refused withdrawal", got)
	}
}

// ----------------------------------------------------------- the double crack

// The regression test for the hole this feature closes: deliver, return, walk
// the order back to confirmed, then cancel — which would restock every line at
// its full quantity on top of what has already come back.
func TestCancelRefusesOnceGoodsHaveComeBack(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	main := defaultLocation(t, app)
	product := simpleProduct(t, app, "RET-CRACK", 1000, 5)
	variant := product.DefaultVariant()
	order := delivered(t, app, variant.ID, 5)

	if _, _, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: order.Lines[0].ID, Quantity: 2, Restock: true}},
	}); err != nil {
		t.Fatalf("return: %v", err)
	}
	walkBack(t, app, order.ID)

	_, err := app.Order().Cancel(ctx, order.ID, "a change of mind")
	if err == nil {
		t.Fatal("cancelled an order whose goods have already come back")
	}
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "withdraw the return first") {
		t.Errorf("refusal = %v, want a conflict naming the way out", err)
	}
	if onHand, _ := stockAt(t, app, variant.ID, main.ID); onHand != 2 {
		t.Errorf("on_hand = %d, want exactly the 2 that came back — not 7", onHand)
	}
}

// Guarding Cancel alone would not have been enough: a walked-back order is
// editable, and the committed branch of moveOrderStock is a second route to the
// same double movement.
func TestEditLinesRefusesOnceGoodsHaveComeBack(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	main := defaultLocation(t, app)
	product := simpleProduct(t, app, "RET-EDIT", 1000, 5)
	variant := product.DefaultVariant()
	order := delivered(t, app, variant.ID, 5)

	if _, _, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: order.Lines[0].ID, Quantity: 2, Restock: true}},
	}); err != nil {
		t.Fatalf("return: %v", err)
	}
	walkBack(t, app, order.ID)

	_, _, err := app.Order().EditLines(ctx, order.ID, OrderEdit{
		Lines: []OrderLineEdit{{ID: order.Lines[0].ID, Quantity: 3}},
	})
	if err == nil {
		t.Fatal("edited the lines of an order whose goods have come back")
	}
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "withdraw the return first") {
		t.Errorf("refusal = %v, want a conflict naming the way out", err)
	}
	if onHand, _ := stockAt(t, app, variant.ID, main.ID); onHand != 2 {
		t.Errorf("on_hand = %d, want the 2 that came back and nothing else", onHand)
	}
}

// The guard is on an ACTIVE return, so a mis-keyed one is not a permanent dead
// end for the order.
func TestWithdrawingAReturnReopensCancelAndEdit(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	main := defaultLocation(t, app)
	product := simpleProduct(t, app, "RET-REOPEN", 1000, 5)
	variant := product.DefaultVariant()
	order := delivered(t, app, variant.ID, 5)

	_, rec, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: order.Lines[0].ID, Quantity: 2, Restock: true}},
	})
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	walkBack(t, app, order.ID)
	if _, err := app.Order().WithdrawReturn(ctx, order.ID, rec.ID); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if _, err := app.Order().Cancel(ctx, order.ID, "and now it really is cancelled"); err != nil {
		t.Fatalf("cancel after withdrawing: %v", err)
	}
	if onHand, _ := stockAt(t, app, variant.ID, main.ID); onHand != 5 {
		t.Errorf("on_hand = %d, want all 5 back exactly once", onHand)
	}
}

// walkBack is the three clicks that reach `confirmed` from a delivered order:
// undo the delivery, then remove the shipment that carried it.
func walkBack(t *testing.T, app *App, orderID int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := app.Order().MarkUndelivered(ctx, orderID); err != nil {
		t.Fatalf("undeliver: %v", err)
	}
	var shipmentID int64
	if err := app.DB().QueryRowContext(ctx,
		`SELECT id FROM fulfillments WHERE order_id = $1 ORDER BY id DESC LIMIT 1`,
		orderID).Scan(&shipmentID); err != nil {
		t.Fatalf("find the shipment: %v", err)
	}
	if _, err := app.Ship().Delete(ctx, shipmentID); err != nil {
		t.Fatalf("delete the shipment: %v", err)
	}
}

// ------------------------------------------------------------------ on the API

// What the order says about what has come back, read the way a client reads it.
func TestOrderReportsWhatHasComeBack(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "RET-API", 1000, 5)
	order := delivered(t, app, product.DefaultVariant().ID, 3)
	for _, qty := range []int{1, 1} {
		if _, _, err := app.Order().Return(ctx, order.ID, ReturnInput{
			Reason: "did not fit",
			Lines:  []ReturnLineInput{{LineID: order.Lines[0].ID, Quantity: qty, Restock: true}},
		}); err != nil {
			t.Fatalf("return: %v", err)
		}
	}

	rec := do(t, app, "GET", "/api/admin/orders/"+id64(order.ID), withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET order = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Data struct {
			Returns []struct {
				ID             int64  `json:"id"`
				Status         string `json:"status"`
				Units          int    `json:"units"`
				RestockedUnits int    `json:"restocked_units"`
				Refundable     struct {
					AmountMinor int64  `json:"amount_minor"`
					Currency    string `json:"currency"`
				} `json:"refundable"`
				Lines []struct {
					SKU string `json:"sku"`
				} `json:"lines"`
			} `json:"returns"`
			Lines []struct {
				ReturnedQuantity int `json:"returned_quantity"`
			} `json:"line_items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data.Returns) != 2 {
		t.Fatalf("returns = %d, want both of them in id order", len(body.Data.Returns))
	}
	if body.Data.Returns[0].ID > body.Data.Returns[1].ID {
		t.Error("returns are not in id order")
	}
	for i, r := range body.Data.Returns {
		if r.Units != 1 || r.RestockedUnits != 1 || len(r.Lines) != 1 {
			t.Errorf("return %d = %+v, want one unit, restocked, on one line", i, r)
		}
		// Money on the wire is minor units plus a code, never a formatted
		// string (rule 6).
		if r.Refundable.AmountMinor != 1000 || r.Refundable.Currency == "" {
			t.Errorf("return %d refundable = %+v, want an amount and a currency", i, r.Refundable)
		}
	}
	if body.Data.Lines[0].ReturnedQuantity != 2 {
		t.Errorf("returned_quantity = %d, want the sum of both returns", body.Data.Lines[0].ReturnedQuantity)
	}

	// An order with nothing back serialises an empty array rather than null, and
	// says nothing about returned quantities.
	clean := delivered(t, app, product.DefaultVariant().ID, 1)
	rec = do(t, app, "GET", "/api/admin/orders/"+id64(clean.ID), withAdmin)
	if !strings.Contains(rec.Body.String(), `"returns":[]`) {
		t.Errorf("an order with no returns did not serialise an empty array: %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), `"returned_quantity"`) {
		t.Errorf("a line with nothing back carries returned_quantity: %s", rec.Body)
	}
}

// The guest sees their own order, not the store's note about what it did with
// the goods.
func TestGuestOrderDoesNotCarryReturns(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "RET-GUEST", 1000, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 2)
	result, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	token := result.Order.AccessToken
	if _, err := app.Ship().Create(ctx, result.Order.ID, "", ShipRequest{}); err != nil {
		t.Fatalf("ship: %v", err)
	}
	order, err := app.Order().MarkDelivered(ctx, result.Order.ID)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if _, _, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Reason: "unsellable",
		Lines:  []ReturnLineInput{{LineID: order.Lines[0].ID, Quantity: 1}},
	}); err != nil {
		t.Fatalf("return: %v", err)
	}

	guest, err := app.Order().GetForGuest(ctx, order.Number, token)
	if err != nil {
		t.Fatalf("guest lookup: %v", err)
	}
	if len(guest.Returns) != 0 {
		t.Errorf("the guest's copy carries %d return(s); that is the store's record", len(guest.Returns))
	}
	rec := do(t, app, "GET", "/api/orders/"+order.Number+"?token="+token)
	if rec.Code != http.StatusOK {
		t.Fatalf("guest GET = %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "unsellable") {
		t.Errorf("the guest response leaks the store's own note: %s", rec.Body)
	}
}

// The two routes are gated, and the body is required: a return with no lines is
// not a return.
func TestReturnRoutesRefuseAndRespond(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "RET-HTTP", 1000, 5)
	order := delivered(t, app, product.DefaultVariant().ID, 2)
	_ = ctx

	body := `{"reason":"too big","lines":[{"line_id":` + id64(order.Lines[0].ID) + `,"quantity":1,"restock":true}]}`
	rec := doBody(t, app, "POST", "/api/admin/orders/"+id64(order.ID)+"/returns", body, withAdmin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST returns = %d: %s", rec.Code, rec.Body)
	}
	var created struct {
		Data struct {
			Order  *Order       `json:"order"`
			Return *OrderReturn `json:"return"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Data.Return == nil || created.Data.Order == nil {
		t.Fatalf("body = %s, want both the order and the return", rec.Body)
	}
	// The order comes back re-read after the commit, so it already carries what
	// just happened.
	if len(created.Data.Order.Returns) != 1 {
		t.Errorf("the order in the response carries %d return(s), want 1",
			len(created.Data.Order.Returns))
	}

	if rec := doBody(t, app, "POST", "/api/admin/orders/"+id64(order.ID)+"/returns",
		`{"lines":[]}`, withAdmin); rec.Code != http.StatusBadRequest {
		t.Errorf("an empty return = %d, want 400: %s", rec.Code, rec.Body)
	}

	del := "/api/admin/orders/" + id64(order.ID) + "/returns/" + id64(created.Data.Return.ID)
	if rec := do(t, app, "DELETE", del, withAdmin); rec.Code != http.StatusOK {
		t.Fatalf("DELETE return = %d: %s", rec.Code, rec.Body)
	}
	if got := returnStatus(t, app, created.Data.Return.ID); got != ReturnWithdrawn {
		t.Errorf("status after DELETE = %q, want withdrawn", got)
	}
	if rec := do(t, app, "DELETE",
		"/api/admin/orders/"+id64(order.ID)+"/returns/9999", withAdmin); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE of a return that does not exist = %d, want 404", rec.Code)
	}
}

// The doctor's tenth check exists for the one invariant no CHECK constraint can
// express, so the only way to break it is the way it is broken here: a write
// that went nowhere near the service.
func TestDoctorNoticesAnOverReturnedLine(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "RET-DOCTOR", 1000, 5)
	order := delivered(t, app, product.DefaultVariant().ID, 2)
	if d := findCheck(t, app.Diagnose(ctx), "returns"); d.Status != StatusOK {
		t.Fatalf("check = %+v, want ok on a store with no returns", d)
	}

	_, rec, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Lines: []ReturnLineInput{{LineID: order.Lines[0].ID, Quantity: 1, Restock: true}},
	})
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if d := findCheck(t, app.Diagnose(ctx), "returns"); d.Status != StatusOK ||
		!strings.Contains(d.Detail, "1 return") {
		t.Errorf("check = %+v, want ok and a count of what has come back", d)
	}

	// Against rule 3, which is the point: the service caps this under the
	// order's row lock, so the only way to reach it is a write that went nowhere
	// near the service.
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE order_return_lines SET quantity = 5 WHERE return_id = $1`, rec.ID); err != nil {
		t.Fatalf("bypass the service: %v", err)
	}
	d := findCheck(t, app.Diagnose(ctx), "returns")
	if d.Status != StatusFail {
		t.Errorf("check = %+v, want a failure", d)
	}
	if !strings.Contains(d.Hint, "directly") {
		t.Errorf("hint = %q, want it to say where such a row can come from", d.Hint)
	}
}

// The whole request is checked before anything moves, which is EditLines' rule
// and for its reason: a half-applied return is worse than a refused one.
func TestReturnChecksTheWholeRequestFirst(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "RET-VALID", 1000, 5)
	order := delivered(t, app, product.DefaultVariant().ID, 2)
	line := order.Lines[0].ID

	cases := []struct {
		name string
		in   ReturnInput
		want string
	}{
		{"no lines", ReturnInput{}, "at least one line"},
		{"a quantity of zero", ReturnInput{
			Lines: []ReturnLineInput{{LineID: line, Quantity: 0}},
		}, "at least one"},
		{"a negative quantity", ReturnInput{
			Lines: []ReturnLineInput{{LineID: line, Quantity: -1}},
		}, "at least one"},
		{"a line the order does not have", ReturnInput{
			Lines: []ReturnLineInput{{LineID: line + 9999, Quantity: 1}},
		}, "has no line"},
		{"the same line twice", ReturnInput{
			Lines: []ReturnLineInput{{LineID: line, Quantity: 1}, {LineID: line, Quantity: 1}},
		}, "named twice"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := app.Order().Return(ctx, order.ID, c.in)
			if err == nil {
				t.Fatal("accepted")
			}
			if !errors.Is(err, ErrValidation) {
				t.Errorf("err = %v, want a validation failure", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %q, want it to say %q", err.Error(), c.want)
			}
			if returnRows(t, app, order.ID) != 0 {
				t.Fatal("a refused request left a row behind")
			}
		})
	}
}
