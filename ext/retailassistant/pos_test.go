package retailassistant

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gocommerce "github.com/misiki-in/gocommerce/core"
	"github.com/misiki-in/gocommerce/gctest"
)

type counterFixture struct {
	*harness
	product *gocommerce.Product
	variant int64
}

func setupCounter(t *testing.T) *counterFixture {
	t.Helper()
	h := setup(t)
	h.setSetting("counter_country", "IN")
	h.setSetting("counter_state", "OD")
	p := gctest.CreateProduct(t, h.app, "LIP-01", 25000, 20)
	barcode := "8901234567890"
	if _, err := h.app.Products().UpdateVariant(h.ctxOf(""), p.DefaultVariant().ID, gocommerce.VariantPatch{Barcode: &barcode}); err != nil {
		t.Fatal(err)
	}
	return &counterFixture{harness: h, product: p, variant: p.DefaultVariant().ID}
}

func (f *counterFixture) available() int {
	f.t.Helper()
	v, err := f.app.Products().GetVariant(f.ctxOf(""), f.variant)
	if err != nil {
		f.t.Fatal(err)
	}
	return v.Available
}

func (f *counterFixture) sell(key string, body map[string]any) (*httptest.ResponseRecorder, SaleResult) {
	f.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		f.t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path("/counter/sales", ""), bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+gctest.AdminToken)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(rec, req)
	var out SaleResult
	if rec.Code < 300 {
		gctest.DecodeData(f.t, rec, &out)
	}
	return rec, out
}

func (f *counterFixture) basket(mode string, qty int) map[string]any {
	return map[string]any{"mode": mode, "lines": []map[string]any{{"variant_id": f.variant, "quantity": qty}}}
}

func TestAScanFindsTheProductExactlyAndNothingNearIt(t *testing.T) {
	f := setupCounter(t)
	var got []CounterItem
	f.admin(http.MethodGet, path("/counter/lookup?code=8901234567890", ""), nil, &got, http.StatusOK)
	if len(got) != 1 || got[0].VariantID != f.variant || got[0].Price.AmountMinor != 25000 || *got[0].Available != 20 {
		t.Fatalf("by barcode: %+v", got)
	}
	f.admin(http.MethodGet, path("/counter/lookup?code=lip-01", ""), nil, &got, http.StatusOK)
	if len(got) != 1 {
		t.Fatalf("by SKU: %+v", got)
	}
	f.admin(http.MethodGet, path("/counter/lookup?code=890123456789", ""), nil, &got, http.StatusOK)
	if len(got) != 0 {
		t.Fatalf("a near match: %+v", got)
	}
}

func TestAnUnknownBarcodeIsSavedThroughTheCatalogue(t *testing.T) {
	f := setupCounter(t)
	f.admin(http.MethodPost, path("/counter/barcodes", ""), map[string]any{"variant_id": f.variant, "barcode": "5550001"}, nil, http.StatusOK)
	var got []CounterItem
	f.admin(http.MethodGet, path("/counter/lookup?code=5550001", ""), nil, &got, http.StatusOK)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestACashSaleMovesStockOnceAndIsPaidAndHandedOver(t *testing.T) {
	f := setupCounter(t)
	rec, sale := f.sell("press-1", f.basket(modeCash, 3))
	if rec.Code != http.StatusCreated {
		t.Fatalf("sale = %d %s", rec.Code, rec.Body)
	}
	if f.available() != 17 {
		t.Fatalf("available = %d", f.available())
	}
	if sale.PaymentStatus != "paid" || sale.Total.AmountMinor != 75000 || len(sale.Warnings) != 0 {
		t.Fatalf("sale = %+v", sale)
	}
	o, err := f.app.Order().Get(f.ctxOf(""), sale.OrderID)
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != gocommerce.OrderDelivered || o.Email != "" || o.PaymentProvider != counterCode {
		t.Fatalf("order = status %s, email %q, provider %s", o.Status, o.Email, o.PaymentProvider)
	}

	// A double press sells once.
	again, repeat := f.sell("press-1", f.basket(modeCash, 3))
	if again.Code != http.StatusOK || repeat.OrderID != sale.OrderID || f.available() != 17 {
		t.Fatalf("again = %d %+v, available %d", again.Code, repeat, f.available())
	}
	if rec, _ := f.sell("", f.basket(modeCash, 1)); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key = %d", rec.Code)
	}
}

