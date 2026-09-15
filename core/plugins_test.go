package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A plugin is a switch and its settings, held by the engine and read by the
// storefront or by the module that owns it. These tests are the registry,
// the state, the masking, and the two doors.

// fakePlugin is a module that registers a plugin with a secret, the shape
// an integration has.
type fakePlugin struct{}

func (fakePlugin) Name() string            { return "fake-mail" }
func (fakePlugin) Migrations() []Migration { return nil }
func (fakePlugin) Register(app *App) error {
	app.RegisterPlugin(PluginDef{
		Key: "fake-mail", Title: "Fake Mail", Category: "marketing",
		Fields: []PluginField{
			{Key: "api_key", Label: "API key", Kind: "secret", Required: true},
			{Key: "public_key", Label: "Public key", Kind: "text", Public: true},
			{Key: "list_id", Label: "List", Kind: "select", Options: []string{"news", "vip"}, Public: true},
		},
	})
	return nil
}

func TestPluginsAreListedWithTheirState(t *testing.T) {
	app := newTestApp(t, fakePlugin{})
	ctx := context.Background()

	list, err := app.Plugins().List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byKey := map[string]*Plugin{}
	for _, p := range list {
		byKey[p.Key] = p
	}
	if hb := byKey["hello-bar"]; hb == nil || !hb.Builtin || hb.Enabled || hb.Configured {
		t.Errorf("hello-bar = %+v, want a built-in, off, and not configured until it has a message", hb)
	}
	if gc := byKey["guest-checkout"]; gc == nil || !gc.Enabled {
		t.Errorf("guest-checkout = %+v, want on by default", gc)
	}
	if fm := byKey["fake-mail"]; fm == nil || fm.Builtin || fm.Module != "fake-mail" {
		t.Errorf("fake-mail = %+v, want the module's own plugin", fm)
	}
	// Built-ins come first, in the order core lists them.
	if list[0].Key != "hello-bar" || list[len(list)-1].Key != "fake-mail" {
		t.Errorf("order = %s … %s", list[0].Key, list[len(list)-1].Key)
	}
}

func TestAPluginIsSwitchedOnWithItsSettings(t *testing.T) {
	app := newTestApp(t, fakePlugin{})
	ctx := context.Background()
	on := true

	p, err := app.Plugins().Update(ctx, "hello-bar", PluginPatch{Enabled: &on, Settings: map[string]any{
		"message": " Free shipping over 50 ", "link": "https://shop.example/sale",
	}})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !p.Enabled || !p.Configured || p.Settings["message"] != "Free shipping over 50" || p.Settings["background"] != "#111111" {
		t.Errorf("hello-bar = %+v, want on, configured, trimmed, with the default colour filled in", p)
	}
	// A later patch merges: the link stays when only the message changes.
	p, err = app.Plugins().Update(ctx, "hello-bar", PluginPatch{Settings: map[string]any{"message": "Sale on"}})
	if err != nil {
		t.Fatalf("second update: %v", err)
	}
	if p.Settings["link"] != "https://shop.example/sale" || !p.Enabled {
		t.Errorf("after the second patch = %+v, want the link and the switch kept", p)
	}
	// Refusals name the field.
	if _, err := app.Plugins().Update(ctx, "hello-bar", PluginPatch{Settings: map[string]any{"colour": "red"}}); err == nil || !strings.Contains(err.Error(), "colour") {
		t.Errorf("unknown setting = %v", err)
	}
	if _, err := app.Plugins().Update(ctx, "hello-bar", PluginPatch{Settings: map[string]any{"link": "shop.example"}}); err == nil || !strings.Contains(err.Error(), "Link") {
		t.Errorf("bad url = %v", err)
	}
	if _, err := app.Plugins().Update(ctx, "fake-mail", PluginPatch{Settings: map[string]any{"list_id": "spam"}}); err == nil || !strings.Contains(err.Error(), "one of") {
		t.Errorf("bad select = %v", err)
	}
	if _, err := app.Plugins().Update(ctx, "nope", PluginPatch{Enabled: &on}); err == nil {
		t.Errorf("unknown plugin was accepted")
	}
	// The trail says what happened.
	feed, _, err := app.Audit().OfEntity(ctx, AuditEntityPlugin, "hello-bar", 50, 0)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if len(feed) < 2 || feed[len(feed)-1].Summary != "Enabled Hello bar" {
		t.Errorf("audit = %+v, want the enabling recorded first", feed)
	}
}

