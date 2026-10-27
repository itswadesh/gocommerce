package razorpay

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

func postWebhook(t *testing.T, app *gocommerce.App, body string) *httptest.ResponseRecorder {
	t.Helper()
	return postWebhookTo(t, app, "/api/checkout/razorpay/webhook", body)
}

func postWebhookTo(t *testing.T, app *gocommerce.App, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	mac := hmac.New(sha256.New, []byte("whsec"))
	mac.Write([]byte(body))
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("X-Razorpay-Signature", hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

func paymentEvent(event, paymentID, notes string) string {
	return fmt.Sprintf(`{"entity":"event","event":%q,"contains":["payment"],"payload":{"payment":{"entity":`+
		`{"id":%q,"entity":"payment","amount":2500,"currency":"INR","order_id":"order_RZP1","notes":%s}}}}`,
		event, paymentID, notes)
}

func paymentStatus(t *testing.T, app *gocommerce.App, orderID int64) string {
	t.Helper()
	o, err := app.Order().Get(context.Background(), orderID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	return o.PaymentStatus
}

// Razorpay sends "notes": [] for a payment without notes (its own sample
// payloads do), and a note's value need not be a string.
func TestWebhookAcceptsNotesOfEveryShapeRazorpaySends(t *testing.T) {
	app, _ := newStatusApp(t)
	result := gctest.PlaceOrder(t, app, "razorpay")

	notes := fmt.Sprintf(`{"order_id":"%d","order_number":%q,"attempt":2,"gift":true}`, result.Order.ID, result.Order.Number)
	if rec := postWebhook(t, app, paymentEvent("payment.captured", "pay_OK1", notes)); rec.Code != http.StatusOK {
		t.Fatalf("notes with non-string values answered %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := paymentStatus(t, app, result.Order.ID); got != gocommerce.PaymentPaid {
		t.Errorf("payment status = %q, want paid", got)
	}

	if rec := postWebhook(t, app, paymentEvent("payment.captured", "pay_NONOTES", `[]`)); rec.Code != http.StatusOK {
		t.Fatalf("notes [] answered %d, want 200: %s", rec.Code, rec.Body)
	}
}

// An earlier attempt's failure that arrives after the order was paid is
// acknowledged and changes nothing; answering 500 would have Razorpay retry it
// for a day.
func TestALateFailureForAPaidOrderIsAcknowledged(t *testing.T) {
	app, _ := newStatusApp(t)
	result := gctest.PlaceOrder(t, app, "razorpay")
	notes := fmt.Sprintf(`{"order_id":"%d"}`, result.Order.ID)

	if rec := postWebhook(t, app, paymentEvent("payment.captured", "pay_OK1", notes)); rec.Code != http.StatusOK {
		t.Fatalf("capture answered %d: %s", rec.Code, rec.Body)
	}
	if rec := postWebhook(t, app, paymentEvent("payment.failed", "pay_FAIL1", notes)); rec.Code != http.StatusOK {
		t.Fatalf("late failure answered %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := paymentStatus(t, app, result.Order.ID); got != gocommerce.PaymentPaid {
		t.Errorf("payment status = %q after a late failure, want paid", got)
	}
}

// Initiate writes the store order's id into the Razorpay order's notes, and
// Razorpay does not promise to copy them onto the payment. A capture with no
// notes is matched by the Razorpay order it was paid against.
func TestACaptureWithoutNotesIsMatchedByItsRazorpayOrder(t *testing.T) {
	app, _ := newStatusApp(t)
	result := gctest.PlaceOrder(t, app, "razorpay") // its Razorpay order is order_RZP1

	if rec := postWebhook(t, app, paymentEvent("payment.captured", "pay_NONOTES", `[]`)); rec.Code != http.StatusOK {
		t.Fatalf("capture answered %d: %s", rec.Code, rec.Body)
	}
	if got := paymentStatus(t, app, result.Order.ID); got != gocommerce.PaymentPaid {
		t.Errorf("payment status = %q, want paid", got)
	}
}

// Two stores on one Razorpay account each receive every event. Store B
// getting a capture for store A's Razorpay order acknowledges it and leaves it
// for A.
func TestAnotherStoresRazorpayOrderIsLeftForIt(t *testing.T) {
	app, _ := newStatusApp(t)
	b, err := app.Stores().Create(context.Background(), "Second", "second")
	if err != nil {
		t.Fatal(err)
	}
	result := gctest.PlaceOrder(t, app, "razorpay") // in store A
	body := paymentEvent("payment.captured", "pay_SHARED", `[]`)

	if rec := postWebhookTo(t, app, "/api/checkout/razorpay/webhook?store="+b.ID, body); rec.Code != http.StatusOK {
		t.Fatalf("store B's endpoint answered %d to store A's capture: %s", rec.Code, rec.Body)
	}
	if got := paymentStatus(t, app, result.Order.ID); got != gocommerce.PaymentPending {
		t.Fatalf("store B's endpoint set store A's order to %q", got)
	}

	if rec := postWebhook(t, app, body); rec.Code != http.StatusOK {
		t.Fatalf("store A's endpoint answered %d: %s", rec.Code, rec.Body)
	}
	if got := paymentStatus(t, app, result.Order.ID); got != gocommerce.PaymentPaid {
		t.Fatalf("store A's order is %q: its capture was swallowed by store B", got)
	}
}

// A capture that matches no order is not claimed, so Razorpay's redelivery
// is applied once the order can be found.
func TestAnUnmatchedCaptureIsNotClaimed(t *testing.T) {
	app, _ := newStatusApp(t)
	result := gctest.PlaceOrder(t, app, "razorpay")
	setReference := func(ref string) {
		t.Helper()
		if _, err := app.DB().ExecContext(context.Background(),
			`UPDATE orders SET payment_reference = $2 WHERE id = $1`, result.Order.ID, ref); err != nil {
			t.Fatal(err)
		}
	}
	body := paymentEvent("payment.captured", "pay_LATE", `[]`)

	setReference("")
	if rec := postWebhook(t, app, body); rec.Code != http.StatusOK {
		t.Fatalf("unmatched capture answered %d: %s", rec.Code, rec.Body)
	}
	setReference("order_RZP1")
	if rec := postWebhook(t, app, body); rec.Code != http.StatusOK {
		t.Fatalf("redelivery answered %d: %s", rec.Code, rec.Body)
	}
	if got := paymentStatus(t, app, result.Order.ID); got != gocommerce.PaymentPaid {
		t.Errorf("payment status = %q after the redelivery, want paid", got)
	}
}
