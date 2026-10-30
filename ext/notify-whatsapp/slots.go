package whatsapp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Slot is one moment a store can send a template at.
type Slot struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Marketing is Meta's category for it, and the consent it needs: a basket
	// reminder is marketing, every order update utility.
	Marketing bool `json:"marketing"`
	// Buttons is how many quick-reply buttons its template must have, and
	// what each one means, in order.
	Buttons []string `json:"buttons,omitempty"`
	// Keys are the values a template variable can be filled with here.
	Keys []string `json:"keys"`
}

var orderKeys = []string{
	"customer_first_name", "customer_name", "order_number", "order_total", "items_summary",
	"tracking", "order_tracking_url", "store_name",
}

var cartKeys = []string{
	"customer_first_name", "cart_total", "summary", "recovery_url", "discount_code", "store_name",
}

// Slots, in the order the screen lists them.
var Slots = []Slot{
	{Key: "order_confirmation", Label: "Order confirmation", Keys: orderKeys},
	{Key: "cod_confirmation", Label: "Cash on delivery confirmation", Buttons: []string{"confirm", "cancel"}, Keys: orderKeys},
	{Key: "shipped", Label: "Shipped", Keys: orderKeys},
	{Key: "out_for_delivery", Label: "Out for delivery", Keys: orderKeys},
	{Key: "delivered", Label: "Delivered", Keys: orderKeys},
	{Key: "cart_reminder", Label: "Basket reminder", Marketing: true, Keys: cartKeys},
}

func slotByKey(key string) *Slot {
	for i := range Slots {
		if Slots[i].Key == key {
			return &Slots[i]
		}
	}
	return nil
}

// cartRecoveryPrefix is ext/cart-recovery's step events ("cart.recovery.1"),
// named here rather than imported: one module does not build on another.
const cartRecoveryPrefix = "cart.recovery."

// slotFor is which moment a message is. A cash order is confirmed by reply
// when the store mapped that template, and gets the plain confirmation
// otherwise.
func (m *Module) slotFor(ctx context.Context, n gocommerce.Notification) *Slot {
	switch {
	case n.Event == gocommerce.EventOrderCreated:
		if n.Data["payment_method"] == gocommerce.CodeCOD {
			if tpl, err := m.mapping(ctx, "cod_confirmation"); err == nil && tpl != nil {
				return slotByKey("cod_confirmation")
			}
		}
		return slotByKey("order_confirmation")
	case n.Event == gocommerce.EventOrderShipped:
		return slotByKey("shipped")
	case n.Event == gocommerce.EventOrderShipmentUpdated && n.Data["tracking_status"] == "out_for_delivery":
		return slotByKey("out_for_delivery")
	case n.Event == gocommerce.EventOrderDelivered:
		return slotByKey("delivered")
	case strings.HasPrefix(n.Event, cartRecoveryPrefix):
		return slotByKey("cart_reminder")
	}
	return nil
}

// Variable is one {{…}} of a template and the value that fills it.
type Variable struct {
	// Name is what the template calls it: "1" for {{1}}, "first_name" for a
	// named parameter.
	Name string `json:"name"`
	Key  string `json:"key"`
}

// mapping is a slot's template.
type mapping struct {
	Slot      string     `json:"slot"`
	Name      string     `json:"name"`
	Language  string     `json:"language"`
	Category  string     `json:"category"`
	Variables []Variable `json:"variables"`
}

func (m *Module) mapping(ctx context.Context, slot string) (*mapping, error) {
	var t mapping
	var vars []byte
	err := m.db.QueryRowContext(ctx, `
		SELECT slot, name, language, category, variables FROM whatsapp_templates WHERE slot = $1`, slot).
		Scan(&t.Slot, &t.Name, &t.Language, &t.Category, &vars)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(vars, &t.Variables); err != nil {
		return nil, err
	}
	return &t, nil
}

func (m *Module) mappings(ctx context.Context) (map[string]*mapping, error) {
	out := map[string]*mapping{}
	for _, s := range Slots {
		t, err := m.mapping(ctx, s.Key)
		if err != nil {
			return nil, err
		}
		if t != nil {
			out[s.Key] = t
		}
	}
	return out, nil
}

// enrich adds the values a message reads better with than the engine's flat
// data has: the total as the shopper reads it, a first name, the shop.
func (m *Module) enrich(ctx context.Context, in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+4)
	for k, v := range in {
		out[k] = v
	}
	if out["order_total"] == "" && out["total_minor"] != "" {
		if minor, err := strconv.ParseInt(out["total_minor"], 10, 64); err == nil {
			out["order_total"] = gocommerce.FormatMoney(minor, out["currency"])
		}
	}
	if out["customer_first_name"] == "" {
		first, _, _ := strings.Cut(strings.TrimSpace(out["customer_name"]), " ")
		out["customer_first_name"] = first
	}
	if out["store_name"] == "" {
		if p, err := m.app.Profile().Get(ctx); err == nil {
			out["store_name"] = p.Name
		}
	}
	return out
}

// param is a value as Meta takes a template parameter: one line, no tabs, no
// run of more than four spaces, never empty — Meta refuses the whole message
// for any of those.
func param(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	if v == "" {
		return "-"
	}
	if r := []rune(v); len(r) > 1000 {
		v = string(r[:1000])
	}
	return v
}

// templateMessage is the request body for one template, by the shape in Meta's
// "Send message templates" and "Template components" references: the body's
// parameters positional or named as the template declares them, and a
// cash-on-delivery confirmation's quick-reply buttons each given the payload
// Meta hands back when the shopper taps it.
func templateMessage(phone string, t *mapping, data map[string]string) map[string]any {
	var params []map[string]any
	for _, v := range t.Variables {
		p := map[string]any{"type": "text", "text": param(data[v.Key])}
		if _, err := strconv.Atoi(v.Name); err != nil {
			p["parameter_name"] = v.Name
		}
		params = append(params, p)
	}
	var components []map[string]any
	if len(params) > 0 {
		components = append(components, map[string]any{"type": "body", "parameters": params})
	}
	if s := slotByKey(t.Slot); s != nil {
		for i, b := range s.Buttons {
			components = append(components, map[string]any{
				"type": "button", "sub_type": "quick_reply", "index": strconv.Itoa(i),
				"parameters": []map[string]any{{"type": "payload", "payload": "cod:" + b + ":" + data["order_id"]}},
			})
		}
	}
	tpl := map[string]any{"name": t.Name, "language": map[string]any{"code": t.Language}}
	if len(components) > 0 {
		tpl["components"] = components
	}
	return map[string]any{
		"messaging_product": "whatsapp", "recipient_type": "individual",
		"to": phone, "type": "template", "template": tpl,
	}
}

// textMessage is a free-form reply, which WhatsApp delivers only inside the
// 24 hours after the shopper last wrote.
func textMessage(phone, body string) map[string]any {
	return map[string]any{
		"messaging_product": "whatsapp", "recipient_type": "individual",
		"to": phone, "type": "text", "text": map[string]any{"body": body},
	}
}

var variableRE = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_]+)\s*\}\}`)

// variablesIn are the distinct {{…}} of a template's text, in order.
func variablesIn(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range variableRE.FindAllStringSubmatch(text, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}
