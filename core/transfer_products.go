package gocommerce

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The product half of CSV transfer: one model, read and written in either
// dialect (transfer_shopify.go). Export gathers a product at a time and hands
// it to a writer; import translates a row at a time into the store's own
// columns and hands each product to one transaction.

// exportVariant is one variant as the export reads it: the product's fields
// beside it, because both dialects write a row per variant.
type exportVariant struct {
	productID                                               int64
	slug, title, description, status                        string
	vendor, productType, category, seoTitle, seoDescription string
	tags                                                    []string
	sku, barcode                                            string
	options                                                 [][2]string // name and value, in the product's axis order
	price                                                   int64
	compareAt, cost, weight                                 sql.NullInt64
	taxable, requiresShipping, tracks, oversell, active     bool
	stock                                                   []int // one per stock column
	totalOnHand                                             int
	weightUnit, origin, hs                                  string
	meta                                                    []byte
	pictures                                                []string // the variant's own, in order
}

// exportPicture is one of a product's pictures, in display order.
type exportPicture struct{ url, alt string }

// productWriter renders one product in one dialect.
type productWriter interface {
	header() []string
	product(variants []exportVariant, pictures []exportPicture) [][]string
}

// nativeProductWriter is the store's own layout: every row carries the
// product's fields (a spreadsheet filtered by title still works), the
// product's pictures ride on its first row, and each variant's own on its
// row.
type nativeProductWriter struct {
	stockCols []stockColumn
	opts      ExportOptions
}

func (n nativeProductWriter) header() []string { return productHeaderFor(n.stockCols) }

func (n nativeProductWriter) product(variants []exportVariant, pictures []exportPicture) [][]string {
	out := make([][]string, 0, len(variants))
	for i, v := range variants {
		options := make([]string, len(v.options))
		for j, o := range v.options {
			options[j] = o[0] + "=" + o[1]
		}
		record := []string{
			v.slug, v.title, v.description, v.status,
			v.vendor, v.productType, strings.Join(v.tags, ","), v.category, v.seoTitle, v.seoDescription,
			v.sku, v.barcode, strings.Join(options, "|"),
			strconv.FormatInt(v.price, 10), nullIntString(v.compareAt), nullIntString(v.cost), strconv.FormatBool(v.taxable),
			strconv.FormatBool(v.requiresShipping),
		}
		for _, s := range v.stock {
			record = append(record, strconv.Itoa(s))
		}
		record = append(record,
			strconv.FormatBool(v.tracks), strconv.FormatBool(v.oversell), strconv.FormatBool(v.active),
			nullIntString(v.weight), v.weightUnit, v.origin, v.hs)
		var urls, alts []string
		if i == 0 {
			for _, p := range pictures {
				urls = append(urls, n.opts.absolute(p.url))
				alts = append(alts, p.alt)
			}
		}
		own := make([]string, len(v.pictures))
		for j, u := range v.pictures {
			own[j] = n.opts.absolute(u)
		}
		record = append(record, strings.Join(urls, "|"), joinAlts(alts), strings.Join(own, "|"), string(v.meta))
		out = append(out, record)
	}
	return out
}

// joinAlts is the alt texts as one cell, or nothing when there are none —
// a cell of "|||" says nothing a blank does not.
func joinAlts(alts []string) string {
	for _, a := range alts {
		if a != "" {
			return strings.Join(alts, "|")
		}
	}
	return ""
}

// ------------------------------------------------------------------ export

