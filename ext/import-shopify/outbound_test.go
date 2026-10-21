package shopify

import (
	"context"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// The shop domain is whatever the operator typed. It is refused as it is
// saved when it is not public, and the importer's client connects only to a
// public address if one got in some other way.
func TestAShopDomainMustBePublic(t *testing.T) {
	m := New()
	app := gctest.New(t, m)
	ctx := context.Background()

	for _, bad := range []string{"127.0.0.1", "localhost", "10.0.0.5", "https://127.0.0.1/admin"} {
		if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{
			Settings: map[string]any{"shop": bad},
		}); err == nil || !strings.Contains(err.Error(), "public") {
			t.Errorf("shop %q: %v, want it refused", bad, err)
		}
	}
	for _, good := range []string{"acme", "acme.myshopify.com", "https://acme.myshopify.com/admin"} {
		if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{
			Settings: map[string]any{"shop": good},
		}); err != nil {
			t.Errorf("shop %q was refused: %v", good, err)
		}
	}

	// As if saved by a release before this rule.
	if _, err := app.DB().ExecContext(ctx, `
		INSERT INTO plugins (key, enabled, settings) VALUES ($1, true, '{"shop":"127.0.0.1","access_token":"shpat_x"}')
		ON CONFLICT (store_id, key) DO UPDATE SET enabled = true, settings = EXCLUDED.settings`, pluginKey); err != nil {
		t.Fatalf("write the setting: %v", err)
	}
	client, err := m.settings(ctx)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if _, err := client.Verify(ctx); err == nil || !strings.Contains(err.Error(), "127.0.0.1 is not a public address") {
		t.Fatalf("verify against a loopback shop = %v, want the refusal", err)
	}
}
