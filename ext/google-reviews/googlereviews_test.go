package googlereviews

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	feeds "github.com/itswadesh/gocommerce/ext/feeds"
	reviews "github.com/itswadesh/gocommerce/ext/reviews"
	"github.com/itswadesh/gocommerce/gctest"
)

// testKey has the shape of a Google Maps Platform key and belongs to nobody;
// testPlace is the Place ID Google's own documentation uses as its example.
const (
	testKey   = "AIzaSyTEST0000000000-not-a-real-key-000"
	testPlace = "ChIJN1t_tDeuEmsRUsoyG83frY4"
)

// placeBody is a Place Details (New) answer in the documented shape
// (developers.google.com/maps/documentation/places/web-service/reference/rest/v1/places):
// the five reviews Google sends at most, one of them a one-star review and
// one carrying an author link that is not https.
const placeBody = `{
  "rating": 4.6,
  "userRatingCount": 182,
  "googleMapsUri": "https://maps.google.com/?cid=10281119596374313554",
  "reviews": [
    {"name": "places/x/reviews/1", "relativePublishTimeDescription": "a month ago", "rating": 5,
     "text": {"text": "Lovely staff and quick delivery.", "languageCode": "en"},
     "originalText": {"text": "Lovely staff and quick delivery.", "languageCode": "en"},
     "authorAttribution": {"displayName": "Asha R", "uri": "https://www.google.com/maps/contrib/1/reviews", "photoUri": "https://lh3.googleusercontent.com/a/one"},
     "publishTime": "2026-09-28T10:00:00.123Z",
     "flagContentUri": "https://www.google.com/local/review/rap/report?postId=1",
     "googleMapsUri": "https://www.google.com/maps/reviews/data=1",
     "visitDate": {"year": 2026, "month": 9}},
    {"name": "places/x/reviews/2", "relativePublishTimeDescription": "2 months ago", "rating": 1,
     "text": {"text": "The parcel came late.", "languageCode": "en"},
     "originalText": {"text": "पार्सल देर से आया।", "languageCode": "hi"},
     "authorAttribution": {"displayName": "Vikram", "uri": "javascript:alert(1)", "photoUri": "http://lh3.googleusercontent.com/a/two"},
     "publishTime": "2026-08-10T10:00:00Z",
     "googleMapsUri": "https://www.google.com/maps/reviews/data=2"},
    {"name": "places/x/reviews/3", "relativePublishTimeDescription": "3 months ago", "rating": 4,
     "authorAttribution": {"displayName": "Meera", "uri": "https://www.google.com/maps/contrib/3/reviews", "photoUri": "https://lh3.googleusercontent.com/a/three"},
     "publishTime": "2026-07-01T10:00:00Z"},
    {"name": "places/x/reviews/4", "relativePublishTimeDescription": "4 months ago", "rating": 5,
     "text": {"text": "Good.", "languageCode": "en"},
     "authorAttribution": {"displayName": "Rahul"}, "publishTime": "2026-06-01T10:00:00Z"},
    {"name": "places/x/reviews/5", "relativePublishTimeDescription": "a year ago", "rating": 3,
     "text": {"text": "Fine.", "languageCode": "en"},
     "authorAttribution": {"displayName": "Nisha"}, "publishTime": "2025-10-01T10:00:00Z"}
  ]
}`

// fakePlaces stands in for places.googleapis.com. Nothing in these tests
// calls Google.
type fakePlaces struct {
	mu     sync.Mutex
	calls  int
	paths  []string
	keys   []string
	masks  []string
	status int
	body   string
}

func (f *fakePlaces) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls++
	f.paths = append(f.paths, r.URL.Path+"?"+r.URL.RawQuery)
	f.keys = append(f.keys, r.Header.Get("X-Goog-Api-Key"))
	f.masks = append(f.masks, r.Header.Get("X-Goog-FieldMask"))
	status, body := f.status, f.body
	f.mu.Unlock()
	if status == 0 {
		status, body = http.StatusOK, placeBody
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func (f *fakePlaces) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type harness struct {
	app    *gocommerce.App
	google *fakePlaces
	logs   *syncBuffer
}

// syncBuffer is the app's log, kept so a test can look for the key in it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	google := &fakePlaces{}
	srv := gctest.StubHTTP(t, google.ServeHTTP)
	logs := &syncBuffer{}
	fd, rv := feeds.New(feeds.Config{}), reviews.New(reviews.Config{AutoApprove: true})
	app := gctest.NewWithConfig(t, gocommerce.Config{
		Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}, fd, rv, New(Config{Feeds: fd, Reviews: rv, PlacesURL: srv.URL}))
	return &harness{app: app, google: google, logs: logs}
}

