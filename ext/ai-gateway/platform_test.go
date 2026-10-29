package aigateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// platformKey is the install's own key in these tests. Long and distinctive,
// so finding it anywhere it must not be is a plain substring search.
const platformKey = "sk-ant-platform-0123456789abcdefSECRET"

// platformConfig is an install with an Anthropic key of its own, its models
// the defaults, and its prices the standard settings' in dollars: a USD
// store's calls cost what they would on its own key at standardSettings.
func platformConfig() Config {
	return Config{
		PlatformAnthropicKey: platformKey,
		PlatformSmallPrices:  "1,5,0.10,1.25",
		PlatformStrongPrices: "3,15,3,3",
	}
}

func sentModel(t *testing.T, s sent) string {
	t.Helper()
	var body struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(s.Body, &body); err != nil {
		t.Fatal(err)
	}
	return body.Model
}

// A store that has entered no key needs no setup at all: on an install with a
// key the gateway is on, the call goes to Anthropic on the install's key at
// the install's model, and the store's AI credit pays for it.
func TestAStoreWithNoKeyRunsOnThePlatformKeyAndDrawsCredit(t *testing.T) {
	_, m, s := newGatewayWith(t, platformConfig(), reply(http.StatusOK, answer(smallID, goodAnswer, Usage{InputTokens: 3000})), rewriteTask())
	ctx := context.Background()

	res, err := m.Run(ctx, "test.rewrite", listing{Title: "x"})
	if err != nil {
		t.Fatalf("run with nothing set up: %v", err)
	}
	got := s.last(t)
	if !strings.HasPrefix(got.URL, anthropicHost) || got.Header.Get("x-api-key") != platformKey || sentModel(t, got) != smallID {
		t.Errorf("sent to %s as %s with the platform key %v, want Anthropic, %s, the platform key",
			got.URL, sentModel(t, got), got.Header.Get("x-api-key") == platformKey, smallID)
	}
	const cost = 3000 * 100
	if res.CostMicroMinor != cost {
		t.Errorf("cost %d, want %d", res.CostMicroMinor, cost)
	}
	if c := mustCredit(t, m, ctx); c.ConsumedMicroMinor != cost || c.ReservedMicroMinor != 0 {
		t.Errorf("credit = %+v, want %d consumed and nothing held", c, cost)
	}
	if b := mustBudget(t, m, ctx); !b.Ready || b.KeySource != KeyPlatform || b.SpentMicroMinor != cost || b.CapMinor != nil {
		t.Errorf("budget = %+v, want ready on the platform key with %d spent and no cap", b, cost)
	}
}

// A store with a key of its own pays its provider directly: its key is the one
// sent, at its own model and prices, and no AI credit is drawn or needed. Its
// own monthly cap still holds.
func TestAStoresOwnKeyIsUsedAndDrawsNoCredit(t *testing.T) {
	app, m, s := newGatewayWith(t, platformConfig(), reply(http.StatusOK, answer(smallID, goodAnswer, Usage{InputTokens: 3000})), rewriteTask())
	ctx := context.Background()
	configure(t, app, ctx, nil)
	if _, err := m.SetFreeAllowance(ctx, 0, ""); err != nil {
		t.Fatal(err)
	}

	res, err := m.Run(ctx, "test.rewrite", listing{Title: "x"})
	if err != nil {
		t.Fatalf("a store with its own key and no credit was refused: %v", err)
	}
	if key := s.last(t).Header.Get("x-api-key"); key != "sk-ant-test-key" {
		t.Errorf("sent with the platform key %v, want the store's own", key == platformKey)
	}
	const cost = 3000 * 100
	if c := mustCredit(t, m, ctx); c.ConsumedMicroMinor != 0 || c.ReservedMicroMinor != 0 {
		t.Errorf("credit = %+v, want nothing drawn by the store's own key", c)
	}
	if b := mustBudget(t, m, ctx); b.KeySource != KeyOwn || b.SpentMicroMinor != cost || res.CostMicroMinor != cost {
		t.Errorf("budget = %+v, run cost %d; want the own key, %d spent against the store's cap", b, res.CostMicroMinor, cost)
	}
}

