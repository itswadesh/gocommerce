package layouts

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

func layoutsApp(t *testing.T) *gocommerce.App {
	t.Helper()
	app := gctest.New(t, New(Config{}))
	switchOn(t, app, true)
	return app
}

func switchOn(t *testing.T, app *gocommerce.App, on bool) {
	t.Helper()
	if rec := gctest.AdminRequest(t, app, http.MethodPatch, "/api/admin/plugins/"+pluginKey, map[string]any{"enabled": on}); rec.Code != http.StatusOK {
		t.Fatalf("switch = %d %s", rec.Code, rec.Body)
	}
}

func adminState(t *testing.T, app *gocommerce.App, method, target string, body any, want int) State {
	t.Helper()
	rec := gctest.AdminRequest(t, app, method, target, body)
	if rec.Code != want {
		t.Fatalf("%s %s = %d %s, want %d", method, target, rec.Code, rec.Body, want)
	}
	var st State
	if want < 300 {
		gctest.DecodeData(t, rec, &st)
	}
	return st
}

func publicPage(t *testing.T, app *gocommerce.App, target string, want int) PublicPage {
	t.Helper()
	rec := gctest.Request(t, app, http.MethodGet, target, nil)
	if rec.Code != want {
		t.Fatalf("GET %s = %d %s, want %d", target, rec.Code, rec.Body, want)
	}
	var p PublicPage
	if want < 300 {
		gctest.DecodeData(t, rec, &p)
	}
	return p
}

func ids(p PublicPage) string {
	var out []string
	for _, s := range p.Sections {
		out = append(out, s.ID)
	}
	return strings.Join(out, ",")
}

func sections(list ...map[string]any) map[string]any { return map[string]any{"sections": list} }

func hero(id, heading string) map[string]any {
	return map[string]any{"id": id, "type": "hero", "enabled": true, "settings": map[string]any{"heading": heading}}
}

// The page is edited as a draft and the storefront sees only what is
// published, whole; each publish is kept, and rolling back publishes an
// earlier one again.
func TestAPageIsDraftedPublishedAndRolledBack(t *testing.T) {
	app := layoutsApp(t)

	st := adminState(t, app, http.MethodGet, "/api/admin/x/layouts/home", nil, http.StatusOK)
	if st.Saved || st.Version != 0 || len(st.Document.Sections) == 0 || st.Published != nil {
		t.Fatalf("before any save = %+v, want the unsaved starter", st)
	}
	wantStarter(t, publicPage(t, app, "/x/layouts/home", http.StatusOK))

	first := sections(hero("a", "One"), hero("b", "Two"),
		map[string]any{"id": "c", "type": "rich-text", "enabled": false, "settings": map[string]any{"body": "Hidden"}})
	st = adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 0, "document": first}, http.StatusOK)
	if !st.Saved || st.Version != 1 || !st.Changed {
		t.Fatalf("after the first save = %+v", st)
	}
	// A saved draft is not shown: until a publish, shoppers see the starter.
	wantStarter(t, publicPage(t, app, "/x/layouts/home", http.StatusOK))

	st = adminState(t, app, http.MethodPost, "/api/admin/x/layouts/home/publish", map[string]any{"version": 1}, http.StatusOK)
	if st.Published == nil || st.Published.Number != 1 || st.Changed {
		t.Fatalf("after publishing = %+v", st)
	}
	if got := publicPage(t, app, "/x/layouts/home", http.StatusOK); ids(got) != "a,b" || got.Revision != 1 {
		t.Fatalf("published page = %s (revision %d), want a,b: the hidden section is not shown", ids(got), got.Revision)
	}

	// An edit to the draft leaves the storefront alone until it is published.
	moved := sections(hero("b", "Two"), hero("new", "Inserted"), hero("a", "One"))
	st = adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 1, "document": moved}, http.StatusOK)
	if !st.Changed {
		t.Fatal("the draft differs from what is published")
	}
	if got := publicPage(t, app, "/x/layouts/home", http.StatusOK); ids(got) != "a,b" {
		t.Fatalf("storefront after a draft edit = %s, want still a,b", ids(got))
	}
	adminState(t, app, http.MethodPost, "/api/admin/x/layouts/home/publish", map[string]any{"version": 2}, http.StatusOK)
	if got := publicPage(t, app, "/x/layouts/home", http.StatusOK); ids(got) != "b,new,a" || got.Revision != 2 {
		t.Fatalf("after the second publish = %s (revision %d), want b,new,a at 2", ids(got), got.Revision)
	}

	st = adminState(t, app, http.MethodPost, "/api/admin/x/layouts/home/rollback", map[string]any{"revision": 1}, http.StatusOK)
	if got := publicPage(t, app, "/x/layouts/home", http.StatusOK); ids(got) != "a,b" || got.Revision != 3 {
		t.Fatalf("after rolling back = %s (revision %d), want a,b as revision 3", ids(got), got.Revision)
	}
	if st.Published == nil || st.Published.RestoredFrom == nil || *st.Published.RestoredFrom != 1 || len(st.Document.Sections) != 3 {
		t.Fatalf("state after rolling back = %+v, want revision 3 from 1 and the draft back to it", st)
	}

	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/layouts/home/revisions", nil)
	var revs []Revision
	gctest.DecodeData(t, rec, &revs)
	if len(revs) != 3 || revs[0].Number != 3 || revs[2].Number != 1 {
		t.Fatalf("revisions = %+v, want 3, 2, 1", revs)
	}
}

