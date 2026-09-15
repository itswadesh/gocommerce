package gocommerce

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
)

// Every message the store sends leaves a row: which event, to whom, through
// what, and whether it went. These tests are the row, the list, and the
// resend.

// failingMail is a module whose email backend refuses everything, the shape
// of a vendor outage.
type failingMail struct{}

func (failingMail) Name() string            { return "failing-mail" }
func (failingMail) Migrations() []Migration { return nil }
func (failingMail) Register(app *App) error {
	app.RegisterNotifier(ChannelEmail, notifierFunc(func(ctx context.Context, n Notification) error {
		return errors.New("smtp: connection refused")
	}))
	return nil
}

type notifierFunc func(ctx context.Context, n Notification) error

func (f notifierFunc) Notify(ctx context.Context, n Notification) error { return f(ctx, n) }

func TestEveryNotificationIsRecorded(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := placeOrder(t, app, "NOTE-LOG")
	if _, err := app.DrainOutbox(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}

	rows, total, err := app.Notifications().List(ctx, NotificationQuery{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total == 0 || len(rows) == 0 {
		t.Fatalf("nothing was recorded for the order")
	}
	first := rows[len(rows)-1]
	if first.Event != EventOrderCreated || first.Channel != ChannelEmail || first.To != "shopper@example.com" ||
		first.OrderNumber != order.Number || first.Status != NotificationLogged {
		t.Errorf("row = %+v, want the confirmation email, logged, for order %s", first, order.Number)
	}
	if first.Data["order_number"] != order.Number {
		t.Errorf("data = %v", first.Data)
	}

	// The filters: by order number, by status, by channel.
	byOrder, n, _ := app.Notifications().List(ctx, NotificationQuery{Search: order.Number})
	if n == 0 || len(byOrder) == 0 {
		t.Errorf("search by order number found nothing")
	}
	if _, n, _ := app.Notifications().List(ctx, NotificationQuery{Status: NotificationFailed}); n != 0 {
		t.Errorf("failed count = %d, want none", n)
	}
	if _, n, _ := app.Notifications().List(ctx, NotificationQuery{Channel: ChannelSMS}); n != 0 {
		t.Errorf("sms count = %d, want none — the shopper gave no phone", n)
	}
}

func TestARefusedNotificationIsRecordedAsFailedAndCanBeResent(t *testing.T) {
	app := newTestApp(t, failingMail{})
	ctx := context.Background()
	placeOrder(t, app, "NOTE-FAIL")
	// The outbox retries a failing consumer, so the drain reports the failure
	// rather than swallowing it; the rows are what this test is about.
	_, _ = app.DrainOutbox(ctx)

	failed, n, err := app.Notifications().List(ctx, NotificationQuery{Status: NotificationFailed})
	if err != nil || n == 0 {
		t.Fatalf("failed rows = %d, %v", n, err)
	}
	rec := failed[0]
	if rec.Backend != "failing-mail" || rec.Error == "" || rec.Status != NotificationFailed {
		t.Errorf("row = %+v, want the backend named and the error kept", rec)
	}

	// Resent: a new row, pointing at the old one, and still failing here
	// because the backend still refuses.
	again, err := app.Notifications().Resend(ctx, rec.ID)
	if err != nil {
		t.Fatalf("resend: %v", err)
	}
	if again.ID == rec.ID || again.ResendOf == nil || *again.ResendOf != rec.ID || again.Status != NotificationFailed {
		t.Errorf("resend = %+v", again)
	}
	if _, err := app.Notifications().Resend(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("resend of nothing = %v", err)
	}
}

func TestNotificationRoutes(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := placeOrder(t, app, "NOTE-HTTP")
	if _, err := app.DrainOutbox(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if denied := do(t, app, http.MethodGet, "/api/admin/notifications"); denied.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d", denied.Code)
	}
	rec := do(t, app, http.MethodGet, "/api/admin/notifications?q="+order.Number+"&channel=email", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Data []NotificationRecord `json:"data"`
		Meta ListMeta             `json:"meta"`
	}
	decodeJSONBody(t, rec.Body.Bytes(), &body)
	if body.Meta.Total == 0 || len(body.Data) == 0 || body.Data[0].OrderNumber != order.Number {
		t.Fatalf("list = %+v", body)
	}
	if bad := do(t, app, http.MethodGet, "/api/admin/notifications?status=lost", withAdmin); bad.Code != http.StatusBadRequest {
		t.Errorf("bad status = %d", bad.Code)
	}
	id := strconv.FormatInt(body.Data[0].ID, 10)
	if one := do(t, app, http.MethodGet, "/api/admin/notifications/"+id, withAdmin); one.Code != http.StatusOK {
		t.Errorf("get = %d", one.Code)
	}
	resent := do(t, app, http.MethodPost, "/api/admin/notifications/"+id+"/resend", withAdmin)
	if resent.Code != http.StatusOK {
		t.Fatalf("resend = %d: %s", resent.Code, resent.Body)
	}
	var again struct {
		Data NotificationRecord `json:"data"`
	}
	decodeJSONBody(t, resent.Body.Bytes(), &again)
	if again.Data.ResendOf == nil || again.Data.Status != NotificationLogged {
		t.Errorf("resend = %+v", again.Data)
	}
}
