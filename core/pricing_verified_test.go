package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// A group price is owed to a proven address, not to a typed one (D66).
//
// Group membership is a list of email addresses, and the cart used to be
// priced as whatever address its token holder had typed onto it. Anybody who
// knew a dealer's address could put it on an anonymous cart, see the dealer's
// prices come back, and check out at them under their own address. These tests
// are that attack, run against every door it came through.

// tradeStore is one product at 10000 and a group whose member pays 6000.
func tradeStore(t *testing.T) (*App, int64) {
	t.Helper()
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "VERIFY-1", 10000, 50)
	variant := product.DefaultVariant().ID

	g, err := app.Pricing().CreateGroup(ctx, CustomerGroupInput{Code: "dealers", Name: "Dealers"})
	if err != nil {
		t.Fatalf("group: %v", err)
	}
	if err := app.Pricing().AddMember(ctx, g.ID, "dealer@example.com"); err != nil {
		t.Fatalf("member: %v", err)
	}
	list, err := app.Pricing().CreateList(ctx, PriceListInput{Name: "Dealer", GroupID: &g.ID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := app.Pricing().SetPrice(ctx, list.ID,
		PriceRow{VariantID: variant, MinQuantity: 1, AmountMinor: 6000}); err != nil {
		t.Fatalf("price: %v", err)
	}
	return app, variant
}

func decodeCart(t *testing.T, body []byte) Cart {
	t.Helper()
	var out struct {
		Data Cart `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode cart: %v: %s", err, body)
	}
	return out.Data
}

// The attack, over the public API: a stranger names a dealer's address on
// both doors that take one, and is charged the catalogue price throughout —
// including the current_price, which used to answer "is this address a
// member?" for anybody who asked.
func TestATypedAddressEarnsNoGroupPrice(t *testing.T) {
	app, variant := tradeStore(t)

	rec := doBody(t, app, http.MethodPost, "/api/carts", `{"email":"dealer@example.com"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create cart = %d: %s", rec.Code, rec.Body)
	}
	cart := decodeCart(t, rec.Body.Bytes())
	if cart.VerifiedEmail != "" {
		t.Fatalf("a cart created with a typed address came back verified as %q", cart.VerifiedEmail)
	}

	rec = doBody(t, app, http.MethodPost, "/api/carts/"+cart.Token+"/line-items",
		`{"variant_id":`+itoa(variant)+`,"quantity":2}`)
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("add line = %d: %s", rec.Code, rec.Body)
	}
	if rec := doBody(t, app, http.MethodPut, "/api/carts/"+cart.Token+"/email",
		`{"email":"DEALER@example.com"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT email = %d: %s", rec.Code, rec.Body)
	}

	got := decodeCart(t, do(t, app, http.MethodGet, "/api/carts/"+cart.Token).Body.Bytes())
	line := got.Lines[0]
	if line.UnitPrice.AmountMinor != 10000 {
		t.Errorf("unit price = %d, want the catalogue 10000 for a typed address", line.UnitPrice.AmountMinor)
	}
	if line.CurrentPrice.AmountMinor != 10000 {
		t.Errorf("current price = %d — the response is telling a stranger the address is a member",
			line.CurrentPrice.AmountMinor)
	}

	// And checking out under the attacker's own address pays full price.
	in := checkoutInput(cart.Token)
	in.Email = "attacker@example.com"
	result, err := app.Order().Checkout(context.Background(), CodeCOD, in, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if got := result.Order.Lines[0].UnitPrice.AmountMinor; got != 10000 {
		t.Errorf("order line = %d, want 10000: the dealer's price reached a stranger's order", got)
	}
}

// Vouching re-prices the basket on the spot, and typing somebody else's
// address takes the vouching away again — the shared-device case.
func TestAVerifiedCartIsPricedAsTheMember(t *testing.T) {
	app, variant := tradeStore(t)
	ctx := context.Background()

	cart := newCart(t, app)
	addToCart(t, app, cart.Token, variant, 1)

	verified, err := app.Cart().VerifyEmail(ctx, cart.Token, " Dealer@Example.com ")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verified.VerifiedEmail != "dealer@example.com" {
		t.Errorf("verified_email = %q, want the folded address", verified.VerifiedEmail)
	}
	if verified.Email != "dealer@example.com" {
		t.Errorf("email = %q, want the contact address to follow the verified one", verified.Email)
	}
	line := verified.Lines[0]
	if line.UnitPrice.AmountMinor != 6000 || line.PriceChanged {
		t.Errorf("line = %d (changed %v), want re-priced to the member's 6000 at once",
			line.UnitPrice.AmountMinor, line.PriceChanged)
	}

	// The same mailbox in another case keeps it.
	same, err := app.Cart().SetEmail(ctx, cart.Token, "DEALER@example.com")
	if err != nil {
		t.Fatalf("set same email: %v", err)
	}
	if same.VerifiedEmail != "dealer@example.com" {
		t.Errorf("retyping the verified address in another case withdrew the verification")
	}

	// Somebody else's address withdraws it, and the basket goes back to the
	// price anybody pays now rather than at checkout.
	other, err := app.Cart().SetEmail(ctx, cart.Token, "someone@example.com")
	if err != nil {
		t.Fatalf("set other email: %v", err)
	}
	if other.VerifiedEmail != "" {
		t.Errorf("verified_email = %q after a different address was typed, want none", other.VerifiedEmail)
	}
	if got := other.Lines[0].UnitPrice.AmountMinor; got != 10000 {
		t.Errorf("line = %d after the verification was withdrawn, want 10000", got)
	}

	// An empty address withdraws it explicitly.
	if _, err := app.Cart().VerifyEmail(ctx, cart.Token, "dealer@example.com"); err != nil {
		t.Fatalf("re-verify: %v", err)
	}
	cleared, err := app.Cart().VerifyEmail(ctx, cart.Token, "")
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared.VerifiedEmail != "" || cleared.Lines[0].UnitPrice.AmountMinor != 10000 {
		t.Errorf("after clearing: verified %q, line %d; want none and 10000",
			cleared.VerifiedEmail, cleared.Lines[0].UnitPrice.AmountMinor)
	}

	if _, err := app.Cart().VerifyEmail(ctx, cart.Token, "not-an-address"); err == nil {
		t.Error("an address with no @ was accepted")
	}
}

// The operator is the proof: an order placed by hand for a dealer is at the
// dealer's price, as a phone order always was.
func TestAnOperatorPlacedOrderIsPricedAsTheCustomer(t *testing.T) {
	app, variant := tradeStore(t)

	result, err := app.Order().Create(context.Background(), NewOrderInput{
		Email: "dealer@example.com", Name: "A Dealer",
		Address: Address{Line1: "1 Trade Way", City: "Testville", PostalCode: "12345", Country: "US"},
		Lines:   []NewOrderLine{{VariantID: variant, Quantity: 3}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := result.Order.Lines[0].UnitPrice.AmountMinor; got != 6000 {
		t.Errorf("order line = %d, want the dealer's 6000", got)
	}
}

// A storefront that signs its own customers in vouches through the admin API,
// holding the token it already has. The right is groups.write: staff, who may
// read groups but not change them, are refused.
func TestTheAdminRouteVouchesForACart(t *testing.T) {
	app, variant := tradeStore(t)

	cart := newCart(t, app)
	addToCart(t, app, cart.Token, variant, 1)
	body := `{"cart_id":"` + cart.Token + `","email":"dealer@example.com"}`

	staff := signInAs(t, app, "staff@example.com", RoleStaff)
	if rec := doBody(t, app, http.MethodPost, "/api/admin/carts/verify-email", body,
		bearer(staff)); rec.Code != http.StatusForbidden {
		t.Errorf("staff = %d, want 403: vouching for a cart is a group membership in effect", rec.Code)
	}
	if rec := doBody(t, app, http.MethodPost, "/api/admin/carts/verify-email",
		body); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d, want 401", rec.Code)
	}

	rec := doBody(t, app, http.MethodPost, "/api/admin/carts/verify-email", body, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin = %d: %s", rec.Code, rec.Body)
	}
	got := decodeCart(t, rec.Body.Bytes())
	if got.VerifiedEmail != "dealer@example.com" || got.Lines[0].UnitPrice.AmountMinor != 6000 {
		t.Errorf("after vouching: verified %q, line %d; want dealer@example.com at 6000",
			got.VerifiedEmail, got.Lines[0].UnitPrice.AmountMinor)
	}

	if rec := doBody(t, app, http.MethodPost, "/api/admin/carts/verify-email",
		`{"cart_id":"no-such-cart","email":"dealer@example.com"}`, withAdmin); rec.Code != http.StatusNotFound {
		t.Errorf("unknown cart = %d, want 404", rec.Code)
	}
	if rec := doBody(t, app, http.MethodPost, "/api/admin/carts/verify-email",
		`{"email":"dealer@example.com"}`, withAdmin); rec.Code != http.StatusBadRequest {
		t.Errorf("no cart_id = %d, want 400", rec.Code)
	}
}