// With no key anywhere, nothing is called and the store is told AI is not set
// up. Here the store switched the gateway on while the install had a key, and
// the install's key has since gone.
func TestWithNoKeyAnywhereAIIsNotSetUp(t *testing.T) {
	app, m, s := newGateway(t, reply(http.StatusOK, "{}"), rewriteTask())
	ctx := context.Background()
	if _, err := app.DB().ExecContext(ctx, `INSERT INTO plugins (key, enabled, settings) VALUES ($1, true, '{}')`, PluginKey); err != nil {
		t.Fatal(err)
	}

	_, err := m.Run(ctx, "test.rewrite", listing{Title: "x"})
	if !errors.Is(err, ErrNotReady) || !strings.Contains(err.Error(), "isn't set up") {
		t.Fatalf("run = %v, want ErrNotReady saying AI isn't set up", err)
	}
	if s.count() != 0 || countRows(t, app, ctx, "ai_executions") != 0 {
		t.Errorf("%d requests and %d records, want neither", s.count(), countRows(t, app, ctx, "ai_executions"))
	}
	if b := mustBudget(t, m, ctx); b.Ready || b.KeySource != KeyNone || !strings.Contains(b.Reason, "isn't set up") {
		t.Errorf("budget = %+v, want not ready with no key", b)
	}
}

