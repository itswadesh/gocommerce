package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A download, a service or a gift card is sold like anything else and never
// leaves a shelf. The flag says so on the variant, and the checkout charges
// nothing to send a basket that holds nothing to send.

func TestAVariantMayNotNeedShipping(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	// The default is a parcel, which is what every variant was.
	tee := simpleProduct(t, app, "RS-TEE", 1000, 5)
	if !tee.Variants[0].RequiresShipping {
		t.Errorf("a plain product does not require shipping")
	}

	no, yes := false, true
	ebook, err := app.Products().CreateProduct(ctx, ProductInput{
		Title:    "Field guide (PDF)",
		Variants: []VariantInput{{SKU: "RS-PDF", PriceMinor: 1500, RequiresShipping: &no, TrackInventory: &no}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	v := ebook.Variants[0]
	if v.RequiresShipping {
		t.Fatalf("the PDF requires shipping after being created without")
	}
	if back, _ := app.Products().GetVariantBySKU(ctx, "RS-PDF"); back.RequiresShipping {
		t.Errorf("read back as requiring shipping")
	}
	if patched, err := app.Products().UpdateVariant(ctx, v.ID, VariantPatch{RequiresShipping: &yes}); err != nil || !patched.RequiresShipping {
		t.Errorf("patch to true = %+v, %v", patched, err)
	}
	// A patch that does not mention it leaves it alone.
	if patched, err := app.Products().UpdateVariant(ctx, v.ID, VariantPatch{Barcode: strp("123")}); err != nil || !patched.RequiresShipping {
		t.Errorf("an unrelated patch changed it: %+v, %v", patched, err)
	}
}

func TestABasketWithNothingToSendIsNotChargedShipping(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	// Rates for the US only: a parcel bound for France is refused.
	z := zone(t, app, "United States", []string{"US"}, nil)
	rate(t, app, z.ID, "Standard", 500, 0, nil)

	no := false
	ebook, err := app.Products().CreateProduct(ctx, ProductInput{
		Title:    "Field guide (PDF)",
		Variants: []VariantInput{{SKU: "RS-PDF-2", PriceMinor: 1500, RequiresShipping: &no, TrackInventory: &no}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, ebook.Variants[0].ID, 1)
	in := checkoutInput(cart.Token)
	in.Address.Country = "FR"
	res, err := app.orders.Checkout(ctx, CodeCOD, in, "")
	if err != nil {
		t.Fatalf("a basket of nothing but a download was refused: %v", err)
	}
	if res.Order.Shipping.AmountMinor != 0 || res.Order.ShippingMethod != "" || res.Order.Total.AmountMinor != 1500 {
		t.Errorf("order = shipping %d via %q, total %d; want nothing charged to send it", res.Order.Shipping.AmountMinor, res.Order.ShippingMethod, res.Order.Total.AmountMinor)
	}

	// One physical line and the whole basket is a parcel again: France is
	// refused, and the US pays the rate.
	tee := simpleProduct(t, app, "RS-TEE-2", 1000, 5)
	mixed := newCart(t, app)
	addToCart(t, app, mixed.Token, ebook.Variants[0].ID, 1)
	addToCart(t, app, mixed.Token, tee.Variants[0].ID, 1)
	in = checkoutInput(mixed.Token)
	in.Address.Country = "FR"
	if _, err := app.orders.Checkout(ctx, CodeCOD, in, ""); err == nil || !strings.Contains(err.Error(), "does not deliver") {
		t.Errorf("a parcel to France = %v, want the refusal", err)
	}
	in.Address.Country = "US"
	res, err = app.orders.Checkout(ctx, CodeCOD, in, "")
	if err != nil {
		t.Fatalf("checkout to the US: %v", err)
	}
	if res.Order.Shipping.AmountMinor != 500 || res.Order.ShippingMethod != "Standard" {
		t.Errorf("mixed basket shipping = %d via %q, want 500 Standard", res.Order.Shipping.AmountMinor, res.Order.ShippingMethod)
	}
}

// The flag travels in both CSV dialects.
func TestRequiresShippingTravelsInTheCSV(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	file := "Handle,Title,Variant SKU,Variant Price,Variant Requires Shipping\n" +
		"guide,Field guide,RS-CSV,15.00,FALSE\n"
	if _, err := app.Data().ImportProducts(ctx, strings.NewReader(file), ImportOptions{}); err != nil {
		t.Fatalf("import: %v", err)
	}
	v, err := app.Products().GetVariantBySKU(ctx, "RS-CSV")
	if err != nil || v.RequiresShipping {
		t.Fatalf("variant = %+v, %v; want it read as not shipped", v, err)
	}
	native := exportCSV(t, app)
	if !strings.Contains(headerOf(native), "requires_shipping") || column(t, native, "RS-CSV", "requires_shipping") != "false" {
		t.Errorf("native export:\n%s", native)
	}
	if _, err := app.Data().ImportProducts(ctx, strings.NewReader("product_slug,sku,requires_shipping\nguide,RS-CSV,true\n"), ImportOptions{}); err != nil {
		t.Fatalf("import native: %v", err)
	}
	if v, _ := app.Products().GetVariantBySKU(ctx, "RS-CSV"); !v.RequiresShipping {
		t.Errorf("the native column did not set it back")
	}
}

func strp(s string) *string { return &s }

// A basket with nothing to send is asked only where the buyer is (D254): the
// country, and the state tax is worked out from. One parcel in it and the full
// address is required again. The cart says which lines are which, so a
// storefront can skip the step before it is refused.
func TestABasketWithNothingToSendNeedsNoStreetAddress(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	no := false
	ebook, err := app.Products().CreateProduct(ctx, ProductInput{
		Title:    "Field guide (PDF)",
		Variants: []VariantInput{{SKU: "RS-PDF-3", PriceMinor: 1500, RequiresShipping: &no, TrackInventory: &no}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, ebook.Variants[0].ID, 1)
	got, err := app.Cart().GetByToken(ctx, cart.Token)
	if err != nil {
		t.Fatalf("get cart: %v", err)
	}
	if got.Lines[0].RequiresShipping {
		t.Error("the download's cart line says it is sent")
	}

	z := zone(t, app, "India", []string{"IN"}, nil)
	rate(t, app, z.ID, "Standard", 500, 0, nil)
	if rec := do(t, app, http.MethodGet, "/api/checkout/rates?cart="+cart.Token+"&country=IN"); !strings.Contains(rec.Body.String(), `"rates":[]`) {
		t.Errorf("delivery options for a download = %s, want none", rec.Body)
	}

	billing := CheckoutInput{CartID: cart.Token, Email: "buyer@example.com", Name: "A Buyer",
		Address: Address{Country: "IN", State: "MH"}}
	res, err := app.orders.Checkout(ctx, CodeCOD, billing, "")
	if err != nil {
		t.Fatalf("a download with only a country and state was refused: %v", err)
	}
	if res.Order.Address.Country != "IN" || res.Order.Address.State != "MH" {
		t.Errorf("order address = %+v, want the billing country and state kept", res.Order.Address)
	}

	noCountry := billing
	noCountry.Address = Address{}
	if _, err := app.orders.Checkout(ctx, CodeCOD, noCountry, ""); err == nil || !strings.Contains(err.Error(), "country") {
		t.Errorf("no country = %v, want it asked for", err)
	}

	tee := simpleProduct(t, app, "RS-TEE-3", 1000, 5)
	mixed := newCart(t, app)
	addToCart(t, app, mixed.Token, ebook.Variants[0].ID, 1)
	addToCart(t, app, mixed.Token, tee.Variants[0].ID, 1)
	got, err = app.Cart().GetByToken(ctx, mixed.Token)
	if err != nil {
		t.Fatalf("get cart: %v", err)
	}
	sent := 0
	for _, l := range got.Lines {
		if l.RequiresShipping {
			sent++
		}
	}
	if sent != 1 {
		t.Errorf("%d lines say they are sent, want the tee alone", sent)
	}
	billing.CartID = mixed.Token
	if _, err := app.orders.Checkout(ctx, CodeCOD, billing, ""); err == nil || !strings.Contains(err.Error(), "line1") {
		t.Errorf("a parcel with no street = %v, want the address asked for", err)
	}
}
