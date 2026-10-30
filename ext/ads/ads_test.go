package ads

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

type fakeClaude struct {
	mu       sync.Mutex
	answer   string
	requests []map[string]any
}

func (c *fakeClaude) serve(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	c.mu.Lock()
	c.requests = append(c.requests, body)
	answer := c.answer
	c.mu.Unlock()
	model, _ := body["model"].(string)
	text, _ := json.Marshal(answer)
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"id":"msg_1","type":"message","role":"assistant","model":%q,
	  "content":[{"type":"text","text":%s}],"stop_reason":"end_turn",
	  "usage":{"input_tokens":2000,"output_tokens":300,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`, model, text)
}

func (c *fakeClaude) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type harness struct {
	t      *testing.T
	app    *gocommerce.App
	mod    *Module
	gw     *aigateway.Module
	claude *fakeClaude
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	c := &fakeClaude{}
	srv := httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(srv.Close)
	to, _ := url.Parse(srv.URL)
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		out := r.Clone(r.Context())
		out.URL.Scheme, out.URL.Host, out.Host = to.Scheme, to.Host, to.Host
		return http.DefaultTransport.RoundTrip(out)
	})}
	gw := aigateway.New(aigateway.Config{Client: client, CallTimeout: 5 * time.Second, PollInterval: time.Hour,
		PlatformAnthropicKey: "sk-ant-platform", PlatformUSDRates: "INR=1",
		PlatformSmallPrices: "1,5,1,1", PlatformStrongPrices: "3,15,3,3"})
	mod := New(Config{Gateway: gw})
	app := gctest.NewWithConfig(t, gocommerce.Config{Currency: "INR"}, gw, mod)
	return &harness{t: t, app: app, mod: mod, gw: gw, claude: c}
}

// product is an active product at price, with its cost recorded when cost is
// not negative.
func (h *harness) product(sku string, price, cost int64) int64 {
	h.t.Helper()
	p := gctest.CreateProduct(h.t, h.app, sku, price, 50)
	v := p.DefaultVariant()
	if cost >= 0 {
		rec := gctest.AdminRequest(h.t, h.app, http.MethodPatch, "/api/admin/variants/"+strconv.FormatInt(v.ID, 10),
			map[string]any{"cost_minor": cost})
		if rec.Code != http.StatusOK {
			h.t.Fatalf("record the cost = %d: %s", rec.Code, rec.Body)
		}
	}
	return v.ID
}

func (h *harness) buy(email string, attribution any, lines map[int64]int) *gocommerce.Order {
	h.t.Helper()
	ctx := context.Background()
	cart, err := h.app.Cart().Create(ctx, "")
	if err != nil {
		h.t.Fatal(err)
	}
	for v, q := range lines {
		if _, err := h.app.Cart().AddLine(ctx, cart.Token, v, q); err != nil {
			h.t.Fatal(err)
		}
	}
	in := gocommerce.CheckoutInput{CartID: cart.Token, Email: email, Name: "Shopper",
		Address: gocommerce.Address{Line1: "1 Test Street", City: "Pune", State: "MH", PostalCode: "411001", Country: "IN"}}
	if attribution != nil {
		in.Metadata = gocommerce.Metadata{"attribution": attribution}
	}
	res, err := h.app.Order().Checkout(ctx, "cod", in, "")
	if err != nil {
		h.t.Fatalf("checkout: %v", err)
	}
	return res.Order
}

func today() string { return time.Now().UTC().Format("2006-01-02") }

func (h *harness) report(query string) Report {
	h.t.Helper()
	rec := gctest.AdminRequest(h.t, h.app, http.MethodGet, "/api/admin/x/ads/report?tz=UTC&from="+today()+"&to="+today()+query, nil)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("report = %d %s", rec.Code, rec.Body)
	}
	var r Report
	gctest.DecodeData(h.t, rec, &r)
	return r
}

func row(t *testing.T, r Report, currency, source, campaign string) Row {
	t.Helper()
	for _, c := range r.Currencies {
		if c.Currency != currency {
			continue
		}
		for _, row := range c.Rows {
			if row.Source == source && row.Campaign == campaign {
				return row
			}
		}
	}
	t.Fatalf("no %s row for %s / %s in %+v", currency, source, campaign, r)
	return Row{}
}

func TestAttributionOnTheOrderIsWhatTheReportReads(t *testing.T) {
	h := newHarness(t)
	costed := h.product("ATTR-1", 100000, 40000)
	uncosted := h.product("ATTR-2", 50000, -1)

	attribution := map[string]any{
		"first": map[string]any{"utm_source": "facebook", "utm_medium": "paid", "utm_campaign": "Launch", "fbclid": "fb.1", "at": "2026-10-01T09:00:00Z"},
		"last":  map[string]any{"utm_source": "google", "utm_medium": "cpc", "utm_campaign": "Diwali", "gclid": "Cj0", "referrer": "www.google.com", "at": "2026-10-20T09:00:00Z"},
	}
	first := h.buy("ann@example.com", attribution, map[int64]int{costed: 1, uncosted: 1})
	if got, _ := json.Marshal(first.Metadata["attribution"]); !strings.Contains(string(got), `"gclid":"Cj0"`) {
		t.Fatalf("the order keeps %s, want the attribution it was placed with", got)
	}
	h.buy("Ann@Example.com", map[string]any{"last": map[string]any{"utm_source": "google", "utm_medium": "cpc", "utm_campaign": "diwali"}},
		map[int64]int{costed: 1})
	h.buy("bob@example.com", nil, map[int64]int{costed: 1})

	last := h.report("")
	d := row(t, last, "INR", "google", "Diwali")
	if d.Orders != 2 || d.Revenue.AmountMinor != first.Total.AmountMinor+100000 {
		t.Errorf("Diwali by last touch %+v (first order total %d)", d, first.Total.AmountMinor)
	}
	if d.ProductCost.AmountMinor != 80000 || d.UncostedUnits != 1 {
		t.Errorf("Diwali cost %+v uncosted %d, want ₹800 of cost and the uncosted unit named", d.ProductCost, d.UncostedUnits)
	}
	if d.NewCustomers != 1 || d.ReturningCustomers != 1 {
		t.Errorf("Diwali customers new %d returning %d", d.NewCustomers, d.ReturningCustomers)
	}
	if nr := row(t, last, "INR", notRecorded, ""); nr.Orders != 1 {
		t.Errorf("unattributed %+v", nr)
	}

	byFirst := h.report("&touch=first")
	if l := row(t, byFirst, "INR", "facebook", "Launch"); l.Orders != 1 {
		t.Errorf("Launch by first touch %+v", l)
	}

	// Spend for the campaign brings ROAS and profit per rupee; spend in
	// another currency stays in its own block.
	for _, body := range []map[string]any{
		{"source": "Google", "medium": "cpc", "campaign": "DIWALI", "from": today(), "to": today(), "amount_minor": 50000, "currency": "INR"},
		{"source": "google", "medium": "cpc", "campaign": "Diwali", "from": today(), "to": today(), "amount_minor": 900, "currency": "USD"},
	} {
		if rec := gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/ads/spend", body); rec.Code != http.StatusCreated {
			t.Fatalf("enter spend = %d %s", rec.Code, rec.Body)
		}
	}
	withSpend := h.report("")
	d = row(t, withSpend, "INR", "google", "Diwali")
	if d.Spend == nil || d.Spend.AmountMinor != 50000 || d.ROAS == nil || d.CostPerOrder.AmountMinor != 25000 {
		t.Errorf("Diwali with spend %+v", d)
	}
	if u := row(t, withSpend, "USD", "google", "Diwali"); u.Orders != 0 || u.Spend.AmountMinor != 900 {
		t.Errorf("USD spend %+v", u)
	}

	// Drilling into the row lists its orders.
	rec := gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/ads/orders?tz=UTC&from="+today()+"&to="+today()+
		"&currency=INR&source=google&medium=cpc&campaign=Diwali", nil)
	var orders []OrderRow
	gctest.DecodeData(t, rec, &orders)
	if len(orders) != 2 || orders[0].Customer == orders[1].Customer {
		t.Errorf("drill-down %+v", orders)
	}
}

func TestSpendImportsAndReplacesTheSameDays(t *testing.T) {
	h := newHarness(t)
	csv := "Day,Campaign,Currency code,Cost\n" + today() + ",Brand,INR,\"1,000.00\"\n"
	for range 2 {
		rec := gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/ads/spend/import",
			map[string]any{"format": "google", "source": "google", "medium": "cpc", "csv": csv})
		if rec.Code != http.StatusOK {
			t.Fatalf("import = %d %s", rec.Code, rec.Body)
		}
	}
	if b := row(t, h.report(""), "INR", "google", "Brand"); b.Spend.AmountMinor != 100000 {
		t.Errorf("imported twice = %+v, want the day's figure once", b.Spend)
	}
	rec := gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/ads/spend/import",
		map[string]any{"format": "google", "source": "google", "csv": "Campaign,Cost\nx,1\n"})
	if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
		t.Errorf("a file without days = %d %s", rec.Code, rec.Body)
	}
}

func (h *harness) switchOn(extra map[string]any) {
	h.t.Helper()
	settings := map[string]any{
		"api_key": "sk-ant-test", "monthly_cap": 100000,
		"small_price_input": 100, "small_price_output": 500,
		"strong_price_input": 300, "strong_price_output": 1500,
		"share_catalogue": true, "share_order_lines": true,
	}
	for k, v := range extra {
		settings[k] = v
	}
	on := true
	if _, err := h.app.Plugins().Update(context.Background(), aigateway.PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: settings}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) summarise() (int, Summary, string) {
	h.t.Helper()
	rec := gctest.AdminRequest(h.t, h.app, http.MethodPost, "/api/admin/x/ads/summary",
		map[string]any{"from": today(), "to": today(), "tz": "UTC"})
	var env struct {
		Data  Summary `json:"data"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	body, _ := io.ReadAll(rec.Body)
	if err := json.Unmarshal(body, &env); err != nil {
		h.t.Fatalf("%d %s", rec.Code, body)
	}
	return rec.Code, env.Data, env.Error.Code
}

func TestTheSummaryIsGroundedInTheReport(t *testing.T) {
	h := newHarness(t)
	v := h.product("SUM-1", 100000, 40000)
	h.buy("ann@example.com", map[string]any{"last": map[string]any{"utm_source": "google", "utm_medium": "cpc", "utm_campaign": "Diwali"}},
		map[int64]int{v: 1})
	gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/ads/spend",
		map[string]any{"source": "google", "medium": "cpc", "campaign": "Diwali", "from": today(), "to": today(), "amount_minor": 400000, "currency": "INR"})
	h.switchOn(nil)
	if rec := gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/ads/summary", nil); !strings.Contains(rec.Body.String(), `"data":null`) {
		t.Errorf("no summary yet = %s, want data null", rec.Body)
	}

	h.claude.answer = `{"headline":"Diwali spent ₹4,000.00 for 1 order.","points":[{"row":1,"kind":"wasting","text":"Diwali: ₹4,000.00 spent, 1 order, a loss after product cost."}]}`
	code, s, errCode := h.summarise()
	if code != http.StatusOK {
		t.Fatalf("summary = %d %s", code, errCode)
	}
	if s.Headline == "" || len(s.Points) != 1 || len(s.Figures) != 1 || s.Figures[0].Spend != "₹4,000.00" {
		t.Errorf("summary %+v", s)
	}
	sent, _ := json.Marshal(h.claude.requests[len(h.claude.requests)-1])
	if !strings.Contains(string(sent), "₹4,000.00") || strings.Contains(string(sent), "ann@example.com") {
		t.Errorf("the model was sent %s; want the figures and no shopper", sent)
	}
	rec := gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/ads/summary", nil)
	var latest Summary
	gctest.DecodeData(t, rec, &latest)
	if latest.ID != s.ID {
		t.Errorf("latest = %+v, want the one just made", latest)
	}

	// A figure the report does not hold fails both models, and nothing is kept.
	h.claude.answer = `{"headline":"Diwali sales grew 35%.","points":[]}`
	calls := h.claude.calls()
	if code, _, errCode := h.summarise(); code != http.StatusBadGateway || errCode != codeNoAnswer {
		t.Errorf("an invented figure = %d %s", code, errCode)
	}
	if n := h.claude.calls() - calls; n != 2 {
		t.Errorf("%d calls, want the small model then the strong one", n)
	}

	// Out of AI credit on the platform's key: refused before any call.
	h.switchOn(map[string]any{"api_key": nil})
	if _, err := h.gw.SetFreeAllowance(context.Background(), 0, ""); err != nil {
		t.Fatal(err)
	}
	calls = h.claude.calls()
	if code, _, errCode := h.summarise(); code != http.StatusConflict || errCode != codeCredit {
		t.Errorf("out of credit = %d %s", code, errCode)
	}
	if n := h.claude.calls() - calls; n != 0 {
		t.Errorf("%d calls with no AI credit left", n)
	}

	// Order lines kept in: the summary cannot be made at all.
	h.switchOn(map[string]any{"share_order_lines": false})
	if code, _, errCode := h.summarise(); code != http.StatusConflict || errCode != codePolicy {
		t.Errorf("order lines kept in = %d %s", code, errCode)
	}
}

func TestTheWeeklySummaryIsTheShopsChoice(t *testing.T) {
	h := newHarness(t)
	h.switchOn(nil)
	ctx := context.Background()
	weeklies := func() int {
		var n int
		if err := h.app.DB().QueryRowContext(ctx, `SELECT count(*) FROM ads_summaries WHERE kind = 'weekly'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if err := h.mod.weekly(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if weeklies() != 0 {
		t.Fatal("a weekly summary was made without the shop switching it on")
	}
	rec := gctest.AdminRequest(t, h.app, http.MethodPut, "/api/admin/x/ads/settings", map[string]any{"weekly_summary": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("settings = %d %s", rec.Code, rec.Body)
	}
	for range 2 {
		if err := h.mod.weekly(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if n := weeklies(); n != 1 {
		t.Errorf("%d weekly summaries, want one a week", n)
	}
}

func TestRoutesNameARightAndAreInTheContract(t *testing.T) {
	h := newHarness(t)
	gctest.AssertAdminRoutesDeclareRights(t, h.app, "ads")
	gctest.AssertSpecCoversModuleRoutes(t, h.app, "ads")
}
