package catalogai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	gocommerce "github.com/itswadesh/gocommerce/core"
	aigateway "github.com/itswadesh/gocommerce/ext/ai-gateway"
)

const improveTaskName = "catalog.improve"

const (
	// maxPictures is how many of a product's pictures the model sees, the
	// first ones in the product's order. Six angles say what an item looks
	// like; more only cost more.
	maxPictures = 6
	// pictureWidth is the width a picture is scaled down to before it is
	// sent: enough to read a label, a fraction of a camera's original.
	pictureWidth = 1024
	// maxCategories bounds the list the model picks a category from.
	// ponytail: the first 500 in tree order; a store with a bigger taxonomy
	// gets a search step that sends only the likely branches.
	maxCategories = gocommerce.MaxWholeCategoryTree
)

// improveInput is the task's input: the product as the model is shown it.
// The handler reads and scales the pictures before the run, so the numbers
// the answer's alt texts come back by are fixed before anything is sent.
type improveInput struct {
	ProductID  int64         `json:"product_id"`
	Product    listing       `json:"product"`
	Categories []categoryRef `json:"categories"`
	Pictures   []picture     `json:"pictures"`
}

// listing is the product's own words: every field the task may change, and
// the ones that tell it what the product is.
type listing struct {
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	Highlights     []string `json:"highlights"`
	Tags           []string `json:"tags"`
	ProductType    string   `json:"product_type"`
	Vendor         string   `json:"vendor"`
	Category       string   `json:"category"`
	Options        []option `json:"options,omitempty"`
	Attributes     any      `json:"attributes,omitempty"`
	SEOTitle       string   `json:"seo_title"`
	SEODescription string   `json:"seo_description"`
}

type option struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

type categoryRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type picture struct {
	MediaID   int64  `json:"media_id"`
	Alt       string `json:"alt"`
	MediaType string `json:"media_type"`
	Bytes     []byte `json:"bytes"`
}

// improveOutput is the answer. A null, or an empty list, keeps what the
// product has.
type improveOutput struct {
	Title             *string   `json:"title"`
	Description       *string   `json:"description"`
	Highlights        []string  `json:"highlights"`
	Tags              []string  `json:"tags"`
	ProductType       *string   `json:"product_type"`
	CategoryID        *int64    `json:"category_id"`
	SuggestedCategory *string   `json:"suggested_category"`
	SEOTitle          *string   `json:"seo_title"`
	SEODescription    *string   `json:"seo_description"`
	AltTexts          []altText `json:"alt_texts"`
	Reasons           []reason  `json:"reasons"`
	Verify            []string  `json:"verify"`
	Confidence        int       `json:"confidence"`
}

type altText struct {
	Picture int    `json:"picture"`
	Alt     string `json:"alt"`
}

type reason struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

var improveSchema = json.RawMessage(`{
  "type": "object", "additionalProperties": false,
  "required": ["title", "description", "highlights", "tags", "product_type", "category_id", "suggested_category",
               "seo_title", "seo_description", "alt_texts", "reasons", "verify", "confidence"],
  "properties": {
    "title": {"type": ["string", "null"]},
    "description": {"type": ["string", "null"], "description": "Plain-text paragraphs separated by a blank line. No HTML, no markdown."},
    "highlights": {"type": ["array", "null"], "items": {"type": "string"}},
    "tags": {"type": ["array", "null"], "items": {"type": "string"}},
    "product_type": {"type": ["string", "null"]},
    "category_id": {"type": ["integer", "null"], "description": "An id from the store's categories, or null to keep the current one."},
    "suggested_category": {"type": ["string", "null"]},
    "seo_title": {"type": ["string", "null"]},
    "seo_description": {"type": ["string", "null"]},
    "alt_texts": {"type": "array", "items": {
      "type": "object", "additionalProperties": false, "required": ["picture", "alt"],
      "properties": {"picture": {"type": "integer"}, "alt": {"type": "string"}}
    }},
    "reasons": {"type": "array", "items": {
      "type": "object", "additionalProperties": false, "required": ["field", "reason"],
      "properties": {
        "field": {"type": "string", "enum": ["title", "description", "highlights", "tags", "product_type", "category",
                                              "seo_title", "seo_description", "alt_text"]},
        "reason": {"type": "string"}
      }
    }},
    "verify": {"type": "array", "items": {"type": "string"}},
    "confidence": {"type": "integer", "description": "0 to 100."}
  }
}`)

