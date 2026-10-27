package invoices

import (
	"fmt"
	"html/template"
	"strconv"
	"strings"
	"time"
)

// The printed GST tax invoice. It reads the stored TaxInvoice and nothing
// else. A GST document is in rupees only, so it prints rupees and paise with
// Indian digit grouping; the plain invoice keeps printing minor units.
var gstFuncs = template.FuncMap{
	"rupees": rupees,
	"rate":   func(bp int) string { return percent(bp, 1) },
	"half":   func(bp int) string { return percent(bp, 2) },
	"date": func(s string) string {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return s
		}
		return t.Format("2 Jan 2006")
	},
}

// rupees prints paise as rupees, grouped the Indian way: 10389333 is
// "1,03,893.33".
func rupees(minor int64) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	whole := strconv.FormatInt(minor/100, 10)
	if len(whole) > 3 {
		head, tail := whole[:len(whole)-3], whole[len(whole)-3:]
		var groups []string
		for len(head) > 2 {
			groups = append([]string{head[len(head)-2:]}, groups...)
			head = head[:len(head)-2]
		}
		whole = strings.Join(append(append([]string{head}, groups...), tail), ",")
	}
	return fmt.Sprintf("%s%s.%02d", sign, whole, minor%100)
}

// percent prints bp/div basis points as a percentage with only the decimals
// it needs: 1800 is "18%", and halved 500 is "2.5%" and 25 is "0.125%".
func percent(bp, div int) string {
	milli := bp * 10 / div
	s := strconv.Itoa(milli / 1000)
	if frac := milli % 1000; frac != 0 {
		s += "." + strings.TrimRight(fmt.Sprintf("%03d", frac), "0")
	}
	return s + "%"
}

