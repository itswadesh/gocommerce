package amazon

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misiki/gocommerce/core"
	"github.com/misiki/gocommerce/gctest"
)

// ------------------------------------------------------------ pure pieces

func TestParsePrice(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		text, currency string
		want           int64
		ok             bool
	}{
		{"$19.99", "USD", 1999, true},
		{"$1,299.00", "USD", 129900, true},
		{"₹1,29,900.00", "INR", 12990000, true},
		{"₹1,29,900", "INR", 12990000, true},
		// A decimal comma, grouping dots: the European shape.
		{"1.299,00 €", "EUR", 129900, true},
		{"19,99 €", "EUR", 1999, true},
		{"£7.50", "GBP", 750, true},
		// A yen has no minor unit, so the comma is grouping.
		{"￥1,200", "JPY", 1200, true},
		{"BD 12.345", "BHD", 12345, true},
		{"Currently unavailable", "USD", 0, false},
		{"", "USD", 0, false},
	} {
		got, ok := parsePrice(tc.text, tc.currency)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parsePrice(%q, %s) = %d, %v; want %d, %v", tc.text, tc.currency, got, ok, tc.want, tc.ok)
		}
	}
}

// The session, not the host, decides the currency: a browser whose delivery
// address is in Finland sees amazon.com in euros.
func TestCurrencyFromPrice(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ text, fallback, want string }{
		{"EUR29.72", "USD", "EUR"},
		{"€ 29,72", "USD", "EUR"},
		{"£7.50", "USD", "GBP"},
		{"₹1,299.00", "USD", "INR"},
		{"$19.99", "USD", "USD"},
		{"$19.99", "CAD", "CAD"},
		{"", "INR", "INR"},
	} {
		if got := currencyFromPrice(tc.text, tc.fallback); got != tc.want {
			t.Errorf("currencyFromPrice(%q, %s) = %s, want %s", tc.text, tc.fallback, got, tc.want)
		}
	}
}

