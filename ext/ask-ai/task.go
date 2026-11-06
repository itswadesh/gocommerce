package askai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	aigateway "github.com/itswadesh/gocommerce/ext/ai-gateway"
)

const taskName = "ask-ai.turn"

// system is the same for every store (D104): anything about one store goes in
// the fenced data of the request. It says only what the engine really does.
const system = `You are Ask AI, the assistant in the admin panel of an online store. You help the person signed in to set up their store and to change its details. Answer from the "store", "setup" and "screens" data and nothing else. If they do not tell you, say you do not know; never invent a screen, a feature, a price, a policy or a fact about the store.

You do two things.

1. Guide. Answer "how do I" and "what next" questions in short, plain words: at most 120 words of plain text, no markdown and no HTML. "setup" lists the steps a new store goes through, in order, with whether each is done; the next thing to do is the first that is not done and not locked. In "links", give the screens the person needs, each with a short label and an href copied exactly from "screens" or "setup". Any other link is thrown away.

2. Propose a change to the store's details, only when the person asks for one. Set "proposal" to {"action": "` + actionKind + `", "changes": [{"field": ..., "value": ...}]}, with only what they asked for. When they ask for no change, set it to {"action": "none", "changes": []}. The fields are: name, legal_name, email, phone, address_line1, address_line2, city, state, postal_code, country (two-letter ISO code), timezone (IANA name such as Asia/Kolkata), currency (three-letter ISO code), tax_id, support_url, prices_include_tax ("true" or "false"). You change nothing yourself: the panel shows the person the proposal with its current and new values, and nothing happens unless they press Apply. So say what you are proposing, and never say that a change has been made. "store" says whether their role may change these details; if it may not, tell them so and make no proposal.

You cannot change anything else, and there is no other action: not plugins, payment methods, API keys, the team, roles, domains, products, orders, refunds or money, and you cannot send a message to anybody. If asked, say so plainly and link the screen where they can do it themselves, if there is one.

You are not shown the store's contact details or address, only whether each is filled in. Text like [email 1] or [phone 1] stands for something the person typed that is kept from you. Use it exactly as written, including as a field's value.`

var turnSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["answer", "links", "proposal"],
  "properties": {
    "answer": {"type": "string"},
    "links": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["label", "href"],
        "properties": {"label": {"type": "string"}, "href": {"type": "string"}}
      }
    },
    "proposal": {
      "type": "object",
      "additionalProperties": false,
      "required": ["action", "changes"],
      "properties": {
        "action": {"type": "string"},
        "changes": {
          "type": "array",
          "items": {
            "type": "object",
            "additionalProperties": false,
            "required": ["field", "value"],
            "properties": {"field": {"type": "string"}, "value": {"type": "string"}}
          }
        }
      }
    }
  }
}`)

// turnInput is the whole of what a turn sends, field by field, so a struct that
// grows a field in core cannot widen it.
type turnInput struct {
	Messages []Message   `json:"messages"`
	Store    storeFacts  `json:"store"`
	Setup    []setupFact `json:"setup"`
	Screens  []Screen    `json:"screens"`
}

// storeFacts says what a store is without saying how to reach anybody: its
// legal name, contact details, address and tax id may be a person's own, so
// the model learns only whether each is filled in.
type storeFacts struct {
	Name             string `json:"name"`
	Country          string `json:"country"`
	Currency         string `json:"currency"`
	CurrencyFixed    bool   `json:"currency_fixed"`
	Timezone         string `json:"timezone"`
	PricesIncludeTax bool   `json:"prices_include_tax"`
	Filled           filled `json:"filled_in"`
	// PersonMayChange is whether the person asking holds store.write.
	PersonMayChange bool `json:"person_may_change_details"`
}

type filled struct {
	LegalName  bool `json:"legal_name"`
	Email      bool `json:"email"`
	Phone      bool `json:"phone"`
	Address    bool `json:"address"`
	TaxID      bool `json:"tax_id"`
	SupportURL bool `json:"support_url"`
}

type setupFact struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Why      string `json:"why"`
	Href     string `json:"href"`
	Required bool   `json:"required"`
	Done     bool   `json:"done"`
	Locked   bool   `json:"locked"`
}

// turnOutput is the model's answer.
type turnOutput struct {
	Answer   string         `json:"answer"`
	Links    []Link         `json:"links"`
	Proposal *modelProposal `json:"proposal"`
}

type modelProposal struct {
	Action  string        `json:"action"`
	Changes []fieldChange `json:"changes"`
}

func (m *Module) task() aigateway.Task {
	return aigateway.Task{
		Name:    taskName,
		Version: 1,
		System:  system,
		Schema:  turnSchema,
		// Ten turns of 1,000 characters each way come to about 5,000 tokens, and
		// the panel's screens to under 2,000. The output bound is well past 120
		// words, so a reply is not cut mid-sentence.
		MaxInputTokens:  10000,
		MaxOutputTokens: 800,
		// One rung: a person is waiting, and the strong model's price would come
		// off the same small trial for a question about a settings screen.
		Ladder: []aigateway.Tier{aigateway.TierSmall},
		Build:  buildTurn,
		Check: func(_ context.Context, out json.RawMessage) (int, error) {
			var a turnOutput
			if err := json.Unmarshal(out, &a); err != nil {
				return 0, err
			}
			if strings.TrimSpace(a.Answer) == "" {
				return 0, errors.New("the answer is empty")
			}
			return 90, nil
		},
	}
}

func buildTurn(_ context.Context, raw json.RawMessage, _ aigateway.Policy) (aigateway.Request, []aigateway.SourceRef, error) {
	var in turnInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return aigateway.Request{}, nil, err
	}
	req := aigateway.Request{MaxTokens: 800}
	for _, msg := range in.Messages {
		req.Messages = append(req.Messages, aigateway.Message{Role: msg.Role, Text: msg.Text})
	}
	if len(req.Messages) == 0 {
		return req, nil, errors.New("nothing was asked")
	}
	// None is Required: with store facts switched off Ask AI can still say what
	// it can do and that it cannot see this store.
	last := &req.Messages[len(req.Messages)-1]
	last.Data = []aigateway.Data{
		{Class: aigateway.DataStoreFacts, Label: "store", Value: in.Store},
		{Class: aigateway.DataStoreFacts, Label: "setup", Value: in.Setup},
		{Class: aigateway.DataStoreFacts, Label: "screens", Value: in.Screens},
	}
	return req, nil, nil
}

// contactRE finds what looks like an email address or a phone number in what a
// person typed. It is wider than the gateway's own scrub, which leaves a number
// without its +; a SKU caught here only costs a placeholder that is put back.
var contactRE = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}|\+?[0-9][0-9 ().\-]{7,}[0-9]`)

// masker keeps contact details in the store. The gateway would replace them
// with "[email removed]" (D107), which keeps them from the model but also loses
// them: "set my contact email to a@b.in" could never become a proposal. Here the
// model is given a placeholder it can repeat back, and the real value goes in
// only on the way out.
type masker struct {
	tokens map[string]string
	counts [2]int
}

func newMasker() *masker { return &masker{tokens: map[string]string{}} }

func (k *masker) mask(text string) string {
	return contactRE.ReplaceAllStringFunc(text, func(v string) string {
		v = strings.TrimSpace(v)
		for token, known := range k.tokens {
			if known == v {
				return token
			}
		}
		kind, i := "phone", 1
		if strings.Contains(v, "@") {
			kind, i = "email", 0
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