func TestChoosingUPIProvesNothing(t *testing.T) {
	f := setupCounter(t)
	_, unseen := f.sell("upi-1", f.basket(modeUPI, 1))
	if unseen.PaymentStatus == "paid" {
		t.Fatal("UPI was called paid because somebody chose it")
	}
	if f.available() != 19 {
		t.Fatalf("an unpaid sale still left the shelf: available %d", f.available())
	}
	// Not swept away as an abandoned checkout.
	if _, _, err := f.app.Order().SweepUnpaidPass(f.ctxOf("")); err != nil {
		t.Fatal(err)
	}
	if f.available() != 19 {
		t.Fatalf("the sweep undid a sale: available %d", f.available())
	}

	body := f.basket(modeUPI, 1)
	body["money_seen"], body["reference"] = true, "UTR999"
	_, seen := f.sell("upi-2", body)
	if seen.PaymentStatus != "paid" {
		t.Fatalf("seen = %+v", seen)
	}
	o, _ := f.app.Order().Get(f.ctxOf(""), seen.OrderID)
	if o.PaymentReference != "UTR999" {
		t.Fatalf("reference = %q", o.PaymentReference)
	}
	_, credit := f.sell("credit-1", f.basket(modeCredit, 1))
	if credit.PaymentStatus == "paid" {
		t.Fatal("credit was called paid")
	}
}