func TestCurrencyForHost(t *testing.T) {
	t.Parallel()
	for host, want := range map[string]string{
		"www.amazon.com": "USD", "www.amazon.in": "INR", "www.amazon.co.uk": "GBP",
		"www.amazon.de": "EUR", "www.amazon.co.jp": "JPY", "www.amazon.com.au": "AUD",
		"amazon.ae": "AED", "www.amazon.ca": "CAD",
	} {
		if got := currencyForHost(host); got != want {
			t.Errorf("currencyForHost(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestParseRewrite(t *testing.T) {
	t.Parallel()
	for name, text := range map[string]string{
		"bare JSON":   `{"title":"Wireless earbuds","description":"<p>x</p>","tags":["a"]}`,
		"fenced JSON": "```json\n{\"title\":\"Wireless earbuds\",\"description\":\"<p>x</p>\"}\n```",
		"prose first": "Here you go:\n{\"title\":\"Wireless earbuds\",\"description\":\"<p>x</p>\"}\nHope that helps.",
	} {
		rw, err := parseRewrite(text)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if rw.Title != "Wireless earbuds" {
			t.Errorf("%s: title = %q", name, rw.Title)
		}
	}
	if _, err := parseRewrite(`{"tags":["only"]}`); err == nil {
		t.Error("an answer with neither title nor description was accepted")
	}
	if _, err := parseRewrite("not json at all"); err == nil {
		t.Error("prose was accepted as a rewrite")
	}
}

// testImage is a 64x32 picture: dark on the left, bright on the right, with a
// gradient down the rows. Big enough that a JPEG round trip keeps each half
// its own colour — an 8x8 block never straddles the middle — and shaped so a
// flip and a rotation can be told apart from each other and from nothing.
func testImage() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 64, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 64; x++ {
			r := uint8(20)
			if x >= 32 {
				r = 220
			}
			img.SetRGBA(x, y, color.RGBA{R: r, G: uint8(y * 4), B: 100, A: 255})
		}
	}
	return img
}

func TestAdjustLighting(t *testing.T) {
	t.Parallel()
	src := testImage()
	out := adjust(src, imageOptions{Brightness: 0.1})
	if luma(out.RGBAAt(1, 1)) <= luma(src.RGBAAt(1, 1)) {
		t.Error("brightness did not make the picture lighter")
	}
	// Clamped, not wrapped: a bright pixel pushed past white stays white.
	white := image.NewRGBA(image.Rect(0, 0, 1, 1))
	white.SetRGBA(0, 0, color.RGBA{250, 250, 250, 255})
	if got := adjust(white, imageOptions{Brightness: 0.5}).RGBAAt(0, 0); got.R != 255 || got.A != 255 {
		t.Errorf("pushed past white = %+v, want 255 with alpha kept", got)
	}
	// Contrast pulls a mid-grey nowhere and a dark pixel darker.
	dark := image.NewRGBA(image.Rect(0, 0, 1, 1))
	dark.SetRGBA(0, 0, color.RGBA{40, 40, 40, 255})
	if got := adjust(dark, imageOptions{Contrast: 1.5}).RGBAAt(0, 0); got.R >= 40 {
		t.Errorf("contrast 1.5 on 40 = %d, want darker", got.R)
	}
}

func TestAdjustOrientation(t *testing.T) {
	t.Parallel()
	src := testImage()

	h := adjust(src, imageOptions{Flip: "horizontal"})
	if h.RGBAAt(0, 0) != src.RGBAAt(63, 0) || h.RGBAAt(63, 31) != src.RGBAAt(0, 31) {
		t.Error("horizontal flip did not mirror left to right")
	}
	v := adjust(src, imageOptions{Flip: "vertical"})
	if v.RGBAAt(0, 0) != src.RGBAAt(0, 31) {
		t.Error("vertical flip did not mirror top to bottom")
	}
	r := adjust(src, imageOptions{Rotate: 90})
	if b := r.Bounds(); b.Dx() != 32 || b.Dy() != 64 {
		t.Fatalf("rotated bounds = %v, want 32x64", b)
	}
	// Clockwise: the top-left pixel ends up top-right.
	if r.RGBAAt(31, 0) != src.RGBAAt(0, 0) {
		t.Error("90° rotation is not clockwise")
	}
	full := adjust(src, imageOptions{Rotate: 180})
	if full.RGBAAt(0, 0) != src.RGBAAt(63, 31) {
		t.Error("180° rotation did not turn the picture over")
	}
	if !(imageOptions{}).noop() || (imageOptions{Flip: "horizontal"}).noop() {
		t.Error("noop is wrong about what changes a picture")
	}
	if err := (imageOptions{Rotate: 45}).validate(); err == nil {
		t.Error("a 45° rotation was accepted")
	}
}

// ------------------------------------------------------------- the module

// fixtures is the stub browser: a map of URL → what the extraction script
// would have returned there.
type fixtures struct {
	mu    sync.Mutex
	pages map[string]*page
	seen  []string
}

func (f *fixtures) fetch(_ context.Context, url string) (*page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, url)
	if p, ok := f.pages[url]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("fixture: no page at %s", url)
}

func (f *fixtures) close() error { return nil }

func (f *fixtures) visited() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

// jpegBytes encodes the test picture, which is what the stub image host serves.
func jpegBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, testImage(), &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type harness struct {
	app    *gocommerce.App
	fx     *fixtures
	images *httptest.Server
	llm    *llmStub
}

type llmStub struct {
	mu     sync.Mutex
	calls  int
	answer string
	status int
}

