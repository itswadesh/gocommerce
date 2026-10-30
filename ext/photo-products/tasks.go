package photoproducts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	aigateway "github.com/itswadesh/gocommerce/ext/ai-gateway"
	catalogai "github.com/itswadesh/gocommerce/ext/catalog-ai"
)

// The two gateway tasks. Both run on the gateway's strong model, which a
// store sets under Settings › Plugins › AI gateway (Claude Sonnet 5.5 unless
// it says otherwise): telling two near-identical photos apart is where a
// small model's mistakes would cost the shop most.
const (
	groupTaskName    = "photos.group"
	describeTaskName = "photos.describe"
)

// ---------------------------------------------------------------- grouping

type groupInput struct {
	BatchID int64 `json:"batch_id"`
}

type groupOutput struct {
	Products []struct {
		Name       string `json:"name"`
		OptionName string `json:"option_name"`
		Variants   []struct {
			Value  string `json:"value"`
			Photos []int  `json:"photos"`
		} `json:"variants"`
	} `json:"products"`
}

var groupSchema = json.RawMessage(`{
  "type": "object", "additionalProperties": false, "required": ["products"],
  "properties": {
    "products": {"type": "array", "items": {
      "type": "object", "additionalProperties": false, "required": ["name", "option_name", "variants"],
      "properties": {
        "name": {"type": "string", "description": "A short working name, e.g. 'Floral hoop earrings'."},
        "option_name": {"type": "string", "description": "What the variants differ in: Colour, Finish, Pattern. Empty when there is one variant."},
        "variants": {"type": "array", "items": {
          "type": "object", "additionalProperties": false, "required": ["value", "photos"],
          "properties": {
            "value": {"type": "string", "description": "This variant's value on the option, e.g. 'Rose gold'. Empty when the product has one variant."},
            "photos": {"type": "array", "items": {"type": "integer"}, "description": "The photo numbers that show this variant."}
          }
        }}
      }
    }}
  }
}`)

// The grouping answer's size: a little for its frame, and room per photo.
const (
	groupBaseTokens     = 500
	groupTokensPerPhoto = 150
)

func groupTask(m *Module) aigateway.Task {
	return aigateway.Task{
		Name:    groupTaskName,
		Version: 2,
		System: `You sort a shop's product photos into the products it sells. Each photo is labelled "Photo N".

- Photos of the same item from different angles, or worn, or close up, belong to one variant.
- The same item in a different colour, finish or pattern is another variant of the same product. Name the option for what differs (Colour, Finish, Pattern, Style) and give each variant a short value naming what is visible, such as "Gold", "Silver", "Blue floral".
- Unrelated items are separate products, even when they look alike in style.
- A product with only one variant has an empty option_name and one variant with an empty value.
- Put every photo in exactly one variant. When unsure whether two photos show the same item, keep them apart: the shop can merge two products more easily than notice a wrong merge.
- The name is a short working name a shop would recognise, from what is visible.

` + catalogai.Honesty,
		Schema: groupSchema,
		// Thumbnails are counted at the gateway's flat estimate per picture,
		// well above what a 512 px picture costs, so the bound is generous.
		MaxInputTokens:  MaxPhotos*1700 + 4000,
		MaxOutputTokens: 8000,
		Ladder:          []aigateway.Tier{aigateway.TierStrong},
		Build: func(ctx context.Context, raw json.RawMessage, _ aigateway.Policy) (aigateway.Request, []aigateway.SourceRef, error) {
			var in groupInput
			if err := json.Unmarshal(raw, &in); err != nil {
				return aigateway.Request{}, nil, err
			}
			photos, err := m.photos(ctx, in.BatchID, true)
			if err != nil {
				return aigateway.Request{}, nil, err
			}
			req := aigateway.Request{Messages: []aigateway.Message{{Role: "user", Text: fmt.Sprintf(
				"Here are %d photos, numbered 1 to %d. Sort them into products and variants.", len(photos), len(photos))}},
				// Output sized to the batch, so a few photos hold a few photos'
				// worth of credit (D151, 2026-10-30). A photo that is a product
				// of its own is about 30 tokens of answer; this allows five times
				// that, and the task's 8,000 caps it.
				MaxTokens: groupBaseTokens + groupTokensPerPhoto*len(photos)}
			for i, p := range photos {
				req.Documents = append(req.Documents, aigateway.Document{
					Name: fmt.Sprintf("photo-%d.jpg", i+1), MediaType: "image/jpeg", Bytes: p.thumb,
					Caption: fmt.Sprintf("Photo %d", i+1),
				})
			}
			return req, []aigateway.SourceRef{{Kind: "photo_products_batch", ID: fmt.Sprint(in.BatchID)}}, nil
		},
		Check: func(_ context.Context, out json.RawMessage) (int, error) {
			var g groupOutput
			if err := json.Unmarshal(out, &g); err != nil {
				return 0, err
			}
			if len(g.Products) == 0 {
				return 0, errors.New("no products")
			}
			return 100, nil
		},
	}
}

