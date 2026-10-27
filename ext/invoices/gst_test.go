package invoices

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

const supplierGSTIN = "21AAPFU0939F1Z7" // Odisha

var (
	odisha      = gocommerce.Address{Line1: "4 Market St", City: "Cuttack", State: "Odisha", PostalCode: "753001", Country: "IN"}
	maharashtra = gocommerce.Address{Line1: "9 Dock Rd", City: "Mumbai", State: "MH", PostalCode: "400001", Country: "IN"}
	chandigarh  = gocommerce.Address{Line1: "Sector 17", City: "Chandigarh", State: "Chandigarh", PostalCode: "160017", Country: "IN"}
)

func gstModule(format string) *Module {
	return New(Config{SellerName: "Ignored on a GST invoice", NumberFormat: format})
}

// gstStore is a store registered for GST in the state its GSTIN names.
func gstStore(t *testing.T, cfg gocommerce.Config, mod *Module, state, gstin string) *gocommerce.App {
	t.Helper()
	app := gctest.NewWithConfig(t, cfg, mod)
	if _, err := app.Profile().Set(context.Background(), gocommerce.StoreProfile{
		Name: "Example Jewels", LegalName: "Example Jewels Pvt Ltd",
		AddressLine1: "12 Station Rd", City: "Bhubaneswar", State: state, PostalCode: "751001",
		Country: "IN", TaxID: gstin, Currency: "INR",
	}, nil); err != nil {
		t.Fatalf("profile: %v", err)
	}
	return app
}

func taxRate(t *testing.T, app *gocommerce.App, in gocommerce.TaxRateInput) {
	t.Helper()
	if _, err := app.Taxes().Create(context.Background(), in); err != nil {
		t.Fatalf("tax rate: %v", err)
	}
}

func hsnProduct(t *testing.T, app *gocommerce.App, sku string, price int64, hsn string) *gocommerce.Product {
	t.Helper()
	p := gctest.CreateProduct(t, app, sku, price, 10)
	if _, err := app.Products().UpdateVariant(context.Background(), p.DefaultVariant().ID,
		gocommerce.VariantPatch{HSCode: &hsn}); err != nil {
		t.Fatalf("hs code: %v", err)
	}
	return p
}

type purchase struct {
	Address  gocommerce.Address
	Metadata gocommerce.Metadata
	Code     string
	Lines    []*gocommerce.Product
}

func checkout(t *testing.T, app *gocommerce.App, p purchase) (*gocommerce.CheckoutResult, error) {
	t.Helper()
	ctx := context.Background()
	cart, err := app.Cart().Create(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, prod := range p.Lines {
		if _, err := app.Cart().AddLine(ctx, cart.Token, prod.DefaultVariant().ID, 1); err != nil {
			t.Fatal(err)
		}
	}
	if p.Code != "" {
		if err := app.Cart().SetDiscountCode(ctx, cart.Token, p.Code); err != nil {
			t.Fatal(err)
		}
	}
	return app.Order().Checkout(ctx, gocommerce.CodeCOD, gocommerce.CheckoutInput{
		CartID: cart.Token, Email: "ap@asha.example", Name: "Asha Traders",
		Address: p.Address, Metadata: p.Metadata,
	}, "")
}

// buyAndPay places the order, pays it and delivers order.paid.
func buyAndPay(t *testing.T, app *gocommerce.App, p purchase) *gocommerce.Order {
	t.Helper()
	result, err := checkout(t, app, p)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if _, err := app.Pay().MarkPaid(context.Background(), result.Order.ID, "cash"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	gctest.DrainOutbox(t, app)
	return result.Order
}

type invoiceJSON struct {
	Number string      `json:"number"`
	Tax    *TaxInvoice `json:"tax"`
}

func invoiceOf(t *testing.T, app *gocommerce.App, orderID int64) (invoiceJSON, map[string]json.RawMessage) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/x/invoices/"+strconv.FormatInt(orderID, 10), nil)
	req.Header.Set("Authorization", "Bearer "+gctest.AdminToken)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("invoice %d = %d: %s", orderID, rec.Code, rec.Body)
	}
	var doc invoiceJSON
	gctest.DecodeData(t, rec, &doc)
	var keys map[string]json.RawMessage
	gctest.DecodeData(t, rec, &keys)
	return doc, keys
}

