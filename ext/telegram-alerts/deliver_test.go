package telegramalerts

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// The module's reason to exist: an order is placed and the owner's phone
// says so, with what they need to act on it and a way into the panel.
func TestANewOrderReachesTheOwnersChat(t *testing.T) {
	app, mod, fake := newApp(t, gocommerce.Config{PanelURL: "https://admin.shop.example"})
	setUp(t, app)

	order := gctest.PlaceOrder(t, app, gocommerce.CodeCOD).Order
	gctest.DrainOutbox(t, app)
	if n := deliverOnce(t, mod); n != 1 {
		t.Fatalf("deliveries attempted = %d, want the one order.created", n)
	}

	sent := fake.messages()
	if len(sent) != 1 {
		t.Fatalf("messages = %d, want 1", len(sent))
	}
	got := sent[0]
	if got.Token != testToken || got.ChatID != "424242" {
		t.Errorf("sent with token %q to chat %q, want the configured bot and chat", got.Token, got.ChatID)
	}
	for _, want := range []string{
		order.Number,
		formatMoney(order.Total.AmountMinor, order.Total.Currency),
		"GC", // the shopper's first name
		"https://admin.shop.example/dash/orders/" + strconv.FormatInt(order.ID, 10),
	} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("text %q does not carry %q", got.Text, want)
		}
	}
	if strings.Contains(got.Text, "<no value>") {
		t.Errorf("text %q prints a missing value", got.Text)
	}
	if !got.NoPreview {
		t.Error("link previews are on; the order link would unfurl the panel's sign-in page")
	}

	ds := deliveries(t, app)
	if len(ds) != 1 || ds[0].State != StateDelivered || ds[0].EventName != gocommerce.EventOrderCreated || ds[0].OrderID != order.ID || ds[0].OrderNumber != order.Number {
		t.Fatalf("deliveries = %+v, want one delivered order.created for order %d", ds, order.ID)
	}
}

// Without a panel address there is nothing to link to, and the message says
// nothing rather than inventing one.
func TestWithNoPanelURLTheAlertCarriesNoLink(t *testing.T) {
	app, mod, fake := newApp(t, gocommerce.Config{})
	setUp(t, app)

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)
	deliverOnce(t, mod)

	sent := fake.messages()
	if len(sent) != 1 {
		t.Fatalf("messages = %d, want 1", len(sent))
	}
	if strings.Contains(sent[0].Text, "/dash/orders/") || strings.Contains(sent[0].Text, "<no value>") {
		t.Errorf("text %q, want no link and no placeholder", sent[0].Text)
	}
}

// Installed idle (D59): a store that has not set up a bot sends nothing and
// keeps nothing queued for the day it does.
func TestInstalledIdleItQueuesAndSendsNothing(t *testing.T) {
	app, mod, fake := newApp(t, gocommerce.Config{})

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)
	deliverOnce(t, mod)

	if n := len(fake.messages()); n != 0 {
		t.Errorf("messages = %d before anybody set up a bot, want 0", n)
	}
	if ds := deliveries(t, app); len(ds) != 0 {
		t.Errorf("deliveries = %d while switched off, want none queued", len(ds))
	}
	gctest.AssertOutboxEmpty(t, app)
}

// The subscriber only writes a row (D51). Telegram is not called while the
// outbox is being drained, so a slow or absent Telegram cannot hold up the
// engine's other consumers.
func TestTheOutboxNeverWaitsOnTelegram(t *testing.T) {
	app, _, fake := newApp(t, gocommerce.Config{})
	setUp(t, app)

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)
	gctest.AssertOutboxEmpty(t, app)

	if n := len(fake.messages()); n != 0 {
		t.Errorf("Telegram was called %d times from inside the outbox, want 0", n)
	}
	if ds := deliveries(t, app); len(ds) != 1 || ds[0].State != StatePending {
		t.Fatalf("deliveries = %+v, want one pending", ds)
	}
}

// The owner switched an alert off: that event is not sent at all.
func TestASwitchedOffAlertIsNotQueued(t *testing.T) {
	app, mod, fake := newApp(t, gocommerce.Config{})
	setUp(t, app)
	off := false
	patchAlert(t, app, gocommerce.EventOrderCreated, alertPatch{Active: &off})

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)
	deliverOnce(t, mod)

	if n := len(fake.messages()); n != 0 {
		t.Errorf("messages = %d with the order.created alert off, want 0", n)
	}
}

