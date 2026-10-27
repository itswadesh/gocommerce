package gocommerce

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// Add-ons (an order bump) and cross-sells (D133). The price an add-on is
// offered at is the engine's: it holds only while the product it is offered
// with is in the basket, for as many as there are of that product.

func setAddOns(t *testing.T, app *App, productID int64, in ...AddOnInput) []AddOn {
	t.Helper()
	got, err := app.Upsells().SetAddOns(context.Background(), productID, in)
	if err != nil {
		t.Fatalf("set add-ons: %v", err)
	}
	return got
}

func addOnLine(c *Cart) *CartLine {
	for i := range c.Lines {
		if c.Lines[i].AddOnID != 0 {
			return &c.Lines[i]
		}
	}
	return nil
}

func regularLineOf(c *Cart, variantID int64) *CartLine {
	for i := range c.Lines {
		if c.Lines[i].VariantID == variantID && c.Lines[i].AddOnID == 0 {
			return &c.Lines[i]
		}
	}
	return nil
}

func TestAddOnIsPricedWithItsProduct(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	phoneCase := simpleProduct(t, app, "CASE-1", 17900, 10)
	grip := simpleProduct(t, app, "GRIP-1", 12900, 10)
	offer := int64(4900)
	addOns := setAddOns(t, app, phoneCase.ID, AddOnInput{VariantID: grip.DefaultVariant().ID, PriceMinor: &offer})
	if len(addOns) != 1 || addOns[0].OfferPrice == nil || addOns[0].OfferPrice.AmountMinor != 4900 ||
		addOns[0].Price.AmountMinor != 12900 {
		t.Fatalf("add-ons = %+v, want the grip at 4900 instead of 12900", addOns)
	}

	cart := newCart(t, app)
	// Without the case in the basket there is nothing to offer it with.
	if _, err := app.Cart().AddAddOn(ctx, cart.Token, addOns[0].ID, 1); !isConflict(err) {
		t.Fatalf("add-on without its product: err = %v, want a conflict", err)
	}
	addToCart(t, app, cart.Token, phoneCase.DefaultVariant().ID, 1)
	cart, err := app.Cart().AddAddOn(ctx, cart.Token, addOns[0].ID, 1)
	if err != nil {
		t.Fatalf("add the add-on: %v", err)
	}
	line := addOnLine(cart)
	if line == nil || line.UnitPrice.AmountMinor != 4900 || line.CurrentPrice.AmountMinor != 4900 || line.PriceChanged {
		t.Fatalf("add-on line = %+v, want 4900 and unchanged", line)
	}
	if line.AddOnFor != phoneCase.ID {
		t.Errorf("add_on_for = %d, want the case %d", line.AddOnFor, phoneCase.ID)
	}
	if cart.Subtotal.AmountMinor != 17900+4900 {
		t.Errorf("subtotal = %d, want %d", cart.Subtotal.AmountMinor, 17900+4900)
	}

	// One grip at the offer price per case: a second is refused, not repriced.
	if _, err := app.Cart().AddAddOn(ctx, cart.Token, addOns[0].ID, 1); !isConflict(err) {
		t.Fatalf("a second add-on for one case: err = %v, want a conflict", err)
	}
	if _, err := app.Cart().UpdateLine(ctx, cart.Token, line.ID, 2); !isConflict(err) {
		t.Fatalf("raising the add-on past its product: err = %v, want a conflict", err)
	}

	result, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if result.Order.Subtotal.AmountMinor != 17900+4900 {
		t.Errorf("order subtotal = %d, want %d", result.Order.Subtotal.AmountMinor, 17900+4900)
	}
}

