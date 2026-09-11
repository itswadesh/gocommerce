package gocommerce

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// What one place holds, and what may never arrive at a closed one.
//
// The gap both halves close is the same shape: a refusal names units nobody can
// see, and a screen offers a shelf the engine will not accept.

// listStock is the location stock listing as a client sees it, envelope and all.
func listStock(t *testing.T, app *App, target string) (rows []*Variant, meta ListMeta, raw string) {
	t.Helper()
	rec := do(t, app, "GET", target, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", target, rec.Code, rec.Body.String())
	}
	var env struct {
		Data []*Variant `json:"data"`
		Meta ListMeta   `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode %s: %v (body %s)", target, err, rec.Body)
	}
	return env.Data, env.Meta, rec.Body.String()
}

// closeByHand deactivates a location without going through Update, which
// refuses while it holds anything. It is how a store that already has stranded
// units is reproduced — the state the recovery path exists for.
func closeByHand(t *testing.T, app *App, id int64) {
	t.Helper()
	if _, err := app.DB().ExecContext(context.Background(),
		`UPDATE locations SET active = false WHERE id = $1`, id); err != nil {
		t.Fatalf("close location %d: %v", id, err)
	}
}

func TestALocationListsWhatItHolds(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	p := simpleProduct(t, app, "HOLD-1", 1000, 4)
	vid := p.DefaultVariant().ID
	shop := newLocation(t, app, "shop", "The shop", 1)
	if _, err := app.Stock().Adjust(ctx, vid, shop.ID, 6, ""); err != nil {
		t.Fatalf("stock the shop: %v", err)
	}

	rows, meta, body := listStock(t, app, "/api/admin/locations/"+id64(shop.ID)+"/stock")
	if len(rows) != 1 || meta.Total != 1 {
		t.Fatalf("shop listing has %d row(s) (total %d), want 1: %s", len(rows), meta.Total, body)
	}
	at := rows[0].AtLocation
	if at == nil {
		t.Fatalf("row carries no at_location: %s", body)
	}
	if at.OnHand != 6 || at.Available != 6 || at.LocationID != shop.ID {
		t.Errorf("at_location = %+v, want 6 on hand at the shop", at)
	}
	// The two numbers coexist and neither is the other: the store has ten.
	if rows[0].StockOnHand != 10 || rows[0].Available != 10 {
		t.Errorf("store-wide figures on the same row = (%d, %d), want (10, 10)",
			rows[0].StockOnHand, rows[0].Available)
	}
	if rows[0].SKU != "HOLD-1" {
		t.Errorf("row names %q, want the SKU — a listing that cannot name what to move is the gap", rows[0].SKU)
	}

	// And the other location lists its own four, not the shop's six.
	def := defaultLocation(t, app)
	rows, _, body = listStock(t, app, "/api/admin/locations/"+id64(def.ID)+"/stock")
	if len(rows) != 1 || rows[0].AtLocation.OnHand != 4 {
		t.Fatalf("default listing = %s, want one row holding 4", body)
	}
}

func TestALocationHoldingOnlyReservedUnitsStillListsThem(t *testing.T) {
	app := newTestApp(t, &gatewayModule{})
	ctx := context.Background()

	// The bug trap the whole feature turns on. A shop holding nothing but
	// reserved units used to report skus 0 and show an empty listing, under a
	// refusal naming a count nobody could see.
	p := simpleProduct(t, app, "RES-ONLY", 1000, 0)
	vid := p.DefaultVariant().ID
	shop := newLocation(t, app, "shop", "The shop", -1)
	if _, err := app.Stock().Adjust(ctx, vid, shop.ID, 2, ""); err != nil {
		t.Fatalf("stock the shop: %v", err)
	}
	hold(t, app, vid, 2)
	if _, err := app.Stock().Adjust(ctx, vid, shop.ID, -2, ""); err == nil {
		t.Fatal("wrote off units that are reserved")
	}
	// Commit the sale so on_hand drops to zero while the reservation stays.
	onHand, reserved := stockAt(t, app, vid, shop.ID)
	if onHand != 2 || reserved != 2 {
		t.Fatalf("shop = (%d, %d), want (2, 2)", onHand, reserved)
	}
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE variant_stock SET on_hand = 0 WHERE variant_id = $1 AND location_id = $2`,
		vid, shop.ID); err != nil {
		t.Fatalf("simulate a picked-but-unreleased shelf: %v", err)
	}

	rows, _, body := listStock(t, app, "/api/admin/locations/"+id64(shop.ID)+"/stock")
	if len(rows) != 1 {
		t.Fatalf("listing = %s, want the reserved-only row", body)
	}
	l, err := app.Places().Get(ctx, shop.ID)
	if err != nil {
		t.Fatalf("get location: %v", err)
	}
	if l.SKUs != 1 {
		t.Errorf("location reports skus %d, want 1 — the same test the refusal uses", l.SKUs)
	}
	err = app.Places().Delete(ctx, shop.ID)
	if err == nil {
		t.Fatal("deleted a location holding reserved units")
	}
	if !strings.Contains(err.Error(), "1 SKU(s)") {
		t.Errorf("refusal = %q, want it to name 1 SKU — the listing and the refusal have to agree", err)
	}
}

