package retailassistant

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	gocommerce "github.com/misiki-in/gocommerce/core"
	"github.com/misiki-in/gocommerce/ext/b2b"
)

const (
	// Which rule priced a quote, kept apart because 20% of each is a different price.
	pricingMarkup = "markup"
	pricingMargin = "margin"

	// rightQuotes is ext/b2b's own right: this flow ends in one of its quotes.
	rightQuotes gocommerce.Right = "quotes.write"

	bpsWhole = 10_000
)

// priceFromCost applies a rule, in basis points, rounding half up to a whole minor
// unit: 20% on 100.00 is 120.00 as markup and 125.00 as gross margin.
func priceFromCost(cost int64, mode string, bps int) (int64, error) {
	if bps < 0 {
		return 0, gocommerce.Validationf("the percentage must not be negative")
	}
	var over, under int64
	switch mode {
	case pricingMarkup:
		over, under = cost*(bpsWhole+int64(bps)), bpsWhole
	case pricingMargin:
		if bps >= bpsWhole {
			return 0, gocommerce.Validationf("a gross margin must be under 100%%")
		}
		over, under = cost*bpsWhole, bpsWhole-int64(bps)
	default:
		return 0, gocommerce.Validationf("the rule is %s or %s", pricingMarkup, pricingMargin)
	}
	return (2*over + under) / (2 * under), nil
}

// WholesaleLine says which line of the document to quote, as what, how many.
type WholesaleLine struct {
	Index     int   `json:"index"`
	VariantID int64 `json:"variant_id"`
	Quantity  int   `json:"quantity"`
	// PriceMinor replaces what the rule gives, for this line.
	PriceMinor *int64 `json:"price_minor"`
}

// WholesaleRequest prices a manufacturer's document for one retailer.
type WholesaleRequest struct {
	DocumentID int64           `json:"document_id"`
	CompanyID  int64           `json:"company_id"`
	BuyerID    int64           `json:"buyer_id"`
	Mode       string          `json:"mode"`
	PercentBps int             `json:"percent_bps"`
	Note       string          `json:"note"`
	Lines      []WholesaleLine `json:"lines"`
	// AllowBelowCost is a person saying a price under cost is deliberate.
	AllowBelowCost bool `json:"allow_below_cost"`
}

// PricedLine is the wholesaler's own view, with what they paid. It never goes in
// a quote: a quote holds the price alone.
type PricedLine struct {
	Index       int    `json:"index"`
	Description string `json:"description"`
	VariantID   int64  `json:"variant_id"`
	Quantity    int    `json:"quantity"`
	CostMinor   *int64 `json:"cost_minor"`
	PriceMinor  int64  `json:"price_minor"`
	// MarginBps is the gross margin the price leaves, when the cost is known.
	MarginBps  *int64 `json:"margin_bps"`
	BelowCost  bool   `json:"below_cost"`
	Overridden bool   `json:"overridden"`
}