// On the platform's key the platform's models and prices are the ones used,
// whatever the store typed: a store cannot pick a dearer model, price a call
// at nothing, or send the platform's key to another provider.
func TestAStoreCannotChooseThePlatformsModelsOrPrices(t *testing.T) {
	app, m, s := newGatewayWith(t, platformConfig(),
		byModel(map[string]string{smallID: answer(smallID, goodAnswer, Usage{InputTokens: 3000})}), rewriteTask())
	ctx := context.Background()
	configure(t, app, ctx, map[string]any{
		"api_key": nil, "provider": ProviderOpenAI, "small_model": "claude-fable-5-1",
		"small_price_input": 1, "small_price_output": 1, "small_price_cache_read": 1, "small_price_cache_write": 1,
	})

	res, err := m.Run(ctx, "test.rewrite", listing{Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	got := s.last(t)
	if !strings.HasPrefix(got.URL, anthropicHost) || sentModel(t, got) != smallID || got.Header.Get("x-api-key") != platformKey {
		t.Errorf("sent %s to %s, want %s to Anthropic on the platform key", sentModel(t, got), got.URL, smallID)
	}
	if s.count() != 1 {
		t.Errorf("%d requests, want 1", s.count())
	}
	const cost = 3000 * 100 // the platform's $1 a million, not the store's 1 minor unit
	if res.CostMicroMinor != cost || mustCredit(t, m, ctx).ConsumedMicroMinor != cost {
		t.Errorf("cost %d, credit consumed %d; want %d each", res.CostMicroMinor, mustCredit(t, m, ctx).ConsumedMicroMinor, cost)
	}
}

// The install's key reaches Anthropic and nothing else: not a response, a
// status, a record, a plugin read, the audit feed or a log line, even when
// the provider repeats it back.
func TestThePlatformKeyNeverLeavesTheServer(t *testing.T) {
	var logs bytes.Buffer
	s := newStandIn(t, reply(http.StatusUnauthorized,
		`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key `+platformKey+`"}}`))
	cfg := platformConfig()
	cfg.Client, cfg.CallTimeout, cfg.PollInterval = s.client(), 2*time.Second, time.Hour
	m := New(cfg)
	if err := m.RegisterTask(rewriteTask()); err != nil {
		t.Fatal(err)
	}
	app := gctest.NewWithConfig(t, gocommerce.Config{
		Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}, m)
	ctx := context.Background()
	// A store that saves the form with no key of its own, so the audit feed has a row.
	configure(t, app, ctx, map[string]any{"api_key": nil})

	res, err := m.Run(ctx, "test.rewrite", listing{Title: "x"})
	if err == nil {
		t.Fatal("a refused key answered")
	}
	seen := map[string]string{"run error": err.Error()}
	for _, path := range []string{
		"/api/admin/x/ai-gateway/budget",
		"/api/admin/x/ai-gateway/credit",
		"/api/admin/x/ai-gateway/executions",
		fmt.Sprintf("/api/admin/x/ai-gateway/executions/%d", res.ExecutionID),
		"/api/admin/plugins",
		"/api/admin/plugins/" + PluginKey,
		"/api/admin/audit",
	} {
		rec := gctest.AdminRequest(t, app, http.MethodGet, path, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d", path, rec.Code)
		}
		seen[path] = rec.Body.String()
	}
	check := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/ai-gateway/check", nil)
	seen["check"] = check.Body.String()
	seen["logs"] = logs.String()
	for where, text := range seen {
		if strings.Contains(text, platformKey) {
			t.Errorf("the platform key is in the %s: %s", where, text)
		}
	}
	if s.count() != 1 {
		t.Errorf("%d provider requests, want only the run's: the check spends nothing on the platform's key", s.count())
	}
}

// On an install with a key, the store's own key and cap are optional, the
// gateway is on before anyone touches it, and pictures may be sent, so
// "Create from photos" works for a store with nothing but credit. Without
// one, the store must set it up as before.
func TestThePlatformKeyMakesTheStoresOwnKeyOptional(t *testing.T) {
	required := func(p *gocommerce.Plugin, key string) bool {
		for _, f := range p.Fields {
			if f.Key == key {
				return f.Required
			}
		}
		t.Fatalf("no field %s", key)
		return false
	}

	app, m, _ := newGatewayWith(t, platformConfig(), reply(http.StatusOK, "{}"))
	ctx := context.Background()
	p, err := app.Plugins().Get(ctx, PluginKey)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Enabled || !p.Configured || required(p, "api_key") || required(p, "monthly_cap") || p.Settings["share_documents"] != true {
		t.Errorf("with a platform key: enabled %v, configured %v, key required %v, cap required %v, documents %v",
			p.Enabled, p.Configured, required(p, "api_key"), required(p, "monthly_cap"), p.Settings["share_documents"])
	}
	if ok, err := m.Allows(ctx, DataDocuments); err != nil || !ok {
		t.Errorf("documents allowed = %v %v, want true", ok, err)
	}
	if err := m.Ready(ctx); err != nil {
		t.Errorf("ready = %v, want ready with nothing set", err)
	}
	// Switching it back on after switching it off needs no key either.
	for _, on := range []bool{false, true} {
		if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Enabled: &on}); err != nil {
			t.Fatalf("switch %v with no key: %v", on, err)
		}
	}

	app2, _, _ := newGateway(t, reply(http.StatusOK, "{}"))
	p, err = app2.Plugins().Get(ctx, PluginKey)
	if err != nil {
		t.Fatal(err)
	}
	if p.Enabled || !required(p, "api_key") || !required(p, "monthly_cap") || p.Settings["share_documents"] != false {
		t.Errorf("without a platform key: enabled %v, key required %v, cap required %v, documents %v",
			p.Enabled, required(p, "api_key"), required(p, "monthly_cap"), p.Settings["share_documents"])
	}
}

