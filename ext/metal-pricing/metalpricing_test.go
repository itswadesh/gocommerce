package metalpricing

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	gocommerce "github.com/misiki-in/gocommerce/core"
	"github.com/misiki-in/gocommerce/gctest"
)

func TestPriceComesOutAsAJewellerWouldWorkIt(t *testing.T) {
	// 10 g of gold at ₹6,000 a gram is ₹60,000. 8% wastage is ₹4,800.
	base := Recipe{WeightMg: 10_000, WastageBP: 800, StonesMinor: 2_500_00}
	const rate, rupee = 6_000_00, 100
	for name, c := range map[string]struct {
		kind  string
		value int64
		want  Breakdown
	}{
		"flat ₹5,000": {MakingFlat, 5_000_00, Breakdown{60_000_00, 4_800_00, 5_000_00, 2_500_00, 72_300_00}},
		"₹450 a gram": {MakingPerGram, 450_00, Breakdown{60_000_00, 4_800_00, 4_500_00, 2_500_00, 71_800_00}},
		"12% of gold": {MakingPercent, 1200, Breakdown{60_000_00, 4_800_00, 7_200_00, 2_500_00, 74_500_00}},
	} {
		r := base
		r.MakingKind, r.MakingValue = c.kind, c.value
		if got := price(r, rate, rupee); got != c.want {
			t.Errorf("%s = %+v, want %+v", name, got, c.want)
		}
	}
}

func TestPriceRoundsToAWholeRupeeHalfUp(t *testing.T) {
	for _, c := range []struct {
		weightMg, rate, want int64
	}{
		{1_234, 6_543_21, 8_074_00}, // 8,074.33
		{1_500, 1_000_33, 1_501_00}, // 1,500.50 rounds up (1,500.495 rounds to 1,500.50 first)
		{1, 1, 0},                   // far under a paisa
	} {
		got := price(Recipe{WeightMg: c.weightMg, MakingKind: MakingFlat}, c.rate, 100).TotalMinor
		if got != c.want {
			t.Errorf("%d mg at %d = %d, want %d", c.weightMg, c.rate, got, c.want)
		}
	}
	// A currency with no minor unit has nothing to round.
	if got := price(Recipe{WeightMg: 1500, MakingKind: MakingFlat}, 1001, 1).TotalMinor; got != 1502 {
		t.Errorf("yen = %d", got)
	}
}

// The largest values check() allows must not overflow, or a price goes negative.
func TestLargestAllowedValuesDoNotOverflow(t *testing.T) {
	r := Recipe{WeightMg: maxWeightMg, WastageBP: maxBasisPoints, MakingKind: MakingPercent, MakingValue: maxBasisPoints, StonesMinor: maxFlatMinor}
	if err := r.check(); err == nil {
		// RateCode is empty on purpose above; check() must still object to that alone.
		t.Fatal("an empty rate code passed")
	}
	if got := price(r, maxRateMinor, 100); got.TotalMinor <= 0 || got.MetalMinor != 100_000_000_000_000 {
		t.Fatalf("overflowed: %+v", got)
	}
	r.MakingKind, r.MakingValue = MakingPerGram, maxRateMinor
	if got := price(r, maxRateMinor, 100); got.TotalMinor <= 0 {
		t.Fatalf("overflowed per gram: %+v", got)
	}
}

func TestDecimalNeverRounds(t *testing.T) {
	for _, c := range []struct {
		in     string
		places int
		want   int64
		bad    bool
	}{
		{"12.345", 3, 12345, false},
		{"12", 3, 12000, false},
		{".5", 3, 500, false},
		{"1,50,000.50", 2, 15000050, false},
		{"", 2, 0, false},
		{"12.3456", 3, 0, true},
		{"-1", 2, 0, true},
		{"abc", 2, 0, true},
	} {
		got, err := decimal(c.in, c.places)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("decimal(%q, %d) = %d, %v", c.in, c.places, got, err)
		}
	}
}

func newApp(t *testing.T) *gocommerce.App {
	t.Helper()
	return gctest.NewWithConfig(t, gocommerce.Config{Currency: "INR"}, New(Config{}))
}

