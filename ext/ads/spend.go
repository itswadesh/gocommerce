package ads

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Spend is what the shop paid an ad platform on one day for one campaign.
// One figure per day, source, medium, campaign and currency: entering or
// importing the same day again replaces it, so a re-downloaded report never
// counts twice.
type Spend struct {
	ID       int64            `json:"id"`
	Day      string           `json:"day"`
	Source   string           `json:"source"`
	Medium   string           `json:"medium"`
	Campaign string           `json:"campaign"`
	Amount   gocommerce.Money `json:"amount"`
	// Origin is manual, google or meta: how the figure got here.
	Origin string `json:"origin"`
}

// SpendInput is spend for a period, entered by hand. It is spread evenly
// over the period's days, so a report on part of the period counts its part.
type SpendInput struct {
	Source      string `json:"source"`
	Medium      string `json:"medium"`
	Campaign    string `json:"campaign"`
	From        string `json:"from"`
	To          string `json:"to"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

// ImportInput is an ad platform's report, as the file it downloads.
type ImportInput struct {
	// Format is google (Google Ads, a report with the Day segment) or meta
	// (Meta Ads Manager's export).
	Format string `json:"format"`
	CSV    string `json:"csv"`
	// Source and Medium are what this platform's ads put in utm_source and
	// utm_medium, which is how the spend finds its orders.
	Source string `json:"source"`
	Medium string `json:"medium"`
	// Currency is used only when the file does not say.
	Currency string `json:"currency"`
}

// ImportResult says what an import wrote.
type ImportResult struct {
	Days      int                `json:"days"`
	Campaigns int                `json:"campaigns"`
	Totals    []gocommerce.Money `json:"totals"`
}

const maxSpendDays = 366

var currencyRE = regexp.MustCompile(`^[A-Z]{3}$`)

type dayRow struct {
	Day      string
	Campaign string
	Currency string
	Amount   int64
}

func (m *Module) upsert(ctx context.Context, tx *sql.Tx, source, medium, origin string, rows []dayRow) error {
	for _, r := range rows {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO ads_spend (day, source, medium, campaign, currency, amount_minor, origin)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (store_id, day, source, medium, lower(campaign), currency)
			DO UPDATE SET amount_minor = EXCLUDED.amount_minor, campaign = EXCLUDED.campaign,
			              origin = EXCLUDED.origin, updated_at = now()`,
			r.Day, source, medium, r.Campaign, r.Currency, r.Amount, origin); err != nil {
			return gocommerce.Internalf(err, "save the spend")
		}
	}
	return nil
}

func channelOf(source, medium string) (string, string, error) {
	source, medium = strings.ToLower(clip(source)), strings.ToLower(clip(medium))
	if source == "" {
		return "", "", gocommerce.Validationf("source is required: what these ads put in utm_source, such as google or facebook")
	}
	return source, medium, nil
}

func (m *Module) handleAddSpend(w http.ResponseWriter, r *http.Request) {
	var in SpendInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	source, medium, err := channelOf(in.Source, in.Medium)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	campaign := clip(in.Campaign)
	if campaign == "" {
		gocommerce.RespondError(w, r, gocommerce.Validationf("campaign is required"))
		return
	}
	if in.Currency == "" {
		in.Currency = m.app.Currency(ctx)
	}
	if !currencyRE.MatchString(in.Currency) {
		gocommerce.RespondError(w, r, gocommerce.Validationf("currency is a three-letter code such as INR"))
		return
	}
	if in.AmountMinor < 0 {
		gocommerce.RespondError(w, r, gocommerce.Validationf("amount_minor is not negative"))
		return
	}
	if in.To == "" {
		in.To = in.From
	}
	days, err := daysBetween(in.From, in.To)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	rows := make([]dayRow, len(days))
	for i, part := range splitEvenly(in.AmountMinor, len(days)) {
		rows[i] = dayRow{days[i], campaign, in.Currency, part}
	}
	if err := gocommerce.InTx(ctx, m.app.DB(), func(tx *sql.Tx) error { return m.upsert(ctx, tx, source, medium, "manual", rows) }); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusCreated, map[string]int{"days": len(rows)})
}

