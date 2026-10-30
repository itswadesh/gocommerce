package photoproducts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	aigateway "github.com/itswadesh/gocommerce/ext/ai-gateway"
	"github.com/itswadesh/gocommerce/gctest"
)

// claude answers in Anthropic's place: the gateway's hosts are constants, so
// it is handed a client that sends them here. It tells the two tasks apart by
// their instructions and answers each with what the test set.
type claude struct {
	mu       sync.Mutex
	group    string
	describe func(n int) (int, string)
	calls    map[string]int
	srv      *httptest.Server
	// onCall, when set, sees each call while the gateway holds its credit.
	onCall func(task string, body []byte)
}

func (c *claude) count(task string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[task]
}

func (c *claude) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	task := "describe"
	if bytes.Contains(body, []byte("You sort a shop")) {
		task = "group"
	}
	c.calls[task]++
	n := c.calls[task]
	answer, status := c.group, http.StatusOK
	if task == "describe" {
		status, answer = c.describe(n)
	}
	onCall := c.onCall
	c.mu.Unlock()
	if onCall != nil {
		onCall(task, body)
	}
	w.Header().Set("Content-Type", "application/json")
	if status != http.StatusOK {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"`+answer+`"}}`)
		return
	}
	text, _ := json.Marshal(answer)
	_, _ = fmt.Fprintf(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5",
	  "content":[{"type":"text","text":%s}],"stop_reason":"end_turn",
	  "usage":{"input_tokens":2000,"output_tokens":300,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`, text)
}

const listing = `{"title":"Floral hoop earrings","description":"Hoop earrings with a floral pattern.\n\nA shiny finish.",
  "highlights":["Floral pattern","Shiny finish","This highlight runs on well past the eighty characters a buy box line may hold, so it is cut"],
  "product_type":"Earrings","tags":["Hoops","floral"],"category_id":null,"suggested_category":"Earrings",
  "seo_title":"Floral hoop earrings","seo_description":"Floral hoop earrings with a shiny finish.","hs_code":"7117.19",
  "option_name":"Finish","variant_values":["Gold tone","Silver tone"],
  "alt_texts":[{"photo":1,"alt":"Gold hoop earrings, front"},{"photo":2,"alt":"Silver hoop earrings"},{"photo":9,"alt":"none"}]}`

type harness struct {
	app    *gocommerce.App
	claude *claude
	mod    *Module
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessOn(t, aigateway.Config{})
}

// newHarnessOn is newHarness with the gateway configured by gw.
func newHarnessOn(t *testing.T, gw aigateway.Config) *harness {
	t.Helper()
	c := &claude{calls: map[string]int{}, describe: func(int) (int, string) { return http.StatusOK, listing }}
	c.srv = httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(c.srv.Close)
	to, _ := url.Parse(c.srv.URL)
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		out := r.Clone(r.Context())
		out.URL.Scheme, out.URL.Host, out.Host = to.Scheme, to.Host, to.Host
		return http.DefaultTransport.RoundTrip(out)
	})}
	gw.Client, gw.PollInterval = client, time.Hour
	gateway := aigateway.New(gw)
	mod := New(Config{Gateway: gateway})
	app := gctest.NewWithConfig(t, gocommerce.Config{Currency: "INR", MediaDir: t.TempDir()}, gateway, mod)
	t.Cleanup(func() { mod.cancel(); mod.wg.Wait() })
	return &harness{app: app, claude: c, mod: mod}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func (h *harness) switchOn(t *testing.T, documents bool) {
	t.Helper()
	on := true
	if _, err := h.app.Plugins().Update(context.Background(), aigateway.PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{
		"api_key": "sk-ant-test", "monthly_cap": 100000,
		"small_price_input": 100, "small_price_output": 500,
		"strong_price_input": 200, "strong_price_output": 1000,
		"share_documents": documents,
	}}); err != nil {
		t.Fatal(err)
	}
}

func picture(t *testing.T, side int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, side, side*3/4))
	for x := range side {
		img.Set(x, x*3/8, color.RGBA{200, 120, 40, 255})
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func (h *harness) do(t *testing.T, method, path string, body any, want int) *Batch {
	t.Helper()
	rec := gctest.AdminRequest(t, h.app, method, "/api/admin/x/photo-products"+path, body)
	if rec.Code != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, rec.Code, want, rec.Body.String())
	}
	if rec.Code >= 300 {
		return nil
	}
	var b Batch
	gctest.DecodeData(t, rec, &b)
	return &b
}

