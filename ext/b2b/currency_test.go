package b2b

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/itswadesh/gocommerce/gctest"
)

// A store sells in its own currency (D85), and every amount this module
// reports for it is labelled with that one. The install's currency is only
// the default for a store that never chose: a credit limit labelled with it
// in a store selling in rupees reads as dollars.

// sellIn moves the fixture's store onto code, which it may do until its first
// order. The fixture has priced its product by then and taken no order.
func (f *fixture) sellIn(t *testing.T, code string) {
	t.Helper()
	if f.app.Config().Currency == code {
		t.Fatalf("the install already sells in %s, so a surface reading the install's currency would pass", code)
	}
	ctx := context.Background()
	p, err := f.app.Profile().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.Currency = code
	if _, err := f.app.Profile().Set(ctx, *p, nil); err != nil {
		t.Fatalf("sell in %s: %v", code, err)
	}
}

func TestACompanysTermsAndCreditAreInTheStoresCurrency(t *testing.T) {
	f := newFixture(t)
	f.sellIn(t, "INR")
	_, tok := f.member(t, "boss@acme.test", RoleAdmin)
	f.placeOnAccount(t, tok, 1, "PO-INR")

	rec := gctest.AdminRequest(t, f.app, http.MethodGet, f.companyPath(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("company = %d: %s", rec.Code, rec.Body)
	}
	var c Company
	gctest.DecodeData(t, rec, &c)
	if c.CreditLimit == nil || c.CreditLimit.Currency != "INR" ||
		c.ApprovalThreshold == nil || c.ApprovalThreshold.Currency != "INR" {
		t.Errorf("limit %+v, threshold %+v; want both in INR", c.CreditLimit, c.ApprovalThreshold)
	}

	rec = gctest.AdminRequest(t, f.app, http.MethodGet, f.companyPath()+"/credit", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("credit = %d: %s", rec.Code, rec.Body)
	}
	var cr Credit
	gctest.DecodeData(t, rec, &cr)
	if cr.Outstanding.AmountMinor != 8000 || cr.Outstanding.Currency != "INR" ||
		cr.Overdue.Currency != "INR" || cr.Available == nil || cr.Available.Currency != "INR" {
		t.Errorf("credit = outstanding %+v, overdue %+v, available %+v; want 8000 owed, all in INR",
			cr.Outstanding, cr.Overdue, cr.Available)
	}
}

func TestATermsHistoryIsInTheStoresCurrency(t *testing.T) {
	f := newFixture(t)
	f.sellIn(t, "INR")

	list, _, err := f.b2b.History(context.Background(), f.company.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, e := range list {
		if e.Field == "credit_limit_minor" {
			seen = true
			if e.Currency != "INR" {
				t.Errorf("the credit limit's history entry is in %q, want INR", e.Currency)
			}
		}
	}
	if !seen {
		t.Fatalf("no credit limit entry in %+v", list)
	}
}

func TestAQuoteIsRequestedInTheStoresCurrency(t *testing.T) {
	f := newFixture(t)
	f.sellIn(t, "INR")
	_, tok := f.member(t, "boss@acme.test", RoleAdmin)

	rec := gctest.SessionRequest(t, f.app, tok, http.MethodPost, "/x/b2b/quotes", map[string]any{
		"lines": []map[string]any{{"variant_id": f.variant, "quantity": 2}},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("request quote = %d: %s", rec.Code, rec.Body)
	}
	var q Quote
	gctest.DecodeData(t, rec, &q)
	priced := gctest.AdminRequest(t, f.app, http.MethodPut, "/api/admin/x/b2b/quotes/"+strconv.FormatInt(q.ID, 10),
		map[string]any{"lines": []map[string]any{{"variant_id": f.variant, "quantity": 2, "unit_price_minor": 7000}}})
	if priced.Code != http.StatusOK {
		t.Fatalf("price = %d: %s", priced.Code, priced.Body)
	}
	gctest.DecodeData(t, priced, &q)
	if q.Total == nil || q.Total.Currency != "INR" || q.Lines[0].UnitPrice == nil || q.Lines[0].UnitPrice.Currency != "INR" {
		t.Errorf("quote total %+v, line %+v; want INR", q.Total, q.Lines[0].UnitPrice)
	}
}

func TestAStatementIsInTheStoresCurrency(t *testing.T) {
	f := newFixture(t)
	f.sellIn(t, "INR")
	_, tok := f.member(t, "boss@acme.test", RoleAdmin)
	f.placeOnAccount(t, tok, 1, "PO-INR")

	if st := f.statement(t, ""); st.Currency != "INR" {
		t.Errorf("statement currency = %q, want INR", st.Currency)
	}
}

func TestTheCatalogueIsPricedInTheStoresCurrency(t *testing.T) {
	f := newFixture(t)
	f.sellIn(t, "INR")
	_, tok := f.member(t, "boss@acme.test", RoleBuyer)

	rec := gctest.SessionRequest(t, f.app, tok, http.MethodGet, "/x/b2b/catalogue", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("catalogue = %d: %s", rec.Code, rec.Body)
	}
	var products []CatalogueProduct
	gctest.DecodeData(t, rec, &products)
	if len(products) == 0 || len(products[0].Variants) == 0 {
		t.Fatalf("catalogue = %s", rec.Body)
	}
	if v := products[0].Variants[0]; v.PriceMinor != 8000 || v.Currency != "INR" {
		t.Errorf("catalogue variant = %d %s; want the group's 8000, in INR", v.PriceMinor, v.Currency)
	}
}
