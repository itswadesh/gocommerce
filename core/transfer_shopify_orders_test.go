package gocommerce

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// Orders and customers in Shopify's layout: the orders file goes both ways,
// because a store moving here brings its history in exactly this shape;
// customers only leave, because a customer here is a reading of the orders.

// Two lines of one order, as Shopify's orders export writes them: the order's
// own columns on the first line only.
const shopifyOrdersFixture = `Name,Email,Financial Status,Paid at,Fulfillment Status,Fulfilled at,Accepts Marketing,Currency,Subtotal,Shipping,Taxes,Total,Discount Code,Discount Amount,Shipping Method,Created at,Lineitem quantity,Lineitem name,Lineitem price,Lineitem compare at price,Lineitem sku,Lineitem requires shipping,Lineitem taxable,Lineitem fulfillment status,Billing Name,Billing Street,Billing Address1,Billing Address2,Billing Company,Billing City,Billing Zip,Billing Province,Billing Country,Billing Phone,Shipping Name,Shipping Street,Shipping Address1,Shipping Address2,Shipping Company,Shipping City,Shipping Zip,Shipping Province,Shipping Country,Shipping Phone,Notes,Note Attributes,Cancelled at,Payment Method,Payment Reference,Refunded Amount,Vendor,Outstanding Balance,Employee,Location,Device ID,Id,Tags,Risk Level,Source,Lineitem discount,Tax 1 Name,Tax 1 Value,Tax 2 Name,Tax 2 Value,Tax 3 Name,Tax 3 Value,Tax 4 Name,Tax 4 Value,Tax 5 Name,Tax 5 Value,Phone,Receipt Number,Duties,Billing Province Name,Shipping Province Name,Payment ID,Payment Terms Name,Next Payment Due At,Payment References
#1001,ann@example.com,paid,2024-05-01 10:00:00 +0000,fulfilled,2024-05-02 09:00:00 +0000,no,USD,29.98,5.00,0.00,34.98,,0.00,Standard,2024-05-01 09:58:12 +0000,2,Crew Tee - Black / S,9.99,,TEE-BLK-S,true,true,fulfilled,Ann Example,1 Main St,1 Main St,,,Springfield,12345,IL,United States,555-0100,Ann Example,1 Main St,1 Main St,,,Springfield,12345,IL,United States,555-0100,,,,Shopify Payments,,0.00,Acme,0.00,,,,5001,,Low,web,0.00,,,,,,,,,,,555-0100,,,,,,,,
#1001,,,,,,,,,,,,,,,,1,Mug,10.00,,MUG,true,true,fulfilled,,,,,,,,,,,,,,,,,,,,,,,,,,,Acme,,,,,,,,,0.00,,,,,,,,,,,,,,,,,,,
`

func TestAShopifyOrdersFileImports(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	result, err := app.Data().ImportOrders(ctx, strings.NewReader(shopifyOrdersFixture), ImportOptions{})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Created != 1 || len(result.Errors) != 0 || result.Format != FormatShopify {
		t.Fatalf("result = %+v, want one order from a file recognised as Shopify's", result)
	}
	order, err := app.Order().GetByNumber(ctx, "1001")
	if err != nil {
		t.Fatalf("order 1001: %v", err)
	}
	if order.Status != OrderShipped || order.PaymentStatus != PaymentPaid || order.PaymentProvider != "Shopify Payments" {
		t.Errorf("order = %s / %s / %s, want shipped, paid, Shopify Payments", order.Status, order.PaymentStatus, order.PaymentProvider)
	}
	if order.Email != "ann@example.com" || order.Name != "Ann Example" || order.Currency != "USD" {
		t.Errorf("order = %q %q %q", order.Email, order.Name, order.Currency)
	}
	if order.Subtotal.AmountMinor != 2998 || order.Shipping.AmountMinor != 500 || order.Total.AmountMinor != 3498 {
		t.Errorf("money = %d / %d / %d, want 29.98, 5.00 and 34.98 in minor units", order.Subtotal.AmountMinor, order.Shipping.AmountMinor, order.Total.AmountMinor)
	}
	if order.Address.City != "Springfield" || order.Address.Country != "United States" || order.Address.PostalCode != "12345" || order.Phone != "555-0100" {
		t.Errorf("address = %+v phone %q", order.Address, order.Phone)
	}
	if order.CreatedAt.UTC().Format("2006-01-02T15:04:05Z") != "2024-05-01T09:58:12Z" {
		t.Errorf("created at = %v, want Shopify's timestamp read", order.CreatedAt)
	}
	if len(order.Lines) != 2 || order.Lines[0].SKU != "TEE-BLK-S" || order.Lines[0].Quantity != 2 || order.Lines[0].UnitPrice.AmountMinor != 999 ||
		order.Lines[1].Title != "Mug" || order.Lines[1].UnitPrice.AmountMinor != 1000 {
		t.Errorf("lines = %+v", order.Lines)
	}

	// The same file again is a no-op, and a cancelled order lands cancelled.
	again, _ := app.Data().ImportOrders(ctx, strings.NewReader(shopifyOrdersFixture), ImportOptions{})
	if again.Created != 0 || again.Skipped != 1 {
		t.Errorf("second import = %+v, want the order skipped", again)
	}
	cancelled := strings.Replace(strings.Replace(shopifyOrdersFixture, "#1001", "#1002", 2),
		",,,Shopify Payments,", ",,2024-05-03 08:00:00 +0000,Shopify Payments,", 1)
	if _, err := app.Data().ImportOrders(ctx, strings.NewReader(cancelled), ImportOptions{}); err != nil {
		t.Fatalf("import cancelled: %v", err)
	}
	if o, err := app.Order().GetByNumber(ctx, "1002"); err != nil || o.Status != OrderCancelled {
		t.Errorf("order 1002 = %+v, %v; want cancelled from its Cancelled at", o, err)
	}
}

