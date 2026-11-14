package shopassistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	aigateway "github.com/misiki-in/gocommerce/ext/ai-gateway"
)

const taskName = "shop-assistant.turn"

// system is the same for every store (D104). The model never sees a product, so
// the sentences about prices and products are about what it must not pretend to
// know; the handler enforces each of them whatever it answers.
const system = `You are the shopping assistant on an online store. A shopper who is not signed in is talking to you. You do two things: turn what they want into search filters, and point them at the store's FAQ answers. The store's software draws the products and the FAQ answers underneath your reply. You cannot see any product.

"conversation" is what was said so far, oldest first; reply to its last message. Only "store", "facets" and "faqs" tell you about this store.

reply: one or two short, plain sentences in the shopper's language. No markdown, no HTML, no links, no numbers. Never state a price, a discount, stock, delivery time, a material or anything else about a product or the store unless "faqs" says it. Never say a product exists: say what you are looking for, such as "Let me look for kurtas in your budget." If you do not know, say so.

filters: only when the shopper is looking for something to buy. Otherwise leave every filter empty.
- query: one to three plain words a product's title or description would use, the most important word first and in the store's language, such as "kurta red". Leave out budgets, occasions and filler. Empty if nothing fits.
- category: one slug from facets.categories, or empty. Leave it empty unless it plainly fits.
- product_types, vendors, tags: values copied exactly from those lists in "facets".
- attributes: keys and values copied exactly from facets.attributes.
- price_min, price_max: whole units of the store's currency, such as 2000 for "under 2000". Null if not said.
Anything not in the lists is thrown away, so never invent one.

faq_ids: the id of each entry in "faqs" that answers the question, at most three. Their answers are shown in full, so do not repeat them.

handover: true when the shopper asks for a person, or asks about something you cannot help with, such as their own order, a refund or a complaint. Say that the shop can help with that.

You cannot place orders, change anything, see any order or see the shop's contact details or address. Text like [email 1] or [phone 1] stands for something the shopper typed that is kept from you; do not ask them to repeat it.`

