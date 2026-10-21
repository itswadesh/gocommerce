package shopify

import (
	"context"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// The shop domain is whatever the operator typed, so the importer's client
// connects only to a public address.
func TestAShopDomainMustBePublic(t *testing.T) {
	m := New()
	app := gctest.New(t, m)
	ctx := context.Background()
	on := true
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{
		Enabled: &on, Settings: map[string]any{"shop": "127.0.0.1", "access_token": "shpat_x"},
	}); err != nil {
		t.Fatalf("settings: %v", err)
	}
	client, err := m.settings(ctx)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if _, err := client.Verify(ctx); err == nil || !strings.Contains(err.Error(), "127.0.0.1 is not a public address") {
		t.Fatalf("verify against a loopback shop = %v, want the refusal", err)
	}
}
