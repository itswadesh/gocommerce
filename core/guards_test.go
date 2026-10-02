package gocommerce

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// guardModule registers one checkout guard and remembers what it was shown.
type guardModule struct {
	name    string
	guard   CheckoutGuard
	attempt *CheckoutAttempt
}

func (m *guardModule) Name() string            { return m.name }
func (m *guardModule) Migrations() []Migration { return nil }
func (m *guardModule) Register(app *App) error {
	app.RegisterCheckoutGuard(func(ctx context.Context, a *CheckoutAttempt) error {
		copied := *a
		m.attempt = &copied
		if m.guard == nil {
			return nil
		}
		return m.guard(ctx, a)
	})
	return nil
}

// A refusal leaves nothing behind: no order, no reservation, and no order
// number drawn — the next order placed is still the first.
func TestACheckoutGuardRefusesBeforeTheOrderExists(t *testing.T) {
	refuse := true
	mod := &guardModule{name: "limit", guard: func(ctx context.Context, a *CheckoutAttempt) error {
		if refuse {
			return Forbiddenf("over the credit limit")
		}
		return nil
	}}
	app := newTestApp(t, mod)
	ctx := context.Background()

	product := simpleProduct(t, app, "GUARD-1", 2500, 10)
	variant := product.DefaultVariant().ID
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, variant, 2)

	_, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden ||
		!strings.Contains(apiErr.Message, "credit limit") {
		t.Fatalf("checkout = %v, want the guard's own 403", err)
	}
	if _, reserved := variantStock(t, app, variant); reserved != 0 {
		t.Errorf("reserved = %d after a refused checkout, want 0", reserved)
	}
	if _, total, err := app.Order().List(ctx, OrderQuery{Limit: 10}); err != nil || total != 0 {
		t.Errorf("orders = %d (%v) after a refused checkout, want none", total, err)
	}

	// The same cart goes through once the guard allows it, and takes the first
	// number: the refusal drew none.
	refuse = false
	result, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout after allowing: %v", err)
	}
	if !strings.HasSuffix(result.Order.Number, "000001") {
		t.Errorf("number = %q, want the first one: a refusal must not burn a number", result.Order.Number)
	}
}

// What a guard is shown is the order it is judging, figure for figure.
func TestACheckoutGuardSeesTheFinalFigures(t *testing.T) {
	mod := &guardModule{name: "watch"}
	app := newTestApp(t, mod)
	ctx := context.Background()

	product := simpleProduct(t, app, "GUARD-2", 2500, 10)
	variant := product.DefaultVariant().ID
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, variant, 3)
	if _, err := app.Cart().VerifyEmail(ctx, cart.Token, "buyer@example.com"); err != nil {
		t.Fatalf("verify: %v", err)
	}

	in := checkoutInput(cart.Token)
	in.Metadata = Metadata{"po_number": "PO-17"}
	result, err := app.Order().Checkout(ctx, CodeCOD, in, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	a := mod.attempt
	if a == nil {
		t.Fatal("the guard was never called")
	}
	if a.Method != CodeCOD || a.VerifiedEmail != "buyer@example.com" || a.ByOperator {
		t.Errorf("attempt = method %q verified %q by-operator %v; want cod, buyer@example.com, false",
			a.Method, a.VerifiedEmail, a.ByOperator)
	}
	if a.Total.AmountMinor != result.Order.Total.AmountMinor || a.Subtotal.AmountMinor != 7500 {
		t.Errorf("attempt total %d subtotal %d, want the order's %d and 7500",
			a.Total.AmountMinor, a.Subtotal.AmountMinor, result.Order.Total.AmountMinor)
	}
	if len(a.Lines) != 1 || a.Lines[0].Quantity != 3 || a.Lines[0].UnitPrice.AmountMinor != 2500 || a.Lines[0].Agreed {
		t.Errorf("attempt lines = %+v, want one line of 3 at 2500, not agreed", a.Lines)
	}
	if a.Input.Metadata["po_number"] != "PO-17" {
		t.Errorf("attempt metadata = %v, want the caller's po_number", a.Input.Metadata)
	}
	if a.Tx == nil {
		t.Error("the guard was given no transaction to read and lock with")
	}
}

// A guard that breaks is not a guard that refused: the shopper gets a 500 and
// none of the guard's internals.
func TestABrokenCheckoutGuardIsAnInternalError(t *testing.T) {
	mod := &guardModule{name: "broken", guard: func(ctx context.Context, a *CheckoutAttempt) error {
		return errors.New("dial tcp: connection refused to secret-host:5432")
	}}
	app := newTestApp(t, mod)

	product := simpleProduct(t, app, "GUARD-3", 1000, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)

	_, err := app.Order().Checkout(context.Background(), CodeCOD, checkoutInput(cart.Token), "")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
		t.Fatalf("checkout = %v, want a 500", err)
	}
	if strings.Contains(apiErr.Message, "secret-host") {
		t.Errorf("the message %q leaks the guard's own error", apiErr.Message)
	}
}

