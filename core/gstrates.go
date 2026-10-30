package gocommerce

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// India's GST rate schedule for goods, keyed by HSN prefix, so the taxes
// screen can offer a rule per category from the code the category already
// carries (D175). Embedded for D172's reasons: the same for every store,
// nobody edits it, and a fetch at run time fails offline.
//
// Source: CBIC's Notification No. 9/2025-Integrated Tax (Rate), 17.09.2025
// (in force 22.09.2025), with its corrigendum of 18.09.2025 and the
// amendments 19/2025 (31.12.2025: biris to 18%, tobacco and pan masala to
// 40%, Schedule VII omitted), 01/2026 (30.04.2026: tariff items of 2202) and
// 02/2026 (27.10.2026, in force 01.11.2026: retreaded tractor tyres to 5%),
// each fetched from taxinformation.cbic.gov.in on 2026-10-30. The IGST
// notification rather than the CGST one because its rate is the whole GST
// rate (18%, not 9%), which is what a rule stores and what the invoice splits
// into CGST+SGST/UTGST or charges as IGST (D137).
//
// Format: prefix<TAB>rate_bp<TAB>conditional<TAB>source<TAB>description, one
// line per code an entry names ("5004 to 5006" is three lines). Descriptions
// are the notification's words; an exclusion printed in the code column is
// appended as "[column (2) excludes …]". Conditional (1) marks an entry whose
// description narrows its codes — a sale value, a use, "other than" — so a
// person has to read it. Entries for "any chapter" have no prefix and are not
// here. Exemptions (10/2025) and services are not here, and nor is
// compensation cess. To refresh, extract a newer consolidated schedule the
// same way and replace hsn/gst-rates.tsv.gz.

//go:embed hsn/gst-rates.tsv.gz
var gstRatesGzip []byte

// GSTRateMatch is one schedule entry that covers a code.
type GSTRateMatch struct {
	// Prefix is the chapter, heading, subheading or tariff item the entry
	// names.
	Prefix      string `json:"prefix"`
	RateBP      int    `json:"rate_bp"`
	Description string `json:"description"`
	// Conditional means the description, not the code, decides whether the
	// entry applies.
	Conditional bool `json:"conditional"`
	// Source is the notification, schedule and serial number.
	Source string `json:"source"`
	// Narrower is an entry under the code asked about rather than over it: a
	// chapter's code, asked about, has no rate of its own, only its headings'.
	Narrower bool `json:"narrower,omitempty"`
}

var gstSchedule = sync.OnceValue(func() []GSTRateMatch {
	zr, err := gzip.NewReader(bytes.NewReader(gstRatesGzip))
	if err != nil {
		panic("gst rates: embedded schedule is not gzip: " + err.Error())
	}
	var out []GSTRateMatch
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) != 5 {
			panic("gst rates: malformed line " + sc.Text())
		}
		bp, err := strconv.Atoi(f[1])
		if err != nil {
			panic("gst rates: bad rate in " + sc.Text())
		}
		out = append(out, GSTRateMatch{Prefix: f[0], RateBP: bp, Conditional: f[2] == "1", Source: f[3], Description: f[4]})
	}
	if err := sc.Err(); err != nil {
		panic("gst rates: embedded schedule is unreadable: " + err.Error())
	}
	return out
})

// LookupGSTRate is every schedule entry covering code, longest prefix first.
// Shorter entries are kept rather than dropped: chapter 64's "footwear up to
// Rs 2,500 a pair" at 5% still applies to a shoe under 6403's 18%, and only a
// person who knows the price can choose. With no entry over the code, the
// entries under it come back marked Narrower. Nil for no match or no code.
func LookupGSTRate(code string) []GSTRateMatch {
	d, ok := hsnDigits(code)
	if !ok {
		return nil
	}
	var over, under []GSTRateMatch
	for _, m := range gstSchedule() {
		switch {
		case strings.HasPrefix(d, m.Prefix):
			over = append(over, m)
		case strings.HasPrefix(m.Prefix, d):
			m.Narrower = true
			under = append(under, m)
		}
	}
	if len(over) == 0 {
		over = under
	}
	slices.SortStableFunc(over, func(a, b GSTRateMatch) int { return len(b.Prefix) - len(a.Prefix) })
	return over
}

