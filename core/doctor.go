package gocommerce

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Operational diagnostics.
//
// This exists because the questions an operator asks at 2am — "is the outbox
// stuck?", "can anyone still sign in?", "is stock pinned by orders nobody is
// going to pay for?" — are answerable from the database, but only if you know
// which queries to run. Diagnose knows them.
//
// It is a core service rather than a CLI feature so that everything can reach
// it: `gocommerce doctor` renders it, an MCP tool can call it, and the panel's
// Settings → Diagnostics screen draws it over GET /api/admin/diagnostics. The
// CLI is a client, like everything else.

// Status is a diagnostic's verdict.
const (
	// StatusOK means the check passed and needs no attention.
	StatusOK = "ok"
	// StatusWarn means something is worth looking at but the store is serving.
	StatusWarn = "warn"
	// StatusFail means something is broken or about to be.
	StatusFail = "fail"
)

// Diagnostic is one check's result.
type Diagnostic struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	// Hint says what to do about it. Every failing check has one — a
	// diagnostic that reports a problem without naming a next step just moves
	// the puzzle.
	Hint string `json:"hint,omitempty"`
	// Cause is the underlying error text, when a check reports a problem
	// because it could not run at all.
	//
	// It is its own field rather than the tail of Detail because the two have
	// different audiences. A local operator at a terminal wants it, and so does
	// an agent reading `gocommerce doctor -json`. A browser session must not
	// have it: a pgx error names a host, a port and a user, and httpx.go
	// refuses to send those to a client on every other path — RespondError
	// scrubs every 500 for exactly this reason, and a 200 must not become the
	// way round it. Splitting the finding from its cause lets one HTTP handler
	// blank one field and keep the rest, so a check added later cannot leak by
	// forgetting a rule it was never told about.
	Cause string `json:"cause,omitempty"`
}

// Report is the whole health picture.
type Report struct {
	Version string       `json:"version"`
	At      time.Time    `json:"at"`
	OK      bool         `json:"ok"`
	Checks  []Diagnostic `json:"checks"`
}

// Failed returns the checks that did not pass, worst first.
func (r Report) Failed() []Diagnostic {
	var out []Diagnostic
	for _, c := range r.Checks {
		if c.Status != StatusOK {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Status == StatusFail && out[j].Status != StatusFail
	})
	return out
}

// Diagnose runs every check and reports what it found.
//
// It never returns an error: a check that cannot run is itself a finding, and
// an operator asking "what is wrong" should never be answered with one problem
// when there might be six.
func (a *App) Diagnose(ctx context.Context) Report {
	rep := Report{Version: Version, At: time.Now().UTC()}
	add := func(d Diagnostic) { rep.Checks = append(rep.Checks, d) }

	add(a.checkDatabase(ctx))
	add(a.checkMigrations(ctx))
	add(a.checkAdminAccess(ctx))
	add(a.checkOutbox(ctx))
	add(a.checkReservations(ctx))
	add(a.checkCarts(ctx))
	add(a.checkCatalog(ctx))
	add(a.checkProviders())
	add(a.checkContract())
	add(a.checkAdminRights())
	add(a.checkFulfillment(ctx))
	add(a.checkRefunds(ctx))
	add(a.checkReturns(ctx))
	add(a.checkLedger(ctx))
	add(a.checkDiscounts(ctx))

	rep.OK = true
	for _, c := range rep.Checks {
		if c.Status == StatusFail {
			rep.OK = false
		}
	}
	return rep
}

func (a *App) checkDatabase(ctx context.Context) Diagnostic {
	d := Diagnostic{Name: "database"}
	var version string
	if err := a.db.QueryRowContext(ctx, `SHOW server_version`).Scan(&version); err != nil {
		d.Status, d.Detail, d.Cause = StatusFail, "cannot reach PostgreSQL", err.Error()
		d.Hint = "check the connection string and that the server is accepting connections"
		return d
	}
	stats := a.db.Stats()
	d.Status = StatusOK
	d.Detail = fmt.Sprintf("PostgreSQL %s, %d/%d connections in use", version, stats.InUse, stats.MaxOpenConnections)
	// A pool pinned at its ceiling is the shape of a leak or of genuine
	// saturation, and both show up here before they show up as timeouts.
	if stats.MaxOpenConnections > 0 && stats.InUse >= stats.MaxOpenConnections {
		d.Status = StatusWarn
		d.Hint = "every pooled connection is checked out; look for long transactions"
	}
	return d
}

