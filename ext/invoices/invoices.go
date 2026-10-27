// Package invoices issues a numbered invoice for every paid order.
//
// An order is not an invoice: an order is what a shopper asked for, while an
// invoice is an accounting document with its own gapless sequence and its own
// retention rules. Keeping them apart is why this is a module and not a column
// on the order.
//
// A store in India with a GSTIN in its profile, selling in rupees, issues a
// GST tax invoice instead: the same number series counted by financial year,
// and a frozen document of the tax heads (gst.go). Every other store gets the
// plain invoice it always did.
//
//	app, err := gocommerce.New(cfg,
//	    invoices.New(invoices.Config{
//	        SellerName: "Example Ltd",
//	        NumberFormat: "INV-{year}-{seq:05}",
//	    }),
//	)
package invoices

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/itswadesh/gocommerce/core"
)

// Config configures the module.
type Config struct {
	// SellerName and SellerAddress head the document. Required.
	SellerName    string
	SellerAddress string
	// TaxID is printed alongside the seller, for jurisdictions that need it —
	// a GSTIN, a VAT number, whatever applies.
	TaxID string
	// NumberFormat builds the invoice number. {year} is the issuing year and
	// {seq} the sequence within it; {seq:05} pads to five digits.
	// Defaults to "INV-{year}-{seq:05}".
	//
	// A store issuing GST tax invoices (D137) counts in financial years,
	// which start on 1 April: its sequence restarts then whatever the format
	// says, {year} is the year the financial year started, and {fy} prints
	// it as "26-27". "INV/{fy}/{seq:05}" is the usual Indian shape. GST
	// allows at most 16 letters, digits, "/" and "-", and a number that
	// breaks that is not issued.
	NumberFormat string
	// Footer is printed at the bottom of every invoice.
	Footer string
}

