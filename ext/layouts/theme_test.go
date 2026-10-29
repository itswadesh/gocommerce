package layouts

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// publicTheme reads GET /x/layouts/theme.
func publicTheme(t *testing.T, app *gocommerce.App, target string, want int) (PublicTheme, http.Header) {
	t.Helper()
	rec := gctest.Request(t, app, http.MethodGet, target, nil)
	if rec.Code != want {
		t.Fatalf("GET %s = %d %s, want %d", target, rec.Code, rec.Body, want)
	}
	var out PublicTheme
	if want < 300 {
		gctest.DecodeData(t, rec, &out)
	}
	return out, rec.Header()
}

// The theme settings publish with the page, and every page of the storefront
// reads them from a small route of their own; a preview key reads the
// draft's, and until something is published the starter's are answered.
func TestThemeSettingsPublishWithThePage(t *testing.T) {
	app := layoutsApp(t)
	starter := Starter("home").ThemeSettings["accent"]
	if got, _ := publicTheme(t, app, "/x/layouts/theme", http.StatusOK); got.Settings["accent"] != starter {
		t.Fatalf("theme before a save = %+v, want the starter's", got)
	}

	doc := sections(hero("a", "One"))
	doc["theme_settings"] = map[string]any{"accent": "#AA3300", "body_font": "lora"}
	st := adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 0, "document": doc}, http.StatusOK)
	if st.Document.ThemeSettings["accent"] != "#aa3300" {
		t.Fatalf("saved theme settings = %v", st.Document.ThemeSettings)
	}
	if got, _ := publicTheme(t, app, "/x/layouts/theme", http.StatusOK); got.Settings["accent"] != starter {
		t.Fatalf("theme with only a draft = %+v, want the starter's", got)
	}

	adminState(t, app, http.MethodPost, "/api/admin/x/layouts/home/publish", map[string]any{"version": 1}, http.StatusOK)
	got, h := publicTheme(t, app, "/x/layouts/theme", http.StatusOK)
	if got.Revision != 1 || got.Settings["accent"] != "#aa3300" || got.Settings["body_font"] != "lora" {
		t.Fatalf("published theme = %+v", got)
	}
	if h.Get("Cache-Control") != "public, max-age=30" {
		t.Errorf("Cache-Control = %q, want a short public cache", h.Get("Cache-Control"))
	}
	if page := publicPage(t, app, "/x/layouts/home", http.StatusOK); page.ThemeSettings["accent"] != "#aa3300" {
		t.Fatalf("home page theme settings = %v, want them with the page", page.ThemeSettings)
	}

	// A draft change is the preview's alone until it is published.
	doc["theme_settings"] = map[string]any{"accent": "#0055ff"}
	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 1, "document": doc}, http.StatusOK)
	rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/layouts/home/preview", nil)
	var key struct {
		Token string `json:"token"`
	}
	gctest.DecodeData(t, rec, &key)
	prev, h := publicTheme(t, app, "/x/layouts/theme?preview="+key.Token, http.StatusOK)
	if !prev.Preview || prev.Settings["accent"] != "#0055ff" || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("preview theme = %+v (cache %q), want the draft's, never stored", prev, h.Get("Cache-Control"))
	}
	if got, _ := publicTheme(t, app, "/x/layouts/theme", http.StatusOK); got.Settings["accent"] != "#aa3300" {
		t.Fatalf("published theme after a draft edit = %v, want still the published one", got.Settings)
	}
	publicTheme(t, app, "/x/layouts/theme?preview=not-a-key", http.StatusNotFound)

	// Published with none: an empty set, so the storefront keeps its look.
	delete(doc, "theme_settings")
	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 2, "document": doc}, http.StatusOK)
	adminState(t, app, http.MethodPost, "/api/admin/x/layouts/home/publish", map[string]any{"version": 3}, http.StatusOK)
	if got, _ := publicTheme(t, app, "/x/layouts/theme", http.StatusOK); got.Revision != 2 || len(got.Settings) != 0 {
		t.Fatalf("theme published without settings = %+v, want none", got)
	}

	switchOn(t, app, false)
	publicTheme(t, app, "/x/layouts/theme", http.StatusNotFound)
}

// The look beyond colours (D155) reaches the storefront as the shop set it:
// the starter's values before a save, then only the keys the published
// revision holds, through publish and rollback alike.
func TestTheLookSettingsPublishAndRollBack(t *testing.T) {
	app := layoutsApp(t)
	raw, _ := json.Marshal(Starter("home").ThemeSettings)
	var starter map[string]any
	if err := json.Unmarshal(raw, &starter); err != nil {
		t.Fatal(err)
	}
	if got, _ := publicTheme(t, app, "/x/layouts/theme", http.StatusOK); !reflect.DeepEqual(got.Settings, starter) {
		t.Fatalf("theme before a save = %v\nwant the starter's %v", got.Settings, starter)
	}

	first := map[string]any{"card_vendor": false, "pdp_low_stock_at": float64(0), "list_columns_desktop": "four"}
	doc := sections(hero("a", "One"))
	doc["theme_settings"] = first
	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 0, "document": doc}, http.StatusOK)
	adminState(t, app, http.MethodPost, "/api/admin/x/layouts/home/publish", map[string]any{"version": 1}, http.StatusOK)
	if got, _ := publicTheme(t, app, "/x/layouts/theme", http.StatusOK); got.Revision != 1 || !reflect.DeepEqual(got.Settings, first) {
		t.Fatalf("published theme = %+v, want exactly %v: nothing unset sent", got, first)
	}

	second := map[string]any{"type_scale": "large", "pdp_size_guide": "/size-guide"}
	doc["theme_settings"] = second
	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 1, "document": doc}, http.StatusOK)
	adminState(t, app, http.MethodPost, "/api/admin/x/layouts/home/publish", map[string]any{"version": 2}, http.StatusOK)
	if got, _ := publicTheme(t, app, "/x/layouts/theme", http.StatusOK); got.Revision != 2 || !reflect.DeepEqual(got.Settings, second) {
		t.Fatalf("second published theme = %+v, want exactly %v: the brand line unset again", got, second)
	}

	adminState(t, app, http.MethodPost, "/api/admin/x/layouts/home/rollback", map[string]any{"revision": 1}, http.StatusOK)
	if got, _ := publicTheme(t, app, "/x/layouts/theme", http.StatusOK); got.Revision != 3 || !reflect.DeepEqual(got.Settings, first) {
		t.Fatalf("theme after rolling back = %+v, want revision 3 with %v", got, first)
	}
}

