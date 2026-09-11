package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Abandonment is the one place the engine keeps a shopper's basket instead of
// deleting it, so these tests are mostly about the two bounds that make keeping
// it safe — an empty cart is still deleted outright, and a kept one is purged
// once retention runs out — and about the biconditional CHECK that stops the
// status and its timestamp from drifting apart.

// ageCart pushes a cart's TTL into the past the way a real one expires, without
// waiting out CartTTL.
func ageCart(t *testing.T, app *App, token string) {
	t.Helper()
	if _, err := app.DB().ExecContext(context.Background(),
		`UPDATE carts SET expires_at = now() - interval '1 hour' WHERE token = $1`, token); err != nil {
		t.Fatalf("age the cart: %v", err)
	}
}

func cartRow(t *testing.T, app *App, token string) (status string, abandonedAt *time.Time, updatedAt time.Time) {
	t.Helper()
	var at *time.Time
	if err := app.DB().QueryRowContext(context.Background(),
		`SELECT status, abandoned_at, updated_at FROM carts WHERE token = $1`, token).
		Scan(&status, &at, &updatedAt); err != nil {
		t.Fatalf("read the cart row: %v", err)
	}
	return status, at, updatedAt
}

// A basket somebody filled and walked away from becomes a record, and the
// sweeper deliberately leaves updated_at alone: "sat five days, then was given
// up on" is unanswerable if the sweep overwrites when the shopper last touched
// it.
func TestExpiredCartWithLinesBecomesAbandoned(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "ABND-1", 2500, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 2)

	_, _, before := cartRow(t, app, cart.Token)
	ageCart(t, app, cart.Token)

	n, err := app.Cart().Abandon(ctx)
	if err != nil {
		t.Fatalf("Abandon: %v", err)
	}
	if n != 1 {
		t.Fatalf("Abandon claimed %d carts, want 1", n)
	}

	status, abandonedAt, after := cartRow(t, app, cart.Token)
	if status != CartAbandoned {
		t.Errorf("status = %q, want abandoned", status)
	}
	if abandonedAt == nil {
		t.Error("abandoned_at is NULL on an abandoned cart")
	}
	if !after.Equal(before) {
		t.Errorf("the sweep moved updated_at from %v to %v; the sweeper is not the shopper", before, after)
	}

	// The basket survives with it — that is the entire point.
	kept, err := app.Cart().GetByToken(ctx, cart.Token)
	if err != nil {
		t.Fatalf("an abandoned cart must still read back: %v", err)
	}
	if len(kept.Lines) != 1 || kept.ItemCount != 2 {
		t.Errorf("the lines did not survive abandonment: %+v", kept.Lines)
	}
}

// The growth guard the DELETE existed for survives: an empty expired cart is
// still deleted, and the NOT EXISTS does not eat the filled ones a capped
// Abandon has not reached.
func TestExpiredEmptyCartIsDeletedNotRecorded(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	empty := newCart(t, app)
	ageCart(t, app, empty.Token)

	if n, err := app.Cart().Abandon(ctx); err != nil || n != 0 {
		t.Fatalf("Abandon on an empty cart = (%d, %v), want (0, nil)", n, err)
	}
	n, err := app.Cart().SweepExpired(ctx)
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if n != 1 {
		t.Errorf("SweepExpired removed %d carts, want 1", n)
	}
	if _, err := app.Cart().GetByToken(ctx, empty.Token); err == nil {
		t.Error("an empty expired cart must be deleted outright")
	}
	if pending, _, err := app.PendingEvents(ctx); err != nil || pending != 0 {
		t.Errorf("deleting an empty cart announced %d events, want none (err %v)", pending, err)
	}

	// The load-bearing half: a filled expired cart is not this sweep's business.
	product := simpleProduct(t, app, "SWEEP-1", 1000, 3)
	filled := newCart(t, app)
	addToCart(t, app, filled.Token, product.DefaultVariant().ID, 1)
	ageCart(t, app, filled.Token)

	if n, err := app.Cart().SweepExpired(ctx); err != nil || n != 0 {
		t.Fatalf("SweepExpired deleted %d filled carts (err %v); the NOT EXISTS is load-bearing", n, err)
	}
	if _, err := app.Cart().GetByToken(ctx, filled.Token); err != nil {
		t.Errorf("the filled cart was deleted by the empty-cart sweep: %v", err)
	}
}

