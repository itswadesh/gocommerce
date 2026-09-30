package sendgrid

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// captured is one message the stub SendGrid received.
type captured struct {
	To      string
	From    string
	Subject string
	Body    string
}

type stub struct {
	mu       sync.Mutex
	messages []captured
	status   int
}

func (s *stub) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var payload mailPayload
	_ = json.Unmarshal(body, &payload)

	s.mu.Lock()
	msg := captured{Subject: payload.Subject, From: payload.From.Email}
	if len(payload.Personalizations) > 0 && len(payload.Personalizations[0].To) > 0 {
		msg.To = payload.Personalizations[0].To[0].Email
	}
	if len(payload.Content) > 0 {
		msg.Body = payload.Content[0].Value
	}
	s.messages = append(s.messages, msg)
	status := s.status
	s.mu.Unlock()

	if status == 0 {
		status = http.StatusAccepted
	}
	w.WriteHeader(status)
}

func (s *stub) all() []captured {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]captured, len(s.messages))
	copy(out, s.messages)
	return out
}

// TestUnconfiguredWaitsForThePanel: installed with nothing in Config, the
// module is idle — the settings say email does not deliver, an order sends
// nothing — until the key and the sender are typed into its plugin, at
// which point the next event goes out. No restart in between.
func TestUnconfiguredWaitsForThePanel(t *testing.T) {
	s := &stub{}
	server := gctest.StubHTTP(t, s.handler)
	app := gctest.New(t, New(Config{BaseURL: server.URL}))
	ctx := context.Background()

	email := channel(app, gocommerce.ChannelEmail)
	if email.Delivers {
		t.Fatal("an unconfigured SendGrid should not count as delivering")
	}
	if len(email.Backends) != 2 || email.Backends[1].Name != "SendGrid" || email.Backends[1].Delivers {
		t.Fatalf("backends = %+v, want the log and an idle SendGrid", email.Backends)
	}

	result := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)
	if n := len(s.all()); n != 0 {
		t.Fatalf("messages sent before configuration = %d, want 0", n)
	}
	rows, _, err := app.Notifications().List(ctx, gocommerce.NotificationQuery{Channel: gocommerce.ChannelEmail})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != gocommerce.NotificationLogged {
		t.Fatalf("log = %+v, want one row that only reached the log", rows)
	}

	on := true
	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{
		Enabled: &on, Settings: map[string]any{"api_key": "SG.panel", "from": "shop@example.com", "from_name": "The Shop"},
	}); err != nil {
		t.Fatalf("configure from the panel: %v", err)
	}
	if !channel(app, gocommerce.ChannelEmail).Delivers {
		t.Fatal("configured from the panel, email should deliver")
	}

	if _, err := app.Ship().Create(ctx, result.Order.ID, gocommerce.ProviderManual, gocommerce.ShipRequest{Tracking: "T-1"}); err != nil {
		t.Fatalf("ship: %v", err)
	}
	gctest.DrainOutbox(t, app)
	messages := s.all()
	if len(messages) != 1 {
		t.Fatalf("messages after configuration = %d, want 1", len(messages))
	}
	if !strings.Contains(messages[0].Subject, "on its way") {
		t.Errorf("subject = %q, want the shipping notice", messages[0].Subject)
	}
	if messages[0].From != "shop@example.com" {
		t.Errorf("from = %q, want the address typed into the panel", messages[0].From)
	}
}

func channel(app *gocommerce.App, name string) gocommerce.NotifierChannelInfo {
	for _, c := range app.Settings().NotifierChannels {
		if c.Channel == name {
			return c
		}
	}
	return gocommerce.NotifierChannelInfo{}
}

