package ads

// Approved changes (D268): the advisor's findings turned into concrete
// changes to a campaign, an ad set or its keywords, each waiting for somebody
// with the right to approve it. Approving one makes it on the platform; every
// attempt is kept with what was asked and what the platform answered, and a
// change the platform can take back can be undone. Nothing is proposed for a
// platform that is not connected, so a store without ad-account credentials
// has nothing to approve.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// The actions a change can take.
const (
	ActionPause             = "pause"
	ActionBudget            = "budget"
	ActionNegativeKeywords  = "negative_keywords"
	ActionExcludePlacements = "exclude_placements"
	ActionAdSchedule        = "ad_schedule"
)

// RightApprove is the right to approve, dismiss and undo changes to the ad
// accounts. It spends the shop's money, so only the owner has it until a role
// is given it.
const RightApprove gocommerce.Right = "ads.approve"

// budgetStep is how far one change moves a daily budget, in percent.
const budgetStep = 20

// maxItems caps the keywords or placements one change adds.
const maxItems = 20

// Change is one proposed change to an ad account and what became of it.
type Change struct {
	ID       int64  `json:"id"`
	Platform string `json:"platform"`
	// Action is pause, budget, negative_keywords, exclude_placements or ad_schedule.
	Action string `json:"action"`
	// Target is the campaign, or for a Meta budget the ad set, it changes.
	TargetKind string `json:"target_kind"`
	TargetID   string `json:"target_id"`
	TargetName string `json:"target_name"`
	CampaignID string `json:"campaign_id"`
	// Summary says the change in words; Before and After are the state it
	// moves between, for a change that is not a budget.
	Summary string `json:"summary"`
	Before  string `json:"before"`
	After   string `json:"after"`
	// BudgetBefore and BudgetAfter are a budget change's daily budget.
	BudgetBefore *gocommerce.Money `json:"budget_before"`
	BudgetAfter  *gocommerce.Money `json:"budget_after"`
	Percent      int               `json:"percent"`
	// Items are the keywords, placements or hours it adds.
	Items  []string `json:"items"`
	Figure string   `json:"figure"`
	Reason string   `json:"reason"`
	// Status is proposed, approved (being made), applied, failed, dismissed or undone.
	Status    string      `json:"status"`
	DecidedBy string      `json:"decided_by"`
	DecidedAt *time.Time  `json:"decided_at"`
	CreatedAt time.Time   `json:"created_at"`
	Undoable  bool        `json:"undoable"`
	Log       []ChangeLog `json:"log"`

	d changeDetail
}

// ChangeLog is one attempt to make or undo a change.
type ChangeLog struct {
	Kind     string    `json:"kind"` // apply or undo
	OK       bool      `json:"ok"`
	Actor    string    `json:"actor"`
	Request  string    `json:"request"`
	Response string    `json:"response"`
	Error    string    `json:"error"`
	At       time.Time `json:"at"`
}

// changeDetail is what the platform call needs that the screen does not.
type changeDetail struct {
	Budget     string     `json:"budget,omitempty"`
	Placements []termCost `json:"placements,omitempty"`
	Hours      [][2]int   `json:"hours,omitempty"`
	// Created are the criteria a Google change made, which undo removes.
	Created []string `json:"created,omitempty"`
}

func (c *Change) undoable() bool {
	return c.Status == "applied" && (c.Action == ActionPause || c.Action == ActionBudget || len(c.d.Created) > 0)
}

// activeHours are the runs of hours, [start, end), outside every run of three
// or more hours in a row with no orders (the advisor's quiet spans), or nil
// when there is no such run.
func activeHours(hours []Slice) [][2]int {
	if len(hours) != 24 {
		return nil
	}
	var quiet [24]bool
	found := false
	for h := range 24 {
		n := 0
		for n < 24 && hours[(h+n)%24].Orders == 0 {
			n++
		}
		if n >= 3 && n < 24 && hours[(h+23)%24].Orders > 0 {
			for i := range n {
				quiet[(h+i)%24] = true
			}
			found = true
		}
	}
	if !found {
		return nil
	}
	var runs [][2]int
	for h := 0; h < 24; {
		if quiet[h] {
			h++
			continue
		}
		s := h
		for h < 24 && !quiet[h] {
			h++
		}
		runs = append(runs, [2]int{s, h})
	}
	return runs
}