// ---------------------------------------------------------------- describing

type describeInput struct {
	BatchID int64 `json:"batch_id"`
	ItemID  int64 `json:"item_id"`
}

type describeOutput struct {
	Title             string   `json:"title"`
	Description       string   `json:"description"`
	Highlights        []string `json:"highlights"`
	ProductType       string   `json:"product_type"`
	Tags              []string `json:"tags"`
	CategoryID        *int64   `json:"category_id"`
	SuggestedCategory *string  `json:"suggested_category"`
	SEOTitle          string   `json:"seo_title"`
	SEODescription    string   `json:"seo_description"`
	HSCode            *string  `json:"hs_code"`
	OptionName        string   `json:"option_name"`
	VariantValues     []string `json:"variant_values"`
	AltTexts          []struct {
		Photo int    `json:"photo"`
		Alt   string `json:"alt"`
	} `json:"alt_texts"`
}

var describeSchema = json.RawMessage(`{
  "type": "object", "additionalProperties": false,
  "required": ["title", "description", "highlights", "product_type", "tags", "category_id", "suggested_category",
               "seo_title", "seo_description", "hs_code", "option_name", "variant_values", "alt_texts"],
  "properties": {
    "title": {"type": "string"},
    "description": {"type": "string", "description": "One to three short plain-text paragraphs separated by a blank line. No HTML, no markdown."},
    "highlights": {"type": "array", "items": {"type": "string"}, "description": "Up to 8 short lines of at most 80 characters, each a visible feature."},
    "product_type": {"type": "string", "description": "A short type such as 'Earrings' or 'Tote bag'."},
    "tags": {"type": "array", "items": {"type": "string"}, "description": "Up to 8 lowercase search tags."},
    "category_id": {"type": ["integer", "null"], "description": "The id of the best-matching category from the store's list, or null when none fits."},
    "suggested_category": {"type": ["string", "null"], "description": "Only when no listed category fits: a name for one the shop might create. Otherwise null."},
    "seo_title": {"type": "string", "description": "At most 60 characters."},
    "seo_description": {"type": "string", "description": "At most 155 characters."},
    "hs_code": {"type": ["string", "null"], "description": "Only when asked for: a likely 4 to 8 digit HSN code. Otherwise null."},
    "option_name": {"type": "string", "description": "The option the variants differ in, as given or improved. Empty for one variant."},
    "variant_values": {"type": "array", "items": {"type": "string"}, "description": "One value per variant, in the order given, as given or improved."},
    "alt_texts": {"type": "array", "items": {
      "type": "object", "additionalProperties": false, "required": ["photo", "alt"],
      "properties": {"photo": {"type": "integer"}, "alt": {"type": "string", "description": "At most 125 characters, saying what the photo shows."}}
    }}
  }
}`)

