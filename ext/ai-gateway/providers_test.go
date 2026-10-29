package aigateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// The provider hosts are constants. A test stands a server up in their place
// by giving the module a client that sends every request to it, and records
// where each request was addressed, so the address itself is under test too.

type sent struct {
	URL    string
	Header http.Header
	Body   []byte
}

type standIn struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []sent
}

func newStandIn(t *testing.T, handler http.HandlerFunc) *standIn {
	t.Helper()
	s := &standIn{}
	s.srv = httptest.NewServer(handler)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *standIn) client() *http.Client {
	to, _ := url.Parse(s.srv.URL)
	return &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
		}
		s.mu.Lock()
		s.reqs = append(s.reqs, sent{URL: r.URL.String(), Header: r.Header.Clone(), Body: body})
		s.mu.Unlock()
		out := r.Clone(r.Context())
		out.URL.Scheme, out.URL.Host, out.Host = to.Scheme, to.Host, to.Host
		out.Body = io.NopCloser(strings.NewReader(string(body)))
		return http.DefaultTransport.RoundTrip(out)
	})}
}

func (s *standIn) last(t *testing.T) sent {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.reqs) == 0 {
		t.Fatal("nothing was sent")
	}
	return s.reqs[len(s.reqs)-1]
}

func (s *standIn) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func reply(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// Response bodies in the shapes the two APIs return them, usage blocks
// included. The numbers are ones a real call of this size reports.
const (
	anthropicToolAnswer = `{
  "id": "msg_01XFDUDYJgAACzvnptvVoYEL",
  "type": "message",
  "role": "assistant",
  "model": "claude-haiku-4-5-20251001",
  "content": [
    {"type": "text", "text": "{\"title\": \"Steel widget, two-pack\", \"tags\": [\"widget\", \"steel\"]}"}
  ],
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {
    "input_tokens": 412,
    "cache_creation_input_tokens": 1536,
    "cache_read_input_tokens": 0,
    "cache_creation": {"ephemeral_5m_input_tokens": 1536, "ephemeral_1h_input_tokens": 0},
    "output_tokens": 187,
    "service_tier": "standard"
  }
}`
	anthropicRefusal = `{
  "id": "msg_01Refusal", "type": "message", "role": "assistant",
  "model": "claude-haiku-4-5-20251001",
  "content": [{"type": "text", "text": "I can't help with rewriting this listing."}],
  "stop_reason": "refusal", "stop_sequence": null,
  "usage": {"input_tokens": 980, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 1536, "output_tokens": 14}
}`
	anthropicCutOff = `{
  "id": "msg_01CutOff", "type": "message", "role": "assistant",
  "model": "claude-sonnet-5-5-20260915",
  "content": [{"type": "text", "text": "{\"title\": \"Steel wid"}],
  "stop_reason": "max_tokens", "stop_sequence": null,
  "usage": {"input_tokens": 1400, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0, "output_tokens": 64}
}`
	anthropicModels = `{
  "data": [
    {"type": "model", "id": "claude-sonnet-5-5", "display_name": "Claude Sonnet 5.5", "created_at": "2026-09-15T00:00:00Z"},
    {"type": "model", "id": "claude-haiku-5-5", "display_name": "Claude Haiku 5.5", "created_at": "2026-10-01T00:00:00Z"}
  ],
  "has_more": false, "first_id": "claude-sonnet-5-5", "last_id": "claude-haiku-5-5"
}`

	openAIAnswer = `{
  "id": "chatcmpl-B9MHDbslfkBeAs8l4bebGdFOJ6PeG",
  "object": "chat.completion",
  "created": 1741570283,
  "model": "gpt-4o-mini-2024-07-18",
  "choices": [
    {"index": 0,
     "message": {"role": "assistant", "content": "{\"title\":\"Steel widget, two-pack\",\"tags\":[\"widget\",\"steel\"]}", "refusal": null, "annotations": []},
     "logprobs": null, "finish_reason": "stop"}
  ],
  "usage": {
    "prompt_tokens": 1117, "completion_tokens": 46, "total_tokens": 1163,
    "prompt_tokens_details": {"cached_tokens": 1024, "audio_tokens": 0},
    "completion_tokens_details": {"reasoning_tokens": 0, "audio_tokens": 0, "accepted_prediction_tokens": 0, "rejected_prediction_tokens": 0}
  },
  "service_tier": "default",
  "system_fingerprint": "fp_06737a9306"
}`
	openAIRefusal = `{
  "id": "chatcmpl-Refusal", "object": "chat.completion", "created": 1741570290,
  "model": "gpt-4o-mini-2024-07-18",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": null, "refusal": "I'm sorry, I can't assist with that request."}, "logprobs": null, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 512, "completion_tokens": 11, "total_tokens": 523, "prompt_tokens_details": {"cached_tokens": 0}}
}`
	openAICutOff = `{
  "id": "chatcmpl-CutOff", "object": "chat.completion", "created": 1741570291,
  "model": "gpt-4o-mini-2024-07-18",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "{\"title\":\"Steel wid", "refusal": null}, "logprobs": null, "finish_reason": "length"}],
  "usage": {"prompt_tokens": 512, "completion_tokens": 64, "total_tokens": 576, "prompt_tokens_details": {"cached_tokens": 0}}
}`
	openAIModels = `{"object": "list", "data": [
  {"id": "gpt-4o-mini", "object": "model", "created": 1721172741, "owned_by": "system"},
  {"id": "gpt-5", "object": "model", "created": 1754425777, "owned_by": "system"}
]}`
)

var testSchema = json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}}},"required":["title","tags"],"additionalProperties":false}`)

func testRequest(model string) Request {
	return Request{
		Model:     model,
		System:    "You rewrite product listings.",
		Messages:  []Message{{Role: "user", Text: "Rewrite this.\n\n<data label=\"listing\" class=\"catalogue\">\n{\"title\":\"WIDGET!!\"}\n</data>"}},
		Schema:    testSchema,
		MaxTokens: 1024,
	}
}

func decodeBody(t *testing.T, s sent) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(s.Body, &m); err != nil {
		t.Fatalf("the request body is not JSON: %v: %s", err, s.Body)
	}
	return m
}

func TestAnthropicAsksForTheAnswerThroughOneForcedTool(t *testing.T) {
	s := newStandIn(t, reply(http.StatusOK, anthropicToolAnswer))
	p := newAnthropic("sk-ant-test-key", s.client())

	resp, err := p.Generate(context.Background(), testRequest("claude-haiku-4-5"))
	if err != nil {
		t.Fatal(err)
	}
	req := s.last(t)
	if req.URL != "https://api.anthropic.com/v1/messages" {
		t.Errorf("sent to %s, want Anthropic's Messages API", req.URL)
	}
	if req.Header.Get("x-api-key") != "sk-ant-test-key" || req.Header.Get("anthropic-version") != "2023-06-01" {
		t.Errorf("headers = %v", req.Header)
	}
	body := decodeBody(t, req)
	if body["model"] != "claude-haiku-4-5" || body["max_tokens"] != float64(1024) {
		t.Errorf("model and max_tokens = %v, %v", body["model"], body["max_tokens"])
	}
	system := body["system"].([]any)[0].(map[string]any)
	if system["text"] != "You rewrite product listings." {
		t.Errorf("system = %v", system["text"])
	}
	// The instructions are the same for every store, so they are the part
	// worth caching, and the part that may be cached without mixing stores.
	if cc, _ := system["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Errorf("the instructions carry no cache_control: %v", system)
	}
	// Structured output, not a forced tool: Sonnet 5.5 and Opus 5.5 answer
	// tool_choice "tool" with a 400.
	if _, forced := body["tool_choice"]; forced {
		t.Errorf("tool_choice = %v, which the current models refuse", body["tool_choice"])
	}
	format := body["output_config"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_schema" {
		t.Errorf("format = %v", format["type"])
	}
	if schema, _ := json.Marshal(format["schema"]); string(schema) != compactJSON(t, testSchema) {
		t.Errorf("schema = %s, want the task's schema", schema)
	}
	msg := body["messages"].([]any)[0].(map[string]any)
	if msg["role"] != "user" {
		t.Errorf("role = %v", msg["role"])
	}

	if string(resp.JSON) != `{"title": "Steel widget, two-pack", "tags": ["widget", "steel"]}` {
		t.Errorf("JSON = %s, want the structured output", resp.JSON)
	}
	if resp.Model != "claude-haiku-4-5-20251001" || resp.StopReason != "end_turn" {
		t.Errorf("model %q stop %q", resp.Model, resp.StopReason)
	}
	want := Usage{InputTokens: 412, OutputTokens: 187, CacheReadTokens: 0, CacheWriteTokens: 1536}
	if resp.Usage != want {
		t.Errorf("usage = %+v, want %+v", resp.Usage, want)
	}
}

func TestAnthropicReportsADeclineAndACutOffAnswer(t *testing.T) {
	s := newStandIn(t, reply(http.StatusOK, anthropicRefusal))
	resp, err := newAnthropic("sk-ant-k", s.client()).Generate(context.Background(), testRequest("claude-haiku-4-5"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.JSON != nil || resp.StopReason != "refusal" || !strings.Contains(resp.Refusal, "can't help") {
		t.Errorf("refusal = %+v", resp)
	}
	if resp.Usage.CacheReadTokens != 1536 || resp.Usage.InputTokens != 980 {
		t.Errorf("a declined call still costs: usage = %+v", resp.Usage)
	}

	s = newStandIn(t, reply(http.StatusOK, anthropicCutOff))
	resp, err = newAnthropic("sk-ant-k", s.client()).Generate(context.Background(), testRequest("claude-sonnet-5-5"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "max_tokens" || resp.Model != "claude-sonnet-5-5-20260915" {
		t.Errorf("cut off = %+v", resp)
	}
}

func TestAnthropicFailuresAreSortedIntoRetryAndRefuse(t *testing.T) {
	cases := []struct {
		status    int
		body      string
		transient bool
		says      string
	}{
		{529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"},"request_id":"req_011CSHoEeqs5C35K2UUqR7Fy"}`, true, "Overloaded"},
		{429, `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed the rate limit for your organization of 50 requests per minute."},"request_id":"req_02"}`, true, "rate limit"},
		{500, `{"type":"error","error":{"type":"api_error","message":"Internal server error"},"request_id":"req_03"}`, true, "Internal server error"},
		{503, `upstream connect error`, true, "503"},
		{400, `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: 100000 > 64000, which is the maximum allowed number of output tokens for claude-haiku-4-5-20251001"},"request_id":"req_04"}`, false, "max_tokens"},
		{401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key sk-ant-secret-value"},"request_id":"req_05"}`, false, "invalid x-api-key"},
		{403, `{"type":"error","error":{"type":"permission_error","message":"Your API key does not have permission to use the specified resource."},"request_id":"req_06"}`, false, "permission"},
	}
	for _, tc := range cases {
		s := newStandIn(t, reply(tc.status, tc.body))
		_, err := newAnthropic("sk-ant-secret-value", s.client()).Generate(context.Background(), testRequest("claude-haiku-4-5"))
		var pe *ProviderError
		if !errors.As(err, &pe) {
			t.Fatalf("%d: error = %v, want a ProviderError", tc.status, err)
		}
		if pe.Transient != tc.transient || pe.Status != tc.status {
			t.Errorf("%d: transient %v status %d, want %v", tc.status, pe.Transient, pe.Status, tc.transient)
		}
		if !strings.Contains(pe.Error(), tc.says) {
			t.Errorf("%d: %q does not say %q", tc.status, pe.Error(), tc.says)
		}
		if strings.Contains(pe.Error(), "sk-ant-secret-value") {
			t.Errorf("%d: the key is in the error: %s", tc.status, pe.Error())
		}
		if strings.Contains(pe.Error(), "WIDGET") {
			t.Errorf("%d: the request body is in the error: %s", tc.status, pe.Error())
		}
	}
}

