package gocommerce

import (
	"bytes"
	"context"
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// Customers are a reading of the orders, not a table — D22 says a shopper never
// needs an account, so "a customer" can only be every order sharing an email.
// These pin what that reading claims.
func TestCustomersAreGroupedOrders(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "CUST-1", 1000, 50)
	variant := product.DefaultVariant()
	address := Address{Line1: "1 Test Street", City: "Testville", PostalCode: "12345", Country: "US"}

	place := func(email, name string, qty int) *Order {
		t.Helper()
		res, err := app.Order().Create(ctx, NewOrderInput{
			Email: email, Name: name, Address: address,
			Lines: []NewOrderLine{{VariantID: variant.ID, Quantity: qty}},
		})
		if err != nil {
			t.Fatalf("place for %s: %v", email, err)
		}
		return res.Order
	}

	first := place("Regular@Example.com", "Reg Ular", 2)
	// The same person, typed differently: an email is a handle, not a string.
	place("regular@example.com", "Reg Ular Jr", 1)
	place("once@example.com", "One Timer", 1)

	// Only what was actually paid counts as spent.
	if _, err := app.Pay().MarkPaid(ctx, first.ID, "cash"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}

	customers, total, err := app.Order().Customers(ctx, CustomerQuery{})
	if err != nil {
		t.Fatalf("customers: %v", err)
	}
	if total != 2 {
		t.Fatalf("total = %d customers, want 2 — the two spellings are one person", total)
	}

	var regular *Customer
	for _, c := range customers {
		if c.Email == "regular@example.com" {
			regular = c
		}
	}
	if regular == nil {
		t.Fatalf("the grouped customer is missing: %+v", customers)
	}
	if regular.Orders != 2 {
		t.Errorf("orders = %d, want 2", regular.Orders)
	}
	if regular.Spent.AmountMinor != 2000 {
		t.Errorf("spent = %d, want only the paid order's 2000", regular.Spent.AmountMinor)
	}
	// The newest order is what the store knows about them now.
	if regular.Name != "Reg Ular Jr" {
		t.Errorf("name = %q, want the most recent order's", regular.Name)
	}
	if regular.Address.City != "Testville" {
		t.Errorf("address did not survive the grouping: %+v", regular.Address)
	}
	if !regular.LastOrderAt.After(regular.FirstOrderAt) && regular.LastOrderAt != regular.FirstOrderAt {
		t.Error("last order is before the first")
	}
}

