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

// What a promotion cost, and what one would do — the two questions the discount
// screen could not answer. `used_count` is a claim, `redeemed_total` is money,
// and a preview must never become the consuming path.

func previewDiscount(t *testing.T, app *App, id int64, body string) (*httptest2, DiscountPreview) {
	t.Helper()
	rec := doBody(t, app, "POST", "/api/admin/discounts/"+strconv.FormatInt(id, 10)+"/preview",
		body, withAdmin)
	var p DiscountPreview
	if rec.Code == http.StatusOK {
		decodeData(t, rec, &p)
	}
	return &httptest2{rec.Code, rec.Body.String()}, p
}

// httptest2 is the two facts these assertions need: what status, and what body.
type httptest2 struct {
	code int
	body string
}

func TestDiscountRedemptionsListTheOrdersThatUsedIt(t *testing.T) {
	app := newTestApp(t)
	product := simpleProduct(t, app, "RED-1", 1000, 20)
	vid := product.DefaultVariant().ID
	d := newDiscount(t, app, DiscountInput{
		Code: "SPRING20", Title: "Spring", Kind: DiscountFixed, ValueMinor: 200,
	})
	newDiscount(t, app, DiscountInput{
		Code: "OTHER", Title: "Other", Kind: DiscountFixed, ValueMinor: 100,
	})

	if _, err := checkoutWithCode(t, app, "SPRING20", vid, 1, "a@example.com"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := checkoutWithCode(t, app, "SPRING20", vid, 2, "b@example.com"); err != nil {
		t.Fatalf("second: %v", err)
	}
	if _, err := checkoutWithCode(t, app, "OTHER", vid, 1, "c@example.com"); err != nil {
		t.Fatalf("other code: %v", err)
	}

	rec := do(t, app, "GET", "/api/admin/discounts/"+strconv.FormatInt(d.ID, 10)+"/orders", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("drilldown = %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data []DiscountRedemption `json:"data"`
		Meta ListMeta             `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data) != 2 || env.Meta.Total != 2 {
		t.Fatalf("drilldown = %d row(s), total %d, want 2 and 2: %s",
			len(env.Data), env.Meta.Total, rec.Body.String())
	}
	// Newest first, and each row names the order it is about.
	if env.Data[0].Email != "b@example.com" {
		t.Errorf("first row is %s, want the newest order", env.Data[0].Email)
	}
	if env.Data[0].Number == "" || env.Data[0].Status == "" {
		t.Errorf("row = %+v, want the order's number and status", env.Data[0])
	}
	if env.Data[0].Amount.AmountMinor != 200 || env.Data[0].Amount.Currency == "" {
		t.Errorf("amount = %+v, want 200 with a currency", env.Data[0].Amount)
	}
	if env.Data[0].Title != "Spring" || env.Data[0].Code != "SPRING20" {
		t.Errorf("row = %+v, want the snapshot's own code and title", env.Data[0])
	}
}

func TestRedeemedTotalExcludesCancelledOrdersAndUsedCountDoesNot(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	product := simpleProduct(t, app, "RED-2", 1000, 30)
	vid := product.DefaultVariant().ID
	d := newDiscount(t, app, DiscountInput{
		Code: "THREE", Title: "Three uses", Kind: DiscountFixed, ValueMinor: 150,
	})

	var cancelled *Order
	for i, email := range []string{"a@x.com", "b@x.com", "c@x.com"} {
		o, err := checkoutWithCode(t, app, "THREE", vid, 1, email)
		if err != nil {
			t.Fatalf("checkout %d: %v", i, err)
		}
		if i == 0 {
			cancelled = o
		}
	}
	if _, err := app.Order().Cancel(ctx, cancelled.ID, "test"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	rec := do(t, app, "GET", "/api/admin/discounts/"+strconv.FormatInt(d.ID, 10), withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail = %d: %s", rec.Code, rec.Body.String())
	}
	var detail struct {
		UsedCount      int   `json:"used_count"`
		RedeemedOrders int   `json:"redeemed_orders"`
		RedeemedTotal  Money `json:"redeemed_total"`
		Other          int   `json:"redeemed_other_currency_orders"`
		Title          string
	}
	decodeData(t, rec, &detail)
	// Three numbers, three questions, and they are allowed to disagree.
	if detail.UsedCount != 3 {
		t.Errorf("used_count = %d, want 3 — the claim is never given back", detail.UsedCount)
	}
	if detail.RedeemedOrders != 2 {
		t.Errorf("redeemed_orders = %d, want 2 — the cancellation does not count", detail.RedeemedOrders)
	}
	if detail.RedeemedTotal.AmountMinor != 300 {
		t.Errorf("redeemed_total = %+v, want 300", detail.RedeemedTotal)
	}
	if detail.RedeemedTotal.Currency == "" {
		t.Error("redeemed_total carries no currency, which is the money bug rule 6 exists to prevent")
	}
	if detail.Other != 0 {
		t.Errorf("redeemed_other_currency_orders = %d, want 0", detail.Other)
	}

	// The drilldown counts every row, cancelled ones included, and shows them.
	rec = do(t, app, "GET", "/api/admin/discounts/"+strconv.FormatInt(d.ID, 10)+"/orders", withAdmin)
	var env struct {
		Data []DiscountRedemption `json:"data"`
		Meta ListMeta             `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Meta.Total != 3 || len(env.Data) != 3 {
		t.Fatalf("drilldown total = %d (%d rows), want 3", env.Meta.Total, len(env.Data))
	}
	found := false
	for _, r := range env.Data {
		if r.Status == OrderCancelled {
			found = true
		}
	}
	if !found {
		t.Error("the cancelled order is missing from the list, so nothing explains the difference")
	}
}

func TestRedeemedTotalSaysWhichCurrencyItSummed(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	product := simpleProduct(t, app, "RED-3", 1000, 10)
	vid := product.DefaultVariant().ID
	d := newDiscount(t, app, DiscountInput{
		Code: "CURR", Title: "Currency", Kind: DiscountFixed, ValueMinor: 500,
	})

	if _, err := checkoutWithCode(t, app, "CURR", vid, 1, "home@x.com"); err != nil {
		t.Fatalf("home currency: %v", err)
	}
	away, err := checkoutWithCode(t, app, "CURR", vid, 1, "away@x.com")
	if err != nil {
		t.Fatalf("second order: %v", err)
	}
	// A store has one settlement currency, so the only way to have an order in
	// another is to have changed it — which is exactly what D14's snapshot is
	// for. Written directly, because no API can produce this state today.
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE orders SET currency = 'JPY' WHERE id = $1`, away.ID); err != nil {
		t.Fatalf("restate the order's currency: %v", err)
	}

	total, orders, other, err := app.Discounts().Redeemed(ctx, d.ID)
	if err != nil {
		t.Fatalf("redeemed: %v", err)
	}
	if total.AmountMinor != 500 || orders != 1 {
		t.Errorf("redeemed = %+v across %d order(s), want 500 across 1 — an unlabelled sum "+
			"across two currencies is the bug this prevents", total, orders)
	}
	if other != 1 {
		t.Errorf("redeemed_other_currency_orders = %d, want 1 — what was left out is counted", other)
	}

	rows, _, err := app.Discounts().Redemptions(ctx, d.ID, 50, 0)
	if err != nil {
		t.Fatalf("redemptions: %v", err)
	}
	var sawJPY bool
	for _, r := range rows {
		if r.Amount.Currency == "JPY" {
			sawJPY = true
		}
	}
	if !sawJPY {
		t.Error("the drilldown lost the order's own currency, which is what makes each row true")
	}
}

func TestRedemptionsOfADeletedDiscountAre404(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	product := simpleProduct(t, app, "RED-4", 1000, 10)
	d := newDiscount(t, app, DiscountInput{
		Code: "GONE", Title: "Gone", Kind: DiscountFixed, ValueMinor: 100,
	})
	order, err := checkoutWithCode(t, app, "GONE", product.DefaultVariant().ID, 1, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if err := app.Discounts().Delete(ctx, d.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// discount_id is ON DELETE SET NULL, so the drilldown is live-rule-only by
	// construction — while the order keeps its snapshot and still reads right.
	rec := do(t, app, "GET", "/api/admin/discounts/"+strconv.FormatInt(d.ID, 10)+"/orders", withAdmin)
	if rec.Code != http.StatusNotFound {
		t.Errorf("drilldown of a deleted rule = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	after, err := app.Order().Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if len(after.Discounts) != 1 || after.Discounts[0].Title != "Gone" {
		t.Errorf("order snapshot = %+v, want it untouched", after.Discounts)
	}
}

func TestTheDrilldownNeedsOrdersReadAsWellAsDiscountsRead(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	d := newDiscount(t, app, DiscountInput{
		Code: "RIGHTS", Title: "Rights", Kind: DiscountFixed, ValueMinor: 100,
	})

	// Re-cut staff to keep discounts.read and lose orders.read. The first core
	// route to name two rights gets a test rather than an assumption.
	rights := []Right{RightCatalogRead, RightDiscountsRead}
	if _, err := app.Roles().Set(ctx, RoleStaff, rights, nil); err != nil {
		t.Fatalf("recut staff: %v", err)
	}
	token := signInAs(t, app, "staff-rights@example.com", RoleStaff)

	id := strconv.FormatInt(d.ID, 10)
	if rec := do(t, app, "GET", "/api/admin/discounts/"+id, bearer(token)); rec.Code != http.StatusOK {
		t.Fatalf("the rule itself = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	rec := do(t, app, "GET", "/api/admin/discounts/"+id+"/orders", bearer(token))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("drilldown without orders.read = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), string(RightOrdersRead)) {
		t.Errorf("403 = %s, want it to name orders.read", rec.Body.String())
	}
}

func TestPreviewingADiscountConsumesNothing(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	product := simpleProduct(t, app, "PREV-1", 1000, 10)
	limit := 1
	d := newDiscount(t, app, DiscountInput{
		Code: "ONLYONE", Title: "Only one", Kind: DiscountFixed, ValueMinor: 100,
		UsageLimit: &limit,
	})

	for i := 0; i < 5; i++ {
		rec, p := previewDiscount(t, app, d.ID, `{"subtotal_minor":100000}`)
		if rec.code != http.StatusOK || !p.Applies {
			t.Fatalf("preview %d = %d %s", i, rec.code, rec.body)
		}
	}
	after, err := app.Discounts().Get(ctx, d.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.UsedCount != 0 {
		t.Fatalf("used_count = %d after five previews, want 0 — a preview must never consume",
			after.UsedCount)
	}
	if _, err := checkoutWithCode(t, app, "ONLYONE", product.DefaultVariant().ID, 1, ""); err != nil {
		t.Errorf("the code was still spendable: %v", err)
	}
}

func TestPreviewSaysAnAutomaticDiscountWouldNotFire(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	product := simpleProduct(t, app, "PREV-AUTO", 1000, 10)
	d := newDiscount(t, app, DiscountInput{
		Title: "Automatic", Kind: DiscountFixed, ValueMinor: 100,
	})
	if d.Code != "" {
		t.Fatalf("the fixture has a code %q, which is not the case under test", d.Code)
	}

	rec, p := previewDiscount(t, app, d.ID, `{"subtotal_minor":100000}`)
	if rec.code != http.StatusOK {
		t.Fatalf("preview = %d, want 200: %s", rec.code, rec.body)
	}
	if p.Applies || p.Applied != nil {
		t.Fatalf("preview = %+v, want applies false — the rule can never fire", p)
	}
	if !strings.Contains(p.Reason, "automatic discounts are not applied at checkout yet") {
		t.Errorf("reason = %q, want the codeless refusal", p.Reason)
	}

	// And the reason is true: an order placed with no code gets nothing.
	order, err := checkoutWithCode(t, app, "", product.DefaultVariant().ID, 1, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if order.Discount.AmountMinor != 0 || len(order.Discounts) != 0 {
		t.Errorf("an automatic rule fired at checkout: %+v", order.Discounts)
	}
	after, err := app.Discounts().Get(ctx, d.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.UsedCount != 0 {
		t.Errorf("used_count = %d, want 0", after.UsedCount)
	}
}

func TestPreviewAnswersWhyARuleWouldNotApply(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	product := simpleProduct(t, app, "PREV-WHY", 1000, 40)
	vid := product.DefaultVariant().ID

	past := time.Now().Add(-48 * time.Hour)
	future := time.Now().Add(48 * time.Hour)
	off := false
	limit := 1
	min := int64(500000)

	cases := []struct {
		name string
		in   DiscountInput
		body string
		want string
	}{
		{"expired", DiscountInput{Code: "EXPIRED", Title: "x", Kind: DiscountFixed,
			ValueMinor: 100, EndsAt: &past}, `{"subtotal_minor":100000}`, "has expired"},
		{"unstarted", DiscountInput{Code: "LATER", Title: "x", Kind: DiscountFixed,
			ValueMinor: 100, StartsAt: &future}, `{"subtotal_minor":100000}`, "has not started"},
		{"inactive", DiscountInput{Code: "OFF", Title: "x", Kind: DiscountFixed,
			ValueMinor: 100, Active: &off}, `{"subtotal_minor":100000}`, "is not active"},
		{"minimum", DiscountInput{Code: "BIG", Title: "x", Kind: DiscountFixed,
			ValueMinor: 100, MinSubtotalMinor: &min}, `{"subtotal_minor":1000}`, "needs a basket of at least"},
	}
	for _, c := range cases {
		d := newDiscount(t, app, c.in)
		rec, p := previewDiscount(t, app, d.ID, c.body)
		if rec.code != http.StatusOK {
			t.Errorf("%s: preview = %d, want 200 — a refusal is data (D43): %s", c.name, rec.code, rec.body)
			continue
		}
		if p.Applies || !strings.Contains(p.Reason, c.want) {
			t.Errorf("%s: preview = %+v, want applies false containing %q", c.name, p, c.want)
		}
	}

	// Fully used: spend the one use, then preview.
	used := newDiscount(t, app, DiscountInput{Code: "SPENT", Title: "x",
		Kind: DiscountFixed, ValueMinor: 100, UsageLimit: &limit})
	if _, err := checkoutWithCode(t, app, "SPENT", vid, 1, "spent@x.com"); err != nil {
		t.Fatalf("spend it: %v", err)
	}
	if _, p := previewDiscount(t, app, used.ID, `{"subtotal_minor":100000}`); p.Applies ||
		!strings.Contains(p.Reason, "fully used") {
		t.Errorf("spent rule = %+v, want the fully-used reason", p)
	}

	// Once per email: the address is what makes the refusal reachable.
	once := newDiscount(t, app, DiscountInput{Code: "ONEEACH", Title: "x",
		Kind: DiscountFixed, ValueMinor: 100, OncePerEmail: true})
	if _, err := checkoutWithCode(t, app, "ONEEACH", vid, 1, "again@x.com"); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if _, p := previewDiscount(t, app, once.ID,
		`{"subtotal_minor":100000,"email":"again@x.com"}`); p.Applies ||
		!strings.Contains(p.Reason, "already been used with this email") {
		t.Errorf("once-per-email = %+v, want the refusal", p)
	}

	// Scoped: a bare subtotal cannot answer for it, and says so.
	scoped := newDiscount(t, app, DiscountInput{Code: "SCOPED", Title: "x",
		Kind: DiscountPercentage, ValueBP: 1000,
		Scope: DiscountScopeProducts, TargetIDs: []int64{product.ID}})
	if _, p := previewDiscount(t, app, scoped.ID, `{"subtotal_minor":100000}`); p.Applies ||
		!strings.Contains(p.Reason, "send the lines") {
		t.Errorf("scoped rule with a bare subtotal = %+v, want the missing-lines reason", p)
	}
	// With the lines it works, and the value comes off the lines its targets reach.
	body := `{"lines":[{"product_id":` + strconv.FormatInt(product.ID, 10) + `,"total_minor":100000}]}`
	if _, p := previewDiscount(t, app, scoped.ID, body); !p.Applies || p.Applied.AmountMinor != 10000 {
		t.Errorf("scoped rule with lines = %+v, want 10%% of the eligible lines", p)
	}

	// And the taxonomy did not move: checkout still answers 400 for the same
	// sentence, because there a refusal is a failed attempt.
	if _, err := checkoutWithCode(t, app, "EXPIRED", vid, 1, "e@x.com"); err == nil {
		t.Error("checkout accepted an expired code")
	}
	_ = ctx
}

func TestPreviewOfAFreeShippingRuleSaysSoRatherThanZero(t *testing.T) {
	app := newTestApp(t)
	d := newDiscount(t, app, DiscountInput{
		Code: "SHIPFREE", Title: "Free shipping", Kind: DiscountFreeShipping,
	})

	_, p := previewDiscount(t, app, d.ID, `{"subtotal_minor":100000}`)
	if !p.Applies {
		t.Fatalf("preview = %+v, want applies true", p)
	}
	// The panel branches on free_shipping, not on the amount: "takes off 0.00"
	// is the wrong sentence for a rule that is working.
	if p.Applied == nil || !p.Applied.FreeShipping || p.Applied.AmountMinor != 0 {
		t.Errorf("applied = %+v, want free_shipping true with amount 0", p.Applied)
	}
}

func TestPreviewRefusesARubbishBasket(t *testing.T) {
	app := newTestApp(t)
	d := newDiscount(t, app, DiscountInput{
		Code: "RUBBISH", Title: "x", Kind: DiscountFixed, ValueMinor: 100,
	})
	id := strconv.FormatInt(d.ID, 10)

	if rec, _ := previewDiscount(t, app, d.ID, `{"subtotal_minor":-1}`); rec.code != http.StatusBadRequest {
		t.Errorf("negative subtotal = %d, want 400: %s", rec.code, rec.body)
	}
	if rec, _ := previewDiscount(t, app, d.ID, `{"subtotal_minor":100,"nonsense":1}`); rec.code != http.StatusBadRequest {
		t.Errorf("unknown field = %d, want 400 — DecodeJSON disallows them: %s", rec.code, rec.body)
	}
	rec := doBody(t, app, "POST", "/api/admin/discounts/999999/preview", `{"subtotal_minor":100}`, withAdmin)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown id = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	rec = doBody(t, app, "POST", "/api/admin/discounts/abc/preview", `{"subtotal_minor":100}`, withAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("non-numeric id = %d, want 400 rather than a 404 on discount 0: %s",
			rec.Code, rec.Body.String())
	}
	if rec, _ := previewDiscount(t, app, d.ID,
		`{"subtotal_minor":100,"lines":[{"product_id":1,"total_minor":100}]}`); rec.code != http.StatusBadRequest {
		t.Errorf("both a subtotal and lines = %d, want 400: %s", rec.code, rec.body)
	}
	_ = id
}