const improveSystem = `You improve one product's listing in a shop's catalogue. The shop reviews every change, field by field, before anything is saved.

You are given the product as it stands: its title, description (as HTML), highlights, tags, product type, vendor, category, options, attributes, SEO title and description, and the categories the store has. You may also be given its pictures, each labelled "Picture N" with its current alt text. Propose a new value only where it helps a shopper find and choose the product. Give null for a field that is already good, so it is kept, and never repeat the current value.

- title: a plain, specific name of a few words, at most 120 characters.
- description: one to three short paragraphs of plain text, at most 2000 characters.
- highlights: up to 8 lines of at most 80 characters, each naming one feature.
- tags: up to 10 lowercase search tags of at most 40 characters.
- product_type: a short type such as "Earrings", at most 60 characters.
- category_id: only an id from the store's categories. When none fits, give null and, if the product has no category, name one the shop might create in suggested_category; otherwise suggested_category is null.
- seo_title: at most 60 characters. seo_description: 50 to 155 characters.
- alt_texts: for a picture whose alt text is missing or poor, by its number, at most 125 characters saying what it shows. None when no pictures are sent.
- reasons: one short sentence for every field you change, naming the field; alt texts share one reason under alt_text.
- verify: each statement in your proposal the shop must check because you inferred it rather than read it in the shop's words, such as a material, size, fit, origin, care, certification, or health or performance claim, written as the statement to check: "The strap is leather". Empty when you inferred nothing.
- confidence: 0 to 100, how sure you are that every proposed value is supported by the shop's words or the pictures.

` + Honesty

// claimRE finds what no proposal may say, whoever wrote it first: a price, a
// discount, stock, a rating or a review, or "bestseller". The shop may type
// any of them itself; the AI step never writes one.
var claimRE = regexp.MustCompile(`(?i)\b(best[- ]?sell(er|ers|ing)|top[- ]rated|star rating|\d+(\.\d+)?\s*(stars?|out of (5|five|10))|\d+\s*%\s*off|discount(s|ed)?|on sale|in stock|out of stock|limited stock|reviews?)\b|[$€£₹¥]\s?\d|\b(rs\.?|inr|usd|eur|gbp)\s?\d`)

// bound is the length a proposed text is held to, in characters.
type bound struct{ min, max int }

// The single texts, by the name a reason gives them. The two SEO bounds are
// what the panel's own counters hold the shop to.
var textBounds = map[string]bound{
	"title":           {1, 120},
	"description":     {1, 2000},
	"product_type":    {1, 60},
	"seo_title":       {10, 60},
	"seo_description": {50, 155},
}

const (
	maxHighlights = 8
	// maxHighlightLine is D134's, which the catalogue refuses past.
	maxHighlightLine = 80
	maxTags          = 10
	maxTag           = 40
	maxAlt           = 125
	maxSuggested     = 80
	maxVerify        = 12
	maxVerifyLine    = 200
	maxReason        = 300
)

func (o *improveOutput) texts() map[string]*string {
	return map[string]*string{
		"title": o.Title, "description": o.Description, "product_type": o.ProductType,
		"seo_title": o.SEOTitle, "seo_description": o.SEODescription,
	}
}

func improveTask(m *Module) aigateway.Task {
	return aigateway.Task{
		Name:    improveTaskName,
		Version: 1,
		System:  improveSystem,
		Schema:  improveSchema,
		// The pictures at the gateway's flat estimate each, and room for a
		// long description and a full category list.
		MaxInputTokens:  maxPictures*1600 + 20000,
		MaxOutputTokens: 3000,
		Ladder:          []aigateway.Tier{aigateway.TierSmall, aigateway.TierStrong},
		EscalateBelow:   70,
		Build: func(_ context.Context, raw json.RawMessage, _ aigateway.Policy) (aigateway.Request, []aigateway.SourceRef, error) {
			var in improveInput
			if err := json.Unmarshal(raw, &in); err != nil {
				return aigateway.Request{}, nil, err
			}
			return buildImprove(in), []aigateway.SourceRef{{Kind: "product", ID: fmt.Sprint(in.ProductID)}}, nil
		},
		Check: m.checkImprove,
	}
}

