package gocommerce

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// CSV import and export. Spreadsheets are the actual tool most operators reach
// for, so the format is denormalised — one row per variant, one row per order
// line — rather than shaped for a machine that could have used the API.

// productCSVHeader is the export format, and the set of columns import
// understands. Versioning it by column name means a file written by an older
// release still imports as long as the columns it has are still meaningful.
//
// The stock column is the one that is not fixed. A spreadsheet has one cell per
// variant per column and stock has a place now, so the header carries the place:
// see stockColumnPrefix.
//
// The same model has a second dialect, Shopify's, in transfer_shopify.go:
// every column here has a home there and back, so a file goes either way
// between the two stores without an edit. Pictures are URLs — `images` is
// the product's, `|`-separated and on its first row, with `image_alts`
// beside them; `variant_images` is a variant's own.
var productCSVHeader = []string{
	"product_slug", "product_title", "product_description", "product_status",
	"vendor", "product_type", "tags", "category", "seo_title", "seo_description",
	"sku", "barcode", "variant_options", "price_minor", "compare_at_price_minor", "cost_minor", "taxable", "requires_shipping",
	"stock_on_hand", "track_inventory", "continue_selling", "active",
	"weight_grams", "weight_unit", "origin_country", "hs_code",
	"images", "image_alts", "variant_images", "metadata",
}

// stockColumnPrefix names a location inside a column heading:
// `stock_on_hand:warehouse` is the count at the location whose code is
// `warehouse`.
//
// The bare `stock_on_hand` still means the default location, so every file ever
// written by this exporter, and every file an operator has in a folder
// somewhere, still imports and still means what it meant. A store with one
// location — which is most of them — never sees a suffix at all.
//
// The location is named by code rather than by id because a CSV is a document
// people edit and mail to each other. `warehouse` survives being re-imported
// into a different store; `3` silently means something else there.
const stockColumnPrefix = "stock_on_hand:"

// stockColumn ties one CSV column to one location.
type stockColumn struct {
	name       string
	code       string
	locationID int64
	// active rides along because the import refuses a cell that raises a count
	// at a closed location (D44). Resolved once against the header rather than
	// once per row, for the reason the misspelt-code check above is.
	active bool
}

