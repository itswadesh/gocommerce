package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The audit trail's tests are about one question: does the row exist exactly
// when the change it records does, and does it say who. Everything else — the
// shape of the feed, the rights on the routes — follows from that.

// ------------------------------------------------------------------ fixtures

// auditRows reads the table directly. The service is what the panel uses; a
// test that only read through the service could not tell a missing row from a
// filtered one.
func auditRows(t *testing.T, app *App, where string, args ...any) []*AuditEntry {
	t.Helper()
	clause := ""
	if where != "" {
		clause = " WHERE " + where
	}
	rows, err := app.DB().QueryContext(context.Background(),
		auditSelect+clause+" ORDER BY id", args...)
	if err != nil {
		t.Fatalf("read admin_audit: %v", err)
	}
	entries, err := scanAuditRows(rows)
	if err != nil {
		t.Fatalf("scan admin_audit: %v", err)
	}
	return entries
}

func auditCount(t *testing.T, app *App) int {
	t.Helper()
	var n int
	if err := app.DB().QueryRowContext(context.Background(),
		`SELECT count(*) FROM admin_audit`).Scan(&n); err != nil {
		t.Fatalf("count admin_audit: %v", err)
	}
	return n
}

func onlyRow(t *testing.T, rows []*AuditEntry) *AuditEntry {
	t.Helper()
	if len(rows) != 1 {
		t.Fatalf("want exactly one audit row, got %d", len(rows))
	}
	return rows[0]
}

// paidOrder places an order through the public path and marks it paid, so a
// test starts from a live confirmed sale with no audit rows of its own.
func paidOrder(t *testing.T, app *App, sku string, priceMinor int64, stock int) *Order {
	t.Helper()
	ctx := context.Background()
	product := simpleProduct(t, app, sku, priceMinor, stock)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, ""); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	order, err := app.Order().Get(ctx, result.Order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	return order
}

// jsonNumber asserts a changes value decoded as a JSON number rather than as a
// string. It is what makes a future "helpful" formatted amount fail (rule 6).
func jsonNumber(t *testing.T, m map[string]any, key string) float64 {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("changes has no %q (it has %v)", key, m)
	}
	n, ok := v.(float64)
	if !ok {
		t.Fatalf("%s = %#v, want a JSON number", key, v)
	}
	return n
}

// ----------------------------------------------------------- the actor seam

// The whole seam end to end — middleware, context, service, row — with no
// service signature having changed.
func TestCancellingAnOrderRecordsWhoDidIt(t *testing.T) {
	app := newTestApp(t)
	token := signInAs(t, app, "manager@example.com", RoleManager)
	order := paidOrder(t, app, "AUD-CANCEL", 2500, 3)

	before := auditCount(t, app)
	rec := doBody(t, app, "POST", "/api/admin/orders/"+strconv.FormatInt(order.ID, 10)+"/cancel",
		`{"reason":"customer changed their mind"}`, bearer(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d: %s", rec.Code, rec.Body.String())
	}

	rows := auditRows(t, app, "action = $1", AuditOrderCancel)
	row := onlyRow(t, rows)
	if auditCount(t, app) != before+1 {
		t.Errorf("cancelling wrote %d rows, want 1", auditCount(t, app)-before)
	}
	if row.ActorKind != ActorOperator {
		t.Errorf("actor_kind = %q, want %q", row.ActorKind, ActorOperator)
	}
	if row.ActorID == nil {
		t.Fatal("actor_id is null for a signed-in operator")
	}
	if row.ActorEmail != "manager@example.com" || row.ActorRole != RoleManager {
		t.Errorf("actor = %s/%s, want manager@example.com/manager", row.ActorEmail, row.ActorRole)
	}
	if row.EntityType != AuditEntityOrder || row.EntityID != strconv.FormatInt(order.ID, 10) {
		t.Errorf("entity = %s/%s, want order/%d", row.EntityType, row.EntityID, order.ID)
	}
	if row.EntityLabel != order.Number {
		t.Errorf("entity_label = %q, want the order number %q", row.EntityLabel, order.Number)
	}
	if !strings.Contains(row.Summary, "customer changed their mind") {
		t.Errorf("summary = %q, want it to carry the reason", row.Summary)
	}
	if got := row.Changes.Before["status"]; got != OrderConfirmed {
		t.Errorf("changes.before.status = %v, want confirmed", got)
	}
	if got := row.Changes.After["status"]; got != OrderCancelled {
		t.Errorf("changes.after.status = %v, want cancelled", got)
	}
	if row.Changes.Event != EventOrderCancelled {
		t.Errorf("changes.event = %q, want %q", row.Changes.Event, EventOrderCancelled)
	}
}

// The two credentials bearerAuth accepts have to stay distinguishable in the
// record: a script is not a person, and it is not the store either.
func TestAStaticTokenIsRecordedAsATokenNotAPerson(t *testing.T) {
	app := newTestApp(t)
	order := paidOrder(t, app, "AUD-TOKEN", 1500, 2)

	rec := doBody(t, app, "POST", "/api/admin/orders/"+strconv.FormatInt(order.ID, 10)+"/cancel",
		`{"reason":"scripted"}`, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d: %s", rec.Code, rec.Body.String())
	}

	row := onlyRow(t, auditRows(t, app, "action = $1", AuditOrderCancel))
	if row.ActorKind != ActorToken {
		t.Errorf("actor_kind = %q, want %q", row.ActorKind, ActorToken)
	}
	if row.ActorID != nil {
		t.Errorf("actor_id = %v, want null: there is nobody behind a token", *row.ActorID)
	}
	if row.ActorEmail != "" || row.ActorRole != "" {
		t.Errorf("a token carries an identity it does not have: %q/%q", row.ActorEmail, row.ActorRole)
	}
}

// Two hundred cancellations at 3am must not read as somebody's night's work.
func TestTheSweeperIsRecordedAsTheSystem(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	product := simpleProduct(t, app, "AUD-SWEEP", 900, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	// Cash on delivery confirms immediately, so the order has to be put back
	// where the sweeper can see it: pending, unpaid and expired.
	if _, err := app.DB().ExecContext(ctx, `
		UPDATE orders SET status = $2, payment_status = $3,
		                  reservation_expires_at = now() - interval '1 hour'
		WHERE id = $1`, result.Order.ID, OrderPending, PaymentPending); err != nil {
		t.Fatalf("expire the reservation: %v", err)
	}

	if _, err := app.Order().SweepUnpaid(ctx); err != nil {
		t.Fatalf("SweepUnpaid: %v", err)
	}

	row := onlyRow(t, auditRows(t, app, "action = $1", AuditOrderCancel))
	if row.ActorKind != ActorSystem {
		t.Errorf("actor_kind = %q, want %q", row.ActorKind, ActorSystem)
	}
	if row.ActorLabel != "unpaid sweeper" {
		t.Errorf("actor_label = %q, want it to name the sweeper", row.ActorLabel)
	}
	if !strings.Contains(row.Summary, "payment not completed in time") {
		t.Errorf("summary = %q, want the sweeper's reason", row.Summary)
	}
}

// The sale's hot transaction stays as it was, and the trail records operator
// acts rather than customer ones.
func TestGuestCheckoutWritesNoAuditRow(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	product := simpleProduct(t, app, "AUD-GUEST", 1200, 4)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 2)
	// Counted after the product exists: creating it is an operator act and has
	// a row of its own. What is being measured is the sale.
	before := auditCount(t, app)

	if _, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), ""); err != nil {
		t.Fatalf("checkout: %v", err)
	}

	if n := auditCount(t, app) - before; n != 0 {
		t.Errorf("a guest checkout wrote %d audit rows, want none", n)
	}
}

