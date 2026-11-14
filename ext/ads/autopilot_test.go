package ads

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	gocommerce "github.com/misiki-in/gocommerce/core"
	"github.com/misiki-in/gocommerce/gctest"
)

func TestAutopilotLimits(t *testing.T) {
	a := &Autopilot{Actions: []string{"budget_down", "budget_up"}, MaxBudgetPct: 20, DailyRaiseCap: 100, CoolingHours: 72}
	inr := func(v int64) *gocommerce.Money { return &gocommerce.Money{AmountMinor: v, Currency: "INR"} }
	down := &Change{Action: ActionBudget, Percent: -20, BudgetBefore: inr(10000), BudgetAfter: inr(8000)}
	up := &Change{Action: ActionBudget, Percent: 20, BudgetBefore: inr(10000), BudgetAfter: inr(12000)}
	for _, c := range []struct {
		name          string
		c             *Change
		recent        bool
		raised, room  int64
		wantRefusedBy string
	}{
		{"a cut inside the limits", down, false, 0, -1, ""},
		{"an action it may not take", &Change{Action: ActionPause}, false, 0, -1, "not an action"},
		{"cooling", down, true, 0, -1, "last 72 hours"},
		{"too big", &Change{Action: ActionBudget, Percent: -30, BudgetBefore: inr(10000), BudgetAfter: inr(7000)}, false, 0, -1, "more than 20%"},
		{"a raise inside the cap and the month", up, false, 0, 100000, ""},
		{"a raise past the cap (₹100 = 10000)", up, false, 9000, 100000, "daily increase cap"},
		{"a raise with no monthly budget", up, false, 0, -1, "ad budget"},
		{"a raise past the month", up, false, 0, 2000*10 - 1, "ad budget"},
	} {
		got := a.within(c.c, c.recent, c.raised, c.room, 10)
		if (c.wantRefusedBy == "") != (got == "") || !strings.Contains(got, c.wantRefusedBy) {
			t.Errorf("%s: %q", c.name, got)
		}
	}
}

func TestAutopilotMakesOnlyWhatIsInsideItsLimits(t *testing.T) {
	api := &adsAPI{}
	h := newStubHarness(t, api.serve)
	h.plugin(GooglePluginKey, map[string]any{"developer_token": "dev-tok", "client_id": "cid", "client_secret": "csecret",
		"refresh_token": "rtok", "customer_id": "123-456-7890"})
	v := h.product("AP-1", 100000, 40000)
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
	gctest.AdminRequest(t, h.app, http.MethodPut, "/api/admin/x/ads/budgets", map[string]any{"source": "google", "amount_minor": 100000000, "currency": "INR"})
	ctx := context.Background()

	// Off by default: the daily job proposes and makes nothing.
	if err := h.mod.proposeDaily(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	view := h.changes()
	if view.Autopilot.Enabled || len(view.Changes) == 0 || len(api.writes()) != 0 {
		t.Fatalf("autopilot off: %+v, writes %v", view.Autopilot, api.writes())
	}
	// It cannot be switched on without an address to tell.
	if rec := gctest.AdminRequest(t, h.app, http.MethodPatch, "/api/admin/plugins/"+AutopilotPluginKey, map[string]any{"enabled": true}); rec.Code != http.StatusBadRequest {
		t.Fatalf("autopilot with no address = %d", rec.Code)
	}

	// On with its defaults: budget cuts, negatives and exclusions alone, one
	// change per campaign, nothing paused or raised.
	h.plugin(AutopilotPluginKey, map[string]any{"notify_email": "owner@example.com"})
	n, err := h.mod.autopilot(ctx, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("autopilot made %d (%v)", n, err)
	}
	var made []*Change
	for _, c := range h.changes().Changes {
		if c.DecidedBy == autopilotActor {
			made = append(made, c)
		}
	}
	if len(made) != 1 || made[0].Action != ActionBudget || made[0].TargetID != "12" || made[0].Status != "applied" || made[0].Log[0].Actor != autopilotActor {
		t.Fatalf("made %+v", made)
	}
	rec := gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/notifications?event="+EventAutopilot, nil)
	if !strings.Contains(rec.Body.String(), "owner@example.com") || !strings.Contains(rec.Body.String(), "Brand") {
		t.Errorf("the owner was not told: %s", rec.Body)
	}

	// Raises, once allowed and inside the cap and the month.
	h.plugin(AutopilotPluginKey, map[string]any{"allow_budget_up": true, "daily_increase_cap": 100})
	if n, _ := h.mod.autopilot(ctx, time.Now()); n != 0 {
		t.Errorf("a ₹200 raise passed a ₹100 cap")
	}
	h.plugin(AutopilotPluginKey, map[string]any{"daily_increase_cap": 500})
	if n, _ := h.mod.autopilot(ctx, time.Now()); n != 1 {
		t.Errorf("the raise inside the cap was not made")
	}

	// One click stops it.
	if rec := gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/ads/autopilot/pause", nil); rec.Code != http.StatusOK {
		t.Fatalf("pause = %d %s", rec.Code, rec.Body)
	}
	before := len(api.writes())
	if n, _ := h.mod.autopilot(ctx, time.Now()); n != 0 || len(api.writes()) != before || h.changes().Autopilot.Enabled {
		t.Errorf("paused autopilot still acted")
	}
}

func TestApprovingIsTheOwnersUntilARoleIsGivenIt(t *testing.T) {
	h := newStubHarness(t, (&adsAPI{}).serve)
	manager := gctest.OperatorToken(t, h.app, "manager@example.com", gocommerce.RoleManager)
	if rec := gctest.SessionRequest(t, h.app, manager, http.MethodGet, "/api/admin/x/ads/changes", nil); rec.Code != http.StatusOK {
		t.Errorf("a manager reading changes = %d", rec.Code)
	}
	for _, path := range []string{"/api/admin/x/ads/changes/approve", "/api/admin/x/ads/changes/1/approve", "/api/admin/x/ads/autopilot/pause"} {
		if rec := gctest.SessionRequest(t, h.app, manager, http.MethodPost, path, nil); rec.Code != http.StatusForbidden {
			t.Errorf("a manager POST %s = %d", path, rec.Code)
		}
	}
}
