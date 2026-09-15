package gocommerce

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// The stock ledger (M26).
//
// One invariant carries the whole feature: sum(on_hand_delta) per (variant,
// location) equals on_hand, and sum(reserved_delta) equals reserved. A ledger
// whose sum does not meet the shelf cannot settle the stock-take dispute it
// exists for, so most of what is below is a way of reaching one of the eleven
// writers and asking whether it recorded what it applied or what it was asked
// for.

// movementsOf reads a variant's history the way the route does.
func movementsOf(t *testing.T, app *App, variantID int64) []StockMovement {
	t.Helper()
	rows, _, err := app.Stock().Movements(context.Background(),
		MovementQuery{VariantID: variantID, Limit: MaxLimit})
	if err != nil {
		t.Fatalf("movements: %v", err)
	}
	return rows
}

// ofKind narrows to one kind, oldest first, which is how a reader follows a
// history forwards.
func ofKind(rows []StockMovement, kind string) []StockMovement {
	out := []StockMovement{}
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Kind == kind {
			out = append(out, rows[i])
		}
	}
	return out
}

func onlyMovement(t *testing.T, rows []StockMovement, kind string) StockMovement {
	t.Helper()
	got := ofKind(rows, kind)
	if len(got) != 1 {
		t.Fatalf("want exactly one %q movement, got %d: %s", kind, len(got), describe(rows))
	}
	return got[0]
}

func describe(rows []StockMovement) string {
	var b strings.Builder
	for _, m := range rows {
		fmt.Fprintf(&b, "\n  %d %-12s %-8s on_hand %+d -> %d  reserved %+d -> %d  %q",
			m.ID, m.Kind, m.Source, m.OnHandDelta, m.OnHandAfter,
			m.ReservedDelta, m.ReservedAfter, m.Reason)
	}
	return b.String()
}

// assertReconciles is checkLedger's query, run as an assertion: every balance
// in the store has to be the sum of the movements that explain it.
func assertReconciles(t *testing.T, app *App) {
	t.Helper()
	rows, err := app.DB().QueryContext(context.Background(), `
		SELECT vs.variant_id, vs.location_id, vs.on_hand, vs.reserved,
		       coalesce((SELECT sum(m.on_hand_delta) FROM stock_movements m
		                  WHERE m.variant_id = vs.variant_id AND m.location_id = vs.location_id), 0),
		       coalesce((SELECT sum(m.reserved_delta) FROM stock_movements m
		                  WHERE m.variant_id = vs.variant_id AND m.location_id = vs.location_id), 0)
		FROM variant_stock vs`)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var variantID, locationID int64
		var onHand, reserved, sumOnHand, sumReserved int
		if err := rows.Scan(&variantID, &locationID, &onHand, &reserved,
			&sumOnHand, &sumReserved); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if onHand != sumOnHand || reserved != sumReserved {
			t.Errorf("variant %d at location %d holds %d/%d but its ledger sums to %d/%d",
				variantID, locationID, onHand, reserved, sumOnHand, sumReserved)
		}
	}
}

