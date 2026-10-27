package invoices

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/itswadesh/gocommerce/core"
)

// The TallyPrime export (D127): a period's GST tax invoices as Tally's
// "Import Data" XML. It reads the stored documents and nothing else, so what
// the accountant imports is what was issued, and it does no tax arithmetic:
// every amount is a figure the document already holds, only summed.
//
// It comes in two files because Tally imports masters before transactions; a
// voucher naming a ledger that does not exist yet lands in its exceptions
// report. REMOTEID is what makes importing the same voucher again replace it
// rather than duplicate it.

// TallyLedgers are the ledger names in the accountant's Tally company. A store
// that never set them reads defaultTallyLedgers.
type TallyLedgers struct {
	// CompanyName picks the Tally company to import into; empty imports into
	// whichever is open.
	CompanyName string `json:"company_name"`
	Sales       string `json:"sales_ledger"`
	// SalesPerRate names one sales ledger per rate, the rate appended to
	// Sales: "Sales 18%".
	SalesPerRate      bool   `json:"sales_per_rate"`
	CGST              string `json:"cgst"`
	SGST              string `json:"sgst"`
	UTGST             string `json:"utgst"`
	IGST              string `json:"igst"`
	Freight           string `json:"freight"`
	UnregisteredParty string `json:"unregistered_party"`
	VoucherType       string `json:"voucher_type"`
}

var defaultTallyLedgers = TallyLedgers{
	Sales: "Sales", CGST: "CGST", SGST: "SGST", UTGST: "UTGST", IGST: "IGST",
	Freight: "Delivery Charges", UnregisteredParty: "Cash Sales", VoucherType: "Sales",
}

// clean trims every name and refuses one Tally could not hold.
func (n *TallyLedgers) clean() error {
	for _, f := range []struct {
		field    string
		v        *string
		optional bool
	}{
		{"company_name", &n.CompanyName, true}, {"sales_ledger", &n.Sales, false},
		{"cgst", &n.CGST, false}, {"sgst", &n.SGST, false}, {"utgst", &n.UTGST, false},
		{"igst", &n.IGST, false}, {"freight", &n.Freight, false},
		{"unregistered_party", &n.UnregisteredParty, false}, {"voucher_type", &n.VoucherType, false},
	} {
		*f.v = strings.TrimSpace(*f.v)
		switch {
		case *f.v == "" && !f.optional:
			return gocommerce.Validationf("%s: name the ledger", f.field)
		case utf8.RuneCountInString(*f.v) > 100:
			return gocommerce.Validationf("%s: at most 100 characters", f.field)
		case strings.ContainsFunc(*f.v, unicode.IsControl):
			return gocommerce.Validationf("%s: no tabs, line breaks or other control characters", f.field)
		}
	}
	// One name is one Tally ledger under one group, so a name may not be both a
	// buyer and an income ledger, or an income and a tax ledger. Sales and
	// delivery may share one, and so may the tax heads.
	group := map[string]string{}
	for _, f := range []struct{ name, group, field string }{
		{n.UnregisteredParty, tallySundryDebtors, "unregistered_party"},
		{n.Sales, tallySalesAccounts, "sales_ledger"}, {n.Freight, tallySalesAccounts, "freight"},
		{n.CGST, tallyDutiesTaxes, "cgst"}, {n.SGST, tallyDutiesTaxes, "sgst"},
		{n.UTGST, tallyDutiesTaxes, "utgst"}, {n.IGST, tallyDutiesTaxes, "igst"},
	} {
		if g, ok := group[f.name]; ok && g != f.group {
			return gocommerce.Validationf("%s: %q is already a ledger under %s", f.field, f.name, g)
		}
		group[f.name] = f.group
	}
	return nil
}

func (m *Module) tallyLedgers(ctx context.Context) (TallyLedgers, error) {
	n := defaultTallyLedgers
	var raw []byte
	err := m.db.QueryRowContext(ctx, `SELECT names FROM invoices_tally_ledgers`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return n, nil
	}
	if err != nil {
		return n, err
	}
	return n, json.Unmarshal(raw, &n)
}

// The Tally parents a ledger is created under: the reserved groups every
// Tally company starts with.
const (
	tallySundryDebtors = "Sundry Debtors"
	tallySalesAccounts = "Sales Accounts"
	tallyDutiesTaxes   = "Duties & Taxes"
)

// tallyLedger is a master the vouchers name. Party is a registered buyer's
// details; the store's own ledgers carry none.
type tallyLedger struct {
	Name, Parent string
	Party        *Party
}

