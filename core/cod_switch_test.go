package gocommerce

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// Cash on delivery is built in and on, until an owner switches it off from
// Payment methods (D102). Off, no shopper is offered it, no checkout takes it,
// and the store's records still know what it was.

func setCOD(t *testing.T, app *App, on bool) {
	t.Helper()
	rec := do(t, app, http.MethodPatch, "/api/admin/plugins/"+CODPluginKey, withAdmin, jsonBody(t, PluginPatch{Enabled: &on}))
	if rec.Code != http.StatusOK {
		t.Fatalf("switch cash on delivery to %v: %d %s", on, rec.Code, rec.Body)
	}
}

func checkoutMethods(t *testing.T, app *App) (codes []string, named []string) {
	t.Helper()
	rec := do(t, app, http.MethodGet, "/api/checkout")
	var out struct {
		PaymentMethods []string        `json:"payment_methods"`
		Methods        []PaymentMethod `json:"methods"`
	}
	decodeData(t, rec, &out)
	for _, m := range out.Methods {
		named = append(named, m.Code)
	}
	return out.PaymentMethods, named
}

func codInfo(t *testing.T, app *App) ProviderInfo {
	t.Helper()
	for _, p := range app.Settings().PaymentMethods {
		if p.Code == CodeCOD {
			return p
		}
	}
	t.Fatal("cash on delivery is not in the settings")
	return ProviderInfo{}
}

func TestCashOnDeliveryIsOnUntilSwitchedOff(t *testing.T) {
	app := newTestApp(t)
	codes, named := checkoutMethods(t, app)
	if !slices.Contains(codes, CodeCOD) || !slices.Contains(named, CodeCOD) {
		t.Fatalf("a new store offers %v / %v, want cash on delivery in both", codes, named)
	}
	info := codInfo(t, app)
	if !info.Configured || info.Plugin != CODPluginKey {
		t.Fatalf("settings say %+v, want it configured and switched by %q", info, CODPluginKey)
	}
}

func TestCashOnDeliverySwitchedOffIsOfferedNowhere(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	product := simpleProduct(t, app, "COD-OFF-1", 1000, 5)
	setCOD(t, app, false)

	codes, named := checkoutMethods(t, app)
	if slices.Contains(codes, CodeCOD) || slices.Contains(named, CodeCOD) {
		t.Fatalf("switched off, the storefront is still offered %v / %v", codes, named)
	}
	if codInfo(t, app).Configured {
		t.Fatal("switched off, the settings still say cash on delivery is configured")
	}

	// A shopper who asks for it by name gets what any unknown method gets.
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	_, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	var e *APIError
	if !errors.As(err, &e) || e.Status != http.StatusNotFound {
		t.Fatalf("checkout with cash on delivery switched off = %v, want not found", err)
	}

	// An operator's order with no method named used to mean cash on delivery;
	// with it off, they are asked to choose one, not told "cod" does not exist.
	_, err = app.Order().Create(ctx, NewOrderInput{
		Email: "phone@example.com", Name: "Phone Order",
		Address: Address{Line1: "1 Test Street", City: "Testville", PostalCode: "12345", Country: "US"},
		Lines:   []NewOrderLine{{VariantID: product.DefaultVariant().ID, Quantity: 1}},
	})
	if !errors.As(err, &e) || e.Status != http.StatusBadRequest || !strings.Contains(err.Error(), "cash on delivery") {
		t.Fatalf("operator order with no method and cash on delivery off = %v, want a validation error naming it", err)
	}

	// Switched back on, it is offered again: nothing was removed.
	setCOD(t, app, true)
	if codes, _ := checkoutMethods(t, app); !slices.Contains(codes, CodeCOD) {
		t.Fatalf("switched back on, offered %v", codes)
	}
}

// The payouts report names a method by what took the money. A method switched
// off is still in this build, so its old sales are not reported as a method
// that was removed.
func TestPayoutsStillNameCashOnDeliveryWhenItIsSwitchedOff(t *testing.T) {
	app := newTestApp(t)
	placeOn(t, app, "COD-OFF-PAYOUT", CodeCOD)
	setCOD(t, app, false)

	report, err := app.Reports().Payouts(context.Background(), PayoutsQuery{})
	if err != nil {
		t.Fatalf("payouts: %v", err)
	}
	for _, block := range report.Currencies {
		for _, m := range block.Methods {
			if m.Code == CodeCOD {
				if !m.Installed || m.Name != "Cash on delivery" {
					t.Fatalf("cash on delivery in the report = %+v, want it installed and named", m)
				}
				return
			}
		}
	}
	t.Fatal("the report lost the cash on delivery sale")
}

