package retailassistant

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	gocommerce "github.com/misiki-in/gocommerce/core"
)

// Document types a draft can be.
const (
	KindInvoice = "purchase_invoice"
	KindCredit  = "credit_note"
	KindPayment = "payment_proof"
)

// maxPart bounds one file inside a zip, so a small upload cannot unpack into
// the server's whole memory.
const maxPart = 32 << 20

// Draft is what a document says, in the one shape the reader produces and a
// reviewer corrects. A figure that was not found is null, never a guess.
type Draft struct {
	DocumentType string   `json:"document_type"`
	Supplier     Party    `json:"supplier"`
	InvoiceNo    string   `json:"invoice_no"`
	InvoiceDate  string   `json:"invoice_date"`
	Currency     string   `json:"currency"`
	Amounts      Amounts  `json:"amounts_minor"`
	Lines        []Line   `json:"lines"`
	Exceptions   []string `json:"exceptions"`
	Source       Source   `json:"source"`
	// Payment and credit-note fields, set by the reviewer.
	Method string `json:"method,omitempty"`
	Reason string `json:"reason,omitempty"`
	Kind   string `json:"credit_kind,omitempty"`
}

// Party is a supplier as the document names it.
type Party struct {
	Name  string  `json:"name"`
	GSTIN *string `json:"gstin"`
}

// Amounts are a document's totals in minor units.
type Amounts struct {
	Subtotal *int64 `json:"subtotal"`
	Tax      *int64 `json:"tax"`
	Total    *int64 `json:"total"`
}

// Line is one row of a bill.
type Line struct {
	Description    string  `json:"description"`
	SKU            *string `json:"sku"`
	Barcode        *string `json:"barcode"`
	HSN            *string `json:"hsn"`
	Quantity       *string `json:"quantity"`
	Unit           *string `json:"unit"`
	UnitPriceMinor *int64  `json:"unit_price_minor"`
	TaxRateBps     *int64  `json:"tax_rate_bps"`
	MRPMinor       *int64  `json:"mrp_minor"`
	LineTotalMinor *int64  `json:"line_total_minor"`
}

// Source says how a draft was read and where each field came from.
type Source struct {
	ExtractionMethod string            `json:"extraction_method"`
	Provenance       map[string]string `json:"provenance,omitempty"`
	// Confidence and Notes are a model's own word on its reading, for the reviewer.
	Confidence *int     `json:"confidence,omitempty"`
	Notes      []string `json:"notes,omitempty"`
}

// Extract reads what the standard library can read honestly: CSV, XLSX and DOCX
// tables. A PDF or a photograph comes back empty for a person to read, because
// text pulled out of a PDF by hand loses its columns, and a wrong amount that
// looks right is worse than none. mime is what the bytes are, whatever the file
// was called.
func Extract(data []byte, filename, currency string) (mime string, d Draft) {
	d = Draft{DocumentType: KindInvoice, Currency: currency, Lines: []Line{}, Exceptions: []string{}}
	unread := func(method, why string) (string, Draft) {
		d.Source.ExtractionMethod = method
		d.Exceptions = append(d.Exceptions, why)
		return mime, d
	}
	switch {
	case bytes.HasPrefix(data, []byte("%PDF-")):
		mime = "application/pdf"
		return unread("none", "This PDF was not read automatically. Enter its details from the file.")
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		mime = "image/jpeg"
		return unread("none", "This photo was not read automatically. Enter its details from the picture.")
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		mime = "image/png"
		return unread("none", "This picture was not read automatically. Enter its details from the picture.")
	case len(data) > 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		mime = "image/webp"
		return unread("none", "This picture was not read automatically. Enter its details from the picture.")
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			mime = "application/zip"
			return unread("none", "This file looks damaged, so nothing was read from it.")
		}
		switch {
		case zipHas(z, "xl/workbook.xml"):
			mime = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
			rows, sheets, err := readXLSX(z)
			if err != nil {
				return unread("none", "This spreadsheet could not be read: "+err.Error())
			}
			if sheets > 1 {
				d.Exceptions = append(d.Exceptions, "This workbook has "+strconv.Itoa(sheets)+" sheets, and only the first was read.")
			}
			d.Source.ExtractionMethod = "xlsx"
			fromGrid(rows, &d)
		case zipHas(z, "word/document.xml"):
			mime = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
			rows, err := readDOCX(z)
			if err != nil {
				return unread("none", "This Word file could not be read: "+err.Error())
			}
			d.Source.ExtractionMethod = "docx"
			fromGrid(rows, &d)
		default:
			mime = "application/zip"
			return unread("none", "This kind of file cannot be read automatically.")
		}
		return mime, d
	case utf8.Valid(data) && !bytes.ContainsRune(data, 0):
		mime = "text/csv"
		r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))))
		r.FieldsPerRecord, r.LazyQuotes = -1, true
		rows, err := r.ReadAll()
		if err != nil {
			return unread("none", "This text file could not be read as a table.")
		}
		d.Source.ExtractionMethod = "csv"
		fromGrid(rows, &d)
		return mime, d
	}
	mime = "application/octet-stream"
	return unread("none", "This kind of file cannot be read automatically.")
}

