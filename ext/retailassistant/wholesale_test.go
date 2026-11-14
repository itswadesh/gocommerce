package retailassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gocommerce "github.com/misiki-in/gocommerce/core"
	"github.com/misiki-in/gocommerce/ext/b2b"
	"github.com/misiki-in/gocommerce/ext/identity"
	"github.com/misiki-in/gocommerce/gctest"
)

func TestMarkupAndMarginAreDifferentPrices(t *testing.T) {
	for _, c := range []struct {
		cost int64
		mode string
		bps  int
		want int64
	}{
		{10000, pricingMarkup, 2000, 12000},
		{10000, pricingMargin, 2000, 12500},
		{10000, pricingMarkup, 0, 10000},
		{10000, pricingMargin, 0, 10000},
		{3333, pricingMarkup, 1000, 3666}, // 3666.3 rounds down
		{3335, pricingMarkup, 1000, 3669}, // 3668.5 rounds up
		{10000, pricingMargin, 3333, 14999},
	} {
		got, err := priceFromCost(c.cost, c.mode, c.bps)
		if err != nil || got != c.want {
			t.Errorf("%d at %d bps %s = %d, %v; want %d", c.cost, c.bps, c.mode, got, err, c.want)
		}
	}
	if _, err := priceFromCost(100, pricingMargin, 10000); err == nil {
		t.Error("a 100% margin has no price, and one was given")
	}
	if _, err := priceFromCost(100, "profit", 100); err == nil {
		t.Error("an unknown rule was accepted")
	}
	if _, err := priceFromCost(100, pricingMarkup, -1); err == nil {
		t.Error("a negative percentage was accepted")
	}
}

type wholesaleFixture struct {
	*harness
	accounts *identity.Module
	b2b      *b2b.Module
	company  int64
	buyer    *identity.Customer
	variant  int64
	doc      Document
}

func setupWholesale(t *testing.T) *wholesaleFixture {
	t.Helper()
	accounts := identity.New(identity.Config{})
	trade := b2b.New(b2b.Config{Accounts: accounts})
	h := setupWith(t, func(c *Config) { c.B2B = trade }, accounts, trade)
	f := &wholesaleFixture{harness: h, accounts: accounts, b2b: trade}
	rec := gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/b2b/companies", map[string]any{"name": "Shop A", "credit_limit_minor": 100000000, "net_days": 30})
	if rec.Code != http.StatusCreated {
		t.Fatalf("company = %d %s", rec.Code, rec.Body)
	}
	var company b2b.Company
	gctest.DecodeData(t, rec, &company)
	f.company = company.ID
	ctx := context.Background()
	acct, _, err := accounts.Signup(ctx, "shop@example.com", "correct horse battery", "Shop", "")
	if err != nil {
		t.Fatal(err)
	}
	if acct, err = accounts.MarkEmailVerified(ctx, acct.ID, "shop@example.com"); err != nil {
		t.Fatal(err)
	}
	f.buyer = acct
	rec = gctest.AdminRequest(t, h.app, http.MethodPost, fmt.Sprintf("/api/admin/x/b2b/companies/%d/members", f.company), map[string]any{"email": "shop@example.com", "role": b2b.RoleAdmin})
	if rec.Code != http.StatusCreated {
		t.Fatalf("member = %d %s", rec.Code, rec.Body)
	}
	f.variant = gctest.CreateProduct(t, h.app, "LIP-01", 50000, 100).DefaultVariant().ID
	h.link("", senderA)
	h.deliver(h.file("msg-w", senderA, "media-w", "maker.csv", []byte(`Supplier,Maker Ltd
Invoice No,M-1
Invoice Date,05/11/2026

Description,Qty,Rate,Amount
Lip colour shade 01,10,100.00,1000.00

Subtotal,1000.00
Tax,0.00
Grand Total,1000.00
`)))
	h.work("")
	f.doc = h.documents("")[0]
	return f
}

func (f *wholesaleFixture) body(mode string, bps int) map[string]any {
	return map[string]any{
		"document_id": f.doc.ID, "company_id": f.company, "buyer_id": f.buyer.ID, "mode": mode, "percent_bps": bps,
		"lines": []map[string]any{{"index": 0, "variant_id": f.variant, "quantity": 5}},
	}
}

