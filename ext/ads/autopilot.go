package ads

// The autopilot (D269): with its plugin switched on, the daily job makes the
// proposed changes (D268) that sit inside the limits the owner set, and
// leaves everything else waiting for approval. Each change it makes is logged
// as an approved one is, under the name "autopilot", and the owner is emailed.
// One click on the ads changes screen switches it off.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	gocommerce "github.com/misiki-in/gocommerce/core"
)

// AutopilotPluginKey is the plugin that switches the autopilot on and holds its limits.
const AutopilotPluginKey = "ads-autopilot"

// EventAutopilot is the email the owner gets when the autopilot changed something.
const EventAutopilot = "ads.autopilot"

// autopilotActor is who the change log says made an automatic change.
const autopilotActor = "autopilot"

// Autopilot is the autopilot's switch and limits, as the screen shows them.
type Autopilot struct {
	Enabled bool `json:"enabled"`
	// Missing names the settings still to fill; it does nothing until they are.
	Missing []string `json:"missing"`
	// Actions are the actions it may take alone; a budget change is
	// budget_down or budget_up.
	Actions        []string `json:"actions"`
	MaxBudgetPct   int      `json:"max_budget_change_pct"`
	DailyRaiseCap  int64    `json:"daily_increase_cap"`
	CoolingHours   int      `json:"cooling_hours"`
	NotifyEmail    string   `json:"notify_email"`
	AllowPause     bool     `json:"-" plugin:"allow_pause"`
	AllowDown      bool     `json:"-" plugin:"allow_budget_down"`
	AllowUp        bool     `json:"-" plugin:"allow_budget_up"`
	AllowNegatives bool     `json:"-" plugin:"allow_negative_keywords"`
	AllowExclude   bool     `json:"-" plugin:"allow_exclude_placements"`
	AllowSchedule  bool     `json:"-" plugin:"allow_ad_schedule"`
	PctSetting     int      `json:"-" plugin:"max_budget_change_pct"`
	CapSetting     int64    `json:"-" plugin:"daily_increase_cap"`
	CoolSetting    int      `json:"-" plugin:"cooling_hours"`
	EmailSetting   string   `json:"-" plugin:"notify_email"`
}

func (m *Module) registerAutopilot(app *gocommerce.App) {
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: AutopilotPluginKey, Title: "Ads autopilot", Icon: "ri-steering-2-line", Category: "marketing",
		Description: "Once a day, makes the proposed changes to your Google Ads and Meta campaigns that sit inside the limits below, " +
			"and leaves the rest for you to approve. It never raises a budget past the month's ad budget, emails you each change, " +
			"and can be paused in one click on the ads changes screen. Never yet run against a real ad account.",
		Fields: []gocommerce.PluginField{
			{Key: "notify_email", Label: "Email each change to", Kind: "text", Required: true, Help: "It makes no change until it has somewhere to tell you."},
			{Key: "allow_budget_down", Label: "May lower a daily budget", Kind: "bool", Default: true},
			{Key: "allow_negative_keywords", Label: "May add negative keywords (Google)", Kind: "bool", Default: true},
			{Key: "allow_exclude_placements", Label: "May exclude placements (Google)", Kind: "bool", Default: true},
			{Key: "allow_pause", Label: "May pause a campaign", Kind: "bool", Default: false},
			{Key: "allow_budget_up", Label: "May raise a daily budget", Kind: "bool", Default: false, Help: "Only within the daily increase cap below and the month's ad budget."},
			{Key: "allow_ad_schedule", Label: "May set an ad schedule (Google)", Kind: "bool", Default: false},
			{Key: "max_budget_change_pct", Label: "Largest budget change, percent", Kind: "number", Default: 20, Help: "A proposed change bigger than this waits for you."},
			{Key: "daily_increase_cap", Label: "Most it may add to daily budgets in a day", Kind: "number", Default: 0,
				Help: "In whole units of the ad account's currency, all raises together; 0 means it never raises a budget alone."},
			{Key: "cooling_hours", Label: "Hours between changes to one campaign", Kind: "number", Default: 72},
		},
	})
	app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
		Channel: gocommerce.ChannelEmail, Event: EventAutopilot, Title: "Ads autopilot changes",
		Description: "To the address in the ads autopilot settings when it changed something in an ad account.",
		Variables:   []string{"count", "changes"},
		Subject:     "The ads autopilot made {{.count}} change(s) to your ad accounts",
		Body: `The ads autopilot made these changes, inside the limits you set:

{{.changes}}

Each one can be undone, and the autopilot paused, on the Ad changes screen.`,
	})
}

