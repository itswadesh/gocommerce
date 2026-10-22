package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
)

// What only the store's own operators are shown.
//
// What the shop paid for a thing is the floor under every price it sets, and
// a storefront that hands it to a visitor hands it to every competitor. That
// cannot be a rule each public handler remembers: a dozen of them answer with
// products, modules add more, and the one that forgets is the leak — the
// public product and variant routes all did, while the only code taking the
// cost off ran on two admin routes and only for sellers.
//
// So the rule runs the other way. A variant encodes its cost only when it was
// read for an operator, and the only thing that says so is an admin route,
// after authentication, for somebody who is not a seller. Every other reader
// — a public route, a module's storefront route, a feed, a search index, a
// background job — gets the variant without it, without having to know the
// field exists. A route added next year is closed by default, and the cost of
// forgetting is an operator seeing an empty box rather than a shopper seeing
// the margin.

type operatorViewKey struct{}

// WithOperatorView marks ctx as reading for the store's own operator, so a
// variant read under it encodes its cost.
//
// Admin routes are marked by the engine, and no handler needs this. It is for
// a caller that is the operator by construction and has no request to
// authenticate — MCP over stdio, a run mode main() chooses. Never call it on a
// request a shopper sent.
func WithOperatorView(ctx context.Context) context.Context {
	return context.WithValue(ctx, operatorViewKey{}, true)
}

// operatorView marks an admin request. mountScoped wraps it innermost, so it
// runs once authentication has said who is asking.
func operatorView(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(WithOperatorView(r.Context())))
	})
}

// costVisible reports whether a variant read under ctx may carry its cost into
// JSON.
//
// A seller is refused even on a marked route: a vendor reads the catalogue to
// find something to offer against, and the shop's cost is the floor of the
// price they are competing with.
func costVisible(ctx context.Context) bool {
	marked, _ := ctx.Value(operatorViewKey{}).(bool)
	return marked && !IsVendorAccount(ctx)
}

// MarshalJSON leaves the cost out unless the variant was read for an operator.
// Removed rather than zeroed, because zero is a number and a reader cannot tell
// it from "this cost nothing".
func (v Variant) MarshalJSON() ([]byte, error) {
	type fields Variant // the same fields without this method, so Marshal does not recurse
	out := fields(v)
	if !v.costShown {
		out.Cost = nil
	}
	return json.Marshal(out)
}
