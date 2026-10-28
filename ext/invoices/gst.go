package invoices

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/itswadesh/gocommerce/core"
)

// India's GST tax invoice (CGST Rules 2017, rule 46; D137).
//
// It is a re-presentation of what checkout charged, never a second
// calculation. The rate on each line is the rate core charged, the tax is the
// tax core charged, and this file only decides which heads it is printed under.
// The result is built once, at issue, and stored as one document; the page and
// any export read that document and nothing else, so a reprint next year is
// the invoice that was issued.
//
// Rounding: checkout's tax per line is final. Within a state, CGST is the
// floor of half the tax and SGST (or UTGST) the rest, so an odd paisa goes to
// the state and the parts always sum to what was charged. Between states and
// for exports, all of it is IGST. The taxable value is the line total less its
// share of the discount, less the tax when prices include it.

// SupplyKind says which tax heads apply.
type SupplyKind string

const (
	SupplyIntra  SupplyKind = "intra"  // same state: CGST + SGST or UTGST
	SupplyInter  SupplyKind = "inter"  // another state: IGST
	SupplyExport SupplyKind = "export" // outside India: IGST
)

// LineKind tells goods from the delivery charge.
type LineKind string

const (
	LineGoods   LineKind = "goods"
	LineFreight LineKind = "freight"
)

// Address is a party's postal address as printed.
type Address struct {
	Line1      string `json:"line1"`
	Line2      string `json:"line2"`
	City       string `json:"city"`
	PostalCode string `json:"postal_code"`
	Country    string `json:"country"`
}

// Party is the supplier or the recipient.
type Party struct {
	LegalName string `json:"legal_name"`
	TradeName string `json:"trade_name,omitempty"`
	// GSTIN is empty for an unregistered recipient.
	GSTIN      string  `json:"gstin,omitempty"`
	Registered bool    `json:"registered"`
	StateCode  string  `json:"state_code"`
	State      string  `json:"state"`
	Address    Address `json:"address"`
	// The supplier's own contacts; never the buyer's, which an operator
	// under the demo mask must not see.
	Email string `json:"email,omitempty"`
	Phone string `json:"phone,omitempty"`
}

// Supply is where the supply is made and so which heads apply.
type Supply struct {
	Kind              SupplyKind `json:"kind"`
	PlaceOfSupplyCode string     `json:"place_of_supply_code"`
	PlaceOfSupply     string     `json:"place_of_supply"`
	// StateTaxName is "SGST" or "UTGST" within a state, empty otherwise.
	StateTaxName string `json:"state_tax_name"`
	// ReverseCharge is always false today; it is stored so every document
	// has the field rule 46 asks for.
	ReverseCharge bool `json:"reverse_charge"`
}

// TaxLine is one printed line. SGSTMinor is UTGST when Supply.StateTaxName
// says so. Every amount is in minor units of the document's currency.
type TaxLine struct {
	N             int      `json:"n"`
	Kind          LineKind `json:"kind"`
	SKU           string   `json:"sku,omitempty"`
	Description   string   `json:"description"`
	HSN           string   `json:"hsn,omitempty"`
	Qty           int      `json:"qty"`
	Unit          string   `json:"unit"`
	GrossMinor    int64    `json:"gross_minor"`
	DiscountMinor int64    `json:"discount_minor"`
	TaxableMinor  int64    `json:"taxable_minor"`
	RateBP        int      `json:"rate_bp"`
	CGSTMinor     int64    `json:"cgst_minor"`
	SGSTMinor     int64    `json:"sgst_minor"`
	IGSTMinor     int64    `json:"igst_minor"`
	TotalMinor    int64    `json:"total_minor"`
}

// RateRow is the rate-wise summary, one row per rate, so an export needs no
// arithmetic of its own.
type RateRow struct {
	RateBP       int   `json:"rate_bp"`
	TaxableMinor int64 `json:"taxable_minor"`
	CGSTMinor    int64 `json:"cgst_minor"`
	SGSTMinor    int64 `json:"sgst_minor"`
	IGSTMinor    int64 `json:"igst_minor"`
}

// Totals sum the lines.
type Totals struct {
	TaxableMinor  int64 `json:"taxable_minor"`
	CGSTMinor     int64 `json:"cgst_minor"`
	SGSTMinor     int64 `json:"sgst_minor"`
	IGSTMinor     int64 `json:"igst_minor"`
	TaxMinor      int64 `json:"tax_minor"`
	RoundOffMinor int64 `json:"round_off_minor"`
	TotalMinor    int64 `json:"total_minor"`
}