func (a *App) checkMigrations(ctx context.Context) Diagnostic {
	d := Diagnostic{Name: "migrations"}

	applied := map[string]bool{}
	rows, err := a.db.QueryContext(ctx, `SELECT owner, id FROM `+migrationsTable)
	if err != nil {
		d.Status, d.Detail, d.Cause = StatusFail, "cannot read the migration ledger", err.Error()
		d.Hint = "run `gocommerce migrate`"
		return d
	}
	for rows.Next() {
		var owner, id string
		if err := rows.Scan(&owner, &id); err == nil {
			applied[owner+"/"+id] = true
		}
	}
	rows.Close()

	var pending []string
	for _, set := range a.migrationSets() {
		for _, m := range set.Migrations {
			if key := set.Owner + "/" + m.ID; !applied[key] {
				pending = append(pending, key)
			}
		}
	}
	if len(pending) > 0 {
		d.Status = StatusFail
		d.Detail = fmt.Sprintf("%d migration(s) not applied: %s", len(pending), strings.Join(pending, ", "))
		d.Hint = "run `gocommerce migrate`"
		return d
	}
	d.Status = StatusOK
	d.Detail = fmt.Sprintf("%d applied, none pending", len(applied))
	return d
}

// checkAdminAccess answers the question a locked-out operator is actually
// asking. Either credential is enough on its own; having neither means the
// admin API is unreachable by anyone, which no amount of uptime compensates
// for.
func (a *App) checkAdminAccess(ctx context.Context) Diagnostic {
	d := Diagnostic{Name: "admin access"}

	tokens := len(a.cfg.AdminTokens)
	supers, err := a.superusers.Count(ctx)
	if err != nil {
		d.Status, d.Detail, d.Cause = StatusWarn, "cannot count superusers", err.Error()
		return d
	}

	switch {
	case supers == 0 && tokens == 0:
		d.Status = StatusFail
		d.Detail = "no superusers and no admin tokens — nobody can administer this store"
		d.Hint = "run `gocommerce superuser create <email> <password>`, or start with -admin-token"
	case supers == 0:
		d.Status = StatusWarn
		d.Detail = fmt.Sprintf("no superusers; %d admin token(s) configured", tokens)
		d.Hint = "the panel needs a superuser: `gocommerce superuser create <email> <password>`"
	default:
		d.Status = StatusOK
		d.Detail = fmt.Sprintf("%d superuser(s), %d admin token(s)", supers, tokens)
	}
	// The same question this check already asks, one step further: an operator
	// who has forgotten their password is locked out unless a link can reach
	// them, and both of these configurations look healthy from every other angle.
	if d.Status == StatusOK {
		switch {
		case !a.notifier.delivers(ChannelEmail):
			d.Status = StatusWarn
			d.Detail += "; no email delivery, so a locked-out operator cannot reset their own password"
			d.Hint = "register an email notifier (ext/notify-sendgrid) and set Config.PanelURL, " +
				"or recover with `gocommerce superuser update <email> <password>`"
		case a.cfg.PanelURL == "":
			d.Status = StatusWarn
			d.Detail += "; Config.PanelURL is unset, so a reset email carries a bare code instead of a link"
			d.Hint = "set Config.PanelURL to where the panel is reached, e.g. https://shop.example.com"
		}
	}
	// Config.Dev is deliberately not reported here. It is only dangerous while
	// serving, and the CLI sets it for its own offline commands — including
	// this one — so a warning keyed on it would fire on every doctor run and
	// mean nothing. ListenAndServe is where that belongs.
	return d
}

