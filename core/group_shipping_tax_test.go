package gocommerce

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Shipping and tax per customer group (D76). A company prices through a group,
// and a cart earns its group's prices only through carts.verified_email (D66);
// these are the same two rules applied to delivery and to tax, and the tests
// below hold them to it — including the attack D66 closed, a typed address.

// groupOf makes a group and puts addresses in it.
func groupOf(t *testing.T, app *App, code string, exempt bool, members ...string) *CustomerGroup {
	t.Helper()
	ctx := context.Background()
	g, err := app.Pricing().CreateGroup(ctx, CustomerGroupInput{
		Code: code, Name: strings.ToUpper(code[:1]) + code[1:], TaxExempt: exempt,
	})
	if err != nil {
		t.Fatalf("group %s: %v", code, err)
	}
	for _, m := range members {
		if err := app.Pricing().AddMember(ctx, g.ID, m); err != nil {
			t.Fatalf("member %s: %v", m, err)
		}
	}
	return g
}

// groupRate prices a method for one group only.
func groupRate(t *testing.T, app *App, zoneID, groupID int64, name string, priceMinor, min int64, max *int64) *ShippingRate {
	t.Helper()
	r, err := app.Shipping().CreateRate(context.Background(), ShippingRateInput{
		ZoneID: zoneID, Name: name, PriceMinor: priceMinor,
		MinSubtotalMinor: min, MaxSubtotalMinor: max,
		GroupID: NullableID{Present: true, Value: &groupID},
	})
	if err != nil {
		t.Fatalf("create group rate %q: %v", name, err)
	}
	return r
}

// quoteNames is what a cart quoted as this address would be offered.
func quoteNames(t *testing.T, app *App, email, country, state string) []string {
	t.Helper()
	quotes, err := app.Shipping().Quote(context.Background(), ShippingQuery{
		Country: country, State: state, SubtotalMinor: 10000, Email: email,
	})
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	names := []string{}
	for _, q := range quotes {
		names = append(names, q.Name)
	}
	return names
}

func sameNames(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// verifiedCart is a basket holding one 10000 line, vouched for as email the
// way an operator or a signed-in account would vouch for it.
func verifiedCart(t *testing.T, app *App, variantID int64, email string) *Cart {
	t.Helper()
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, variantID, 1)
	if email != "" {
		if _, err := app.Cart().VerifyEmail(context.Background(), cart.Token, email); err != nil {
			t.Fatalf("verify: %v", err)
		}
	}
	return cart
}

func checkoutStatus(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return 0
}