func (m *Module) autopilotSettings(ctx context.Context) *Autopilot {
	a := &Autopilot{Missing: []string{}, Actions: []string{}}
	a.Enabled, _ = m.app.Plugins().Enabled(ctx, AutopilotPluginKey)
	_ = m.app.Plugins().Fill(ctx, AutopilotPluginKey, a)
	a.MaxBudgetPct, a.DailyRaiseCap, a.CoolingHours, a.NotifyEmail = max(a.PctSetting, 0), max(a.CapSetting, 0), max(a.CoolSetting, 0), a.EmailSetting
	if a.NotifyEmail == "" {
		a.Missing = append(a.Missing, "an address to email each change to")
	}
	for _, f := range []struct {
		on   bool
		name string
	}{{a.AllowPause, ActionPause}, {a.AllowDown, "budget_down"}, {a.AllowUp, "budget_up"},
		{a.AllowNegatives, ActionNegativeKeywords}, {a.AllowExclude, ActionExcludePlacements}, {a.AllowSchedule, ActionAdSchedule}} {
		if f.on {
			a.Actions = append(a.Actions, f.name)
		}
	}
	return a
}

// within says whether the autopilot may make c alone, and if not, why.
// raised is what it has already added to daily budgets in c's currency in
// the last day; room what the month's budget for c's source has left after
// the raises it has made, or -1 with no budget.
func (a *Autopilot) within(c *Change, recent bool, raised, room int64, remaining int64) string {
	action := c.Action
	if action == ActionBudget {
		action = "budget_down"
		if c.Percent > 0 {
			action = "budget_up"
		}
	}
	allowed := false
	for _, x := range a.Actions {
		allowed = allowed || x == action
	}
	switch {
	case !allowed:
		return "not an action it may take alone"
	case recent:
		return fmt.Sprintf("the campaign was changed in the last %d hours", a.CoolingHours)
	case c.Action == ActionBudget && abs(c.Percent) > a.MaxBudgetPct:
		return fmt.Sprintf("a %d%% change is more than %d%%", abs(c.Percent), a.MaxBudgetPct)
	}
	if action == "budget_up" {
		delta := c.BudgetAfter.AmountMinor - c.BudgetBefore.AmountMinor
		cap := a.DailyRaiseCap
		for range gocommerce.CurrencyExponent(c.BudgetAfter.Currency) {
			cap *= 10
		}
		switch {
		case raised+delta > cap:
			return "it would pass the daily increase cap"
		case room < 0 || delta*remaining > room:
			return "it would take the month past its ad budget"
		}
	}
	return ""
}

func abs(v int) int { return max(v, -v) }