func newHarness(t *testing.T, cfg Config, withLLM bool) *harness {
	t.Helper()
	img := jpegBytes(t)
	images := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/missing.jpg") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(img)
	})

	llm := &llmStub{answer: `{"title":"Rewritten title","description":"<p>Rewritten copy.</p>","product_type":"Test thing","vendor":"Acme","tags":["one","two"],"seo_title":"Rewritten title","seo_description":"A test thing for testers."}`}
	if withLLM {
		anthropic := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
			llm.mu.Lock()
			llm.calls++
			answer, status := llm.answer, llm.status
			llm.mu.Unlock()
			if r.Header.Get("x-api-key") != "sk-test" || r.Header.Get("anthropic-version") == "" {
				http.Error(w, `{"error":{"type":"authentication_error","message":"bad key"}}`, http.StatusUnauthorized)
				return
			}
			if status != 0 {
				http.Error(w, `{"error":{"type":"overloaded_error","message":"Overloaded"}}`, status)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			payload, _ := json.Marshal(map[string]any{
				"content": []map[string]any{{"type": "text", "text": answer}},
			})
			_, _ = w.Write(payload)
		})
		cfg.AnthropicAPIKey, cfg.AnthropicBaseURL = "sk-test", anthropic.URL
	}

	fx := &fixtures{pages: map[string]*page{}}
	cfg.JobTimeout = 30 * time.Second
	mod := New(cfg)
	mod.newFetcher = func(context.Context) (pageFetcher, error) { return fx, nil }

	app := gctest.NewWithConfig(t, gocommerce.Config{Currency: "USD", MediaDir: t.TempDir()}, mod)
	return &harness{app: app, fx: fx, images: images, llm: llm}
}

// listingWithVariants is a fixture in the shape Amazon's twister leaves: a
// parent naming three ASINs across two dimensions, each with its own page.
func (h *harness) listingWithVariants() string {
	base := "https://www.amazon.com"
	pic := func(name string) string { return h.images.URL + "/" + name }
	parent := &page{
		URL: base + "/dp/B0PARENT01", ASIN: "B0PARENT01", Title: "Acme Widget Pro, 2-Pack",
		Brand: "Visit the Acme Store", Bullets: []string{"Sturdy", "Blue"},
		Description: "A widget.", Specs: [][]string{{"Material", "Steel"}, {"Weight", "1 lb"}},
		Price: "$19.99", Available: true, Breadcrumbs: []string{"Tools", "Widgets"},
		Images: []string{pic("parent-1.jpg"), pic("parent-2.jpg")},
		Twister: &twister{
			Dimensions: []string{"color_name", "size_name"},
			Labels:     map[string]string{"color_name": "Color", "size_name": "Size"},
			Values: map[string][]string{
				"B0PARENT01": {"Blue", "Small"},
				"B0CHILD002": {"Blue", "Large"},
				"B0CHILD003": {"Red", "Small"},
			},
			Order:   map[string][]string{"color_name": {"Blue", "Red"}, "size_name": {"Small", "Large"}},
			Current: "B0PARENT01", Parent: "B0FAMILY00",
		},
	}
	h.fx.pages[parent.URL] = parent
	h.fx.pages[base+"/dp/B0CHILD002"] = &page{
		ASIN: "B0CHILD002", Title: parent.Title, Price: "$24.99", Available: true,
		Images: []string{pic("large-1.jpg")},
	}
	h.fx.pages[base+"/dp/B0CHILD003"] = &page{
		ASIN: "B0CHILD003", Title: parent.Title, Price: "$21.49", Available: false,
		Images: []string{pic("red-1.jpg"), pic("missing.jpg")},
	}
	return parent.URL
}

func (h *harness) start(t *testing.T, body map[string]any) Job {
	t.Helper()
	rec := gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/import-amazon/jobs", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	var job Job
	gctest.DecodeData(t, rec, &job)
	return job
}