// tallyEntry is one ledger line in Tally's sign: a debit is negative.
type tallyEntry struct {
	Ledger string
	Minor  int64
}

type tallyVoucher struct {
	RemoteID, Order string
	Doc             *TaxInvoice
	Party           string
	// Entries starts with the party's debit; the rest are credits.
	Entries []tallyEntry
}

// tallyVoucherOf turns one issued document into a voucher and the ledgers it
// names. It refuses a voucher whose entries do not sum to zero, because Tally
// would refuse it too ("Voucher totals do not match!"), one file later.
func tallyVoucherOf(storeID, order string, doc *TaxInvoice, n TallyLedgers) (tallyVoucher, []tallyLedger, error) {
	v := tallyVoucher{RemoteID: "gocommerce:" + storeID + ":" + doc.Number, Order: order, Doc: doc}
	var ledgers []tallyLedger
	credit := func(name, parent string, minor int64) {
		if minor != 0 {
			v.Entries = append(v.Entries, tallyEntry{Ledger: name, Minor: minor})
			ledgers = append(ledgers, tallyLedger{Name: name, Parent: parent})
		}
	}

	r := doc.Recipient
	v.Party = n.UnregisteredParty
	party := tallyLedger{Name: v.Party, Parent: tallySundryDebtors}
	if r.Registered {
		// The GSTIN keeps two buyers with one legal name apart.
		v.Party = r.LegalName + " (" + r.GSTIN + ")"
		party = tallyLedger{Name: v.Party, Parent: tallySundryDebtors, Party: &r}
	}
	v.Entries = append(v.Entries, tallyEntry{Ledger: v.Party, Minor: -doc.Totals.TotalMinor})
	ledgers = append(ledgers, party)

	var sales, freight int64
	perRate := map[int]int64{}
	for _, l := range doc.Lines {
		switch l.Kind {
		case LineFreight:
			freight += l.TaxableMinor
		default:
			sales += l.TaxableMinor
			perRate[l.RateBP] += l.TaxableMinor
		}
	}
	if n.SalesPerRate {
		for _, rate := range slices.Sorted(maps.Keys(perRate)) {
			credit(n.Sales+" "+percent(rate, 1), tallySalesAccounts, perRate[rate])
		}
	} else {
		credit(n.Sales, tallySalesAccounts, sales)
	}
	state := n.SGST
	if doc.Supply.StateTaxName == "UTGST" {
		state = n.UTGST
	}
	credit(n.CGST, tallyDutiesTaxes, doc.Totals.CGSTMinor)
	credit(state, tallyDutiesTaxes, doc.Totals.SGSTMinor)
	credit(n.IGST, tallyDutiesTaxes, doc.Totals.IGSTMinor)
	credit(n.Freight, tallySalesAccounts, freight)

	var sum int64
	for _, e := range v.Entries {
		sum += e.Minor
	}
	if sum != 0 {
		return v, nil, gocommerce.Conflictf("invoice %s: its Tally entries miss by %d paise", doc.Number, sum)
	}
	return v, ledgers, nil
}

// tallyPeriod reads from and to, both inclusive dates on the store's clock,
// which is the clock each document's date was written on.
func tallyPeriod(from, to string) error {
	f, err := time.Parse("2006-01-02", from)
	if err != nil {
		return gocommerce.Validationf("from: a date like 2026-04-01")
	}
	t, err := time.Parse("2006-01-02", to)
	if err != nil {
		return gocommerce.Validationf("to: a date like 2027-03-31")
	}
	if t.Before(f) {
		return gocommerce.Validationf("to is before from")
	}
	if !t.Before(f.AddDate(1, 0, 0)) {
		return gocommerce.Validationf("at most a year at a time: a financial year is 1 April to 31 March")
	}
	return nil
}

// tallyRead builds the period's vouchers, in date then number order, and the
// ledgers they name, each once.
func (m *Module) tallyRead(ctx context.Context, from, to string, n TallyLedgers) ([]tallyVoucher, []tallyLedger, error) {
	// ponytail: the date filter reads inside the document and so scans the
	// store's tax invoices; an expression index on tax_doc->>'date' when a
	// store's years of invoices make this slow.
	rows, err := m.db.QueryContext(ctx, `
		SELECT store_id, snapshot->>'number', tax_doc FROM invoices_documents
		WHERE tax_doc IS NOT NULL AND tax_doc->>'date' BETWEEN $1 AND $2
		ORDER BY tax_doc->>'date', year, sequence`, from, to)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var vouchers []tallyVoucher
	var ledgers []tallyLedger
	seen := map[string]bool{}
	for rows.Next() {
		var store, order string
		var raw []byte
		if err := rows.Scan(&store, &order, &raw); err != nil {
			return nil, nil, err
		}
		doc := &TaxInvoice{}
		if err := json.Unmarshal(raw, doc); err != nil {
			return nil, nil, gocommerce.Internalf(err, "read the tax invoice for order %s", order)
		}
		v, named, err := tallyVoucherOf(store, order, doc, n)
		if err != nil {
			return nil, nil, err
		}
		vouchers = append(vouchers, v)
		for _, l := range named {
			if !seen[l.Name] {
				seen[l.Name] = true
				ledgers = append(ledgers, l)
			}
		}
	}
	return vouchers, ledgers, rows.Err()
}