// productStockColumns is the stock part of the export header.
//
// One column per location, always — not one per location that currently holds
// something. The header would otherwise change shape as stock moved, which
// breaks anyone diffing two exports or scripting against the columns, and it
// would leave no cell to type into to receive stock somewhere empty.
func (t *Transfer) productStockColumns(ctx context.Context) ([]stockColumn, error) {
	rows, err := t.app.db.QueryContext(ctx,
		`SELECT id, code, is_default, active FROM locations ORDER BY priority, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []stockColumn
	var single stockColumn
	n := 0
	for rows.Next() {
		var c stockColumn
		var isDefault bool
		if err := rows.Scan(&c.locationID, &c.code, &isDefault, &c.active); err != nil {
			return nil, err
		}
		c.name = stockColumnPrefix + c.code
		cols = append(cols, c)
		if isDefault {
			single = c
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if n == 1 {
		// A single-location store gets exactly the file it got before locations
		// existed, byte for byte.
		single.name = "stock_on_hand"
		return []stockColumn{single}, nil
	}
	return cols, nil
}

// resolveStockColumns works out which locations a file's stock columns name.
//
// It runs once, against the header, rather than per row. A misspelt location
// code is one mistake in one header cell and it affects every line in the file,
// so it fails the import with a sentence naming the code — the same treatment a
// missing required column already gets. Reporting it a thousand times, once per
// row, would bury the one fact the operator needs.
func (t *Transfer) resolveStockColumns(ctx context.Context, header []string) ([]stockColumn, error) {
	var named []string
	plain := false
	for _, raw := range header {
		name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff")))
		switch {
		case name == "stock_on_hand":
			plain = true
		case strings.HasPrefix(name, stockColumnPrefix):
			named = append(named, strings.TrimPrefix(name, stockColumnPrefix))
		}
	}

	if plain && len(named) > 0 {
		return nil, Validationf(
			"the CSV has both %q and per-location stock columns; use one form or the other, "+
				"because there is no way to tell which one a row means",
			"stock_on_hand")
	}
	if !plain && len(named) == 0 {
		// No stock columns at all is fine and common — a price list, say. The
		// import simply does not touch stock.
		return nil, nil
	}

	if plain {
		var c stockColumn
		err := t.app.db.QueryRowContext(ctx,
			`SELECT id, code, active FROM locations WHERE is_default`).Scan(&c.locationID, &c.code, &c.active)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, Conflictf("this store has no default location")
		}
		if err != nil {
			return nil, err
		}
		c.name = "stock_on_hand"
		return []stockColumn{c}, nil
	}

	cols := make([]stockColumn, 0, len(named))
	for _, code := range named {
		var c stockColumn
		err := t.app.db.QueryRowContext(ctx,
			`SELECT id, code, active FROM locations WHERE code = $1`, code).Scan(&c.locationID, &c.code, &c.active)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, Validationf(
				"the CSV column %q names a location that does not exist; "+
					"open it first, or fix the code",
				stockColumnPrefix+code)
		}
		if err != nil {
			return nil, err
		}
		c.name = stockColumnPrefix + c.code
		cols = append(cols, c)
	}
	return cols, nil
}

// productHeaderFor is productCSVHeader with the stock column expanded.
func productHeaderFor(cols []stockColumn) []string {
	out := make([]string, 0, len(productCSVHeader)+len(cols)-1)
	for _, name := range productCSVHeader {
		if name != "stock_on_hand" {
			out = append(out, name)
			continue
		}
		for _, c := range cols {
			out = append(out, c.name)
		}
	}
	return out
}

// customerCSVHeader is the mailing-list shape: one row per person, which is
// what a campaign tool, a loyalty spreadsheet and an accountant asking "who are
// our best customers" all want. There is no importer for it and there will not
// be one — a customer is a reading of the orders (customers.go), so a row here
// describes something that has no table to be written back into.
//
// spent_minor and currency rather than a formatted amount, for AGENTS rule 6:
// how many decimals a currency has is the reader's business, and a spreadsheet
// that received "£12.50" could not add the column up.
var customerCSVHeader = []string{
	"email", "name", "phone",
	"address_line1", "address_line2", "city", "state", "postal_code", "country",
	"orders", "spent_minor", "currency", "first_order_at", "last_order_at",
}

var orderCSVHeader = []string{
	"number", "created_at", "status", "payment_status", "payment_provider",
	"currency", "email", "phone", "name",
	"address_line1", "address_line2", "city", "state", "postal_code", "country",
	"subtotal_minor", "shipping_minor", "discount_minor", "total_minor", "language",
	"sku", "title", "variant_label", "quantity", "unit_price_minor", "line_total_minor",
}

// ImportResult reports what an import did. Row errors never abort the file:
// one bad line in a thousand should not cost the other 999.
type ImportResult struct {
	Created  int        `json:"created"`
	Updated  int        `json:"updated"`
	Skipped  int        `json:"skipped"`
	Errors   []RowError `json:"errors"`
	DryRun   bool       `json:"dry_run"`
	Duration string     `json:"duration,omitempty"`
	// Format is the dialect the file turned out to be in.
	Format Format `json:"format,omitempty"`
}

// RowError names the line so an operator can go and fix it.
type RowError struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// Transfer owns CSV import and export.
type Transfer struct {
	app *App
}

// Data returns the import/export service.
func (a *App) Data() *Transfer { return a.transfer }

// ------------------------------------------------------------------ export

// ExportOrders streams orders as CSV, one row per order line, with the order's
// own columns repeated in the store's layout and on the first line only in
// Shopify's. That shape is what an accountant's pivot table wants.
func (t *Transfer) ExportOrders(ctx context.Context, out io.Writer, q OrderQuery, opts ExportOptions) error {
	w := csv.NewWriter(out)
	defer w.Flush()
	header := orderCSVHeader
	if opts.Format == FormatShopify {
		header = shopifyOrderHeader
	}
	if err := w.Write(header); err != nil {
		return err
	}

	where, args := []string{"1 = 1"}, []any{}
	add := func(expr string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(expr, len(args)))
	}
	if q.Status != "" {
		add("o.status = $%d", q.Status)
	}
	// The same predicate the listing uses, from the same function: an export
	// taken from a filtered screen must be the rows on that screen. The
	// statement below aliases orders as `o`, which is what lets it transfer.
	if clause := orderSearchClause(q.Search, &args); clause != "" {
		where = append(where, clause)
	}
	if q.From != nil {
		add("o.created_at >= $%d", *q.From)
	}
	if q.To != nil {
		add("o.created_at < $%d", *q.To)
	}

	rows, err := t.app.db.QueryContext(ctx, `
		SELECT o.id, o.number, o.created_at, o.updated_at, o.status, o.payment_status, o.payment_provider,
		       coalesce(o.payment_reference, ''), o.currency, o.email, coalesce(o.phone, ''), coalesce(o.name, ''), o.address,
		       o.subtotal_minor, o.shipping_minor, o.discount_minor, o.tax_minor, o.total_minor, o.refunded_minor,
		       o.lang, o.shipping_method,
		       coalesce((SELECT d.code FROM order_discounts d WHERE d.order_id = o.id ORDER BY d.id LIMIT 1), ''),
		       l.sku, l.title, l.variant_label, l.quantity, l.unit_price_minor, l.total_minor
		FROM orders o
		JOIN order_lines l ON l.order_id = o.id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY o.id, l.id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	var lastOrder int64
	for rows.Next() {
		var l exportOrderLine
		var addrRaw []byte
		if err := rows.Scan(&l.id, &l.number, &l.createdAt, &l.updatedAt, &l.status, &l.payStatus, &l.provider,
			&l.reference, &l.currency, &l.email, &l.phone, &l.name, &addrRaw,
			&l.subtotal, &l.shipping, &l.discount, &l.tax, &l.total, &l.refunded,
			&l.lang, &l.shippingMethod, &l.discountCode,
			&l.sku, &l.title, &l.label, &l.qty, &l.unitPrice, &l.lineTotal); err != nil {
			return err
		}
		_ = json.Unmarshal(addrRaw, &l.addr)
		first := l.id != lastOrder
		lastOrder = l.id

		var record []string
		if opts.Format == FormatShopify {
			record = shopifyOrderRow(l, first, currencyExponent(l.currency))
		} else {
			record = []string{
				l.number, l.createdAt.UTC().Format(time.RFC3339), l.status, l.payStatus, l.provider,
				l.currency, l.email, l.phone, l.name,
				l.addr.Line1, l.addr.Line2, l.addr.City, l.addr.State, l.addr.PostalCode, l.addr.Country,
				strconv.FormatInt(l.subtotal, 10), strconv.FormatInt(l.shipping, 10),
				strconv.FormatInt(l.discount, 10), strconv.FormatInt(l.total, 10), l.lang,
				l.sku, l.title, l.label, strconv.Itoa(l.qty),
				strconv.FormatInt(l.unitPrice, 10), strconv.FormatInt(l.lineTotal, 10),
			}
		}
		if err := w.Write(escapeRecord(record)); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	w.Flush()
	return w.Error()
}

// ExportCustomers streams the customer reading as CSV, one row per person.
//
// It re-runs the grouping rather than paging [Orders.Customers], because an
// export is every row that matched and the service answers with a page. What it
// must not do is re-derive what a customer IS: the aggregates below are the
// same four consts the listing selects and sorts by, and the predicate is the
// listing's own function — so the figure in the file and the figure on the
// screen cannot come to differ, which is the failure this shape exists to
// prevent (ExportOrders says the same thing about its search).
func (t *Transfer) ExportCustomers(ctx context.Context, out io.Writer, q CustomerQuery, opts ExportOptions) error {
	w := csv.NewWriter(out)
	defer w.Flush()
	header := customerCSVHeader
	if opts.Format == FormatShopify {
		header = shopifyCustomerHeader
	}
	if err := w.Write(header); err != nil {
		return err
	}

	where, args := []string{"o.email <> ''"}, []any{}
	if clause := customerSearchClause(q.Search, &args); clause != "" {
		where = append(where, clause)
	}
	// Validationf, returned bare: wrapping it in Internalf would serve a bad
	// sort field as a 500. The handler turns it into a 400 before a single CSV
	// header byte is written, which is the only moment it still can.
	order, err := customerSorts.Clause(q.Sort, customerLastOrder+" DESC, lower(o.email) ASC")
	if err != nil {
		return err
	}

	rows, err := t.app.db.QueryContext(ctx, `
		SELECT lower(o.email),
		       `+customerOrderCount+`,
		       `+customerSpent+`,
		       `+customerFirstOrder+`, `+customerLastOrder+`,
		       (array_agg(coalesce(o.name, '')  ORDER BY o.id DESC))[1],
		       (array_agg(coalesce(o.phone, '') ORDER BY o.id DESC))[1],
		       (array_agg(o.address             ORDER BY o.id DESC))[1]
		FROM orders o
		WHERE `+strings.Join(where, " AND ")+`
		GROUP BY lower(o.email)
		ORDER BY `+order, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	currency := t.app.cfg.Currency
	for rows.Next() {
		var c exportCustomer
		var addrRaw []byte
		if err := rows.Scan(&c.email, &c.orders, &c.spent, &c.first, &c.last,
			&c.name, &c.phone, &addrRaw); err != nil {
			return err
		}
		_ = json.Unmarshal(addrRaw, &c.addr)

		var record []string
		if opts.Format == FormatShopify {
			record = shopifyCustomerRow(c, currencyExponent(currency))
		} else {
			record = []string{
				c.email, c.name, c.phone,
				c.addr.Line1, c.addr.Line2, c.addr.City, c.addr.State, c.addr.PostalCode, c.addr.Country,
				strconv.Itoa(c.orders), strconv.FormatInt(c.spent, 10), currency,
				c.first.UTC().Format(time.RFC3339), c.last.UTC().Format(time.RFC3339),
			}
		}
		if err := w.Write(escapeRecord(record)); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	w.Flush()
	return w.Error()
}

// ------------------------------------------------------------------ import

// ImportOrders loads historical orders, for a migration from another
// platform — in the store's own layout or Shopify's, whichever the header
// announces.
//
// It does not touch inventory — the stock movements happened on the old system
// — and it fires no events unless asked, because importing five thousand
// orders must not send five thousand confirmation emails.
func (t *Transfer) ImportOrders(ctx context.Context, in io.Reader, opts ImportOptions) (*ImportResult, error) {
	start := time.Now()
	r := csv.NewReader(in)
	r.FieldsPerRecord = -1

	header, err := r.Read()
	if err != nil {
		return nil, Validationf("could not read the CSV header: %v", err)
	}
	cols := indexColumns(header)
	format := opts.Format
	if format == "" {
		format = detectOrderFormat(cols)
	}
	var translate *shopifyOrderReader
	switch format {
	case FormatShopify:
		for _, required := range []string{"name", "lineitem quantity", "lineitem price"} {
			if _, ok := cols[required]; !ok {
				return nil, Validationf("a Shopify orders file needs the column %q", required)
			}
		}
		translate = newShopifyOrderReader(cols, t.app.cfg.Currency)
		cols = translate.out
	default:
		for _, required := range []string{"number", "email", "sku", "quantity", "unit_price_minor"} {
			if _, ok := cols[required]; !ok {
				return nil, Validationf("the CSV is missing the required column %q", required)
			}
		}
	}

	result := &ImportResult{DryRun: opts.DryRun, Format: format}
	var group []csvRow
	var groupNumber string

	flush := func() {
		if len(group) == 0 {
			return
		}
		t.importOrderGroup(ctx, group, opts.DryRun, opts.FireEvents, result)
		group = nil
	}

	line := 1
	for {
		record, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line++
		if err != nil {
			result.Errors = append(result.Errors, RowError{Line: line, Message: err.Error()})
			continue
		}
		row := csvRow{line: line, cols: cols, values: unescapeRecord(record)}
		if translate != nil {
			if row, err = translate.row(line, unescapeRecord(record)); err != nil {
				result.Errors = append(result.Errors, RowError{Line: line, Message: err.Error()})
				continue
			}
		}
		number := row.get("number")
		if number == "" {
			result.Errors = append(result.Errors, RowError{Line: line, Message: "number is empty"})
			continue
		}
		if number != groupNumber {
			flush()
			groupNumber = number
		}
		group = append(group, row)
	}
	flush()

	result.Duration = time.Since(start).String()
	return result, nil
}

// importedRefunded is what an imported order has already sent back. The CSV
// carries no refund column, so `refunded` is the only thing it can say and it
// says all of it — which is what the word meant before refunds were recorded
// one at a time. Without this an imported refunded order would read "fully
// refunded" and "nothing came back" at once, and the doctor would rightly fail
// on it.
func importedRefunded(paymentStatus string, total int64) int64 {
	if paymentStatus == PaymentRefunded {
		return total
	}
	return 0
}

func (t *Transfer) importOrderGroup(ctx context.Context, rows []csvRow, dryRun, fireEvents bool, result *ImportResult) {
	first := rows[0]
	number := first.get("number")

	err := InTx(ctx, t.app.db, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM orders WHERE number = $1)`, number).Scan(&exists); err != nil {
			return err
		}
		if exists {
			// Re-importing a file the store itself exported is a no-op rather
			// than a duplicate, which makes the round trip safe to repeat.
			result.Skipped++
			return nil
		}

		status := first.get("status")
		if status == "" {
			status = OrderConfirmed
		}
		payStatus := first.get("payment_status")
		if payStatus == "" {
			payStatus = PaymentPaid
		}
		provider := first.get("payment_provider")
		if provider == "" {
			provider = "imported"
		}
		lang := first.get("language")
		if lang == "" {
			lang = t.app.cfg.DefaultLanguage
		}
		createdAt := time.Now().UTC()
		if v := first.get("created_at"); v != "" {
			if parsed, err := time.Parse(time.RFC3339, v); err == nil {
				createdAt = parsed
			}
		}
		addr, _ := json.Marshal(Address{
			Line1: first.get("address_line1"), Line2: first.get("address_line2"),
			City: first.get("city"), State: first.get("state"),
			PostalCode: first.get("postal_code"), Country: first.get("country"),
		})

		var subtotal int64
		type line struct {
			sku, title, label string
			qty               int
			unit, total       int64
		}
		var lines []line
		for _, row := range rows {
			qty, err := row.intDefault("quantity", 0)
			if err != nil || qty <= 0 {
				return fmt.Errorf("line %d: quantity must be a positive integer", row.line)
			}
			unit, err := row.int64("unit_price_minor")
			if err != nil {
				return fmt.Errorf("line %d: %v", row.line, err)
			}
			total := unit * int64(qty)
			subtotal += total
			lines = append(lines, line{
				sku: row.get("sku"), title: firstNonEmpty(row.get("title"), row.get("sku")),
				label: row.get("variant_label"), qty: qty, unit: unit, total: total,
			})
		}
		shipping, _ := first.int64Default("shipping_minor", 0)
		discount, _ := first.int64Default("discount_minor", 0)
		total, _ := first.int64Default("total_minor", subtotal+shipping-discount)

		accessToken, err := token()
		if err != nil {
			return err
		}
		var orderID int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO orders (number, access_token, status, payment_status, payment_provider,
			                    currency, subtotal_minor, shipping_minor, discount_minor,
			                    total_minor, refunded_minor, email, phone, name, address, lang,
			                    created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$17)
			RETURNING id`,
			number, accessToken, status, payStatus, provider,
			firstNonEmpty(first.get("currency"), t.app.cfg.Currency),
			subtotal, shipping, discount, total, importedRefunded(payStatus, total),
			strings.ToLower(first.get("email")), nullString(first.get("phone")),
			nullString(first.get("name")), addr, lang, createdAt).Scan(&orderID); err != nil {
			return err
		}

		for _, l := range lines {
			// Link to a live variant when the SKU still exists, so reporting
			// can join; the snapshot columns stand on their own when it does
			// not, which is the point of snapshotting them.
			var variantID, productID sql.NullInt64
			_ = tx.QueryRowContext(ctx,
				`SELECT id, product_id FROM variants WHERE sku = $1`, l.sku).
				Scan(&variantID, &productID)
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO order_lines (order_id, product_id, variant_id, sku, title,
				                         variant_label, quantity, unit_price_minor, total_minor)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
				orderID, productID, variantID, l.sku, l.title, l.label,
				l.qty, l.unit, l.total); err != nil {
				return err
			}
		}

		if fireEvents {
			o := &Order{ID: orderID, Number: number, Status: status, PaymentStatus: payStatus,
				PaymentProvider: provider, Currency: t.app.cfg.Currency,
				Total: money(total, t.app.cfg.Currency), Email: first.get("email"), Language: lang}
			if err := t.app.outbox.write(ctx, tx, EventOrderCreated, AggregateOrder, orderID,
				t.app.orders.eventPayload(o)); err != nil {
				return err
			}
		}

		result.Created++
		if dryRun {
			return errDryRun
		}
		return nil
	})

	if err != nil && !errors.Is(err, errDryRun) {
		result.Errors = append(result.Errors, RowError{Line: first.line, Message: err.Error()})
	}
}

