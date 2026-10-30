package ads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	aigateway "github.com/itswadesh/gocommerce/ext/ai-gateway"
)

const summaryTaskName = "ads.summarise"

// maxFigures is how many report rows the model is shown: the ones with the
// most spend, then the most sales.
const maxFigures = 30

// figure is one report row as the model reads it. Amounts are written out
// with their currency, because the model copies them into sentences and a
// minor-unit integer would be copied wrong.
type figure struct {
	Row           int       `json:"row"`
	Source        string    `json:"source"`
	Medium        string    `json:"medium"`
	Campaign      string    `json:"campaign"`
	Orders        int       `json:"orders"`
	Revenue       string    `json:"revenue"`
	Refunded      string    `json:"refunded"`
	Net           string    `json:"net"`
	GrossProfit   string    `json:"gross_profit"`
	UncostedUnits int       `json:"units_without_cost"`
	Returns       int       `json:"orders_returned"`
	NewCustomers  int       `json:"new_customers"`
	Spend         string    `json:"spend"`
	ROAS          string    `json:"roas"`
	POAS          string    `json:"profit_per_spend"`
	CostPerOrder  string    `json:"cost_per_order"`
	Products      []product `json:"products,omitempty"`
	currency      string
}

type product struct {
	Title      string `json:"title"`
	Units      int    `json:"units"`
	OutOfStock bool   `json:"out_of_stock"`
}

type summaryInput struct {
	From    string   `json:"from"`
	To      string   `json:"to"`
	Touch   string   `json:"touch"`
	Figures []figure `json:"figures"`
}

