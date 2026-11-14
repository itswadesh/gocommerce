package ads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	gocommerce "github.com/misiki-in/gocommerce/core"
	aigateway "github.com/misiki-in/gocommerce/ext/ai-gateway"
)

const adviseTaskName = "ads.advise"

// Finding is one thing the advisor says is wrong, the figure it rests on, and
// what the owner could do about it. Nothing is ever done for them (D266).
type Finding struct {
	// Kind is no_orders, loss, quiet_hours, losing_region or attribution.
	Kind   string `json:"kind"`
	Text   string `json:"text"`
	Figure string `json:"figure"`
	Action string `json:"action"`
}

// Advice is the advisor's reading of one window. Source is "rules" when the
// findings are the engine's own, "ai" when a model wrote them from those; a
// note says why the model was not used when it was asked for.
type Advice struct {
	From        string    `json:"from"`
	To          string    `json:"to"`
	Touch       string    `json:"touch"`
	Source      string    `json:"source"`
	Note        string    `json:"note,omitempty"`
	Findings    []Finding `json:"findings"`
	ExecutionID int64     `json:"execution_id,omitempty"`
}

// minOrdersForHours is how many orders a window needs before an hour with
// none says anything: with a handful, most hours are empty by chance.
const minOrdersForHours = 30

func money(v int64, currency string) string { return gocommerce.FormatMoney(v, currency) }

// rules finds, in figures the report already holds, what wastes money:
// campaigns that spend and sell nothing or lose money after product cost,
// hours that never sell, states whose orders lose money, and orders that
// cannot be traced to where they came from.
func rules(rep *Report, cuts []*CurrencyBreakdown) []Finding {
	out := []Finding{}
	for _, c := range rep.Currencies {
		for _, r := range c.Rows {
			if r.Spend == nil || r.Spend.AmountMinor == 0 {
				continue
			}
			name := r.Source + " / " + r.Campaign
			if r.Campaign == "" {
				name = r.Source + " (no campaign)"
			}
			spend := money(r.Spend.AmountMinor, c.Currency)
			switch {
			case r.Orders == 0:
				out = append(out, Finding{Kind: "no_orders",
					Text:   fmt.Sprintf("%s spent %s and brought no orders.", name, spend),
					Figure: fmt.Sprintf("Spend %s · 0 orders", spend),
					Action: fmt.Sprintf("Pause it, or check its links carry utm_source=%s and utm_campaign=%s so its orders are counted.", r.Source, r.Campaign)})
			case r.GrossProfit.AmountMinor < r.Spend.AmountMinor:
				out = append(out, Finding{Kind: "loss",
					Text: fmt.Sprintf("%s earned %s of gross profit on %s of spend: it loses money after product cost.",
						name, money(r.GrossProfit.AmountMinor, c.Currency), spend),
					Figure: fmt.Sprintf("Spend %s · gross profit %s · %d orders", spend, money(r.GrossProfit.AmountMinor, c.Currency), r.Orders),
					Action: "Lower its budget or bids, or narrow it to what sells, unless its customers come back to buy again."})
			}
		}
		// Orders the report cannot place: no attribution at all, or an ad
		// click with no campaign, which no spend can meet.
		unknown, untagged := 0, 0
		for _, r := range c.Rows {
			switch {
			case r.Source == notRecorded:
				unknown += r.Orders
			case r.Medium == "cpc" && r.Campaign == "":
				untagged += r.Orders
			}
		}
		if total := c.Totals.Orders; unknown > 0 && total > 0 {
			out = append(out, Finding{Kind: "attribution",
				Text:   fmt.Sprintf("%d of %d orders have no record of where the shopper came from.", unknown, total),
				Figure: fmt.Sprintf("%d of %d orders not recorded", unknown, total),
				Action: "Check the storefront records arrivals (it does from the connector's checkout); orders placed by staff or another app have none."})
		}
		if untagged > 0 {
			out = append(out, Finding{Kind: "attribution",
				Text:   fmt.Sprintf("%d orders came from ad clicks with no campaign name, so no spend can be matched to them.", untagged),
				Figure: fmt.Sprintf("%d orders under (no campaign)", untagged),
				Action: "Add utm_source, utm_medium and utm_campaign to the ads' final URL suffix (in Google Ads: utm_campaign={campaignname})."})
		}
	}
	for _, b := range cuts {
		total := 0
		for _, h := range b.Hours {
			total += h.Orders
		}
		if total >= minOrdersForHours {
			if spans := quietSpans(b.Hours); spans != "" {
				out = append(out, Finding{Kind: "quiet_hours",
					Text:   fmt.Sprintf("No orders were placed %s in this period.", spans),
					Figure: fmt.Sprintf("0 of %d orders %s", total, spans),
					Action: "If ads run at these hours, schedule them off or bid them down (ad schedule in Google Ads, dayparting in Meta)."})
			}
		}
		for _, r := range b.Regions {
			if r.Orders > 0 && r.GrossProfit.AmountMinor < 0 {
				out = append(out, Finding{Kind: "losing_region",
					Text:   fmt.Sprintf("Orders shipped to %s lost %s after product cost and refunds.", r.Key, money(-r.GrossProfit.AmountMinor, b.Currency)),
					Figure: fmt.Sprintf("%s · %d orders · gross profit %s", r.Key, r.Orders, money(r.GrossProfit.AmountMinor, b.Currency)),
					Action: "Check its returns and shipping charge; lower bids for it in the ads' location targeting if it keeps losing."})
			}
		}
	}
	return out
}

