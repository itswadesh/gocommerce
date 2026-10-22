package cashfree

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// The return page can be reloaded, or called in a loop. Cashfree is asked at
// most once in a while for one Cashfree order; the calls in between are
// answered from the store.
func TestVerifyAsksCashfreeOnceInAWhile(t *testing.T) {
	app, stub := newApp(t)
	result := gctest.PlaceOrder(t, app, "cashfree")
	path := "/x/payments-cashfree/orders/" + result.Payment.Reference + "/verify"
	before := stub.reads.Load()
	for i := range 6 {
		if rec := gctest.Request(t, app, http.MethodPost, path, nil); rec.Code != http.StatusOK {
			t.Fatalf("verify %d = %d: %s", i+1, rec.Code, rec.Body)
		}
	}
	if n := stub.reads.Load() - before; n != 1 {
		t.Errorf("Cashfree was asked %d times for six rapid verifies, want 1", n)
	}
}

// A checkout retried without end must not leave an order with an unbounded
// list of Cashfree orders, each one more call when the order is checked.
func TestCashfreeOrdersPerOrderAreCapped(t *testing.T) {
	app, _, mod := newAppModule(t)
	ctx := context.Background()
	result := gctest.PlaceOrder(t, app, "cashfree") // the first attempt
	var err error
	made := 1
	for made < maxAttempts+3 {
		if _, err = mod.Initiate(ctx, result.Order, gocommerce.PayOptions{}); err != nil {
			break
		}
		made++
	}
	if made != maxAttempts || err == nil || !strings.Contains(err.Error(), "attempts") {
		t.Errorf("made %d Cashfree orders, last error %v; want %d and then a refusal", made, err, maxAttempts)
	}
}

// A refund whose answer was lost is retried under the same refund id, so
// Cashfree refuses the second rather than paying the money out twice.
func TestRefundIDIsStableAcrossARetry(t *testing.T) {
	app, stub := newApp(t)
	ctx := context.Background()
	result := gctest.PlaceOrder(t, app, "cashfree")
	cf := result.Payment.Reference
	if rec := postWebhook(t, app, success(cf, "88", "25.00")); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}
	stub.setPayments(cf, `[`+paymentJSON("88", "SUCCESS", "25.00", "ok")+`]`)
	stub.answerRefund("timeout")
	if _, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{AmountMinor: 1000}, nil); err == nil {
		t.Fatal("a refund with no answer was recorded")
	}
	stub.answerRefund("SUCCESS")
	if _, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{AmountMinor: 1000}, nil); err != nil {
		t.Fatalf("retry: %v", err)
	}
	// A second, separate refund of the same amount is a new refund.
	if _, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{AmountMinor: 1000}, nil); err != nil {
		t.Fatalf("second refund: %v", err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.refunds) != 3 {
		t.Fatalf("refund calls = %d, want 3", len(stub.refunds))
	}
	first, retry, second := stub.refunds[0]["refund_id"], stub.refunds[1]["refund_id"], stub.refunds[2]["refund_id"]
	if first != retry {
		t.Errorf("retry refund_id %v differs from the lost one %v: Cashfree could pay out twice", retry, first)
	}
	if second == first {
		t.Errorf("a separate refund reused refund_id %v", first)
	}
}

// An offer paid part of the order, so Cashfree holds less than the total. A
// refund is never asked for more than Cashfree captured.
func TestRefundNeverExceedsWhatCashfreeCaptured(t *testing.T) {
	app, stub := newApp(t)
	ctx := context.Background()
	result := gctest.PlaceOrder(t, app, "cashfree")
	cf := result.Payment.Reference
	body := strings.Replace(success(cf, "89", "25.00"), `"payment_amount":25.00`, `"payment_amount":20.00`, 1)
	if rec := postWebhook(t, app, body); rec.Code != http.StatusOK {
		t.Fatalf("webhook = %d: %s", rec.Code, rec.Body)
	}
	stub.setPayments(cf, `[`+offerPaymentJSON("89", "SUCCESS", "25.00", "20.00", "ok")+`]`)
	if _, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{}, nil); err == nil {
		t.Error("a full refund of an offer-paid order was sent")
	}
	if sent, _ := stub.lastRefund(); sent != nil {
		t.Errorf("a refund was sent: %v", sent)
	}
	if _, err := app.Pay().Refund(ctx, result.Order.ID, gocommerce.RefundRequest{AmountMinor: 2000}, nil); err != nil {
		t.Fatalf("refund of what was captured: %v", err)
	}
	if sent, _ := stub.lastRefund(); fmt.Sprint(sent["refund_amount"]) != "20.00" {
		t.Errorf("refund sent %v, want 20.00", sent)
	}
}

// A success whose order amount Cashfree did not give in a readable form is
// not counted as paid, as the webhook already refuses it.
func TestStatusFailsClosedOnAnUnreadableAmount(t *testing.T) {
	app, stub := newApp(t)
	result := gctest.PlaceOrder(t, app, "cashfree")
	stub.setPayments(result.Payment.Reference, `[`+paymentJSON("90", "SUCCESS", "null", "ok")+`]`)
	check, err := app.Pay().CheckPayment(context.Background(), result.Order.ID)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if check.Gateway.Paid || check.Recorded {
		t.Errorf("gateway = %+v, want an unreadable amount not counted as paid", check.Gateway)
	}
}

// Money that arrives for an order already cancelled is not recorded and not
// retried for ever; it is put where an operator will see it, because it has
// to go back.
func TestSuccessForACancelledOrderIsSurfaced(t *testing.T) {
	app, _ := newApp(t)
	ctx := context.Background()
	result := gctest.PlaceOrder(t, app, "cashfree")
	if _, err := app.Order().Cancel(ctx, result.Order.ID, "shopper asked"); err != nil {
		t.Fatal(err)
	}
	body := success(result.Payment.Reference, "91", "25.00")
	for i := range 2 {
		if rec := postWebhook(t, app, body); rec.Code != http.StatusOK {
			t.Fatalf("delivery %d = %d: %s, want 200 so Cashfree stops", i+1, rec.Code, rec.Body)
		}
	}
	order := getOrder(t, app, result.Order.ID)
	if order.Status != gocommerce.OrderCancelled || order.PaymentStatus == gocommerce.PaymentPaid {
		t.Errorf("order %s / %s, want still cancelled and not paid", order.Status, order.PaymentStatus)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		issues, _, err := app.Errors().List(ctx, gocommerce.ErrorQuery{})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, is := range issues {
			found = found || (strings.Contains(is.Title, "cancelled") && strings.Contains(is.Title, "refund"))
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no issue on the Errors screen for a payment on a cancelled order: %+v", issues)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// An order moved to another payment method is no longer Cashfree's to settle.
func TestVerifyRefusesAnOrderNoLongerOnCashfree(t *testing.T) {
	app, _ := newApp(t)
	result := gctest.PlaceOrder(t, app, "cashfree")
	cod := gocommerce.CodeCOD
	if _, err := app.Order().Update(context.Background(), result.Order.ID, gocommerce.OrderPatch{PaymentProvider: &cod}); err != nil {
		t.Fatalf("move to cash on delivery: %v", err)
	}
	rec := gctest.Request(t, app, http.MethodPost, "/x/payments-cashfree/orders/"+result.Payment.Reference+"/verify", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no longer") {
		t.Errorf("verify = %d: %s; want 409 saying the order is no longer paid through Cashfree", rec.Code, rec.Body)
	}
}