// The invariant the whole feature rests on, driven through every path that
// moves stock. It is the one test that fails if any writer records the
// requested amount instead of the applied one.
func TestTheLedgerReconcilesToTheShelf(t *testing.T) {
	app := newTestApp(t, &gatewayModule{})
	ctx := context.Background()

	product := simpleProduct(t, app, "LEDGER-1", 1500, 20)
	vid := product.DefaultVariant().ID
	shop := newLocation(t, app, "shop", "The shop", 1)
	def := defaultLocation(t, app)

	if _, err := app.Stock().Adjust(ctx, vid, def.ID, 5, "delivery from Acme"); err != nil {
		t.Fatalf("adjust: %v", err)
	}
	if _, err := app.Stock().Move(ctx, vid, def.ID, shop.ID, 6, "rebalancing"); err != nil {
		t.Fatalf("move: %v", err)
	}
	buy(t, app, vid, 2)  // COD: reserve, then commit.
	hold(t, app, vid, 3) // gateway: reserve only.
	cancelled := buy(t, app, vid, 1)
	if _, err := app.Order().Cancel(ctx, cancelled.ID, "customer changed their mind"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	edited := buy(t, app, vid, 4)
	if _, _, err := app.Order().EditLines(ctx, edited.ID, OrderEdit{
		Lines: []OrderLineEdit{{ID: edited.Lines[0].ID, Quantity: 2}},
	}); err != nil {
		t.Fatalf("edit lines: %v", err)
	}
	if _, err := app.Stock().SetOnHand(ctx, vid, shop.ID, 4, "quarterly count"); err != nil {
		t.Fatalf("set on hand: %v", err)
	}

	assertReconciles(t, app)

	// And the newest row's after-balances are the shelf itself, which is what
	// makes a page of history that starts in the middle readable.
	rows := movementsOf(t, app, vid)
	if len(rows) == 0 {
		t.Fatal("nothing was recorded at all")
	}
	newest := rows[0]
	onHand, reserved := stockAt(t, app, vid, *newest.LocationID)
	if newest.OnHandAfter != onHand || newest.ReservedAfter != reserved {
		t.Errorf("newest row says %d/%d, the shelf says %d/%d",
			newest.OnHandAfter, newest.ReservedAfter, onHand, reserved)
	}
}

// The case a stored `qty` column would have got wrong in both directions.
func TestAnUntrackedVariantRecordsNoOrderMovement(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	stock := 5
	p, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "A download", Status: ProductActive,
		Variants: []VariantInput{{
			SKU: "LEDGER-UNTRACKED", PriceMinor: 900,
			StockOnHand: &stock, TrackInventory: ptr(false),
		}},
	})
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	vid := p.DefaultVariant().ID

	buy(t, app, vid, 2)

	rows := movementsOf(t, app, vid)
	if got := len(ofKind(rows, MovementReserve)); got != 0 {
		t.Errorf("%d reserve row(s) for a variant that reserves nothing: %s", got, describe(rows))
	}
	if got := len(ofKind(rows, MovementCommit)); got != 0 {
		t.Errorf("%d commit row(s) for a variant that commits nothing: %s", got, describe(rows))
	}

	// Adjust carries no track_inventory CASE, deliberately: an operator
	// counting a digital product's shelf means the number they typed.
	if _, err := app.Stock().Adjust(ctx, vid, 0, 5, ""); err != nil {
		t.Fatalf("adjust: %v", err)
	}
	adjust := onlyMovement(t, movementsOf(t, app, vid), MovementAdjust)
	if adjust.OnHandDelta != 5 {
		t.Errorf("adjust recorded %+d, want +5", adjust.OnHandDelta)
	}
	assertReconciles(t, app)
}