func (h *harness) set(t *testing.T, on bool, settings map[string]any) {
	t.Helper()
	if _, err := h.app.Plugins().Update(context.Background(), pluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: settings}); err != nil {
		t.Fatalf("plugin settings: %v", err)
	}
}

func TestNothingIsServedOrCalledWhileOff(t *testing.T) {
	h := newHarness(t)
	order := gctest.PlaceOrder(t, h.app, gocommerce.CodeCOD).Order
	survey := "/x/google-reviews/survey/" + order.Number + "?token=" + order.AccessToken
	routes := []string{"/x/google-reviews/place", "/x/google-reviews/product-reviews.xml", survey}

	check := func(when string) {
		t.Helper()
		for _, path := range routes {
			if rec := gctest.Request(t, h.app, http.MethodGet, path, nil); rec.Code != http.StatusNotFound {
				t.Errorf("%s: GET %s = %d %s, want 404", when, path, rec.Code, rec.Body)
			}
		}
		if n := h.google.count(); n != 0 {
			t.Errorf("%s: Google was called %d times", when, n)
		}
		rec := gctest.Request(t, h.app, http.MethodGet, "/api/plugins", nil)
		if strings.Contains(rec.Body.String(), pluginKey) {
			t.Errorf("%s: the storefront is told about the plugin: %s", when, rec.Body)
		}
	}
	check("never switched on")

	// Every setting filled in but the plugin off: still nothing.
	h.set(t, false, map[string]any{
		"show_place": true, "api_key": testKey, "place_id": testPlace,
		"customer_reviews": true, "merchant_id": "1234567", "delivery_days": 5,
		"product_ratings": true,
	})
	check("off with settings")

	// On, with each of the three switches off: still nothing, and the
	// storefront is told only that they are off.
	h.set(t, true, map[string]any{"show_place": false, "customer_reviews": false, "product_ratings": false})
	for _, path := range routes {
		if rec := gctest.Request(t, h.app, http.MethodGet, path, nil); rec.Code != http.StatusNotFound {
			t.Errorf("switches off: GET %s = %d, want 404", path, rec.Code)
		}
	}
	if n := h.google.count(); n != 0 {
		t.Errorf("switches off: Google was called %d times", n)
	}
}