func describeTask(m *Module) aigateway.Task {
	return aigateway.Task{
		Name:    describeTaskName,
		Version: 2,
		System: `You write a draft product listing for a shop from its own photos of one product. The shop reviews every word before anything is published.

Write for a shopper: a plain, specific title of a few words; a short description; highlights that each name one visible feature; a product type; search tags; an SEO title and description; and alt text for each photo saying what it shows.

Choose category_id only from the categories listed in the data; if none fits, give null and suggest a name in suggested_category. Give hs_code only when the request asks for it, otherwise null. Keep the variants in the order given, one value each.

` + catalogai.Honesty,
		Schema:          describeSchema,
		MaxInputTokens:  photosPerProduct*1700 + 12000,
		MaxOutputTokens: 3000,
		Ladder:          []aigateway.Tier{aigateway.TierStrong},
		Build: func(ctx context.Context, raw json.RawMessage, _ aigateway.Policy) (aigateway.Request, []aigateway.SourceRef, error) {
			var in describeInput
			if err := json.Unmarshal(raw, &in); err != nil {
				return aigateway.Request{}, nil, err
			}
			return m.buildDescribe(ctx, in)
		},
		Check: func(_ context.Context, out json.RawMessage) (int, error) {
			var d describeOutput
			if err := json.Unmarshal(out, &d); err != nil {
				return 0, err
			}
			if strings.TrimSpace(d.Title) == "" {
				return 0, errors.New("no title")
			}
			return 100, nil
		},
	}
}

func (m *Module) buildDescribe(ctx context.Context, in describeInput) (aigateway.Request, []aigateway.SourceRef, error) {
	item, err := m.item(ctx, in.BatchID, in.ItemID)
	if err != nil {
		return aigateway.Request{}, nil, err
	}
	all, err := m.photos(ctx, in.BatchID, false)
	if err != nil {
		return aigateway.Request{}, nil, err
	}
	byID := map[int64]photo{}
	for _, p := range all {
		byID[p.id] = p
	}
	var shown []photo
	for _, id := range shownIDs(item.Draft, func(id int64) bool { _, ok := byID[id]; return ok }) {
		shown = append(shown, byID[id])
	}
	if len(shown) == 0 {
		return aigateway.Request{}, nil, errors.New("this product has no photos left to describe")
	}

	values := make([]string, len(item.Draft.Variants))
	for i, v := range item.Draft.Variants {
		values[i] = v.Value
	}
	ask := fmt.Sprintf("Write the listing for this product, working name %q. Its option is %q with these variants, in order: %q.",
		item.Draft.Title, item.Draft.OptionName, values)
	if len(item.Draft.Sizes) > 0 {
		ask += fmt.Sprintf(" The shop sells it in these sizes, which the photos do not show: %q. Do not describe sizes.", item.Draft.Sizes)
	}
	if m.indianGST(ctx) {
		ask += " The shop files Indian GST: give a likely HSN code in hs_code."
	}
	ask += " The photos are numbered as labelled; give alt text for each by its number."

	categories, err := m.app.Categories().List(ctx)
	if err != nil {
		return aigateway.Request{}, nil, err
	}
	type cat struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	list := make([]cat, 0, len(categories))
	for _, c := range categories {
		list = append(list, cat{c.ID, c.FullName})
	}
	msg := aigateway.Message{Role: "user", Text: ask, Data: []aigateway.Data{{
		Class: aigateway.DataCatalogue, Label: "categories", Value: list,
	}}}
	req := aigateway.Request{Messages: []aigateway.Message{msg}}
	for i, p := range shown {
		req.Documents = append(req.Documents, aigateway.Document{
			Name: fmt.Sprintf("photo-%d.jpg", i+1), MediaType: "image/jpeg", Bytes: p.image,
			Caption: fmt.Sprintf("Photo %d", i+1),
		})
	}
	return req, []aigateway.SourceRef{{Kind: "photo_products_item", ID: fmt.Sprint(in.ItemID)}}, nil
}

// indianGST is whether the shop's profile is Indian, which is the only case
// an HSN code is asked for: a GST invoice needs one, and nowhere else does a
// shop of this kind fill one in.
func (m *Module) indianGST(ctx context.Context) bool {
	p, err := m.app.Profile().Get(ctx)
	return err == nil && strings.EqualFold(p.Country, "IN")
}

// shownIDs is the photos the describing call sees, in the order it numbers
// them: spread across the variants, the first of each first, so a product in
// four colours is seen in all four. It is a function of the draft alone, so
// the alt texts that come back by number can be put on the right photo.
func shownIDs(d Draft, exists func(int64) bool) []int64 {
	var out []int64
	for round := 0; len(out) < photosPerProduct; round++ {
		added := false
		for _, v := range d.Variants {
			if round < len(v.Photos) && len(out) < photosPerProduct && exists(v.Photos[round]) {
				out = append(out, v.Photos[round])
				added = true
			}
		}
		if !added {
			return out
		}
	}
	return out
}