// GSTRateTarget is a category or product the schedule could set a rule for.
type GSTRateTarget struct {
	Kind string `json:"kind"` // "category" or "product"
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Codes is the category's own HSN code, or the distinct codes on a
	// product's variants (more than one is for a person to settle).
	Codes   []string       `json:"codes"`
	Matches []GSTRateMatch `json:"matches"`
}

// GSTRateTargets is what the catalogue would need a rule for: each category
// with an HSN code of its own, and each product with a variant whose code
// differs from the one its category gives it (D171).
//
// Only categories with their own code, because a tax rule reaches down the
// tree exactly as the HSN fallback does: a subcategory without a code takes
// its parent's code on the invoice and its parent's rule at checkout, so a
// row for it would only repeat its parent.
func (a *App) GSTRateTargets(ctx context.Context) ([]GSTRateTarget, error) {
	rows, err := a.db.QueryContext(ctx, `
		WITH RECURSIVE eff AS (
		    SELECT id, hsn_code AS code, 0 AS depth FROM categories WHERE parent_id IS NULL
		  UNION ALL
		    SELECT c.id, CASE WHEN c.hsn_code <> '' THEN c.hsn_code ELSE eff.code END, eff.depth + 1
		    FROM categories c JOIN eff ON c.parent_id = eff.id
		    WHERE eff.depth < $1
		)
		SELECT 'category', id, title, hsn_code FROM categories WHERE hsn_code <> ''
		UNION ALL
		SELECT DISTINCT 'product', p.id, p.title, v.hs_code
		FROM products p
		JOIN variants v ON v.product_id = p.id
		LEFT JOIN eff ON eff.id = p.category_id
		WHERE v.hs_code <> '' AND v.hs_code <> coalesce(eff.code, '')
		ORDER BY 1, 3, 2, 4`, MaxCategoryDepth)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GSTRateTarget{}
	for rows.Next() {
		var t GSTRateTarget
		var code string
		if err := rows.Scan(&t.Kind, &t.ID, &t.Name, &code); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].Kind == t.Kind && out[n-1].ID == t.ID {
			out[n-1].Codes = append(out[n-1].Codes, code)
			continue
		}
		t.Codes = []string{code}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Matches = []GSTRateMatch{}
		for _, c := range out[i].Codes {
			for _, m := range LookupGSTRate(c) {
				if !slices.ContainsFunc(out[i].Matches, func(x GSTRateMatch) bool {
					return x.Source == m.Source && x.Prefix == m.Prefix
				}) {
					out[i].Matches = append(out[i].Matches, m)
				}
			}
		}
	}
	return out, nil
}

// handleGSTRates answers ?hsn= with the schedule entries covering it. A code
// the schedule does not cover is an empty list, not a 404: "no rate found" is
// an answer the screen shows.
func (a *App) handleGSTRates(w http.ResponseWriter, r *http.Request) {
	d, ok := hsnDigits(r.URL.Query().Get("hsn"))
	if !ok {
		RespondError(w, r, Validationf("hsn must be an HSN code, digits only"))
		return
	}
	m := LookupGSTRate(d)
	if m == nil {
		m = []GSTRateMatch{}
	}
	Respond(w, http.StatusOK, map[string]any{"hsn": d, "matches": m})
}

func (a *App) handleGSTRateTargets(w http.ResponseWriter, r *http.Request) {
	list, err := a.GSTRateTargets(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	RespondList(w, list, ListMeta{Total: len(list), Limit: len(list)})
}