// Switching it is changing a plugin, so it needs plugins.write like any
// gateway's switch, and it is on the audit trail like one.
func TestOnlyPluginsWriteSwitchesCashOnDelivery(t *testing.T) {
	app := newTestApp(t)
	staff := signInAs(t, app, "cod-staff@example.com", RoleStaff)
	off := false
	rec := do(t, app, http.MethodPatch, "/api/admin/plugins/"+CODPluginKey, bearer(staff), jsonBody(t, PluginPatch{Enabled: &off}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("staff switching cash on delivery off = %d, want 403", rec.Code)
	}
	setCOD(t, app, false)
	if rows := auditRows(t, app, "action = $1 AND entity_id = $2", AuditPluginUpdate, CODPluginKey); len(rows) != 1 {
		t.Fatalf("audit rows for the switch = %d, want 1", len(rows))
	}
}

// The switch is one store's. Off in one, every other store still takes cash
// on delivery, and each store's checkout answers for itself, names included.
func TestCashOnDeliveryIsSwitchedPerStore(t *testing.T) {
	app := newStrictTestApp(t)
	a, b := firstStore(t, app), addTestStore(t, app, "b")
	off := false
	if rec := do(t, app, http.MethodPatch, "/api/admin/plugins/"+CODPluginKey, withAdmin, asStore(a), jsonBody(t, PluginPatch{Enabled: &off})); rec.Code != http.StatusOK {
		t.Fatalf("switch off in store a: %d %s", rec.Code, rec.Body)
	}
	offered := func(store string) (codes, named []string) {
		rec := do(t, app, http.MethodGet, "/api/checkout", asStore(store))
		var out struct {
			PaymentMethods []string        `json:"payment_methods"`
			Methods        []PaymentMethod `json:"methods"`
		}
		decodeData(t, rec, &out)
		for _, m := range out.Methods {
			named = append(named, m.Code)
		}
		return out.PaymentMethods, named
	}
	if codes, named := offered(a); slices.Contains(codes, CodeCOD) || slices.Contains(named, CodeCOD) {
		t.Fatalf("store a switched it off and is still offered %v / %v", codes, named)
	}
	if codes, named := offered(b); !slices.Contains(codes, CodeCOD) || !slices.Contains(named, CodeCOD) {
		t.Fatalf("store b never switched it and is offered %v / %v", codes, named)
	}
}

// The owner types the extra charge as they would say it; checkout adds it to a
// cash order's total, says it on the order, and the storefront is told it
// beside the method. A gateway order is not charged it.
func TestCashOnDeliveryAddsTheStoresExtraCharge(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	rec := do(t, app, http.MethodPatch, "/api/admin/plugins/"+CODPluginKey, withAdmin,
		jsonBody(t, PluginPatch{Settings: map[string]any{"fee": 60}}))
	if rec.Code != http.StatusOK {
		t.Fatalf("set the extra charge: %d %s", rec.Code, rec.Body)
	}

	var fee *Money
	for _, m := range app.Pay().InstalledFor(ctx) {
		if m.Code == CodeCOD {
			fee = m.Fee
		}
	}
	if fee == nil || fee.AmountMinor != 6000 {
		t.Fatalf("the storefront is told a fee of %+v, want 6000 minor", fee)
	}

	product := simpleProduct(t, app, "COD-FEE-1", 1000, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	res, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatal(err)
	}
	o := res.Order
	if o.PaymentFee.AmountMinor != 6000 {
		t.Fatalf("payment_fee is %d, want 6000", o.PaymentFee.AmountMinor)
	}
	want := o.Subtotal.AmountMinor + o.Shipping.AmountMinor - o.Discount.AmountMinor + 6000
	if !o.TaxInclusive {
		want += o.Tax.AmountMinor
	}
	if o.Total.AmountMinor != want {
		t.Fatalf("total is %d, want %d with the extra charge in it", o.Total.AmountMinor, want)
	}
}
