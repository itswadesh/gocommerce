package aigateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
)

// Anthropic's Messages API, over net/http (rule 2).
//
// The answer is forced through a single tool whose input_schema is the
// task's schema, which every current Claude model supports and which gives a
// JSON object back rather than prose to be parsed. The instructions carry
// cache_control: tools and system come before the messages in Anthropic's
// cache order, so one breakpoint on the system block caches the schema and
// the instructions together, and both are the same for every store.
const (
	anthropicHost    = "https://api.anthropic.com"
	anthropicVersion = "2023-06-01"
	// answerTool is the one tool every request offers and forces.
	answerTool = "answer"
)

type anthropicProvider struct{ call httpCall }

func newAnthropic(key string, client *http.Client) *anthropicProvider {
	return &anthropicProvider{call: httpCall{
		provider: "Anthropic",
		key:      key,
		client:   client,
		headers:  map[string]string{"x-api-key": key, "anthropic-version": anthropicVersion},
		errorOf: func(status int, body []byte) (string, string) {
			var e struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
				return e.Error.Type, e.Error.Message
			}
			return "", http.StatusText(status)
		},
		// 529 is Anthropic's "overloaded", which transientStatus already
		// covers as a 5xx; named here because it is the one most seen.
		transient: func(status int, kind string) bool { return transientStatus(status) || status == 529 },
	}}
}

func (p *anthropicProvider) Name() string { return ProviderAnthropic }

type anthropicBlock struct {
	Type   string          `json:"type"`
	Text   string          `json:"text,omitempty"`
	Source *anthropicSrc   `json:"source,omitempty"`
	Cache  *anthropicCache `json:"cache_control,omitempty"`
}

type anthropicSrc struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthropicCache struct {
	Type string `json:"type"`
}

func (p *anthropicProvider) Generate(ctx context.Context, req Request) (Response, error) {
	messages := make([]map[string]any, 0, len(req.Messages))
	for i, m := range req.Messages {
		var content []anthropicBlock
		// Files before the words, which is the order Anthropic recommends
		// and the one a question about a file reads naturally in.
		if i == 0 {
			for _, d := range req.Documents {
				kind := "image"
				if d.MediaType == "application/pdf" {
					kind = "document"
				}
				content = append(content, anthropicBlock{Type: kind, Source: &anthropicSrc{
					Type: "base64", MediaType: d.MediaType, Data: base64.StdEncoding.EncodeToString(d.Bytes),
				}})
			}
		}
		content = append(content, anthropicBlock{Type: "text", Text: m.Text})
		messages = append(messages, map[string]any{"role": m.Role, "content": content})
	}
	body := map[string]any{
		"model":      req.Model,
		"max_tokens": req.MaxTokens,
		"system": []anthropicBlock{{
			Type: "text", Text: req.System, Cache: &anthropicCache{Type: "ephemeral"},
		}},
		"tools": []map[string]any{{
			"name":         answerTool,
			"description":  "Give the answer. Call this exactly once, with every field the schema asks for.",
			"input_schema": req.Schema,
		}},
		"tool_choice": map[string]string{"type": "tool", "name": answerTool},
		"messages":    messages,
	}

	var out struct {
		Model   string `json:"model"`
		Content []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Text  string          `json:"text"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := p.call.do(ctx, http.MethodPost, anthropicHost+"/v1/messages", body, &out); err != nil {
		return Response{}, err
	}
	resp := Response{
		Model:      out.Model,
		StopReason: out.StopReason,
		// input_tokens already excludes what was read from or written to the
		// cache, so the four counts are billed once each as they stand.
		Usage: Usage{
			InputTokens:      out.Usage.InputTokens,
			OutputTokens:     out.Usage.OutputTokens,
			CacheReadTokens:  out.Usage.CacheReadInputTokens,
			CacheWriteTokens: out.Usage.CacheCreationInputTokens,
		},
	}
	var said []string
	for _, c := range out.Content {
		switch {
		case c.Type == "tool_use" && c.Name == answerTool && resp.JSON == nil:
			resp.JSON = c.Input
		case c.Type == "text" && strings.TrimSpace(c.Text) != "":
			said = append(said, strings.TrimSpace(c.Text))
		}
	}
	if resp.JSON == nil {
		resp.Refusal = strings.Join(said, " ")
	}
	return resp, nil
}

// Models lists what the key reaches. Listing models spends no tokens.
func (p *anthropicProvider) Models(ctx context.Context) ([]string, error) {
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := p.call.do(ctx, http.MethodGet, anthropicHost+"/v1/models?limit=1000", nil, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}
