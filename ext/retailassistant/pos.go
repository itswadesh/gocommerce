package retailassistant

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	counterCode = "counter"

	modeCash   = "cash"
	modeUPI    = "upi"
	modeCredit = "credit"
)

type counterSaleKey struct{}

// counterPayment is how a sale at the counter gets through checkout. It answers
// only to a context this module made, so it is never offered to a shopper: the
// storefront's list of methods and POST /api/checkout/counter both find it not set
// up. It takes no money and proves none; the sale records how it was paid.
type counterPayment struct{}

func (counterPayment) Code() string        { return counterCode }
func (counterPayment) DisplayName() string { return "Counter sale" }
func (counterPayment) Configured(ctx context.Context) bool {
	return ctx.Value(counterSaleKey{}) != nil
}
func (counterPayment) Initiate(context.Context, *gocommerce.Order, gocommerce.PayOptions) (gocommerce.PaymentIntent, error) {
	return gocommerce.PaymentIntent{Kind: gocommerce.IntentNone, Provider: counterCode}, nil
}

// CounterItem is a variant found by what was scanned.
type CounterItem struct {
	VariantID int64            `json:"variant_id"`
	ProductID int64            `json:"product_id"`
	SKU       string           `json:"sku"`
	Barcode   string           `json:"barcode"`
	Title     string           `json:"title"`
	Price     gocommerce.Money `json:"price"`
	Available *int             `json:"available"`
}

// handleLookup finds what a scanner read. A scanner types like a keyboard, so this
// is what its Enter asks. Exact on the barcode or the SKU: a near match is how the
// wrong product gets sold. Two products may share a barcode, and then the cashier
// chooses.
//
// ponytail: no index on variants.barcode, so this scans a store's variants, which
// is fast for a shop's few thousand. An index in core when a catalogue outgrows it.
func (m *Module) handleLookup(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		gocommerce.RespondError(w, r, gocommerce.Validationf("code is what was scanned"))
		return
	}
	ctx := r.Context()
	rows, err := m.db.QueryContext(ctx, `
		SELECT v.id, v.product_id, v.sku, COALESCE(v.barcode, ''), p.title, v.price_minor, v.track_inventory,
		       COALESCE((SELECT sum(s.on_hand - s.reserved) FROM variant_stock s WHERE s.variant_id = v.id), 0)::int
		FROM variants v JOIN products p ON p.id = v.product_id
		WHERE v.active AND p.status = 'active' AND (v.barcode = $1 OR lower(v.sku) = lower($1))
		ORDER BY v.id LIMIT 10`, code)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "look up a scanned code"))
		return
	}
	defer rows.Close()
	cur := m.app.Currency(ctx)
	out := []CounterItem{}
	for rows.Next() {
		var i CounterItem
		var minor int64
		var tracked bool
		var available int
		if err := rows.Scan(&i.VariantID, &i.ProductID, &i.SKU, &i.Barcode, &i.Title, &minor, &tracked, &available); err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "look up a scanned code"))
			return
		}
		i.Price = gocommerce.Money{AmountMinor: minor, Currency: cur}
		if tracked {
			i.Available = &available
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "look up a scanned code"))
		return
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