// An agreed price holds over the price lists and over quantity, and a guard
// is told which lines carry one and that an operator placed the order.
func TestAnAgreedPriceHolds(t *testing.T) {
	mod := &guardModule{name: "watch"}
	app := newTestApp(t, mod)
	ctx := context.Background()

	product := simpleProduct(t, app, "AGREED-1", 10000, 50)
	variant := product.DefaultVariant().ID
	other := simpleProduct(t, app, "AGREED-2", 3000, 50).DefaultVariant().ID

	// A list the customer is in, with a break the quantity crosses: neither
	// may move an agreed price.
	g, err := app.Pricing().CreateGroup(ctx, CustomerGroupInput{Code: "agreed", Name: "Agreed"})
	if err != nil {
		t.Fatalf("group: %v", err)
	}
	if err := app.Pricing().AddMember(ctx, g.ID, "buyer@example.com"); err != nil {
		t.Fatalf("member: %v", err)
	}
	list, err := app.Pricing().CreateList(ctx, PriceListInput{Name: "Trade", GroupID: &g.ID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, row := range []PriceRow{
		{VariantID: variant, MinQuantity: 1, AmountMinor: 9000},
		{VariantID: variant, MinQuantity: 5, AmountMinor: 8000},
	} {
		if err := app.Pricing().SetPrice(ctx, list.ID, row); err != nil {
			t.Fatalf("price: %v", err)
		}
	}

	agreed := int64(7250)
	result, err := app.Order().Create(ctx, NewOrderInput{
		Email: "buyer@example.com", Name: "A Buyer",
		Address: Address{Line1: "1 Trade Way", City: "Testville", PostalCode: "12345", Country: "US"},
		Lines: []NewOrderLine{
			{VariantID: variant, Quantity: 6, UnitPriceMinor: &agreed},
			{VariantID: other, Quantity: 1},
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	prices := map[string]int64{}
	for _, l := range result.Order.Lines {
		prices[l.SKU] = l.UnitPrice.AmountMinor
	}
	if prices["AGREED-1"] != 7250 {
		t.Errorf("agreed line = %d, want 7250 over both the list and its break", prices["AGREED-1"])
	}
	if prices["AGREED-2"] != 3000 {
		t.Errorf("unagreed line = %d, want its catalogue 3000", prices["AGREED-2"])
	}

	a := mod.attempt
	if a == nil || !a.ByOperator {
		t.Fatalf("attempt = %+v, want ByOperator for Orders.Create", a)
	}
	for _, l := range a.Lines {
		if (l.SKU == "AGREED-1") != l.Agreed {
			t.Errorf("line %s agreed = %v", l.SKU, l.Agreed)
		}
	}

	// One variant at two prices is refused rather than guessed at.
	low, high := int64(1), int64(2)
	_, err = app.Order().Create(ctx, NewOrderInput{
		Email: "buyer@example.com", Name: "A Buyer",
		Address: Address{Line1: "1 Trade Way", City: "Testville", PostalCode: "12345", Country: "US"},
		Lines: []NewOrderLine{
			{VariantID: other, Quantity: 1, UnitPriceMinor: &low},
			{VariantID: other, Quantity: 1, UnitPriceMinor: &high},
		},
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Errorf("two prices for one variant = %v, want a 400", err)
	}
}

// Over HTTP an agreed price needs pricing.write on top of orders.write: staff
// may take a phone order, and may not set its price.
func TestAnAgreedPriceNeedsPricingWrite(t *testing.T) {
	app := newTestApp(t)
	product := simpleProduct(t, app, "AGREED-3", 5000, 10)
	body := `{"email":"buyer@example.com","name":"A Buyer",
		"address":{"line1":"1 Trade Way","city":"Testville","postal_code":"12345","country":"US"},
		"lines":[{"variant_id":` + itoa(product.DefaultVariant().ID) + `,"quantity":1,"unit_price_minor":100}]}`

	staff := signInAs(t, app, "staff@example.com", RoleStaff)
	if rec := doBody(t, app, http.MethodPost, "/api/admin/orders", body, bearer(staff)); rec.Code != http.StatusForbidden {
		t.Errorf("staff = %d, want 403: %s", rec.Code, rec.Body)
	}
	manager := signInAs(t, app, "manager@example.com", RoleManager)
	rec := doBody(t, app, http.MethodPost, "/api/admin/orders", body, bearer(manager))
	if rec.Code != http.StatusCreated {
		t.Fatalf("manager = %d, want 201: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"amount_minor":100`) {
		t.Errorf("the order does not carry the agreed price: %s", rec.Body)
	}

	// Without a price, staff may still take the order.
	plain := strings.Replace(body, `,"unit_price_minor":100`, "", 1)
	if rec := doBody(t, app, http.MethodPost, "/api/admin/orders", plain, bearer(staff)); rec.Code != http.StatusCreated {
		t.Errorf("staff without a price = %d, want 201: %s", rec.Code, rec.Body)
	}
}

func TestANilCheckoutGuardFailsStartup(t *testing.T) {
	dsn := requireDB(t)
	resetSchema(t, dsn)
	mod := &nilGuardModule{}
	if _, err := New(testConfig(dsn), mod); err == nil {
		t.Fatal("New accepted a nil checkout guard")
	}
}

type nilGuardModule struct{}

func (nilGuardModule) Name() string            { return "nilguard" }
func (nilGuardModule) Migrations() []Migration { return nil }
func (nilGuardModule) Register(app *App) error {
	app.RegisterCheckoutGuard(nil)
	return nil
}
