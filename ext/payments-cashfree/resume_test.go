package cashfree

import (
	"context"
	"testing"

	gocommerce "github.com/misiki-in/gocommerce/core"
)

// place checks out under a key and returns the input, so a test can retry it.
func place(t *testing.T, app *gocommerce.App, sku string) (gocommerce.CheckoutInput, *gocommerce.CheckoutResult) {
	t.Helper()
	in := checkoutInput(t, app, sku, "+919876543210")
	first, err := app.Order().Checkout(context.Background(), "cashfree", in, "key-"+sku)
	if err != nil {
		t.Fatalf("first checkout: %v", err)
	}
	return in, first
}

func (s *cashfreeStub) ordersMade() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.created)
}

// The shopper closes the window and presses pay again. It must open on the same
// Cashfree order: Cashfree refuses a second payment on one order, not across two.
func TestRetryAfterClosingTheWindowReopensTheSameCashfreeOrder(t *testing.T) {
	app, stub := newApp(t)
	ctx := context.Background()
	in, first := place(t, app, "GCTEST-reopen")

	again, err := app.Order().Checkout(ctx, "cashfree", in, "key-GCTEST-reopen")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := again.Payment.ClientData["payment_session_id"]; got != "session_reopened" {
		t.Errorf("session = %q, want the one Cashfree gives now", got)
	}
	if again.Payment.Reference != first.Payment.Reference || again.Payment.ClientData["order_id"] != first.Payment.Reference {
		t.Errorf("retry opened %q, want the first Cashfree order %q", again.Payment.Reference, first.Payment.Reference)
	}
	if again.Order.ID != first.Order.ID {
		t.Errorf("retry made order %d, want %d", again.Order.ID, first.Order.ID)
	}
	if n := stub.ordersMade(); n != 1 {
		t.Errorf("%d Cashfree orders made, want 1", n)
	}
}

// Paid on Cashfree with the webhook still on its way: the retry records it, so
// the shopper is told, and no second payment is opened.
func TestRetryOfAPaidCashfreeOrderRecordsThePayment(t *testing.T) {
	app, stub := newApp(t)
	in, first := place(t, app, "GCTEST-paid")
	cf := first.Payment.Reference
	stub.mu.Lock()
	stub.statuses[cf] = "PAID"
	stub.payments[cf] = `[{"cf_payment_id":"777","payment_status":"SUCCESS","order_amount":25.00,"payment_amount":25.00}]`
	stub.mu.Unlock()

	again, err := app.Order().Checkout(context.Background(), "cashfree", in, "key-GCTEST-paid")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if again.Order.PaymentStatus != gocommerce.PaymentPaid {
		t.Errorf("payment status = %q, want paid", again.Order.PaymentStatus)
	}
	if got := getOrder(t, app, first.Order.ID).PaymentReference; got != "777" {
		t.Errorf("payment reference = %q, want Cashfree's payment id", got)
	}
	if n := stub.ordersMade(); n != 1 {
		t.Errorf("%d Cashfree orders made, want 1", n)
	}
}

// No payment can start on an expired Cashfree order, so only then is a new one
// made, and the order points at it.
func TestRetryOfAnExpiredCashfreeOrderMakesANewOne(t *testing.T) {
	app, stub := newApp(t)
	in, first := place(t, app, "GCTEST-expired")
	stub.mu.Lock()
	stub.statuses[first.Payment.Reference] = "EXPIRED"
	stub.mu.Unlock()

	again, err := app.Order().Checkout(context.Background(), "cashfree", in, "key-GCTEST-expired")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if again.Payment.Reference == first.Payment.Reference {
		t.Error("the retry reopened an expired Cashfree order")
	}
	if n := stub.ordersMade(); n != 2 {
		t.Errorf("%d Cashfree orders made, want 2", n)
	}
	if got := getOrder(t, app, first.Order.ID).PaymentReference; got != again.Payment.Reference {
		t.Errorf("order's reference = %q, want the new Cashfree order %q", got, again.Payment.Reference)
	}
}

// A cancelled order's stock has gone back, and money for it would need a refund.
func TestRetryOfACancelledOrderIsRefused(t *testing.T) {
	app, _ := newApp(t)
	ctx := context.Background()
	in, first := place(t, app, "GCTEST-cancelled")
	if _, err := app.Order().Cancel(ctx, first.Order.ID, "test"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := app.Order().Checkout(ctx, "cashfree", in, "key-GCTEST-cancelled"); err == nil {
		t.Error("a retry opened payment on a cancelled order")
	}
}
