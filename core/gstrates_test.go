package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The embedded schedule loads and answers by the longest heading that
// covers a code, keeping every other entry that could also apply.
func TestGSTRateSchedule(t *testing.T) {
	if n := len(gstSchedule()); n < 1200 {
		t.Fatalf("schedule has %d rows, want the whole of 9/2025 (~1,330)", n)
	}

	// Gold jewellery: one entry, plain, 3%.
	m := LookupGSTRate("7113.19.10")
	if len(m) != 1 || m[0].Prefix != "7113" || m[0].RateBP != 300 || m[0].Conditional ||
		!strings.Contains(m[0].Source, "Schedule IV, S. No. 10") {
		t.Fatalf("7113.19.10 = %+v; want 7113 at 3%% from Schedule IV, S. No. 10", m)
	}

	// Leather shoes: 6403 says 18%, but chapter 64 says 5% up to Rs 2,500 a
	// pair. The longer prefix comes first; the shorter one is not dropped.
	m = LookupGSTRate("64031990")
	if len(m) < 2 || m[0].Prefix != "6403" || m[0].RateBP != 1800 {
		t.Fatalf("64031990 = %+v; want 6403 at 18%% first", m)
	}
	if !hasGSTMatch(m, "64", 500, true) {
		t.Errorf("64031990 lost chapter 64's 5%% conditional entry: %+v", m)
	}

	// T-shirts: two entries on the same chapter, split by sale value, both
	// conditional.
	m = LookupGSTRate("6109 10 00")
	if !hasGSTMatch(m, "61", 500, true) || !hasGSTMatch(m, "61", 1800, true) {
		t.Errorf("6109 10 00 = %+v; want chapter 61 at 5%% and 18%%, both conditional", m)
	}

	// Imitation jewellery names an exclusion in its description.
	if m := LookupGSTRate("7117"); len(m) != 1 || !m[0].Conditional || m[0].RateBP != 300 {
		t.Errorf("7117 = %+v; want one conditional 3%% entry", m)
	}

	// A chapter code is shorter than every entry under it: those entries are
	// offered as narrower, never as the code's own rate.
	m = LookupGSTRate("71")
	if len(m) == 0 {
		t.Fatal("71 found nothing; want the chapter's entries offered as narrower")
	}
	for _, x := range m {
		if !x.Narrower || !strings.HasPrefix(x.Prefix, "71") {
			t.Fatalf("71 offered %+v; want only narrower 71xx entries", x)
		}
	}

	if m := LookupGSTRate("0000"); len(m) != 0 {
		t.Errorf("0000 = %+v; want nothing", m)
	}
	if m := LookupGSTRate("gold"); len(m) != 0 {
		t.Errorf("a word = %+v; want nothing", m)
	}
}

func hasGSTMatch(m []GSTRateMatch, prefix string, bp int, conditional bool) bool {
	for _, x := range m {
		if x.Prefix == prefix && x.RateBP == bp && x.Conditional == conditional {
			return true
		}
	}
	return false
}

