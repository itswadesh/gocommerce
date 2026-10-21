package aigateway

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// Fifty reservations racing for a cap that holds ten: exactly ten win, and
// what is held never passes the cap.
func TestConcurrentReservationsNeverPassTheCap(t *testing.T) {
	app, m, _ := newGateway(t, reply(http.StatusOK, "{}"))
	ctx := context.Background()
	configure(t, app, ctx, nil)
	month := monthOf(time.Now())

	const capMinor = 10
	var won atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := m.reserve(ctx, month, capMinor, microPerMinor)
			if err != nil {
				t.Error(err)
				return
			}
			if ok {
				won.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if won.Load() != capMinor {
		t.Errorf("%d reservations won, want exactly %d", won.Load(), capMinor)
	}
	b := mustBudget(t, m, ctx)
	if b.ReservedMicroMinor != capMinor*microPerMinor || b.SpentMicroMinor != 0 {
		t.Errorf("budget = %+v", b)
	}
}

// The same race through whole runs: however the calls interleave, what was
// spent never passes the cap, and the runs that could not reserve stop
// before calling.
func TestConcurrentRunsNeverSpendPastTheCap(t *testing.T) {
	// Each call really costs 300,000 micro-minor (3000 tokens in at 100);
	// each reserves its ceiling of 1,000,000 (4000 in at 125, 1000 out at
	// 500). A cap of 2 minor units holds two ceilings at a time.
	var calls atomic.Int32
	app, m, _ := newGateway(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(30 * time.Millisecond)
		reply(http.StatusOK, answer(smallID, goodAnswer, Usage{InputTokens: 3000}))(w, r)
	}, rewriteTask())
	ctx := context.Background()
	configure(t, app, ctx, map[string]any{"monthly_cap": 2})

	var ok, exhausted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Run(ctx, "test.rewrite", listing{Title: "x"})
			switch {
			case err == nil:
				ok.Add(1)
			case runErrOutcome(err) == OutcomeBudgetExhausted:
				exhausted.Add(1)
			default:
				t.Errorf("run: %v", err)
			}
		}()
	}
	wg.Wait()

	b := mustBudget(t, m, ctx)
	if b.SpentMicroMinor > 2*microPerMinor {
		t.Errorf("spent %d micro-minor against a cap of %d", b.SpentMicroMinor, 2*microPerMinor)
	}
	if b.ReservedMicroMinor != 0 {
		t.Errorf("%d still held after every run finished", b.ReservedMicroMinor)
	}
	if ok.Load() == 0 || exhausted.Load() == 0 {
		t.Errorf("%d ran and %d were refused; the race should produce both", ok.Load(), exhausted.Load())
	}
	if int32(b.SpentMicroMinor/300_000) != calls.Load() || calls.Load() != ok.Load() {
		t.Errorf("%d calls, %d ok runs, %d spent", calls.Load(), ok.Load(), b.SpentMicroMinor)
	}
}

func runErrOutcome(err error) string {
	if re, ok := err.(*RunError); ok {
		return re.Outcome
	}
	return ""
}

// newQuickGateway is newGateway with a short call deadline, for the calls
// that are meant to run out of it.
func newQuickGateway(t *testing.T, s *standIn, deadline time.Duration) (*gocommerce.App, *Module) {
	t.Helper()
	m := New(Config{Client: s.client(), CallTimeout: deadline, PollInterval: time.Hour})
	if err := m.RegisterTask(rewriteTask()); err != nil {
		t.Fatal(err)
	}
	return gctest.New(t, m), m
}

// The small model's ceiling: 4000 tokens in at 125, 1000 out at 500.
const smallCeiling = 4000*125 + 1000*500

// A call that was sent and then timed out may still be billed, and nothing
// came back to say what for: it is charged at its ceiling and the record
// says the figure is an estimate. With twelve retries, anything less would
// let a slow provider spend past the cap.
func TestACallThatTimesOutIsChargedAtItsCeiling(t *testing.T) {
	s := newStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	})
	app, m := newQuickGateway(t, s, 300*time.Millisecond)
	ctx := context.Background()
	configure(t, app, ctx, nil)

	res, err := m.Run(ctx, "test.rewrite", listing{Title: "x"})
	if re := runErr(t, err); re.Outcome != OutcomeProviderError || !re.Transient {
		t.Fatalf("error = %+v", re)
	}
	e := mustExecution(t, m, ctx, res.ExecutionID)
	if e.CostMicroMinor != smallCeiling || !e.CostEstimated || !e.Steps[0].Estimated {
		t.Errorf("record cost %d estimated %v (step %v), want the ceiling %d, estimated", e.CostMicroMinor, e.CostEstimated, e.Steps[0].Estimated, smallCeiling)
	}
	if b := mustBudget(t, m, ctx); b.SpentMicroMinor != smallCeiling || b.ReservedMicroMinor != 0 {
		t.Errorf("budget = %+v", b)
	}
}

// A 200 whose body cannot be read was answered, and billed, all the same.
func TestAnAnswerThatCannotBeReadIsChargedAtItsCeiling(t *testing.T) {
	s := newStandIn(t, reply(http.StatusOK, `{"id": "msg_cut`))
	app, m := newQuickGateway(t, s, 2*time.Second)
	ctx := context.Background()
	configure(t, app, ctx, nil)

	res, err := m.Run(ctx, "test.rewrite", listing{Title: "x"})
	if re := runErr(t, err); re.Outcome != OutcomeProviderError {
		t.Fatalf("error = %+v", re)
	}
	if e := mustExecution(t, m, ctx, res.ExecutionID); e.CostMicroMinor != smallCeiling || !e.CostEstimated {
		t.Errorf("record cost %d estimated %v", e.CostMicroMinor, e.CostEstimated)
	}
}

// A call that never left (the connection was refused) cost nothing, and is
// charged nothing.
func TestACallThatNeverLeftCostsNothing(t *testing.T) {
	s := newStandIn(t, reply(http.StatusOK, "{}"))
	s.srv.Close()
	app, m := newQuickGateway(t, s, 2*time.Second)
	ctx := context.Background()
	configure(t, app, ctx, nil)

	res, err := m.Run(ctx, "test.rewrite", listing{Title: "x"})
	if re := runErr(t, err); re.Outcome != OutcomeProviderError || !re.Transient {
		t.Fatalf("error = %+v", re)
	}
	if e := mustExecution(t, m, ctx, res.ExecutionID); e.CostMicroMinor != 0 || e.CostEstimated {
		t.Errorf("record cost %d estimated %v, want nothing", e.CostMicroMinor, e.CostEstimated)
	}
	if b := mustBudget(t, m, ctx); b.SpentMicroMinor != 0 || b.ReservedMicroMinor != 0 {
		t.Errorf("budget = %+v", b)
	}
}