func TestTheStockListingAndTheRefusalAgreeAcrossPages(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	// Two pages deliberately: a single-page fixture passes even when the drawer
	// footer sums the rows it has loaded, which is the bug this pins. The page is
	// narrowed rather than the fixture widened — twelve SKUs over a page of ten
	// prove the same thing as thirty over twenty-five, at half a minute less.
	shop := newLocation(t, app, "shop", "The shop", 1)
	const n = 12
	const perPage = 10
	for i := 0; i < n; i++ {
		p := simpleProduct(t, app, fmt.Sprintf("PAGE-%02d", i), 1000, 0)
		if _, err := app.Stock().Adjust(ctx, p.DefaultVariant().ID, shop.ID, i+1, ""); err != nil {
			t.Fatalf("stock %d: %v", i, err)
		}
	}

	rows, meta, _ := listStock(t, app,
		"/api/admin/locations/"+id64(shop.ID)+"/stock?limit="+strconv.Itoa(perPage))
	if meta.Total != n {
		t.Fatalf("meta.total = %d, want %d", meta.Total, n)
	}
	if len(rows) != perPage {
		t.Fatalf("first page has %d rows, want %d", len(rows), perPage)
	}
	if rows[0].AtLocation.OnHand != n {
		t.Errorf("first row holds %d, want %d — biggest holdings first",
			rows[0].AtLocation.OnHand, n)
	}

	l, err := app.Places().Get(ctx, shop.ID)
	if err != nil {
		t.Fatalf("get location: %v", err)
	}
	want := n * (n + 1) / 2
	if l.OnHand+l.Reserved != want || l.SKUs != n {
		t.Fatalf("location record = %d unit(s) across %d SKU(s), want %d across %d",
			l.OnHand+l.Reserved, l.SKUs, want, n)
	}
	err = app.Places().Delete(ctx, shop.ID)
	if err == nil {
		t.Fatal("deleted a full location")
	}
	// The refusal's arithmetic and the record's have to be one number, because
	// the drawer footer renders the record under the refusal's sentence.
	if !strings.Contains(err.Error(), strconv.Itoa(want)) ||
		!strings.Contains(err.Error(), strconv.Itoa(n)+" SKU(s)") {
		t.Errorf("refusal = %q, want %d unit(s) across %d SKU(s)", err, want, n)
	}
}

func TestAZeroShelfIsHiddenUntilAskedFor(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	p := simpleProduct(t, app, "ZERO-1", 1000, 0)
	vid := p.DefaultVariant().ID
	shop := newLocation(t, app, "shop", "The shop", 1)
	// A row at 0/0 exists the moment anything is done there, and survives.
	if _, err := app.Stock().Adjust(ctx, vid, shop.ID, 3, ""); err != nil {
		t.Fatalf("stock: %v", err)
	}
	if _, err := app.Stock().Adjust(ctx, vid, shop.ID, -3, ""); err != nil {
		t.Fatalf("empty: %v", err)
	}

	base := "/api/admin/locations/" + id64(shop.ID) + "/stock"
	if rows, _, body := listStock(t, app, base); len(rows) != 0 {
		t.Errorf("default listing = %s, want the zero shelf hidden", body)
	}
	if rows, _, body := listStock(t, app, base+"?nonzero=0"); len(rows) != 1 {
		t.Errorf("nonzero=0 listing = %s, want the zero shelf", body)
	}
	if rows, _, body := listStock(t, app, base+"?nonzero=false"); len(rows) != 1 {
		t.Errorf("nonzero=false listing = %s, want the zero shelf — both spellings", body)
	}
	if rows, _, body := listStock(t, app, base+"?nonzero=1"); len(rows) != 0 {
		t.Errorf("nonzero=1 listing = %s, want the default", body)
	}
}