func buildImprove(in improveInput) aigateway.Request {
	data := []aigateway.Data{
		{Class: aigateway.DataCatalogue, Label: "product", Value: in.Product, Required: true},
		{Class: aigateway.DataCatalogue, Label: "categories", Value: in.Categories},
	}
	ask := "Improve this product's listing."
	var req aigateway.Request
	if n := len(in.Pictures); n > 0 {
		type label struct {
			Picture int    `json:"picture"`
			Alt     string `json:"alt"`
		}
		labels := make([]label, n)
		for i, p := range in.Pictures {
			labels[i] = label{i + 1, p.Alt}
			req.Documents = append(req.Documents, aigateway.Document{
				Name: fmt.Sprintf("picture-%d", i+1), MediaType: p.MediaType, Bytes: p.Bytes,
				Caption: fmt.Sprintf("Picture %d", i+1),
			})
		}
		data = append(data, aigateway.Data{Class: aigateway.DataCatalogue, Label: "pictures", Value: labels})
		ask += fmt.Sprintf(" Its %d pictures follow, labelled Picture 1 to Picture %d.", n, n)
	} else {
		ask += " No pictures are sent: give no alt texts, and say nothing about how it looks beyond the shop's words."
	}
	req.Messages = []aigateway.Message{{Role: "user", Text: ask, Data: data}}
	return req
}

// checkImprove holds an answer to the limits and the rules above. An answer
// that breaks one goes up the ladder with the reason, as one under the
// confidence bar does.
func (m *Module) checkImprove(ctx context.Context, raw json.RawMessage) (int, error) {
	var o improveOutput
	if err := json.Unmarshal(raw, &o); err != nil {
		return 0, err
	}
	if o.Confidence < 0 || o.Confidence > 100 {
		return 0, fmt.Errorf("confidence %d is not 0 to 100", o.Confidence)
	}
	var said []string
	for field, v := range o.texts() {
		if v == nil {
			continue
		}
		b := textBounds[field]
		if n := len([]rune(strings.TrimSpace(*v))); n < b.min || n > b.max {
			return 0, fmt.Errorf("%s is %d characters, and must be %d to %d", field, n, b.min, b.max)
		}
		said = append(said, *v)
	}
	if err := lines("highlights", o.Highlights, maxHighlights, maxHighlightLine); err != nil {
		return 0, err
	}
	if err := lines("tags", o.Tags, maxTags, maxTag); err != nil {
		return 0, err
	}
	said = append(append(said, o.Highlights...), o.Tags...)
	for _, a := range o.AltTexts {
		if a.Picture < 1 {
			return 0, fmt.Errorf("an alt text names picture %d", a.Picture)
		}
		if err := lines("an alt text", []string{a.Alt}, 1, maxAlt); err != nil {
			return 0, err
		}
		said = append(said, a.Alt)
	}
	if o.CategoryID != nil {
		if o.SuggestedCategory != nil {
			return 0, errors.New("a category was chosen and another suggested; suggest one only when none of the store's fits")
		}
		if _, err := m.app.Categories().Get(ctx, *o.CategoryID); err != nil {
			return 0, fmt.Errorf("category %d is not one of this store's", *o.CategoryID)
		}
	}
	if o.SuggestedCategory != nil {
		if err := lines("the suggested category", []string{*o.SuggestedCategory}, 1, maxSuggested); err != nil {
			return 0, err
		}
		said = append(said, *o.SuggestedCategory)
	}
	for _, s := range said {
		if claim := claimRE.FindString(s); claim != "" {
			return 0, fmt.Errorf("the proposal says %q, a claim the AI step never makes", claim)
		}
	}
	for _, r := range o.Reasons {
		if len([]rune(r.Reason)) > maxReason {
			return 0, fmt.Errorf("the reason for %s runs to %d characters", r.Field, len([]rune(r.Reason)))
		}
	}
	if err := lines("the verify list", o.Verify, maxVerify, maxVerifyLine); err != nil {
		return 0, err
	}
	return o.Confidence, nil
}

// lines checks a list of one-line texts: how many, and how long each is.
func lines(what string, list []string, most, longest int) error {
	if len(list) > most {
		return fmt.Errorf("%s has %d lines, and may have %d", what, len(list), most)
	}
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == "" || strings.ContainsAny(s, "\r\n") {
			return fmt.Errorf("%s has an empty line or one that runs over two", what)
		}
		if n := len([]rune(s)); n > longest {
			return fmt.Errorf("%s has a line of %d characters, and may have %d", what, n, longest)
		}
	}
	return nil
}