// Module issues invoices.
type Module struct {
	cfg    Config
	app    *gocommerce.App
	db     *sql.DB
	log    *slog.Logger
	orders *gocommerce.Orders
	tpl    *template.Template
	gstTpl *template.Template
	// now is the clock that dates and numbers an invoice; a test sets it to
	// stand either side of 1 April.
	now func() time.Time
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "invoices" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_invoices",
		SQL: `
			CREATE TABLE invoices_documents (
			    id         bigserial   PRIMARY KEY,
			    order_id   bigint      NOT NULL UNIQUE,
			    number     text        NOT NULL UNIQUE,
			    year       integer     NOT NULL,
			    sequence   integer     NOT NULL,
			    issued_at  timestamptz NOT NULL DEFAULT now(),
			    total_minor bigint     NOT NULL,
			    currency   text        NOT NULL,
			    snapshot   jsonb       NOT NULL,
			    UNIQUE (year, sequence)
			);
			CREATE INDEX invoices_documents_issued_idx ON invoices_documents (issued_at DESC);

			-- The sequence lives in a row rather than a PostgreSQL sequence
			-- because an invoice sequence must have no gaps: a rolled-back
			-- transaction has to give its number back, and a sequence would
			-- not.
			CREATE TABLE invoices_counters (
			    year integer PRIMARY KEY,
			    next integer NOT NULL
			);`,
	},
		gocommerce.ScopeMigration("0002_store_scope", "invoices_counters", "invoices_documents"),
		{
			// The GST tax invoice as issued (D137), NULL for a plain
			// invoice. invoices_counters.year is now the series year: the
			// calendar year for a plain store, the year its financial year
			// started for a GST one.
			ID: "0003_tax_doc",
			SQL: `
				ALTER TABLE invoices_documents ADD COLUMN tax_doc jsonb;
				-- Rule 46: a tax invoice's serial is at most 16 letters, digits, '-' and '/'.
				ALTER TABLE invoices_documents ADD CONSTRAINT invoices_documents_gst_number
				    CHECK (tax_doc IS NULL OR number ~ '^[A-Za-z0-9/-]{1,16}$');`,
		},
		{
			// The store's Tally ledger names (D127), one row per store; no
			// row reads as the defaults.
			ID: "0004_tally_ledgers",
			SQL: `
				CREATE TABLE invoices_tally_ledgers (
				    store_id   text        PRIMARY KEY,
				    names      jsonb       NOT NULL,
				    updated_at timestamptz NOT NULL DEFAULT now()
				);`,
		},
		gocommerce.ScopeMigration("0005_tally_store_scope", "invoices_tally_ledgers"),
	}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	// After the configuration check, not before: a module that refuses to
	// register has no rights to bring, and the test that proves the refusal
	// passes a nil App.
	if strings.TrimSpace(m.cfg.SellerName) == "" {
		return errors.New("invoices: SellerName is required")
	}
	m.registerRights(app)
	if m.cfg.NumberFormat == "" {
		m.cfg.NumberFormat = "INV-{year}-{seq:05}"
	}
	m.app = app
	m.db = app.DB()
	m.log = app.Log()
	m.orders = app.Order()
	if m.now == nil {
		m.now = time.Now
	}
	// A warning, not a refusal: a store outside India may number however it
	// likes, and the issue path refuses the number on a GST store anyway.
	if longest := formatNumber(m.cfg.NumberFormat, 2026, 999999); !gstNumber.MatchString(longest) {
		m.log.Warn("invoices: this number format can break GST's 16-character serial, so a store issuing GST tax invoices will fail to issue one",
			"format", m.cfg.NumberFormat, "example", longest)
	}

	tpl, err := template.New("invoice").Parse(invoiceHTML)
	if err != nil {
		return fmt.Errorf("invoices: parse template: %w", err)
	}
	m.tpl = tpl
	if m.gstTpl, err = template.New("tax-invoice").Funcs(gstFuncs).Parse(taxInvoiceHTML); err != nil {
		return fmt.Errorf("invoices: parse the tax invoice template: %w", err)
	}

	app.Subscribe(gocommerce.EventOrderPaid, m.onOrderPaid)
	app.RegisterCheckoutGuard(m.gstGuard)

	// An invoice is a rendering of a paid order, and its snapshot embeds the
	// buyer's name, email and address. Whoever may read the order may read it;
	// whoever may not, may not.
	app.HandleAdminFunc("GET /api/admin/x/invoices", m.handleList, rightInvoicesRead)
	app.HandleAdminFunc("GET /api/admin/x/invoices/{orderId}", m.handleGet, rightInvoicesRead)
	app.HandleAdminFunc("GET /api/admin/x/invoices/tally", m.handleTallyExport, rightInvoicesRead)
	app.HandleAdminFunc("GET /api/admin/x/invoices/tally/ledgers", m.handleTallyLedgersGet, rightInvoicesRead)
	app.HandleAdminFunc("PUT /api/admin/x/invoices/tally/ledgers", m.handleTallyLedgersPut, rightInvoicesConfigure)

	// Delivery is at-least-once but not guaranteed to have happened before a
	// crash, and an invoice that was never issued is an accounting hole. So
	// the module also reconciles against the orders table at startup rather
	// than trusting that it saw every event.
	app.OnStart(func(ctx context.Context) error {
		// Every store's paid orders, each bound to its own store (D83).
		return app.ForEachStore(ctx, m.reconcile)
	})
	return nil
}

// onOrderPaid issues the invoice. It is idempotent because event delivery is
// at-least-once: the unique constraint on order_id is what makes a redelivery
// a no-op rather than a duplicate document.
func (m *Module) onOrderPaid(ctx context.Context, e gocommerce.Event) error {
	var ev gocommerce.OrderEvent
	if err := e.Decode(&ev); err != nil {
		return err
	}
	return m.issue(ctx, ev.OrderID)
}