func setRate(t *testing.T, app *gocommerce.App, code string, minor int64) Outcome {
	t.Helper()
	rec := gctest.AdminRequest(t, app, http.MethodPut, "/api/admin/x/metal-pricing/rates/"+code,
		map[string]any{"name": strings.ToUpper(code), "rate_minor": minor})
	if rec.Code != http.StatusOK {
		t.Fatalf("set rate = %d %s", rec.Code, rec.Body)
	}
	var out Outcome
	gctest.DecodeData(t, rec, &out)
	return out
}

func priceOf(t *testing.T, app *gocommerce.App, sku string) int64 {
	t.Helper()
	v, err := app.Products().GetVariantBySKU(context.Background(), sku)
	if err != nil {
		t.Fatal(err)
	}
	return v.Price.AmountMinor
}

func recipe(sku, rate string) map[string]any {
	return map[string]any{"sku": sku, "rate_code": rate, "weight_mg": 10_000, "wastage_bp": 800,
		"making_kind": "percent", "making_value": 1200, "stones_minor": 2_500_00}
}

func TestEnteringARateRepricesOnlyThatMetal(t *testing.T) {
	app := newApp(t)
	gctest.CreateProduct(t, app, "RING-22K", 1_00, 5)
	gctest.CreateProduct(t, app, "RING-18K", 1_00, 5)
	gctest.CreateProduct(t, app, "PLAIN-BAG", 999_00, 5)
	setRate(t, app, "gold-22k", 6_000_00)
	setRate(t, app, "gold-18k", 5_000_00)

	for sku, rate := range map[string]string{"RING-22K": "gold-22k", "RING-18K": "gold-18k"} {
		if rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/metal-pricing/recipes", recipe(sku, rate)); rec.Code != http.StatusOK {
			t.Fatalf("recipe = %d %s", rec.Code, rec.Body)
		}
	}
	if got := priceOf(t, app, "RING-22K"); got != 74_500_00 {
		t.Fatalf("priced at once to %d, want 74,500.00", got)
	}
	r18 := priceOf(t, app, "RING-18K")

	out := setRate(t, app, "gold-22k", 6_500_00)
	if out.Repriced != 1 || len(out.Failed) != 0 {
		t.Fatalf("outcome %+v", out)
	}
	// 65,000 + 5,200 + 7,800 + 2,500
	if got := priceOf(t, app, "RING-22K"); got != 80_500_00 {
		t.Errorf("22K at the new rate = %d", got)
	}
	if got := priceOf(t, app, "RING-18K"); got != r18 {
		t.Errorf("18K moved with 22K: %d", got)
	}
	if got := priceOf(t, app, "PLAIN-BAG"); got != 999_00 {
		t.Errorf("a product with no recipe moved: %d", got)
	}
}

// As gold rises, a struck-through price below today's would read as a discount
// that costs more.
func TestAStrikeThroughBelowTheNewPriceComesOff(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()
	gctest.CreateProduct(t, app, "BANGLE", 1_00, 5)
	v, _ := app.Products().GetVariantBySKU(ctx, "BANGLE")
	compare := int64(76_000_00)
	if _, err := app.Products().UpdateVariant(ctx, v.ID, gocommerce.VariantPatch{CompareAtPriceMinor: gocommerce.NullableAmount{Present: true, Value: &compare}}); err != nil {
		t.Fatal(err)
	}
	setRate(t, app, "gold-22k", 6_000_00)
	gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/metal-pricing/recipes", recipe("BANGLE", "gold-22k"))
	v, _ = app.Products().GetVariantBySKU(ctx, "BANGLE")
	if v.CompareAtPrice == nil {
		t.Fatal("a compare-at above the price was removed")
	}
	setRate(t, app, "gold-22k", 6_500_00)
	v, _ = app.Products().GetVariantBySKU(ctx, "BANGLE")
	if v.CompareAtPrice != nil {
		t.Fatalf("compare-at %d left under price %d", v.CompareAtPrice.AmountMinor, v.Price.AmountMinor)
	}
}

