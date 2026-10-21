package amazon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	aigateway "github.com/itswadesh/gocommerce/ext/ai-gateway"
)

// The rewrite as a task of the AI gateway (D107). It asks the same thing the
// environment's model is asked in optimize.go, with two differences that are
// the gateway's: the listing travels as catalogue data inside a fence rather
// than pasted into the prompt, and the answer's shape is the schema below,
// held by the provider and checked again on the way back, rather than JSON
// fished out of prose.

const rewriteTaskName = "product.rewrite"

const rewriteInstructions = `You write product pages for an independent online store. You are given a product listing scraped from a marketplace. Rewrite it for this store's own catalogue.

Rules:
- Use only facts present in the listing. Invent nothing: no claims, sizes, materials or compatibility the listing does not state.
- The title is what a shopper would search for: the product, its key distinguishing attribute, the brand if it matters. No marketplace keyword stuffing, no ALL CAPS, no trailing feature lists. Under 80 characters.
- The description is well-formed HTML using only <p>, <ul>, <li>, <strong>: two to four short paragraphs, then a bullet list of the concrete specifications worth knowing. Plain, specific, no filler, no exclamation marks.
- product_type is a two-or-three word category noun, e.g. "Wireless earbuds".
- vendor is the brand, or empty if the listing names none.
- tags are three to eight lowercase search terms a shopper might use.
- seo_title is under 60 characters; seo_description is under 155 and states what the product is and for whom.`

// rewriteSchema is the rewrite type's own shape: every field present, nothing
// else. Lengths are checked in Check rather than in the schema, because not
// every provider's strict mode accepts the length keywords.
var rewriteSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["title", "description", "product_type", "vendor", "tags", "seo_title", "seo_description"],
  "properties": {
    "title": {"type": "string"},
    "description": {"type": "string"},
    "product_type": {"type": "string"},
    "vendor": {"type": "string"},
    "tags": {"type": "array", "items": {"type": "string"}},
    "seo_title": {"type": "string"},
    "seo_description": {"type": "string"}
  }
}`)

// rewriteInput is what a job hands the task: the listing as the model may
// see it, and the ASIN the run's record names as its source.
type rewriteInput struct {
	ASIN    string         `json:"asin"`
	Listing map[string]any `json:"listing"`
}

func rewriteTask() aigateway.Task {
	return aigateway.Task{
		Name:    rewriteTaskName,
		Version: 1,
		System:  rewriteInstructions,
		Schema:  rewriteSchema,
		// The listing is trimmed to a 6,000-character description plus its
		// bullets and specifications; this holds the longest one seen with
		// room to spare, and refuses one that would cost more than a rewrite
		// is worth.
		MaxInputTokens:  8000,
		MaxOutputTokens: 2048,
		Ladder:          []aigateway.Tier{aigateway.TierSmall, aigateway.TierStrong},
		Build: func(ctx context.Context, input json.RawMessage, p aigateway.Policy) (aigateway.Request, []aigateway.SourceRef, error) {
			var in rewriteInput
			if err := json.Unmarshal(input, &in); err != nil {
				return aigateway.Request{}, nil, err
			}
			var refs []aigateway.SourceRef
			if in.ASIN != "" {
				refs = []aigateway.SourceRef{{Kind: "amazon_listing", ID: in.ASIN}}
			}
			return aigateway.Request{Messages: []aigateway.Message{{
				Role: "user",
				Text: "Rewrite this listing for the store's catalogue.",
				// A rewrite is nothing but catalogue text: with the catalogue
				// kept in, the gateway stops rather than send the bare
				// instruction.
				Data: []aigateway.Data{{Class: aigateway.DataCatalogue, Label: "listing", Value: in.Listing, Required: true}},
			}}}, refs, nil
		},
		Check: func(ctx context.Context, out json.RawMessage) (int, error) {
			_, err := decodeRewrite(out)
			if err != nil {
				return 0, err
			}
			return 100, nil
		},
	}
}

// decodeRewrite reads the answer into the importer's own type, refusing a
// field it does not have, and holds it to what the instructions asked.
func decodeRewrite(out json.RawMessage) (*rewrite, error) {
	var r rewrite
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, err
	}
	if strings.TrimSpace(r.Title) == "" && strings.TrimSpace(r.Description) == "" {
		return nil, errors.New("the answer has neither a title nor a description")
	}
	// Generous against the instructions' 80: a little over is a title, a
	// paragraph is not.
	if n := len([]rune(r.Title)); n > 200 {
		return nil, fmt.Errorf("the title is %d characters", n)
	}
	return &r, nil
}

// rewriteThroughGateway runs the task now. The import is already a worker of
// its own, holding no transaction, so Run is the door rather than Enqueue.
func (m *Module) rewriteThroughGateway(ctx context.Context, l *Listing) (*rewrite, error) {
	res, err := m.cfg.Gateway.Run(ctx, rewriteTaskName, rewriteInput{ASIN: l.ASIN, Listing: trimmed(l)})
	if err != nil {
		var re *aigateway.RunError
		if errors.As(err, &re) && re.Outcome == aigateway.OutcomeBudgetExhausted {
			return nil, errors.New("this month's AI cap cannot cover it; raise the cap under Settings › Plugins › AI gateway, or wait for next month")
		}
		return nil, err
	}
	return decodeRewrite(res.Output)
}
