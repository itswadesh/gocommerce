package twilio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// On the real module: a store that sets its own Twilio account in the panel still
// gets no operator's code on it, where the install hosts other stores (D210).
func TestAStoresOwnAccountCarriesNoOperatorsCode(t *testing.T) {
	s := &stub{}
	srv := gctest.StubHTTP(t, s.handler)
	mod := New(Config{AccountSID: "ACinstall", AuthToken: "install-token", From: "+15550000001", BaseURL: srv.URL, Client: srv.Client()})
	app := gctest.New(t, mod)
	ctx := context.Background()

	call := func(method, target, token, store string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, target, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		if store != "" {
			req.Header.Set("X-Store", store)
		}
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		return rec
	}

	const pass = "a-long-enough-password"
	st, err := app.Stores().Create(ctx, "Their shop", "their-shop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Superusers().Create(gocommerce.WithStore(ctx, st.ID), "asker@example.com", pass, gocommerce.RoleOwner); err != nil {
		t.Fatal(err)
	}
	_, sess, err := app.Superusers().Authenticate(ctx, "asker@example.com", pass, "203.0.113.1")
	if err != nil {
		t.Fatal(err)
	}
	r := call(http.MethodPatch, "/api/admin/plugins/"+PluginKey, sess.Token, st.ID, map[string]any{"enabled": true,
		"settings": map[string]any{"account_sid": "ACstore", "auth_token": "store-token", "from": "+15550000002"}})
	if r.Code != http.StatusOK {
		t.Fatalf("plugin = %d: %s", r.Code, r.Body)
	}
	if r := call(http.MethodPost, "/api/admin/me/phone/code", sess.Token, st.ID, map[string]string{"phone": "+919876555555"}); r.Code != http.StatusAccepted {
		t.Fatalf("ask = %d: %s", r.Code, r.Body)
	}
	for i := 0; i < 200; i++ {
		s.mu.Lock()
		n := len(s.calls)
		s.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) != 1 || s.calls[0].AccountSid != "ACinstall" {
		t.Fatalf("calls = %+v, want one on the install's account", s.calls)
	}
}