func (m *Module) issue(ctx context.Context, orderID int64) error {
	// A sample order (D96) is one nobody placed, and an invoice number is the
	// tax office's, issued without gaps: none goes to it.
	var exists, sample bool
	if err := m.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM invoices_documents WHERE order_id = $1),
		       EXISTS (SELECT 1 FROM orders WHERE id = $1 AND sample)`, orderID).Scan(&exists, &sample); err != nil {
		return err
	}
	if exists || sample {
		return nil
	}

	order, err := m.orders.Get(ctx, orderID)
	if err != nil {
		return err
	}
	snapshot, err := json.Marshal(order)
	if err != nil {
		return err
	}
	reg, err := m.regimeFor(ctx)
	if err != nil {
		return err
	}
	var shares []int64
	var hsn map[int64]string
	if reg != nil {
		if shares, err = m.orders.LineDiscountShares(ctx, order); err != nil {
			return err
		}
		if hsn, err = m.hsnFor(ctx, orderID); err != nil {
			return err
		}
	}

	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		now := m.now()
		year := seriesYear(now, reg)

		// Take the next number under a row lock. The UPDATE locks the year's
		// counter row, so two concurrent deliveries queue rather than both
		// being handed the same number — and because it is inside this
		// transaction, a rollback gives the number back and the sequence
		// stays gapless.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO invoices_counters (year, next) VALUES ($1, 1)
			 ON CONFLICT (store_id, year) DO NOTHING`, year); err != nil {
			return err
		}
		var seq int
		if err := tx.QueryRowContext(ctx,
			`UPDATE invoices_counters SET next = next + 1 WHERE year = $1 RETURNING next - 1`,
			year).Scan(&seq); err != nil {
			return err
		}

		number := formatNumber(m.cfg.NumberFormat, year, seq)
		// Returning an error here rolls the number back, so a refusal leaves
		// no gap; the paid order stays without an invoice, logged, until the
		// cause is fixed and redelivery or the startup reconcile issues it.
		var taxDoc []byte
		if reg != nil {
			if !gstNumber.MatchString(number) {
				m.log.Error("the invoice number format breaks GST's 16-character serial; no tax invoice was issued",
					"order_id", orderID, "number", number)
				return fmt.Errorf("invoice number %q is not a GST serial: at most 16 letters, digits, / and -", number)
			}
			doc, err := buildTaxInvoice(taxInput{
				Number: number, IssuedAt: now, Regime: *reg, Order: order,
				DiscountShares: shares, HSN: hsn,
			})
			if err != nil {
				m.log.Error("could not build the GST tax invoice; none was issued", "order_id", orderID, "error", err)
				return err
			}
			if taxDoc, err = json.Marshal(doc); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO invoices_documents (order_id, number, year, sequence, total_minor, currency, snapshot, issued_at, tax_doc)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (store_id, order_id) DO NOTHING`,
			orderID, number, year, seq, order.Total.AmountMinor, order.Currency, snapshot, now, taxDoc)
		if err != nil {
			return err
		}
		// Another delivery issued this order's invoice after the check above.
		// Committing would keep the number this one took and leave a hole in
		// the series, so roll it back instead.
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return errAlreadyIssued
		}
		m.log.Info("invoice issued", "invoice", number, "order", order.Number)
		return nil
	})
	if errors.Is(err, errAlreadyIssued) {
		return nil
	}
	return err
}

var errAlreadyIssued = errors.New("invoice already issued")

// hsnFor is the HS code of each variant on the order as it stands at issue:
// the supplier's classification for the document, read from the catalogue.
func (m *Module) hsnFor(ctx context.Context, orderID int64) (map[int64]string, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT v.id, v.hs_code FROM order_lines l JOIN variants v ON v.id = l.variant_id
		WHERE l.order_id = $1`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var code string
		if err := rows.Scan(&id, &code); err != nil {
			return nil, err
		}
		out[id] = code
	}
	return out, rows.Err()
}

