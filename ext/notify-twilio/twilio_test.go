package twilio

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	gocommerce "github.com/misiki/gocommerce/core"
	"github.com/misiki/gocommerce/gctest"
)

// sent is one message the stub Twilio received.
type sent struct {
	AccountSid string
	User, Pass string
	Path       string
	To, From   string
	Body       string
	ServiceSid string
}

type stub struct {
	mu     sync.Mutex
	calls  []sent
	status int
	body   string
}

func (s *stub) handler(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	user, pass, _ := r.BasicAuth()
	msg := sent{
		User: user, Pass: pass, Path: r.URL.Path,
		To: r.PostFormValue("To"), From: r.PostFormValue("From"),
		Body: r.PostFormValue("Body"), ServiceSid: r.PostFormValue("MessagingServiceSid"),
	}
	// The account sid is in the path, which is how Twilio scopes the call.
	if parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/"); len(parts) >= 3 {
		msg.AccountSid = parts[2]
	}

	s.mu.Lock()
	s.calls = append(s.calls, msg)
	status, body := s.status, s.body
	s.mu.Unlock()

	if status == 0 {
		status = http.StatusCreated
	}
	if body == "" {
		body = `{"sid":"SM123","status":"queued"}`
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (s *stub) all() []sent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sent{}, s.calls...)
}

func channel(app *gocommerce.App, name string) gocommerce.NotifierChannelInfo {
	for _, c := range app.Settings().NotifierChannels {
		if c.Channel == name {
			return c
		}
	}
	return gocommerce.NotifierChannelInfo{}
}