// The rule itself. Inside a zone where the dealers have a rate, a dealer is
// offered the dealers' rates and nothing else — no consumer express beside the
// negotiated freight — and where they have none, exactly what anybody is.
func TestGroupRatesReplaceThePublicOnesWhereTheGroupHasAny(t *testing.T) {
	app := newTestApp(t)
	dealers := groupOf(t, app, "dealers", false, "dealer@example.com")

	us := zone(t, app, "US", []string{"US"}, nil)
	rate(t, app, us.ID, "Standard", 4900, 0, nil)
	rate(t, app, us.ID, "Express", 19900, 0, nil)
	freight := groupRate(t, app, us.ID, dealers.ID, "Dealer freight", 1000, 0, nil)
	if freight.GroupID == nil || *freight.GroupID != dealers.ID || freight.GroupCode != "dealers" {
		t.Errorf("rate = %+v, want it to name the dealers", freight)
	}

	india := zone(t, app, "India", []string{"IN"}, nil)
	rate(t, app, india.ID, "Standard", 2000, 0, nil)

	if got := quoteNames(t, app, "dealer@example.com", "US", ""); !sameNames(got, "Dealer freight") {
		t.Errorf("a dealer in the US is offered %v, want only the dealers' freight", got)
	}
	if got := quoteNames(t, app, "DEALER@example.com", "IN", ""); !sameNames(got, "Standard") {
		t.Errorf("a dealer in India is offered %v, want the public Standard: the group has nothing there", got)
	}
	if got := quoteNames(t, app, "", "US", ""); !sameNames(got, "Standard", "Express") {
		t.Errorf("anybody in the US is offered %v, want Standard and Express and never the dealers' rate", got)
	}
	if got := quoteNames(t, app, "someone@example.com", "US", ""); !sameNames(got, "Standard", "Express") {
		t.Errorf("an address in no group is offered %v, want the public rates", got)
	}

	// Checkout with no choice takes the dealers' first, and a public rate the
	// dealer was not offered is refused like any rate that is not on offer.
	p := simpleProduct(t, app, "GRP-SHIP-1", 10000, 10)
	cart := verifiedCart(t, app, p.DefaultVariant().ID, "dealer@example.com")
	in := checkoutInput(cart.Token)
	res, err := app.Order().Checkout(context.Background(), CodeCOD, in, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if res.Order.Shipping.AmountMinor != 1000 || res.Order.ShippingMethod != "Dealer freight" {
		t.Errorf("order shipping = %d by %q, want 1000 by Dealer freight",
			res.Order.Shipping.AmountMinor, res.Order.ShippingMethod)
	}

	express := int64(0)
	zones, rates, err := app.Shipping().Zones(context.Background())
	if err != nil || len(zones) != 2 {
		t.Fatalf("zones: %v (%d)", err, len(zones))
	}
	for _, r := range rates[us.ID] {
		if r.Name == "Express" {
			express = r.ID
		}
	}
	other := verifiedCart(t, app, p.DefaultVariant().ID, "dealer@example.com")
	in = checkoutInput(other.Token)
	in.ShippingRateID = &express
	if _, err := app.Order().Checkout(context.Background(), CodeCOD, in, ""); checkoutStatus(err) != http.StatusConflict {
		t.Errorf("a dealer choosing public Express = %v, want 409: it is not on offer to them", err)
	}
}

// A group rate is an exception written into the zones a store already has, so
// a dealers' rate on the catch-all still reaches a dealer in the one state with
// a courier of its own for everybody else. And a zone written only for a group
// is not there for anybody else — it must not beat the country's zone for a
// consumer and offer them nothing.
func TestAGroupRateFindsTheMostSpecificZoneWhereTheGroupHasOne(t *testing.T) {
	app := newTestApp(t)
	dealers := groupOf(t, app, "dealers", false, "dealer@example.com")

	anywhere := zone(t, app, "Anywhere", nil, nil)
	rate(t, app, anywhere.ID, "Post", 9900, 0, nil)
	groupRate(t, app, anywhere.ID, dealers.ID, "Dealer freight", 0, 0, nil)

	california := zone(t, app, "California", []string{"US"}, []string{"CA"})
	rate(t, app, california.ID, "Courier", 3000, 0, nil)

	us := zone(t, app, "US", []string{"US"}, nil)
	rate(t, app, us.ID, "Standard", 4900, 0, nil)

	texasDealers := zone(t, app, "Texas dealers", []string{"US"}, []string{"TX"})
	groupRate(t, app, texasDealers.ID, dealers.ID, "Texas pallet", 500, 0, nil)

	if got := quoteNames(t, app, "dealer@example.com", "US", "CA"); !sameNames(got, "Dealer freight") {
		t.Errorf("a dealer in California is offered %v, want the dealers' freight from the catch-all", got)
	}
	if got := quoteNames(t, app, "", "US", "CA"); !sameNames(got, "Courier") {
		t.Errorf("anybody in California is offered %v, want the state's Courier", got)
	}
	if got := quoteNames(t, app, "dealer@example.com", "US", "Texas"); !sameNames(got, "Texas pallet") {
		t.Errorf("a dealer in Texas is offered %v, want the Texas dealers' zone, the most specific", got)
	}
	if got := quoteNames(t, app, "", "US", "TX"); !sameNames(got, "Standard") {
		t.Errorf("anybody in Texas is offered %v, want the country's Standard: a dealers-only zone is not theirs", got)
	}
}

// A band a group's rate does not cover is a rate that does not apply, and the
// group falls back to what anybody is offered rather than to nothing — "dealers
// ship free over 50000" must not strand a dealer's smaller order.
func TestAGroupRateOutsideItsBandLeavesThePublicRates(t *testing.T) {
	app := newTestApp(t)
	dealers := groupOf(t, app, "dealers", false, "dealer@example.com")
	us := zone(t, app, "US", []string{"US"}, nil)
	rate(t, app, us.ID, "Standard", 4900, 0, nil)
	groupRate(t, app, us.ID, dealers.ID, "Dealer free freight", 0, 50000, nil)

	if got := quoteNames(t, app, "dealer@example.com", "US", ""); !sameNames(got, "Standard") {
		t.Errorf("a 10000 basket is offered %v, want Standard: the dealers' band starts at 50000", got)
	}
	big, err := app.Shipping().Quote(context.Background(), ShippingQuery{
		Country: "US", SubtotalMinor: 60000, Email: "dealer@example.com"})
	if err != nil || len(big) != 1 || big[0].Name != "Dealer free freight" {
		t.Errorf("a 60000 basket is offered %+v (%v), want only the dealers' free freight", big, err)
	}
}

// An address in two groups is offered both groups' rates. Two of them naming
// the same method is settled the way two price lists are: the cheaper stands.
func TestAnAddressInSeveralGroupsIsOfferedTheUnion(t *testing.T) {
	app := newTestApp(t)
	both := "buyer@example.com"
	trade := groupOf(t, app, "trade", false, both, "trade@example.com")
	export := groupOf(t, app, "export", false, both)

	us := zone(t, app, "US", []string{"US"}, nil)
	rate(t, app, us.ID, "Standard", 4900, 0, nil)
	groupRate(t, app, us.ID, trade.ID, "Trade van", 1500, 0, nil)
	groupRate(t, app, us.ID, trade.ID, "Pallet", 8000, 0, nil)
	groupRate(t, app, us.ID, export.ID, "Export crate", 2500, 0, nil)
	groupRate(t, app, us.ID, export.ID, "Pallet", 6000, 0, nil)

	quotes, err := app.Shipping().Quote(context.Background(), ShippingQuery{
		Country: "US", SubtotalMinor: 10000, Email: both})
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	got := map[string]int64{}
	for _, q := range quotes {
		if _, twice := got[q.Name]; twice {
			t.Errorf("%q offered twice", q.Name)
		}
		got[q.Name] = q.Price.AmountMinor
	}
	if len(got) != 3 || got["Trade van"] != 1500 || got["Export crate"] != 2500 || got["Pallet"] != 6000 {
		t.Errorf("offered %v, want Trade van 1500, Export crate 2500 and the cheaper Pallet at 6000", got)
	}
	if names := quoteNames(t, app, "trade@example.com", "US", ""); !sameNames(names, "Trade van", "Pallet") {
		t.Errorf("a trade-only address is offered %v, want only trade's rates", names)
	}
}

// The attack D66 closed, against delivery: a stranger types a dealer's address
// onto an anonymous cart and asks for the dealer's freight. Typed is not
// proven — the quote is the public one and the checkout refuses the rate.
func TestATypedAddressIsOfferedOnlyThePublicRates(t *testing.T) {
	app := newTestApp(t)
	dealers := groupOf(t, app, "dealers", false, "dealer@example.com")
	us := zone(t, app, "US", []string{"US"}, nil)
	rate(t, app, us.ID, "Standard", 4900, 0, nil)
	freight := groupRate(t, app, us.ID, dealers.ID, "Dealer freight", 0, 0, nil)

	p := simpleProduct(t, app, "GRP-TYPED-1", 10000, 10)
	cart := verifiedCart(t, app, p.DefaultVariant().ID, "")
	if _, err := app.Cart().SetEmail(context.Background(), cart.Token, "dealer@example.com"); err != nil {
		t.Fatalf("set email: %v", err)
	}

	rec := do(t, app, http.MethodGet, "/api/checkout/rates?cart="+url.QueryEscape(cart.Token)+"&country=US")
	if rec.Code != http.StatusOK {
		t.Fatalf("rates = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Data struct {
			Rates []ShippingQuote `json:"rates"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data.Rates) != 1 || body.Data.Rates[0].Name != "Standard" {
		t.Errorf("a typed dealer address is quoted %+v, want only Standard", body.Data.Rates)
	}

	in := checkoutInput(cart.Token)
	in.Email = "dealer@example.com"
	in.ShippingRateID = &freight.ID
	if _, err := app.Order().Checkout(context.Background(), CodeCOD, in, ""); checkoutStatus(err) != http.StatusConflict {
		t.Errorf("checking out on the dealers' rate with a typed address = %v, want 409", err)
	}

	// Vouched for, the same basket is quoted the dealers' rate over HTTP.
	if _, err := app.Cart().VerifyEmail(context.Background(), cart.Token, "dealer@example.com"); err != nil {
		t.Fatalf("verify: %v", err)
	}
	rec = do(t, app, http.MethodGet, "/api/checkout/rates?cart="+url.QueryEscape(cart.Token)+"&country=US")
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data.Rates) != 1 || body.Data.Rates[0].RateID != freight.ID {
		t.Errorf("a verified dealer is quoted %+v, want the dealers' freight", body.Data.Rates)
	}
}

// A rate is re-judged at checkout against the address as it stands under the
// lock: a cart that was quoted the dealers' freight and has since left the
// group cannot check out on it.
func TestCheckoutRefusesAGroupRateTheCartNoLongerHas(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	dealers := groupOf(t, app, "dealers", false, "dealer@example.com")
	us := zone(t, app, "US", []string{"US"}, nil)
	rate(t, app, us.ID, "Standard", 4900, 0, nil)
	freight := groupRate(t, app, us.ID, dealers.ID, "Dealer freight", 0, 0, nil)

	p := simpleProduct(t, app, "GRP-LOST-1", 10000, 10)
	cart := verifiedCart(t, app, p.DefaultVariant().ID, "dealer@example.com")
	if err := app.Pricing().RemoveMember(ctx, dealers.ID, "dealer@example.com"); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	in := checkoutInput(cart.Token)
	in.ShippingRateID = &freight.ID
	if _, err := app.Order().Checkout(ctx, CodeCOD, in, ""); checkoutStatus(err) != http.StatusConflict {
		t.Errorf("checkout on a group rate after leaving the group = %v, want 409", err)
	}

	// With the choice dropped it goes through at the public rate.
	in.ShippingRateID = nil
	res, err := app.Order().Checkout(ctx, CodeCOD, in, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if res.Order.ShippingMethod != "Standard" {
		t.Errorf("shipping_method = %q, want Standard", res.Order.ShippingMethod)
	}
}

// A store that has priced dealer freight and nothing else has not said where
// it delivers to everybody, so everybody stays on the flat number — the first
// dealer rate must not start refusing every consumer's order.
func TestOnlyGroupRatesLeaveEverybodyElseOnTheFlatNumber(t *testing.T) {
	app := newTestApp(t)
	app.cfg.FlatShippingMinor = 700
	dealers := groupOf(t, app, "dealers", false, "dealer@example.com")
	us := zone(t, app, "US", []string{"US"}, nil)
	groupRate(t, app, us.ID, dealers.ID, "Dealer freight", 100, 0, nil)

	p := simpleProduct(t, app, "GRP-FLAT-1", 10000, 10)
	ctx := context.Background()

	anon := verifiedCart(t, app, p.DefaultVariant().ID, "")
	res, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(anon.Token), "")
	if err != nil {
		t.Fatalf("anonymous checkout: %v", err)
	}
	if res.Order.Shipping.AmountMinor != 700 || res.Order.ShippingMethod != "" {
		t.Errorf("anybody paid %d by %q, want the flat 700 and no method",
			res.Order.Shipping.AmountMinor, res.Order.ShippingMethod)
	}

	dealer := verifiedCart(t, app, p.DefaultVariant().ID, "dealer@example.com")
	res, err = app.Order().Checkout(ctx, CodeCOD, checkoutInput(dealer.Token), "")
	if err != nil {
		t.Fatalf("dealer checkout: %v", err)
	}
	if res.Order.Shipping.AmountMinor != 100 || res.Order.ShippingMethod != "Dealer freight" {
		t.Errorf("the dealer paid %d by %q, want 100 by Dealer freight",
			res.Order.Shipping.AmountMinor, res.Order.ShippingMethod)
	}

	// Outside the dealers' zone the dealer is on the flat number too.
	dealer = verifiedCart(t, app, p.DefaultVariant().ID, "dealer@example.com")
	in := checkoutInput(dealer.Token)
	in.Address.Country = "IN"
	res, err = app.Order().Checkout(ctx, CodeCOD, in, "")
	if err != nil {
		t.Fatalf("dealer checkout abroad: %v", err)
	}
	if res.Order.Shipping.AmountMinor != 700 {
		t.Errorf("the dealer abroad paid %d, want the flat 700", res.Order.Shipping.AmountMinor)
	}
}

// What an operator types: overlapping bands are refused only between rates
// offered to the same people, an unknown group is a 400, and an update that
// does not mention the group leaves it alone — an old client editing a price
// must not hand the dealers' free freight to everybody.
func TestAGroupRateIsValidatedAgainstItsOwnAudience(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	dealers := groupOf(t, app, "dealers", false)
	us := zone(t, app, "US", []string{"US"}, nil)
	rate(t, app, us.ID, "Standard", 4900, 0, nil)

	dealerStd := groupRate(t, app, us.ID, dealers.ID, "Standard", 1000, 0, nil)
	if _, err := app.Shipping().CreateRate(ctx, ShippingRateInput{
		ZoneID: us.ID, Name: "standard", PriceMinor: 900,
		GroupID: NullableID{Present: true, Value: &dealers.ID},
	}); checkoutStatus(err) != http.StatusBadRequest {
		t.Errorf("a second dealers' Standard over the same band = %v, want 400", err)
	}
	missing := int64(987654)
	if _, err := app.Shipping().CreateRate(ctx, ShippingRateInput{
		ZoneID: us.ID, Name: "Ghost", PriceMinor: 900,
		GroupID: NullableID{Present: true, Value: &missing},
	}); checkoutStatus(err) != http.StatusBadRequest || !strings.Contains(err.Error(), "customer group") {
		t.Errorf("a rate for a group that does not exist = %v, want 400 naming the group", err)
	}

	// Over HTTP, a PATCH that leaves group_id out keeps the audience.
	rec := doBody(t, app, http.MethodPatch, "/api/admin/shipping/rates/"+itoa(dealerStd.ID),
		`{"zone_id":`+itoa(us.ID)+`,"name":"Standard","price_minor":1200}`, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rec.Code, rec.Body)
	}
	var patched struct {
		Data ShippingRate `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &patched); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if patched.Data.GroupID == nil || *patched.Data.GroupID != dealers.ID || patched.Data.Price.AmountMinor != 1200 {
		t.Errorf("after a patch without group_id: %+v, want the dealers' rate at 1200", patched.Data)
	}

	// Null says everybody, and then it collides with the public Standard.
	rec = doBody(t, app, http.MethodPatch, "/api/admin/shipping/rates/"+itoa(dealerStd.ID),
		`{"zone_id":`+itoa(us.ID)+`,"name":"Standard","price_minor":1200,"group_id":null}`, withAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("making the dealers' Standard everybody's = %d, want 400 for the overlap: %s", rec.Code, rec.Body)
	}
	rec = doBody(t, app, http.MethodPatch, "/api/admin/shipping/rates/"+itoa(dealerStd.ID),
		`{"zone_id":`+itoa(us.ID)+`,"name":"Trade","price_minor":1200,"group_id":null}`, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch to everybody = %d: %s", rec.Code, rec.Body)
	}
	var everybody struct {
		Data ShippingRate `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &everybody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if everybody.Data.GroupID != nil || everybody.Data.GroupCode != "" {
		t.Errorf("after group_id null: %+v, want a rate for everybody", everybody.Data)
	}

	// The zone listing names the group on every group rate.
	groupRate(t, app, us.ID, dealers.ID, "Dealer van", 300, 0, nil)
	_, rates, err := app.Shipping().Zones(ctx)
	if err != nil {
		t.Fatalf("zones: %v", err)
	}
	named := 0
	for _, r := range rates[us.ID] {
		if r.GroupID != nil && r.GroupName == "Dealers" {
			named++
		}
	}
	if named != 1 {
		t.Errorf("%d rates name the Dealers group, want 1", named)
	}

	// Deleting the group takes its rates with it; they never fall to everybody.
	if err := app.Pricing().DeleteGroup(ctx, dealers.ID); err != nil {
		t.Fatalf("delete group: %v", err)
	}
	if got := quoteNames(t, app, "", "US", ""); !sameNames(got, "Trade", "Standard") {
		t.Errorf("after the group went, anybody is offered %v, want Trade and Standard only", got)
	}
	_, rates, err = app.Shipping().Zones(ctx)
	if err != nil {
		t.Fatalf("zones: %v", err)
	}
	if len(rates[us.ID]) != 2 {
		t.Errorf("%d rates left in the zone, want the two that were everybody's", len(rates[us.ID]))
	}
}

// ------------------------------------------------------------------ tax

// exemptStore is one 10000 product, GST at 18% on India, and an exempt group
// with one member.
func exemptStore(t *testing.T, app *App, sku string, price int64) (int64, *CustomerGroup) {
	t.Helper()
	p := simpleProduct(t, app, sku, price, 20)
	newTaxRate(t, app, TaxRateInput{Name: "GST 18%", RateBP: 1800, Country: "IN"})
	g := groupOf(t, app, "resellers", true, "reseller@example.com")
	return p.DefaultVariant().ID, g
}

func checkoutIndia(t *testing.T, app *App, variantID int64, email string) *Order {
	t.Helper()
	cart := verifiedCart(t, app, variantID, email)
	in := checkoutInput(cart.Token)
	in.Address.Country = "IN"
	in.Address.State = "KA"
	res, err := app.Order().Checkout(context.Background(), CodeCOD, in, "")
	if err != nil {
		t.Fatalf("checkout as %q: %v", email, err)
	}
	return res.Order
}

// Exclusive prices: an exempt buyer's order adds no tax to any line, and says
// which group it was sold through. Everybody else is taxed as before.
func TestAnExemptGroupPaysNoTaxOnExclusivePrices(t *testing.T) {
	app := newTestApp(t)
	variant, g := exemptStore(t, app, "EXEMPT-EX-1", 10000)

	taxed := checkoutIndia(t, app, variant, "")
	if taxed.Tax.AmountMinor != 1800 || taxed.TaxExemption != nil {
		t.Fatalf("anybody: tax %d, exemption %+v; want 1800 and none", taxed.Tax.AmountMinor, taxed.TaxExemption)
	}

	exempt := checkoutIndia(t, app, variant, "Reseller@Example.com")
	if exempt.Tax.AmountMinor != 0 {
		t.Errorf("exempt tax = %d, want 0", exempt.Tax.AmountMinor)
	}
	if exempt.Total.AmountMinor != exempt.Subtotal.AmountMinor+exempt.Shipping.AmountMinor {
		t.Errorf("exempt total = %d, want subtotal plus shipping and nothing else", exempt.Total.AmountMinor)
	}
	for _, l := range exempt.Lines {
		if l.Tax.AmountMinor != 0 || l.Tax.RateBP != 0 || l.Tax.Name != "" {
			t.Errorf("an exempt line carries %+v, want no tax at all", l.Tax)
		}
	}
	if e := exempt.TaxExemption; e == nil || e.GroupID == nil || *e.GroupID != g.ID ||
		e.GroupCode != "resellers" || e.GroupName != "Resellers" {
		t.Errorf("exemption = %+v, want the Resellers group", exempt.TaxExemption)
	}

	// A typed address is not proof, for tax as for prices.
	cart := verifiedCart(t, app, variant, "")
	if _, err := app.Cart().SetEmail(context.Background(), cart.Token, "reseller@example.com"); err != nil {
		t.Fatalf("set email: %v", err)
	}
	in := checkoutInput(cart.Token)
	in.Email = "reseller@example.com"
	in.Address.Country = "IN"
	res, err := app.Order().Checkout(context.Background(), CodeCOD, in, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if res.Order.Tax.AmountMinor != 1800 || res.Order.TaxExemption != nil {
		t.Errorf("a typed reseller address: tax %d, exemption %+v; want 1800 and none",
			res.Order.Tax.AmountMinor, res.Order.TaxExemption)
	}

	// A group that is not exempt exempts nobody.
	groupOf(t, app, "trade", false, "trader@example.com")
	if o := checkoutIndia(t, app, variant, "trader@example.com"); o.Tax.AmountMinor != 1800 {
		t.Errorf("a member of a group that pays tax was charged %d, want 1800", o.Tax.AmountMinor)
	}
}

// Inclusive prices: the price the cart showed is the price charged, and none
// of it is recorded as tax. The exempt buyer pays the shelf price, the store
// keeps all of it as revenue, and the order says why it carries no tax. A
// lower price for exempt buyers is a price list on the same group.
func TestAnExemptGroupPaysTheShelfPriceWithNoTaxInItOnInclusivePrices(t *testing.T) {
	app := newTaxApp(t, func(c *Config) { c.PricesIncludeTax = true })
	variant, _ := exemptStore(t, app, "EXEMPT-IN-1", 11800)

	taxed := checkoutIndia(t, app, variant, "")
	if taxed.Tax.AmountMinor != 1800 || taxed.Total.AmountMinor != 11800 {
		t.Fatalf("anybody: tax %d total %d; want 1800 inside 11800", taxed.Tax.AmountMinor, taxed.Total.AmountMinor)
	}

	exempt := checkoutIndia(t, app, variant, "reseller@example.com")
	if !exempt.TaxInclusive {
		t.Error("tax_inclusive is not set on an inclusive store's exempt order")
	}
	if exempt.Tax.AmountMinor != 0 {
		t.Errorf("exempt tax = %d, want 0: none of the price is tax", exempt.Tax.AmountMinor)
	}
	if exempt.Subtotal.AmountMinor != 11800 || exempt.Total.AmountMinor != 11800+exempt.Shipping.AmountMinor {
		t.Errorf("exempt subtotal %d total %d, want the shelf price 11800 unchanged",
			exempt.Subtotal.AmountMinor, exempt.Total.AmountMinor)
	}
	if exempt.TaxExemption == nil || exempt.TaxExemption.GroupCode != "resellers" {
		t.Errorf("exemption = %+v, want the resellers", exempt.TaxExemption)
	}
}

// The order is a record of the sale. Renaming the group, making it pay tax
// again and deleting it all happen after; none of them reaches the order.
func TestTheExemptionOnAnOrderOutlivesItsGroup(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	variant, g := exemptStore(t, app, "EXEMPT-SNAP-1", 10000)
	order := checkoutIndia(t, app, variant, "reseller@example.com")

	newName, payTax := "Former resellers", false
	if _, err := app.Pricing().UpdateGroup(ctx, g.ID, CustomerGroupPatch{Name: &newName, TaxExempt: &payTax}); err != nil {
		t.Fatalf("update group: %v", err)
	}
	after, err := app.Order().Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Tax.AmountMinor != 0 || after.TaxExemption == nil || after.TaxExemption.GroupName != "Resellers" {
		t.Errorf("after the group changed: tax %d, exemption %+v; want 0 and the name it was sold under",
			after.Tax.AmountMinor, after.TaxExemption)
	}
	// And a new order is taxed, because the group no longer exempts anybody.
	if o := checkoutIndia(t, app, variant, "reseller@example.com"); o.Tax.AmountMinor != 1800 || o.TaxExemption != nil {
		t.Errorf("after the exemption ended: tax %d, exemption %+v; want 1800 and none",
			o.Tax.AmountMinor, o.TaxExemption)
	}

	if err := app.Pricing().DeleteGroup(ctx, g.ID); err != nil {
		t.Fatalf("delete group: %v", err)
	}
	after, err = app.Order().Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if e := after.TaxExemption; e == nil || e.GroupID != nil || e.GroupCode != "resellers" || e.GroupName != "Resellers" {
		t.Errorf("after the group was deleted: %+v, want the snapshot without the way back", after.TaxExemption)
	}

	// The shopper's own copy says why there was no tax, without the store's
	// handle on the group.
	var token string
	if err := app.DB().QueryRowContext(ctx,
		`SELECT access_token FROM orders WHERE id = $1`, order.ID).Scan(&token); err != nil {
		t.Fatalf("token: %v", err)
	}
	rec := do(t, app, http.MethodGet, "/api/orders/"+order.Number+"?token="+url.QueryEscape(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("guest read = %d: %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"group_name":"Resellers"`) || strings.Contains(body, `"group_id"`) {
		t.Errorf("guest copy = %s, want the group's name and not its id", body)
	}
}

// The phone order is the same checkout: the operator typed the address, which
// is the proof (D66), so a dealer rung through by hand is offered the dealers'
// freight, refused a rate they are not offered, and charged no tax when the
// group is exempt.
func TestAnOperatorPlacedOrderGetsTheGroupsShippingAndExemption(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	variant, g := exemptStore(t, app, "EXEMPT-PHONE-1", 10000)
	if err := app.Pricing().AddMember(ctx, g.ID, "dealer@example.com"); err != nil {
		t.Fatalf("member: %v", err)
	}
	india := zone(t, app, "India", []string{"IN"}, nil)
	express := rate(t, app, india.ID, "Express", 9000, 0, nil)
	rate(t, app, india.ID, "Standard", 3000, 0, nil)
	freight := groupRate(t, app, india.ID, g.ID, "Dealer freight", 500, 0, nil)

	place := func(email string, rateID *int64) (*CheckoutResult, error) {
		return app.Order().Create(ctx, NewOrderInput{
			Email: email, Name: "A Customer", ShippingRateID: rateID,
			Address: Address{Line1: "1 MG Road", City: "Bengaluru", PostalCode: "560001",
				Country: "IN", State: "KA"},
			Lines: []NewOrderLine{{VariantID: variant, Quantity: 1}},
		})
	}

	res, err := place("dealer@example.com", nil)
	if err != nil {
		t.Fatalf("dealer phone order: %v", err)
	}
	o := res.Order
	if o.ShippingMethod != "Dealer freight" || o.Shipping.AmountMinor != 500 {
		t.Errorf("dealer phone order shipped %q at %d, want Dealer freight at 500", o.ShippingMethod, o.Shipping.AmountMinor)
	}
	if o.Tax.AmountMinor != 0 || o.TaxExemption == nil || o.TaxExemption.GroupID == nil || *o.TaxExemption.GroupID != g.ID {
		t.Errorf("dealer phone order: tax %d, exemption %+v; want 0 through the group", o.Tax.AmountMinor, o.TaxExemption)
	}

	if _, err := place("dealer@example.com", &express.ID); checkoutStatus(err) != http.StatusConflict {
		t.Errorf("a dealer phone order on public Express = %v, want 409", err)
	}
	if _, err := place("walkin@example.com", &freight.ID); checkoutStatus(err) != http.StatusConflict {
		t.Errorf("a walk-in phone order on the dealers' freight = %v, want 409", err)
	}

	res, err = place("walkin@example.com", &express.ID)
	if err != nil {
		t.Fatalf("walk-in phone order: %v", err)
	}
	if res.Order.ShippingMethod != "Express" || res.Order.Tax.AmountMinor != 1800 || res.Order.TaxExemption != nil {
		t.Errorf("walk-in: %q, tax %d, exemption %+v; want Express, 1800 and none",
			res.Order.ShippingMethod, res.Order.Tax.AmountMinor, res.Order.TaxExemption)
	}
}

// Deciding that a group pays no tax is a tax decision: it needs taxes.write on
// top of groups.write. A manager holds groups.write and not taxes.write by
// default, so they can rename a group and cannot exempt it. Membership stays
// groups.write, which is the D66 trade-off said out loud: putting an address
// into an exempt group exempts it.
func TestExemptingAGroupNeedsTheTaxRight(t *testing.T) {
	app := newTestApp(t)
	manager := signInAs(t, app, "manager@example.com", RoleManager)
	owner := signInAs(t, app, "owner@example.com", RoleOwner)
	g := groupOf(t, app, "charities", false)
	path := "/api/admin/customer-groups/" + itoa(g.ID)

	if rec := doBody(t, app, http.MethodPatch, path, `{"tax_exempt":true}`, bearer(manager)); rec.Code != http.StatusForbidden {
		t.Errorf("manager exempting = %d, want 403: %s", rec.Code, rec.Body)
	}
	if rec := doBody(t, app, http.MethodPost, "/api/admin/customer-groups",
		`{"code":"schools","name":"Schools","tax_exempt":true}`, bearer(manager)); rec.Code != http.StatusForbidden {
		t.Errorf("manager creating an exempt group = %d, want 403: %s", rec.Code, rec.Body)
	}
	// Sending back the value the group already has is not a change.
	if rec := doBody(t, app, http.MethodPatch, path, `{"name":"Charities UK","tax_exempt":false}`,
		bearer(manager)); rec.Code != http.StatusOK {
		t.Errorf("manager renaming = %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := doBody(t, app, http.MethodPost, path+"/members", `{"email":"giver@example.com"}`,
		bearer(manager)); rec.Code != http.StatusOK && rec.Code != http.StatusCreated && rec.Code != http.StatusNoContent {
		t.Errorf("manager adding a member = %d, want it allowed under groups.write: %s", rec.Code, rec.Body)
	}

	rec := doBody(t, app, http.MethodPatch, path, `{"tax_exempt":true}`, bearer(owner))
	if rec.Code != http.StatusOK {
		t.Fatalf("owner exempting = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Data CustomerGroup `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Data.TaxExempt {
		t.Errorf("group = %+v, want tax_exempt", got.Data)
	}

	// The decision is on the audit trail with who made it, once — a repeat of
	// the same value writes nothing.
	if rec := doBody(t, app, http.MethodPatch, path, `{"tax_exempt":true}`, bearer(owner)); rec.Code != http.StatusOK {
		t.Fatalf("owner repeating = %d: %s", rec.Code, rec.Body)
	}
	rows := auditRows(t, app, "action = $1", AuditCustomerGroupTaxExemption)
	if len(rows) != 1 {
		t.Fatalf("%d audit rows, want 1", len(rows))
	}
	if rows[0].ActorEmail != "owner@example.com" || rows[0].EntityType != AuditEntityCustomerGroup ||
		rows[0].EntityID != itoa(g.ID) {
		t.Errorf("audit row = %+v, want the owner exempting the group", rows[0])
	}
}