// A save names the version it was read at: two editors on one page cannot
// overwrite each other unseen.
func TestAStaleSaveIsRefused(t *testing.T) {
	app := layoutsApp(t)
	body := map[string]any{"version": 0, "document": sections(hero("a", "One"))}
	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", body, http.StatusOK)

	rec := gctest.AdminRequest(t, app, http.MethodPut, "/api/admin/x/layouts/home", body)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "stale_draft") {
		t.Fatalf("second first-save = %d %s, want 409 stale_draft", rec.Code, rec.Body)
	}
	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 1, "document": sections(hero("a", "Mine"))}, http.StatusOK)
	rec = gctest.AdminRequest(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 1, "document": sections(hero("a", "Theirs"))})
	if rec.Code != http.StatusConflict {
		t.Fatalf("save at an old version = %d, want 409", rec.Code)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/layouts/home/publish", map[string]any{"version": 1}); rec.Code != http.StatusConflict {
		t.Fatalf("publish at an old version = %d, want 409", rec.Code)
	}
}

func TestABadPageIsRefusedWithItsReason(t *testing.T) {
	app := layoutsApp(t)
	rec := gctest.AdminRequest(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 0, "document": sections(
		map[string]any{"id": "a", "type": "announcement-bar", "settings": map[string]any{"text": "Hi", "link": "javascript:alert(1)"}})})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Announcement bar") {
		t.Fatalf("a script link = %d %s, want 400 naming the section", rec.Code, rec.Body)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/layouts/checkout", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("a page that cannot be arranged = %d, want 404", rec.Code)
	}
}

// On by default: a shop that publishes a page sees it on the storefront
// without first finding a switch, and until it publishes the storefront draws
// the starting home page.
func TestTheEditorIsOnUntilSwitchedOff(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	wantStarter(t, publicPage(t, app, "/x/layouts/home", http.StatusOK))
	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 0, "document": sections(hero("a", "One"))}, http.StatusOK)
	adminState(t, app, http.MethodPost, "/api/admin/x/layouts/home/publish", map[string]any{"version": 1}, http.StatusOK)
	publicPage(t, app, "/x/layouts/home", http.StatusOK)
}

// Switched off, the storefront is told there is no arranged page even when
// one is published, and draws its built-in home page.
func TestTheSwitchHidesThePublishedPage(t *testing.T) {
	app := layoutsApp(t)
	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 0, "document": sections(hero("a", "One"))}, http.StatusOK)
	adminState(t, app, http.MethodPost, "/api/admin/x/layouts/home/publish", map[string]any{"version": 1}, http.StatusOK)
	publicPage(t, app, "/x/layouts/home", http.StatusOK)
	switchOn(t, app, false)
	publicPage(t, app, "/x/layouts/home", http.StatusNotFound)
}

