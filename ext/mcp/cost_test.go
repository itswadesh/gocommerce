package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// get_product hands an agent the whole product, and the agent is the store's
// operator whichever way it connects — over the admin route, or as the
// binary's own stdio. Both see the cost price, as the panel does.
func TestAnAgentSeesWhatTheShopPaidAsThePanelDoes(t *testing.T) {
	m := New(Config{})
	app := gctest.New(t, m)
	p := gctest.CreateProduct(t, app, "MCP-COST", 2500, 3)
	if _, err := app.Products().UpdateVariant(context.Background(), p.Variants[0].ID,
		gocommerce.VariantPatch{CostMinor: gocommerce.SetAmount(1900)}); err != nil {
		t.Fatal(err)
	}
	const want = `"cost":{"amount_minor":1900`

	if text, isError := callTool(t, app, "get_product", map[string]any{"id": p.ID}); isError || !strings.Contains(compact(t, text), want) {
		t.Errorf("get_product over the admin route = %s, want the cost", text)
	}

	ctx, err := stdioContext(app)
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := m.dispatch(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_product","arguments":{"id":`+
		strconv.FormatInt(p.ID, 10)+`}}}`))
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Result.Content) == 0 {
		t.Fatalf("stdio get_product = %s (%v)", raw, err)
	}
	if text := out.Result.Content[0].Text; !strings.Contains(compact(t, text), want) {
		t.Errorf("get_product over stdio = %s, want the cost", text)
	}
}

// compact undoes the indentation a tool's text is written with.
func compact(t *testing.T, text string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(text)); err != nil {
		t.Fatalf("tool text is not JSON: %v: %s", err, text)
	}
	return buf.String()
}
