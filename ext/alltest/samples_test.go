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
	// links only those: four categories in the header and two pages below.
	want := gocommerce.SampleCounts{"products": 8, "orders": 12, "reviews": 8, "pages": 2, "faq": 5, "menu_links": 6}
	for kind, n := range want {
		if added[kind] != n {
			t.Errorf("added %d %s, want %d (all: %v)", added[kind], kind, n, added)
		}
	}

	// Every sample review is by somebody whose sample order bought the
	// product, so the engine's own test marks it verified.
	var verified int
	if err := app.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM reviews WHERE sample AND verified AND status = 'approved'`).Scan(&verified); err != nil || verified != 8 {
		t.Fatalf("verified, approved sample reviews = %d (%v), want 8", verified, err)
	}

	// What a storefront reads.
	rec := gctest.Request(t, app, http.MethodGet, "/x/navigation/menus/header", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"/categories/apparel"`) {
		t.Fatalf("header menu = %d %s, want a link to the apparel category", rec.Code, rec.Body)
	}
	rec = gctest.Request(t, app, http.MethodGet, "/x/navigation/menus/footer", nil)
	if !strings.Contains(rec.Body.String(), `"/pages/about"`) || strings.Contains(rec.Body.String(), `"/pages/returns"`) {
		t.Fatalf("footer menu = %s, want the sample about page and not the merchant's returns page", rec.Body)
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