// Warning is something an accountant should look at. Codes: freight_untaxed,
// hsn_missing (Ref is the SKU), buyer_gstin_ignored (Ref is why).
type Warning struct {
	Code string `json:"code"`
	Ref  string `json:"ref,omitempty"`
}

// TaxInvoice is the issued document, frozen in invoices_documents.tax_doc.
// Version lets a reader branch on its shape for as long as it is kept.
type TaxInvoice struct {
	Version       int       `json:"version"`
	DocType       string    `json:"doc_type"`
	Number        string    `json:"number"`
	Date          string    `json:"date"`
	FinancialYear string    `json:"financial_year"`
	Currency      string    `json:"currency"`
	Supplier      Party     `json:"supplier"`
	Recipient     Party     `json:"recipient"`
	Supply        Supply    `json:"supply"`
	Lines         []TaxLine `json:"lines"`
	ByRate        []RateRow `json:"by_rate"`
	Totals        Totals    `json:"totals"`
	Warnings      []Warning `json:"warnings"`
}

// regime is a store that issues GST tax invoices. A nil *regime is a store
// that issues the plain invoice, exactly as before.
type regime struct {
	Supplier Party
	GSTIN    gocommerce.GSTIN
	// Location is the store's clock, which decides the invoice date and the
	// financial year. India's when the profile names none.
	Location *time.Location
}

// ist is India's clock without the zone database: India has one zone and no
// daylight saving, and a host without zoneinfo must not move 1 April.
var ist = time.FixedZone("IST", 5*3600+1800)

// regimeFor is GST mode for the store on ctx: its profile is in India, its
// tax id is a GSTIN and it sells in rupees. Anything less is a plain invoice.
func (m *Module) regimeFor(ctx context.Context) (*regime, error) {
	p, err := m.app.Profile().Get(ctx)
	if err != nil {
		return nil, err
	}
	if p.Country != "IN" || p.Currency != "INR" {
		return nil, nil
	}
	g, err := gocommerce.ParseGSTIN(p.TaxID)
	if err != nil {
		return nil, nil
	}
	loc := ist
	if p.Timezone != "" {
		if l, err := time.LoadLocation(p.Timezone); err == nil {
			loc = l
		}
	}
	legal := p.LegalName
	if legal == "" {
		legal = p.Name
	}
	st := g.State()
	return &regime{
		GSTIN:    g,
		Location: loc,
		Supplier: Party{
			LegalName: legal, TradeName: p.Name, GSTIN: string(g), Registered: true,
			StateCode: st.Code, State: st.Name,
			Address: Address{Line1: p.AddressLine1, Line2: p.AddressLine2, City: p.City,
				PostalCode: p.PostalCode, Country: "IN"},
			Email: p.Email, Phone: p.Phone,
		},
	}, nil
}

// seriesYear is the year a number is counted in: the calendar year in UTC for
// a plain store, as it always was, and for a GST store the year its financial
// year started, which turns on 1 April on the store's clock.
func seriesYear(now time.Time, reg *regime) int {
	if reg == nil {
		return now.UTC().Year()
	}
	t := now.In(reg.Location)
	if t.Month() < time.April {
		return t.Year() - 1
	}
	return t.Year()
}

// gstNumber is rule 46's serial: at most 16 characters, of letters, digits,
// hyphen and slash.
var gstNumber = regexp.MustCompile(`^[A-Za-z0-9/-]{1,16}$`)

// taxInput is everything buildTaxInvoice needs; it does no I/O.
type taxInput struct {
	Number   string
	IssuedAt time.Time
	Regime   regime
	Order    *gocommerce.Order
	// DiscountShares is Orders.LineDiscountShares, aligned with Order.Lines.
	DiscountShares []int64
	// HSN is each variant's code, by variant id.
	HSN map[int64]string
}