func taxDocOf(t *testing.T, app *gocommerce.App, orderID int64) *TaxInvoice {
	t.Helper()
	doc, _ := invoiceOf(t, app, orderID)
	if doc.Tax == nil {
		t.Fatalf("order %d has no tax invoice", orderID)
	}
	return doc.Tax
}

func TestAnIntraStateInvoiceSplitsEighteenPercent(t *testing.T) {
	app := gstStore(t, gocommerce.Config{}, gstModule(""), "Odisha", supplierGSTIN)
	taxRate(t, app, gocommerce.TaxRateInput{Name: "GST 18%", RateBP: 1800, Country: "IN"})
	cloth := hsnProduct(t, app, "CLOTH", 10000, "630790")

	order := buyAndPay(t, app, purchase{Address: odisha, Lines: []*gocommerce.Product{cloth}})
	doc := taxDocOf(t, app, order.ID)

	want := TaxLine{N: 1, Kind: LineGoods, SKU: "CLOTH", Description: "gctest CLOTH", HSN: "630790",
		Qty: 1, Unit: "NOS", GrossMinor: 10000, TaxableMinor: 10000, RateBP: 1800,
		CGSTMinor: 900, SGSTMinor: 900, TotalMinor: 11800}
	if len(doc.Lines) != 1 || doc.Lines[0] != want {
		t.Errorf("lines = %+v, want %+v", doc.Lines, want)
	}
	if doc.Supply != (Supply{Kind: SupplyIntra, PlaceOfSupplyCode: "21", PlaceOfSupply: "Odisha", StateTaxName: "SGST"}) {
		t.Errorf("supply = %+v", doc.Supply)
	}
	if doc.Totals != (Totals{TaxableMinor: 10000, CGSTMinor: 900, SGSTMinor: 900, TaxMinor: 1800, TotalMinor: 11800}) {
		t.Errorf("totals = %+v", doc.Totals)
	}
	if doc.Supplier.GSTIN != supplierGSTIN || doc.Supplier.LegalName != "Example Jewels Pvt Ltd" || doc.Supplier.StateCode != "21" {
		t.Errorf("supplier = %+v", doc.Supplier)
	}
	if doc.Recipient.Registered || doc.Recipient.LegalName != "Asha Traders" || doc.Recipient.StateCode != "21" {
		t.Errorf("recipient = %+v, want an unregistered buyer in Odisha", doc.Recipient)
	}
	// The default format, counted in the financial year it is now in India.
	fy := seriesYear(time.Now(), &regime{Location: ist})
	if want := "INV-" + strconv.Itoa(fy) + "-00001"; doc.Number != want {
		t.Errorf("number = %q, want %q", doc.Number, want)
	}
	if len(doc.Warnings) != 0 {
		t.Errorf("warnings = %+v, want none", doc.Warnings)
	}

	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/invoices/"+strconv.FormatInt(order.ID, 10), nil)
	html := rec.Body.String()
	for _, s := range []string{"TAX INVOICE", "GSTIN " + supplierGSTIN, "Place of supply: Odisha (21)",
		"Reverse charge: No", "Billed and shipped to", "630790", "<th class=\"num\">SGST</th>",
		"118.00", "For Example Jewels Pvt Ltd", "Authorised signatory"} {
		if !strings.Contains(html, s) {
			t.Errorf("the printed invoice is missing %q", s)
		}
	}
	if strings.Contains(html, ">IGST<") {
		t.Error("an intra-state invoice prints an IGST column")
	}

	rec = gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/invoices", nil)
	if !strings.Contains(rec.Body.String(), `"kind":"tax_invoice"`) {
		t.Errorf("the list row does not say it is a tax invoice: %s", rec.Body)
	}
}

func TestAnInterStateInvoiceChargesIGST(t *testing.T) {
	app := gstStore(t, gocommerce.Config{}, gstModule(""), "Odisha", supplierGSTIN)
	taxRate(t, app, gocommerce.TaxRateInput{Name: "GST 18%", RateBP: 1800, Country: "IN"})
	cloth := hsnProduct(t, app, "CLOTH", 10000, "630790")

	order := buyAndPay(t, app, purchase{Address: maharashtra, Lines: []*gocommerce.Product{cloth}})
	doc := taxDocOf(t, app, order.ID)
	if doc.Supply != (Supply{Kind: SupplyInter, PlaceOfSupplyCode: "27", PlaceOfSupply: "Maharashtra"}) {
		t.Errorf("supply = %+v", doc.Supply)
	}
	if l := doc.Lines[0]; l.IGSTMinor != 1800 || l.CGSTMinor != 0 || l.SGSTMinor != 0 || l.TotalMinor != 11800 {
		t.Errorf("line = %+v, want IGST 1800 only", l)
	}
}

