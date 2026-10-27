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
	mac := hmac.New(sha256.New, []byte("whsec"))
	mac.Write([]byte(body))
	req := httptest.NewRequest(http.MethodPost, "/api/checkout/razorpay/webhook", strings.NewReader(body))
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

	if rec := postWebhook(t, app, paymentEvent("payment.captured", "pay_NONOTES", `[]`)); rec.Code != http.StatusOK {
		t.Fatalf("notes [] answered %d, want 200: %s", rec.Code, rec.Body)
	}

	notes := fmt.Sprintf(`{"order_id":"%d","order_number":%q,"attempt":2,"gift":true}`, result.Order.ID, result.Order.Number)
	if rec := postWebhook(t, app, paymentEvent("payment.captured", "pay_OK1", notes)); rec.Code != http.StatusOK {
		t.Fatalf("notes with non-string values answered %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := paymentStatus(t, app, result.Order.ID); got != gocommerce.PaymentPaid {
		t.Errorf("payment status = %q, want paid", got)
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
