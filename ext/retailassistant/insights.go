package retailassistant

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	insightDays    = 30
	slowStockDays  = 90
	lowStockAt     = 5
	insightListLen = 10
	// When the morning summary goes out. India only for now.
	//
	// ponytail: one fixed zone and hour; a store's own time zone and a chosen hour
	// when a store outside India turns this on.
	summaryHourIST = 8
)

var ist = time.FixedZone("IST", 5*60*60+30*60)

// Insights are figures from the ledger and the shop's own records, as of a moment.
// No model is near them. Where the records cannot support a claim, the claim is
// withheld and the reason says what is missing.
type Insights struct {
	AsOf   time.Time   `json:"as_of"`
	Ledger LedgerFacts `json:"ledger"`
	Stock  *StockFacts `json:"stock"`
}

// LedgerFacts are what the supplier ledger says.
type LedgerFacts struct {
	Outstanding        gocommerce.Money `json:"outstanding"`
	Purchases30d       gocommerce.Money `json:"purchases_30d"`
	Paid30d            gocommerce.Money `json:"paid_30d"`
	Unverified         gocommerce.Money `json:"unverified"`
	UnverifiedCount    int              `json:"unverified_count"`
	BillsWaiting       int              `json:"bills_waiting"`
	LinesNotBookedIn   int              `json:"lines_not_booked_in"`
	BankLinesUnmatched int              `json:"bank_lines_unmatched"`
	TopOwed            []OwedSupplier   `json:"top_owed"`
}

// OwedSupplier is one row of who is owed most.
type OwedSupplier struct {
	ID          int64            `json:"id"`
	Name        string           `json:"name"`
	Outstanding gocommerce.Money `json:"outstanding"`
}

// StockFacts are what the catalogue and sales records say. An operator who may not
// read orders and inventory gets none of this.
type StockFacts struct {
	Sales90d int `json:"sales_90d"`
	// FirstSaleAt says how far back the sales records reach.
	FirstSaleAt *time.Time  `json:"first_sale_at"`
	LowStock    []StockItem `json:"low_stock"`
	// SlowStock is empty with a reason when sales records do not reach back far
	// enough to say a product has not sold. No sales recorded is not no sales.
	SlowStock       []StockItem      `json:"slow_stock"`
	SlowWithheld    string           `json:"slow_withheld,omitempty"`
	StockValue      gocommerce.Money `json:"stock_value"`
	ItemsWithNoCost int              `json:"items_with_no_cost"`
}

// StockItem is one variant in a list.
type StockItem struct {
	VariantID int64  `json:"variant_id"`
	ProductID int64  `json:"product_id"`
	SKU       string `json:"sku"`
	Title     string `json:"title"`
	Available int    `json:"available"`
	OnHand    int    `json:"on_hand"`
}

func mayRead(ctx context.Context, rights ...gocommerce.Right) bool {
	su := gocommerce.SuperuserFrom(ctx)
	if su == nil {
		return true
	}
	for _, r := range rights {
		if !su.Has(r) {
			return false
		}
	}
	return true
}

func (m *Module) handleInsights(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := Insights{AsOf: time.Now().UTC()}
	var err error
	if out.Ledger, err = m.ledgerFacts(ctx); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "work out the ledger's figures"))
		return
	}
	if mayRead(ctx, gocommerce.RightOrdersRead, gocommerce.RightInventoryRead) {
		if out.Stock, err = m.stockFacts(ctx); err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "work out the stock's figures"))
			return
		}
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

