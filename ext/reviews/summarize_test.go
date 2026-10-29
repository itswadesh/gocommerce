package reviews

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	aigateway "github.com/itswadesh/gocommerce/ext/ai-gateway"
	"github.com/itswadesh/gocommerce/gctest"
)

const (
	smallModel  = "claude-haiku-4-5"
	strongModel = "claude-sonnet-5-5"
)

// claude answers in Anthropic's place. The gateway's hosts are constants, so
// it is handed a client that sends them here; each model gets the answer the
// test set for it, and every request body is kept.
type claude struct {
	mu      sync.Mutex
	answers map[string]string
	status  int
	bodies  []string
}

func (c *claude) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &req)
	c.mu.Lock()
	c.bodies = append(c.bodies, string(body))
	answer, status := c.answers[req.Model], c.status
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if status != 0 {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"overloaded"}}`)
		return
	}
	text, _ := json.Marshal(answer)
	_, _ = fmt.Fprintf(w, `{"id":"msg_1","type":"message","role":"assistant","model":%q,
	  "content":[{"type":"text","text":%s}],"stop_reason":"end_turn",
	  "usage":{"input_tokens":2000,"output_tokens":300,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`, req.Model, text)
}

func (c *claude) calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.bodies...)
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newSummaries(t *testing.T) (*gocommerce.App, *aigateway.Module, *claude) {
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
	gw := aigateway.New(aigateway.Config{Client: client, CallTimeout: 2 * time.Second, PollInterval: time.Hour})
	app := gctest.New(t, gw, New(Config{Gateway: gw}))
	return app, gw, c
}

// switchOn sets the gateway up for ctx's store with message bodies shared,
// changed by extra.
func switchOn(t *testing.T, app *gocommerce.App, ctx context.Context, extra map[string]any) {
	t.Helper()
	settings := map[string]any{
		"api_key": "sk-ant-test", "monthly_cap": 100000,
		"small_price_input": 100, "small_price_output": 500,
		"strong_price_input": 300, "strong_price_output": 1500,
		"share_message_bodies": true,
	}
	for k, v := range extra {
		settings[k] = v
	}
	on := true
	if _, err := app.Plugins().Update(ctx, aigateway.PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: settings}); err != nil {
		t.Fatal(err)
	}
}

type posted struct {
	name, email, status, title, body string
	rating                           int
}

// addReviews writes reviews oldest first and returns their ids.
func addReviews(t *testing.T, app *gocommerce.App, ctx context.Context, productID int64, rows ...posted) []int64 {
	t.Helper()
	ids := make([]int64, len(rows))
	for i, rv := range rows {
		if err := app.DB().QueryRowContext(ctx, `
			INSERT INTO reviews (product_id, name, email, rating, title, body, status, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, now() - make_interval(mins => $8)) RETURNING id`,
			productID, rv.name, rv.email, rv.rating, rv.title, rv.body, rv.status, len(rows)-i).Scan(&ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

func approved(rating int, body string) posted {
	return posted{name: "Shopper", email: "shopper@example.com", status: StatusApproved, rating: rating, body: body}
}

func summarizePath(productID int64) string {
	return "/api/admin/x/reviews/products/" + strconv.FormatInt(productID, 10) + "/summarize"
}

func answerCiting(praise, complaint []int64) string {
	ids := func(v []int64) string { b, _ := json.Marshal(v); return string(b) }
	return `{"praise": [{"topic": "quality", "label": "Soft, thick fabric", "review_ids": ` + ids(praise) + `}],
	  "complaints": [{"topic": "fit", "label": "Runs a size small", "review_ids": ` + ids(complaint) + `}]}`
}

func summarizeOK(t *testing.T, app *gocommerce.App, productID int64) Digest {
	t.Helper()
	rec := gctest.AdminRequest(t, app, http.MethodPost, summarizePath(productID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("summarize = %d: %s", rec.Code, rec.Body)
	}
	var d Digest
	gctest.DecodeData(t, rec, &d)
	return d
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("not an error envelope: %s", body)
	}
	return e.Error.Code
}

// The model is sent the published reviews' words and ratings and nothing
// else: no pending or rejected review, no name, no email. The average, the
// counts and the order of the themes are the engine's own.
func TestASummaryReadsOnlyPublishedWordsAndCountsInGo(t *testing.T) {
	app, _, c := newSummaries(t)
	ctx := context.Background()
	switchOn(t, app, ctx, nil)
	product := gctest.CreateProduct(t, app, "RV-SHIRT", 1900, 10)
	ids := addReviews(t, app, ctx, product.ID,
		posted{name: "Zelda Quixote", email: "zelda.quixote@example.com", status: StatusApproved, rating: 5, title: "Lovely", body: "Soft fabric, but it runs small."},
		approved(4, "Thick and soft."),
		approved(4, "Order a size up, it runs small."),
		approved(2, "Too tight across the shoulders."),
		posted{name: "Pending Pat", email: "pat@example.com", status: StatusPending, rating: 1, body: "Unmoderated grumble."},
		posted{name: "Rejected Rob", email: "rob@example.com", status: StatusRejected, rating: 1, body: "Rejected rant."},
	)
	// Praise cites one review twice; the complaint cites three.
	c.answers[smallModel] = answerCiting([]int64{ids[0], ids[1], ids[0]}, []int64{ids[0], ids[2], ids[3]})

	d := summarizeOK(t, app, product.ID)

	if d.State != StateSummarized || d.Outcome != aigateway.OutcomeOK || d.Read != 4 {
		t.Fatalf("digest = %+v", d)
	}
	if d.Rating.Count != 4 || d.Rating.Average != 3.8 {
		t.Errorf("rating = %+v, want 4 reviews averaging 3.8", d.Rating)
	}
	if len(d.Praise) != 1 || d.Praise[0].Count != 2 || fmt.Sprint(d.Praise[0].ReviewIDs) != fmt.Sprint([]int64{ids[0], ids[1]}) {
		t.Errorf("praise = %+v, want one theme counting reviews %d and %d once each", d.Praise, ids[0], ids[1])
	}
	if len(d.Complaints) != 1 || d.Complaints[0].Count != 3 || d.Complaints[0].Label != "Runs a size small" {
		t.Errorf("complaints = %+v", d.Complaints)
	}
	if len(d.Reviews) != 4 || d.Reviews[0].ID != ids[3] {
		t.Errorf("cited reviews = %+v, want the four cited, newest first", d.Reviews)
	}

	calls := c.calls()
	if len(calls) != 1 {
		t.Fatalf("%d calls, want 1", len(calls))
	}
	sent := calls[0]
	for _, want := range []string{"Soft fabric, but it runs small.", "Too tight across the shoulders.", "Lovely"} {
		if !strings.Contains(sent, want) {
			t.Errorf("the request is missing %q", want)
		}
	}
	for _, kept := range []string{"Unmoderated grumble", "Rejected rant", "Zelda", "zelda.quixote", "Pending Pat", "shopper@example.com"} {
		if strings.Contains(sent, kept) {
			t.Errorf("the request carries %q: %s", kept, sent)
		}
	}
}

// A theme citing a review that was not sent, here a pending one, fails the
// task's check and goes to the strong model; when that one does too, nothing
// is shown.
func TestAThemeCitingAReviewNotSentGoesUpTheLadder(t *testing.T) {
	app, _, c := newSummaries(t)
	ctx := context.Background()
	switchOn(t, app, ctx, nil)
	product := gctest.CreateProduct(t, app, "RV-MUG", 900, 10)
	ids := addReviews(t, app, ctx, product.ID,
		approved(5, "Keeps tea hot."), approved(4, "Keeps coffee hot."), approved(3, "Handle chipped."),
		posted{name: "P", email: "p@example.com", status: StatusPending, rating: 1, body: "Not yet published."},
	)
	c.answers[smallModel] = answerCiting([]int64{ids[0], ids[3]}, []int64{ids[2]})
	c.answers[strongModel] = answerCiting([]int64{ids[0], ids[1]}, []int64{ids[2]})

	d := summarizeOK(t, app, product.ID)
	if n := len(c.calls()); n != 2 {
		t.Fatalf("%d calls, want the small model then the strong one", n)
	}
	if d.Praise[0].Count != 2 || d.Praise[0].ReviewIDs[1] != ids[1] {
		t.Errorf("praise = %+v, want the strong model's answer", d.Praise)
	}

	c.answers[strongModel] = c.answers[smallModel]
	addReviews(t, app, ctx, product.ID, approved(5, "A new review, so the cache misses."))
	rec := gctest.AdminRequest(t, app, http.MethodPost, summarizePath(product.ID), nil)
	if rec.Code != http.StatusBadGateway || errorCode(t, rec.Body.Bytes()) != CodeAIFailed {
		t.Errorf("both rungs citing an unsent review = %d: %s", rec.Code, rec.Body)
	}
}

// With fewer than three published reviews the answer is "too few" and no
// model is asked, however many wait for moderation.
func TestTooFewReviewsCallNoModel(t *testing.T) {
	app, gw, c := newSummaries(t)
	ctx := context.Background()
	switchOn(t, app, ctx, nil)
	// No credit left, and still an answer: nothing is drawn without a model.
	if _, err := gw.SetFreeAllowance(ctx, 0, ""); err != nil {
		t.Fatal(err)
	}
	product := gctest.CreateProduct(t, app, "RV-FEW", 900, 10)
	addReviews(t, app, ctx, product.ID,
		approved(5, "Great."), approved(3, "Fine."),
		posted{name: "P", email: "p@example.com", status: StatusPending, rating: 4, body: "One."},
		posted{name: "Q", email: "q@example.com", status: StatusPending, rating: 4, body: "Two."},
	)
	d := summarizeOK(t, app, product.ID)
	if d.State != StateTooFew || d.Outcome != aigateway.OutcomeDeterministic || d.Rating.Count != 2 || len(d.Praise)+len(d.Complaints) != 0 || d.CostMicroMinor != 0 {
		t.Errorf("digest = %+v", d)
	}
	if n := len(c.calls()); n != 0 {
		t.Errorf("%d model calls, want none", n)
	}
	if credit, err := gw.Credit(ctx); err != nil || credit.ConsumedMicroMinor != 0 {
		t.Errorf("credit = %+v %v, want nothing consumed", credit, err)
	}
}

// Asking again about the same reviews costs nothing, from the cap or the
// credit, even with no credit left; a new review is a new question.
func TestAnUnchangedSetOfReviewsIsAnsweredFromTheCache(t *testing.T) {
	app, gw, c := newSummaries(t)
	ctx := context.Background()
	switchOn(t, app, ctx, nil)
	product := gctest.CreateProduct(t, app, "RV-CACHE", 900, 10)
	ids := addReviews(t, app, ctx, product.ID, approved(5, "Good."), approved(4, "Nice."), approved(2, "Small."))
	c.answers[smallModel] = answerCiting([]int64{ids[0], ids[1]}, []int64{ids[2]})

	first := summarizeOK(t, app, product.ID)
	spent := func() int64 {
		b, err := gw.Budget(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return b.SpentMicroMinor
	}
	before := spent()
	if first.CostMicroMinor != 350_000 || before != 350_000 {
		t.Fatalf("first run cost %d, budget spent %d; want 350000 each", first.CostMicroMinor, before)
	}

	consumed := func() int64 {
		credit, err := gw.Credit(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return credit.ConsumedMicroMinor
	}
	if consumed() != 350_000 {
		t.Fatalf("credit consumed %d, want 350000", consumed())
	}
	if _, err := gw.SetFreeAllowance(ctx, 0, ""); err != nil {
		t.Fatal(err)
	}

	again := summarizeOK(t, app, product.ID)
	if again.Outcome != aigateway.OutcomeCached || again.CostMicroMinor != 0 || spent() != before || consumed() != 350_000 || len(c.calls()) != 1 {
		t.Errorf("second ask = %+v, spent %d, consumed %d, %d calls; want the cache, free", again, spent(), consumed(), len(c.calls()))
	}
	if fmt.Sprint(again.Complaints) != fmt.Sprint(first.Complaints) {
		t.Errorf("cached complaints = %+v, want %+v", again.Complaints, first.Complaints)
	}

	if _, err := gw.SetFreeAllowance(ctx, 5, ""); err != nil {
		t.Fatal(err)
	}
	addReviews(t, app, ctx, product.ID, approved(1, "Fell apart."))
	if d := summarizeOK(t, app, product.ID); d.Outcome != aigateway.OutcomeOK || len(c.calls()) != 2 {
		t.Errorf("after a new review = %+v with %d calls; want a fresh answer", d, len(c.calls()))
	}
}

// Each way a summary cannot be made answers with its own code, and none of
// them reaches the model except a provider that fails.
func TestASummarySaysWhatStopsIt(t *testing.T) {
	for _, tc := range []struct {
		name       string
		settings   map[string]any
		off        bool
		noCredit   bool
		failing    bool
		wantStatus int
		wantCode   string
		wantCalls  int
	}{
		{name: "gateway off", off: true, wantStatus: http.StatusConflict, wantCode: CodeAINotReady},
		{name: "message bodies kept in", settings: map[string]any{"share_message_bodies": false}, wantStatus: http.StatusConflict, wantCode: CodeAIDataKeptIn},
		{name: "cap used up", settings: map[string]any{"monthly_cap": 1}, wantStatus: http.StatusConflict, wantCode: CodeAIBudgetExceeded},
		{name: "no credit left", noCredit: true, wantStatus: http.StatusConflict, wantCode: CodeAICreditExceeded},
		{name: "no price", settings: map[string]any{"small_price_output": nil}, wantStatus: http.StatusConflict, wantCode: CodeAINotReady},
		{name: "provider fails", failing: true, wantStatus: http.StatusBadGateway, wantCode: CodeAIFailed, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, gw, c := newSummaries(t)
			ctx := context.Background()
			if !tc.off {
				switchOn(t, app, ctx, tc.settings)
			}
			if tc.noCredit {
				if _, err := gw.SetFreeAllowance(ctx, 0, ""); err != nil {
					t.Fatal(err)
				}
			}
			if tc.failing {
				c.status = http.StatusInternalServerError
			}
			product := gctest.CreateProduct(t, app, "RV-STOP", 900, 10)
			addReviews(t, app, ctx, product.ID, approved(5, "A."), approved(4, "B."), approved(3, "C."))
			rec := gctest.AdminRequest(t, app, http.MethodPost, summarizePath(product.ID), nil)
			if rec.Code != tc.wantStatus || errorCode(t, rec.Body.Bytes()) != tc.wantCode {
				t.Errorf("= %d %s, want %d %s", rec.Code, rec.Body, tc.wantStatus, tc.wantCode)
			}
			if n := len(c.calls()); n != tc.wantCalls {
				t.Errorf("%d model calls, want %d", n, tc.wantCalls)
			}
		})
	}
}

func TestABinaryWithoutTheGatewaySaysSo(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	product := gctest.CreateProduct(t, app, "RV-NOGW", 900, 10)
	rec := gctest.AdminRequest(t, app, http.MethodPost, summarizePath(product.ID), nil)
	if rec.Code != http.StatusConflict || errorCode(t, rec.Body.Bytes()) != CodeAIUnavailable {
		t.Errorf("= %d %s", rec.Code, rec.Body)
	}
}

// Summarizing is reading reviews: a role that may see them may ask, and one
// that may not is refused before anything is sent.
func TestSummariesAreGatedOnSeeingReviews(t *testing.T) {
	app, _, c := newSummaries(t)
	ctx := context.Background()
	switchOn(t, app, ctx, nil)
	product := gctest.CreateProduct(t, app, "RV-RIGHTS", 900, 10)
	ids := addReviews(t, app, ctx, product.ID, approved(5, "A."), approved(4, "B."), approved(3, "C."))
	c.answers[smallModel] = answerCiting([]int64{ids[0]}, []int64{ids[2]})

	catalogue := gctest.AgentKey(t, app, gocommerce.RightCatalogRead)
	if rec := gctest.SessionRequest(t, app, catalogue, http.MethodPost, summarizePath(product.ID), nil); rec.Code != http.StatusForbidden {
		t.Errorf("a key that reads the catalogue but not reviews = %d, want 403", rec.Code)
	}
	if n := len(c.calls()); n != 0 {
		t.Errorf("a refused caller made %d model calls", n)
	}
	staff := gctest.OperatorToken(t, app, "staff@example.com", gocommerce.RoleStaff)
	if rec := gctest.SessionRequest(t, app, staff, http.MethodPost, summarizePath(product.ID), nil); rec.Code != http.StatusOK {
		t.Errorf("staff, who see reviews = %d: %s", rec.Code, rec.Body)
	}
}

// A store asking about another store's product finds no product, and
// nothing is sent.
func TestAStoreCannotSummarizeAnotherStoresProduct(t *testing.T) {
	app, _, c := newSummaries(t)
	a := context.Background()
	second, err := app.Stores().Create(a, "Second", "second")
	if err != nil {
		t.Fatal(err)
	}
	b := gocommerce.WithStore(context.Background(), second.ID)
	switchOn(t, app, a, nil)
	switchOn(t, app, b, nil)
	product := gctest.CreateProduct(t, app, "RV-A", 900, 10)
	addReviews(t, app, a, product.ID, approved(5, "A."), approved(4, "B."), approved(3, "C."))

	req := httptest.NewRequest(http.MethodPost, summarizePath(product.ID), nil)
	req.Header.Set("Authorization", "Bearer "+gctest.AdminToken)
	req.Header.Set("X-Store", second.ID)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("store B summarizing store A's product = %d: %s", rec.Code, rec.Body)
	}
	if n := len(c.calls()); n != 0 {
		t.Errorf("%d model calls, want none", n)
	}
}

// Past the cap the newest reviews are read, and the digest says how many of
// how many.
func TestTheNewestReviewsAreRead(t *testing.T) {
	app, _, c := newSummaries(t)
	ctx := context.Background()
	switchOn(t, app, ctx, nil)
	product := gctest.CreateProduct(t, app, "RV-MANY", 900, 10)
	rows := make([]posted, maxReviewsRead+2)
	for i := range rows {
		rows[i] = approved(4, fmt.Sprintf("Review number %03d.", i))
	}
	ids := addReviews(t, app, ctx, product.ID, rows...)
	c.answers[smallModel] = answerCiting([]int64{ids[len(ids)-1]}, []int64{ids[2]})

	d := summarizeOK(t, app, product.ID)
	if d.Read != maxReviewsRead || d.Rating.Count != maxReviewsRead+2 {
		t.Errorf("read %d of %d, want %d of %d", d.Read, d.Rating.Count, maxReviewsRead, maxReviewsRead+2)
	}
	sent := c.calls()[0]
	for i, want := range map[int]bool{0: false, 1: false, 2: true, len(rows) - 1: true} {
		if got := strings.Contains(sent, fmt.Sprintf("Review number %03d.", i)); got != want {
			t.Errorf("review %d sent = %v, want %v", i, got, want)
		}
	}
}
