package invoices

import (
	"context"
	"encoding/xml"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

type tallyFile struct {
	Request  string `xml:"HEADER>TALLYREQUEST"`
	Report   string `xml:"BODY>IMPORTDATA>REQUESTDESC>REPORTNAME"`
	Company  string `xml:"BODY>IMPORTDATA>REQUESTDESC>STATICVARIABLES>SVCURRENTCOMPANY"`
	Messages []struct {
		Voucher *tallyVoucherXML `xml:"VOUCHER"`
		Ledger  *tallyLedgerXML  `xml:"LEDGER"`
	} `xml:"BODY>IMPORTDATA>REQUESTDATA>TALLYMESSAGE"`
}

type tallyVoucherXML struct {
	RemoteID    string `xml:"REMOTEID,attr"`
	Date        string `xml:"DATE"`
	Number      string `xml:"VOUCHERNUMBER"`
	Reference   string `xml:"REFERENCE"`
	Party       string `xml:"PARTYLEDGERNAME"`
	PartyGSTIN  string `xml:"PARTYGSTIN"`
	RegType     string `xml:"GSTREGISTRATIONTYPE"`
	State       string `xml:"STATENAME"`
	Place       string `xml:"PLACEOFSUPPLY"`
	CompanyGSTN string `xml:"CMPGSTIN"`
	Entries     []struct {
		Ledger   string `xml:"LEDGERNAME"`
		Positive string `xml:"ISDEEMEDPOSITIVE"`
		Party    string `xml:"ISPARTYLEDGER"`
		Amount   string `xml:"AMOUNT"`
		Bills    []struct {
			Name   string `xml:"NAME"`
			Type   string `xml:"BILLTYPE"`
			Amount string `xml:"AMOUNT"`
		} `xml:"BILLALLOCATIONS.LIST"`
	} `xml:"ALLLEDGERENTRIES.LIST"`
}

type tallyLedgerXML struct {
	Name    string   `xml:"NAME,attr"`
	Parent  string   `xml:"PARENT"`
	Mailing string   `xml:"MAILINGNAME.LIST>MAILINGNAME"`
	Address []string `xml:"ADDRESS.LIST>ADDRESS"`
	Pincode string   `xml:"PINCODE"`
	State   string   `xml:"LEDSTATENAME"`
	Country string   `xml:"COUNTRYNAME"`
}

// entry is a voucher line as "ledger amount", the shape every test compares.
func (v *tallyVoucherXML) entries() []string {
	var out []string
	for _, e := range v.Entries {
		out = append(out, e.Ledger+" "+e.Amount)
	}
	return out
}

// paise reads Tally's "-118.00" back without floats.
func paise(t *testing.T, s string) int64 {
	t.Helper()
	whole, frac, ok := strings.Cut(strings.TrimPrefix(s, "-"), ".")
	w, err1 := strconv.ParseInt(whole, 10, 64)
	f, err2 := strconv.ParseInt(frac, 10, 64)
	if !ok || len(frac) != 2 || err1 != nil || err2 != nil {
		t.Fatalf("amount %q is not rupees with two decimals", s)
	}
	if strings.HasPrefix(s, "-") {
		return -(w*100 + f)
	}
	return w*100 + f
}

// exportTally fetches one part and parses it, and fails on any voucher whose
// entries do not sum to zero or whose sign does not match its debit flag.
func exportTally(t *testing.T, app *gocommerce.App, part, from, to string) (tallyFile, string) {
	t.Helper()
	rec := gctest.AdminRequest(t, app, http.MethodGet,
		"/api/admin/x/invoices/tally?part="+part+"&from="+from+"&to="+to, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("export %s = %d: %s", part, rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/xml; charset=utf-8" {
		t.Errorf("content type = %q", got)
	}
	if got, want := rec.Header().Get("Content-Disposition"),
		`attachment; filename="tally-`+part+"-"+from+"-"+to+`.xml"`; got != want {
		t.Errorf("disposition = %q, want %q", got, want)
	}
	body := rec.Body.String()
	var f tallyFile
	if err := xml.Unmarshal([]byte(body), &f); err != nil {
		t.Fatalf("the %s file does not parse: %v\n%s", part, err, body)
	}
	if f.Request != "Import Data" {
		t.Errorf("TALLYREQUEST = %q", f.Request)
	}
	for _, m := range f.Messages {
		v := m.Voucher
		if v == nil {
			continue
		}
		var sum int64
		for _, e := range v.Entries {
			a := paise(t, e.Amount)
			sum += a
			if (a < 0) != (e.Positive == "Yes") {
				t.Errorf("voucher %s: %s %s is marked ISDEEMEDPOSITIVE %s", v.Number, e.Ledger, e.Amount, e.Positive)
			}
		}
		if sum != 0 {
			t.Errorf("voucher %s: the entries sum to %d paise, want 0: %v", v.Number, sum, v.entries())
		}
	}
	return f, body
}

func vouchersOf(f tallyFile) []*tallyVoucherXML {
	var out []*tallyVoucherXML
	for _, m := range f.Messages {
		if m.Voucher != nil {
			out = append(out, m.Voucher)
		}
	}
	return out
}

func ledgersOf(f tallyFile) map[string]*tallyLedgerXML {
	out := map[string]*tallyLedgerXML{}
	for _, m := range f.Messages {
		if m.Ledger != nil {
			out[m.Ledger.Name] = m.Ledger
		}
	}
	return out
}

// today is the date an invoice issued now carries.
func today() string { return time.Now().In(ist).Format("2006-01-02") }

func onlyVoucher(t *testing.T, app *gocommerce.App) *tallyVoucherXML {
	t.Helper()
	f, _ := exportTally(t, app, "vouchers", today(), today())
	vs := vouchersOf(f)
	if len(vs) != 1 {
		t.Fatalf("%d vouchers, want 1", len(vs))
	}
	return vs[0]
}

func TestTallyVouchersForAnIntraStateInvoice(t *testing.T) {
	app := gstStore(t, gocommerce.Config{}, gstModule(""), "Odisha", supplierGSTIN)
	taxRate(t, app, gocommerce.TaxRateInput{Name: "GST 18%", RateBP: 1800, Country: "IN"})
	cloth := hsnProduct(t, app, "CLOTH", 10000, "630790")
	order := buyAndPay(t, app, purchase{Address: odisha, Lines: []*gocommerce.Product{cloth}})
	number := taxDocOf(t, app, order.ID).Number
	var store string
	if err := app.DB().QueryRowContext(context.Background(), `SELECT store_id FROM invoices_documents`).Scan(&store); err != nil {
		t.Fatal(err)
	}

	f, _ := exportTally(t, app, "vouchers", today(), today())
	if f.Report != "Vouchers" || f.Company != "" {
		t.Errorf("report %q, company %q, want Vouchers and no company", f.Report, f.Company)
	}
	v := vouchersOf(f)[0]
	if want := []string{"Cash Sales -118.00", "Sales 100.00", "CGST 9.00", "SGST 9.00"}; !reflect.DeepEqual(v.entries(), want) {
		t.Errorf("entries = %v, want %v", v.entries(), want)
	}
	if v.RemoteID != "gocommerce:"+store+":"+number || v.Number != number || v.Reference != order.Number {
		t.Errorf("remote id %q, number %q, reference %q", v.RemoteID, v.Number, v.Reference)
	}
	if v.Date != strings.ReplaceAll(today(), "-", "") || v.Party != "Cash Sales" || v.PartyGSTIN != "" ||
		v.RegType != "Unregistered/Consumer" || v.Place != "Odisha" || v.State != "Odisha" || v.CompanyGSTN != supplierGSTIN {
		t.Errorf("voucher = %+v", v)
	}
	bills := v.Entries[0].Bills
	if v.Entries[0].Party != "Yes" || len(bills) != 1 || bills[0].Name != number || bills[0].Type != "New Ref" || bills[0].Amount != "-118.00" {
		t.Errorf("party entry = %+v, want a New Ref bill of -118.00", v.Entries[0])
	}
}

func TestTallyVouchersForAnInterStateInvoice(t *testing.T) {
	app := gstStore(t, gocommerce.Config{}, gstModule(""), "Odisha", supplierGSTIN)
	taxRate(t, app, gocommerce.TaxRateInput{Name: "GST 18%", RateBP: 1800, Country: "IN"})
	cloth := hsnProduct(t, app, "CLOTH", 10000, "630790")
	buyAndPay(t, app, purchase{Address: maharashtra, Lines: []*gocommerce.Product{cloth},
		Metadata: gocommerce.Metadata{"gst": map[string]any{"gstin": "27AAPFU0939F1ZV", "name": "Mumbai Traders"}}})

	v := onlyVoucher(t, app)
	party := "Mumbai Traders (27AAPFU0939F1ZV)"
	if want := []string{party + " -118.00", "Sales 100.00", "IGST 18.00"}; !reflect.DeepEqual(v.entries(), want) {
		t.Errorf("entries = %v, want %v", v.entries(), want)
	}
	if v.Party != party || v.PartyGSTIN != "27AAPFU0939F1ZV" || v.RegType != "Regular" || v.Place != "Maharashtra" {
		t.Errorf("voucher = %+v", v)
	}
}

func TestTallySalesPerRateUsesTheSavedNames(t *testing.T) {
	app := gstStore(t, gocommerce.Config{}, gstModule(""), "Odisha", supplierGSTIN)
	chain := hsnProduct(t, app, "CHAIN", 10000, "711319")
	cloth := hsnProduct(t, app, "CLOTH", 10000, "630790")
	taxRate(t, app, gocommerce.TaxRateInput{Name: "GST 18%", RateBP: 1800, Country: "IN"})
	taxRate(t, app, gocommerce.TaxRateInput{Name: "GST 3%", RateBP: 300, Country: "IN", ProductID: &chain.ID})
	buyAndPay(t, app, purchase{Address: odisha, Lines: []*gocommerce.Product{chain, cloth}})

	rec := gctest.AdminRequest(t, app, http.MethodPut, "/api/admin/x/invoices/tally/ledgers", map[string]any{
		"sales_per_rate": true, "cgst": " Output CGST ", "company_name": "Example Jewels 2026-27",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", rec.Code, rec.Body)
	}

	f, _ := exportTally(t, app, "vouchers", today(), today())
	if f.Company != "Example Jewels 2026-27" {
		t.Errorf("company = %q", f.Company)
	}
	want := []string{"Cash Sales -221.00", "Sales 3% 100.00", "Sales 18% 100.00", "Output CGST 10.50", "SGST 10.50"}
	if got := vouchersOf(f)[0].entries(); !reflect.DeepEqual(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
	lf, _ := exportTally(t, app, "ledgers", today(), today())
	ledgers := ledgersOf(lf)
	for name, parent := range map[string]string{"Sales 3%": "Sales Accounts", "Sales 18%": "Sales Accounts",
		"Output CGST": "Duties & Taxes", "SGST": "Duties & Taxes", "Cash Sales": "Sundry Debtors"} {
		if l := ledgers[name]; l == nil || l.Parent != parent {
			t.Errorf("ledger %q = %+v, want it under %s", name, l, parent)
		}
	}
	if len(ledgers) != 5 || lf.Report != "All Masters" {
		t.Errorf("report %q, ledgers %v, want All Masters and only the five the voucher names", lf.Report, ledgers)
	}
}

func TestTallyDeliveryGoesToTheFreightLedger(t *testing.T) {
	app := gstStore(t, gocommerce.Config{FlatShippingMinor: 5000}, gstModule(""), "Odisha", supplierGSTIN)
	taxRate(t, app, gocommerce.TaxRateInput{Name: "GST 18%", RateBP: 1800, Country: "IN"})
	buyAndPay(t, app, purchase{Address: odisha, Lines: []*gocommerce.Product{hsnProduct(t, app, "CLOTH", 10000, "630790")}})

	want := []string{"Cash Sales -168.00", "Sales 100.00", "CGST 9.00", "SGST 9.00", "Delivery Charges 50.00"}
	if got := onlyVoucher(t, app).entries(); !reflect.DeepEqual(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
}

func TestTallyCarriesTheOddPaisa(t *testing.T) {
	app := gstStore(t, gocommerce.Config{}, gstModule(""), "Odisha", supplierGSTIN)
	taxRate(t, app, gocommerce.TaxRateInput{Name: "GST 5%", RateBP: 500, Country: "IN"})
	buyAndPay(t, app, purchase{Address: odisha, Lines: []*gocommerce.Product{hsnProduct(t, app, "TEA", 3333, "0902")}})

	want := []string{"Cash Sales -35.00", "Sales 33.33", "CGST 0.83", "SGST 0.84"}
	if got := onlyVoucher(t, app).entries(); !reflect.DeepEqual(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
}

func TestTallyLedgerNamesDefaultAndRefuseBadOnes(t *testing.T) {
	app := gstStore(t, gocommerce.Config{}, gstModule(""), "Odisha", supplierGSTIN)
	path := "/api/admin/x/invoices/tally/ledgers"
	read := func() TallyLedgers {
		t.Helper()
		var n TallyLedgers
		gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet, path, nil), &n)
		return n
	}
	defaults := TallyLedgers{Sales: "Sales", CGST: "CGST", SGST: "SGST", UTGST: "UTGST", IGST: "IGST",
		Freight: "Delivery Charges", UnregisteredParty: "Cash Sales", VoucherType: "Sales"}
	if got := read(); got != defaults {
		t.Errorf("unset = %+v, want %+v", got, defaults)
	}
	for _, c := range []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"igst": "  "}, "igst: name the ledger"},
		{map[string]any{"sales_ledger": strings.Repeat("ś", 101)}, "sales_ledger: at most 100 characters"},
		{map[string]any{"freight": "Delivery\nCharges"}, "freight: no tabs, line breaks"},
		{map[string]any{"cgst": "CGST", "gst": "x"}, "unknown field"},
	} {
		rec := gctest.AdminRequest(t, app, http.MethodPut, path, c.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("PUT %v = %d %s, want 400 %q", c.body, rec.Code, rec.Body, c.want)
		}
	}
	if got := read(); got != defaults {
		t.Errorf("after refusals = %+v, want the defaults untouched", got)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodPut, path, map[string]any{"sales_ledger": strings.Repeat("ś", 100)}); rec.Code != http.StatusOK {
		t.Errorf("a 100-character name = %d: %s", rec.Code, rec.Body)
	}
	want := defaults
	want.Sales = strings.Repeat("ś", 100)
	if got := read(); got != want {
		t.Errorf("saved = %+v, want %+v", got, want)
	}
}

func TestTallyLedgersListEachRegisteredBuyerOnce(t *testing.T) {
	app := gstStore(t, gocommerce.Config{}, gstModule(""), "Odisha", supplierGSTIN)
	taxRate(t, app, gocommerce.TaxRateInput{Name: "GST 18%", RateBP: 1800, Country: "IN"})
	cloth := hsnProduct(t, app, "CLOTH", 10000, "630790")
	asha := gocommerce.Metadata{"gst": map[string]any{"gstin": "21AABCT1332L1ZQ", "name": "Asha Traders Pvt Ltd"}}
	buyAndPay(t, app, purchase{Address: odisha, Lines: []*gocommerce.Product{cloth}, Metadata: asha})
	buyAndPay(t, app, purchase{Address: odisha, Lines: []*gocommerce.Product{cloth}, Metadata: asha})
	buyAndPay(t, app, purchase{Address: odisha, Lines: []*gocommerce.Product{cloth}})

	f, _ := exportTally(t, app, "ledgers", today(), today())
	ledgers := ledgersOf(f)
	if len(f.Messages) != len(ledgers) {
		t.Errorf("%d ledgers written for %d names: one is repeated", len(f.Messages), len(ledgers))
	}
	want := &tallyLedgerXML{Name: "Asha Traders Pvt Ltd (21AABCT1332L1ZQ)", Parent: "Sundry Debtors",
		Mailing: "Asha Traders Pvt Ltd", Address: []string{"4 Market St", "Cuttack"}, Pincode: "753001",
		State: "Odisha", Country: "India"}
	if got := ledgers[want.Name]; !reflect.DeepEqual(got, want) {
		t.Errorf("buyer ledger = %+v, want %+v", got, want)
	}
	if l := ledgers["Cash Sales"]; l == nil || l.Parent != "Sundry Debtors" || l.Pincode != "" {
		t.Errorf("unregistered party ledger = %+v", l)
	}
	if _, ok := ledgers["Asha Traders"]; ok {
		t.Error("an unregistered buyer got a ledger of their own")
	}
	vs := vouchersOf(mustExport(t, app, "vouchers"))
	if len(vs) != 3 || vs[0].Party != want.Name || vs[2].Party != "Cash Sales" {
		t.Errorf("vouchers = %d, parties %q %q", len(vs), vs[0].Party, vs[len(vs)-1].Party)
	}
	for i := 1; i < len(vs); i++ {
		if vs[i-1].Number >= vs[i].Number {
			t.Errorf("voucher %s comes before %s", vs[i-1].Number, vs[i].Number)
		}
	}
}

func mustExport(t *testing.T, app *gocommerce.App, part string) tallyFile {
	t.Helper()
	f, _ := exportTally(t, app, part, today(), today())
	return f
}

func TestTallyEscapesNames(t *testing.T) {
	app := gstStore(t, gocommerce.Config{}, gstModule(""), "Odisha", supplierGSTIN)
	cloth := hsnProduct(t, app, "CLOTH", 10000, "630790")
	for _, b := range []struct{ gstin, name string }{
		{"21AABCT1332L1ZQ", `A&B <Traders> "Ltd"`},
		{"27AAPFU0939F1ZV", "Śrī Gaṇeśa ज्वेलर्स"},
	} {
		buyAndPay(t, app, purchase{Address: odisha, Lines: []*gocommerce.Product{cloth},
			Metadata: gocommerce.Metadata{"gst": map[string]any{"gstin": b.gstin, "name": b.name}}})
	}
	for _, part := range []string{"ledgers", "vouchers"} {
		f, body := exportTally(t, app, part, today(), today())
		for i, r := range body {
			if r > '~' {
				t.Fatalf("the %s file has %q at %d: everything outside ASCII must be a character reference", part, r, i)
			}
		}
		if !strings.Contains(body, "A&amp;B &lt;Traders&gt;") || !strings.Contains(body, "&#346;r&#299;") {
			t.Errorf("the %s file does not escape the names:\n%s", part, body)
		}
		names := map[string]bool{}
		for _, v := range vouchersOf(f) {
			names[v.Party] = true
		}
		for n := range ledgersOf(f) {
			names[n] = true
		}
		for _, want := range []string{`A&B <Traders> "Ltd" (21AABCT1332L1ZQ)`, "Śrī Gaṇeśa ज्वेलर्स (27AAPFU0939F1ZV)"} {
			if !names[want] {
				t.Errorf("the %s file does not round-trip %q: %v", part, want, names)
			}
		}
	}
}

func TestTallyPeriodIsOnTheStoresClock(t *testing.T) {
	mod := gstModule("")
	mod.now = func() time.Time { return time.Date(2026, 10, 27, 19, 0, 0, 0, time.UTC) }
	app := gstStore(t, gocommerce.Config{}, mod, "Odisha", supplierGSTIN)
	buyAndPay(t, app, purchase{Address: odisha, Lines: []*gocommerce.Product{hsnProduct(t, app, "CLOTH", 10000, "630790")}})

	f, _ := exportTally(t, app, "vouchers", "2026-10-28", "2026-10-28")
	if vs := vouchersOf(f); len(vs) != 1 || vs[0].Date != "20261028" {
		t.Errorf("28 Oct = %+v, want the invoice issued at 19:00 UTC on 27 Oct, dated 20261028", vs)
	}
	if f, _ := exportTally(t, app, "vouchers", "2026-04-01", "2026-10-27"); len(f.Messages) != 0 {
		t.Errorf("up to 27 Oct = %d messages, want none", len(f.Messages))
	}
	for query, want := range map[string]string{
		"part=vouchers&from=2026-04-01&to=2027-04-01": "at most a year",
		"part=vouchers&from=2026-10-28&to=2026-10-27": "to is before from",
		"part=vouchers&from=28-10-2026&to=2026-10-28": "from: a date",
		"part=masters&from=2026-10-28&to=2026-10-28":  "part: ledgers or vouchers",
	} {
		rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/invoices/tally?"+query, nil)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s = %d %s, want 400 %q", query, rec.Code, rec.Body, want)
		}
	}
	if f, _ := exportTally(t, app, "vouchers", "2026-04-01", "2027-03-31"); len(f.Messages) != 1 {
		t.Errorf("the financial year = %d messages, want 1", len(f.Messages))
	}
}