func TestAnOrderKeepsThePriceItWasPlacedAt(t *testing.T) {
	app := newApp(t)
	p := gctest.CreateProduct(t, app, "CHAIN", 1_00, 5)
	setRate(t, app, "gold-22k", 6_000_00)
	gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/metal-pricing/recipes", recipe("CHAIN", "gold-22k"))
	placed := gctest.Buy(t, app, "cod", p.DefaultVariant().ID, 1)
	before := placed.Order.Total.AmountMinor

	setRate(t, app, "gold-22k", 9_000_00)
	o, err := app.Order().Get(context.Background(), placed.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.Total.AmountMinor != before {
		t.Fatalf("a rate change moved a placed order from %d to %d", before, o.Total.AmountMinor)
	}
}

func TestHandEditedPriceIsRestoredByReprice(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()
	gctest.CreateProduct(t, app, "PENDANT", 1_00, 5)
	setRate(t, app, "gold-22k", 6_000_00)
	gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/metal-pricing/recipes", recipe("PENDANT", "gold-22k"))
	v, _ := app.Products().GetVariantBySKU(ctx, "PENDANT")
	wrong := int64(10_00)
	if _, err := app.Products().UpdateVariant(ctx, v.ID, gocommerce.VariantPatch{PriceMinor: &wrong}); err != nil {
		t.Fatal(err)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/metal-pricing/reprice", nil); rec.Code != http.StatusOK {
		t.Fatalf("reprice = %d %s", rec.Code, rec.Body)
	}
	if got := priceOf(t, app, "PENDANT"); got != 74_500_00 {
		t.Fatalf("after reprice %d", got)
	}
}

func TestRemovingARecipeKeepsThePriceAndStopsFollowing(t *testing.T) {
	app := newApp(t)
	p := gctest.CreateProduct(t, app, "EARRING", 1_00, 5)
	setRate(t, app, "gold-22k", 6_000_00)
	gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/metal-pricing/recipes", recipe("EARRING", "gold-22k"))
	path := fmt.Sprintf("/api/admin/x/metal-pricing/recipes/%d", p.DefaultVariant().ID)
	if rec := gctest.AdminRequest(t, app, http.MethodDelete, path, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	setRate(t, app, "gold-22k", 9_000_00)
	if got := priceOf(t, app, "EARRING"); got != 74_500_00 {
		t.Fatalf("still following the rate: %d", got)
	}
}

func TestARateInUseCannotBeDeleted(t *testing.T) {
	app := newApp(t)
	gctest.CreateProduct(t, app, "ANKLET", 1_00, 5)
	setRate(t, app, "silver", 90_00)
	gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/metal-pricing/recipes", recipe("ANKLET", "silver"))
	if rec := gctest.AdminRequest(t, app, http.MethodDelete, "/api/admin/x/metal-pricing/rates/silver", nil); rec.Code != http.StatusConflict {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestBadInputChangesNothing(t *testing.T) {
	app := newApp(t)
	gctest.CreateProduct(t, app, "NOSE", 5_00, 5)
	setRate(t, app, "gold-22k", 6_000_00)
	for name, body := range map[string]map[string]any{
		"unknown rate": {"sku": "NOSE", "rate_code": "gold-9k", "weight_mg": 100, "making_kind": "flat"},
		"no weight":    {"sku": "NOSE", "rate_code": "gold-22k", "weight_mg": 0, "making_kind": "flat"},
		"bad kind":     {"sku": "NOSE", "rate_code": "gold-22k", "weight_mg": 100, "making_kind": "free"},
		"over 100%":    {"sku": "NOSE", "rate_code": "gold-22k", "weight_mg": 100, "making_kind": "percent", "making_value": 10_001},
		"unknown sku":  {"sku": "GHOST", "rate_code": "gold-22k", "weight_mg": 100, "making_kind": "flat"},
	} {
		if rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/metal-pricing/recipes", body); rec.Code == http.StatusOK {
			t.Errorf("%s was accepted", name)
		}
	}
	if got := priceOf(t, app, "NOSE"); got != 5_00 {
		t.Errorf("price moved to %d", got)
	}
	for _, minor := range []int64{0, -5, maxRateMinor + 1} {
		rec := gctest.AdminRequest(t, app, http.MethodPut, "/api/admin/x/metal-pricing/rates/gold-22k", map[string]any{"name": "G", "rate_minor": minor})
		if rec.Code == http.StatusOK {
			t.Errorf("rate %d was accepted", minor)
		}
	}
}

func TestImportReportsBadRowsAndKeepsTheRest(t *testing.T) {
	app := newApp(t)
	gctest.CreateProduct(t, app, "A1", 1_00, 5)
	gctest.CreateProduct(t, app, "A2", 1_00, 5)
	setRate(t, app, "gold-22k", 6_000_00)
	file := "SKU,Rate_Code,Weight_G,Wastage_Percent,Making_Kind,Making_Value,Stones_Value\n" +
		"A1,gold-22k,10,8,percent,12,2500\n" +
		"GHOST,gold-22k,10,8,flat,500,0\n" +
		"A2,gold-22k,1.2345,0,flat,500,0\n" +
		",,,,,,\n"
	rec := gctest.AdminUpload(t, app, http.MethodPost, "/api/admin/x/metal-pricing/recipes/import", "text/csv", file)
	if rec.Code != http.StatusOK {
		t.Fatalf("import = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Saved    int           `json:"saved"`
		Rejected []RejectedRow `json:"rejected"`
	}
	gctest.DecodeData(t, rec, &out)
	if out.Saved != 1 || len(out.Rejected) != 2 || out.Rejected[0].Row != 3 || out.Rejected[1].Row != 4 {
		t.Fatalf("outcome %+v", out)
	}
	if got := priceOf(t, app, "A1"); got != 74_500_00 {
		t.Errorf("A1 = %d", got)
	}
	if got := priceOf(t, app, "A2"); got != 1_00 {
		t.Errorf("a row with too many decimals changed A2 to %d", got)
	}
}

func TestStaffCanSeeButNotChangeRates(t *testing.T) {
	app := newApp(t)
	gctest.AssertAdminRoutesDeclareRights(t, app, "metal-pricing")
	staff := gctest.OperatorToken(t, app, "staff@example.com", gocommerce.RoleStaff)
	if rec := gctest.SessionRequest(t, app, staff, http.MethodGet, "/api/admin/x/metal-pricing/rates", nil); rec.Code != http.StatusOK {
		t.Errorf("staff read = %d", rec.Code)
	}
	rec := gctest.SessionRequest(t, app, staff, http.MethodPut, "/api/admin/x/metal-pricing/rates/gold-22k", map[string]any{"name": "G", "rate_minor": 100})
	if rec.Code != http.StatusForbidden {
		t.Errorf("staff write = %d %s", rec.Code, rec.Body)
	}
}

func TestTwoStoresKeepTheirOwnRates(t *testing.T) {
	app := newApp(t)
	gctest.AssertAdminRoutesDeclareRights(t, app, "metal-pricing")
	setRate(t, app, "gold-22k", 6_000_00)
	var count int
	if err := app.DB().QueryRowContext(gocommerce.WithStore(context.Background(), "another-store"),
		`SELECT count(*) FROM metal_pricing_rates`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("another store sees %d rates", count)
	}
}

// A silver price somebody just set by hand is not gold's business.
func TestARateChangeLeavesOtherMetalsAlone(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()
	gctest.CreateProduct(t, app, "SILVER-RING", 1_00, 5)
	setRate(t, app, "silver", 90_00)
	setRate(t, app, "gold-22k", 6_000_00)
	gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/metal-pricing/recipes", recipe("SILVER-RING", "silver"))
	v, _ := app.Products().GetVariantBySKU(ctx, "SILVER-RING")
	byHand := int64(4_321_00)
	if _, err := app.Products().UpdateVariant(ctx, v.ID, gocommerce.VariantPatch{PriceMinor: &byHand}); err != nil {
		t.Fatal(err)
	}
	if out := setRate(t, app, "gold-22k", 7_000_00); out.Repriced+out.Unchanged != 0 {
		t.Fatalf("a gold rate touched silver: %+v", out)
	}
	if got := priceOf(t, app, "SILVER-RING"); got != byHand {
		t.Fatalf("silver moved to %d", got)
	}
}

func TestWeightLimitsAreEnforced(t *testing.T) {
	app := newApp(t)
	gctest.CreateProduct(t, app, "HEAVY", 5_00, 5)
	setRate(t, app, "gold-22k", 6_000_00)
	for _, mg := range []int64{0, -1, maxWeightMg + 1} {
		body := map[string]any{"sku": "HEAVY", "rate_code": "gold-22k", "weight_mg": mg, "making_kind": "flat"}
		if rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/metal-pricing/recipes", body); rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("weight %d answered %d", mg, rec.Code)
		}
	}
}