// ExportProducts streams the catalog as CSV, one row per variant, in the
// dialect asked for.
//
// It takes the same ProductQuery the admin listing does, through the same
// filter function, so an export from a filtered screen is the rows on that
// screen and nothing else.
func (t *Transfer) ExportProducts(ctx context.Context, out io.Writer, q ProductQuery, opts ExportOptions) error {
	w := csv.NewWriter(out)
	defer w.Flush()

	stockCols, err := t.productStockColumns(ctx)
	if err != nil {
		return err
	}
	var writer productWriter
	switch opts.Format {
	case FormatShopify:
		writer = shopifyProductWriter{exp: currencyExponent(t.app.cfg.Currency), opts: opts}
	default:
		writer = nativeProductWriter{stockCols: stockCols, opts: opts}
	}
	if err := w.Write(writer.header()); err != nil {
		return err
	}

	// One scalar subquery per location rather than a join or a pre-loaded map:
	// the export streams, and holding a row per variant per location in memory
	// would cost about what the catalog itself costs.
	stockSelect := ""
	args := make([]any, 0, len(stockCols))
	for _, c := range stockCols {
		args = append(args, c.locationID)
		stockSelect += fmt.Sprintf(
			"coalesce((SELECT vs.on_hand FROM variant_stock vs"+
				" WHERE vs.variant_id = v.id AND vs.location_id = $%d), 0), ", len(args))
	}

	// The filters are numbered after the per-location arguments above, which
	// is why productFilters takes the slice rather than starting at $1.
	join, where, args := productFilters(q, args)

	// The ORDER BY does not move with the filters: ImportProducts requires a
	// product's variant rows to be contiguous, so this order is the importer's
	// contract rather than a display choice.
	rows, err := t.app.db.QueryContext(ctx, `
		SELECT p.id, p.slug, p.title, p.description, p.status,
		       p.vendor, p.product_type, to_jsonb(p.tags), p.category_id, p.seo_title, p.seo_description,
		       v.sku, coalesce(v.barcode, ''),
		       coalesce((
		           SELECT string_agg(o.name || '=' || pov.value, '|' ORDER BY o.position, o.id)
		           FROM variant_option_values vov
		           JOIN product_option_values pov ON pov.id = vov.option_value_id
		           JOIN product_options o ON o.id = pov.option_id
		           WHERE vov.variant_id = v.id
		       ), ''),
		       v.price_minor, v.compare_at_price_minor, v.cost_minor, v.taxable, v.requires_shipping, `+stockSelect+`
		       (SELECT coalesce(sum(vs.on_hand), 0) FROM variant_stock vs WHERE vs.variant_id = v.id),
		       v.track_inventory, v.continue_selling, v.active,
		       v.weight_grams, v.weight_unit, v.origin_country, v.hs_code, v.metadata,
		       coalesce((
		           SELECT string_agg(m.url, '|' ORDER BY vm.position, vm.media_id)
		           FROM variant_media vm JOIN media m ON m.id = vm.media_id
		           WHERE vm.variant_id = v.id AND m.kind = 'image'
		       ), '')
		FROM variants v
		JOIN products p ON p.id = v.product_id`+join+`
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY p.id, v.position, v.id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	// Category names are joined in Go rather than SQL: the full name is the
	// ancestry, which categories.go computes and nothing stores.
	var categories map[int64]string
	categoryName := func(id sql.NullInt64) (string, error) {
		if !id.Valid {
			return "", nil
		}
		if categories == nil {
			list, err := t.app.Categories().List(ctx)
			if err != nil {
				return "", err
			}
			categories = make(map[int64]string, len(list))
			for _, c := range list {
				categories[c.ID] = c.FullName
			}
		}
		return categories[id.Int64], nil
	}

	var group []exportVariant
	flush := func() error {
		if len(group) == 0 {
			return nil
		}
		pictures, err := t.productPictures(ctx, group[0].productID)
		if err != nil {
			return err
		}
		for _, record := range writer.product(group, pictures) {
			if err := w.Write(escapeRecord(record)); err != nil {
				return err
			}
		}
		group = group[:0]
		return nil
	}

	for rows.Next() {
		var v exportVariant
		var tags []byte
		var categoryID sql.NullInt64
		var options, pictures string
		dest := []any{&v.productID, &v.slug, &v.title, &v.description, &v.status,
			&v.vendor, &v.productType, &tags, &categoryID, &v.seoTitle, &v.seoDescription,
			&v.sku, &v.barcode, &options, &v.price, &v.compareAt, &v.cost, &v.taxable, &v.requiresShipping}
		v.stock = make([]int, len(stockCols))
		for i := range v.stock {
			dest = append(dest, &v.stock[i])
		}
		dest = append(dest, &v.totalOnHand, &v.tracks, &v.oversell, &v.active,
			&v.weight, &v.weightUnit, &v.origin, &v.hs, &v.meta, &pictures)
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		if err := scanTags(tags, &v.tags); err != nil {
			return err
		}
		if v.category, err = categoryName(categoryID); err != nil {
			return err
		}
		for _, pair := range splitList(options, "|") {
			name, value, _ := strings.Cut(pair, "=")
			v.options = append(v.options, [2]string{name, value})
		}
		v.pictures = splitList(pictures, "|")

		if len(group) > 0 && group[0].productID != v.productID {
			if err := flush(); err != nil {
				return err
			}
		}
		group = append(group, v)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	w.Flush()
	return w.Error()
}

// productPictures is a product's own pictures in display order.
func (t *Transfer) productPictures(ctx context.Context, productID int64) ([]exportPicture, error) {
	rows, err := t.app.db.QueryContext(ctx, `
		SELECT m.url, coalesce(m.alt, '')
		FROM product_media pm JOIN media m ON m.id = pm.media_id
		WHERE pm.product_id = $1 AND m.kind = 'image'
		ORDER BY pm.position, m.id`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []exportPicture
	for rows.Next() {
		var p exportPicture
		if err := rows.Scan(&p.url, &p.alt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ------------------------------------------------------------------ import

// ImportProducts upserts products and variants from CSV, keyed on SKU, in
// whichever dialect the header announces.
//
// Rows for one product must be contiguous, which is what both exporters
// produce and what lets this stream a large file instead of holding it in
// memory. Each product is one transaction, so a failure leaves neither a
// half-built product nor a poisoned import. A column the file does not have
// says nothing about that field: a price list updates prices and leaves the
// barcodes alone.
func (t *Transfer) ImportProducts(ctx context.Context, in io.Reader, opts ImportOptions) (*ImportResult, error) {
	// Labelled once, here, so every movement the file causes says so — the
	// helpers below reach the ledger through importProductGroup's own InTx and
	// have no other way to know they are an import.
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
		format = detectProductFormat(cols)
	}
	var translate *shopifyProductReader
	switch format {
	case FormatShopify:
		if _, ok := cols["handle"]; !ok {
			return nil, Validationf("a Shopify product file needs a Handle column")
		}
		translate = newShopifyProductReader(cols, t.app.cfg.Currency)
		// From here on the file is in the store's own columns.
		header, cols = shopifyAsNative, translate.out
	default:
		for _, required := range []string{"product_slug", "sku"} {
			if _, ok := cols[required]; !ok {
				return nil, Validationf("the CSV is missing the required column %q", required)
			}
		}
	}
	stockCols, err := t.resolveStockColumns(ctx, header)
	if err != nil {
		return nil, err
	}

	result := &ImportResult{DryRun: opts.DryRun, Format: format}
	categories := &categoryIndex{t: t}
	var group []csvRow
	var groupSlug string

	flush := func() {
		if len(group) == 0 {
			return
		}
		t.importProductGroup(ctx, group, stockCols, opts, categories, result)
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
		slug := row.get("product_slug")
		if slug == "" {
			result.Errors = append(result.Errors, RowError{Line: line, Message: "product_slug is empty"})
			continue
		}
		if slug != groupSlug {
			flush()
			groupSlug = slug
		}
		group = append(group, row)
	}
	flush()

	result.Duration = time.Since(start).String()
	return result, nil
}

var errDryRun = errors.New("dry run")

// importPicture is one URL a file names for a product, with the order the
// file gave it.
type importPicture struct {
	url, alt string
	position int
}

// picturesOf reads a row's product pictures: a `|`-separated list, with alt
// texts beside it, and Shopify's one-per-row position when there is one.
func picturesOf(row csvRow) []importPicture {
	if !row.has("images") {
		return nil
	}
	alts := strings.Split(row.get("image_alts"), "|")
	position, _ := strconv.Atoi(row.get("image_position"))
	var out []importPicture
	for i, u := range splitList(row.get("images"), "|") {
		p := importPicture{url: u}
		if i < len(alts) {
			p.alt = strings.TrimSpace(alts[i])
		}
		if i == 0 {
			p.position = position
		}
		out = append(out, p)
	}
	return out
}

// importProductGroup writes one product and its variants in one transaction,
// then hangs its pictures on it.
func (t *Transfer) importProductGroup(ctx context.Context, rows []csvRow, stockCols []stockColumn, opts ImportOptions, categories *categoryIndex, result *ImportResult) {
	first := rows[0]
	slug := first.get("product_slug")

	// A row with no SKU that names neither a price nor options is a picture
	// alone (Shopify writes one per picture past the last variant); every
	// other row is a variant. Pictures are ordered by the position the file
	// gave them, and by row order where it gave none.
	var variants []csvRow
	var pictures []importPicture
	for _, row := range rows {
		pictures = append(pictures, picturesOf(row)...)
		if row.get("sku") == "" && !row.has("price_minor") && !row.has("variant_options") {
			continue
		}
		variants = append(variants, row)
	}
	sort.SliceStable(pictures, func(i, j int) bool {
		a, b := pictures[i].position, pictures[j].position
		return a > 0 && (b == 0 || a < b)
	})
	if len(variants) == 0 {
		result.Errors = append(result.Errors, RowError{Line: first.line, Message: "product " + slug + " has no variant rows"})
		result.Skipped += len(rows)
		return
	}

	if !opts.overwrite() {
		var exists bool
		if err := t.app.db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM products WHERE slug = $1)`, slug).Scan(&exists); err != nil {
			result.Errors = append(result.Errors, RowError{Line: first.line, Message: err.Error()})
			result.Skipped += len(rows)
			return
		}
		if exists {
			result.Skipped += len(rows)
			return
		}
	}

	var created bool
	var productID int64
	variantPictures := map[int64][]string{}
	var attention []string

	err := InTx(ctx, t.app.db, func(tx *sql.Tx) error {
		created = false
		attention = attention[:0]
		var categoryID any
		if first.has("category") {
			id, ok, err := categories.lookup(ctx, first.get("category"))
			if err != nil {
				return err
			}
			if ok {
				categoryID = id
			} else {
				attention = append(attention, fmt.Sprintf("category %q is not in this store's tree, so the product has none", first.get("category")))
			}
		}
		tags, err := tagsValue(splitList(first.get("tags"), ","))
		if err != nil {
			return err
		}

		err = tx.QueryRowContext(ctx, `SELECT id FROM products WHERE slug = $1`, slug).Scan(&productID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			title := first.get("product_title")
			if title == "" {
				title = slug
			}
			status := first.get("product_status")
			if !validProductStatus(status) {
				status = ProductDraft
			}
			if err := tx.QueryRowContext(ctx, `
				INSERT INTO products (slug, title, description, status, currency,
				                      vendor, product_type, tags, category_id, seo_title, seo_description)
				VALUES ($1, $2, $3, $4, $5, $6, $7, `+tagsExpr(8)+`, $9, $10, $11) RETURNING id`,
				slug, title, first.get("product_description"), status, t.app.cfg.Currency,
				first.get("vendor"), first.get("product_type"), tags, categoryID,
				first.get("seo_title"), first.get("seo_description")).Scan(&productID); err != nil {
				return translateCatalogErr(err)
			}
			created = true
		case err != nil:
			return err
		default:
			// Only what the file says: a column it lacks leaves the field as it
			// was, and a blank cell in the product's own columns means the
			// same, because Shopify blanks them on every row but the first.
			sets, args := []string{}, []any{}
			set := func(col string, v any) {
				args = append(args, v)
				sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
			}
			if v := first.get("product_title"); v != "" {
				set("title", v)
			}
			if v := first.get("product_description"); v != "" {
				set("description", v)
			}
			if v := first.get("product_status"); validProductStatus(v) {
				set("status", v)
			}
			for _, col := range []string{"vendor", "product_type", "seo_title", "seo_description"} {
				if first.has(col) {
					set(col, first.get(col))
				}
			}
			if first.has("tags") {
				args = append(args, tags)
				sets = append(sets, "tags = "+tagsExpr(len(args)))
			}
			if categoryID != nil {
				set("category_id", categoryID)
			}
			if len(sets) > 0 {
				args = append(args, productID)
				if _, err := tx.ExecContext(ctx,
					"UPDATE products SET "+strings.Join(sets, ", ")+", updated_at = now()"+
						fmt.Sprintf(" WHERE id = $%d", len(args)), args...); err != nil {
					return translateCatalogErr(err)
				}
			}
		}

		for _, row := range variants {
			id, err := t.importVariantRow(ctx, tx, productID, row, stockCols)
			if err != nil {
				return err
			}
			if row.has("variant_images") {
				variantPictures[id] = splitList(row.get("variant_images"), "|")
			}
		}
		if opts.DryRun {
			// Everything above proved the file would apply; rolling back is
			// what makes it a rehearsal rather than a change.
			return errDryRun
		}
		return nil
	})

	// Counters are updated only after the transaction resolves, so the report
	// describes what is in the database rather than what was attempted.
	switch {
	case err == nil, errors.Is(err, errDryRun):
		if created {
			result.Created++
		} else {
			result.Updated++
		}
		for _, note := range attention {
			result.Errors = append(result.Errors, RowError{Line: first.line, Message: note})
		}
		// Pictures are hung on after the commit rather than inside it: the
		// media library is its own service, and a rehearsal must not leave
		// library rows behind for a product that was rolled back.
		if err == nil && (len(pictures) > 0 || len(variantPictures) > 0) {
			if err := t.attachPictures(ctx, productID, pictures, variantPictures); err != nil {
				result.Errors = append(result.Errors, RowError{Line: first.line, Message: "pictures: " + err.Error()})
			}
		}
	default:
		result.Errors = append(result.Errors, RowError{Line: first.line, Message: err.Error()})
		result.Skipped += len(rows)
	}
}

