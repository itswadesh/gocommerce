package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// TestMarkPaymentFailedFromTheAdminRoute walks the half of the payment state
// machine that had no route: an operator whose gateway declined could only
// cancel, which ends the sale, when what they meant was "not yet".
func TestMarkPaymentFailedFromTheAdminRoute(t *testing.T) {
	notifier := &recordingNotifier{}
	app := newTestApp(t, &gatewayModule{}, &notifyModule{rec: notifier})
	ctx := context.Background()

	product := simpleProduct(t, app, "FAIL-1", 1500, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(ctx, "testgateway", checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	id := result.Order.ID

	rec := do(t, app, http.MethodPost, orderPath(id, "mark-payment-failed"), withAdmin,
		jsonBody(t, map[string]any{"reason": "card declined"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	order := orderFromBody(t, rec.Body.Bytes())
	if order.PaymentStatus != PaymentFailed {
		t.Errorf("payment status = %q, want failed", order.PaymentStatus)
	}
	// The sale is not over. Recording a decline must not lose the order — the
	// whole point of the route is that cancelling was the only thing available.
	if order.Status != OrderPending {
		t.Errorf("status = %q, want the order left pending for a retry", order.Status)
	}

	// A gateway replays; so does an operator who clicked twice.
	if again := do(t, app, http.MethodPost, orderPath(id, "mark-payment-failed"), withAdmin); again.Code != http.StatusOK {
		t.Errorf("a second failure = %d, want a 200 no-op: %s", again.Code, again.Body.String())
	}

	if _, err := app.DrainOutbox(ctx); err != nil {
		t.Fatalf("drain outbox: %v", err)
	}
	for _, event := range []string{EventOrderPaid, EventOrderUnpaid, EventOrderCancelled} {
		if got := notifier.count(event); got != 0 {
			t.Errorf("%s delivered %d times; a failed payment announces nothing", event, got)
		}
	}

	// Paid afterwards, and the route refuses to unsay it.
	if _, err := app.Pay().MarkPaid(ctx, id, "second attempt"); err != nil {
		t.Fatalf("mark paid after a failure: %v", err)
	}
	paid := do(t, app, http.MethodPost, orderPath(id, "mark-payment-failed"), withAdmin)
	if paid.Code != http.StatusConflict {
		t.Errorf("failing a paid order = %d, want 409", paid.Code)
	}
	if !strings.Contains(paid.Body.String(), "already paid") {
		t.Errorf("body = %s, want it to say the order is already paid", paid.Body.String())
	}
}

// TestMarkPaidAfterAFailedAttempt proves failed -> paid was never the engine's
// objection: the forward path the panel now offers actually works, and moves
// the stock the way a first-attempt payment does.
func TestMarkPaidAfterAFailedAttempt(t *testing.T) {
	app := newTestApp(t, &gatewayModule{})
	ctx := context.Background()

	product := simpleProduct(t, app, "FAIL-2", 1500, 5)
	variant := product.DefaultVariant().ID
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, variant, 2)
	result, err := app.Order().Checkout(ctx, "testgateway", checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if _, err := app.Pay().MarkFailed(ctx, result.Order.ID, "declined"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if onHand, reserved := variantStock(t, app, variant); onHand != 5 || reserved != 2 {
		t.Fatalf("stock after a failure = (%d, %d), want (5, 2) — the reservation stands", onHand, reserved)
	}

	order, err := app.Pay().MarkPaid(ctx, result.Order.ID, "bank-transfer-991")
	if err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	if order.Status != OrderConfirmed {
		t.Errorf("status = %q, want confirmed", order.Status)
	}
	if order.PaymentReference != "bank-transfer-991" {
		t.Errorf("reference = %q, want the one just recorded", order.PaymentReference)
	}
	if onHand, reserved := variantStock(t, app, variant); onHand != 3 || reserved != 0 {
		t.Errorf("stock after payment = (%d, %d), want (3, 0)", onHand, reserved)
	}
}

// TestMarkFailedRefusesARefund is the guard that stops an operator holding only
// orders.write from erasing the record of a refund that orders.refund produced.
func TestMarkFailedRefusesARefund(t *testing.T) {
	app := newTestApp(t, refundableModule{})
	ctx := context.Background()

	product := simpleProduct(t, app, "FAIL-3", 2000, 3)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(ctx, "refundable", checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	id := result.Order.ID
	if _, err := app.Pay().MarkPaid(ctx, id, "ref"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	if _, err := app.Pay().Refund(ctx, id, RefundRequest{}, nil); err != nil {
		t.Fatalf("refund: %v", err)
	}

	_, err = app.Pay().MarkFailed(ctx, id, "declined")
	if err == nil {
		t.Fatal("a refunded order was allowed to be marked failed")
	}
	if !strings.Contains(err.Error(), "refunded") {
		t.Errorf("error = %q, want it to name the refund", err.Error())
	}
	order, err := app.Order().Get(ctx, id)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if order.PaymentStatus != PaymentRefunded {
		t.Errorf("payment status = %q, want the refund untouched", order.PaymentStatus)
	}
}

// TestMarkPaidRefusesARefund is the mirror of the guard MarkUnpaid has always
// had: a refunded order is not paid and not cancelled, so without this it falls
// through and announces an order.paid for a sale that already ended.
func TestMarkPaidRefusesARefund(t *testing.T) {
	notifier := &recordingNotifier{}
	app := newTestApp(t, refundableModule{}, &notifyModule{rec: notifier})
	ctx := context.Background()

	product := simpleProduct(t, app, "FAIL-4", 2000, 3)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(ctx, "refundable", checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	id := result.Order.ID
	if _, err := app.Pay().MarkPaid(ctx, id, "ref"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	if _, err := app.Pay().Refund(ctx, id, RefundRequest{}, nil); err != nil {
		t.Fatalf("refund: %v", err)
	}

	_, err = app.Pay().MarkPaid(ctx, id, "ref-again")
	if err == nil {
		t.Fatal("a refunded order was allowed to be marked paid again")
	}
	if !strings.Contains(err.Error(), "refunded") {
		t.Errorf("error = %q, want it to name the refund", err.Error())
	}
	order, err := app.Order().Get(ctx, id)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if order.PaymentStatus != PaymentRefunded {
		t.Errorf("payment status = %q, want it still refunded", order.PaymentStatus)
	}
	if _, err := app.DrainOutbox(ctx); err != nil {
		t.Fatalf("drain outbox: %v", err)
	}
	if got := notifier.count(EventOrderPaid); got != 1 {
		t.Errorf("order.paid delivered %d times, want the one real payment", got)
	}
}

// TestMarkFailedOnACancelledOrderIsSilent has two cases because the guard order
// is the point. Both bundled gateways answer 500 and release their idempotency
// claim on any error from MarkFailed, so a 409 here is a webhook retried
// forever over an order nobody can fix.
func TestMarkFailedOnACancelledOrderIsSilent(t *testing.T) {
	ctx := context.Background()

	t.Run("cancelled and unpaid", func(t *testing.T) {
		app := newTestApp(t, &gatewayModule{})
		product := simpleProduct(t, app, "FAIL-5", 1000, 4)
		cart := newCart(t, app)
		addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
		result, err := app.Order().Checkout(ctx, "testgateway", checkoutInput(cart.Token), "")
		if err != nil {
			t.Fatalf("checkout: %v", err)
		}
		if _, err := app.Order().Cancel(ctx, result.Order.ID, "abandoned"); err != nil {
			t.Fatalf("cancel: %v", err)
		}

		order, err := app.Pay().MarkFailed(ctx, result.Order.ID, "declined, far too late")
		if err != nil {
			t.Fatalf("a cancelled order should record nothing, quietly: %v", err)
		}
		if order.PaymentStatus != PaymentPending {
			t.Errorf("payment status = %q, want it untouched", order.PaymentStatus)
		}
	})

	// Orders.Cancel has no payment guard, so cancelled-and-paid is reachable.
	// With the paid check first this would 409 — which is the loop above.
	t.Run("cancelled and paid", func(t *testing.T) {
		app := newTestApp(t, &gatewayModule{})
		product := simpleProduct(t, app, "FAIL-6", 1000, 4)
		cart := newCart(t, app)
		addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
		result, err := app.Order().Checkout(ctx, "testgateway", checkoutInput(cart.Token), "")
		if err != nil {
			t.Fatalf("checkout: %v", err)
		}
		if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "paid"); err != nil {
			t.Fatalf("mark paid: %v", err)
		}
		if _, err := app.Order().Cancel(ctx, result.Order.ID, "cancelled after payment"); err != nil {
			t.Fatalf("cancel: %v", err)
		}

		order, err := app.Pay().MarkFailed(ctx, result.Order.ID, "a late webhook")
		if err != nil {
			t.Fatalf("cancelled is checked before paid, so this must not fail: %v", err)
		}
		if order.PaymentStatus != PaymentPaid {
			t.Errorf("payment status = %q, want the payment untouched", order.PaymentStatus)
		}
	})
}

// TestClearingARecordedFailure is the Undo the panel promises. A mis-click on
// MarkFailed writes no event and stores no reason, so without a way back it
// would be the one operator-reachable transition nothing records.
func TestClearingARecordedFailure(t *testing.T) {
	notifier := &recordingNotifier{}
	app := newTestApp(t, &gatewayModule{}, &notifyModule{rec: notifier})
	ctx := context.Background()

	product := simpleProduct(t, app, "FAIL-7", 1200, 3)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(ctx, "testgateway", checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	id := result.Order.ID
	if _, err := app.Pay().MarkFailed(ctx, id, "wrong row"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	// A reference the failed attempt left behind must not outlive it.
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE orders SET payment_reference = 'pi_dead' WHERE id = $1`, id); err != nil {
		t.Fatalf("plant a reference: %v", err)
	}

	rec := do(t, app, http.MethodPost, orderPath(id, "mark-unpaid"), withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	order := orderFromBody(t, rec.Body.Bytes())
	if order.PaymentStatus != PaymentPending {
		t.Errorf("payment status = %q, want pending", order.PaymentStatus)
	}
	if order.PaymentReference != "" {
		t.Errorf("reference = %q, want it gone with the failure", order.PaymentReference)
	}
	if _, err := app.DrainOutbox(ctx); err != nil {
		t.Fatalf("drain outbox: %v", err)
	}
	if got := notifier.count(EventOrderUnpaid); got != 0 {
		t.Errorf("order.unpaid delivered %d times; the failure was never announced", got)
	}

	// The branch that was already there is undisturbed: still a no-op on a
	// pending order, and still an event on a real payment taken back.
	if _, err := app.Pay().MarkUnpaid(ctx, id); err != nil {
		t.Fatalf("mark unpaid on a pending order: %v", err)
	}
	if _, err := app.Pay().MarkPaid(ctx, id, "for real"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	if _, err := app.Pay().MarkUnpaid(ctx, id); err != nil {
		t.Fatalf("mark unpaid on a paid order: %v", err)
	}
	if _, err := app.DrainOutbox(ctx); err != nil {
		t.Fatalf("drain outbox: %v", err)
	}
	if got := notifier.count(EventOrderUnpaid); got != 1 {
		t.Errorf("order.unpaid delivered %d times, want exactly the one real reversal", got)
	}
}

// TestFailedPaymentIsStillSwept is the regression the whole item exists to
// prevent: before M23 the sweep filtered on payment_status = 'pending', so
// recording a failure removed an order from it permanently and its stock was
// held out of sale forever.
func TestFailedPaymentIsStillSwept(t *testing.T) {
	app := newTestApp(t, &gatewayModule{})
	ctx := context.Background()

	product := simpleProduct(t, app, "SWEEP-F1", 1000, 4)
	variant := product.DefaultVariant().ID
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, variant, 3)
	result, err := app.Order().Checkout(ctx, "testgateway", checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if _, err := app.Pay().MarkFailed(ctx, result.Order.ID, "declined"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE orders SET reservation_expires_at = now() - interval '1 hour' WHERE id = $1`,
		result.Order.ID); err != nil {
		t.Fatalf("age the order: %v", err)
	}

	swept, err := app.Order().SweepUnpaid(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept != 1 {
		t.Fatalf("swept %d orders, want 1 — a failed payment is the one nobody is coming back for", swept)
	}
	if onHand, reserved := variantStock(t, app, variant); onHand != 4 || reserved != 0 {
		t.Errorf("stock after sweep = (%d, %d), want (4, 0)", onHand, reserved)
	}
	order, err := app.Order().Get(ctx, result.Order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if order.Status != OrderCancelled {
		t.Errorf("status = %q, want cancelled", order.Status)
	}
}

// TestDoctorSeesAFailedOrdersReservation: the diagnostic must not go blind on
// the population the new route creates, or the stock leak is invisible as well
// as unswept.
func TestDoctorSeesAFailedOrdersReservation(t *testing.T) {
	app := newTestApp(t, &gatewayModule{})
	ctx := context.Background()

	product := simpleProduct(t, app, "SWEEP-F2", 1000, 6)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 2)
	result, err := app.Order().Checkout(ctx, "testgateway", checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if _, err := app.Pay().MarkFailed(ctx, result.Order.ID, "declined"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE orders SET reservation_expires_at = now() - interval '1 hour' WHERE id = $1`,
		result.Order.ID); err != nil {
		t.Fatalf("age the order: %v", err)
	}

	check := diagnostic(t, app.Diagnose(ctx), "stock reservations")
	if check.Status != StatusWarn {
		t.Fatalf("status = %q, want a warning: %s", check.Status, check.Detail)
	}
	if !strings.Contains(check.Detail, "2 unit") {
		t.Errorf("detail = %q, want the held units counted", check.Detail)
	}
}

// TestOperatorPlacedOrderTakesADiscountCode: the code reaches applyTx through
// the cart and is claimed, not merely displayed.
func TestOperatorPlacedOrderTakesADiscountCode(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "PHONE-D1", 2500, 10)
	discount := newDiscount(t, app, DiscountInput{
		Code: "PHONE20", Title: "Twenty percent", Kind: DiscountPercentage, ValueBP: 2000,
	})

	result, err := app.Order().Create(ctx, NewOrderInput{
		Email:        "caller@example.com",
		DiscountCode: " phone20 ", // trimmed and matched case-insensitively, like a shopper's
		Address: Address{Line1: "1 Test Street", City: "Testville",
			PostalCode: "12345", Country: "US"},
		Lines: []NewOrderLine{{VariantID: product.DefaultVariant().ID, Quantity: 2}},
	})
	if err != nil {
		t.Fatalf("place the order: %v", err)
	}
	order := result.Order

	if order.Discount.AmountMinor != 1000 {
		t.Errorf("discount = %d, want 1000 (20%% of 5000)", order.Discount.AmountMinor)
	}
	if len(order.Discounts) != 1 || !strings.EqualFold(order.Discounts[0].Code, "PHONE20") {
		t.Errorf("order discounts = %+v, want the code snapshotted", order.Discounts)
	}
	want := order.Subtotal.AmountMinor + order.Shipping.AmountMinor - order.Discount.AmountMinor
	if order.Total.AmountMinor != want {
		t.Errorf("total = %d, want %d", order.Total.AmountMinor, want)
	}

	after, err := app.Discounts().Get(ctx, discount.ID)
	if err != nil {
		t.Fatalf("re-read the discount: %v", err)
	}
	if after.UsedCount != 1 {
		t.Errorf("used count = %d, want 1 — the code was claimed, not just shown", after.UsedCount)
	}
}

// TestOperatorPlacedOrderRefusesABadDiscountCode: the refusal happens in
// checkout phase A, so no order and no reservation survive it. That is the
// property that makes a bare code field safe without a preview beside it.
func TestOperatorPlacedOrderRefusesABadDiscountCode(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "PHONE-D2", 2500, 10)
	variant := product.DefaultVariant().ID
	good := Address{Line1: "1 Test Street", City: "Testville", PostalCode: "12345", Country: "US"}

	inactive := false
	newDiscount(t, app, DiscountInput{
		Code: "SWITCHEDOFF", Title: "Off", Kind: DiscountPercentage, ValueBP: 1000,
		Active: &inactive,
	})
	limit := 1
	newDiscount(t, app, DiscountInput{
		Code: "USEDUP", Title: "Used up", Kind: DiscountPercentage, ValueBP: 1000,
		UsageLimit: &limit,
	})
	// Spent, by an ordinary sale, so the refusal below is the real one.
	if _, err := checkoutWithCode(t, app, "USEDUP", variant, 1, ""); err != nil {
		t.Fatalf("spend the one use: %v", err)
	}
	floor := int64(100000)
	newDiscount(t, app, DiscountInput{
		Code: "BIGBASKET", Title: "Big basket", Kind: DiscountPercentage, ValueBP: 1000,
		MinSubtotalMinor: &floor,
	})

	cases := []struct {
		name string
		code string
		want string
	}{
		{name: "a code nobody created", code: "NOSUCHCODE", want: "code"},
		{name: "an inactive code", code: "SWITCHEDOFF", want: "not active"},
		{name: "a code with nothing left", code: "USEDUP", want: "fully used"},
		{name: "a basket below the minimum", code: "BIGBASKET", want: "least"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := app.Order().Create(ctx, NewOrderInput{
				Email: "caller@example.com", Address: good, DiscountCode: tc.code,
				Lines: []NewOrderLine{{VariantID: variant, Quantity: 1}},
			})
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), tc.want)
			}
			if _, reserved := variantStock(t, app, variant); reserved != 0 {
				t.Errorf("%d unit(s) reserved after a refusal, want 0", reserved)
			}
		})
	}
}

// TestCartDiscountCodeHasOneWriter: both public verbs now go through
// Carts.SetDiscountCode, which is also what the operator-placed order calls —
// so a cart that has already been checked out refuses both, where the raw
// UPDATE they used to run wrote a row nothing would ever read.
func TestCartDiscountCodeHasOneWriter(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "CARTD-1", 1000, 10)
	newDiscount(t, app, DiscountInput{
		Code: "TENTH", Title: "A tenth", Kind: DiscountPercentage, ValueBP: 1000,
	})

	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 2)
	path := "/api/carts/" + cart.Token + "/discount"

	if rec := do(t, app, http.MethodPut, path, jsonBody(t, map[string]any{"code": "TENTH"})); rec.Code != http.StatusOK {
		t.Fatalf("put = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, app, http.MethodDelete, path); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	// Set it again and buy, so the write is proved to land where checkout reads.
	if rec := do(t, app, http.MethodPut, path, jsonBody(t, map[string]any{"code": "TENTH"})); rec.Code != http.StatusOK {
		t.Fatalf("second put = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	result, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if result.Order.Discount.AmountMinor != 200 {
		t.Errorf("discount = %d, want 200 — the public route's write reached the checkout",
			result.Order.Discount.AmountMinor)
	}

	// The cart is converted now. Both verbs refuse it rather than writing.
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		rec := do(t, app, method, path, jsonBody(t, map[string]any{"code": "TENTH"}))
		if rec.Code != http.StatusConflict {
			t.Errorf("%s on a converted cart = %d, want 409: %s", method, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "already been checked out") {
			t.Errorf("%s body = %s, want the refusal to say why", method, rec.Body.String())
		}
	}

	// And a token nobody minted is still a 404, from the same one place.
	if rec := do(t, app, http.MethodDelete, "/api/carts/not-a-token/discount"); rec.Code != http.StatusNotFound {
		t.Errorf("delete on an unknown cart = %d, want 404", rec.Code)
	}
}

// TestInstalledMethodsAreNamed: the name comes from the provider, and the bare
// code array three in-repo consumers read is untouched beside it.
func TestInstalledMethodsAreNamed(t *testing.T) {
	app := newTestApp(t, namedPaymentModule{})

	installed := app.Pay().Installed()
	want := []PaymentMethod{
		{Code: "acme", Name: "Acme Pay"},
		{Code: "bank_transfer", Name: "bank_transfer"},
		{Code: CodeCOD, Name: "Cash on delivery"},
	}
	if len(installed) != len(want) {
		t.Fatalf("installed = %+v, want %+v", installed, want)
	}
	for i := range want {
		if installed[i] != want[i] {
			t.Errorf("installed[%d] = %+v, want %+v", i, installed[i], want[i])
		}
	}

	// The array smoke.ps1, the settings screen and the MCP store_info tool read.
	codes := app.Pay().Methods()
	if strings.Join(codes, ",") != "acme,bank_transfer,cod" {
		t.Errorf("Methods() = %v, want the bare codes unchanged", codes)
	}
}

// TestCheckoutMethodsResponseCarriesBothShapes: the addition is additive at the
// wire level, which is the whole justification for carrying two shapes.
func TestCheckoutMethodsResponseCarriesBothShapes(t *testing.T) {
	app := newTestApp(t, namedPaymentModule{})

	rec := do(t, app, http.MethodGet, "/api/checkout")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Data struct {
			PaymentMethods []string        `json:"payment_methods"`
			Methods        []PaymentMethod `json:"methods"`
			Currency       string          `json:"currency"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data.PaymentMethods) != len(body.Data.Methods) {
		t.Fatalf("payment_methods %v and methods %+v are different lists",
			body.Data.PaymentMethods, body.Data.Methods)
	}
	for i, code := range body.Data.PaymentMethods {
		if body.Data.Methods[i].Code != code {
			t.Errorf("methods[%d].code = %q, want %q — same set, same order",
				i, body.Data.Methods[i].Code, code)
		}
	}
	if body.Data.Currency == "" {
		t.Error("currency went missing from a response that only gained a key")
	}
}

// TestCreateOrderReturnsTheTokenAndTheIntentOnce pins both halves of the
// contract the panel's create-success state is built on: the access token is
// there once, and it is never there again.
func TestCreateOrderReturnsTheTokenAndTheIntentOnce(t *testing.T) {
	app := newTestApp(t, &gatewayModule{})

	product := simpleProduct(t, app, "PHONE-T1", 3000, 5)
	rec := do(t, app, http.MethodPost, "/api/admin/orders", withAdmin, jsonBody(t, map[string]any{
		"email":          "caller@example.com",
		"payment_method": "testgateway",
		"address": map[string]any{
			"line1": "1 Test Street", "city": "Testville",
			"postal_code": "12345", "country": "US",
		},
		"lines": []map[string]any{{"variant_id": product.DefaultVariant().ID, "quantity": 1}},
	}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data struct {
			Order   Order         `json:"order"`
			Payment PaymentIntent `json:"payment"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data.Order.AccessToken) < 32 {
		t.Errorf("access token = %q, want the credential the customer reads the order back with",
			body.Data.Order.AccessToken)
	}
	if body.Data.Payment.Kind != IntentClientAction {
		t.Errorf("payment kind = %q, want the intent the operator has to act on", body.Data.Payment.Kind)
	}
	if len(body.Data.Payment.ClientData) == 0 {
		t.Error("payment client_data is empty, so the success state has nothing to read out")
	}

	// Once, and only once.
	again := do(t, app, http.MethodGet, orderPath(body.Data.Order.ID, ""), withAdmin)
	if again.Code != http.StatusOK {
		t.Fatalf("re-read = %d, want 200", again.Code)
	}
	if strings.Contains(again.Body.String(), "access_token") {
		t.Error("GET /api/admin/orders/{id} carries the access token; it is returned at creation and nowhere else")
	}
}

// orderPath builds an admin order URL. action may be empty for the order itself.
func orderPath(id int64, action string) string {
	p := "/api/admin/orders/" + strconv.FormatInt(id, 10)
	if action != "" {
		p += "/" + action
	}
	return p
}

func orderFromBody(t *testing.T, raw []byte) Order {
	t.Helper()
	var body struct {
		Data Order `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode order: %v", err)
	}
	return body.Data
}

// namedPaymentModule installs one provider that names itself and one that does
// not, which is the whole of what [Named] is optional for.
type namedPaymentModule struct{}

func (namedPaymentModule) Name() string            { return "namedpayments" }
func (namedPaymentModule) Migrations() []Migration { return nil }
func (namedPaymentModule) Register(app *App) error {
	app.RegisterPayment(namedProvider{})
	app.RegisterPayment(unnamedProvider{})
	return nil
}

type namedProvider struct{}

func (namedProvider) Code() string        { return "acme" }
func (namedProvider) DisplayName() string { return "Acme Pay" }
func (namedProvider) Initiate(context.Context, *Order, PayOptions) (PaymentIntent, error) {
	return PaymentIntent{Kind: IntentNone, Provider: "acme"}, nil
}

type unnamedProvider struct{}

func (unnamedProvider) Code() string { return "bank_transfer" }
func (unnamedProvider) Initiate(context.Context, *Order, PayOptions) (PaymentIntent, error) {
	return PaymentIntent{Kind: IntentNone, Provider: "bank_transfer"}, nil
}
