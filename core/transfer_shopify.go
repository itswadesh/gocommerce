package gocommerce

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// The store's CSV comes in two dialects of one model. Its own is the model;
// Shopify's is the same model in the column names Shopify's exporter writes
// and its importer reads, so a file goes either way between the two without
// an edit — a store moving here brings its catalogue as it was exported, and
// a file written here opens in every tool built for Shopify's layout, which
// is most of them. Nothing in this file knows how a product is stored: it
// translates rows, and transfer_products.go does the work.

// Format names a CSV dialect.
type Format string

const (
	FormatNative  Format = "gocommerce"
	FormatShopify Format = "shopify"
)

// ParseFormat reads a format name off a request. Empty is the store's own.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "gocommerce", "native":
		return FormatNative, nil
	case "shopify":
		return FormatShopify, nil
	}
	return "", Validationf("unknown format %q; use gocommerce or shopify", s)
}

// ImportOptions is what an import is told besides the file.
type ImportOptions struct {
	// DryRun proves the file would apply and writes nothing.
	DryRun bool
	// Overwrite is whether a product the store already has (by slug) is
	// updated from the file. Nil means yes — the file is the newer truth,
	// which is what re-importing an edited export needs. False skips it,
	// which is what loading a second catalogue beside the first needs.
	Overwrite *bool
	// Format names the dialect; empty reads it off the header.
	Format Format
	// FireEvents is the orders importer's: whether imported orders announce
	// themselves. Off unless asked, because five thousand old orders must not
	// send five thousand emails.
	FireEvents bool
}

func (o ImportOptions) overwrite() bool { return o.Overwrite == nil || *o.Overwrite }

// ExportOptions is what an export is told besides the filter.
type ExportOptions struct {
	Format Format
	// BaseURL makes a site-relative picture URL absolute, because a file that
	// says "/media/abc.jpg" means nothing to another store's importer. The
	// HTTP layer fills it from the request; a caller writing to disk may
	// leave it empty and get the URLs as the store holds them.
	BaseURL string
}

func (o ExportOptions) absolute(url string) string {
	if o.BaseURL != "" && strings.HasPrefix(url, "/") {
		return strings.TrimRight(o.BaseURL, "/") + url
	}
	return url
}

// ------------------------------------------------------------------- money

// currencyExponent is how many decimals a currency writes: two for most, none
// for the yen, three for the dinar. Shopify's dialect writes "19.99", so the
// file needs it; the API never does (rule 6), which is why this lives here
// and not beside Money.
func currencyExponent(code string) int {
	switch strings.ToUpper(code) {
	case "BIF", "CLP", "DJF", "GNF", "ISK", "JPY", "KMF", "KRW", "PYG", "RWF",
		"UGX", "VND", "VUV", "XAF", "XOF", "XPF":
		return 0
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND":
		return 3
	}
	return 2
}

// parseMoney reads "19.99" into minor units. A thousands separator and a
// currency sign are dropped rather than refused, because a spreadsheet adds
// them without being asked; a decimal comma is not understood, because it
// cannot be told from a thousands separator and Shopify writes a point.
func parseMoney(s string, exp int) (int64, error) {
	clean := strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || r == '.' || r == '-' {
			return r
		}
		return -1
	}, s)
	neg := strings.HasPrefix(clean, "-")
	clean = strings.TrimPrefix(clean, "-")
	whole, frac, _ := strings.Cut(clean, ".")
	if clean == "" || clean == "." || strings.ContainsAny(frac, ".-") || strings.Contains(whole, "-") {
		return 0, fmt.Errorf("%q is not an amount", s)
	}
	if len(frac) > exp {
		return 0, fmt.Errorf("%q has more decimals than the currency allows (%d)", s, exp)
	}
	if whole == "" {
		whole = "0"
	}
	n, err := strconv.ParseInt(whole+frac+strings.Repeat("0", exp-len(frac)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not an amount", s)
	}
	if neg {
		n = -n
	}
	return n, nil
}

// formatMoney is the inverse: minor units as the decimal a spreadsheet shows.
func formatMoney(minor int64, exp int) string {
	if exp == 0 {
		return strconv.FormatInt(minor, 10)
	}
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	scale := int64(math.Pow10(exp))
	return fmt.Sprintf("%s%d.%0*d", sign, minor/scale, exp, minor%scale)
}

func shopifyBool(b bool) string {
	if b {
		return "TRUE"
	}
	return "FALSE"
}

// ---------------------------------------------------------------- products

