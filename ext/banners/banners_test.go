package banners

import (
	"net/http"
	"strconv"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

func addBanner(t *testing.T, app *gocommerce.App, in map[string]any) *Banner {
	t.Helper()
	rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/banners", in)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %v = %d %s", in, rec.Code, rec.Body)
	}
	var b Banner
	gctest.DecodeData(t, rec, &b)
	return &b
}

type publicPage struct {
	Slides []PublicBanner `json:"slides"`
	Grids  []PublicGrid   `json:"grids"`
}

func publicBanners(t *testing.T, app *gocommerce.App, page string) publicPage {
	t.Helper()
	target := "/x/banners"
	if page != "" {
		target += "?page=" + page
	}
	rec := gctest.Request(t, app, http.MethodGet, target, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", target, rec.Code, rec.Body)
	}
	var out publicPage
	gctest.DecodeData(t, rec, &out)
	return out
}

// The module's reason to exist: a shop puts pictures on a page, as slides or
// as a grid under a heading, arranges them, and the storefront reads the live
// ones for that page in that arrangement.
func TestBannersAreServedByPageInTheShopsOrder(t *testing.T) {
	app := gctest.New(t, New(Config{}))

	second := addBanner(t, app, map[string]any{"title": "Second slide", "image_url": "/media/b.webp", "link": "/products"})
	first := addBanner(t, app, map[string]any{"title": "First slide", "image_url": "https://cdn.example.com/a.webp",
		"mobile_image_url": "https://cdn.example.com/a-phone.webp", "link": "/collections/new-in"})
	addBanner(t, app, map[string]any{"placement": "grid", "group_title": "Shop by category", "title": "Apparel",
		"image_url": "/media/apparel.webp", "link": "/categories/apparel"})
	addBanner(t, app, map[string]any{"placement": "grid", "group_title": "Shop by category", "title": "Home",
		"image_url": "/media/home.webp", "link": "/categories/home"})
	addBanner(t, app, map[string]any{"title": "Hidden", "image_url": "/media/c.webp", "active": false})

	// The shop drags the first one up.
	var all []Banner
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/banners?page=home", nil), &all)
	ids := []int64{first.ID, second.ID}
	for _, b := range all {
		if b.ID != first.ID && b.ID != second.ID {
			ids = append(ids, b.ID)
		}
	}
	if rec := gctest.AdminRequest(t, app, http.MethodPut, "/api/admin/x/banners/order",
		map[string]any{"page": "home", "ids": ids}); rec.Code != http.StatusOK {
		t.Fatalf("reorder = %d %s", rec.Code, rec.Body)
	}

	home := publicBanners(t, app, "") // no page names the home page
	if len(home.Slides) != 2 || home.Slides[0].Title != "First slide" || home.Slides[1].Title != "Second slide" {
		t.Fatalf("slides = %+v, want the two live ones, first slide first", home.Slides)
	}
	if home.Slides[0].MobileImageURL != "https://cdn.example.com/a-phone.webp" || home.Slides[1].MobileImageURL != "" {
		t.Fatalf("phone pictures = %q, %q", home.Slides[0].MobileImageURL, home.Slides[1].MobileImageURL)
	}
	if len(home.Grids) != 1 || home.Grids[0].Title != "Shop by category" || len(home.Grids[0].Banners) != 2 ||
		home.Grids[0].Banners[0].Link != "/categories/apparel" {
		t.Fatalf("grids = %+v, want one grid of two tiles in the order added", home.Grids)
	}
	if none := publicBanners(t, app, "contact"); len(none.Slides) != 0 || none.Slides == nil || none.Grids == nil {
		t.Fatalf("a page with no banners = %+v, want empty lists, not nulls", none)
	}
}

