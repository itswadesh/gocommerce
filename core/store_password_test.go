package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A closed store with a password lets in whoever types it, and the password
// itself reaches nobody: not the plugin list, not the store's public face.
func TestStorePasswordAdmitsTheRightOneAndNeverLeaks(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	const secret = "launch-day-7731"
	try := func(body string) (int, StorePass) {
		t.Helper()
		rec := doBody(t, app, http.MethodPost, "/api/store/password", body)
		var pass StorePass
		if rec.Code == http.StatusOK {
			decodeData(t, rec, &pass)
		}
		return rec.Code, pass
	}

	// Open, there is nothing to unlock.
	if code, _ := try(`{"password":"` + secret + `"}`); code != http.StatusNotFound {
		t.Errorf("an open store answered %d, want 404", code)
	}

	on := true
	if _, err := app.Plugins().Update(ctx, storeCloseKey, PluginPatch{Enabled: &on,
		Settings: map[string]any{"message": "Launching soon", "password": secret}}); err != nil {
		t.Fatalf("close the store: %v", err)
	}

	if code, _ := try(`{"password":"launch-day-7732"}`); code != http.StatusUnprocessableEntity {
		t.Errorf("a wrong password answered %d, want 422", code)
	}
	code, pass := try(`{"password":"` + secret + `"}`)
	if code != http.StatusOK || pass.Token == "" || !pass.ExpiresAt.After(time.Now()) {
		t.Fatalf("the right password = %d %+v, want a pass", code, pass)
	}
	if code, again := try(`{"token":"` + pass.Token + `"}`); code != http.StatusOK || again.Token != pass.Token {
		t.Errorf("the pass checked = %d %+v, want it good", code, again)
	}
	forged := pass.Token[:len(pass.Token)-2] + "AA"
	if code, _ := try(`{"token":"` + forged + `"}`); code != http.StatusUnprocessableEntity {
		t.Errorf("a forged pass answered %d, want 422", code)
	}

	// The storefront hears the message and that there is a password; the
	// password is in no public answer.
	plugins := do(t, app, http.MethodGet, "/api/plugins")
	store := do(t, app, http.MethodGet, "/api/store")
	for name, rec := range map[string]string{"GET /api/plugins": plugins.Body.String(), "GET /api/store": store.Body.String()} {
		if strings.Contains(rec, secret) {
			t.Errorf("%s carries the password: %s", name, rec)
		}
	}
	var list []PluginPublic
	decodeData(t, plugins, &list)
	found := false
	for _, p := range list {
		if p.Key == storeCloseKey {
			found = true
			if p.Settings["password"] != true || p.Settings["message"] != "Launching soon" {
				t.Errorf("store-close settings = %v, want the message and password: true", p.Settings)
			}
		}
	}
	if !found {
		t.Errorf("store-close is not in GET /api/plugins: %s", plugins.Body)
	}

	// A new password ends every pass handed out under the old one.
	if _, err := app.Plugins().Update(ctx, storeCloseKey, PluginPatch{Settings: map[string]any{"password": "another-one"}}); err != nil {
		t.Fatalf("change the password: %v", err)
	}
	if code, _ := try(`{"token":"` + pass.Token + `"}`); code != http.StatusUnprocessableEntity {
		t.Errorf("an old pass after a new password answered %d, want 422", code)
	}
}

// Every password try counts, so the route is not a free oracle.
func TestStorePasswordTriesAreLimited(t *testing.T) {
	app := newTestApp(t)
	on := true
	if _, err := app.Plugins().Update(context.Background(), storeCloseKey, PluginPatch{Enabled: &on,
		Settings: map[string]any{"message": "Launching soon", "password": "right"}}); err != nil {
		t.Fatalf("close the store: %v", err)
	}
	body, _ := json.Marshal(map[string]string{"password": "wrong"})
	for i := 0; i < storePasswordTriesPerWindow; i++ {
		doBody(t, app, http.MethodPost, "/api/store/password", string(body))
	}
	right := doBody(t, app, http.MethodPost, "/api/store/password", `{"password":"right"}`)
	if right.Code != http.StatusTooManyRequests {
		t.Errorf("try %d answered %d, want 429 even for the right password", storePasswordTriesPerWindow+1, right.Code)
	}
}
