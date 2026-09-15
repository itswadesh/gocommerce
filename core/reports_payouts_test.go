package gocommerce

import (
	"context"
	"net/http"
	"testing"
)

// placeOn is placeOrder through a named payment method, which is the whole
// distinction this report exists to make.
func placeOn(t *testing.T, app *App, sku, code string) *Order {
	t.Helper()
	product := simpleProduct(t, app, sku, 1000, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(context.Background(), code, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout %s on %s: %v", sku, code, err)
	}
	return result.Order
}

// The report's reason to exist: the sales report says how much was sold and
// never which gateway carried it. This says that, and what went back out.
func TestPayoutsSplitTheWindowByMethod(t *testing.T) {
	app := newTestApp(t, refundableModule{})
	ctx := context.Background()

	// Two sales on the card gateway, one refunded in part; one cash order
	// left unpaid, which is the money-in-transit book.
	paid := placeOn(t, app, "PO-CARD-1", "refundable")
	if _, err := app.Pay().MarkPaid(ctx, paid.ID, "ref-1"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	refunded := placeOn(t, app, "PO-CARD-2", "refundable")
	if _, err := app.Pay().MarkPaid(ctx, refunded.ID, "ref-2"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	if _, err := app.Pay().Refund(ctx, refunded.ID, RefundRequest{AmountMinor: 400, Reason: "one item back"}, nil); err != nil {
		t.Fatalf("refund: %v", err)
	}
	owed := placeOn(t, app, "PO-COD-1", CodeCOD)

	report, err := app.Reports().Payouts(ctx, PayoutsQuery{})
	if err != nil {
		t.Fatalf("payouts: %v", err)
	}
	if len(report.Currencies) != 1 {
		t.Fatalf("currencies = %d, want the store's one", len(report.Currencies))
	}
	block := report.Currencies[0]

	byCode := map[string]MethodPayout{}
	for _, m := range block.Methods {
		byCode[m.Code] = m
	}
	card, ok := byCode["refundable"]
	if !ok {
		t.Fatalf("methods = %+v, want a row for the gateway that took the money", block.Methods)
	}
	if card.Orders != 2 {
		t.Errorf("gateway orders = %d, want the two it charged", card.Orders)
	}
	wantCollected := paid.Total.AmountMinor + refunded.Total.AmountMinor
	if card.Collected.AmountMinor != wantCollected {
		t.Errorf("collected = %d, want %d", card.Collected.AmountMinor, wantCollected)
	}
	if card.Refunded.AmountMinor != 400 {
		t.Errorf("refunded = %d, want the 400 that went back", card.Refunded.AmountMinor)
	}
	if card.Net.AmountMinor != wantCollected-400 {
		t.Errorf("net = %d, want collected less refunded", card.Net.AmountMinor)
	}
	if !card.Installed {
		t.Error("a method this binary carries should say so")
	}

	// Cash on delivery sold and has not paid: that is outstanding, not
	// collected, and the difference is the whole point of the column.
	cod, ok := byCode[CodeCOD]
	if !ok {
		t.Fatalf("methods = %+v, want a row for the unpaid cash order", block.Methods)
	}
	if cod.Orders != 0 || cod.Collected.AmountMinor != 0 {
		t.Errorf("cash collected = %+v, want nothing collected yet", cod)
	}
	if cod.OutstandingOrders != 1 || cod.Outstanding.AmountMinor != owed.Total.AmountMinor {
		t.Errorf("cash outstanding = %d orders / %d, want the one unpaid order",
			cod.OutstandingOrders, cod.Outstanding.AmountMinor)
	}

	// The totals are the methods added up, in the block's own currency.
	if block.Totals.Collected.AmountMinor != card.Collected.AmountMinor+cod.Collected.AmountMinor {
		t.Errorf("totals.collected = %d, want the methods summed", block.Totals.Collected.AmountMinor)
	}
	if block.Totals.Collected.Currency != block.Currency {
		t.Errorf("totals currency = %q, want %q", block.Totals.Collected.Currency, block.Currency)
	}

	// A cancelled order is not a sale here either, so this report and the
	// sales report cannot disagree about what a sale is.
	cancelled := placeOn(t, app, "PO-CANCELLED", CodeCOD)
	if _, err := app.Order().Cancel(ctx, cancelled.ID, "changed their mind"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	after, err := app.Reports().Payouts(ctx, PayoutsQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range after.Currencies[0].Methods {
		if m.Code == CodeCOD && m.OutstandingOrders != 1 {
			t.Errorf("after cancelling, cash outstanding = %d, want still the one real order", m.OutstandingOrders)
		}
	}
}

// A store with no sales still has a currency, and the route still answers.
func TestPayoutsRouteAnswersAnEmptyStore(t *testing.T) {
	app := newTestApp(t)

	rec := do(t, app, http.MethodGet, "/api/admin/reports/payouts", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("payouts = %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Data PayoutsReport `json:"data"`
	}
	decodeJSONBody(t, rec.Body.Bytes(), &body)
	if len(body.Data.Currencies) != 1 || body.Data.Currencies[0].Currency != app.Config().Currency {
		t.Fatalf("currencies = %+v, want the store's own even with nothing sold", body.Data.Currencies)
	}
	if len(body.Data.Currencies[0].Methods) != 0 {
		t.Errorf("methods = %+v, want none", body.Data.Currencies[0].Methods)
	}
	if body.Data.TimeZone == "" || body.Data.To.Before(body.Data.From) {
		t.Errorf("window = %+v, want a resolved one", body.Data)
	}

	// A malformed window is refused rather than guessed at.
	if rec := do(t, app, http.MethodGet, "/api/admin/reports/payouts?from=yesterday", withAdmin); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad from = %d, want 400", rec.Code)
	}
	// And it is behind the same right as the rest of the reports.
	if rec := do(t, app, http.MethodGet, "/api/admin/reports/payouts"); rec.Code != http.StatusUnauthorized {
		t.Errorf("with no token = %d, want 401", rec.Code)
	}
}