func TestAUnionTerritoryUsesUTGST(t *testing.T) {
	app := gstStore(t, gocommerce.Config{}, gstModule(""), "Chandigarh", "04AABCT1332L1ZM")
	taxRate(t, app, gocommerce.TaxRateInput{Name: "GST 18%", RateBP: 1800, Country: "IN"})
	cloth := hsnProduct(t, app, "CLOTH", 10000, "630790")

	order := buyAndPay(t, app, purchase{Address: chandigarh, Lines: []*gocommerce.Product{cloth}})
	doc := taxDocOf(t, app, order.ID)
	if doc.Supply.StateTaxName != "UTGST" || doc.Lines[0].CGSTMinor != 900 || doc.Lines[0].SGSTMinor != 900 {
		t.Errorf("supply %+v, line %+v, want CGST 900 and UTGST 900", doc.Supply, doc.Lines[0])
	}
	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/invoices/"+strconv.FormatInt(order.ID, 10), nil)
	if !strings.Contains(rec.Body.String(), "<th class=\"num\">UTGST</th>") {
		t.Error("the printed invoice has no UTGST column")
	}
}

func TestMixedRatesFollowTheProductRule(t *testing.T) {
	app := gstStore(t, gocommerce.Config{}, gstModule(""), "Odisha", supplierGSTIN)
	ctx := context.Background()
	jewellery, err := app.Categories().Create(ctx, gocommerce.CategoryInput{Title: "Jewellery"})
	if err != nil {
		t.Fatal(err)
	}
	chain := hsnProduct(t, app, "CHAIN", 10000, "711319")
	cloth := hsnProduct(t, app, "CLOTH", 10000, "630790")
	for _, p := range []*gocommerce.Product{chain, cloth} {
		if _, err := app.Products().UpdateProduct(ctx, p.ID, gocommerce.ProductPatch{CategoryID: gocommerce.SetID(jewellery.ID)}); err != nil {
			t.Fatal(err)
		}
	}
	taxRate(t, app, gocommerce.TaxRateInput{Name: "GST 18%", RateBP: 1800, Country: "IN", CategoryID: &jewellery.ID})
	taxRate(t, app, gocommerce.TaxRateInput{Name: "GST 3%", RateBP: 300, Country: "IN", ProductID: &chain.ID})

	order := buyAndPay(t, app, purchase{Address: odisha, Lines: []*gocommerce.Product{chain, cloth}})
	doc := taxDocOf(t, app, order.ID)
	want := []RateRow{
		{RateBP: 300, TaxableMinor: 10000, CGSTMinor: 150, SGSTMinor: 150},
		{RateBP: 1800, TaxableMinor: 10000, CGSTMinor: 900, SGSTMinor: 900},
	}
	if !reflect.DeepEqual(doc.ByRate, want) {
		t.Errorf("by_rate = %+v, want %+v", doc.ByRate, want)
	}
	if doc.Totals != (Totals{TaxableMinor: 20000, CGSTMinor: 1050, SGSTMinor: 1050, TaxMinor: 2100, TotalMinor: 22100}) {
		t.Errorf("totals = %+v", doc.Totals)
	}
}

func TestAnOddPaisaGoesToTheStateHead(t *testing.T) {
	app := gstStore(t, gocommerce.Config{}, gstModule(""), "Odisha", supplierGSTIN)
	taxRate(t, app, gocommerce.TaxRateInput{Name: "GST 5%", RateBP: 500, Country: "IN"})
	p := hsnProduct(t, app, "TEA", 3333, "0902")

	doc := taxDocOf(t, app, buyAndPay(t, app, purchase{Address: odisha, Lines: []*gocommerce.Product{p}}).ID)
	if l := doc.Lines[0]; l.CGSTMinor != 83 || l.SGSTMinor != 84 || l.TotalMinor != 3500 {
		t.Errorf("line = %+v, want tax 167 as CGST 83 and SGST 84", l)
	}
}