func TestASecretIsMaskedAndKeptThroughTheMask(t *testing.T) {
	app := newTestApp(t, fakePlugin{})
	ctx := context.Background()
	on := true
	p, err := app.Plugins().Update(ctx, "fake-mail", PluginPatch{Enabled: &on, Settings: map[string]any{"api_key": "sk-secret-1234", "public_key": "pk-1"}})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if p.Settings["api_key"] != SecretMask || !p.Configured {
		t.Errorf("read back = %+v, want the key masked and the plugin configured", p.Settings)
	}
	// The module reads the real value; a form that saves the mask back
	// changes nothing.
	if got := app.Plugins().String(ctx, "fake-mail", "api_key"); got != "sk-secret-1234" {
		t.Errorf("module read %q", got)
	}
	if _, err := app.Plugins().Update(ctx, "fake-mail", PluginPatch{Settings: map[string]any{"api_key": SecretMask, "public_key": "pk-2"}}); err != nil {
		t.Fatalf("save the mask: %v", err)
	}
	if got := app.Plugins().String(ctx, "fake-mail", "api_key"); got != "sk-secret-1234" {
		t.Errorf("the mask overwrote the key: %q", got)
	}
	// An empty value clears it, and the plugin is no longer configured.
	if p, err := app.Plugins().Update(ctx, "fake-mail", PluginPatch{Settings: map[string]any{"api_key": ""}}); err != nil || p.Configured {
		t.Errorf("clearing = %+v, %v", p, err)
	}
	enabled, _ := app.Plugins().Enabled(ctx, "fake-mail")
	if !enabled {
		t.Errorf("clearing a setting switched the plugin off")
	}
}

func TestTheStorefrontSeesOnlyWhatIsOnAndPublic(t *testing.T) {
	app := newTestApp(t, fakePlugin{})
	ctx := context.Background()
	on := true
	if _, err := app.Plugins().Update(ctx, "fake-mail", PluginPatch{Enabled: &on, Settings: map[string]any{"api_key": "sk", "public_key": "pk", "list_id": "vip"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Plugins().Update(ctx, "whatsapp-chat", PluginPatch{Settings: map[string]any{"phone": "919876543210"}}); err != nil {
		t.Fatal(err)
	}
	rec := do(t, app, http.MethodGet, "/api/plugins")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/plugins = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Data []PluginPublic `json:"data"`
	}
	decodeJSONBody(t, rec.Body.Bytes(), &body)
	keys := map[string]PluginPublic{}
	for _, p := range body.Data {
		keys[p.Key] = p
	}
	if _, ok := keys["whatsapp-chat"]; ok {
		t.Errorf("a plugin that is off reached the storefront")
	}
	if _, ok := keys["guest-checkout"]; !ok {
		t.Errorf("a plugin on by default did not reach the storefront")
	}
	fm := keys["fake-mail"]
	if fm.Settings["public_key"] != "pk" || fm.Settings["list_id"] != "vip" {
		t.Errorf("public settings = %v", fm.Settings)
	}
	if _, leaked := fm.Settings["api_key"]; leaked {
		t.Errorf("the secret reached the storefront")
	}

	// The admin doors: gated, and the list masks the secret too.
	if denied := do(t, app, http.MethodGet, "/api/admin/plugins"); denied.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d", denied.Code)
	}
	list := do(t, app, http.MethodGet, "/api/admin/plugins", withAdmin)
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), `"sk"`) || !strings.Contains(list.Body.String(), SecretMask) {
		t.Errorf("admin list = %d, secret masked = %v", list.Code, strings.Contains(list.Body.String(), SecretMask))
	}
	off := false
	patched := do(t, app, http.MethodPatch, "/api/admin/plugins/fake-mail", withAdmin, jsonBody(t, PluginPatch{Enabled: &off}))
	if patched.Code != http.StatusOK || strings.Contains(patched.Body.String(), `"enabled":true`) {
		t.Errorf("PATCH = %d: %s", patched.Code, patched.Body)
	}
}
