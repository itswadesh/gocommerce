package gocommerce

import (
	"context"
	"sort"
	"time"
)

// What each payment method took, and what went back out through it.
//
// The sales report answers "how much did we sell" and never asks which
// gateway carried it, because a bar chart of revenue does not care. An
// operator reconciling a bank statement does: the money arrives one payout
// per gateway, and the question is which of them owes what.
//
// What this is NOT, and the screen says so: a settlement statement. The
// engine knows what it charged and what it refunded, from its own orders;
// it does not know when a gateway actually paid out, what it withheld in
// fees, or which orders it grouped into one transfer — those are facts
// that live in the gateway's own API, and a module that fetched them would
// be reconciling two records rather than inventing one. So this is the
// store's side of the ledger, counted by the day the order was placed, and
// the difference from a bank statement is fees and settlement lag (D61).

// MethodPayout is one payment method's share of a window.
type MethodPayout struct {
	// Code is what the checkout URL speaks; Name is what the provider calls
	// itself, or the code for one that answers to no name.
	Code string `json:"code"`
	Name string `json:"name"`
	// Installed is false for a method that took money once and is no longer
	// in the binary. The row still counts — the money was still taken — and
	// the flag is what stops an operator hunting for a screen to open.
	Installed bool `json:"installed"`
	// Orders and Collected are the paid sales: what this method charged.
	Orders    int   `json:"orders"`
	Collected Money `json:"collected"`
	// Refunded is refunded_minor over exactly those orders, and Net is what
	// is left — the figure to compare against a bank statement.
	Refunded Money `json:"refunded"`
	Net      Money `json:"net"`
	// Outstanding is what this method is owed: orders that sold but have not
	// been paid. On cash on delivery that is the book of money in transit;
	// on a card gateway it is usually a failure worth looking at.
	OutstandingOrders int   `json:"outstanding_orders"`
	Outstanding       Money `json:"outstanding"`
}

// CurrencyPayouts is one currency's methods and their sum. Nothing is ever
// added across two currencies: minor units of different ones add up to a
// number that is not money.
type CurrencyPayouts struct {
	Currency string         `json:"currency"`
	Methods  []MethodPayout `json:"methods"`
	Totals   MethodPayout   `json:"totals"`
}

// PayoutsReport is the window, by method.
type PayoutsReport struct {
	From       time.Time         `json:"from"`
	To         time.Time         `json:"to"`
	TimeZone   string            `json:"time_zone"`
	Currencies []CurrencyPayouts `json:"currencies"`
}

// PayoutsQuery is the window and the zone to read it in.
type PayoutsQuery struct {
	From, To ReportBound
	TimeZone string
}

// Payouts reports what each payment method collected, refunded and is owed.
func (r *Reports) Payouts(ctx context.Context, q PayoutsQuery) (*PayoutsReport, error) {
	tz, err := r.zone(ctx, q.TimeZone)
	if err != nil {
		return nil, err
	}
	lo, hi, err := r.window(ctx, tz, q.From, q.To)
	if err != nil {
		return nil, err
	}

	// A sale is an order whose stock has left the shelf, exactly as the
	// sales report defines one — the two screens must agree about what a
	// sale is or an operator comparing them has to work out which lied.
	rows, err := r.app.db.QueryContext(ctx, `
		SELECT o.currency, o.payment_provider,
		       count(*)                     FILTER (WHERE o.payment_status = 'paid'),
		       coalesce(sum(o.total_minor)  FILTER (WHERE o.payment_status = 'paid'), 0),
		       coalesce(sum(o.refunded_minor) FILTER (WHERE o.payment_status = 'paid'), 0),
		       count(*)                     FILTER (WHERE o.payment_status IN ('pending', 'failed')),
		       coalesce(sum(o.total_minor)  FILTER (WHERE o.payment_status IN ('pending', 'failed')), 0)
		  FROM orders o
		 WHERE o.created_at >= $1 AND o.created_at < $2 AND o.status = ANY($3::text[])
		 GROUP BY o.currency, o.payment_provider
		 ORDER BY o.currency, o.payment_provider`,
		lo, hi, stringArray(saleStatuses))
	if err != nil {
		return nil, Internalf(err, "read the payouts report")
	}
	defer rows.Close()

	// The names come from the providers this build carries. A method that
	// took money and has since been removed from the binary keeps its code
	// as its name, and says it is gone.
	installed := map[string]string{}
	for _, m := range r.app.payments.Installed() {
		installed[m.Code] = m.Name
	}

	report := &PayoutsReport{From: lo, To: hi, TimeZone: tz}
	byCurrency := map[string]*CurrencyPayouts{}
	for rows.Next() {
		var currency, code string
		var orders int
		var collected, refunded int64
		var owedOrders int
		var owed int64
		if err := rows.Scan(&currency, &code, &orders, &collected, &refunded, &owedOrders, &owed); err != nil {
			return nil, Internalf(err, "scan the payouts report")
		}
		block, ok := byCurrency[currency]
		if !ok {
			block = &CurrencyPayouts{Currency: currency, Methods: []MethodPayout{}}
			byCurrency[currency] = block
		}
		name, known := installed[code]
		if !known {
			name = code
		}
		block.Methods = append(block.Methods, MethodPayout{
			Code: code, Name: name, Installed: known,
			Orders:    orders,
			Collected: money(collected, currency),
			Refunded:  money(refunded, currency),
			Net:       money(collected-refunded, currency),

			OutstandingOrders: owedOrders,
			Outstanding:       money(owed, currency),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, Internalf(err, "read the payouts report")
	}

	// A store with no sales in the window still has a currency, and an
	// empty response looks broken rather than quiet.
	if _, ok := byCurrency[r.app.cfg.Currency]; !ok {
		byCurrency[r.app.cfg.Currency] = &CurrencyPayouts{Currency: r.app.cfg.Currency, Methods: []MethodPayout{}}
	}

	currencies := make([]string, 0, len(byCurrency))
	for currency := range byCurrency {
		currencies = append(currencies, currency)
	}
	sort.Strings(currencies)

	for _, currency := range currencies {
		block := byCurrency[currency]
		block.Totals = MethodPayout{
			Code: "", Name: "All methods", Installed: true,
			Collected:   money(0, currency),
			Refunded:    money(0, currency),
			Net:         money(0, currency),
			Outstanding: money(0, currency),
		}
		for _, m := range block.Methods {
			block.Totals.Orders += m.Orders
			block.Totals.Collected.AmountMinor += m.Collected.AmountMinor
			block.Totals.Refunded.AmountMinor += m.Refunded.AmountMinor
			block.Totals.Net.AmountMinor += m.Net.AmountMinor
			block.Totals.OutstandingOrders += m.OutstandingOrders
			block.Totals.Outstanding.AmountMinor += m.Outstanding.AmountMinor
		}
		report.Currencies = append(report.Currencies, *block)
	}
	return report, nil
}