// buildTaxInvoice is the GST rule set as one pure function. It refuses rather
// than issue a document whose figures do not add up to what the buyer paid,
// or one whose place of supply it would have to guess: a wrong tax head on a
// legal document is worse than a late one.
func buildTaxInvoice(in taxInput) (*TaxInvoice, error) {
	o := in.Order
	if len(in.DiscountShares) != len(o.Lines) {
		return nil, fmt.Errorf("%d discount shares for %d lines", len(in.DiscountShares), len(o.Lines))
	}
	supply, place, err := supplyOf(o.Address, in.Regime.GSTIN.State())
	if err != nil {
		return nil, err
	}
	fy := seriesYear(in.IssuedAt, &in.Regime)
	doc := &TaxInvoice{
		Version: 1, DocType: "INV", Number: in.Number,
		Date:          in.IssuedAt.In(in.Regime.Location).Format("2006-01-02"),
		FinancialYear: fmt.Sprintf("%d-%02d", fy, (fy+1)%100),
		Currency:      o.Currency,
		Supplier:      in.Regime.Supplier,
		Supply:        supply,
		Lines:         []TaxLine{},
		Warnings:      []Warning{},
	}
	doc.Recipient = recipientOf(o, place, &doc.Warnings)

	for i, l := range o.Lines {
		share, tax := in.DiscountShares[i], l.Tax.AmountMinor
		base := l.Total.AmountMinor - share
		taxable, total := base, base+tax
		if o.TaxInclusive {
			taxable, total = base-tax, base
		}
		cgst, sgst, igst := splitTax(tax, supply.Kind)
		if taxable+cgst+sgst+igst != total {
			return nil, fmt.Errorf("line %s: taxable %d and tax %d do not make %d", l.SKU, taxable, tax, total)
		}
		hsn := ""
		if l.VariantID != nil {
			hsn = in.HSN[*l.VariantID]
		}
		if hsn == "" {
			doc.Warnings = append(doc.Warnings, Warning{Code: "hsn_missing", Ref: l.SKU})
		}
		desc := l.Title
		if l.VariantLabel != "" {
			desc += " (" + l.VariantLabel + ")"
		}
		doc.Lines = append(doc.Lines, TaxLine{
			N: i + 1, Kind: LineGoods, SKU: l.SKU, Description: desc, HSN: hsn,
			Qty: l.Quantity, Unit: "NOS",
			GrossMinor: l.Total.AmountMinor, DiscountMinor: share, TaxableMinor: taxable,
			RateBP: l.Tax.RateBP, CGSTMinor: cgst, SGSTMinor: sgst, IGSTMinor: igst, TotalMinor: total,
		})
	}
	// Core never taxes shipping, and the invoice cannot show tax nobody was
	// charged; the warning keeps the gap visible until it can.
	if s := o.Shipping.AmountMinor; s > 0 {
		desc := "Delivery"
		if o.ShippingMethod != "" {
			desc += " (" + o.ShippingMethod + ")"
		}
		doc.Lines = append(doc.Lines, TaxLine{
			N: len(doc.Lines) + 1, Kind: LineFreight, Description: desc, Qty: 1, Unit: "NOS",
			GrossMinor: s, TaxableMinor: s, TotalMinor: s,
		})
		doc.Warnings = append(doc.Warnings, Warning{Code: "freight_untaxed"})
	}

	byRate := map[int]*RateRow{}
	for _, l := range doc.Lines {
		r := byRate[l.RateBP]
		if r == nil {
			r = &RateRow{RateBP: l.RateBP}
			byRate[l.RateBP] = r
		}
		r.TaxableMinor += l.TaxableMinor
		r.CGSTMinor += l.CGSTMinor
		r.SGSTMinor += l.SGSTMinor
		r.IGSTMinor += l.IGSTMinor

		t := &doc.Totals
		t.TaxableMinor += l.TaxableMinor
		t.CGSTMinor += l.CGSTMinor
		t.SGSTMinor += l.SGSTMinor
		t.IGSTMinor += l.IGSTMinor
		t.TotalMinor += l.TotalMinor
	}
	doc.Totals.TaxMinor = doc.Totals.CGSTMinor + doc.Totals.SGSTMinor + doc.Totals.IGSTMinor
	for _, r := range byRate {
		doc.ByRate = append(doc.ByRate, *r)
	}
	sort.Slice(doc.ByRate, func(i, j int) bool { return doc.ByRate[i].RateBP < doc.ByRate[j].RateBP })

	if doc.Totals.TotalMinor != o.Total.AmountMinor {
		return nil, fmt.Errorf("the lines come to %d but order %s is %d", doc.Totals.TotalMinor, o.Number, o.Total.AmountMinor)
	}
	return doc, nil
}

// splitTax divides a line's tax between the heads; the parts always sum to it.
func splitTax(tax int64, k SupplyKind) (cgst, sgst, igst int64) {
	if k == SupplyIntra {
		cgst = tax / 2
		return cgst, tax - cgst, 0
	}
	return 0, 0, tax
}