// proposals turns one platform's report rows into changes, only where the
// figures support them: a campaign that spent what an average order earns and
// brought none is paused; one that loses money after product cost has its
// budget lowered; one that earns twice its spend in gross profit from five
// orders or more has it raised, only while the month stays inside the
// source's monthly budget (D265). On Google, search terms and placements that
// cost a campaign what one of its orders costs and converted nothing are
// excluded, and hours that never sell are scheduled off.
func proposals(platform, source string, a *account, rep *Report, cuts []*CurrencyBreakdown, pace *Pacing) []Change {
	var out []Change
	var block *CurrencyReport
	for _, c := range rep.Currencies {
		if c.Currency == a.Currency {
			block = c
		}
	}
	if block == nil {
		return nil
	}
	cur := a.Currency
	byName := map[string]adEntity{}
	for _, e := range a.Entities {
		if e.Kind == "campaign" {
			byName[strings.ToLower(e.Name)] = e
		}
	}
	// holders are what carries a campaign's daily budget: the campaign, or on
	// Meta without a campaign budget, each of its ad sets that has one.
	holders := func(c adEntity) []adEntity {
		if c.Daily > 0 && !c.Shared && (platform == "meta" || c.Budget != "") {
			return []adEntity{c}
		}
		var out []adEntity
		if platform == "meta" {
			for _, e := range a.Entities {
				if e.Kind == "ad_set" && e.CampaignID == c.ID && e.Daily > 0 {
					out = append(out, e)
				}
			}
		}
		return out
	}
	room := int64(-1) // what the month's budget has left; -1 is no budget
	remaining := int64(1)
	if pace != nil {
		remaining = int64(pace.Days - pace.Day + 1)
		for _, p := range pace.Sources {
			if p.Source == source && p.Budget != nil && p.Budget.Currency == cur {
				room = max(p.Budget.AmountMinor-p.Projected.AmountMinor, 0)
			}
		}
	}
	avgProfit := int64(0)
	if block.Totals.Orders > 0 {
		avgProfit = block.Totals.GrossProfit.AmountMinor / int64(block.Totals.Orders)
	}
	budget := func(c, h adEntity, pct int, figure, reason string) Change {
		after := h.Daily * int64(100+pct) / 100
		dir, size := "Raise", pct
		if pct < 0 {
			dir, size = "Lower", -pct
		}
		what := "the campaign's"
		if h.Kind == "ad_set" {
			what = "ad set " + h.Name + "'s"
		}
		return Change{Platform: platform, Action: ActionBudget, TargetKind: h.Kind, TargetID: h.ID, TargetName: h.Name, CampaignID: c.ID,
			Summary:      fmt.Sprintf("%s %s daily budget by %d%%", dir, what, size),
			BudgetBefore: &gocommerce.Money{AmountMinor: h.Daily, Currency: cur}, BudgetAfter: &gocommerce.Money{AmountMinor: after, Currency: cur},
			Percent: pct, Figure: figure, Reason: reason, d: changeDetail{Budget: h.Budget}}
	}
	spent := map[string]bool{}
	for _, r := range block.Rows {
		c, ok := byName[strings.ToLower(r.Campaign)]
		if r.Source != source || r.Spend == nil || r.Spend.AmountMinor == 0 || r.Campaign == "" || !ok {
			continue
		}
		spend, profit := r.Spend.AmountMinor, r.GrossProfit.AmountMinor
		figure := fmt.Sprintf("%s to %s: spend %s · %d orders · gross profit %s", rep.From, rep.To, money(spend, cur), r.Orders, money(profit, cur))
		switch {
		case r.Orders == 0:
			if avgProfit > 0 && spend >= avgProfit {
				out = append(out, Change{Platform: platform, Action: ActionPause, TargetKind: "campaign", TargetID: c.ID, TargetName: c.Name, CampaignID: c.ID,
					Summary: "Pause the campaign", Before: "Running", After: "Paused",
					Figure: fmt.Sprintf("%s · an average order's gross profit is %s", figure, money(avgProfit, cur)),
					Reason: fmt.Sprintf("%s spent more than an average order earns and brought no orders.", c.Name)})
			}
			continue // a campaign with no orders gives nothing to judge its terms by
		case profit < spend:
			for _, h := range holders(c) {
				out = append(out, budget(c, h, -budgetStep, figure, fmt.Sprintf("%s loses money after product cost.", c.Name)))
			}
		case r.Orders >= 5 && profit >= 2*spend && room >= 0:
			for _, h := range holders(c) {
				if delta := h.Daily * budgetStep / 100; delta > 0 && delta*remaining <= room {
					room -= delta * remaining
					out = append(out, budget(c, h, budgetStep, figure+fmt.Sprintf(" · monthly budget room %s", money(room+delta*remaining, cur)),
						fmt.Sprintf("%s earns at least twice its spend in gross profit, and the raise keeps the month inside its budget.", c.Name)))
				}
			}
		}
		spent[c.ID] = true
		if platform != "google" {
			continue
		}
		cpo := spend / int64(r.Orders)
		for _, k := range []struct {
			list   []termCost
			action string
			what   string
		}{{a.Terms, ActionNegativeKeywords, "search terms"}, {a.Placements, ActionExcludePlacements, "placements"}} {
			var hit []termCost
			var cost int64
			for _, t := range k.list {
				if t.CampaignID == c.ID && t.Cost > 0 && t.Cost >= cpo {
					hit = append(hit, t)
				}
			}
			sort.SliceStable(hit, func(i, j int) bool { return hit[i].Cost > hit[j].Cost })
			if len(hit) > maxItems {
				hit = hit[:maxItems]
			}
			if len(hit) == 0 {
				continue
			}
			items := make([]string, len(hit))
			for i, t := range hit {
				items[i], cost = t.Text, cost+t.Cost
			}
			ch := Change{Platform: platform, Action: k.action, TargetKind: "campaign", TargetID: c.ID, TargetName: c.Name, CampaignID: c.ID,
				Items: items, Figure: fmt.Sprintf("%d %s cost %s with no conversions; the campaign's cost per order is %s", len(hit), k.what, money(cost, cur), money(cpo, cur)),
				Reason: fmt.Sprintf("Each of these %s cost %s what one of its orders costs and, by Google's count, sold nothing.", k.what, c.Name)}
			if k.action == ActionNegativeKeywords {
				ch.Summary = fmt.Sprintf("Add %d negative keywords (exact match)", len(hit))
			} else {
				ch.Summary, ch.d.Placements = fmt.Sprintf("Exclude %d placements", len(hit)), hit
			}
			out = append(out, ch)
		}
	}
	if platform == "google" && a.TimeZone != "" {
		for _, b := range cuts {
			total := 0
			for _, h := range b.Hours {
				total += h.Orders
			}
			runs := activeHours(b.Hours)
			if b.Currency != cur || total < minOrdersForHours || runs == nil {
				continue
			}
			items := make([]string, len(runs))
			for i, h := range runs {
				items[i] = fmt.Sprintf("%02d:00–%02d:00", h[0], h[1])
			}
			for _, e := range a.Entities {
				if e.Kind == "campaign" && spent[e.ID] && !a.Scheduled[e.ID] {
					out = append(out, Change{Platform: platform, Action: ActionAdSchedule, TargetKind: "campaign", TargetID: e.ID, TargetName: e.Name, CampaignID: e.ID,
						Summary: "Run the ads only in the hours that sell, every day", Before: "All hours", After: strings.Join(items, ", "), Items: items,
						Figure: fmt.Sprintf("0 of %d orders %s, %s to %s, in %s", total, quietSpans(b.Hours), rep.From, rep.To, a.TimeZone),
						Reason: "No orders were placed in the other hours.", d: changeDetail{Hours: runs}})
				}
			}
		}
	}
	return out
}