// What the owner wrote is what arrives.
func TestTheOwnersWordingIsWhatGoesOut(t *testing.T) {
	app, mod, fake := newApp(t, gocommerce.Config{})
	setUp(t, app)
	wording := "Order {{.order_number}} for {{.total}}"
	patchAlert(t, app, gocommerce.EventOrderCreated, alertPatch{Template: &wording})

	order := gctest.PlaceOrder(t, app, gocommerce.CodeCOD).Order
	gctest.DrainOutbox(t, app)
	deliverOnce(t, mod)

	sent := fake.messages()
	want := "Order " + order.Number + " for " + formatMoney(order.Total.AmountMinor, order.Total.Currency)
	if len(sent) != 1 || sent[0].Text != want {
		t.Fatalf("sent = %+v, want one message reading %q", sent, want)
	}
}

// A cancellation says why, which is the one thing the owner cannot guess.
func TestACancellationCarriesItsReason(t *testing.T) {
	app, mod, fake := newApp(t, gocommerce.Config{})
	setUp(t, app)

	order := gctest.PlaceOrder(t, app, gocommerce.CodeCOD).Order
	if _, err := app.Order().Cancel(context.Background(), order.ID, "the shopper rang to cancel"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	gctest.DrainOutbox(t, app)
	deliverOnce(t, mod)

	var cancelled string
	for _, m := range fake.messages() {
		if strings.Contains(m.Text, "the shopper rang to cancel") {
			cancelled = m.Text
		}
	}
	if cancelled == "" || !strings.Contains(cancelled, order.Number) {
		t.Fatalf("messages = %+v, want a cancellation naming the order and the reason", fake.messages())
	}
}

// The outbox delivers at least once. A second delivery of the same event is
// not a second message on the owner's phone.
func TestARedeliveredEventIsNotSentTwice(t *testing.T) {
	app, mod, fake := newApp(t, gocommerce.Config{})
	setUp(t, app)

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)
	deliverOnce(t, mod)

	if _, err := app.DB().ExecContext(context.Background(),
		`UPDATE outbox_events SET published_at = NULL, available_at = now()`); err != nil {
		t.Fatalf("replay: %v", err)
	}
	gctest.DrainOutbox(t, app)
	deliverOnce(t, mod)

	if n := len(fake.messages()); n != 1 {
		t.Errorf("messages = %d after a redelivery, want 1", n)
	}
	if n := len(deliveries(t, app)); n != 1 {
		t.Errorf("deliveries = %d after a redelivery, want 1", n)
	}
}

// Telegram having a bad minute is the ordinary failure: the alert waits and
// goes again.
func TestTelegramBeingDownIsRetriedWithBackoff(t *testing.T) {
	app, mod, fake := newApp(t, gocommerce.Config{})
	setUp(t, app)
	fake.answer(http.StatusBadGateway, `{"ok":false,"error_code":502,"description":"Bad Gateway"}`)

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)
	deliverOnce(t, mod)

	ds := deliveries(t, app)
	if len(ds) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(ds))
	}
	d := ds[0]
	if d.State != StatePending || d.Attempts != 1 || d.LastStatus != http.StatusBadGateway {
		t.Fatalf("delivery = %+v, want pending after one 502", d)
	}
	if !d.AvailableAt.After(time.Now()) {
		t.Errorf("available_at %v is not in the future, so there is no backoff", d.AvailableAt)
	}

	// Due again, and Telegram is back.
	fake.answer(0, "")
	if _, err := app.DB().ExecContext(context.Background(),
		`UPDATE telegram_alerts_deliveries SET available_at = now()`); err != nil {
		t.Fatal(err)
	}
	deliverOnce(t, mod)
	if ds := deliveries(t, app); ds[0].State != StateDelivered {
		t.Errorf("after Telegram came back the delivery is %s, want delivered", ds[0].State)
	}
}

// Telegram asking to slow down names how long; the next try waits at least
// that.
func TestARateLimitWaitsAsLongAsTelegramAsks(t *testing.T) {
	app, mod, fake := newApp(t, gocommerce.Config{})
	setUp(t, app)
	fake.answer(http.StatusTooManyRequests,
		`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 120","parameters":{"retry_after":120}}`)

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)
	deliverOnce(t, mod)

	d := deliveries(t, app)[0]
	if d.State != StatePending {
		t.Fatalf("a rate-limited delivery is %s, want pending", d.State)
	}
	if wait := time.Until(d.AvailableAt); wait < 100*time.Second {
		t.Errorf("next try in %v, want at least the 120s Telegram asked for", wait)
	}
}

