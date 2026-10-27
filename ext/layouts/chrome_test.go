package layouts

import (
	"net/http"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// menusStub stands in for ext/navigation: the plugin the header's and the
// footer's menu settings work through.
type menusStub struct{}

func (menusStub) Name() string                       { return "menus-stub" }
func (menusStub) Migrations() []gocommerce.Migration { return nil }
func (menusStub) Register(app *gocommerce.App) error {
	app.RegisterPlugin(gocommerce.PluginDef{Key: "navigation", Title: "Menus", Icon: "ri-plug-line", Category: "storefront", DefaultEnabled: true})
	return nil
}

func chromeSection(id, typ string, settings map[string]any) map[string]any {
	return map[string]any{"id": id, "type": typ, "enabled": true, "settings": settings}
}

// The header and the footer are pages of their own (D131): each one section,
// the storefront's own header or footer, whose settings the shop edits, drafts,
// previews and publishes like any other page, and which the storefront draws
// on every page.
func TestTheHeaderAndFooterArePublishedLikeAPage(t *testing.T) {
	app := gctest.New(t, New(Config{}), menusStub{})
	switchOn(t, app, true)

	// Before the first save each is the storefront's own, setting for setting.
	head := adminState(t, app, http.MethodGet, "/api/admin/x/layouts/header", nil, http.StatusOK)
	if head.Saved || len(head.Document.Sections) != 1 || head.Document.Sections[0].Type != "header" {
		t.Fatalf("the header's starter = %+v, want the one header section", head.Document.Sections)
	}
	hs := head.Document.Sections[0].Settings
	if hs["show_announcement"] != true || hs["announcement_pages"] != "home" || hs["menu"] != "header" || hs["announcement_text"] == nil {
		t.Fatalf("the header's starter settings = %v, want today's strip on the home page and the header menu", hs)
	}
	foot := adminState(t, app, http.MethodGet, "/api/admin/x/layouts/footer", nil, http.StatusOK)
	if len(foot.Document.Sections) != 1 || foot.Document.Sections[0].Type != "footer" || foot.Document.Sections[0].Settings["menu"] != "footer" {
		t.Fatalf("the footer's starter = %+v, want the one footer section on the footer menu", foot.Document.Sections)
	}
	publicPage(t, app, "/x/layouts/header", http.StatusNotFound)
	publicPage(t, app, "/x/layouts/footer", http.StatusNotFound)

	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/footer", map[string]any{"version": 0, "document": sections(chromeSection("footer", "footer", map[string]any{
		"menu": "shop-links", "about": "  Small-batch goods.  ", "copyright": "© The shop",
		"social": []any{map[string]any{"network": "instagram", "url": "https://instagram.com/example"}},
	}))}, http.StatusOK)
	adminState(t, app, http.MethodPost, "/api/admin/x/layouts/footer/publish", map[string]any{"version": 1}, http.StatusOK)

	got := publicPage(t, app, "/x/layouts/footer", http.StatusOK)
	if got.Page != "footer" || len(got.Sections) != 1 || got.Sections[0].Type != "footer" {
		t.Fatalf("published footer = %+v", got)
	}
	s := got.Sections[0].Settings
	if s["menu"] != "shop-links" || s["about"] != "Small-batch goods." || s["copyright"] != "© The shop" {
		t.Fatalf("published footer settings = %v", s)
	}
	if social, _ := s["social"].([]any); len(social) != 1 {
		t.Fatalf("published footer social links = %v, want the one", s["social"])
	}
	// Each is its own document: the footer's publish publishes no header.
	publicPage(t, app, "/x/layouts/header", http.StatusNotFound)

	// Read on every page of the storefront, so it may be cached briefly.
	rec := gctest.Request(t, app, http.MethodGet, "/x/layouts/footer", nil)
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age=30") {
		t.Fatalf("footer Cache-Control = %q, want a short public cache", cc)
	}
}

// A header or footer holds its own section and nothing else: a hero in the
// header is not a header, and a second footer is not a footer either.
func TestTheHeaderAndFooterHoldOnlyThemselves(t *testing.T) {
	app := layoutsApp(t)
	refused(t, app, "header", sections(chromeSection("h", "header", map[string]any{}), hero("a", "Big")), "header")
	refused(t, app, "header", sections(), "Header")
	refused(t, app, "footer", sections(chromeSection("f", "footer", map[string]any{}), chromeSection("g", "footer", map[string]any{})), "once")
	refused(t, app, "home", sections(chromeSection("h", "header", map[string]any{})), "home page")
	refused(t, app, "footer", sections(chromeSection("h", "header", map[string]any{})), "footer")
}