// quietSpans names runs of three or more hours in a row with no orders, such
// as "from 01:00 to 06:00", wrapping past midnight.
func quietSpans(hours []Slice) string {
	if len(hours) != 24 {
		return ""
	}
	empty := func(h int) bool { return hours[(h+24)%24].Orders == 0 }
	start := 0
	for start < 24 && empty(start) {
		start++
	}
	if start == 24 {
		return "" // no orders at all is said by the report itself
	}
	var spans []string
	for i := 1; i <= 24; i++ {
		h := (start + i) % 24
		if !empty(h) {
			continue
		}
		n := 0
		for n < 24 && empty(h+n) {
			n++
		}
		if n >= 3 {
			spans = append(spans, fmt.Sprintf("from %02d:00 to %02d:00", h, (h+n)%24))
		}
		i += n
	}
	return strings.Join(spans, " and ")
}

type adviceInput struct {
	From     string    `json:"from"`
	To       string    `json:"to"`
	Touch    string    `json:"touch"`
	Findings []Finding `json:"findings"`
	Figures  []figure  `json:"figures"`
}

type adviceOutput struct {
	Findings []Finding `json:"findings"`
}

var adviceSchema = json.RawMessage(`{
  "type": "object", "additionalProperties": false, "required": ["findings"],
  "properties": {
    "findings": {"type": "array", "items": {
      "type": "object", "additionalProperties": false, "required": ["kind", "text", "figure", "action"],
      "properties": {
        "kind": {"type": "string", "enum": ["no_orders", "loss", "quiet_hours", "losing_region", "attribution"]},
        "text": {"type": "string"},
        "figure": {"type": "string"},
        "action": {"type": "string"}
      }
    }}
  }
}`)

const adviceSystem = `You review a shop's advertising for its owner and say, in plain words, where money is wasted and what to do about it. You never change anything; the owner decides.

You are given findings the shop's software already worked out from its ads report, and the report's rows they come from. Rewrite them for the owner, most money at stake first, at most 8.

- kind: the finding's kind, as given.
- text: one or two sentences, at most 300 characters, naming the campaign, hour or state.
- figure: the figures the finding rests on, copied from the input.
- action: one suggested step the owner can take in the ad platform or the shop, at most 200 characters.

Use only numbers that appear in the input, copied exactly as written there. Never calculate, total, round, compare as a percentage or estimate a new number. Do not add a finding the input does not support.`

func adviseTask() aigateway.Task {
	return aigateway.Task{
		Name:            adviseTaskName,
		Version:         1,
		System:          adviceSystem,
		Schema:          adviceSchema,
		MaxInputTokens:  20000,
		MaxOutputTokens: 4096,
		Ladder:          []aigateway.Tier{aigateway.TierSmall, aigateway.TierStrong},
		Build: func(_ context.Context, raw json.RawMessage, _ aigateway.Policy) (aigateway.Request, []aigateway.SourceRef, error) {
			var in adviceInput
			if err := json.Unmarshal(raw, &in); err != nil {
				return aigateway.Request{}, nil, err
			}
			return aigateway.Request{Messages: []aigateway.Message{{
				Role: "user",
				Text: fmt.Sprintf("Review the shop's ads from %s to %s, orders credited to their %s visit.", in.From, in.To, in.Touch),
				Data: []aigateway.Data{
					{Class: aigateway.DataOrderLines, Label: "findings", Value: in.Findings, Required: true},
					{Class: aigateway.DataOrderLines, Label: "figures", Value: in.Figures},
				},
			}}}, []aigateway.SourceRef{{Kind: "ads_report", ID: in.From + "/" + in.To}}, nil
		},
		Check: checkAdvice,
	}
}

