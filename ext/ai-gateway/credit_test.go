package aigateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// The free trial in micro-minor, as a fresh dollar store on platformConfig's
// install gets it.
const freeTrialMicro = 500 * microPerMinor

func mustCredit(t *testing.T, m *Module, ctx context.Context) *Credit {
	t.Helper()
	c, err := m.Credit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A store nobody has touched has its whole free trial to spend, and no row is
// written until it first spends: reading credit does not create one.
func TestFreshStoreHasItsFreeTrial(t *testing.T) {
	app, m, _ := newGatewayWith(t, platformConfig(), reply(http.StatusOK, "{}"))
	ctx := context.Background()
	configure(t, app, ctx, nil)

	c := mustCredit(t, m, ctx)
	if c.FreeAllowanceMicroMinor != freeTrialMicro || c.FreeRemainingMicroMinor != freeTrialMicro {
		t.Errorf("free allowance %d, remaining %d, want %d", c.FreeAllowanceMicroMinor, c.FreeRemainingMicroMinor, freeTrialMicro)
	}
	if c.AvailableMicroMinor != freeTrialMicro || c.Exhausted || c.GrantedMicroMinor != 0 || c.ConsumedMicroMinor != 0 {
		t.Errorf("credit = %+v", c)
	}
	if n := countRows(t, app, ctx, "ai_credits"); n != 0 {
		t.Errorf("%d credit rows, want none before any spend", n)
	}
}

// An AI call on the platform's key draws the free trial down by what it cost;
// a store with nothing set up is on the platform's key.
func TestACallDrawsTheFreeTrial(t *testing.T) {
	_, m, _ := newGatewayWith(t, platformConfig(), reply(http.StatusOK, answer(smallID, goodAnswer, Usage{InputTokens: 3000})), rewriteTask())
	ctx := context.Background()

	if _, err := m.Run(ctx, "test.rewrite", listing{Title: "x"}); err != nil {
		t.Fatal(err)
	}
	c := mustCredit(t, m, ctx)
	const cost = 3000 * 100 // 300,000 micro-minor
	if c.ConsumedMicroMinor != cost || c.FreeRemainingMicroMinor != freeTrialMicro-cost {
		t.Errorf("consumed %d, free remaining %d (want consumed %d)", c.ConsumedMicroMinor, c.FreeRemainingMicroMinor, cost)
	}
	if c.GrantedMicroMinor != 0 || c.BalanceRemainingMicroMinor != 0 || c.ReservedMicroMinor != 0 {
		t.Errorf("the free trial was drawn, not a balance: %+v", c)
	}
}

// With no free trial and a topped-up balance, a call draws the balance.
func TestACallDrawsTheBalanceWhenTheFreeTrialIsGone(t *testing.T) {
	_, m, _ := newGatewayWith(t, platformConfig(), reply(http.StatusOK, answer(smallID, goodAnswer, Usage{InputTokens: 3000})), rewriteTask())
	ctx := context.Background()
	if _, err := m.SetFreeAllowance(ctx, 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Grant(ctx, 1, "first top-up"); err != nil { // 1 minor = 1,000,000 micro
		t.Fatal(err)
	}
	if _, err := m.Run(ctx, "test.rewrite", listing{Title: "x"}); err != nil {
		t.Fatal(err)
	}
	c := mustCredit(t, m, ctx)
	const cost = 3000 * 100
	if c.FreeRemainingMicroMinor != 0 || c.BalanceRemainingMicroMinor != microPerMinor-cost || c.ConsumedMicroMinor != cost {
		t.Errorf("credit = %+v, want the balance drawn by %d", c, cost)
	}
}

// Lowering the free allowance below what a store has already spent never
// shows a negative balance: the figures floor at zero, and the store is
// simply out of credit.
func TestLoweringTheFreeAllowanceBelowSpendDoesNotGoNegative(t *testing.T) {
	app, m, _ := newGateway(t, reply(http.StatusOK, "{}"))
	ctx := context.Background()
	configure(t, app, ctx, nil)
	// Spend part of the trial without a model call, then cut the trial below it.
	if _, err := m.SetFreeAllowance(ctx, 5, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.settleCredit(ctx, 0, 3*microPerMinor); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetFreeAllowance(ctx, 1, ""); err != nil { // below the 3 spent
		t.Fatal(err)
	}
	c := mustCredit(t, m, ctx)
	if c.FreeRemainingMicroMinor != 0 || c.BalanceRemainingMicroMinor != 0 {
		t.Errorf("credit = %+v, want both remaining figures floored at zero", c)
	}
	if !c.Exhausted {
		t.Errorf("available %d, want the store exhausted", c.AvailableMicroMinor)
	}
}

// A call with nothing left is refused before the model is asked, and the
// record says the free trial is spent.
func TestACallWithNoCreditIsRefusedAndNeverCallsTheModel(t *testing.T) {
	var calls atomic.Int32
	_, m, _ := newGatewayWith(t, platformConfig(), func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		reply(http.StatusOK, answer(smallID, goodAnswer, Usage{InputTokens: 3000}))(w, r)
	}, rewriteTask())
	ctx := context.Background()
	if _, err := m.SetFreeAllowance(ctx, 0, ""); err != nil {
		t.Fatal(err)
	}

	res, err := m.Run(ctx, "test.rewrite", listing{Title: "x"})
	re := runErr(t, err)
	if re.Outcome != OutcomeCreditExhausted {
		t.Fatalf("outcome = %q, want %q", re.Outcome, OutcomeCreditExhausted)
	}
	if !strings.Contains(re.Reason, "free AI trial") {
		t.Errorf("reason = %q, want it to mention the free AI trial", re.Reason)
	}
	if calls.Load() != 0 {
		t.Errorf("the model was called %d times for a store with no credit", calls.Load())
	}
	if e := mustExecution(t, m, ctx, res.ExecutionID); e.Outcome != OutcomeCreditExhausted || e.CostMicroMinor != 0 {
		t.Errorf("record outcome %q cost %d, want credit_exhausted at no cost", e.Outcome, e.CostMicroMinor)
	}
	// Nothing was drawn, and nothing stays held.
	if c := mustCredit(t, m, ctx); c.ConsumedMicroMinor != 0 || c.ReservedMicroMinor != 0 {
		t.Errorf("credit = %+v, want untouched", c)
	}
}

// A topped-up store whose balance runs out is told it is out of credit, not
// that its free trial is spent.
func TestOutOfBalanceSaysOutOfCredit(t *testing.T) {
	app, m, _ := newGateway(t, reply(http.StatusOK, "{}"), rewriteTask())
	ctx := context.Background()
	configure(t, app, ctx, nil)
	if _, err := m.SetFreeAllowance(ctx, 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Grant(ctx, 1, ""); err != nil {
		t.Fatal(err)
	}
	// Consume the balance down to nothing without a model call.
	if err := m.settleCredit(ctx, 0, microPerMinor); err != nil {
		t.Fatal(err)
	}
	if got := m.creditReason(ctx); !strings.Contains(got, "out of AI credit") {
		t.Errorf("reason = %q, want it to mention being out of AI credit", got)
	}
}

// A model call that never left draws no credit: a failed call is never
// charged, so a store is not billed twice for one attempt.
func TestAFailedCallDrawsNoCredit(t *testing.T) {
	s := newStandIn(t, reply(http.StatusOK, "{}"))
	s.srv.Close()
	app, m := newQuickGateway(t, s, 2*time.Second)
	ctx := context.Background()
	configure(t, app, ctx, nil)

	if _, err := m.Run(ctx, "test.rewrite", listing{Title: "x"}); runErr(t, err).Outcome != OutcomeProviderError {
		t.Fatalf("outcome = %v", err)
	}
	if c := mustCredit(t, m, ctx); c.ConsumedMicroMinor != 0 || c.ReservedMicroMinor != 0 {
		t.Errorf("credit = %+v, want nothing drawn by a failed call", c)
	}
}

// A deterministic answer calls no model, so it draws no credit even when the
// store has none: credit is a cost of model calls, not of using the feature.
func TestADeterministicAnswerDrawsNoCredit(t *testing.T) {
	task := rewriteTask()
	task.Name = "test.deterministic"
	task.Deterministic = func(ctx context.Context, input json.RawMessage) (json.RawMessage, bool, error) {
		return json.RawMessage(goodAnswer), true, nil
	}
	app, m, _ := newGateway(t, reply(http.StatusOK, "{}"), task)
	ctx := context.Background()
	configure(t, app, ctx, nil)
	if _, err := m.SetFreeAllowance(ctx, 0, ""); err != nil {
		t.Fatal(err)
	}

	res, err := m.Run(ctx, "test.deterministic", listing{Title: "x"})
	if err != nil {
		t.Fatalf("a deterministic answer was refused for want of credit: %v", err)
	}
	if res.Outcome != OutcomeDeterministic {
		t.Errorf("outcome = %q, want deterministic", res.Outcome)
	}
	if c := mustCredit(t, m, ctx); c.ConsumedMicroMinor != 0 {
		t.Errorf("a deterministic answer drew %d credit", c.ConsumedMicroMinor)
	}
}

// Fifty runs race for a free trial that covers only a few ceilings: credit
// never goes negative, nothing stays held, and the runs that could not reserve
// stopped before calling.
func TestConcurrentRunsCannotOverspendCredit(t *testing.T) {
	// Each ceiling is about 590,000 micro (smallCeiling); each real call
	// costs 300,000. A free trial of 3 minor units (3,000,000 micro) reserves
	// five ceilings at once.
	var calls atomic.Int32
	app, m, _ := newGatewayWith(t, platformConfig(), func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		reply(http.StatusOK, answer(smallID, goodAnswer, Usage{InputTokens: 3000}))(w, r)
	}, rewriteTask())
	ctx := context.Background()
	configure(t, app, ctx, map[string]any{"api_key": nil, "monthly_cap": 1000000}) // the cap is not the limiter here
	if _, err := m.SetFreeAllowance(ctx, 3, ""); err != nil {
		t.Fatal(err)
	}

	var ok, exhausted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Run(ctx, "test.rewrite", listing{Title: "x"})
			switch {
			case err == nil:
				ok.Add(1)
			case runErrOutcome(err) == OutcomeCreditExhausted:
				exhausted.Add(1)
			default:
				t.Errorf("run: %v", err)
			}
		}()
	}
	wg.Wait()

	c := mustCredit(t, m, ctx)
	if c.AvailableMicroMinor < 0 {
		t.Errorf("available went negative: %+v", c)
	}
	if c.ReservedMicroMinor != 0 {
		t.Errorf("%d still held after every run finished", c.ReservedMicroMinor)
	}
	if c.ConsumedMicroMinor > 3*microPerMinor {
		t.Errorf("consumed %d, over the free trial of %d", c.ConsumedMicroMinor, 3*microPerMinor)
	}
	if ok.Load() != calls.Load() {
		t.Errorf("%d ok runs but %d model calls", ok.Load(), calls.Load())
	}
	if ok.Load() == 0 || exhausted.Load() == 0 {
		t.Errorf("%d ran and %d were refused; the race should produce both", ok.Load(), exhausted.Load())
	}
}