// Merchant input stays plain values: links pass the banners' rule, a social
// link is a web address, a menu is named by its handle, and nothing becomes
// markup.
func TestHeaderAndFooterSettingsAreChecked(t *testing.T) {
	app := layoutsApp(t)
	header := func(s map[string]any) map[string]any { return sections(chromeSection("h", "header", s)) }
	footer := func(s map[string]any) map[string]any { return sections(chromeSection("f", "footer", s)) }

	refused(t, app, "header", header(map[string]any{"announcement_link": "javascript:alert(1)"}), "web address")
	refused(t, app, "header", header(map[string]any{"menu": "Main Menu!"}), "menu")
	refused(t, app, "header", header(map[string]any{"announcement_pages": "some"}), "choices")
	refused(t, app, "footer", footer(map[string]any{"social": []any{map[string]any{"network": "instagram", "url": "/instagram"}}}), "web address")
	refused(t, app, "footer", footer(map[string]any{"social": []any{map[string]any{"network": "myspace", "url": "https://example.com"}}}), "choices")
	refused(t, app, "footer", footer(map[string]any{"about": strings.Repeat("a", 601)}), "600")

	// The strip on with nothing to say is a mistake the shop should hear of.
	refused(t, app, "header", header(map[string]any{"show_announcement": true}), "announcement")
	st := adminState(t, app, http.MethodPut, "/api/admin/x/layouts/header", map[string]any{"version": 0, "document": header(map[string]any{
		"show_announcement": false, "menu": "main-menu",
	})}, http.StatusOK)
	if st.Document.Sections[0].Settings["show_announcement"] != false {
		t.Fatalf("header settings = %v", st.Document.Sections[0].Settings)
	}
}

// A setting that works through a plugin — the footer's newsletter box, the
// menus — is offered only while that plugin is on, and the storefront is not
// sent it otherwise; a draft that holds one still saves.
func TestChromeSettingsFollowTheirPlugins(t *testing.T) {
	app := gctest.New(t, New(Config{}), newsletterStub{})
	switchOn(t, app, true)
	fieldsOf := func(typ string) map[string]bool {
		t.Helper()
		rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/layouts/sections", nil)
		var reg struct {
			Sections []SectionType `json:"sections"`
			Pages    []PageInfo    `json:"pages"`
		}
		gctest.DecodeData(t, rec, &reg)
		out := map[string]bool{}
		for _, s := range reg.Sections {
			if s.Type == typ {
				for _, f := range s.Fields {
					out[f.Key] = true
				}
			}
		}
		return out
	}
	if f := fieldsOf("footer"); !f["newsletter"] || f["menu"] {
		t.Fatalf("footer fields with the newsletter on and no menus module = %v, want the newsletter and no menu", f)
	}

	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/footer", map[string]any{"version": 0, "document": sections(chromeSection("f", "footer", map[string]any{
		"newsletter": true, "menu": "footer", "copyright": "© Shop",
	}))}, http.StatusOK)
	adminState(t, app, http.MethodPost, "/api/admin/x/layouts/footer/publish", map[string]any{"version": 1}, http.StatusOK)
	s := publicPage(t, app, "/x/layouts/footer", http.StatusOK).Sections[0].Settings
	if s["newsletter"] != true || s["menu"] != nil || s["copyright"] != "© Shop" {
		t.Fatalf("published footer = %v, want the newsletter shown and no menu (there are no menus here)", s)
	}

	if rec := gctest.AdminRequest(t, app, http.MethodPatch, "/api/admin/plugins/newsletter", map[string]any{"enabled": false}); rec.Code != http.StatusOK {
		t.Fatalf("switch the newsletter off = %d %s", rec.Code, rec.Body)
	}
	if fieldsOf("footer")["newsletter"] {
		t.Fatal("the footer offers a newsletter box while the newsletter is off")
	}
	if s := publicPage(t, app, "/x/layouts/footer", http.StatusOK).Sections[0].Settings; s["newsletter"] != nil {
		t.Fatalf("published footer with the newsletter off = %v, want no newsletter", s)
	}
}

// A preview key for the header opens the header's draft, and nothing else's.
func TestAHeaderPreviewOpensTheHeaderOnly(t *testing.T) {
	app := layoutsApp(t)
	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/header", map[string]any{"version": 0, "document": sections(chromeSection("h", "header", map[string]any{
		"show_announcement": true, "announcement_text": "Draft strip", "announcement_pages": "all",
	}))}, http.StatusOK)
	rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/layouts/header/preview", map[string]any{})
	var key struct {
		Token string `json:"token"`
	}
	gctest.DecodeData(t, rec, &key)
	got := publicPage(t, app, "/x/layouts/header?preview="+key.Token, http.StatusOK)
	if !got.Preview || got.Sections[0].Settings["announcement_text"] != "Draft strip" {
		t.Fatalf("header preview = %+v", got)
	}
	publicPage(t, app, "/x/layouts/footer?preview="+key.Token, http.StatusNotFound)
	publicPage(t, app, "/x/layouts/home?preview="+key.Token, http.StatusNotFound)
}