// checkOutbox is the most load-bearing check here. The outbox is what makes
// events durable, and its failure mode is silent: deliveries stop, state keeps
// committing, and nothing surfaces until someone notices the emails stopped.
func (a *App) checkOutbox(ctx context.Context) Diagnostic {
	d := Diagnostic{Name: "outbox"}

	pending, dead, err := a.PendingEvents(ctx)
	if err != nil {
		d.Status, d.Detail, d.Cause = StatusFail, "cannot read the outbox", err.Error()
		return d
	}

	var oldest sql.NullTime
	_ = a.db.QueryRowContext(ctx, `
		SELECT min(created_at) FROM outbox_events
		WHERE published_at IS NULL AND NOT dead`).Scan(&oldest)

	parts := []string{fmt.Sprintf("%d pending, %d dead-lettered", pending, dead)}
	d.Status = StatusOK

	if oldest.Valid {
		age := time.Since(oldest.Time)
		parts = append(parts, fmt.Sprintf("oldest unpublished %s", age.Round(time.Second)))
		// The dispatcher polls every second and backs off exponentially on
		// failure. Minutes of backlog means it is failing, not busy.
		if age > 5*time.Minute {
			d.Status = StatusFail
			d.Hint = "the dispatcher is not draining; check handler errors in the log"
		} else if age > 30*time.Second {
			d.Status = StatusWarn
			d.Hint = "backlog is growing; check whether a handler is slow or erroring"
		}
	}
	if dead > 0 && d.Status == StatusOK {
		d.Status = StatusWarn
		d.Hint = "dead-lettered events exhausted their retries and will never be delivered; read them at GET /api/admin/events?state=dead, or Settings > Platform > Events, and retry when the cause is fixed"
	}
	d.Detail = strings.Join(parts, ", ")
	return d
}

// checkReservations looks for stock held by orders that will never be paid.
// A reservation outliving its order is how a store silently goes out of stock
// while the shelves are full.
func (a *App) checkReservations(ctx context.Context) Diagnostic {
	d := Diagnostic{Name: "stock reservations"}

	var stale int
	var units sql.NullInt64
	err := a.db.QueryRowContext(ctx, `
		SELECT count(DISTINCT o.id), sum(ol.quantity)
		FROM orders o
		JOIN order_lines ol ON ol.order_id = o.id
		WHERE o.status = 'pending'
		  -- The same population SweepUnpaid collects, or the diagnostic goes
		  -- blind on exactly the orders an operator can now create: a payment
		  -- recorded as failed holds its stock like any other unsettled one.
		  AND o.payment_status IN ('pending', 'failed')
		  AND o.reservation_expires_at IS NOT NULL
		  AND o.reservation_expires_at < now()`).Scan(&stale, &units)
	if err != nil {
		d.Status, d.Detail, d.Cause = StatusWarn, "cannot inspect reservations", err.Error()
		return d
	}

	if stale == 0 {
		d.Status, d.Detail = StatusOK, "no expired reservations"
		return d
	}
	d.Status = StatusWarn
	d.Detail = fmt.Sprintf("%d unsettled order(s) past their reservation window holding %d unit(s)", stale, units.Int64)
	d.Hint = "the sweeper releases these on its next pass; if the count keeps growing, check that background work is running"
	return d
}

