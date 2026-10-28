package navigation

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

func TestAMenuIsATreeSavedWholeAndResolvedForTheStorefront(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ctx := context.Background()

	created := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/navigation/menus", MenuInput{Title: "Main menu"})
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", created.Code, created.Body)
	}
	var menu Menu
	gctest.DecodeData(t, created, &menu)
	if menu.Handle != "main-menu" || menu.ItemCount != 0 {
		t.Errorf("menu = %+v, want the handle derived from the title", menu)
	}
	if dup := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/navigation/menus", MenuInput{Title: "Main Menu"}); dup.Code != http.StatusConflict {
		t.Errorf("duplicate handle = %d, want 409", dup.Code)
	}

	tree := map[string]any{"items": []map[string]any{
		{"title": "Home", "kind": "home"},
		{"title": "Shop", "kind": "collection", "target": "all", "children": []map[string]any{
			{"title": "Tees", "kind": "category", "target": "tees"},
			{"title": "The classic", "kind": "product", "target": "crew-tee"},
		}},
		{"title": "About", "kind": "page", "target": "about"},
		{"title": "Blog", "kind": "url", "target": "https://blog.example"},
	}}
	saved := gctest.AdminRequest(t, app, http.MethodPut, "/api/admin/x/navigation/menus/"+strconv.FormatInt(menu.ID, 10)+"/items", tree)
	if saved.Code != http.StatusOK {
		t.Fatalf("set items = %d: %s", saved.Code, saved.Body)
	}
	gctest.DecodeData(t, saved, &menu)
	if menu.ItemCount != 6 || len(menu.Items) != 4 || len(menu.Items[1].Children) != 2 {
		t.Fatalf("tree = %+v", menu.Items)
	}
	if menu.Items[0].URL != "/" || menu.Items[1].URL != "/collections/all" || menu.Items[1].Children[1].URL != "/products/crew-tee" ||
		menu.Items[2].URL != "/pages/about" || menu.Items[3].URL != "https://blog.example" {
		t.Errorf("urls = %s %s %s %s %s", menu.Items[0].URL, menu.Items[1].URL, menu.Items[1].Children[1].URL, menu.Items[2].URL, menu.Items[3].URL)
	}

	// The storefront's paths are the Plugins screen's.
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{Settings: map[string]any{"product_path": "/p/{slug}"}}); err != nil {
		t.Fatal(err)
	}
	public := gctest.Request(t, app, http.MethodGet, "/x/navigation/menus/main-menu", nil)
	if public.Code != http.StatusOK {
		t.Fatalf("public = %d: %s", public.Code, public.Body)
	}
	gctest.DecodeData(t, public, &menu)
	if menu.Items[1].Children[1].URL != "/p/crew-tee" {
		t.Errorf("product url = %q, want the path from the plugin", menu.Items[1].Children[1].URL)
	}
	if list := gctest.Request(t, app, http.MethodGet, "/x/navigation/menus", nil); list.Code != http.StatusOK {
		t.Errorf("public list = %d", list.Code)
	}

	// Refusals name the item; the tree is untouched by a refused save.
	bad := gctest.AdminRequest(t, app, http.MethodPut, "/api/admin/x/navigation/menus/"+strconv.FormatInt(menu.ID, 10)+"/items",
		map[string]any{"items": []map[string]any{{"title": "Nowhere", "kind": "product"}}})
	if bad.Code != http.StatusBadRequest {
		t.Errorf("item without a target = %d: %s", bad.Code, bad.Body)
	}
	after := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/navigation/menus/"+strconv.FormatInt(menu.ID, 10), nil)
	gctest.DecodeData(t, after, &menu)
	if menu.ItemCount != 6 {
		t.Errorf("a refused save changed the tree: %d items", menu.ItemCount)
	}

	// A replaced tree is the new tree, not the old one plus.
	smaller := gctest.AdminRequest(t, app, http.MethodPut, "/api/admin/x/navigation/menus/"+strconv.FormatInt(menu.ID, 10)+"/items",
		map[string]any{"items": []map[string]any{{"title": "Home", "kind": "home"}}})
	gctest.DecodeData(t, smaller, &menu)
	if menu.ItemCount != 1 {
		t.Errorf("after replacing = %d items", menu.ItemCount)
	}

	if del := gctest.AdminRequest(t, app, http.MethodDelete, "/api/admin/x/navigation/menus/"+strconv.FormatInt(menu.ID, 10), nil); del.Code != http.StatusNoContent {
		t.Errorf("delete = %d", del.Code)
	}
	if gone := gctest.Request(t, app, http.MethodGet, "/x/navigation/menus/main-menu", nil); gone.Code != http.StatusNotFound {
		t.Errorf("after delete = %d", gone.Code)
	}
}

func TestNavigationRoutesAreDocumentedAndGated(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	gctest.AssertAdminRoutesDeclareRights(t, app, "navigation")
	gctest.AssertSpecCoversModuleRoutes(t, app, "navigation")
}

// A store opened after the module was installed gets a header and a footer
// that links the six standard pages (D145); the store that was there when
// the migration ran keeps the menus it was given.
func TestANewStoreStartsWithTheDefaultMenus(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	st, err := app.Stores().Create(context.Background(), "Second", "second")
	if err != nil {
		t.Fatal(err)
	}
	urls := func(ctx context.Context, handle string) []string {
		t.Helper()
		rows, err := app.DB().QueryContext(ctx, `
			SELECT i.title || ' ' || i.target FROM navigation_items i
			JOIN navigation_menus m ON m.id = i.menu_id
			WHERE m.handle = $1 ORDER BY i.position`, handle)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}
	second := gocommerce.WithStore(context.Background(), st.ID)
	want := "About us /about-us|Contact us /contact-us|Privacy policy /privacy-policy|Terms and conditions /terms-and-conditions|" +
		"Shipping policy /shipping-policy|Refund and returns policy /refund-policy"
	if got := strings.Join(urls(second, "footer"), "|"); got != want {
		t.Errorf("new store's footer = %s, want %s", got, want)
	}
	if got := strings.Join(urls(second, "header"), "|"); got != "Home |Shop /products|Contact /contact-us" {
		t.Errorf("new store's header = %s", got)
	}

	// Without the CMS module the footer still links the six paths, which the
	// storefront hides until a page is published, and no page is written.
	var cms *string
	if err := app.DB().QueryRowContext(second, `SELECT to_regclass('cms_pages')::text`).Scan(&cms); err != nil || cms != nil {
		t.Errorf("cms_pages = %v (%v), want no table without the module", cms, err)
	}

	// Opening a store writes nothing to another one.
	if got := strings.Join(urls(context.Background(), "footer"), "|"); got != "Search /search|Contact us /contact|Privacy policy /pages/privacy" {
		t.Errorf("the first store's footer = %s, want the migration's", got)
	}
}