// price works a request out from the document's own figures. The cost comes from
// the document here, never from the request, so the check against it means
// something.
func (m *Module) price(ctx context.Context, in WholesaleRequest) ([]PricedLine, error) {
	if len(in.Lines) == 0 {
		return nil, gocommerce.Validationf("choose at least one line")
	}
	d, err := m.document(ctx, in.DocumentID)
	if err != nil {
		return nil, err
	}
	if d.Status != "needs_review" && d.Status != "posted" {
		return nil, gocommerce.Conflictf("this document is %s, so it cannot be quoted from", d.Status)
	}
	if d.Draft == nil {
		return nil, gocommerce.Conflictf("nothing was read from this document")
	}
	out := make([]PricedLine, 0, len(in.Lines))
	for _, l := range in.Lines {
		if l.Index < 0 || l.Index >= len(d.Draft.Lines) {
			return nil, gocommerce.Validationf("this document has no line %d", l.Index+1)
		}
		line := d.Draft.Lines[l.Index]
		p := PricedLine{Index: l.Index, Description: line.Description, VariantID: l.VariantID, Quantity: l.Quantity, CostMinor: line.UnitPriceMinor}
		switch {
		case l.PriceMinor != nil:
			if *l.PriceMinor < 0 {
				return nil, gocommerce.Validationf("a price must not be negative")
			}
			p.PriceMinor, p.Overridden = *l.PriceMinor, true
		case line.UnitPriceMinor == nil:
			return nil, gocommerce.Validationf("%q has no price on the document, so type its price", line.Description)
		default:
			if p.PriceMinor, err = priceFromCost(*line.UnitPriceMinor, in.Mode, in.PercentBps); err != nil {
				return nil, err
			}
		}
		if line.UnitPriceMinor != nil {
			p.BelowCost = p.PriceMinor < *line.UnitPriceMinor
			if p.PriceMinor > 0 {
				margin := (p.PriceMinor - *line.UnitPriceMinor) * bpsWhole / p.PriceMinor
				p.MarginBps = &margin
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// wholesaleReady refuses when this install or this person cannot make a quote. The
// right is checked here, not on the route, so the routes exist in a build without
// ext/b2b and answer as a switched-off feature does.
func (m *Module) wholesaleReady(w http.ResponseWriter, r *http.Request) bool {
	if m.cfg.B2B == nil {
		gocommerce.RespondError(w, r, &gocommerce.APIError{
			Status: http.StatusNotFound, Code: "feature_off", Message: "this install does not have B2B quotes",
		})
		return false
	}
	if !mayRead(r.Context(), rightQuotes) {
		gocommerce.RespondError(w, r, gocommerce.Forbiddenf("this needs the %s right", rightQuotes))
		return false
	}
	return true
}

func (m *Module) handleWholesalePreview(w http.ResponseWriter, r *http.Request) {
	if !m.wholesaleReady(w, r) {
		return
	}
	var in WholesaleRequest
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	lines, err := m.price(r.Context(), in)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]any{"lines": lines, "currency": m.app.Currency(r.Context())})
}

// handleWholesaleQuote makes a draft B2B quote and stops there. Sending it is a
// separate press on the quote's own screen, so a person sees what the retailer
// will see. A quote is not a tax invoice, and nothing here makes one.
//
// What the wholesaler paid stays in this module's row, for the audit trail, and is
// never passed to ext/b2b. The claim row commits before the quote is made, so a
// retry with the same key finds it rather than making a second quote; a crash
// between the two leaves a row with no quote, and the press has to be made again
// with a new key.
func (m *Module) handleWholesaleQuote(w http.ResponseWriter, r *http.Request) {
	if !m.wholesaleReady(w, r) {
		return
	}
	var in WholesaleRequest
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	lines, err := m.price(ctx, in)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	belowCost, overridden := false, false
	inputs := make([]b2b.QuoteLineInput, len(lines))
	for i, l := range lines {
		belowCost = belowCost || l.BelowCost
		overridden = overridden || l.Overridden
		price := l.PriceMinor
		inputs[i] = b2b.QuoteLineInput{VariantID: l.VariantID, Quantity: l.Quantity, UnitPriceMinor: &price}
	}
	if belowCost && !in.AllowBelowCost {
		gocommerce.RespondError(w, r, gocommerce.Conflictf("a price is below what you paid. Say that is deliberate to go on"))
		return
	}
	raw, err := json.Marshal(lines)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "record a wholesale quote"))
		return
	}
	var rowID int64
	var existing sql.NullInt64
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `
			INSERT INTO retailassistant_wholesale_quotes
			    (document_id, company_id, mode, percent_bps, lines, below_cost, overridden, idempotency_key, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (store_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
			RETURNING id`,
			in.DocumentID, in.CompanyID, in.Mode, in.PercentBps, raw, belowCost, overridden, keyFrom(r), actor(ctx)).Scan(&rowID)
		if errors.Is(err, sql.ErrNoRows) {
			return tx.QueryRowContext(ctx, `SELECT id, quote_id FROM retailassistant_wholesale_quotes WHERE idempotency_key = $1`, keyFrom(r)).Scan(&rowID, &existing)
		}
		if err != nil {
			return err
		}
		return m.audit(ctx, tx, "wholesale.priced", "wholesale_quote", rowID, map[string]any{
			"document_id": in.DocumentID, "company_id": in.CompanyID, "mode": in.Mode, "percent_bps": in.PercentBps,
			"below_cost": belowCost, "overridden": overridden,
		})
	})
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "record a wholesale quote"))
		return
	}
	if existing.Valid {
		q, err := m.cfg.B2B.Quote(ctx, existing.Int64)
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		gocommerce.Respond(w, http.StatusOK, q)
		return
	}
	q, err := m.cfg.B2B.DraftQuote(ctx, in.CompanyID, in.BuyerID, b2b.QuoteRequest{Lines: inputs, Note: strings.TrimSpace(in.Note)})
	if err != nil {
		// Nothing was made, so nothing stays claiming the key.
		_, _ = m.db.ExecContext(ctx, `DELETE FROM retailassistant_wholesale_quotes WHERE id = $1 AND quote_id IS NULL`, rowID)
		gocommerce.RespondError(w, r, err)
		return
	}
	if _, err := m.db.ExecContext(ctx, `UPDATE retailassistant_wholesale_quotes SET quote_id = $2 WHERE id = $1`, rowID, q.ID); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "record a wholesale quote"))
		return
	}
	gocommerce.Respond(w, http.StatusCreated, q)
}
