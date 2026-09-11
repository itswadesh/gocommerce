package gocommerce

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// decodeReport reads the health report out of the standard envelope.
func decodeReport(t *testing.T, body string) Report {
	t.Helper()
	var env struct {
		Data Report `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode the report: %v\n%s", err, body)
	}
	return env.Data
}

// opsData reads a maintenance route's data object.
func opsData(t *testing.T, body string) map[string]any {
	t.Helper()
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode the response: %v\n%s", err, body)
	}
	return env.Data
}

func opsNumber(t *testing.T, data map[string]any, key string) float64 {
	t.Helper()
	v, ok := data[key].(float64)
	if !ok {
		t.Fatalf("%q is %#v, want a number", key, data[key])
	}
	return v
}

// The route is a renderer, not a second opinion: no handler re-decides what
// healthy means, filters a check out or re-orders them.
func TestDiagnosticsRouteRendersTheSameReport(t *testing.T) {
	app := newTestApp(t)

	direct := app.Diagnose(context.Background())

	rec := do(t, app, "GET", "/api/admin/diagnostics", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	served := decodeReport(t, rec.Body.String())

	if len(served.Checks) != len(direct.Checks) {
		t.Fatalf("the route returned %d checks, the service %d",
			len(served.Checks), len(direct.Checks))
	}
	for i := range direct.Checks {
		if served.Checks[i].Name != direct.Checks[i].Name {
			t.Errorf("check %d is %q over HTTP and %q from the service",
				i, served.Checks[i].Name, direct.Checks[i].Name)
		}
		switch served.Checks[i].Status {
		case StatusOK, StatusWarn, StatusFail:
		default:
			t.Errorf("check %q has status %q, which is not one of ok/warn/fail",
				served.Checks[i].Name, served.Checks[i].Status)
		}
		if served.Checks[i].Detail == "" {
			t.Errorf("check %q reports no detail", served.Checks[i].Name)
		}
	}
	if served.Version != Version {
		t.Errorf("version = %q, want %q", served.Version, Version)
	}
}

// An unhealthy store is a report, not an error envelope. The screen has to keep
// rendering at the one moment it matters most.
func TestDiagnosticsAnswers200WhenTheStoreIsUnwell(t *testing.T) {
	app := newTestApp(t)

	// Forget a shipped migration, which is what checkMigrations calls a failure.
	if _, err := app.DB().ExecContext(context.Background(),
		`DELETE FROM `+migrationsTable+` WHERE id = '0005_superusers'`); err != nil {
		t.Fatalf("forget a migration: %v", err)
	}

	rec := do(t, app, "GET", "/api/admin/diagnostics", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — an unwell store must still render: %s",
			rec.Code, rec.Body.String())
	}
	rep := decodeReport(t, rec.Body.String())
	if rep.OK {
		t.Error("ok is true with a migration missing")
	}
	var migrations *Diagnostic
	for i := range rep.Checks {
		if rep.Checks[i].Name == "migrations" {
			migrations = &rep.Checks[i]
		}
	}
	if migrations == nil {
		t.Fatal("no migrations check in the report")
	}
	if migrations.Status != StatusFail {
		t.Errorf("migrations = %q, want fail", migrations.Status)
	}
	if migrations.Hint == "" {
		t.Error("a failing check must say what to do about it")
	}
}

// The one thing httpx.go refuses to do on every other path must not be
// reachable by way of a 200.
func TestDiagnosticsWithholdsTheDriverCause(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	// Take the outbox out from under checkOutbox, in this test's own schema, so
	// PendingEvents fails and the check records why.
	if _, err := app.DB().ExecContext(ctx,
		`ALTER TABLE outbox_events RENAME TO outbox_events_hidden`); err != nil {
		t.Fatalf("hide the outbox: %v", err)
	}
	t.Cleanup(func() {
		_, _ = app.DB().ExecContext(context.Background(),
			`ALTER TABLE outbox_events_hidden RENAME TO outbox_events`)
	})

	direct := app.Diagnose(ctx)
	var cause, detail string
	for _, c := range direct.Checks {
		if c.Name == "outbox" {
			cause, detail = c.Cause, c.Detail
		}
	}
	if cause == "" {
		t.Fatalf("the service did not record a cause for the broken outbox check (detail %q)", detail)
	}
	if strings.Contains(detail, cause) {
		t.Errorf("the detail still carries the driver text: %q", detail)
	}

	rec := do(t, app, "GET", "/api/admin/diagnostics", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The raw body, not the decoded struct: the assertion is that the key is
	// absent from the wire, not merely empty after decoding.
	if strings.Contains(body, `"cause"`) {
		t.Errorf("the response carries a cause key:\n%s", body)
	}
	if strings.Contains(body, cause) {
		t.Errorf("the response carries the driver text %q:\n%s", cause, body)
	}
	served := decodeReport(t, body)
	for _, c := range served.Checks {
		if c.Name == "outbox" && c.Detail != detail {
			t.Errorf("outbox detail over HTTP = %q, want the plain sentence %q", c.Detail, detail)
		}
	}
}

// The twenty-first right is re-cuttable exactly like the other twenty and is
// special-cased nowhere.
func TestDiagnosticsNeedsStoreOperate(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	staff := signInAs(t, app, "ops-staff@example.com", RoleStaff)
	manager := signInAs(t, app, "ops-manager@example.com", RoleManager)
	owner := signInAs(t, app, "ops-owner@example.com", RoleOwner)

	// Owner alone carries it out of the box — the default this engine ships,
	// and the reason the roles matrix is where a store says otherwise.
	if rec := do(t, app, "GET", "/api/admin/diagnostics", bearer(staff)); rec.Code != http.StatusForbidden {
		t.Errorf("staff got %d, want 403", rec.Code)
	} else if body := rec.Body.String(); !strings.Contains(body, "does not carry") ||
		!strings.Contains(body, string(RightStoreOperate)) {
		t.Errorf("the refusal does not name store.operate: %s", body)
	}
	if rec := do(t, app, "GET", "/api/admin/diagnostics", bearer(manager)); rec.Code != http.StatusForbidden {
		t.Errorf("manager got %d, want 403 — store.operate is not a manager default", rec.Code)
	}
	if rec := do(t, app, "GET", "/api/admin/diagnostics", bearer(owner)); rec.Code != http.StatusOK {
		t.Errorf("owner got %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// And a store that wants a manager on call grants it in the matrix, which
	// is the whole reason the default is narrow rather than absent.
	granted := append(DefaultRightsOf(RoleManager), RightStoreOperate)
	if _, err := app.Roles().Set(ctx, RoleManager, granted, nil); err != nil {
		t.Fatalf("grant store.operate to manager: %v", err)
	}
	// Rights resolve on each authentication, so the same session sees it.
	if rec := do(t, app, "GET", "/api/admin/diagnostics", bearer(manager)); rec.Code != http.StatusOK {
		t.Errorf("manager got %d after being granted store.operate: %s", rec.Code, rec.Body.String())
	}
}

// The panel's Run-now buttons are keyed to these literal check names, and this
// is the only thing anywhere that couples the two vocabularies: Go cannot see
// the panel, and check-docs.ps1 validates routes and method names, not checks.
func TestDiagnosticsNamesTheChecksThePanelActsOn(t *testing.T) {
	app := newTestApp(t)

	have := map[string]bool{}
	for _, c := range app.Diagnose(context.Background()).Checks {
		have[c.Name] = true
	}
	for _, name := range []string{"outbox", "stock reservations", "carts"} {
		if !have[name] {
			t.Errorf("no check named %q: admin/src/routes/settings/diagnostics/+page.svelte "+
				"keys its Run-now button to that exact string, so renaming the check "+
				"silently removes the button", name)
		}
	}
}

// The route is the service, not new SQL, and pressing the button twice is
// harmless — which is the case an operator will actually produce.
func TestSweepUnpaidRouteReleasesStock(t *testing.T) {
	app := newTestApp(t, &gatewayModule{})
	ctx := context.Background()

	product := simpleProduct(t, app, "OPS-SWEEP-1", 1000, 4)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 3)
	result, err := app.Order().Checkout(ctx, "testgateway", checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE orders SET reservation_expires_at = now() - interval '1 hour' WHERE id = $1`,
		result.Order.ID); err != nil {
		t.Fatalf("age the order: %v", err)
	}

	rec := do(t, app, "POST", "/api/admin/maintenance/sweep-unpaid", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	data := opsData(t, rec.Body.String())
	if got := opsNumber(t, data, "cancelled"); got != 1 {
		t.Errorf("cancelled = %v, want 1", got)
	}
	if data["capped"] != false {
		t.Errorf("capped = %v, want false on a single expired order", data["capped"])
	}

	if onHand, reserved := variantStock(t, app, product.DefaultVariant().ID); onHand != 4 || reserved != 0 {
		t.Errorf("stock after the sweep = (%d, %d), want (4, 0)", onHand, reserved)
	}
	order, err := app.Order().Get(ctx, result.Order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if order.Status != OrderCancelled {
		t.Errorf("status = %q, want cancelled", order.Status)
	}

	rec = do(t, app, "POST", "/api/admin/maintenance/sweep-unpaid", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("second press: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := opsNumber(t, opsData(t, rec.Body.String()), "cancelled"); got != 0 {
		t.Errorf("second press cancelled = %v, want 0", got)
	}
}

// capped is derived from what the pass FOUND, never from what it cancelled: a
// count-derived flag would report "queue clear" in exactly the backlogged state
// the button exists for.
func TestSweepUnpaidReportsAFullBatchFromWhatItFound(t *testing.T) {
	app := newTestApp(t, &gatewayModule{})
	ctx := context.Background()

	// sweepUnpaidBatch is a var for precisely this: reaching the capped path
	// without building a two-hundred-order backlog.
	restore := sweepUnpaidBatch
	sweepUnpaidBatch = 1
	t.Cleanup(func() { sweepUnpaidBatch = restore })

	product := simpleProduct(t, app, "OPS-SWEEP-2", 1000, 10)
	for i := 0; i < 2; i++ {
		cart := newCart(t, app)
		addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
		result, err := app.Order().Checkout(ctx, "testgateway", checkoutInput(cart.Token), "")
		if err != nil {
			t.Fatalf("checkout %d: %v", i, err)
		}
		if _, err := app.DB().ExecContext(ctx,
			`UPDATE orders SET reservation_expires_at = now() - interval '1 hour' WHERE id = $1`,
			result.Order.ID); err != nil {
			t.Fatalf("age order %d: %v", i, err)
		}
	}

	rec := do(t, app, "POST", "/api/admin/maintenance/sweep-unpaid", withAdmin)
	data := opsData(t, rec.Body.String())
	if got := opsNumber(t, data, "cancelled"); got != 1 {
		t.Errorf("cancelled = %v, want 1 with a batch of one", got)
	}
	if data["capped"] != true {
		t.Errorf("capped = %v, want true: the pass filled its batch and there is more", data["capped"])
	}

	rec = do(t, app, "POST", "/api/admin/maintenance/sweep-unpaid", withAdmin)
	data = opsData(t, rec.Body.String())
	if got := opsNumber(t, data, "cancelled"); got != 1 {
		t.Errorf("second pass cancelled = %v, want the remaining 1", got)
	}
	if data["capped"] != true {
		t.Errorf("second pass capped = %v, want true — it also filled a batch of one", data["capped"])
	}

	rec = do(t, app, "POST", "/api/admin/maintenance/sweep-unpaid", withAdmin)
	data = opsData(t, rec.Body.String())
	if data["capped"] != false {
		t.Errorf("third pass capped = %v, want false: nothing left to find", data["capped"])
	}
}

// The route runs all three cart phases the ticker runs, and the check the
// button sits beside agrees about what was reclaimed.
func TestSweepCartsRouteRunsTheThreePhases(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "OPS-CART-1", 1000, 5)

	live := newCart(t, app)
	empty := newCart(t, app)
	ageCart(t, app, empty.Token)
	filled := newCart(t, app)
	addToCart(t, app, filled.Token, product.DefaultVariant().ID, 1)
	ageCart(t, app, filled.Token)

	rec := do(t, app, "POST", "/api/admin/maintenance/sweep-carts", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	data := opsData(t, rec.Body.String())
	if got := opsNumber(t, data, "abandoned"); got != 1 {
		t.Errorf("abandoned = %v, want the one filled cart", got)
	}
	if got := opsNumber(t, data, "removed"); got != 1 {
		t.Errorf("removed = %v, want the one empty cart", got)
	}
	if got := opsNumber(t, data, "purged"); got != 0 {
		t.Errorf("purged = %v, want 0 — nothing is past retention yet", got)
	}
	if data["capped"] != false {
		t.Errorf("capped = %v, want false", data["capped"])
	}

	if _, err := app.Cart().GetByToken(ctx, empty.Token); err == nil {
		t.Error("the empty expired cart survived the sweep")
	}
	if _, err := app.Cart().GetByToken(ctx, live.Token); err != nil {
		t.Errorf("the live cart was swept: %v", err)
	}

	// The diagnostic the button sits beside has to say the same thing.
	after := app.Diagnose(ctx)
	var carts *Diagnostic
	for i := range after.Checks {
		if after.Checks[i].Name == "carts" {
			carts = &after.Checks[i]
		}
	}
	if carts == nil {
		t.Fatal("no carts check in the report")
	}
	if !strings.Contains(carts.Detail, "1 live") || !strings.Contains(carts.Detail, "0 awaiting") {
		t.Errorf("the carts check says %q, want 1 live and 0 awaiting the sweeper", carts.Detail)
	}
}

// The one pass that used to have no ceiling can now say it did not finish.
func TestSweepCartsReportsAFullBatch(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		ageCart(t, app, newCart(t, app).Token)
	}

	// cartSweepBatch is a var for precisely this: reaching the capped path
	// without building a five-hundred-cart backlog.
	restore := cartSweepBatch
	cartSweepBatch = 1
	t.Cleanup(func() { cartSweepBatch = restore })

	removed, capped, err := app.Cart().SweepExpiredPass(ctx)
	if err != nil {
		t.Fatalf("SweepExpiredPass: %v", err)
	}
	if removed != 1 || !capped {
		t.Fatalf("pass = (%d, %v), want (1, true) under a batch of one", removed, capped)
	}

	rec := do(t, app, "POST", "/api/admin/maintenance/sweep-carts", withAdmin)
	data := opsData(t, rec.Body.String())
	if got := opsNumber(t, data, "removed"); got != 1 {
		t.Errorf("removed = %v, want the remaining 1", got)
	}
	if data["capped"] != true {
		t.Errorf("capped = %v, want true: the pass filled its batch", data["capped"])
	}

	rec = do(t, app, "POST", "/api/admin/maintenance/sweep-carts", withAdmin)
	if data := opsData(t, rec.Body.String()); data["capped"] != false {
		t.Errorf("capped = %v after the backlog cleared, want false", data["capped"])
	}
}

// The route makes delivery happen on demand rather than reimplementing it.
func TestDrainOutboxRouteDeliversPendingEvents(t *testing.T) {
	var seen atomic.Int64
	app := newTestApp(t, &gatewayModule{})
	app.Subscribe(EventOrderCreated, func(context.Context, Event) error {
		seen.Add(1)
		return nil
	})
	ctx := context.Background()

	product := simpleProduct(t, app, "OPS-DRAIN-1", 1000, 3)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	if _, err := app.Order().Checkout(ctx, "testgateway", checkoutInput(cart.Token), ""); err != nil {
		t.Fatalf("checkout: %v", err)
	}

	// The dispatcher only runs under ListenAndServe, so a test app leaves the
	// rows pending — which is what makes this route observable at all.
	pending, _, err := app.PendingEvents(ctx)
	if err != nil {
		t.Fatalf("PendingEvents: %v", err)
	}
	if pending == 0 {
		t.Fatal("no backlog to drain; the fixture is not doing its job")
	}

	rec := do(t, app, "POST", "/api/admin/maintenance/drain-outbox", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	data := opsData(t, rec.Body.String())
	if got := opsNumber(t, data, "delivered"); got <= 0 {
		t.Errorf("delivered = %v, want the backlog", got)
	}
	if data["capped"] != false {
		t.Errorf("capped = %v, want false — the pass came back empty inside its budget", data["capped"])
	}
	if seen.Load() == 0 {
		t.Error("the subscriber never saw the event")
	}
	if pending, _, err := app.PendingEvents(ctx); err != nil || pending != 0 {
		t.Errorf("pending after the drain = %d (err %v), want 0", pending, err)
	}
}

// The blocker's regression test. With a deadline on the delivery context, the
// rows a spent pass had claimed and not reached came back with attempts
// incremented and a "context deadline exceeded" last_error — twelve of which
// park a healthy event as dead.
func TestDrainBudgetDoesNotBurnRetries(t *testing.T) {
	dsn := requireDB(t)
	cfg := testConfig(dsn)
	cfg.OutboxBatchSize = 1
	app, err := New(cfg, &gatewayModule{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	ctx := context.Background()

	product := simpleProduct(t, app, "OPS-DRAIN-2", 1000, 9)
	for i := 0; i < 3; i++ {
		cart := newCart(t, app)
		addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
		if _, err := app.Order().Checkout(ctx, "testgateway", checkoutInput(cart.Token), ""); err != nil {
			t.Fatalf("checkout %d: %v", i, err)
		}
	}
	pendingBefore, _, err := app.PendingEvents(ctx)
	if err != nil {
		t.Fatalf("PendingEvents: %v", err)
	}
	if pendingBefore < 3 {
		t.Fatalf("only %d events pending; the fixture needs at least three", pendingBefore)
	}

	// A budget of zero: one pass runs whole, then the clock refuses the next.
	delivered, capped, err := app.DrainOutboxFor(ctx, 0)
	if err != nil {
		t.Fatalf("DrainOutboxFor: %v", err)
	}
	if delivered != 1 {
		t.Errorf("delivered = %d, want exactly the one batch the pass had started", delivered)
	}
	if !capped {
		t.Error("capped = false; a spent budget with work left is a partial pass")
	}

	// Everything the pass never reached must be untouched: no attempt spent, no
	// error recorded, nothing parked.
	var attempts, dead, withError int
	if err := app.DB().QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE attempts > 0),
		       count(*) FILTER (WHERE dead),
		       count(*) FILTER (WHERE last_error IS NOT NULL)
		FROM outbox_events
		WHERE published_at IS NULL`).Scan(&attempts, &dead, &withError); err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	if attempts != 0 || dead != 0 || withError != 0 {
		t.Errorf("unreached rows: %d with attempts, %d dead, %d carrying last_error — "+
			"want all zero; a budget must never burn a retry on an event it did not dispatch",
			attempts, dead, withError)
	}
}

// context.WithoutCancel doing its job: an operator navigating away mid-drain
// must not be able to corrupt delivery state.
func TestDrainSurvivesACancelledRequest(t *testing.T) {
	var seen atomic.Int64
	app := newTestApp(t, &gatewayModule{})
	app.Subscribe(EventOrderCreated, func(context.Context, Event) error {
		seen.Add(1)
		return nil
	})

	product := simpleProduct(t, app, "OPS-DRAIN-3", 1000, 3)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	if _, err := app.Order().Checkout(context.Background(), "testgateway",
		checkoutInput(cart.Token), ""); err != nil {
		t.Fatalf("checkout: %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	delivered, capped, err := app.DrainOutboxFor(cancelled, time.Minute)
	if err != nil {
		t.Fatalf("DrainOutboxFor on a cancelled context: %v", err)
	}
	if delivered == 0 {
		t.Error("nothing was delivered; the request's cancellation reached the delivery work")
	}
	if capped {
		t.Error("capped = true with a minute of budget")
	}
	if seen.Load() == 0 {
		t.Error("the subscriber never ran")
	}
	if pending, _, err := app.PendingEvents(context.Background()); err != nil || pending != 0 {
		t.Errorf("pending after the drain = %d (err %v), want 0", pending, err)
	}
}

// The gate refuses rather than queues, which is the whole reason it exists: N
// tabs against a wedged vendor is the state this button is pressed in.
func TestDrainOutboxRefusesASecondPass(t *testing.T) {
	inside := make(chan struct{})
	release := make(chan struct{})
	var once atomic.Bool

	app := newTestApp(t, &gatewayModule{})
	app.Subscribe(EventOrderCreated, func(context.Context, Event) error {
		if once.CompareAndSwap(false, true) {
			close(inside)
			<-release
		}
		return nil
	})

	product := simpleProduct(t, app, "OPS-DRAIN-4", 1000, 3)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	if _, err := app.Order().Checkout(context.Background(), "testgateway",
		checkoutInput(cart.Token), ""); err != nil {
		t.Fatalf("checkout: %v", err)
	}

	first := make(chan int, 1)
	go func() {
		rec := do(t, app, "POST", "/api/admin/maintenance/drain-outbox", withAdmin)
		first <- rec.Code
	}()

	select {
	case <-inside:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("the first drain never reached a handler")
	}

	rec := do(t, app, "POST", "/api/admin/maintenance/drain-outbox", withAdmin)
	if rec.Code != http.StatusConflict {
		t.Errorf("second press got %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `"conflict"`) ||
		!strings.Contains(body, "already running") {
		t.Errorf("the refusal does not explain itself: %s", body)
	}

	close(release)
	select {
	case code := <-first:
		if code != http.StatusOK {
			t.Errorf("the first drain finished with %d, want 200", code)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the first drain never finished")
	}
}

// Cheap, and the assertion that fails if one of these is ever mounted with
// HandleFunc instead of HandleAdminFunc.
func TestMaintenanceRefusesAnUnauthenticatedRequest(t *testing.T) {
	app := newTestApp(t)

	cases := []struct{ method, path string }{
		{"GET", "/api/admin/diagnostics"},
		{"POST", "/api/admin/maintenance/sweep-carts"},
		{"POST", "/api/admin/maintenance/sweep-unpaid"},
		{"POST", "/api/admin/maintenance/drain-outbox"},
	}
	for _, c := range cases {
		rec := do(t, app, c.method, c.path)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", c.method, c.path, rec.Code)
			continue
		}
		if rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s %s answered 401 with no WWW-Authenticate header", c.method, c.path)
		}
		if !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s %s did not answer in the envelope: %s", c.method, c.path, rec.Body.String())
		}
	}
}

// The panel glosses every right in one file and nothing compiles it, so this is
// what keeps admin/src/lib/rights.js from drifting away from AllRights — the
// guarantee that file's own header already claims.
func TestPanelRightsTableMatchesTheEngine(t *testing.T) {
	source, err := readPanelRights()
	if err != nil {
		t.Skipf("admin/src/lib/rights.js is not readable here: %v", err)
	}

	for _, table := range []string{"RIGHT_ORDER", "RIGHT_LABELS", "RIGHT_SCOPES"} {
		listed, err := panelRightsIn(source, table)
		if err != nil {
			t.Errorf("%s: %v", table, err)
			continue
		}
		if len(listed) != len(AllRights) {
			t.Errorf("%s names %d rights, the engine has %d", table, len(listed), len(AllRights))
		}
		have := map[string]bool{}
		for _, r := range listed {
			have[r] = true
		}
		for _, right := range AllRights {
			if !have[string(right)] {
				t.Errorf("%s does not name %q; the panel would render it as a bare identifier",
					table, right)
			}
		}
		known := map[string]bool{}
		for _, right := range AllRights {
			known[string(right)] = true
		}
		for _, r := range listed {
			if !known[r] {
				t.Errorf("%s names %q, which the engine does not have", table, r)
			}
		}
	}

	// RIGHT_ORDER is documented as the engine's own order, which is what the
	// roles matrix draws its rows in.
	order, err := panelRightsIn(source, "RIGHT_ORDER")
	if err == nil {
		for i := range AllRights {
			if i < len(order) && order[i] != string(AllRights[i]) {
				t.Errorf("RIGHT_ORDER[%d] = %q, the engine's AllRights[%d] = %q",
					i, order[i], i, AllRights[i])
				break
			}
		}
	}
}

// readPanelRights loads the panel's rights table from the source tree.
//
// A relative path because the Go test's working directory is core/ and the
// panel is a sibling. It is allowed not to be there — an API-only checkout has
// no admin/ — which is why the caller skips rather than fails.
func readPanelRights() (string, error) {
	b, err := os.ReadFile(filepath.Join("..", "admin", "src", "lib", "rights.js"))
	return string(b), err
}

// panelRightsIn pulls the dotted right names out of one table in rights.js.
//
// A text scan rather than a parser: the file is three flat literals and the
// alternative is a JavaScript runtime in the Go test suite.
func panelRightsIn(source, table string) ([]string, error) {
	start := strings.Index(source, table+" = ")
	if start < 0 {
		return nil, fmt.Errorf("not found in admin/src/lib/rights.js")
	}
	rest := source[start:]
	// Whichever terminator comes first: RIGHT_ORDER is an array and the other
	// two are objects, and taking one kind by preference would run an array
	// straight on into the object below it.
	end := -1
	for _, close := range []string{"\n};", "\n];"} {
		if at := strings.Index(rest, close); at >= 0 && (end < 0 || at < end) {
			end = at
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("could not find the end of the table")
	}
	var out []string
	for _, line := range strings.Split(rest[:end], "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, `"`) {
			continue
		}
		name := line[1:]
		q := strings.IndexByte(name, '"')
		if q < 0 {
			continue
		}
		if name = name[:q]; strings.Contains(name, ".") {
			out = append(out, name)
		}
	}
	return out, nil
}