// releaseStock floors at zero, so what it released and what it was asked to
// release can differ — and recording the request would rot the sum silently.
func TestAReleaseRecordsWhatItCouldRelease(t *testing.T) {
	app := newTestApp(t, &gatewayModule{})
	ctx := context.Background()

	product := simpleProduct(t, app, "LEDGER-CLAMP", 700, 9)
	vid := product.DefaultVariant().ID
	order := hold(t, app, vid, 3)

	// The only way to reach the defensive clamp: inside the engine a pending
	// order's units cannot go missing, which is exactly why greatest(0, …) is
	// there — it is protection against a shelf somebody wrote by hand, which is
	// what this simulates. The reconciliation invariant is deliberately not
	// asserted afterwards; that drift is the doctor's business.
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE variant_stock SET reserved = 2 WHERE variant_id = $1`, vid); err != nil {
		t.Fatalf("write the shelf by hand: %v", err)
	}

	if _, err := app.Order().Cancel(ctx, order.ID, "gone quiet"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	release := onlyMovement(t, movementsOf(t, app, vid), MovementRelease)
	if release.ReservedDelta != -2 {
		t.Errorf("released %+d, want -2: the row must say what it could release, not what it was asked for",
			release.ReservedDelta)
	}
	if release.ReservedAfter != 0 {
		t.Errorf("reserved_after = %d, want 0", release.ReservedAfter)
	}
}

// A commit moves both counters in one statement, which is the thing a single
// signed delta column could not have represented at all.
func TestACommitRecordsBothCounters(t *testing.T) {
	app := newTestApp(t)

	product := simpleProduct(t, app, "LEDGER-COMMIT", 1200, 8)
	vid := product.DefaultVariant().ID
	buy(t, app, vid, 2)

	rows := movementsOf(t, app, vid)
	reserve := onlyMovement(t, rows, MovementReserve)
	if reserve.ReservedDelta != 2 || reserve.OnHandDelta != 0 {
		t.Errorf("reserve = %+d on hand / %+d reserved, want 0/+2",
			reserve.OnHandDelta, reserve.ReservedDelta)
	}
	commit := onlyMovement(t, rows, MovementCommit)
	if commit.ReservedDelta != -2 || commit.OnHandDelta != -2 {
		t.Errorf("commit = %+d on hand / %+d reserved, want -2/-2 in ONE row",
			commit.OnHandDelta, commit.ReservedDelta)
	}
	assertReconciles(t, app)
}

// The ledger writes are inside the transaction that moves the stock, so a
// checkout that refuses leaves nothing behind at all.
func TestARefusedCheckoutLeavesNoMovements(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	plenty := simpleProduct(t, app, "LEDGER-PLENTY", 500, 10)
	short := simpleProduct(t, app, "LEDGER-SHORT", 500, 3)
	cart := newCart(t, app)
	// The lower variant id reserves first, so the shelf has already moved by
	// the time the second line refuses — which is the case that proves the
	// rollback.
	addToCart(t, app, cart.Token, plenty.DefaultVariant().ID, 1)
	addToCart(t, app, cart.Token, short.DefaultVariant().ID, 2)

	// The cart refuses a line it cannot cover, so the shelf is taken away
	// afterwards: this is the shopper who sat on the checkout page while
	// somebody else bought the last one.
	if _, err := app.Stock().Adjust(ctx, short.DefaultVariant().ID, 0, -2, "sold elsewhere"); err != nil {
		t.Fatalf("clear the shelf: %v", err)
	}

	if _, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), ""); err == nil {
		t.Fatal("a cart whose second line is short should not check out")
	}

	rows := movementsOf(t, app, plenty.DefaultVariant().ID)
	if got := len(ofKind(rows, MovementReserve)); got != 0 {
		t.Errorf("the first line's reservation survived a rolled-back checkout: %s", describe(rows))
	}
	if onHand, reserved := variantStock(t, app, plenty.DefaultVariant().ID); onHand != 10 || reserved != 0 {
		t.Errorf("shelf = %d/%d, want 10/0", onHand, reserved)
	}
	assertReconciles(t, app)
}

// attachMovementsToOrder's whole reason for existing: the order id does not
// exist when the reservation is written.
func TestASaleNamesTheOrderThatCausedIt(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "LEDGER-NAMED", 2500, 6)
	vid := product.DefaultVariant().ID
	order := buy(t, app, vid, 2)

	for _, m := range movementsOf(t, app, vid) {
		if m.Kind != MovementReserve && m.Kind != MovementCommit {
			continue
		}
		if m.OrderID == nil || *m.OrderID != order.ID {
			t.Errorf("%s row names order %v, want %d", m.Kind, m.OrderID, order.ID)
		}
		if m.OrderNumber != order.Number {
			t.Errorf("%s row names %q, want %q", m.Kind, m.OrderNumber, order.Number)
		}
		if m.Source != sourceCheckout && m.Source != sourceOrder {
			t.Errorf("%s row has source %q", m.Kind, m.Source)
		}
	}

	// And the regression test for anyone who hoists the nextval above the
	// reservation loop: a conflicted checkout in between must burn no number.
	first := buy(t, app, vid, 1)
	shortCart := newCart(t, app)
	addToCart(t, app, shortCart.Token, vid, 2)
	if _, err := app.Stock().Adjust(ctx, vid, 0, -3, "sold elsewhere"); err != nil {
		t.Fatalf("clear the shelf: %v", err)
	}
	if _, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(shortCart.Token), ""); err == nil {
		t.Fatal("a short cart should not check out")
	}
	if _, err := app.Stock().Adjust(ctx, vid, 0, 5, "restocked"); err != nil {
		t.Fatalf("restock: %v", err)
	}
	second := buy(t, app, vid, 1)
	if numberOf(t, second.Number) != numberOf(t, first.Number)+1 {
		t.Errorf("order numbers %s then %s: a conflicted checkout burned one",
			first.Number, second.Number)
	}
}

func numberOf(t *testing.T, number string) int {
	t.Helper()
	digits := strings.TrimLeftFunc(number, func(r rune) bool { return r < '0' || r > '9' })
	n, err := strconv.Atoi(digits)
	if err != nil {
		t.Fatalf("order number %q has no number in it", number)
	}
	return n
}

// withStockSource is first-label-wins, which is what keeps the sweeper's own
// name from being overwritten by Orders.transition a moment later.
func TestTheSweeperIsNamedAsTheSource(t *testing.T) {
	app := newTestApp(t, &gatewayModule{})
	ctx := context.Background()

	product := simpleProduct(t, app, "LEDGER-SWEEP", 400, 5)
	vid := product.DefaultVariant().ID
	order := hold(t, app, vid, 2)

	if _, err := app.DB().ExecContext(ctx,
		`UPDATE orders SET reservation_expires_at = now() - interval '1 hour' WHERE id = $1`,
		order.ID); err != nil {
		t.Fatalf("expire the reservation: %v", err)
	}
	if _, err := app.Order().SweepUnpaid(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	release := onlyMovement(t, movementsOf(t, app, vid), MovementRelease)
	if release.Source != sourceSweeper {
		t.Errorf("source = %q, want %q: the sweeper has to be distinguishable from an operator",
			release.Source, sourceSweeper)
	}
	if release.SuperuserID != nil || release.ActorEmail != "" {
		t.Errorf("the sweeper was recorded as a person: %v / %q",
			release.SuperuserID, release.ActorEmail)
	}
	if release.Reason != "payment not completed in time" {
		t.Errorf("reason = %q, want the sweeper's own", release.Reason)
	}
	assertReconciles(t, app)
}

// A nil superuser is a fact rather than an error, and `source` names the path
// either way.
func TestTheOperatorIsRecordedFromTheSession(t *testing.T) {
	app := newTestApp(t)

	product := simpleProduct(t, app, "LEDGER-WHO", 900, 4)
	vid := product.DefaultVariant().ID
	path := "/api/admin/variants/" + strconv.FormatInt(vid, 10) + "/inventory"

	token := signInAs(t, app, "counter@example.com", RoleManager)
	if rec := doBody(t, app, "POST", path, `{"adjust":2}`, bearer(token)); rec.Code != http.StatusOK {
		t.Fatalf("adjust as an operator = %d: %s", rec.Code, rec.Body.String())
	}
	byPerson := ofKind(movementsOf(t, app, vid), MovementAdjust)[0]
	if byPerson.SuperuserID == nil || byPerson.ActorEmail != "counter@example.com" {
		t.Errorf("operator movement recorded as %v / %q", byPerson.SuperuserID, byPerson.ActorEmail)
	}
	if byPerson.Source != sourceAdmin {
		t.Errorf("source = %q, want %q", byPerson.Source, sourceAdmin)
	}

	if rec := doBody(t, app, "POST", path, `{"adjust":1}`, withAdmin); rec.Code != http.StatusOK {
		t.Fatalf("adjust with the static token = %d: %s", rec.Code, rec.Body.String())
	}
	byToken := movementsOf(t, app, vid)[0]
	if byToken.SuperuserID != nil || byToken.ActorEmail != "" {
		t.Errorf("a script was recorded as a person: %v / %q", byToken.SuperuserID, byToken.ActorEmail)
	}
	if byToken.Source != sourceAdmin {
		t.Errorf("source = %q, want %q — source names the path, not the actor",
			byToken.Source, sourceAdmin)
	}
}

func TestAReasonSurvivesToTheLedger(t *testing.T) {
	app := newTestApp(t)

	product := simpleProduct(t, app, "LEDGER-WHY", 1100, 7)
	vid := product.DefaultVariant().ID
	path := "/api/admin/variants/" + strconv.FormatInt(vid, 10)

	if rec := doBody(t, app, "POST", path+"/inventory",
		`{"adjust":-3,"reason":"damaged in transit"}`, withAdmin); rec.Code != http.StatusOK {
		t.Fatalf("adjust = %d: %s", rec.Code, rec.Body.String())
	}

	rec := do(t, app, "GET", path+"/movements", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("read the ledger = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data []StockMovement `json:"data"`
		Meta ListMeta        `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Data) == 0 || out.Data[0].Reason != "damaged in transit" {
		t.Errorf("the reason did not reach the ledger: %+v", out.Data)
	}
	if out.Data[0].OnHandDelta != -3 {
		t.Errorf("delta = %+d, want -3", out.Data[0].OnHandDelta)
	}

	long := strings.Repeat("x", 201)
	if rec := doBody(t, app, "POST", path+"/inventory",
		`{"adjust":1,"reason":"`+long+`"}`, withAdmin); rec.Code != http.StatusBadRequest {
		t.Errorf("an over-long reason = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if onHand, _ := variantStock(t, app, vid); onHand != 4 {
		t.Errorf("the refused request moved stock: on hand = %d, want 4", onHand)
	}
}

