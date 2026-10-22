package gctest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/itswadesh/gocommerce/core"
)

// AssertPublicRoutesHideCost fails when a public route tells somebody with no
// credentials what the shop paid for p.
//
// It calls every public GET route the app serves — core's and every module's
// mounted on it — filling each path parameter with every value it has: p's id,
// slug, SKUs and variant ids, plus whatever the caller adds in values (a
// showcase's slug, a wishlist's token: whatever puts p on that route). A route
// answers wrong when a variant-shaped object in its JSON (one with a "sku")
// carries a "cost", or when a recorded cost appears anywhere in the body, in
// minor units or as a decimal — which is what catches a feed, a CSV or a
// module's own shape. So give p's variants a cost with digits found nowhere
// else; ten is plenty.
//
// It returns the routes that answered with p, and fails by itself when core's
// own product and variant routes are not among them, so a fixture that never
// reached the catalogue cannot pass by finding nothing.
func AssertPublicRoutesHideCost(t *testing.T, app *gocommerce.App, p *gocommerce.Product, values ...string) []string {
	t.Helper()
	// Read again, so a cost recorded after p was created is the one looked for.
	id := p.ID
	p, err := app.Products().GetProduct(context.Background(), id)
	if err != nil {
		t.Fatalf("gctest: read product %d: %v", id, err)
	}
	fill := append([]string{strconv.FormatInt(p.ID, 10), p.Slug}, values...)
	var skus, needles []string
	for _, v := range p.Variants {
		fill = append(fill, strconv.FormatInt(v.ID, 10), v.SKU)
		skus = append(skus, v.SKU)
		if v.Cost != nil {
			needles = append(needles, costSpellings(v.Cost.AmountMinor)...)
		}
	}
	if len(needles) == 0 {
		t.Fatalf("gctest: product %d has no variant with a cost, so there is nothing to look for", p.ID)
	}
	query := "?product_id=" + strconv.FormatInt(p.ID, 10)

	reached := map[string]bool{}
	for _, r := range app.Routes() {
		if r.Admin || r.UI || (r.Method != "" && r.Method != http.MethodGet) || documentsTheAPI[r.Path] {
			continue
		}
		for _, path := range fillPath(r.Path, fill) {
			rec := Request(t, app, http.MethodGet, path+query, nil)
			body := rec.Body.String()
			for _, n := range needles {
				if strings.Contains(body, n) {
					t.Errorf("gctest: GET %s shows what the shop paid (%s): %s", path, n, clip(body))
				}
			}
			var decoded any
			if json.Unmarshal(rec.Body.Bytes(), &decoded) == nil && variantWithCost(decoded) {
				t.Errorf("gctest: GET %s answers with a variant's cost: %s", path, clip(body))
			}
			if rec.Code == http.StatusOK && containsAny(body, skus) {
				reached[r.Path] = true
			}
		}
	}

	for _, must := range []string{"/api/products", "/api/products/{id}", "/api/products/slug/{slug}",
		"/api/products/sku/{sku}", "/api/variants", "/api/variants/{id}"} {
		if !reached[must] {
			t.Errorf("gctest: GET %s never answered with product %d, so the walk proves nothing there", must, p.ID)
		}
	}
	out := make([]string, 0, len(reached))
	for path := range reached {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// documentsTheAPI are the routes that describe the API rather than a store:
// the contract names the cost field, as it names every field, and holds no
// store's numbers.
var documentsTheAPI = map[string]bool{"/doc": true, "/docs": true}

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

// fillPath is every way of filling path's parameters from values. A route
// with none is itself.
func fillPath(path string, values []string) []string {
	out := []string{""}
	rest := path
	for {
		loc := pathParam.FindStringIndex(rest)
		if loc == nil {
			break
		}
		next := make([]string, 0, len(out)*len(values))
		for _, prefix := range out {
			for _, v := range values {
				next = append(next, prefix+rest[:loc[0]]+url.PathEscape(v))
			}
		}
		out, rest = next, rest[loc[1]:]
	}
	for i := range out {
		out[i] += rest
	}
	return out
}

// costSpellings is an amount as a body might carry it: minor units, and the
// decimals a two- or three-place currency writes it with.
func costSpellings(minor int64) []string {
	s := strconv.FormatInt(minor, 10)
	out := []string{s}
	for _, places := range []int{2, 3} {
		if len(s) > places {
			out = append(out, s[:len(s)-places]+"."+s[len(s)-places:])
		}
	}
	return out
}

// variantWithCost finds an object that is a variant — it has a sku — and
// carries a cost, at any depth.
func variantWithCost(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		_, sku := t["sku"]
		_, cost := t["cost"]
		if sku && cost {
			return true
		}
		for _, inner := range t {
			if variantWithCost(inner) {
				return true
			}
		}
	case []any:
		for _, inner := range t {
			if variantWithCost(inner) {
				return true
			}
		}
	}
	return false
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if sub != "" && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func clip(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
