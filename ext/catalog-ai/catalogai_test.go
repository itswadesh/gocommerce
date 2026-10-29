package catalogai

import (
	"bytes"
	"context"
	"encoding/base64"
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

const (
	small  = "claude-haiku-4-5"
	strong = "claude-sonnet-5-5"
)

// claude answers in Anthropic's place, each model with what the test set,
// and keeps every request it was sent.
type claude struct {
	mu       sync.Mutex
	answers  map[string]string
	status   int
	requests []map[string]any
}

func (c *claude) serve(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	c.mu.Lock()
	c.requests = append(c.requests, body)
	model, _ := body["model"].(string)
	answer, status := c.answers[model], c.status
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if status != 0 {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"the stand-in is down"}}`)
		return
	}
	text, _ := json.Marshal(answer)
	_, _ = fmt.Fprintf(w, `{"id":"msg_1","type":"message","role":"assistant","model":%q,
	  "content":[{"type":"text","text":%s}],"stop_reason":"end_turn",
	  "usage":{"input_tokens":3000,"output_tokens":400,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`, model, text)
}

func (c *claude) calls() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]any(nil), c.requests...)
}

type harness struct {
	app    *gocommerce.App
	claude *claude
	mod    *Module
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newHarness(t *testing.T) *harness {
	t.Helper()
	c := &claude{answers: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(srv.Close)
	to, _ := url.Parse(srv.URL)
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		out := r.Clone(r.Context())
		out.URL.Scheme, out.URL.Host, out.Host = to.Scheme, to.Host, to.Host
		return http.DefaultTransport.RoundTrip(out)
	})}
	gw := aigateway.New(aigateway.Config{Client: client, CallTimeout: 5 * time.Second, PollInterval: time.Hour})
	mod := New(Config{Gateway: gw})
	app := gctest.NewWithConfig(t, gocommerce.Config{Currency: "INR", MediaDir: t.TempDir()}, gw, mod)
	return &harness{app: app, claude: c, mod: mod}
}

// switchOn sets the gateway up for ctx's store, changed by extra.
func (h *harness) switchOn(t *testing.T, ctx context.Context, extra map[string]any) {
	t.Helper()
	settings := map[string]any{
		"api_key": "sk-ant-test", "monthly_cap": 100000,
		"small_price_input": 100, "small_price_output": 500,
		"strong_price_input": 300, "strong_price_output": 1500,
		"share_catalogue": true, "share_documents": true,
	}
	for k, v := range extra {
		settings[k] = v
	}
	on := true
	if _, err := h.app.Plugins().Update(ctx, aigateway.PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: settings}); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) category(t *testing.T, ctx context.Context, title string) *gocommerce.Category {
	t.Helper()
	c, err := h.app.Categories().Create(ctx, gocommerce.CategoryInput{Title: title})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// mug is a product with something to improve and one picture 2000 px wide.
func (h *harness) mug(t *testing.T, ctx context.Context, category *gocommerce.Category) (*gocommerce.Product, int64) {
	t.Helper()
	price, stock := int64(49900), 5
	in := gocommerce.ProductInput{
		Title: "blue mug", Description: "<p>A mug.</p>", Status: "active", ProductType: "Mug",
		Tags: []string{"mug"}, SKU: "MUG-" + fmt.Sprint(time.Now().UnixNano()), PriceMinor: &price, Stock: &stock,
	}
	if category != nil {
		in.CategoryID = &category.ID
	}
	p, err := h.app.Products().CreateProduct(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 2000, 1500))
	for x := range 2000 {
		img.Set(x, x*3/4, color.RGBA{30, 60, 200, 255})
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	item, err := h.app.MediaLibrary().Upload(ctx, "mug.jpg", "image/jpeg", &b, int64(b.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.app.MediaLibrary().SetProductMedia(ctx, p.ID, []int64{item.ID}); err != nil {
		t.Fatal(err)
	}
	return p, item.ID
}

// improve posts for product id as store (empty for the default store) and
// decodes the envelope.
func (h *harness) improve(t *testing.T, id int64, store string) (int, Proposal, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/admin/x/catalog-ai/products/%d/improve", id), nil)
	req.Header.Set("Authorization", "Bearer "+gctest.AdminToken)
	if store != "" {
		req.Header.Set("X-Store", store)
	}
	rec := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(rec, req)
	var env struct {
		Data  Proposal `json:"data"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("%d %s: %v", rec.Code, rec.Body.String(), err)
	}
	return rec.Code, env.Data, env.Error.Code
}

// answer is a whole answer with every field kept, changed by fields.
func answer(fields map[string]any) string {
	a := map[string]any{
		"title": nil, "description": nil, "highlights": nil, "tags": nil, "product_type": nil,
		"category_id": nil, "suggested_category": nil, "seo_title": nil, "seo_description": nil,
		"alt_texts": []any{}, "reasons": []any{}, "verify": []any{}, "confidence": 90,
	}
	for k, v := range fields {
		a[k] = v
	}
	b, _ := json.Marshal(a)
	return string(b)
}

func reasons(fields ...string) []any {
	out := []any{}
	for _, f := range fields {
		out = append(out, map[string]any{"field": f, "reason": "Clearer for a shopper."})
	}
	return out
}

func TestAProposalIsEachChangeBesideTheCurrentValue(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.switchOn(t, ctx, nil)
	kitchen := h.category(t, ctx, "Kitchen")
	drinkware := h.category(t, ctx, "Drinkware")
	p, mediaID := h.mug(t, ctx, kitchen)
	before, err := h.app.Products().GetProduct(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.claude.answers[small] = answer(map[string]any{
		"title":           "Blue ceramic mug",
		"description":     "A blue mug for <tea> & coffee.\n\nIt has a wide handle.",
		"highlights":      []string{"Wide handle", "Glossy blue glaze"},
		"tags":            []string{"Mug", "blue"},
		"product_type":    "Mug",
		"category_id":     drinkware.ID,
		"seo_title":       "Blue ceramic mug with a wide handle",
		"seo_description": "A blue ceramic mug with a glossy glaze and a wide handle, sized for a morning cup of tea or coffee.",
		"alt_texts":       []any{map[string]any{"picture": 1, "alt": "Blue mug, side view"}, map[string]any{"picture": 4, "alt": "No such picture"}},
		"reasons":         reasons("title", "description", "highlights", "tags", "category", "seo_title", "seo_description", "alt_text"),
		"verify":          []string{"The mug is ceramic"},
	})

	code, prop, errCode := h.improve(t, p.ID, "")
	if code != http.StatusOK {
		t.Fatalf("improve = %d %s", code, errCode)
	}
	got := map[string]Change{}
	var order []string
	for _, c := range prop.Changes {
		got[c.Field], order = c, append(order, c.Field)
	}
	want := "title description highlights tags category seo_title seo_description alt"
	if strings.Join(order, " ") != want {
		t.Errorf("changes = %q, want %q: a kept value or a repeated one must not be offered", order, want)
	}
	if c := got["title"]; c.Current != "blue mug" || c.Proposed != "Blue ceramic mug" || c.Reason != "Clearer for a shopper." {
		t.Errorf("title = %+v", c)
	}
	if c := got["description"]; c.Current != "<p>A mug.</p>" ||
		c.Proposed != "<p>A blue mug for &lt;tea&gt; &amp; coffee.</p><p>It has a wide handle.</p>" {
		t.Errorf("description = %+v: the model's words are escaped into paragraphs", c)
	}
	if c := got["tags"]; fmt.Sprint(c.Current) != "[mug]" || fmt.Sprint(c.Proposed) != "[Mug blue]" {
		t.Errorf("tags = %+v", c)
	}
	if c := got["category"]; fmt.Sprint(c.Current) != fmt.Sprintf("map[id:%d name:Kitchen]", kitchen.ID) ||
		fmt.Sprint(c.Proposed) != fmt.Sprintf("map[id:%d name:Drinkware]", drinkware.ID) {
		t.Errorf("category = %+v", c)
	}
	if c := got["alt"]; c.MediaID != mediaID || c.Current != "" || c.Proposed != "Blue mug, side view" {
		t.Errorf("alt = %+v", c)
	}
	if len(prop.Verify) != 1 || prop.Verify[0] != "The mug is ceramic" || prop.Pictures != 1 || prop.PicturesNote != "" {
		t.Errorf("verify %q, pictures %d %q", prop.Verify, prop.Pictures, prop.PicturesNote)
	}
	if prop.ExecutionID == 0 || prop.Model != small || prop.CostMicroMinor == 0 || prop.Currency != "INR" {
		t.Errorf("run = %d %q %d %q", prop.ExecutionID, prop.Model, prop.CostMicroMinor, prop.Currency)
	}

	// What the model was shown: the product's words, the store's categories,
	// and the picture scaled to 1024 px.
	calls := h.claude.calls()
	if len(calls) != 1 {
		t.Fatalf("%d calls, want 1", len(calls))
	}
	sent, _ := json.Marshal(calls[0])
	for _, s := range []string{"blue mug", "Drinkware", "Picture 1"} {
		if !bytes.Contains(sent, []byte(s)) {
			t.Errorf("the request does not carry %q", s)
		}
	}
	if bytes.Contains(sent, []byte("49900")) || bytes.Contains(sent, []byte("499.00")) {
		t.Error("the request carries the price, which the model is never asked about")
	}
	pics := images(calls[0])
	if len(pics) != 1 {
		t.Fatalf("%d images sent, want 1", len(pics))
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(pics[0]))
	if err != nil || format != "jpeg" || cfg.Width != 1024 {
		t.Errorf("picture sent = %s %dx%d %v, want a 1024 px JPEG", format, cfg.Width, cfg.Height, err)
	}

	// Nothing is saved by asking.
	after, err := h.app.Products().GetProduct(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Title != "blue mug" || after.Category.ID != kitchen.ID || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("the product moved: %q in %d", after.Title, after.Category.ID)
	}
}

// images is the pictures an Anthropic request carries, decoded.
func images(req map[string]any) [][]byte {
	var out [][]byte
	messages, _ := req["messages"].([]any)
	for _, m := range messages {
		content, _ := m.(map[string]any)["content"].([]any)
		for _, block := range content {
			b, _ := block.(map[string]any)
			if b["type"] != "image" {
				continue
			}
			src, _ := b["source"].(map[string]any)
			data, _ := base64.StdEncoding.DecodeString(fmt.Sprint(src["data"]))
			out = append(out, data)
		}
	}
	return out
}

func TestAnAnswerOutsideTheRulesGoesUpTheLadder(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.switchOn(t, ctx, nil)
	drinkware := h.category(t, ctx, "Drinkware")
	p, _ := h.mug(t, ctx, nil)

	// A category the store does not have: refused by the small model's check,
	// and the strong model's choice is what is offered.
	h.claude.answers[small] = answer(map[string]any{"category_id": drinkware.ID + 1000, "reasons": reasons("category")})
	h.claude.answers[strong] = answer(map[string]any{"category_id": drinkware.ID, "reasons": reasons("category")})
	code, prop, _ := h.improve(t, p.ID, "")
	if code != http.StatusOK || len(prop.Changes) != 1 || prop.Model != strong {
		t.Fatalf("improve = %d by %q: %+v", code, prop.Model, prop.Changes)
	}
	if fmt.Sprint(prop.Changes[0].Proposed) != fmt.Sprintf("map[id:%d name:Drinkware]", drinkware.ID) {
		t.Errorf("category = %+v", prop.Changes[0])
	}
	if n := len(h.claude.calls()); n != 2 {
		t.Errorf("%d calls, want the small model then the strong one", n)
	}

	// Neither rung answers within the rules: no proposal, and why.
	h.claude.answers[small] = answer(map[string]any{"seo_title": strings.Repeat("Blue mug ", 10), "reasons": reasons("seo_title")})
	h.claude.answers[strong] = answer(map[string]any{"title": "Bestseller blue mug", "reasons": reasons("title")})
	if code, _, errCode := h.improve(t, p.ID, ""); code != http.StatusBadGateway || errCode != codeNoAnswer {
		t.Errorf("improve = %d %s, want 502 %s", code, errCode, codeNoAnswer)
	}
}

func TestTheChecksHoldEachLimit(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	cat := h.category(t, ctx, "Drinkware")
	long := func(n int) string { return strings.Repeat("a", n) }
	cases := []struct {
		name   string
		fields map[string]any
		ok     bool
	}{
		{"everything kept", nil, true},
		{"a good title", map[string]any{"title": "Blue mug", "reasons": reasons("title")}, true},
		{"a reason of 301", map[string]any{"title": "Blue mug", "reasons": []any{map[string]any{"field": "title", "reason": long(301)}}}, false},
		{"an empty title", map[string]any{"title": " ", "reasons": reasons("title")}, false},
		{"a title of 121", map[string]any{"title": long(121), "reasons": reasons("title")}, false},
		{"an SEO title of 60", map[string]any{"seo_title": long(60), "reasons": reasons("seo_title")}, true},
		{"an SEO title of 61", map[string]any{"seo_title": long(61), "reasons": reasons("seo_title")}, false},
		{"an SEO description of 49", map[string]any{"seo_description": long(49), "reasons": reasons("seo_description")}, false},
		{"an SEO description of 155", map[string]any{"seo_description": long(155), "reasons": reasons("seo_description")}, true},
		{"an SEO description of 156", map[string]any{"seo_description": long(156), "reasons": reasons("seo_description")}, false},
		{"a highlight of 80", map[string]any{"highlights": []string{long(80)}, "reasons": reasons("highlights")}, true},
		{"a highlight of 81", map[string]any{"highlights": []string{long(81)}, "reasons": reasons("highlights")}, false},
		{"nine highlights", map[string]any{"highlights": strings.Split("a b c d e f g h i", " "), "reasons": reasons("highlights")}, false},
		{"a highlight over two lines", map[string]any{"highlights": []string{"one\ntwo"}, "reasons": reasons("highlights")}, false},
		{"eleven tags", map[string]any{"tags": strings.Split("a b c d e f g h i j k", " "), "reasons": reasons("tags")}, false},
		{"an alt text of 126", map[string]any{"alt_texts": []any{map[string]any{"picture": 1, "alt": long(126)}}, "reasons": reasons("alt_text")}, false},
		{"a store's category", map[string]any{"category_id": cat.ID, "reasons": reasons("category")}, true},
		{"a made-up category", map[string]any{"category_id": cat.ID + 1000, "reasons": reasons("category")}, false},
		{"a category and a suggestion", map[string]any{"category_id": cat.ID, "suggested_category": "Mugs", "reasons": reasons("category")}, false},
		{"a suggestion alone", map[string]any{"suggested_category": "Mugs"}, true},
		{"a price", map[string]any{"description": "Only ₹499 today.", "reasons": reasons("description")}, false},
		{"a discount", map[string]any{"highlights": []string{"20% off this week"}, "reasons": reasons("highlights")}, false},
		{"a rating", map[string]any{"seo_description": strings.Repeat("Loved by all, ", 3) + "rated 4.8 stars by our customers.", "reasons": reasons("seo_description")}, false},
		{"bestseller", map[string]any{"tags": []string{"best-seller"}, "reasons": reasons("tags")}, false},
		{"stock", map[string]any{"title": "Blue mug, in stock", "reasons": reasons("title")}, false},
		{"a material is not a claim the check refuses", map[string]any{"title": "100% cotton tote", "reasons": reasons("title")}, true},
		{"confidence over 100", map[string]any{"confidence": 101}, false},
		{"thirteen things to verify", map[string]any{"verify": strings.Split("a b c d e f g h i j k l m", " ")}, false},
	}
	for _, c := range cases {
		_, err := h.mod.checkImprove(ctx, json.RawMessage(answer(c.fields)))
		if (err == nil) != c.ok {
			t.Errorf("%s: check = %v, want ok %v", c.name, err, c.ok)
		}
	}
	if conf, err := h.mod.checkImprove(ctx, json.RawMessage(answer(map[string]any{"confidence": 40}))); err != nil || conf != 40 {
		t.Errorf("confidence = %d %v: the model's own figure is what the ladder climbs on", conf, err)
	}
}

func TestPicturesStayInWhenTheStoreKeepsDocumentsIn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.switchOn(t, ctx, map[string]any{"share_documents": false})
	p, _ := h.mug(t, ctx, nil)
	h.claude.answers[small] = answer(map[string]any{
		"title":     "Blue mug with a handle",
		"alt_texts": []any{map[string]any{"picture": 1, "alt": "A guess at a picture it never saw"}},
		"reasons":   reasons("title", "alt_text"),
	})
	code, prop, errCode := h.improve(t, p.ID, "")
	if code != http.StatusOK {
		t.Fatalf("improve = %d %s", code, errCode)
	}
	calls := h.claude.calls()
	if len(calls) != 1 || len(images(calls[0])) != 0 {
		t.Fatalf("%d calls; a store that keeps documents in sent pictures", len(calls))
	}
	if prop.Pictures != 0 || !strings.Contains(prop.PicturesNote, "Share documents") {
		t.Errorf("pictures %d, note %q", prop.Pictures, prop.PicturesNote)
	}
	if len(prop.Changes) != 1 || prop.Changes[0].Field != "title" {
		t.Errorf("changes = %+v: an alt text for an unseen picture is not offered", prop.Changes)
	}
	status := gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/catalog-ai/status", nil)
	if !strings.Contains(status.Body.String(), `"pictures":false`) || !strings.Contains(status.Body.String(), `"ready":true`) {
		t.Errorf("status = %s", status.Body.String())
	}
}

func TestImprovingNeedsCatalogWrite(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.switchOn(t, ctx, nil)
	p, _ := h.mug(t, ctx, nil)
	h.claude.answers[small] = answer(nil)
	staff := gctest.OperatorToken(t, h.app, "staff@example.com", gocommerce.RoleStaff)
	manager := gctest.OperatorToken(t, h.app, "manager@example.com", gocommerce.RoleManager)
	path := fmt.Sprintf("/api/admin/x/catalog-ai/products/%d/improve", p.ID)
	if rec := gctest.SessionRequest(t, h.app, staff, http.MethodPost, path, nil); rec.Code != http.StatusForbidden {
		t.Errorf("staff improving = %d, want 403", rec.Code)
	}
	if rec := gctest.SessionRequest(t, h.app, staff, http.MethodGet, "/api/admin/x/catalog-ai/status", nil); rec.Code != http.StatusForbidden {
		t.Errorf("staff asking the status = %d, want 403", rec.Code)
	}
	if n := len(h.claude.calls()); n != 0 {
		t.Errorf("a refused request made %d calls", n)
	}
	if rec := gctest.SessionRequest(t, h.app, manager, http.MethodPost, path, nil); rec.Code != http.StatusOK {
		t.Errorf("a manager improving = %d %s", rec.Code, rec.Body.String())
	}
}

func TestAnotherStoresProductAndCategoriesAreNotReached(t *testing.T) {
	h := newHarness(t)
	a := context.Background()
	second, err := h.app.Stores().Create(a, "Second", "second")
	if err != nil {
		t.Fatal(err)
	}
	b := gocommerce.WithStore(a, second.ID)
	h.switchOn(t, a, nil)
	h.switchOn(t, b, nil)
	p, _ := h.mug(t, a, h.category(t, a, "First's kitchen"))
	theirs := h.category(t, b, "Second's drinkware")
	h.claude.answers[small] = answer(nil)

	if code, _, _ := h.improve(t, p.ID, second.ID); code != http.StatusNotFound {
		t.Errorf("store B improving store A's product = %d, want 404", code)
	}
	if n := len(h.claude.calls()); n != 0 {
		t.Errorf("%d calls for a product B does not have", n)
	}
	if _, err := h.mod.checkImprove(a, json.RawMessage(answer(map[string]any{"category_id": theirs.ID, "reasons": reasons("category")}))); err == nil {
		t.Error("store B's category was accepted for store A's product")
	}
	// B's own product reads B's categories only.
	mine, _ := h.mug(t, b, nil)
	if code, _, errCode := h.improve(t, mine.ID, second.ID); code != http.StatusOK {
		t.Fatalf("store B improving its own = %d %s", code, errCode)
	}
	sent, _ := json.Marshal(h.claude.calls()[0])
	if !bytes.Contains(sent, []byte("Second's drinkware")) || bytes.Contains(sent, []byte("First's kitchen")) {
		t.Errorf("store B's request carries the wrong store's categories: %s", sent)
	}
}

func TestEachRefusalHasItsOwnCode(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	p, _ := h.mug(t, ctx, nil)
	h.claude.answers[small] = answer(nil)

	// Switched off: the status says why, and the button leads to settings.
	status := gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/catalog-ai/status", nil)
	if !strings.Contains(status.Body.String(), `"ready":false`) || !strings.Contains(status.Body.String(), "switched off") {
		t.Errorf("status = %s", status.Body.String())
	}
	if code, _, errCode := h.improve(t, p.ID, ""); code != http.StatusConflict || errCode != codeNotReady {
		t.Errorf("switched off = %d %s", code, errCode)
	}

	// A cap that cannot cover one call.
	h.switchOn(t, ctx, map[string]any{"monthly_cap": 1})
	if code, _, errCode := h.improve(t, p.ID, ""); code != http.StatusConflict || errCode != codeBudget {
		t.Errorf("over the cap = %d %s", code, errCode)
	}
	if n := len(h.claude.calls()); n != 0 {
		t.Errorf("%d calls past the cap", n)
	}

	// The catalogue kept in: nothing can be sent.
	h.switchOn(t, ctx, map[string]any{"share_catalogue": false})
	if code, _, errCode := h.improve(t, p.ID, ""); code != http.StatusConflict || errCode != codePolicy {
		t.Errorf("catalogue kept in = %d %s", code, errCode)
	}

	// The provider down: worth another try.
	h.switchOn(t, ctx, nil)
	h.claude.status = http.StatusInternalServerError
	if code, _, errCode := h.improve(t, p.ID, ""); code != http.StatusBadGateway || errCode != codeProvider {
		t.Errorf("provider down = %d %s", code, errCode)
	}
	h.claude.status = 0

	// The free trial spent and nothing topped up: refused before any call.
	calls := len(h.claude.calls())
	if _, err := h.mod.cfg.Gateway.SetFreeAllowance(ctx, 0, ""); err != nil {
		t.Fatal(err)
	}
	if code, _, errCode := h.improve(t, p.ID, ""); code != http.StatusConflict || errCode != codeCredit {
		t.Errorf("out of credit = %d %s", code, errCode)
	}
	if n := len(h.claude.calls()) - calls; n != 0 {
		t.Errorf("%d calls with no AI credit left", n)
	}
	if code, _, _ := h.improve(t, 999999, ""); code != http.StatusNotFound {
		t.Errorf("no such product = %d", code)
	}
}

func TestRoutesNameARightAndAreInTheContract(t *testing.T) {
	h := newHarness(t)
	gctest.AssertAdminRoutesDeclareRights(t, h.app, "catalog-ai")
	gctest.AssertSpecCoversModuleRoutes(t, h.app, "catalog-ai")
}