const taxInvoiceHTML = `<!doctype html>
<html lang="en-IN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Tax invoice {{.Number}}</title>
<style>
 @page{size:A4;margin:12mm}
 *{box-sizing:border-box}
 body{font-family:system-ui,-apple-system,"Segoe UI",sans-serif;max-width:52rem;margin:2rem auto;padding:0 16px;color:#111;background:#fff;line-height:1.45;font-size:15px}
 header{display:flex;flex-wrap:wrap;justify-content:space-between;gap:1rem 2rem;border-bottom:2px solid #111;padding-bottom:1rem}
 h1{font-size:1.25rem;margin:0 0 .25rem}
 .doc{text-align:right}
 .doc h2{font-size:1.25rem;letter-spacing:.06em;margin:0 0 .25rem}
 .muted{color:#555;font-size:.875rem}
 .parties{display:flex;flex-wrap:wrap;gap:1rem 2rem;justify-content:space-between;margin:1.25rem 0}
 .parties>div{min-width:14rem;flex:1}
 .label{font-size:.75rem;text-transform:uppercase;letter-spacing:.06em;color:#555;margin:0 0 .25rem}
 .lines{overflow-x:auto;margin:1rem 0}
 table{width:100%;border-collapse:collapse}
 th,td{text-align:left;padding:.4rem .5rem .4rem 0;border-bottom:1px solid #ddd;vertical-align:top}
 th{font-size:.75rem;text-transform:uppercase;letter-spacing:.04em;color:#555;white-space:nowrap}
 .num{text-align:right;font-variant-numeric:tabular-nums;white-space:nowrap}
 tfoot td{border:0;font-weight:700;border-top:2px solid #111}
 .totals{margin-left:auto;max-width:22rem}
 .totals td{border:0;padding:.2rem 0}
 .totals tr:last-child td{font-weight:700;border-top:2px solid #111;padding-top:.5rem;font-size:1.05rem}
 .sign{margin-top:3rem;text-align:right}
 .sign .line{display:inline-block;min-width:14rem;border-top:1px solid #111;padding-top:.25rem;margin-top:3rem}
 @media print{body{margin:0;max-width:none;font-size:12px}.lines{overflow:visible}}
</style></head>
<body>
<header>
  <div>
    <h1>{{.Supplier.LegalName}}</h1>
    <div class="muted">
      {{with .Supplier.Address}}{{.Line1}}{{if .Line2}}, {{.Line2}}{{end}}<br>{{.City}} {{.PostalCode}}{{end}}<br>
      {{.Supplier.State}} ({{.Supplier.StateCode}})<br>
      GSTIN {{.Supplier.GSTIN}}
      {{if .Supplier.Email}}<br>{{.Supplier.Email}}{{end}}{{if .Supplier.Phone}}<br>{{.Supplier.Phone}}{{end}}
    </div>
  </div>
  <div class="doc">
    <h2>TAX INVOICE</h2>
    <div class="muted">
      Invoice <strong>{{.Number}}</strong><br>
      Date {{date .Date}}<br>
      Place of supply: {{.Supply.PlaceOfSupply}} ({{.Supply.PlaceOfSupplyCode}})<br>
      Reverse charge: {{if .Supply.ReverseCharge}}Yes{{else}}No{{end}}
    </div>
  </div>
</header>

<div class="parties">
  <div>
    <p class="label">Billed and shipped to</p>
    {{with .Recipient}}<strong>{{.LegalName}}</strong><br>
    {{if .Registered}}GSTIN {{.GSTIN}}<br>{{end}}
    {{with .Address}}{{.Line1}}{{if .Line2}}, {{.Line2}}{{end}}<br>{{.City}} {{.PostalCode}}{{end}}<br>
    {{.State}} ({{.StateCode}}){{end}}
  </div>
</div>

{{$intra := eq .Supply.Kind "intra"}}{{$state := .Supply.StateTaxName}}
<div class="lines">
<table>
  <thead><tr>
    <th>#</th><th>Description</th><th>HSN</th><th class="num">Qty</th><th>Unit</th>
    <th class="num">Taxable value</th><th class="num">Rate</th>
    {{if $intra}}<th class="num">CGST</th><th class="num">{{$state}}</th>{{else}}<th class="num">IGST</th>{{end}}
    <th class="num">Total</th>
  </tr></thead>
  <tbody>
  {{range .Lines}}
    <tr>
      <td>{{.N}}</td>
      <td>{{.Description}}{{if .SKU}}<br><span class="muted">{{.SKU}}</span>{{end}}{{if .DiscountMinor}}<br><span class="muted">less discount {{rupees .DiscountMinor}}</span>{{end}}</td>
      <td>{{.HSN}}</td>
      <td class="num">{{.Qty}}</td>
      <td>{{.Unit}}</td>
      <td class="num">{{rupees .TaxableMinor}}</td>
      <td class="num">{{if .RateBP}}{{rate .RateBP}}{{else}}-{{end}}</td>
      {{if $intra}}<td class="num">{{rupees .CGSTMinor}}</td><td class="num">{{rupees .SGSTMinor}}</td>{{else}}<td class="num">{{rupees .IGSTMinor}}</td>{{end}}
      <td class="num">{{rupees .TotalMinor}}</td>
    </tr>
  {{end}}
  </tbody>
</table>
</div>

<p class="label">Rate-wise summary</p>
<div class="lines">
<table>
  <thead><tr>
    <th class="num">Rate</th><th class="num">Taxable value</th>
    {{if $intra}}<th class="num">CGST</th><th class="num">{{$state}}</th>{{else}}<th class="num">IGST</th>{{end}}
  </tr></thead>
  <tbody>
  {{range .ByRate}}
    <tr>
      <td class="num">{{if .RateBP}}{{rate .RateBP}}{{else}}Nil{{end}}</td>
      <td class="num">{{rupees .TaxableMinor}}</td>
      {{if $intra}}<td class="num">{{rupees .CGSTMinor}} <span class="muted">@ {{half .RateBP}}</span></td><td class="num">{{rupees .SGSTMinor}} <span class="muted">@ {{half .RateBP}}</span></td>{{else}}<td class="num">{{rupees .IGSTMinor}}</td>{{end}}
    </tr>
  {{end}}
  </tbody>
</table>
</div>

<table class="totals">
  <tr><td>Taxable value</td><td class="num">{{rupees .Totals.TaxableMinor}}</td></tr>
  {{if $intra}}<tr><td>CGST</td><td class="num">{{rupees .Totals.CGSTMinor}}</td></tr>
  <tr><td>{{$state}}</td><td class="num">{{rupees .Totals.SGSTMinor}}</td></tr>{{else}}<tr><td>IGST</td><td class="num">{{rupees .Totals.IGSTMinor}}</td></tr>{{end}}
  <tr><td>Total (₹)</td><td class="num">{{rupees .Totals.TotalMinor}}</td></tr>
</table>

<div class="sign">
  For {{.Supplier.LegalName}}<br>
  <span class="line">Authorised signatory</span>
</div>
</body></html>
`