// The status predicate is re-evaluated under the claim's lock, which is what
// makes N replicas of runSweepers safe.
func TestAbandonIsExactlyOncePerCart(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "ONCE-1", 1500, 4)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	ageCart(t, app, cart.Token)

	if n, err := app.Cart().Abandon(ctx); err != nil || n != 1 {
		t.Fatalf("first Abandon = (%d, %v), want (1, nil)", n, err)
	}
	if n, err := app.Cart().Abandon(ctx); err != nil || n != 0 {
		t.Fatalf("second Abandon = (%d, %v), want (0, nil)", n, err)
	}

	var events int
	if err := app.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM outbox_events WHERE event_name = $1`, EventCartAbandoned).
		Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events != 1 {
		t.Errorf("%d cart.abandoned events for one abandonment, want 1", events)
	}
}

// FOR UPDATE SKIP LOCKED, which no mock reproduces: a row another session holds
// is left for the next pass rather than blocking this one.
func TestAbandonSkipsCartsLockedByAnotherSession(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "SKIP-1", 900, 3)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	ageCart(t, app, cart.Token)

	holder, err := app.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin the holding transaction: %v", err)
	}
	var held int64
	if err := holder.QueryRowContext(ctx,
		`SELECT id FROM carts WHERE token = $1 FOR UPDATE`, cart.Token).Scan(&held); err != nil {
		_ = holder.Rollback()
		t.Fatalf("hold the row: %v", err)
	}

	done := make(chan struct{})
	var claimed int
	var abandonErr error
	go func() {
		claimed, abandonErr = app.Cart().Abandon(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = holder.Rollback()
		t.Fatal("Abandon blocked on a locked row; the claim must SKIP LOCKED")
	}
	if abandonErr != nil {
		_ = holder.Rollback()
		t.Fatalf("Abandon: %v", abandonErr)
	}
	if claimed != 0 {
		t.Errorf("Abandon claimed %d locked carts, want 0", claimed)
	}

	if err := holder.Rollback(); err != nil {
		t.Fatalf("release the row: %v", err)
	}
	if n, err := app.Cart().Abandon(ctx); err != nil || n != 1 {
		t.Errorf("Abandon after the lock was released = (%d, %v), want (1, nil)", n, err)
	}
}

// The outbox invariant: the row is already abandoned and its event is already
// written, both visible before anything is delivered.
func TestAbandonWritesTheEventInTheSameTransaction(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "TX-1", 1200, 2)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	ageCart(t, app, cart.Token)

	if _, err := app.Cart().Abandon(ctx); err != nil {
		t.Fatalf("Abandon: %v", err)
	}

	status, _, _ := cartRow(t, app, cart.Token)
	pending, dead, err := app.PendingEvents(ctx)
	if err != nil {
		t.Fatalf("pending events: %v", err)
	}
	if status != CartAbandoned || pending != 1 || dead != 0 {
		t.Errorf("status %q with %d pending and %d dead events; want abandoned with exactly 1 pending",
			status, pending, dead)
	}
}

// recordingCartModule captures cart.* deliveries so a test can read the payload
// a recovery consumer would actually receive.
type recordingCartModule struct {
	events []Event
}

func (m *recordingCartModule) Name() string            { return "cartrecorder" }
func (m *recordingCartModule) Migrations() []Migration { return nil }
func (m *recordingCartModule) Register(app *App) error {
	app.Subscribe("cart.*", func(ctx context.Context, e Event) error {
		m.events = append(m.events, e)
		return nil
	})
	return nil
}

// The payload's whole contract: a consumer can write the recovery email without
// touching the database.
func TestCartAbandonedPayloadCarriesWhatARecoveryNeeds(t *testing.T) {
	recorder := &recordingCartModule{}
	app := newTestApp(t, recorder)
	ctx := context.Background()

	product := simpleProduct(t, app, "PAY-1", 2500, 10)
	cart := newCart(t, app)
	if _, err := app.Cart().SetEmail(ctx, cart.Token, "shopper@example.com"); err != nil {
		t.Fatalf("set the email: %v", err)
	}
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 3)
	ageCart(t, app, cart.Token)

	if _, err := app.Cart().Abandon(ctx); err != nil {
		t.Fatalf("Abandon: %v", err)
	}
	if _, err := app.DrainOutbox(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("%d cart events delivered, want 1", len(recorder.events))
	}

	e := recorder.events[0]
	if e.Name != EventCartAbandoned {
		t.Errorf("event name = %q, want %q", e.Name, EventCartAbandoned)
	}
	if e.AggregateType != AggregateCart {
		t.Errorf("aggregate type = %q, want %q", e.AggregateType, AggregateCart)
	}
	var ev CartEvent
	if err := e.Decode(&ev); err != nil {
		t.Fatalf("decode the payload: %v", err)
	}
	if e.AggregateID != ev.CartID || ev.CartID == 0 {
		t.Errorf("aggregate id %d does not name the cart (%d)", e.AggregateID, ev.CartID)
	}
	if ev.Token != cart.Token {
		t.Errorf("cart_token = %q, want the cart's own token", ev.Token)
	}
	if ev.Email != "shopper@example.com" {
		t.Errorf("email = %q, want the address the shopper gave", ev.Email)
	}
	if ev.ItemCount != 3 || ev.SubtotalMinor != 7500 {
		t.Errorf("item_count/subtotal_minor = %d/%d, want 3/7500", ev.ItemCount, ev.SubtotalMinor)
	}
	if len(ev.Lines) != 1 {
		t.Fatalf("%d lines in the payload, want 1", len(ev.Lines))
	}
	line := ev.Lines[0]
	if line.SKU != "PAY-1" || line.Quantity != 3 || line.UnitPriceMinor != 2500 || line.TotalMinor != 7500 {
		t.Errorf("line = %+v, want the basket as it stood", line)
	}
	if !ev.LastActiveAt.Before(ev.AbandonedAt) {
		t.Errorf("last_active_at %v is not before abandoned_at %v; a recovery flow schedules against the gap",
			ev.LastActiveAt, ev.AbandonedAt)
	}
}

// Abandonment is the store's observation, not the shopper's decision, so it
// must not be able to destroy their basket.
func TestAbandonedCartRevivesWhenTheShopperReturns(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "REV-1", 500, 10)
	second := simpleProduct(t, app, "REV-2", 700, 10)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	ageCart(t, app, cart.Token)
	if _, err := app.Cart().Abandon(ctx); err != nil {
		t.Fatalf("Abandon: %v", err)
	}

	revived := addToCart(t, app, cart.Token, second.DefaultVariant().ID, 1)
	if revived.Status != CartOpen {
		t.Errorf("status after a mutation = %q, want open", revived.Status)
	}
	if len(revived.Lines) != 2 {
		t.Errorf("%d lines after reviving, want the old one and the new one", len(revived.Lines))
	}
	if !revived.ExpiresAt.After(time.Now()) {
		t.Errorf("expires_at = %v, want a fresh TTL", revived.ExpiresAt)
	}
	if _, abandonedAt, _ := cartRow(t, app, cart.Token); abandonedAt != nil {
		t.Error("abandoned_at survived a revival; the purge would take a live basket")
	}

	// And through SetEmail, which is the mutation a recovery link's form makes
	// and the only one with no line-item body.
	ageCart(t, app, cart.Token)
	if _, err := app.Cart().Abandon(ctx); err != nil {
		t.Fatalf("second Abandon: %v", err)
	}
	before := revived.ExpiresAt
	back, err := app.Cart().SetEmail(ctx, cart.Token, "back@example.com")
	if err != nil {
		t.Fatalf("SetEmail on an abandoned cart: %v", err)
	}
	if back.Status != CartOpen || back.Email != "back@example.com" {
		t.Errorf("after SetEmail: status %q, email %q", back.Status, back.Email)
	}
	if !back.ExpiresAt.After(before) {
		t.Errorf("SetEmail left expires_at at %v; every other mutation extends the cart's life", back.ExpiresAt)
	}
}

// A checkout on an abandoned cart IS the recovery succeeding, and reviving
// invents no new validation — it rides on the guards checkout already runs.
func TestAbandonedCartCanStillCheckOut(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "RECOV-1", 3000, 5)
	variant := product.DefaultVariant()
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, variant.ID, 1)
	ageCart(t, app, cart.Token)
	if _, err := app.Cart().Abandon(ctx); err != nil {
		t.Fatalf("Abandon: %v", err)
	}

	// A price that moved while the basket sat is still refused, which is the
	// point: nothing about reviving relaxes checkout.
	raised := int64(3500)
	if _, err := app.Products().UpdateVariant(ctx, variant.ID, VariantPatch{
		PriceMinor: &raised,
	}); err != nil {
		t.Fatalf("raise the price: %v", err)
	}
	if _, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), ""); err == nil {
		t.Fatal("a revived checkout at a stale price must still be refused")
	}

	// The revive is atomic with the mutation it rode in on: the refusal rolled
	// the transaction back, so the cart is exactly as the sweeper left it.
	if status, abandonedAt, _ := cartRow(t, app, cart.Token); status != CartAbandoned || abandonedAt == nil {
		t.Errorf("after a refused checkout: status %q with abandoned_at %v; the revive should have rolled back",
			status, abandonedAt)
	}

	// The refusal re-snapshotted the cart, so the shopper's retry is at the
	// price they can now see.
	result, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout after the shopper re-confirmed: %v", err)
	}
	if result.Order.ID == 0 {
		t.Error("no order came out of a recovered checkout")
	}
	status, abandonedAt, _ := cartRow(t, app, cart.Token)
	if status != CartConverted || abandonedAt != nil {
		t.Errorf("after checkout: status %q, abandoned_at %v; want converted with no timestamp",
			status, abandonedAt)
	}
}

// Widening the two guards for 'abandoned' must not widen them for 'converted'.
// This is the test that notices if the allowlist ever drifts into a denylist.
func TestConvertedCartStaysTerminal(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "TERM-1", 1000, 5)
	cart := newCart(t, app)
	added := addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	if _, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), ""); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	ageCart(t, app, cart.Token)

	if n, err := app.Cart().Abandon(ctx); err != nil || n != 0 {
		t.Errorf("Abandon claimed %d converted carts (err %v), want 0", n, err)
	}

	lineID := added.Lines[0].ID
	refusals := map[string]error{}
	_, refusals["AddLine"] = app.Cart().AddLine(ctx, cart.Token, product.DefaultVariant().ID, 1)
	_, refusals["UpdateLine"] = app.Cart().UpdateLine(ctx, cart.Token, lineID, 2)
	_, refusals["SetEmail"] = app.Cart().SetEmail(ctx, cart.Token, "late@example.com")
	_, refusals["Checkout"] = app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	for name, err := range refusals {
		if err == nil {
			t.Errorf("%s on a converted cart was allowed", name)
			continue
		}
		if !strings.Contains(err.Error(), "already been checked out") {
			t.Errorf("%s refused with %q, want the checked-out conflict", name, err)
		}
	}
}

// Retention is the second bound, and it is clocked off abandoned_at so draining
// a backlog cannot abandon and purge a row inside the same pass.
func TestPurgeAbandonedRespectsRetention(t *testing.T) {
	dsn := requireDB(t)
	resetSchema(t, dsn)
	cfg := testConfig(dsn)
	cfg.CartRetention = time.Minute
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	ctx := context.Background()

	product := simpleProduct(t, app, "PURGE-1", 800, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	ageCart(t, app, cart.Token)
	if _, err := app.Cart().Abandon(ctx); err != nil {
		t.Fatalf("Abandon: %v", err)
	}

	if n, err := app.Cart().PurgeAbandoned(ctx); err != nil || n != 0 {
		t.Fatalf("PurgeAbandoned inside the window = (%d, %v), want (0, nil)", n, err)
	}

	// An expires_at a year old must not purge anything: the clock is
	// abandoned_at, or a backlog would be announced and destroyed in one pass.
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE carts SET expires_at = now() - interval '1 year' WHERE token = $1`, cart.Token); err != nil {
		t.Fatalf("age expires_at: %v", err)
	}
	if n, err := app.Cart().PurgeAbandoned(ctx); err != nil || n != 0 {
		t.Fatalf("PurgeAbandoned ran off expires_at: (%d, %v), want (0, nil)", n, err)
	}

	if _, err := app.DB().ExecContext(ctx,
		`UPDATE carts SET abandoned_at = now() - interval '2 minutes' WHERE token = $1`, cart.Token); err != nil {
		t.Fatalf("age abandoned_at: %v", err)
	}
	if n, err := app.Cart().PurgeAbandoned(ctx); err != nil || n != 1 {
		t.Fatalf("PurgeAbandoned past the window = (%d, %v), want (1, nil)", n, err)
	}
	if _, err := app.Cart().GetByToken(ctx, cart.Token); err == nil {
		t.Error("a purged cart still reads back")
	}
}

