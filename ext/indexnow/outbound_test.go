package indexnow

import (
	"context"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// The storefront URL is a store setting the module fetches the key file from,
// so it is refused as it is saved when it is not public, and the module's own
// client refuses to fetch it if it got in some other way.
func TestTheStorefrontURLMustBePublic(t *testing.T) {
	m := New()
	app := gctest.New(t, m)
	ctx := context.Background()

	on := true
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{
		Enabled: &on, Settings: map[string]any{"storefront_url": "http://localhost:5173"},
	}); err == nil {
		t.Error("a loopback storefront URL was saved")
	}

	if _, err := app.DB().ExecContext(ctx, `
		INSERT INTO plugins (key, enabled, settings) VALUES ($1, true, '{"storefront_url":"http://127.0.0.1:5173","key":"abcdefgh12"}')
		ON CONFLICT (store_id, key) DO UPDATE SET enabled = true, settings = EXCLUDED.settings`, pluginKey); err != nil {
		t.Fatalf("write the setting: %v", err)
	}
	res, err := m.Check(ctx)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if res.OK || !strings.Contains(res.Problem, "127.0.0.1 is not a public address") {
		t.Errorf("check = %+v, want the refusal as its problem", res)
	}
}