// shopifyProductHeader is Shopify's product export, column for column, minus
// the per-market price and Google Shopping columns its importer does not
// need back. Their importer accepts a file without them; ours ignores them
// when they are there.
var shopifyProductHeader = []string{
	"Handle", "Title", "Body (HTML)", "Vendor", "Product Category", "Type", "Tags", "Published",
	"Option1 Name", "Option1 Value", "Option2 Name", "Option2 Value", "Option3 Name", "Option3 Value",
	"Variant SKU", "Variant Grams", "Variant Inventory Tracker", "Variant Inventory Qty",
	"Variant Inventory Policy", "Variant Fulfillment Service", "Variant Price", "Variant Compare At Price",
	"Variant Requires Shipping", "Variant Taxable", "Variant Barcode",
	"Image Src", "Image Position", "Image Alt Text", "Gift Card", "SEO Title", "SEO Description",
	"Variant Image", "Variant Weight Unit", "Variant Tax Code", "Cost per item", "Status",
}

// shopifyAsNative is the header a Shopify row is translated into: the
// store's own column names, so everything past the translation — grouping,
// the transaction, the stock rules — is the one importer both dialects share.
var shopifyAsNative = []string{
	"product_slug", "product_title", "product_description", "product_status",
	"vendor", "product_type", "tags", "category", "seo_title", "seo_description",
	"sku", "barcode", "variant_options", "price_minor", "compare_at_price_minor", "cost_minor", "taxable", "requires_shipping",
	"stock_on_hand", "track_inventory", "continue_selling", "weight_grams", "weight_unit",
	"images", "image_alts", "image_position", "variant_images",
}

// detectProductFormat reads the dialect off a header: Shopify's has a Handle,
// the store's own a product_slug.
func detectProductFormat(cols map[string]int) Format {
	if _, ok := cols["handle"]; ok {
		return FormatShopify
	}
	return FormatNative
}

// shopifyProductReader turns Shopify's rows into the store's own, one at a
// time, keeping what a later row of the same handle needs from an earlier
// one: the option names, which Shopify writes on the first row only, and
// how many variants it has seen, for the SKU a row without one is given.
type shopifyProductReader struct {
	cols    map[string]int
	out     map[string]int
	exp     int
	handle  string
	names   [3]string
	ordinal int
}

func newShopifyProductReader(cols map[string]int, currency string) *shopifyProductReader {
	return &shopifyProductReader{cols: cols, out: indexColumns(shopifyAsNative), exp: currencyExponent(currency)}
}

func (s *shopifyProductReader) row(line int, record []string) (csvRow, error) {
	in := csvRow{line: line, cols: s.cols, values: record}
	handle := in.get("handle")
	if handle != s.handle {
		s.handle, s.names, s.ordinal = handle, [3]string{}, 0
	}
	for i := range s.names {
		if name := in.get(fmt.Sprintf("option%d name", i+1)); name != "" {
			s.names[i] = name
		}
	}

	values := make([]string, len(shopifyAsNative))
	set := func(name, v string) { values[s.out[name]] = v }
	set("product_slug", handle)
	set("product_title", in.get("title"))
	set("product_description", in.get("body (html)"))
	set("vendor", in.get("vendor"))
	set("product_type", in.get("type"))
	set("tags", in.get("tags"))
	set("category", in.get("product category"))
	set("seo_title", in.get("seo title"))
	set("seo_description", in.get("seo description"))
	// Status is the newer column and wins; Published is the older yes/no.
	status := strings.ToLower(in.get("status"))
	if !validProductStatus(status) {
		switch strings.ToUpper(in.get("published")) {
		case "TRUE":
			status = ProductActive
		case "FALSE":
			status = ProductDraft
		default:
			status = ""
		}
	}
	set("product_status", status)
	set("images", in.get("image src"))
	set("image_alts", in.get("image alt text"))
	set("image_position", in.get("image position"))

	// A row with no SKU, no price and no option value is a picture alone —
	// Shopify writes one per picture past the last variant.
	if in.get("variant sku") == "" && in.get("variant price") == "" && in.get("option1 value") == "" {
		return csvRow{line: line, cols: s.out, values: values}, nil
	}
	s.ordinal++

	// "Title / Default Title" is Shopify for a product with no options.
	var options []string
	for i, name := range s.names {
		value := in.get(fmt.Sprintf("option%d value", i+1))
		if value == "" || (strings.EqualFold(name, "Title") && strings.EqualFold(value, "Default Title")) {
			continue
		}
		if name == "" {
			name = fmt.Sprintf("Option%d", i+1)
		}
		options = append(options, name+"="+value)
	}
	set("variant_options", strings.Join(options, "|"))

	// A variant here must have a SKU; Shopify lets one go without. The handle
	// is the obvious one for a product with a single variant, and the handle
	// numbered for the rest — the same file gives the same SKUs twice.
	sku := in.get("variant sku")
	if sku == "" {
		sku = handle
		if len(options) > 0 {
			sku = fmt.Sprintf("%s-%d", handle, s.ordinal)
		}
	}
	set("sku", sku)
	set("barcode", in.get("variant barcode"))
	for _, m := range [][2]string{
		{"variant price", "price_minor"}, {"variant compare at price", "compare_at_price_minor"}, {"cost per item", "cost_minor"},
	} {
		if raw := in.get(m[0]); raw != "" {
			minor, err := parseMoney(raw, s.exp)
			if err != nil {
				return csvRow{}, fmt.Errorf("%s: %v", strings.ToUpper(m[0][:1])+m[0][1:], err)
			}
			set(m[1], strconv.FormatInt(minor, 10))
		}
	}
	set("stock_on_hand", in.get("variant inventory qty"))
	set("track_inventory", strconv.FormatBool(in.get("variant inventory tracker") != ""))
	set("continue_selling", strconv.FormatBool(strings.EqualFold(in.get("variant inventory policy"), "continue")))
	set("taxable", in.get("variant taxable"))
	set("requires_shipping", in.get("variant requires shipping"))
	set("weight_grams", in.get("variant grams"))
	set("weight_unit", strings.ToLower(in.get("variant weight unit")))
	set("variant_images", in.get("variant image"))
	return csvRow{line: line, cols: s.out, values: values}, nil
}