// importVariantRow inserts a variant, or updates the one that already has
// its SKU — only in the columns the row has. It returns the variant's id.
func (t *Transfer) importVariantRow(ctx context.Context, tx *sql.Tx, productID int64, row csvRow, stockCols []stockColumn) (int64, error) {
	sku := row.get("sku")
	if sku == "" {
		return 0, fmt.Errorf("line %d: sku is empty", row.line)
	}
	fail := func(err error) (int64, error) { return 0, fmt.Errorf("line %d: %v", row.line, err) }

	var valueIDs []int64
	hasOptions := row.has("variant_options")
	if hasOptions {
		var err error
		if valueIDs, err = t.ensureOptions(ctx, tx, productID, row.get("variant_options")); err != nil {
			return fail(err)
		}
	}
	// Validated here rather than left to the CHECK, so a mistyped cell is a row
	// error naming its line instead of a constraint violation that takes the
	// whole file down.
	origin, err := normalizeOriginCountry(row.get("origin_country"))
	if err != nil {
		return fail(err)
	}
	hs, err := normalizeHSCode(row.get("hs_code"))
	if err != nil {
		return fail(err)
	}
	weightUnit := strings.ToLower(row.get("weight_unit"))
	switch weightUnit {
	case "", "g", "kg", "oz", "lb":
	default:
		return fail(fmt.Errorf("weight_unit must be g, kg, oz or lb, got %q", weightUnit))
	}

	var variantID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM variants WHERE sku = $1`, sku).Scan(&variantID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		price, err := row.int64("price_minor")
		if err != nil {
			return fail(fmt.Errorf("a new variant needs a price: %v", err))
		}
		if weightUnit == "" {
			weightUnit = "g"
		}
		meta := row.get("metadata")
		if strings.TrimSpace(meta) == "" {
			meta = "{}"
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO variants (product_id, sku, barcode, price_minor, compare_at_price_minor, cost_minor, taxable,
			                      requires_shipping, track_inventory, continue_selling, active,
			                      weight_grams, weight_unit, origin_country, hs_code, option_key, metadata)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
			RETURNING id`,
			productID, sku, nullString(row.get("barcode")), price, row.nullInt64("compare_at_price_minor"),
			row.nullInt64("cost_minor"), row.boolDefault("taxable", true), row.boolDefault("requires_shipping", true),
			row.boolDefault("track_inventory", true), row.boolDefault("continue_selling", false), row.boolDefault("active", true),
			row.nullInt64("weight_grams"), weightUnit, origin, hs, optionKey(valueIDs), meta).Scan(&variantID); err != nil {
			return 0, translateCatalogErr(err)
		}
	case err != nil:
		return 0, err
	default:
		sets, args := []string{}, []any{}
		set := func(col string, v any) {
			args = append(args, v)
			sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
		}
		// A SKU is store-wide, so the row's product is where it lives now.
		set("product_id", productID)
		if row.has("barcode") {
			set("barcode", nullString(row.get("barcode")))
		}
		if row.has("price_minor") {
			price, err := row.int64("price_minor")
			if err != nil {
				return fail(err)
			}
			set("price_minor", price)
		}
		for _, col := range []string{"compare_at_price_minor", "cost_minor", "weight_grams"} {
			if row.has(col) {
				n, err := row.int64(col)
				if err != nil {
					return fail(err)
				}
				set(col, n)
			}
		}
		for _, col := range []string{"taxable", "requires_shipping", "track_inventory", "continue_selling", "active"} {
			if row.has(col) {
				set(col, row.boolDefault(col, true))
			}
		}
		if weightUnit != "" {
			set("weight_unit", weightUnit)
		}
		if row.has("origin_country") {
			set("origin_country", origin)
		}
		if row.has("hs_code") {
			set("hs_code", hs)
		}
		if hasOptions {
			set("option_key", optionKey(valueIDs))
		}
		if row.has("metadata") {
			set("metadata", row.get("metadata"))
		}
		args = append(args, variantID)
		if _, err := tx.ExecContext(ctx,
			"UPDATE variants SET "+strings.Join(sets, ", ")+", updated_at = now()"+
				fmt.Sprintf(" WHERE id = $%d", len(args)), args...); err != nil {
			return 0, translateCatalogErr(err)
		}
	}

	// The variant needs somewhere to be even when the file says nothing about
	// stock, so that ByLocation and the reservation picker have a row to find.
	def, err := defaultLocationID(ctx, tx)
	if err != nil {
		return 0, err
	}
	if err := ensureStockRow(ctx, tx, variantID, def); err != nil {
		return 0, err
	}
	for _, c := range stockCols {
		if err := t.applyStockCell(ctx, tx, variantID, c, row); err != nil {
			return 0, err
		}
	}

	if hasOptions {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM variant_option_values WHERE variant_id = $1`, variantID); err != nil {
			return 0, err
		}
		for _, id := range valueIDs {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO variant_option_values (variant_id, option_value_id) VALUES ($1, $2)`,
				variantID, id); err != nil {
				return 0, err
			}
		}
	}
	return variantID, nil
}