func (m *Module) handleImport(w http.ResponseWriter, r *http.Request) {
	var in ImportInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	source, medium, err := channelOf(in.Source, in.Medium)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if in.Currency == "" {
		in.Currency = m.app.Currency(ctx)
	}
	rows, err := parseSpend(in.Format, in.CSV, strings.ToUpper(in.Currency))
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if err := gocommerce.InTx(ctx, m.app.DB(), func(tx *sql.Tx) error { return m.upsert(ctx, tx, source, medium, in.Format, rows) }); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	res := ImportResult{Days: len(rows), Totals: []gocommerce.Money{}}
	campaigns := map[string]bool{}
	totals := map[string]int64{}
	var order []string
	for _, row := range rows {
		campaigns[strings.ToLower(row.Campaign)] = true
		if _, ok := totals[row.Currency]; !ok {
			order = append(order, row.Currency)
		}
		totals[row.Currency] += row.Amount
	}
	for _, c := range order {
		res.Totals = append(res.Totals, gocommerce.Money{AmountMinor: totals[c], Currency: c})
	}
	res.Campaigns = len(campaigns)
	gocommerce.Respond(w, http.StatusOK, res)
}

func (m *Module) handleListSpend(w http.ResponseWriter, r *http.Request) {
	q := query(r)
	win, err := m.parseWindow(r.Context(), q)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	rows, err := m.app.DB().QueryContext(r.Context(), `
		SELECT id, day, source, medium, campaign, currency, amount_minor, origin
		  FROM ads_spend WHERE day BETWEEN $1 AND $2
		 ORDER BY day DESC, lower(campaign), id LIMIT 1000`, win.from, win.to)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read the spend"))
		return
	}
	defer rows.Close()
	out := []Spend{}
	for rows.Next() {
		var s Spend
		var day time.Time
		if err := rows.Scan(&s.ID, &day, &s.Source, &s.Medium, &s.Campaign, &s.Amount.Currency, &s.Amount.AmountMinor, &s.Origin); err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read the spend"))
			return
		}
		s.Day = day.Format(time.DateOnly)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read the spend"))
		return
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

func (m *Module) handleDeleteSpend(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no spend %q", r.PathValue("id")))
		return
	}
	res, err := m.app.DB().ExecContext(r.Context(), `DELETE FROM ads_spend WHERE id = $1`, id)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "delete the spend"))
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no spend %d", id))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func daysBetween(from, to string) ([]string, error) {
	a, err := parseDay(from)
	if err != nil {
		return nil, gocommerce.Validationf("from: %v", err)
	}
	b, err := parseDay(to)
	if err != nil {
		return nil, gocommerce.Validationf("to: %v", err)
	}
	if b.Before(a) {
		return nil, gocommerce.Validationf("the period ends before it starts")
	}
	n := int(b.Sub(a).Hours()/24) + 1
	if n > maxSpendDays {
		return nil, gocommerce.Validationf("a period is at most %d days", maxSpendDays)
	}
	out := make([]string, n)
	for i := range out {
		out[i] = a.AddDate(0, 0, i).Format(time.DateOnly)
	}
	return out, nil
}

// splitEvenly divides an amount over n days, the remainder a minor unit at a
// time to the first days, so the parts add up to the whole exactly.
func splitEvenly(amount int64, n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = amount / int64(n)
		if int64(i) < amount%int64(n) {
			out[i]++
		}
	}
	return out
}

// The date spellings the two exports use: ISO, and Google's in English
// ("Oct 1, 2026" or "October 1, 2026"), which its downloads use in some
// account languages.
var dayLayouts = []string{time.DateOnly, "Jan 2, 2006", "January 2, 2006"}

func parseDay(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, l := range dayLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a date this import reads (use YYYY-MM-DD)", s)
}

// bom opens a file saved by Excel; nbsp groups digits in some locales.
const (
	bom  = string(rune(0xFEFF))
	nbsp = string(rune(0xA0))
)

var amountJunk = strings.NewReplacer(" ", "", "₹", "", "$", "", "€", "", "£", "", nbsp, "")

