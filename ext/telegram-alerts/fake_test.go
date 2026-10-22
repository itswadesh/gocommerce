package telegramalerts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// testToken has the shape @BotFather hands out; it is the example from
// Telegram's own documentation, not a bot anybody owns.
const testToken = "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11"

// message is one sendMessage the fake Telegram received.
type message struct {
	Token  string
	ChatID string
	Text   string
	// NoPreview is whether link previews were switched off, so an order link
	// does not unfurl the panel's sign-in page into the owner's chat.
	NoPreview bool
}

// fakeTelegram answers the two Bot API methods the module calls. Nothing
// here talks to the real api.telegram.org.
type fakeTelegram struct {
	mu   sync.Mutex
	sent []message
	// status and body replace sendMessage's answer when set.
	status int
	body   string
	// updates is getUpdates' result array.
	updates string
	methods []string
}

func (f *fakeTelegram) handler(w http.ResponseWriter, r *http.Request) {
	// /bot<token>/<method>
	path := strings.TrimPrefix(r.URL.Path, "/bot")
	token, method, _ := strings.Cut(path, "/")
	raw, _ := io.ReadAll(r.Body)

	f.mu.Lock()
	f.methods = append(f.methods, method)
	status, body, updates := f.status, f.body, f.updates
	if method == "sendMessage" {
		var in struct {
			ChatID  any    `json:"chat_id"`
			Text    string `json:"text"`
			Preview *struct {
				Disabled bool `json:"is_disabled"`
			} `json:"link_preview_options"`
		}
		_ = json.Unmarshal(raw, &in)
		chat, _ := in.ChatID.(string)
		f.sent = append(f.sent, message{Token: token, ChatID: chat, Text: in.Text,
			NoPreview: in.Preview != nil && in.Preview.Disabled})
	}
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch {
	case method == "sendMessage" && status != 0:
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	case method == "sendMessage":
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	case method == "getUpdates" && status != 0:
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	case method == "getUpdates":
		if updates == "" {
			updates = "[]"
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":` + updates + `}`))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":404,"description":"Not Found"}`))
	}
}

func (f *fakeTelegram) messages() []message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]message(nil), f.sent...)
}

func (f *fakeTelegram) answer(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

// newApp boots a store with the module pointed at a fake Telegram.
func newApp(t *testing.T, cfg gocommerce.Config) (*gocommerce.App, *Module, *fakeTelegram) {
	t.Helper()
	fake := &fakeTelegram{}
	srv := gctest.StubHTTP(t, fake.handler)
	mod := New(Config{BaseURL: srv.URL})
	return gctest.NewWithConfig(t, cfg, mod), mod, fake
}

// setUp is what the owner does on Setup Telegram: the token, the chat, and
// the switch.
func setUp(t *testing.T, app *gocommerce.App) {
	t.Helper()
	on := true
	if _, err := app.Plugins().Update(context.Background(), PluginKey, gocommerce.PluginPatch{
		Enabled: &on, Settings: map[string]any{"bot_token": testToken, "chat_id": "424242"},
	}); err != nil {
		t.Fatalf("set up the bot: %v", err)
	}
}

func deliverOnce(t *testing.T, m *Module) int {
	t.Helper()
	n, err := m.DeliverPass(context.Background())
	if err != nil {
		t.Fatalf("deliver pass: %v", err)
	}
	return n
}

func deliveries(t *testing.T, app *gocommerce.App) []Delivery {
	t.Helper()
	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/telegram-alerts/deliveries", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list deliveries = %d %s", rec.Code, rec.Body)
	}
	var out []Delivery
	gctest.DecodeData(t, rec, &out)
	return out
}
