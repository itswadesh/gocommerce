package gocommerce

import (
	"context"
	"testing"
)

// The ad and analytics tags are a switch and an ID each. Off, or with no ID,
// the storefront is told nothing and so loads nothing; on, it is told the ID
// and nothing else.
func TestEachAdTagShipsDarkUntilItHasItsID(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	tags := map[string]string{
		"microsoft-clarity": "project_id",
		"hotjar":            "site_id",
		"tiktok-pixel":      "pixel_id",
		"pinterest-tag":     "tag_id",
		"snapchat-pixel":    "pixel_id",
	}
	public := func() map[string]PluginPublic {
		list, err := app.Plugins().Public(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]PluginPublic{}
		for _, p := range list {
			out[p.Key] = p
		}
		return out
	}
	on := true
	for key, field := range tags {
		if _, ok := public()[key]; ok {
			t.Errorf("%s reached the storefront before it was switched on", key)
		}
		if _, err := app.Plugins().Update(ctx, key, PluginPatch{Enabled: &on}); err == nil {
			t.Errorf("%s was switched on without its %s", key, field)
		}
		if _, ok := public()[key]; ok {
			t.Errorf("%s reached the storefront with no %s", key, field)
		}
		if _, err := app.Plugins().Update(ctx, key, PluginPatch{Enabled: &on, Settings: map[string]any{field: "ID-1"}}); err != nil {
			t.Fatalf("%s on with its %s: %v", key, field, err)
		}
		got, ok := public()[key]
		if !ok || len(got.Settings) != 1 || got.Settings[field] != "ID-1" {
			t.Errorf("%s on = %+v, want exactly {%s: ID-1}", key, got, field)
		}
	}
}
