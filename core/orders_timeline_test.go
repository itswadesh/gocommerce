package gocommerce

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// An order's history, merged from the two tables it is spread across. What the
// tests below are really pinning is that one moment renders once: a transition
// an operator caused writes an outbox row and an audit row in the same
// transaction, and a reader that showed both would double every line.

func timelineOf(t *testing.T, app *App, id int64) []OrderTimelineEntry {
	t.Helper()
	entries, total, err := app.Order().Timeline(context.Background(), id, 0, 0)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if total != len(entries) {
		t.Errorf("the page holds %d entries and the total says %d", len(entries), total)
	}
	return entries
}

func names(entries []OrderTimelineEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		switch {
		case e.Name != "":
			out = append(out, e.Name)
		default:
			out = append(out, e.Action)
		}
	}
	return out
}

func TestOrderTimelineIsTheOrdersOwnEvents(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	order := placeOrder(t, app, "TIME-1")
	// A second order, to prove the per-aggregate scoping.
	other := placeOrder(t, app, "TIME-2")

	if _, err := app.Pay().MarkPaid(ctx, order.ID, "ref-1"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	if _, err := app.Ship().Create(ctx, order.ID, ProviderManual, ShipRequest{Tracking: "TRACK-9"}); err != nil {
		t.Fatalf("ship: %v", err)
	}
	if _, err := app.Order().MarkDelivered(ctx, order.ID); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if _, err := app.DrainOutbox(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}

	entries := timelineOf(t, app, order.ID)
	want := []string{EventOrderCreated, EventOrderPaid, EventOrderShipped, EventOrderDelivered}
	if got := names(entries); !equalStrings(got, want) {
		t.Fatalf("timeline = %v, want %v", got, want)
	}
	for _, e := range entries {
		if e.PublishedAt == nil {
			t.Errorf("%s has no published_at after a drain", e.Name)
		}
		if e.EventID == "" {
			t.Errorf("%s carries no event id", e.Name)
		}
		// The uuid the dispatcher delivers under, not the row id — that is what
		// makes an entry stable across a redelivery.
		if !strings.Contains(e.EventID, "-") {
			t.Errorf("%s has event id %q, want the event uuid", e.Name, e.EventID)
		}
	}
	shipped := entries[2]
	if shipped.Tracking != "TRACK-9" {
		t.Errorf("the shipped entry carries tracking %q, want TRACK-9", shipped.Tracking)
	}
	if shipped.Status != OrderShipped {
		t.Errorf("the shipped entry says status %q", shipped.Status)
	}

	// Nothing from the other order leaked in, and it has a history of its own.
	if got := names(timelineOf(t, app, other.ID)); !equalStrings(got, []string{EventOrderCreated}) {
		t.Errorf("the second order's timeline = %v, want just its creation", got)
	}
}

// The merge, which is the whole reason this is one route. A transition an
// operator caused is one moment, one entry, carrying the actor from the trail
// and the delivery state from the outbox.
func TestOrderTimelineMergesTheTrailWithTheOutbox(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := placeOrder(t, app, "TIME-3")
	token := signInAs(t, app, "manager@example.com", RoleManager)
	id := itoa(order.ID)

	if rec := doBody(t, app, "POST", "/api/admin/orders/"+id+"/mark-paid",
		`{"reference":"R-88"}`, bearer(token)); rec.Code != http.StatusOK {
		t.Fatalf("mark paid = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := app.DrainOutbox(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}

	entries := timelineOf(t, app, order.ID)
	if len(entries) != 2 {
		t.Fatalf("timeline = %v, want the creation and one payment — a merged moment "+
			"must not render twice", names(entries))
	}

	// The checkout: nobody was signed in, so it is an event and says so rather
	// than inventing an actor.
	created := entries[0]
	if created.Kind != "event" || created.Name != EventOrderCreated {
		t.Errorf("the creation entry is %+v, want an event", created)
	}
	if created.ActorEmail != "" {
		t.Errorf("the creation names %q as an actor; a shopper is not an operator", created.ActorEmail)
	}

	paid := entries[1]
	if paid.Kind != "action" {
		t.Errorf("the payment is kind %q, want action", paid.Kind)
	}
	if paid.Name != EventOrderPaid || paid.Action != AuditOrderMarkPaid {
		t.Errorf("the payment entry is name=%q action=%q, want both halves", paid.Name, paid.Action)
	}
	if paid.ActorEmail != "manager@example.com" || paid.ActorKind != ActorOperator {
		t.Errorf("the payment names %q (%s), want the manager who clicked it",
			paid.ActorEmail, paid.ActorKind)
	}
	if paid.PublishedAt == nil || paid.EventID == "" {
		t.Errorf("the merged entry lost its delivery state: %+v", paid)
	}
	if paid.Summary == "" {
		t.Errorf("the merged entry lost its summary")
	}
	if paid.After["payment_status"] != PaymentPaid {
		t.Errorf("the merged entry lost its change block: before=%v after=%v", paid.Before, paid.After)
	}
}

// An operator's note publishes nothing, so it is the one act that reaches this
// screen through the trail alone — and the one proof that the merge is not just
// a decoration on the outbox.
func TestOrderTimelineCarriesAnActThatAnnouncedNothing(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := placeOrder(t, app, "TIME-4")

	if _, err := app.Order().Update(ctx, order.ID, metaPatch(map[string]any{
		OrderNoteKey: "rang the customer, no answer",
	})); err != nil {
		t.Fatalf("write a note: %v", err)
	}

	entries := timelineOf(t, app, order.ID)
	if len(entries) != 2 {
		t.Fatalf("timeline = %v, want the creation and the note", names(entries))
	}
	note := entries[1]
	if note.Kind != "action" || note.Action != AuditOrderNote {
		t.Fatalf("the note entry is %+v, want an action", note)
	}
	if note.Name != "" {
		t.Errorf("the note claims event %q; it publishes none", note.Name)
	}
	if note.EventID != "" || note.PublishedAt != nil || note.Dead {
		t.Errorf("the note carries delivery state it cannot have: %+v", note)
	}
}

func TestOrderTimelineCarriesTheChangeBlock(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "TIME-5", 1000, 10)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	order := result.Order

	if _, _, err := app.Order().EditLines(ctx, order.ID, OrderEdit{
		Lines: []OrderLineEdit{{ID: order.Lines[0].ID, Quantity: 3}},
	}); err != nil {
		t.Fatalf("edit lines: %v", err)
	}
	// And a contact correction, which emits the same event name and carries no
	// change block at all. That difference is how the panel tells "items
	// changed" from "details corrected".
	email := "moved@example.com"
	if _, err := app.Order().Update(ctx, order.ID, OrderPatch{Email: &email}); err != nil {
		t.Fatalf("correct the email: %v", err)
	}

	entries := timelineOf(t, app, order.ID)
	if len(entries) != 3 {
		t.Fatalf("timeline = %v, want three entries", names(entries))
	}
	edited, corrected := entries[1], entries[2]
	if edited.Name != EventOrderEdited || edited.Change == nil {
		t.Fatalf("the line edit entry is %+v, want an order.edited carrying a change", edited)
	}
	if len(edited.Change.LinesChanged) == 0 {
		t.Errorf("the change block names nothing: %+v", edited.Change)
	}
	if edited.Change.TotalAfter.AmountMinor <= edited.Change.TotalBefore.AmountMinor {
		t.Errorf("the change block's totals did not move: %+v", edited.Change)
	}
	if corrected.Name != EventOrderEdited {
		t.Fatalf("the correction is %q, want order.edited", corrected.Name)
	}
	if corrected.Change != nil {
		t.Errorf("a contact correction carries a change block: %+v", corrected.Change)
	}
	if corrected.After["email"] != email {
		t.Errorf("the correction did not record which field moved: %v", corrected.After)
	}
}

// "Did the customer's confirmation actually go out" is the question this route
// exists to answer, and half of it is answerable only when it did not.
func TestOrderTimelineReportsAFailedDelivery(t *testing.T) {
	flaky := &flakyModule{failures: 5}
	app := newTestApp(t, flaky)
	ctx := context.Background()
	order := placeOrder(t, app, "TIME-6")

	if _, err := app.DrainOutbox(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	entries := timelineOf(t, app, order.ID)
	if len(entries) != 1 {
		t.Fatalf("timeline = %v, want the creation", names(entries))
	}
	created := entries[0]
	if created.PublishedAt != nil {
		t.Error("a failed delivery must not report a publication")
	}
	if created.Attempts < 1 {
		t.Errorf("attempts = %d, want at least one", created.Attempts)
	}
	if created.LastError == "" {
		t.Error("the service must return what the handler said; the handler blanks it, not this")
	}
	if !strings.Contains(created.LastError, "bad day") {
		t.Errorf("last_error = %q, want the consumer's own words", created.LastError)
	}
}

func TestOrderTimelineHidesTheFailureReasonWithoutStoreOperate(t *testing.T) {
	flaky := &flakyModule{failures: 5}
	app := newTestApp(t, flaky)
	ctx := context.Background()
	order := placeOrder(t, app, "TIME-7")
	if _, err := app.DrainOutbox(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	path := "/api/admin/orders/" + itoa(order.ID) + "/timeline"

	// Staff hold orders.read and not store.operate. last_error is a handler's
	// raw words and can carry an upstream URL or a fragment of a key.
	staff := signInAs(t, app, "staff@example.com", RoleStaff)
	rec := do(t, app, "GET", path, bearer(staff))
	if rec.Code != http.StatusOK {
		t.Fatalf("staff timeline = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "last_error") {
		t.Errorf("a reader without store.operate was shown the failure reason:\n%s", rec.Body.String())
	}
	// The delivery state itself is not a secret: staff must still be able to
	// see that a notification did not go out.
	if !strings.Contains(rec.Body.String(), `"attempts"`) {
		t.Errorf("the attempts count went with it:\n%s", rec.Body.String())
	}

	// An owner carries every right, including store.operate.
	owner := signInAs(t, app, "owner@example.com", RoleOwner)
	rec = do(t, app, "GET", path, bearer(owner))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "last_error") {
		t.Errorf("owner timeline = %d, want the failure reason:\n%s", rec.Code, rec.Body.String())
	}

	// A static admin token carries every right by the same rule.
	rec = do(t, app, "GET", path, withAdmin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "last_error") {
		t.Errorf("token timeline = %d, want the failure reason:\n%s", rec.Code, rec.Body.String())
	}
}

func TestOrderTimelineRouteNeedsOrdersRead(t *testing.T) {
	app := newTestApp(t)
	order := placeOrder(t, app, "TIME-8")
	path := "/api/admin/orders/" + itoa(order.ID) + "/timeline"

	if rec := do(t, app, "GET", path); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d, want 401", rec.Code)
	}

	rec := do(t, app, "GET", path, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("timeline = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []OrderTimelineEntry `json:"data"`
		Meta ListMeta             `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Meta.Total != 1 || len(body.Data) != 1 {
		t.Fatalf("body = %s, want one entry and a meta block", rec.Body.String())
	}

	// An id no order has is a 404. "No such order" and "nothing happened to
	// this order" are different answers to different questions.
	if rec := do(t, app, "GET", "/api/admin/orders/999999/timeline", withAdmin); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown order = %d, want 404", rec.Code)
	}
	if _, _, err := app.Order().Timeline(context.Background(), 999999, 0, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("Timeline on an unknown order = %v, want not-found", err)
	}
	if rec := do(t, app, "GET", "/api/admin/orders/banana/timeline", withAdmin); rec.Code != http.StatusBadRequest {
		t.Errorf("a non-numeric id = %d, want 400", rec.Code)
	}
	if rec := do(t, app, "GET", path+"?limit=500", withAdmin); rec.Code != http.StatusBadRequest {
		t.Errorf("an over-large limit = %d, want 400", rec.Code)
	}

	// An order with nothing recorded serializes as [], never null: the panel
	// iterates it without a guard, and so does everybody else.
	if _, err := app.DB().ExecContext(context.Background(),
		`DELETE FROM outbox_events WHERE aggregate_id = $1`, order.ID); err != nil {
		t.Fatalf("empty the outbox: %v", err)
	}
	rec = do(t, app, "GET", path, withAdmin)
	if !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Errorf("an empty history = %s, want an empty array", rec.Body.String())
	}
}

// Paging over a merged, two-table read has to be a total order, or a page
// boundary repeats an entry or loses one.
func TestOrderTimelinePagesWithoutRepeatingAnything(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := placeOrder(t, app, "TIME-9")

	for i := 0; i < 4; i++ {
		if _, err := app.Order().Update(ctx, order.ID, metaPatch(map[string]any{
			OrderNoteKey: "note " + itoa(int64(i)),
		})); err != nil {
			t.Fatalf("note %d: %v", i, err)
		}
	}

	all := timelineOf(t, app, order.ID)
	if len(all) != 5 {
		t.Fatalf("timeline = %v, want the creation and four notes", names(all))
	}
	seen := map[string]bool{}
	for offset := 0; offset < len(all); offset += 2 {
		page, total, err := app.Order().Timeline(ctx, order.ID, 2, offset)
		if err != nil {
			t.Fatalf("page at %d: %v", offset, err)
		}
		if total != len(all) {
			t.Errorf("page at %d reports a total of %d, want %d", offset, total, len(all))
		}
		for _, e := range page {
			key := e.At.String() + e.Action + e.Name + e.Summary
			if seen[key] {
				t.Errorf("page at %d repeated an entry: %+v", offset, e)
			}
			seen[key] = true
		}
	}
	if len(seen) != len(all) {
		t.Errorf("paging saw %d of %d entries", len(seen), len(all))
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