func zipHas(z *zip.Reader, name string) bool {
	for _, f := range z.File {
		if f.Name == name {
			return true
		}
	}
	return false
}

func zipRead(z *zip.Reader, name string) ([]byte, error) {
	for _, f := range z.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		data, err := io.ReadAll(io.LimitReader(rc, maxPart+1))
		if err != nil {
			return nil, err
		}
		if len(data) > maxPart {
			return nil, errors.New("it unpacks to more than this module will read")
		}
		return data, nil
	}
	return nil, nil
}

// readXLSX reads the workbook's first sheet into rows. It takes the file order
// of xl/worksheets, which is how every writer we know of numbers a first sheet.
func readXLSX(z *zip.Reader) (rows [][]string, sheets int, err error) {
	var names []string
	for _, f := range z.File {
		if strings.HasPrefix(f.Name, "xl/worksheets/") && strings.HasSuffix(f.Name, ".xml") {
			names = append(names, f.Name)
		}
	}
	if len(names) == 0 {
		return nil, 0, errors.New("it has no sheets")
	}
	sort.Strings(names)

	var shared []string
	if raw, err := zipRead(z, "xl/sharedStrings.xml"); err != nil {
		return nil, 0, err
	} else if raw != nil {
		var sst struct {
			SI []struct {
				T string `xml:"t"`
				R []struct {
					T string `xml:"t"`
				} `xml:"r"`
			} `xml:"si"`
		}
		if err := xml.Unmarshal(raw, &sst); err != nil {
			return nil, 0, errors.New("its text table is damaged")
		}
		for _, si := range sst.SI {
			text := si.T
			for _, r := range si.R {
				text += r.T
			}
			shared = append(shared, text)
		}
	}

	raw, err := zipRead(z, names[0])
	if err != nil {
		return nil, 0, err
	}
	var sheet struct {
		Rows []struct {
			R     int `xml:"r,attr"`
			Cells []struct {
				Ref string `xml:"r,attr"`
				T   string `xml:"t,attr"`
				V   string `xml:"v"`
				Is  struct {
					T string `xml:"t"`
				} `xml:"is"`
			} `xml:"c"`
		} `xml:"sheetData>row"`
	}
	if err := xml.Unmarshal(raw, &sheet); err != nil {
		return nil, 0, errors.New("its first sheet is damaged")
	}
	for _, row := range sheet.Rows {
		for row.R > len(rows)+1 && len(rows) < 100_000 {
			rows = append(rows, nil)
		}
		var cells []string
		for _, c := range row.Cells {
			col := columnIndex(c.Ref)
			for col > len(cells) {
				cells = append(cells, "")
			}
			value := c.V
			switch c.T {
			case "s":
				i, err := strconv.Atoi(c.V)
				if err != nil || i < 0 || i >= len(shared) {
					return nil, 0, errors.New("a cell points at text that is not there")
				}
				value = shared[i]
			case "inlineStr":
				value = c.Is.T
			}
			cells = append(cells, value)
		}
		rows = append(rows, cells)
	}
	return rows, len(names), nil
}

// columnIndex is "B2" as 1. A cell with no reference takes the next column.
func columnIndex(ref string) int {
	n := 0
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		n = n*26 + int(r-'A') + 1
	}
	return max(n-1, 0)
}

