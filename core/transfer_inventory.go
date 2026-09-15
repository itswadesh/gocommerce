package gocommerce

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// The inventory file is the stock-take's shape: one row per variant per
// location, and nothing about the product but enough to recognise the row.
// The product file carries stock too, one column per location; this one is
// for the day the count is the only thing being changed.

// inventoryCSVHeader is the store's own layout. `location` is the code;
// blank on import means the default. `available` is written for the
// reader's benefit and read only when `on_hand` is blank — a count taken as
// "what could I sell" is turned back into a shelf count against what is
// reserved right now.
var inventoryCSVHeader = []string{
	"sku", "product_title", "variant_label", "location", "on_hand", "reserved", "available",
}

// shopifyInventoryHeader is Shopify's inventory export, column for column.
// Committed is what is reserved here; Incoming and Unavailable are states
// this store does not keep, written as zero and ignored on the way in.
var shopifyInventoryHeader = []string{
	"Handle", "Title", "Option1 Name", "Option1 Value", "Option2 Name", "Option2 Value",
	"Option3 Name", "Option3 Value", "SKU", "HS Code", "COO", "Location",
	"Incoming", "Unavailable", "Committed", "Available", "On hand",
}

// ExportInventory streams every variant's count at every location.
func (t *Transfer) ExportInventory(ctx context.Context, out io.Writer, opts ExportOptions) error {
	w := csv.NewWriter(out)
	defer w.Flush()

	header := inventoryCSVHeader
	if opts.Format == FormatShopify {
		header = shopifyInventoryHeader
	}
	if err := w.Write(header); err != nil {
		return err
	}

	// Every variant at every location, whether or not a row exists yet: a
	// stock-take wants a cell for the empty shelf too.
	rows, err := t.app.db.QueryContext(ctx, `
		SELECT p.slug, p.title, v.sku, v.hs_code, v.origin_country,
		       coalesce((
		           SELECT string_agg(o.name || '=' || pov.value, '|' ORDER BY o.position, o.id)
		           FROM variant_option_values vov
		           JOIN product_option_values pov ON pov.id = vov.option_value_id
		           JOIN product_options o ON o.id = pov.option_id
		           WHERE vov.variant_id = v.id
		       ), ''),
		       l.code, l.name,
		       coalesce(vs.on_hand, 0), coalesce(vs.reserved, 0)
		FROM variants v
		JOIN products p ON p.id = v.product_id
		CROSS JOIN locations l
		LEFT JOIN variant_stock vs ON vs.variant_id = v.id AND vs.location_id = l.id
		ORDER BY p.id, v.position, v.id, l.priority, l.id`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var slug, title, sku, hs, origin, options, code, name string
		var onHand, reserved int
		if err := rows.Scan(&slug, &title, &sku, &hs, &origin, &options, &code, &name, &onHand, &reserved); err != nil {
			return err
		}
		var record []string
		if opts.Format == FormatShopify {
			record = make([]string, len(shopifyInventoryHeader))
			pairs := splitList(options, "|")
			set := func(col, v string) { record[shopifyInventoryColumn[col]] = v }
			set("Handle", slug)
			set("Title", title)
			if len(pairs) == 0 {
				set("Option1 Name", "Title")
				set("Option1 Value", "Default Title")
			}
			for i, pair := range pairs {
				if i >= 3 {
					break
				}
				n, v, _ := strings.Cut(pair, "=")
				set(fmt.Sprintf("Option%d Name", i+1), n)
				set(fmt.Sprintf("Option%d Value", i+1), v)
			}
			set("SKU", sku)
			set("HS Code", hs)
			set("COO", origin)
			set("Location", name)
			set("Incoming", "0")
			set("Unavailable", "0")
			set("Committed", strconv.Itoa(reserved))
			set("Available", strconv.Itoa(onHand-reserved))
			set("On hand", strconv.Itoa(onHand))
		} else {
			label := strings.Join(optionValues(options), " / ")
			record = []string{sku, title, label, code,
				strconv.Itoa(onHand), strconv.Itoa(reserved), strconv.Itoa(onHand - reserved)}
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

var shopifyInventoryColumn = func() map[string]int {
	m := make(map[string]int, len(shopifyInventoryHeader))
	for i, name := range shopifyInventoryHeader {
		m[name] = i
	}
	return m
}()

// optionValues is the values of "Color=Black|Size=S", for a label.
func optionValues(options string) []string {
	var out []string
	for _, pair := range splitList(options, "|") {
		_, v, _ := strings.Cut(pair, "=")
		out = append(out, v)
	}
	return out
}

// ImportInventory sets counts from a file in either dialect, one row at a
// time and each in its own transaction, under the rules the product file's
// stock cells follow: a count cannot drop below what is reserved, and a
// closed location takes nothing in. A row that names a SKU or location the
// store does not have is a row error, not a failed file.
func (t *Transfer) ImportInventory(ctx context.Context, in io.Reader, opts ImportOptions) (*ImportResult, error) {
	ctx = withStockSource(ctx, sourceImport)
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
		format = FormatNative
		if _, ok := cols["on hand"]; ok {
			format = FormatShopify
		} else if _, ok := cols["handle"]; ok {
			format = FormatShopify
		}
	}
	var skuCol, locationCol, onHandCol, availableCol string
	switch format {
	case FormatShopify:
		skuCol, locationCol, onHandCol, availableCol = "sku", "location", "on hand", "available"
	default:
		skuCol, locationCol, onHandCol, availableCol = "sku", "location", "on_hand", "available"
	}
	if _, ok := cols[skuCol]; !ok {
		return nil, Validationf("the CSV is missing the required column %q", skuCol)
	}

	result := &ImportResult{DryRun: opts.DryRun, Format: format}
	locations := map[string]stockColumn{}
	// Resolved by code or by name, because the store's file says the code and
	// Shopify's says the name; blank is the default location.
	resolveLocation := func(key string) (stockColumn, error) {
		if c, ok := locations[key]; ok {
			return c, nil
		}
		var c stockColumn
		var err error
		if key == "" {
			err = t.app.db.QueryRowContext(ctx,
				`SELECT id, code, active FROM locations WHERE is_default`).Scan(&c.locationID, &c.code, &c.active)
		} else {
			err = t.app.db.QueryRowContext(ctx,
				`SELECT id, code, active FROM locations WHERE code = $1 OR lower(name) = lower($1) ORDER BY code = $1 DESC LIMIT 1`,
				key).Scan(&c.locationID, &c.code, &c.active)
		}
		if errors.Is(err, sql.ErrNoRows) {
			if key == "" {
				return c, Conflictf("this store has no default location")
			}
			return c, fmt.Errorf("location %q does not exist; open it first, or fix the name", key)
		}
		if err != nil {
			return c, err
		}
		c.name = onHandCol
		locations[key] = c
		return c, nil
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
		sku := row.get(skuCol)
		if sku == "" {
			result.Errors = append(result.Errors, RowError{Line: line, Message: "sku is empty"})
			continue
		}
		// Shopify writes "not stocked" for a variant it does not count; the
		// row says nothing and is left alone.
		if strings.EqualFold(row.get(onHandCol), "not stocked") {
			result.Skipped++
			continue
		}
		fail := func(err error) {
			result.Errors = append(result.Errors, RowError{Line: line, Message: err.Error()})
			result.Skipped++
		}

		loc, err := resolveLocation(row.get(locationCol))
		if err != nil {
			fail(err)
			continue
		}
		err = InTx(ctx, t.app.db, func(tx *sql.Tx) error {
			var variantID int64
			err := tx.QueryRowContext(ctx, `SELECT id FROM variants WHERE sku = $1`, sku).Scan(&variantID)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("no variant has the SKU %q", sku)
			}
			if err != nil {
				return err
			}
			if err := ensureStockRow(ctx, tx, variantID, loc.locationID); err != nil {
				return err
			}
			var qty int
			switch {
			case row.has(onHandCol):
				if qty, err = row.intDefault(onHandCol, 0); err != nil {
					return err
				}
			case row.has(availableCol):
				// "What could I sell" plus what is already promised is the
				// shelf count the file is describing.
				available, err := row.intDefault(availableCol, 0)
				if err != nil {
					return err
				}
				var reserved int
				if err := tx.QueryRowContext(ctx,
					`SELECT reserved FROM variant_stock WHERE variant_id = $1 AND location_id = $2`,
					variantID, loc.locationID).Scan(&reserved); err != nil {
					return err
				}
				qty = available + reserved
			default:
				return fmt.Errorf("the row names neither %s nor %s", onHandCol, availableCol)
			}
			if err := t.setStockFromFile(ctx, tx, variantID, loc, qty, line); err != nil {
				return err
			}
			if opts.DryRun {
				return errDryRun
			}
			return nil
		})
		switch {
		case err == nil, errors.Is(err, errDryRun):
			result.Updated++
		default:
			fail(err)
		}
	}
	result.Duration = time.Since(start).String()
	return result, nil
}