// parseAmount reads a decimal amount into minor units without a float,
// rounding half up past the currency's decimals. Google writes "--" for
// nothing spent.
func parseAmount(s string, exp int) (int64, error) {
	s = amountJunk.Replace(strings.TrimSpace(s))
	if s == "" || s == "--" {
		return 0, nil
	}
	// A comma after the last point, or a lone comma not followed by three
	// digits, is a decimal comma: "1.234,56", "12,34".
	if c := strings.LastIndex(s, ","); c >= 0 && (c > strings.LastIndex(s, ".") && strings.Contains(s, ".") ||
		!strings.Contains(s, ".") && strings.Count(s, ",") == 1 && len(s)-c-1 != 3) {
		s = strings.ReplaceAll(s[:c], ".", "") + "." + s[c+1:]
	}
	s = strings.ReplaceAll(s, ",", "")
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	for _, part := range []string{whole, frac} {
		for _, c := range part {
			if c < '0' || c > '9' {
				return 0, fmt.Errorf("%q is not an amount", s)
			}
		}
	}
	round := false
	if len(frac) > exp {
		round = frac[exp] >= '5'
		frac = frac[:exp]
	}
	frac += strings.Repeat("0", exp-len(frac))
	v, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not an amount", s)
	}
	if round {
		v++
	}
	return v, nil
}

var spentRE = regexp.MustCompile(`(?i)^amount spent(?:\s*\(([a-z]{3})\))?$`)