// A collection list shows the collections still there, in the order picked,
// each with what a card needs.
func TestACollectionListShowsItsCollections(t *testing.T) {
	app := layoutsApp(t)
	ctx := context.Background()
	rings, err := app.Collections().Create(ctx, gocommerce.CollectionInput{Title: "Rings", Metadata: gocommerce.Metadata{"image": "/media/rings.jpg"}})
	if err != nil {
		t.Fatal(err)
	}
	chains, err := app.Collections().Create(ctx, gocommerce.CollectionInput{Title: "Chains"})
	if err != nil {
		t.Fatal(err)
	}
	doc := sections(map[string]any{"id": "cl", "type": "collection-list", "enabled": true,
		"settings": map[string]any{"collections": []int64{chains.ID, 999999, rings.ID}}})
	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 0, "document": doc}, http.StatusOK)
	adminState(t, app, http.MethodPost, "/api/admin/x/layouts/home/publish", map[string]any{"version": 1}, http.StatusOK)

	got := publicPage(t, app, "/x/layouts/home", http.StatusOK)
	cs := got.Sections[0].Collections
	if len(cs) != 2 || cs[0].ID != chains.ID || cs[1].ID != rings.ID {
		t.Fatalf("collections = %+v, want chains then rings, the deleted one skipped", cs)
	}
	if cs[1].Title != "Rings" || cs[1].Slug != rings.Slug || cs[1].Image != "/media/rings.jpg" || cs[0].Image != "" {
		t.Fatalf("collection cards = %+v", cs)
	}
}

// newsletterStub stands in for ext/newsletter: the plugin the section works
// through, and nothing else.
type newsletterStub struct{}

func (newsletterStub) Name() string                       { return "newsletter-stub" }
func (newsletterStub) Migrations() []gocommerce.Migration { return nil }
func (newsletterStub) Register(app *gocommerce.App) error {
	app.RegisterPlugin(gocommerce.PluginDef{Key: "newsletter", Title: "Newsletter", Icon: "ri-plug-line", Category: "marketing", DefaultEnabled: true})
	return nil
}

func registryTypes(t *testing.T, app *gocommerce.App) map[string]bool {
	t.Helper()
	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/layouts/sections", nil)
	var reg struct {
		Sections      []SectionType  `json:"sections"`
		ThemeSettings []SettingGroup `json:"theme_settings"`
	}
	gctest.DecodeData(t, rec, &reg)
	if len(reg.ThemeSettings) == 0 {
		t.Fatalf("registry = %s, want the theme settings described", rec.Body)
	}
	out := map[string]bool{}
	for _, s := range reg.Sections {
		out[s.Type] = true
	}
	return out
}

// The newsletter sign-up is offered only where there is a newsletter to sign
// up to, and a shopper is never shown a box that posts nowhere.
func TestTheNewsletterSectionNeedsTheNewsletter(t *testing.T) {
	without := layoutsApp(t)
	if registryTypes(t, without)["newsletter"] {
		t.Fatal("the newsletter section is offered on a store without the newsletter")
	}

	app := gctest.New(t, New(Config{}), newsletterStub{})
	switchOn(t, app, true)
	if !registryTypes(t, app)["newsletter"] {
		t.Fatal("the newsletter section is not offered while the newsletter is on")
	}
	doc := sections(hero("a", "One"), map[string]any{"id": "n", "type": "newsletter", "enabled": true, "settings": map[string]any{}})
	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 0, "document": doc}, http.StatusOK)
	adminState(t, app, http.MethodPost, "/api/admin/x/layouts/home/publish", map[string]any{"version": 1}, http.StatusOK)
	if got := publicPage(t, app, "/x/layouts/home", http.StatusOK); ids(got) != "a,n" {
		t.Fatalf("with the newsletter on = %s, want a,n", ids(got))
	}

	if rec := gctest.AdminRequest(t, app, http.MethodPatch, "/api/admin/plugins/newsletter", map[string]any{"enabled": false}); rec.Code != http.StatusOK {
		t.Fatalf("switch the newsletter off = %d %s", rec.Code, rec.Body)
	}
	if registryTypes(t, app)["newsletter"] {
		t.Fatal("the newsletter section is offered while the newsletter is off")
	}
	if got := publicPage(t, app, "/x/layouts/home", http.StatusOK); ids(got) != "a" {
		t.Fatalf("with the newsletter off = %s, want only a", ids(got))
	}
	// The draft that holds it still saves: switching a plugin off loses nothing.
	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 1, "document": doc}, http.StatusOK)
}