// ---------------------------------------------------- the vocabulary is separate

// Both emit order.edited. A trail keyed off event names could not tell them
// apart, which is the whole reason Action is not Event.
func TestEditingLinesAndEditingTheAddressAreDifferentActions(t *testing.T) {
	app := newTestApp(t)
	token := signInAs(t, app, "owner@example.com", RoleOwner)
	ctx := context.Background()
	product := simpleProduct(t, app, "AUD-EDIT", 500, 10)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	id := strconv.FormatInt(result.Order.ID, 10)

	body := fmt.Sprintf(`{"lines":[{"variant_id":%d,"quantity":3}]}`, product.DefaultVariant().ID)
	if rec := doBody(t, app, "PUT", "/api/admin/orders/"+id+"/lines", body, bearer(token)); rec.Code != http.StatusOK {
		t.Fatalf("edit lines = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := doBody(t, app, "PATCH", "/api/admin/orders/"+id,
		`{"address":{"line1":"2 Other Street","city":"Elsewhere","postal_code":"54321","country":"US"}}`,
		bearer(token)); rec.Code != http.StatusOK {
		t.Fatalf("patch order = %d: %s", rec.Code, rec.Body.String())
	}

	lines := onlyRow(t, auditRows(t, app, "action = $1", AuditOrderEditLines))
	update := onlyRow(t, auditRows(t, app, "action = $1", AuditOrderUpdate))
	for _, row := range []*AuditEntry{lines, update} {
		if row.Changes.Event != EventOrderEdited {
			t.Errorf("%s carries event %q, want %q", row.Action, row.Changes.Event, EventOrderEdited)
		}
	}
	if _, ok := lines.Changes.After["lines_added"]; !ok {
		t.Errorf("the line edit records %v, want it to name what moved", lines.Changes.After)
	}
	if got := update.Changes.After["address"]; got == nil {
		t.Errorf("the address change records %v, want the new address", update.Changes.After)
	}
	if _, ok := update.Changes.Before["address"]; !ok {
		t.Error("the address change has no before: the old address was readable and should be recorded")
	}
}

// The loudest question the trail exists to answer. Until M24 a refund also
// produced no event at all; it publishes order.refunded now, and the audit row
// names it, which is the join a merged order timeline renders each fact once by.
func TestARefundIsRecordedWithItsAmount(t *testing.T) {
	app := newTestApp(t, refundableModule{})
	ctx := context.Background()
	token := signInAs(t, app, "owner@example.com", RoleOwner)

	product := simpleProduct(t, app, "AUD-REFUND", 4000, 2)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(ctx, "refundable", checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "ref-1"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	id := strconv.FormatInt(result.Order.ID, 10)

	if rec := doBody(t, app, "POST", "/api/admin/orders/"+id+"/refund",
		`{"amount_minor":2000}`, bearer(token)); rec.Code != http.StatusOK {
		t.Fatalf("refund = %d: %s", rec.Code, rec.Body.String())
	}

	row := onlyRow(t, auditRows(t, app, "action = $1", AuditOrderRefund))
	if row.Changes.Event != EventOrderRefunded {
		t.Errorf("changes.event = %q, want %q: the row and the event it published are correlated by this",
			row.Changes.Event, EventOrderRefunded)
	}
	if got := jsonNumber(t, row.Changes.After, "amount_minor"); got != 2000 {
		t.Errorf("amount_minor = %v, want 2000", got)
	}
	if row.Changes.After["currency"] == nil {
		t.Error("a refund with an amount and no currency is half a record")
	}
	if strings.ContainsAny(row.Summary, "£$€") {
		t.Errorf("summary = %q: money is minor units plus a currency, never a formatted string", row.Summary)
	}
}

// The mark-paid row carries its reference, and the transition seam merges the
// payment status it moved.
func TestMarkingPaidRecordsTheReference(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	token := signInAs(t, app, "owner@example.com", RoleOwner)
	product := simpleProduct(t, app, "AUD-PAID", 700, 3)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}

	if rec := doBody(t, app, "POST",
		"/api/admin/orders/"+strconv.FormatInt(result.Order.ID, 10)+"/mark-paid",
		`{"reference":"BANK-99"}`, bearer(token)); rec.Code != http.StatusOK {
		t.Fatalf("mark paid = %d: %s", rec.Code, rec.Body.String())
	}

	row := onlyRow(t, auditRows(t, app, "action = $1", AuditOrderMarkPaid))
	if row.Changes.After["payment_reference"] != "BANK-99" {
		t.Errorf("after = %v, want the reference", row.Changes.After)
	}
	if row.Changes.Before["payment_status"] != PaymentPending ||
		row.Changes.After["payment_status"] != PaymentPaid {
		t.Errorf("payment_status not recorded either side: %v -> %v",
			row.Changes.Before["payment_status"], row.Changes.After["payment_status"])
	}
}

// --------------------------------------------------- committing with the change

// The row commits with the change or not at all. This is the test that fails if
// the insert ever moves outside the transaction, or before the guard.
func TestAFailedTransitionLeavesNoTrail(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	token := signInAs(t, app, "owner@example.com", RoleOwner)
	order := paidOrder(t, app, "AUD-FAIL", 3000, 2)

	if _, err := app.Ship().Create(ctx, order.ID, "", ShipRequest{Tracking: "TRACK-1"}); err != nil {
		t.Fatalf("ship: %v", err)
	}
	before := auditCount(t, app)

	rec := doBody(t, app, "POST", "/api/admin/orders/"+strconv.FormatInt(order.ID, 10)+"/cancel",
		`{"reason":"too late"}`, bearer(token))
	if rec.Code != http.StatusConflict {
		t.Fatalf("cancelling a shipped order = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if got := auditCount(t, app); got != before {
		t.Errorf("a refused cancellation wrote %d rows", got-before)
	}
}

// The failure mode this design chose, written down so nobody later "fixes" it
// with a SAVEPOINT without meaning to.
func TestAFailedAuditWriteRollsBackTheChange(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := paidOrder(t, app, "AUD-ROLLBACK", 1800, 2)

	if _, err := app.DB().ExecContext(ctx,
		`ALTER TABLE admin_audit RENAME TO admin_audit_gone`); err != nil {
		t.Fatalf("rename the table: %v", err)
	}

	if _, err := app.Order().Cancel(ctx, order.ID, "should not stick"); err == nil {
		t.Fatal("cancelling with no audit table should fail, not succeed quietly")
	}

	after, err := app.Order().Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if after.Status != OrderConfirmed {
		t.Errorf("status = %q, want the change rolled back to confirmed", after.Status)
	}
}

// The correlation a merged order timeline is built on, and why: now() is
// transaction_timestamp(), so both rows of one transaction share an instant.
func TestAnAuditRowAndItsEventShareAnInstant(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := paidOrder(t, app, "AUD-INSTANT", 2200, 2)

	if _, err := app.Order().Cancel(ctx, order.ID, "reconciliation"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	row := onlyRow(t, auditRows(t, app, "action = $1", AuditOrderCancel))
	var eventAt time.Time
	if err := app.DB().QueryRowContext(ctx, `
		SELECT created_at FROM outbox_events
		WHERE aggregate_type = $1 AND aggregate_id = $2 AND event_name = $3`,
		AggregateOrder, order.ID, EventOrderCancelled).Scan(&eventAt); err != nil {
		t.Fatalf("read the outbox row: %v", err)
	}
	if !row.CreatedAt.Equal(eventAt) {
		t.Errorf("audit at %v, event at %v: they were written by one transaction and must share an instant",
			row.CreatedAt, eventAt)
	}
	if row.Changes.Event != EventOrderCancelled {
		t.Errorf("changes.event = %q, want the outbox event it is paired with", row.Changes.Event)
	}
}

// ------------------------------------------------------------ the other services

// The row that answers "who dropped the price", filed against the product
// because that is the screen the operator is on.
func TestAPriceChangeNamesTheOperatorAndBothPrices(t *testing.T) {
	app := newTestApp(t)
	token := signInAs(t, app, "manager@example.com", RoleManager)
	product := simpleProduct(t, app, "AUD-PRICE", 5000, 1)
	variant := product.DefaultVariant()

	rec := doBody(t, app, "PATCH", "/api/admin/variants/"+strconv.FormatInt(variant.ID, 10),
		`{"price_minor":2500}`, bearer(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("patch variant = %d: %s", rec.Code, rec.Body.String())
	}

	row := onlyRow(t, auditRows(t, app, "action = $1", AuditVariantUpdate))
	if row.EntityType != AuditEntityProduct {
		t.Errorf("entity_type = %q, want %q: a variant edit is filed against its product",
			row.EntityType, AuditEntityProduct)
	}
	if row.EntityID != strconv.FormatInt(product.ID, 10) {
		t.Errorf("entity_id = %q, want the product id %d", row.EntityID, product.ID)
	}
	if got := jsonNumber(t, row.Changes.Before, "price_minor"); got != 5000 {
		t.Errorf("before.price_minor = %v, want 5000", got)
	}
	if got := jsonNumber(t, row.Changes.After, "price_minor"); got != 2500 {
		t.Errorf("after.price_minor = %v, want 2500", got)
	}
	if row.ActorEmail != "manager@example.com" {
		t.Errorf("actor = %q", row.ActorEmail)
	}
}

// What the missing foreign key buys, so it is the test that stops somebody
// adding one back.
func TestAnEntryOutlivesTheOperator(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	signInAs(t, app, "owner@example.com", RoleOwner)
	token := signInAs(t, app, "leaving@example.com", RoleOwner)
	order := paidOrder(t, app, "AUD-GHOST", 1300, 2)

	rec := doBody(t, app, "POST", "/api/admin/orders/"+strconv.FormatInt(order.ID, 10)+"/cancel",
		`{"reason":"before leaving"}`, bearer(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d: %s", rec.Code, rec.Body.String())
	}
	row := onlyRow(t, auditRows(t, app, "action = $1", AuditOrderCancel))
	actorID := *row.ActorID

	if err := app.Superusers().Delete(ctx, actorID); err != nil {
		t.Fatalf("delete the operator: %v", err)
	}

	row = onlyRow(t, auditRows(t, app, "action = $1", AuditOrderCancel))
	if row.ActorID == nil || *row.ActorID != actorID {
		t.Errorf("actor_id = %v, want it still pointing at %d", row.ActorID, actorID)
	}
	if row.ActorEmail != "leaving@example.com" || row.ActorRole != RoleOwner {
		t.Errorf("the snapshot did not survive: %q/%q", row.ActorEmail, row.ActorRole)
	}
}

// A deleted product is exactly what somebody opens the log to ask about.
func TestALabelOutlivesTheRecord(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	token := signInAs(t, app, "owner@example.com", RoleOwner)
	product := simpleProduct(t, app, "AUD-GONE", 2000, 1)

	title := "Renamed before deletion"
	if _, err := app.Products().UpdateProduct(ctx, product.ID, ProductPatch{Title: &title}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := app.Products().DeleteProduct(ctx, product.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	rec := do(t, app, "GET", "/api/admin/products/"+strconv.FormatInt(product.ID, 10)+"/history",
		bearer(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("history = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data []AuditEntry `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Data) == 0 {
		t.Fatal("a deleted product has no history; it should have all of it")
	}
	var deleted *AuditEntry
	for i := range out.Data {
		if out.Data[i].Action == AuditProductDelete {
			deleted = &out.Data[i]
		}
	}
	if deleted == nil {
		t.Fatal("no product.delete row")
	}
	if deleted.EntityLabel != title {
		t.Errorf("entity_label = %q, want the name it had at the time (%q)", deleted.EntityLabel, title)
	}
}

// The columns a service already holds encoded — an address, a metadata blob —
// have to read as the JSON they are. encoding/json renders a []byte as base64,
// and the field an operator most wants to read would arrive as a wall of
// letters.
func TestAnEncodedColumnIsNotBase64InTheRecord(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	loc, err := app.Places().Create(ctx, LocationInput{Code: "yard", Name: "The yard"})
	if err != nil {
		t.Fatalf("create location: %v", err)
	}

	addr := &Address{Line1: "9 Dock Road", City: "Portsmouth", PostalCode: "PO1 1AA", Country: "GB"}
	if _, err := app.Places().Update(ctx, loc.ID, LocationPatch{Address: addr}); err != nil {
		t.Fatalf("update location: %v", err)
	}

	row := onlyRow(t, auditRows(t, app, "action = $1", AuditLocationUpdate))
	got, ok := row.Changes.After["address"].(map[string]any)
	if !ok {
		t.Fatalf("after.address = %#v, want a decoded object", row.Changes.After["address"])
	}
	if got["line1"] != "9 Dock Road" {
		t.Errorf("after.address = %v, want the address that was set", got)
	}
}

// The wrap that made room for the audit row also closed a two-statement window.
func TestDeletingALocationIsOneTransaction(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	product := simpleProduct(t, app, "AUD-LOC", 600, 4)
	loc, err := app.Places().Create(ctx, LocationInput{Code: "shed", Name: "The shed"})
	if err != nil {
		t.Fatalf("create location: %v", err)
	}
	if _, err := app.Stock().Adjust(ctx, product.DefaultVariant().ID, loc.ID, 5, ""); err != nil {
		t.Fatalf("stock the shed: %v", err)
	}

	if err := app.Places().Delete(ctx, loc.ID); err == nil {
		t.Fatal("deleting a location that holds stock should be refused")
	}

	var rows int
	if err := app.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM variant_stock WHERE location_id = $1`, loc.ID).Scan(&rows); err != nil {
		t.Fatalf("count variant_stock: %v", err)
	}
	if rows == 0 {
		t.Error("the refusal took the bookkeeping rows with it: the two statements are not atomic")
	}
	if _, err := app.Places().Get(ctx, loc.ID); err != nil {
		t.Errorf("the location is gone after a refused delete: %v", err)
	}
}

// The audit records that a password changed, never to what.
func TestSecretsNeverReachTheLog(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	const secret = "a-very-secret-passphrase"

	su, err := app.Superusers().Create(ctx, "new@example.com", secret, RoleStaff)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := app.Superusers().Update(ctx, su.ID, "", secret+"-again"); err != nil {
		t.Fatalf("update: %v", err)
	}

	for _, row := range auditRows(t, app, "") {
		blob, err := json.Marshal(row)
		if err != nil {
			t.Fatalf("marshal row: %v", err)
		}
		if strings.Contains(string(blob), secret) {
			t.Fatalf("a password reached the log: %s", blob)
		}
		if strings.Contains(string(blob), "$2a$") || strings.Contains(string(blob), "$argon2") {
			t.Fatalf("a password hash reached the log: %s", blob)
		}
	}
	row := onlyRow(t, auditRows(t, app, "action = $1", AuditSuperuserUpdate))
	if row.Changes.After["password_changed"] != true {
		t.Errorf("after = %v, want it to record that a password changed", row.Changes.After)
	}
}

// ------------------------------------------------------------------ the routes

// The cross-store feed is surveillance of the team; the history of one record
// is part of that record.
func TestTheFeedNeedsStoreOperate(t *testing.T) {
	app := newTestApp(t)
	staff := signInAs(t, app, "staff@example.com", RoleStaff)
	manager := signInAs(t, app, "manager@example.com", RoleManager)
	owner := signInAs(t, app, "owner@example.com", RoleOwner)

	for _, token := range []string{staff, manager} {
		rec := do(t, app, "GET", "/api/admin/audit", bearer(token))
		if rec.Code != http.StatusForbidden {
			t.Errorf("feed = %d, want 403", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), string(RightStoreOperate)) {
			t.Errorf("the refusal does not name store.operate: %s", rec.Body.String())
		}
	}
	if rec := do(t, app, "GET", "/api/admin/audit", bearer(owner)); rec.Code != http.StatusOK {
		t.Errorf("owner feed = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// A static token carries everything.
	if rec := do(t, app, "GET", "/api/admin/audit", withAdmin); rec.Code != http.StatusOK {
		t.Errorf("token feed = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// The whole reason the per-record read is not behind store.operate.
func TestARecordsHistoryFollowsItsOwnRight(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	manager := signInAs(t, app, "manager@example.com", RoleManager)
	staff := signInAs(t, app, "staff@example.com", RoleStaff)
	product := simpleProduct(t, app, "AUD-RIGHTS", 1000, 1)

	if rec := do(t, app, "GET",
		"/api/admin/products/"+strconv.FormatInt(product.ID, 10)+"/history",
		bearer(manager)); rec.Code != http.StatusOK {
		t.Errorf("a manager cannot read a product's history: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, app, "GET", "/api/admin/audit", bearer(manager)); rec.Code != http.StatusForbidden {
		t.Errorf("the manager reached the feed: %d", rec.Code)
	}

	// Staff can read a location's history to begin with, because staff carries
	// locations.read; narrowed on the same session token, with no sign-out, the
	// per-record read follows the record's own right and goes with it.
	//
	// This used to prove the same thing with a variant's history, which was
	// admin_audit's until M26 moved a variant's history into the stock ledger.
	// TestMovementRoutesNeedInventoryRead is where that case lives now.
	locationHistory := "/api/admin/locations/" +
		strconv.FormatInt(defaultLocation(t, app).ID, 10) + "/history"
	if rec := do(t, app, "GET", locationHistory, bearer(staff)); rec.Code != http.StatusOK {
		t.Fatalf("staff cannot read a location's history: %d %s", rec.Code, rec.Body.String())
	}

	narrowed := []Right{}
	for _, r := range DefaultRightsOf(RoleStaff) {
		if r != RightLocationsRead {
			narrowed = append(narrowed, r)
		}
	}
	if _, err := app.Roles().Set(ctx, RoleStaff, narrowed, nil); err != nil {
		t.Fatalf("narrow staff: %v", err)
	}
	rec := do(t, app, "GET", locationHistory, bearer(staff))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("narrowed staff = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), string(RightLocationsRead)) {
		t.Errorf("the refusal does not name locations.read: %s", rec.Body.String())
	}
}

// A 404 would make the History card render as broken on every record that
// predates the migration.
func TestHistoryOfAMissingRecordIsEmptyNotMissing(t *testing.T) {
	app := newTestApp(t)
	token := signInAs(t, app, "owner@example.com", RoleOwner)

	rec := do(t, app, "GET", "/api/admin/products/999999/history", bearer(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("history of a missing record = %d, want 200", rec.Code)
	}
	var out struct {
		Data []AuditEntry `json:"data"`
		Meta ListMeta     `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Data) != 0 || out.Meta.Total != 0 {
		t.Errorf("want an empty page, got %d rows and total %d", len(out.Data), out.Meta.Total)
	}
	if !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Errorf("an empty history must be [] and not null: %s", rec.Body.String())
	}
}

// Pagination metadata that disagrees with its own list is the bug this class of
// screen always has.
func TestFilteringTheTrail(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	token := signInAs(t, app, "owner@example.com", RoleOwner)

	// One operator's act, one token's act, and a catalog act for a third shape.
	orderA := paidOrder(t, app, "AUD-F1", 1000, 2)
	orderB := paidOrder(t, app, "AUD-F2", 1000, 2)
	if rec := doBody(t, app, "POST", "/api/admin/orders/"+strconv.FormatInt(orderA.ID, 10)+"/cancel",
		`{"reason":"a"}`, bearer(token)); rec.Code != http.StatusOK {
		t.Fatalf("cancel A: %s", rec.Body.String())
	}
	if rec := doBody(t, app, "POST", "/api/admin/orders/"+strconv.FormatInt(orderB.ID, 10)+"/cancel",
		`{"reason":"b"}`, withAdmin); rec.Code != http.StatusOK {
		t.Fatalf("cancel B: %s", rec.Body.String())
	}
	loc, err := app.Places().Create(ctx, LocationInput{Code: "annex", Name: "The annex"})
	if err != nil {
		t.Fatalf("create location: %v", err)
	}

	byEntity, total, err := app.Audit().List(ctx, AuditQuery{
		EntityType: AuditEntityOrder, EntityID: strconv.FormatInt(orderA.ID, 10)})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != len(byEntity) {
		t.Fatalf("meta.total %d disagrees with the list it came with (%d)", total, len(byEntity))
	}
	for _, row := range byEntity {
		if row.EntityID != strconv.FormatInt(orderA.ID, 10) {
			t.Errorf("the entity filter let order %s through", row.EntityID)
		}
	}
	if byEntity[0].Action != AuditOrderCancel {
		t.Errorf("newest row is %q, want the cancellation", byEntity[0].Action)
	}

	byKind, total, err := app.Audit().List(ctx,
		AuditQuery{ActorKind: ActorToken, Action: AuditOrderCancel})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(byKind) != 1 || total != 1 {
		t.Errorf("actor_kind=token returned %d rows (total %d), want 1", len(byKind), total)
	}

	actorID := *byEntity[0].ActorID
	byActor, total, err := app.Audit().List(ctx, AuditQuery{ActorID: &actorID})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != len(byActor) {
		t.Errorf("meta.total %d disagrees with the list it came with (%d)", total, len(byActor))
	}
	for _, row := range byActor {
		if row.ActorID == nil || *row.ActorID != actorID {
			t.Errorf("actor filter let %v through", row.ActorID)
		}
	}

	byAction, _, err := app.Audit().List(ctx, AuditQuery{Action: AuditLocationCreate})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(byAction) != 1 || byAction[0].EntityID != strconv.FormatInt(loc.ID, 10) {
		t.Errorf("action filter returned %v", byAction)
	}

	// Newest first, and an unknown filter is an empty page rather than an error.
	all, _, err := app.Audit().List(ctx, AuditQuery{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].ID < all[i].ID {
			t.Fatalf("the feed is not newest first: %d before %d", all[i-1].ID, all[i].ID)
		}
	}
	rec := do(t, app, "GET", "/api/admin/audit?action=nonsense.verb", bearer(token))
	if rec.Code != http.StatusOK {
		t.Errorf("an unknown action filter = %d, want an empty 200", rec.Code)
	}

	// A window that ends before anything happened holds nothing.
	past := time.Now().Add(-48 * time.Hour)
	windowed, total, err := app.Audit().List(ctx, AuditQuery{To: &past})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(windowed) != 0 || total != 0 {
		t.Errorf("a window before the store existed returned %d rows (total %d)", len(windowed), total)
	}
}

// Built from the trail rather than from the team table.
func TestTheActorListDoesNotLeakTheRoster(t *testing.T) {
	app := newTestApp(t)
	owner := signInAs(t, app, "owner@example.com", RoleOwner)
	signInAs(t, app, "quiet@example.com", RoleManager)
	order := paidOrder(t, app, "AUD-ACTORS", 1100, 2)

	if rec := doBody(t, app, "POST", "/api/admin/orders/"+strconv.FormatInt(order.ID, 10)+"/cancel",
		`{"reason":"x"}`, bearer(owner)); rec.Code != http.StatusOK {
		t.Fatalf("cancel: %s", rec.Body.String())
	}

	rec := do(t, app, "GET", "/api/admin/audit/actors", bearer(owner))
	if rec.Code != http.StatusOK {
		t.Fatalf("actors = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "quiet@example.com") {
		t.Error("somebody who has done nothing appears in the trail's actor list")
	}
	if !strings.Contains(rec.Body.String(), "owner@example.com") {
		t.Errorf("the actor who acted is missing: %s", rec.Body.String())
	}
}

// Append-only is a property of there being no way to write the table, not of a
// handler refusing.
func TestTheTrailIsAppendOnlyOverHTTP(t *testing.T) {
	app := newTestApp(t)
	token := signInAs(t, app, "owner@example.com", RoleOwner)

	for _, route := range app.Routes() {
		if strings.HasPrefix(route.Path, "/api/admin/audit") && route.Method != http.MethodGet {
			t.Errorf("%s %s: the audit table has no write route", route.Method, route.Path)
		}
	}
	for _, method := range []string{"POST", "PATCH", "DELETE"} {
		rec := doBody(t, app, method, "/api/admin/audit", `{}`, bearer(token))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/admin/audit = %d, want 405", method, rec.Code)
		}
		if rec.Header().Get("Allow") == "" {
			t.Errorf("%s: a 405 with no Allow header", method)
		}
		if !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s: the 405 is not in the JSON envelope: %s", method, rec.Body.String())
		}
	}
}

// ------------------------------------------------------------- the vocabulary

// The database will not police the vocabulary, so a test has to: a constant
// nothing emits is a filter option that matches nothing, and a misspelling at a
// call site is a row no route can ever reach.
func TestTheVocabularyIsReachable(t *testing.T) {
	app := newTestApp(t, refundableModule{})
	ctx := context.Background()
	token := signInAs(t, app, "owner@example.com", RoleOwner)

	// Everything this change wires, exercised once each.
	product := simpleProduct(t, app, "AUD-VOCAB", 3000, 5)
	variant := product.DefaultVariant()
	title := "Vocabulary"
	if _, err := app.Products().UpdateProduct(ctx, product.ID, ProductPatch{Title: &title}); err != nil {
		t.Fatalf("update product: %v", err)
	}
	if _, err := app.Products().AddOption(ctx, product.ID, OptionInput{Name: "Size", Values: []string{"S", "M"}}); err != nil {
		t.Fatalf("add option: %v", err)
	}
	second, err := app.Products().CreateVariant(ctx, product.ID, VariantInput{
		SKU: "AUD-VOCAB-M", PriceMinor: 3000, Options: []string{"M"}})
	if err != nil {
		t.Fatalf("create variant: %v", err)
	}
	price := int64(2800)
	if _, err := app.Products().UpdateVariant(ctx, second.ID, VariantPatch{PriceMinor: &price}); err != nil {
		t.Fatalf("update variant: %v", err)
	}
	if err := app.Products().DeleteVariant(ctx, second.ID); err != nil {
		t.Fatalf("delete variant: %v", err)
	}

	cat, err := app.Categories().Create(ctx, CategoryInput{Title: "Shirts"})
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	newTitle := "Shirts and blouses"
	if _, err := app.Categories().Update(ctx, cat.ID, CategoryPatch{Title: &newTitle}); err != nil {
		t.Fatalf("update category: %v", err)
	}
	if err := app.Categories().Delete(ctx, cat.ID); err != nil {
		t.Fatalf("delete category: %v", err)
	}

	col, err := app.Collections().Create(ctx, CollectionInput{Title: "Summer"})
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	if err := app.Collections().SetProductCollections(ctx, product.ID, []int64{col.ID}); err != nil {
		t.Fatalf("set collections: %v", err)
	}
	// The other axis: what is in the collection, and in what order.
	if err := app.Collections().SetCollectionProducts(ctx, col.ID, []int64{product.ID}); err != nil {
		t.Fatalf("curate the collection: %v", err)
	}
	colTitle := "Summer sale"
	if _, err := app.Collections().Update(ctx, col.ID, CollectionPatch{Title: &colTitle}); err != nil {
		t.Fatalf("update collection: %v", err)
	}
	if err := app.Collections().SetProductCollections(ctx, product.ID, nil); err != nil {
		t.Fatalf("clear collections: %v", err)
	}
	if err := app.Collections().Delete(ctx, col.ID); err != nil {
		t.Fatalf("delete collection: %v", err)
	}

	attr, err := app.Categories().CreateAttribute(ctx, TaxonomyAttributeInput{
		Handle: "sleeve-length", Label: "Sleeve length", Choices: []string{"Short", "Long"}})
	if err != nil {
		t.Fatalf("create attribute: %v", err)
	}
	attrLabel := "Sleeve"
	if _, err := app.Categories().UpdateAttribute(ctx, attr.Handle,
		TaxonomyAttributePatch{Label: &attrLabel}); err != nil {
		t.Fatalf("update attribute: %v", err)
	}
	if err := app.Categories().DeleteAttribute(ctx, attr.Handle); err != nil {
		t.Fatalf("delete attribute: %v", err)
	}

	if err := app.MediaLibrary().SetProductMedia(ctx, product.ID, nil); err != nil {
		t.Fatalf("set product media: %v", err)
	}
	if err := app.MediaLibrary().SetVariantMedia(ctx, variant.ID, nil); err != nil {
		t.Fatalf("set variant media: %v", err)
	}

	disc, err := app.Discounts().Create(ctx, DiscountInput{
		Title: "Ten off", Code: "TEN", Kind: DiscountPercentage, ValueBP: 1000})
	if err != nil {
		t.Fatalf("create discount: %v", err)
	}
	bp := 1500
	if _, err := app.Discounts().Update(ctx, disc.ID, DiscountPatch{ValueBP: &bp}); err != nil {
		t.Fatalf("update discount: %v", err)
	}
	if err := app.Discounts().Delete(ctx, disc.ID); err != nil {
		t.Fatalf("delete discount: %v", err)
	}

	rate, err := app.Taxes().Create(ctx, TaxRateInput{Name: "VAT", RateBP: 2000})
	if err != nil {
		t.Fatalf("create tax rate: %v", err)
	}
	newRate := 2200
	if _, err := app.Taxes().Update(ctx, rate.ID, TaxRatePatch{RateBP: &newRate}); err != nil {
		t.Fatalf("update tax rate: %v", err)
	}
	if err := app.Taxes().Delete(ctx, rate.ID); err != nil {
		t.Fatalf("delete tax rate: %v", err)
	}

	loc, err := app.Places().Create(ctx, LocationInput{Code: "depot", Name: "Depot"})
	if err != nil {
		t.Fatalf("create location: %v", err)
	}
	locName := "Main depot"
	if _, err := app.Places().Update(ctx, loc.ID, LocationPatch{Name: &locName}); err != nil {
		t.Fatalf("update location: %v", err)
	}
	if _, err := app.Stock().Adjust(ctx, variant.ID, loc.ID, 4, ""); err != nil {
		t.Fatalf("adjust: %v", err)
	}
	if _, err := app.Stock().SetOnHand(ctx, variant.ID, loc.ID, 9, ""); err != nil {
		t.Fatalf("set on hand: %v", err)
	}
	// Moved before the default changes, or the two ids would resolve to the
	// same shelf.
	if _, err := app.Stock().Move(ctx, variant.ID, loc.ID, 0, 2, ""); err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, err := app.Places().SetDefault(ctx, loc.ID); err != nil {
		t.Fatalf("set default: %v", err)
	}
	spare, err := app.Places().Create(ctx, LocationInput{Code: "spare", Name: "Spare shelf"})
	if err != nil {
		t.Fatalf("create the spare location: %v", err)
	}
	if err := app.Places().Delete(ctx, spare.ID); err != nil {
		t.Fatalf("delete location: %v", err)
	}

	doomed := simpleProduct(t, app, "AUD-VOCAB-DOOMED", 100, 1)
	if err := app.Products().DeleteProduct(ctx, doomed.ID); err != nil {
		t.Fatalf("delete product: %v", err)
	}

	su, err := app.Superusers().Create(ctx, "vocab@example.com", "a-long-enough-password", RoleStaff)
	if err != nil {
		t.Fatalf("create superuser: %v", err)
	}
	if _, err := app.Superusers().Update(ctx, su.ID, "vocab2@example.com", ""); err != nil {
		t.Fatalf("update superuser: %v", err)
	}
	if _, err := app.Superusers().SetRole(ctx, su.ID, RoleManager); err != nil {
		t.Fatalf("set role: %v", err)
	}
	if _, err := app.Superusers().RevokeAll(ctx, su.ID); err != nil {
		t.Fatalf("revoke sessions: %v", err)
	}
	if _, err := app.Superusers().UpdateSelf(ctx, su.ID, "a-long-enough-password",
		"vocab3@example.com", "", ""); err != nil {
		t.Fatalf("update self: %v", err)
	}
	if err := app.Superusers().Delete(ctx, su.ID); err != nil {
		t.Fatalf("delete superuser: %v", err)
	}

	// One order taken all the way round: paid, edited, shipped, delivered and
	// back again, then refunded and cancelled.
	orderProduct := simpleProduct(t, app, "AUD-VOCAB-ORDER", 1400, 6)
	orderVariant := orderProduct.DefaultVariant()
	orderCart := newCart(t, app)
	addToCart(t, app, orderCart.Token, orderVariant.ID, 1)
	checkout, err := app.Order().Checkout(ctx, "refundable", checkoutInput(orderCart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	order := checkout.Order
	id := strconv.FormatInt(order.ID, 10)

	if _, err := app.Pay().MarkFailed(ctx, order.ID, "the card was declined"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	for _, step := range []struct{ method, path, body string }{
		{"POST", "/api/admin/orders/" + id + "/mark-paid", `{"reference":"R1"}`},
		{"POST", "/api/admin/orders/" + id + "/mark-unpaid", `{}`},
		{"POST", "/api/admin/orders/" + id + "/mark-paid", `{"reference":"R2"}`},
		{"PATCH", "/api/admin/orders/" + id, `{"name":"A Different Name"}`},
		// The shop's own note, which is the one order write that publishes no
		// event at all — so this row is the only record that it happened.
		{"PATCH", "/api/admin/orders/" + id, `{"metadata":{"notes":"rang the customer"}}`},
		// Handing the guest's own credential back to them. It changes nothing
		// about the order, which is exactly why the row is the only trace.
		{"POST", "/api/admin/orders/" + id + "/access-token", ``},
	} {
		if rec := doBody(t, app, step.method, step.path, step.body, bearer(token)); rec.Code != http.StatusOK {
			t.Fatalf("%s %s = %d: %s", step.method, step.path, rec.Code, rec.Body.String())
		}
	}
	lines := fmt.Sprintf(`{"lines":[{"variant_id":%d,"quantity":2}]}`, orderVariant.ID)
	if rec := doBody(t, app, "PUT", "/api/admin/orders/"+id+"/lines", lines, bearer(token)); rec.Code != http.StatusOK {
		t.Fatalf("edit lines = %d: %s", rec.Code, rec.Body.String())
	}

	if _, err := app.Ship().Create(ctx, order.ID, "", ShipRequest{Tracking: "TRACK-VOCAB"}); err != nil {
		t.Fatalf("ship: %v", err)
	}
	if _, err := app.Order().MarkDelivered(ctx, order.ID); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	// Goods back and that record taken back, while the order is delivered —
	// which is also the only state Return accepts. Withdrawing it again is what
	// leaves the order cancellable at the end of this walk.
	// Re-read: the edit above replaced the order's lines, so the id the
	// checkout returned no longer names anything.
	edited, err := app.Order().Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("re-read the order: %v", err)
	}
	_, returned, err := app.Order().Return(ctx, order.ID, ReturnInput{
		Reason: "did not fit",
		Lines:  []ReturnLineInput{{LineID: edited.Lines[0].ID, Quantity: 1, Restock: true}},
	})
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if _, err := app.Order().WithdrawReturn(ctx, order.ID, returned.ID); err != nil {
		t.Fatalf("withdraw the return: %v", err)
	}
	if _, err := app.Order().MarkUndelivered(ctx, order.ID); err != nil {
		t.Fatalf("undeliver: %v", err)
	}
	var shipmentID int64
	if err := app.DB().QueryRowContext(ctx,
		`SELECT id FROM fulfillments WHERE order_id = $1`, order.ID).Scan(&shipmentID); err != nil {
		t.Fatalf("find the shipment: %v", err)
	}
	if _, err := app.Ship().Delete(ctx, shipmentID); err != nil {
		t.Fatalf("delete the shipment: %v", err)
	}
	if _, err := app.Pay().Refund(ctx, order.ID, RefundRequest{AmountMinor: 700}, nil); err != nil {
		t.Fatalf("refund: %v", err)
	}
	// And a refund the gateway never answered about, settled by hand — the only
	// path that moves an order_refunds row without a provider saying anything,
	// and the only thing that emits order.refund_settle.
	stranded, _, err := app.Pay().reserveRefund(ctx, order.ID, 100, "stranded", nil)
	if err != nil {
		t.Fatalf("reserve a refund: %v", err)
	}
	// Backdated past refundStaleAfter, which is what the settle route refuses
	// before: a refund younger than that may still be in flight.
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE order_refunds SET created_at = now() - interval '1 hour' WHERE id = $1`,
		stranded); err != nil {
		t.Fatalf("age the pending refund: %v", err)
	}
	if _, err := app.Pay().SettleRefund(ctx, order.ID, stranded,
		RefundSettlement{Outcome: RefundFailed, Note: "the gateway has no such refund"}, nil); err != nil {
		t.Fatalf("settle the stranded refund: %v", err)
	}
	if _, err := app.Order().Cancel(ctx, order.ID, "done"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// A plugin switched on with a setting is the newest word in the vocabulary.
	switchedOn := true
	if _, err := app.Plugins().Update(ctx, "hello-bar", PluginPatch{Enabled: &switchedOn, Settings: map[string]any{"message": "Hi"}}); err != nil {
		t.Fatalf("plugin update: %v", err)
	}
	// And a message reworded.
	if _, err := app.NotifyTemplates().Set(ctx, ChannelEmail, EventOrderCreated, "Order {{.order_number}}", "Thanks, {{.customer_name}}."); err != nil {
		t.Fatalf("template update: %v", err)
	}

	// pending names the actions whose call sites live in files this change does
	// not own: the guarded admin-placed-order write in checkout.go, bulk import
	// in transfer.go, the options service, invitations and role sets. Each one
	// errors here the moment it starts being emitted, which is what stops this
	// list going stale the way the store.operate one did.
	pending := map[string]string{
		AuditOrderCreate:         "checkout.go — the guarded admin-placed-order write",
		AuditOrderImport:         "transfer.go — importOrderGroup",
		AuditProductImport:       "transfer.go — importProductGroup",
		AuditProductOptionsSet:   "options.go — SetOptions",
		AuditOrderShipmentUpdate: "fulfillment.go — Update, which needs order_id on its RETURNING",
		AuditInvitationCreate:    "invitations.go — Invite",
		AuditInvitationRevoke:    "invitations.go — Revoke",
		AuditInvitationAccept:    "invitations.go — Accept",
		AuditRoleSet:             "roles.go — Set",
		AuditRoleReset:           "roles.go — Reset",
	}

	seen := map[string]bool{}
	for _, row := range auditRows(t, app, "") {
		seen[row.Action] = true
		if !containsString(AuditEntityTypes, row.EntityType) {
			t.Errorf("row %d has entity_type %q, which is not in AuditEntityTypes", row.ID, row.EntityType)
		}
		if !containsString(AllAuditActions, row.Action) {
			t.Errorf("row %d has action %q, which is not in AllAuditActions", row.ID, row.Action)
		}
	}
	for _, action := range AllAuditActions {
		if reason, deferred := pending[action]; deferred {
			if seen[action] {
				t.Errorf("%q is emitted now: drop it from the pending list (%s)", action, reason)
			}
			continue
		}
		if !seen[action] {
			t.Errorf("%q is declared and nothing emits it", action)
		}
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// A caller's mistake has to surface the first time the path runs, rather than
// as an unreadable row somebody finds in a year.
func TestARecordWithNoActionIsRefused(t *testing.T) {
	app := newTestApp(t)
	err := InTx(context.Background(), app.DB(), func(tx *sql.Tx) error {
		return writeAudit(context.Background(), tx, auditRecord{Entity: AuditEntityOrder, ID: 1})
	})
	if err == nil {
		t.Fatal("an audit record with no action should be refused")
	}
	if !strings.Contains(err.Error(), "no action or entity") {
		t.Errorf("error = %v, want it to name the missing field", err)
	}
}