type point struct {
	Row  int    `json:"row"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type summaryOutput struct {
	Headline string  `json:"headline"`
	Points   []point `json:"points"`
}

// Summary is a summary as the panel shows it: the words and, beside them,
// the figures they were written from.
type Summary struct {
	ID          int64     `json:"id"`
	Kind        string    `json:"kind"`
	From        string    `json:"from"`
	To          string    `json:"to"`
	Touch       string    `json:"touch"`
	Headline    string    `json:"headline"`
	Points      []point   `json:"points"`
	Figures     []figure  `json:"figures"`
	ExecutionID int64     `json:"execution_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

var summarySchema = json.RawMessage(`{
  "type": "object", "additionalProperties": false, "required": ["headline", "points"],
  "properties": {
    "headline": {"type": "string"},
    "points": {"type": "array", "items": {
      "type": "object", "additionalProperties": false, "required": ["row", "kind", "text"],
      "properties": {
        "row": {"type": "integer", "description": "The figure row the point is about."},
        "kind": {"type": "string", "enum": ["working", "wasting", "watch"]},
        "text": {"type": "string"}
      }
    }}
  }
}`)

const summarySystem = `You summarise a shop's advertising for its owner in plain words: what is working and what is wasting money.

You are given the figures of the shop's ads report, one numbered row per source, medium and campaign. Each row has the orders it brought, sales, refunds, net sales, gross profit after product cost, units sold with no cost recorded, orders returned, new customers, ad spend, ROAS (sales per unit of spend), profit per unit of spend, cost per order, and the products sold with whether each is now out of stock. A spend of "none" means none was entered, not that the ads were free.

- headline: one sentence, at most 160 characters.
- points: up to 8, each about one row, by its number. kind is "working" for a row that earns more than it costs, "wasting" for spend that brings little or no profit, "watch" for anything the owner should check, such as a product out of stock that a campaign sells, or a profit that leaves out units with no cost. text is one or two sentences, at most 300 characters, naming the campaign.

Use only numbers that appear in the figures, copied exactly as written there. Never calculate, total, round, compare as a percentage or estimate a new number. When a row has units without a cost, say its profit leaves them out. Do not give advice beyond what the figures show.`

type allowedKey struct{}

var numberRE = regexp.MustCompile(`\d[\d,]*(?:\.\d+)?`)

// madeRE finds a number given as a share or a multiple, which the figures
// state only as ROAS and profit per spend, never as a percentage.
var madeRE = regexp.MustCompile(`(\d[\d,]*(?:\.\d+)?)\s*(%|x\b|×|times\b)`)

// canonical writes a number the way the check compares it: no grouping, no
// trailing zeros after the point.
func canonical(s string) string {
	s = strings.ReplaceAll(s, ",", "")
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s = strings.TrimLeft(s, "0"); s == "" || s[0] == '.' {
		s = "0" + s
	}
	return s
}

// withAllowed puts on ctx the numbers a summary of in may use: every number
// in its figures, the dates of its window, and counts up to its row count.
func withAllowed(ctx context.Context, in summaryInput) context.Context {
	allowed := map[string]bool{}
	raw, _ := json.Marshal(in)
	for _, n := range numberRE.FindAllString(string(raw), -1) {
		allowed[canonical(n)] = true
	}
	for i := 0; i <= len(in.Figures); i++ {
		allowed[strconv.Itoa(i)] = true
	}
	return context.WithValue(ctx, allowedKey{}, struct {
		numbers map[string]bool
		rows    int
	}{allowed, len(in.Figures)})
}

// checkSummary refuses an answer that states a number the figures do not
// hold, which sends it up the ladder and, from the top, refuses the run. The
// figures are the report's; a summary is only ever a reading of them.
func checkSummary(ctx context.Context, raw json.RawMessage) (int, error) {
	a, ok := ctx.Value(allowedKey{}).(struct {
		numbers map[string]bool
		rows    int
	})
	if !ok {
		return 0, errors.New("no figures to check the summary against")
	}
	var o summaryOutput
	if err := json.Unmarshal(raw, &o); err != nil {
		return 0, err
	}
	texts := []string{o.Headline}
	if n := len([]rune(strings.TrimSpace(o.Headline))); n == 0 || n > 200 {
		return 0, fmt.Errorf("the headline is %d characters, and must be 1 to 200", n)
	}
	if len(o.Points) > 8 {
		return 0, fmt.Errorf("%d points, and at most 8", len(o.Points))
	}
	for _, p := range o.Points {
		if p.Row < 1 || p.Row > a.rows {
			return 0, fmt.Errorf("a point is about row %d, and the rows are 1 to %d", p.Row, a.rows)
		}
		if p.Kind != "working" && p.Kind != "wasting" && p.Kind != "watch" {
			return 0, fmt.Errorf("a point's kind is %q", p.Kind)
		}
		if n := len([]rune(strings.TrimSpace(p.Text))); n == 0 || n > 400 {
			return 0, fmt.Errorf("a point is %d characters, and must be 1 to 400", n)
		}
		texts = append(texts, p.Text)
	}
	for _, t := range texts {
		for _, m := range madeRE.FindAllStringSubmatch(t, -1) {
			// A ratio the figures hold may be written with an x; a count
			// made into a multiple ("3x the orders"), or any percentage, is new.
			if m[2] == "%" || !strings.Contains(canonical(m[1]), ".") {
				return 0, fmt.Errorf("%q is a share or multiple the figures do not hold", m[0])
			}
		}
		for _, n := range numberRE.FindAllString(t, -1) {
			if !a.numbers[canonical(n)] {
				return 0, fmt.Errorf("%q is not a number in the figures; use only the figures as given", n)
			}
		}
	}
	return 0, nil
}

func summaryTask() aigateway.Task {
	return aigateway.Task{
		Name:            summaryTaskName,
		Version:         1,
		System:          summarySystem,
		Schema:          summarySchema,
		MaxInputTokens:  20000,
		MaxOutputTokens: 8192,
		Ladder:          []aigateway.Tier{aigateway.TierSmall, aigateway.TierStrong},
		Build: func(_ context.Context, raw json.RawMessage, _ aigateway.Policy) (aigateway.Request, []aigateway.SourceRef, error) {
			var in summaryInput
			if err := json.Unmarshal(raw, &in); err != nil {
				return aigateway.Request{}, nil, err
			}
			return aigateway.Request{Messages: []aigateway.Message{{
				Role: "user",
				Text: fmt.Sprintf("Summarise the shop's ads from %s to %s, orders credited to their %s visit.", in.From, in.To, in.Touch),
				Data: []aigateway.Data{{Class: aigateway.DataOrderLines, Label: "figures", Value: in.Figures, Required: true}},
			}}}, []aigateway.SourceRef{{Kind: "ads_report", ID: in.From + "/" + in.To}}, nil
		},
		Check: checkSummary,
	}
}

func dash(m *gocommerce.Money) string {
	if m == nil {
		return "none"
	}
	return gocommerce.FormatMoney(m.AmountMinor, m.Currency)
}

func times(r *float64) string {
	if r == nil {
		return "none"
	}
	return strconv.FormatFloat(*r, 'f', 2, 64)
}

// figures turns a report into the rows the model reads: each currency's rows
// with spend first, numbered across the whole report.
func (m *Module) figures(ctx context.Context, rep *Report, orders []orderFact) ([]figure, error) {
	var out []figure
	for _, c := range rep.Currencies {
		for _, r := range c.Rows {
			if len(out) == maxFigures {
				break
			}
			f := func(v gocommerce.Money) string { return gocommerce.FormatMoney(v.AmountMinor, v.Currency) }
			out = append(out, figure{
				Row: len(out) + 1, Source: r.Source, Medium: r.Medium, Campaign: r.Campaign, Orders: r.Orders,
				Revenue: f(r.Revenue), Refunded: f(r.Refunded), Net: f(r.Net), GrossProfit: f(r.GrossProfit),
				UncostedUnits: r.UncostedUnits, Returns: r.Returns, NewCustomers: r.NewCustomers,
				Spend: dash(r.Spend), ROAS: times(r.ROAS), POAS: times(r.POAS), CostPerOrder: dash(r.CostPerOrder),
				currency: c.Currency,
			})
		}
	}
	// The products each row sold, and whether they can still be bought: an
	// ad that sends shoppers to an empty shelf is money spent on nothing.
	type rowKey struct {
		currency string
		ch       channel
	}
	ids := map[rowKey][]int64{}
	for _, o := range orders {
		k := rowKey{o.Currency, o.Attr.touch(rep.Touch).channel().key()}
		ids[k] = append(ids[k], o.ID)
	}
	for i := range out {
		f := &out[i]
		orderIDs := ids[rowKey{f.currency, channel{f.Source, f.Medium, f.Campaign}.key()}]
		if len(orderIDs) == 0 {
			continue
		}
		rows, err := m.app.DB().QueryContext(ctx, `
			SELECT l.title, sum(l.quantity)::int,
			       coalesce(bool_and(v.id IS NOT NULL AND NOT v.continue_selling AND coalesce(
			           (SELECT sum(s.on_hand - s.reserved) FROM variant_stock s WHERE s.variant_id = v.id), 0) <= 0), false)
			  FROM order_lines l LEFT JOIN variants v ON v.id = l.variant_id
			 WHERE l.order_id = ANY($1)
			 GROUP BY l.title ORDER BY 2 DESC, 1 LIMIT 3`, orderIDs)
		if err != nil {
			return nil, gocommerce.Internalf(err, "read the products sold")
		}
		for rows.Next() {
			var p product
			if err := rows.Scan(&p.Title, &p.Units, &p.OutOfStock); err != nil {
				rows.Close()
				return nil, gocommerce.Internalf(err, "read the products sold")
			}
			f.Products = append(f.Products, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, gocommerce.Internalf(err, "read the products sold")
		}
	}
	return out, nil
}

const (
	codeNotReady = "ai_not_ready"
	codeBudget   = "ai_budget_exhausted"
	codeCredit   = "ai_credit_exhausted"
	codePolicy   = "ai_policy_blocked"
	codeProvider = "ai_provider_failed"
	codeNoAnswer = "ai_no_summary"
)

// refusal turns a gateway refusal into the panel's codes, which are the
// ones catalog-ai answers with, so one screen of explanations serves both.
func refusal(err error) error {
	var re *aigateway.RunError
	if !errors.As(err, &re) {
		if errors.Is(err, aigateway.ErrNotReady) {
			return &gocommerce.APIError{Status: http.StatusConflict, Code: codeNotReady, Message: err.Error()}
		}
		return err
	}
	status, code := http.StatusBadGateway, codeNoAnswer
	switch re.Outcome {
	case aigateway.OutcomeBudgetExhausted:
		status, code = http.StatusConflict, codeBudget
	case aigateway.OutcomeCreditExhausted:
		status, code = http.StatusConflict, codeCredit
	case aigateway.OutcomeNoPrice:
		status, code = http.StatusConflict, codeNotReady
	case aigateway.OutcomePolicyBlocked:
		status, code = http.StatusConflict, codePolicy
	case aigateway.OutcomeProviderError:
		code = codeProvider
	}
	return &gocommerce.APIError{Status: status, Code: code, Message: re.Reason}
}

// summarise reads the report for w, has the model summarise it, and keeps
// the result. claimed is a weekly row already inserted for this window, to
// fill in; 0 inserts a new row.
func (m *Module) summarise(ctx context.Context, w window, kind string, claimed int64) (*Summary, error) {
	if m.cfg.Gateway == nil {
		return nil, &gocommerce.APIError{Status: http.StatusConflict, Code: codeNotReady,
			Message: "this store was built without the AI gateway (-ai-gateway), which the summary runs through"}
	}
	if err := m.cfg.Gateway.Ready(ctx); err != nil {
		return nil, refusal(err)
	}
	rep, orders, err := m.report(ctx, w)
	if err != nil {
		return nil, err
	}
	figs, err := m.figures(ctx, rep, orders)
	if err != nil {
		return nil, err
	}
	in := summaryInput{From: w.from, To: w.to, Touch: w.touch, Figures: figs}
	var out summaryOutput
	var execID int64
	if len(figs) == 0 {
		// Nothing sold and nothing spent: there is nothing for a model to read.
		out.Headline = "No orders and no ad spend in this period."
	} else {
		// Detached from the request, so a closed tab still leaves the run's
		// record and its true cost.
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(withAllowed(ctx, in)), 4*time.Minute)
		defer cancel()
		res, err := m.cfg.Gateway.Run(runCtx, summaryTaskName, in)
		if err != nil {
			return nil, refusal(err)
		}
		if err := json.Unmarshal(res.Output, &out); err != nil {
			return nil, gocommerce.Internalf(err, "read the summary")
		}
		execID = res.ExecutionID
	}
	if out.Points == nil {
		out.Points = []point{}
	}
	if figs == nil {
		figs = []figure{}
	}
	s := &Summary{Kind: kind, From: w.from, To: w.to, Touch: w.touch, Headline: out.Headline,
		Points: out.Points, Figures: figs, ExecutionID: execID}
	points, _ := json.Marshal(s.Points)
	figures, _ := json.Marshal(s.Figures)
	if claimed != 0 {
		err = m.app.DB().QueryRowContext(ctx, `
			UPDATE ads_summaries SET status = 'done', headline = $2, points = $3, figures = $4, execution_id = $5
			 WHERE id = $1 RETURNING id, created_at`, claimed, s.Headline, points, figures, nullID(execID)).Scan(&s.ID, &s.CreatedAt)
	} else {
		err = m.app.DB().QueryRowContext(ctx, `
			INSERT INTO ads_summaries (kind, period_from, period_to, touch, status, headline, points, figures, execution_id)
			VALUES ($1, $2, $3, $4, 'done', $5, $6, $7, $8) RETURNING id, created_at`,
			kind, w.from, w.to, w.touch, s.Headline, points, figures, nullID(execID)).Scan(&s.ID, &s.CreatedAt)
	}
	if err != nil {
		return nil, gocommerce.Internalf(err, "keep the summary")
	}
	return s, nil
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func (m *Module) handleSummarise(w http.ResponseWriter, r *http.Request) {
	var in struct {
		From  string `json:"from"`
		To    string `json:"to"`
		TZ    string `json:"tz"`
		Touch string `json:"touch"`
	}
	if r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0 {
		if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
	}
	win, err := m.parseWindow(r.Context(), map[string]string{"from": in.From, "to": in.To, "tz": in.TZ, "touch": in.Touch})
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(4 * time.Minute)); err != nil {
		m.app.Log().Warn("could not extend the write deadline for an ads summary", "error", err)
	}
	s, err := m.summarise(r.Context(), win, "manual", 0)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, s)
}