// Both drift directions are silent and permanent, so the database refuses them.
func TestAbandonedStatusWithoutATimestampIsRejected(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "CHK-1", 600, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)

	// Marked with no timestamp: an immortal row with somebody's email in it.
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE carts SET status = 'abandoned' WHERE token = $1`, cart.Token); err == nil {
		t.Error("status = 'abandoned' with a NULL abandoned_at was accepted")
	} else if !strings.Contains(err.Error(), "carts_abandoned_at_matches_status") {
		t.Errorf("refusal does not name the constraint: %v", err)
	}

	// And a timestamp with no mark.
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE carts SET abandoned_at = now() WHERE token = $1`, cart.Token); err == nil {
		t.Error("abandoned_at on an open cart was accepted")
	}

	ageCart(t, app, cart.Token)
	if _, err := app.Cart().Abandon(ctx); err != nil {
		t.Fatalf("Abandon: %v", err)
	}
	// And clearing the timestamp while the mark stays.
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE carts SET abandoned_at = NULL WHERE token = $1`, cart.Token); err == nil {
		t.Error("clearing abandoned_at on an abandoned cart was accepted")
	}
}

// adminCartFixtures builds the three shapes the screen has to tell apart: an
// empty basket, a filled one with an address, and one that became an order.
func adminCartFixtures(t *testing.T, app *App) (empty, filled, converted *Cart) {
	t.Helper()
	ctx := context.Background()

	product := simpleProduct(t, app, "ADM-1", 2000, 20)
	empty = newCart(t, app)

	filled = newCart(t, app)
	addToCart(t, app, filled.Token, product.DefaultVariant().ID, 2)
	if _, err := app.Cart().SetEmail(ctx, filled.Token, "basket@example.com"); err != nil {
		t.Fatalf("set the email: %v", err)
	}

	converted = newCart(t, app)
	addToCart(t, app, converted.Token, product.DefaultVariant().ID, 1)
	if _, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(converted.Token), ""); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	return empty, filled, converted
}

// The credential-withholding decision is only real if a test says so.
func TestAdminCartListHidesTheToken(t *testing.T) {
	app := newTestApp(t)
	empty, filled, converted := adminCartFixtures(t, app)

	rec := do(t, app, http.MethodGet, "/api/admin/carts", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, cart := range []*Cart{empty, filled, converted} {
		if strings.Contains(body, cart.Token) {
			t.Errorf("the listing leaks a cart token: %s", body)
		}
	}

	var listed struct {
		Data []CartSummary `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode the listing: %v", err)
	}
	for _, s := range listed.Data {
		detail := do(t, app, http.MethodGet, "/api/admin/carts/"+strconv.FormatInt(s.ID, 10), withAdmin)
		if detail.Code != http.StatusOK {
			t.Fatalf("detail %d = %d: %s", s.ID, detail.Code, detail.Body)
		}
		for _, cart := range []*Cart{empty, filled, converted} {
			if strings.Contains(detail.Body.String(), cart.Token) {
				t.Errorf("the detail leaks a cart token: %s", detail.Body)
			}
		}
	}
}