// A product grid holds references; the storefront gets the products as the
// catalogue has them now — only active ones, in the shop's order, the limit
// respected — and a grid whose collection is gone is empty, not an error.
func TestProductGridsShowLiveProducts(t *testing.T) {
	app := layoutsApp(t)
	ctx := context.Background()
	a := gctest.CreateProduct(t, app, "LY-A", 1000, 3)
	b := gctest.CreateProduct(t, app, "LY-B", 1500, 3)
	c := gctest.CreateProduct(t, app, "LY-C", 2000, 3)
	col, err := app.Collections().Create(ctx, gocommerce.CollectionInput{Title: "Featured"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Collections().SetCollectionProducts(ctx, col.ID, []int64{c.ID, a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	draft := gocommerce.ProductDraft
	if _, err := app.Products().UpdateProduct(ctx, b.ID, gocommerce.ProductPatch{Status: &draft}); err != nil {
		t.Fatal(err)
	}

	doc := sections(
		map[string]any{"id": "picked", "type": "product-grid", "enabled": true, "settings": map[string]any{
			"source": "products", "products": []int64{b.ID, c.ID, a.ID}, "limit": 1}},
		map[string]any{"id": "featured", "type": "product-grid", "enabled": true, "settings": map[string]any{"collection": col.ID}},
		map[string]any{"id": "gone", "type": "product-grid", "enabled": true, "settings": map[string]any{"collection": 999999}},
		map[string]any{"id": "newest", "type": "product-grid", "enabled": true, "settings": map[string]any{"source": "newest", "limit": 2}},
		map[string]any{"id": "unpicked", "type": "product-grid", "enabled": true, "settings": map[string]any{}},
	)
	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 0, "document": doc}, http.StatusOK)
	adminState(t, app, http.MethodPost, "/api/admin/x/layouts/home/publish", map[string]any{"version": 1}, http.StatusOK)

	got := publicPage(t, app, "/x/layouts/home", http.StatusOK)
	if len(got.Sections) != 5 {
		t.Fatalf("sections = %d, want 5", len(got.Sections))
	}
	if p := got.Sections[0].Products; len(p) != 1 || p[0].ID != c.ID {
		t.Errorf("picked grid = %v, want only %d: the draft is skipped and the limit is one", p, c.ID)
	}
	if p := got.Sections[1].Products; len(p) != 2 || p[0].ID != c.ID || p[1].ID != a.ID {
		t.Errorf("collection grid = %v, want %d then %d", p, c.ID, a.ID)
	}
	if p := got.Sections[2].Products; len(p) != 0 {
		t.Errorf("grid of a deleted collection = %v, want empty", p)
	}
	// The starting page's grid: the newest on sale, the draft skipped.
	if p := got.Sections[3].Products; len(p) != 2 || p[0].ID != c.ID || p[1].ID != a.ID {
		t.Errorf("newest grid = %v, want the newest active, %d then %d", p, c.ID, a.ID)
	}
	// A collection grid with none picked stays empty, as it always has.
	if p := got.Sections[4].Products; len(p) != 0 {
		t.Errorf("grid with no collection = %v, want empty", p)
	}
	if p := got.Sections[1].Products[0]; p.Variants[0].Price.AmountMinor != 2000 {
		t.Errorf("price = %d, want the catalogue's 2000", p.Variants[0].Price.AmountMinor)
	}
}

// A preview key shows the saved draft, for half an hour, to whoever holds it,
// and is never cached or indexed; without it only the published page shows.
// Before the first save the draft is the starting page, and a key shows that:
// the theme editor frames the page from the moment it opens, and opening it
// writes nothing.
func TestAPreviewShowsTheDraftToItsKey(t *testing.T) {
	app := layoutsApp(t)
	rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/layouts/home/preview", nil)
	var early struct {
		Token string `json:"token"`
	}
	gctest.DecodeData(t, rec, &early)
	if rec.Code != http.StatusCreated {
		t.Fatalf("preview before a save = %d %s, want 201", rec.Code, rec.Body)
	}
	if got := publicPage(t, app, "/x/layouts/home?preview="+early.Token, http.StatusOK); len(got.Sections) != len(Starter("home").Sections) || !got.Preview {
		t.Fatalf("preview before a save = %+v, want the starting page", got)
	}
	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 0, "document": sections(hero("draft", "Draft"))}, http.StatusOK)

	rec = gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/layouts/home/preview", nil)
	var key struct {
		Token string `json:"token"`
	}
	gctest.DecodeData(t, rec, &key)
	if rec.Code != http.StatusCreated || key.Token == "" {
		t.Fatalf("preview = %d %s", rec.Code, rec.Body)
	}

	prev := gctest.Request(t, app, http.MethodGet, "/x/layouts/home?preview="+key.Token, nil)
	if prev.Code != http.StatusOK || prev.Header().Get("Cache-Control") != "no-store" || prev.Header().Get("X-Robots-Tag") != "noindex" {
		t.Fatalf("preview page = %d, cache %q, robots %q", prev.Code, prev.Header().Get("Cache-Control"), prev.Header().Get("X-Robots-Tag"))
	}
	var p PublicPage
	gctest.DecodeData(t, prev, &p)
	if !p.Preview || ids(p) != "draft" {
		t.Fatalf("preview = %+v, want the draft", p)
	}
	publicPage(t, app, "/x/layouts/home?preview=not-a-key", http.StatusNotFound)
	wantStarter(t, publicPage(t, app, "/x/layouts/home", http.StatusOK))
}