func TestTallyIsNotOfferedWithoutGST(t *testing.T) {
	app := gctest.New(t, testModule())
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/admin/x/invoices/tally?part=vouchers&from=2026-04-01&to=2026-10-27"},
		{http.MethodGet, "/api/admin/x/invoices/tally/ledgers"},
		{http.MethodPut, "/api/admin/x/invoices/tally/ledgers"},
	} {
		rec := gctest.AdminRequest(t, app, c.method, c.path, map[string]any{"cgst": "CGST"})
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "does not issue GST tax invoices") {
			t.Errorf("%s %s = %d %s, want 404", c.method, c.path, rec.Code, rec.Body)
		}
	}
	var rows int
	if err := app.DB().QueryRowContext(context.Background(), `SELECT count(*) FROM invoices_tally_ledgers`).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("%d ledger rows (%v), want none written", rows, err)
	}
}

func TestTallyRights(t *testing.T) {
	app := gstStore(t, gocommerce.Config{}, gstModule(""), "Odisha", supplierGSTIN)
	path := "/api/admin/x/invoices/tally/ledgers"
	staff := gctest.OperatorToken(t, app, "staff@example.com", gocommerce.RoleStaff)
	if rec := gctest.SessionRequest(t, app, staff, http.MethodGet, path, nil); rec.Code != http.StatusOK {
		t.Errorf("staff reading the names = %d, want 200", rec.Code)
	}
	if rec := gctest.SessionRequest(t, app, staff, http.MethodPut, path, map[string]any{"cgst": "X"}); rec.Code != http.StatusForbidden {
		t.Errorf("staff saving the names = %d, want 403: only a manager may", rec.Code)
	}
	manager := gctest.OperatorToken(t, app, "manager@example.com", gocommerce.RoleManager)
	if rec := gctest.SessionRequest(t, app, manager, http.MethodPut, path, map[string]any{"cgst": "X"}); rec.Code != http.StatusOK {
		t.Errorf("a manager saving the names = %d, want 200: %s", rec.Code, rec.Body)
	}
	agent := gctest.AgentKey(t, app, rightInvoicesRead)
	rec := gctest.SessionRequest(t, app, agent, http.MethodGet,
		"/api/admin/x/invoices/tally?part=ledgers&from=2026-04-01&to=2026-10-27", nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "agent_key_refused") {
		t.Errorf("an agent exporting = %d, want 403: the file names buyers and their addresses: %s", rec.Code, rec.Body)
	}
}

func TestATallyVoucherThatDoesNotBalanceIsRefused(t *testing.T) {
	t.Parallel()
	doc := &TaxInvoice{Number: "INV/26-27/00001",
		Lines:  []TaxLine{{Kind: LineGoods, RateBP: 1800, TaxableMinor: 10000, CGSTMinor: 900, SGSTMinor: 900, TotalMinor: 11800}},
		Totals: Totals{TaxableMinor: 10000, CGSTMinor: 900, SGSTMinor: 900, TaxMinor: 1800, RoundOffMinor: 1, TotalMinor: 11801}}
	if _, _, err := tallyVoucherOf("s", "1001", doc, defaultTallyLedgers); err == nil ||
		!strings.Contains(err.Error(), "miss by -1 paise") {
		t.Errorf("an unbalanced voucher = %v, want a refusal", err)
	}
}