func TestAManufacturerBillBecomesARetailersQuoteWithoutItsCost(t *testing.T) {
	f := setupWholesale(t)
	var preview struct {
		Lines []PricedLine `json:"lines"`
	}
	f.admin(http.MethodPost, path("/wholesale/preview", ""), f.body(pricingMargin, 2000), &preview, http.StatusOK)
	if p := preview.Lines[0]; p.PriceMinor != 12500 || *p.CostMinor != 10000 || *p.MarginBps != 2000 {
		t.Fatalf("preview = %+v", p)
	}
	f.admin(http.MethodPost, path("/wholesale/preview", ""), f.body(pricingMarkup, 2000), &preview, http.StatusOK)
	if p := preview.Lines[0]; p.PriceMinor != 12000 {
		t.Fatalf("markup preview = %+v", p)
	}

	rec := gctest.AdminRequest(t, f.app, http.MethodPost, path("/wholesale/quotes", ""), f.body(pricingMargin, 2000))
	if rec.Code != http.StatusCreated {
		t.Fatalf("quote = %d %s", rec.Code, rec.Body)
	}
	var q b2b.Quote
	gctest.DecodeData(t, rec, &q)
	if q.Status != b2b.QuoteRequested {
		t.Fatalf("a quote sent itself: %s", q.Status)
	}
	if q.Lines[0].UnitPrice == nil || q.Lines[0].UnitPrice.AmountMinor != 12500 {
		t.Fatalf("line = %+v", q.Lines[0])
	}
	seen, _ := json.Marshal(q)
	for _, leak := range []string{"10000", "cost", "margin", "Maker"} {
		if strings.Contains(string(seen), leak) {
			t.Fatalf("the retailer's quote shows %q: %s", leak, seen)
		}
	}
	if n := f.count("", "retailassistant_purchase_invoices"); n != 0 {
		t.Fatalf("quoting posted %d bills", n)
	}

	// The rest of the way is ext/b2b's: send, accept, order.
	ctx := f.ctxOf("")
	if _, err := f.b2b.SendQuote(ctx, q.ID); err != nil {
		t.Fatalf("send: %v", err)
	}
	res, _, err := f.b2b.AcceptQuote(ctx, f.buyer, q.ID, b2b.QuoteAcceptance{Address: gocommerce.Address{
		Line1: "1 Market Road", City: "Koraput", State: "OD", PostalCode: "764020", Country: "IN",
	}})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if res == nil {
		t.Fatal("accepting made no order")
	}
}

func TestAPriceBelowCostNeedsAPersonToSaySo(t *testing.T) {
	f := setupWholesale(t)
	body := f.body(pricingMarkup, 0)
	body["lines"].([]map[string]any)[0]["price_minor"] = 9000
	f.admin(http.MethodPost, path("/wholesale/quotes", ""), body, nil, http.StatusConflict)
	body["allow_below_cost"] = true
	f.admin(http.MethodPost, path("/wholesale/quotes", ""), body, nil, http.StatusCreated)
	var below, overridden bool
	if err := f.app.DB().QueryRowContext(f.ctxOf(""), `SELECT below_cost, overridden FROM retailassistant_wholesale_quotes`).Scan(&below, &overridden); err != nil {
		t.Fatal(err)
	}
	if !below || !overridden {
		t.Fatalf("the override was not recorded: below %v, overridden %v", below, overridden)
	}
}

func TestACostInTheRequestIsIgnored(t *testing.T) {
	f := setupWholesale(t)
	body := f.body(pricingMarkup, 2000)
	body["lines"].([]map[string]any)[0]["cost_minor"] = 1
	var preview struct {
		Lines []PricedLine `json:"lines"`
	}
	rec := gctest.AdminRequest(t, f.app, http.MethodPost, path("/wholesale/preview", ""), body)
	if rec.Code == http.StatusOK {
		gctest.DecodeData(t, rec, &preview)
		if preview.Lines[0].PriceMinor != 12000 {
			t.Fatalf("priced from the request's cost: %+v", preview.Lines[0])
		}
	}
}

type httpResult struct {
	code int
	id   int64
}

func (f *wholesaleFixture) keyed(target, key string, body any) *httpResult {
	f.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		f.t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+gctest.AdminToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(rec, req)
	var q b2b.Quote
	if rec.Code < 300 {
		gctest.DecodeData(f.t, rec, &q)
	}
	return &httpResult{rec.Code, q.ID}
}

func TestAPressRepeatedMakesOneQuote(t *testing.T) {
	f := setupWholesale(t)
	key := func() *httpResult {
		return f.keyed(path("/wholesale/quotes", ""), "press-1", f.body(pricingMarkup, 2000))
	}
	first, second := key(), key()
	if first.code != http.StatusCreated || second.code != http.StatusOK || first.id != second.id {
		t.Fatalf("first %+v, second %+v", first, second)
	}
	var n int
	if err := f.app.DB().QueryRowContext(f.ctxOf(""), `SELECT count(*) FROM b2b_quotes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d quotes", n)
	}
}

func TestABuyerFromAnotherCompanyIsRefusedAndKeepsNoClaim(t *testing.T) {
	f := setupWholesale(t)
	body := f.body(pricingMarkup, 2000)
	body["buyer_id"] = int64(999999)
	f.admin(http.MethodPost, path("/wholesale/quotes", ""), body, nil, http.StatusBadRequest)
	if n := f.count("", "retailassistant_wholesale_quotes"); n != 0 {
		t.Fatalf("%d rows left claiming a quote that was never made", n)
	}
}

func TestQuotingNeedsTheQuotesRightAndTheModule(t *testing.T) {
	f := setupWholesale(t)
	su := &gocommerce.Superuser{Rights: []gocommerce.Right{rightRead, rightWrite}}
	req := httptest.NewRequest(http.MethodPost, path("/wholesale/preview", ""), nil)
	req = req.WithContext(gocommerce.WithSuperuser(req.Context(), su))
	rec := httptest.NewRecorder()
	if f.mod.wholesaleReady(rec, req) || rec.Code != http.StatusForbidden {
		t.Fatalf("without quotes.write: code %d", rec.Code)
	}
	plain := setup(t)
	plain.admin(http.MethodPost, path("/wholesale/preview", ""), map[string]any{}, nil, http.StatusNotFound)
}