func TestTheRegistryDescribesEverySection(t *testing.T) {
	app := layoutsApp(t)
	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/layouts/sections", nil)
	var reg struct {
		Sections []SectionType `json:"sections"`
		Themes   []Theme       `json:"themes"`
	}
	gctest.DecodeData(t, rec, &reg)
	// Every type but the newsletter sign-up and Google reviews, whose plugins
	// this store lacks.
	if len(reg.Sections) != len(Sections)-2 || len(reg.Themes) == 0 {
		t.Fatalf("registry = %+v", reg)
	}
	// The starter is a valid page: what the editor opens with can be saved.
	if _, err := Validate("home", Starter("home")); err != nil {
		t.Fatalf("the starter page does not validate: %v", err)
	}
}

// The starting home page brings its look: theme settings that survive the
// registry's checks unchanged, and every pair the theme editor warns about
// readable at 4.5:1. Those four pairs are every text-on-colour pair the
// starter's sections draw (the hero, tiles, product grid, picture beside
// words and the benefits band).
func TestTheStarterBringsABoldReadableLook(t *testing.T) {
	start := Starter("home")
	got, err := Validate("home", start)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ThemeSettings) == 0 || len(got.ThemeSettings) != len(start.ThemeSettings) {
		t.Fatalf("theme settings = %v, want the starter's %v kept whole", got.ThemeSettings, start.ThemeSettings)
	}
	ts := got.ThemeSettings
	// The storefront's own colours for what the starter leaves unset, and the
	// pairs, as admin/src/lib/layouts.js has them (STOREFRONT_COLOURS, PAIRS).
	own := map[string]string{"background": "#ffffff", "text": "#0f172a"}
	colour := func(key string) string {
		if v, ok := ts[key].(string); ok {
			return v
		}
		return own[key]
	}
	for _, pair := range [][2]string{{"text", "background"}, {"accent_text", "accent"}, {"text", "muted"}, {"sale", "background"}} {
		if r := contrast(colour(pair[0]), colour(pair[1])); r < 4.5 {
			t.Errorf("%s on %s is %.2f:1, want at least 4.5", pair[0], pair[1], r)
		}
	}
	// The yellow is not checked against the page: it is a fill, never text.
	for k, want := range map[string]any{"accent": "#ffd232", "accent_text": "#111111", "muted": "#fef3c7",
		"sale": "#198837", "radius": "large", "type_scale": "standard", "heading_weight": "extrabold", "density": "compact",
		"card_badge": "product_badge", "card_vendor": true, "card_title_lines": "one", "card_rating": true, "card_discount": true,
		"card_wishlist": true, "card_add_to_cart": false, "card_second_image": true, "list_columns_mobile": "two",
		"list_columns_desktop": "five", "list_subcategories": true, "list_quick_filters": true, "list_filters_desktop": "sidebar",
		"list_mobile_bar": true, "pdp_sticky_buy": true, "pdp_gallery": "carousel", "pdp_highlights_grid": true,
		"pdp_delivery_check": true, "pdp_low_stock_at": 5} {
		if ts[k] != want {
			t.Errorf("starter %s = %v, want %v", k, ts[k], want)
		}
	}
	if _, ok := ts["pdp_size_guide"]; ok {
		t.Errorf("starter size guide = %v, want none: a new store has no such page", ts["pdp_size_guide"])
	}
}

