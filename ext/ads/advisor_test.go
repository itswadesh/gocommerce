package ads

import (
	"net/http"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

func (h *harness) advice(method string) Advice {
	h.t.Helper()
	rec := gctest.AdminRequest(h.t, h.app, method, "/api/admin/x/ads/advice?tz=UTC&from="+today()+"&to="+today(), nil)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("advice = %d %s", rec.Code, rec.Body)
	}
	var a Advice
	gctest.DecodeData(h.t, rec, &a)
	return a
}

func kinds(a Advice) string {
	var k []string
	for _, f := range a.Findings {
		k = append(k, f.Kind)
	}
	return strings.Join(k, ",")
}

func TestTheAdvisorFindsWasteWithAndWithoutAI(t *testing.T) {
	h := newHarness(t)
	v := h.product("ADV-1", 100000, 40000)
	h.buy("ann@example.com", map[string]any{"last": map[string]any{"utm_source": "google", "utm_medium": "cpc", "utm_campaign": "Diwali"}}, map[int64]int{v: 1})
	h.buy("bob@example.com", nil, map[int64]int{v: 1})
	h.buy("cy@example.com", map[string]any{"last": map[string]any{"gclid": "x"}}, map[int64]int{v: 1})
	for _, body := range []map[string]any{
		{"source": "google", "medium": "cpc", "campaign": "Diwali", "from": today(), "amount_minor": 400000, "currency": "INR"},
		{"source": "facebook", "medium": "paid", "campaign": "Nothing", "from": today(), "amount_minor": 5000, "currency": "INR"},
	} {
		gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/ads/spend", body)
	}

	// Without AI set up: the engine's own findings, each with its figure.
	a := h.advice(http.MethodGet)
	if a.Source != "rules" || kinds(a) != "loss,no_orders,attribution,attribution" {
		t.Fatalf("rules %s: %+v", kinds(a), a.Findings)
	}
	for _, f := range a.Findings {
		if f.Text == "" || f.Figure == "" || f.Action == "" {
			t.Errorf("finding %+v lacks its figure or action", f)
		}
	}
	if !strings.Contains(a.Findings[1].Figure, "₹50.00") {
		t.Errorf("no-orders figure %q", a.Findings[1].Figure)
	}
	// Asking for AI when the model gives no usable answer falls back and says why.
	if b := h.advice(http.MethodPost); b.Source != "rules" || b.Note == "" || len(b.Findings) != 4 {
		t.Errorf("fallback %+v", b)
	}

	// With AI: the model's rewrite, held to the input's numbers.
	h.switchOn(nil)
	h.claude.answer = `{"findings":[{"kind":"no_orders","text":"Nothing spent ₹50.00 and sold nothing.","figure":"Spend ₹50.00 · 0 orders","action":"Pause the campaign."}]}`
	if b := h.advice(http.MethodPost); b.Source != "ai" || len(b.Findings) != 1 || b.ExecutionID == 0 {
		t.Errorf("ai advice %+v", b)
	}
	// An invented number fails both models and falls back to the rules.
	h.claude.answer = `{"findings":[{"kind":"loss","text":"Diwali wastes 75% of its spend.","figure":"x","action":"Stop it."}]}`
	if b := h.advice(http.MethodPost); b.Source != "rules" || !strings.Contains(b.Note, "not made") {
		t.Errorf("invented figure %+v", b)
	}
}

func TestQuietSpans(t *testing.T) {
	hours := make([]Slice, 24)
	for _, h := range []int{9, 10, 14, 20, 23} {
		hours[h].Orders = 1
	}
	got := quietSpans(hours)
	want := "from 11:00 to 14:00 and from 15:00 to 20:00 and from 00:00 to 09:00"
	if got != want {
		t.Errorf("quietSpans = %q, want %q", got, want)
	}
	hours[0].Orders = 1 // midnight sells, so the night starts at 01:00
	if got := quietSpans(hours); got != "from 01:00 to 09:00 and from 11:00 to 14:00 and from 15:00 to 20:00" {
		t.Errorf("quietSpans = %q", got)
	}
	hours[23].Orders, hours[0].Orders = 0, 0
	if got := quietSpans(hours); !strings.HasSuffix(got, "from 21:00 to 09:00") {
		t.Errorf("wrapping span = %q", got)
	}
}

func TestLosingRegionIsAFinding(t *testing.T) {
	inr := func(v int64) gocommerce.Money { return gocommerce.Money{AmountMinor: v, Currency: "INR"} }
	f := rules(&Report{}, []*CurrencyBreakdown{{Currency: "INR", Regions: []Slice{{Key: "Goa", Orders: 2, GrossProfit: inr(-5000)}}}})
	if len(f) != 1 || f[0].Kind != "losing_region" || !strings.Contains(f[0].Text, "Goa") {
		t.Errorf("findings %+v", f)
	}
}