// ------------------------------------------------------------------ csv row

type csvRow struct {
	line   int
	cols   map[string]int
	values []string
}

func (r csvRow) has(name string) bool {
	i, ok := r.cols[name]
	return ok && i < len(r.values) && strings.TrimSpace(r.values[i]) != ""
}

func (r csvRow) get(name string) string {
	i, ok := r.cols[name]
	if !ok || i >= len(r.values) {
		return ""
	}
	return strings.TrimSpace(r.values[i])
}

func (r csvRow) int64(name string) (int64, error) {
	v := r.get(name)
	if v == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number, got %q", name, v)
	}
	return n, nil
}

func (r csvRow) int64Default(name string, def int64) (int64, error) {
	if !r.has(name) {
		return def, nil
	}
	return r.int64(name)
}

func (r csvRow) intDefault(name string, def int) (int, error) {
	if !r.has(name) {
		return def, nil
	}
	n, err := r.int64(name)
	return int(n), err
}

func (r csvRow) nullInt64(name string) any {
	if !r.has(name) {
		return nil
	}
	n, err := r.int64(name)
	if err != nil {
		return nil
	}
	return n
}

func (r csvRow) boolDefault(name string, def bool) bool {
	if !r.has(name) {
		return def
	}
	switch strings.ToLower(r.get(name)) {
	case "true", "t", "yes", "y", "1":
		return true
	case "false", "f", "no", "n", "0":
		return false
	}
	return def
}

func indexColumns(header []string) map[string]int {
	cols := make(map[string]int, len(header))
	for i, name := range header {
		// A spreadsheet saving as UTF-8 often prepends a byte-order mark, which
		// would otherwise make the first column's name unrecognisable.
		cols[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "\ufeff")))] = i
	}
	return cols
}

// escapeRecord defuses spreadsheet formula injection. A cell beginning with
// =, +, - or @ is executed by Excel and Sheets when the file is opened, so a
// product titled "=cmd|..." would be a live attack on whoever opens the
// export. Prefixing an apostrophe is the standard defence, and import strips
// exactly one back — so the round trip stays lossless.
func escapeRecord(record []string) []string {
	out := make([]string, len(record))
	for i, cell := range record {
		if cell != "" && strings.ContainsRune("=+-@", rune(cell[0])) {
			cell = "'" + cell
		}
		out[i] = cell
	}
	return out
}

func unescapeRecord(record []string) []string {
	out := make([]string, len(record))
	for i, cell := range record {
		out[i] = strings.TrimPrefix(cell, "'")
	}
	return out
}

func nullIntString(v sql.NullInt64) string {
	if !v.Valid {
		return ""
	}
	return strconv.FormatInt(v.Int64, 10)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