// Telegram saying no — a chat that does not exist, a bot that was blocked —
// says the same no on the twelfth try. The delivery stops at once, says why,
// and the owner can send it again once it is fixed.
func TestARefusalIsNotRetriedAndCanBeSentAgainByHand(t *testing.T) {
	app, mod, fake := newApp(t, gocommerce.Config{})
	setUp(t, app)
	fake.answer(http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)
	deliverOnce(t, mod)

	d := deliveries(t, app)[0]
	if d.State != StateDead || d.Attempts != 1 {
		t.Fatalf("delivery = %+v, want dead after the one refusal", d)
	}
	if !strings.Contains(d.LastError, "chat not found") {
		t.Errorf("last_error = %q, want Telegram's reason", d.LastError)
	}

	fake.answer(0, "")
	rec := gctest.AdminRequest(t, app, http.MethodPost,
		"/api/admin/x/telegram-alerts/deliveries/"+strconv.FormatInt(d.ID, 10)+"/retry", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry = %d %s", rec.Code, rec.Body)
	}
	deliverOnce(t, mod)
	if d := deliveries(t, app)[0]; d.State != StateDelivered {
		t.Errorf("after a retry the delivery is %s, want delivered", d.State)
	}
	if n := len(fake.messages()); n != 2 {
		t.Errorf("messages = %d, want the refused one and the retried one", n)
	}

	// A delivered one is not sent twice.
	rec = gctest.AdminRequest(t, app, http.MethodPost,
		"/api/admin/x/telegram-alerts/deliveries/"+strconv.FormatInt(d.ID, 10)+"/retry", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("retrying a delivered alert = %d, want 404", rec.Code)
	}
}

// After twelve failures the queue gives up visibly rather than trying forever.
func TestTheQueueGivesUpAfterTwelveTries(t *testing.T) {
	app, mod, fake := newApp(t, gocommerce.Config{})
	setUp(t, app)
	fake.answer(http.StatusInternalServerError, `{"ok":false,"error_code":500,"description":"Internal Server Error"}`)

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)
	for i := 0; i < maxAttempts; i++ {
		if _, err := app.DB().ExecContext(context.Background(),
			`UPDATE telegram_alerts_deliveries SET available_at = now()`); err != nil {
			t.Fatal(err)
		}
		deliverOnce(t, mod)
	}
	if d := deliveries(t, app)[0]; d.State != StateDead || d.Attempts != maxAttempts {
		t.Errorf("delivery = %+v, want dead after %d attempts", d, maxAttempts)
	}
}

// The bot token is a password to the owner's bot, and Telegram's API puts it
// in the URL path — exactly where a Go network error quotes it. It must not
// reach the server log, the delivery log or a response.
func TestTheBotTokenNeverReachesALog(t *testing.T) {
	var logged bytes.Buffer
	fake := &fakeTelegram{}
	srv := gctest.StubHTTP(t, fake.handler)
	url := srv.URL
	srv.Close() // every call now fails to connect, quoting the URL it tried

	mod := New(Config{BaseURL: url})
	app := gctest.NewWithConfig(t, gocommerce.Config{
		Logger: slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}, mod)
	setUp(t, app)

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)
	deliverOnce(t, mod)

	d := deliveries(t, app)[0]
	if d.State != StatePending || d.LastError == "" {
		t.Fatalf("delivery = %+v, want pending with the network error recorded", d)
	}
	if strings.Contains(d.LastError, testToken) {
		t.Errorf("last_error carries the bot token: %q", d.LastError)
	}

	for _, path := range []string{"/api/admin/x/telegram-alerts/test", "/api/admin/x/telegram-alerts/chats"} {
		method := http.MethodPost
		if strings.HasSuffix(path, "/chats") {
			method = http.MethodGet
		}
		rec := gctest.AdminRequest(t, app, method, path, nil)
		if rec.Code < 400 {
			t.Errorf("%s with Telegram unreachable = %d, want an error", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), testToken) {
			t.Errorf("%s answered with the bot token: %s", path, rec.Body)
		}
	}
	if strings.Contains(logged.String(), testToken) {
		t.Errorf("the server log carries the bot token:\n%s", logged.String())
	}
}

// A sample order is nobody's (D96): it never reaches the owner's phone.
func TestASampleOrderSendsNoAlert(t *testing.T) {
	app, mod, fake := newApp(t, gocommerce.Config{})
	setUp(t, app)
	if _, err := app.AddSampleShop(context.Background()); err != nil {
		t.Fatalf("add the sample shop: %v", err)
	}
	gctest.DrainOutbox(t, app)
	deliverOnce(t, mod)
	if n := len(fake.messages()); n != 0 {
		t.Errorf("messages = %d for sample orders, want 0", n)
	}
}