// The LATERAL aggregate, the filters, and meta counting the filtered set rather
// than the table.
func TestAdminCartListFiltersAndCounts(t *testing.T) {
	app := newTestApp(t)
	_, filled, _ := adminCartFixtures(t, app)

	list := func(q string) (data []CartSummary, total int, code int) {
		t.Helper()
		rec := do(t, app, http.MethodGet, "/api/admin/carts"+q, withAdmin)
		var out struct {
			Data []CartSummary `json:"data"`
			Meta ListMeta      `json:"meta"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Data, out.Meta.Total, rec.Code
	}

	data, total, code := list("")
	if code != http.StatusOK || total != 3 || len(data) != 3 {
		t.Fatalf("unfiltered = %d with %d rows and total %d, want 200/3/3", code, len(data), total)
	}
	// Newest first.
	if !(data[0].ID > data[1].ID && data[1].ID > data[2].ID) {
		t.Errorf("the listing is not newest-first: %d, %d, %d", data[0].ID, data[1].ID, data[2].ID)
	}
	// The summary's money is the snapshot arithmetic in minor units, carrying
	// the cart's own currency.
	var found bool
	for _, s := range data {
		if s.Email == "basket@example.com" {
			found = true
			if s.LineCount != 1 || s.ItemCount != 2 || s.Subtotal.AmountMinor != 4000 {
				t.Errorf("the filled basket aggregates to %+v, want 1 line / 2 items / 4000", s)
			}
			if s.Subtotal.Currency != s.Currency || s.Currency == "" {
				t.Errorf("subtotal currency %q does not match the cart's %q", s.Subtotal.Currency, s.Currency)
			}
		}
	}
	if !found {
		t.Error("the filled basket is not in the listing")
	}

	if _, total, _ = list("?has_lines=true"); total != 2 {
		t.Errorf("has_lines=true counted %d, want 2", total)
	}
	// Only the one a shopper typed an address into: checkout records the email
	// on the order, not back onto the cart.
	if _, total, _ = list("?has_email=true"); total != 1 {
		t.Errorf("has_email=true counted %d, want 1", total)
	}
	if _, total, _ = list("?has_lines=false"); total != 1 {
		t.Errorf("has_lines=false counted %d, want the one empty basket", total)
	}
	if data, total, _ = list("?state=converted"); total != 1 || len(data) != 1 || data[0].State != CartStateConverted {
		t.Errorf("state=converted returned %d rows (total %d)", len(data), total)
	}
	if data, total, _ = list("?min_value_minor=999999"); total != 0 || len(data) != 0 {
		t.Errorf("an unreachable min_value_minor returned %d rows (total %d)", len(data), total)
	}
	if _, _, code = list("?state=nonsense"); code != http.StatusBadRequest {
		t.Errorf("state=nonsense = %d, want 400 — a filter must fail loudly, not return an empty page", code)
	}
	if _, _, code = list("?min_value_minor=-5"); code != http.StatusBadRequest {
		t.Errorf("a negative min_value_minor = %d, want 400", code)
	}
	if _, _, code = list("?limit=500"); code != http.StatusBadRequest {
		t.Errorf("limit=500 = %d, want 400", code)
	}
	_ = filled
}

// The screen must not give a different answer depending on where the
// five-minute ticker happens to be.
func TestCartListStateIsWhatTheCartIs(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "STATE-1", 1100, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)

	stateOf := func() (state, status string) {
		t.Helper()
		rec := do(t, app, http.MethodGet, "/api/admin/carts", withAdmin)
		var out struct {
			Data []CartSummary `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(out.Data) != 1 {
			t.Fatalf("%d carts listed, want 1", len(out.Data))
		}
		return out.Data[0].State, out.Data[0].Status
	}

	if state, status := stateOf(); state != CartStateLive || status != CartOpen {
		t.Errorf("a fresh cart lists as %q/%q, want live/open", state, status)
	}

	ageCart(t, app, cart.Token)
	if state, status := stateOf(); state != CartStateAbandoned || status != CartOpen {
		t.Errorf("an expired cart the sweeper has not reached lists as %q/%q, want abandoned/open", state, status)
	}
	rec := do(t, app, http.MethodGet, "/api/admin/carts?state=abandoned", withAdmin)
	var filtered struct {
		Data []CartSummary `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &filtered)
	if len(filtered.Data) != 1 {
		t.Errorf("state=abandoned missed the cart the sweeper has not reached: %s", rec.Body)
	}

	if _, err := app.Cart().Abandon(ctx); err != nil {
		t.Fatalf("Abandon: %v", err)
	}
	if state, status := stateOf(); state != CartStateAbandoned || status != CartAbandoned {
		t.Errorf("after the sweep the cart lists as %q/%q, want abandoned/abandoned", state, status)
	}

	if _, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), ""); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if state, status := stateOf(); state != CartStateConverted || status != CartConverted {
		t.Errorf("after checkout the cart lists as %q/%q, want converted/converted", state, status)
	}
}

// Is this basket still worth chasing? The detail answers with both coordinates.
func TestAdminCartDetailShowsLivePrices(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "LIVE-1", 2000, 5)
	variant := product.DefaultVariant()
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, variant.ID, 2)

	raised, inactive := int64(2600), false
	if _, err := app.Products().UpdateVariant(ctx, variant.ID, VariantPatch{
		PriceMinor: &raised,
		Active:     &inactive,
	}); err != nil {
		t.Fatalf("move the price and deactivate: %v", err)
	}

	listed, _, err := app.Cart().List(ctx, CartQuery{})
	if err != nil || len(listed) != 1 {
		t.Fatalf("List = %d carts, %v", len(listed), err)
	}
	rec := do(t, app, http.MethodGet, "/api/admin/carts/"+strconv.FormatInt(listed[0].ID, 10), withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail = %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Data CartDetail `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Data.Lines) != 1 {
		t.Fatalf("%d lines, want 1", len(out.Data.Lines))
	}
	line := out.Data.Lines[0]
	if line.UnitPrice.AmountMinor != 2000 {
		t.Errorf("unit_price = %d, want the snapshot 2000", line.UnitPrice.AmountMinor)
	}
	if line.CurrentPrice.AmountMinor != 2600 || !line.PriceChanged {
		t.Errorf("current_price = %d, price_changed = %v; want 2600 and true",
			line.CurrentPrice.AmountMinor, line.PriceChanged)
	}
	if line.InStock {
		t.Error("a deactivated variant reports in_stock true")
	}
	if out.Data.Subtotal.AmountMinor != 4000 {
		t.Errorf("subtotal = %d, want the snapshot 4000", out.Data.Subtotal.AmountMinor)
	}
	// Reading a cart never revives it and never touches the row.
	if status, _, _ := cartRow(t, app, cart.Token); status != CartOpen {
		t.Errorf("an admin read moved the cart to %q", status)
	}
}

// The routes are gated on the right the nav says they are.
func TestAdminCartRoutesRequireOrdersRead(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	staff := signInAs(t, app, "cartstaff@example.com", RoleStaff)
	narrowed := []Right{}
	for _, r := range DefaultRightsOf(RoleStaff) {
		if r != RightOrdersRead {
			narrowed = append(narrowed, r)
		}
	}
	if _, err := app.Roles().Set(ctx, RoleStaff, narrowed, nil); err != nil {
		t.Fatalf("narrow staff: %v", err)
	}

	for _, target := range []string{"/api/admin/carts", "/api/admin/carts/1"} {
		rec := do(t, app, http.MethodGet, target, bearer(staff))
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s without orders.read = %d, want 403", target, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), string(RightOrdersRead)) {
			t.Errorf("the refusal does not name orders.read: %s", rec.Body)
		}
	}
}

