package shopify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Talking to a Shopify store.
//
// Plain net/http and encoding/json, because ext/ packages add no third-party
// dependencies (AGENTS.md rule 2) and because Shopify's Admin API is an
// ordinary REST API over JSON. An SDK would buy nothing here and would be a
// dependency this repository has a rule against.
//
// Two things about this API shape the whole file.
//
// Paging is by opaque cursor in a Link header, not by page number — Shopify
// removed page numbers in 2019 and a client that counts pages silently stops at
// the first 250 products. So the walk follows rel="next" until it is absent.
//
// The rate limit is a leaky bucket: 40 calls, refilling at 2 a second. Going
// over answers 429 with Retry-After. A catalogue import is exactly the workload
// that hits it, so this waits rather than failing, and reads the bucket's own
// header to slow down before it has to.

// APIVersion is pinned rather than tracking "latest".
//
// Shopify dates its versions and retires each after a year, so an unpinned
// client changes behaviour underneath a store on Shopify's schedule rather than
// on ours. Moving it is an edit somebody makes deliberately, with the release
// notes open.
const APIVersion = "2024-10"

// Client is one Shopify store's Admin API.
type Client struct {
	Shop  string // "acme.myshopify.com"
	Token string // the Admin API access token of a custom app
	HTTP  *http.Client
}

// NewClient builds a client for a shop domain and an access token.
//
// The domain is normalised rather than validated into submission: an operator
// copies whatever is in their address bar, which may carry https://, a path or
// a trailing slash, and refusing that teaches them nothing.
func NewClient(shop, token string) (*Client, error) {
	shop = strings.TrimSpace(shop)
	shop = strings.TrimPrefix(strings.TrimPrefix(shop, "https://"), "http://")
	shop = strings.TrimSuffix(strings.Trim(shop, "/"), "/admin")
	if i := strings.IndexByte(shop, '/'); i >= 0 {
		shop = shop[:i]
	}
	if shop == "" {
		return nil, fmt.Errorf("shopify: no shop domain — it looks like acme.myshopify.com")
	}
	if !strings.Contains(shop, ".") {
		// A bare "acme" is the commonest thing an operator types.
		shop += ".myshopify.com"
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("shopify: no access token — create a custom app in the Shopify " +
			"admin, give it read_products, and copy the Admin API access token")
	}
	return &Client{
		Shop:  shop,
		Token: strings.TrimSpace(token),
		// A catalogue page of 250 products with their variants is a large
		// response over somebody else's network; the default of no timeout is
		// how an import hangs for ever.
		HTTP: &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// Product is a Shopify product, in the shape its REST API sends.
//
// Only the fields this importer reads. Money crosses as a decimal string
// because that is what Shopify sends ("19.99"), and it is converted to minor
// units exactly once, in map.go.
type Product struct {
	ID          int64     `json:"id"`
	Title       string    `json:"title"`
	BodyHTML    string    `json:"body_html"`
	Vendor      string    `json:"vendor"`
	ProductType string    `json:"product_type"`
	Handle      string    `json:"handle"`
	Status      string    `json:"status"`
	Tags        string    `json:"tags"`
	Options     []Option  `json:"options"`
	Variants    []Variant `json:"variants"`
	Images      []Image   `json:"images"`
	UpdatedAt   string    `json:"updated_at"`
}

type Option struct {
	Name     string   `json:"name"`
	Position int      `json:"position"`
	Values   []string `json:"values"`
}

type Variant struct {
	ID                int64   `json:"id"`
	Title             string  `json:"title"`
	SKU               string  `json:"sku"`
	Price             string  `json:"price"`
	CompareAtPrice    *string `json:"compare_at_price"`
	Barcode           string  `json:"barcode"`
	InventoryQuantity int     `json:"inventory_quantity"`
	InventoryPolicy   string  `json:"inventory_policy"`
	InventoryMgmt     *string `json:"inventory_management"`
	Taxable           bool    `json:"taxable"`
	RequiresShipping  bool    `json:"requires_shipping"`
	Grams             int     `json:"grams"`
	Position          int     `json:"position"`
	ImageID           *int64  `json:"image_id"`
	Option1           *string `json:"option1"`
	Option2           *string `json:"option2"`
	Option3           *string `json:"option3"`
}

// Options returns the variant's option values in axis order, dropping the ones
// the product does not have. Shopify always sends three slots.
func (v Variant) OptionValues(axes int) []string {
	out := []string{}
	for i, p := range []*string{v.Option1, v.Option2, v.Option3} {
		if i >= axes || p == nil || *p == "" {
			continue
		}
		out = append(out, *p)
	}
	return out
}

type Image struct {
	ID  int64  `json:"id"`
	Src string `json:"src"`
	Alt string `json:"alt"`
}

// Shop is what the store calls itself, used to confirm the credentials work
// before an import starts walking a catalogue.
type Shop struct {
	Name     string `json:"name"`
	Domain   string `json:"domain"`
	Currency string `json:"currency"`
	Products int    `json:"-"`
}

// Verify checks the credentials and returns what the shop calls itself.
//
// Called before an import rather than letting the first page fail, so a wrong
// token is reported as a wrong token rather than as "0 products imported".
func (c *Client) Verify(ctx context.Context) (*Shop, error) {
	var body struct {
		Shop Shop `json:"shop"`
	}
	if err := c.get(ctx, "/shop.json", nil, &body); err != nil {
		return nil, err
	}
	count, err := c.CountProducts(ctx)
	if err != nil {
		return nil, err
	}
	body.Shop.Products = count
	return &body.Shop, nil
}

// CountProducts is how many products the import will walk, for a progress bar
// that can say "40 of 900" rather than counting upwards into the dark.
func (c *Client) CountProducts(ctx context.Context) (int, error) {
	var body struct {
		Count int `json:"count"`
	}
	if err := c.get(ctx, "/products/count.json", nil, &body); err != nil {
		return 0, err
	}
	return body.Count, nil
}

// EachProduct walks the whole catalogue, a page at a time, calling fn for each
// product.
//
// A callback rather than a slice: a large catalogue is tens of thousands of
// products with their variants, and holding all of it in memory to hand back
// one list is how an import of somebody else's big store takes the server down
// with it. This way each page is written and released.
func (c *Client) EachProduct(ctx context.Context, fn func(Product) error) error {
	next := "/products.json"
	query := url.Values{"limit": {"250"}}

	for next != "" {
		var body struct {
			Products []Product `json:"products"`
		}
		link, err := c.getWithLink(ctx, next, query, &body)
		if err != nil {
			return err
		}
		for _, p := range body.Products {
			if err := fn(p); err != nil {
				return err
			}
		}
		// The cursor carries everything, including the limit, so the query is
		// dropped after the first page. Sending both is how a walk restarts.
		next, query = nextLink(link), nil
	}
	return nil
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	_, err := c.getWithLink(ctx, path, query, out)
	return err
}

// getWithLink performs one request, retrying while Shopify says to wait.
func (c *Client) getWithLink(ctx context.Context, path string, query url.Values, out any) (string, error) {
	target := path
	if !strings.HasPrefix(path, "http") {
		target = "https://" + c.Shop + "/admin/api/" + APIVersion + path
		if len(query) > 0 {
			target += "?" + query.Encode()
		}
	}

	// Bounded: a store that answers 429 nine times running has a problem this
	// cannot wait out, and an import that retries for ever looks like an import
	// that has hung.
	for attempt := 0; attempt < 8; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("X-Shopify-Access-Token", c.Token)
		req.Header.Set("Accept", "application/json")

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return "", fmt.Errorf("shopify: %s: %w", path, err)
		}

		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			wait := retryAfter(resp.Header.Get("Retry-After"))
			resp.Body.Close()
			if err := sleep(ctx, wait); err != nil {
				return "", err
			}
			continue

		case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
			resp.Body.Close()
			return "", fmt.Errorf("shopify: the access token was refused (%d). Check the custom "+
				"app is installed on %s and has the read_products scope", resp.StatusCode, c.Shop)

		case resp.StatusCode == http.StatusNotFound:
			resp.Body.Close()
			return "", fmt.Errorf("shopify: %s answered 404. Check the shop domain — it looks "+
				"like acme.myshopify.com, not the storefront's own domain", c.Shop)

		case resp.StatusCode >= 400:
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
			resp.Body.Close()
			return "", fmt.Errorf("shopify: %s answered %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
		}

		link := resp.Header.Get("Link")
		limit := resp.Header.Get("X-Shopify-Shop-Api-Call-Limit")
		err = json.NewDecoder(resp.Body).Decode(out)
		resp.Body.Close()
		if err != nil {
			return "", fmt.Errorf("shopify: %s sent something that is not JSON: %w", path, err)
		}

		// Slow down before being told to. The header is "used/capacity"; past
		// four fifths of the bucket a pause costs a moment and a 429 costs the
		// round trip plus whatever Retry-After says.
		if nearLimit(limit) {
			if err := sleep(ctx, time.Second); err != nil {
				return "", err
			}
		}
		return link, nil
	}
	return "", fmt.Errorf("shopify: %s is still rate limiting after several waits", c.Shop)
}