func TestOrdersExportInShopifysLayout(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	order := placeOrder(t, app, "SHOP-EXP")

	var buf bytes.Buffer
	if err := app.Data().ExportOrders(ctx, &buf, OrderQuery{}, ExportOptions{Format: FormatShopify}); err != nil {
		t.Fatalf("export: %v", err)
	}
	header, rows := readCSV(t, buf.String())
	if strings.Join(header, ",") != strings.Join(shopifyOrderHeader, ",") {
		t.Errorf("header = %v", header)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want one line:\n%s", len(rows), buf.String())
	}
	row := rows[0]
	for name, want := range map[string]string{
		"Name": "#" + order.Number, "Email": "shopper@example.com", "Financial Status": "pending",
		"Fulfillment Status": "unfulfilled", "Currency": order.Currency,
		"Subtotal": "10.00", "Shipping": "0.00", "Taxes": "0.00", "Total": "10.00", "Discount Amount": "0.00",
		"Lineitem quantity": "1", "Lineitem price": "10.00", "Lineitem sku": "SHOP-EXP",
		"Shipping Name": "A Shopper", "Shipping Address1": "1 Test Street", "Shipping City": "Testville",
		"Shipping Zip": "12345", "Shipping Country": "US", "Billing City": "Testville",
		"Payment Method": CodeCOD, "Refunded Amount": "0.00", "Source": "web",
	} {
		if got := cell(t, header, row, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if got := cell(t, header, row, "Created at"); !strings.HasPrefix(got, order.CreatedAt.UTC().Format("2006-01-02")) {
		t.Errorf("Created at = %q", got)
	}

	// A Shopify-shaped export is a file the importer reads: the round trip
	// is a no-op because the order is already here.
	again, err := app.Data().ImportOrders(ctx, strings.NewReader(buf.String()), ImportOptions{})
	if err != nil || again.Skipped != 1 || again.Created != 0 {
		t.Errorf("re-import = %+v, %v; want the order recognised and skipped", again, err)
	}
}

func TestCustomersExportInShopifysLayout(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	placeOrder(t, app, "CUST-SHOP")

	var buf bytes.Buffer
	if err := app.Data().ExportCustomers(ctx, &buf, CustomerQuery{}, ExportOptions{Format: FormatShopify}); err != nil {
		t.Fatalf("export: %v", err)
	}
	header, rows := readCSV(t, buf.String())
	if strings.Join(header, ",") != strings.Join(shopifyCustomerHeader, ",") {
		t.Errorf("header = %v", header)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want the one shopper:\n%s", len(rows), buf.String())
	}
	for name, want := range map[string]string{
		"First Name": "A", "Last Name": "Shopper", "Email": "shopper@example.com",
		"Default Address Address1": "1 Test Street", "Default Address City": "Testville",
		"Default Address Zip": "12345", "Default Address Country Code": "US",
		"Total Spent": "0.00", "Total Orders": "1", "Accepts Email Marketing": "no", "Tax Exempt": "no",
	} {
		if got := cell(t, header, rows[0], name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}