// The kind-keyed zero rule, and the CHECK that backs it.
func TestAStockTakeThatConfirmsTheCountIsRecorded(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "LEDGER-ZERO", 300, 6)
	vid := product.DefaultVariant().ID

	if _, err := app.Stock().SetOnHand(ctx, vid, 0, 6, "counted, unchanged"); err != nil {
		t.Fatalf("set on hand: %v", err)
	}
	take := onlyMovement(t, movementsOf(t, app, vid), MovementStockTake)
	if take.OnHandDelta != 0 || take.ReservedDelta != 0 {
		t.Errorf("a confirming count recorded %+d/%+d, want zeroes",
			take.OnHandDelta, take.ReservedDelta)
	}
	if take.OnHandAfter != 6 {
		t.Errorf("on_hand_after = %d, want 6", take.OnHandAfter)
	}

	// Every other kind of nothing stays out of the table.
	before := len(movementsOf(t, app, vid))
	if _, err := app.Stock().Adjust(ctx, vid, 0, 0, "nothing happened"); err != nil {
		t.Fatalf("adjust by zero: %v", err)
	}
	unchanged := "product_slug,sku,price_minor,stock_on_hand\n" +
		"test-ledger-zero,LEDGER-ZERO,300,6\n"
	if _, err := app.Data().ImportProducts(ctx, strings.NewReader(unchanged), ImportOptions{}); err != nil {
		t.Fatalf("import: %v", err)
	}
	if after := len(movementsOf(t, app, vid)); after != before {
		t.Errorf("%d row(s) that say nothing happened: %s", after-before, describe(movementsOf(t, app, vid)))
	}
	assertReconciles(t, app)
}

