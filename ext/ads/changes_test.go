package ads

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// adsAPI is a stub of Google's token, search and mutate endpoints and Meta's
// Graph API, answering as their documentation says they answer.
type adsAPI struct {
	mu      sync.Mutex
	calls   []string // host path body
	failing bool
}

func (s *adsAPI) serve(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	body := string(b)
	s.mu.Lock()
	s.calls = append(s.calls, r.Host+" "+r.URL.Path+" "+body)
	failing := s.failing
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Host == "oauth2.googleapis.com":
		_, _ = io.WriteString(w, `{"access_token":"ya29.stub","expires_in":3599,"token_type":"Bearer"}`)
	case failing:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":400,"message":"Budget amount is too low.","status":"INVALID_ARGUMENT"}}`)
	case strings.HasSuffix(r.URL.Path, "googleAds:mutate") && strings.Contains(body, `"create"`):
		var res []string
		for i := range strings.Count(body, `"create"`) {
			res = append(res, fmt.Sprintf(`{"campaignCriterionResult":{"resourceName":"customers/1234567890/campaignCriteria/12~%d"}}`, 500+i))
		}
		fmt.Fprintf(w, `{"mutateOperationResponses":[%s]}`, strings.Join(res, ","))
	case strings.HasSuffix(r.URL.Path, "googleAds:mutate"):
		_, _ = io.WriteString(w, `{"mutateOperationResponses":[{"campaignResult":{"resourceName":"customers/1234567890/campaigns/11"}}]}`)
	case strings.Contains(body, "FROM campaign WHERE campaign.status"):
		_, _ = io.WriteString(w, `{"results":[
		  {"customer":{"currencyCode":"INR","timeZone":"UTC"},"campaign":{"id":"11","name":"Diwali"},"campaignBudget":{"resourceName":"customers/1234567890/campaignBudgets/91","amountMicros":"1000000000"}},
		  {"customer":{"currencyCode":"INR","timeZone":"UTC"},"campaign":{"id":"12","name":"Brand"},"campaignBudget":{"resourceName":"customers/1234567890/campaignBudgets/92","amountMicros":"500000000"}},
		  {"customer":{"currencyCode":"INR","timeZone":"UTC"},"campaign":{"id":"13","name":"Winner"},"campaignBudget":{"resourceName":"customers/1234567890/campaignBudgets/93","amountMicros":"1000000000"}},
		  {"customer":{"currencyCode":"INR","timeZone":"UTC"},"campaign":{"id":"14","name":"Shared"},"campaignBudget":{"resourceName":"customers/1234567890/campaignBudgets/94","amountMicros":"1000000000","explicitlyShared":true}}
		]}`)
	case strings.Contains(body, "FROM search_term_view"):
		_, _ = io.WriteString(w, `{"results":[
		  {"campaign":{"id":"12"},"searchTermView":{"searchTerm":"free kurta"},"metrics":{"clicks":"40","costMicros":"1200000000"}},
		  {"campaign":{"id":"12"},"searchTermView":{"searchTerm":"kurta"},"metrics":{"clicks":"2","costMicros":"1000000"}}
		]}`)
	case strings.Contains(body, "FROM detail_placement_view"):
		_, _ = io.WriteString(w, `{"results":[
		  {"campaign":{"id":"12"},"detailPlacementView":{"placement":"junk.example","placementType":"WEBSITE"},"metrics":{"clicks":"90","costMicros":"1500000000"}},
		  {"campaign":{"id":"12"},"detailPlacementView":{"placement":"mobileapp::2-com.game","placementType":"MOBILE_APPLICATION"},"metrics":{"clicks":"90","costMicros":"1500000000"}}
		]}`)
	case strings.Contains(body, "FROM campaign_criterion"):
		_, _ = io.WriteString(w, `{"results":[]}`)
	case r.Host == "graph.facebook.com" && r.Method == http.MethodPost:
		_, _ = io.WriteString(w, `{"success":true}`)
	case r.Host == "graph.facebook.com" && strings.HasSuffix(r.URL.Path, "/campaigns"):
		_, _ = io.WriteString(w, `{"data":[{"id":"c1","name":"Retarget"},{"id":"c2","name":"Cold","daily_budget":"30000"}],"paging":{}}`)
	case r.Host == "graph.facebook.com" && strings.HasSuffix(r.URL.Path, "/adsets"):
		_, _ = io.WriteString(w, `{"data":[{"id":"as1","name":"Women 25-34","campaign_id":"c1","daily_budget":"50000"},{"id":"as2","name":"Under CBO","campaign_id":"c2"}],"paging":{}}`)
	case r.Host == "graph.facebook.com":
		_, _ = io.WriteString(w, `{"currency":"INR","timezone_name":"UTC","id":"act_42"}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *adsAPI) writes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.calls {
		if strings.Contains(c, "googleAds:mutate") || (strings.HasPrefix(c, "graph.facebook.com") && !strings.Contains(c, "/act_")) {
			out = append(out, c)
		}
	}
	return out
}

func (h *harness) changes() ChangesView {
	h.t.Helper()
	var v ChangesView
	gctest.DecodeData(h.t, gctest.AdminRequest(h.t, h.app, http.MethodGet, "/api/admin/x/ads/changes", nil), &v)
	return v
}

func (h *harness) propose() ChangesView {
	h.t.Helper()
	rec := gctest.AdminRequest(h.t, h.app, http.MethodPost, "/api/admin/x/ads/changes/propose", nil)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("propose = %d %s", rec.Code, rec.Body)
	}
	var v ChangesView
	gctest.DecodeData(h.t, rec, &v)
	return v
}

func (h *harness) act(id int64, what string, want int) Change {
	h.t.Helper()
	rec := gctest.AdminRequest(h.t, h.app, http.MethodPost, fmt.Sprintf("/api/admin/x/ads/changes/%d/%s", id, what), nil)
	if rec.Code != want {
		h.t.Fatalf("%s %d = %d %s", what, id, rec.Code, rec.Body)
	}
	var c Change
	if want == http.StatusOK {
		gctest.DecodeData(h.t, rec, &c)
	}
	return c
}

func find(v ChangesView, action, target string) *Change {
	for _, c := range v.Changes {
		if c.Action == action && c.TargetID == target && c.Status == "proposed" {
			return c
		}
	}
	return nil
}

func spendToday(t *testing.T, h *harness, source, medium, campaign string, amount int64) {
	t.Helper()
	rec := gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/ads/spend", map[string]any{"source": source, "medium": medium,
		"campaign": campaign, "from": today(), "amount_minor": amount, "currency": "INR"})
	if rec.Code/100 != 2 {
		t.Fatalf("spend = %d %s", rec.Code, rec.Body)
	}
}

func TestNothingIsProposedWithoutCredentials(t *testing.T) {
	api := &adsAPI{}
	h := newStubHarness(t, api.serve)
	spendToday(t, h, "google", "cpc", "Diwali", 200000)
	got := h.propose()
	if len(got.Connected) != 0 || len(got.Changes) != 0 || len(api.calls) != 0 {
		t.Fatalf("with no credentials: %+v, %d calls", got, len(api.calls))
	}
	if err := h.mod.proposeDaily(context.Background(), time.Now()); err != nil || len(api.calls) != 0 {
		t.Fatalf("daily job: %v, %d calls", err, len(api.calls))
	}
}

func TestGoogleChangesAreProposedApprovedAndUndone(t *testing.T) {
	api := &adsAPI{}
	h := newStubHarness(t, api.serve)
	h.plugin(GooglePluginKey, map[string]any{"developer_token": "dev-tok", "client_id": "cid", "client_secret": "csecret",
		"refresh_token": "rtok", "customer_id": "123-456-7890"})
	v := h.product("CHG-1", 100000, 40000)
	google := func(c string) any {
		return map[string]any{"last": map[string]any{"utm_source": "google", "utm_medium": "cpc", "utm_campaign": c}}
	}
	for i := range 2 {
		h.buy(fmt.Sprintf("b%d@example.com", i), google("Brand"), map[int64]int{v: 1})
	}
	for i := range 5 {
		h.buy(fmt.Sprintf("w%d@example.com", i), google("Winner"), map[int64]int{v: 1})
	}
	spendToday(t, h, "google", "cpc", "Diwali", 200000)
	spendToday(t, h, "google", "cpc", "Brand", 200000)
	spendToday(t, h, "google", "cpc", "Winner", 10000)
	spendToday(t, h, "google", "cpc", "Shared", 1000000)
	// A raise is only proposed inside a monthly budget.
	if rec := gctest.AdminRequest(t, h.app, http.MethodPut, "/api/admin/x/ads/budgets",
		map[string]any{"source": "google", "amount_minor": 100000000, "currency": "INR"}); rec.Code != http.StatusOK {
		t.Fatalf("budget = %d %s", rec.Code, rec.Body)
	}

	got := h.propose()
	pause, cut := find(got, ActionPause, "11"), find(got, ActionBudget, "12")
	neg, place, raise := find(got, ActionNegativeKeywords, "12"), find(got, ActionExcludePlacements, "12"), find(got, ActionBudget, "13")
	if pause == nil || cut == nil || neg == nil || place == nil || raise == nil || find(got, ActionPause, "14") == nil || len(got.Changes) != 6 {
		for _, c := range got.Changes {
			t.Logf("%s %s %s %s", c.Action, c.TargetID, c.Summary, c.Figure)
		}
		t.Fatalf("%d changes proposed", len(got.Changes))
	}
	if cut.BudgetBefore.AmountMinor != 50000 || cut.BudgetAfter.AmountMinor != 40000 || cut.Percent != -20 ||
		raise.BudgetAfter.AmountMinor != 120000 || raise.Percent != 20 {
		t.Errorf("budgets: cut %+v→%+v, raise %+v", cut.BudgetBefore, cut.BudgetAfter, raise.BudgetAfter)
	}
	if strings.Join(neg.Items, ",") != "free kurta" || strings.Join(place.Items, ",") != "junk.example" || pause.Figure == "" {
		t.Errorf("items: %v %v, figure %q", neg.Items, place.Items, pause.Figure)
	}
	if len(api.writes()) != 0 {
		t.Fatalf("proposing wrote to the account: %v", api.writes())
	}

	// Approving makes it, and undoing takes it back.
	c := h.act(pause.ID, "approve", http.StatusOK)
	w := api.writes()
	if c.Status != "applied" || !c.Undoable || len(c.Log) != 1 || !c.Log[0].OK || c.DecidedBy == "" ||
		len(w) != 1 || !strings.Contains(w[0], `"resourceName":"customers/1234567890/campaigns/11","status":"PAUSED"`) {
		t.Fatalf("approve: %+v, writes %v", c, w)
	}
	h.act(pause.ID, "approve", http.StatusConflict)
	if c = h.act(pause.ID, "undo", http.StatusOK); c.Status != "undone" || len(c.Log) != 2 || !strings.Contains(api.writes()[1], `"status":"ENABLED"`) {
		t.Fatalf("undo: %+v", c)
	}

	// Keywords are created, and their undo removes exactly what was created.
	c = h.act(neg.ID, "approve", http.StatusOK)
	if w := api.writes(); !strings.Contains(w[2], `"keyword":{"matchType":"EXACT","text":"free kurta"}`) || !strings.Contains(w[2], `"negative":true`) {
		t.Fatalf("negatives sent %s", w[2])
	}
	h.act(neg.ID, "undo", http.StatusOK)
	if w := api.writes(); !strings.Contains(w[3], `"remove":"customers/1234567890/campaignCriteria/12~500"`) {
		t.Fatalf("negatives undo sent %s", w[3])
	}

	// A refusal is kept with the platform's words, and can be approved again.
	api.failing = true
	c = h.act(cut.ID, "approve", http.StatusOK)
	if c.Status != "failed" || !strings.Contains(c.Log[0].Error, "Budget amount is too low") || c.Undoable {
		t.Fatalf("failed apply: %+v", c)
	}
	api.failing = false
	if c = h.act(cut.ID, "approve", http.StatusOK); c.Status != "applied" || !strings.Contains(c.Log[1].Request, `"amountMicros":"400000000"`) {
		t.Fatalf("retry: %+v", c)
	}

	// Dismissed stays dismissed when the platforms are read again.
	h.act(raise.ID, "dismiss", http.StatusOK)
	got = h.propose()
	if find(got, ActionBudget, "13") != nil || find(got, ActionPause, "11") != nil {
		t.Errorf("a dismissed or recent change came back")
	}
	if rec := gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/ads/changes/approve", nil); rec.Code != http.StatusOK {
		t.Fatalf("approve all = %d %s", rec.Code, rec.Body)
	}
	for _, c := range h.changes().Changes {
		if c.Status == "proposed" {
			t.Errorf("still proposed after approve all: %+v", c)
		}
	}
}

func TestMetaChangesAreMadeOnTheAdSetOrCampaign(t *testing.T) {
	api := &adsAPI{}
	h := newStubHarness(t, api.serve)
	h.plugin(MetaPluginKey, map[string]any{"access_token": "EAAB", "ad_account_id": "act_42", "app_secret": "shh"})
	v := h.product("CHG-2", 100000, 40000)
	h.buy("m@example.com", map[string]any{"last": map[string]any{"utm_source": "facebook", "utm_medium": "paid", "utm_campaign": "Retarget"}}, map[int64]int{v: 1})
	spendToday(t, h, "facebook", "paid", "Retarget", 300000)
	spendToday(t, h, "facebook", "paid", "Cold", 300000)

	got := h.propose()
	cut, pause := find(got, ActionBudget, "as1"), find(got, ActionPause, "c2")
	if cut == nil || pause == nil || cut.TargetKind != "ad_set" || cut.BudgetAfter.AmountMinor != 40000 {
		t.Fatalf("meta proposals %+v", got.Changes)
	}
	h.act(cut.ID, "approve", http.StatusOK)
	h.act(cut.ID, "undo", http.StatusOK)
	h.act(pause.ID, "approve", http.StatusOK)
	w := api.writes()
	if len(w) != 3 || !strings.Contains(w[0], "/as1 ") || !strings.Contains(w[0], "daily_budget=40000") || !strings.Contains(w[0], "appsecret_proof=") ||
		!strings.Contains(w[1], "daily_budget=50000") || !strings.Contains(w[2], "/c2 ") || !strings.Contains(w[2], "status=PAUSED") {
		t.Fatalf("meta writes %v", w)
	}
	for _, c := range h.changes().Changes {
		for _, l := range c.Log {
			if strings.Contains(l.Request, "EAAB") {
				t.Errorf("the access token was kept in the log: %s", l.Request)
			}
		}
	}
}

func TestActiveHoursSkipQuietRuns(t *testing.T) {
	hours := make([]Slice, 24)
	for h := 9; h < 23; h++ {
		hours[h].Orders = 2
	}
	hours[2].Orders = 1 // one sale at 2am leaves 23–02 and 03–09 quiet
	if got := fmt.Sprint(activeHours(hours)); got != "[[2 3] [9 23]]" {
		t.Errorf("active hours %s", got)
	}
	for h := range hours {
		hours[h].Orders = 1
	}
	if activeHours(hours) != nil {
		t.Errorf("every hour sells, and a schedule was made")
	}
}

func TestAScheduleIsProposedOnlyInTheAccountsZoneWithEnoughOrders(t *testing.T) {
	rep := &Report{From: "2026-11-01", To: "2026-11-30", Currencies: []*CurrencyReport{{Currency: "INR",
		Totals: Row{Orders: 40, GrossProfit: gocommerce.Money{AmountMinor: 400000}},
		Rows: []Row{{Source: "google", Campaign: "brand", Orders: 40, GrossProfit: gocommerce.Money{AmountMinor: 400000},
			Spend: &gocommerce.Money{AmountMinor: 300000, Currency: "INR"}}}}}}
	hours := make([]Slice, 24)
	for h := 8; h < 22; h++ {
		hours[h].Orders = 3
	}
	cuts := []*CurrencyBreakdown{{Currency: "INR", Hours: hours}}
	a := &account{Currency: "INR", TimeZone: "Asia/Kolkata", Scheduled: map[string]bool{},
		Entities: []adEntity{{Kind: "campaign", ID: "7", CampaignID: "7", Name: "Brand", Budget: "b", Daily: 10000}}}
	list := proposals("google", "google", a, rep, cuts, nil)
	if len(list) != 1 || list[0].Action != ActionAdSchedule || fmt.Sprint(list[0].d.Hours) != "[[8 22]]" {
		t.Fatalf("schedule %+v", list)
	}
	ops := googleOps(googleConfig{CustomerID: "1"}, &list[0], &list[0].d, false)
	if len(ops) != 7 {
		t.Errorf("%d schedule criteria, want one a day", len(ops))
	}
	a.Scheduled["7"] = true
	if list := proposals("google", "google", a, rep, cuts, nil); len(list) != 0 {
		t.Errorf("a campaign with its own schedule got another: %+v", list)
	}
	a.Scheduled["7"], a.TimeZone = false, ""
	if list := proposals("google", "google", a, rep, cuts, nil); len(list) != 0 {
		t.Errorf("a schedule in another zone: %+v", list)
	}
}