func TestAnInclusivePriceSplitsOutTheTax(t *testing.T) {
	app := gstStore(t, gocommerce.Config{PricesIncludeTax: true}, gstModule(""), "Odisha", supplierGSTIN)
	taxRate(t, app, gocommerce.TaxRateInput{Name: "GST 18%", RateBP: 1800, Country: "IN"})
	p := hsnProduct(t, app, "CLOTH", 11800, "630790")

	doc := taxDocOf(t, app, buyAndPay(t, app, purchase{Address: odisha, Lines: []*gocommerce.Product{p}}).ID)
	if l := doc.Lines[0]; l.TaxableMinor != 10000 || l.CGSTMinor+l.SGSTMinor != 1800 || l.TotalMinor != 11800 {
		t.Errorf("line = %+v, want taxable 10000, tax 1800, total 11800", l)
	}
}

func TestADiscountedLineIsTaxedOnWhatWasPaid(t *testing.T) {
	app := gstStore(t, gocommerce.Config{}, gstModule(""), "Odisha", supplierGSTIN)
	taxRate(t, app, gocommerce.TaxRateInput{Name: "GST 18%", RateBP: 1800, Country: "IN"})
	if _, err := app.Discounts().Create(context.Background(), gocommerce.DiscountInput{
		Code: "DIWALI", Title: "Diwali", Kind: gocommerce.DiscountFixed, ValueMinor: 2000,
	}); err != nil {
		t.Fatal(err)
	}
	p := hsnProduct(t, app, "SAREE", 20000, "5007")

	doc := taxDocOf(t, app, buyAndPay(t, app, purchase{Address: odisha, Code: "DIWALI", Lines: []*gocommerce.Product{p}}).ID)
	want := TaxLine{N: 1, Kind: LineGoods, SKU: "SAREE", Description: "gctest SAREE", HSN: "5007",
		Qty: 1, Unit: "NOS", GrossMinor: 20000, DiscountMinor: 2000, TaxableMinor: 18000, RateBP: 1800,
		CGSTMinor: 1620, SGSTMinor: 1620, TotalMinor: 21240}
	if doc.Lines[0] != want {
		t.Errorf("line = %+v, want %+v", doc.Lines[0], want)
	}
}

func TestDeliveryIsAnUntaxedLine(t *testing.T) {
	app := gstStore(t, gocommerce.Config{FlatShippingMinor: 5000}, gstModule(""), "Odisha", supplierGSTIN)
	taxRate(t, app, gocommerce.TaxRateInput{Name: "GST 18%", RateBP: 1800, Country: "IN"})
	p := hsnProduct(t, app, "CLOTH", 10000, "630790")

	order := buyAndPay(t, app, purchase{Address: odisha, Lines: []*gocommerce.Product{p}})
	doc := taxDocOf(t, app, order.ID)
	freight := TaxLine{N: 2, Kind: LineFreight, Description: "Delivery", Qty: 1, Unit: "NOS",
		GrossMinor: 5000, TaxableMinor: 5000, TotalMinor: 5000}
	if len(doc.Lines) != 2 || doc.Lines[1] != freight {
		t.Errorf("lines = %+v, want the delivery as %+v", doc.Lines, freight)
	}
	if !reflect.DeepEqual(doc.Warnings, []Warning{{Code: "freight_untaxed"}}) {
		t.Errorf("warnings = %+v, want freight_untaxed", doc.Warnings)
	}
	if doc.Totals.TotalMinor != 16800 || order.Total.AmountMinor != 16800 {
		t.Errorf("invoice total %d, order total %d, want 16800 both", doc.Totals.TotalMinor, order.Total.AmountMinor)
	}
}

// A store outside India gets exactly the invoice it always did: the same
// number, no tax document, and the same page from the same template.
func TestANonIndianStoresInvoiceIsUnchanged(t *testing.T) {
	mod := New(Config{SellerName: "Example Ltd", SellerAddress: "1 Commerce Way", TaxID: "GB123456789"})
	app := gctest.New(t, mod)
	ctx := context.Background()
	if _, err := app.Profile().Set(ctx, gocommerce.StoreProfile{Country: "GB", TaxID: "GB123456789", Currency: "GBP"}, nil); err != nil {
		t.Fatal(err)
	}
	assertPlainInvoice(t, app, mod)
}

func TestAnIndianStoreWithoutGSTINIssuesAPlainInvoice(t *testing.T) {
	mod := New(Config{SellerName: "Example Ltd"})
	app := gctest.New(t, mod)
	if _, err := app.Profile().Set(context.Background(), gocommerce.StoreProfile{Country: "IN", State: "Odisha", Currency: "INR"}, nil); err != nil {
		t.Fatal(err)
	}
	assertPlainInvoice(t, app, mod)
}

