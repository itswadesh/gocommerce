package ads

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

func TestBreakdownCutsTheReportsOrders(t *testing.T) {
	h := newHarness(t)
	v := h.product("CUT-1", 100000, 40000)
	h.buy("ann@example.com", map[string]any{"last": map[string]any{"utm_source": "google", "utm_medium": "cpc", "utm_campaign": "Diwali"}}, map[int64]int{v: 1})
	h.buy("bob@example.com", map[string]any{"last": map[string]any{"utm_source": "facebook", "utm_medium": "paid", "utm_campaign": "diwali"}}, map[int64]int{v: 1})
	h.buy("cy@example.com", nil, map[int64]int{v: 1})
	for _, body := range []map[string]any{
		{"source": "google", "medium": "cpc", "campaign": "Diwali", "from": today(), "amount_minor": 30000, "currency": "INR"},
		{"source": "meta", "medium": "paid", "campaign": "Nothing sold", "from": today(), "amount_minor": 5000, "currency": "INR"},
	} {
		if rec := gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/ads/spend", body); rec.Code != http.StatusCreated {
			t.Fatalf("enter spend = %d %s", rec.Code, rec.Body)
		}
	}

	rec := gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/ads/breakdown?tz=UTC&from="+today()+"&to="+today(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("breakdown = %d %s", rec.Code, rec.Body)
	}
	var b Breakdown
	gctest.DecodeData(t, rec, &b)
	if len(b.Currencies) != 1 {
		t.Fatalf("currencies %+v", b.Currencies)
	}
	c := b.Currencies[0]
	if len(c.Weekdays) != 7 || len(c.Hours) != 24 {
		t.Fatalf("%d weekdays and %d hours, want every one listed", len(c.Weekdays), len(c.Hours))
	}
	now := time.Now().UTC()
	wd := (int(now.Weekday())+6)%7 + 1
	// The orders were placed seconds ago; an hour that turned meanwhile
	// splits them, so the hours are summed.
	hours := 0
	for _, s := range c.Hours {
		hours += s.Orders
	}
	if c.Weekdays[wd-1].Key != strconv.Itoa(wd) || c.Weekdays[wd-1].Orders != 3 || hours != 3 {
		t.Errorf("weekday %d: %+v, hours sum %d", wd, c.Weekdays[wd-1], hours)
	}
	if len(c.Regions) != 1 || c.Regions[0].Key != "MH" || c.Regions[0].Orders != 3 {
		t.Errorf("regions %+v", c.Regions)
	}
	// One campaign on two platforms is one line; spend with no sales is its own.
	var diwali, nothing *Slice
	for i := range c.Campaigns {
		switch c.Campaigns[i].Key {
		case "Diwali":
			diwali = &c.Campaigns[i]
		case "Nothing sold":
			nothing = &c.Campaigns[i]
		}
	}
	if diwali == nil || diwali.Orders != 2 || diwali.Spend == nil || diwali.Spend.AmountMinor != 30000 || diwali.ROAS == nil {
		t.Errorf("Diwali %+v in %+v", diwali, c.Campaigns)
	}
	if nothing == nil || nothing.Orders != 0 || nothing.Spend.AmountMinor != 5000 {
		t.Errorf("unsold campaign %+v", nothing)
	}
	if len(c.Campaigns) != 2 {
		t.Errorf("campaigns %+v: an order with no campaign has no line", c.Campaigns)
	}
}

func TestPacingSetsSpendAgainstTheBudget(t *testing.T) {
	h := newHarness(t)
	if rec := gctest.AdminRequest(t, h.app, http.MethodPut, "/api/admin/x/ads/budgets",
		map[string]any{"source": "Google", "amount_minor": 3000000}); rec.Code != http.StatusOK {
		t.Fatalf("budget = %d %s", rec.Code, rec.Body)
	}
	if rec := gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/ads/spend",
		map[string]any{"source": "meta", "campaign": "C", "from": today(), "amount_minor": 700, "currency": "INR"}); rec.Code != http.StatusCreated {
		t.Fatalf("spend = %d %s", rec.Code, rec.Body)
	}
	rec := gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/ads/pacing?tz=UTC", nil)
	var p Pacing
	gctest.DecodeData(t, rec, &p)
	if len(p.Sources) != 2 || p.Sources[0].Source != "google" || p.Sources[0].Budget.AmountMinor != 3000000 ||
		p.Sources[0].Spent.AmountMinor != 0 || p.Sources[0].Status != "under" {
		t.Fatalf("pacing %+v", p)
	}
	if m := p.Sources[1]; m.Source != "meta" || m.Budget != nil || m.Status != "none" || m.Spent.AmountMinor != 700 {
		t.Errorf("unbudgeted source %+v", m)
	}
	if rec := gctest.AdminRequest(t, h.app, http.MethodPut, "/api/admin/x/ads/budgets",
		map[string]any{"source": "google", "amount_minor": 0}); rec.Code != http.StatusNoContent {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body)
	}
	gctest.DecodeData(t, gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/ads/pacing?tz=UTC", nil), &p)
	if len(p.Sources) != 1 {
		t.Errorf("after removing the budget %+v", p.Sources)
	}
}

func TestPace(t *testing.T) {
	inr := func(v int64) gocommerce.Money { return gocommerce.Money{AmountMinor: v, Currency: "INR"} }
	b := inr(30000)
	for _, c := range []struct {
		spent     int64
		day, days int
		want      string
		projected int64
	}{
		{10000, 10, 30, "on_pace", 30000},
		{12000, 10, 30, "over", 36000},
		{7000, 10, 30, "under", 21000},
		{7500, 10, 30, "on_pace", 22500}, // exactly three quarters of the expected share
	} {
		p := pace("google", &b, inr(c.spent), c.day, c.days)
		if p.Status != c.want || p.Projected.AmountMinor != c.projected || p.Expected.AmountMinor != 10000 {
			t.Errorf("spent %d on day %d: %+v, want %s projecting %d", c.spent, c.day, p, c.want, c.projected)
		}
	}
}