// The starting home page opens with its pictures: the hero (and its phone
// crop), the four tiles and the lookbook, each one the engine itself serves,
// so they load on every store's storefront and in the editor's preview.
func TestTheStarterPicturesAreServedByTheEngine(t *testing.T) {
	app := layoutsApp(t)
	var pics []string
	var walk func(v any, key string)
	walk = func(v any, key string) {
		switch x := v.(type) {
		case map[string]any:
			for k, y := range x {
				walk(y, k)
			}
		case []any:
			for _, y := range x {
				walk(y, "")
			}
		case []map[string]any:
			for _, y := range x {
				walk(y, "")
			}
		case string:
			if key == "image" || key == "mobile_image" {
				pics = append(pics, x)
			}
		}
	}
	start, err := Validate("home", Starter("home"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range start.Sections {
		walk(s.Settings, "")
	}
	if len(pics) != 7 {
		t.Fatalf("starter pictures = %v, want the hero, its phone crop, four tiles and the lookbook", pics)
	}
	// The photographs say what they show; the tiles' pictures are decoration
	// beside the tile's name.
	for _, sec := range start.Sections {
		if sec.ID == "hero" || sec.ID == "lookbook" {
			if alt, _ := sec.Settings["image_alt"].(string); len(alt) < 20 {
				t.Errorf("%s image_alt = %q, want a description of the photograph", sec.ID, alt)
			}
		}
	}
	for _, p := range pics {
		rec := gctest.Request(t, app, http.MethodGet, p, nil)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/webp" || rec.Body.Len() == 0 {
			t.Errorf("GET %s = %d %q (%d bytes), want a WebP", p, rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
		}
		if !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
			t.Errorf("GET %s cache = %q, want immutable: a changed picture takes a new folder", p, rec.Header().Get("Cache-Control"))
		}
	}
	for _, bad := range []string{starterPicturePrefix + "missing.webp", starterPicturePrefix + "..%2Flayouts.go"} {
		if rec := gctest.Request(t, app, http.MethodGet, bad, nil); rec.Code == http.StatusOK {
			t.Errorf("GET %s = 200, want nothing outside the pictures", bad)
		}
	}
}

// wantStarter fails unless p is the starting home page as the storefront gets
// it before a first publish: its shown sections, no revision, no preview.
func wantStarter(t *testing.T, p PublicPage) {
	t.Helper()
	want := PublicPage{}
	for _, s := range Starter("home").Sections {
		if s.Enabled {
			want.Sections = append(want.Sections, PublicSection{ID: s.ID})
		}
	}
	if p.Revision != 0 || p.Preview || ids(p) != ids(want) || p.ThemeSettings["accent"] != Starter("home").ThemeSettings["accent"] {
		t.Fatalf("home = %s (revision %d, preview %v, theme %v), want the starter %s", ids(p), p.Revision, p.Preview, p.ThemeSettings, ids(want))
	}
}

// The owner, 2026-10-28: "show the bold starter to unpublished stores". A
// store that has published nothing shows the starting home page and its look
// rather than the storefront's built-in page; the header, footer and
// templates still answer 404, so the storefront keeps its own; a published
// page replaces the starter; and the switch still turns it all off.
func TestAnUnpublishedHomeShowsTheStarter(t *testing.T) {
	app := layoutsApp(t)
	wantStarter(t, publicPage(t, app, "/x/layouts/home", http.StatusOK))
	theme, _ := publicTheme(t, app, "/x/layouts/theme", http.StatusOK)
	if theme.Revision != 0 || theme.Settings["accent"] != Starter("home").ThemeSettings["accent"] {
		t.Fatalf("theme before a publish = %+v, want the starter's", theme)
	}
	for _, page := range []string{"header", "footer", "product", "collection"} {
		publicPage(t, app, "/x/layouts/"+page, http.StatusNotFound)
	}

	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 0, "document": sections(hero("a", "One"))}, http.StatusOK)
	adminState(t, app, http.MethodPost, "/api/admin/x/layouts/home/publish", map[string]any{"version": 1}, http.StatusOK)
	if got := publicPage(t, app, "/x/layouts/home", http.StatusOK); ids(got) != "a" || got.Revision != 1 {
		t.Fatalf("published home = %s (revision %d), want the shop's own", ids(got), got.Revision)
	}
	if theme, _ := publicTheme(t, app, "/x/layouts/theme", http.StatusOK); len(theme.Settings) != 0 {
		t.Fatalf("theme after publishing a page without settings = %v, want the shop's (none)", theme.Settings)
	}

	switchOn(t, app, false)
	publicPage(t, app, "/x/layouts/home", http.StatusNotFound)
	publicTheme(t, app, "/x/layouts/theme", http.StatusNotFound)
}

