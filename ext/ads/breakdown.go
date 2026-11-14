package ads

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/misiki-in/gocommerce/core"
)

// Slice is the report's orders cut one more way: a day of the week, an hour,
// a state shipped to, or a campaign. Spend and what follows from it are only
// on a campaign, the one cut ad spend is recorded against.
type Slice struct {
	Key         string            `json:"key"`
	Orders      int               `json:"orders"`
	Net         gocommerce.Money  `json:"net"`
	GrossProfit gocommerce.Money  `json:"gross_profit"`
	Spend       *gocommerce.Money `json:"spend,omitempty"`
	ROAS        *float64          `json:"roas,omitempty"`
	POAS        *float64          `json:"poas,omitempty"`
	revenue     int64             // what ROAS is of, as in the report
}

// CurrencyBreakdown is one currency's cuts. Weekdays (1 is Monday) and Hours
// list every day and hour, the empty ones too, because an hour that never
// sells is the point of looking.
type CurrencyBreakdown struct {
	Currency  string  `json:"currency"`
	Weekdays  []Slice `json:"weekdays"`
	Hours     []Slice `json:"hours"`
	Regions   []Slice `json:"regions"`
	Campaigns []Slice `json:"campaigns"`
}

// Breakdown is the ads report's orders by when they were placed, where they
// were shipped and the campaign that brought them, for the same window.
type Breakdown struct {
	From       string               `json:"from"`
	To         string               `json:"to"`
	TimeZone   string               `json:"time_zone"`
	Touch      string               `json:"touch"`
	Currencies []*CurrencyBreakdown `json:"currencies"`
}

// noRegion is an order with no state in its address: a country without
// states, or an address typed without one.
const noRegion = "(no state)"

// campaignOf is the campaign an order is credited to, or "" when the visit
// had none. Campaigns are matched by name alone, whatever the source, so one
// campaign run on two platforms is one line.
func campaignOf(o orderFact, touch string) string {
	return o.Attr.touch(touch).channel().Campaign
}

// cut adds orders up by key, in one currency. keys, when given, are listed
// first and always, in their order; any other key follows, most net sales first.
func cut(orders []orderFact, currency string, key func(orderFact) string, keys []string) []Slice {
	tallies := map[string]*tally{}
	names := map[string]string{} // the first spelling seen, for a key matched ignoring case
	order := append([]string(nil), keys...)
	for _, k := range keys {
		tallies[k] = &tally{}
		names[k] = k
	}
	for _, o := range orders {
		if o.Currency != currency {
			continue
		}
		name := key(o)
		k := strings.ToLower(name)
		if keys != nil {
			k = name
		}
		t := tallies[k]
		if t == nil {
			if keys != nil || name == "" {
				continue
			}
			t = &tally{}
			tallies[k], names[k] = t, name
			order = append(order, k)
		}
		t.add(o)
	}
	out := make([]Slice, 0, len(order))
	for _, k := range order {
		r := tallies[k].row(currency)
		out = append(out, Slice{Key: names[k], Orders: r.Orders, Net: r.Net, GrossProfit: r.GrossProfit, revenue: r.Revenue.AmountMinor})
	}
	if keys == nil {
		sort.SliceStable(out, func(i, j int) bool { return out[i].Net.AmountMinor > out[j].Net.AmountMinor })
	}
	return out
}