// nextLink pulls the rel="next" URL out of a Link header.
//
// The header holds one or two entries — previous and next — and the cursor is
// opaque and must be sent back exactly as given, which is why this returns the
// whole URL rather than parsing a token out of it.
func nextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		segments := strings.Split(strings.TrimSpace(part), ";")
		if len(segments) < 2 {
			continue
		}
		target := strings.Trim(strings.TrimSpace(segments[0]), "<>")
		for _, attr := range segments[1:] {
			if strings.Contains(strings.ReplaceAll(attr, " ", ""), `rel="next"`) {
				return target
			}
		}
	}
	return ""
}

// nearLimit reads "33/40" and reports whether the bucket is nearly full.
func nearLimit(header string) bool {
	used, capacity, ok := strings.Cut(header, "/")
	if !ok {
		return false
	}
	u, err1 := strconv.Atoi(strings.TrimSpace(used))
	c, err2 := strconv.Atoi(strings.TrimSpace(capacity))
	if err1 != nil || err2 != nil || c == 0 {
		return false
	}
	return float64(u)/float64(c) > 0.8
}

func retryAfter(header string) time.Duration {
	if seconds, err := strconv.ParseFloat(strings.TrimSpace(header), 64); err == nil && seconds > 0 {
		return time.Duration(seconds * float64(time.Second))
	}
	return 2 * time.Second
}

// sleep waits, unless the import was cancelled in the meantime.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