// parseSpend maps an ad platform's export onto days of spend by the columns'
// names, never by their positions, and refuses a file that lacks one it
// needs, naming it. A day of nothing spent is kept, as a zero.
//
// Google Ads: a report downloaded with the Day segment. The download puts a
// title and the date range above the column names and "Total:" rows below the
// data, and both are skipped. Columns: Day, Campaign, Cost, and Currency code
// when the report has it.
//
// Meta Ads Manager: the table export. Columns: Campaign name and "Amount
// spent (XXX)", whose brackets name the currency, with Day when the export is
// broken down by day, and otherwise Reporting starts and Reporting ends.
func parseSpend(format, text, fallbackCurrency string) ([]dayRow, error) {
	if format != "google" && format != "meta" {
		return nil, gocommerce.Validationf("format is google or meta")
	}
	records, err := readRecords(text)
	if err != nil {
		return nil, err
	}
	at := func(header []string, names ...string) int {
		for i, h := range header {
			for _, n := range names {
				if strings.EqualFold(strings.TrimSpace(h), n) {
					return i
				}
			}
		}
		return -1
	}
	// The column names are the first row naming the campaign.
	campaignCol := map[string]string{"google": "Campaign", "meta": "Campaign name"}[format]
	head := -1
	for i, rec := range records {
		if at(rec, campaignCol) >= 0 {
			head = i
			break
		}
	}
	if head < 0 {
		return nil, gocommerce.Validationf("no %q column: is this a %s export at the campaign level? Its first rows are %s",
			campaignCol, map[string]string{"google": "Google Ads", "meta": "Meta Ads Manager"}[format], preview(records))
	}
	header := records[head]
	campaign := at(header, campaignCol)
	day, starts, ends := at(header, "Day"), at(header, "Reporting starts"), at(header, "Reporting ends")
	amount, currencyCol, fileCurrency := -1, -1, ""
	switch format {
	case "google":
		amount, currencyCol = at(header, "Cost"), at(header, "Currency code")
		if amount < 0 {
			return nil, gocommerce.Validationf("no \"Cost\" column; the columns are %s", strings.Join(header, ", "))
		}
		if day < 0 {
			return nil, gocommerce.Validationf("no \"Day\" column: add the Day segment (Segment › Time › Day) before downloading, so each day's spend is known")
		}
	case "meta":
		for i, h := range header {
			if m := spentRE.FindStringSubmatch(strings.TrimSpace(h)); m != nil {
				amount, fileCurrency = i, strings.ToUpper(m[1])
			}
		}
		if amount < 0 {
			return nil, gocommerce.Validationf("no \"Amount spent\" column; the columns are %s", strings.Join(header, ", "))
		}
		if day < 0 && (starts < 0 || ends < 0) {
			return nil, gocommerce.Validationf("no \"Day\" column, and no \"Reporting starts\" and \"Reporting ends\" to say when the spend was")
		}
	}

	var out []dayRow
	for n, rec := range records[head+1:] {
		line := head + n + 2
		cell := func(i int) string {
			if i < 0 || i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		name := clip(cell(campaign))
		first := strings.TrimSpace(strings.Join(rec, ""))
		if first == "" || strings.HasPrefix(strings.ToLower(cell(0)), "total:") || strings.HasPrefix(strings.ToLower(name), "total:") {
			continue
		}
		currency := fileCurrency
		if currencyCol >= 0 && cell(currencyCol) != "" {
			currency = strings.ToUpper(cell(currencyCol))
		}
		if currency == "" {
			currency = fallbackCurrency
		}
		if !currencyRE.MatchString(currency) {
			return nil, gocommerce.Validationf("line %d: %q is not a currency code", line, currency)
		}
		minor, err := parseAmount(cell(amount), gocommerce.CurrencyExponent(currency))
		if err != nil {
			return nil, gocommerce.Validationf("line %d: %v", line, err)
		}
		// A day of nothing is kept, so a report downloaded again can lower a
		// day it once had spend for.
		if name == "" {
			if minor == 0 {
				continue
			}
			return nil, gocommerce.Validationf("line %d has spend and no campaign", line)
		}
		var days []string
		if day >= 0 {
			d, err := parseDay(cell(day))
			if err != nil {
				return nil, gocommerce.Validationf("line %d: %v", line, err)
			}
			days = []string{d.Format(time.DateOnly)}
		} else if days, err = daysBetween(cell(starts), cell(ends)); err != nil {
			return nil, gocommerce.Validationf("line %d: %v", line, err)
		}
		for i, part := range splitEvenly(minor, len(days)) {
			out = append(out, dayRow{days[i], name, currency, part})
		}
	}
	if len(out) == 0 {
		return nil, gocommerce.Validationf("the file has no spend in it")
	}
	return merge(out), nil
}

// merge adds up rows for the same day and campaign, which a report broken
// down further than by campaign (by ad set, by device) has many of.
func merge(rows []dayRow) []dayRow {
	type k struct{ day, campaign, currency string }
	at := map[k]int{}
	var out []dayRow
	for _, r := range rows {
		key := k{r.Day, strings.ToLower(r.Campaign), r.Currency}
		if i, ok := at[key]; ok {
			out[i].Amount += r.Amount
			continue
		}
		at[key] = len(out)
		out = append(out, r)
	}
	return out
}

// readRecords reads the file as CSV, or as tab-separated text when that is
// what it is: Google's "Excel .csv" download is. The separator is the one
// that splits the column names into the most columns, since a title or an
// amount above or among the data can carry commas of its own.
func readRecords(text string) ([][]string, error) {
	text = strings.TrimPrefix(text, bom)
	if strings.TrimSpace(text) == "" {
		return nil, gocommerce.Validationf("the file is empty")
	}
	var best [][]string
	var bestErr error
	widest := 0
	for _, sep := range []rune{',', '\t'} {
		r := csv.NewReader(strings.NewReader(text))
		r.Comma, r.FieldsPerRecord, r.LazyQuotes = sep, -1, true
		var out [][]string
		var err error
		for {
			rec, e := r.Read()
			if errors.Is(e, io.EOF) {
				break
			}
			if e != nil {
				err = gocommerce.Validationf("the file could not be read as CSV: %v", e)
				break
			}
			out = append(out, rec)
		}
		w := 0
		for _, rec := range out[:min(len(out), 5)] {
			w = max(w, len(rec))
		}
		if err == nil && w > widest {
			best, widest = out, w
		} else if best == nil && err != nil {
			bestErr = err
		}
	}
	if best == nil {
		return nil, bestErr
	}
	return best, nil
}

func preview(records [][]string) string {
	var lines []string
	for _, r := range records[:min(len(records), 3)] {
		lines = append(lines, strings.Join(r, ", "))
	}
	return strconv.Quote(strings.Join(lines, " / "))
}
