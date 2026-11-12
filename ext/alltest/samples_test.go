package alltest

import (
	"context"
	"net/http"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// The sample shop with every module mounted (D96): the modules add their own
// samples beside the engine's — reviews, pages, questions, menu links — link
// them to each other, and erase them with the rest, leaving the merchant's.
func TestTheSampleShopReachesEveryModuleAndErasesCleanly(t *testing.T) {
	app := gctest.New(t, everyModule()...)
	ctx := context.Background()

	// The merchant already wrote a returns page and a question of their own.
	if rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/cms/pages", map[string]any{
		"slug": "returns", "title": "Our returns", "body": "Ours.", "status": "published",
	}); rec.Code != http.StatusCreated {
		t.Fatalf("their page = %d %s", rec.Code, rec.Body)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/faq", map[string]any{
		"question": "Do you gift wrap?", "answer": "Yes.",
	}); rec.Code != http.StatusCreated {
		t.Fatalf("their question = %d %s", rec.Code, rec.Body)
	}

	added, err := app.AddSampleShop(ctx)
	if err != nil {
		t.Fatalf("add the sample shop: %v", err)
	}
	// Their returns page stays theirs, so two sample pages, and the footer
	// links only those: ten categories in the header and two pages below.
	// No banners (D257): the starting home page opens with its own picture.
	want := gocommerce.SampleCounts{"products": 24, "orders": 12, "reviews": 11, "pages": 2, "faq": 5, "menu_links": 12, "banners": 0}
	for kind, n := range want {
		if added[kind] != n {
			t.Errorf("added %d %s, want %d (all: %v)", added[kind], kind, n, added)
		}
	}

	// Every sample review is by somebody whose sample order bought the
	// product, so the engine's own test marks it verified.
	var verified int
	if err := app.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM reviews WHERE sample AND verified AND status = 'approved'`).Scan(&verified); err != nil || verified != 11 {
		t.Fatalf("verified, approved sample reviews = %d (%v), want 11", verified, err)
	}

	// What a storefront reads. The header nests the categories as they are:
	// Women and Men, each with its own under it.
	var header struct {
		Items []struct {
			URL      string `json:"url"`
			Children []struct {
				URL string `json:"url"`
			} `json:"children"`
		} `json:"items"`
	}
	gctest.DecodeData(t, gctest.Request(t, app, http.MethodGet, "/x/navigation/menus/header", nil), &header)
	var tree []string
	for _, it := range header.Items {
		var under []string
		for _, c := range it.Children {
			under = append(under, c.URL)
		}
		tree = append(tree, it.URL+" ["+strings.Join(under, " ")+"]")
	}
	if got, want := strings.Join(tree, "\n"), "/categories/women [/categories/women-dresses /categories/women-jumpsuits /categories/women-ethnic-wear /categories/women-co-ord-sets /categories/women-tops]\n"+
		"/categories/men [/categories/men-kurtas /categories/men-jackets /categories/men-shirts]"; !strings.HasSuffix(got, want) {
		t.Fatalf("header menu =\n%s\nwant it to end with\n%s", got, want)
	}
	rec := gctest.Request(t, app, http.MethodGet, "/x/navigation/menus/footer", nil)
	if !strings.Contains(rec.Body.String(), `"/pages/about"`) || strings.Contains(rec.Body.String(), `"/pages/returns"`) {
		t.Fatalf("footer menu = %s, want the sample about page and not the merchant's returns page", rec.Body)
	}
	rec = gctest.Request(t, app, http.MethodGet, "/x/banners?page=home", nil)
	if body := rec.Body.String(); strings.Contains(body, "/sample-media/") {
		t.Fatalf("home banners = %s, want none from the sample shop", body)
	}
	// The placeholders stay served for the stores whose older sample banners
	// link to them.
	if got := gctest.Request(t, app, http.MethodGet, "/sample-media/3/banner-1-phone.webp", nil); got.Code != http.StatusOK {
		t.Fatalf("an older sample banner's picture = %d, want it still served", got.Code)
	}
	// The catalogue's first product is the newest, so "New in" opens on it.
	products, _, err := app.Products().ListProducts(ctx, gocommerce.ProductQuery{Limit: 1})
	if err != nil || len(products) != 1 || products[0].Slug != "printed-anarkali-kurta" {
		t.Fatalf("newest product = %v (%v), want the catalogue's first, printed-anarkali-kurta", products, err)
	}
	if rec := gctest.Request(t, app, http.MethodGet, "/x/cms/pages/about", nil); rec.Code != http.StatusOK {
		t.Fatalf("the about page = %d %s, want it published", rec.Code, rec.Body)
	}
	if rec := gctest.Request(t, app, http.MethodGet, "/x/faq", nil); !strings.Contains(rec.Body.String(), "How long does delivery take?") {
		t.Fatalf("faq = %s, want the sample questions", rec.Body)
	}

	// Nothing went anywhere: no notification for any sample order.
	var notified int
	if err := app.DB().QueryRowContext(ctx, `SELECT count(*) FROM notifications`).Scan(&notified); err != nil || notified != 0 {
		t.Fatalf("notifications = %d (%v), want none", notified, err)
	}

	again, err := app.AddSampleShop(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for kind, n := range again {
		if n != 0 {
			t.Errorf("a second add made %d %s, want none", n, kind)
		}
	}

	erased, err := app.EraseSampleShop(ctx)
	if err != nil {
		t.Fatalf("erase: %v", err)
	}
	for kind, n := range want {
		if erased[kind] != n {
			t.Errorf("erased %d %s, want %d (all: %v)", erased[kind], kind, n, erased)
		}
	}
	counts, err := app.SampleShopCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for kind, n := range counts {
		if n != 0 {
			t.Errorf("%d sample %s left after the erase", n, kind)
		}
	}
	// The merchant's own page and question are still there.
	if rec := gctest.Request(t, app, http.MethodGet, "/x/cms/pages/returns", nil); !strings.Contains(rec.Body.String(), "Our returns") {
		t.Fatalf("their returns page after the erase = %d %s", rec.Code, rec.Body)
	}
	if rec := gctest.Request(t, app, http.MethodGet, "/x/faq", nil); !strings.Contains(rec.Body.String(), "Do you gift wrap?") ||
		strings.Contains(rec.Body.String(), "How long does delivery take?") {
		t.Fatalf("faq after the erase = %s, want only their question", rec.Body)
	}
}