func (a *App) checkCarts(ctx context.Context) Diagnostic {
	d := Diagnostic{Name: "carts"}

	var live, pending, abandoned, overdue int
	err := a.db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE status = 'open' AND expires_at >= now()),
		       count(*) FILTER (WHERE status = 'open' AND expires_at < now()),
		       count(*) FILTER (WHERE status = 'abandoned'),
		       count(*) FILTER (WHERE status = 'abandoned'
		                          AND abandoned_at < now() - make_interval(secs => $1))
		FROM carts`, a.cfg.CartRetention.Seconds()).Scan(&live, &pending, &abandoned, &overdue)
	if err != nil {
		d.Status, d.Detail, d.Cause = StatusWarn, "cannot inspect carts", err.Error()
		return d
	}

	d.Status = StatusOK
	d.Detail = fmt.Sprintf("%d live, %d awaiting the sweeper, %d abandoned", live, pending, abandoned)
	// POST /api/carts is unauthenticated, so unswept carts are an
	// unbounded-growth vector rather than merely untidy. That is now the reason
	// there are two bounds rather than one: the empty ones are deleted, and the
	// filled ones are kept only as long as Config.CartRetention.
	switch {
	case pending > 1000:
		d.Status = StatusWarn
		d.Hint = "expired carts are not being swept; check that background work is running — " +
			"Abandon records the ones with lines and SweepExpired deletes the empty ones, " +
			"and either failing leaves this number climbing"
	case overdue > 1000:
		d.Status = StatusWarn
		d.Hint = "abandoned carts are past Config.CartRetention and not being purged; " +
			"PurgeAbandoned is what bounds this table now that expiry no longer deletes"
	}
	return d
}

func (a *App) checkCatalog(ctx context.Context) Diagnostic {
	d := Diagnostic{Name: "catalog"}

	var products, active, orphans, oversold int
	err := a.db.QueryRowContext(ctx, `
		SELECT
			(SELECT count(*) FROM products),
			(SELECT count(*) FROM products WHERE status = 'active'),
			-- An active product with no sellable variant is invisible to
			-- shoppers while looking fine in the admin list.
			(SELECT count(*) FROM products p WHERE p.status = 'active'
			   AND NOT EXISTS (SELECT 1 FROM variants v WHERE v.product_id = p.id AND v.active)),
			-- Mirroring variants_reserved_within_on_hand, which M12 conditioned
			-- on continue_selling: a variant that sells past zero is *meant* to
			-- go negative, and counting it here reported a supported state as
			-- impossible and sent the operator to check a constraint that was
			-- working.
			--
			-- M17 moved the count onto (variant, location) rows and the CHECK
			-- could not follow it: continue_selling is on the variant, and
			-- copying the flag onto every stock row to keep a conditional CHECK
			-- would be a stored duplicate. reserveStock's conditional UPDATE
			-- does the enforcing now, and this is where a drift from it shows.
			(SELECT count(*) FROM variant_stock vs
			   JOIN variants v ON v.id = vs.variant_id
			  WHERE NOT v.continue_selling AND vs.reserved > vs.on_hand)
	`).Scan(&products, &active, &orphans, &oversold)
	if err != nil {
		d.Status, d.Detail, d.Cause = StatusWarn, "cannot inspect the catalog", err.Error()
		return d
	}

	d.Status = StatusOK
	d.Detail = fmt.Sprintf("%d product(s), %d active", products, active)

	if orphans > 0 {
		d.Status = StatusWarn
		d.Detail += fmt.Sprintf(", %d active with no sellable variant", orphans)
		d.Hint = "those products cannot be bought; add an active variant or set the product to draft"
	}
	// The database has a CHECK constraint for this, so a hit here means the
	// constraint is missing — a hand-edited schema, or a restore from a dump
	// that dropped it.
	if oversold > 0 {
		d.Status = StatusFail
		d.Detail += fmt.Sprintf(", %d variant(s) reserved beyond stock", oversold)
		d.Hint = "this should be impossible: verify variants_reserved_within_on_hand still exists"
	}
	return d
}

func (a *App) checkProviders() Diagnostic {
	d := Diagnostic{Name: "providers"}

	pay := make([]string, 0, len(a.payments.providers))
	for code := range a.payments.providers {
		pay = append(pay, code)
	}
	sort.Strings(pay)

	ship := make([]string, 0, len(a.fulfillment.providers))
	for code := range a.fulfillment.providers {
		ship = append(ship, code)
	}
	sort.Strings(ship)

	mods := a.Modules()
	modText := "none"
	if len(mods) > 0 {
		modText = strings.Join(mods, ", ")
	}

	// Named, not counted. "modules: 4" is the answer to a question nobody has;
	// which four is the answer to the one an operator reading a diagnostic
	// actually asks, and it is what tells them the store they are looking at is
	// not the store they deployed.
	d.Status = StatusOK
	d.Detail = fmt.Sprintf("payment: %s; fulfillment: %s; modules: %s",
		strings.Join(pay, ", "), strings.Join(ship, ", "), modText)
	if len(pay) == 0 {
		d.Status = StatusFail
		d.Detail = "no payment providers registered — checkout is impossible"
		d.Hint = "cash on delivery is built in; if it is missing, core wiring did not run"
		return d
	}

	// The notification trap, per channel. The built-in logger is registered for
	// both channels at boot and returns success, so a store with no vendor
	// installed reports every order confirmation as delivered and sends none —
	// the one failure in this file that produces no error, no retry and no
	// sign at all until a customer says they never heard from you.
	//
	// A warning rather than a failure: a store that takes phone orders and
	// sends no SMS is a legitimate configuration, and the doctor's exit code
	// gates deployments.
	var silent []string
	for _, c := range a.NotifierChannels() {
		if !c.Delivers {
			silent = append(silent, c.Channel)
		}
	}
	if len(silent) > 0 {
		d.Status = StatusWarn
		d.Detail += fmt.Sprintf("; no delivery backend for %s — those notifications are written to the log and reported as sent",
			strings.Join(silent, " or "))
		d.Hint = "register a notifier for each channel the store uses (ext/notify-sendgrid, ext/notify-msg91); " +
			"a channel nobody sends on is fine, but it should be a decision"
	}
	return d
}

// checkContract compares what is served against what is documented. Drift here
// is invisible in production and only bites an integrator, which is exactly the
// kind of problem worth having a machine notice.
func (a *App) checkContract() Diagnostic {
	d := Diagnostic{Name: "api contract"}

	documented, err := a.SpecPaths()
	if err != nil {
		d.Status, d.Detail, d.Cause = StatusWarn, "cannot read the OpenAPI document", err.Error()
		return d
	}
	have := make(map[string]bool, len(documented))
	for _, p := range documented {
		have[p] = true
	}

	var missing []string
	for _, r := range a.Routes() {
		if r.UI {
			continue // the panel's files are not an API surface
		}
		if !have[r.Path] {
			missing = append(missing, r.Method+" "+r.Path)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		d.Status = StatusWarn
		d.Detail = fmt.Sprintf("%d served route(s) missing from /doc: %s", len(missing), strings.Join(missing, ", "))
		d.Hint = "add them to openapi.json, or to the module's OpenAPI() fragment"
		return d
	}
	d.Status = StatusOK
	d.Detail = fmt.Sprintf("%d documented path(s) cover every served route", len(documented))
	return d
}

// checkAdminRights finds admin routes that name no right.
//
// Rights are variadic on HandleAdmin, so forgetting them is silent: the route
// mounts, authentication still runs, and every signed-in operator reaches it
// whatever their role. Core has had a test for this since roles shipped, but
// the test boots an app with no modules in it — which is exactly why twelve
// module admin routes went ungated for as long as they did. A check here runs
// in everybody's binary, including one whose module author never wrote a test.
//
// Warn rather than fail: the store is serving, and only a fail flips Report.OK,
// so `gocommerce doctor` keeps its exit code for the things that stop a sale.
func (a *App) checkAdminRights() Diagnostic {
	d := Diagnostic{Name: "admin rights"}

	var ungated []string
	for _, r := range a.Routes() {
		if !r.Admin || len(r.Rights) > 0 || rightsExempt[r.Method+" "+r.Path] {
			continue
		}
		ungated = append(ungated, r.Method+" "+r.Path)
	}
	if len(ungated) > 0 {
		sort.Strings(ungated)
		d.Status = StatusWarn
		d.Detail = fmt.Sprintf("%d admin route(s) reachable by any role: %s",
			len(ungated), strings.Join(ungated, ", "))
		d.Hint = "name the rights on HandleAdmin, or add the route to rightsExempt"
		return d
	}
	d.Status = StatusOK
	d.Detail = "every admin route names the rights it needs"
	return d
}

// checkFulfillment finds orders whose status disagrees with their parcels.
//
// settleOrderShipping is the only thing that writes the shipping axis of
// orders.status, and it derives it from fulfillment_lines every time, so inside
// the engine the two cannot drift. A hit therefore means one of two things:
// somebody wrote orders.status with SQL, which is what AGENTS.md rule 3 exists
// to prevent, or an order arrived through the CSV importer, which passes an
// imported status straight through and brings no fulfillments with it.
//
// This is the compensating control for the composite foreign key that was
// declined — a detector rather than a constraint. Delivered orders are excluded:
// delivered implies fully shipped, and an imported one has no parcels at all.
func (a *App) checkFulfillment(ctx context.Context) Diagnostic {
	d := Diagnostic{Name: "fulfillment"}

	var drifted int
	err := a.db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM (
		    SELECT o.id, o.status,
		           coalesce(sum(ol.quantity), 0) AS ordered,
		           coalesce(sum(s.shipped), 0)   AS shipped
		    FROM orders o
		    JOIN order_lines ol ON ol.order_id = o.id
		    LEFT JOIN (
		        SELECT fl.order_line_id, sum(fl.quantity) AS shipped
		        FROM fulfillment_lines fl
		        JOIN fulfillments f ON f.id = fl.fulfillment_id
		        WHERE f.status <> 'cancelled'
		        GROUP BY fl.order_line_id
		    ) s ON s.order_line_id = ol.id
		    WHERE o.status IN ('confirmed', 'partial', 'shipped')
		    GROUP BY o.id, o.status
		) t
		WHERE (shipped = 0 AND status <> 'confirmed')
		   OR (shipped > 0 AND shipped < ordered AND status <> 'partial')
		   OR (shipped >= ordered AND status <> 'shipped')`).Scan(&drifted)
	if err != nil {
		d.Status, d.Detail, d.Cause = StatusWarn, "cannot read fulfillment lines", err.Error()
		d.Hint = "check that the migrations are applied"
		return d
	}
	if drifted > 0 {
		d.Status = StatusWarn
		d.Detail = fmt.Sprintf("%d order(s) say something different from what their parcels do", drifted)
		d.Hint = "the shipping status is derived from fulfillment_lines; a mismatch means orders.status " +
			"was written by hand or the order was imported without its shipments"
		return d
	}
	d.Status = StatusOK
	d.Detail = "every order's status matches what its parcels say"
	return d
}

