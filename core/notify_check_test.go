package gocommerce

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// failingNotifier is an email provider that cannot be reached.
type failingNotifier struct{}

func (failingNotifier) Notify(context.Context, Notification) error {
	return errors.New("resend: send: dial tcp 10.0.0.1:443: connection refused")
}

type failingNotifyModule struct{}

func (failingNotifyModule) Name() string            { return "failing" }
func (failingNotifyModule) Migrations() []Migration { return nil }
func (failingNotifyModule) Register(app *App) error {
	app.RegisterNotifier(ChannelEmail, failingNotifier{})
	return nil
}

// A test email goes through the store's email provider at once, as its own
// message with wording an operator can change like any other.
func TestSendTestEmailDelivers(t *testing.T) {
	rec := &recordingNotifier{}
	app := newTestApp(t, &notifyModule{rec: rec})
	ctx := context.Background()

	if _, ok, err := app.NotifyTemplates().Get(ctx, ChannelEmail, EventNotificationTest); err != nil || !ok {
		t.Fatalf("no wording for the test email: %v %v", ok, err)
	}
	if err := app.SendTestEmail(ctx, " owner@example.com "); err != nil {
		t.Fatalf("SendTestEmail: %v", err)
	}
	if got := rec.count(EventNotificationTest); got != 1 {
		t.Fatalf("test emails sent = %d, want 1", got)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if to := rec.sent[0].To; to != "owner@example.com" {
		t.Errorf("To = %q", to)
	}
}

// With no provider that delivers, nothing is "sent" to the log and called a
// success: the operator is told what to set up.
func TestSendTestEmailWithoutAProvider(t *testing.T) {
	app := newTestApp(t)
	err := app.SendTestEmail(context.Background(), "owner@example.com")
	var api *APIError
	if !errors.As(err, &api) || api.Status != http.StatusConflict || !strings.Contains(api.Message, "No email provider") {
		t.Fatalf("err = %v", err)
	}
}

func TestSendTestEmailRefusesABadAddress(t *testing.T) {
	app := newTestApp(t, &notifyModule{rec: &recordingNotifier{}})
	for _, to := range []string{"", "owner", "owner@", "@example.com", "a b@example.com"} {
		var api *APIError
		if err := app.SendTestEmail(context.Background(), to); !errors.As(err, &api) || api.Status != http.StatusBadRequest {
			t.Errorf("%q: err = %v", to, err)
		}
	}
}

// refusingNotifier is a provider that answered and said no.
type refusingNotifier struct{}

func (refusingNotifier) Notify(context.Context, Notification) error {
	return fmt.Errorf("resend: %w: API key is invalid", ErrNotificationRefused)
}

type refusingNotifyModule struct{}

func (refusingNotifyModule) Name() string            { return "refusing" }
func (refusingNotifyModule) Migrations() []Migration { return nil }
func (refusingNotifyModule) Register(app *App) error {
	app.RegisterNotifier(ChannelEmail, refusingNotifier{})
	return nil
}

// A refusal is a failure the operator sees — on the test, and in the
// notification's record — but not one the outbox retries, because the
// provider would refuse it again. A provider that could not be reached is
// still retried.
func TestARefusalFailsTheTestButIsNotRetried(t *testing.T) {
	app := newTestApp(t, refusingNotifyModule{})
	ctx := context.Background()
	var api *APIError
	if err := app.SendTestEmail(ctx, "owner@example.com"); !errors.As(err, &api) || !strings.Contains(api.Message, "API key is invalid") {
		t.Fatalf("test email: %v", err)
	}
	if err := app.Notify(ctx, Notification{Event: "cart.abandoned", Channel: ChannelEmail, To: "a@example.com"}); err != nil {
		t.Fatalf("a refusal asked the outbox to retry: %v", err)
	}

	down := errors.New("dial tcp: connection refused")
	refused := fmt.Errorf("x: %w", ErrNotificationRefused)
	if err := worthRetrying(errors.Join(refused, down)); !errors.Is(err, down) || errors.Is(err, ErrNotificationRefused) {
		t.Fatalf("mixed: %v", err)
	}
	if err := worthRetrying(errors.Join(refused)); err != nil {
		t.Fatalf("only refused: %v", err)
	}
	if err := worthRetrying(down); err != down {
		t.Fatalf("only down: %v", err)
	}
}

// The provider's own words come back — "API key is invalid" is the answer the
// operator came for — as a 4xx, because a 5xx's message never leaves the
// server. Said once, without the engine's wrapping around it.
func TestSendTestEmailReportsTheProvidersReason(t *testing.T) {
	app := newTestApp(t, refusingNotifyModule{})
	err := app.SendTestEmail(context.Background(), "owner@example.com")
	var api *APIError
	if !errors.As(err, &api) || api.Status != http.StatusUnprocessableEntity ||
		api.Message != "The email provider refused the test: API key is invalid" {
		t.Fatalf("err = %v", err)
	}
}

// A provider that could not be reached says so, without the dial error.
func TestSendTestEmailWhenTheProviderIsUnreachable(t *testing.T) {
	app := newTestApp(t, failingNotifyModule{})
	err := app.SendTestEmail(context.Background(), "owner@example.com")
	var api *APIError
	if !errors.As(err, &api) || api.Status != http.StatusUnprocessableEntity ||
		!strings.Contains(api.Message, "Could not reach") || strings.Contains(api.Message, "10.0.0.1") {
		t.Fatalf("err = %v", err)
	}
}

// NotifyNow is for an operator who pressed Send and will tell a customer it
// went. Notify cannot tell them: a refusal is dropped from its error so the
// outbox does not retry it, and a channel with nobody to carry it "sends" to
// the log. So NotifyNow refuses without a provider, and says why when the
// provider refused or could not be reached — each as a 4xx, whose words leave
// the server — and every attempt is on the Notifications screen.
func TestNotifyNowSaysWhyAMessageDidNotGo(t *testing.T) {
	ctx := context.Background()
	note := Notification{Event: "contact.reply", Channel: ChannelEmail, To: "ann@example.com",
		Data: map[string]string{"reply": "It ships today."}}
	status := func(err error) (int, string) {
		t.Helper()
		var api *APIError
		if !errors.As(err, &api) {
			t.Fatalf("err = %v, want an APIError", err)
		}
		return api.Status, api.Message
	}

	if code, msg := status(newTestApp(t).NotifyNow(ctx, note)); code != http.StatusConflict ||
		!strings.Contains(msg, "No email provider") {
		t.Errorf("without a provider = %d %q, want 409 naming the missing provider", code, msg)
	}

	rec := &recordingNotifier{}
	app := newTestApp(t, &notifyModule{rec: rec})
	if err := app.NotifyNow(ctx, note); err != nil {
		t.Fatalf("delivered: %v", err)
	}
	if rec.count("contact.reply") != 1 {
		t.Errorf("sent = %d, want 1", rec.count("contact.reply"))
	}
	if err := app.NotifyNow(ctx, Notification{Event: "contact.reply", Channel: ChannelEmail, To: " "}); err == nil {
		t.Error("a message with no recipient was accepted")
	}

	refusing := newTestApp(t, refusingNotifyModule{})
	if code, msg := status(refusing.NotifyNow(ctx, note)); code != http.StatusUnprocessableEntity ||
		msg != "The email provider refused the message: API key is invalid" {
		t.Errorf("refused = %d %q", code, msg)
	}
	logged, _, err := refusing.Notifications().List(ctx, NotificationQuery{Event: "contact.reply"})
	if err != nil || len(logged) != 1 || logged[0].Status != NotificationFailed {
		t.Errorf("the refusal's record = %+v, %v; want one failed row", logged, err)
	}

	if code, msg := status(newTestApp(t, failingNotifyModule{}).NotifyNow(ctx, note)); code != http.StatusUnprocessableEntity ||
		!strings.Contains(msg, "Could not reach") || strings.Contains(msg, "10.0.0.1") {
		t.Errorf("unreachable = %d %q", code, msg)
	}
}
