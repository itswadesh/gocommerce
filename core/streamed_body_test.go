package gocommerce

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// streamed sends body as a proxy streams it: no Content-Length, which Go
// reports as -1. A route that checked the length skipped such a body.
func streamed(body string) func(*http.Request) {
	return func(r *http.Request) {
		r.Body = io.NopCloser(strings.NewReader(body))
		r.ContentLength = -1
		r.TransferEncoding = []string{"chunked"}
		r.Header.Set("Content-Type", "application/json")
	}
}

func TestAStreamedBodyIsRead(t *testing.T) {
	app := newTestApp(t)

	rec := do(t, app, http.MethodPost, "/api/carts", streamed(`{"email":"streamed@example.com"}`))
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("create a cart with a streamed body = %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "streamed@example.com") {
		t.Fatalf("the streamed body's email was dropped: %s", rec.Body)
	}

	// An empty streamed body is no body: the cart is still made.
	if rec := do(t, app, http.MethodPost, "/api/carts", streamed("")); rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("create a cart with an empty streamed body = %d %s", rec.Code, rec.Body)
	}
}