// readDOCX reads a Word file in order: each table row a row, each paragraph
// outside a table a row of one cell, so "Invoice No: 109" and a table of lines
// both reach fromGrid.
func readDOCX(z *zip.Reader) ([][]string, error) {
	raw, err := zipRead(z, "word/document.xml")
	if err != nil {
		return nil, err
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var (
		rows        [][]string
		row         []string
		text        strings.Builder
		inRow, inTC bool
	)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return rows, nil
		}
		if err != nil {
			return nil, errors.New("its contents are damaged")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "tr":
				inRow, row = true, nil
			case "tc":
				inTC = true
				text.Reset()
			case "t":
				var s string
				if err := dec.DecodeElement(&s, &t); err != nil {
					return nil, errors.New("its contents are damaged")
				}
				text.WriteString(s)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "tc":
				inTC = false
				row = append(row, text.String())
				text.Reset()
			case "tr":
				inRow = false
				rows = append(rows, row)
			case "p":
				if !inRow && !inTC {
					if s := strings.TrimSpace(text.String()); s != "" {
						rows = append(rows, []string{s})
					}
					text.Reset()
				}
			}
		}
	}
}

// The words documents use for each thing. Normalised by labelOf first.
var (
	fieldLabels = map[string]string{
		"invoice no": "invoice_no", "invoice number": "invoice_no", "invoice": "invoice_no", "inv no": "invoice_no",
		"bill no": "invoice_no", "bill number": "invoice_no", "credit note no": "invoice_no", "credit note number": "invoice_no",
		"invoice date": "invoice_date", "bill date": "invoice_date", "date": "invoice_date", "credit note date": "invoice_date",
		"supplier": "supplier", "supplier name": "supplier", "sold by": "supplier", "seller": "supplier", "vendor": "supplier", "from": "supplier",
		"gstin": "gstin", "gst no": "gstin", "gstin uin": "gstin", "supplier gstin": "gstin", "seller gstin": "gstin",
		"subtotal": "subtotal", "sub total": "subtotal", "taxable value": "subtotal", "taxable amount": "subtotal",
		"tax": "tax", "total tax": "tax", "tax amount": "tax", "gst": "tax", "total gst": "tax",
		"total": "total", "grand total": "total", "invoice total": "total", "total amount": "total", "amount payable": "total", "net payable": "total", "invoice value": "total",
	}
	columnLabels = map[string]string{
		"description": "description", "item": "description", "item name": "description", "product": "description", "product name": "description", "particulars": "description", "description of goods": "description",
		"sku": "sku", "item code": "sku", "code": "sku",
		"barcode": "barcode", "ean": "barcode",
		"hsn": "hsn", "hsn code": "hsn", "hsn sac": "hsn",
		"qty": "quantity", "quantity": "quantity",
		"unit": "unit", "uom": "unit",
		"rate": "price", "price": "price", "unit price": "price",
		"gst %": "tax_rate", "gst rate": "tax_rate", "tax rate": "tax_rate", "tax %": "tax_rate",
		"mrp":    "mrp",
		"amount": "line_total", "line total": "line_total", "total": "line_total", "total amount": "line_total", "net amount": "line_total",
	}
	punctuation = regexp.MustCompile(`[^\p{L}\p{N}%]+`)
	gstinShape  = regexp.MustCompile(`^[0-9]{2}[A-Z0-9]{13}$`)
)

func labelOf(s string) string {
	return strings.TrimSpace(punctuation.ReplaceAllString(strings.ToLower(s), " "))
}