// ------------------------------------------------------------------ the XML

// tallyXML writes Tally's XML. By hand rather than with encoding/xml, for the
// exact element order Tally's samples use and the xmlns:UDF attribute, which
// encoding/xml would treat as a namespace.
type tallyXML struct {
	b     strings.Builder
	depth int
}

func (x *tallyXML) open(tag string, attrs ...string) {
	x.b.WriteString(strings.Repeat(" ", x.depth) + "<" + tag)
	for i := 0; i+1 < len(attrs); i += 2 {
		x.b.WriteString(" " + attrs[i] + `="` + tallyEscape(attrs[i+1]) + `"`)
	}
	x.b.WriteString(">\n")
	x.depth++
}

func (x *tallyXML) close(tag string) {
	x.depth--
	x.b.WriteString(strings.Repeat(" ", x.depth) + "</" + tag + ">\n")
}

func (x *tallyXML) leaf(tag, text string) {
	x.b.WriteString(strings.Repeat(" ", x.depth) + "<" + tag + ">" + tallyEscape(text) + "</" + tag + ">\n")
}

// tallyEscape escapes for XML and writes every character outside ASCII as a
// numeric reference, so the file reads the same whichever encoding Tally
// assumes. A control character XML cannot carry is dropped.
func tallyEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		case r == '\'':
			b.WriteString("&apos;")
		case r == '\t' || r == '\n' || r == '\r' || (r > '~' && r != 0xFFFE && r != 0xFFFF):
			fmt.Fprintf(&b, "&#%d;", r)
		case r < ' ' || r == 0xFFFE || r == 0xFFFF:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// tallyAmount prints paise as rupees with two decimals, by integer division.
func tallyAmount(minor int64) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	return fmt.Sprintf("%s%d.%02d", sign, minor/100, minor%100)
}