// wait polls the job the way the panel does, until it stops running.
func (h *harness) wait(t *testing.T, id int64) Job {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		rec := gctest.AdminRequest(t, h.app, http.MethodGet,
			"/api/admin/x/import-amazon/jobs/"+fmt.Sprint(id), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET job = %d: %s", rec.Code, rec.Body)
		}
		var job Job
		gctest.DecodeData(t, rec, &job)
		if job.Status != StatusQueued && job.Status != StatusRunning {
			return job
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the import did not finish in time")
	return Job{}
}

// The module's reason to exist, end to end: a listing with variations becomes
// a draft product with options, per-variant prices, and adjusted pictures.
func TestImportCreatesTheProductWithVariantsAndPictures(t *testing.T) {
	h := newHarness(t, Config{}, true)
	url := h.listingWithVariants()

	job := h.wait(t, h.start(t, map[string]any{"url": url}).ID)
	if job.Status != StatusDone {
		t.Fatalf("status = %s: %s (warnings %v)", job.Status, job.Message, job.Warnings)
	}
	if job.ASIN != "B0PARENT01" || job.ProductID == nil {
		t.Fatalf("job = %+v, want the ASIN and a product", job)
	}
	// Every variant's own page was visited, the parent's only once.
	visited := h.fx.visited()
	if len(visited) != 3 {
		t.Errorf("visited %v, want the parent and two children", visited)
	}

	product, err := h.app.Products().GetProduct(context.Background(), *job.ProductID)
	if err != nil {
		t.Fatalf("get product: %v", err)
	}
	if product.Status != "draft" {
		t.Errorf("status = %q, want draft — an import is for review", product.Status)
	}
	// The rewrite won, the listing's own copy did not.
	if product.Title != "Rewritten title" || product.Vendor != "Acme" || product.ProductType != "Test thing" {
		t.Errorf("product = %q / %q / %q, want the rewritten fields", product.Title, product.Vendor, product.ProductType)
	}
	if product.SEOTitle == "" || len(product.Tags) != 2 {
		t.Errorf("seo title %q, tags %v — the rewrite's extras were dropped", product.SEOTitle, product.Tags)
	}
	if len(product.Options) != 2 || product.Options[0].Name != "Color" || product.Options[1].Name != "Size" {
		t.Errorf("options = %+v, want Color and Size from the widget's labels", product.Options)
	}
	if len(product.Variants) != 3 {
		t.Fatalf("variants = %d, want 3", len(product.Variants))
	}
	bySKU := map[string]gocommerce.Variant{}
	for _, v := range product.Variants {
		bySKU[v.SKU] = v
	}
	if bySKU["B0CHILD002"].Price.AmountMinor != 2499 || bySKU["B0PARENT01"].Price.AmountMinor != 1999 {
		t.Errorf("prices = %d / %d, want each variant's own", bySKU["B0CHILD002"].Price.AmountMinor, bySKU["B0PARENT01"].Price.AmountMinor)
	}
	if got := bySKU["B0CHILD003"].Options; len(got) != 2 || got[0] != "Red" || got[1] != "Small" {
		t.Errorf("B0CHILD003 options = %v, want Red, Small", got)
	}
	meta, _ := product.Metadata["amazon"].(map[string]any)
	if meta["asin"] != "B0PARENT01" || meta["currency"] != "USD" {
		t.Errorf("metadata.amazon = %v, want the ASIN and currency recorded", meta)
	}

	// Pictures: parent's two, then each child's own; the one that 404ed is a
	// warning, not a failure. Each variant nominates its own first picture.
	media, err := h.app.MediaLibrary().ForProduct(context.Background(), product.ID)
	if err != nil {
		t.Fatalf("product media: %v", err)
	}
	if len(media) != 4 {
		t.Errorf("attached %d pictures, want 4 (2 parent + large + red; missing.jpg skipped)", len(media))
	}
	if !containsPrefix(job.Warnings, "could not download") {
		t.Errorf("warnings = %v, want the 404 reported", job.Warnings)
	}
	var nominated int
	for _, pm := range media {
		if pm.VariantID != nil {
			nominated++
		}
	}
	if nominated != 3 {
		t.Errorf("%d variants nominate a picture, want all 3", nominated)
	}

	// The stored picture was adjusted: mirrored, so the bright half is now on
	// the left, and lighter than the source. Sampled well inside each half,
	// where JPEG's blocks have not touched the boundary.
	stored := readStoredImage(t, h.app, media[0].URL)
	if b := stored.Bounds(); b.Dx() != 64 || b.Dy() != 32 {
		t.Fatalf("stored bounds = %v, want 64x32", b)
	}
	left, right := stored.RGBAAt(8, 16), stored.RGBAAt(56, 16)
	if left.R < 180 || right.R > 80 {
		t.Errorf("stored red left/right = %d/%d, want bright on the left and dark on the right after mirroring", left.R, right.R)
	}
	// 220 lifted by 0.06 and stretched by 1.05 is about 232; JPEG keeps it
	// within a few.
	if left.R < 224 {
		t.Errorf("stored bright half red = %d, want it lighter than the source's 220", left.R)
	}
}

// Without a key the listing's own copy is used, and the job says so rather
// than failing: a working import with worse copy beats no import.
func TestImportWithoutAKeyUsesTheListingCopy(t *testing.T) {
	h := newHarness(t, Config{}, false)
	url := h.listingWithVariants()

	job := h.wait(t, h.start(t, map[string]any{"url": url}).ID)
	if job.Status != StatusDone {
		t.Fatalf("status = %s: %s", job.Status, job.Message)
	}
	if !containsPrefix(job.Warnings, "no Anthropic API key") {
		t.Errorf("warnings = %v, want the missing key named", job.Warnings)
	}
	product, err := h.app.Products().GetProduct(context.Background(), *job.ProductID)
	if err != nil {
		t.Fatal(err)
	}
	if product.Title != "Acme Widget Pro, 2-Pack" || product.Vendor != "Acme" {
		t.Errorf("title %q vendor %q, want the listing's own, with the byline cleaned", product.Title, product.Vendor)
	}
	if !strings.Contains(product.Description, "<li>Sturdy</li>") {
		t.Errorf("description = %q, want the bullets as a list", product.Description)
	}
}

// A rewrite that fails is a warning and a fallback, never a failed import.
func TestARewriteFailureFallsBack(t *testing.T) {
	h := newHarness(t, Config{}, true)
	h.llm.mu.Lock()
	h.llm.status = http.StatusServiceUnavailable
	h.llm.mu.Unlock()
	url := h.listingWithVariants()

	job := h.wait(t, h.start(t, map[string]any{"url": url}).ID)
	if job.Status != StatusDone {
		t.Fatalf("status = %s: %s", job.Status, job.Message)
	}
	if !containsPrefix(job.Warnings, "the rewrite failed") {
		t.Errorf("warnings = %v, want the failed rewrite named", job.Warnings)
	}
}

// A session whose delivery address the item cannot ship to sees no price.
// That is a warning naming the fix, not a wrong price and not a failure.
func TestUnshippableListingWarnsAboutTheDeliveryLocation(t *testing.T) {
	h := newHarness(t, Config{}, false)
	h.fx.pages["https://www.amazon.com/dp/B0NOSHIP00"] = &page{
		ASIN: "B0NOSHIP00", Title: "Far away thing", Unshippable: true, Available: true,
		Images: []string{h.images.URL + "/x.jpg"},
	}
	job := h.wait(t, h.start(t, map[string]any{"url": "https://www.amazon.com/dp/B0NOSHIP00"}).ID)
	if job.Status != StatusDone {
		t.Fatalf("status = %s: %s", job.Status, job.Message)
	}
	if !containsPrefix(job.Warnings, "Amazon says this item cannot be shipped") {
		t.Errorf("warnings = %v, want the delivery location named", job.Warnings)
	}
	product, _ := h.app.Products().GetProduct(context.Background(), *job.ProductID)
	if got := product.DefaultVariant().Price.AmountMinor; got != 0 {
		t.Errorf("price = %d, want none rather than a guess", got)
	}
}

// Amazon's robot check is its own outcome, with the fix in the message.
func TestARobotCheckIsBlockedNotFailed(t *testing.T) {
	h := newHarness(t, Config{}, false)
	h.fx.pages["https://www.amazon.com/dp/B0BLOCKED0"] = &page{Blocked: true}

	job := h.wait(t, h.start(t, map[string]any{"url": "https://www.amazon.com/dp/B0BLOCKED0"}).ID)
	if job.Status != StatusBlocked {
		t.Fatalf("status = %s, want blocked", job.Status)
	}
	if !strings.Contains(job.Message, "Headed") {
		t.Errorf("message = %q, want it to say how to get past the check", job.Message)
	}
	if job.ProductID != nil {
		t.Error("a blocked import created a product")
	}
}

// Prices do not cross currencies. They are recorded, not converted, and the
// product stays a draft with a warning — unless the operator gave a price.
func TestPricesInAnotherCurrencyAreNotSet(t *testing.T) {
	h := newHarness(t, Config{}, false)
	pic := h.images.URL + "/x.jpg"
	h.fx.pages["https://www.amazon.in/dp/B0INDIA001"] = &page{
		ASIN: "B0INDIA001", Title: "Widget", Price: "₹1,299.00", Available: true, Images: []string{pic},
	}

	job := h.wait(t, h.start(t, map[string]any{"url": "https://www.amazon.in/dp/B0INDIA001", "status": "active"}).ID)
	if job.Status != StatusDone {
		t.Fatalf("status = %s: %s", job.Status, job.Message)
	}
	product, _ := h.app.Products().GetProduct(context.Background(), *job.ProductID)
	if product.Status != "draft" {
		t.Errorf("status = %q, want draft forced — it has no prices", product.Status)
	}
	if got := product.DefaultVariant().Price.AmountMinor; got != 0 {
		t.Errorf("price = %d, want 0 — rupees are not dollars", got)
	}
	if !containsPrefix(job.Warnings, "prices are in INR") {
		t.Errorf("warnings = %v", job.Warnings)
	}
	meta, _ := product.Metadata["amazon"].(map[string]any)
	if fmt.Sprint(meta["price_minor"]) != "129900" || meta["currency"] != "INR" {
		t.Errorf("metadata.amazon = %v, want the rupee price on record", meta)
	}

	// The operator's price applies to every variant, and then the status holds.
	job = h.wait(t, h.start(t, map[string]any{"url": "https://www.amazon.in/dp/B0INDIA001", "status": "active", "price_minor": 1599}).ID)
	if job.Status != StatusDone {
		t.Fatalf("status = %s: %s", job.Status, job.Message)
	}
	product, _ = h.app.Products().GetProduct(context.Background(), *job.ProductID)
	if product.DefaultVariant().Price.AmountMinor != 1599 || product.Status != "active" {
		t.Errorf("price %d status %q, want 1599 and active", product.DefaultVariant().Price.AmountMinor, product.Status)
	}
}

// The same listing twice: the second slug carries the ASIN rather than failing.
func TestImportingTwiceDoesNotCollideOnTheSlug(t *testing.T) {
	h := newHarness(t, Config{}, false)
	url := h.listingWithVariants()
	first := h.wait(t, h.start(t, map[string]any{"url": url}).ID)
	second := h.wait(t, h.start(t, map[string]any{"url": url}).ID)
	if first.Status != StatusDone || second.Status != StatusDone {
		t.Fatalf("statuses = %s / %s: %s", first.Status, second.Status, second.Message)
	}
	a, _ := h.app.Products().GetProduct(context.Background(), *first.ProductID)
	b, _ := h.app.Products().GetProduct(context.Background(), *second.ProductID)
	if a.Slug == b.Slug || !strings.HasSuffix(b.Slug, "b0parent01") {
		t.Errorf("slugs = %q / %q, want the second to carry the ASIN", a.Slug, b.Slug)
	}
}

func TestVariantsPastTheCapCarryTheParent(t *testing.T) {
	h := newHarness(t, Config{}, false)
	url := h.listingWithVariants()

	job := h.wait(t, h.start(t, map[string]any{"url": url, "max_variants": 1}).ID)
	if job.Status != StatusDone {
		t.Fatalf("status = %s: %s", job.Status, job.Message)
	}
	if len(h.fx.visited()) != 2 {
		t.Errorf("visited %v, want the parent and one child", h.fx.visited())
	}
	if !containsPrefix(job.Warnings, "variant B0CHILD003 was not visited") {
		t.Errorf("warnings = %v, want the unvisited variant named", job.Warnings)
	}
	product, _ := h.app.Products().GetProduct(context.Background(), *job.ProductID)
	for _, v := range product.Variants {
		if v.SKU == "B0CHILD003" && v.Price.AmountMinor != 1999 {
			t.Errorf("unvisited variant price = %d, want the parent's 1999", v.Price.AmountMinor)
		}
	}
}

func TestRequestValidation(t *testing.T) {
	h := newHarness(t, Config{}, false)
	for name, body := range map[string]map[string]any{
		"no url":         {},
		"not amazon":     {"url": "https://example.com/dp/B0X"},
		"bad flip":       {"url": "https://www.amazon.com/dp/B0X", "images": map[string]any{"flip": "diagonal"}},
		"bad rotate":     {"url": "https://www.amazon.com/dp/B0X", "images": map[string]any{"rotate": 45}},
		"bad status":     {"url": "https://www.amazon.com/dp/B0X", "status": "archived"},
		"negative price": {"url": "https://www.amazon.com/dp/B0X", "price_minor": -1},
		"unknown field":  {"url": "https://www.amazon.com/dp/B0X", "nope": true},
	} {
		if rec := gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/import-amazon/jobs", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: POST = %d, want 400", name, rec.Code)
		}
	}
	if rec := gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/import-amazon/jobs/999999", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown job = %d, want 404", rec.Code)
	}
}