// propose reads each connected platform and replaces the changes still
// waiting with what the last 30 days support now. A change dismissed, made,
// failed or undone in the past week is not proposed again.
func (m *Module) propose(ctx context.Context, now time.Time) (int, error) {
	to := now.UTC().Format(time.DateOnly)
	from := now.UTC().AddDate(0, 0, -29).Format(time.DateOnly)
	n := 0
	for _, p := range []string{"google", "meta"} {
		var (
			a      *account
			source string
			err    error
		)
		switch p {
		case "google":
			c, ok := m.googleConfig(ctx)
			if !ok {
				continue
			}
			source = c.Source
			a, err = m.googleAccount(ctx, c, from, to)
		case "meta":
			c, ok := m.metaConfig(ctx)
			if !ok {
				continue
			}
			source = c.Source
			a, err = m.metaAccount(ctx, c)
		}
		if err != nil {
			return n, &gocommerce.APIError{Status: http.StatusBadGateway, Code: "platform_error", Message: err.Error()}
		}
		tz := a.TimeZone
		if tz == "" {
			tz = "UTC"
		}
		w, err := m.parseWindow(ctx, map[string]string{"from": from, "to": to, "tz": tz})
		if err != nil {
			w, err = m.parseWindow(ctx, map[string]string{"from": from, "to": to})
			a.TimeZone = "" // hours in another zone than the account's are no schedule
		}
		if err != nil {
			return n, err
		}
		rep, orders, err := m.report(ctx, w)
		if err != nil {
			return n, err
		}
		spend, err := m.spendFacts(ctx, w)
		if err != nil {
			return n, err
		}
		pace, err := m.pacing(ctx, w.tz, now)
		if err != nil {
			return n, err
		}
		source, _, _ = channelOf(source, "")
		list := proposals(p, source, a, rep, breakdown(orders, spend, w.touch), pace)
		err = gocommerce.InTx(ctx, m.app.DB(), func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `DELETE FROM ads_changes WHERE platform = $1 AND status = 'proposed'`, p); err != nil {
				return err
			}
			for _, c := range list {
				res, err := tx.ExecContext(ctx, `
					INSERT INTO ads_changes (platform, action, target_kind, target_id, target_name, campaign_id, summary,
					    before_state, after_state, currency, before_minor, after_minor, percent, items, figure, reason, detail, status)
					SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, 'proposed'
					 WHERE NOT EXISTS (SELECT 1 FROM ads_changes WHERE platform = $1 AND action = $2 AND target_id = $4
					                     AND status IN ('dismissed', 'applied', 'approved', 'failed', 'undone') AND coalesce(decided_at, created_at) > now() - interval '7 days')`,
					c.Platform, c.Action, c.TargetKind, c.TargetID, clip(c.TargetName), c.CampaignID, c.Summary, c.Before, c.After,
					budgetCurrency(c), budgetMinor(c.BudgetBefore), budgetMinor(c.BudgetAfter), c.Percent, jsonOf(c.Items), c.Figure, c.Reason, jsonOf(c.d))
				if err != nil {
					return err
				}
				k, _ := res.RowsAffected()
				n += int(k)
			}
			return nil
		})
		if err != nil {
			return n, gocommerce.Internalf(err, "keep the proposed changes")
		}
	}
	return n, nil
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	if string(b) == "null" {
		return "[]"
	}
	return string(b)
}