// reconcile issues invoices for paid orders that have none — the safety net
// under at-least-once delivery, because an event that was lost before this
// module ever ran would otherwise leave a permanent gap.
func (m *Module) reconcile(ctx context.Context) error {
	rows, err := m.db.QueryContext(ctx, `
		SELECT o.id FROM orders o
		LEFT JOIN invoices_documents i ON i.order_id = o.id
		WHERE o.payment_status = 'paid' AND i.id IS NULL AND NOT o.sample
		ORDER BY o.id LIMIT 500`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, id := range ids {
		if err := m.issue(ctx, id); err != nil {
			m.log.Error("could not issue a missing invoice", "order_id", id, "error", err)
		}
	}
	if len(ids) > 0 {
		m.log.Info("issued invoices missing for paid orders", "count", len(ids))
	}
	return nil
}

// formatNumber expands {year}, {fy} and {seq}, with optional zero padding.
func formatNumber(format string, year, seq int) string {
	out := strings.ReplaceAll(format, "{year}", strconv.Itoa(year))
	out = strings.ReplaceAll(out, "{fy}", fmt.Sprintf("%02d-%02d", year%100, (year+1)%100))
	if i := strings.Index(out, "{seq:"); i >= 0 {
		if j := strings.Index(out[i:], "}"); j > 0 {
			spec := out[i+len("{seq:") : i+j]
			width, err := strconv.Atoi(spec)
			if err == nil {
				out = out[:i] + fmt.Sprintf("%0*d", width, seq) + out[i+j+1:]
			}
		}
	}
	return strings.ReplaceAll(out, "{seq}", strconv.Itoa(seq))
}

// ---------------------------------------------------------------- endpoints

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var total int
	if err := m.db.QueryRowContext(r.Context(),
		`SELECT count(*) FROM invoices_documents`).Scan(&total); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	rows, err := m.db.QueryContext(r.Context(), `
		SELECT id, order_id, number, issued_at, total_minor, currency, tax_doc IS NOT NULL
		FROM invoices_documents ORDER BY id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	defer rows.Close()

	type row struct {
		ID       int64            `json:"id"`
		OrderID  int64            `json:"order_id"`
		Number   string           `json:"number"`
		IssuedAt time.Time        `json:"issued_at"`
		Total    gocommerce.Money `json:"total"`
		Kind     string           `json:"kind,omitempty"`
	}
	list := []row{}
	for rows.Next() {
		var v row
		var tax bool
		if err := rows.Scan(&v.ID, &v.OrderID, &v.Number, &v.IssuedAt,
			&v.Total.AmountMinor, &v.Total.Currency, &tax); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		if tax {
			v.Kind = "tax_invoice"
		}
		list = append(list, v)
	}
	gocommerce.RespondList(w, list, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (m *Module) handleGet(w http.ResponseWriter, r *http.Request) {
	orderID, err := strconv.ParseInt(r.PathValue("orderId"), 10, 64)
	if err != nil || orderID <= 0 {
		gocommerce.RespondError(w, r, gocommerce.Validationf("orderId must be a positive integer"))
		return
	}

	var number string
	var issuedAt time.Time
	var snapshot, taxDoc []byte
	err = m.db.QueryRowContext(r.Context(),
		`SELECT number, issued_at, snapshot, tax_doc FROM invoices_documents WHERE order_id = $1`,
		orderID).Scan(&number, &issuedAt, &snapshot, &taxDoc)
	if errors.Is(err, sql.ErrNoRows) {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no invoice has been issued for order %d", orderID))
		return
	}
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}

	var order gocommerce.Order
	if err := json.Unmarshal(snapshot, &order); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// On the way out, never on the way in: issue() stores this snapshot as the
	// order actually was, and masking there would write bullets into the
	// document a real store has to be able to produce. Both shapes below read
	// from here, so the JSON and the printable page mask together.
	m.app.MaskOrder(r.Context(), &order)

	// JSON for a client that wants the data, HTML for a human who wants to
	// print it. A tax invoice is both from the document as issued, never from
	// the order or the profile as they are now; it holds no buyer email or
	// phone, so the mask above has nothing in it to hide.
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		body := map[string]any{"number": number, "issued_at": issuedAt, "order": order}
		if taxDoc != nil {
			body["tax"] = json.RawMessage(taxDoc)
		}
		gocommerce.Respond(w, http.StatusOK, body)
		return
	}

	if taxDoc != nil {
		var doc TaxInvoice
		if err := json.Unmarshal(taxDoc, &doc); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := m.gstTpl.Execute(w, doc); err != nil {
			m.log.Error("could not render the tax invoice", "order_id", orderID, "error", err)
		}
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := m.tpl.Execute(w, map[string]any{
		"Number": number, "IssuedAt": issuedAt.UTC().Format("2 January 2006"),
		"Order": order, "Config": m.cfg,
	}); err != nil {
		m.log.Error("could not render the invoice", "order_id", orderID, "error", err)
	}
}

const invoiceHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>Invoice {{.Number}}</title>
<style>
 body{font-family:system-ui,-apple-system,"Segoe UI",sans-serif;max-width:46rem;margin:3rem auto;padding:0 1.5rem;color:#111;line-height:1.5}
 header{display:flex;justify-content:space-between;align-items:flex-start;gap:2rem;border-bottom:2px solid #111;padding-bottom:1rem}
 h1{font-size:1.5rem;margin:0 0 .25rem}
 .muted{color:#555;font-size:.875rem}
 table{width:100%;border-collapse:collapse;margin:2rem 0}
 th,td{text-align:left;padding:.5rem 0;border-bottom:1px solid #ddd}
 td.num,th.num{text-align:right;font-variant-numeric:tabular-nums}
 tfoot td{border:0;padding-top:.4rem}
 tfoot tr:last-child td{font-weight:700;border-top:2px solid #111;padding-top:.6rem}
 footer{margin-top:3rem;color:#555;font-size:.8125rem}
</style></head>
<body>
<header>
  <div>
    <h1>{{.Config.SellerName}}</h1>
    <div class="muted">{{.Config.SellerAddress}}{{if .Config.TaxID}}<br>{{.Config.TaxID}}{{end}}</div>
  </div>
  <div class="muted" style="text-align:right">
    <strong>Invoice {{.Number}}</strong><br>
    Issued {{.IssuedAt}}<br>
    Order {{.Order.Number}}
  </div>
</header>

<p><strong>Billed to</strong><br>
{{with .Order}}{{.Name}}<br>{{.Email}}<br>
{{.Address.Line1}}{{if .Address.Line2}}, {{.Address.Line2}}{{end}}<br>
{{.Address.City}} {{.Address.PostalCode}}<br>{{.Address.Country}}{{end}}</p>

<table>
  <thead><tr><th>Item</th><th class="num">Qty</th><th class="num">Unit</th><th class="num">Total</th></tr></thead>
  <tbody>
  {{range .Order.Lines}}
    <tr>
      <td>{{.Title}}{{if .VariantLabel}} <span class="muted">({{.VariantLabel}})</span>{{end}}<br><span class="muted">{{.SKU}}</span></td>
      <td class="num">{{.Quantity}}</td>
      <td class="num">{{.UnitPrice.Currency}} {{.UnitPrice.AmountMinor}}</td>
      <td class="num">{{.Total.Currency}} {{.Total.AmountMinor}}</td>
    </tr>
  {{end}}
  </tbody>
  <tfoot>
    <tr><td colspan="3" class="num">Subtotal</td><td class="num">{{.Order.Subtotal.Currency}} {{.Order.Subtotal.AmountMinor}}</td></tr>
    {{if .Order.Shipping.AmountMinor}}<tr><td colspan="3" class="num">Shipping</td><td class="num">{{.Order.Shipping.Currency}} {{.Order.Shipping.AmountMinor}}</td></tr>{{end}}
    {{if .Order.Discount.AmountMinor}}<tr><td colspan="3" class="num">Discount</td><td class="num">-{{.Order.Discount.Currency}} {{.Order.Discount.AmountMinor}}</td></tr>{{end}}
    <tr><td colspan="3" class="num">Total</td><td class="num">{{.Order.Total.Currency}} {{.Order.Total.AmountMinor}}</td></tr>
  </tfoot>
</table>

<p class="muted">Amounts are shown in minor units of {{.Order.Currency}}.</p>
{{if .Config.Footer}}<footer>{{.Config.Footer}}</footer>{{end}}
</body></html>
`

// The rights this module's own screens are gated on.
//
// They were core's until now — orders.read —
// which meant the permission could not be given without the thing it was
// borrowed from, and the screens had no row of their own on the Roles grid.
// Default names the roles that held the old right, so nobody's access moves.
const (
	rightInvoicesRead      gocommerce.Right = "invoices.read" // was orders.read
	rightInvoicesConfigure gocommerce.Right = "invoices.configure"
)

func (m *Module) registerRights(app *gocommerce.App) {
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightInvoicesRead,
		Label:   "See invoices",
		Scope:   "The numbered invoice for a paid order",
		Default: []string{gocommerce.RoleManager, gocommerce.RoleStaff},
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightInvoicesConfigure,
		Label:   "Set the Tally ledger names",
		Scope:   "The ledger names a GST store's Tally export uses",
		Default: []string{gocommerce.RoleManager},
	})
}