func TestRoutesAreGatedAndDocumented(t *testing.T) {
	h := newHarness(t, Config{}, false)
	if rec := gctest.Request(t, h.app, http.MethodGet, "/api/admin/x/import-amazon/jobs", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated list = %d, want 401", rec.Code)
	}
	gctest.AssertAdminRoutesDeclareRights(t, h.app, "import-amazon")
	gctest.AssertSpecCoversModuleRoutes(t, h.app, "import-amazon")
}

// ------------------------------------------------------ the browser client

// fakeCDP is enough of Chrome's DevTools endpoint to prove the WebSocket
// framing and the request/reply/event plumbing: it completes the handshake,
// answers a couple of methods, fires an event on navigate, and pings.
func fakeCDP(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/version" {
			_, _ = fmt.Fprintf(w, `{"webSocketDebuggerUrl":"ws://%s/devtools/browser/fake"}`, r.Host)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("no hijacker")
		}
		key := r.Header.Get("Sec-WebSocket-Key")
		conn, rw, err := hj.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		accept := wsAccept(key)
		_, _ = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept)
		_ = rw.Flush()
		srv := &wsConn{conn: conn, r: rw.Reader}

		send := func(v any) {
			b, _ := json.Marshal(v)
			_ = srv.writeServerText(b)
		}
		// A ping first: the client must answer without surfacing it.
		_ = srv.writeServerFrame(opPing, []byte("hi"))
		for {
			raw, err := srv.readMessage()
			if err != nil {
				return
			}
			var msg cdpMessage
			_ = json.Unmarshal(raw, &msg)
			switch msg.Method {
			case "Target.createTarget":
				send(map[string]any{"id": msg.ID, "result": map[string]any{"targetId": "t1"}})
			case "Target.attachToTarget":
				send(map[string]any{"id": msg.ID, "result": map[string]any{"sessionId": "s1"}})
			case "Page.enable":
				send(map[string]any{"id": msg.ID, "result": map[string]any{}})
			case "Page.navigate":
				// The event before the reply, which is what Chrome does for
				// a fast page — and what the waiter registered ahead is for.
				send(map[string]any{"method": "Page.loadEventFired", "sessionId": msg.SessionID, "params": map[string]any{"timestamp": 1}})
				send(map[string]any{"id": msg.ID, "result": map[string]any{"frameId": "f1"}})
			case "Runtime.evaluate":
				// A large payload, to cross the 16-bit length boundary.
				big := strings.Repeat("x", 70000)
				send(map[string]any{"id": msg.ID, "sessionId": msg.SessionID, "result": map[string]any{
					"result": map[string]any{"type": "string", "value": `{"asin":"B0FAKE","big":"` + big + `"}`},
				}})
			case "Boom":
				send(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32000, "message": "no such thing"}})
			default:
				send(map[string]any{"id": msg.ID, "result": map[string]any{}})
			}
		}
	}))
}

