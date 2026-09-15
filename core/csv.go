package gocommerce

import (
	"encoding/csv"
	"io"
	"strings"
)

// CSV for modules.
//
// The engine's own importers and exporters have carried the same three
// disciplines since the first one: a cell that begins with =, +, - or @ is
// prefixed with an apostrophe on the way out and stripped of exactly one on
// the way back, because a spreadsheet executes the first and a round trip has
// to be lossless; a header is matched case-insensitively with any byte-order
// mark removed, because a spreadsheet saving as UTF-8 often adds one; and an
// absent column says nothing, so a file that omits a field leaves it alone
// rather than blanking it.
//
// Three modules now want CSV of their own — reviews, menus, a module nobody
// has written yet — and each of them hand-rolling those three rules is how
// one of them comes to get the first one wrong. So they are one exported
// reader and one exported writer here, over the same unexported helpers the
// engine's own transfers use, and there is exactly one implementation of the
// escaping rule in the repository (D62).

// CSVWriter writes rows with the engine's escaping. Every export goes
// through it; nothing writes encoding/csv directly.
type CSVWriter struct {
	w *csv.Writer
}

// NewCSVWriter starts a CSV, writing the header immediately.
func NewCSVWriter(out io.Writer, header []string) (*CSVWriter, error) {
	w := &CSVWriter{w: csv.NewWriter(out)}
	if err := w.w.Write(header); err != nil {
		return nil, Internalf(err, "write the CSV header")
	}
	return w, nil
}

// Write adds one row, escaped.
func (w *CSVWriter) Write(record []string) error {
	if err := w.w.Write(escapeRecord(record)); err != nil {
		return Internalf(err, "write a CSV row")
	}
	return nil
}

// Flush finishes the file and reports anything the writer swallowed. A
// caller that forgets it writes a truncated file, so every export defers it.
func (w *CSVWriter) Flush() error {
	w.w.Flush()
	if err := w.w.Error(); err != nil {
		return Internalf(err, "finish the CSV")
	}
	return nil
}

// CSVReader reads a file with a header row, handing back each row by column
// name rather than by position — which is what lets a file keep importing
// after the engine adds a column.
type CSVReader struct {
	r    *csv.Reader
	cols map[string]int
	line int
}

// NewCSVReader reads the header and prepares the column index. A file with
// no readable header is refused here rather than row by row.
func NewCSVReader(in io.Reader) (*CSVReader, error) {
	r := csv.NewReader(in)
	// Rows shorter than the header are normal in hand-edited files; a row
	// that is short simply has nothing to say about the last columns.
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return nil, Validationf("could not read the CSV header: %v", err)
	}
	return &CSVReader{r: r, cols: indexColumns(header), line: 1}, nil
}

// Has reports whether the file carries a column at all — which is how an
// importer tells "the file does not mention this" from "the file says it is
// empty", and the difference between the two decides whether a field is
// left alone or cleared.
func (r *CSVReader) Has(name string) bool {
	_, ok := r.cols[strings.ToLower(name)]
	return ok
}

// Line is the line the reader has reached, counting the header as line 1.
// It is what a caller reports when Next itself fails: the row never arrived,
// so there is no CSVRow to ask.
func (r *CSVReader) Line() int { return r.line }

// Next reads the next row. It returns io.EOF when the file is done, and a
// *APIError naming the line for a row the CSV parser could not read.
func (r *CSVReader) Next() (CSVRow, error) {
	record, err := r.r.Read()
	r.line++
	if err == io.EOF {
		return CSVRow{}, io.EOF
	}
	if err != nil {
		return CSVRow{}, Validationf("line %d could not be read: %v", r.line, err)
	}
	return CSVRow{row: csvRow{line: r.line, cols: r.cols, values: unescapeRecord(record)}}, nil
}

// CSVRow is one row, read by column name.
type CSVRow struct {
	row csvRow
}

// Line is the row's line in the file, counting the header as line 1 — so it
// matches the row number a spreadsheet shows.
func (c CSVRow) Line() int { return c.row.line }

// Has reports whether this row actually says something in that column: the
// column exists, the row is long enough, and the cell is not blank. An
// importer guards every update on it, which is what makes an absent column
// leave a field alone.
func (c CSVRow) Has(name string) bool { return c.row.has(strings.ToLower(name)) }

// Get is the cell, trimmed, or "" when the row says nothing there.
func (c CSVRow) Get(name string) string { return c.row.get(strings.ToLower(name)) }

// Int is the cell as a whole number, or fallback when the row says nothing.
// A cell that is present and not a number is an error naming the column.
func (c CSVRow) Int(name string, fallback int) (int, error) {
	n, err := c.row.intDefault(strings.ToLower(name), fallback)
	if err != nil {
		return fallback, Validationf("line %d: %v", c.row.line, err)
	}
	return n, nil
}

// Int64 is Int for a larger number — an id, a price in minor units.
func (c CSVRow) Int64(name string, fallback int64) (int64, error) {
	n, err := c.row.int64Default(strings.ToLower(name), fallback)
	if err != nil {
		return fallback, Validationf("line %d: %v", c.row.line, err)
	}
	return n, nil
}

// Bool accepts true/t/yes/y/1 and false/f/no/n/0, and falls back for a cell
// that says nothing or something else — a spreadsheet's idea of a boolean is
// not worth failing a whole row over.
func (c CSVRow) Bool(name string, fallback bool) bool {
	return c.row.boolDefault(strings.ToLower(name), fallback)
}
