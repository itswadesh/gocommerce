package meilisearch

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// The install's own Meilisearch is usually on its own network, and whoever
// runs the server put it there (MEILI_HOST), so that address is called as
// given. A host a store types on the Plugins screen is held to the public
// rule: refused as it is saved, and refused where it is used if it got in some
// other way.
func TestTheInstallsHostIsTrustedAndAStoresIsNot(t *testing.T) {
	installs, stores := newFake(), newFake()
	installSrv := gctest.StubHTTP(t, installs.handler)
	storeSrv := gctest.StubHTTP(t, stores.handler)

	m := New(Config{Host: installSrv.URL, APIKey: "master", Poll: -1})
	app := gctest.New(t, m)
	ctx := context.Background()
	gctest.CreateProduct(t, app, "MS-OUT-1", 1000, 1)

	if _, _, err := m.Sync(ctx, true); err != nil {
		t.Fatalf("sync to the install's own host: %v", err)
	}
	if installs.created != 1 {
		t.Fatalf("the install's host saw %d index creations, want 1", installs.created)
	}

	on := true
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{
		Enabled: &on, Settings: map[string]any{"host": storeSrv.URL, "api_key": "store-key"},
	}); err == nil {
		t.Fatal("a store saved a loopback Meilisearch host")
	}

	// As if saved by a release before this rule, or while the install allowed it.
	settings, _ := json.Marshal(map[string]any{"host": storeSrv.URL, "api_key": "store-key"})
	if _, err := app.DB().ExecContext(ctx, `
		INSERT INTO plugins (key, enabled, settings) VALUES ($1, true, $2::jsonb)
		ON CONFLICT (store_id, key) DO UPDATE SET enabled = true, settings = EXCLUDED.settings`,
		pluginKey, string(settings)); err != nil {
		t.Fatalf("write the store's host: %v", err)
	}
	if _, _, err := m.Sync(ctx, true); err == nil || !strings.Contains(err.Error(), "is not a public address") {
		t.Fatalf("sync to the store's loopback host = %v, want the refusal", err)
	}
	if n := len(stores.keys); n != 0 {
		t.Errorf("the store's private host was reached %d times", n)
	}
}

// The install's keys belong to the install's Meilisearch. A store that points
// the module at a host of its own gets only the keys it typed: with none, the
// module stays unconfigured and calls nothing, rather than sending the
// install's master key to the store's server.
func TestAStoresHostNeverGetsTheInstallsKeys(t *testing.T) {
	installs, stores := newFake(), newFake()
	installSrv := gctest.StubHTTP(t, installs.handler)
	storeSrv := gctest.StubHTTP(t, stores.handler)

	m := New(Config{Host: installSrv.URL, APIKey: "install-master", SearchKey: "install-search", Poll: -1})
	app := gctest.NewWithConfig(t, gocommerce.Config{AllowPrivateURLs: true}, m)
	ctx := context.Background()
	gctest.CreateProduct(t, app, "MS-KEY-1", 1000, 1)

	on := true
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{
		Enabled: &on, Settings: map[string]any{"host": storeSrv.URL},
	}); err != nil {
		t.Fatalf("point the store at its own host: %v", err)
	}
	if _, _, err := m.Sync(ctx, true); err == nil {
		t.Error("a sync to the store's host ran with no key of the store's own")
	}
	gctest.Request(t, app, http.MethodGet, "/x/meilisearch/search?q=widget", nil)
	for _, k := range stores.keys {
		if strings.Contains(k, "install-") {
			t.Errorf("the store's host was sent the install's key: %q", k)
		}
	}

	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{
		Settings: map[string]any{"api_key": "store-master"},
	}); err != nil {
		t.Fatalf("give the store its own key: %v", err)
	}
	if _, _, err := m.Sync(ctx, true); err != nil {
		t.Fatalf("sync with the store's own key: %v", err)
	}
	if len(stores.keys) == 0 {
		t.Fatal("the store's host was never called")
	}
	for _, k := range stores.keys {
		if k != "Bearer store-master" {
			t.Errorf("the store's host got key %q, want only the store's own", k)
		}
	}
	if len(installs.keys) != 0 {
		t.Errorf("the install's host was called %d times for a store with its own", len(installs.keys))
	}
}