// applyStockCell sets one location's count from one cell, under the rules
// that make the file safe to hand-edit (skills/inventory.md).
func (t *Transfer) applyStockCell(ctx context.Context, tx *sql.Tx, variantID int64, c stockColumn, row csvRow) error {
	// An empty cell is not a zero. Stock is only set where the file actually
	// says a number, because an import that reran must not silently undo
	// sales that happened since it was exported — and because a file listing
	// two of five locations is saying nothing about the other three.
	if !row.has(c.name) {
		return nil
	}
	qty, err := row.intDefault(c.name, 0)
	if err != nil {
		return fmt.Errorf("line %d: %v", row.line, err)
	}
	if err := ensureStockRow(ctx, tx, variantID, c.locationID); err != nil {
		return err
	}
	return t.setStockFromFile(ctx, tx, variantID, c, qty, row.line)
}

// setStockFromFile is one count from one file, at one location: the shared
// half of the product file's stock cells and the inventory file's rows.
func (t *Transfer) setStockFromFile(ctx context.Context, tx *sql.Tx, variantID int64, c stockColumn, qty, line int) error {
	// The pre-read the clamp below makes necessary: what the file asked for
	// and what the shelf got routinely differ, so the delta cannot be derived
	// from the parameter. It is arithmetic only — the condition that decides
	// stays in the UPDATE — and it takes the lock that statement takes anyway.
	var before stockBalance
	if err := tx.QueryRowContext(ctx,
		`SELECT on_hand, reserved FROM variant_stock
		 WHERE variant_id = $1 AND location_id = $2 FOR UPDATE`,
		variantID, c.locationID).Scan(&before.OnHand, &before.Reserved); err != nil {
		return err
	}
	// A closed location may be counted down to zero — that is how one is
	// cleared, and it is what keeps an unedited export->import round trip of a
	// closed shelf's zeros passing — but never up (D44). Compared against the
	// figure just read under FOR UPDATE, so the classification cannot change
	// under the statement below. The code rather than the name, because the
	// code is what the header cell says and what the operator has to edit.
	if !c.active && qty > before.OnHand {
		return fmt.Errorf("line %d: %s is closed; stock moves out of a closed location, never into it",
			line, c.code)
	}
	// The floor is reserved, for the reason SetOnHand refuses outright: a
	// count taken on the shop floor does not know about the order that came
	// in while it was being taken, and dropping below what is promised would
	// oversell it. The file loses, the reservation wins.
	//
	// Except for a variant that sells past zero, where a negative count is
	// not a mistake — it is a debt to a customer who has already ordered.
	// Flooring it would let an export-edit-import round trip quietly write
	// that debt off, which is the one thing a round trip must never do. The
	// condition is reserveStock's, and M12's, deliberately.
	var after stockBalance
	if err := tx.QueryRowContext(ctx,
		`UPDATE variant_stock vs
		 SET on_hand = CASE WHEN v.continue_selling
		                    THEN $3 ELSE greatest($3, vs.reserved) END,
		     updated_at = now()
		 FROM variants v
		 WHERE v.id = vs.variant_id
		   AND vs.variant_id = $1 AND vs.location_id = $2
		 RETURNING vs.on_hand, vs.reserved`,
		variantID, c.locationID, qty).Scan(&after.OnHand, &after.Reserved); err != nil {
		return translateCatalogErr(err)
	}
	// No reason string: kind='import' with source='import' is the whole
	// explanation, and the CSV has no field for one. A re-run of an
	// unchanged file writes nothing, because the delta is zero and import
	// is not the stock-take exception.
	_, err := recordMovement(ctx, tx, variantID, c.locationID, MovementImport,
		stockBalance{
			OnHand:   after.OnHand - before.OnHand,
			Reserved: after.Reserved - before.Reserved,
		}, after, stockRef{})
	return err
}

