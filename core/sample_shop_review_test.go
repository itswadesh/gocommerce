package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// What a merchant does around the sample shop must not make it reach anybody,
// nor leave it stuck half erased (D96).

// Whatever is done to a sample order later — marked paid, cancelled — it
// announces nothing, so no email, SMS, webhook or invoice follows it. A real
// order still announces itself.
func TestASampleOrderAnnouncesNothingWhateverIsDoneToIt(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if _, err := app.AddSampleShop(ctx); err != nil {
		t.Fatal(err)
	}
	orderID := func(number string) int64 {
		t.Helper()
		var id int64
		if err := app.DB().QueryRowContext(ctx, `SELECT id FROM orders WHERE number = $1`, number).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	if _, err := app.Pay().MarkPaid(ctx, orderID("SAMPLE-1010"), ""); err != nil {
		t.Fatalf("mark a sample order paid: %v", err)
	}
	if _, err := app.Order().Cancel(ctx, orderID("SAMPLE-1012"), "changed their mind"); err != nil {
		t.Fatalf("cancel a sample order: %v", err)
	}
	var events int
	if err := app.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM outbox_events WHERE aggregate_type = $1`, AggregateOrder).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("order events = %d, want none for sample orders", events)
	}

	simpleProduct(t, app, "OWN-1", 1500, 3)
	if _, err := app.Data().ImportOrders(ctx, strings.NewReader(
		"number,email,sku,quantity,unit_price_minor\nOWN-100,real@example.org,OWN-1,1,1500\n"),
		ImportOptions{FireEvents: true}); err != nil {
		t.Fatal(err)
	}
	if got := len(outboxEvents(t, app, EventOrderCreated)); got != 1 {
		t.Fatalf("order.created events = %d, want the real order's one", got)
	}
}

// A sample photograph the merchant put on their own product stays theirs, and
// so does a picture they linked under the same address: neither blocks the
// erase, now or on the next run.
func TestASamplePhotoTheMerchantUsesStaysTheirs(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if _, err := app.AddSampleShop(ctx); err != nil {
		t.Fatal(err)
	}
	own := simpleProduct(t, app, "OWN-1", 1500, 3)
	var kurtaPhoto int64
	if err := app.DB().QueryRowContext(ctx,
		`SELECT id FROM media WHERE url LIKE '%/printed-anarkali-kurta-rose-front.webp'`).Scan(&kurtaPhoto); err != nil {
		t.Fatal(err)
	}
	if err := app.media.SetProductMedia(ctx, own.ID, []int64{kurtaPhoto}); err != nil {
		t.Fatal(err)
	}
	linked, err := app.media.AddURL(ctx, SampleMediaPrefix+"printed-anarkali-kurta-indigo-front.webp", MediaImage, "ours")
	if err != nil {
		t.Fatal(err)
	}

	for run := 1; run <= 2; run++ {
		if _, err := app.EraseSampleShop(ctx); err != nil {
			t.Fatalf("erase, run %d: %v", run, err)
		}
	}
	var kept []int64
	rows, err := app.DB().QueryContext(ctx, `SELECT id FROM media ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		kept = append(kept, id)
	}
	rows.Close()
	if len(kept) != 2 || kept[0] != kurtaPhoto || kept[1] != linked.ID {
		t.Fatalf("media left = %v, want the photo on their product (%d) and the one they linked (%d)", kept, kurtaPhoto, linked.ID)
	}
	counts, err := app.SampleShopCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts.total() != 0 {
		t.Fatalf("counts after the erase = %v, want nothing left", counts)
	}
}

// A code of the merchant's that matches a sample's in another case is theirs:
// the sample one is skipped, and the rest of the sample shop still arrives.
func TestAMerchantsCodeInAnotherCaseDoesNotStopTheSampleShop(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if _, err := app.Discounts().Create(ctx, DiscountInput{
		Code: "welcome10", Title: "Theirs", Kind: DiscountPercentage, ValueBP: 500,
	}); err != nil {
		t.Fatal(err)
	}
	added, err := app.AddSampleShop(ctx)
	if err != nil {
		t.Fatalf("add the sample shop: %v", err)
	}
	if added["discounts"] != 1 || added["orders"] != 12 {
		t.Fatalf("added = %v, want the other discount and all 12 orders", added)
	}
}

// The erase leaves an audit row for each sample order, as every other delete
// does, and a sample category the merchant filed their own product in becomes
// theirs rather than holding the banner up for ever.
func TestErasingTheSampleShopIsAuditedAndHandsOverWhatItKeeps(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if _, err := app.AddSampleShop(ctx); err != nil {
		t.Fatal(err)
	}
	own := simpleProduct(t, app, "OWN-1", 1500, 3)
	var dresses int64
	if err := app.DB().QueryRowContext(ctx, `SELECT id FROM categories WHERE slug = 'women-dresses'`).Scan(&dresses); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Products().UpdateProduct(ctx, own.ID, ProductPatch{CategoryID: SetID(dresses)}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.EraseSampleShop(ctx); err != nil {
		t.Fatal(err)
	}
	var audited int
	if err := app.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM admin_audit WHERE action = $1`, AuditOrderEraseSample).Scan(&audited); err != nil {
		t.Fatal(err)
	}
	if audited != 12 {
		t.Fatalf("audited sample order erases = %d, want 12", audited)
	}
	var kept int
	if err := app.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM categories WHERE slug IN ('women', 'women-dresses') AND NOT sample`).Scan(&kept); err != nil || kept != 2 {
		t.Fatalf("kept categories handed over = %d (%v), want the one in use and its parent, both the merchant's", kept, err)
	}
	counts, err := app.SampleShopCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts.total() != 0 {
		t.Fatalf("counts after the erase = %v, want nothing left to erase", counts)
	}
}

// The routes D94 shipped still answer, for a client written then: the count
// of sample products, and an erase that takes the whole sample shop, since a
// product cannot go without the orders and reviews that point at it.
func TestTheSampleProductsRoutesStillAnswer(t *testing.T) {
	app := newTestApp(t)
	if _, err := app.AddSampleShop(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count struct {
		Count int `json:"count"`
	}
	decodeData(t, do(t, app, http.MethodGet, "/api/admin/sample-products", withAdmin), &count)
	if count.Count != 11 {
		t.Fatalf("count = %d, want 11", count.Count)
	}
	rec := do(t, app, http.MethodDelete, "/api/admin/sample-products", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("erase = %d: %s", rec.Code, rec.Body)
	}
	var erased struct {
		Deleted int `json:"deleted"`
	}
	decodeData(t, rec, &erased)
	if erased.Deleted != 11 {
		t.Fatalf("deleted = %d, want 11", erased.Deleted)
	}
	counts, err := app.SampleShopCounts(context.Background())
	if err != nil || counts.total() != 0 {
		t.Fatalf("counts after the old erase = %v (%v), want the whole sample shop gone", counts, err)
	}
}
