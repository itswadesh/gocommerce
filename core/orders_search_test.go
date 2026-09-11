package gocommerce

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// An operator with a customer on the telephone searches by whatever the
// customer just said: the number off the receipt, half an address, a surname.
// These tests pin all three, and pin the exact filter beside them that the
// Customers screen links with and means one person by.

// placeSearchable checks out one order with a chosen email and name.
func placeSearchable(t *testing.T, app *App, sku, email, name string) *Order {
	t.Helper()
	ctx := context.Background()
	product := simpleProduct(t, app, sku, 1000, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	in := checkoutInput(cart.Token)
	in.Email, in.Name = email, name
	result, err := app.Order().Checkout(ctx, CodeCOD, in, "")
	if err != nil {
		t.Fatalf("checkout %s: %v", sku, err)
	}
	return result.Order
}

func searchOrders(t *testing.T, app *App, q OrderQuery) ([]*Order, int) {
	t.Helper()
	orders, total, err := app.Order().List(context.Background(), q)
	if err != nil {
		t.Fatalf("list orders: %v", err)
	}
	if len(orders) != total {
		t.Errorf("the page holds %d orders and the total says %d — the count and the page "+
			"must run under the same WHERE clause", len(orders), total)
	}
	return orders, total
}

func numbersOf(orders []*Order) []string {
	out := make([]string, 0, len(orders))
	for _, o := range orders {
		out = append(out, o.Number)
	}
	return out
}

func TestOrderSearchMatchesNumberEmailAndName(t *testing.T) {
	app := newTestApp(t)

	regular := placeSearchable(t, app, "SEARCH-1", "regular@example.com", "Petra Molnar")
	placeSearchable(t, app, "SEARCH-2", "other@elsewhere.test", "Quentin Vance")
	placeSearchable(t, app, "SEARCH-3", "third@elsewhere.test", "Rosa Iqbal")

	for _, tc := range []struct {
		name   string
		needle string
		want   int
	}{
		{"the whole number off a receipt", regular.Number, 1},
		// Lower-cased: the operator types what they read, not what the column holds.
		{"the number, case folded", strings.ToLower(regular.Number), 1},
		// A prefix is a prefix. Everything the store has sold shares this one,
		// which is the honest answer rather than a bug.
		{"the prefix every number shares", "gc-", 3},
		{"a fragment of an email", "regular@ex", 1},
		{"a fragment of a name", "ular", 1},
		{"a domain two orders share", "elsewhere.test", 2},
		{"nothing at all", "nobody-here", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orders, total := searchOrders(t, app, OrderQuery{Search: tc.needle})
			if total != tc.want {
				t.Fatalf("searching %q found %v, want %d matches", tc.needle, numbersOf(orders), tc.want)
			}
		})
	}
}

func TestOrderSearchAnchorsTheNumberAndLeavesEmailExact(t *testing.T) {
	app := newTestApp(t)
	first := placeSearchable(t, app, "ANCHOR-1", "regular@example.com", "Petra Molnar")
	placeSearchable(t, app, "ANCHOR-2", "other@elsewhere.test", "Quentin Vance")

	// A needle that sits inside a number but not at its start finds nothing: an
	// order number is read off a receipt from the left, and a contains match on
	// it would make "1" find half the table.
	inside := first.Number[2:]
	if orders, total := searchOrders(t, app, OrderQuery{Search: inside}); total != 0 {
		t.Errorf("searching %q (inside %s, not its start) found %v — the number clause must be anchored",
			inside, first.Number, numbersOf(orders))
	}

	// The exact filter is not widened by any of this. The Customers screen links
	// with a whole address and means that one person, and `_` — a LIKE
	// single-character wildcard — is common in real addresses.
	if orders, total := searchOrders(t, app, OrderQuery{Email: "ular@example.com"}); total != 0 {
		t.Errorf("?email=ular@example.com found %v; the exact filter must stay equality", numbersOf(orders))
	}
	if _, total := searchOrders(t, app, OrderQuery{Email: "REGULAR@example.com"}); total != 1 {
		t.Errorf("the exact filter must still fold case, got %d matches", total)
	}

	// Search AND Email, never OR: two filters that disagree return nothing.
	if orders, total := searchOrders(t, app, OrderQuery{
		Search: "Quentin", Email: "regular@example.com",
	}); total != 0 {
		t.Errorf("search and email must AND; found %v", numbersOf(orders))
	}
	if _, total := searchOrders(t, app, OrderQuery{
		Search: "Petra", Email: "regular@example.com",
	}); total != 1 {
		t.Errorf("search and email naming the same order must find it, got %d", total)
	}
}

func TestOrdersRouteSearchesByQ(t *testing.T) {
	app := newTestApp(t)
	placeSearchable(t, app, "ROUTE-1", "regular@example.com", "Petra Molnar")
	placeSearchable(t, app, "ROUTE-2", "other@elsewhere.test", "Quentin Vance")

	if rec := do(t, app, "GET", "/api/admin/orders?q=petra"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated search = %d, want 401", rec.Code)
	}

	rec := do(t, app, "GET", "/api/admin/orders?q=petra", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []struct {
			Number string `json:"number"`
			Email  string `json:"email"`
		} `json:"data"`
		Meta ListMeta `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Meta.Total != 1 || len(body.Data) != 1 {
		t.Fatalf("?q=petra returned %d rows and a total of %d, want one of each — the parameter "+
			"has to be read by the handler, not merely exist on the struct", len(body.Data), body.Meta.Total)
	}
	if body.Data[0].Email != "regular@example.com" {
		t.Errorf("found %s, want the order Petra placed", body.Data[0].Email)
	}

	// A search that matches nothing is an empty page, never a 404: "no such
	// order" and "nothing matched what you typed" are different answers.
	rec = do(t, app, "GET", "/api/admin/orders?q=nobody-here", withAdmin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Errorf("an empty search = %d: %s", rec.Code, rec.Body.String())
	}
}

// A field honoured by one of a struct's consumers is a trap: an operator who
// searches, sees three orders and clicks Export must not get the whole table.
func TestExportOrdersHonoursTheSameSearch(t *testing.T) {
	app := newTestApp(t)
	wanted := placeSearchable(t, app, "EXPORT-1", "regular@example.com", "Petra Molnar")
	other := placeSearchable(t, app, "EXPORT-2", "other@elsewhere.test", "Quentin Vance")

	var buf bytes.Buffer
	if err := app.transfer.ExportOrders(context.Background(), &buf, OrderQuery{
		Search: wanted.Number,
	}); err != nil {
		t.Fatalf("export: %v", err)
	}
	csv := buf.String()
	if !strings.Contains(csv, wanted.Number) {
		t.Errorf("the export dropped the order that was searched for:\n%s", csv)
	}
	if strings.Contains(csv, other.Number) {
		t.Errorf("the export carried %s, which the search excluded:\n%s", other.Number, csv)
	}

	buf.Reset()
	if err := app.transfer.ExportOrders(context.Background(), &buf, OrderQuery{
		Search: "elsewhere.test",
	}); err != nil {
		t.Fatalf("export by email fragment: %v", err)
	}
	if !strings.Contains(buf.String(), other.Number) || strings.Contains(buf.String(), wanted.Number) {
		t.Errorf("an email fragment exported the wrong rows:\n%s", buf.String())
	}
}