// fromGrid reads a table of cells however it was laid out: labelled values above
// and below, and one table of lines. Anything it cannot place stays unset.
func fromGrid(rows [][]string, d *Draft) {
	exponent := gocommerce.CurrencyExponent(d.Currency)
	prov := map[string]string{}
	set := func(field string, r, c int, value string) {
		switch field {
		case "invoice_no":
			if d.InvoiceNo == "" {
				d.InvoiceNo = value
				prov[field] = at(r, c)
			}
		case "invoice_date":
			if d.InvoiceDate == "" {
				if iso, ok := parseDate(value); ok {
					d.InvoiceDate = iso
					prov[field] = at(r, c)
				} else {
					d.Exceptions = append(d.Exceptions, fmt.Sprintf("The date %q at %s could not be understood.", value, at(r, c)))
				}
			}
		case "supplier":
			if d.Supplier.Name == "" {
				d.Supplier.Name = value
				prov[field] = at(r, c)
			}
		case "gstin":
			if d.Supplier.GSTIN == nil {
				up := strings.ToUpper(strings.TrimSpace(value))
				if gstinShape.MatchString(up) {
					d.Supplier.GSTIN = &up
					prov[field] = at(r, c)
				} else {
					d.Exceptions = append(d.Exceptions, fmt.Sprintf("%q at %s does not look like a GSTIN.", value, at(r, c)))
				}
			}
		case "subtotal", "tax", "total":
			target := map[string]**int64{"subtotal": &d.Amounts.Subtotal, "tax": &d.Amounts.Tax, "total": &d.Amounts.Total}[field]
			if *target != nil {
				return
			}
			n, err := parseMoney(value, exponent)
			if err != nil {
				d.Exceptions = append(d.Exceptions, fmt.Sprintf("The %s %q at %s could not be read as an amount.", field, value, at(r, c)))
				return
			}
			*target = &n
			prov[field] = at(r, c)
		}
	}

	header, columns := findHeader(rows)
	for r, row := range rows {
		if r == header {
			continue
		}
		for c, cell := range row {
			if strings.Contains(strings.ToLower(cell), "credit note") {
				d.DocumentType = KindCredit
			}
			// "Label: value" in one cell, as a Word paragraph often has it.
			if label, value, ok := strings.Cut(cell, ":"); ok {
				if f := fieldLabels[labelOf(label)]; f != "" && strings.TrimSpace(value) != "" {
					set(f, r, c, strings.TrimSpace(value))
					continue
				}
			}
			if f := fieldLabels[labelOf(cell)]; f != "" {
				if value, vc := nextValue(row, c); value != "" {
					set(f, r, vc, value)
				}
			}
		}
	}

	if header >= 0 {
		for r := header + 1; r < len(rows); r++ {
			line, stop := readLine(rows[r], columns, r, exponent, d)
			if stop {
				break
			}
			d.Lines = append(d.Lines, line)
		}
	}

	if len(prov) > 0 {
		d.Source.Provenance = prov
	}
	if header < 0 {
		d.Exceptions = append(d.Exceptions, "No table of items was found.")
	}
	d.Exceptions = append(d.Exceptions, Check(*d)...)
}

func at(r, c int) string { return fmt.Sprintf("row %d, column %d", r+1, c+1) }

func nextValue(row []string, c int) (string, int) {
	for i := c + 1; i < len(row); i++ {
		if s := strings.TrimSpace(row[i]); s != "" {
			return s, i
		}
	}
	return "", -1
}

// findHeader is the first row naming a description and something countable.
func findHeader(rows [][]string) (int, map[string]int) {
	for r, row := range rows {
		columns := map[string]int{}
		for c, cell := range row {
			if f := columnLabels[labelOf(cell)]; f != "" {
				if _, dup := columns[f]; !dup {
					columns[f] = c
				}
			}
		}
		_, named := columns["description"]
		_, qty := columns["quantity"]
		_, price := columns["price"]
		_, total := columns["line_total"]
		if named && (qty || price || total) {
			return r, columns
		}
	}
	return -1, nil
}

// readLine reads one row of the table. A blank row, or a row whose first filled
// cell is a totals label, ends the table.
func readLine(row []string, columns map[string]int, r, exponent int, d *Draft) (Line, bool) {
	cell := func(f string) string {
		c, ok := columns[f]
		if !ok || c >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[c])
	}
	first := ""
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			first = c
			break
		}
	}
	if first == "" || fieldLabels[labelOf(first)] != "" {
		return Line{}, true
	}
	text := func(f string) *string {
		if s := cell(f); s != "" {
			return &s
		}
		return nil
	}
	amount := func(f string) *int64 {
		s := cell(f)
		if s == "" {
			return nil
		}
		n, err := parseMoney(s, exponent)
		if err != nil {
			d.Exceptions = append(d.Exceptions, fmt.Sprintf("%q on row %d could not be read as an amount.", s, r+1))
			return nil
		}
		return &n
	}
	l := Line{
		Description: cell("description"), SKU: text("sku"), Barcode: text("barcode"), HSN: text("hsn"),
		Quantity: text("quantity"), Unit: text("unit"),
		UnitPriceMinor: amount("price"), MRPMinor: amount("mrp"), LineTotalMinor: amount("line_total"),
	}
	if s := cell("tax_rate"); s != "" {
		n, err := parseMoney(strings.TrimSuffix(s, "%"), 2)
		if err != nil {
			d.Exceptions = append(d.Exceptions, fmt.Sprintf("The tax rate %q on row %d could not be read.", s, r+1))
		} else {
			l.TaxRateBps = &n
		}
	}
	return l, false
}