// handleLinkBarcode teaches the catalogue a barcode it did not know, through the
// catalogue's own service, so nothing here keeps a second list of barcodes.
func (m *Module) handleLinkBarcode(w http.ResponseWriter, r *http.Request) {
	if !mayRead(r.Context(), gocommerce.RightCatalogWrite) {
		gocommerce.RespondError(w, r, gocommerce.Forbiddenf("saving a barcode on a product needs the %s right", gocommerce.RightCatalogWrite))
		return
	}
	var in struct {
		VariantID int64  `json:"variant_id"`
		Barcode   string `json:"barcode"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	code := strings.TrimSpace(in.Barcode)
	if code == "" {
		gocommerce.RespondError(w, r, gocommerce.Validationf("barcode is what was scanned"))
		return
	}
	if _, err := m.app.Products().UpdateVariant(r.Context(), in.VariantID, gocommerce.VariantPatch{Barcode: &code}); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]string{"barcode": code})
}

// SaleLine is one product and how many.
type SaleLine struct {
	VariantID int64 `json:"variant_id"`
	Quantity  int   `json:"quantity"`
}

// SaleRequest is a basket at the counter.
type SaleRequest struct {
	Lines       []SaleLine `json:"lines"`
	DiscountBps int        `json:"discount_bps"`
	Mode        string     `json:"mode"`
	Reference   string     `json:"reference"`
	// MoneySeen is the cashier saying UPI money arrived. Choosing UPI says only
	// that the customer meant to pay that way.
	MoneySeen bool `json:"money_seen"`
}

// counterSettings are what a store set for its counter. The zero discount limit is
// the point: nobody gives money away until the owner has said how much.
type counterSettings struct {
	DiscountLimit int    `plugin:"counter_discount_limit"`
	Country       string `plugin:"counter_country"`
	State         string `plugin:"counter_state"`
}

// newOrder turns a basket into core's order input, with prices the engine works
// out. Prices never come from the screen.
func (m *Module) newOrder(ctx context.Context, in SaleRequest) (gocommerce.NewOrderInput, error) {
	var s counterSettings
	if err := m.app.Plugins().Fill(ctx, pluginKey, &s); err != nil {
		return gocommerce.NewOrderInput{}, err
	}
	if len(in.Lines) == 0 {
		return gocommerce.NewOrderInput{}, gocommerce.Validationf("scan something first")
	}
	if in.DiscountBps < 0 {
		return gocommerce.NewOrderInput{}, gocommerce.Validationf("a discount must not be negative")
	}
	if in.DiscountBps > s.DiscountLimit*100 {
		return gocommerce.NewOrderInput{}, gocommerce.Forbiddenf("the most this counter can take off is %d%%. The owner sets this in the settings", s.DiscountLimit)
	}
	lines := make([]gocommerce.NewOrderLine, len(in.Lines))
	for i, l := range in.Lines {
		lines[i] = gocommerce.NewOrderLine{VariantID: l.VariantID, Quantity: l.Quantity}
		if in.DiscountBps == 0 {
			continue
		}
		v, err := m.app.Products().GetVariant(ctx, l.VariantID)
		if err != nil {
			return gocommerce.NewOrderInput{}, err
		}
		// Half up, the same as a wholesale price, on the unit: a line then says what
		// each one sold for, and its returns can be worked out from that.
		price := (2*v.Price.AmountMinor*(bpsWhole-int64(in.DiscountBps)) + bpsWhole) / (2 * bpsWhole)
		lines[i].UnitPriceMinor = &price
	}
	return gocommerce.NewOrderInput{
		Counter: true, PaymentMethod: counterCode, Lines: lines,
		// Where the sale happened, which is what tax is worked out from.
		Address: gocommerce.Address{Country: s.Country, State: s.State},
	}, nil
}

func counterContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, counterSaleKey{}, true)
}

// handleSalePreview is the total to ask the customer for, from checkout's own
// arithmetic, with nothing placed.
func (m *Module) handleSalePreview(w http.ResponseWriter, r *http.Request) {
	var in SaleRequest
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := counterContext(r.Context())
	order, err := m.newOrder(ctx, in)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	q, err := m.app.Order().Preview(ctx, order)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, q)
}

// SaleResult is what the receipt needs.
type SaleResult struct {
	OrderID       int64            `json:"order_id"`
	Number        string           `json:"number"`
	Total         gocommerce.Money `json:"total"`
	PaymentStatus string           `json:"payment_status"`
	Mode          string           `json:"mode"`
	// Warnings are steps after the sale that did not finish. The sale stands.
	Warnings []string `json:"warnings"`
}

// handleSale records a sale through core's own order path, so stock is committed
// under its locks, exactly once, and nothing here is a second ledger of sales.
//
// What the money did is said honestly. Cash in the drawer is paid. UPI is paid only
// when the cashier saw it arrive, and on credit it is not. An unpaid sale is still
// a sale: confirmed, off the shelf, and not swept away as an abandoned checkout.
//
// The claim row commits first, so a double press or a retry of the same key finds
// it and answers with the same order. A crash between the claim and the order
// leaves a row with no order, which refuses that key and says to check.
func (m *Module) handleSale(w http.ResponseWriter, r *http.Request) {
	var in SaleRequest
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	switch in.Mode {
	case modeCash, modeUPI, modeCredit:
	default:
		gocommerce.RespondError(w, r, gocommerce.Validationf("mode is %s, %s or %s", modeCash, modeUPI, modeCredit))
		return
	}
	key := keyFrom(r)
	if key == nil {
		gocommerce.RespondError(w, r, gocommerce.Validationf("a sale needs an Idempotency-Key, so a double press sells once"))
		return
	}
	ctx := counterContext(r.Context())
	order, err := m.newOrder(ctx, in)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	reference := strings.TrimSpace(in.Reference)
	var rowID int64
	var existing sql.NullInt64
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `
			INSERT INTO retailassistant_counter_sales (idempotency_key, mode, reference, money_seen, discount_bps, created_by)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (store_id, idempotency_key) DO NOTHING RETURNING id`,
			key, in.Mode, reference, in.MoneySeen, in.DiscountBps, actor(ctx)).Scan(&rowID)
		if errors.Is(err, sql.ErrNoRows) {
			return tx.QueryRowContext(ctx, `SELECT id, order_id FROM retailassistant_counter_sales WHERE idempotency_key = $1`, key).Scan(&rowID, &existing)
		}
		return err
	})
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "record a counter sale"))
		return
	}
	if rowID != 0 && existing.Valid {
		o, err := m.app.Order().Get(ctx, existing.Int64)
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		gocommerce.Respond(w, http.StatusOK, saleResult(o, in.Mode, nil))
		return
	}
	placed, err := m.app.Order().Create(ctx, order)
	if err != nil {
		// Nothing was sold, so the same basket can be tried again.
		_, _ = m.db.ExecContext(ctx, `DELETE FROM retailassistant_counter_sales WHERE id = $1 AND order_id IS NULL`, rowID)
		gocommerce.RespondError(w, r, err)
		return
	}
	o := placed.Order
	if _, err := m.db.ExecContext(ctx, `UPDATE retailassistant_counter_sales SET order_id = $2 WHERE id = $1`, rowID, o.ID); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "record a counter sale"))
		return
	}
	warnings := []string{}
	if in.Mode == modeCash || (in.Mode == modeUPI && in.MoneySeen) {
		payRef := reference
		if payRef == "" {
			payRef = "Counter: " + in.Mode
		}
		if paid, err := m.app.Pay().MarkPaid(ctx, o.ID, payRef); err != nil {
			warnings = append(warnings, "The sale is recorded, but it could not be marked paid. Mark it paid on the order.")
		} else {
			o = paid
		}
	}
	// The customer has the goods, so it owes the picking list nothing.
	if _, err := m.app.Ship().Create(ctx, o.ID, gocommerce.ProviderManual, gocommerce.ShipRequest{}); err != nil {
		warnings = append(warnings, "The sale is recorded, but it may still show on the picking list.")
	} else if delivered, err := m.app.Order().MarkDelivered(ctx, o.ID); err != nil {
		warnings = append(warnings, "The sale is recorded, but it could not be marked as handed over.")
	} else {
		o = delivered
	}
	gocommerce.Respond(w, http.StatusCreated, saleResult(o, in.Mode, warnings))
}

func saleResult(o *gocommerce.Order, mode string, warnings []string) SaleResult {
	if warnings == nil {
		warnings = []string{}
	}
	return SaleResult{OrderID: o.ID, Number: o.Number, Total: o.Total, PaymentStatus: o.PaymentStatus, Mode: mode, Warnings: warnings}
}