// checkRefunds reconciles the money that went back with the ledger that says it
// did, and looks for refunds nobody ever heard the end of.
//
// This check is the price of storing orders.refunded_minor at all. M24 keeps a
// running total on the order — against M17's own reasoning about stored sums —
// because it is what makes orders_refunded_within_total a real row CHECK and
// what lets lockOrder read the figure off the row it is already holding. The
// service only ever moves the column and the ledger inside one transaction, so
// a disagreement is not drift: it is somebody having written orders by hand.
//
// The stale-pending half is a different question with the same query. A refund
// is committed as `pending` before the gateway is called, so a process that
// died in between leaves a row that may or may not have moved money — and that
// amount stays reserved against every later refund until a person settles it.
func (a *App) checkRefunds(ctx context.Context) Diagnostic {
	d := Diagnostic{Name: "refunds"}

	var drifted, inFlight, settled int
	err := a.db.QueryRowContext(ctx, `
		SELECT
		    (SELECT count(*) FROM orders o
		      WHERE o.refunded_minor <> coalesce(
		            (SELECT sum(r.amount_minor) FROM order_refunds r
		              WHERE r.order_id = o.id AND r.status = 'succeeded'), 0)),
		    (SELECT count(*) FROM order_refunds
		      WHERE status = 'pending' AND created_at < now() - $1::interval),
		    (SELECT count(*) FROM order_refunds WHERE status = 'succeeded')`,
		intervalSeconds(refundStaleAfter)).Scan(&drifted, &inFlight, &settled)
	if err != nil {
		d.Status, d.Detail, d.Cause = StatusWarn, "cannot read the refund ledger", err.Error()
		d.Hint = "check that the migrations are applied"
		return d
	}

	if drifted > 0 {
		d.Status = StatusFail
		d.Detail = fmt.Sprintf("%d order(s) disagree with their refund records", drifted)
		d.Hint = "orders.refunded_minor and the order_refunds ledger disagree; the service only ever " +
			"moves the two together, so somebody wrote orders by hand"
		return d
	}
	if inFlight > 0 {
		d.Status = StatusWarn
		d.Detail = fmt.Sprintf("%d refund(s) started more than %s ago and never settled", inFlight, refundStaleAfter)
		d.Hint = "the provider may or may not have moved the money: check the gateway, then " +
			"POST /api/admin/orders/{id}/refunds/{refund_id}/settle to record what happened"
		return d
	}
	d.Status = StatusOK
	if settled == 0 {
		d.Detail = "no refunds recorded"
		return d
	}
	d.Detail = fmt.Sprintf("%d refund(s), all reconciled", settled)
	return d
}

