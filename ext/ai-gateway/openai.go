package aigateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
)

// OpenAI's Chat Completions, over net/http (rule 2).
//
// The answer comes back through response_format with a strict JSON schema,
// which is OpenAI's way of holding a model to a shape. Strict mode wants
// every object closed and every property required, which is why RegisterTask
// holds every task's schema to the same rule whichever provider a store
// picks.
const openAIHost = "https://api.openai.com"

type openAIProvider struct{ call httpCall }

func newOpenAI(key string, client *http.Client) *openAIProvider {
	return &openAIProvider{call: httpCall{
		provider: "OpenAI",
		key:      key,
		client:   client,
		headers:  map[string]string{"Authorization": "Bearer " + key},
		errorOf: func(status int, body []byte) (string, string) {
			var e struct {
				Error struct {
					Type    string `json:"type"`
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
				kind := e.Error.Code
				if kind == "" {
					kind = e.Error.Type
				}
				return kind, e.Error.Message
			}
			return "", http.StatusText(status)
		},
		// An account out of credit answers 429 like a rate limit, and
		// retrying it twelve times only delays telling a person.
		transient: func(status int, kind string) bool {
			return transientStatus(status) && kind != "insufficient_quota"
		},
	}}
}

func (p *openAIProvider) Name() string { return ProviderOpenAI }

func (p *openAIProvider) Generate(ctx context.Context, req Request) (Response, error) {
	messages := []map[string]any{{"role": "system", "content": req.System}}
	for i, m := range req.Messages {
		if i > 0 || len(req.Documents) == 0 {
			messages = append(messages, map[string]any{"role": m.Role, "content": m.Text})
			continue
		}
		var parts []map[string]any
		for _, d := range req.Documents {
			if d.Caption != "" {
				parts = append(parts, map[string]any{"type": "text", "text": d.Caption})
			}
			data := "data:" + d.MediaType + ";base64," + base64.StdEncoding.EncodeToString(d.Bytes)
			if d.MediaType == "application/pdf" {
				parts = append(parts, map[string]any{"type": "file", "file": map[string]string{"filename": d.Name, "file_data": data}})
			} else {
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": data}})
			}
		}
		parts = append(parts, map[string]any{"type": "text", "text": m.Text})
		messages = append(messages, map[string]any{"role": m.Role, "content": parts})
	}
	body := map[string]any{
		"model":                 req.Model,
		"messages":              messages,
		"max_completion_tokens": req.MaxTokens,
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   answerTool,
				"strict": true,
				"schema": req.Schema,
			},
		},
	}

	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content *string `json:"content"`
				Refusal *string `json:"refusal"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens        int64 `json:"prompt_tokens"`
			CompletionTokens    int64 `json:"completion_tokens"`
			PromptTokensDetails struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := p.call.do(ctx, http.MethodPost, openAIHost+"/v1/chat/completions", body, &out); err != nil {
		return Response{}, err
	}
	// prompt_tokens includes the cached ones, so input is what was not
	// cached; OpenAI bills nothing extra for writing its cache.
	resp := Response{
		Model: out.Model,
		Usage: Usage{
			InputTokens:     out.Usage.PromptTokens - out.Usage.PromptTokensDetails.CachedTokens,
			OutputTokens:    out.Usage.CompletionTokens,
			CacheReadTokens: out.Usage.PromptTokensDetails.CachedTokens,
		},
	}
	if len(out.Choices) == 0 {
		resp.StopReason = "no_choice"
		return resp, nil
	}
	choice := out.Choices[0]
	resp.StopReason = choice.FinishReason
	switch {
	case choice.Message.Refusal != nil && *choice.Message.Refusal != "":
		resp.Refusal, resp.StopReason = *choice.Message.Refusal, "refusal"
	case choice.Message.Content != nil:
		resp.JSON = json.RawMessage(*choice.Message.Content)
	}
	return resp, nil
}

// Models lists what the key reaches. Listing models spends no tokens.
func (p *openAIProvider) Models(ctx context.Context) ([]string, error) {
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := p.call.do(ctx, http.MethodGet, openAIHost+"/v1/models", nil, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}