// A brand-new variant reconciles from the moment it exists.
func TestAVariantIsCreatedWithAnOpeningMovement(t *testing.T) {
	app := newTestApp(t)

	stocked := simpleProduct(t, app, "LEDGER-OPEN", 1000, 7)
	opening := onlyMovement(t, movementsOf(t, app, stocked.DefaultVariant().ID), MovementOpening)
	if opening.OnHandDelta != 7 || opening.OnHandAfter != 7 {
		t.Errorf("opening row = %+d -> %d, want +7 -> 7", opening.OnHandDelta, opening.OnHandAfter)
	}
	if opening.Source != sourceAdmin {
		t.Errorf("source = %q, want %q: creating a variant with stock is an admin act",
			opening.Source, sourceAdmin)
	}

	empty := simpleProduct(t, app, "LEDGER-EMPTY", 1000, 0)
	if rows := movementsOf(t, app, empty.DefaultVariant().ID); len(rows) != 0 {
		t.Errorf("a variant created empty wrote %d row(s): %s", len(rows), describe(rows))
	}
	assertReconciles(t, app)
}

// The CSV clamps against what is reserved, so what the file asked for and what
// the shelf got routinely differ.
func TestAnImportRecordsWhatItWroteNotWhatItAsked(t *testing.T) {
	app := newTestApp(t, &gatewayModule{})
	ctx := context.Background()

	product := simpleProduct(t, app, "LEDGER-CSV", 800, 10)
	vid := product.DefaultVariant().ID
	hold(t, app, vid, 4) // four promised to an open order

	csv := "product_slug,sku,price_minor,stock_on_hand\n" +
		"test-ledger-csv,LEDGER-CSV,800,1\n"
	if _, err := app.Data().ImportProducts(ctx, strings.NewReader(csv), ImportOptions{}); err != nil {
		t.Fatalf("import: %v", err)
	}

	imported := onlyMovement(t, movementsOf(t, app, vid), MovementImport)
	if imported.Source != sourceImport {
		t.Errorf("source = %q, want %q", imported.Source, sourceImport)
	}
	// The file said 1; the floor is the 4 already reserved, so the shelf went
	// from 10 to 4 and the row has to say -6.
	if imported.OnHandDelta != -6 || imported.OnHandAfter != 4 {
		t.Errorf("import recorded %+d -> %d, want -6 -> 4 (the clamped result, not the file's number)",
			imported.OnHandDelta, imported.OnHandAfter)
	}
	assertReconciles(t, app)

	// A re-run of the same file changes nothing and records nothing.
	before := len(movementsOf(t, app, vid))
	if _, err := app.Data().ImportProducts(ctx, strings.NewReader(csv), ImportOptions{}); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if after := len(movementsOf(t, app, vid)); after != before {
		t.Errorf("re-running an unchanged file wrote %d row(s)", after-before)
	}
}