// checkAdvice holds the model to the input's numbers, as the summary is held.
func checkAdvice(ctx context.Context, raw json.RawMessage) (int, error) {
	var o adviceOutput
	if err := json.Unmarshal(raw, &o); err != nil {
		return 0, err
	}
	if len(o.Findings) > 8 {
		return 0, fmt.Errorf("%d findings, and at most 8", len(o.Findings))
	}
	var texts []string
	for _, f := range o.Findings {
		for _, t := range []struct {
			s   string
			max int
		}{{f.Text, 400}, {f.Figure, 300}, {f.Action, 300}} {
			if n := len([]rune(strings.TrimSpace(t.s))); n == 0 || n > t.max {
				return 0, fmt.Errorf("a finding's field is %d characters, and must be 1 to %d", n, t.max)
			}
			texts = append(texts, t.s)
		}
	}
	return checkNumbers(ctx, texts)
}

// advise reads the window's report and returns the rule findings; with ai,
// a model rewrites them, and a refusal falls back to the rules with the reason.
func (m *Module) advise(ctx context.Context, w window, ai bool) (*Advice, error) {
	rep, orders, err := m.report(ctx, w)
	if err != nil {
		return nil, err
	}
	spend, err := m.spendFacts(ctx, w)
	if err != nil {
		return nil, err
	}
	a := &Advice{From: w.from, To: w.to, Touch: w.touch, Source: "rules",
		Findings: rules(rep, breakdown(orders, spend, w.touch))}
	if !ai || len(a.Findings) == 0 {
		return a, nil
	}
	if m.cfg.Gateway == nil {
		a.Note = "This store was built without the AI gateway, so these are the engine's own findings."
		return a, nil
	}
	if err := m.cfg.Gateway.Ready(ctx); err != nil {
		a.Note = "AI is not set up (" + refusalText(err) + "), so these are the engine's own findings."
		return a, nil
	}
	figs, err := m.figures(ctx, rep, orders)
	if err != nil {
		return nil, err
	}
	in := adviceInput{From: w.from, To: w.to, Touch: w.touch, Findings: a.Findings, Figures: figs}
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(allowFrom(ctx, in, len(figs))), 4*time.Minute)
	defer cancel()
	res, err := m.cfg.Gateway.Run(runCtx, adviseTaskName, in)
	if err != nil {
		a.Note = "The AI review was not made (" + refusalText(err) + "), so these are the engine's own findings."
		return a, nil
	}
	var out adviceOutput
	if err := json.Unmarshal(res.Output, &out); err != nil || len(out.Findings) == 0 {
		a.Note = "The AI review came back empty, so these are the engine's own findings."
		return a, nil
	}
	a.Source, a.Findings, a.ExecutionID = "ai", out.Findings, res.ExecutionID
	return a, nil
}

func refusalText(err error) string {
	var api *gocommerce.APIError
	if errors.As(refusal(err), &api) {
		return api.Message
	}
	return err.Error()
}

func (m *Module) handleAdvice(rw http.ResponseWriter, r *http.Request) {
	w, err := m.parseWindow(r.Context(), query(r))
	if err != nil {
		gocommerce.RespondError(rw, r, err)
		return
	}
	ai := r.Method == http.MethodPost
	if ai {
		if err := http.NewResponseController(rw).SetWriteDeadline(time.Now().Add(4 * time.Minute)); err != nil {
			m.app.Log().Warn("could not extend the write deadline for ads advice", "error", err)
		}
	}
	a, err := m.advise(r.Context(), w, ai)
	if err != nil {
		gocommerce.RespondError(rw, r, err)
		return
	}
	gocommerce.Respond(rw, http.StatusOK, a)
}