func (m *Module) ledgerFacts(ctx context.Context) (LedgerFacts, error) {
	cur := m.app.Currency(ctx)
	mk := func(n int64) gocommerce.Money { return gocommerce.Money{AmountMinor: n, Currency: cur} }
	balances, err := m.supplierBalances(ctx)
	if err != nil {
		return LedgerFacts{}, err
	}
	f := LedgerFacts{TopOwed: []OwedSupplier{}}
	var owed int64
	for _, b := range balances {
		if b.Outstanding.Currency != cur {
			continue
		}
		owed += b.Outstanding.AmountMinor
		if b.Outstanding.AmountMinor > 0 {
			f.TopOwed = append(f.TopOwed, OwedSupplier{b.ID, b.Name, b.Outstanding})
		}
	}
	f.Outstanding = mk(owed)
	// Largest first. A shop has few suppliers, so a sort here is no cost.
	for i := 1; i < len(f.TopOwed); i++ {
		for j := i; j > 0 && f.TopOwed[j].Outstanding.AmountMinor > f.TopOwed[j-1].Outstanding.AmountMinor; j-- {
			f.TopOwed[j], f.TopOwed[j-1] = f.TopOwed[j-1], f.TopOwed[j]
		}
	}
	if len(f.TopOwed) > insightListLen {
		f.TopOwed = f.TopOwed[:insightListLen]
	}
	var purchases, paid, unverified int64
	err = m.db.QueryRowContext(ctx, `
		SELECT
		  (SELECT COALESCE(sum(total_minor), 0) FROM retailassistant_purchase_invoices
		   WHERE reversed_at IS NULL AND currency = $1 AND invoice_date >= (now() - make_interval(days => $2))::date),
		  (SELECT COALESCE(sum(amount_minor), 0) FROM retailassistant_supplier_payments
		   WHERE status = 'verified' AND currency = $1 AND paid_on >= (now() - make_interval(days => $2))::date),
		  (SELECT COALESCE(sum(amount_minor), 0) FROM retailassistant_supplier_payments WHERE status = 'provisional' AND currency = $1),
		  (SELECT count(*) FROM retailassistant_supplier_payments p WHERE p.status = 'provisional' AND p.reverses_id IS NULL
		     AND NOT EXISTS (SELECT 1 FROM retailassistant_supplier_payments x WHERE x.reverses_id = p.id)),
		  (SELECT count(*) FROM retailassistant_documents WHERE status = 'needs_review'),
		  (SELECT count(*) FROM retailassistant_purchase_lines l
		   JOIN retailassistant_purchase_invoices i ON i.id = l.invoice_id AND i.reversed_at IS NULL
		   WHERE NOT EXISTS (SELECT 1 FROM retailassistant_stock_receipts x WHERE x.line_id = l.id)),
		  (SELECT count(*) FROM retailassistant_bank_lines WHERE status = 'unmatched')`, cur, insightDays).
		Scan(&purchases, &paid, &unverified, &f.UnverifiedCount, &f.BillsWaiting, &f.LinesNotBookedIn, &f.BankLinesUnmatched)
	if err != nil {
		return LedgerFacts{}, err
	}
	f.Purchases30d, f.Paid30d, f.Unverified = mk(purchases), mk(paid), mk(unverified)
	return f, nil
}

// soldStatuses is what the engine counts as a sale (skills/reports.md).
const soldStatuses = `('confirmed', 'partial', 'shipped', 'delivered')`

// stockTotals is a variant's stock across every location, as core keeps it. A
// variant with no row has none.
const stockTotals = `
	WITH totals AS (
		SELECT v.id, COALESCE(sum(s.on_hand), 0)::int AS on_hand, COALESCE(sum(s.on_hand - s.reserved), 0)::int AS available
		FROM variants v LEFT JOIN variant_stock s ON s.variant_id = v.id GROUP BY v.id
	) `