func TestThePlaceIsGooglesOwnAnswerReadAfreshEachTime(t *testing.T) {
	h := newHarness(t)
	h.set(t, true, map[string]any{"show_place": true, "api_key": testKey, "place_id": testPlace})

	rec := gctest.Request(t, h.app, http.MethodGet, "/x/google-reviews/place", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("place = %d: %s", rec.Code, rec.Body)
	}
	// Google's terms allow storing the place ID and nothing else of a place,
	// so neither the engine nor anything between it and the shopper keeps
	// the answer.
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var got Place
	gctest.DecodeData(t, rec, &got)
	if got.Rating != 4.6 || got.UserRatingCount != 182 || got.GoogleMapsURI != "https://maps.google.com/?cid=10281119596374313554" {
		t.Errorf("place = %+v", got)
	}
	// All five, in Google's order, the one-star review among them.
	if len(got.Reviews) != 5 {
		t.Fatalf("reviews = %d, want Google's five", len(got.Reviews))
	}
	var order []string
	for _, rv := range got.Reviews {
		order = append(order, rv.Author.Name)
	}
	if strings.Join(order, ",") != "Asha R,Vikram,Meera,Rahul,Nisha" {
		t.Errorf("order = %v, want Google's", order)
	}
	first, second := got.Reviews[0], got.Reviews[1]
	if first.Rating != 5 || first.Text != "Lovely staff and quick delivery." || first.Language != "en" ||
		first.RelativeTime != "a month ago" || first.VisitDate != "2026-09" ||
		first.Author.URI != "https://www.google.com/maps/contrib/1/reviews" ||
		first.Author.PhotoURI != "https://lh3.googleusercontent.com/a/one" ||
		first.GoogleMapsURI != "https://www.google.com/maps/reviews/data=1" ||
		first.FlagURI != "https://www.google.com/local/review/rap/report?postId=1" {
		t.Errorf("first review = %+v", first)
	}
	// The words the reviewer wrote, not Google's translation of them, so the
	// storefront never shows a translation it has not said is one.
	if second.Rating != 1 || second.Text != "पार्सल देर से आया।" || second.Language != "hi" {
		t.Errorf("second review = %+v", second)
	}
	// A link the storefront would put in an href is https or nothing.
	if second.Author.URI != "" || second.Author.PhotoURI != "" {
		t.Errorf("unsafe author links passed through: %+v", second.Author)
	}
	if got.Reviews[2].Text != "" || got.Reviews[2].Rating != 4 {
		t.Errorf("a rating with no words = %+v", got.Reviews[2])
	}

	h.google.mu.Lock()
	path, key, mask := h.google.paths[0], h.google.keys[0], h.google.masks[0]
	h.google.mu.Unlock()
	if path != "/v1/places/"+testPlace+"?" || key != testKey || mask != "rating,userRatingCount,googleMapsUri,reviews" {
		t.Errorf("request = %s key %q mask %q", path, key, mask)
	}

	// Asked again, Google is asked again.
	if rec := gctest.Request(t, h.app, http.MethodGet, "/x/google-reviews/place", nil); rec.Code != http.StatusOK {
		t.Fatalf("second read = %d", rec.Code)
	}
	if n := h.google.count(); n != 2 {
		t.Errorf("Google was called %d times for two reads, want 2: nothing may be cached", n)
	}
	// The module stores nothing of Google's: it has no table to put it in.
	if migrations := New(Config{}).Migrations(); len(migrations) != 0 {
		t.Errorf("migrations = %v, want none", migrations)
	}
}

func TestADailyLimitCapsWhatAStrangerCanSpend(t *testing.T) {
	h := newHarness(t)
	h.set(t, true, map[string]any{"show_place": true, "api_key": testKey, "place_id": testPlace, "daily_lookups": 2})
	for i := 1; i <= 3; i++ {
		rec := gctest.Request(t, h.app, http.MethodGet, "/x/google-reviews/place", nil)
		want := http.StatusOK
		if i == 3 {
			want = http.StatusTooManyRequests
		}
		if rec.Code != want {
			t.Errorf("read %d = %d, want %d: %s", i, rec.Code, want, rec.Body)
		}
	}
	if n := h.google.count(); n != 2 {
		t.Errorf("Google was called %d times, want the limit of 2", n)
	}
}