func yesNo(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

// tallyEnvelope is the "Import Data" request around the messages.
func tallyEnvelope(report, company string, messages func(*tallyXML)) []byte {
	x := &tallyXML{}
	x.b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	x.open("ENVELOPE")
	x.open("HEADER")
	x.leaf("TALLYREQUEST", "Import Data")
	x.close("HEADER")
	x.open("BODY")
	x.open("IMPORTDATA")
	x.open("REQUESTDESC")
	x.leaf("REPORTNAME", report)
	if company != "" {
		x.open("STATICVARIABLES")
		x.leaf("SVCURRENTCOMPANY", company)
		x.close("STATICVARIABLES")
	}
	x.close("REQUESTDESC")
	x.open("REQUESTDATA")
	messages(x)
	x.close("REQUESTDATA")
	x.close("IMPORTDATA")
	x.close("BODY")
	x.close("ENVELOPE")
	return []byte(x.b.String())
}

func tallyLedgersXML(company string, ledgers []tallyLedger) []byte {
	return tallyEnvelope("All Masters", company, func(x *tallyXML) {
		for _, l := range ledgers {
			x.open("TALLYMESSAGE", "xmlns:UDF", "TallyUDF")
			x.open("LEDGER", "NAME", l.Name, "ACTION", "Create")
			x.leaf("NAME", l.Name)
			x.leaf("PARENT", l.Parent)
			if p := l.Party; p != nil {
				x.open("MAILINGNAME.LIST", "TYPE", "String")
				x.leaf("MAILINGNAME", p.LegalName)
				x.close("MAILINGNAME.LIST")
				x.open("ADDRESS.LIST", "TYPE", "String")
				for _, line := range []string{p.Address.Line1, p.Address.Line2, p.Address.City} {
					if line != "" {
						x.leaf("ADDRESS", line)
					}
				}
				x.close("ADDRESS.LIST")
				x.leaf("PINCODE", p.Address.PostalCode)
				x.leaf("LEDSTATENAME", p.State)
				x.leaf("COUNTRYNAME", "India")
			}
			x.close("LEDGER")
			x.close("TALLYMESSAGE")
		}
	})
}

func tallyVouchersXML(company, voucherType string, vouchers []tallyVoucher) []byte {
	return tallyEnvelope("Vouchers", company, func(x *tallyXML) {
		for _, v := range vouchers {
			d := v.Doc
			x.open("TALLYMESSAGE", "xmlns:UDF", "TallyUDF")
			x.open("VOUCHER", "REMOTEID", v.RemoteID, "VCHTYPE", voucherType, "ACTION", "Create",
				"OBJVIEW", "Invoice Voucher View")
			x.leaf("DATE", strings.ReplaceAll(d.Date, "-", ""))
			x.leaf("VOUCHERTYPENAME", voucherType)
			x.leaf("VOUCHERNUMBER", d.Number)
			x.leaf("REFERENCE", v.Order)
			x.leaf("PARTYLEDGERNAME", v.Party)
			x.leaf("PERSISTEDVIEW", "Invoice Voucher View")
			x.leaf("ISINVOICE", "Yes")
			x.leaf("CMPGSTIN", d.Supplier.GSTIN)
			if d.Recipient.Registered {
				x.leaf("GSTREGISTRATIONTYPE", "Regular")
				x.leaf("PARTYGSTIN", d.Recipient.GSTIN)
			} else {
				x.leaf("GSTREGISTRATIONTYPE", "Unregistered/Consumer")
			}
			x.leaf("STATENAME", d.Recipient.State)
			x.leaf("PLACEOFSUPPLY", d.Supply.PlaceOfSupply)
			for i, e := range v.Entries {
				x.open("ALLLEDGERENTRIES.LIST")
				x.leaf("LEDGERNAME", e.Ledger)
				// The party is the debit even on an invoice for nothing.
				x.leaf("ISDEEMEDPOSITIVE", yesNo(i == 0 || e.Minor < 0))
				x.leaf("ISPARTYLEDGER", yesNo(i == 0))
				x.leaf("AMOUNT", tallyAmount(e.Minor))
				if i == 0 {
					x.open("BILLALLOCATIONS.LIST")
					x.leaf("NAME", d.Number)
					x.leaf("BILLTYPE", "New Ref")
					x.leaf("AMOUNT", tallyAmount(e.Minor))
					x.close("BILLALLOCATIONS.LIST")
				}
				x.close("ALLLEDGERENTRIES.LIST")
			}
			x.close("VOUCHER")
			x.close("TALLYMESSAGE")
		}
	})
}

// ---------------------------------------------------------------- endpoints

// gstOnly answers 404 on a store that issues no GST tax invoices, for which
// there is nothing to send to Tally.
func (m *Module) gstOnly(w http.ResponseWriter, r *http.Request) bool {
	reg, err := m.regimeFor(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return false
	}
	if reg == nil {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf(
			"this store does not issue GST tax invoices, so there is nothing to export to Tally"))
		return false
	}
	return true
}

func (m *Module) handleTallyExport(w http.ResponseWriter, r *http.Request) {
	if !m.gstOnly(w, r) {
		return
	}
	q := r.URL.Query()
	part := q.Get("part")
	if part != "ledgers" && part != "vouchers" {
		gocommerce.RespondError(w, r, gocommerce.Validationf("part: ledgers or vouchers"))
		return
	}
	from, to := q.Get("from"), q.Get("to")
	if err := tallyPeriod(from, to); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	n, err := m.tallyLedgers(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	vouchers, ledgers, err := m.tallyRead(r.Context(), from, to, n)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	body := tallyVouchersXML(n.CompanyName, n.VoucherType, vouchers)
	if part == "ledgers" {
		body = tallyLedgersXML(n.CompanyName, ledgers)
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="tally-%s-%s-%s.xml"`, part, from, to))
	w.Write(body)
}

func (m *Module) handleTallyLedgersGet(w http.ResponseWriter, r *http.Request) {
	if !m.gstOnly(w, r) {
		return
	}
	n, err := m.tallyLedgers(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, n)
}

// handleTallyLedgersPut takes the fields it is sent over the saved names.
func (m *Module) handleTallyLedgersPut(w http.ResponseWriter, r *http.Request) {
	if !m.gstOnly(w, r) {
		return
	}
	n, err := m.tallyLedgers(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if err := gocommerce.DecodeJSON(w, r, &n); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if err := n.clean(); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	raw, _ := json.Marshal(n)
	if _, err := m.db.ExecContext(r.Context(), `
		INSERT INTO invoices_tally_ledgers (names) VALUES ($1)
		ON CONFLICT (store_id) DO UPDATE SET names = EXCLUDED.names, updated_at = now()`, raw); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, n)
}