// autopilot makes the waiting changes inside the limits, oldest first, and
// emails the owner what it made. A change it may not make stays waiting.
func (m *Module) autopilot(ctx context.Context, now time.Time) (int, error) {
	a := m.autopilotSettings(ctx)
	if !a.Enabled || len(a.Missing) > 0 || len(a.Actions) == 0 {
		return 0, nil
	}
	rows, err := m.app.DB().QueryContext(ctx, `SELECT `+changeColumns+` FROM ads_changes WHERE status = 'proposed' ORDER BY id`)
	if err != nil {
		return 0, gocommerce.Internalf(err, "read the ad changes")
	}
	var waiting []*Change
	for rows.Next() {
		c, err := scanChange(rows)
		if err != nil {
			rows.Close()
			return 0, gocommerce.Internalf(err, "read the ad changes")
		}
		waiting = append(waiting, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, gocommerce.Internalf(err, "read the ad changes")
	}
	pace, err := m.pacing(ctx, "UTC", now)
	if err != nil {
		return 0, err
	}
	remaining := int64(pace.Days - pace.Day + 1)
	_, gc := m.googleSettings(ctx)
	_, mc := m.metaSettings(ctx)
	sources := map[string]string{}
	sources["google"], _, _ = channelOf(gc.Source, "")
	sources["meta"], _, _ = channelOf(mc.Source, "")
	actx := context.WithValue(ctx, actorKey{}, autopilotActor)
	var made []string
	for _, c := range waiting {
		var recent bool
		var raised int64
		err := m.app.DB().QueryRowContext(ctx, `
			SELECT EXISTS (SELECT 1 FROM ads_changes WHERE platform = $1 AND campaign_id = $2 AND status IN ('applied', 'undone', 'approved')
			                 AND decided_at > $3::timestamptz - make_interval(hours => $4)),
			       coalesce((SELECT sum(after_minor - before_minor) FROM ads_changes
			                  WHERE decided_by = $5 AND status = 'applied' AND action = 'budget' AND percent > 0
			                    AND currency IS NOT DISTINCT FROM $6 AND decided_at > $3::timestamptz - interval '24 hours'), 0)`,
			c.Platform, c.CampaignID, now, a.CoolingHours, autopilotActor, budgetCurrency(*c)).Scan(&recent, &raised)
		if err != nil {
			return len(made), gocommerce.Internalf(err, "read the recent ad changes")
		}
		room := int64(-1)
		for _, p := range pace.Sources {
			if c.BudgetAfter != nil && p.Source == sources[c.Platform] && p.Budget != nil && p.Budget.Currency == c.BudgetAfter.Currency {
				// The month at the rate so far, plus every raise the autopilot
				// made today for the rest of the month.
				room = p.Budget.AmountMinor - p.Projected.AmountMinor - raised*remaining
			}
		}
		if why := a.within(c, recent, raised, room, remaining); why != "" {
			continue
		}
		done, err := m.approve(actx, c.ID)
		if err != nil {
			return len(made), err
		}
		line := fmt.Sprintf("- %s, %s %s: %s", map[string]string{"google": "Google Ads", "meta": "Meta"}[c.Platform], strings.ReplaceAll(c.TargetKind, "_", " "), c.TargetName, c.Summary)
		if c.BudgetAfter != nil {
			line += fmt.Sprintf(" (%s to %s a day)", money(c.BudgetBefore.AmountMinor, c.BudgetBefore.Currency), money(c.BudgetAfter.AmountMinor, c.BudgetAfter.Currency))
		}
		if done.Status != "applied" {
			line += " - the platform refused it, so it waits for you: " + done.Log[len(done.Log)-1].Error
		}
		made = append(made, line)
	}
	if len(made) > 0 {
		// Best effort, as the store's other announcements are: the changes
		// and their log stand whether or not the email goes.
		if err := m.app.Notify(ctx, gocommerce.Notification{Event: EventAutopilot, Channel: gocommerce.ChannelEmail, To: a.NotifyEmail,
			Data: map[string]string{"count": fmt.Sprint(len(made)), "changes": strings.Join(made, "\n")}}); err != nil {
			m.app.Log().Info("ads: the autopilot email was not sent", "error", err)
		}
	}
	return len(made), nil
}

// pauseAutopilot switches the autopilot's plugin off.
func (m *Module) pauseAutopilot(ctx context.Context) error {
	off := false
	_, err := m.app.Plugins().Update(ctx, AutopilotPluginKey, gocommerce.PluginPatch{Enabled: &off})
	return err
}

func (m *Module) handlePauseAutopilot(w http.ResponseWriter, r *http.Request) {
	if err := m.pauseAutopilot(r.Context()); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	m.handleChanges(w, r)
}
