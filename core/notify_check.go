package gocommerce

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"
)

// EventNotificationTest is the email an operator sends themselves from Setup
// Email to see that the provider really delivers. A notification name, like
// the operator reset, and never an outbox event.
const EventNotificationTest = "notification.test"

// sendNowTimeout bounds a send somebody is waiting on: the test email, and
// NotifyNow.
const sendNowTimeout = 20 * time.Second

// SendTestEmail sends the test email to one address now, through the store's
// email provider, and says why when it does not arrive. Without a provider
// that delivers it refuses rather than "sending" to the log and calling that
// a success — the very mistake the button exists to catch. The send is
// recorded like any other, so it also appears on the Notifications screen.
func (a *App) SendTestEmail(ctx context.Context, to string) error {
	to = strings.TrimSpace(to)
	if addr, err := mail.ParseAddress(to); err != nil || addr.Address != to || !strings.Contains(to[strings.LastIndex(to, "@")+1:], ".") {
		return Validationf("Enter the email address to send the test to, such as you@yourshop.com.")
	}
	if !a.notifier.delivers(ctx, ChannelEmail) {
		return Conflictf("No email provider is set up for this store, so nothing can be sent. Activate Resend or SendGrid above and enter its API key, then send the test again.")
	}
	store := ""
	if st, err := a.stores.Get(ctx, a.storeOf(ctx)); err == nil {
		store = st.Name
	}
	ctx, cancel := context.WithTimeout(ctx, sendNowTimeout)
	defer cancel()
	err := a.notifier.send(ctx, Notification{
		Event:    EventNotificationTest,
		Channel:  ChannelEmail,
		To:       to,
		Language: a.mainLanguage(ctx),
		Data:     map[string]string{"store_name": store, "to": to},
	})
	return a.sendFailure(err, ChannelEmail, "the test")
}

// NotifyNow is Notify for a caller with a person waiting on the answer — an
// operator who pressed Send and will tell a customer the message went.
//
// Notify is shaped for an outbox handler, and two of its answers read as
// "delivered" to anybody else: a refusal is dropped from its error, because a
// retry would be refused again, and a channel with no provider that delivers
// sends to the log and returns nil. So NotifyNow refuses when nothing on the
// channel delivers, and says why when the provider refused or could not be
// reached. The send is recorded like any other, so every attempt, failed or
// not, is on the Notifications screen.
func (a *App) NotifyNow(ctx context.Context, n Notification) error {
	n, err := a.addressed(ctx, n)
	if err != nil {
		return err
	}
	if !a.notifier.delivers(ctx, n.Channel) {
		return Conflictf("No %s provider is set up for this store, so nothing was sent. Set one up under Notifications, then try again.",
			channelNoun(n.Channel))
	}
	ctx, cancel := context.WithTimeout(ctx, sendNowTimeout)
	defer cancel()
	return a.sendFailure(a.notifier.send(ctx, n), n.Channel, "the message")
}

// WouldDeliver is whether n, sent now, would reach a provider rather than only
// the log. A channel can deliver and still have nobody for one message: a
// provider that texts only what it has a template for leaves the rest. A
// caller about to tell somebody "we sent you a code" asks this first.
func (a *App) WouldDeliver(ctx context.Context, n Notification) bool {
	return a.notifier.carries(ctx, n)
}

// sendFailure is what an operator is told about a send that did not go.
//
// 422, not 502: a 5xx's message is replaced before it leaves the server, and
// the provider's own words — "domain not verified", "API key is invalid" — are
// the answer the operator pressed the button for.
func (a *App) sendFailure(err error, channel, what string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotificationRefused):
		return &APIError{Status: http.StatusUnprocessableEntity, Code: "provider_refused",
			Message: "The " + channelNoun(channel) + " provider refused " + what + ": " + refusalReason(err)}
	default:
		// Unreachable: transient, and its detail is an address on the
		// server's network, which stays in the log.
		a.log.Error("a message could not reach the provider", "channel", channel, "error", err)
		return &APIError{Status: http.StatusUnprocessableEntity, Code: "provider_unreachable",
			Message: "Could not reach the " + channelNoun(channel) + " provider, so nothing was sent. Try again in a minute; if it keeps failing, the server log says why."}
	}
}

func channelNoun(channel string) string {
	if channel == ChannelSMS {
		return "SMS"
	}
	return channel
}

// refusalReason is what the provider said, without the module's prefix and
// the engine's "refused the message" around it.
func refusalReason(err error) string {
	msg := err.Error()
	if _, after, ok := strings.Cut(msg, ErrNotificationRefused.Error()+": "); ok {
		return after
	}
	return msg
}

func (a *App) handleSendTestEmail(w http.ResponseWriter, r *http.Request) {
	var in struct {
		To string `json:"to"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.SendTestEmail(r.Context(), in.To); err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, map[string]string{"to": strings.TrimSpace(in.To)})
}