// A banner is a picture and a link a shopper follows, so both are checked:
// no picture is no banner, and a link or picture that is not a web address
// or a path on this site is refused rather than rendered.
func TestABannerNeedsAPictureAndSafeAddresses(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	for _, in := range []map[string]any{
		{"title": "No picture"},
		{"title": "Script", "image_url": "/media/a.webp", "link": "javascript:alert(1)"},
		{"title": "Data", "image_url": "data:image/png;base64,AAAA"},
		{"title": "Protocol-relative", "image_url": "//evil.example/a.webp"},
		{"title": "Placement", "image_url": "/media/a.webp", "placement": "sidebar"},
		{"title": "Page", "image_url": "/media/a.webp", "page": "Not a slug"},
		// Only the home page shows banners on a storefront yet, so a banner
		// for another page would be one nobody ever sees.
		{"title": "About", "image_url": "/media/a.webp", "page": "about"},
		// A tab or a newline inside a path is dropped by the browser, which
		// turns "/\t/evil.example" into //evil.example; spaces break srcset.
		{"title": "Tab", "image_url": "/media/a.webp", "link": "/\t/evil.example"},
		{"title": "Newline", "image_url": "/media/a.webp", "link": "/\n/evil.example"},
		{"title": "Space", "image_url": "/media/a b.webp"},
		{"title": "Nul", "image_url": "/media/a.webp", "link": "/\x00x"},
	} {
		if rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/banners", in); rec.Code != http.StatusBadRequest {
			t.Errorf("%v = %d, want 400: %s", in, rec.Code, rec.Body)
		}
	}
	b := addBanner(t, app, map[string]any{"title": "Fine", "image_url": "/media/a.webp"})
	rec := gctest.AdminRequest(t, app, http.MethodPatch, "/api/admin/x/banners/"+itoa(b.ID),
		map[string]any{"title": "Renamed", "image_url": "/media/a.webp", "link": "https://example.com/sale"})
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d %s", rec.Code, rec.Body)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodDelete, "/api/admin/x/banners/"+itoa(b.ID), nil); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodDelete, "/api/admin/x/banners/"+itoa(b.ID), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("delete again = %d, want 404", rec.Code)
	}
}

// An order that does not name every banner on the page, once each, is
// refused rather than half applied.
func TestAReorderMustNameEveryBannerOnThePageOnce(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	a := addBanner(t, app, map[string]any{"title": "A", "image_url": "/media/a.webp"})
	b := addBanner(t, app, map[string]any{"title": "B", "image_url": "/media/b.webp"})
	addBanner(t, app, map[string]any{"title": "C", "image_url": "/media/c.webp"})
	for _, ids := range [][]int64{{a.ID}, {b.ID, b.ID, a.ID}} {
		if rec := gctest.AdminRequest(t, app, http.MethodPut, "/api/admin/x/banners/order",
			map[string]any{"page": "home", "ids": ids}); rec.Code != http.StatusBadRequest {
			t.Errorf("order %v = %d, want 400: %s", ids, rec.Code, rec.Body)
		}
	}
}

// A save that does not mention the switch leaves it where it was.
func TestASaveWithoutTheSwitchKeepsItsState(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	b := addBanner(t, app, map[string]any{"title": "Off", "image_url": "/media/a.webp", "active": false})
	rec := gctest.AdminRequest(t, app, http.MethodPatch, "/api/admin/x/banners/"+itoa(b.ID),
		map[string]any{"title": "Still off", "image_url": "/media/a.webp"})
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d %s", rec.Code, rec.Body)
	}
	var got Banner
	gctest.DecodeData(t, rec, &got)
	if got.Active {
		t.Fatal("a save without active switched the banner back on")
	}
}

// Banners have no switch (D132): a store cannot turn them off, but one that
// had the old switch off keeps its storefront as it was until it turns them
// on again, and the gate still holds for it.
func TestThePluginGatesTheStorefrontOnly(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	off := false
	if _, err := app.Plugins().Update(t.Context(), pluginKey, gocommerce.PluginPatch{Enabled: &off}); err == nil {
		t.Fatal("banners were switched off; they have no switch")
	}
	if rec := gctest.Request(t, app, http.MethodGet, "/x/banners", nil); rec.Code != http.StatusOK {
		t.Errorf("public banners = %d, want 200", rec.Code)
	}
	// A row written off before the switch went.
	if _, err := app.DB().ExecContext(t.Context(), `INSERT INTO plugins (key, enabled, settings) VALUES ($1, false, '{}')
		ON CONFLICT (store_id, key) DO UPDATE SET enabled = false`, pluginKey); err != nil {
		t.Fatal(err)
	}
	if rec := gctest.Request(t, app, http.MethodGet, "/x/banners", nil); rec.Code != http.StatusNotFound {
		t.Errorf("public banners while off = %d, want 404", rec.Code)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/banners", nil); rec.Code != http.StatusOK {
		t.Errorf("admin banners while off = %d, want 200", rec.Code)
	}
}

func TestRoutesDeclareRightsAndAreDocumented(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	gctest.AssertAdminRoutesDeclareRights(t, app, "banners")
	gctest.AssertSpecCoversModuleRoutes(t, app, "banners")
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
