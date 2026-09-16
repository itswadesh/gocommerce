package resend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	gocommerce "github.com/misiki/gocommerce/core"
	"github.com/misiki/gocommerce/gctest"
)

// captured is one message the stub Resend received.
type captured struct {
	To, From, ReplyTo, Subject, Body string
}

type stub struct {
	mu       sync.Mutex
	messages []captured
	status   int
	body     string
}

func (s *stub) handler(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var payload mailPayload
	_ = json.Unmarshal(raw, &payload)

	s.mu.Lock()
	msg := captured{
		From: payload.From, ReplyTo: payload.ReplyTo,
		Subject: payload.Subject, Body: payload.Text,
	}
	if len(payload.To) > 0 {
		msg.To = payload.To[0]
	}
	s.messages = append(s.messages, msg)
	status, body := s.status, s.body
	s.mu.Unlock()

	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == "" {
		body = `{"id":"re_abc123"}`
	}
	_, _ = w.Write([]byte(body))
}

func (s *stub) all() []captured {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]captured, len(s.messages))
	copy(out, s.messages)
	return out
}

func channel(app *gocommerce.App, name string) gocommerce.NotifierChannelInfo {
	for _, c := range app.Settings().NotifierChannels {
		if c.Channel == name {
			return c
		}
	}
	return gocommerce.NotifierChannelInfo{}
}

// The point of this module: an API key is the only thing a store has to
// provide. Every other notifier needs a verified sender before anything goes
// out, which means a store's first week of orders confirms nothing while the
// DNS is argued about. Resend sends from its own onboarding address until a
// domain is ready.
func TestAKeyAloneIsEnoughToSend(t *testing.T) {
	s := &stub{}
	server := gctest.StubHTTP(t, s.handler)
	app := gctest.New(t, New(Config{APIKey: "re_test", BaseURL: server.URL}))

	if !channel(app, gocommerce.ChannelEmail).Delivers {
		t.Fatal("a key alone should be enough for email to deliver")
	}

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)

	messages := s.all()
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want the order confirmation", len(messages))
	}
	if messages[0].From != DefaultFrom {
		t.Errorf("from = %q, want %q", messages[0].From, DefaultFrom)
	}
	if !strings.Contains(messages[0].Subject, "confirmed") {
		t.Errorf("subject = %q, want the order confirmation", messages[0].Subject)
	}
}

// And a store that has a domain says so, in the panel, without a restart.
func TestAVerifiedSenderReplacesTheDefault(t *testing.T) {
	s := &stub{}
	server := gctest.StubHTTP(t, s.handler)
	app := gctest.New(t, New(Config{APIKey: "re_test", BaseURL: server.URL}))
	ctx := context.Background()

	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{
		Settings: map[string]any{
			"from": "orders@example.com", "from_name": "The Shop", "reply_to": "help@example.com",
		},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)

	messages := s.all()
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	// Resend takes the display name inside the from header rather than as a
	// field of its own.
	if messages[0].From != "The Shop <orders@example.com>" {
		t.Errorf("from = %q, want the name and the verified address", messages[0].From)
	}
	if messages[0].ReplyTo != "help@example.com" {
		t.Errorf("reply_to = %q", messages[0].ReplyTo)
	}
}

// With no key the module cannot send, and says so rather than going quiet.
// The engine's own log notifier still records the message, which is what
// keeps "nothing is configured" from looking exactly like "everything was
// delivered".
func TestWithoutAKeyNothingSendsAndTheMessageIsStillRecorded(t *testing.T) {
	s := &stub{}
	server := gctest.StubHTTP(t, s.handler)
	app := gctest.New(t, New(Config{BaseURL: server.URL}))
	ctx := context.Background()

	email := channel(app, gocommerce.ChannelEmail)
	if email.Delivers {
		t.Fatal("with no key, email should not count as delivering")
	}
	if len(email.Backends) != 2 || email.Backends[1].Name != "Resend" || email.Backends[1].Delivers {
		t.Fatalf("backends = %+v, want the log and an idle Resend", email.Backends)
	}

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)

	if n := len(s.all()); n != 0 {
		t.Fatalf("messages sent without a key = %d, want 0", n)
	}
	rows, _, err := app.Notifications().List(ctx, gocommerce.NotificationQuery{Channel: gocommerce.ChannelEmail})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != gocommerce.NotificationLogged {
		t.Fatalf("log = %+v, want one row that only reached the log", rows)
	}

	// Typing the key in switches it on with no restart.
	on := true
	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{
		Enabled: &on, Settings: map[string]any{"api_key": "re_panel"},
	}); err != nil {
		t.Fatalf("configure from the panel: %v", err)
	}
	if !channel(app, gocommerce.ChannelEmail).Delivers {
		t.Fatal("configured from the panel, email should deliver")
	}
}

// A rejected address is not retried twelve times: it settles, and the reason
// Resend gave is in the log rather than in a stack trace.
func TestARejectedMessageSettlesRatherThanRetrying(t *testing.T) {
	s := &stub{status: http.StatusUnprocessableEntity, body: `{"message":"Invalid to field","name":"validation_error"}`}
	server := gctest.StubHTTP(t, s.handler)
	app := gctest.New(t, New(Config{APIKey: "re_test", BaseURL: server.URL}))

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)

	if n := len(s.all()); n != 1 {
		t.Fatalf("attempts = %d, want the one that was refused", n)
	}
	// Nothing is left pending: a bad address is not a transient failure.
	gctest.AssertOutboxEmpty(t, app)
}

// What the operator edits under Setup Email is what goes out, over both the
// engine's default and a Config override.
func TestPanelWordingWins(t *testing.T) {
	s := &stub{}
	server := gctest.StubHTTP(t, s.handler)
	app := gctest.New(t, New(Config{
		APIKey: "re_test", BaseURL: server.URL,
		Subjects: map[string]string{gocommerce.EventOrderCreated: "Code says {{.order_number}}"},
	}))
	ctx := context.Background()
	if _, err := app.NotifyTemplates().Set(ctx, gocommerce.ChannelEmail, gocommerce.EventOrderCreated,
		"Panel says {{.order_number}}", "Hello {{.customer_name}}"); err != nil {
		t.Fatalf("reword: %v", err)
	}

	result := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)

	messages := s.all()
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	if want := "Panel says " + result.Order.Number; messages[0].Subject != want {
		t.Errorf("subject = %q, want %q", messages[0].Subject, want)
	}
}