func TestAProviderThatDoesNotAnswerInTimeIsTransient(t *testing.T) {
	s := newStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	for _, p := range []Provider{newAnthropic("sk-ant-k", s.client()), newOpenAI("sk-k", s.client())} {
		_, err := p.Generate(ctx, testRequest("m"))
		var pe *ProviderError
		if !errors.As(err, &pe) || !pe.Transient || pe.Status != 0 {
			t.Errorf("%s: error = %v, want a transient ProviderError with no status", p.Name(), err)
		}
	}
}

func TestOpenAIAsksForAStrictJSONSchemaAnswer(t *testing.T) {
	s := newStandIn(t, reply(http.StatusOK, openAIAnswer))
	resp, err := newOpenAI("sk-proj-test", s.client()).Generate(context.Background(), testRequest("gpt-4o-mini"))
	if err != nil {
		t.Fatal(err)
	}
	req := s.last(t)
	if req.URL != "https://api.openai.com/v1/chat/completions" {
		t.Errorf("sent to %s, want OpenAI's Chat Completions", req.URL)
	}
	if req.Header.Get("Authorization") != "Bearer sk-proj-test" {
		t.Errorf("Authorization = %q", req.Header.Get("Authorization"))
	}
	body := decodeBody(t, req)
	if body["model"] != "gpt-4o-mini" || body["max_completion_tokens"] != float64(1024) {
		t.Errorf("model %v max_completion_tokens %v", body["model"], body["max_completion_tokens"])
	}
	rf := body["response_format"].(map[string]any)
	js := rf["json_schema"].(map[string]any)
	if rf["type"] != "json_schema" || js["strict"] != true || js["name"] != "answer" {
		t.Errorf("response_format = %v", rf)
	}
	if schema, _ := json.Marshal(js["schema"]); string(schema) != compactJSON(t, testSchema) {
		t.Errorf("schema = %s", schema)
	}
	msgs := body["messages"].([]any)
	if first := msgs[0].(map[string]any); first["role"] != "system" || first["content"] != "You rewrite product listings." {
		t.Errorf("first message = %v, want the instructions as the system message", first)
	}
	if second := msgs[1].(map[string]any); second["role"] != "user" {
		t.Errorf("second message = %v", second)
	}

	if string(resp.JSON) != `{"title":"Steel widget, two-pack","tags":["widget","steel"]}` {
		t.Errorf("JSON = %s", resp.JSON)
	}
	if resp.Model != "gpt-4o-mini-2024-07-18" || resp.StopReason != "stop" {
		t.Errorf("model %q stop %q", resp.Model, resp.StopReason)
	}
	// prompt_tokens counts the cached ones too; input is what was not cached.
	want := Usage{InputTokens: 93, OutputTokens: 46, CacheReadTokens: 1024, CacheWriteTokens: 0}
	if resp.Usage != want {
		t.Errorf("usage = %+v, want %+v", resp.Usage, want)
	}
}