// handleLatest is the newest summary, or null when there is none.
func (m *Module) handleLatest(w http.ResponseWriter, r *http.Request) {
	var s Summary
	var points, figures []byte
	var exec sql.NullInt64
	var from, to time.Time
	err := m.app.DB().QueryRowContext(r.Context(), `
		SELECT id, kind, period_from, period_to, touch, headline, points, figures, execution_id, created_at
		  FROM ads_summaries WHERE status = 'done' ORDER BY created_at DESC, id DESC LIMIT 1`).
		Scan(&s.ID, &s.Kind, &from, &to, &s.Touch, &s.Headline, &points, &figures, &exec, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		// Spelled out: the envelope leaves a nil data out altogether.
		gocommerce.Respond(w, http.StatusOK, json.RawMessage("null"))
		return
	}
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read the summary"))
		return
	}
	s.From, s.To, s.ExecutionID = from.Format(time.DateOnly), to.Format(time.DateOnly), exec.Int64
	if json.Unmarshal(points, &s.Points) != nil || json.Unmarshal(figures, &s.Figures) != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(errors.New("unreadable"), "read the summary"))
		return
	}
	gocommerce.Respond(w, http.StatusOK, s)
}

// Settings is the shop's choices for this module.
type Settings struct {
	// WeeklySummary has the summary of the week before made every Monday. It
	// is off until the shop turns it on, because each one draws AI credit
	// with nobody pressing a button.
	WeeklySummary bool `json:"weekly_summary"`
}

