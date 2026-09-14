package gocommerce

import (
	"encoding/json"
	"net/http"
	"testing"
)

// screenModule contributes one screen, as a module would.
type screenModule struct {
	name    string
	screens []Screen
}

func (m *screenModule) Name() string            { return m.name }
func (m *screenModule) Migrations() []Migration { return nil }
func (m *screenModule) Register(app *App) error { return nil }
func (m *screenModule) Screens() []Screen       { return m.screens }

func goodScreen(slug string) Screen {
	return Screen{
		Slug: slug, Title: "Things", Right: RightCatalogRead,
		List: ScreenList{Endpoint: "/api/admin/x/demo/things"},
	}
}

// A contributed screen reaches the panel's listing, stamped with the module it
// came from rather than whatever it claimed.
func TestModuleContributesAScreen(t *testing.T) {
	app := newTestApp(t, &screenModule{name: "demo", screens: []Screen{{
		Slug: "demo-things", Title: "Things", Icon: "ri-box-3-line",
		Group: "Catalog", Right: RightCatalogRead,
		Module: "a-lie",
		List: ScreenList{
			Endpoint: "/api/admin/x/demo/things",
			Columns:  []ScreenColumn{{Key: "name", Label: "Name"}},
			Empty:    "Nothing yet.",
		},
	}}})

	rec := do(t, app, http.MethodGet, "/api/admin/screens", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("list screens = %d: %s", rec.Code, rec.Body)
	}
	var body struct{ Data []Screen }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 {
		t.Fatalf("got %d screens, want 1", len(body.Data))
	}
	got := body.Data[0]
	if got.Slug != "demo-things" || got.Title != "Things" {
		t.Errorf("screen = %+v, want the contributed one", got)
	}
	// The engine stamps the module, so a descriptor cannot claim to come from
	// somewhere it does not.
	if got.Module != "demo" {
		t.Errorf("module = %q, want the registering module rather than what the descriptor claimed", got.Module)
	}
}

// The mistakes that would otherwise surface as a blank page long after the
// typo are refused at boot instead.
func TestBadScreensAreRefusedAtBoot(t *testing.T) {
	for _, tc := range []struct {
		name   string
		screen Screen
	}{
		{"no slug", Screen{Title: "X", Right: RightCatalogRead, List: ScreenList{Endpoint: "/x"}}},
		{"bad slug", Screen{Slug: "Not A Slug", Title: "X", Right: RightCatalogRead, List: ScreenList{Endpoint: "/x"}}},
		{"no title", Screen{Slug: "s", Right: RightCatalogRead, List: ScreenList{Endpoint: "/x"}}},
		{"no right", Screen{Slug: "s", Title: "X", List: ScreenList{Endpoint: "/x"}}},
		{"no endpoint", Screen{Slug: "s", Title: "X", Right: RightCatalogRead}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(testConfig(requireDB(t)), &screenModule{
				name: "demo", screens: []Screen{tc.screen},
			}); err == nil {
				t.Error("boot succeeded; want the descriptor refused")
			}
		})
	}
}

// Two modules cannot both own one slug: one screen would shadow the other and
// the panel would serve whichever registered first.
func TestScreenSlugCollisionIsRefused(t *testing.T) {
	_, err := New(testConfig(requireDB(t)),
		&screenModule{name: "one", screens: []Screen{goodScreen("shared")}},
		&screenModule{name: "two", screens: []Screen{goodScreen("shared")}},
	)
	if err == nil {
		t.Fatal("boot succeeded with two modules claiming one slug")
	}
}

// A screen names a right and the listing honours it, so the nav never offers an
// operator a screen that would answer 403.
func TestScreensAreFilteredByRight(t *testing.T) {
	app := newTestApp(t, &screenModule{name: "demo", screens: []Screen{{
		Slug: "demo-secret", Title: "Secret", Right: RightStoreOperate,
		List: ScreenList{Endpoint: "/api/admin/x/demo/secret"},
	}}})

	// A staff operator does not carry store.operate by default (D24), so the
	// screen must not appear in their listing.
	token := signInAs(t, app, "staff@example.com", RoleStaff)
	rec := do(t, app, http.MethodGet, "/api/admin/screens", bearer(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body)
	}
	var body struct{ Data []Screen }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, s := range body.Data {
		if s.Slug == "demo-secret" {
			t.Error("a screen needing store.operate was offered to staff")
		}
	}
}
