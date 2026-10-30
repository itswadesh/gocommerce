package aigateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// trialStore is a fresh store in currency on an install configured by cfg.
func trialStore(t *testing.T, cfg Config, currency string) (*gocommerce.App, *Module) {
	t.Helper()
	s := newStandIn(t, reply(http.StatusOK, "{}"))
	cfg.Client, cfg.CallTimeout, cfg.PollInterval = s.client(), 2*time.Second, time.Hour
	m := New(cfg)
	app := gctest.NewWithConfig(t, gocommerce.Config{Currency: currency}, m)
	return app, m
}

// The owner's free trial is ₹200 (D151, 2026-10-30): a fresh Indian store gets
// exactly 20,000 paise, whatever the rupee's rate.
func TestAFreshRupeeStoreGetsTwoHundredRupees(t *testing.T) {
	cfg := Config{PlatformAnthropicKey: platformKey, PlatformUSDRates: "INR=88.50"}
	_, m := trialStore(t, cfg, "INR")
	if c := mustCredit(t, m, context.Background()); c.FreeAllowanceMicroMinor != 20000*microPerMinor || c.Currency != "INR" {
		t.Errorf("free trial %d %s, want ₹200 (%d)", c.FreeAllowanceMicroMinor, c.Currency, 20000*microPerMinor)
	}
}

// A store in another currency gets ₹200's worth at the install's own rates,
// rounded down, rather than 20,000 of its own minor units: $200 would be a
// hundred times the trial.
func TestAStoreInAnotherCurrencyGetsTwoHundredRupeesWorth(t *testing.T) {
	cfg := Config{PlatformAnthropicKey: platformKey, PlatformUSDRates: "INR=88.50,EUR=0.92"}
	for currency, want := range map[string]int64{
		"USD": 225, // 20000 / 88.50 = 225.9 cents
		"EUR": 207, // 20000 / 88.50 × 0.92 = 207.9 cents
	} {
		_, m := trialStore(t, cfg, currency)
		if c := mustCredit(t, m, context.Background()); c.FreeAllowanceMicroMinor != want*microPerMinor {
			t.Errorf("%s free trial %d micro-minor, want %d minor", currency, c.FreeAllowanceMicroMinor, want)
		}
	}
}

// AI_FREE_TRIAL names the trial per currency; a currency it names gets that
// figure exactly, and one it does not gets the first figure converted.
func TestTheInstallNamesTheTrialPerCurrency(t *testing.T) {
	cfg := Config{PlatformAnthropicKey: platformKey, PlatformUSDRates: "INR=88.50", FreeTrial: "INR=5000, USD=50"}
	for currency, want := range map[string]int64{"INR": 5000, "USD": 50} {
		_, m := trialStore(t, cfg, currency)
		if c := mustCredit(t, m, context.Background()); c.FreeAllowanceMicroMinor != want*microPerMinor {
			t.Errorf("%s free trial %d micro-minor, want %d minor", currency, c.FreeAllowanceMicroMinor, want)
		}
	}
	// A figure that does not read falls back to the owner's ₹200, not to nothing.
	cfg.FreeTrial = "INR=30.00"
	_, m := trialStore(t, cfg, "INR")
	if c := mustCredit(t, m, context.Background()); c.FreeAllowanceMicroMinor != 20000*microPerMinor {
		t.Errorf("unreadable AI_FREE_TRIAL gave %d micro-minor, want ₹200", c.FreeAllowanceMicroMinor)
	}
}