func configure(t *testing.T, app *gocommerce.App, settings map[string]any) {
	t.Helper()
	on := true
	if _, err := app.Plugins().Update(context.Background(), PluginKey, gocommerce.PluginPatch{
		Enabled: &on, Settings: settings,
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
}

// Installed idle it texts nothing and says SMS does not deliver; with the
// three settings typed in, the next message goes out with the wording the
// panel holds.
//
// Unlike MSG91 there is no per-event template id to register with a carrier:
// Twilio sends free text, so the words come from the engine's own SMS
// templates and an operator can change them without asking anybody.
func TestConfiguredFromThePanel(t *testing.T) {
	s := &stub{}
	server := gctest.StubHTTP(t, s.handler)
	app := gctest.New(t, New(Config{BaseURL: server.URL}))
	ctx := context.Background()

	if channel(app, gocommerce.ChannelSMS).Delivers {
		t.Fatal("with no account, SMS should not count as delivering")
	}
	if err := app.Notify(ctx, gocommerce.Notification{
		Event: gocommerce.EventOrderShipped, Channel: gocommerce.ChannelSMS, To: "+15551234567",
		Data: map[string]string{"order_number": "GC-42", "customer_name": "Asha"},
	}); err != nil {
		t.Fatal(err)
	}
	if n := len(s.all()); n != 0 {
		t.Fatalf("texts sent before configuring = %d, want 0", n)
	}

	configure(t, app, map[string]any{
		"account_sid": "AC123", "auth_token": "secret", "from": "+15550000000",
	})
	if !channel(app, gocommerce.ChannelSMS).Delivers {
		t.Fatal("configured from the panel, SMS should deliver")
	}

	if err := app.Notify(ctx, gocommerce.Notification{
		Event: gocommerce.EventOrderShipped, Channel: gocommerce.ChannelSMS, To: "+15551234567",
		Data: map[string]string{"order_number": "GC-42", "customer_name": "Asha"},
	}); err != nil {
		t.Fatal(err)
	}

	calls := s.all()
	if len(calls) != 1 {
		t.Fatalf("texts = %d, want 1", len(calls))
	}
	got := calls[0]
	if got.User != "AC123" || got.Pass != "secret" {
		t.Errorf("basic auth = %q/%q, want the sid and the token", got.User, got.Pass)
	}
	if want := "/2010-04-01/Accounts/AC123/Messages.json"; got.Path != want {
		t.Errorf("path = %q, want %q", got.Path, want)
	}
	if got.To != "+15551234567" || got.From != "+15550000000" {
		t.Errorf("to = %q, from = %q", got.To, got.From)
	}
	if !strings.Contains(got.Body, "GC-42") {
		t.Errorf("body = %q, want the order number in it", got.Body)
	}
}

// A messaging service is the other way Twilio addresses a message, and it
// replaces the from number rather than joining it — sending both is an error
// on Twilio's side.
func TestAMessagingServiceReplacesTheFromNumber(t *testing.T) {
	s := &stub{}
	server := gctest.StubHTTP(t, s.handler)
	app := gctest.New(t, New(Config{BaseURL: server.URL}))
	configure(t, app, map[string]any{
		"account_sid": "AC123", "auth_token": "secret",
		"from": "+15550000000", "messaging_service_sid": "MG999",
	})

	if err := app.Notify(context.Background(), gocommerce.Notification{
		Event: gocommerce.EventOrderShipped, Channel: gocommerce.ChannelSMS, To: "+15551234567",
		Data: map[string]string{"order_number": "GC-42"},
	}); err != nil {
		t.Fatal(err)
	}
	calls := s.all()
	if len(calls) != 1 {
		t.Fatalf("texts = %d", len(calls))
	}
	if calls[0].ServiceSid != "MG999" {
		t.Errorf("messaging service = %q, want MG999", calls[0].ServiceSid)
	}
	if calls[0].From != "" {
		t.Errorf("from = %q, want it left off when a messaging service is set", calls[0].From)
	}
}

// A number Twilio refuses is not retried twelve times: it settles, and the
// reason is in the log rather than in a stack trace.
func TestARefusedNumberSettlesRatherThanRetrying(t *testing.T) {
	s := &stub{
		status: http.StatusBadRequest,
		body:   `{"code":21211,"message":"The 'To' number is not a valid phone number."}`,
	}
	server := gctest.StubHTTP(t, s.handler)
	app := gctest.New(t, New(Config{BaseURL: server.URL}))
	configure(t, app, map[string]any{
		"account_sid": "AC123", "auth_token": "secret", "from": "+15550000000",
	})

	if err := app.Notify(context.Background(), gocommerce.Notification{
		Event: gocommerce.EventOrderShipped, Channel: gocommerce.ChannelSMS, To: "nonsense",
		Data: map[string]string{"order_number": "GC-42"},
	}); err != nil {
		t.Fatalf("a refused number should settle, not error: %v", err)
	}
	if n := len(s.all()); n != 1 {
		t.Fatalf("attempts = %d, want the one that was refused", n)
	}
}

// Twilio being down is a different thing from Twilio saying no, and only one
// of them is worth trying again.
func TestAnOutageIsRetried(t *testing.T) {
	s := &stub{status: http.StatusServiceUnavailable, body: `{"message":"service unavailable"}`}
	server := gctest.StubHTTP(t, s.handler)
	app := gctest.New(t, New(Config{BaseURL: server.URL}))
	configure(t, app, map[string]any{
		"account_sid": "AC123", "auth_token": "secret", "from": "+15550000000",
	})

	err := app.Notify(context.Background(), gocommerce.Notification{
		Event: gocommerce.EventOrderShipped, Channel: gocommerce.ChannelSMS, To: "+15551234567",
		Data: map[string]string{"order_number": "GC-42"},
	})
	if err == nil {
		t.Fatal("a 503 should be reported so the outbox tries again")
	}
}

// What the operator types under Setup SMS is what goes out.
func TestPanelWordingWins(t *testing.T) {
	s := &stub{}
	server := gctest.StubHTTP(t, s.handler)
	app := gctest.New(t, New(Config{BaseURL: server.URL}))
	ctx := context.Background()
	configure(t, app, map[string]any{
		"account_sid": "AC123", "auth_token": "secret", "from": "+15550000000",
	})
	if _, err := app.NotifyTemplates().Set(ctx, gocommerce.ChannelSMS, gocommerce.EventOrderShipped,
		"", "Your parcel {{.order_number}} is on its way"); err != nil {
		t.Fatalf("reword: %v", err)
	}

	if err := app.Notify(ctx, gocommerce.Notification{
		Event: gocommerce.EventOrderShipped, Channel: gocommerce.ChannelSMS, To: "+15551234567",
		Data: map[string]string{"order_number": "GC-42"},
	}); err != nil {
		t.Fatal(err)
	}
	calls := s.all()
	if len(calls) != 1 {
		t.Fatalf("texts = %d", len(calls))
	}
	if want := "Your parcel GC-42 is on its way"; calls[0].Body != want {
		t.Errorf("body = %q, want %q", calls[0].Body, want)
	}
}

// The body is form-encoded, which is what Twilio's REST API takes — a JSON
// body is accepted with an empty message and no error anyone would notice.
func TestTheRequestIsFormEncoded(t *testing.T) {
	var contentType string
	var raw string
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		buf := make([]byte, 2048)
		n, _ := r.Body.Read(buf)
		raw = string(buf[:n])
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sid":"SM1","status":"queued"}`))
	})
	app := gctest.New(t, New(Config{BaseURL: server.URL}))
	configure(t, app, map[string]any{
		"account_sid": "AC123", "auth_token": "secret", "from": "+15550000000",
	})
	if err := app.Notify(context.Background(), gocommerce.Notification{
		Event: gocommerce.EventOrderShipped, Channel: gocommerce.ChannelSMS, To: "+15551234567",
		Data: map[string]string{"order_number": "GC-42"},
	}); err != nil {
		t.Fatal(err)
	}
	if contentType != "application/x-www-form-urlencoded" {
		t.Errorf("content type = %q", contentType)
	}
	if _, err := url.ParseQuery(raw); err != nil {
		t.Errorf("body is not form-encoded: %v (%q)", err, raw)
	}
}