func TestAnEmptyStockPageIsAnEmptyArray(t *testing.T) {
	app := newTestApp(t)

	shop := newLocation(t, app, "shop", "The shop", 1)
	_, _, body := listStock(t, app, "/api/admin/locations/"+id64(shop.ID)+"/stock")
	if !strings.Contains(body, `"data":[]`) {
		t.Errorf("empty location page = %s, want \"data\":[]", body)
	}

	simpleProduct(t, app, "EMPTY-1", 1000, 50)
	_, _, body = listStock(t, app, "/api/admin/inventory/low-stock?threshold=0")
	if !strings.Contains(body, `"data":[]`) {
		t.Errorf("empty store-wide low-stock page = %s, want \"data\":[] rather than null", body)
	}
	_, _, body = listStock(t, app,
		"/api/admin/inventory/low-stock?threshold=0&location_id="+id64(shop.ID))
	if !strings.Contains(body, `"data":[]`) {
		t.Errorf("empty per-location low-stock page = %s, want \"data\":[]", body)
	}
}

func TestLowStockAtOneLocationCountsThatShelfOnly(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	// One unit in each of two shops. The store has two, so nothing is low by
	// the store-wide reading — and every shelf is.
	p := simpleProduct(t, app, "SPLIT-1", 1000, 1)
	vid := p.DefaultVariant().ID
	shop := newLocation(t, app, "shop", "The shop", 1)
	if _, err := app.Stock().Adjust(ctx, vid, shop.ID, 1, ""); err != nil {
		t.Fatalf("stock the shop: %v", err)
	}

	if rows, _, body := listStock(t, app, "/api/admin/inventory/low-stock?threshold=1"); len(rows) != 0 {
		t.Errorf("store-wide low stock = %s, want nothing — the store holds two", body)
	}
	rows, _, body := listStock(t, app,
		"/api/admin/inventory/low-stock?threshold=1&location_id="+id64(shop.ID))
	if len(rows) != 1 {
		t.Fatalf("the shop's low stock = %s, want the variant", body)
	}
	if rows[0].AtLocation == nil || rows[0].AtLocation.Available != 1 {
		t.Errorf("at_location = %+v, want 1 available here", rows[0].AtLocation)
	}
}

func TestLowStockAtALocationCarriesBothNumbers(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	p := simpleProduct(t, app, "BOTH-1", 1000, 40)
	vid := p.DefaultVariant().ID
	shop := newLocation(t, app, "shop", "The shop", 1)
	if _, err := app.Stock().Adjust(ctx, vid, shop.ID, 1, ""); err != nil {
		t.Fatalf("stock the shop: %v", err)
	}

	rec := do(t, app, "GET",
		"/api/admin/inventory/low-stock?threshold=2&location_id="+id64(shop.ID), withAdmin)
	body := rec.Body.String()
	if !strings.Contains(body, `"at_location"`) {
		t.Fatalf("no at_location block: %s", body)
	}
	// The key names differ between the two shapes, and the panel reconciles them
	// per row rather than swapping objects. Both spellings have to be present,
	// or a row selected per location renders store-wide numbers.
	for _, key := range []string{`"stock_on_hand":41`, `"available":41`, `"on_hand":1`} {
		if !strings.Contains(body, key) {
			t.Errorf("response is missing %s: %s", key, body)
		}
	}
}

func TestAVariantNeverStockedHereIsNotThisShopsProblem(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	// A variant that existed before the shop opened has no row there. A left
	// join would list the whole catalog against a new shop and bury the rows
	// that matter.
	p := simpleProduct(t, app, "OLD-1", 1000, 0)
	shop := newLocation(t, app, "shop", "The shop", 1)

	rows, _, body := listStock(t, app, "/api/admin/locations/"+id64(shop.ID)+"/stock?nonzero=0")
	if len(rows) != 0 {
		t.Errorf("new shop lists %s, want nothing — it has never carried anything", body)
	}
	rows, _, _ = listStock(t, app,
		"/api/admin/inventory/low-stock?threshold=5&location_id="+id64(shop.ID))
	if len(rows) != 0 {
		t.Errorf("new shop is short of %d variant(s), want 0", len(rows))
	}
	// And it is still the default location's problem.
	rows, _, body = listStock(t, app,
		"/api/admin/inventory/low-stock?threshold=5&location_id="+id64(defaultLocation(t, app).ID))
	if len(rows) != 1 || rows[0].ID != p.DefaultVariant().ID {
		t.Errorf("the default's low stock = %s, want the variant", body)
	}
	_ = ctx
}

