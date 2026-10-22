package gocommerce

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// The engine seals what is stored when it starts with a key, so an owner on
// a hosted install — whose image has no shell to run `secrets seal` in —
// only sets the key and redeploys.

// syncBuffer is a log the background work and the test can share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startedApp boots and starts an engine on dsn with these keys, and waits for
// the start-up seal to finish.
func startedApp(t *testing.T, dsn, current, previous string, logs *syncBuffer, mods ...Module) *App {
	t.Helper()
	cfg := testConfig(dsn)
	cfg.SecretsKey, cfg.SecretsKeyPrevious = current, previous
	if logs != nil {
		cfg.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	app, err := New(cfg, mods...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	app.startSeal.Wait()
	return app
}

// storedSecrets is every plugin secret and API key copy in the store ctx is
// bound to, as the database holds them.
func storedSecrets(t *testing.T, ctx context.Context, app *App) []string {
	t.Helper()
	var out []string
	if s, _ := storedPluginSettingsIn(t, ctx, app, "fake-mail")["api_key"].(string); s != "" {
		out = append(out, s)
	}
	rows, err := app.db.QueryContext(ctx, `SELECT secret FROM api_keys ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestStartingWithAKeySealsEveryStore(t *testing.T) {
	dsn := keyedDSN(t, fakePlugin{})
	ctx := context.Background()
	on := true

	// Readable values in two stores, as an install has them before its key.
	bare := keyedApp(t, dsn, "", "", fakePlugin{})
	first := WithStore(ctx, bare.DefaultStore())
	second := WithStore(ctx, addTestStore(t, bare, "second"))
	presented := map[string]string{}
	for name, st := range map[string]context.Context{"first": first, "second": second} {
		if _, err := bare.Plugins().Update(st, "fake-mail", PluginPatch{Enabled: &on, Settings: map[string]any{"api_key": "sk-" + name}}); err != nil {
			t.Fatal(err)
		}
		_, p, err := bare.APIKeys().Create(st, APIKeyInput{Name: name, Role: RoleStaff}, nil)
		if err != nil {
			t.Fatal(err)
		}
		presented[name] = p
	}
	// And one under an older key, which a rotation moves.
	oldKey := newSecretsKey(t)
	_, early, err := keyedApp(t, dsn, oldKey, "", fakePlugin{}).APIKeys().Create(first, APIKeyInput{Name: "early", Role: RoleStaff}, nil)
	if err != nil {
		t.Fatal(err)
	}

	newKey := newSecretsKey(t)
	logs := &syncBuffer{}
	app := startedApp(t, dsn, newKey, oldKey, logs, fakePlugin{})

	for name, st := range map[string]context.Context{"first": first, "second": second} {
		for _, s := range storedSecrets(t, st, app) {
			if !sealedUnder(s, app) {
				t.Errorf("the %s store still stores %q after starting with a key", name, s)
			}
		}
		if got := app.Plugins().String(st, "fake-mail", "api_key"); got != "sk-"+name {
			t.Errorf("the %s store's plugin key reads %q", name, got)
		}
		keys, err := app.APIKeys().List(st)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range keys {
			got, err := app.APIKeys().Secret(st, k.ID)
			want := presented[k.Name]
			if k.Name == "early" {
				want = early
			}
			if err != nil || got != want {
				t.Errorf("the %s store's API key %q reads back %v", name, k.Name, err)
			}
		}
		if d := diagnostic(t, app.Diagnose(st), "secrets"); d.Status != StatusOK || !strings.Contains(d.Detail, "sealed under key "+app.secrets.current.id) {
			t.Errorf("the %s store's doctor after a start-up seal = %+v, want ok", name, d)
		}
	}

	out := logs.String()
	for _, id := range []string{bare.DefaultStore(), mustStoreID(t, second)} {
		if !strings.Contains(out, id) {
			t.Errorf("the start-up log does not name store %s:\n%s", id, out)
		}
	}
	for _, secret := range []string{"sk-first", "sk-second", presented["first"], presented["second"], early} {
		if strings.Contains(out, secret) {
			t.Error("the start-up log carries a secret")
		}
	}

	// A second start finds nothing to do and changes nothing.
	before := append(storedSecrets(t, first, app), storedSecrets(t, second, app)...)
	again := startedApp(t, dsn, newKey, "", nil, fakePlugin{})
	after := append(storedSecrets(t, first, again), storedSecrets(t, second, again)...)
	if strings.Join(before, "|") != strings.Join(after, "|") {
		t.Error("a second start rewrote values that were already sealed")
	}
}

func mustStoreID(t *testing.T, ctx context.Context) string {
	t.Helper()
	id, ok := StoreFrom(ctx)
	if !ok {
		t.Fatal("no store bound")
	}
	return id
}

// Several instances start at once on a hosted install. Two passes over the
// same values must seal each exactly once and lose none, and between them
// count each value once.
func TestTwoSealPassesAtOnceSealEachValueOnce(t *testing.T) {
	dsn := keyedDSN(t, fakePlugin{})
	ctx := context.Background()
	on := true
	bare := keyedApp(t, dsn, "", "", fakePlugin{})
	first := WithStore(ctx, bare.DefaultStore())
	second := WithStore(ctx, addTestStore(t, bare, "second"))
	want := map[string]bool{}
	for name, st := range map[string]context.Context{"first": first, "second": second} {
		if _, err := bare.Plugins().Update(st, "fake-mail", PluginPatch{Enabled: &on, Settings: map[string]any{"api_key": "sk-" + name}}); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 5; i++ {
			_, p, err := bare.APIKeys().Create(st, APIKeyInput{Name: name, Role: RoleStaff}, nil)
			if err != nil {
				t.Fatal(err)
			}
			want[p] = true
		}
	}

	key := newSecretsKey(t)
	apps := []*App{keyedApp(t, dsn, key, "", fakePlugin{}), keyedApp(t, dsn, key, "", fakePlugin{})}
	reports := make([]*SealReport, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range apps {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			reports[i], errs[i] = apps[i].sealEveryStore(ctx, nil)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}

	sealedNow := map[string]int{}
	for _, rep := range reports {
		for _, c := range rep.Counts {
			sealedNow[c.Label] += c.Readable
			if c.Unreadable != 0 || c.Previous != 0 {
				t.Errorf("%s: %+v, want nothing unreadable or under a previous key", c.Label, c)
			}
		}
	}
	if sealedNow["plugin settings"] != 2 || sealedNow["API keys"] != 10 {
		t.Errorf("between them the passes sealed %v, want each value counted once: 2 plugin settings, 10 API keys", sealedNow)
	}

	got := map[string]bool{}
	for _, st := range []context.Context{first, second} {
		keys, err := apps[0].APIKeys().List(st)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range keys {
			s, err := apps[0].APIKeys().Secret(st, k.ID)
			if err != nil {
				t.Fatalf("key %d does not open: %v", k.ID, err)
			}
			got[s] = true
		}
		for _, s := range storedSecrets(t, st, apps[0]) {
			// Sealed once: the envelope opens straight to the value, never to
			// another envelope.
			if !sealedUnder(s, apps[0]) {
				t.Errorf("stored as %q, want sealed", s)
			}
		}
		if s := apps[0].Plugins().String(st, "fake-mail", "api_key"); !strings.HasPrefix(s, "sk-") {
			t.Errorf("a plugin key opens to %q, want the key itself", s)
		}
	}
	for p := range want {
		if !got[p] {
			t.Error("an API key copy was lost or no longer opens to the key it was")
		}
	}
}

// Under a login that passes the row policies the CLI refuses to seal, and so
// does start-up: one warning that says why, and nothing touched.
func TestStartingUnderALoginThatPassesRowSecuritySkipsSealing(t *testing.T) {
	dsn := keyedDSN(t, fakePlugin{})
	ctx := context.Background()
	on := true
	bare := keyedApp(t, dsn, "", "", fakePlugin{})
	if _, err := bare.Plugins().Update(ctx, "fake-mail", PluginPatch{Enabled: &on, Settings: map[string]any{"api_key": "sk-1"}}); err != nil {
		t.Fatal(err)
	}

	logs := &syncBuffer{}
	startedApp(t, superuserDSN(t, dsn), newSecretsKey(t), "", logs, fakePlugin{})
	if got := storedPluginSettings(t, bare, "fake-mail")["api_key"]; got != "sk-1" {
		t.Errorf("under a superuser login the start-up seal changed the key to %v", got)
	}
	out := logs.String()
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "not sealed") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "level=WARN") || !strings.Contains(lines[0], "superuser") {
		t.Errorf("want one warning saying the secrets were not sealed and why, got:\n%s", out)
	}
}

func TestStartingWithoutAKeyLeavesSecretsAsTheyAre(t *testing.T) {
	dsn := keyedDSN(t, fakePlugin{})
	ctx := context.Background()
	on := true
	bare := keyedApp(t, dsn, "", "", fakePlugin{})
	if _, err := bare.Plugins().Update(ctx, "fake-mail", PluginPatch{Enabled: &on, Settings: map[string]any{"api_key": "sk-1"}}); err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	startedApp(t, dsn, "", "", logs, fakePlugin{})
	if got := storedPluginSettings(t, bare, "fake-mail")["api_key"]; got != "sk-1" {
		t.Errorf("starting without a key changed the key to %v", got)
	}
	if strings.Contains(logs.String(), "secrets") {
		t.Errorf("starting without a key logged about secrets:\n%s", logs.String())
	}
}
