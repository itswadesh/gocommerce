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

const testEmailTimeout = 20 * time.Second

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
	ctx, cancel := context.WithTimeout(ctx, testEmailTimeout)
	defer cancel()
	err := a.notifier.send(ctx, Notification{
		Event:    EventNotificationTest,
		Channel:  ChannelEmail,
		To:       to,
		Language: a.mainLanguage(ctx),
		Data:     map[string]string{"store_name": store, "to": to},
	})
	// 422, not 502: a 5xx's message is replaced before it leaves the server,
	// and the provider's own words — "domain not verified", "API key is
	// invalid" — are the answer the operator pressed the button for.
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotificationRefused):
		return &APIError{Status: http.StatusUnprocessableEntity, Code: "provider_refused",
			Message: "The email provider refused the test: " + refusalReason(err)}
	default:
		// Unreachable: transient, and its detail is an address on the
		// server's network, which stays in the log.
		a.log.Error("test email could not reach the provider", "error", err)
		return &APIError{Status: http.StatusUnprocessableEntity, Code: "provider_unreachable",
			Message: "Could not reach the email provider, so nothing was sent. Try again in a minute; if it keeps failing, the server log says why."}
	}
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