func (m *Module) settings(ctx context.Context) (Settings, error) {
	var s Settings
	err := m.app.DB().QueryRowContext(ctx, `SELECT weekly_summary FROM ads_settings`).Scan(&s.WeeklySummary)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, gocommerce.Internalf(err, "read the ads settings")
	}
	return s, nil
}

func (m *Module) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	s, err := m.settings(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, s)
}

func (m *Module) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var s Settings
	if err := gocommerce.DecodeJSON(w, r, &s); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if _, err := m.app.DB().ExecContext(r.Context(), `
		INSERT INTO ads_settings (weekly_summary) VALUES ($1)
		ON CONFLICT (store_id) DO UPDATE SET weekly_summary = EXCLUDED.weekly_summary, updated_at = now()`,
		s.WeeklySummary); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "save the ads settings"))
		return
	}
	gocommerce.Respond(w, http.StatusOK, s)
}

// weekly makes last week's summary, Monday to Sunday in UTC, for a store
// that asked for it, once: the row is claimed before the model is called, so
// two processes never both spend credit on it.
func (m *Module) weekly(ctx context.Context, now time.Time) error {
	s, err := m.settings(ctx)
	if err != nil || !s.WeeklySummary || m.cfg.Gateway == nil {
		return err
	}
	today := now.UTC().Truncate(24 * time.Hour)
	monday := today.AddDate(0, 0, -((int(today.Weekday())+6)%7)-7)
	from, to := monday.Format(time.DateOnly), monday.AddDate(0, 0, 6).Format(time.DateOnly)
	var id int64
	err = m.app.DB().QueryRowContext(ctx, `
		INSERT INTO ads_summaries (kind, period_from, period_to, touch, status)
		VALUES ('weekly', $1, $2, 'last', 'running')
		ON CONFLICT (store_id, period_from) WHERE kind = 'weekly' DO NOTHING RETURNING id`, from, to).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	w, err := m.parseWindow(ctx, map[string]string{"from": from, "to": to})
	if err == nil {
		_, err = m.summarise(ctx, w, "weekly", id)
	}
	if err != nil {
		// Kept as failed rather than retried: a store out of credit would
		// otherwise be asked again every hour all week.
		// Not on ctx: a shutdown mid-run would leave the week claimed and never made.
		_, _ = m.app.DB().ExecContext(context.WithoutCancel(ctx), `UPDATE ads_summaries SET status = 'failed', error = $2 WHERE id = $1`, id, err.Error())
		m.app.Log().Info("ads: the weekly summary was not made", "error", err)
	}
	return nil
}

func (m *Module) start(ctx context.Context) error {
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			err := m.app.ForEachStore(ctx, func(ctx context.Context) error { return m.weekly(ctx, time.Now()) })
			if err != nil && ctx.Err() == nil {
				m.app.Log().Error("ads: weekly summaries failed", "error", err)
			}
		}
	}()
	return nil
}