// checkReturns looks for an order line with more units returned than were sold.
//
// It is here because that cap is the one invariant in returns that no CHECK
// constraint can express — it is a sum across rows — so it is enforced in the
// service, under the order's row lock, and this is where a bypass would show:
// a module or a script writing order_return_lines directly, against rule 3.
// The oversold half of checkCatalog exists for exactly the same reason.
func (a *App) checkReturns(ctx context.Context) Diagnostic {
	d := Diagnostic{Name: "returns"}

	var overReturned, received int
	err := a.db.QueryRowContext(ctx, `
		SELECT
		    (SELECT count(*) FROM (
		        SELECT ol.id
		        FROM order_lines ol
		        JOIN order_return_lines rl ON rl.order_line_id = ol.id
		        JOIN order_returns r ON r.id = rl.return_id AND r.status = 'received'
		        GROUP BY ol.id, ol.quantity
		        HAVING sum(rl.quantity) > ol.quantity) x),
		    (SELECT count(*) FROM order_returns WHERE status = 'received')`).
		Scan(&overReturned, &received)
	if err != nil {
		d.Status, d.Detail, d.Cause = StatusWarn, "cannot read the returns", err.Error()
		d.Hint = "check that the migrations are applied"
		return d
	}

	if overReturned > 0 {
		d.Status = StatusFail
		d.Detail = fmt.Sprintf("%d order line(s) have had more units back than went out", overReturned)
		d.Hint = "this should be impossible: returns are capped in the service under the order's " +
			"row lock, so a row here means something wrote order_return_lines directly"
		return d
	}
	d.Status = StatusOK
	if received == 0 {
		d.Detail = "no returns recorded"
		return d
	}
	d.Detail = fmt.Sprintf("%d return(s) received, none over what was sold", received)
	return d
}