func (m *Module) stockFacts(ctx context.Context) (*StockFacts, error) {
	cur := m.app.Currency(ctx)
	f := &StockFacts{LowStock: []StockItem{}, SlowStock: []StockItem{}}
	var first sql.NullTime
	var value int64
	err := m.db.QueryRowContext(ctx, stockTotals+`
		SELECT
		  (SELECT count(*) FROM orders WHERE status IN `+soldStatuses+` AND created_at >= now() - make_interval(days => $1)),
		  (SELECT min(created_at) FROM orders WHERE status IN `+soldStatuses+`),
		  (SELECT COALESCE(sum(t.on_hand::bigint * v.cost_minor), 0)::bigint FROM variants v JOIN totals t ON t.id = v.id WHERE v.track_inventory AND t.on_hand > 0),
		  (SELECT count(*) FROM variants v JOIN totals t ON t.id = v.id WHERE v.track_inventory AND t.on_hand > 0 AND v.cost_minor IS NULL)`, slowStockDays).
		Scan(&f.Sales90d, &first, &value, &f.ItemsWithNoCost)
	if err != nil {
		return nil, err
	}
	f.StockValue = gocommerce.Money{AmountMinor: value, Currency: cur}
	if first.Valid {
		f.FirstSaleAt = &first.Time
	}
	if f.LowStock, err = m.stockList(ctx, stockTotals+`
		SELECT v.id, v.product_id, v.sku, p.title, t.available, t.on_hand
		FROM variants v JOIN totals t ON t.id = v.id JOIN products p ON p.id = v.product_id
		WHERE v.track_inventory AND v.active AND t.available <= $1
		ORDER BY t.available, v.id LIMIT $2`, lowStockAt, insightListLen); err != nil {
		return nil, err
	}
	switch {
	case !first.Valid:
		f.SlowWithheld = "No sales are recorded yet, so nothing can be called slow. Record sales to see this."
	case time.Since(first.Time) < slowStockDays*24*time.Hour:
		f.SlowWithheld = "Sales records go back less than " + strconv.Itoa(slowStockDays) + " days, so it is too soon to call a product slow."
	default:
		if f.SlowStock, err = m.stockList(ctx, stockTotals+`
			SELECT v.id, v.product_id, v.sku, p.title, t.available, t.on_hand
			FROM variants v JOIN totals t ON t.id = v.id JOIN products p ON p.id = v.product_id
			WHERE v.track_inventory AND v.active AND t.on_hand > 0
			  AND NOT EXISTS (
			    SELECT 1 FROM order_lines l JOIN orders o ON o.id = l.order_id
			    WHERE l.variant_id = v.id AND o.status IN `+soldStatuses+` AND o.created_at >= now() - make_interval(days => $1))
			ORDER BY t.on_hand DESC, v.id LIMIT $2`, slowStockDays, insightListLen); err != nil {
			return nil, err
		}
	}
	return f, nil
}

func (m *Module) stockList(ctx context.Context, query string, args ...any) ([]StockItem, error) {
	rows, err := m.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StockItem{}
	for rows.Next() {
		var i StockItem
		if err := rows.Scan(&i.VariantID, &i.ProductID, &i.SKU, &i.Title, &i.Available, &i.OnHand); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// SummaryPass queues the day's morning message to each phone that takes
// confirmations, once a store has turned it on. The dedupe key is the day, so a
// restart or the next hour's pass sends nothing twice. Exported for tests.
func (m *Module) SummaryPass(ctx context.Context, now time.Time) error {
	var s struct {
		On bool `plugin:"morning_summary"`
	}
	if err := m.app.Plugins().Fill(ctx, pluginKey, &s); err != nil {
		return err
	}
	local := now.In(ist)
	if !s.On || local.Hour() < summaryHourIST {
		return nil
	}
	f, err := m.ledgerFacts(ctx)
	if err != nil {
		return err
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT phone FROM retailassistant_phone_links
		WHERE notify AND opted_out_at IS NULL AND verified_at IS NOT NULL AND revoked_at IS NULL`)
	if err != nil {
		return err
	}
	var phones []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		phones = append(phones, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	cur := f.Outstanding.Currency
	return gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		for _, phone := range phones {
			key := "summary:" + local.Format("2006-01-02") + ":" + phone
			if err := m.tell(ctx, tx, key, phone, func(lang string) string {
				return phrase(lang, "morning", cur, plainAmount(f.Outstanding.AmountMinor, cur), strconv.Itoa(f.BillsWaiting), strconv.Itoa(f.UnverifiedCount))
			}); err != nil {
				return err
			}
		}
		return nil
	})
}