// TestPanelWordingWins: what the operator typed under Setup Email is what
// goes out, over both the engine's default and a Config override.
func TestPanelWordingWins(t *testing.T) {
	s := &stub{}
	server := gctest.StubHTTP(t, s.handler)
	app := gctest.New(t, New(Config{
		APIKey: "SG.test", From: "orders@example.com", BaseURL: server.URL,
		Subjects: map[string]string{gocommerce.EventOrderCreated: "Code says {{.order_number}}"},
	}))
	ctx := context.Background()
	if _, err := app.NotifyTemplates().Set(ctx, gocommerce.ChannelEmail, gocommerce.EventOrderCreated,
		"Panel says {{.order_number}}", "Namaste {{.customer_name}}"); err != nil {
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
	if !strings.HasPrefix(messages[0].Body, "Namaste GC Test") {
		t.Errorf("body = %q, want the panel's wording", messages[0].Body)
	}

	// Restored, the Config override is next in line.
	if _, err := app.NotifyTemplates().Reset(ctx, gocommerce.ChannelEmail, gocommerce.EventOrderCreated); err != nil {
		t.Fatalf("reset: %v", err)
	}
	second := gctest.CreateProduct(t, app, "GCTEST-second", 500, 5)
	gctest.Buy(t, app, gocommerce.CodeCOD, second.Variants[0].ID, 1)
	gctest.DrainOutbox(t, app)
	messages = s.all()
	if len(messages) != 2 || !strings.HasPrefix(messages[1].Subject, "Code says") {
		t.Fatalf("after the reset the subject should be code's override, got %+v", messages)
	}
}

// TestSendsOrderEmails is the seam this module exists to fill: the engine
// emits events and this turns them into email.
func TestSendsOrderEmails(t *testing.T) {
	s := &stub{}
	server := gctest.StubHTTP(t, s.handler)

	app := gctest.New(t, New(Config{
		APIKey: "SG.test", From: "orders@example.com", FromName: "Example",
		BaseURL: server.URL,
	}))

	result := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)

	messages := s.all()
	if len(messages) != 1 {
		t.Fatalf("messages sent = %d, want 1", len(messages))
	}
	msg := messages[0]
	if msg.To != "gctest@example.com" {
		t.Errorf("recipient = %q, want the shopper's address", msg.To)
	}
	if !strings.Contains(msg.Subject, result.Order.Number) {
		t.Errorf("subject %q should name the order", msg.Subject)
	}
	if !strings.Contains(msg.Body, "GC Test") {
		t.Errorf("body %q should greet the customer by name", msg.Body)
	}

	// Shipping produces a second, different email.
	if _, err := app.Ship().Create(context.Background(), result.Order.ID,
		gocommerce.ProviderManual, gocommerce.ShipRequest{Tracking: "TRACK-9"}); err != nil {
		t.Fatalf("ship: %v", err)
	}
	gctest.DrainOutbox(t, app)

	messages = s.all()
	if len(messages) != 2 {
		t.Fatalf("messages sent = %d, want 2", len(messages))
	}
	shipped := messages[1]
	if !strings.Contains(shipped.Subject, "on its way") {
		t.Errorf("shipping subject = %q", shipped.Subject)
	}
	if !strings.Contains(shipped.Body, "TRACK-9") {
		t.Errorf("shipping body should carry the tracking number: %q", shipped.Body)
	}
	gctest.AssertOutboxEmpty(t, app)
}

// TestCustomTemplates: wording belongs to the store, so it must be
// overridable without editing this module.
func TestCustomTemplates(t *testing.T) {
	s := &stub{}
	server := gctest.StubHTTP(t, s.handler)

	app := gctest.New(t, New(Config{
		APIKey: "SG.test", From: "orders@example.com", BaseURL: server.URL,
		Subjects: map[string]string{
			gocommerce.EventOrderCreated: "Merci pour votre commande {{.order_number}}",
		},
		Bodies: map[string]string{
			gocommerce.EventOrderCreated: "Bonjour {{.customer_name}} — {{.items_summary}}",
		},
	}))

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)

	messages := s.all()
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	if !strings.HasPrefix(messages[0].Subject, "Merci") {
		t.Errorf("subject = %q, want the override", messages[0].Subject)
	}
	if !strings.HasPrefix(messages[0].Body, "Bonjour") {
		t.Errorf("body = %q, want the override", messages[0].Body)
	}
}

// TestRejectedMessageDoesNotRetryForever: a permanently bad address should
// not consume twelve delivery attempts.
func TestRejectedMessageDoesNotRetryForever(t *testing.T) {
	s := &stub{status: http.StatusBadRequest}
	server := gctest.StubHTTP(t, s.handler)

	app := gctest.New(t, New(Config{
		APIKey: "SG.test", From: "orders@example.com", BaseURL: server.URL,
	}))

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)

	// The event is considered delivered even though SendGrid refused it: a
	// 400 will be a 400 again, so retrying would only delay every event
	// behind it.
	gctest.AssertOutboxEmpty(t, app)
}
