package shopify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The two things an importer gets wrong quietly: money, and stopping early.
//
// A price that is a hundred times too small is visible on the storefront in
// minutes. A walk that stops at the first page is not visible at all — the
// import says "done", 250 products are in, and nobody notices the other 1,400
// until a customer asks for one.

func TestMoneyCrossesExactly(t *testing.T) {
	cases := []struct {
		amount   string
		exponent int
		want     int64
		name     string
	}{
		{"19.99", 2, 1999, "the ordinary case"},
		{"0.05", 2, 5, "a few pence"},
		{"1234.50", 2, 123450, "a larger one"},
		{"", 2, 0, "an absent price is zero, not an error"},
		{"100", 2, 10000, "no decimal point at all"},
		{"100.5", 2, 10050, "one decimal digit is padded, not read as 5"},
		// The expensive one. Shopify writes two decimals whatever the currency,
		// so a ¥2,500 item arrives as "2500.00" — and multiplying that by 100
		// would price it at ¥250,000.
		{"2500.00", 0, 2500, "the yen writes no decimals"},
		{"2500", 0, 2500, "and does not need the point either"},
		{"2.500", 3, 2500, "the dinar writes three"},
		// Not truncation through a float: 19.99 * 100 is 1998.9999999999998,
		// and this must never be 1998.
		{"0.07", 2, 7, "the float-rounding trap"},
		{"8.11", 2, 811, "and another"},
	}
	for _, c := range cases {
		got, err := toMinor(c.amount, c.exponent)
		if err != nil {
			t.Errorf("%s: toMinor(%q, %d): %v", c.name, c.amount, c.exponent, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: toMinor(%q, %d) = %d, want %d", c.name, c.amount, c.exponent, got, c.want)
		}
	}

	if _, err := toMinor("not money", 2); err == nil {
		t.Error("a price that is not a number was accepted")
	}
}

// Shopify pages by opaque cursor in a Link header. A client that stops when it
// has "a page" imports the first 250 products and reports success.
func TestTheWalkFollowsEveryPage(t *testing.T) {
	pages := [][]Product{
		{{ID: 1, Title: "One", Handle: "one", Variants: []Variant{{ID: 11, Price: "1.00"}}}},
		{{ID: 2, Title: "Two", Handle: "two", Variants: []Variant{{ID: 22, Price: "2.00"}}}},
		{{ID: 3, Title: "Three", Handle: "three", Variants: []Variant{{ID: 33, Price: "3.00"}}}},
	}
	var served int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Shopify-Access-Token") != "shpat_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		page := served
		served++
		if page < len(pages)-1 {
			// The cursor is opaque and absolute, which is why the client has to
			// send back the whole URL rather than a token it picked out.
			w.Header().Set("Link", fmt.Sprintf(
				`<https://acme.myshopify.com/admin/api/%s/products.json?page_info=cursor%d>; rel="next"`,
				APIVersion, page+1))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"products": pages[page]})
	}))
	defer srv.Close()

	// The client builds https URLs for a real shop; the test server speaks
	// http on localhost, and rewriteToTest is the seam between them.
	client := &Client{
		Shop:  "acme.myshopify.com",
		Token: "shpat_test",
		HTTP:  &http.Client{Transport: rewriteToTest(srv.URL)},
	}

	seen := []int64{}
	if err := client.EachProduct(context.Background(), func(p Product) error {
		seen = append(seen, p.ID)
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(seen) != 3 {
		t.Errorf("the walk saw %v, want all three pages", seen)
	}
}

// nextLink is the whole of the paging contract, so it is pinned on its own.
func TestNextLink(t *testing.T) {
	const both = `<https://x.myshopify.com/admin/api/2024-10/products.json?page_info=aaa>; rel="previous", ` +
		`<https://x.myshopify.com/admin/api/2024-10/products.json?page_info=bbb>; rel="next"`
	if got := nextLink(both); !strings.Contains(got, "page_info=bbb") {
		t.Errorf("nextLink picked %q, want the next one", got)
	}
	// The last page carries only a previous link, and reading that as "next"
	// walks the catalogue backwards for ever.
	const last = `<https://x.myshopify.com/admin/api/2024-10/products.json?page_info=aaa>; rel="previous"`
	if got := nextLink(last); got != "" {
		t.Errorf("nextLink on the last page returned %q, want nothing", got)
	}
	if got := nextLink(""); got != "" {
		t.Errorf("nextLink on no header returned %q", got)
	}
}

func TestNearLimit(t *testing.T) {
	if nearLimit("10/40") {
		t.Error("a quarter full should not slow the walk down")
	}
	if !nearLimit("35/40") {
		t.Error("nearly full should")
	}
	if nearLimit("") || nearLimit("nonsense") {
		t.Error("a missing or unreadable header must not be read as full")
	}
}

// A shop domain is whatever an operator had in their address bar.
func TestTheShopDomainIsForgiving(t *testing.T) {
	for _, in := range []string{
		"acme.myshopify.com",
		"https://acme.myshopify.com",
		"https://acme.myshopify.com/",
		"https://acme.myshopify.com/admin",
		"acme",
	} {
		c, err := NewClient(in, "shpat_x")
		if err != nil {
			t.Errorf("NewClient(%q): %v", in, err)
			continue
		}
		if c.Shop != "acme.myshopify.com" {
			t.Errorf("NewClient(%q) = %q, want acme.myshopify.com", in, c.Shop)
		}
	}
	if _, err := NewClient("", "shpat_x"); err == nil {
		t.Error("an empty domain was accepted")
	}
	if _, err := NewClient("acme.myshopify.com", " "); err == nil {
		t.Error("an empty token was accepted")
	}
}

// Shopify's "Default Title" option is its way of saying a product has no
// options. Carrying it across puts a one-choice picker on the storefront.
func TestAProductWithNoOptionsGetsNone(t *testing.T) {
	p := Product{
		ID: 1, Title: "Plain tee", Handle: "plain-tee", Status: "active",
		Options: []Option{{Name: "Title", Values: []string{"Default Title"}}},
		Variants: []Variant{{
			ID: 11, Title: "Default Title", SKU: "TEE-1", Price: "19.99",
			Option1: strptr("Default Title"),
		}},
	}
	mapped, err := MapProduct(p, "USD", 2)
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if len(mapped.Input.Options) != 0 {
		t.Errorf("options = %+v, want none", mapped.Input.Options)
	}
	if len(mapped.Input.Variants) != 1 {
		t.Fatalf("variants = %d, want 1", len(mapped.Input.Variants))
	}
	if got := mapped.Input.Variants[0].Options; len(got) != 0 {
		t.Errorf("the variant selects %v, want nothing", got)
	}
	if got := mapped.Input.Variants[0].PriceMinor; got != 1999 {
		t.Errorf("price = %d, want 1999", got)
	}
}

// An untracked variant reports inventory_quantity 0. Importing that as "none
// left" takes a product off sale that was never counted in the first place.
func TestUntrackedStockIsNotZeroStock(t *testing.T) {
	shopify := "shopify"
	p := Product{
		ID: 2, Title: "Gift card", Handle: "gift-card", Status: "active",
		Variants: []Variant{
			{ID: 21, SKU: "GC", Price: "25.00", InventoryQuantity: 0, InventoryMgmt: nil},
			{ID: 22, SKU: "TEE", Price: "19.99", InventoryQuantity: 4, InventoryMgmt: &shopify},
			// Shopify allows an oversold count; this engine's CHECK does not.
			{ID: 23, SKU: "OVER", Price: "9.99", InventoryQuantity: -3, InventoryMgmt: &shopify},
		},
	}
	mapped, err := MapProduct(p, "USD", 2)
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	untracked := mapped.Input.Variants[0]
	if untracked.TrackInventory == nil || *untracked.TrackInventory {
		t.Error("a variant Shopify does not track was imported as tracked")
	}
	tracked := mapped.Input.Variants[1]
	if tracked.TrackInventory == nil || !*tracked.TrackInventory {
		t.Error("a tracked variant was imported as untracked")
	}
	if tracked.StockOnHand == nil || *tracked.StockOnHand != 4 {
		t.Errorf("stock = %v, want 4", tracked.StockOnHand)
	}
	oversold := mapped.Input.Variants[2]
	if oversold.StockOnHand == nil || *oversold.StockOnHand != 0 {
		t.Errorf("an oversold count imported as %v, want 0", oversold.StockOnHand)
	}
}

// The description field holds text. Shopify sends markup.
func TestTheDescriptionArrivesAsText(t *testing.T) {
	got := plainText("<p>Soft &amp; warm.</p><ul><li>Merino</li><li>Made in Leeds</li></ul>")
	if strings.Contains(got, "<") {
		t.Errorf("markup survived: %q", got)
	}
	if !strings.Contains(got, "Soft & warm.") {
		t.Errorf("the entity was not decoded: %q", got)
	}
	// The list must not collapse into one line.
	if !strings.Contains(got, "Merino\nMade in Leeds") {
		t.Errorf("the list ran together: %q", got)
	}
}

// Shopify's `vendor` is the manufacturer, and so is this engine's. It must not
// wander into the new marketplace-seller table.
func TestShopifysVendorStaysTheBrand(t *testing.T) {
	p := Product{
		ID: 3, Title: "Power bank", Handle: "power-bank", Status: "active",
		Vendor: "Anker", ProductType: "Electronics", Tags: "usb-c, travel",
		Variants: []Variant{{ID: 31, SKU: "PB", Price: "49.00"}},
	}
	mapped, err := MapProduct(p, "USD", 2)
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if mapped.Input.Vendor != "Anker" {
		t.Errorf("vendor = %q, want Anker", mapped.Input.Vendor)
	}
	if mapped.Input.ProductType != "Electronics" {
		t.Errorf("product type = %q", mapped.Input.ProductType)
	}
	if len(mapped.Input.Tags) != 2 {
		t.Errorf("tags = %v, want two", mapped.Input.Tags)
	}
	// And where it came from, so a second import updates rather than copies.
	sh, _ := mapped.Input.Metadata["shopify"].(map[string]any)
	if sh == nil || fmt.Sprint(sh["id"]) != "3" {
		t.Errorf("the Shopify id was not recorded: %+v", mapped.Input.Metadata)
	}
}

func strptr(s string) *string { return &s }

// rewriteToTest sends every request to the test server, whatever host the
// client built. The client always speaks https to a real shop; the test server
// speaks http on localhost, and this is the seam between them.
func rewriteToTest(base string) http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		target := strings.TrimPrefix(base, "http://")
		r.URL.Scheme = "http"
		r.URL.Host = target
		r.Host = target
		return http.DefaultTransport.RoundTrip(r)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
