package alltest

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// The sample shop beside the modules that act on orders and on rights (D96).

// The invoices module numbers invoices without gaps, for the tax office: none
// of those numbers goes to an order nobody placed, at start-up or later.
func TestNoInvoiceIsIssuedForASampleOrder(t *testing.T) {
	app := gctest.New(t, everyModule()...)
	ctx := context.Background()
	if _, err := app.AddSampleShop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	gctest.DrainOutbox(t, app)
	var invoices int
	if err := app.DB().QueryRowContext(ctx, `SELECT count(*) FROM invoices_documents`).Scan(&invoices); err != nil {
		t.Fatal(err)
	}
	if invoices != 0 {
		t.Fatalf("invoices = %d, want none for sample orders", invoices)
	}
}

// Adding or erasing the sample shop writes pages, reviews, questions and menu
// links, so it needs those modules' rights as well as the engine's: a role a
// store has kept off the pages cannot publish them this way.
func TestTheSampleShopNeedsTheModulesRightsToo(t *testing.T) {
	app := gctest.New(t, everyModule()...)
	core := []string{"catalog.read", "catalog.write", "categories.write", "collections.write",
		"discounts.write", "orders.write", "media.write"}
	if rec := gctest.AdminRequest(t, app, http.MethodPut, "/api/admin/roles/manager",
		map[string]any{"rights": core}); rec.Code != http.StatusOK {
		t.Fatalf("narrow the manager = %d %s", rec.Code, rec.Body)
	}
	manager := gctest.OperatorToken(t, app, "manager@example.com", gocommerce.RoleManager)

	rec := gctest.SessionRequest(t, app, manager, http.MethodPost, "/api/admin/sample-content", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("add as a manager without the modules' rights = %d, want 403: %s", rec.Code, rec.Body)
	}

	// The count says which rights adding and erasing need, so the panel can
	// offer them only to somebody who holds them all.
	var counted struct {
		Rights []string `json:"rights"`
	}
	gctest.DecodeData(t, gctest.SessionRequest(t, app, manager, http.MethodGet, "/api/admin/sample-content", nil), &counted)
	for _, want := range []string{"pages.write", "faq.write", "reviews.moderate", "menus.write", "orders.write"} {
		found := false
		for _, r := range counted.Rights {
			found = found || r == want
		}
		if !found {
			t.Errorf("rights = %v, want %s among them", counted.Rights, want)
		}
	}
}

// Each sample review is written after the order that bought what it reviews.
func TestSampleReviewsFollowTheirPurchases(t *testing.T) {
	app := gctest.New(t, everyModule()...)
	ctx := context.Background()
	if _, err := app.AddSampleShop(ctx); err != nil {
		t.Fatal(err)
	}
	var early int
	if err := app.DB().QueryRowContext(ctx, `
		SELECT count(*) FROM reviews r
		WHERE r.sample AND NOT EXISTS (
		    SELECT 1 FROM orders o JOIN order_lines l ON l.order_id = o.id
		    WHERE lower(o.email) = r.email AND l.product_id = r.product_id
		      AND o.status = 'confirmed' AND o.created_at < r.created_at)`).Scan(&early); err != nil {
		t.Fatal(err)
	}
	if early != 0 {
		t.Fatalf("%d sample reviews predate their author's purchase, want none", early)
	}
}

// A menu the merchant saves without touching the sample links keeps them
// erasable: the erase must not leave a link to a page it removed.
func TestSavingAMenuKeepsItsSampleLinksErasable(t *testing.T) {
	app := gctest.New(t, everyModule()...)
	ctx := context.Background()
	if _, err := app.AddSampleShop(ctx); err != nil {
		t.Fatal(err)
	}
	var footer int64
	if err := app.DB().QueryRowContext(ctx, `SELECT id FROM navigation_menus WHERE handle = 'footer'`).Scan(&footer); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(footer, 10)
	var menu struct {
		Items []map[string]any `json:"items"`
	}
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/navigation/menus/"+id, nil), &menu)
	items := make([]map[string]any, 0, len(menu.Items))
	for _, it := range menu.Items {
		items = append(items, map[string]any{"title": it["title"], "kind": it["kind"], "target": it["target"]})
	}
	if rec := gctest.AdminRequest(t, app, http.MethodPut, "/api/admin/x/navigation/menus/"+id+"/items",
		map[string]any{"items": items}); rec.Code != http.StatusOK {
		t.Fatalf("save the footer = %d %s", rec.Code, rec.Body)
	}
	if _, err := app.EraseSampleShop(ctx); err != nil {
		t.Fatal(err)
	}
	rec := gctest.Request(t, app, http.MethodGet, "/x/navigation/menus/footer", nil)
	if strings.Contains(rec.Body.String(), `"/pages/about"`) {
		t.Fatalf("footer after the erase = %s, want no link to the erased about page", rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"Contact us"`) {
		t.Fatalf("footer after the erase = %s, want the store's own links kept", rec.Body)
	}
}