func wsAccept(key string) string {
	return base64Sum(key + wsGUID)
}

func TestDevToolsClientOverTheWebSocket(t *testing.T) {
	srv := fakeCDP(t)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := getJSON(ctx, srv.URL+"/json/version", &version); err != nil {
		t.Fatal(err)
	}
	ws, err := dialWS(ctx, version.WebSocketDebuggerURL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	b := &browser{ws: ws, done: make(chan struct{})}
	go b.readLoop()
	defer ws.close()

	tab, err := b.newTab(ctx)
	if err != nil {
		t.Fatalf("new tab: %v", err)
	}
	if tab.sessionID != "s1" {
		t.Errorf("session = %q", tab.sessionID)
	}
	if err := tab.navigate(ctx, "https://example.test/", 0); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	var raw string
	if err := tab.evaluate(ctx, "1", &raw); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	var got struct {
		ASIN string `json:"asin"`
		Big  string `json:"big"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil || got.ASIN != "B0FAKE" || len(got.Big) != 70000 {
		t.Errorf("evaluate returned %q… (%v)", raw[:min(40, len(raw))], err)
	}
	if err := b.call(ctx, "s1", "Boom", nil, nil); err == nil || !strings.Contains(err.Error(), "no such thing") {
		t.Errorf("a protocol error was not surfaced: %v", err)
	}
}

// A robot check is recognised even when Chrome was reachable — the test that
// keeps the "blocked" outcome from silently becoming "no product on the page".
func TestCrawlReportsBlocked(t *testing.T) {
	t.Parallel()
	fx := &fixtures{pages: map[string]*page{"https://www.amazon.com/dp/B0X": {Blocked: true}}}
	if _, _, err := crawl(context.Background(), fx, "https://www.amazon.com/dp/B0X", 5); !errors.Is(err, errBlocked) {
		t.Errorf("err = %v, want errBlocked", err)
	}
	if _, _, err := crawl(context.Background(), fx, "https://example.com/dp/B0X", 5); err == nil {
		t.Error("a non-Amazon host was crawled")
	}
}

// ------------------------------------------------------------------ helpers

func containsPrefix(list []string, prefix string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// readStoredImage fetches a stored picture back through the store's own media
// route and decodes it.
func readStoredImage(t *testing.T, app *gocommerce.App, url string) *image.RGBA {
	t.Helper()
	rec := gctest.Request(t, app, http.MethodGet, url, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", url, rec.Code)
	}
	img, _, err := image.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode the stored picture: %v", err)
	}
	return toRGBA(img)
}

// Server-side frames for the fake: unmasked, which is what the client must
// accept and what a real Chrome sends.
func (c *wsConn) writeServerText(payload []byte) error { return c.writeServerFrame(opText, payload) }

func (c *wsConn) writeServerFrame(opcode byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	header := []byte{0x80 | opcode}
	n := len(payload)
	switch {
	case n < 126:
		header = append(header, byte(n))
	case n <= 0xFFFF:
		header = append(header, 126, byte(n>>8), byte(n))
	default:
		header = append(header, 127, 0, 0, 0, 0, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	_, err := io.Copy(c.conn, bytes.NewReader(payload))
	return err
}

// base64Sum is the handshake's accept key: base64 of the SHA-1 of key+GUID.
func base64Sum(s string) string {
	sum := sha1.Sum([]byte(s))
	return base64.StdEncoding.EncodeToString(sum[:])
}