// attachPictures makes a product's pictures the ones the file named, in the
// file's order, and each variant's the ones its row named. A URL the library
// already holds is reused; a new one is linked, not downloaded — the file is
// the record of where the picture is, and the store shows it from there.
//
// A file that names product pictures replaces the product's list, because
// that is what an edited export means; one that names only variants'
// pictures adds them to the product's list, because a variant shows the
// product's pictures and cannot show one the product does not have.
func (t *Transfer) attachPictures(ctx context.Context, productID int64, pictures []importPicture, variants map[int64][]string) error {
	lib := t.app.MediaLibrary()
	known := map[string]int64{}
	resolve := func(raw, alt string) (int64, error) {
		raw = strings.TrimSpace(raw)
		if id, ok := known[raw]; ok {
			return id, nil
		}
		var id int64
		var have string
		err := t.app.db.QueryRowContext(ctx,
			`SELECT id, coalesce(alt, '') FROM media WHERE url = $1 ORDER BY id LIMIT 1`, raw).Scan(&id, &have)
		if errors.Is(err, sql.ErrNoRows) {
			// The store's own pictures leave in an export as absolute URLs and
			// come back the same way; the library holds them by path.
			if u, perr := url.Parse(raw); perr == nil && u.Host != "" && strings.HasPrefix(u.Path, "/") {
				err = t.app.db.QueryRowContext(ctx,
					`SELECT id, coalesce(alt, '') FROM media WHERE url = $1 ORDER BY id LIMIT 1`, u.Path).Scan(&id, &have)
			}
		}
		if errors.Is(err, sql.ErrNoRows) {
			item, err := lib.AddURL(ctx, raw, MediaImage, alt)
			if err != nil {
				return 0, err
			}
			id, have = item.ID, alt
		} else if err != nil {
			return 0, err
		}
		if alt != "" && alt != have {
			if _, err := lib.SetAlt(ctx, id, alt); err != nil {
				return 0, err
			}
		}
		known[raw] = id
		return id, nil
	}

	var list []int64
	seen := map[int64]bool{}
	add := func(id int64) {
		if !seen[id] {
			seen[id] = true
			list = append(list, id)
		}
	}
	if len(pictures) == 0 {
		current, err := lib.ForProduct(ctx, productID)
		if err != nil {
			return err
		}
		for _, pm := range current {
			add(pm.ID)
		}
	}
	for _, p := range pictures {
		id, err := resolve(p.url, p.alt)
		if err != nil {
			return err
		}
		add(id)
	}
	own := map[int64][]int64{}
	for variantID, urls := range variants {
		for _, u := range urls {
			id, err := resolve(u, "")
			if err != nil {
				return err
			}
			add(id)
			own[variantID] = append(own[variantID], id)
		}
	}
	if err := lib.SetProductMedia(ctx, productID, list); err != nil {
		return err
	}
	for variantID, ids := range own {
		if err := lib.SetVariantMedia(ctx, variantID, ids); err != nil {
			return err
		}
	}
	return nil
}

