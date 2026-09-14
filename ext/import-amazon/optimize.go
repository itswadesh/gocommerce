package amazon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// The rewrite step. A scraped listing is a seller's copy — written for
// Amazon's search, stuffed with the brand name, and in the seller's voice. The
// model turns it into the store's own product page: a title a shopper would
// search for, a description in paragraphs, a type, tags, and the two SEO
// fields, all from the facts the listing gave and nothing it did not.
//
// It is optional. Without an API key the listing is used as scraped, which is
// a worse product page and a working one, and the job says so in its warnings.
// Claude's Messages API over net/http, no SDK — rule 2.

const (
	defaultAnthropicURL = "https://api.anthropic.com"
	defaultModel        = "claude-sonnet-5"
	anthropicVersion    = "2023-06-01"
)

type optimizer struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

// rewrite is what the model hands back. Every field is optional on purpose:
// a field the model left empty keeps the listing's own value rather than
// blanking it.
type rewrite struct {
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	ProductType    string   `json:"product_type"`
	Vendor         string   `json:"vendor"`
	Tags           []string `json:"tags"`
	SEOTitle       string   `json:"seo_title"`
	SEODescription string   `json:"seo_description"`
}

const systemPrompt = `You write product pages for an independent online store. You are given a product listing scraped from a marketplace. Rewrite it for this store's own catalogue.

Rules:
- Use only facts present in the listing. Invent nothing: no claims, sizes, materials or compatibility the listing does not state.
- The title is what a shopper would search for: the product, its key distinguishing attribute, the brand if it matters. No marketplace keyword stuffing, no ALL CAPS, no trailing feature lists. Under 80 characters.
- The description is well-formed HTML using only <p>, <ul>, <li>, <strong>: two to four short paragraphs, then a bullet list of the concrete specifications worth knowing. Plain, specific, no filler, no exclamation marks.
- product_type is a two-or-three word category noun, e.g. "Wireless earbuds".
- vendor is the brand, or empty if the listing names none.
- tags are three to eight lowercase search terms a shopper might use.
- seo_title is under 60 characters; seo_description is under 155 and states what the product is and for whom.

Answer with a single JSON object with exactly these keys: title, description, product_type, vendor, tags, seo_title, seo_description. No prose before or after it, no code fence.`

// optimize asks the model for the rewrite.
func (o *optimizer) optimize(ctx context.Context, l *Listing) (*rewrite, error) {
	if o == nil || o.apiKey == "" {
		return nil, errors.New("no Anthropic API key is configured")
	}

	// A trimmed view: the model needs the copy and the facts, not the image
	// URLs or the per-variant prices.
	input := map[string]any{
		"title":       l.Title,
		"brand":       l.Brand,
		"bullets":     l.Bullets,
		"description": truncate(l.Description, 6000),
		"specs":       l.Specs,
		"breadcrumbs": l.Breadcrumbs,
		"options":     l.Options,
	}
	listing, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}

	body := map[string]any{
		"model":      o.model,
		"max_tokens": 2048,
		"system":     systemPrompt,
		"messages": []map[string]any{{
			"role":    "user",
			"content": "The listing:\n\n" + string(listing),
		}},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/v1/messages", bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", o.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("anthropic: read the response: %w", err)
	}
	if resp.StatusCode >= 300 {
		var fail struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &fail) == nil && fail.Error.Message != "" {
			return nil, fmt.Errorf("anthropic: %s (%s)", fail.Error.Message, fail.Error.Type)
		}
		return nil, fmt.Errorf("anthropic: %s", resp.Status)
	}

	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("anthropic: could not read the response: %w", err)
	}
	var answer strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			answer.WriteString(c.Text)
		}
	}
	return parseRewrite(answer.String())
}

// parseRewrite reads the model's JSON, tolerating the code fence it was told
// not to use — a fence is a formatting slip, not a wrong answer.
func parseRewrite(text string) (*rewrite, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```json")
		text = strings.TrimPrefix(text, "```")
		text = strings.TrimSuffix(text, "```")
		text = strings.TrimSpace(text)
	}
	if start := strings.Index(text, "{"); start > 0 {
		text = text[start:]
	}
	if end := strings.LastIndex(text, "}"); end >= 0 && end < len(text)-1 {
		text = text[:end+1]
	}
	var r rewrite
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		return nil, fmt.Errorf("the model did not answer with JSON: %w", err)
	}
	if strings.TrimSpace(r.Title) == "" && strings.TrimSpace(r.Description) == "" {
		return nil, errors.New("the model answered with neither a title nor a description")
	}
	return &r, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func newOptimizer(apiKey, model, baseURL string) *optimizer {
	if apiKey == "" {
		return nil
	}
	if model == "" {
		model = defaultModel
	}
	if baseURL == "" {
		baseURL = defaultAnthropicURL
	}
	return &optimizer{
		apiKey: apiKey, model: model, baseURL: strings.TrimRight(baseURL, "/"),
		client: &http.Client{Timeout: 90 * time.Second},
	}
}