func TestATransferNamesBothEnds(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "LEDGER-MOVE", 650, 9)
	vid := product.DefaultVariant().ID
	shop := newLocation(t, app, "shop", "The shop", 1)
	def := defaultLocation(t, app)

	if _, err := app.Stock().Move(ctx, vid, def.ID, shop.ID, 3, "store request"); err != nil {
		t.Fatalf("move: %v", err)
	}

	rows := movementsOf(t, app, vid)
	out := onlyMovement(t, rows, MovementTransferOut)
	in := onlyMovement(t, rows, MovementTransferIn)

	if out.OnHandDelta != -3 || in.OnHandDelta != 3 {
		t.Errorf("deltas %+d / %+d, want -3 / +3", out.OnHandDelta, in.OnHandDelta)
	}
	if out.CounterpartLocationID == nil || *out.CounterpartLocationID != shop.ID {
		t.Errorf("the outgoing row does not name where the units went: %v", out.CounterpartLocationID)
	}
	if out.CounterpartCode != shop.Code {
		t.Errorf("counterpart_code = %q, want %q", out.CounterpartCode, shop.Code)
	}
	if in.CounterpartLocationID == nil || *in.CounterpartLocationID != def.ID {
		t.Errorf("the incoming row does not name where the units came from: %v", in.CounterpartLocationID)
	}
	if out.Reason != "store request" || in.Reason != "store request" {
		t.Errorf("the reason reached only one end: %q / %q", out.Reason, in.Reason)
	}
	// Both rows are written by one transaction and share now() exactly, which
	// is why the ledger is ordered by id and never by timestamp.
	if !out.CreatedAt.Equal(in.CreatedAt) {
		t.Errorf("the two ends of one transfer have different timestamps: %v / %v",
			out.CreatedAt, in.CreatedAt)
	}
	assertReconciles(t, app)
}

// ON DELETE SET NULL plus the snapshot, against a future CASCADE.
func TestMovementsSurviveTheirLocation(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "LEDGER-CLOSE", 450, 8)
	vid := product.DefaultVariant().ID
	shed := newLocation(t, app, "shed", "The shed", 2)
	def := defaultLocation(t, app)

	if _, err := app.Stock().Move(ctx, vid, def.ID, shed.ID, 5, "overflow"); err != nil {
		t.Fatalf("move out: %v", err)
	}
	if _, err := app.Stock().Move(ctx, vid, shed.ID, def.ID, 5, "bringing it back"); err != nil {
		t.Fatalf("move back: %v", err)
	}
	if err := app.Places().Delete(ctx, shed.ID); err != nil {
		t.Fatalf("delete the location: %v", err)
	}

	var orphaned int
	for _, m := range movementsOf(t, app, vid) {
		if m.LocationCode != "shed" {
			continue
		}
		orphaned++
		if m.LocationID != nil {
			t.Errorf("row %d still points at a deleted location", m.ID)
		}
	}
	if orphaned != 2 {
		t.Errorf("%d row(s) survived the shed, want 2", orphaned)
	}

	rec := do(t, app, "GET", "/api/admin/locations/"+strconv.FormatInt(shed.ID, 10)+"/movements", withAdmin)
	if rec.Code != http.StatusNotFound {
		t.Errorf("a closed location's route = %d, want 404", rec.Code)
	}
	assertReconciles(t, app)
}