// ensureOptions parses "Size=M|Color=Black", creating any option or value the
// product does not have yet, and returns the value ids.
func (t *Transfer) ensureOptions(ctx context.Context, tx *sql.Tx, productID int64, spec string) ([]int64, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	var ids []int64
	for _, pair := range strings.Split(spec, "|") {
		name, value, ok := strings.Cut(pair, "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || name == "" || value == "" {
			return nil, fmt.Errorf("variant_options entry %q must look like Name=Value", pair)
		}

		var optionID int64
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM product_options WHERE product_id = $1 AND name = $2`,
			productID, name).Scan(&optionID)
		if errors.Is(err, sql.ErrNoRows) {
			if err := tx.QueryRowContext(ctx, `
				INSERT INTO product_options (product_id, name, position)
				VALUES ($1, $2, (SELECT coalesce(max(position) + 1, 0)
				                 FROM product_options WHERE product_id = $1))
				RETURNING id`, productID, name).Scan(&optionID); err != nil {
				return nil, translateCatalogErr(err)
			}
		} else if err != nil {
			return nil, err
		}

		var valueID int64
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM product_option_values WHERE option_id = $1 AND value = $2`,
			optionID, value).Scan(&valueID)
		if errors.Is(err, sql.ErrNoRows) {
			if err := tx.QueryRowContext(ctx, `
				INSERT INTO product_option_values (option_id, value, position)
				VALUES ($1, $2, (SELECT coalesce(max(position) + 1, 0)
				                 FROM product_option_values WHERE option_id = $1))
				RETURNING id`, optionID, value).Scan(&valueID); err != nil {
				return nil, translateCatalogErr(err)
			}
		} else if err != nil {
			return nil, err
		}
		ids = append(ids, valueID)
	}
	return ids, nil
}