func TestOpenAIReportsADeclineAndACutOffAnswer(t *testing.T) {
	s := newStandIn(t, reply(http.StatusOK, openAIRefusal))
	resp, err := newOpenAI("sk-k", s.client()).Generate(context.Background(), testRequest("gpt-4o-mini"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.JSON != nil || resp.StopReason != "refusal" || !strings.Contains(resp.Refusal, "can't assist") {
		t.Errorf("refusal = %+v", resp)
	}
	s = newStandIn(t, reply(http.StatusOK, openAICutOff))
	resp, err = newOpenAI("sk-k", s.client()).Generate(context.Background(), testRequest("gpt-4o-mini"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "length" {
		t.Errorf("cut off = %+v", resp)
	}
}

func TestOpenAIFailuresAreSortedIntoRetryAndRefuse(t *testing.T) {
	cases := []struct {
		status    int
		body      string
		transient bool
		says      string
	}{
		{429, `{"error":{"message":"Rate limit reached for gpt-4o-mini in organization org-x on requests per min (RPM): Limit 500, Used 500, Requested 1.","type":"requests","param":null,"code":"rate_limit_exceeded"}}`, true, "Rate limit"},
		// Out of credit is a 429 too, and no amount of waiting fixes it.
		{429, `{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","param":null,"code":"insufficient_quota"}}`, false, "quota"},
		{500, `{"error":{"message":"The server had an error while processing your request. Sorry about that!","type":"server_error","param":null,"code":null}}`, true, "server had an error"},
		{502, `<html>Bad gateway</html>`, true, "502"},
		{400, `{"error":{"message":"Invalid schema for response_format 'answer': In context=(), 'required' is required to be supplied.","type":"invalid_request_error","param":"response_format","code":null}}`, false, "Invalid schema"},
		{401, `{"error":{"message":"Incorrect API key provided: sk-proj-secret. You can find your API key at https://platform.openai.com/account/api-keys.","type":"invalid_request_error","param":null,"code":"invalid_api_key"}}`, false, "Incorrect API key"},
	}
	for _, tc := range cases {
		s := newStandIn(t, reply(tc.status, tc.body))
		_, err := newOpenAI("sk-proj-secret", s.client()).Generate(context.Background(), testRequest("gpt-4o-mini"))
		var pe *ProviderError
		if !errors.As(err, &pe) {
			t.Fatalf("%d: error = %v, want a ProviderError", tc.status, err)
		}
		if pe.Transient != tc.transient {
			t.Errorf("%d %s: transient = %v, want %v", tc.status, tc.says, pe.Transient, tc.transient)
		}
		if !strings.Contains(pe.Error(), tc.says) {
			t.Errorf("%d: %q does not say %q", tc.status, pe.Error(), tc.says)
		}
		if strings.Contains(pe.Error(), "sk-proj-secret") {
			t.Errorf("%d: the key is in the error: %s", tc.status, pe.Error())
		}
	}
}

func TestDocumentsTravelInEachProvidersOwnShape(t *testing.T) {
	req := testRequest("m")
	req.Documents = []Document{
		{Name: "po.pdf", MediaType: "application/pdf", Bytes: []byte("%PDF-1.7")},
		{Name: "label.png", MediaType: "image/png", Bytes: []byte("\x89PNG"), Caption: "Photo 2"},
	}

	s := newStandIn(t, reply(http.StatusOK, anthropicToolAnswer))
	if _, err := newAnthropic("sk-ant-k", s.client()).Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	content := decodeBody(t, s.last(t))["messages"].([]any)[0].(map[string]any)["content"].([]any)
	var kinds []string
	for _, c := range content {
		kinds = append(kinds, c.(map[string]any)["type"].(string))
	}
	// A caption is a text block just before its file.
	if strings.Join(kinds, ",") != "document,text,image,text" || content[1].(map[string]any)["text"] != "Photo 2" {
		t.Errorf("anthropic content = %v, want the files, each after its caption, before the text", kinds)
	}

	s = newStandIn(t, reply(http.StatusOK, openAIAnswer))
	if _, err := newOpenAI("sk-k", s.client()).Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	parts := decodeBody(t, s.last(t))["messages"].([]any)[1].(map[string]any)["content"].([]any)
	kinds = nil
	for _, c := range parts {
		kinds = append(kinds, c.(map[string]any)["type"].(string))
	}
	if strings.Join(kinds, ",") != "file,text,image_url,text" {
		t.Errorf("openai content = %v", kinds)
	}
}

func TestEachProviderListsItsModelsWithoutSpendingTokens(t *testing.T) {
	s := newStandIn(t, reply(http.StatusOK, anthropicModels))
	ids, err := newAnthropic("sk-ant-k", s.client()).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "claude-sonnet-5-5,claude-haiku-5-5" {
		t.Errorf("models = %v", ids)
	}
	if req := s.last(t); !strings.HasPrefix(req.URL, "https://api.anthropic.com/v1/models") || req.Header.Get("x-api-key") != "sk-ant-k" {
		t.Errorf("asked %s with %v", req.URL, req.Header)
	}

	s = newStandIn(t, reply(http.StatusOK, openAIModels))
	ids, err = newOpenAI("sk-k", s.client()).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "gpt-4o-mini,gpt-5" {
		t.Errorf("models = %v", ids)
	}
	if req := s.last(t); req.URL != "https://api.openai.com/v1/models" {
		t.Errorf("asked %s", req.URL)
	}

	s = newStandIn(t, reply(http.StatusUnauthorized, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	if _, err := newAnthropic("sk-ant-k", s.client()).Models(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Errorf("a refused key = %v", err)
	}
}

func compactJSON(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(v)
	return string(out)
}