// A GST invoice is in rupees, so a registered Indian store selling in another
// currency issues the plain one.
func TestAnIndianStoreSellingInDollarsIssuesAPlainInvoice(t *testing.T) {
	mod := New(Config{SellerName: "Example Ltd"})
	app := gctest.New(t, mod)
	if _, err := app.Profile().Set(context.Background(), gocommerce.StoreProfile{
		Country: "IN", State: "Odisha", TaxID: supplierGSTIN, Currency: "USD"}, nil); err != nil {
		t.Fatal(err)
	}
	assertPlainInvoice(t, app, mod)
}

func assertPlainInvoice(t *testing.T, app *gocommerce.App, mod *Module) {
	t.Helper()
	ctx := context.Background()
	result := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "cash"); err != nil {
		t.Fatal(err)
	}
	gctest.DrainOutbox(t, app)

	doc, keys := invoiceOf(t, app, result.Order.ID)
	if _, ok := keys["tax"]; ok {
		t.Error("a plain invoice's JSON has a tax key")
	}
	if want := "INV-" + strconv.Itoa(time.Now().UTC().Year()) + "-00001"; doc.Number != want {
		t.Errorf("number = %q, want %q", doc.Number, want)
	}
	var isNull bool
	if err := app.DB().QueryRowContext(ctx, `SELECT tax_doc IS NULL FROM invoices_documents WHERE order_id = $1`,
		result.Order.ID).Scan(&isNull); err != nil || !isNull {
		t.Errorf("tax_doc is null = %v (%v), want true", isNull, err)
	}
	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/invoices", nil)
	if strings.Contains(rec.Body.String(), "kind") {
		t.Errorf("a plain list row carries a kind: %s", rec.Body)
	}

	var issuedAt time.Time
	var snapshot []byte
	if err := app.DB().QueryRowContext(ctx, `SELECT issued_at, snapshot FROM invoices_documents WHERE order_id = $1`,
		result.Order.ID).Scan(&issuedAt, &snapshot); err != nil {
		t.Fatal(err)
	}
	var order gocommerce.Order
	if err := json.Unmarshal(snapshot, &order); err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	if err := template.Must(template.New("invoice").Parse(invoiceHTML)).Execute(&want, map[string]any{
		"Number": doc.Number, "IssuedAt": issuedAt.UTC().Format("2 January 2006"),
		"Order": order, "Config": mod.cfg,
	}); err != nil {
		t.Fatal(err)
	}
	page := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/invoices/"+strconv.FormatInt(result.Order.ID, 10), nil)
	if page.Body.String() != want.String() {
		t.Errorf("the plain page changed:\n%s\nwant:\n%s", page.Body, want.String())
	}
}

func TestAnInvalidBuyerGSTINIsRefusedAtCheckout(t *testing.T) {
	app := gstStore(t, gocommerce.Config{}, gstModule(""), "Odisha", supplierGSTIN)
	p := gctest.CreateProduct(t, app, "CLOTH", 10000, 10)

	_, err := checkout(t, app, purchase{Address: odisha, Lines: []*gocommerce.Product{p},
		Metadata: gocommerce.Metadata{"gst": map[string]any{"gstin": "21AABCT1332L1ZX"}}})
	var api *gocommerce.APIError
	if !errors.As(err, &api) || api.Status != http.StatusBadRequest ||
		!strings.Contains(api.Message, "metadata.gst.gstin") || !strings.Contains(api.Message, "should be Q") {
		t.Errorf("a bad buyer GSTIN = %v, want a validation error naming the field and the check character", err)
	}

	_, err = checkout(t, app, purchase{Address: gocommerce.Address{Line1: "1 Rd", City: "X", PostalCode: "1", State: "Atlantis", Country: "IN"},
		Lines: []*gocommerce.Product{p}})
	if err == nil || !strings.Contains(err.Error(), "choose a state") {
		t.Errorf("an Indian address with no known state = %v, want choose a state", err)
	}

	order := buyAndPay(t, app, purchase{Address: odisha, Lines: []*gocommerce.Product{p},
		Metadata: gocommerce.Metadata{"gst": map[string]any{"gstin": "21aabct1332l1zq", "name": "Asha Traders Pvt Ltd"}}})
	r := taxDocOf(t, app, order.ID).Recipient
	if !r.Registered || r.GSTIN != "21AABCT1332L1ZQ" || r.LegalName != "Asha Traders Pvt Ltd" {
		t.Errorf("recipient = %+v, want the registered buyer", r)
	}
}

