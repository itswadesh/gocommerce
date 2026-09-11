package gocommerce

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// The shop's own note on an order. It rides the metadata column the row already
// had, which is why these tests are mostly about the two directions the
// reserved key must be closed in — refused on the way in, stripped on the way
// out — and about the one write in the engine that deliberately announces
// nothing.

func placeOrder(t *testing.T, app *App, sku string) *Order {
	t.Helper()
	product := simpleProduct(t, app, sku, 1000, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(context.Background(), CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout %s: %v", sku, err)
	}
	return result.Order
}

func metaPatch(kv map[string]any) OrderPatch {
	m := Metadata(kv)
	return OrderPatch{Metadata: &m}
}

func TestOrderNoteIsWrittenThroughTheMetadataPatch(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := placeOrder(t, app, "NOTE-1")

	const note = "Customer rang, leave it with the neighbour"
	if _, err := app.Order().Update(ctx, order.ID, metaPatch(map[string]any{
		OrderNoteKey: note,
	})); err != nil {
		t.Fatalf("write the note: %v", err)
	}
	got, err := app.Order().Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if got.Metadata[OrderNoteKey] != note {
		t.Fatalf("metadata = %v, want the note back", got.Metadata)
	}

	// Replaced whole, like every other patch that carries metadata: the
	// read-modify-write belongs to the caller.
	if _, err := app.Order().Update(ctx, order.ID, metaPatch(map[string]any{
		"mymodule": "kept",
	})); err != nil {
		t.Fatalf("replace the object: %v", err)
	}
	got, err = app.Order().Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if _, still := got.Metadata[OrderNoteKey]; still {
		t.Errorf("metadata = %v; a whole-object replace must not merge the old note back in", got.Metadata)
	}
	if got.Metadata["mymodule"] != "kept" {
		t.Errorf("metadata = %v, want the module's key", got.Metadata)
	}

	// A patch carrying nothing at all is still the same validation error: the
	// nil pointer is what says "this patch did not mention metadata".
	if _, err := app.Order().Update(ctx, order.ID, OrderPatch{}); !errors.Is(err, ErrValidation) {
		t.Errorf("an empty patch = %v, want a validation error", err)
	}
}

func TestANonStringNoteIsRefused(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := placeOrder(t, app, "NOTE-2")

	// The panel reads this key into a textarea and trims it. jsonb would take
	// either of these happily, and the operator would meet it as a crash.
	for _, bad := range []any{map[string]any{"text": "hi"}, 42, []any{"a"}, true} {
		_, err := app.Order().Update(ctx, order.ID, metaPatch(map[string]any{OrderNoteKey: bad}))
		if !errors.Is(err, ErrValidation) {
			t.Errorf("a %T note = %v, want a validation error", bad, err)
			continue
		}
		if !strings.Contains(err.Error(), "metadata."+OrderNoteKey) {
			t.Errorf("the error must name the field, got %q", err.Error())
		}
	}

	if _, err := app.Order().Update(ctx, order.ID, metaPatch(map[string]any{
		OrderNoteKey: "a string is fine",
	})); err != nil {
		t.Fatalf("a string note = %v, want it accepted", err)
	}

	// Only the reserved key is constrained: metadata is where a module attaches
	// its own data, and its shape is the module's business.
	if _, err := app.Order().Update(ctx, order.ID, metaPatch(map[string]any{
		"mymodule": map[string]any{"anything": 1},
	})); err != nil {
		t.Fatalf("a module's object = %v, want it accepted", err)
	}
}

func TestCheckoutRefusesTheReservedNoteKey(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	product := simpleProduct(t, app, "NOTE-3", 1000, 5)

	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	in := checkoutInput(cart.Token)
	in.Metadata = Metadata{OrderNoteKey: "written by a hostile storefront"}
	if _, err := app.Order().Checkout(ctx, CodeCOD, in, ""); !errors.Is(err, ErrValidation) {
		t.Fatalf("checkout carrying the reserved key = %v, want a validation error", err)
	}

	// The same guard covers the operator's phone order, because Create builds a
	// CheckoutInput and goes through the same validation.
	_, err := app.Order().Create(ctx, NewOrderInput{
		Email:    "shopper@example.com",
		Address:  Address{Line1: "1 Test Street", City: "Testville", PostalCode: "12345", Country: "US"},
		Lines:    []NewOrderLine{{VariantID: product.DefaultVariant().ID, Quantity: 1}},
		Metadata: Metadata{OrderNoteKey: "typed into the wrong field"},
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Orders.Create carrying the reserved key = %v, want a validation error", err)
	}

	// Over HTTP, and no order row is left behind by the refusal.
	before := countOrders(t, app)
	body := `{"cart_id":"` + cart.Token + `","email":"shopper@example.com",` +
		`"address":{"line1":"1 Test Street","city":"Testville","postal_code":"12345","country":"US"},` +
		`"metadata":{"` + OrderNoteKey + `":"hello"}}`
	rec := doBody(t, app, "POST", "/api/checkout/"+CodeCOD, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/checkout = %d: %s", rec.Code, rec.Body.String())
	}
	if after := countOrders(t, app); after != before {
		t.Errorf("the refusal created %d orders", after-before)
	}

	// Any other key still checks out and reads back, which is the whole point
	// of refusing exactly one.
	in = checkoutInput(cart.Token)
	in.Metadata = Metadata{"mymodule": "fine"}
	result, err := app.Order().Checkout(ctx, CodeCOD, in, "")
	if err != nil {
		t.Fatalf("checkout with a module key: %v", err)
	}
	if result.Order.Metadata["mymodule"] != "fine" {
		t.Errorf("metadata = %v, want the module's key stored", result.Order.Metadata)
	}
}

// A note is written precisely on the order that went wrong — "refunded manually
// by bank transfer, ref 88213" — so cancellation must not close the one surface
// that records what happened.
func TestANoteIsAcceptedOnACancelledOrder(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := placeOrder(t, app, "NOTE-4")
	if _, err := app.Order().Cancel(ctx, order.ID, "customer changed their mind"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	const note = "refunded manually by bank transfer, ref 88213"
	if _, err := app.Order().Update(ctx, order.ID, metaPatch(map[string]any{
		OrderNoteKey: note,
	})); err != nil {
		t.Fatalf("a note on a cancelled order = %v, want it accepted", err)
	}
	got, err := app.Order().Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if got.Metadata[OrderNoteKey] != note {
		t.Errorf("metadata = %v, want the note", got.Metadata)
	}

	// Everything else the patch reaches is still refused, and the relaxation is
	// gated on what the apply loop produced rather than on a list of field
	// names — so a seventh field added to OrderPatch tomorrow is refused here
	// on the day it lands, without anybody remembering to extend a predicate.
	email := "new@example.com"
	if _, err := app.Order().Update(ctx, order.ID, OrderPatch{Email: &email}); !errors.Is(err, ErrConflict) {
		t.Errorf("an email on a cancelled order = %v, want a conflict", err)
	}
	mixed := metaPatch(map[string]any{OrderNoteKey: "and a note"})
	mixed.Email = &email
	if _, err := app.Order().Update(ctx, order.ID, mixed); !errors.Is(err, ErrConflict) {
		t.Errorf("an email and a note together = %v, want a conflict", err)
	}
}

func TestANoteAnnouncesNothingButAMetadataChangeDoes(t *testing.T) {
	rec := &recordingNotifier{}
	app := newTestApp(t, &notifyModule{rec: rec})
	ctx := context.Background()
	order := placeOrder(t, app, "NOTE-5")
	if _, err := app.DrainOutbox(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	base := rec.count(EventOrderEdited)

	drainedCount := func(t *testing.T) int {
		t.Helper()
		if _, err := app.DrainOutbox(ctx); err != nil {
			t.Fatalf("drain: %v", err)
		}
		return rec.count(EventOrderEdited)
	}

	// A note, and nothing else. Every order.* event reaches the notifier bridge
	// and fans out to the customer's email and phone, so announcing this would
	// put "your order has been updated" in the shopper's inbox once per line an
	// operator wrote to themselves.
	if _, err := app.Order().Update(ctx, order.ID, metaPatch(map[string]any{
		OrderNoteKey: "spoke to the warehouse",
	})); err != nil {
		t.Fatalf("write a note: %v", err)
	}
	if got := drainedCount(t); got != base {
		t.Errorf("a note announced %d order.edited events, want none", got-base)
	}

	// A second note, over the first. Still nothing but the note moved.
	if _, err := app.Order().Update(ctx, order.ID, metaPatch(map[string]any{
		OrderNoteKey: "spoke to the warehouse again",
	})); err != nil {
		t.Fatalf("rewrite the note: %v", err)
	}
	if got := drainedCount(t); got != base {
		t.Errorf("rewriting a note announced %d events, want none", got-base)
	}

	// The same shape of patch, but it also adds a module's key. Silence has to
	// be earned: this is a real metadata change and says so.
	if _, err := app.Order().Update(ctx, order.ID, metaPatch(map[string]any{
		OrderNoteKey: "spoke to the warehouse again",
		"mymodule":   "arrived",
	})); err != nil {
		t.Fatalf("add a module key: %v", err)
	}
	if got := drainedCount(t); got != base+1 {
		t.Errorf("adding a module key announced %d events, want exactly one", got-base)
	}

	// And dropping one, which is the destructive half of a whole-object
	// replace. Loud beats silent-and-destructive.
	if _, err := app.Order().Update(ctx, order.ID, metaPatch(map[string]any{
		OrderNoteKey: "spoke to the warehouse again",
	})); err != nil {
		t.Fatalf("drop the module key: %v", err)
	}
	if got := drainedCount(t); got != base+2 {
		t.Errorf("dropping a module key announced %d events, want exactly one more", got-base-1)
	}

	// An ordinary correction is unchanged by any of this.
	email := "corrected@example.com"
	if _, err := app.Order().Update(ctx, order.ID, OrderPatch{Email: &email}); err != nil {
		t.Fatalf("correct the email: %v", err)
	}
	if got := drainedCount(t); got != base+3 {
		t.Errorf("an email correction announced %d events, want one", got-base-2)
	}
}

// Quiet in the outbox is not quiet in the trail. Nothing else records that a
// note was written, and "who wrote this and what did it say before" is exactly
// what somebody reading the order back later is asking.
func TestANoteIsStillRecordedInTheTrail(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := placeOrder(t, app, "NOTE-6")

	if _, err := app.Order().Update(ctx, order.ID, metaPatch(map[string]any{
		OrderNoteKey: "rang the customer",
	})); err != nil {
		t.Fatalf("write a note: %v", err)
	}
	rows := auditRows(t, app, "action = $1", AuditOrderNote)
	if len(rows) != 1 {
		t.Fatalf("the trail holds %d order.note rows, want one", len(rows))
	}
	if rows[0].Changes.Event != "" {
		t.Errorf("the note's audit row claims event %q; it publishes none", rows[0].Changes.Event)
	}
	if rows[0].EntityLabel != order.Number {
		t.Errorf("the row is filed against %q, want %s", rows[0].EntityLabel, order.Number)
	}
}

func TestGuestOrderHidesTheOperatorsNote(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "NOTE-7", 1000, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	in := checkoutInput(cart.Token)
	in.Metadata = Metadata{"mymodule": "a storefront's own key"}
	result, err := app.Order().Checkout(ctx, CodeCOD, in, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	// The other direction of the same method: the checkout response still
	// carries the token, which is the shopper's only handle on their own order.
	if result.Order.AccessToken == "" {
		t.Fatal("the checkout response lost its access token; Redact must not be wired into it")
	}
	token := result.Order.AccessToken
	number := result.Order.Number

	const note = "this customer disputes everything"
	if _, err := app.Order().Update(ctx, result.Order.ID, metaPatch(map[string]any{
		OrderNoteKey: note,
		"mymodule":   "a storefront's own key",
	})); err != nil {
		t.Fatalf("write the note: %v", err)
	}

	guest, err := app.Order().GetForGuest(ctx, number, token)
	if err != nil {
		t.Fatalf("guest read: %v", err)
	}
	if _, leaked := guest.Metadata[OrderNoteKey]; leaked {
		t.Errorf("the guest's copy carries the note: %v", guest.Metadata)
	}
	if guest.Metadata["mymodule"] != "a storefront's own key" {
		t.Errorf("Redact took more than the reserved key: %v", guest.Metadata)
	}
	if guest.AccessToken != "" {
		t.Error("the guest read echoed the access token back")
	}

	// And over the wire, which is where it would actually leak.
	rec := do(t, app, "GET", "/api/orders/"+number+"?token="+token)
	if rec.Code != http.StatusOK {
		t.Fatalf("guest route = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), note) {
		t.Errorf("the guest response carries the note:\n%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "a storefront's own key") {
		t.Errorf("the guest response lost the module's key:\n%s", rec.Body.String())
	}

	// The operator's own read still has it, which is the whole point.
	admin, err := app.Order().Get(ctx, result.Order.ID)
	if err != nil {
		t.Fatalf("admin read: %v", err)
	}
	if admin.Metadata[OrderNoteKey] != note {
		t.Errorf("the admin read lost the note: %v", admin.Metadata)
	}
}

func countOrders(t *testing.T, app *App) int {
	t.Helper()
	var n int
	if err := app.DB().QueryRowContext(context.Background(),
		`SELECT count(*) FROM orders`).Scan(&n); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	return n
}