// supplyOf is the place of supply of goods: where they are delivered.
func supplyOf(a gocommerce.Address, supplier gocommerce.GSTState) (Supply, gocommerce.GSTState, error) {
	country := strings.TrimSpace(a.Country)
	if country == "" {
		// Nothing delivered, such as a download, and no address recorded: the
		// place of supply is the supplier's own location (IGST Act s.10(1)(ca)
		// and s.12(2)(b)), so it is a supply within the state.
		return Supply{Kind: SupplyIntra, PlaceOfSupplyCode: supplier.Code, PlaceOfSupply: supplier.Name,
			StateTaxName: stateTaxName(supplier)}, supplier, nil
	}
	if !strings.EqualFold(country, "IN") {
		// 96 is GST's code for a place of supply in another country.
		abroad := gocommerce.GSTState{Code: "96", Name: "Other Countries"}
		return Supply{Kind: SupplyExport, PlaceOfSupplyCode: abroad.Code, PlaceOfSupply: abroad.Name}, abroad, nil
	}
	place, ok := gocommerce.GSTStateOfAddress(a)
	if !ok {
		return Supply{}, place, fmt.Errorf("the delivery address is in India but GST knows no state %q", a.State)
	}
	s := Supply{Kind: SupplyInter, PlaceOfSupplyCode: place.Code, PlaceOfSupply: place.Name}
	if place.Code == supplier.Code {
		s.Kind, s.StateTaxName = SupplyIntra, stateTaxName(place)
	}
	return s, place, nil
}

// stateTaxName is the head that sits beside CGST: UTGST in a union territory
// without a legislature, SGST everywhere else.
func stateTaxName(s gocommerce.GSTState) string {
	if s.UT {
		return "UTGST"
	}
	return "SGST"
}

// recipientOf is the buyer: registered when the order carries a GSTIN under
// metadata.gst. The checkout guard checks it, but an order may predate the
// guard or the store's GST mode, so an unreadable one makes an unregistered
// buyer with a warning rather than a failed invoice.
func recipientOf(o *gocommerce.Order, place gocommerce.GSTState, warnings *[]Warning) Party {
	name := o.Name
	if name == "" {
		name = o.Address.Name
	}
	p := Party{
		LegalName: name, StateCode: place.Code, State: place.Name,
		Address: Address{Line1: o.Address.Line1, Line2: o.Address.Line2, City: o.Address.City,
			PostalCode: o.Address.PostalCode, Country: strings.ToUpper(o.Address.Country)},
	}
	raw, sent := o.Metadata["gst"]
	if !sent {
		return p
	}
	gstin, legal := gstMeta(raw)
	g, err := gocommerce.ParseGSTIN(gstin)
	if err != nil {
		*warnings = append(*warnings, Warning{Code: "buyer_gstin_ignored", Ref: err.Error()})
		return p
	}
	p.GSTIN, p.Registered = string(g), true
	if legal != "" {
		p.LegalName = legal
	}
	return p
}

// gstMeta reads metadata.gst: {"gstin": "...", "name": "..."}.
func gstMeta(raw any) (gstin, name string) {
	m, _ := raw.(map[string]any)
	gstin, _ = m["gstin"].(string)
	name, _ = m["name"].(string)
	return gstin, strings.TrimSpace(name)
}

// gstGuard is the buyer's boundary on a GST store: a GSTIN sent at checkout
// must be one, and an Indian delivery address must name a state GST knows,
// because the place of supply decides the tax heads.
//
// Most checkouts have nothing here to check, and those return before reading
// anything; the rest read the profile through the checkout's own transaction,
// so a guard never waits on a second pooled connection while the checkout
// holds its stock locks.
func (m *Module) gstGuard(ctx context.Context, a *gocommerce.CheckoutAttempt) error {
	raw, sentGSTIN := a.Input.Metadata["gst"]
	inIndia := strings.EqualFold(strings.TrimSpace(a.Input.Address.Country), "IN")
	if !sentGSTIN && !inIndia {
		return nil
	}
	var country, taxID string
	if err := a.Tx.QueryRowContext(ctx,
		`SELECT country, tax_id FROM store_profile WHERE id = 1`).Scan(&country, &taxID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	// The currency is not asked: an Indian GST-registered store that sells in
	// another currency gets no tax invoice, and checking its buyers' GSTINs
	// and states anyway costs it nothing.
	if _, err := gocommerce.ParseGSTIN(taxID); country != "IN" || err != nil {
		return nil
	}
	if sentGSTIN {
		gstin, name := gstMeta(raw)
		if _, err := gocommerce.ParseGSTIN(gstin); err != nil {
			return gocommerce.Validationf("metadata.gst.gstin: %s", err.Error())
		}
		if len([]rune(name)) > gocommerce.MaxProfileField {
			return gocommerce.Validationf("metadata.gst.name is at most %d characters", gocommerce.MaxProfileField)
		}
	}
	if inIndia {
		if _, ok := gocommerce.GSTStateOfAddress(a.Input.Address); !ok {
			return gocommerce.Validationf("choose a state for the delivery address: a GST invoice needs it")
		}
	}
	return nil
}
