package ads

// What the approved changes (D268) read from and write to the ad platforms.
// Like the spend connectors, none of it has been run against a real ad
// account: it is written to the platforms' documented APIs and tested against
// stub servers. Reads happen only with the platform's plugin on and filled in;
// a write happens only when somebody approves a change, or the autopilot
// (D269) applies one inside its limits.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	gocommerce "github.com/misiki-in/gocommerce/core"
)

// adEntity is a campaign, or a Meta ad set, as the platform has it now.
type adEntity struct {
	Kind       string // campaign or ad_set
	ID         string
	CampaignID string // the ad set's campaign; the campaign's own ID
	Name       string
	// Budget is a Google campaign's budget resource; Shared is a budget other
	// campaigns draw on too, which changing would change for them as well.
	Budget string
	Shared bool
	// Daily is the daily budget in minor units, zero when it has none of its
	// own (a Meta ad set under a campaign budget, a lifetime budget).
	Daily int64
}

// termCost is a search term or placement that cost money and, by the
// platform's own count, converted nothing.
type termCost struct {
	CampaignID string
	Text       string // the search term, or the placement's URL or ID
	Type       string // a placement's type: WEBSITE, YOUTUBE_VIDEO, YOUTUBE_CHANNEL
	Clicks     int64
	Cost       int64 // minor units
}

// account is what a proposal run reads from one platform.
type account struct {
	Currency, TimeZone string
	Entities           []adEntity
	Terms, Placements  []termCost
	// Scheduled are the Google campaigns that already have an ad schedule.
	Scheduled map[string]bool
}

// num reads one of Google's int64-as-string fields, or Meta's numeric strings.
func num(s string) int64 { v, _ := strconv.ParseInt(s, 10, 64); return v }

func (m *Module) googleAccount(ctx context.Context, c googleConfig, from, to string) (*account, error) {
	tok, err := m.googleToken(ctx, c)
	if err != nil {
		return nil, err
	}
	a := &account{Scheduled: map[string]bool{}}
	err = m.googleSearch(ctx, c, tok, "SELECT customer.currency_code, customer.time_zone, campaign.id, campaign.name, "+
		"campaign_budget.resource_name, campaign_budget.amount_micros, campaign_budget.explicitly_shared "+
		"FROM campaign WHERE campaign.status = 'ENABLED'", func(raw json.RawMessage) error {
		var r struct {
			Customer       struct{ CurrencyCode, TimeZone string }
			Campaign       struct{ ID, Name string }
			CampaignBudget struct {
				ResourceName     string
				AmountMicros     string
				ExplicitlyShared bool
			}
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		a.Currency, a.TimeZone = strings.ToUpper(r.Customer.CurrencyCode), r.Customer.TimeZone
		a.Entities = append(a.Entities, adEntity{Kind: "campaign", ID: r.Campaign.ID, CampaignID: r.Campaign.ID, Name: r.Campaign.Name,
			Budget: r.CampaignBudget.ResourceName, Shared: r.CampaignBudget.ExplicitlyShared,
			Daily: micros(num(r.CampaignBudget.AmountMicros), gocommerce.CurrencyExponent(a.Currency))})
		return nil
	})
	if err != nil || len(a.Entities) == 0 {
		return a, err
	}
	exp := gocommerce.CurrencyExponent(a.Currency)
	costRow := func(dst *[]termCost, pick func(map[string]json.RawMessage) (text, typ string)) func(json.RawMessage) error {
		return func(raw json.RawMessage) error {
			var r map[string]json.RawMessage
			var row struct {
				Campaign struct{ ID string }
				Metrics  struct{ Clicks, CostMicros string }
			}
			if err := json.Unmarshal(raw, &r); err != nil {
				return err
			}
			_ = json.Unmarshal(raw, &row)
			text, typ := pick(r)
			if text != "" {
				*dst = append(*dst, termCost{CampaignID: row.Campaign.ID, Text: text, Type: typ,
					Clicks: num(row.Metrics.Clicks), Cost: micros(num(row.Metrics.CostMicros), exp)})
			}
			return nil
		}
	}
	window := fmt.Sprintf("segments.date BETWEEN '%s' AND '%s' AND metrics.cost_micros > 0 AND metrics.conversions = 0", from, to)
	err = m.googleSearch(ctx, c, tok, "SELECT campaign.id, search_term_view.search_term, metrics.clicks, metrics.cost_micros "+
		"FROM search_term_view WHERE "+window+" AND search_term_view.status NOT IN ('EXCLUDED', 'ADDED_EXCLUDED')",
		costRow(&a.Terms, func(r map[string]json.RawMessage) (string, string) {
			var v struct{ SearchTerm string }
			_ = json.Unmarshal(r["searchTermView"], &v)
			return v.SearchTerm, ""
		}))
	if err == nil {
		err = m.googleSearch(ctx, c, tok, "SELECT campaign.id, detail_placement_view.placement, detail_placement_view.placement_type, "+
			"metrics.clicks, metrics.cost_micros FROM detail_placement_view WHERE "+window,
			costRow(&a.Placements, func(r map[string]json.RawMessage) (string, string) {
				var v struct{ Placement, PlacementType string }
				_ = json.Unmarshal(r["detailPlacementView"], &v)
				switch v.PlacementType {
				case "WEBSITE", "YOUTUBE_VIDEO", "YOUTUBE_CHANNEL":
					return v.Placement, v.PlacementType
				}
				return "", "" // a mobile app's placement IDs are not excluded by URL
			}))
	}
	if err == nil {
		err = m.googleSearch(ctx, c, tok, "SELECT campaign.id FROM campaign_criterion WHERE campaign_criterion.type = 'AD_SCHEDULE'",
			func(raw json.RawMessage) error {
				var r struct{ Campaign struct{ ID string } }
				_ = json.Unmarshal(raw, &r)
				a.Scheduled[r.Campaign.ID] = true
				return nil
			})
	}
	return a, err
}

// metaGet reads one Graph edge, following its pages.
func (m *Module) metaGet(ctx context.Context, c metaConfig, path string, q url.Values, each func(json.RawMessage) error) error {
	next := fmt.Sprintf("https://graph.facebook.com/%s/%s?%s", c.APIVersion, path, metaAuth(c, q).Encode())
	for range 100 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return err
		}
		var out struct {
			Data   []json.RawMessage `json:"data"`
			Paging struct {
				Next string `json:"next"`
			} `json:"paging"`
		}
		if err := m.getJSON(req, &out); err != nil {
			return fmt.Errorf("read Meta: %w", err)
		}
		for _, d := range out.Data {
			if err := each(d); err != nil {
				return err
			}
		}
		if next = out.Paging.Next; next == "" || !strings.HasPrefix(next, "https://graph.facebook.com/") {
			return nil
		}
	}
	return errors.New("read Meta: more than 100 pages")
}

