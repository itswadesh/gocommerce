package whatsapp

// Suggestion is GoCommerce's wording for a slot, which a store may submit to
// Meta for review rather than writing its own. Meta approves or rejects it;
// nothing here assumes which.
type Suggestion struct {
	Name     string `json:"name"`
	Language string `json:"language"`
	Category string `json:"category"`
	Body     string `json:"body"`
	// Keys fill {{1}}, {{2}}, … in order, and are what the screen proposes
	// once Meta approves the template.
	Keys         []string `json:"keys"`
	Examples     []string `json:"examples"`
	QuickReplies []string `json:"quick_replies,omitempty"`
}

var suggestions = map[string]Suggestion{
	"order_confirmation": {
		Name: "gc_order_confirmation", Category: "UTILITY",
		Body:     "Hi {{1}}, thank you for your order {{2}} from {{3}}. Total: {{4}}. We will message you here when it ships.",
		Keys:     []string{"customer_first_name", "order_number", "store_name", "order_total"},
		Examples: []string{"Asha", "GC-000123", "Example Store", "₹1,499.00"},
	},
	"cod_confirmation": {
		Name: "gc_cod_confirmation", Category: "UTILITY",
		Body:         "Hi {{1}}, please confirm your cash on delivery order {{2}} from {{3}} for {{4}}. Tap a button below.",
		Keys:         []string{"customer_first_name", "order_number", "store_name", "order_total"},
		Examples:     []string{"Asha", "GC-000123", "Example Store", "₹1,499.00"},
		QuickReplies: []string{"Confirm order", "Cancel order"},
	},
	"shipped": {
		Name: "gc_order_shipped", Category: "UTILITY",
		Body:     "Hi {{1}}, your order {{2}} has shipped. Tracking: {{3}}",
		Keys:     []string{"customer_first_name", "order_number", "tracking"},
		Examples: []string{"Asha", "GC-000123", "AWB123456789"},
	},
	"out_for_delivery": {
		Name: "gc_out_for_delivery", Category: "UTILITY",
		Body:     "Hi {{1}}, your order {{2}} is out for delivery today.",
		Keys:     []string{"customer_first_name", "order_number"},
		Examples: []string{"Asha", "GC-000123"},
	},
	"delivered": {
		Name: "gc_order_delivered", Category: "UTILITY",
		Body:     "Hi {{1}}, your order {{2}} has been delivered. Thank you for shopping with {{3}}.",
		Keys:     []string{"customer_first_name", "order_number", "store_name"},
		Examples: []string{"Asha", "GC-000123", "Example Store"},
	},
	"cart_reminder": {
		Name: "gc_basket_reminder", Category: "MARKETING",
		Body:         "Hi {{1}}, you left {{2}} in your basket at {{3}}. Pick up where you left off: {{4}}",
		Keys:         []string{"customer_first_name", "summary", "store_name", "recovery_url"},
		Examples:     []string{"Asha", "1 x Linen shirt (M)", "Example Store", "https://shop.example/cart"},
		QuickReplies: []string{stopPromotions},
	},
}

func suggestionFor(slot string) *Suggestion {
	s, ok := suggestions[slot]
	if !ok {
		return nil
	}
	s.Language = "en"
	return &s
}

// request is the template as Meta's create call takes it: the body with an
// example for each variable, which review requires, and the buttons.
func (s *Suggestion) request() map[string]any {
	components := []map[string]any{{
		"type": "BODY", "text": s.Body,
		"example": map[string]any{"body_text": [][]string{s.Examples}},
	}}
	if len(s.QuickReplies) > 0 {
		var buttons []map[string]any
		for _, t := range s.QuickReplies {
			buttons = append(buttons, map[string]any{"type": "QUICK_REPLY", "text": t})
		}
		components = append(components, map[string]any{"type": "BUTTONS", "buttons": buttons})
	}
	return map[string]any{
		"name": s.Name, "language": s.Language, "category": s.Category, "components": components,
	}
}