func budgetCurrency(c Change) any {
	if c.BudgetBefore == nil {
		return nil
	}
	return c.BudgetBefore.Currency
}

func budgetMinor(v *gocommerce.Money) any {
	if v == nil {
		return nil
	}
	return v.AmountMinor
}

const changeColumns = `id, platform, action, target_kind, target_id, target_name, campaign_id, summary, before_state, after_state,
	currency, before_minor, after_minor, percent, items, figure, reason, detail, status, decided_by, decided_at, created_at`

func scanChange(sc interface{ Scan(...any) error }) (*Change, error) {
	var c Change
	var cur sql.NullString
	var before, after sql.NullInt64
	var items, detail []byte
	err := sc.Scan(&c.ID, &c.Platform, &c.Action, &c.TargetKind, &c.TargetID, &c.TargetName, &c.CampaignID, &c.Summary, &c.Before, &c.After,
		&cur, &before, &after, &c.Percent, &items, &c.Figure, &c.Reason, &detail, &c.Status, &c.DecidedBy, &c.DecidedAt, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	if cur.Valid {
		c.BudgetBefore = &gocommerce.Money{AmountMinor: before.Int64, Currency: cur.String}
		c.BudgetAfter = &gocommerce.Money{AmountMinor: after.Int64, Currency: cur.String}
	}
	_ = json.Unmarshal(items, &c.Items)
	_ = json.Unmarshal(detail, &c.d)
	if c.Items == nil {
		c.Items = []string{}
	}
	c.Undoable = c.undoable()
	c.Log = []ChangeLog{}
	return &c, nil
}

// listChanges is the latest 200 changes, newest first, each with its
// attempts; with an id, only that one.
func (m *Module) listChanges(ctx context.Context, id int64) ([]*Change, error) {
	rows, err := m.app.DB().QueryContext(ctx, `SELECT `+changeColumns+` FROM ads_changes
		 WHERE $1 = 0 OR id = $1 ORDER BY created_at DESC, id DESC LIMIT 200`, id)
	if err != nil {
		return nil, gocommerce.Internalf(err, "read the ad changes")
	}
	defer rows.Close()
	out := []*Change{}
	byID := map[int64]*Change{}
	var ids []int64
	for rows.Next() {
		c, err := scanChange(rows)
		if err != nil {
			return nil, gocommerce.Internalf(err, "read the ad changes")
		}
		out, byID[c.ID], ids = append(out, c), c, append(ids, c.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, gocommerce.Internalf(err, "read the ad changes")
	}
	lrows, err := m.app.DB().QueryContext(ctx, `
		SELECT change_id, kind, ok, actor, request, response, error, at FROM ads_change_log WHERE change_id = ANY($1) ORDER BY at, id`, ids)
	if err != nil {
		return nil, gocommerce.Internalf(err, "read the ad change log")
	}
	defer lrows.Close()
	for lrows.Next() {
		var id int64
		var l ChangeLog
		if err := lrows.Scan(&id, &l.Kind, &l.OK, &l.Actor, &l.Request, &l.Response, &l.Error, &l.At); err != nil {
			return nil, gocommerce.Internalf(err, "read the ad change log")
		}
		byID[id].Log = append(byID[id].Log, l)
	}
	return out, lrows.Err()
}

// change is one change with its attempts, as the screen shows it.
func (m *Module) change(ctx context.Context, id int64) (*Change, error) {
	list, err := m.listChanges(ctx, id)
	if err == nil && len(list) == 0 {
		err = gocommerce.NotFoundf("no ad change %d", id)
	}
	if err != nil {
		return nil, err
	}
	return list[0], nil
}

// actor names who is acting, as the change log keeps it.
func actor(ctx context.Context) string {
	if label, ok := ctx.Value(actorKey{}).(string); ok {
		return label
	}
	if su := gocommerce.SuperuserFrom(ctx); su != nil {
		if su.Email != "" {
			return su.Email
		}
		return gocommerce.MaskPhone(su.Phone)
	}
	if gocommerce.AdminTokenFrom(ctx) {
		return "admin token"
	}
	return "system"
}

type actorKey struct{}

// approve makes a waiting (or failed) change on its platform and keeps what
// happened. It is claimed first, so two approvals of one change make it once.
func (m *Module) approve(ctx context.Context, id int64) (*Change, error) {
	who := actor(ctx)
	c, err := scanChange(m.app.DB().QueryRowContext(ctx, `
		UPDATE ads_changes SET status = 'approved', decided_by = $2, decided_at = now()
		 WHERE id = $1 AND status IN ('proposed', 'failed') RETURNING `+changeColumns, id, who))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, m.notWaiting(ctx, id)
	}
	if err != nil {
		return nil, gocommerce.Internalf(err, "claim the ad change")
	}
	req, res, sendErr := m.send(ctx, c, &c.d, false)
	c.Status = "applied"
	if sendErr != nil {
		c.Status = "failed"
	}
	if err := m.record(ctx, c, "apply", who, req, res, sendErr); err != nil {
		return nil, err
	}
	return m.change(ctx, id)
}

// undo takes an applied change back where the platform allows it.
func (m *Module) undo(ctx context.Context, id int64) (*Change, error) {
	who := actor(ctx)
	err := gocommerce.InTx(ctx, m.app.DB(), func(tx *sql.Tx) error {
		c, err := scanChange(tx.QueryRowContext(ctx, `SELECT `+changeColumns+` FROM ads_changes WHERE id = $1 FOR UPDATE`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return gocommerce.NotFoundf("no ad change %d", id)
		}
		if err != nil {
			return err
		}
		if !c.undoable() {
			return &gocommerce.APIError{Status: http.StatusConflict, Code: "not_undoable", Message: "only a change that was made, and that the platform can take back, can be undone"}
		}
		req, res, sendErr := m.send(ctx, c, &c.d, true)
		if sendErr == nil {
			c.Status = "undone"
		}
		return m.recordTx(ctx, tx, c, "undo", who, req, res, sendErr)
	})
	if err != nil {
		return nil, err
	}
	return m.change(ctx, id)
}

func (m *Module) record(ctx context.Context, c *Change, kind, who, req, res string, sendErr error) error {
	// Not on ctx alone: what the platform was asked is kept even if the
	// request that asked it has gone.
	ctx = context.WithoutCancel(ctx)
	return gocommerce.InTx(ctx, m.app.DB(), func(tx *sql.Tx) error { return m.recordTx(ctx, tx, c, kind, who, req, res, sendErr) })
}

func (m *Module) recordTx(ctx context.Context, tx *sql.Tx, c *Change, kind, who, req, res string, sendErr error) error {
	msg := ""
	if sendErr != nil {
		msg = sendErr.Error()
		// The engine's own refusals, such as a platform not connected, in
		// their words, without the code in front.
		var api *gocommerce.APIError
		if errors.As(sendErr, &api) {
			msg = api.Message
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ads_changes SET status = $2, detail = $3 WHERE id = $1`, c.ID, c.Status, jsonOf(c.d)); err != nil {
		return gocommerce.Internalf(err, "keep the ad change")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ads_change_log (change_id, kind, ok, actor, request, response, error) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		c.ID, kind, sendErr == nil, who, clip2000(req), clip2000(res), clip2000(msg)); err != nil {
		return gocommerce.Internalf(err, "keep the ad change log")
	}
	return nil
}

func (m *Module) notWaiting(ctx context.Context, id int64) error {
	var status string
	err := m.app.DB().QueryRowContext(ctx, `SELECT status FROM ads_changes WHERE id = $1`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return gocommerce.NotFoundf("no ad change %d: a newer reading may have replaced it", id)
	}
	if err != nil {
		return gocommerce.Internalf(err, "read the ad change")
	}
	return &gocommerce.APIError{Status: http.StatusConflict, Code: "not_waiting", Message: "this change is " + status + ", not waiting for approval"}
}

// ChangesView is the changes screen.
type ChangesView struct {
	// Connected are the platforms whose plugins are on and filled in; with
	// none, nothing is proposed or applied.
	Connected []string  `json:"connected"`
	Changes   []*Change `json:"changes"`
	// Autopilot is its switch and limits (D269).
	Autopilot *Autopilot `json:"autopilot"`
}

func (m *Module) changesView(ctx context.Context) (*ChangesView, error) {
	v := &ChangesView{Connected: []string{}, Autopilot: m.autopilotSettings(ctx)}
	if _, ok := m.googleConfig(ctx); ok {
		v.Connected = append(v.Connected, "google")
	}
	if _, ok := m.metaConfig(ctx); ok {
		v.Connected = append(v.Connected, "meta")
	}
	var err error
	v.Changes, err = m.listChanges(ctx, 0)
	return v, err
}

func (m *Module) handleChanges(w http.ResponseWriter, r *http.Request) {
	v, err := m.changesView(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, v)
}

func (m *Module) handlePropose(w http.ResponseWriter, r *http.Request) {
	if _, err := m.propose(r.Context(), time.Now()); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	m.handleChanges(w, r)
}

func changeID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, gocommerce.NotFoundf("no ad change %q", r.PathValue("id"))
	}
	return id, nil
}

func (m *Module) handleApprove(w http.ResponseWriter, r *http.Request) {
	id, err := changeID(r)
	if err == nil {
		var c *Change
		if c, err = m.approve(r.Context(), id); err == nil {
			gocommerce.Respond(w, http.StatusOK, c)
			return
		}
	}
	gocommerce.RespondError(w, r, err)
}

func (m *Module) handleUndo(w http.ResponseWriter, r *http.Request) {
	id, err := changeID(r)
	if err == nil {
		var c *Change
		if c, err = m.undo(r.Context(), id); err == nil {
			gocommerce.Respond(w, http.StatusOK, c)
			return
		}
	}
	gocommerce.RespondError(w, r, err)
}

func (m *Module) handleDismiss(w http.ResponseWriter, r *http.Request) {
	id, err := changeID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	c, err := scanChange(m.app.DB().QueryRowContext(r.Context(), `
		UPDATE ads_changes SET status = 'dismissed', decided_by = $2, decided_at = now()
		 WHERE id = $1 AND status IN ('proposed', 'failed') RETURNING `+changeColumns, id, actor(r.Context())))
	if errors.Is(err, sql.ErrNoRows) {
		err = m.notWaiting(r.Context(), id)
	}
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, c)
}

// handleApproveAll approves every change still proposed, one at a time; one
// the platform refuses is kept as failed and the rest still go.
func (m *Module) handleApproveAll(w http.ResponseWriter, r *http.Request) {
	rows, err := m.app.DB().QueryContext(r.Context(), `SELECT id FROM ads_changes WHERE status = 'proposed' ORDER BY id`)
	var ids []int64
	if err == nil {
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		rows.Close()
	}
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read the ad changes"))
		return
	}
	for _, id := range ids {
		if _, err := m.approve(r.Context(), id); err != nil {
			var api *gocommerce.APIError
			if !errors.As(err, &api) || api.Status >= 500 {
				gocommerce.RespondError(w, r, err)
				return
			}
		}
	}
	m.handleChanges(w, r)
}

// proposeDaily is the hourly job's step: once a day, with a platform
// connected, it reads the platforms again and refreshes what is waiting. The
// day is claimed before the platforms are read, so a failing platform is
// asked again tomorrow, not every hour.
func (m *Module) proposeDaily(ctx context.Context, now time.Time) error {
	if _, ok := m.googleConfig(ctx); !ok {
		if _, ok := m.metaConfig(ctx); !ok {
			return nil
		}
	}
	res, err := m.app.DB().ExecContext(ctx, `
		INSERT INTO ads_settings (proposed_at) VALUES ($1)
		ON CONFLICT (store_id) DO UPDATE SET proposed_at = EXCLUDED.proposed_at
		 WHERE ads_settings.proposed_at IS NULL OR ads_settings.proposed_at <= $1 - interval '20 hours'`, now)
	if err != nil {
		return gocommerce.Internalf(err, "claim the day's proposals")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	if _, err := m.propose(ctx, now); err != nil {
		m.app.Log().Info("ads: changes were not proposed", "error", err)
		return nil
	}
	_, err = m.autopilot(ctx, now)
	return err
}
