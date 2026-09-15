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
// It is optional. Without a model to call, the listing is used as scraped,
// which is a worse product page and a working one, and the job says so in its
// warnings.
//
// Two wire formats, no SDK — rule 2. Claude's Messages API when an Anthropic
// key is configured; otherwise the OpenAI chat-completions shape, which
// Gemini answers at its OpenAI-compatible endpoint, OpenAI answers, and every
// local server answers — Ollama, LM Studio, anything of that kind on this
// machine — so a store can have the rewrite on a free tier or with no key
// and no bill at all. A chat subscription is not a door: Claude.ai and
// ChatGPT are products for people, with no endpoint a server can call.

const (
	defaultAnthropicURL   = "https://api.anthropic.com"
	defaultAnthropicModel = "claude-sonnet-5"
	anthropicVersion      = "2023-06-01"
	// The OpenAI-shaped doors take a base that already carries the version
	// segment, because the three differ in it and a local server has its own.
	defaultOpenAIURL   = "https://api.openai.com/v1"
	defaultOpenAIModel = "gpt-4o-mini"
	defaultGeminiURL   = "https://generativelanguage.googleapis.com/v1beta/openai"
	defaultGeminiModel = "gemini-3.6-flash"

	providerAnthropic = "anthropic"
	providerGemini    = "gemini"
	providerOpenAI    = "openai"
	providerLocal     = "local model"
)

type optimizer struct {
	provider string
	apiKey   string
	model    string
	baseURL  string
	client   *http.Client
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
	if o == nil {
		return nil, errors.New("no model is configured for the rewrite")
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

	user := "The listing:\n\n" + string(listing)
	var answer string
	if o.provider == providerAnthropic {
		answer, err = o.askAnthropic(ctx, user)
	} else {
		answer, err = o.askOpenAI(ctx, user)
	}
	if err != nil {
		return nil, err
	}
	return parseRewrite(answer)
}

// askAnthropic is the Messages API.
func (o *optimizer) askAnthropic(ctx context.Context, user string) (string, error) {
	body := map[string]any{
		"model":      o.model,
		"max_tokens": 2048,
		"system":     systemPrompt,
		"messages":   []map[string]any{{"role": "user", "content": user}},
	}
	headers := map[string]string{"x-api-key": o.apiKey, "anthropic-version": anthropicVersion}
	payload, err := o.post(ctx, "/v1/messages", body, headers)
	if err != nil {
		return "", err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return "", fmt.Errorf("%s: could not read the response: %w", o.provider, err)
	}
	var answer strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			answer.WriteString(c.Text)
		}
	}
	return answer.String(), nil
}

// askOpenAI is the chat-completions shape, which Gemini, OpenAI and every
// local server speak. The key is optional because a local server has none.
func (o *optimizer) askOpenAI(ctx context.Context, user string) (string, error) {
	body := map[string]any{
		"model": o.model,
		"messages": []map[string]any{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": user},
		},
		// Low, not zero: a rewrite should be the same product twice, but the
		// model is allowed a turn of phrase.
		"temperature": 0.3,
	}
	headers := map[string]string{}
	if o.apiKey != "" {
		headers["Authorization"] = "Bearer " + o.apiKey
	}
	payload, err := o.post(ctx, "/chat/completions", body, headers)
	if err != nil {
		return "", err
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return "", fmt.Errorf("%s: could not read the response: %w", o.provider, err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("%s: the model answered with no choices", o.provider)
	}
	return out.Choices[0].Message.Content, nil
}

func (o *optimizer) post(ctx context.Context, path string, body any, headers map[string]string) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", o.provider, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: read the response: %w", o.provider, err)
	}
	if resp.StatusCode >= 300 {
		// The three services agree on {"error": {"message": …}}; Gemini wraps
		// its in a one-element array, which is why the array is tried too.
		type failure struct {
			Error struct {
				Type    string `json:"type"`
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"error"`
		}
		var fail failure
		if json.Unmarshal(payload, &fail) != nil || fail.Error.Message == "" {
			var list []failure
			if json.Unmarshal(payload, &list) == nil && len(list) > 0 {
				fail = list[0]
			}
		}
		if fail.Error.Message != "" {
			return nil, fmt.Errorf("%s: %s (%s)", o.provider, fail.Error.Message, firstNonEmpty(fail.Error.Type, fail.Error.Status))
		}
		return nil, fmt.Errorf("%s: %s", o.provider, resp.Status)
	}
	return payload, nil
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

// newOptimizer picks the door from what is configured, in this order: an
// Anthropic key, a Gemini key, an OpenAI key, a base URL alone (a local
// server). Nothing configured means no rewrite, and nil says so.
func newOptimizer(cfg Config) *optimizer {
	client := &http.Client{Timeout: 120 * time.Second}
	base := func(configured, fallback string) string {
		return strings.TrimRight(firstNonEmpty(configured, fallback), "/")
	}
	switch {
	case cfg.AnthropicAPIKey != "":
		return &optimizer{
			provider: providerAnthropic, apiKey: cfg.AnthropicAPIKey,
			model: firstNonEmpty(cfg.Model, defaultAnthropicModel), baseURL: base(cfg.AnthropicBaseURL, defaultAnthropicURL), client: client,
		}
	case cfg.GeminiAPIKey != "":
		return &optimizer{
			provider: providerGemini, apiKey: cfg.GeminiAPIKey,
			model: firstNonEmpty(cfg.Model, defaultGeminiModel), baseURL: base(cfg.LLMBaseURL, defaultGeminiURL), client: client,
		}
	case cfg.OpenAIAPIKey != "":
		return &optimizer{
			provider: providerOpenAI, apiKey: cfg.OpenAIAPIKey,
			model: firstNonEmpty(cfg.Model, defaultOpenAIModel), baseURL: base(cfg.LLMBaseURL, defaultOpenAIURL), client: client,
		}
	case cfg.LLMBaseURL != "":
		return &optimizer{
			provider: providerLocal,
			model:    firstNonEmpty(cfg.Model, defaultOpenAIModel), baseURL: base(cfg.LLMBaseURL, ""), client: client,
		}
	}
	return nil
}