// upload makes a batch of n photos, each stored in the media library first,
// as the panel does.
func (h *harness) upload(t *testing.T, n int, sizes ...string) *Batch {
	t.Helper()
	b := h.do(t, "POST", "/batches", map[string]any{"sizes": sizes}, http.StatusCreated)
	for i := range n {
		data := picture(t, 64)
		item, err := h.app.MediaLibrary().Upload(context.Background(), fmt.Sprintf("p%d.jpg", i), "image/jpeg", bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		b = h.do(t, "POST", fmt.Sprintf("/batches/%d/photos", b.ID), map[string]any{
			"media_id": item.ID, "thumb": picture(t, 32), "image": data,
		}, http.StatusOK)
	}
	return b
}

func (h *harness) wait(t *testing.T, id int64) *Batch {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		b := h.do(t, "GET", fmt.Sprintf("/batches/%d", id), nil, http.StatusOK)
		if b.Status == StatusReview || b.Status == StatusFailed {
			return b
		}
		if time.Now().After(deadline) {
			t.Fatalf("batch still %s: %s", b.Status, b.Message)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (h *harness) products(t *testing.T) []*gocommerce.Product {
	t.Helper()
	ps, _, err := h.app.Products().ListProducts(context.Background(), gocommerce.ProductQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

// Five photos: two angles of a gold pair and one of a silver pair are one
// product in two finishes; one is a different item; the answer leaves the
// fifth out, and it becomes a product of its own rather than vanishing.
const grouping = `{"products":[
  {"name":"Floral hoops","option_name":"Finish","variants":[{"value":"Gold","photos":[1,2]},{"value":"Silver","photos":[3]}]},
  {"name":"Beaded bracelet","option_name":"","variants":[{"value":"","photos":[4,4,99]}]}]}`

func TestPhotosBecomeDraftProductsWithVariantsAndPictures(t *testing.T) {
	h := newHarness(t)
	h.switchOn(t, true)
	h.claude.group = grouping
	b := h.upload(t, 5)
	h.do(t, "POST", fmt.Sprintf("/batches/%d/start", b.ID), nil, http.StatusAccepted)
	b = h.wait(t, b.ID)
	if b.Status != StatusReview {
		t.Fatalf("status %s: %s", b.Status, b.Message)
	}
	if h.claude.count("group") != 1 || h.claude.count("describe") != 3 {
		t.Errorf("calls: group %d describe %d, want 1 and one per product", h.claude.count("group"), h.claude.count("describe"))
	}
	if len(b.Items) != 3 || b.StepDone != 3 || b.StepTotal != 3 || b.CostMicroMinor == 0 {
		t.Fatalf("items %d, progress %d/%d, cost %d", len(b.Items), b.StepDone, b.StepTotal, b.CostMicroMinor)
	}
	hoops := b.Items[0].Draft
	if hoops.Title != "Floral hoop earrings" || hoops.OptionName != "Finish" ||
		hoops.Variants[0].Value != "Gold tone" || hoops.Variants[1].Value != "Silver tone" {
		t.Errorf("hoops = %+v", hoops)
	}
	if len(hoops.Variants[0].Photos) != 2 || hoops.Variants[0].Photos[0] != b.Photos[0].ID || hoops.Variants[1].Photos[0] != b.Photos[2].ID {
		t.Errorf("hoop photos = %+v", hoops.Variants)
	}
	// Alt text by the numbers the model saw: one of each variant first.
	if hoops.Alts[fmt.Sprint(b.Photos[0].ID)] != "Gold hoop earrings, front" || hoops.Alts[fmt.Sprint(b.Photos[2].ID)] != "Silver hoop earrings" {
		t.Errorf("alts = %v", hoops.Alts)
	}
	if len(hoops.Highlights) != 3 || len([]rune(hoops.Highlights[2])) > 80 || hoops.Tags[0] != "hoops" {
		t.Errorf("highlights %q tags %q", hoops.Highlights, hoops.Tags)
	}
	// Not an Indian GST store yet, so no tariff code is kept.
	if hoops.HSCode != "" || hoops.SuggestedCategory != "Earrings" || hoops.CategoryID != nil {
		t.Errorf("hs %q suggested %q category %v", hoops.HSCode, hoops.SuggestedCategory, hoops.CategoryID)
	}
	if v := b.Items[1].Draft.Variants; len(v) != 1 || len(v[0].Photos) != 1 || v[0].Photos[0] != b.Photos[3].ID {
		t.Errorf("bracelet variants = %+v", v)
	}
	if v := b.Items[2].Draft.Variants; len(v) != 1 || v[0].Photos[0] != b.Photos[4].ID {
		t.Errorf("the left-out photo = %+v", b.Items[2].Draft)
	}

	var res CreateResult
	rec := gctest.AdminRequest(t, h.app, "POST", fmt.Sprintf("/api/admin/x/photo-products/batches/%d/create", b.ID), nil)
	gctest.DecodeData(t, rec, &res)
	if res.Created != 3 || res.Failed != 0 || res.Batch.Status != StatusDone {
		t.Fatalf("create = %+v (%s)", res, rec.Body.String())
	}
	products := h.products(t)
	if len(products) != 3 {
		t.Fatalf("%d products", len(products))
	}
	var p *gocommerce.Product
	for _, x := range products {
		if x.Title == "Floral hoop earrings" && len(x.Variants) == 2 {
			p = x
		}
		if x.Status != gocommerce.ProductDraft {
			t.Errorf("%s is %s: nothing drafted goes live by itself", x.Title, x.Status)
		}
		for _, v := range x.Variants {
			if v.Price.AmountMinor != 0 || v.StockOnHand != 0 {
				t.Errorf("%s: price %d stock %d", v.SKU, v.Price.AmountMinor, v.StockOnHand)
			}
		}
	}
	if p == nil {
		t.Fatal("no two-finish product")
	}
	if p.Options[0].Name != "Finish" || p.Description != "<p>Hoop earrings with a floral pattern.</p><p>A shiny finish.</p>" {
		t.Errorf("options %+v description %q", p.Options, p.Description)
	}
	if mark, _ := p.Metadata[MetaKey].(map[string]any); mark["suggested_category"] != "Earrings" || mark["item"] != fmt.Sprintf("%d/%d", b.ID, b.Items[0].ID) {
		t.Errorf("metadata = %v", p.Metadata)
	}
	if p.MediaCount != 3 || len(p.Variants[0].Images) != 2 || len(p.Variants[1].Images) != 1 {
		t.Errorf("media %d, gold %d, silver %d", p.MediaCount, len(p.Variants[0].Images), len(p.Variants[1].Images))
	}
	if p.Variants[0].Images[0].Alt != "Gold hoop earrings, front" {
		t.Errorf("alt = %q", p.Variants[0].Images[0].Alt)
	}

	// The copies the model saw are gone once every product exists.
	var left int
	if err := h.app.DB().QueryRowContext(context.Background(),
		`SELECT count(*) FROM photo_products_photos WHERE image IS NOT NULL`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d copies kept after the batch finished", left)
	}
}

// Sizes the shop listed become a second option on every product; the model is
// never asked what sizes a photo shows.
func TestSizesTheShopListsBecomeASecondOption(t *testing.T) {
	h := newHarness(t)
	h.switchOn(t, true)
	h.claude.group = `{"products":[{"name":"Hoops","option_name":"Finish","variants":[{"value":"Gold","photos":[1]},{"value":"Silver","photos":[2]}]}]}`
	b := h.upload(t, 2, "S", "M", "s")
	h.do(t, "POST", fmt.Sprintf("/batches/%d/start", b.ID), nil, http.StatusAccepted)
	h.wait(t, b.ID)
	h.do(t, "POST", fmt.Sprintf("/batches/%d/create", b.ID), nil, http.StatusOK)
	p := h.products(t)[0]
	if len(p.Options) != 2 || p.Options[1].Name != "Size" || len(p.Variants) != 4 {
		t.Fatalf("options %+v, %d variants", p.Options, len(p.Variants))
	}
	if !strings.Contains(p.Variants[0].SKU, "GOLD") {
		t.Errorf("sku %q is not derived from the options", p.Variants[0].SKU)
	}
}

// Create again makes nothing new: not from a finished batch, and not after a
// run that made a product and stopped before recording it.
func TestCreatingAgainNeverDuplicatesAProduct(t *testing.T) {
	h := newHarness(t)
	h.switchOn(t, true)
	h.claude.group = `{"products":[{"name":"Hoops","option_name":"","variants":[{"value":"","photos":[1]}]}]}`
	b := h.upload(t, 1)
	h.do(t, "POST", fmt.Sprintf("/batches/%d/start", b.ID), nil, http.StatusAccepted)
	h.wait(t, b.ID)
	h.do(t, "POST", fmt.Sprintf("/batches/%d/create", b.ID), nil, http.StatusOK)
	h.do(t, "POST", fmt.Sprintf("/batches/%d/create", b.ID), nil, http.StatusConflict)
	// Nor can the proposals of a finished batch be edited, or photos added.
	h.do(t, "PUT", fmt.Sprintf("/batches/%d/items", b.ID), map[string]any{"items": []any{}}, http.StatusConflict)
	h.do(t, "POST", fmt.Sprintf("/batches/%d/photos", b.ID), map[string]any{
		"media_id": b.Photos[0].MediaID, "thumb": picture(t, 32), "image": picture(t, 64),
	}, http.StatusConflict)

	ctx := context.Background()
	if _, err := h.app.DB().ExecContext(ctx, `UPDATE photo_products_items SET product_id = NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.DB().ExecContext(ctx, `UPDATE photo_products_batches SET status = 'review'`); err != nil {
		t.Fatal(err)
	}
	// The run that made the product stopped before its pictures, too.
	p := h.products(t)[0]
	if err := h.app.MediaLibrary().SetProductMedia(ctx, p.ID, nil); err != nil {
		t.Fatal(err)
	}
	h.do(t, "POST", fmt.Sprintf("/batches/%d/create", b.ID), nil, http.StatusOK)
	if ps := h.products(t); len(ps) != 1 || ps[0].MediaCount != 1 {
		t.Errorf("after creating again: %d products, %d pictures", len(ps), ps[0].MediaCount)
	}
	// And running the batch again repeats no model call.
	h.do(t, "POST", fmt.Sprintf("/batches/%d/start", b.ID), nil, http.StatusConflict)
	if h.claude.count("group") != 1 || h.claude.count("describe") != 1 {
		t.Errorf("calls %v", h.claude.calls)
	}
}

// One product's failure leaves the rest; starting again redoes only it.
func TestAFailedListingDoesNotSinkTheBatch(t *testing.T) {
	h := newHarness(t)
	h.switchOn(t, true)
	h.claude.group = grouping
	h.claude.describe = func(n int) (int, string) {
		if n == 2 {
			return http.StatusBadRequest, "image could not be processed"
		}
		return http.StatusOK, listing
	}
	b := h.upload(t, 5)
	h.do(t, "POST", fmt.Sprintf("/batches/%d/start", b.ID), nil, http.StatusAccepted)
	b = h.wait(t, b.ID)
	var failed int
	for _, it := range b.Items {
		if it.Error != "" {
			failed++
			if !strings.Contains(it.Error, "image could not be processed") || it.Described {
				t.Errorf("failed item = %+v", it)
			}
		}
	}
	if b.Status != StatusReview || failed != 1 {
		t.Fatalf("status %s, %d failed", b.Status, failed)
	}

	h.do(t, "POST", fmt.Sprintf("/batches/%d/start", b.ID), nil, http.StatusAccepted)
	b = h.wait(t, b.ID)
	if h.claude.count("group") != 1 || h.claude.count("describe") != 4 {
		t.Errorf("calls %v, want the one failed listing tried again and nothing else", h.claude.calls)
	}
	for _, it := range b.Items {
		if !it.Described || it.Error != "" {
			t.Errorf("after the retry: %+v", it)
		}
	}
}

// With no key, or with pictures kept in, the screen is told what to set and
// nothing is sent.
func TestABatchSaysWhatToSetBeforeItCanRun(t *testing.T) {
	h := newHarness(t)
	var status struct {
		Ready  bool   `json:"ready"`
		Reason string `json:"reason"`
	}
	gctest.DecodeData(t, gctest.AdminRequest(t, h.app, "GET", "/api/admin/x/photo-products/status", nil), &status)
	if status.Ready || !strings.Contains(status.Reason, "Settings › Plugins") {
		t.Errorf("status = %+v", status)
	}
	b := h.upload(t, 1)
	h.do(t, "POST", fmt.Sprintf("/batches/%d/start", b.ID), nil, http.StatusConflict)

	h.switchOn(t, false)
	gctest.DecodeData(t, gctest.AdminRequest(t, h.app, "GET", "/api/admin/x/photo-products/status", nil), &status)
	if status.Ready || !strings.Contains(status.Reason, "Share documents") {
		t.Errorf("status with pictures kept in = %+v", status)
	}
	h.switchOn(t, true)
	gctest.DecodeData(t, gctest.AdminRequest(t, h.app, "GET", "/api/admin/x/photo-products/status", nil), &status)
	if !status.Ready {
		t.Errorf("status when configured = %+v", status)
	}
	if h.claude.count("group")+h.claude.count("describe") != 0 {
		t.Errorf("a call was made: %v", h.claude.calls)
	}
}

// On an install with a key of its own, a shop that has set nothing up drafts
// products from photos with nothing but AI credit: the gateway is on, pictures
// may leave, and the calls are paid from the credit at the platform's prices.
func TestPhotosNeedOnlyCreditOnThePlatformKey(t *testing.T) {
	h := newHarnessOn(t, aigateway.Config{PlatformAnthropicKey: "sk-ant-platform", PlatformUSDRates: "INR=88.50"})
	ctx := context.Background()
	h.claude.group = grouping
	b := h.upload(t, 5)
	h.do(t, "POST", fmt.Sprintf("/batches/%d/start", b.ID), nil, http.StatusAccepted)
	if b = h.wait(t, b.ID); b.Status != StatusReview || len(b.Items) != 3 {
		t.Fatalf("status %s with %d items: %s", b.Status, len(b.Items), b.Message)
	}
	credit, err := h.mod.cfg.Gateway.Credit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if credit.ConsumedMicroMinor == 0 || credit.ConsumedMicroMinor != b.CostMicroMinor || credit.ReservedMicroMinor != 0 {
		t.Errorf("credit consumed %d, held %d; the batch cost %d", credit.ConsumedMicroMinor, credit.ReservedMicroMinor, b.CostMicroMinor)
	}
}

// The shop's edits are what gets created: a photo moved, one dropped, a
// photo from elsewhere refused.
func TestTheShopsEditsAreWhatIsCreated(t *testing.T) {
	h := newHarness(t)
	h.switchOn(t, true)
	h.claude.group = `{"products":[{"name":"A","option_name":"","variants":[{"value":"","photos":[1,2]}]},
	                                {"name":"B","option_name":"","variants":[{"value":"","photos":[3]}]}]}`
	b := h.upload(t, 3)
	h.do(t, "POST", fmt.Sprintf("/batches/%d/start", b.ID), nil, http.StatusAccepted)
	b = h.wait(t, b.ID)

	keep := b.Items[0]
	keep.Draft.Title = "Renamed"
	keep.Draft.Variants[0].Photos = []int64{b.Photos[0].ID}
	split := Draft{Title: "Split off", Variants: []DraftVariant{{Photos: []int64{b.Photos[1].ID}}}}
	path := fmt.Sprintf("/batches/%d/items", b.ID)
	h.do(t, "PUT", path, map[string]any{"items": []any{
		map[string]any{"id": keep.ID, "draft": keep.Draft},
		map[string]any{"draft": map[string]any{"title": "Stray", "variants": []any{map[string]any{"value": "", "photos": []int64{999999}}}}},
	}}, http.StatusBadRequest)
	b = h.do(t, "PUT", path, map[string]any{"items": []any{
		map[string]any{"id": keep.ID, "draft": keep.Draft},
		map[string]any{"draft": split},
	}}, http.StatusOK)
	if len(b.Items) != 2 || b.Items[0].Draft.Title != "Renamed" {
		t.Fatalf("items = %+v", b.Items)
	}
	// What the shop wrote is the shop's: a retitled item counts as written,
	// and an over-long highlight is cut to the catalogue's limit, not refused.
	if !b.Items[0].Described {
		t.Error("an edited item would be written over by running the batch again")
	}
	keep = b.Items[0]
	keep.Draft.Highlights = []string{strings.Repeat("long ", 30)}
	b = h.do(t, "PUT", path, map[string]any{"items": []any{
		map[string]any{"id": keep.ID, "draft": keep.Draft},
		map[string]any{"id": b.Items[1].ID, "draft": b.Items[1].Draft},
	}}, http.StatusOK)
	if hl := b.Items[0].Draft.Highlights; len(hl) != 1 || len([]rune(hl[0])) > 80 {
		t.Errorf("highlights = %q", hl)
	}
	h.do(t, "POST", fmt.Sprintf("/batches/%d/create", b.ID), nil, http.StatusOK)
	titles := map[string]bool{}
	for _, p := range h.products(t) {
		titles[p.Title] = true
	}
	if len(titles) != 2 || !titles["Renamed"] || !titles["Split off"] {
		t.Errorf("created %v", titles)
	}
}

func TestRoutesNameARightAndAreInTheContract(t *testing.T) {
	h := newHarness(t)
	gctest.AssertAdminRoutesDeclareRights(t, h.app, "photo-products")
	gctest.AssertSpecCoversModuleRoutes(t, h.app, "photo-products")
}

// The free trial is ₹30 (D151, 2026-10-30), and it is enough to try photos:
// a three-photo batch on a fresh Indian store, nothing topped up, sorts and
// describes every product. Each call holds what its own request could cost,
// not the most any batch could, and never costs more than it held.
func TestAThreePhotoBatchRunsOnTheFreeTrial(t *testing.T) {
	for _, n := range []int{3, 6, 20} {
		h := newHarnessOn(t, aigateway.Config{PlatformAnthropicKey: "sk-ant-platform", PlatformUSDRates: "INR=88.50"})
		ctx := context.Background()
		var mu sync.Mutex
		peak := map[string]int64{}
		h.claude.onCall = func(task string, body []byte) {
			var held int64
			if err := h.app.DB().QueryRowContext(ctx, `SELECT reserved_micro FROM ai_credits`).Scan(&held); err != nil {
				t.Error(err)
			}
			mu.Lock()
			peak[task] = max(peak[task], held)
			mu.Unlock()
		}
		// Each photo a product of its own: the most describing calls a batch
		// of n can make.
		var products []string
		for i := 1; i <= n; i++ {
			products = append(products, fmt.Sprintf(`{"name":"Item %d","option_name":"","variants":[{"value":"","photos":[%d]}]}`, i, i))
		}
		h.claude.group = `{"products":[` + strings.Join(products, ",") + `]}`
		b := h.upload(t, n)
		h.do(t, "POST", fmt.Sprintf("/batches/%d/start", b.ID), nil, http.StatusAccepted)
		b = h.wait(t, b.ID)
		credit, err := h.mod.cfg.Gateway.Credit(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%d photos: grouping held %d, describing held at most %d (up to %d at once), consumed %d micro-paise",
			n, peak["group"], peak["describe"], describers, credit.ConsumedMicroMinor)
		if n > 3 {
			continue
		}
		if b.Status != StatusReview || len(b.Items) != n {
			t.Fatalf("status %s with %d items: %s", b.Status, len(b.Items), b.Message)
		}
		for _, it := range b.Items {
			if !it.Described {
				t.Errorf("item %q not described: %s", it.Draft.Title, it.Error)
			}
		}
		if credit.FreeAllowanceMicroMinor != 3000*1_000_000 || credit.GrantedMicroMinor != 0 {
			t.Errorf("credit %+v, want the ₹30 trial and nothing topped up", credit)
		}
		if credit.ConsumedMicroMinor != b.CostMicroMinor || credit.ReservedMicroMinor != 0 {
			t.Errorf("consumed %d, held %d; the batch cost %d", credit.ConsumedMicroMinor, credit.ReservedMicroMinor, b.CostMicroMinor)
		}
	}
}