// checkLedger asks whether the stock ledger still explains the shelf: for every
// (variant, location) pair, sum(on_hand_delta) has to equal on_hand and
// sum(reserved_delta) has to equal reserved.
//
// It is the production counterpart of TestTheLedgerReconcilesToTheShelf, and it
// is the only mechanism in the engine that catches a writer bypassing
// app.Stock(): every service path writes its movement inside the transaction
// that moved the balance, so inside the engine the two cannot drift — a drift
// is raw SQL against variant_stock, which is AGENTS rule 3.
//
// Warn rather than fail, deliberately. The shelf is still correct and the store
// is still serving; only the explanation is missing. A fail would make `doctor`
// exit non-zero forever for a store that ran one hand-fix years ago, which
// trains an operator to ignore the whole report — the opposite posture from
// checkCatalog's oversold count, where a CHECK constraint is supposed to make
// the state impossible in the first place.
func (a *App) checkLedger(ctx context.Context) Diagnostic {
	d := Diagnostic{Name: "stock ledger"}

	var drifted, total int
	err := a.db.QueryRowContext(ctx, `
		SELECT
		    (SELECT count(*) FROM variant_stock vs
		      WHERE vs.on_hand <> coalesce((SELECT sum(m.on_hand_delta) FROM stock_movements m
		                                     WHERE m.variant_id = vs.variant_id
		                                       AND m.location_id = vs.location_id), 0)
		         OR vs.reserved <> coalesce((SELECT sum(m.reserved_delta) FROM stock_movements m
		                                      WHERE m.variant_id = vs.variant_id
		                                        AND m.location_id = vs.location_id), 0)),
		    (SELECT count(*) FROM stock_movements)`).Scan(&drifted, &total)
	if err != nil {
		d.Status, d.Detail, d.Cause = StatusWarn, "cannot read the stock ledger", err.Error()
		d.Hint = "check that the migrations are applied"
		return d
	}

	if drifted > 0 {
		d.Status = StatusWarn
		d.Detail = fmt.Sprintf("%d stock row(s) the ledger cannot account for, across %d movement(s)",
			drifted, total)
		d.Hint = "something wrote variant_stock outside app.Stock() — see rule 3 in AGENTS.md; " +
			"the balances are still the truth, the ledger just cannot explain them"
		return d
	}
	d.Status = StatusOK
	d.Detail = fmt.Sprintf("%d movement(s) recorded; every balance reconciles", total)
	return d
}