// A cancelled order still means the person exists; it is not a sale.
func TestCustomersCountAndSearch(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "CUST-2", 1000, 50)
	variant := product.DefaultVariant()
	address := Address{Line1: "1 Test Street", City: "Testville", PostalCode: "12345", Country: "US"}
	res, err := app.Order().Create(ctx, NewOrderInput{
		Email: "gone@example.com", Name: "Cancelled Carol", Address: address,
		Lines: []NewOrderLine{{VariantID: variant.ID, Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("place: %v", err)
	}
	if _, err := app.Order().Cancel(ctx, res.Order.ID, "changed their mind"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	customers, _, err := app.Order().Customers(ctx, CustomerQuery{Search: "carol"})
	if err != nil {
		t.Fatalf("search by name: %v", err)
	}
	if len(customers) != 1 {
		t.Fatalf("search for a name found %d, want 1", len(customers))
	}
	if customers[0].Orders != 0 {
		t.Errorf("orders = %d after the only one was cancelled, want 0", customers[0].Orders)
	}
	if customers[0].Spent.AmountMinor != 0 {
		t.Errorf("spent = %d on a cancelled order", customers[0].Spent.AmountMinor)
	}

	byEmail, _, err := app.Order().Customers(ctx, CustomerQuery{Search: "GONE@example"})
	if err != nil {
		t.Fatalf("search by email: %v", err)
	}
	if len(byEmail) != 1 || byEmail[0].Email != "gone@example.com" {
		t.Errorf("case-insensitive email search returned %+v", byEmail)
	}

	none, _, err := app.Order().Customers(ctx, CustomerQuery{Search: "nobody-here"})
	if err != nil {
		t.Fatalf("search for nobody: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("a search that matches nothing returned %d", len(none))
	}
}

// The route is admin-only, like everything else that reads the whole book.
func TestCustomersRouteNeedsAdmin(t *testing.T) {
	app := newTestApp(t)
	rec := do(t, app, "GET", "/api/admin/customers")
	if rec.Code != 401 {
		t.Errorf("unauthenticated = %d, want 401", rec.Code)
	}
	rec = do(t, app, "GET", "/api/admin/customers", withAdmin)
	if rec.Code != 200 {
		t.Fatalf("admin = %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"data"`) {
		t.Errorf("body has no data envelope: %s", rec.Body)
	}
}

// ------------------------------------------------------------------ the CSV

// The mailing list. A shop asks this screen for one constantly and the panel
// could not produce it: there was no customers export at all, only products and
// orders.
//
// What it has to pin is that the file says the same thing the screen does.
// Nothing stops an exporter re-deriving "spent" with its own SQL, and once two
// copies of that expression exist they drift — so this compares the figures in
// the file against the reading the listing serves.
func TestCustomersExportIsTheSameReadingAsTheListing(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "CSV-CUST", 2500, 50)
	variant := product.DefaultVariant()
	address := Address{Line1: "9 Export Way", City: "Filetown", State: "KA",
		PostalCode: "56001", Country: "IN"}

	place := func(email, name, phone string, qty int) *Order {
		t.Helper()
		res, err := app.Order().Create(ctx, NewOrderInput{
			Email: email, Name: name, Phone: phone, Address: address,
			Lines: []NewOrderLine{{VariantID: variant.ID, Quantity: qty}},
		})
		if err != nil {
			t.Fatalf("place for %s: %v", email, err)
		}
		return res.Order
	}

	paid := place("Buyer@Example.com", "Bea Buyer", "+91 555 0100", 2)
	place("buyer@example.com", "Bea Buyer", "+91 555 0100", 1)
	place("browser@elsewhere.test", "Cal Browser", "", 1)
	if _, err := app.Pay().MarkPaid(ctx, paid.ID, "cash"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}

	rows := exportedCustomers(t, app, CustomerQuery{})
	if len(rows) != 2 {
		t.Fatalf("exported %d rows, want 2 people — the two spellings are one customer: %v", len(rows), rows)
	}

	bea := rows["buyer@example.com"]
	if bea == nil {
		t.Fatalf("the grouped customer is missing from the file: %v", rows)
	}
	if bea["orders"] != "2" {
		t.Errorf("orders = %q, want 2", bea["orders"])
	}
	// 2 x 2500, and only the paid order counts. Minor units and a currency
	// code, never a formatted amount: a spreadsheet cannot add up "₹50.00".
	if bea["spent_minor"] != "5000" {
		t.Errorf("spent_minor = %q, want 5000", bea["spent_minor"])
	}
	if bea["currency"] != app.cfg.Currency {
		t.Errorf("currency = %q, want %q", bea["currency"], app.cfg.Currency)
	}
	if bea["name"] != "Bea Buyer" {
		t.Errorf("name = %q, want the most recent order's", bea["name"])
	}
	// Escaped, and it has to be: a cell opening with `+` is a formula to a
	// spreadsheet, and a mailing list is full of international phone numbers.
	// escapeRecord does this for every export and the import strips it again.
	if bea["phone"] != "'+91 555 0100" {
		t.Errorf("phone = %q, want the number with the formula escape in front of it", bea["phone"])
	}
	if bea["city"] != "Filetown" || bea["country"] != "IN" || bea["postal_code"] != "56001" {
		t.Errorf("the address did not survive the file: %v", bea)
	}
	if bea["first_order_at"] == "" || bea["last_order_at"] == "" {
		t.Errorf("the dates are empty: %v", bea)
	}

	// The same numbers the screen shows, taken from the service rather than
	// recomputed here. This is the assertion that catches the two expressions
	// drifting apart.
	listed, _, err := app.Order().Customers(ctx, CustomerQuery{})
	if err != nil {
		t.Fatalf("customers: %v", err)
	}
	for _, c := range listed {
		row := rows[c.Email]
		if row == nil {
			t.Errorf("%s is on the screen and not in the file", c.Email)
			continue
		}
		if row["orders"] != strconv.Itoa(c.Orders) {
			t.Errorf("%s: file says %q orders, the listing says %d", c.Email, row["orders"], c.Orders)
		}
		if row["spent_minor"] != strconv.FormatInt(c.Spent.AmountMinor, 10) {
			t.Errorf("%s: file says %q spent, the listing says %d",
				c.Email, row["spent_minor"], c.Spent.AmountMinor)
		}
	}
}

// A filter honoured by the screen and ignored by the button is the trap
// TestExportOrdersHonoursTheSameSearch names: an operator who searched, saw one
// customer and pressed Export must not walk away with the whole book.
func TestCustomersExportHonoursTheSameSearch(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "CSV-SEARCH", 1000, 50)
	variant := product.DefaultVariant()
	address := Address{Line1: "1 Test Street", City: "Testville", PostalCode: "12345", Country: "US"}
	for _, who := range []struct{ email, name string }{
		{"petra@example.com", "Petra Molnar"},
		{"quentin@elsewhere.test", "Quentin Vance"},
	} {
		if _, err := app.Order().Create(ctx, NewOrderInput{
			Email: who.email, Name: who.name, Address: address,
			Lines: []NewOrderLine{{VariantID: variant.ID, Quantity: 1}},
		}); err != nil {
			t.Fatalf("place for %s: %v", who.email, err)
		}
	}

	byName := exportedCustomers(t, app, CustomerQuery{Search: "petra"})
	if len(byName) != 1 || byName["petra@example.com"] == nil {
		t.Errorf("a name search exported %v, want only Petra", byName)
	}
	byDomain := exportedCustomers(t, app, CustomerQuery{Search: "elsewhere.test"})
	if len(byDomain) != 1 || byDomain["quentin@elsewhere.test"] == nil {
		t.Errorf("an email fragment exported %v, want only Quentin", byDomain)
	}
}

// data.export, and the file is a file. The 400 matters more than it looks: once
// a CSV header has been written the status line is spent, so a bad sort has to
// be refused before the first byte or it arrives as a broken download.
func TestTheCustomerExportRouteIsGatedAndStreams(t *testing.T) {
	app := newTestApp(t)
	owner := signInAs(t, app, "owner@example.com", RoleOwner)
	manager := signInAs(t, app, "manager@example.com", RoleManager)

	rec := do(t, app, "GET", "/api/admin/export/admin-customers", bearer(manager))
	if rec.Code != http.StatusForbidden {
		t.Errorf("manager = %d, want 403 — a manager reads customers but does not take the book home", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), string(RightDataExport)) {
		t.Errorf("the refusal does not name data.export: %s", rec.Body.String())
	}

	rec = do(t, app, "GET", "/api/admin/export/admin-customers", bearer(owner))
	if rec.Code != http.StatusOK {
		t.Fatalf("owner = %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type = %q, want text/csv", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "customers-") {
		t.Errorf("Content-Disposition = %q, want a customers-<date>.csv filename", cd)
	}
	if !strings.HasPrefix(rec.Body.String(), strings.Join(customerCSVHeader, ",")) {
		t.Errorf("the file does not open with the header: %q", rec.Body.String())
	}

	rec = do(t, app, "GET", "/api/admin/export/admin-customers?sort=nonsense", bearer(owner))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a bad sort = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "email,name") {
		t.Errorf("the refusal arrived as a CSV: %s", rec.Body.String())
	}
}

// exportedCustomers runs the export and returns each row as a column map keyed
// by email, so an assertion names a column rather than an index.
func exportedCustomers(t *testing.T, app *App, q CustomerQuery) map[string]map[string]string {
	t.Helper()
	var buf bytes.Buffer
	if err := app.transfer.ExportCustomers(context.Background(), &buf, q); err != nil {
		t.Fatalf("export customers: %v", err)
	}
	records, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("the export is not valid CSV: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("the export is empty; it must at least carry its header")
	}
	header := records[0]
	if strings.Join(header, ",") != strings.Join(customerCSVHeader, ",") {
		t.Fatalf("header = %v, want %v", header, customerCSVHeader)
	}
	out := map[string]map[string]string{}
	for _, record := range records[1:] {
		row := map[string]string{}
		for i, cell := range record {
			row[header[i]] = cell
		}
		out[row["email"]] = row
	}
	return out
}
