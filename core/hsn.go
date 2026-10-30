package gocommerce

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"slices"
	"strings"
	"sync"
)

// India's HSN and SAC codes, the numbers a GST invoice prints against each
// line: HSN for goods, SAC (services accounting codes, all under 99) for
// services.
//
// Embedded for the reason the Shopify taxonomy is (taxonomy.go): a picker that
// reaches the network fails in an air-gapped deployment and drifts between two
// installs of the same version. It is reference data, read-only, so it is a
// file and not a table — no store owns a row of it (D170).
//
// Source: the GST portal's HSN/SAC master,
// https://tutorial.gst.gov.in/downloads/HSN_SAC.xlsx, fetched 2026-10-30.
// Converted to `code<TAB>kind<TAB>description`, kind G (goods) or S
// (services), sorted, gzipped. Descriptions are the portal's own words; the
// conversion only restored leading zeros the spreadsheet had lost from cells
// stored as numbers, turned its "~" level separators into " > " and folded
// whitespace. 22,609 codes: 98 chapters, 1,348 headings, 5,773 subheadings and
// 14,709 eight-digit tariff items of goods, and 681 SAC codes. To refresh,
// convert a newer copy the same way and replace hsn/hsn.tsv.gz.

//go:embed hsn/hsn.tsv.gz
var hsnGzip []byte

// HSNCode is one entry of the HSN/SAC master.
type HSNCode struct {
	// Code is 2 (chapter), 4 (heading), 6 (subheading) or 8 (tariff item)
	// digits.
	Code        string `json:"code"`
	Description string `json:"description"`
	// Heading is the four-digit heading's description for a longer code,
	// because the master often describes a tariff item only against its
	// heading: 61091000 is just "OF COTTON", and means nothing without
	// "T-SHIRTS, SINGLETS AND OTHER VESTS".
	Heading string `json:"heading,omitempty"`
	// Service marks a SAC code rather than an HSN one.
	Service bool `json:"service"`
}

type hsnMaster struct {
	codes  []HSNCode
	byCode map[string]int
	// folded is each code's description and heading, lower-cased, for
	// searching by words without folding 22,000 strings per keystroke.
	folded []string
}

// The master is parsed on first use, not at start-up: most processes never
// ask, and the decompressed list is a few megabytes.
var hsnData = sync.OnceValue(func() *hsnMaster {
	zr, err := gzip.NewReader(bytes.NewReader(hsnGzip))
	if err != nil {
		panic("hsn: embedded master is not gzip: " + err.Error())
	}
	m := &hsnMaster{byCode: map[string]int{}}
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		code, rest, _ := strings.Cut(sc.Text(), "\t")
		kind, desc, _ := strings.Cut(rest, "\t")
		m.byCode[code] = len(m.codes)
		m.codes = append(m.codes, HSNCode{Code: code, Description: desc, Service: kind == "S"})
	}
	if err := sc.Err(); err != nil {
		panic("hsn: embedded master is unreadable: " + err.Error())
	}
	for i := range m.codes {
		c := &m.codes[i]
		if len(c.Code) > 4 {
			if h, ok := m.byCode[c.Code[:4]]; ok {
				c.Heading = m.codes[h].Description
			}
		}
		m.folded = append(m.folded, strings.ToLower(c.Description+" "+c.Heading))
	}
	return m
})

// hsnDigits is s with the dots and spaces people type inside a code removed
// ("6109.10", "6109 10"), or false when s is not a code at all.
func hsnDigits(s string) (string, bool) {
	d := strings.NewReplacer(".", "", " ", "").Replace(strings.TrimSpace(s))
	if d == "" {
		return "", false
	}
	for _, r := range d {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return d, true
}

// LookupHSN finds one code, typed with or without dots.
func LookupHSN(code string) (HSNCode, bool) {
	d, ok := hsnDigits(code)
	if !ok {
		return HSNCode{}, false
	}
	m := hsnData()
	i, ok := m.byCode[d]
	if !ok {
		return HSNCode{}, false
	}
	return m.codes[i], true
}

// SearchHSN matches q as a code prefix when it is digits ("7113", "6109.10")
// and otherwise as words, every one of which must appear in the description or
// its heading ("cotton t-shirts"). total counts every match.
//
// A prefix search is in code order, so a heading comes before its items. A
// word search ranks codes whose own description has every word above those
// that needed their heading's, and shorter codes first within each: matching
// on the heading pulls in every item under it, and "jewellery" should offer
// 7113 before the forty tariff items of 4202 that only mention jewellery boxes.
func SearchHSN(q string, limit, offset int) ([]HSNCode, int) {
	m := hsnData()
	var hits []int
	if d, ok := hsnDigits(q); ok {
		for i, c := range m.codes {
			if strings.HasPrefix(c.Code, d) {
				hits = append(hits, i)
			}
		}
	} else {
		words := strings.Fields(strings.ToLower(q))
		if len(words) == 0 {
			return []HSNCode{}, 0
		}
		rank := map[int]int{}
		for i := range m.codes {
			if !hasAll(m.folded[i], words) {
				continue
			}
			hits = append(hits, i)
			r := len(m.codes[i].Code)
			if !hasAll(strings.ToLower(m.codes[i].Description), words) {
				r += 100
			}
			rank[i] = r
		}
		slices.SortStableFunc(hits, func(a, b int) int { return rank[a] - rank[b] })
	}
	out := []HSNCode{}
	for _, i := range hits[min(offset, len(hits)):min(offset+limit, len(hits))] {
		out = append(out, m.codes[i])
	}
	return out, len(hits)
}

func hasAll(s string, words []string) bool {
	for _, w := range words {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}
