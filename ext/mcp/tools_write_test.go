package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// The tools added for agents that build and run a catalogue rather than only
// read one. Each test asserts the store actually changed, read back through the
// domain service — a tool that returns a cheerful JSON blob and writes nothing
// would pass a test that only checked the reply.

func TestAgentCreatesAProduct(t *testing.T) {
	app := gctest.New(t, New(Config{}))

	text, isErr := callTool(t, app, "create_product", map[string]any{
		"title":       "Agent kettle",
		"description": "Made by an agent.",
		"status":      "active",
		"price_minor": 4500,
		"sku":         "AGENT-KETTLE",
		"stock":       7,
	})
	if isErr {
		t.Fatalf("create_product should succeed: %s", text)
	}

	products, _, err := app.Products().ListProducts(t.Context(), gocommerce.ProductQuery{Search: "Agent kettle", Limit: 10})
	if err != nil {
		t.Fatalf("list products: %v", err)
	}
	if len(products) != 1 {
		t.Fatalf("the store holds %d matching products, want 1", len(products))
	}
	got := products[0]
	if got.Title != "Agent kettle" {
		t.Errorf("title = %q, want %q", got.Title, "Agent kettle")
	}
	if len(got.Variants) == 0 {
		t.Fatal("a product created with a price should have a variant to carry it")
	}
	if got.Variants[0].Price.AmountMinor != 4500 {
		t.Errorf("price = %d minor, want 4500", got.Variants[0].Price.AmountMinor)
	}
}

func TestAgentUpdatesAProduct(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	product := gctest.CreateProduct(t, app, "AGENT-FIXTURE", 2500, 10)

	text, isErr := callTool(t, app, "update_product", map[string]any{
		"product_id": product.ID,
		"title":      "Renamed by an agent",
		"status":     "draft",
	})
	if isErr {
		t.Fatalf("update_product should succeed: %s", text)
	}

	after, err := app.Products().GetProduct(t.Context(), product.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if after.Title != "Renamed by an agent" {
		t.Errorf("title = %q, want it renamed", after.Title)
	}
	if after.Status != "draft" {
		t.Errorf("status = %q, want draft", after.Status)
	}
}

func TestAgentSetsAVariantPrice(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	product := gctest.CreateProduct(t, app, "AGENT-FIXTURE", 2500, 10)
	variant := product.Variants[0]

	text, isErr := callTool(t, app, "set_variant_price", map[string]any{
		"variant_id":  variant.ID,
		"price_minor": 9999,
	})
	if isErr {
		t.Fatalf("set_variant_price should succeed: %s", text)
	}

	after, err := app.Products().GetProduct(t.Context(), product.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if after.Variants[0].Price.AmountMinor != 9999 {
		t.Errorf("price = %d minor, want 9999", after.Variants[0].Price.AmountMinor)
	}
}

// Money crosses this boundary as integer minor units, and an agent that sends a
// price as a float is the likeliest way that rule gets broken from outside. A
// fractional minor unit is not a price and must be refused rather than
// silently truncated to a different one.
func TestAgentCannotSetAFractionalPrice(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	product := gctest.CreateProduct(t, app, "AGENT-FIXTURE", 2500, 10)

	text, isErr := callTool(t, app, "set_variant_price", map[string]any{
		"variant_id":  product.Variants[0].ID,
		"price_minor": 19.99,
	})
	if !isErr {
		t.Fatalf("a fractional price_minor should be refused, got: %s", text)
	}

	after, err := app.Products().GetProduct(t.Context(), product.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if after.Variants[0].Price.AmountMinor != product.Variants[0].Price.AmountMinor {
		t.Error("a refused price change must leave the price alone")
	}
}

func TestAgentCreatesADiscount(t *testing.T) {
	app := gctest.New(t, New(Config{}))

	text, isErr := callTool(t, app, "create_discount", map[string]any{
		"code":     "AGENT10",
		"title":    "Agent ten percent",
		"kind":     "percentage",
		"value_bp": 1000,
	})
	if isErr {
		t.Fatalf("create_discount should succeed: %s", text)
	}

	got, err := app.Discounts().GetByCode(t.Context(), "AGENT10")
	if err != nil {
		t.Fatalf("the discount should exist: %v", err)
	}
	if got.ValueBP != 1000 {
		t.Errorf("value = %d bp, want 1000", got.ValueBP)
	}
}

func TestAgentRefundsAnOrder(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	placed := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	if _, err := app.Pay().MarkPaid(t.Context(), placed.Order.ID, "cash"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}

	// Cash on delivery cannot refund — it has no provider to refund through —
	// so the agent must be told that rather than left believing money moved.
	text, isErr := callTool(t, app, "refund_order", map[string]any{
		"order_id": placed.Order.ID,
		"reason":   "agent goodwill",
	})
	if !isErr {
		t.Fatalf("refunding a COD order should be refused, got: %s", text)
	}
	if !strings.Contains(strings.ToLower(text), "refund") {
		t.Errorf("the refusal should say what it is about: %s", text)
	}
}

func TestAgentReadsCustomersAndSales(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)

	text, isErr := callTool(t, app, "list_customers", map[string]any{"limit": 10})
	if isErr {
		t.Fatalf("list_customers should succeed: %s", text)
	}
	var customers struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal([]byte(text), &customers); err != nil {
		t.Fatalf("list_customers should return JSON: %v (%s)", err, text)
	}
	if customers.Total < 1 {
		t.Errorf("a store with an order should report at least one customer, got %d", customers.Total)
	}

	text, isErr = callTool(t, app, "sales_report", map[string]any{"group_by": "day"})
	if isErr {
		t.Fatalf("sales_report should succeed: %s", text)
	}
	if !strings.Contains(text, "currency") {
		t.Errorf("a sales report should name the currency it is counting in: %s", text)
	}
}

// ReadOnly is the switch an operator uses while deciding how far to trust an
// agent. Every tool added here that changes state has to be behind it, or the
// switch quietly stops meaning what it says.
func TestReadOnlyWithholdsEveryNewWriteTool(t *testing.T) {
	app := gctest.New(t, New(Config{ReadOnly: true}))

	result := rpc(t, app, "tools/list", nil)
	tools, _ := result["tools"].([]any)
	present := map[string]bool{}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		name, _ := tool["name"].(string)
		present[name] = true
	}

	for _, name := range []string{
		"create_product", "update_product", "set_variant_price",
		"create_discount", "refund_order",
	} {
		if present[name] {
			t.Errorf("%s changes state and must not be offered when ReadOnly is set", name)
		}
	}
	for _, name := range []string{"list_customers", "sales_report"} {
		if !present[name] {
			t.Errorf("%s only reads and should still be offered when ReadOnly is set", name)
		}
	}
}