func TestACounterSaleAddsNoDeliveryAndNoMailEvenWhereTheStoreCharges(t *testing.T) {
	f := setupCounter(t)
	zone, err := f.app.Shipping().CreateZone(f.ctxOf(""), gocommerce.ShippingZoneInput{Name: "India", Countries: []string{"IN"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Shipping().CreateRate(f.ctxOf(""), gocommerce.ShippingRateInput{ZoneID: zone.ID, Name: "Courier", PriceMinor: 9900}); err != nil {
		t.Fatal(err)
	}
	var quote gocommerce.CheckoutQuote
	f.admin(http.MethodPost, path("/counter/preview", ""), f.basket(modeCash, 2), &quote, http.StatusOK)
	if quote.Shipping.AmountMinor != 0 || quote.Total.AmountMinor != 50000 {
		t.Fatalf("quote = %+v", quote)
	}
	_, sale := f.sell("ship-1", f.basket(modeCash, 2))
	if sale.Total.AmountMinor != 50000 {
		t.Fatalf("total = %+v", sale.Total)
	}
}

func TestAShopperCannotReachTheCounterMethod(t *testing.T) {
	f := setupCounter(t)
	rec := gctest.Request(t, f.app, http.MethodGet, "/api/payment-methods", nil)
	if strings.Contains(rec.Body.String(), counterCode) {
		t.Fatalf("offered to shoppers: %s", rec.Body)
	}
	cart, err := f.app.Cart().Create(f.ctxOf(""), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Cart().AddLine(f.ctxOf(""), cart.Token, f.variant, 1); err != nil {
		t.Fatal(err)
	}
	rec = gctest.Request(t, f.app, http.MethodPost, "/api/checkout/"+counterCode, map[string]any{
		"cart_id": cart.Token, "email": "a@example.com",
		"address": map[string]string{"line1": "1 Road", "city": "Koraput", "postal_code": "764020", "country": "IN"},
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a shopper checking out with it = %d %s", rec.Code, rec.Body)
	}
	if f.available() != 20 {
		t.Fatalf("available = %d", f.available())
	}
	// And a shopper's order still needs an email: only this module's flag waives it.
	rec = gctest.Request(t, f.app, http.MethodPost, "/api/checkout/cod", map[string]any{
		"cart_id": cart.Token, "counter": true, "address": map[string]string{"line1": "1 Road", "city": "Koraput", "postal_code": "764020", "country": "IN"},
	})
	if rec.Code < 400 {
		t.Fatalf("no email at the storefront = %d", rec.Code)
	}
	admin := gctest.AdminRequest(t, f.app, http.MethodPost, "/api/admin/orders", map[string]any{
		"counter": true, "lines": []map[string]any{{"variant_id": f.variant, "quantity": 1}},
	})
	if admin.Code < 400 {
		t.Fatalf("the flag came in over the API = %d", admin.Code)
	}
}

func TestADiscountIsCappedByTheOwnerAndWorkedOutHere(t *testing.T) {
	f := setupCounter(t)
	body := f.basket(modeCash, 2)
	body["discount_bps"] = 500
	rec, _ := f.sell("disc-1", body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("with no limit set = %d %s", rec.Code, rec.Body)
	}
	if f.available() != 20 {
		t.Fatalf("a refused sale moved stock: %d", f.available())
	}
	f.setSetting("counter_discount_limit", 5)
	rec, sale := f.sell("disc-2", body)
	if rec.Code != http.StatusCreated || sale.Total.AmountMinor != 47500 {
		t.Fatalf("5%% off 2 at 250.00 = %d %+v", rec.Code, sale)
	}
	body["discount_bps"] = 501
	if rec, _ := f.sell("disc-3", body); rec.Code != http.StatusForbidden {
		t.Fatalf("over the limit = %d", rec.Code)
	}
}

func TestAPriceInTheCatalogueIsWhatIsCharged(t *testing.T) {
	f := setupCounter(t)
	price := int64(30000)
	if _, err := f.app.Products().UpdateVariant(f.ctxOf(""), f.variant, gocommerce.VariantPatch{PriceMinor: &price}); err != nil {
		t.Fatal(err)
	}
	_, sale := f.sell("price-1", f.basket(modeCash, 1))
	if sale.Total.AmountMinor != 30000 {
		t.Fatalf("total = %+v", sale.Total)
	}
}

func TestAPriceFromTheScreenIsRefused(t *testing.T) {
	f := setupCounter(t)
	body := f.basket(modeCash, 1)
	body["lines"].([]map[string]any)[0]["unit_price_minor"] = 1
	if rec, _ := f.sell("cheat-1", body); rec.Code != http.StatusBadRequest {
		t.Fatalf("a price in the basket = %d %s", rec.Code, rec.Body)
	}
	if f.available() != 20 {
		t.Fatalf("available = %d", f.available())
	}
}

func TestReturningASaleOnTheOrderScreenPutsStockBack(t *testing.T) {
	f := setupCounter(t)
	_, sale := f.sell("return-1", f.basket(modeCash, 4))
	if f.available() != 16 {
		t.Fatalf("available = %d", f.available())
	}
	o, err := f.app.Order().Get(f.ctxOf(""), sale.OrderID)
	if err != nil {
		t.Fatal(err)
	}
	f.admin(http.MethodPost, fmt.Sprintf("/api/admin/orders/%d/returns", sale.OrderID), map[string]any{
		"reason": "Wrong shade",
		"lines":  []map[string]any{{"line_id": o.Lines[0].ID, "quantity": 2, "restock": true}},
	}, nil, http.StatusCreated)
	if f.available() != 18 {
		t.Fatalf("after a return, available = %d", f.available())
	}
}

func TestMoreThanTheShelfHoldsIsRefusedAndKeepsNoClaim(t *testing.T) {
	f := setupCounter(t)
	rec, _ := f.sell("big-1", f.basket(modeCash, 21))
	if rec.Code < 400 {
		t.Fatalf("overselling = %d", rec.Code)
	}
	if n := f.count("", "retailassistant_counter_sales"); n != 0 {
		t.Fatalf("%d rows claim a sale that never happened", n)
	}
	rec, _ = f.sell("big-1", f.basket(modeCash, 20))
	if rec.Code != http.StatusCreated {
		t.Fatalf("the same press, for what is there = %d %s", rec.Code, rec.Body)
	}
}

func TestStaffCanSellAndCannotTeachTheCatalogue(t *testing.T) {
	f := setupCounter(t)
	staff := gctest.OperatorToken(t, f.app, "cashier@example.com", gocommerce.RoleStaff)
	lookup := gctest.SessionRequest(t, f.app, staff, http.MethodGet, path("/counter/lookup?code=lip-01", ""), nil)
	if lookup.Code != http.StatusOK {
		t.Fatalf("a cashier looking up = %d %s", lookup.Code, lookup.Body)
	}
	link := gctest.SessionRequest(t, f.app, staff, http.MethodPost, path("/counter/barcodes", ""), map[string]any{"variant_id": f.variant, "barcode": "1"})
	if link.Code != http.StatusForbidden {
		t.Fatalf("a cashier changing a barcode = %d", link.Code)
	}
	// And the counter does not open the rest of the supplier ledger to them.
	if rec := gctest.SessionRequest(t, f.app, staff, http.MethodGet, path("/suppliers", ""), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("a cashier reading suppliers = %d", rec.Code)
	}
}

func TestStoresKeepTheirOwnCounters(t *testing.T) {
	f := setupCounter(t)
	var got []CounterItem
	f.admin(http.MethodGet, path("/counter/lookup?code=lip-01", f.second), nil, &got, http.StatusOK)
	if len(got) != 0 {
		t.Fatalf("Store B finds Store A's product: %+v", got)
	}
	f.sell("iso-1", f.basket(modeCash, 1))
	if n := f.count(f.second, "retailassistant_counter_sales"); n != 0 {
		t.Fatalf("Store B sees %d of Store A's sales", n)
	}
}