func TestTheKeyNeverLeavesTheServer(t *testing.T) {
	h := newHarness(t)
	on := true
	rec := gctest.AdminRequest(t, h.app, http.MethodPatch, "/api/admin/plugins/"+pluginKey, gocommerce.PluginPatch{
		Enabled: &on, Settings: map[string]any{"show_place": true, "api_key": testKey, "place_id": testPlace},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", rec.Code, rec.Body)
	}
	// Google refuses the key, as it does a key without Places API (New).
	h.google.mu.Lock()
	h.google.status = http.StatusForbidden
	h.google.body = `{"error": {"code": 403, "message": "Places API (New) has not been used in project 1 before or it is disabled.", "status": "PERMISSION_DENIED"}}`
	h.google.mu.Unlock()
	failed := gctest.Request(t, h.app, http.MethodGet, "/x/google-reviews/place", nil)
	if failed.Code != http.StatusBadGateway {
		t.Errorf("refused key = %d, want 502: %s", failed.Code, failed.Body)
	}

	for what, body := range map[string]string{
		"the refusal":      failed.Body.String(),
		"the admin read":   gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/plugins/"+pluginKey, nil).Body.String(),
		"the storefront":   gctest.Request(t, h.app, http.MethodGet, "/api/plugins", nil).Body.String(),
		"the audit trail":  gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/audit", nil).Body.String(),
		"the log":          h.logs.String(),
		"the place answer": gctest.Request(t, h.app, http.MethodGet, "/x/google-reviews/place", nil).Body.String(),
	} {
		if strings.Contains(body, testKey) {
			t.Errorf("%s carries the key: %s", what, body)
		}
	}
	// What was logged says why, so the owner can fix it.
	if !strings.Contains(h.logs.String(), "PERMISSION_DENIED") {
		t.Errorf("the log does not say why Google refused:\n%s", h.logs.String())
	}
}

// productReviewsFeed is the parts of Google's product reviews feed (schema
// 2.4, developers.google.com/product-review-feeds/schema) the tests read.
type productReviewsFeed struct {
	XMLName   xml.Name `xml:"feed"`
	Version   string   `xml:"version"`
	Publisher struct {
		Name string `xml:"name"`
	} `xml:"publisher"`
	Reviews []struct {
		ID       string `xml:"review_id"`
		Reviewer struct {
			Name string `xml:"name"`
		} `xml:"reviewer"`
		Verified  string `xml:"is_verified_purchase"`
		Timestamp string `xml:"review_timestamp"`
		Title     string `xml:"title"`
		Content   string `xml:"content"`
		URL       struct {
			Type  string `xml:"type,attr"`
			Value string `xml:",chardata"`
		} `xml:"review_url"`
		Overall struct {
			Min   string `xml:"min,attr"`
			Max   string `xml:"max,attr"`
			Value string `xml:",chardata"`
		} `xml:"ratings>overall"`
		Products []struct {
			GTINs  []string `xml:"product_ids>gtins>gtin"`
			MPNs   []string `xml:"product_ids>mpns>mpn"`
			SKUs   []string `xml:"product_ids>skus>sku"`
			Brands []string `xml:"product_ids>brands>brand"`
			Name   string   `xml:"product_name"`
			URL    string   `xml:"product_url"`
		} `xml:"products>product"`
	} `xml:"reviews>review"`
}

func TestTheProductRatingsFeedCarriesOnlyRealPublishedReviews(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	stock := 5
	tee, err := h.app.Products().CreateProduct(ctx, gocommerce.ProductInput{
		Title: "Crew Tee", Slug: "crew-tee", Status: "active", Vendor: "Acme",
		Options: []gocommerce.OptionInput{{Name: "Size", Values: []string{"S", "M"}}},
		Variants: []gocommerce.VariantInput{
			{SKU: "TEE-S", PriceMinor: 1999, Options: []string{"S"}, StockOnHand: &stock, Barcode: "4006381333931"},
			{SKU: "TEE-M", PriceMinor: 1999, Options: []string{"M"}, StockOnHand: &stock},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The test shopper buys it, so their reviews are verified purchases.
	gctest.Buy(t, h.app, gocommerce.CodeCOD, tee.Variants[0].ID, 1)
	post := func(name, email string, rating int, title, body string) {
		t.Helper()
		rec := gctest.Request(t, h.app, http.MethodPost, "/x/reviews", reviews.ReviewInput{
			ProductID: tee.ID, Name: name, Email: email, Rating: rating, Title: title, Body: body})
		if rec.Code != http.StatusCreated {
			t.Fatalf("review by %s = %d: %s", name, rec.Code, rec.Body)
		}
	}
	post("Asha Kumari Rao", "gctest@example.com", 5, "Soft", "Soft cotton, true to size.")
	post("GC Test", "gctest@example.com", 1, "", "Shrank after one wash.")
	// Not a buyer: Google takes only reviews from customers who bought.
	post("Stranger", "stranger@example.com", 5, "", "Looks great in the photos.")
	// Contact details in the words: Google says not to send them.
	post("GC", "gctest@example.com", 4, "", "Mail me at gc@example.com for the size chart.")
	post("GC", "gctest@example.com", 4, "", "Call +91 98765 43210 for bulk orders.")
	post("GC", "gctest@example.com", 4, "", "Cheaper at https://elsewhere.example.")
	// One the store rejected, and one written by the sample shop.
	post("Rejected", "gctest@example.com", 2, "", "Rejected words.")
	if _, err := h.app.DB().ExecContext(ctx, `UPDATE reviews SET status = 'rejected' WHERE name = 'Rejected'`); err != nil {
		t.Fatal(err)
	}
	post("Sample", "gctest@example.com", 5, "", "Sample words.")
	if _, err := h.app.DB().ExecContext(ctx, `UPDATE reviews SET sample = true WHERE name = 'Sample'`); err != nil {
		t.Fatal(err)
	}

	h.set(t, true, map[string]any{"product_ratings": true})
	// The feed names products the way the product feed does, so it waits for
	// that feed to know where the products live.
	if rec := gctest.Request(t, h.app, http.MethodGet, "/x/google-reviews/product-reviews.xml", nil); rec.Code != http.StatusConflict {
		t.Fatalf("feed without the product feed = %d: %s", rec.Code, rec.Body)
	}
	on := true
	if _, err := h.app.Plugins().Update(ctx, "product-feeds", gocommerce.PluginPatch{Enabled: &on,
		Settings: map[string]any{"storefront_url": "https://shop.example"}}); err != nil {
		t.Fatal(err)
	}

	rec := gctest.Request(t, h.app, http.MethodGet, "/x/google-reviews/product-reviews.xml", nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/xml") {
		t.Fatalf("feed = %d %s: %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `xsi:noNamespaceSchemaLocation="http://www.google.com/shopping/reviews/schema/product/2.4/product_reviews.xsd"`) {
		t.Errorf("feed does not name its schema:\n%s", body)
	}
	var feed productReviewsFeed
	if err := xml.Unmarshal(rec.Body.Bytes(), &feed); err != nil {
		t.Fatalf("feed is not XML: %v\n%s", err, body)
	}
	if feed.Version != "2.4" || feed.Publisher.Name == "" {
		t.Errorf("version %q publisher %q", feed.Version, feed.Publisher.Name)
	}
	if len(feed.Reviews) != 2 {
		t.Fatalf("reviews = %d, want the two verified, published, contact-free ones:\n%s", len(feed.Reviews), body)
	}
	byContent := map[string]int{}
	for i, rv := range feed.Reviews {
		byContent[rv.Content] = i
		// Every element the schema requires.
		if rv.ID == "" || rv.Reviewer.Name == "" || rv.Timestamp == "" || rv.Content == "" ||
			rv.URL.Value == "" || rv.Overall.Value == "" || len(rv.Products) == 0 {
			t.Errorf("review %d lacks a required element: %+v", i, rv)
		}
		if _, err := time.Parse(time.RFC3339, rv.Timestamp); err != nil {
			t.Errorf("review_timestamp %q is not xs:dateTime", rv.Timestamp)
		}
		if rv.URL.Type != "group" || rv.URL.Value != "https://shop.example/products/crew-tee" {
			t.Errorf("review_url = %+v", rv.URL)
		}
		if rv.Overall.Min != "1" || rv.Overall.Max != "5" || rv.Verified != "true" {
			t.Errorf("rating %+v verified %q", rv.Overall, rv.Verified)
		}
		// One product per variant, named as the product feed names it.
		if len(rv.Products) != 2 {
			t.Fatalf("products = %+v", rv.Products)
		}
		s, m := rv.Products[0], rv.Products[1]
		if strings.Join(s.GTINs, ",") != "4006381333931" || strings.Join(s.SKUs, ",") != "TEE-S" ||
			strings.Join(s.MPNs, ",") != "TEE-S" || strings.Join(s.Brands, ",") != "Acme" ||
			s.Name != "Crew Tee - S" || s.URL != "https://shop.example/products/crew-tee?variant=TEE-S" {
			t.Errorf("first product = %+v", s)
		}
		if len(m.GTINs) != 0 || strings.Join(m.SKUs, ",") != "TEE-M" {
			t.Errorf("second product = %+v", m)
		}
	}
	five, ok1 := byContent["Soft cotton, true to size."]
	one, ok2 := byContent["Shrank after one wash."]
	if !ok1 || !ok2 {
		t.Fatalf("want the five-star and the one-star review, got %v", byContent)
	}
	// A full name is personal information Google asks not to receive.
	if got := feed.Reviews[five].Reviewer.Name; got != "Asha R." {
		t.Errorf("reviewer = %q, want the first name and an initial", got)
	}
	if feed.Reviews[five].Title != "Soft" || feed.Reviews[five].Overall.Value != "5" || feed.Reviews[one].Overall.Value != "1" {
		t.Errorf("five = %+v one = %+v", feed.Reviews[five], feed.Reviews[one])
	}
	for _, never := range []string{"Stranger", "gc@example.com", "98765", "elsewhere", "Rejected", "Sample", "gctest@example.com"} {
		if strings.Contains(body, never) {
			t.Errorf("the feed carries %q:\n%s", never, body)
		}
	}
}

func TestTheSurveyIsBuiltFromTheOrderAlone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	order := gctest.PlaceOrder(t, h.app, gocommerce.CodeCOD).Order
	path := "/x/google-reviews/survey/" + order.Number + "?token=" + order.AccessToken

	// On, with no delivery estimate: Google needs one, and the store has not
	// said, so there is no survey rather than a guessed date.
	h.set(t, true, map[string]any{"customer_reviews": true, "merchant_id": "1234567"})
	if rec := gctest.Request(t, h.app, http.MethodGet, path, nil); rec.Code != http.StatusNotFound {
		t.Errorf("no delivery estimate = %d, want 404", rec.Code)
	}
	h.set(t, true, map[string]any{"delivery_days": 5, "opt_in_style": "Bottom tray"})

	rec := gctest.Request(t, h.app, http.MethodGet, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("survey = %d: %s", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	var got map[string]any
	gctest.DecodeData(t, rec, &got)
	want := map[string]any{
		"merchant_id": float64(1234567), "order_id": order.Number, "email": "gctest@example.com",
		"delivery_country":        "US",
		"estimated_delivery_date": order.CreatedAt.UTC().AddDate(0, 0, 5).Format("2006-01-02"),
		"opt_in_style":            "BOTTOM_TRAY",
	}
	if g, w := mustJSON(t, got), mustJSON(t, want); g != w {
		t.Errorf("survey = %s\nwant     %s", g, w)
	}

	// Somebody else's token, or none, opens nothing.
	for _, bad := range []string{"/x/google-reviews/survey/" + order.Number + "?token=wrong", "/x/google-reviews/survey/" + order.Number} {
		if rec := gctest.Request(t, h.app, http.MethodGet, bad, nil); rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d", bad, rec.Code)
		}
	}
	// A merchant ID that is not Merchant Center's number is not sent.
	h.set(t, true, map[string]any{"merchant_id": "my-shop"})
	if rec := gctest.Request(t, h.app, http.MethodGet, path, nil); rec.Code != http.StatusNotFound {
		t.Errorf("non-numeric merchant ID = %d, want 404", rec.Code)
	}
	h.set(t, true, map[string]any{"merchant_id": "1234567"})
	// A cancelled order was not a purchase, so nobody is asked about it.
	if _, err := h.app.Order().Cancel(ctx, order.ID, "test"); err != nil {
		t.Fatal(err)
	}
	if rec := gctest.Request(t, h.app, http.MethodGet, path, nil); rec.Code != http.StatusNotFound {
		t.Errorf("cancelled order = %d, want 404", rec.Code)
	}
}

func TestTheModuleDeclaresItsRoutes(t *testing.T) {
	h := newHarness(t)
	gctest.AssertSpecCoversModuleRoutes(t, h.app, "google-reviews")
}

func TestReviewerNames(t *testing.T) {
	for in, want := range map[string]string{
		"Asha":            "Asha",
		"Asha Rao":        "Asha R.",
		"Asha Kumari Rao": "Asha R.",
		"  Ravi   Śarma ": "Ravi Ś.",
		"अनीता शर्मा":     "अनीता श.",
		"":                "",
	} {
		if got := reviewerName(in); got != want {
			t.Errorf("reviewerName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestContactDetailsAreFound(t *testing.T) {
	for text, want := range map[string]bool{
		"Mail me at a.b@example.co.in":       true,
		"see www.example.com":                true,
		"see https://example.com/x":          true,
		"Call +91 98765 43210":               true,
		"call 080-2345-6789 now":             true,
		"Size 10, fits a 32 inch waist":      false,
		"Ordered 3 on 12/10/2026, all good.": false,
		"Bought 2 for 1999 each":             false,
	} {
		if got := hasContact(text); got != want {
			t.Errorf("hasContact(%q) = %v, want %v", text, got, want)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