// Anthropic bills in dollars and a store's credit is in its own currency: the
// platform's prices convert at the rate the install sets, rounded up.
func TestPlatformPricesConvertAtTheInstallsRate(t *testing.T) {
	s := newStandIn(t, reply(http.StatusOK, answer(smallID, goodAnswer, Usage{InputTokens: 3000})))
	cfg := platformConfig()
	cfg.Client, cfg.CallTimeout, cfg.PollInterval = s.client(), 2*time.Second, time.Hour
	cfg.PlatformUSDRates = "EUR=0.92, INR=88.50"
	m := New(cfg)
	if err := m.RegisterTask(rewriteTask()); err != nil {
		t.Fatal(err)
	}
	gctest.NewWithConfig(t, gocommerce.Config{Currency: "INR"}, m)
	ctx := context.Background()

	res, err := m.Run(ctx, "test.rewrite", listing{Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	// $1.00 a million input tokens at ₹88.50 to the dollar is 8,850 paise a million.
	if res.Currency != "INR" || res.CostMicroMinor != 3000*8850 {
		t.Errorf("cost %d %s, want %d INR", res.CostMicroMinor, res.Currency, 3000*8850)
	}
	st, err := m.settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// $0.10 is 885 paise; $1.25 is 11,062.5, rounded up.
	if p := st.prices[TierSmall]; p.cacheRead != 885 || p.cacheWrite != 11063 {
		t.Errorf("small prices = %+v, want cache read 885 and write 11063", p)
	}
}

// A store whose currency the install gives no rate for is refused before
// anything is sent, rather than charged at a rate nobody chose.
func TestAStoreInACurrencyWithNoRateCannotUseThePlatformKey(t *testing.T) {
	s := newStandIn(t, reply(http.StatusOK, answer(smallID, goodAnswer, Usage{InputTokens: 3000})))
	cfg := platformConfig()
	cfg.Client, cfg.CallTimeout, cfg.PollInterval = s.client(), 2*time.Second, time.Hour
	m := New(cfg)
	if err := m.RegisterTask(rewriteTask()); err != nil {
		t.Fatal(err)
	}
	gctest.NewWithConfig(t, gocommerce.Config{Currency: "INR"}, m)
	ctx := context.Background()

	if err := m.Ready(ctx); !errors.Is(err, ErrNotReady) || !strings.Contains(err.Error(), "INR") {
		t.Errorf("ready = %v, want not ready naming INR", err)
	}
	if _, err := m.Run(ctx, "test.rewrite", listing{Title: "x"}); !errors.Is(err, ErrNotReady) || s.count() != 0 {
		t.Errorf("run = %v with %d requests, want refused before sending", err, s.count())
	}
}

// With no prices given, the default models carry Anthropic's published
// prices, in US cents a million tokens for a USD store.
func TestTheDefaultModelsCarryAnthropicsPublishedPrices(t *testing.T) {
	_, m, _ := newGatewayWith(t, Config{PlatformAnthropicKey: platformKey}, reply(http.StatusOK, "{}"))
	st, err := m.settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.models[TierSmall] != "claude-haiku-5-5" || st.models[TierStrong] != "claude-sonnet-5-5" {
		t.Errorf("models = %v", st.models)
	}
	// Claude Haiku 5.5: $0.10 in, $0.50 out, $0.01 cache read, $0.125 cache
	// write (13c, rounded up). Claude Sonnet 5.5: $2, $10, $0.10, $2.50.
	want := map[Tier]prices{
		TierSmall:  {input: 10, output: 50, cacheRead: 1, cacheWrite: 13, complete: true},
		TierStrong: {input: 200, output: 1000, cacheRead: 10, cacheWrite: 250, complete: true},
	}
	for tier, w := range want {
		if st.prices[tier] != w {
			t.Errorf("%s prices = %+v, want %+v", tier, st.prices[tier], w)
		}
	}
}

// A platform model with no verified price is never called on the platform's
// key: one the install names without prices, or whose prices do not read.
func TestAPlatformModelWithNoPriceIsNeverCalled(t *testing.T) {
	for name, cfg := range map[string]Config{
		"another model, no price":    {PlatformAnthropicKey: platformKey, PlatformSmallModel: "claude-opus-5-5"},
		"a price that does not read": {PlatformAnthropicKey: platformKey, PlatformSmallPrices: "0.10,ten,0.01,0.125"},
		"a price of nothing":         {PlatformAnthropicKey: platformKey, PlatformSmallPrices: "0,0.50,0.01,0.125"},
		"too few prices":             {PlatformAnthropicKey: platformKey, PlatformSmallPrices: "0.10,0.50"},
	} {
		t.Run(name, func(t *testing.T) {
			_, m, s := newGatewayWith(t, cfg, reply(http.StatusOK, answer(smallID, goodAnswer, Usage{InputTokens: 3000})), rewriteTask())
			_, err := m.Run(context.Background(), "test.rewrite", listing{Title: "x"})
			if runErrOutcome(err) != OutcomeNoPrice || s.count() != 0 {
				t.Errorf("run = %v with %d requests, want no_price before sending", err, s.count())
			}
		})
	}
}
