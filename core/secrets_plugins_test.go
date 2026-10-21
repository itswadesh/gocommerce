package gocommerce

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/itswadesh/gocommerce/internal/testdb"
)

// Sealing as a store sees it: a plugin's secret goes into the database sealed
// and comes out of every reader as it always did — plain to the module that
// owns it, masked to the panel.

// keyedDSN is a database of its own, cloned from the template newTestApp uses
// for these modules, for a test that boots more than one App on it.
func keyedDSN(t *testing.T, mods ...Module) string {
	t.Helper()
	return testdb.Clone(t, "core:"+moduleKey(mods), func(dsn string) error {
		app, err := New(testConfig(dsn), mods...)
		if err != nil {
			return err
		}
		return app.Close()
	})
}

// keyedApp is newTestApp with the install's keys set.
func keyedApp(t *testing.T, dsn, current, previous string, mods ...Module) *App {
	t.Helper()
	cfg := testConfig(dsn)
	cfg.SecretsKey, cfg.SecretsKeyPrevious = current, previous
	app, err := New(cfg, mods...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app
}

// storedPluginSettings is the row as the database holds it, read past every
// service.
func storedPluginSettings(t *testing.T, app *App, key string) map[string]any {
	t.Helper()
	return storedPluginSettingsIn(t, context.Background(), app, key)
}

// storedPluginSettingsIn is storedPluginSettings in the store ctx is bound to.
func storedPluginSettingsIn(t *testing.T, ctx context.Context, app *App, key string) map[string]any {
	t.Helper()
	var raw []byte
	if err := app.db.QueryRowContext(ctx,
		`SELECT settings FROM plugins WHERE key = $1`, key).Scan(&raw); err != nil {
		t.Fatalf("read the plugin row: %v", err)
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAPluginSecretIsStoredSealed(t *testing.T) {
	app := keyedApp(t, keyedDSN(t, fakePlugin{}), newSecretsKey(t), "", fakePlugin{})
	ctx := context.Background()
	on := true

	p, err := app.Plugins().Update(ctx, "fake-mail", PluginPatch{Enabled: &on, Settings: map[string]any{
		"api_key": "sk-secret-1234", "public_key": "pk-1",
	}})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if p.Settings["api_key"] != SecretMask || !p.Configured {
		t.Errorf("update read back %+v, want the key masked and the plugin configured", p.Settings)
	}

	raw := storedPluginSettings(t, app, "fake-mail")
	stored, _ := raw["api_key"].(string)
	if !strings.HasPrefix(stored, "sealed:v1:") || strings.Contains(stored, "sk-secret-1234") {
		t.Errorf("stored api_key = %q, want it sealed", stored)
	}
	if raw["public_key"] != "pk-1" {
		t.Errorf("stored public_key = %v, want a setting that is not a secret left readable", raw["public_key"])
	}

	// Every reader sees what it saw before.
	got, err := app.Plugins().Get(ctx, "fake-mail")
	if err != nil || got.Settings["api_key"] != SecretMask || !got.Configured {
		t.Errorf("get = %+v, %v; want masked and configured", got, err)
	}
	settings, err := app.Plugins().Settings(ctx, "fake-mail")
	if err != nil || settings["api_key"] != "sk-secret-1234" {
		t.Errorf("settings = %v, %v; want the key in plain text for its module", settings, err)
	}
	if s := app.Plugins().String(ctx, "fake-mail", "api_key"); s != "sk-secret-1234" {
		t.Errorf("string = %q", s)
	}
	var cfg struct {
		APIKey string `plugin:"api_key"`
	}
	if err := app.Plugins().Fill(ctx, "fake-mail", &cfg); err != nil || cfg.APIKey != "sk-secret-1234" {
		t.Errorf("fill = %+v, %v", cfg, err)
	}
	list, err := app.Plugins().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, pl := range list {
		if pl.Key == "fake-mail" && (pl.Settings["api_key"] != SecretMask || !pl.Configured) {
			t.Errorf("list = %+v, want masked and configured", pl)
		}
	}

	// The mask still means "as it was", and a save re-seals rather than
	// writing the key out in the clear.
	if _, err := app.Plugins().Update(ctx, "fake-mail", PluginPatch{Settings: map[string]any{
		"api_key": SecretMask, "public_key": "pk-2",
	}}); err != nil {
		t.Fatalf("save the mask: %v", err)
	}
	if s := app.Plugins().String(ctx, "fake-mail", "api_key"); s != "sk-secret-1234" {
		t.Errorf("after saving the mask the key reads %q", s)
	}
	if again, _ := storedPluginSettings(t, app, "fake-mail")["api_key"].(string); !strings.HasPrefix(again, "sealed:v1:") {
		t.Errorf("after saving the mask the key is stored as %q", again)
	}
}

// With no key the row holds exactly what it held before sealing existed.
func TestWithoutAKeyAPluginSecretIsStoredAsBefore(t *testing.T) {
	app := newTestApp(t, fakePlugin{})
	ctx := context.Background()
	on := true
	if _, err := app.Plugins().Update(ctx, "fake-mail", PluginPatch{Enabled: &on, Settings: map[string]any{
		"api_key": "sk-secret-1234",
	}}); err != nil {
		t.Fatal(err)
	}
	if got := storedPluginSettings(t, app, "fake-mail")["api_key"]; got != "sk-secret-1234" {
		t.Errorf("stored without a key = %v, want the value as typed", got)
	}
	if s := app.Plugins().String(ctx, "fake-mail", "api_key"); s != "sk-secret-1234" {
		t.Errorf("read = %q", s)
	}
}

// A value sealed under a key the install no longer has reads as unset, so the
// plugin says it is not configured. It is kept as it is until somebody types
// a new one, so putting the right key back still opens it.
func TestAPluginSecretUnderAnUnknownKeyReadsAsUnset(t *testing.T) {
	dsn := keyedDSN(t, fakePlugin{})
	oldKey := newSecretsKey(t)
	ctx := context.Background()
	on := true
	first := keyedApp(t, dsn, oldKey, "", fakePlugin{})
	if _, err := first.Plugins().Update(ctx, "fake-mail", PluginPatch{Enabled: &on, Settings: map[string]any{
		"api_key": "sk-secret-1234", "public_key": "pk-1",
	}}); err != nil {
		t.Fatal(err)
	}
	sealed := storedPluginSettings(t, first, "fake-mail")["api_key"]

	lost := keyedApp(t, dsn, newSecretsKey(t), "", fakePlugin{})
	p, err := lost.Plugins().Get(ctx, "fake-mail")
	if err != nil {
		t.Fatal(err)
	}
	if p.Configured {
		t.Error("a plugin whose key cannot be opened says it is configured")
	}
	if _, masked := p.Settings["api_key"]; masked {
		t.Errorf("an unreadable key reads as %v, want it absent like any unset field", p.Settings["api_key"])
	}
	if s := lost.Plugins().String(ctx, "fake-mail", "api_key"); s != "" {
		t.Errorf("the module read %q, want unset", s)
	}
	for _, pl := range mustList(t, lost) {
		if pl.Key == "fake-mail" && pl.Configured {
			t.Error("the list says the plugin is configured")
		}
	}

	// A save that does not type a new key leaves the sealed one where it was —
	// the panel's form sends an empty field for it, and so does this.
	if _, err := lost.Plugins().Update(ctx, "fake-mail", PluginPatch{Settings: map[string]any{"public_key": "pk-2"}}); err != nil {
		t.Fatalf("save another setting: %v", err)
	}
	off := false
	if _, err := lost.Plugins().Update(ctx, "fake-mail", PluginPatch{Enabled: &off, Settings: map[string]any{"api_key": ""}}); err != nil {
		t.Fatalf("switch off: %v", err)
	}
	if got := storedPluginSettings(t, lost, "fake-mail")["api_key"]; got != sealed {
		t.Errorf("the unreadable key was replaced by %v", got)
	}
	back := keyedApp(t, dsn, oldKey, "", fakePlugin{})
	if s := back.Plugins().String(ctx, "fake-mail", "api_key"); s != "sk-secret-1234" {
		t.Errorf("with the right key back the module reads %q", s)
	}

	// Typing a new one replaces it, sealed under the key the install has.
	if _, err := lost.Plugins().Update(ctx, "fake-mail", PluginPatch{Settings: map[string]any{"api_key": "sk-new"}}); err != nil {
		t.Fatal(err)
	}
	if s := lost.Plugins().String(ctx, "fake-mail", "api_key"); s != "sk-new" {
		t.Errorf("after a new key the module reads %q", s)
	}
}

func mustList(t *testing.T, app *App) []*Plugin {
	t.Helper()
	list, err := app.Plugins().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// fakePluginLater is fake-mail as a later release might describe it: the key
// field dropped, or still there as plain text.
type fakePluginLater struct{ keyAsText bool }

func (fakePluginLater) Name() string            { return "fake-mail" }
func (fakePluginLater) Migrations() []Migration { return nil }
func (m fakePluginLater) Register(app *App) error {
	fields := []PluginField{{Key: "public_key", Label: "Public key", Kind: "text", Public: true}}
	if m.keyAsText {
		fields = append(fields, PluginField{Key: "api_key", Label: "API key", Kind: "text"})
	}
	app.RegisterPlugin(PluginDef{Key: "fake-mail", Title: "Fake Mail", Category: "marketing", Fields: fields})
	return nil
}

// A value that arrived sealed leaves sealed, whatever the descriptor says
// about its field now: a release that drops or renames a secret field must
// not have the next save write the credential out in the clear.
func TestASealedValueStaysSealedWhenItsFieldIsNoLongerASecret(t *testing.T) {
	dsn := keyedDSN(t, fakePlugin{})
	key := newSecretsKey(t)
	ctx := context.Background()
	on := true
	if _, err := keyedApp(t, dsn, key, "", fakePlugin{}).Plugins().Update(ctx, "fake-mail", PluginPatch{
		Enabled: &on, Settings: map[string]any{"api_key": "sk-secret-1234", "public_key": "pk-1"},
	}); err != nil {
		t.Fatal(err)
	}

	for name, mod := range map[string]fakePluginLater{"dropped": {}, "now text": {keyAsText: true}} {
		app := keyedApp(t, dsn, key, "", mod)
		if _, err := app.Plugins().Update(ctx, "fake-mail", PluginPatch{Settings: map[string]any{"public_key": "pk-" + name}}); err != nil {
			t.Fatalf("%s: save: %v", name, err)
		}
		stored, _ := storedPluginSettings(t, app, "fake-mail")["api_key"].(string)
		if !strings.HasPrefix(stored, "sealed:v1:") {
			t.Errorf("%s: after a save the key is stored as %q, want it still sealed", name, stored)
		}
	}
	// And it still opens for the descriptor that has it as a secret.
	if s := keyedApp(t, dsn, key, "", fakePlugin{}).Plugins().String(ctx, "fake-mail", "api_key"); s != "sk-secret-1234" {
		t.Errorf("the key reads %q after the saves", s)
	}
}

// No real credential starts with "sealed:", and a value that does would read
// as one the engine sealed — then fail to open and vanish. So it is refused
// at the door, in any text field, with or without a key.
func TestASettingThatLooksSealedIsRefused(t *testing.T) {
	app := newTestApp(t, fakePlugin{})
	ctx := context.Background()
	on := true
	for field, v := range map[string]string{"api_key": "sealed:v1:hunter2-TOPSECRET", "public_key": "sealed:pk"} {
		_, err := app.Plugins().Update(ctx, "fake-mail", PluginPatch{Enabled: &on, Settings: map[string]any{"api_key": "sk-1", field: v}})
		if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "sealed:") {
			t.Errorf("%s = %q: %v, want a validation error naming the prefix", field, v, err)
		}
		if strings.Contains(fmt.Sprint(err), "hunter2") {
			t.Errorf("%s: the error repeats the value", field)
		}
	}
}