func TestMovementsSurviveTheirVariant(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "LEDGER-GONE", 999, 40)
	vid := product.DefaultVariant().ID
	if _, err := app.Stock().Adjust(ctx, vid, 0, -40, "written off"); err != nil {
		t.Fatalf("adjust: %v", err)
	}
	if err := app.Products().DeleteProduct(ctx, product.ID); err != nil {
		t.Fatalf("delete the product: %v", err)
	}

	rows, _, err := app.Stock().Movements(ctx, MovementQuery{
		LocationID: defaultLocation(t, app).ID, Limit: MaxLimit,
	})
	if err != nil {
		t.Fatalf("movements: %v", err)
	}
	var found int
	for _, m := range rows {
		if m.SKU != "LEDGER-GONE" {
			continue
		}
		found++
		if m.VariantID != nil {
			t.Errorf("row %d still points at a deleted variant", m.ID)
		}
	}
	if found != 2 {
		t.Errorf("%d row(s) survived the SKU, want 2 — history that dissolves is not history",
			found)
	}
}

func TestMovementsAreNewestFirstAndPaged(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "LEDGER-PAGE", 250, 1)
	vid := product.DefaultVariant().ID
	for i := 0; i < 12; i++ {
		if _, err := app.Stock().Adjust(ctx, vid, 0, 1, ""); err != nil {
			t.Fatalf("adjust %d: %v", i, err)
		}
	}

	path := "/api/admin/variants/" + strconv.FormatInt(vid, 10) + "/movements"
	var page1, page2 struct {
		Data []StockMovement `json:"data"`
		Meta ListMeta        `json:"meta"`
	}
	decodeInto(t, do(t, app, "GET", path+"?limit=5&page=1", withAdmin), &page1)
	decodeInto(t, do(t, app, "GET", path+"?limit=5&page=2", withAdmin), &page2)

	if page1.Meta.Total != 13 {
		t.Errorf("total = %d, want 13 (twelve adjustments and the opening row)", page1.Meta.Total)
	}
	if len(page2.Data) != 5 {
		t.Fatalf("page 2 holds %d rows, want 5", len(page2.Data))
	}
	last := page1.Data[0].ID + 1
	for _, m := range append(page1.Data, page2.Data...) {
		if m.ID >= last {
			t.Fatalf("ids are not strictly descending across pages: %d after %d", m.ID, last)
		}
		last = m.ID
	}

	var filtered struct {
		Data []StockMovement `json:"data"`
		Meta ListMeta        `json:"meta"`
	}
	decodeInto(t, do(t, app, "GET", path+"?kind=adjust&limit=200", withAdmin), &filtered)
	if filtered.Meta.Total != 12 {
		t.Errorf("?kind=adjust total = %d, want 12", filtered.Meta.Total)
	}
	if rec := do(t, app, "GET", path+"?kind=nonsense", withAdmin); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown kind = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func decodeInto(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("request = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestMovementRoutesNeedInventoryRead(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "LEDGER-RIGHTS", 350, 3)
	vid := strconv.FormatInt(product.DefaultVariant().ID, 10)
	loc := strconv.FormatInt(defaultLocation(t, app).ID, 10)
	paths := []string{
		"/api/admin/variants/" + vid + "/movements",
		"/api/admin/locations/" + loc + "/movements",
	}

	for _, p := range paths {
		if rec := do(t, app, "GET", p); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with no credential = %d, want 401", p, rec.Code)
		}
	}

	// Staff carry inventory.read by default: reading stock history is reading
	// stock.
	staff := signInAs(t, app, "shelf@example.com", RoleStaff)
	for _, p := range paths {
		if rec := do(t, app, "GET", p, bearer(staff)); rec.Code != http.StatusOK {
			t.Errorf("%s as staff = %d, want 200: %s", p, rec.Code, rec.Body.String())
		}
	}

	narrowed := []Right{}
	for _, r := range DefaultRightsOf(RoleStaff) {
		if r != RightInventoryRead {
			narrowed = append(narrowed, r)
		}
	}
	if _, err := app.Roles().Set(ctx, RoleStaff, narrowed, nil); err != nil {
		t.Fatalf("re-cut staff: %v", err)
	}
	for _, p := range paths {
		rec := do(t, app, "GET", p, bearer(staff))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s without inventory.read = %d, want 403", p, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), string(RightInventoryRead)) {
			t.Errorf("the refusal does not name the right: %s", rec.Body)
		}
	}
}