// The lookup is read with the tax table's right, like the rules it fills.
func TestGSTRateRoute(t *testing.T) {
	app := newTestApp(t)
	owner := person(t, app, "owner@example.com", RoleOwner)

	rec := do(t, app, http.MethodGet, "/api/admin/gst-rates?hsn=7113", withAdmin)
	var body struct {
		Data struct {
			HSN     string         `json:"hsn"`
			Matches []GSTRateMatch `json:"matches"`
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("gst-rates: %d %s", rec.Code, rec.Body)
	}
	got := body.Data
	if got.HSN != "7113" || len(got.Matches) != 1 || got.Matches[0].RateBP != 300 || got.Matches[0].Source == "" {
		t.Errorf("gst-rates?hsn=7113 = %+v", got)
	}

	rec = do(t, app, http.MethodGet, "/api/admin/gst-rates?hsn=0000", withAdmin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"matches":[]`) {
		t.Errorf("unknown code: %d %s; want 200 and no matches", rec.Code, rec.Body)
	}
	if rec := do(t, app, http.MethodGet, "/api/admin/gst-rates?hsn=gold", withAdmin); rec.Code != http.StatusBadRequest {
		t.Errorf("hsn=gold: %d, want 400", rec.Code)
	}

	_, catalogOnly := newAgentKey(t, app, owner, "Catalogue robot", RightCatalogRead)
	_, taxReader := newAgentKey(t, app, owner, "Tax robot", RightTaxesRead)
	for _, path := range []string{"/api/admin/gst-rates?hsn=7113", "/api/admin/gst-rates/catalog"} {
		if rec := do(t, app, http.MethodGet, path, bearer(catalogOnly)); rec.Code != http.StatusForbidden {
			t.Errorf("%s without taxes.read: %d, want 403", path, rec.Code)
		}
		if rec := do(t, app, http.MethodGet, path, bearer(taxReader)); rec.Code != http.StatusOK {
			t.Errorf("%s with taxes.read: %d %s", path, rec.Code, rec.Body)
		}
		if rec := do(t, app, http.MethodGet, path); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s anonymous: %d, want 401", path, rec.Code)
		}
	}
}

// The catalogue sweep lists the categories that carry an HSN code and the
// products whose variants say something else, each with its candidates.
func TestGSTRateCatalogSweep(t *testing.T) {
	ctx := context.Background()
	app := newTestApp(t)

	jewellery, err := app.Categories().Create(ctx, CategoryInput{Title: "Jewellery", HSNCode: "7113"})
	if err != nil {
		t.Fatal(err)
	}
	// No code of its own: its products climb to Jewellery's, and so does a
	// rule on Jewellery, so it needs no row.
	rings, err := app.Categories().Create(ctx, CategoryInput{Title: "Rings", ParentID: &jewellery.ID})
	if err != nil {
		t.Fatal(err)
	}
	file := func(p *Product, c *Category) {
		t.Helper()
		if _, err := app.Products().UpdateProduct(ctx, p.ID, ProductPatch{
			CategoryID: NullableID{Present: true, Value: &c.ID}}); err != nil {
			t.Fatal(err)
		}
	}
	hs := func(p *Product, code string) {
		t.Helper()
		if _, err := app.Products().UpdateVariant(ctx, p.DefaultVariant().ID, VariantPatch{HSCode: &code}); err != nil {
			t.Fatal(err)
		}
	}
	ring := simpleProduct(t, app, "RING", 10000, 1)
	file(ring, rings)
	hs(ring, "7113") // the same as its category's: no row of its own
	bangle := simpleProduct(t, app, "BANGLE", 500, 1)
	file(bangle, rings)
	hs(bangle, "7117") // imitation, filed under Jewellery: a row
	plain := simpleProduct(t, app, "PLAIN", 500, 1)
	file(plain, rings) // no code: covered by the category

	rec := do(t, app, http.MethodGet, "/api/admin/gst-rates/catalog", withAdmin)
	var got struct{ Data []GSTRateTarget }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("catalog: %d %s", rec.Code, rec.Body)
	}
	if len(got.Data) != 2 {
		t.Fatalf("catalog = %+v; want Jewellery and the bangle", got.Data)
	}
	c, p := got.Data[0], got.Data[1]
	if c.Kind != "category" || c.ID != jewellery.ID || c.Name != "Jewellery" ||
		len(c.Codes) != 1 || c.Codes[0] != "7113" || len(c.Matches) != 1 || c.Matches[0].RateBP != 300 {
		t.Errorf("category row = %+v", c)
	}
	if p.Kind != "product" || p.ID != bangle.ID || len(p.Codes) != 1 || p.Codes[0] != "7117" ||
		len(p.Matches) != 1 || !p.Matches[0].Conditional {
		t.Errorf("product row = %+v", p)
	}
}
