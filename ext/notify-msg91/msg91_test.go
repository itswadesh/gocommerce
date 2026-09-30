package msg91

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// flow is one call the stub MSG91 received.
type flow struct {
	AuthKey    string
	TemplateID string              `json:"template_id"`
	Recipients []map[string]string `json:"recipients"`
}

type stub struct {
	mu    sync.Mutex
	calls []flow
}

func (s *stub) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var f flow
	_ = json.Unmarshal(body, &f)
	f.AuthKey = r.Header.Get("authkey")
	s.mu.Lock()
	s.calls = append(s.calls, f)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"type":"success","message":"queued"}`))
}

func (s *stub) all() []flow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]flow{}, s.calls...)
}

// TestConfiguredFromThePanel: installed idle, the module texts nothing and
// the settings say SMS does not deliver; with the key and one template id
// typed into its plugin, the next message goes out as that template with
// the event's data as its variables, to a number MSG91 will accept. An event
// with no template id is skipped, not failed.
func TestConfiguredFromThePanel(t *testing.T) {
	s := &stub{}
	server := gctest.StubHTTP(t, s.handler)
	app := gctest.New(t, New(Config{BaseURL: server.URL}))
	ctx := context.Background()

	text := func(event string) {
		t.Helper()
		if err := app.Notify(ctx, gocommerce.Notification{
			Event: event, Channel: gocommerce.ChannelSMS, To: "98765 43210",
			Data: map[string]string{"order_number": "GC-42", "customer_name": "Asha"},
		}); err != nil {
			t.Fatalf("notify %s: %v", event, err)
		}
	}

	if sms := channel(app, gocommerce.ChannelSMS); sms.Delivers || len(sms.Backends) != 2 || sms.Backends[1].Name != "MSG91" {
		t.Fatalf("sms channel = %+v, want the log and an idle MSG91", sms)
	}
	text(gocommerce.EventOrderCreated)
	if n := len(s.all()); n != 0 {
		t.Fatalf("texts before configuration = %d, want 0", n)
	}
	rows, _, err := app.Notifications().List(ctx, gocommerce.NotificationQuery{Channel: gocommerce.ChannelSMS})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != gocommerce.NotificationLogged {
		t.Fatalf("log = %+v, want one row that only reached the log", rows)
	}

	on := true
	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{
		Enabled: &on, Settings: map[string]any{"auth_key": "k-panel", "template_order_placed": "tpl-placed", "country_code": "91"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if !channel(app, gocommerce.ChannelSMS).Delivers {
		t.Fatal("configured from the panel, SMS should deliver")
	}

	text(gocommerce.EventOrderCreated)
	calls := s.all()
	if len(calls) != 1 {
		t.Fatalf("texts after configuration = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.AuthKey != "k-panel" || call.TemplateID != "tpl-placed" {
		t.Errorf("call = %+v, want the panel's key and template", call)
	}
	if len(call.Recipients) != 1 {
		t.Fatalf("recipients = %+v, want one", call.Recipients)
	}
	r := call.Recipients[0]
	if r["mobiles"] != "919876543210" {
		t.Errorf("mobiles = %q, want the digits with the default country code", r["mobiles"])
	}
	if r["order_number"] != "GC-42" || r["customer_name"] != "Asha" {
		t.Errorf("variables = %+v, want the event's data", r)
	}
	rows, _, _ = app.Notifications().List(ctx, gocommerce.NotificationQuery{Channel: gocommerce.ChannelSMS})
	if len(rows) != 2 || rows[0].Status != gocommerce.NotificationSent || rows[0].Backend != "notify-msg91" {
		t.Fatalf("log = %+v, want the newest row sent through MSG91", rows)
	}

	// Shipping has no template id, so it is not sent — and not a failure.
	text(gocommerce.EventOrderShipped)
	if n := len(s.all()); n != 1 {
		t.Fatalf("texts after an event with no template = %d, want still 1", n)
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
