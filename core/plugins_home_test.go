package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A built-in feature is part of the product and set up on its own screen; a
// plugin is an add-on, listed on the Plugins screen (D132). Both are the same
// row underneath, so these tests are about what the descriptor says and what
// the one feature with no switch does.

// fakeAlwaysOn is a module whose feature has no switch, the shape banners is.
type fakeAlwaysOn struct{}

func (fakeAlwaysOn) Name() string            { return "fake-pictures" }
func (fakeAlwaysOn) Migrations() []Migration { return nil }
func (fakeAlwaysOn) Register(app *App) error {
	app.RegisterPlugin(PluginDef{
		Key: "fake-pictures", Title: "Fake pictures", Icon: "ri-image-line", Category: "storefront",
		DefaultEnabled: true, AlwaysOn: true, Home: "/dash/themes/fake",
	})
	return nil
}

func TestCoreSaysWhereEachBuiltInFeatureIsSetUp(t *testing.T) {
	app := newTestApp(t)
	homes := map[string]string{}
	for _, def := range app.Plugins().Defs() {
		homes[def.Key] = def.Home
	}
	want := map[string]string{
		// Storefront extras, under Themes.
		"hello-bar":      "/dash/themes/extras",
		"news-ticker":    "/dash/themes/extras",
		"trust-badges":   "/dash/themes/extras",
		"social-sharing": "/dash/themes/extras",
		// The store's own rules, on its settings page.
		"store-close":    "/dash/settings",
		"enquiry-mode":   "/dash/settings",
		"guest-checkout": "/dash/settings",
		CODPluginKey:     "/dash/settings/payments",
		// Add-ons stay on the Plugins screen.
		"recent-purchase-popup": "",
		"whatsapp-chat":         "",
		"header-scripts":        "",
		"google-analytics":      "",
		"meta-pixel":            "",
		"tidio":                 "",
		"tawk-to":               "",
		"umami":                 "",
		"plausible":             "",
		"trustpilot":            "",
	}
	for key, home := range want {
		got, ok := homes[key]
		if !ok {
			t.Errorf("%s is not a built-in any more", key)
			continue
		}
		if got != home {
			t.Errorf("%s is set up at %q, want %q", key, got, home)
		}
	}
	for key := range homes {
		if _, ok := want[key]; !ok {
			t.Errorf("built-in %s is not classified here: give it a Home, or list it as a plugin", key)
		}
	}
}

func TestAHomeIsAPanelPath(t *testing.T) {
	p := newPlugins(&App{})
	err := p.register(PluginDef{Key: "x-feature", Title: "X", Icon: "ri-plug-line", Home: "https://example.com"})
	if err == nil || !strings.Contains(err.Error(), "/dash/") {
		t.Errorf("a home outside the panel = %v, want refused", err)
	}
	if err := p.register(PluginDef{Key: "y-feature", Title: "Y", Icon: "ri-plug-line", AlwaysOn: true}); err == nil {
		t.Errorf("a feature with no switch and no screen of its own was accepted")
	}
}

// A feature with no switch is on, and stays on: the storefront keeps reading
// it as enabled and a patch cannot turn it off. A store whose row says it was
// switched off before the switch went keeps it off, and can turn it on once.
func TestAFeatureWithNoSwitchStaysOn(t *testing.T) {
	app := newTestApp(t, fakeAlwaysOn{})
	ctx := context.Background()

	if on, _ := app.Plugins().Enabled(ctx, "fake-pictures"); !on {
		t.Fatalf("a feature with no switch reads as off")
	}
	off := false
	if _, err := app.Plugins().Update(ctx, "fake-pictures", PluginPatch{Enabled: &off}); err == nil || !strings.Contains(err.Error(), "always on") {
		t.Errorf("switching it off = %v, want refused", err)
	}
	rec := do(t, app, http.MethodPatch, "/api/admin/plugins/fake-pictures", withAdmin, jsonBody(t, PluginPatch{Enabled: &off}))
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("PATCH off = %d: %s", rec.Code, rec.Body)
	}
	if on, _ := app.Plugins().Enabled(ctx, "fake-pictures"); !on {
		t.Errorf("the refused patch switched it off")
	}

	// A row written off before the switch was taken away.
	if _, err := app.db.ExecContext(ctx, `INSERT INTO plugins (key, enabled, settings) VALUES ('fake-pictures', false, '{}')
		ON CONFLICT (store_id, key) DO UPDATE SET enabled = false`); err != nil {
		t.Fatal(err)
	}
	if on, _ := app.Plugins().Enabled(ctx, "fake-pictures"); on {
		t.Errorf("a store that had it off now has it on; its storefront would change")
	}
	on := true
	p, err := app.Plugins().Update(ctx, "fake-pictures", PluginPatch{Enabled: &on})
	if err != nil || !p.Enabled {
		t.Fatalf("switching it back on = %+v, %v", p, err)
	}
	if _, err := app.Plugins().Update(ctx, "fake-pictures", PluginPatch{Enabled: &off}); err == nil {
		t.Errorf("once on, it could be switched off again")
	}
}
