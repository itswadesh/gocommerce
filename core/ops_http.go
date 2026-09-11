package gocommerce

import (
	"net/http"
	"time"
)

// The store as a running system rather than as a shop.
//
// Everything here answers or acts on an operational question rather than a
// commercial one, which is why it is one file behind one right. Splitting four
// routes across the three services they touch would put store.operate in three
// files and stop it being greppable, which is how settings.write happened.
//
// Nothing here reaches for SQL. Every pass is the same service method the
// five-minute ticker calls (runSweepers), so a sweep from a browser and a sweep
// from the ticker are the same code — a state change still commits with its
// event, and there is no second state machine to keep true. The routes are
// renderers and triggers; the engine keeps the opinions.
func (a *App) mountOpsRoutes() {
	a.HandleAdminFunc("GET /api/admin/diagnostics", a.handleDiagnostics, RightStoreOperate)
	a.HandleAdminFunc("POST /api/admin/maintenance/sweep-carts", a.handleSweepCarts, RightStoreOperate)
	a.HandleAdminFunc("POST /api/admin/maintenance/sweep-unpaid", a.handleSweepUnpaid, RightStoreOperate)
	a.HandleAdminFunc("POST /api/admin/maintenance/drain-outbox", a.handleDrainOutbox, RightStoreOperate)
}

// maintenanceBudget is how long a delivery pass may keep running before it
// reports itself as partial. It is a wall clock and not a deadline on the work —
// see DrainOutboxFor for why those are not the same thing.
const maintenanceBudget = 30 * time.Second

// handleDiagnostics renders the health report.
//
// 200 even when the report says the store is unwell: the report IS the answer,
// not the error. A 503 would send the panel down its ApiError path and raise a
// toast at the one moment the screen matters most, where it should be drawing
// the rows that say what is wrong. /health/ready keeps its 503 because an
// orchestrator needs a status code; a human needs the detail.
//
// Diagnose never returns an error — a check that cannot run is itself a finding
// — so there is no error branch here at all.
func (a *App) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	Respond(w, http.StatusOK, a.withoutCauses(a.Diagnose(r.Context())))
}

// withoutCauses drops the driver text a failing check attached to its finding.
//
// RespondError scrubs every 500 so that a client never sees a pgx message or a
// query fragment, and a 200 must not become the way round that rule — least of
// all now the caller may be a browser session rather than an owner at a
// terminal. The cause goes to the log instead, which is where the person who
// can act on it already is.
//
// Warn rather than Error: the check's own status is the severity, and this line
// repeats on every poll.
func (a *App) withoutCauses(rep Report) Report {
	checks := make([]Diagnostic, len(rep.Checks))
	copy(checks, rep.Checks)
	for i := range checks {
		if checks[i].Cause == "" {
			continue
		}
		a.log.Warn("diagnostic cause withheld from the API",
			"check", checks[i].Name, "cause", checks[i].Cause)
		checks[i].Cause = ""
	}
	rep.Checks = checks
	return rep
}

// handleSweepCarts runs the three cart phases the ticker runs.
//
// Ungated deliberately, unlike the drain: each phase claims its rows with FOR
// UPDATE SKIP LOCKED, so two simultaneous passes take different baskets rather
// than fighting over the same ones. One shared gate across all three would
// refuse a cart sweep while a delivery pass was running, which is a worse lie
// than any cost concurrency here has.
func (a *App) handleSweepCarts(w http.ResponseWriter, r *http.Request) {
	swept, err := a.carts.SweepPass(r.Context())
	if err != nil {
		RespondError(w, r, Internalf(err, "could not sweep the carts"))
		return
	}
	a.log.Info("carts swept on request",
		"abandoned", swept.Abandoned, "removed", swept.Removed, "purged", swept.Purged)
	Respond(w, http.StatusOK, swept)
}

// handleSweepUnpaid releases stock held by orders nobody will pay for.
//
// capped is derived from what the pass FOUND, not from what it managed to
// cancel: an order the service could not cancel is logged and skipped, so
// keying this to the cancellation count would report "queue clear" in exactly
// the backlogged-and-partly-broken state the button exists for.
//
// One pass, not a loop. Each cancellation is its own transaction with its own
// event, and the ticker comes round in five minutes. Cancel is idempotent on an
// order somebody else has already cancelled, so a press racing the ticker
// under-counts rather than erroring — the stock and the events stay right
// either way, and the diagnostics row refreshed beside the button still tells
// the truth.
func (a *App) handleSweepUnpaid(w http.ResponseWriter, r *http.Request) {
	cancelled, scanned, err := a.orders.SweepUnpaidPass(r.Context())
	if err != nil {
		RespondError(w, r, Internalf(err, "could not sweep unpaid orders"))
		return
	}
	a.log.Info("unpaid orders swept on request", "cancelled", cancelled, "scanned", scanned)
	Respond(w, http.StatusOK, map[string]any{
		"cancelled": cancelled,
		"capped":    scanned >= sweepUnpaidBatch,
	})
}

// handleDrainOutbox forces a delivery pass.
//
// The gate refuses rather than queues because every delivery is a handler doing
// network I/O: waiting in line would turn N tabs into N sequential 30-second
// passes against a vendor that is, by hypothesis, already slow. A 409 says so
// and the panel renders the message; the taxonomy already has one and nothing
// new is invented here.
//
// delivered counts rows claimed, not rows a handler accepted. A failed handler
// leaves its row for the dispatcher's backoff, which is what at-least-once
// means. And a drain does not resurrect dead-lettered rows — deliverBatch's
// claim filters NOT dead — so this button does not claim to fix them; that is
// why the outbox check's own hint points at last_error, and RequeueEvent is
// what un-parks one.
//
// There is no errors.Is classification anywhere: DrainOutboxFor reports a spent
// budget as capped and the request's own cancellation cannot reach the delivery
// work at all, so an error arriving here is a real database failure.
func (a *App) handleDrainOutbox(w http.ResponseWriter, r *http.Request) {
	if !a.drainInFlight.CompareAndSwap(false, true) {
		RespondError(w, r, Conflictf("a delivery pass is already running; wait for it to finish"))
		return
	}
	defer a.drainInFlight.Store(false)

	delivered, capped, err := a.DrainOutboxFor(r.Context(), maintenanceBudget)
	if err != nil {
		RespondError(w, r, Internalf(err, "could not drain the outbox"))
		return
	}
	a.log.Info("outbox drained on request", "delivered", delivered, "capped", capped)
	Respond(w, http.StatusOK, map[string]any{"delivered": delivered, "capped": capped})
}