func breakdown(orders []orderFact, spend []spendFact, touch string) []*CurrencyBreakdown {
	currencies := map[string]bool{}
	for _, o := range orders {
		currencies[o.Currency] = true
	}
	for _, s := range spend {
		currencies[s.Currency] = true
	}
	days := make([]string, 7)
	for i := range days {
		days[i] = strconv.Itoa(i + 1)
	}
	hours := make([]string, 24)
	for i := range hours {
		hours[i] = strconv.Itoa(i)
	}
	out := []*CurrencyBreakdown{}
	for cur := range currencies {
		b := &CurrencyBreakdown{
			Currency: cur,
			Weekdays: cut(orders, cur, func(o orderFact) string { return strconv.Itoa(o.Weekday) }, days),
			Hours:    cut(orders, cur, func(o orderFact) string { return strconv.Itoa(o.Hour) }, hours),
			Regions: cut(orders, cur, func(o orderFact) string {
				if o.Region == "" {
					return noRegion
				}
				return o.Region
			}, nil),
			Campaigns: cut(orders, cur, func(o orderFact) string { return campaignOf(o, touch) }, nil),
		}
		// Spend by campaign name, in this currency; a campaign that spent and
		// sold nothing is a line of its own, as in the report.
		spent := map[string]int64{}
		var names []string
		for _, s := range spend {
			if s.Currency != cur {
				continue
			}
			k := strings.ToLower(s.channel.Campaign)
			if _, ok := spent[k]; !ok {
				names = append(names, s.channel.Campaign)
			}
			spent[k] += s.Amount
		}
		seen := map[string]bool{}
		for i := range b.Campaigns {
			c := &b.Campaigns[i]
			k := strings.ToLower(c.Key)
			seen[k] = true
			if amount, ok := spent[k]; ok {
				withSpend(c, amount, cur)
			}
		}
		for _, name := range names {
			if k := strings.ToLower(name); !seen[k] {
				seen[k] = true
				c := Slice{Key: name, Net: gocommerce.Money{Currency: cur}, GrossProfit: gocommerce.Money{Currency: cur}}
				withSpend(&c, spent[k], cur)
				b.Campaigns = append(b.Campaigns, c)
			}
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}

func withSpend(c *Slice, amount int64, currency string) {
	c.Spend = &gocommerce.Money{AmountMinor: amount, Currency: currency}
	c.ROAS, c.POAS = ratio(c.revenue, amount), ratio(c.GrossProfit.AmountMinor, amount)
}

func (m *Module) breakdown(ctx context.Context, w window) (*Breakdown, error) {
	orders, err := m.facts(ctx, w)
	if err != nil {
		return nil, err
	}
	spend, err := m.spendFacts(ctx, w)
	if err != nil {
		return nil, err
	}
	return &Breakdown{From: w.from, To: w.to, TimeZone: w.tz, Touch: w.touch, Currencies: breakdown(orders, spend, w.touch)}, nil
}

func (m *Module) handleBreakdown(rw http.ResponseWriter, r *http.Request) {
	w, err := m.parseWindow(r.Context(), query(r))
	if err != nil {
		gocommerce.RespondError(rw, r, err)
		return
	}
	b, err := m.breakdown(r.Context(), w)
	if err != nil {
		gocommerce.RespondError(rw, r, err)
		return
	}
	gocommerce.Respond(rw, http.StatusOK, b)
}

// Pace is one source's spend this month against the budget typed in for it.
type Pace struct {
	Source string `json:"source"`
	// Budget is null for a source with spend and no budget: there is nothing
	// to pace against, and the spend is still shown.
	Budget *gocommerce.Money `json:"budget"`
	Spent  gocommerce.Money  `json:"spent"`
	// Expected is the budget's share for the days gone, Projected the month's
	// spend at the rate so far. Both in whole minor units, rounded half up.
	Expected  *gocommerce.Money `json:"expected"`
	Projected gocommerce.Money  `json:"projected"`
	// Status is over (projected past the budget), under (spent less than
	// three quarters of the expected share) or on_pace; none without a budget.
	Status string `json:"status"`
}

// Pacing is this month's spend, to today, in the report's zone.
type Pacing struct {
	Month    string `json:"month"`
	Day      int    `json:"day"`
	Days     int    `json:"days"`
	TimeZone string `json:"time_zone"`
	Sources  []Pace `json:"sources"`
}

// pace works out one source's line. A month's spend reaches the engine a day
// or more late when it is imported, so a source with yesterday not yet in
// reads under: the screen says so.
func pace(source string, budget *gocommerce.Money, spent gocommerce.Money, day, days int) Pace {
	p := Pace{Source: source, Budget: budget, Spent: spent, Status: "none",
		Projected: gocommerce.Money{AmountMinor: spent.AmountMinor * int64(days) / int64(day), Currency: spent.Currency}}
	if budget == nil {
		return p
	}
	exp := (budget.AmountMinor*int64(day) + int64(days)/2) / int64(days)
	p.Expected = &gocommerce.Money{AmountMinor: exp, Currency: budget.Currency}
	switch {
	case p.Projected.AmountMinor > budget.AmountMinor:
		p.Status = "over"
	case spent.AmountMinor*4 < exp*3:
		p.Status = "under"
	default:
		p.Status = "on_pace"
	}
	return p
}

func (m *Module) pacing(ctx context.Context, tz string, now time.Time) (*Pacing, error) {
	if tz == "" {
		tz = "UTC"
	}
	var today time.Time
	if err := m.app.DB().QueryRowContext(ctx, `SELECT ($1::timestamptz AT TIME ZONE $2)::date`, now, tz).Scan(&today); err != nil {
		return nil, gocommerce.Validationf("%q is not a time zone this database knows", tz)
	}
	first := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	days := first.AddDate(0, 1, -1).Day()
	p := &Pacing{Month: first.Format("2006-01"), Day: today.Day(), Days: days, TimeZone: tz, Sources: []Pace{}}

	budgets := map[string]gocommerce.Money{}
	var order []string
	rows, err := m.app.DB().QueryContext(ctx, `SELECT source, currency, amount_minor FROM ads_budgets ORDER BY source`)
	if err != nil {
		return nil, gocommerce.Internalf(err, "read the ad budgets")
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		var b gocommerce.Money
		if err := rows.Scan(&s, &b.Currency, &b.AmountMinor); err != nil {
			return nil, gocommerce.Internalf(err, "read the ad budgets")
		}
		budgets[s] = b
		order = append(order, s)
	}
	if err := rows.Err(); err != nil {
		return nil, gocommerce.Internalf(err, "read the ad budgets")
	}
	type key struct{ source, currency string }
	spent := map[key]int64{}
	var spentOrder []key
	srows, err := m.app.DB().QueryContext(ctx, `
		SELECT source, currency, sum(amount_minor) FROM ads_spend
		 WHERE day BETWEEN $1 AND $2 GROUP BY source, currency ORDER BY source, currency`,
		first.Format(time.DateOnly), today.Format(time.DateOnly))
	if err != nil {
		return nil, gocommerce.Internalf(err, "read the ad spend")
	}
	defer srows.Close()
	for srows.Next() {
		var k key
		var v int64
		if err := srows.Scan(&k.source, &k.currency, &v); err != nil {
			return nil, gocommerce.Internalf(err, "read the ad spend")
		}
		spent[k] = v
		spentOrder = append(spentOrder, k)
	}
	if err := srows.Err(); err != nil {
		return nil, gocommerce.Internalf(err, "read the ad spend")
	}
	for _, s := range order {
		b := budgets[s]
		k := key{s, b.Currency}
		p.Sources = append(p.Sources, pace(s, &b, gocommerce.Money{AmountMinor: spent[k], Currency: b.Currency}, p.Day, days))
		delete(spent, k)
	}
	// Spend with no budget, or in another currency than its budget, is still shown.
	for _, k := range spentOrder {
		if v, ok := spent[k]; ok {
			p.Sources = append(p.Sources, pace(k.source, nil, gocommerce.Money{AmountMinor: v, Currency: k.currency}, p.Day, days))
		}
	}
	return p, nil
}

func (m *Module) handlePacing(w http.ResponseWriter, r *http.Request) {
	p, err := m.pacing(r.Context(), query(r)["tz"], time.Now())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, p)
}

// BudgetInput is a source's monthly budget. An amount of zero removes it.
type BudgetInput struct {
	Source      string `json:"source"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

func (m *Module) handlePutBudget(w http.ResponseWriter, r *http.Request) {
	var in BudgetInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	source, _, err := channelOf(in.Source, "")
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if in.AmountMinor < 0 {
		gocommerce.RespondError(w, r, gocommerce.Validationf("amount_minor is not negative"))
		return
	}
	if in.AmountMinor == 0 {
		if _, err := m.app.DB().ExecContext(ctx, `DELETE FROM ads_budgets WHERE source = $1`, source); err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "remove the budget"))
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if in.Currency == "" {
		in.Currency = m.app.Currency(ctx)
	}
	if !currencyRE.MatchString(in.Currency) {
		gocommerce.RespondError(w, r, gocommerce.Validationf("currency is a three-letter code such as INR"))
		return
	}
	if _, err := m.app.DB().ExecContext(ctx, `
		INSERT INTO ads_budgets (source, currency, amount_minor) VALUES ($1, $2, $3)
		ON CONFLICT (store_id, source) DO UPDATE SET currency = EXCLUDED.currency,
		    amount_minor = EXCLUDED.amount_minor, updated_at = now()`, source, in.Currency, in.AmountMinor); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "save the budget"))
		return
	}
	gocommerce.Respond(w, http.StatusOK, gocommerce.Money{AmountMinor: in.AmountMinor, Currency: in.Currency})
}
