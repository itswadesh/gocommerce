package gocommerce

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// TraceHeader carries a request's trace id, in and out.
//
// One id per request, kept wherever the request leaves something behind: the
// request log line, and the jobs, model calls and proposals it causes. A
// support conversation that starts with "it failed at 3:14" ends at every
// record of that failure through one value, without the log having to carry
// what the records hold (D104: the log names the store and the trace, never a
// prompt).
const TraceHeader = "X-Trace-Id"

// TraceID is the trace the work on ctx belongs to, or "" when nothing set one
// — the engine's own background work, before it adopts a job's.
func TraceID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyTraceID).(string)
	return id
}

// WithTraceID carries a trace into work that runs after its request has
// gone: a job runner adopting the trace its job was enqueued under. An id
// that is not well formed is replaced, as a request's would be.
func WithTraceID(ctx context.Context, id string) context.Context {
	if !ValidTraceID(id) {
		id = newTraceID()
	}
	return context.WithValue(ctx, ctxKeyTraceID, id)
}

// ValidTraceID is the shape a caller's trace id must have to be kept: 8 to 64
// letters, digits, dashes or underscores. Short enough to index and to read
// aloud, and nothing in it can break a log line — the header is the caller's
// to fill, and a line break in it would forge one.
func ValidTraceID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// newTraceID is 16 random bytes in hex: W3C trace-context's size, so an id
// that later travels in a traceparent header needs no second format.
func newTraceID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