// The route SetEmail never had, and the reason the screen is not a list of
// anonymous baskets.
func TestSetCartEmailOverHTTP(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "MAIL-1", 1300, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)

	rec := doBody(t, app, http.MethodPut, "/api/carts/"+cart.Token+"/email",
		`{"email":"shopper@example.com"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT email = %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Data Cart `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Data.Email != "shopper@example.com" {
		t.Errorf("email = %q, want the address that was sent", out.Data.Email)
	}
	if !out.Data.ExpiresAt.After(cart.ExpiresAt) {
		t.Errorf("expires_at did not move; every other mutation extends the cart's life")
	}

	if rec := doBody(t, app, http.MethodPut, "/api/carts/"+cart.Token+"/email",
		`{"email":""}`); rec.Code != http.StatusOK {
		t.Errorf("clearing the address = %d, want 200", rec.Code)
	} else {
		var cleared struct {
			Data Cart `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &cleared)
		if cleared.Data.Email != "" {
			t.Errorf("an empty email left %q behind", cleared.Data.Email)
		}
	}

	if rec := doBody(t, app, http.MethodPut, "/api/carts/"+cart.Token+"/email",
		`{"email":"nonsense"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("an address with no @ = %d, want 400", rec.Code)
	}
	if rec := doBody(t, app, http.MethodPut, "/api/carts/nope/email",
		`{"email":"a@b.com"}`); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown token = %d, want 404", rec.Code)
	}

	// End to end: the address reaches the payload a recovery flow reads.
	if _, err := app.Cart().SetEmail(ctx, cart.Token, "recover@example.com"); err != nil {
		t.Fatalf("SetEmail: %v", err)
	}
	ageCart(t, app, cart.Token)
	if _, err := app.Cart().Abandon(ctx); err != nil {
		t.Fatalf("Abandon: %v", err)
	}
	var payload []byte
	if err := app.DB().QueryRowContext(ctx,
		`SELECT payload FROM outbox_events WHERE event_name = $1`, EventCartAbandoned).
		Scan(&payload); err != nil {
		t.Fatalf("read the event: %v", err)
	}
	var ev CartEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		t.Fatalf("decode the payload: %v", err)
	}
	if ev.Email != "recover@example.com" {
		t.Errorf("the abandonment payload carries %q, want the address the shopper typed", ev.Email)
	}

	// And a converted cart refuses.
	other := newCart(t, app)
	addToCart(t, app, other.Token, product.DefaultVariant().ID, 1)
	if _, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(other.Token), ""); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if rec := doBody(t, app, http.MethodPut, "/api/carts/"+other.Token+"/email",
		`{"email":"late@example.com"}`); rec.Code != http.StatusConflict {
		t.Errorf("setting an email on a converted cart = %d, want 409", rec.Code)
	}
}

// The diagnostic reports the three populations separately, because the two
// sweeps that keep them bounded now fail independently.
func TestDoctorNamesAbandonedCarts(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "DOC-1", 700, 5)
	live := newCart(t, app)
	addToCart(t, app, live.Token, product.DefaultVariant().ID, 1)

	gone := newCart(t, app)
	addToCart(t, app, gone.Token, product.DefaultVariant().ID, 1)
	ageCart(t, app, gone.Token)
	if _, err := app.Cart().Abandon(ctx); err != nil {
		t.Fatalf("Abandon: %v", err)
	}

	report := app.Diagnose(ctx)
	var carts *Diagnostic
	for i := range report.Checks {
		if report.Checks[i].Name == "carts" {
			carts = &report.Checks[i]
		}
	}
	if carts == nil {
		t.Fatal("doctor no longer reports a carts diagnostic")
	}
	if carts.Status != StatusOK {
		t.Errorf("carts = %s (%s)", carts.Status, carts.Hint)
	}
	for _, want := range []string{"1 live", "0 awaiting the sweeper", "1 abandoned"} {
		if !strings.Contains(carts.Detail, want) {
			t.Errorf("detail %q does not report %q", carts.Detail, want)
		}
	}
}
