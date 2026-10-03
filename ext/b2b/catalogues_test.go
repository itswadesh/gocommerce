package b2b

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// shelves is a small tree and a product on each branch:
//
//	Tools            HAMMER
//	  Power tools    DRILL
//	Paint            PAINT
//	(none)           B2B-WIDGET, the fixture's
type shelves struct {
	tools, power, paint    int64
	hammer, drill, painted int64 // variant ids
}

func (f *fixture) shelves(t *testing.T) shelves {
	t.Helper()
	ctx := context.Background()
	category := func(title string, parent *int64) int64 {
		c, err := f.app.Categories().Create(ctx, gocommerce.CategoryInput{Title: title, ParentID: parent})
		if err != nil {
			t.Fatalf("category %s: %v", title, err)
		}
		return c.ID
	}
	var s shelves
	s.tools = category("Tools", nil)
	s.power = category("Power tools", &s.tools)
	s.paint = category("Paint", nil)
	product := func(sku string, price int64, cat int64) int64 {
		p := gctest.CreateProduct(t, f.app, sku, price, 100)
		if _, err := f.app.Products().UpdateProduct(ctx, p.ID, gocommerce.ProductPatch{CategoryID: gocommerce.SetID(cat)}); err != nil {
			t.Fatalf("file %s: %v", sku, err)
		}
		return p.Variants[0].ID
	}
	s.hammer = product("HAMMER", 2000, s.tools)
	s.drill = product("DRILL", 9000, s.power)
	s.painted = product("PAINT", 1500, s.paint)
	return s
}