func TestLowStockAtAnUnknownLocationIs404(t *testing.T) {
	app := newTestApp(t)

	// An empty page for a typo is how an operator concludes a shop is fully
	// stocked.
	if rec := do(t, app, "GET", "/api/admin/inventory/low-stock?location_id=9999", withAdmin); rec.Code != http.StatusNotFound {
		t.Errorf("unknown location = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	for _, bad := range []string{"0", "-1", "abc"} {
		rec := do(t, app, "GET", "/api/admin/inventory/low-stock?location_id="+bad, withAdmin)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("location_id=%s = %d, want 400: %s", bad, rec.Code, rec.Body.String())
		}
	}
	if rec := do(t, app, "GET", "/api/admin/locations/9999/stock", withAdmin); rec.Code != http.StatusNotFound {
		t.Errorf("stock of an unknown location = %d, want 404", rec.Code)
	}
}

func TestStockCannotBeMovedIntoAClosedLocation(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	p := simpleProduct(t, app, "CLOSED-1", 1000, 5)
	vid := p.DefaultVariant().ID
	shop := newLocation(t, app, "shop", "The shop", 1)
	off := false
	if _, err := app.Places().Update(ctx, shop.ID, LocationPatch{Active: &off}); err != nil {
		t.Fatalf("close the empty shop: %v", err)
	}

	def := defaultLocation(t, app)
	_, err := app.Stock().Move(ctx, vid, def.ID, shop.ID, 2, "")
	if err == nil {
		t.Fatal("moved stock into a closed location")
	}
	if !strings.Contains(err.Error(), "The shop is closed") {
		t.Errorf("refusal = %q, want it to name the location", err)
	}
	if onHand, _ := variantStock(t, app, vid); onHand != 5 {
		t.Errorf("store total = %d after the refusal, want 5", onHand)
	}
	// The ensureStockRow inside the aborted transaction must not survive it.
	var rows int
	if err := app.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM variant_stock WHERE variant_id = $1 AND location_id = $2`,
		vid, shop.ID).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 0 {
		t.Errorf("the closed shop has %d stock row(s), want none", rows)
	}

	rec := doBody(t, app, "POST", "/api/admin/variants/"+id64(vid)+"/stock/transfer",
		`{"from_location_id":`+id64(def.ID)+`,"to_location_id":`+id64(shop.ID)+`,"quantity":1}`, withAdmin)
	if rec.Code != http.StatusConflict {
		t.Errorf("transfer over HTTP = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

func TestStockCanAlwaysLeaveAClosedLocation(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	// The recovery path, and the reason the check is not inside resolveLocation:
	// a store that already has stranded units has to be able to free them.
	p := simpleProduct(t, app, "STRAND-1", 1000, 0)
	vid := p.DefaultVariant().ID
	shop := newLocation(t, app, "shop", "The shop", 1)
	if _, err := app.Stock().Adjust(ctx, vid, shop.ID, 4, ""); err != nil {
		t.Fatalf("stock the shop: %v", err)
	}
	closeByHand(t, app, shop.ID)

	def := defaultLocation(t, app)
	if _, err := app.Stock().Move(ctx, vid, shop.ID, def.ID, 4, "recovering"); err != nil {
		t.Fatalf("move out of a closed location: %v", err)
	}
	if err := app.Places().Delete(ctx, shop.ID); err != nil {
		t.Errorf("close it properly once emptied: %v", err)
	}
}

func TestReceivingAtAClosedLocationIsRefusedAndWritingOffIsNot(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	p := simpleProduct(t, app, "ARRIVE-1", 1000, 0)
	vid := p.DefaultVariant().ID
	shop := newLocation(t, app, "shop", "The shop", 1)
	if _, err := app.Stock().Adjust(ctx, vid, shop.ID, 5, ""); err != nil {
		t.Fatalf("stock the shop: %v", err)
	}
	closeByHand(t, app, shop.ID)

	if _, err := app.Stock().Adjust(ctx, vid, shop.ID, 5, ""); err == nil {
		t.Error("received stock at a closed location")
	}
	if _, err := app.Stock().Adjust(ctx, vid, shop.ID, -2, ""); err != nil {
		t.Errorf("writing off at a closed location: %v — that is how one is emptied", err)
	}
	if _, err := app.Stock().SetOnHand(ctx, vid, shop.ID, 10, ""); err == nil {
		t.Error("counted up at a closed location")
	}
	if _, err := app.Stock().SetOnHand(ctx, vid, shop.ID, 3, ""); err != nil {
		t.Errorf("counting a closed location down: %v", err)
	}
	if _, err := app.Stock().SetOnHand(ctx, vid, shop.ID, 3, ""); err != nil {
		t.Errorf("counting a closed location to what is already there: %v", err)
	}
	if _, err := app.Stock().SetOnHand(ctx, vid, shop.ID, 0, ""); err != nil {
		t.Errorf("emptying a closed location: %v", err)
	}
}

func TestClosingALocationRacesNobody(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	// Whatever the interleaving, either the transfer is refused or the
	// deactivation is — never both, and the location never ends up closed and
	// holding. Delete matters more than Update here: its unconditional DELETE
	// FROM variant_stock would destroy the units rather than strand them.
	//
	// Four rounds rather than one, because the interesting interleaving is the
	// rare one. Raise it with -count when hunting a suspected race; the suite
	// itself is already close to the default test timeout.
	for i := 0; i < 4; i++ {
		p := simpleProduct(t, app, fmt.Sprintf("RACE-%02d", i), 1000, 4)
		vid := p.DefaultVariant().ID
		shop := newLocation(t, app, fmt.Sprintf("shop%02d", i), "Shop", 1)
		def := defaultLocation(t, app)

		var wg sync.WaitGroup
		var moveErr, closeErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, moveErr = app.Stock().Move(ctx, vid, def.ID, shop.ID, 4, "")
		}()
		go func() {
			defer wg.Done()
			off := false
			_, closeErr = app.Places().Update(ctx, shop.ID, LocationPatch{Active: &off})
		}()
		wg.Wait()

		if moveErr == nil && closeErr == nil {
			t.Fatalf("run %d: both the transfer and the close succeeded", i)
		}
		l, err := app.Places().Get(ctx, shop.ID)
		if err != nil {
			t.Fatalf("get location: %v", err)
		}
		if !l.Active && l.OnHand+l.Reserved != 0 {
			t.Fatalf("run %d: the shop is closed holding %d unit(s)", i, l.OnHand+l.Reserved)
		}
		if onHand, _ := variantStock(t, app, vid); onHand != 4 {
			t.Fatalf("run %d: the store holds %d, want 4 — no units may be lost", i, onHand)
		}
	}
}

func TestDeletingALocationRacesNobody(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		p := simpleProduct(t, app, fmt.Sprintf("DRACE-%02d", i), 1000, 4)
		vid := p.DefaultVariant().ID
		shop := newLocation(t, app, fmt.Sprintf("dshop%02d", i), "Shop", 1)
		def := defaultLocation(t, app)

		var wg sync.WaitGroup
		var moveErr, delErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, moveErr = app.Stock().Move(ctx, vid, def.ID, shop.ID, 4, "")
		}()
		go func() {
			defer wg.Done()
			delErr = app.Places().Delete(ctx, shop.ID)
		}()
		wg.Wait()

		if moveErr == nil && delErr == nil {
			t.Fatalf("run %d: the transfer landed at a location that was deleted", i)
		}
		if onHand, _ := variantStock(t, app, vid); onHand != 4 {
			t.Fatalf("run %d: the store holds %d, want 4 — the delete destroyed units", i, onHand)
		}
	}
}

func TestCancellingAnOrderRestocksToAClosedShelf(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	// restockStock is exempt from the arrival guard, and has to stay exempt:
	// those units have already left the shelf, the line records which shelf, and
	// refusing would lose them. Without this test the guard grows until it
	// swallows them and a cancellation starts failing.
	p := simpleProduct(t, app, "CANCEL-CLOSED", 1000, 3)
	vid := p.DefaultVariant().ID
	order := buy(t, app, vid, 2)
	if onHand, _ := variantStock(t, app, vid); onHand != 1 {
		t.Fatalf("on hand after the sale = %d, want 1", onHand)
	}
	closeByHand(t, app, defaultLocation(t, app).ID)

	if _, err := app.Order().Cancel(ctx, order.ID, "changed their mind"); err != nil {
		t.Fatalf("cancel into a closed shelf: %v", err)
	}
	if onHand, _ := variantStock(t, app, vid); onHand != 3 {
		t.Errorf("on hand after the cancellation = %d, want 3 — the units came back", onHand)
	}
}