// D38: a table, not an event. This is the pin against a future contributor
// adding a stock.* name by reflex.
func TestNoStockEventIsEmitted(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "LEDGER-QUIET", 750, 5)
	vid := product.DefaultVariant().ID
	if _, err := app.Stock().Adjust(ctx, vid, 0, 3, "delivery"); err != nil {
		t.Fatalf("adjust: %v", err)
	}
	buy(t, app, vid, 1)

	rows, err := app.DB().QueryContext(ctx,
		`SELECT aggregate_type, event_name FROM outbox_events ORDER BY id`)
	if err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var aggregate, name string
		if err := rows.Scan(&aggregate, &name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if aggregate != AggregateOrder {
			t.Errorf("event %q is on aggregate %q; the ledger is a table, not an event stream",
				name, aggregate)
		}
		if strings.HasPrefix(name, "stock.") {
			t.Errorf("a stock event was emitted: %q", name)
		}
	}
}

// The guard M17's comment protects, restated where it now belongs: the reads
// added for the ledger must never become the check that decides.
func TestARefusedStockTakeRecordsNothing(t *testing.T) {
	app := newTestApp(t, &gatewayModule{})
	ctx := context.Background()

	product := simpleProduct(t, app, "LEDGER-GUARD", 1000, 5)
	vid := product.DefaultVariant().ID
	hold(t, app, vid, 3)
	before := len(movementsOf(t, app, vid))

	if _, err := app.Stock().SetOnHand(ctx, vid, 0, 1, "counted one"); err == nil {
		t.Fatal("counting below what is reserved should be refused")
	}
	if after := len(movementsOf(t, app, vid)); after != before {
		t.Errorf("a refused stock take wrote %d row(s)", after-before)
	}
	assertReconciles(t, app)
}

// The migration's opening-balance seed, run against balances that already
// exist.
//
// gctest gives every test an empty schema, so the migration's own run of this
// statement is always a no-op and the one thing it exists for — a store that
// has been trading for a year when the ledger arrives — would otherwise ship
// untested. The statement is taken verbatim out of the migration constant
// rather than retyped, so this cannot drift from what a real upgrade runs.
func TestTheSeedExplainsBalancesThatPredateIt(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	stocked := simpleProduct(t, app, "SEED-HELD", 500, 6)
	vid := stocked.DefaultVariant().ID
	shop := newLocation(t, app, "shop", "The shop", 1)
	// A pair that nets to zero: bookkeeping, not stock, and the seed must skip
	// it — its sum is 0 either way, so the invariant holds without a row.
	if _, err := app.Stock().Adjust(ctx, vid, shop.ID, 3, "in"); err != nil {
		t.Fatalf("adjust in: %v", err)
	}
	if _, err := app.Stock().Adjust(ctx, vid, shop.ID, -3, "out"); err != nil {
		t.Fatalf("adjust out: %v", err)
	}

	// Now make it a store that predates the ledger.
	if _, err := app.DB().ExecContext(ctx, `DELETE FROM stock_movements`); err != nil {
		t.Fatalf("clear the ledger: %v", err)
	}
	seed := migration0026StockMovements[strings.Index(
		migration0026StockMovements, "INSERT INTO stock_movements"):]
	if _, err := app.DB().ExecContext(ctx, seed); err != nil {
		t.Fatalf("run the seed: %v", err)
	}

	rows := movementsOf(t, app, vid)
	if len(rows) != 1 {
		t.Fatalf("the seed wrote %d row(s), want 1 — the 0/0 shelf is not stock: %s",
			len(rows), describe(rows))
	}
	opening := rows[0]
	if opening.Kind != MovementOpening || opening.Source != "migration" {
		t.Errorf("seed row is %s/%s, want opening/migration", opening.Kind, opening.Source)
	}
	if opening.OnHandDelta != 6 || opening.OnHandAfter != 6 {
		t.Errorf("seed row = %+d -> %d, want +6 -> 6", opening.OnHandDelta, opening.OnHandAfter)
	}
	if opening.Reason == "" {
		t.Error("the seed row does not say what its date means")
	}
	// The point of the whole exercise: an upgraded store reconciles from its
	// first day, rather than beginning with a balance nothing explains.
	assertReconciles(t, app)
}
