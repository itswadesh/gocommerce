package gocommerce

import (
	"context"
	"strings"
	"testing"
)

// checkoutWithGiftCard puts a gift card on a cart and buys by cash on delivery.
func checkoutWithGiftCard(t *testing.T, app *App, code string, variantID int64, qty int) (*Order, error) {
	t.Helper()
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, variantID, qty)
	if _, err := app.GiftCards().SetCartGiftCard(context.Background(), cart.Token, code); err != nil {
		return nil, err
	}
	result, err := app.Order().Checkout(context.Background(), CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		return nil, err
	}
	return result.Order, nil
}

func giftCardBalance(t *testing.T, app *App, id int64) int64 {
	t.Helper()
	g, err := app.GiftCards().Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get gift card: %v", err)
	}
	return g.Balance.AmountMinor
}

// TestGiftCardPaysPartThenAll is the money: a card pays what it can, tax stays
// on the full price, the gateway is left the rest, a cancel gives it back, and
// a card that covers everything pays the order outright.
func TestGiftCardPaysPartThenAll(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	product := simpleProduct(t, app, "GC-RING", 5000, 10)
	variant := product.DefaultVariant().ID

	card, err := app.GiftCards().Issue(ctx, GiftCardInput{AmountMinor: 3000, Email: "Friend@Example.com"})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if len(card.Code) != 16 || card.Balance.AmountMinor != 3000 || card.Email != "friend@example.com" {
		t.Fatalf("issued %+v, want a 16-character code worth 3000 for friend@example.com", card)
	}

	// Typed the way a person reads it off a card.
	typed := strings.ToLower(card.Code[:4] + "-" + card.Code[4:8] + " " + card.Code[8:])
	order, err := checkoutWithGiftCard(t, app, typed, variant, 1)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	full := order.Subtotal.AmountMinor + order.Shipping.AmountMinor - order.Discount.AmountMinor +
		order.PaymentFee.AmountMinor
	if !order.TaxInclusive {
		full += order.Tax.AmountMinor
	}
	if order.GiftCard.AmountMinor != 3000 || order.Total.AmountMinor != full-3000 {
		t.Fatalf("gift card %d and total %d, want 3000 and %d", order.GiftCard.AmountMinor, order.Total.AmountMinor, full-3000)
	}
	if got := giftCardBalance(t, app, card.ID); got != 0 {
		t.Fatalf("balance after spending = %d, want 0", got)
	}

	// An empty card is refused, at the cart and at checkout alike.
	if _, err := checkoutWithGiftCard(t, app, card.Code, variant, 1); err == nil {
		t.Fatal("an empty card was accepted")
	}

	// Cancelling gives the money back, once.
	if _, err := app.Order().Cancel(ctx, order.ID, "changed mind"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := app.Order().Cancel(ctx, order.ID, "again"); err != nil {
		t.Fatalf("second cancel: %v", err)
	}
	if got := giftCardBalance(t, app, card.ID); got != 3000 {
		t.Fatalf("balance after cancel = %d, want 3000 back exactly once", got)
	}

	// A card worth more than the order pays it outright: no gateway, paid now.
	big, err := app.GiftCards().Issue(ctx, GiftCardInput{AmountMinor: 100000})
	if err != nil {
		t.Fatalf("issue big: %v", err)
	}
	paid, err := checkoutWithGiftCard(t, app, big.Code, variant, 1)
	if err != nil {
		t.Fatalf("checkout paid by card: %v", err)
	}
	if paid.Total.AmountMinor != 0 || paid.PaymentStatus != PaymentPaid {
		t.Fatalf("total %d, payment %s; want 0 and paid", paid.Total.AmountMinor, paid.PaymentStatus)
	}
	if got := giftCardBalance(t, app, big.ID); got != 100000-paid.GiftCard.AmountMinor {
		t.Fatalf("big card balance = %d, want %d", got, 100000-paid.GiftCard.AmountMinor)
	}

	// A switched-off card is refused.
	off := false
	if _, err := app.GiftCards().Update(ctx, big.ID, GiftCardPatch{Active: &off}); err != nil {
		t.Fatalf("switch off: %v", err)
	}
	if _, err := checkoutWithGiftCard(t, app, big.Code, variant, 1); err == nil {
		t.Fatal("a switched-off card was accepted")
	}
}

// TestBuyingAGiftCardIssuesOnePerUnit: paid once, a card per unit; paid again,
// no more cards.
func TestBuyingAGiftCardIssuesOnePerUnit(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	product := simpleProduct(t, app, "GC-1000", 1000, 10)
	yes, no := true, false
	if _, err := app.Products().UpdateVariant(ctx, product.DefaultVariant().ID,
		VariantPatch{GiftCard: &yes, RequiresShipping: &no}); err != nil {
		t.Fatalf("mark as gift card: %v", err)
	}

	order, err := checkoutWithGiftCard(t, app, "", product.DefaultVariant().ID, 2)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if _, err := app.Pay().MarkPaid(ctx, order.ID, "cash"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	got, err := app.Order().Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.GiftCards) != 2 || got.GiftCards[0].Initial.AmountMinor != 1000 || got.GiftCards[0].Code == got.GiftCards[1].Code {
		t.Fatalf("cards = %+v, want two different cards worth 1000", got.GiftCards)
	}
	if _, err := app.Pay().MarkUnpaid(ctx, order.ID); err != nil {
		t.Fatalf("mark unpaid: %v", err)
	}
	if _, err := app.Pay().MarkPaid(ctx, order.ID, "cash"); err != nil {
		t.Fatalf("mark paid again: %v", err)
	}
	again, _ := app.Order().Get(ctx, order.ID)
	if len(again.GiftCards) != 2 {
		t.Fatalf("paying twice made %d cards, want 2", len(again.GiftCards))
	}
}
