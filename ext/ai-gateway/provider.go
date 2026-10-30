package aigateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
)

// Provider is one model API (ADR 0005). It turns a Request into an answer
// and reports what the answer cost in tokens. It knows nothing about stores,
// budgets or tasks: the gateway decides whether a call may happen, and a
// provider only makes it.
type Provider interface {
	Name() string
	Generate(ctx context.Context, req Request) (Response, error)
}

// Embedder turns text into vectors. Declared so the interface is settled
// beside Provider; nothing implements or calls it until semantic search is
// built (D104).
type Embedder interface {
	Embed(ctx context.Context, model string, texts []string) ([][]float32, Usage, error)
}

// Request is one call to a model. A task's Build fills Messages and
// Documents; the gateway fills the rest from the task and the store's
// settings, and renders each message's Data into its text before a provider
// sees it.
type Request struct {
	Model string
	// System is the task's instructions, the same for every store (D104), so
	// a provider may cache it across stores without mixing them.
	System   string
	Messages []Message
	// Documents are PDFs and images, sent only when the store's policy lets
	// documents leave.
	Documents []Document
	// Schema is the JSON Schema the answer must satisfy.
	Schema json.RawMessage
	// MaxTokens is the most output the provider may return, and what the call
	// holds credit for. Build may set it below the task's MaxOutputTokens for a
	// smaller request; zero, or anything above, is the task's.
	MaxTokens int
}

// Message is one turn. Text is the task's own words; Data is what the store
// or its customers supplied, which the gateway fences off as data (ADR 0005
// §7) and leaves out entirely when the policy does not let its class leave.
type Message struct {
	Role string // "user" or "assistant"
	Text string
	Data []Data
}

// Data is one piece of store content in a request.
type Data struct {
	Class DataClass
	// Label names the block for the model: "listing", "customer". Lowercase
	// letters, digits, dashes and underscores.
	Label string
	// Value is encoded as JSON inside the fence. Go's encoder escapes <, >
	// and &, so nothing in a value can close the fence early.
	Value any
	// Required is data the task cannot do without. When the policy keeps its
	// class in, the run stops as policy_blocked instead of sending a request
	// that would be answered without it.
	Required bool
}

// Document is a file sent to the provider whole.
type Document struct {
	Name      string
	MediaType string // application/pdf, image/png, image/jpeg, image/gif, image/webp
	Bytes     []byte
	// Caption is the task's own words sent just before the file, "Photo 3",
	// so an answer about many files can say which one it means.
	Caption string
}

// Response is a model's answer. JSON is never trusted as it stands: the
// gateway checks it against the task's schema and the task's own Check.
type Response struct {
	JSON json.RawMessage
	// Model is the version the provider says answered, kept as model_version.
	Model      string
	Usage      Usage
	StopReason string
	// Refusal is what a model said when it declined to answer.
	Refusal string
}

// Usage is what a call consumed, in the four counts the providers bill.
// InputTokens excludes tokens read from or written to a cache, so the four
// never overlap and each is priced once.
type Usage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

func (u Usage) add(o Usage) Usage {
	return Usage{
		InputTokens:      u.InputTokens + o.InputTokens,
		OutputTokens:     u.OutputTokens + o.OutputTokens,
		CacheReadTokens:  u.CacheReadTokens + o.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens + o.CacheWriteTokens,
	}
}

// ProviderError is a call that did not produce an answer.
//
// Transient is the one decision that matters downstream: a job retries a
// transient failure with backoff and sends anything else to a person. A
// timeout, a rate limit, a 5xx and Anthropic's 529 are transient; a request
// the provider refused is not, and asking again would be refused again.
type ProviderError struct {
	Provider string
	// Status is the HTTP status, or 0 when nothing came back.
	Status int
	// Kind is the provider's own name for the failure: overloaded_error,
	// insufficient_quota.
	Kind      string
	Message   string
	Transient bool
	// Sent is a request that left in full before the failure: the provider
	// may have done the work, and may bill it, whatever came back.
	Sent bool

	// panicked is a call whose transport panicked: never retried, and
	// charged at the ceiling.
	panicked bool
}

func (e *ProviderError) Error() string {
	var b strings.Builder
	b.WriteString(e.Provider)
	if e.Status != 0 {
		fmt.Fprintf(&b, " answered %d", e.Status)
	}
	if e.Kind != "" {
		b.WriteString(" (" + e.Kind + ")")
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	return b.String()
}

// The most of a provider's answer read into memory. An answer is a few
// kilobytes; this bounds a misbehaving one.
const maxResponseBytes = 4 << 20

// modelLister is a provider that can say which models a key reaches, which
// is how the connection check proves a key works without spending a token.
type modelLister interface {
	Models(ctx context.Context) ([]string, error)
}

// newProvider is the provider a store's settings name.
func newProvider(name, key string, client *http.Client) (Provider, error) {
	switch name {
	case ProviderAnthropic:
		return newAnthropic(key, client), nil
	case ProviderOpenAI:
		return newOpenAI(key, client), nil
	}
	return nil, fmt.Errorf("no provider is called %q", name)
}

// httpCall is one request to a provider and its answer, with failures sorted
// into ProviderErrors. errorOf reads the provider's error body.
type httpCall struct {
	provider string
	key      string
	client   *http.Client
	headers  map[string]string
	errorOf  func(status int, body []byte) (kind, message string)
	// transient decides a failed status, after the kind is known.
	transient func(status int, kind string) bool
}

func (c httpCall) do(ctx context.Context, method, url string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "gocommerce-ai-gateway")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	// Whether the request was written decides what a failure costs: one
	// refused at the door cost nothing, one that timed out after leaving
	// may be billed in full.
	var sent atomic.Bool
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				sent.Store(true)
			}
		},
	}))
	resp, err := c.client.Do(req)
	if err != nil {
		// Nothing came back: a timeout, a reset, a refused connection. The
		// URL and the body stay out of the message; the cause is enough.
		msg := "no answer"
		switch {
		case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
			msg = "no answer before the deadline"
		case errors.Is(err, context.Canceled):
			msg = "the call was cancelled"
		}
		return &ProviderError{Provider: c.provider, Message: msg, Transient: true, Sent: sent.Load()}
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return &ProviderError{Provider: c.provider, Status: resp.StatusCode, Message: "the answer could not be read", Transient: true, Sent: true}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		kind, message := c.errorOf(resp.StatusCode, payload)
		return &ProviderError{
			Provider:  c.provider,
			Status:    resp.StatusCode,
			Kind:      kind,
			Message:   c.redact(message),
			Transient: c.transient(resp.StatusCode, kind),
		}
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return &ProviderError{Provider: c.provider, Status: resp.StatusCode, Message: "the answer was not the JSON expected", Sent: true}
	}
	return nil
}

// redact keeps the key out of anything a provider says back, and bounds the
// message: it is stored on the job and the run, and shown on a screen.
func (c httpCall) redact(message string) string {
	if c.key != "" {
		message = strings.ReplaceAll(message, c.key, "[key]")
	}
	message = strings.TrimSpace(message)
	if len(message) > 500 {
		message = message[:500] + "…"
	}
	return message
}

// transientStatus is the rule both providers share: rate limits and server
// failures pass, everything else was the request's fault.
func transientStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}
