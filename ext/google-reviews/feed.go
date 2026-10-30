package googlereviews

import (
	"context"
	"encoding/xml"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	gocommerce "github.com/itswadesh/gocommerce/core"
	feeds "github.com/itswadesh/gocommerce/ext/feeds"
)

// The product reviews feed, schema 2.4:
// developers.google.com/product-review-feeds/schema, with the sample at
// developers.google.com/product-review-feeds/sample. What goes in follows
// the Product Ratings policies (support.google.com/merchants/answer/6098512):
//
//   - every published review, low ratings included, and the whole set each
//     time, since a review missing from a fetch is deleted;
//   - only reviews from customers who bought, so only verified ones;
//   - no personal information: the reviewer is a first name and an initial,
//     and a review whose words hold an email address, a phone number or a
//     link is left out rather than edited;
//   - products named by the identifiers the product feed uses, so they match.
//
// ponytail: a review of a product no longer in the product feed (archived or
// deleted) is left out, since it has no product to rate; Google then drops it.
type reviewFeed struct {
	XMLName   xml.Name `xml:"feed"`
	XSI       string   `xml:"xmlns:xsi,attr"`
	Schema    string   `xml:"xsi:noNamespaceSchemaLocation,attr"`
	Version   string   `xml:"version"`
	Publisher struct {
		Name string `xml:"name"`
	} `xml:"publisher"`
	Reviews []feedReview `xml:"reviews>review"`
}

type feedReview struct {
	ID        int64  `xml:"review_id"`
	Reviewer  string `xml:"reviewer>name"`
	Verified  bool   `xml:"is_verified_purchase"`
	Timestamp string `xml:"review_timestamp"`
	Title     string `xml:"title,omitempty"`
	Content   string `xml:"content"`
	URL       struct {
		Type  string `xml:"type,attr"`
		Value string `xml:",chardata"`
	} `xml:"review_url"`
	Overall struct {
		Min   int `xml:"min,attr"`
		Max   int `xml:"max,attr"`
		Value int `xml:",chardata"`
	} `xml:"ratings>overall"`
	Products []feedProduct `xml:"products>product"`
}

type feedProduct struct {
	GTINs  []string `xml:"product_ids>gtins>gtin,omitempty"`
	MPNs   []string `xml:"product_ids>mpns>mpn,omitempty"`
	SKUs   []string `xml:"product_ids>skus>sku,omitempty"`
	Brands []string `xml:"product_ids>brands>brand,omitempty"`
	Name   string   `xml:"product_name"`
	URL    string   `xml:"product_url"`
}

func (m *Module) handleFeed(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s, err := m.settings(ctx)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if !s.productRatings {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("the product ratings feed is not enabled on this store"))
		return
	}
	if m.cfg.Feeds == nil || m.cfg.Reviews == nil {
		gocommerce.RespondError(w, r, gocommerce.Conflictf("the product ratings feed needs the feeds and reviews modules installed"))
		return
	}
	variants, err := m.cfg.Feeds.Variants(ctx)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Conflictf("the product ratings feed names products as the product feed does: %s", feedProblem(err)))
		return
	}
	published, err := m.cfg.Reviews.Published(ctx)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Conflictf("the product ratings feed is made of your product reviews: %s", feedProblem(err)))
		return
	}
	byProduct := map[int64][]feeds.Variant{}
	for _, v := range variants {
		byProduct[v.ProductID] = append(byProduct[v.ProductID], v)
	}

	feed := reviewFeed{
		XSI:     "http://www.w3.org/2001/XMLSchema-instance",
		Schema:  "http://www.google.com/shopping/reviews/schema/product/2.4/product_reviews.xsd",
		Version: "2.4",
	}
	feed.Publisher.Name = m.storeName(ctx)
	feed.Reviews = []feedReview{}
	for _, rv := range published {
		products := byProduct[rv.ProductID]
		name := reviewerName(rv.Name)
		if !rv.Verified || len(products) == 0 || name == "" || hasContact(rv.Title) || hasContact(rv.Body) {
			continue
		}
		fr := feedReview{ID: rv.ID, Reviewer: name, Verified: true, Timestamp: rv.CreatedAt.UTC().Format(time.RFC3339),
			Title: rv.Title, Content: rv.Body}
		fr.URL.Type, fr.URL.Value = "group", products[0].Page
		fr.Overall.Min, fr.Overall.Max, fr.Overall.Value = 1, 5, rv.Rating
		for _, v := range products {
			fp := feedProduct{SKUs: []string{v.SKU}, Name: v.Title, URL: v.Link}
			if v.GTIN != "" {
				fp.GTINs = []string{v.GTIN}
			}
			if v.MPN != "" {
				fp.MPNs = []string{v.MPN}
			}
			if v.Brand != "" {
				fp.Brands = []string{v.Brand}
			}
			fr.Products = append(fr.Products, fp)
		}
		feed.Reviews = append(feed.Reviews, fr)
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=900")
	_, _ = w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	_ = enc.Encode(feed)
}

// feedProblem is a module's refusal in words an operator can act on.
func feedProblem(err error) string {
	if e, ok := err.(*gocommerce.APIError); ok {
		return e.Message
	}
	return err.Error()
}

// storeName is the shop's trading name, else the store's own: the feed's
// publisher.
func (m *Module) storeName(ctx context.Context) string {
	if p, err := m.app.Profile().Get(ctx); err == nil && p.Name != "" {
		return p.Name
	}
	if id, ok := gocommerce.StoreFrom(ctx); ok {
		if st, err := m.app.Stores().Get(ctx, id); err == nil && st.Name != "" {
			return st.Name
		}
	}
	return "Store"
}

// reviewerName is a first name and the last name's initial: Google asks for
// no full names (Product Ratings policies, personal information).
func reviewerName(full string) string {
	parts := strings.Fields(full)
	if len(parts) < 2 {
		return strings.Join(parts, "")
	}
	initial, _ := utf8.DecodeRuneInString(parts[len(parts)-1])
	return parts[0] + " " + string(initial) + "."
}

var (
	emailRE = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	linkRE  = regexp.MustCompile(`(?i)\bhttps?://|\bwww\.`)
	// Ten digits or more with only spaces, dots, dashes or brackets between:
	// a phone number, and not a size, a price or a date.
	phoneRE = regexp.MustCompile(`\+?\d(?:[\s().-]?\d){9,}`)
)

// hasContact reports words Google asks a feed not to carry: an email
// address, a phone number or a link.
func hasContact(text string) bool {
	return emailRE.MatchString(text) || linkRE.MatchString(text) || phoneRE.MatchString(text)
}