// What a call holds is priced on the request it sends: the estimate of its
// input and the max_tokens it asks for. The provider cannot bill more output
// than max_tokens, so even an answer that uses every token it was allowed
// costs no more than was held.
func TestACallNeverCostsMoreThanItHeld(t *testing.T) {
	var app *gocommerce.App
	var held, maxTokens atomic.Int64
	handler := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			MaxTokens int64 `json:"max_tokens"`
		}
		_ = json.Unmarshal(body, &req)
		maxTokens.Store(req.MaxTokens)
		var h int64
		_ = app.DB().QueryRowContext(context.Background(), `SELECT reserved_micro FROM ai_credits`).Scan(&h)
		held.Store(h)
		// The most this request can be billed: every output token it allowed,
		// and its whole prompt written to the cache. The prompt is a few
		// hundred tokens at English's four characters a token; the estimate
		// counts three.
		reply(http.StatusOK, answer(smallID, goodAnswer, Usage{CacheWriteTokens: 500, OutputTokens: req.MaxTokens}))(w, r)
	}
	task := rewriteTask()
	task.MaxInputTokens = 50_000 // the old ceiling would hold all of it
	app, m, _ := newGatewayWith(t, platformConfig(), handler, task)
	ctx := context.Background()

	res, err := m.Run(ctx, "test.rewrite", listing{Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if maxTokens.Load() != int64(task.MaxOutputTokens) {
		t.Errorf("max_tokens %d, want the task's %d", maxTokens.Load(), task.MaxOutputTokens)
	}
	if h := held.Load(); h == 0 || res.CostMicroMinor > h {
		t.Errorf("cost %d, held %d: a call cost more than it held", res.CostMicroMinor, h)
	}
	// $1.25 cache write and $5 out per million at 100 cents.
	if old := int64(task.MaxInputTokens)*125 + int64(task.MaxOutputTokens)*500; held.Load() >= old/2 {
		t.Errorf("held %d, want the request's own estimate, far under the task's bound of %d", held.Load(), old)
	}
	if c := mustCredit(t, m, ctx); c.ReservedMicroMinor != 0 || c.ConsumedMicroMinor != res.CostMicroMinor {
		t.Errorf("after the call: %+v, want nothing held and the cost consumed", c)
	}
}

// A task may ask for fewer output tokens than its bound for a smaller
// request, and holds less for it; it may not ask for more.
func TestARequestsOwnMaxTokensSizesItsHold(t *testing.T) {
	var sentMax atomic.Int64
	var app *gocommerce.App
	var held atomic.Int64
	handler := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MaxTokens int64 `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		sentMax.Store(req.MaxTokens)
		var h int64
		_ = app.DB().QueryRowContext(context.Background(), `SELECT reserved_micro FROM ai_credits`).Scan(&h)
		held.Store(h)
		reply(http.StatusOK, answer(smallID, goodAnswer, Usage{InputTokens: 100, OutputTokens: 10}))(w, r)
	}
	var ask int
	task := rewriteTask()
	build := task.Build
	task.Build = func(ctx context.Context, input json.RawMessage, p Policy) (Request, []SourceRef, error) {
		req, refs, err := build(ctx, input, p)
		req.MaxTokens = ask
		return req, refs, err
	}
	app, m, _ := newGatewayWith(t, platformConfig(), handler, task)
	ctx := context.Background()

	holds := map[int]int64{}
	for _, n := range []int{0, 200, 5000} {
		ask = n
		if _, err := m.Run(ctx, "test.rewrite", listing{Title: "x"}); err != nil {
			t.Fatal(err)
		}
		holds[n] = held.Load()
		want := int64(n)
		if n == 0 || n > task.MaxOutputTokens {
			want = int64(task.MaxOutputTokens)
		}
		if sentMax.Load() != want {
			t.Errorf("asked for %d: max_tokens %d, want %d", n, sentMax.Load(), want)
		}
	}
	// 800 fewer output tokens at 500 cents a million.
	if d := holds[0] - holds[200]; d != 800*500 {
		t.Errorf("hold for 200 output tokens is %d less than for 1,000, want %d", d, 800*500)
	}
	if holds[5000] != holds[0] {
		t.Errorf("asking past the task's bound held %d, want the bound's %d", holds[5000], holds[0])
	}
}

// Stores seeded with the old placeholder trial (500 minor units) that nobody
// set by hand are raised to the new one when the engine upgrades. A trial an
// operator set stays as set, and no trial is ever lowered.
func TestUpgradeRaisesOnlyThePlaceholderTrial(t *testing.T) {
	cfg := Config{PlatformAnthropicKey: platformKey, PlatformUSDRates: "INR=88.50"}
	_, m := trialStore(t, cfg, "INR")
	app := m.app
	ctx := context.Background()
	second, err := app.Stores().Create(ctx, "Second", "second")
	if err != nil {
		t.Fatal(err)
	}
	third, err := app.Stores().Create(ctx, "Third", "third")
	if err != nil {
		t.Fatal(err)
	}
	const placeholder = 500 * microPerMinor
	seed := func(ctx context.Context, currency string) {
		t.Helper()
		if _, err := app.DB().ExecContext(ctx, `
			INSERT INTO ai_credits (free_allowance_micro, currency) VALUES ($1, $2)`, placeholder, currency); err != nil {
			t.Fatal(err)
		}
	}
	// First: on the placeholder, never set by hand. Second: an operator set
	// it to 500 on purpose. Third: a dollar store, whose $5 is more than
	// ₹200's worth.
	seed(ctx, "INR")
	b := gocommerce.WithStore(ctx, second.ID)
	seed(b, "INR")
	if _, err := m.SetFreeAllowance(b, 500, "agreed"); err != nil {
		t.Fatal(err)
	}
	c := gocommerce.WithStore(ctx, third.ID)
	seed(c, "USD")

	runMigration(t, m, "0007_free_trial")

	for name, tc := range map[string]struct {
		ctx  context.Context
		want int64
	}{
		"placeholder":  {ctx, 20000 * microPerMinor},
		"set by hand":  {b, placeholder},
		"dollar store": {c, placeholder},
	} {
		var got int64
		if err := app.DB().QueryRowContext(tc.ctx, `SELECT free_allowance_micro FROM ai_credits`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("%s: free trial %d, want %d", name, got, tc.want)
		}
	}
}

// The owner raised the trial from ₹30 to ₹200 (D151, 2026-10-30). Stores
// already seeded with ₹30, or ₹30's worth, that nobody set by hand are raised
// to the new trial; one an operator set stays as set, and none is lowered.
func TestUpgradeRaisesTheThirtyRupeeTrial(t *testing.T) {
	cfg := Config{PlatformAnthropicKey: platformKey, PlatformUSDRates: "INR=88.50"}
	_, m := trialStore(t, cfg, "INR")
	app := m.app
	ctx := context.Background()
	stores := map[string]context.Context{"thirty rupees": ctx}
	for _, name := range []string{"set by hand", "thirty rupees worth", "five dollars"} {
		s, err := app.Stores().Create(ctx, name, strings.ReplaceAll(name, " ", "-"))
		if err != nil {
			t.Fatal(err)
		}
		stores[name] = gocommerce.WithStore(ctx, s.ID)
	}
	seed := func(ctx context.Context, minor int64, currency string) {
		t.Helper()
		if _, err := app.DB().ExecContext(ctx, `
			INSERT INTO ai_credits (free_allowance_micro, currency) VALUES ($1, $2)`, minor*microPerMinor, currency); err != nil {
			t.Fatal(err)
		}
	}
	seed(stores["thirty rupees"], 3000, "INR")
	seed(stores["set by hand"], 3000, "INR")
	if _, err := m.SetFreeAllowance(stores["set by hand"], 3000, "agreed"); err != nil {
		t.Fatal(err)
	}
	seed(stores["thirty rupees worth"], 33, "USD")
	seed(stores["five dollars"], 500, "USD")

	runMigration(t, m, "0008_free_trial_200")

	for name, want := range map[string]int64{
		"thirty rupees":       20000,
		"set by hand":         3000,
		"thirty rupees worth": 225, // 20000 / 88.50 = 225.9 cents
		"five dollars":        500,
	} {
		var got int64
		if err := app.DB().QueryRowContext(stores[name], `SELECT free_allowance_micro FROM ai_credits`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want*microPerMinor {
			t.Errorf("%s: free trial %d micro-minor, want %d minor", name, got, want)
		}
	}
}

// runMigration runs one of the module's migrations in a transaction of its own.
func runMigration(t *testing.T, m *Module, id string) {
	t.Helper()
	var run func(context.Context, *sql.Tx) error
	for _, mig := range m.Migrations() {
		if mig.ID == id {
			run = mig.Run
		}
	}
	if run == nil {
		t.Fatalf("no %s migration", id)
	}
	tx, err := m.app.DB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// The estimate is now what money is held on, so a picture counts at least
// what its pixels cost (about a token per 750), not a flat figure that a
// 2048 px photo on a high-resolution model would pass; and the caption sent
// before it counts too.
func TestTheEstimateCountsAPictureAtItsSize(t *testing.T) {
	jpegOf := func(side int) []byte {
		var b bytes.Buffer
		if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, side, side)), nil); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	est := func(d Document) int { return estimateTokens(Request{Documents: []Document{d}}) }
	small := est(Document{MediaType: "image/jpeg", Bytes: jpegOf(512)})
	if small != requestOverhead+TokensPerImage {
		t.Errorf("a 512 px picture estimates %d, want the floor %d", small, requestOverhead+TokensPerImage)
	}
	if big := est(Document{MediaType: "image/jpeg", Bytes: jpegOf(2048)}); big < requestOverhead+2048*2048/750 {
		t.Errorf("a 2048 px picture estimates %d, want at least %d", big, requestOverhead+2048*2048/750)
	}
	if c := est(Document{MediaType: "image/jpeg", Bytes: jpegOf(512), Caption: "Photo 12345"}); c <= small {
		t.Errorf("a caption added nothing: %d", c)
	}
}