func (m *Module) metaAccount(ctx context.Context, c metaConfig) (*account, error) {
	act := "act_" + digits(c.AdAccountID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://graph.facebook.com/%s/%s?%s",
		c.APIVersion, act, metaAuth(c, url.Values{"fields": {"currency,timezone_name"}}).Encode()), nil)
	if err != nil {
		return nil, err
	}
	var info struct {
		Currency     string `json:"currency"`
		TimezoneName string `json:"timezone_name"`
	}
	if err := m.getJSON(req, &info); err != nil {
		return nil, fmt.Errorf("read Meta: %w", err)
	}
	a := &account{Currency: strings.ToUpper(info.Currency), TimeZone: info.TimezoneName}
	active := url.Values{"effective_status": {`["ACTIVE"]`}, "limit": {"500"}}
	for _, edge := range []struct{ path, kind, fields string }{
		{act + "/campaigns", "campaign", "id,name,daily_budget"},
		{act + "/adsets", "ad_set", "id,name,campaign_id,daily_budget"},
	} {
		q := url.Values{"fields": {edge.fields}}
		for k, v := range active {
			q[k] = v
		}
		err := m.metaGet(ctx, c, edge.path, q, func(raw json.RawMessage) error {
			var r struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				CampaignID  string `json:"campaign_id"`
				DailyBudget string `json:"daily_budget"`
			}
			if err := json.Unmarshal(raw, &r); err != nil {
				return err
			}
			e := adEntity{Kind: edge.kind, ID: r.ID, CampaignID: r.CampaignID, Name: r.Name, Daily: num(r.DailyBudget)}
			if e.Kind == "campaign" {
				e.CampaignID = e.ID
			}
			a.Entities = append(a.Entities, e)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return a, nil
}

// minorToMicros is a budget in minor units as Google's micros.
func minorToMicros(v int64, currency string) int64 {
	for range 6 - gocommerce.CurrencyExponent(currency) {
		v *= 10
	}
	return v
}

// googleOps are the mutate operations that make the change, or with undo,
// take it back.
func googleOps(c googleConfig, ch *Change, d *changeDetail, undo bool) []map[string]any {
	campaign := fmt.Sprintf("customers/%s/campaigns/%s", digits(c.CustomerID), ch.CampaignID)
	if undo && len(d.Created) > 0 {
		var ops []map[string]any
		for _, rn := range d.Created {
			ops = append(ops, map[string]any{"campaignCriterionOperation": map[string]any{"remove": rn}})
		}
		return ops
	}
	switch ch.Action {
	case ActionPause:
		status := "PAUSED"
		if undo {
			status = "ENABLED"
		}
		return []map[string]any{{"campaignOperation": map[string]any{
			"update": map[string]any{"resourceName": campaign, "status": status}, "updateMask": "status"}}}
	case ActionBudget:
		amount := ch.BudgetAfter.AmountMinor
		if undo {
			amount = ch.BudgetBefore.AmountMinor
		}
		return []map[string]any{{"campaignBudgetOperation": map[string]any{
			"update":     map[string]any{"resourceName": d.Budget, "amountMicros": strconv.FormatInt(minorToMicros(amount, ch.BudgetAfter.Currency), 10)},
			"updateMask": "amount_micros"}}}
	}
	var ops []map[string]any
	create := func(criterion map[string]any) {
		criterion["campaign"] = campaign
		ops = append(ops, map[string]any{"campaignCriterionOperation": map[string]any{"create": criterion}})
	}
	switch ch.Action {
	case ActionNegativeKeywords:
		for _, t := range ch.Items {
			create(map[string]any{"negative": true, "keyword": map[string]any{"text": t, "matchType": "EXACT"}})
		}
	case ActionExcludePlacements:
		for _, p := range d.Placements {
			switch p.Type {
			case "WEBSITE":
				create(map[string]any{"negative": true, "placement": map[string]any{"url": p.Text}})
			case "YOUTUBE_VIDEO":
				create(map[string]any{"negative": true, "youtubeVideo": map[string]any{"videoId": p.Text}})
			case "YOUTUBE_CHANNEL":
				create(map[string]any{"negative": true, "youtubeChannel": map[string]any{"channelId": p.Text}})
			}
		}
	case ActionAdSchedule:
		for _, day := range []string{"MONDAY", "TUESDAY", "WEDNESDAY", "THURSDAY", "FRIDAY", "SATURDAY", "SUNDAY"} {
			for _, h := range d.Hours {
				create(map[string]any{"adSchedule": map[string]any{"dayOfWeek": day,
					"startHour": h[0], "startMinute": "ZERO", "endHour": h[1], "endMinute": "ZERO"}})
			}
		}
	}
	return ops
}

// clip2000 keeps a record's request or response short enough to show.
func clip2000(s string) string {
	if len(s) > 2000 {
		return s[:2000] + "…"
	}
	return s
}

// send makes the change, or takes it back, on the platform. It returns what
// was asked, in a form fit to keep (no tokens), and what the platform said.
// A Google criterion it creates is remembered in d, so it can be removed.
func (m *Module) send(ctx context.Context, ch *Change, d *changeDetail, undo bool) (request, response string, err error) {
	switch ch.Platform {
	case "google":
		c, ok := m.googleConfig(ctx)
		if !ok {
			return "", "", errNotConnected("Google Ads")
		}
		body, _ := json.Marshal(map[string]any{"mutateOperations": googleOps(c, ch, d, undo)})
		request = "POST googleAds:mutate " + string(body)
		tok, err := m.googleToken(ctx, c)
		if err != nil {
			return request, "", err
		}
		req, err := googleRequest(ctx, c, tok, "googleAds:mutate", body)
		if err != nil {
			return request, "", err
		}
		res, err := m.call(req)
		if err != nil {
			return request, string(res), err
		}
		if !undo {
			var out struct {
				MutateOperationResponses []struct {
					CampaignCriterionResult struct{ ResourceName string }
				}
			}
			_ = json.Unmarshal(res, &out)
			d.Created = nil
			for _, r := range out.MutateOperationResponses {
				if rn := r.CampaignCriterionResult.ResourceName; rn != "" {
					d.Created = append(d.Created, rn)
				}
			}
		}
		return request, string(res), nil
	case "meta":
		c, ok := m.metaConfig(ctx)
		if !ok {
			return "", "", errNotConnected("Meta")
		}
		form := url.Values{}
		switch ch.Action {
		case ActionPause:
			form.Set("status", map[bool]string{false: "PAUSED", true: "ACTIVE"}[undo])
		case ActionBudget:
			amount := ch.BudgetAfter.AmountMinor
			if undo {
				amount = ch.BudgetBefore.AmountMinor
			}
			form.Set("daily_budget", strconv.FormatInt(amount, 10))
		default:
			return "", "", gocommerce.Validationf("Meta has no %s change", ch.Action)
		}
		request = "POST " + ch.TargetID + " " + form.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			fmt.Sprintf("https://graph.facebook.com/%s/%s", c.APIVersion, ch.TargetID), strings.NewReader(metaAuth(c, form).Encode()))
		if err != nil {
			return request, "", err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := m.call(req)
		return request, string(res), err
	}
	return "", "", fmt.Errorf("no platform is called %q", ch.Platform)
}

func errNotConnected(title string) error {
	return &gocommerce.APIError{Status: http.StatusConflict, Code: "not_connected",
		Message: title + " is not connected: switch on its plugin and fill in its settings"}
}