func TestAnInvalidBuyerGSTINIsIgnoredOnAStoreWithoutGST(t *testing.T) {
	app := gctest.New(t, testModule())
	p := gctest.CreateProduct(t, app, "CLOTH", 10000, 10)
	if _, err := checkout(t, app, purchase{Address: maharashtra, Lines: []*gocommerce.Product{p},
		Metadata: gocommerce.Metadata{"gst": map[string]any{"gstin": "not a gstin"}}}); err != nil {
		t.Errorf("checkout on a store without GST = %v, want it placed", err)
	}
}

func TestTheSerialRestartsEachFinancialYear(t *testing.T) {
	clock := time.Date(2027, 3, 31, 18, 29, 59, 0, time.UTC) // 23:59:59 in India
	mod := gstModule("INV/{fy}/{seq:05}")
	mod.now = func() time.Time { return clock }
	app := gstStore(t, gocommerce.Config{}, mod, "Odisha", supplierGSTIN)
	p := gctest.CreateProduct(t, app, "CLOTH", 10000, 10)

	first := buyAndPay(t, app, purchase{Address: odisha, Lines: []*gocommerce.Product{p}})
	clock = time.Date(2027, 3, 31, 18, 30, 0, 0, time.UTC) // 1 April in India
	second := buyAndPay(t, app, purchase{Address: odisha, Lines: []*gocommerce.Product{p}})

	for _, c := range []struct {
		id         int64
		number, fy string
		date       string
	}{
		{first.ID, "INV/26-27/00001", "2026-27", "2027-03-31"},
		{second.ID, "INV/27-28/00001", "2027-28", "2027-04-01"},
	} {
		doc := taxDocOf(t, app, c.id)
		if doc.Number != c.number || doc.FinancialYear != c.fy || doc.Date != c.date {
			t.Errorf("invoice = %s %s %s, want %s %s %s", doc.Number, doc.FinancialYear, doc.Date, c.number, c.fy, c.date)
		}
	}
}

func TestATooLongGSTNumberIsRefused(t *testing.T) {
	mod := gstModule("INVOICE/{fy}/{seq:08}")
	app := gstStore(t, gocommerce.Config{}, mod, "Odisha", supplierGSTIN)
	ctx := context.Background()
	p := gctest.CreateProduct(t, app, "CLOTH", 10000, 10)
	order := buyAndPay(t, app, purchase{Address: odisha, Lines: []*gocommerce.Product{p}})

	if err := mod.issue(ctx, order.ID); err == nil || !strings.Contains(err.Error(), "not a GST serial") {
		t.Errorf("issue = %v, want the number refused", err)
	}
	var invoices, counters int
	if err := app.DB().QueryRowContext(ctx,
		`SELECT (SELECT count(*) FROM invoices_documents), (SELECT count(*) FROM invoices_counters)`).
		Scan(&invoices, &counters); err != nil {
		t.Fatal(err)
	}
	if invoices != 0 || counters != 0 {
		t.Errorf("%d invoices and %d counters after a refused number, want none: the number must roll back", invoices, counters)
	}
}

// The builder's refusals, which no checkout can produce.
func TestTheBuilderRefusesWhatItCannotStandBehind(t *testing.T) {
	g, err := gocommerce.ParseGSTIN(supplierGSTIN)
	if err != nil {
		t.Fatal(err)
	}
	reg := regime{GSTIN: g, Location: ist}
	line := gocommerce.OrderLine{SKU: "X", Quantity: 1, Total: gocommerce.Money{AmountMinor: 10000},
		Tax: gocommerce.LineTax{RateBP: 1800, AmountMinor: 1800}}
	order := func(addr gocommerce.Address, total int64) *gocommerce.Order {
		return &gocommerce.Order{Number: "1001", Currency: "INR", Address: addr,
			Lines: []gocommerce.OrderLine{line}, Total: gocommerce.Money{AmountMinor: total}}
	}

	_, err = buildTaxInvoice(taxInput{Regime: reg, IssuedAt: time.Now(), DiscountShares: []int64{0},
		Order: order(gocommerce.Address{Country: "IN", State: "Atlantis"}, 11800)})
	if err == nil || !strings.Contains(err.Error(), "GST knows no state") {
		t.Errorf("an unplaceable Indian address = %v, want a refusal, not a guessed state", err)
	}
	_, err = buildTaxInvoice(taxInput{Regime: reg, IssuedAt: time.Now(), DiscountShares: []int64{0},
		Order: order(odisha, 11900)})
	if err == nil || !strings.Contains(err.Error(), "the lines come to 11800 but order 1001 is 11900") {
		t.Errorf("lines that miss the total = %v, want a refusal", err)
	}
	doc, err := buildTaxInvoice(taxInput{Regime: reg, IssuedAt: time.Now(), DiscountShares: []int64{0},
		Order: order(gocommerce.Address{Country: "GB"}, 11800)})
	if err != nil || doc.Supply.Kind != SupplyExport || doc.Lines[0].IGSTMinor != 1800 {
		t.Errorf("an export = %v / %+v, want IGST", err, doc)
	}
}