// Check is what a draft's own figures say about it. It is the same test whether
// the figures were read or typed, so a correction cannot slip past it.
func Check(d Draft) []string {
	problems := []string{}
	if d.DocumentType == KindPayment {
		if strings.TrimSpace(d.Supplier.Name) == "" {
			problems = append(problems, "The supplier is missing.")
		}
		if d.Amounts.Total == nil || *d.Amounts.Total <= 0 {
			problems = append(problems, "The amount is missing.")
		}
		if d.InvoiceDate == "" {
			problems = append(problems, "The date is missing.")
		}
		return problems
	}
	if strings.TrimSpace(d.Supplier.Name) == "" {
		problems = append(problems, "The supplier is missing.")
	}
	if strings.TrimSpace(d.InvoiceNo) == "" {
		problems = append(problems, "The number is missing.")
	}
	if d.InvoiceDate == "" {
		problems = append(problems, "The date is missing.")
	}
	if d.Amounts.Total == nil {
		problems = append(problems, "The total is missing.")
		return problems
	}
	if *d.Amounts.Total <= 0 {
		problems = append(problems, "The total must be more than zero.")
	}
	if s, t := d.Amounts.Subtotal, d.Amounts.Tax; s != nil && t != nil && *s+*t != *d.Amounts.Total {
		problems = append(problems, fmt.Sprintf("Subtotal and tax add up to %s, and the total says %s.", plainAmount(*s+*t, d.Currency), plainAmount(*d.Amounts.Total, d.Currency)))
	}
	sum, all := int64(0), len(d.Lines) > 0
	for _, l := range d.Lines {
		if l.LineTotalMinor == nil {
			all = false
			break
		}
		sum += *l.LineTotalMinor
	}
	if all && sum != *d.Amounts.Total && (d.Amounts.Subtotal == nil || sum != *d.Amounts.Subtotal) {
		problems = append(problems, fmt.Sprintf("The lines add up to %s, which matches neither the subtotal nor the total.", plainAmount(sum, d.Currency)))
	}
	return problems
}

// parseMoney reads "1,18,000.50" or "₹ 1180" as minor units, through digits and
// never a float (AGENTS.md rule 6). More decimals than the currency has, unless
// they are zeros, is refused rather than rounded.
func parseMoney(s string, exponent int) (int64, error) {
	s = strings.TrimSpace(s)
	for _, cut := range []string{"₹", "Rs.", "Rs", "INR", ","} {
		s = strings.ReplaceAll(s, cut, "")
	}
	s = strings.TrimSpace(s)
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" && frac == "" {
		return 0, errors.New("empty")
	}
	if trimmed := strings.TrimRight(frac, "0"); len(trimmed) > exponent {
		return 0, errors.New("too many decimals")
	} else {
		frac = trimmed
	}
	frac += strings.Repeat("0", exponent-len(frac))
	digitsOnly := whole + frac
	if digitsOnly == "" {
		return 0, errors.New("empty")
	}
	for _, r := range digitsOnly {
		if r < '0' || r > '9' {
			return 0, errors.New("not a number")
		}
	}
	if len(digitsOnly) > 17 {
		return 0, errors.New("too large")
	}
	return strconv.ParseInt(digitsOnly, 10, 64)
}

var dateLayouts = []string{"2006-01-02", "02/01/2006", "2/1/2006", "02-01-2006", "2-1-2006", "02.01.2006", "02 Jan 2006", "2 Jan 2006", "02-Jan-2006", "2-Jan-2006", "02/01/06", "02-01-06"}

// parseDate reads a date as India writes one, day first, and a spreadsheet's
// date serial. An ambiguous 03/04/2026 is read as the 3rd of April and shown to
// the reviewer like every other field.
func parseDate(s string) (string, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02"), true
		}
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil && n > 20000 && n < 80000 {
		return time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(n)).Format("2006-01-02"), true
	}
	return "", false
}