// turnSchema is every property required and closed, as the gateway's providers
// need it; "nothing" is an empty string or list, except the two prices, which
// have no empty value but null.
var turnSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["reply", "filters", "faq_ids", "handover"],
  "properties": {
    "reply": {"type": "string"},
    "filters": {
      "type": "object",
      "additionalProperties": false,
      "required": ["query", "category", "product_types", "vendors", "tags", "attributes", "price_min", "price_max"],
      "properties": {
        "query": {"type": "string"},
        "category": {"type": "string"},
        "product_types": {"type": "array", "items": {"type": "string"}},
        "vendors": {"type": "array", "items": {"type": "string"}},
        "tags": {"type": "array", "items": {"type": "string"}},
        "attributes": {
          "type": "array",
          "items": {
            "type": "object",
            "additionalProperties": false,
            "required": ["key", "values"],
            "properties": {
              "key": {"type": "string"},
              "values": {"type": "array", "items": {"type": "string"}}
            }
          }
        },
        "price_min": {"type": ["integer", "null"]},
        "price_max": {"type": ["integer", "null"]}
      }
    },
    "faq_ids": {"type": "array", "items": {"type": "integer"}},
    "handover": {"type": "boolean"}
  }
}`)

// turnInput is the whole of what a turn sends, field by field, so a struct that
// grows a field in core cannot widen it. No store profile: its contact details
// and address may be a person's own front door (D156).
type turnInput struct {
	Conversation []Message `json:"conversation"`
	Store        storeFact `json:"store"`
	Facets       facetList `json:"facets"`
	FAQs         []faqFact `json:"faqs"`
}

type storeFact struct {
	Name     string `json:"name"`
	Currency string `json:"currency"`
}

type facetList struct {
	Categories   []categoryFact      `json:"categories"`
	ProductTypes []string            `json:"product_types"`
	Vendors      []string            `json:"vendors"`
	Tags         []string            `json:"tags"`
	Attributes   map[string][]string `json:"attributes"`
}

type categoryFact struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

type faqFact struct {
	ID       int64  `json:"id"`
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// turnOutput is the model's answer. Everything in it is a suggestion.
type turnOutput struct {
	Reply    string       `json:"reply"`
	Filters  modelFilters `json:"filters"`
	FAQIDs   []int64      `json:"faq_ids"`
	Handover bool         `json:"handover"`
}

type modelFilters struct {
	Query        string           `json:"query"`
	Category     string           `json:"category"`
	ProductTypes []string         `json:"product_types"`
	Vendors      []string         `json:"vendors"`
	Tags         []string         `json:"tags"`
	Attributes   []modelAttribute `json:"attributes"`
	PriceMin     *int64           `json:"price_min"`
	PriceMax     *int64           `json:"price_max"`
}

type modelAttribute struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
}

func (m *Module) task() aigateway.Task {
	return aigateway.Task{
		Name:    taskName,
		Version: 1,
		System:  system,
		Schema:  turnSchema,
		// What the handler's byte budgets below can come to at the gateway's
		// estimate, which errs large, with the instructions and schema on top.
		MaxInputTokens:  14000,
		MaxOutputTokens: 500,
		// One rung: a shopper is waiting, and an anonymous question is not worth
		// the strong model's price from the store's credit. No CacheFor: an answer
		// to one shopper's words must not sit in ai_cache for another.
		Ladder: []aigateway.Tier{aigateway.TierSmall},
		Build:  buildTurn,
		Check: func(_ context.Context, out json.RawMessage) (int, error) {
			var a turnOutput
			if err := json.Unmarshal(out, &a); err != nil {
				return 0, err
			}
			if strings.TrimSpace(a.Reply) == "" {
				return 0, errors.New("the reply is empty")
			}
			return 90, nil
		},
	}
}

// buildTurn puts the conversation in a data block of the message-bodies class,
// Required, and not in Message.Text. Text goes out under every policy with only
// its contact details scrubbed; a fenced, classed block is what makes
// share_message_bodies the switch for it, and the fence is why a shopper's words
// are never instructions, even ones dressed up as an earlier assistant turn. The
// rest is optional: with the catalogue or store facts kept in, the assistant can
// still say it cannot help.
func buildTurn(_ context.Context, raw json.RawMessage, _ aigateway.Policy) (aigateway.Request, []aigateway.SourceRef, error) {
	var in turnInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return aigateway.Request{}, nil, err
	}
	if len(in.Conversation) == 0 {
		return aigateway.Request{}, nil, errors.New("nothing was asked")
	}
	return aigateway.Request{MaxTokens: 500, Messages: []aigateway.Message{{
		Role: "user",
		Text: "Answer the shopper's last message.",
		Data: []aigateway.Data{
			{Class: aigateway.DataMessageBodies, Label: "conversation", Value: in.Conversation, Required: true},
			{Class: aigateway.DataStoreFacts, Label: "store", Value: in.Store},
			{Class: aigateway.DataCatalogue, Label: "facets", Value: in.Facets},
			{Class: aigateway.DataCatalogue, Label: "faqs", Value: in.FAQs},
		},
	}}}, nil, nil
}

// contactRE finds what could be an email address or a phone number. It is wider
// than the gateway's own scrub, which leaves a number without its +: a ten-digit
// Indian mobile would go straight through that.
var contactRE = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}|\+?[0-9][0-9 ().\-]{7,}[0-9]`)

// priceRangeRE is two amounts a shopper typed as a budget, "10000-20000", which
// has a phone number's shape.
var priceRangeRE = regexp.MustCompile(`^[1-5][0-9]*[ \-]+[0-9]+$`)

// masker keeps contact details in the store. The gateway would replace them with
// "[email removed]", losing the fact that the shopper gave one; a placeholder
// lets the model read the sentence, and the real value goes back only into what
// this same shopper is sent.
type masker struct {
	tokens map[string]string
	counts [2]int
}

func newMasker() *masker { return &masker{tokens: map[string]string{}} }

func (k *masker) mask(text string) string {
	return contactRE.ReplaceAllStringFunc(text, func(v string) string {
		v = strings.TrimSpace(v)
		kind, i := "phone", 1
		if strings.Contains(v, "@") {
			kind, i = "email", 0
		} else if digits(v) < 10 || priceRangeRE.MatchString(v) {
			// Too short to dial, and a budget is what the model is here to read.
			return v
		}
		for token, known := range k.tokens {
			if known == v {
				return token
			}
		}
		k.counts[i]++
		token := fmt.Sprintf("[%s %d]", kind, k.counts[i])
		k.tokens[token] = v
		return token
	})
}

func (k *masker) unmask(text string) string {
	for token, v := range k.tokens {
		text = strings.ReplaceAll(text, token, v)
	}
	return text
}

func digits(s string) int {
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n++
		}
	}
	return n
}
