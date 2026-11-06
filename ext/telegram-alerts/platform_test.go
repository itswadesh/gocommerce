package telegramalerts

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

func newPlatformApp(t *testing.T, chat string) (*gocommerce.App, *fakeTelegram) {
	t.Helper()
	fake := &fakeTelegram{}
	srv := gctest.StubHTTP(t, fake.handler)
	mod := New(Config{BaseURL: srv.URL, PlatformBotToken: testToken, PlatformChatID: chat})
	return gctest.NewWithConfig(t, gocommerce.Config{BaseDomain: "shops.example.com"}, mod), fake
}

func openStore(t *testing.T, app *gocommerce.App) context.Context {
	t.Helper()
	st, err := app.Stores().Create(context.Background(), "Asha Jewels", "asha-jewels")
	if err != nil {
		t.Fatalf("open a store: %v", err)
	}
	return gocommerce.WithStore(context.Background(), st.ID)
}

// The install's owner hears of a new store, without the store having switched
// anything on: the chat is the install's.
func TestANewStoreReachesThePlatformChat(t *testing.T) {
	app, fake := newPlatformApp(t, "777")
	ctx := openStore(t, app)
	if _, err := app.DrainOutbox(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	sent := fake.messages()
	if len(sent) != 1 {
		t.Fatalf("messages = %d, want 1", len(sent))
	}
	if sent[0].ChatID != "777" || sent[0].Token != testToken {
		t.Errorf("sent with token %q to chat %q, want the platform's", sent[0].Token, sent[0].ChatID)
	}
	for _, want := range []string{"Asha Jewels", "https://asha-jewels.shops.example.com"} {
		if !strings.Contains(sent[0].Text, want) {
			t.Errorf("message %q lacks %q", sent[0].Text, want)
		}
	}
}

// With no platform chat set, opening a store says nothing to anybody.
func TestANewStoreIsSilentWithoutAPlatformChat(t *testing.T) {
	app, _, fake := newApp(t, gocommerce.Config{})
	ctx := openStore(t, app)
	if _, err := app.DrainOutbox(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if sent := fake.messages(); len(sent) != 0 {
		t.Fatalf("messages = %v, want none", sent)
	}
}

// Telegram being down is worth another try, which the outbox gives; Telegram
// saying no is not, and must not sit in the store's Events screen as a failure.
func TestANewStoreIsRetriedOnlyWhenTelegramMayAnswerLater(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		wantPending int
	}{
		{"unavailable", http.StatusBadGateway, 1},
		{"refused", http.StatusBadRequest, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, fake := newPlatformApp(t, "777")
			fake.answer(tc.status, `{"ok":false,"description":"no"}`)
			ctx := openStore(t, app)
			if _, err := app.DrainOutbox(ctx); err != nil {
				t.Fatalf("drain: %v", err)
			}
			pending, dead, err := app.PendingEvents(ctx)
			if err != nil {
				t.Fatalf("pending: %v", err)
			}
			if pending != tc.wantPending || dead != 0 {
				t.Fatalf("pending = %d, dead = %d; want %d pending, 0 dead", pending, dead, tc.wantPending)
			}
		})
	}
}

type storeWebhook struct {
	mu     sync.Mutex
	status int
	posts  []map[string]string
}

func (h *storeWebhook) handler(w http.ResponseWriter, r *http.Request) {
	var got map[string]string
	_ = json.NewDecoder(r.Body).Decode(&got)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.posts = append(h.posts, got)
	if h.status != 0 {
		w.WriteHeader(h.status)
	}
}

func newWebhookApp(t *testing.T, onlyOn string, hook *storeWebhook) *gocommerce.App {
	t.Helper()
	srv := gctest.StubHTTP(t, hook.handler)
	mod := New(Config{BaseURL: srv.URL, PlatformWebhookURL: srv.URL + "/hooks/secret-path", PlatformWebhookBaseDomain: onlyOn})
	return gctest.NewWithConfig(t, gocommerce.Config{BaseDomain: "shops.example.com"}, mod)
}

// The automation reads these keys by name.
func TestANewStoreReachesThePlatformWebhook(t *testing.T) {
	hook := &storeWebhook{}
	app := newWebhookApp(t, "shops.example.com", hook)
	ctx := openStore(t, app)
	if _, err := app.DrainOutbox(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(hook.posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(hook.posts))
	}
	got := hook.posts[0]
	if got["store_name"] != "Asha Jewels" || got["domain"] != "asha-jewels.shops.example.com" {
		t.Errorf("posted %v", got)
	}
	for _, key := range []string{"business_name", "user_first_name", "user_last_name", "user_email", "user_phone", "address", "country"} {
		if _, ok := got[key]; !ok {
			t.Errorf("posted %v without %q, which the automation reads", got, key)
		}
	}
}

func TestAnotherBrandsStoreDoesNotReachThePlatformWebhook(t *testing.T) {
	hook := &storeWebhook{}
	app := newWebhookApp(t, "shopnx.in", hook)
	ctx := openStore(t, app)
	if _, err := app.DrainOutbox(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(hook.posts) != 0 {
		t.Fatalf("posts = %v, want none", hook.posts)
	}
}

// Down is worth another try and a refusal is not, as with Telegram. The address
// carries the secret, so no failure may repeat it on a store's Events screen.
func TestAPlatformWebhookFailureIsRetriedAndKeepsItsAddressSecret(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		wantPending int
	}{
		{"unavailable", http.StatusBadGateway, 1},
		{"refused", http.StatusGone, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hook := &storeWebhook{status: tc.status}
			app := newWebhookApp(t, "", hook)
			ctx := openStore(t, app)
			if _, err := app.DrainOutbox(ctx); err != nil {
				t.Fatalf("drain: %v", err)
			}
			pending, dead, err := app.PendingEvents(ctx)
			if err != nil {
				t.Fatalf("pending: %v", err)
			}
			if pending != tc.wantPending || dead != 0 {
				t.Fatalf("pending = %d, dead = %d; want %d pending, 0 dead", pending, dead, tc.wantPending)
			}
		})
	}

	m := &Module{cfg: Config{PlatformWebhookURL: "http://127.0.0.1:1/hooks/secret-path"}}
	err := m.postStoreWebhook(context.Background(), "store_x", []byte("{}"))
	if err == nil || strings.Contains(err.Error(), "secret-path") {
		t.Fatalf("error = %v, want one that does not quote the address", err)
	}
}