// shopifyProductWriter writes a product the way Shopify's exporter does: one
// row per variant, the product's fields and option names on the first row
// only, the pictures one per row in position order, and a row of nothing
// but a picture for each one past the last variant.
type shopifyProductWriter struct {
	exp  int
	opts ExportOptions
}

func (s shopifyProductWriter) header() []string { return shopifyProductHeader }

func (s shopifyProductWriter) product(variants []exportVariant, pictures []exportPicture) [][]string {
	n := len(variants)
	if len(pictures) > n {
		n = len(pictures)
	}
	var out [][]string
	for i := 0; i < n; i++ {
		row := make([]string, len(shopifyProductHeader))
		set := func(name, v string) { row[shopifyColumn[name]] = v }
		first := variants[0]
		set("Handle", first.slug)
		if i == 0 {
			set("Title", first.title)
			set("Body (HTML)", first.description)
			set("Vendor", first.vendor)
			set("Product Category", strings.ReplaceAll(first.category, " / ", " > "))
			set("Type", first.productType)
			set("Tags", strings.Join(first.tags, ", "))
			set("Published", shopifyBool(first.status == ProductActive))
			set("SEO Title", first.seoTitle)
			set("SEO Description", first.seoDescription)
			set("Status", first.status)
			if len(first.options) == 0 {
				set("Option1 Name", "Title")
			}
			for j, o := range first.options {
				if j < 3 {
					set(fmt.Sprintf("Option%d Name", j+1), o[0])
				}
			}
		}
		if i < len(variants) {
			v := variants[i]
			if len(v.options) == 0 {
				set("Option1 Value", "Default Title")
			}
			for j, o := range v.options {
				if j < 3 {
					set(fmt.Sprintf("Option%d Value", j+1), o[1])
				}
			}
			set("Variant SKU", v.sku)
			set("Variant Grams", nullIntString(v.weight))
			if v.tracks {
				set("Variant Inventory Tracker", "shopify")
			}
			set("Variant Inventory Qty", strconv.Itoa(v.totalOnHand))
			policy := "deny"
			if v.oversell {
				policy = "continue"
			}
			set("Variant Inventory Policy", policy)
			set("Variant Fulfillment Service", "manual")
			set("Variant Price", formatMoney(v.price, s.exp))
			if v.compareAt.Valid {
				set("Variant Compare At Price", formatMoney(v.compareAt.Int64, s.exp))
			}
			set("Variant Requires Shipping", shopifyBool(v.requiresShipping))
			set("Variant Taxable", shopifyBool(v.taxable))
			set("Variant Barcode", v.barcode)
			set("Gift Card", "FALSE")
			if len(v.pictures) > 0 {
				set("Variant Image", s.opts.absolute(v.pictures[0]))
			}
			set("Variant Weight Unit", v.weightUnit)
			if v.cost.Valid {
				set("Cost per item", formatMoney(v.cost.Int64, s.exp))
			}
		}
		if i < len(pictures) {
			set("Image Src", s.opts.absolute(pictures[i].url))
			set("Image Position", strconv.Itoa(i+1))
			set("Image Alt Text", pictures[i].alt)
		}
		out = append(out, row)
	}
	return out
}

var shopifyColumn = func() map[string]int {
	m := make(map[string]int, len(shopifyProductHeader))
	for i, name := range shopifyProductHeader {
		m[name] = i
	}
	return m
}()