// Taking the product out takes the offer away: the add-on stays in the basket
// at its own price, merged into a line of it the shopper already had.
func TestAddOnLosesItsPriceWithoutItsProduct(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	phoneCase := simpleProduct(t, app, "CASE-2", 17900, 10)
	grip := simpleProduct(t, app, "GRIP-2", 12900, 10)
	offer := int64(4900)
	addOns := setAddOns(t, app, phoneCase.ID, AddOnInput{VariantID: grip.DefaultVariant().ID, PriceMinor: &offer})

	cart := newCart(t, app)
	cart = addToCart(t, app, cart.Token, phoneCase.DefaultVariant().ID, 2)
	caseLine := cart.Lines[0].ID
	cart = addToCart(t, app, cart.Token, grip.DefaultVariant().ID, 1)
	cart, err := app.Cart().AddAddOn(ctx, cart.Token, addOns[0].ID, 2)
	if err != nil {
		t.Fatalf("two add-ons for two cases: %v", err)
	}
	if got := addOnLine(cart); got == nil || got.Quantity != 2 {
		t.Fatalf("add-on line = %+v, want two at the offer price", got)
	}
	if regular := regularLineOf(cart, grip.DefaultVariant().ID); regular == nil || regular.Quantity != 1 ||
		regular.UnitPrice.AmountMinor != 12900 {
		t.Fatalf("the grip bought on its own = %+v, want one at 12900 beside the offer", regular)
	}

	// One case fewer: one grip goes back to its own price.
	cart, err = app.Cart().UpdateLine(ctx, cart.Token, caseLine, 1)
	if err != nil {
		t.Fatalf("one case fewer: %v", err)
	}
	if got := addOnLine(cart); got == nil || got.Quantity != 1 {
		t.Fatalf("add-on line after one case fewer = %+v, want one", got)
	}
	if regular := regularLineOf(cart, grip.DefaultVariant().ID); regular == nil || regular.Quantity != 2 {
		t.Fatalf("regular grips = %+v, want two", regular)
	}

	// No case at all: no offer.
	cart, err = app.Cart().RemoveLine(ctx, cart.Token, caseLine)
	if err != nil {
		t.Fatalf("remove the case: %v", err)
	}
	if got := addOnLine(cart); got != nil {
		t.Fatalf("an add-on line outlived its product: %+v", got)
	}
	if len(cart.Lines) != 1 || cart.Lines[0].Quantity != 3 || cart.Lines[0].UnitPrice.AmountMinor != 12900 ||
		cart.Subtotal.AmountMinor != 3*12900 {
		t.Fatalf("basket = %+v, want three grips at 12900", cart.Lines)
	}
}

// An add-on with no offer price is a suggestion: it goes in at its own price.
func TestAddOnWithoutAnOfferPriceIsAnOrdinaryLine(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	phoneCase := simpleProduct(t, app, "CASE-3", 17900, 10)
	guard := simpleProduct(t, app, "GUARD-3", 9900, 10)
	addOns := setAddOns(t, app, phoneCase.ID, AddOnInput{VariantID: guard.DefaultVariant().ID})

	cart := newCart(t, app)
	addToCart(t, app, cart.Token, phoneCase.DefaultVariant().ID, 1)
	cart, err := app.Cart().AddAddOn(ctx, cart.Token, addOns[0].ID, 1)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if addOnLine(cart) != nil {
		t.Fatal("an add-on with no offer price made an offer line")
	}
	if l := regularLineOf(cart, guard.DefaultVariant().ID); l == nil || l.UnitPrice.AmountMinor != 9900 {
		t.Fatalf("guard line = %+v, want 9900", l)
	}
}

// The shop withdrawing an offer while it is in a basket is a price change,
// which checkout refuses until the shopper has seen the new price.
func TestWithdrawnAddOnIsAPriceChange(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	phoneCase := simpleProduct(t, app, "CASE-4", 17900, 10)
	grip := simpleProduct(t, app, "GRIP-4", 12900, 10)
	offer := int64(4900)
	addOns := setAddOns(t, app, phoneCase.ID, AddOnInput{VariantID: grip.DefaultVariant().ID, PriceMinor: &offer})
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, phoneCase.DefaultVariant().ID, 1)
	if _, err := app.Cart().AddAddOn(ctx, cart.Token, addOns[0].ID, 1); err != nil {
		t.Fatalf("add: %v", err)
	}
	setAddOns(t, app, phoneCase.ID)

	cart, err := app.Cart().GetByToken(ctx, cart.Token)
	if err != nil {
		t.Fatal(err)
	}
	if l := addOnLine(cart); l == nil || !l.PriceChanged || l.CurrentPrice.AmountMinor != 12900 {
		t.Fatalf("withdrawn add-on line = %+v, want price_changed to 12900", l)
	}
	_, err = app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	if !isConflict(err) || !strings.Contains(err.Error(), "prices") {
		t.Fatalf("checkout = %v, want a price conflict", err)
	}
}

func TestSetAddOnsRefusesNonsense(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	phoneCase := simpleProduct(t, app, "CASE-5", 17900, 10)
	grip := simpleProduct(t, app, "GRIP-5", 12900, 10)
	neg := int64(-1)
	for name, in := range map[string][]AddOnInput{
		"itself":         {{VariantID: phoneCase.DefaultVariant().ID}},
		"twice":          {{VariantID: grip.DefaultVariant().ID}, {VariantID: grip.DefaultVariant().ID}},
		"negative price": {{VariantID: grip.DefaultVariant().ID, PriceMinor: &neg}},
		"no variant":     {{VariantID: 999999}},
	} {
		if _, err := app.Upsells().SetAddOns(ctx, phoneCase.ID, in); err == nil {
			t.Errorf("%s: saved, want refused", name)
		}
	}
	tooMany := make([]AddOnInput, maxAddOns+1)
	if _, err := app.Upsells().SetAddOns(ctx, phoneCase.ID, tooMany); err == nil {
		t.Error("more than the limit was saved")
	}
}