// Credit is what the platform's key spends, so only the install's own admin
// token grants it or sets a free trial: no store's staff, its owner included,
// can give their store the platform's money. A grant is recorded.
func TestOnlyTheInstallGrantsCredit(t *testing.T) {
	app, m, _ := newGatewayWith(t, platformConfig(), reply(http.StatusOK, "{}"))
	ctx := context.Background()

	// An owner holds every right of the store, and may read the balance, but
	// may not grant or set a free trial.
	owner := gctest.OperatorToken(t, app, "owner@example.com", gocommerce.RoleOwner)
	if rec := gctest.SessionRequest(t, app, owner, http.MethodGet, "/api/admin/x/ai-gateway/credit", nil); rec.Code != http.StatusOK {
		t.Errorf("owner reading credit = %d, want 200", rec.Code)
	}
	grant := map[string]any{"amount_minor": 2500, "note": "welcome"}
	for _, path := range []string{"/api/admin/x/ai-gateway/credit/grant", "/api/admin/x/ai-gateway/credit/free-allowance"} {
		if rec := gctest.SessionRequest(t, app, owner, http.MethodPost, path, grant); rec.Code != http.StatusForbidden {
			t.Errorf("owner POST %s = %d, want 403", path, rec.Code)
		}
	}
	if c := mustCredit(t, m, ctx); c.GrantedMicroMinor != 0 || c.FreeAllowanceMicroMinor != freeTrialMicro {
		t.Fatalf("credit = %+v after refused grants", c)
	}

	rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/ai-gateway/credit/grant", grant)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin token granting credit = %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data Credit `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.GrantedMicroMinor != 2500*microPerMinor {
		t.Errorf("granted %d, want %d", env.Data.GrantedMicroMinor, 2500*microPerMinor)
	}
	entries, total, err := m.CreditLedger(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(entries) != 1 || entries[0].Kind != "grant" || entries[0].AmountMinor != 2500 || entries[0].Note != "welcome" {
		t.Errorf("ledger = %+v (%d)", entries, total)
	}
}

// Credit is held in the currency it was granted in. A store that changes its
// currency afterwards cannot spend ₹10,000 of credit as $10,000: nothing is
// drawn until the platform moves it.
func TestCreditInAnotherCurrencyIsNotSpent(t *testing.T) {
	_, m, s := newGatewayWith(t, platformConfig(), reply(http.StatusOK, answer(smallID, goodAnswer, Usage{InputTokens: 3000})), rewriteTask())
	ctx := context.Background()
	if _, err := m.Grant(ctx, 1_000_000, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.db.ExecContext(ctx, `UPDATE ai_credits SET currency = 'INR'`); err != nil {
		t.Fatal(err)
	}
	_, err := m.Run(ctx, "test.rewrite", listing{Title: "x"})
	re := runErr(t, err)
	if re.Outcome != OutcomeCreditExhausted || !strings.Contains(re.Reason, "INR") || s.count() != 0 {
		t.Errorf("run = %v with %d requests, want refused naming the credit's currency", err, s.count())
	}
}