// checkDiscounts looks for promotions that cannot do what their form said.
//
// The scoped states below are refused by the service from here on, so a hit
// means either a row written before that, or SQL run by hand, or a partial
// restore. Reporting them is what stands in for the foreign key a polymorphic
// target column cannot have: a promotion that has narrowed is put in front of
// an operator rather than in front of a constraint.
func (a *App) checkDiscounts(ctx context.Context) Diagnostic {
	d := Diagnostic{Name: "discounts"}

	var scoped, untargeted, mismatched, dangling, freeShipping int
	err := a.db.QueryRowContext(ctx, `
		SELECT
		    (SELECT count(*) FROM discounts WHERE scope <> 'order'),
		    -- The one state the service now refuses to create: a rule that
		    -- names a kind of thing and then names none of them. It is refused
		    -- at the till, which is the worst place to find out.
		    (SELECT count(*) FROM discounts d WHERE d.scope <> 'order'
		       AND NOT EXISTS (SELECT 1 FROM discount_targets t WHERE t.discount_id = d.id)),
		    -- A target row whose kind disagrees with its discount's scope.
		    -- Inert — the matcher only reads the kind the scope names — but
		    -- somebody meant something by it.
		    (SELECT count(*) FROM discount_targets t JOIN discounts d ON d.id = t.discount_id
		      WHERE t.kind <> CASE d.scope WHEN 'products'    THEN 'product'
		                                   WHEN 'collections' THEN 'collection'
		                                   WHEN 'categories'  THEN 'category'
		                                   ELSE '' END),
		    -- A target naming something that has been deleted. The rule still
		    -- works; it just covers less than it says it does.
		    (SELECT count(*) FROM discount_targets t
		      WHERE (t.kind = 'product'    AND NOT EXISTS (SELECT 1 FROM products    p WHERE p.id = t.target_id))
		         OR (t.kind = 'collection' AND NOT EXISTS (SELECT 1 FROM collections c WHERE c.id = t.target_id))
		         OR (t.kind = 'category'   AND NOT EXISTS (SELECT 1 FROM categories  c WHERE c.id = t.target_id))),
		    (SELECT count(*) FROM discounts WHERE kind = 'free_shipping' AND scope <> 'order')
	`).Scan(&scoped, &untargeted, &mismatched, &dangling, &freeShipping)
	if err != nil {
		d.Status, d.Detail, d.Cause = StatusWarn, "cannot inspect discounts", err.Error()
		d.Hint = "check that the migrations are applied"
		return d
	}

	// Fail wins over warn: a rule that can never apply is a promotion an
	// operator believes is running.
	if untargeted > 0 {
		d.Status = StatusFail
		d.Detail = fmt.Sprintf("%d scoped discount(s) point at nothing", untargeted)
		d.Hint = "a scoped rule with no targets is refused at the till; " +
			"give it targets or set its scope back to the whole basket"
		return d
	}
	if dangling > 0 {
		d.Status = StatusWarn
		d.Detail = fmt.Sprintf("%d discount target(s) name something that has been deleted", dangling)
		d.Hint = "those promotions have narrowed; re-aim them or remove the dead targets"
		return d
	}
	if mismatched > 0 {
		d.Status = StatusWarn
		d.Detail = fmt.Sprintf("%d target row(s) disagree with their discount's scope", mismatched)
		d.Hint = "those rows are ignored; remove them"
		return d
	}
	if freeShipping > 0 {
		d.Status = StatusWarn
		d.Detail = fmt.Sprintf("%d free-shipping rule(s) carry a scope", freeShipping)
		d.Hint = "shipping is not a line, so these are refused at the till; " +
			"set the scope back to the whole basket or deactivate them"
		return d
	}

	d.Status = StatusOK
	d.Detail = fmt.Sprintf("%d scoped discount(s), all targeted", scoped)
	return d
}