func contrast(a, b string) float64 {
	lum := func(hex string) float64 {
		var out float64
		for i, w := range []float64{0.2126, 0.7152, 0.0722} {
			v, _ := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
			c := float64(v) / 255
			if c <= 0.04045 {
				c /= 12.92
			} else {
				c = math.Pow((c+0.055)/1.055, 2.4)
			}
			out += w * c
		}
		return out
	}
	x, y := lum(a)+0.05, lum(b)+0.05
	return math.Max(x, y) / math.Min(x, y)
}

func TestRoutesDeclareRightsAndAreDocumented(t *testing.T) {
	app := layoutsApp(t)
	gctest.AssertAdminRoutesDeclareRights(t, app, "layouts")
	gctest.AssertSpecCoversModuleRoutes(t, app, "layouts")
}

// Reset throws the draft away: back to what is published, or to the starting
// page when nothing is. It names the version it was read at, like a save, so
// it never discards an edit made in another tab unseen.
func TestResetPutsTheDraftBackToWhatIsPublished(t *testing.T) {
	app := layoutsApp(t)

	// Before anything is saved there is nothing to throw away.
	st := adminState(t, app, http.MethodPost, "/api/admin/x/layouts/home/reset", map[string]any{"version": 0}, http.StatusOK)
	if st.Saved || st.Version != 0 || len(st.Document.Sections) != len(Starter("home").Sections) {
		t.Fatalf("reset of an unsaved page = %+v, want the unsaved starter", st)
	}

	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 0, "document": sections(hero("a", "Mine"))}, http.StatusOK)
	st = adminState(t, app, http.MethodPost, "/api/admin/x/layouts/home/reset", map[string]any{"version": 1}, http.StatusOK)
	if st.Version != 2 || !st.Changed || len(st.Document.Sections) != len(Starter("home").Sections) || st.Document.Sections[0].ID != Starter("home").Sections[0].ID {
		t.Fatalf("reset with nothing published = %+v, want the starter as version 2", st)
	}

	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 2, "document": sections(hero("a", "Live"))}, http.StatusOK)
	adminState(t, app, http.MethodPost, "/api/admin/x/layouts/home/publish", map[string]any{"version": 3}, http.StatusOK)
	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 3, "document": sections(hero("a", "Edited"), hero("b", "New"))}, http.StatusOK)

	if rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/layouts/home/reset", map[string]any{"version": 3}); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "stale_draft") {
		t.Fatalf("reset at an old version = %d %s, want 409 stale_draft", rec.Code, rec.Body)
	}
	st = adminState(t, app, http.MethodPost, "/api/admin/x/layouts/home/reset", map[string]any{"version": 4}, http.StatusOK)
	if st.Changed || st.Version != 5 || len(st.Document.Sections) != 1 || st.Document.Sections[0].Settings["heading"] != "Live" {
		t.Fatalf("reset after a publish = %+v, want the published page back as the draft", st)
	}
	if got := publicPage(t, app, "/x/layouts/home", http.StatusOK); got.Revision != 1 || ids(got) != "a" {
		t.Fatalf("the storefront after a reset = %s at %d, want revision 1 untouched", ids(got), got.Revision)
	}
}

// The preview key comes back with the storefront's address, so the editor
// can frame the store's own home page without being told where it is.
func TestAPreviewNamesTheStorefront(t *testing.T) {
	app := layoutsApp(t)
	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 0, "document": sections(hero("a", "One"))}, http.StatusOK)
	rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/layouts/home/preview", nil)
	var key map[string]any
	gctest.DecodeData(t, rec, &key)
	if _, ok := key["storefront"]; !ok || key["token"] == "" {
		t.Fatalf("preview = %s, want a token and the storefront's address", rec.Body)
	}
}