// categoryIndex matches a file's category names to the store's tree, by the
// full path — "Apparel & Accessories > Clothing" as Shopify writes it or
// "Apparel & Accessories / Clothing" as the store does — or by slug. Loaded
// once per import, on the first row that names one.
type categoryIndex struct {
	t      *Transfer
	byPath map[string]int64
}

func (c *categoryIndex) lookup(ctx context.Context, name string) (int64, bool, error) {
	if c.byPath == nil {
		list, err := c.t.app.Categories().List(ctx)
		if err != nil {
			return 0, false, err
		}
		c.byPath = make(map[string]int64, 2*len(list))
		for _, cat := range list {
			c.byPath[categoryKey(cat.FullName)] = cat.ID
			c.byPath["slug:"+strings.ToLower(cat.Slug)] = cat.ID
		}
	}
	if id, ok := c.byPath[categoryKey(name)]; ok {
		return id, true, nil
	}
	id, ok := c.byPath["slug:"+strings.ToLower(strings.TrimSpace(name))]
	return id, ok, nil
}

func categoryKey(path string) string {
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '>' || r == '/' })
	for i, p := range parts {
		parts[i] = strings.ToLower(strings.TrimSpace(p))
	}
	return strings.Join(parts, " / ")
}

// splitList splits a cell on sep, trimming and dropping blanks.
func splitList(s, sep string) []string {
	var out []string
	for _, part := range strings.Split(s, sep) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
