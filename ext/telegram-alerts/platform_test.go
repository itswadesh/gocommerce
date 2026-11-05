package telegramalerts

import (
	"context"
	"net/http"
	"strings"
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