func TestAnOrderWithNoAddressIsSuppliedInTheSuppliersState(t *testing.T) {
	g, err := gocommerce.ParseGSTIN(supplierGSTIN)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := buildTaxInvoice(taxInput{Regime: regime{GSTIN: g, Location: ist}, IssuedAt: time.Now(),
		DiscountShares: []int64{0}, Order: &gocommerce.Order{Number: "1002", Currency: "INR",
			Lines: []gocommerce.OrderLine{{SKU: "EBOOK", Quantity: 1, Total: gocommerce.Money{AmountMinor: 10000},
				Tax: gocommerce.LineTax{RateBP: 1800, AmountMinor: 1800}}},
			Total: gocommerce.Money{AmountMinor: 11800}}})
	if err != nil {
		t.Fatal(err)
	}
	want := Supply{Kind: SupplyIntra, PlaceOfSupplyCode: "21", PlaceOfSupply: "Odisha", StateTaxName: "SGST"}
	if doc.Supply != want {
		t.Errorf("supply = %+v, want %+v, not an export", doc.Supply, want)
	}
	if l := doc.Lines[0]; l.CGSTMinor != 900 || l.SGSTMinor != 900 || l.IGSTMinor != 0 {
		t.Errorf("line = %+v, want CGST 900 and SGST 900", l)
	}
}

// TestTwoDeliveriesAtOnceLeaveNoGap: the paid event and the startup
// reconcile can both reach an order with no invoice; the loser must give its
// number back.
func TestTwoDeliveriesAtOnceLeaveNoGap(t *testing.T) {
	mod := gstModule("INV/{fy}/{seq:05}")
	app := gstStore(t, gocommerce.Config{}, mod, "Odisha", supplierGSTIN)
	taxRate(t, app, gocommerce.TaxRateInput{Name: "GST 18%", RateBP: 1800, Country: "IN"})
	cloth := hsnProduct(t, app, "CLOTH", 10000, "630790")
	ctx := context.Background()

	result, err := checkout(t, app, purchase{Address: odisha, Lines: []*gocommerce.Product{cloth}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "cash"); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- mod.issue(ctx, result.Order.ID) }()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Errorf("issue: %v", err)
		}
	}
	gctest.DrainOutbox(t, app)
	second := buyAndPay(t, app, purchase{Address: odisha, Lines: []*gocommerce.Product{cloth}})
	if got := taxDocOf(t, app, second.ID).Number; !strings.HasSuffix(got, "/00002") {
		t.Errorf("the next invoice = %s, want sequence 00002 with no number lost", got)
	}
}

func TestRupeesAndRates(t *testing.T) {
	t.Parallel()
	for minor, want := range map[int64]string{0: "0.00", 5: "0.05", 99999: "999.99", 103893333: "10,38,933.33",
		10389333: "1,03,893.33", -1500: "-15.00"} {
		if got := rupees(minor); got != want {
			t.Errorf("rupees(%d) = %q, want %q", minor, got, want)
		}
	}
	for _, c := range []struct {
		bp, div int
		want    string
	}{{1800, 1, "18%"}, {1800, 2, "9%"}, {500, 2, "2.5%"}, {25, 2, "0.125%"}, {300, 1, "3%"}} {
		if got := percent(c.bp, c.div); got != c.want {
			t.Errorf("percent(%d, %d) = %q, want %q", c.bp, c.div, got, c.want)
		}
	}
}