// holdTo gives the fixture's company a catalogue of these categories and the
// widget, through the routes the panel uses.
func (f *fixture) holdTo(t *testing.T, categories ...int64) *Catalogue {
	t.Helper()
	widget, err := f.app.Products().GetVariant(context.Background(), f.variant)
	if err != nil {
		t.Fatalf("widget: %v", err)
	}
	rec := gctest.AdminRequest(t, f.app, http.MethodPost, "/api/admin/x/b2b/catalogues", map[string]any{
		"name": "Trade tools", "category_ids": categories, "product_ids": []int64{widget.ProductID},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create catalogue = %d: %s", rec.Code, rec.Body)
	}
	var c Catalogue
	gctest.DecodeData(t, rec, &c)
	if rec := gctest.AdminRequest(t, f.app, http.MethodPatch, f.companyPath(),
		map[string]any{"catalogue_id": c.ID}); rec.Code != http.StatusOK {
		t.Fatalf("hold the company to it = %d: %s", rec.Code, rec.Body)
	}
	return &c
}

// A catalogue is a control, not a hint: what it leaves out does not go into a
// basket, is refused at checkout, cannot be quoted, and an approval of it
// granted after the catalogue narrowed is refused when it is placed.
func TestACatalogueHoldsACompanyToWhatItMayBuy(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.shelves(t)
	boss, bossTok := f.member(t, "boss@acme.test", RoleAdmin)
	junior, _ := f.member(t, "junior@acme.test", RoleBuyer)

	// An order of paint placed before there was a catalogue, to repeat later.
	before := f.checkout(t, bossTok, f.basket(t, item{s.painted, 2}, item{s.hammer, 1}), nil)
	if before.code != http.StatusCreated {
		t.Fatalf("checkout before = %d: %s", before.code, before.body)
	}
	var earlier struct {
		Order gocommerce.Order `json:"order"`
	}
	decodeBody(t, before.body, &earlier)

	if rec := gctest.AdminRequest(t, f.app, http.MethodPost, "/api/admin/x/b2b/catalogues",
		map[string]any{"name": "Nothing", "category_ids": []int64{999999}}); rec.Code != http.StatusBadRequest {
		t.Errorf("a catalogue of a category nobody has = %d, want 400", rec.Code)
	}
	cat := f.holdTo(t, s.tools)
	if len(cat.Categories) != 1 || cat.Categories[0].Name != "Tools" || len(cat.Products) != 1 ||
		cat.Products[0].Name == "" || cat.CompanyCount != 0 {
		t.Errorf("catalogue = %+v; want Tools and the widget, by name", cat)
	}
	list, _, err := f.b2b.History(ctx, f.company.ID, 1, 0)
	if err != nil || len(list) != 1 || list[0].Field != "catalogue_id" || string(list[0].OldValue) != "null" ||
		string(list[0].NewValue) != strconv.FormatInt(cat.ID, 10) {
		t.Errorf("history = %+v (%v); want the catalogue recorded as a term", list, err)
	}

	// Quick order: what it leaves out is reported, the rest goes in.
	rec := gctest.SessionRequest(t, f.app, bossTok, http.MethodPost, "/x/b2b/cart/lines", map[string]any{
		"lines": []map[string]any{{"sku": "DRILL", "quantity": 1}, {"sku": "PAINT", "quantity": 3}, {"sku": "B2B-WIDGET", "quantity": 1}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("quick order = %d: %s", rec.Code, rec.Body)
	}
	var fill CartFill
	gctest.DecodeData(t, rec, &fill)
	if len(fill.Cart.Lines) != 2 || len(fill.Rejected) != 1 || fill.Rejected[0].SKU != "PAINT" ||
		fill.Rejected[0].Reason != RejectNotInCatalogue {
		t.Errorf("quick order = %d lines, rejected %+v; want the drill and the widget in, the paint out", len(fill.Cart.Lines), fill.Rejected)
	}

	// Repeating the paint order leaves the paint out the same way.
	rec = gctest.SessionRequest(t, f.app, bossTok, http.MethodPost,
		"/x/b2b/orders/"+strconv.FormatInt(earlier.Order.ID, 10)+"/reorder", nil)
	gctest.DecodeData(t, rec, &fill)
	if rec.Code != http.StatusCreated || len(fill.Rejected) != 1 || fill.Rejected[0].Reason != RejectNotInCatalogue ||
		len(fill.Cart.Lines) != 1 {
		t.Errorf("reorder = %d %+v; want the hammer in and the paint not in the catalogue", rec.Code, fill.Rejected)
	}

	// A basket filled some other way is refused at checkout, naming the line.
	orders := f.orderCount(t)
	refused := f.checkout(t, bossTok, f.basket(t, item{s.drill, 1}, item{s.painted, 1}), nil)
	if refused.code != http.StatusForbidden || errCode(refused.body) != RejectNotInCatalogue ||
		!strings.Contains(refused.body, `"sku":"PAINT"`) || strings.Contains(refused.body, `"sku":"DRILL"`) {
		t.Errorf("checkout with paint = %d %s; want 403 not_in_catalogue naming only the paint", refused.code, refused.body)
	}
	if n := f.orderCount(t); n != orders {
		t.Errorf("orders = %d after a refused checkout, want %d", n, orders)
	}
	if ok := f.checkout(t, bossTok, f.basket(t, item{s.drill, 1}), nil); ok.code != http.StatusCreated {
		t.Errorf("checkout of the drill = %d %s, want 201", ok.code, ok.body)
	}

	// A quote cannot ask for it.
	if _, err := f.b2b.RequestQuote(ctx, boss, QuoteRequest{Lines: []QuoteLineInput{{VariantID: s.painted, Quantity: 10}}}); !isCode(err, RejectNotInCatalogue) {
		t.Errorf("quoting paint = %v, want not_in_catalogue", err)
	}

	// An approval asked for while the drill was in the catalogue, approved
	// after it was taken out, is refused when it is placed.
	_, approval, err := f.b2b.Checkout(ctx, junior, CheckoutRequest{CartID: f.basket(t, item{s.drill, 4}), Address: address}, "")
	if err != nil || approval == nil {
		t.Fatalf("junior's drills = approval %v, err %v; want an approval (36000 > 30000)", approval, err)
	}
	if rec := gctest.AdminRequest(t, f.app, http.MethodPatch, "/api/admin/x/b2b/catalogues/"+strconv.FormatInt(cat.ID, 10),
		map[string]any{"category_ids": []int64{}}); rec.Code != http.StatusOK {
		t.Fatalf("narrow the catalogue = %d: %s", rec.Code, rec.Body)
	}
	if _, _, err := f.b2b.Approve(ctx, boss, approval.ID); !isCode(err, RejectNotInCatalogue) {
		t.Errorf("approving drills no longer in the catalogue = %v, want not_in_catalogue", err)
	}
	if a, _ := f.b2b.Approval(ctx, approval.ID); a.Status != ApprovalPending || a.LastError == "" {
		t.Errorf("approval = %+v; want it back with the approver, saying why", a)
	}

	// The category tree is read as it stands: paint moved under Tools is
	// in, once Tools is.
	if _, err := f.app.Categories().Update(ctx, s.paint, gocommerce.CategoryPatch{ParentID: gocommerce.SetID(s.tools)}); err != nil {
		t.Fatalf("move paint: %v", err)
	}
	f.setCatalogueCategories(t, cat.ID, s.tools)
	if ok := f.checkout(t, bossTok, f.basket(t, item{s.painted, 1}), nil); ok.code != http.StatusCreated {
		t.Errorf("paint filed under Tools = %d %s, want 201", ok.code, ok.body)
	}

	// A catalogue a company is held to is not deleted from under it.
	path := "/api/admin/x/b2b/catalogues/" + strconv.FormatInt(cat.ID, 10)
	if rec := gctest.AdminRequest(t, f.app, http.MethodDelete, path, nil); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), f.company.Name) {
		t.Errorf("deleting a catalogue in use = %d %s, want 409 naming the company", rec.Code, rec.Body)
	}
	if rec := gctest.AdminRequest(t, f.app, http.MethodPatch, f.companyPath(), map[string]any{"catalogue_id": nil}); rec.Code != http.StatusOK {
		t.Fatalf("free the company = %d: %s", rec.Code, rec.Body)
	}
	if rec := gctest.AdminRequest(t, f.app, http.MethodDelete, path, nil); rec.Code != http.StatusNoContent {
		t.Errorf("deleting an unused catalogue = %d %s, want 204", rec.Code, rec.Body)
	}
}

func (f *fixture) setCatalogueCategories(t *testing.T, catalogueID int64, categories ...int64) {
	t.Helper()
	if rec := gctest.AdminRequest(t, f.app, http.MethodPatch, "/api/admin/x/b2b/catalogues/"+strconv.FormatInt(catalogueID, 10),
		map[string]any{"category_ids": categories}); rec.Code != http.StatusOK {
		t.Fatalf("set the catalogue's categories = %d: %s", rec.Code, rec.Body)
	}
}

func isCode(err error, code string) bool {
	var apiErr *gocommerce.APIError
	return errors.As(err, &apiErr) && apiErr.Code == code
}

func decodeMeta(t *testing.T, body string, meta *gocommerce.ListMeta) {
	t.Helper()
	var env struct {
		Meta *gocommerce.ListMeta `json:"meta"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil || env.Meta == nil {
		t.Fatalf("decode meta %s: %v", body, err)
	}
	*meta = *env.Meta
}

// A buyer reads what their company may buy at the price their basket would
// charge for one: the group's price where it has one, the shelf price where
// it does not. A company with no catalogue sees everything on sale.
func TestABuyerSeesTheirCatalogueAtTheirPrices(t *testing.T) {
	f := newFixture(t)
	s := f.shelves(t)
	_, tok := f.member(t, "boss@acme.test", RoleBuyer)

	read := func(query string) ([]CatalogueProduct, gocommerce.ListMeta) {
		t.Helper()
		rec := gctest.SessionRequest(t, f.app, tok, http.MethodGet, "/x/b2b/catalogue"+query, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("catalogue%s = %d: %s", query, rec.Code, rec.Body)
		}
		var env struct {
			Data []CatalogueProduct  `json:"data"`
			Meta gocommerce.ListMeta `json:"meta"`
		}
		decodeBody(t, rec.Body.String(), &env.Data)
		decodeMeta(t, rec.Body.String(), &env.Meta)
		return env.Data, env.Meta
	}
	titles := func(ps []CatalogueProduct) string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Variants[0].SKU)
		}
		return strings.Join(out, ",")
	}

	if all, _ := read(""); len(all) != 4 {
		t.Errorf("with no catalogue = %s, want every product on sale", titles(all))
	}
	f.holdTo(t, s.tools)
	held, meta := read("")
	if got := titles(held); got != "B2B-WIDGET,DRILL,HAMMER" || meta.Total != 3 {
		t.Errorf("held to Tools and the widget = %s (total %d), want B2B-WIDGET,DRILL,HAMMER", got, meta.Total)
	}
	for _, p := range held {
		v := p.Variants[0]
		want := map[string]int64{"B2B-WIDGET": 8000, "DRILL": 9000, "HAMMER": 2000}[v.SKU]
		if v.PriceMinor != want || v.Currency != "USD" {
			t.Errorf("%s at %d %s, want %d USD", v.SKU, v.PriceMinor, v.Currency, want)
		}
	}
	if got, _ := read("?q=dri"); titles(got) != "DRILL" {
		t.Errorf("searching dri = %s, want DRILL", titles(got))
	}
	if got, _ := read("?category_id=" + strconv.FormatInt(s.power, 10)); titles(got) != "DRILL" {
		t.Errorf("Power tools = %s, want DRILL", titles(got))
	}
	if got, m := read("?per_page=1&page=2"); titles(got) != "DRILL" || m.Total != 3 {
		t.Errorf("page 2 of 1 = %s (total %d), want DRILL of 3", titles(got), m.Total)
	}
	if rec := gctest.SessionRequest(t, f.app, tok, http.MethodGet, "/x/b2b/catalogue?category_id=x", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("category_id=x = %d, want 400", rec.Code)
	}
}
