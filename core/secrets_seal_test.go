package gocommerce

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
)

// `gocommerce secrets seal` and the doctor's view of the same thing: what is
// still readable, what an old key sealed, and what nothing here can open.

func countFor(t *testing.T, counts []SecretCount, label string) SecretCount {
	t.Helper()
	for _, c := range counts {
		if c.Label == label {
			return c
		}
	}
	t.Fatalf("no %q in %+v", label, counts)
	return SecretCount{}
}

// sealedUnder reports whether a stored value is sealed under the key with
// this id.
func sealedUnder(stored any, app *App) bool {
	s, _ := stored.(string)
	return strings.HasPrefix(s, "sealed:v1:"+app.secrets.current.id+":")
}

// The whole of a rotation, from an install that never had a key to one whose
// every secret is under the new key: readable values are sealed, the previous
// key's are moved, every store is reached, and a second run changes nothing.
func TestSecretsSealMovesEverythingUnderTheCurrentKey(t *testing.T) {
	dsn := keyedDSN(t, fakePlugin{})
	ctx := context.Background()
	on := true

	// Before any key: a plugin secret and an API key's copy, readable.
	bare := keyedApp(t, dsn, "", "", fakePlugin{})
	if _, err := bare.Plugins().Update(ctx, "fake-mail", PluginPatch{Enabled: &on, Settings: map[string]any{"api_key": "sk-first"}}); err != nil {
		t.Fatal(err)
	}
	plainKey, plainPresented, err := bare.APIKeys().Create(ctx, APIKeyInput{Name: "Plain", Role: RoleStaff}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A second store, so the walk has to bind each one: a value sealed for
	// the wrong store would not open afterwards.
	second := WithStore(ctx, addTestStore(t, bare, "second"))
	if _, err := bare.Plugins().Update(second, "fake-mail", PluginPatch{Enabled: &on, Settings: map[string]any{"api_key": "sk-second"}}); err != nil {
		t.Fatal(err)
	}

	// Under the first key: another API key, sealed as it is made.
	oldKey := newSecretsKey(t)
	early := keyedApp(t, dsn, oldKey, "", fakePlugin{})
	oldSealed, oldPresented, err := early.APIKeys().Create(ctx, APIKeyInput{Name: "Early", Role: RoleStaff}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Rotated: the new key, with the old one beside it.
	newKey := newSecretsKey(t)
	rotating := keyedApp(t, dsn, newKey, oldKey, fakePlugin{})
	rep, err := rotating.SealSecrets(ctx)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if rep.Stores != 2 || rep.KeyID != rotating.secrets.current.id {
		t.Errorf("report = %+v, want both stores under the current key", rep)
	}
	if c := countFor(t, rep.Counts, "plugin settings"); c.Readable != 2 || c.Previous != 0 || c.Unreadable != 0 {
		t.Errorf("plugin settings = %+v, want the two readable keys sealed", c)
	}
	if c := countFor(t, rep.Counts, "API keys"); c.Readable != 1 || c.Previous != 1 || c.Unreadable != 0 {
		t.Errorf("API keys = %+v, want one sealed and one moved off the previous key", c)
	}

	for name, st := range map[string]context.Context{"first": ctx, "second": second} {
		if raw := storedPluginSettingsIn(t, st, rotating, "fake-mail"); !sealedUnder(raw["api_key"], rotating) {
			t.Errorf("the %s store's plugin key is stored as %v", name, raw["api_key"])
		}
	}
	for _, id := range []int64{plainKey.ID, oldSealed.ID} {
		if s := storedAPIKeySecret(t, rotating, id); !sealedUnder(s, rotating) {
			t.Errorf("API key %d is stored as %q, want it under the new key", id, s)
		}
	}

	// Idempotent: everything is already where it belongs.
	again, err := rotating.SealSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range again.Counts {
		if c.Readable != 0 || c.Previous != 0 || c.Unreadable != 0 {
			t.Errorf("second run %+v, want nothing left to do", c)
		}
	}
	if c := countFor(t, again.Counts, "API keys"); c.Current != 2 {
		t.Errorf("second run API keys = %+v, want both counted as already sealed", c)
	}

	// The old key can go now: the new one alone opens everything, in each
	// store.
	settled := keyedApp(t, dsn, newKey, "", fakePlugin{})
	if s := settled.Plugins().String(ctx, "fake-mail", "api_key"); s != "sk-first" {
		t.Errorf("first store's plugin key reads %q", s)
	}
	if s := settled.Plugins().String(second, "fake-mail", "api_key"); s != "sk-second" {
		t.Errorf("second store's plugin key reads %q", s)
	}
	for id, want := range map[int64]string{plainKey.ID: plainPresented, oldSealed.ID: oldPresented} {
		if got, err := settled.APIKeys().Secret(ctx, id); err != nil || got != want {
			t.Errorf("API key %d reads %v, want the key as issued", id, err)
		}
	}
}

// A value no key here opens is counted, named, and left exactly as it is.
func TestSecretsSealLeavesWhatItCannotOpen(t *testing.T) {
	dsn := keyedDSN(t, fakePlugin{})
	ctx := context.Background()
	on := true
	lostKey := keyedApp(t, dsn, newSecretsKey(t), "", fakePlugin{})
	if _, err := lostKey.Plugins().Update(ctx, "fake-mail", PluginPatch{Enabled: &on, Settings: map[string]any{"api_key": "sk-lost"}}); err != nil {
		t.Fatal(err)
	}
	before := storedPluginSettings(t, lostKey, "fake-mail")["api_key"]

	app := keyedApp(t, dsn, newSecretsKey(t), "", fakePlugin{})
	rep, err := app.SealSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := countFor(t, rep.Counts, "plugin settings")
	if c.Unreadable != 1 || len(c.Where) != 1 || c.Where[0] != "plugins/fake-mail/api_key" {
		t.Errorf("plugin settings = %+v, want the one it cannot open named", c)
	}
	if after := storedPluginSettings(t, app, "fake-mail")["api_key"]; after != before {
		t.Error("the value it could not open was changed")
	}
}

func TestSecretsSealNeedsAKey(t *testing.T) {
	app := newTestApp(t)
	if _, err := app.SealSecrets(context.Background()); err == nil || !strings.Contains(err.Error(), "GOCOMMERCE_SECRETS_KEY") {
		t.Errorf("seal with no key = %v, want it refused naming the variable", err)
	}
}

// A module's column is named once, and an identifier that is not a plain
// lowercase name is refused at registration rather than spliced into SQL.
func TestASecretColumnMustBeAPlainName(t *testing.T) {
	_, err := New(testConfig(keyedDSN(t)), &secretColumnModule{table: "x; DROP TABLE orders"})
	if err == nil || !strings.Contains(err.Error(), "secret column") {
		t.Errorf("New = %v, want the registration refused", err)
	}
}

type secretColumnModule struct{ table string }

func (m *secretColumnModule) Name() string            { return "secretcols" }
func (m *secretColumnModule) Migrations() []Migration { return nil }
func (m *secretColumnModule) Register(app *App) error {
	app.RegisterSecretColumn(SecretColumn{Table: m.table, Column: "token", Field: "secretcols/token", Label: "tokens"})
	return nil
}

// The doctor says whether this store's secrets are protected, and what to do
// when they are not.
func TestDoctorReportsHowSecretsAreStored(t *testing.T) {
	dsn := keyedDSN(t, fakePlugin{})
	ctx := context.Background()
	on := true
	check := func(app *App) Diagnostic { return diagnostic(t, app.Diagnose(ctx), "secrets") }

	bare := keyedApp(t, dsn, "", "", fakePlugin{})
	if d := check(bare); d.Status != StatusOK {
		t.Errorf("nothing stored, no key = %+v, want ok", d)
	}
	if _, err := bare.Plugins().Update(ctx, "fake-mail", PluginPatch{Enabled: &on, Settings: map[string]any{"api_key": "sk-1"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bare.APIKeys().Create(ctx, APIKeyInput{Name: "K", Role: RoleStaff}, nil); err != nil {
		t.Fatal(err)
	}
	// The engine seals at start-up, so the next step is a restart, not a
	// terminal: a hosted install's image has no shell to run a command in.
	if d := check(bare); d.Status != StatusWarn || !strings.Contains(d.Detail, "2 secret") ||
		!strings.Contains(d.Detail+d.Hint, "GOCOMMERCE_SECRETS_KEY") || !strings.Contains(d.Hint, "when it starts") {
		t.Errorf("readable secrets, no key = %+v, want a warning with the count naming the variable and saying the engine seals at start-up", d)
	}

	oldKey := newSecretsKey(t)
	keyed := keyedApp(t, dsn, oldKey, "", fakePlugin{})
	if d := check(keyed); d.Status != StatusWarn || !strings.Contains(d.Hint, "restart") {
		t.Errorf("a key and readable secrets = %+v, want a warning to restart the engine", d)
	}
	if _, err := keyed.SealSecrets(ctx); err != nil {
		t.Fatal(err)
	}
	if d := check(keyed); d.Status != StatusOK || !strings.Contains(d.Detail, keyed.secrets.current.id) {
		t.Errorf("everything sealed = %+v, want ok naming the key", d)
	}

	rotating := keyedApp(t, dsn, newSecretsKey(t), oldKey, fakePlugin{})
	if d := check(rotating); d.Status != StatusOK || !strings.Contains(d.Detail, "previous key") ||
		!strings.Contains(d.Hint, "restart") || !strings.Contains(d.Hint, "unset") {
		t.Errorf("values under the previous key = %+v, want a note to restart, then unset the previous key", d)
	}

	lost := keyedApp(t, dsn, newSecretsKey(t), "", fakePlugin{})
	if d := check(lost); d.Status != StatusWarn || !strings.Contains(d.Detail, "plugins/fake-mail/api_key") || !strings.Contains(d.Hint, "restart") {
		t.Errorf("values under an unknown key = %+v, want a warning naming where they are and to restart with the old key", d)
	}
	if d := check(bare); d.Status != StatusWarn || !strings.Contains(d.Detail, "sealed") {
		t.Errorf("sealed values and no key = %+v, want a warning", d)
	}
}

// superuserDSN is dsn signed in as the cluster's superuser, which PostgreSQL
// lets past every row policy — the login a misconfigured install might use.
func superuserDSN(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	base, err := url.Parse(os.Getenv(testDSNEnv))
	if err != nil {
		t.Fatal(err)
	}
	u.User = base.User
	return u.String()
}

// Under a login that passes the row policies, a pass bound to one store sees
// every store's rows. Each statement names its store as well, so a pass still
// touches only its own; and the command refuses such a login outright, as
// serving several stores does.
func TestSecretsSealKeepsEachStoreToItsOwnRows(t *testing.T) {
	dsn := keyedDSN(t, fakePlugin{})
	ctx := context.Background()
	on := true

	bare := keyedApp(t, dsn, "", "", fakePlugin{})
	first := WithStore(ctx, bare.DefaultStore())
	second := WithStore(ctx, addTestStore(t, bare, "second"))
	presented := map[context.Context]string{}
	for st, key := range map[context.Context]string{first: "sk-first", second: "sk-second"} {
		if _, err := bare.Plugins().Update(st, "fake-mail", PluginPatch{Enabled: &on, Settings: map[string]any{"api_key": key}}); err != nil {
			t.Fatal(err)
		}
		_, p, err := bare.APIKeys().Create(st, APIKeyInput{Name: "K", Role: RoleStaff}, nil)
		if err != nil {
			t.Fatal(err)
		}
		presented[st] = p
	}

	key := newSecretsKey(t)
	super := keyedApp(t, superuserDSN(t, dsn), key, "", fakePlugin{})
	if _, err := super.SealSecrets(ctx); err == nil || !strings.Contains(err.Error(), "row") {
		t.Errorf("seal under a login that passes row security = %v, want it refused saying why", err)
	}
	for _, st := range []context.Context{first, second} {
		if _, err := super.surveySecrets(st, true); err != nil {
			t.Fatalf("seal pass: %v", err)
		}
	}

	normal := keyedApp(t, dsn, key, "", fakePlugin{})
	for name, st := range map[string]context.Context{"first": first, "second": second} {
		want := "sk-" + name
		if got := normal.Plugins().String(st, "fake-mail", "api_key"); got != want {
			t.Errorf("the %s store's plugin key reads %q, want %q", name, got, want)
		}
		keys, err := normal.APIKeys().List(st)
		if err != nil || len(keys) != 1 {
			t.Fatalf("the %s store's keys = %v, %v", name, keys, err)
		}
		if got, err := normal.APIKeys().Secret(st, keys[0].ID); err != nil || got != presented[st] {
			t.Errorf("the %s store's API key copy reads back %v, want the one it issued", name, err)
		}
	}
}