func TestAddOnsOverHTTP(t *testing.T) {
	app := newTestApp(t)
	phoneCase := simpleProduct(t, app, "CASE-6", 17900, 10)
	grip := simpleProduct(t, app, "GRIP-6", 12900, 10)
	hidden := simpleProduct(t, app, "HIDDEN-6", 9900, 10)
	if _, err := app.Products().UpdateProduct(context.Background(), hidden.ID, ProductPatch{Status: strPtr(ProductDraft)}); err != nil {
		t.Fatal(err)
	}

	body := `{"add_ons":[{"variant_id":` + strconv.FormatInt(grip.DefaultVariant().ID, 10) + `,"price_minor":4900},` +
		`{"variant_id":` + strconv.FormatInt(hidden.DefaultVariant().ID, 10) + `}]}`
	path := "/api/admin/products/" + strconv.FormatInt(phoneCase.ID, 10) + "/add-ons"
	rec := do(t, app, http.MethodPut, path, withAdmin, withBody(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT add-ons = %d %s", rec.Code, rec.Body)
	}
	rec = do(t, app, http.MethodGet, path, withAdmin)
	var admin struct{ Data []AddOn }
	_ = json.Unmarshal(rec.Body.Bytes(), &admin)
	if len(admin.Data) != 2 {
		t.Fatalf("admin add-ons = %s, want both", rec.Body)
	}

	// The storefront sees only what can be bought.
	rec = do(t, app, http.MethodGet, "/api/add-ons?product_id="+strconv.FormatInt(phoneCase.ID, 10))
	var public struct{ Data []AddOn }
	_ = json.Unmarshal(rec.Body.Bytes(), &public)
	if rec.Code != http.StatusOK || len(public.Data) != 1 || public.Data[0].Item.Title != grip.Title {
		t.Fatalf("public add-ons = %d %s, want the grip alone", rec.Code, rec.Body)
	}

	cart := newCart(t, app)
	addToCart(t, app, cart.Token, phoneCase.DefaultVariant().ID, 1)
	rec = do(t, app, http.MethodPost, "/api/carts/"+cart.Token+"/line-items",
		withBody(`{"add_on_id":`+strconv.FormatInt(public.Data[0].ID, 10)+`}`))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"add_on_id":`+strconv.FormatInt(public.Data[0].ID, 10)) {
		t.Fatalf("add the add-on over HTTP = %d %s", rec.Code, rec.Body)
	}
}

func TestCrossSells(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	phoneCase := simpleProduct(t, app, "CASE-7", 17900, 10)
	cable := simpleProduct(t, app, "CABLE-7", 12900, 10)
	light := simpleProduct(t, app, "LIGHT-7", 1900, 10)
	draft := simpleProduct(t, app, "DRAFT-7", 1900, 10)
	if _, err := app.Products().UpdateProduct(ctx, draft.ID, ProductPatch{Status: strPtr(ProductDraft)}); err != nil {
		t.Fatal(err)
	}

	if err := app.Upsells().SetCrossSells(ctx, phoneCase.ID, []int64{phoneCase.ID}); err == nil {
		t.Error("a product was saved as its own cross-sell")
	}
	if err := app.Upsells().SetCrossSells(ctx, phoneCase.ID, []int64{light.ID, draft.ID, cable.ID}); err != nil {
		t.Fatalf("set cross-sells: %v", err)
	}
	ids, err := app.Upsells().CrossSellIDs(ctx, phoneCase.ID)
	if err != nil || len(ids) != 3 || ids[0] != light.ID || ids[2] != cable.ID {
		t.Fatalf("cross-sell ids = %v %v, want light, draft, cable in that order", ids, err)
	}

	rec := do(t, app, http.MethodGet, "/api/cross-sells?product_id="+strconv.FormatInt(phoneCase.ID, 10))
	var got struct{ Data []Product }
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || len(got.Data) != 2 || got.Data[0].ID != light.ID || got.Data[1].ID != cable.ID {
		t.Fatalf("public cross-sells = %d %s, want light then cable", rec.Code, rec.Body)
	}

	// The basket's: what its products suggest, less what it already holds.
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, phoneCase.DefaultVariant().ID, 1)
	addToCart(t, app, cart.Token, light.DefaultVariant().ID, 1)
	rec = do(t, app, http.MethodGet, "/api/carts/"+cart.Token+"/cross-sells")
	got.Data = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || len(got.Data) != 1 || got.Data[0].ID != cable.ID {
		t.Fatalf("cart cross-sells = %d %s, want the cable alone", rec.Code, rec.Body)
	}
}

// The coupons a shop chose to show, each with what this basket still needs.
func TestCartOffersListOnlyListedCodes(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	min := int64(30000)
	listed := true
	if _, err := app.Discounts().Create(ctx, DiscountInput{Code: "HELLO5", Title: "5% off", Kind: DiscountPercentage,
		ValueBP: 500, Listed: &listed}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Discounts().Create(ctx, DiscountInput{Code: "BIG100", Title: "100 off", Kind: DiscountFixed,
		ValueMinor: 10000, MinSubtotalMinor: &min, Listed: &listed}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Discounts().Create(ctx, DiscountInput{Code: "STAFF50", Title: "Staff", Kind: DiscountPercentage,
		ValueBP: 5000}); err != nil {
		t.Fatal(err)
	}
	p := simpleProduct(t, app, "OFFER-1", 12900, 10)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, p.DefaultVariant().ID, 1)

	rec := do(t, app, http.MethodGet, "/api/carts/"+cart.Token+"/offers")
	var got struct{ Data []CartOffer }
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || len(got.Data) != 2 {
		t.Fatalf("offers = %d %s, want the two listed codes", rec.Code, rec.Body)
	}
	byCode := map[string]CartOffer{}
	for _, o := range got.Data {
		byCode[o.Code] = o
	}
	if o := byCode["HELLO5"]; !o.Applies || o.Amount == nil || o.Amount.AmountMinor != 645 {
		t.Errorf("HELLO5 = %+v, want it to apply for 645", o)
	}
	if o := byCode["BIG100"]; o.Applies || o.Remaining == nil || o.Remaining.AmountMinor != 30000-12900 {
		t.Errorf("BIG100 = %+v, want 17100 still to add", o)
	}
	if _, ok := byCode["STAFF50"]; ok {
		t.Error("an unlisted code was shown to a shopper")
	}
}

// How far the basket is from free delivery, read from the shop's own rates.
func TestFreeShippingProgress(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	zone, err := app.Shipping().CreateZone(ctx, ShippingZoneInput{Name: "India", Countries: []string{"IN"}})
	if err != nil {
		t.Fatal(err)
	}
	limit := int64(49900)
	for _, r := range []ShippingRateInput{
		{ZoneID: zone.ID, Name: "Standard", PriceMinor: 6000, MaxSubtotalMinor: &limit},
		{ZoneID: zone.ID, Name: "Standard", PriceMinor: 0, MinSubtotalMinor: 49900},
	} {
		if _, err := app.Shipping().CreateRate(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	p := simpleProduct(t, app, "FREE-1", 12900, 10)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, p.DefaultVariant().ID, 1)

	rec := do(t, app, http.MethodGet, "/api/carts/"+cart.Token+"/free-shipping?country=IN")
	var got struct{ Data FreeShipping }
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || got.Data.Qualifies || got.Data.Threshold == nil ||
		got.Data.Threshold.AmountMinor != 49900 || got.Data.Remaining == nil || got.Data.Remaining.AmountMinor != 49900-12900 {
		t.Fatalf("free shipping = %d %s, want 37000 to go to 49900", rec.Code, rec.Body)
	}

	addToCart(t, app, cart.Token, p.DefaultVariant().ID, 3)
	rec = do(t, app, http.MethodGet, "/api/carts/"+cart.Token+"/free-shipping?country=IN")
	got.Data = FreeShipping{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.Data.Qualifies || got.Data.Remaining != nil {
		t.Fatalf("free shipping at 51600 = %s, want it to qualify", rec.Body)
	}

	// Where the shop has no free band there is nothing to promise.
	rec = do(t, app, http.MethodGet, "/api/carts/"+cart.Token+"/free-shipping?country=US")
	got.Data = FreeShipping{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Data.Qualifies || got.Data.Threshold != nil {
		t.Fatalf("free shipping where nothing is free = %s", rec.Body)
	}
}

func isConflict(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Code == ErrConflict.Code
}

func withBody(body string) func(*http.Request) {
	return func(r *http.Request) {
		r.Body = io.NopCloser(strings.NewReader(body))
		r.ContentLength = int64(len(body))
		r.Header.Set("Content-Type", "application/json")
	}
}
