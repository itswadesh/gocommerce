package layouts

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

func core(id, typ string) map[string]any {
	return map[string]any{"id": id, "type": typ, "enabled": true, "settings": map[string]any{}}
}

func text(id, body string) map[string]any {
	return map[string]any{"id": id, "type": "rich-text", "enabled": true, "settings": map[string]any{"body": body}}
}

func refused(t *testing.T, app *gocommerce.App, page string, doc map[string]any, want string) {
	t.Helper()
	rec := gctest.AdminRequest(t, app, http.MethodPut, "/api/admin/x/layouts/"+page, map[string]any{"version": 0, "document": doc})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("PUT %s = %d %s, want 400 saying %q", page, rec.Code, rec.Body, want)
	}
}

// The product page is a template every product is drawn in: the catalogue's
// own core — pictures, title, price, the buy box — is one section that is
// always there, exactly once, and the shop arranges the rest above and below
// it.
func TestTheProductPageIsArrangedAroundItsCore(t *testing.T) {
	app := layoutsApp(t)

	st := adminState(t, app, http.MethodGet, "/api/admin/x/layouts/product", nil, http.StatusOK)
	var types []string
	for _, s := range st.Document.Sections {
		types = append(types, s.Type)
	}
	if st.Saved || strings.Join(types, ",") != "main-product,cross-sells,product-reviews,related-products" {
		t.Fatalf("the product page's starter = %v, want the storefront's own page: core, its suggestions, reviews, related", types)
	}
	publicPage(t, app, "/x/layouts/product", http.StatusNotFound)

	refused(t, app, "product", sections(hero("a", "Above")), "Product")
	refused(t, app, "product", sections(core("m", "main-product"), core("n", "main-product")), "once")
	refused(t, app, "home", sections(core("m", "main-product")), "home page")
	refused(t, app, "home", sections(core("r", "product-reviews")), "home page")
	refused(t, app, "home", sections(core("x", "cross-sells")), "home page")
	refused(t, app, "collection", sections(core("m", "main-product"), core("c", "main-collection")), "collection page")

	hidden := core("m", "main-product")
	hidden["enabled"] = false
	doc := sections(hero("a", "Above"), hidden, core("r", "product-reviews"), text("b", "Below"))
	doc["theme_settings"] = map[string]any{"accent": "#112233"}
	st = adminState(t, app, http.MethodPut, "/api/admin/x/layouts/product", map[string]any{"version": 0, "document": doc}, http.StatusOK)
	if !st.Document.Sections[1].Enabled {
		t.Fatal("the core cannot be hidden: a product page without its product is no product page")
	}
	if st.Document.ThemeSettings != nil {
		t.Fatalf("theme settings on the product page = %v, want none: they publish with the home page (D127)", st.Document.ThemeSettings)
	}
	adminState(t, app, http.MethodPost, "/api/admin/x/layouts/product/publish", map[string]any{"version": 1}, http.StatusOK)

	got := publicPage(t, app, "/x/layouts/product", http.StatusOK)
	if ids(got) != "a,m,r,b" || got.Page != "product" || got.Revision != 1 {
		t.Fatalf("published product page = %s (%s, revision %d), want a,m,r,b", ids(got), got.Page, got.Revision)
	}
	// Each page is its own document: publishing the product page publishes
	// nothing else.
	publicPage(t, app, "/x/layouts/home", http.StatusNotFound)
	publicPage(t, app, "/x/layouts/collection", http.StatusNotFound)
}

func TestTheCollectionPageIsArrangedAroundItsGrid(t *testing.T) {
	app := layoutsApp(t)
	st := adminState(t, app, http.MethodGet, "/api/admin/x/layouts/collection", nil, http.StatusOK)
	if len(st.Document.Sections) != 1 || st.Document.Sections[0].Type != "main-collection" {
		t.Fatalf("the collection page's starter = %+v, want the grid alone", st.Document.Sections)
	}
	refused(t, app, "collection", sections(text("b", "Below")), "Products")
	refused(t, app, "collection", sections(core("c", "main-collection"), core("r", "related-products")), "collection page")

	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/collection", map[string]any{"version": 0, "document": sections(
		map[string]any{"id": "bar", "type": "announcement-bar", "enabled": true, "settings": map[string]any{"text": "Sale"}},
		core("c", "main-collection"), text("b", "Below"))}, http.StatusOK)
	adminState(t, app, http.MethodPost, "/api/admin/x/layouts/collection/publish", map[string]any{"version": 1}, http.StatusOK)
	if got := publicPage(t, app, "/x/layouts/collection", http.StatusOK); ids(got) != "bar,c,b" {
		t.Fatalf("published collection page = %s, want bar,c,b", ids(got))
	}
}

// The registry says which page each type belongs on and which one is the
// fixed core, so the panel offers only what a page can hold.
func TestTheRegistryNamesEachTypesPages(t *testing.T) {
	app := layoutsApp(t)
	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/layouts/sections", nil)
	var reg struct {
		Sections []SectionType `json:"sections"`
		Pages    []PageInfo    `json:"pages"`
	}
	gctest.DecodeData(t, rec, &reg)
	byType := map[string]SectionType{}
	for _, s := range reg.Sections {
		byType[s.Type] = s
	}
	if c := byType["main-product"]; !c.Fixed || strings.Join(c.Pages, ",") != "product" {
		t.Fatalf("main-product = %+v, want fixed, on the product page only", c)
	}
	if h := byType["hero"]; h.Fixed || len(h.Pages) != 0 {
		t.Fatalf("hero = %+v, want every page", h)
	}
	if len(reg.Pages) != 5 || reg.Pages[0].Key != "home" || reg.Pages[1].Core != "main-product" || reg.Pages[2].Core != "main-collection" ||
		!reg.Pages[3].CoreOnly || reg.Pages[3].Core != "header" || !reg.Pages[4].CoreOnly || reg.Pages[4].Core != "footer" {
		t.Fatalf("pages = %+v", reg.Pages)
	}
	for _, p := range Pages {
		if _, err := Validate(p.Key, Starter(p.Key)); err != nil {
			t.Fatalf("the %s page's starter does not validate: %v", p.Key, err)
		}
	}
}

func scheduleBody(version int, at time.Time) map[string]any {
	return map[string]any{"version": version, "publish_at": at.UTC().Format(time.RFC3339)}
}

// dueNow moves every schedule into the past, as the clock would.
func dueNow(t *testing.T, app *gocommerce.App) {
	t.Helper()
	if err := app.ForEachStore(context.Background(), func(ctx context.Context) error {
		_, err := app.DB().ExecContext(ctx, `UPDATE layout_schedules SET publish_at = now() - interval '1 second'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func publishDue(t *testing.T, app *gocommerce.App, m *Module) int {
	t.Helper()
	total := 0
	if err := app.ForEachStore(context.Background(), func(ctx context.Context) error {
		n, err := m.PublishDue(ctx)
		total += n
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return total
}

// A schedule publishes the draft as it was when it was scheduled, at its
// time, through the same publish as the button: a revision like any other,
// marked as scheduled. Edits made since stay in the draft.
func TestAScheduledDraftPublishesAtItsTime(t *testing.T) {
	m := New(Config{})
	app := gctest.New(t, m)
	switchOn(t, app, true)

	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/product", map[string]any{"version": 0, "document": sections(
		hero("sale", "Sale"), core("m", "main-product"))}, http.StatusOK)
	at := time.Now().Add(time.Hour).Truncate(time.Second)
	st := adminState(t, app, http.MethodPost, "/api/admin/x/layouts/product/schedule", scheduleBody(1, at), http.StatusOK)
	if st.Scheduled == nil || !st.Scheduled.PublishAt.Equal(at) || st.Scheduled.Version != 1 || st.Scheduled.Outdated {
		t.Fatalf("after scheduling = %+v, want the version-1 draft at %s", st.Scheduled, at)
	}

	st = adminState(t, app, http.MethodPut, "/api/admin/x/layouts/product", map[string]any{"version": 1, "document": sections(
		hero("sale", "Changed later"), core("m", "main-product"))}, http.StatusOK)
	if st.Scheduled == nil || !st.Scheduled.Outdated {
		t.Fatalf("after a later edit = %+v, want the schedule kept and marked as not holding it", st.Scheduled)
	}

	if n := publishDue(t, app, m); n != 0 {
		t.Fatalf("published %d before the time", n)
	}
	publicPage(t, app, "/x/layouts/product", http.StatusNotFound)

	dueNow(t, app)
	if n := publishDue(t, app, m); n != 1 {
		t.Fatalf("published %d at the time, want 1", n)
	}
	got := publicPage(t, app, "/x/layouts/product", http.StatusOK)
	if got.Revision != 1 || got.Sections[0].Settings["heading"] != "Sale" {
		t.Fatalf("published = %+v, want the draft as scheduled, not the later edit", got.Sections)
	}
	if n := publishDue(t, app, m); n != 0 {
		t.Fatalf("published %d more on the next pass, want none: a schedule runs once", n)
	}

	st = adminState(t, app, http.MethodGet, "/api/admin/x/layouts/product", nil, http.StatusOK)
	if st.Scheduled != nil || st.Published == nil || !st.Published.Scheduled || !st.Changed {
		t.Fatalf("state after = %+v, want no schedule, a scheduled revision 1, and the later edit still a change", st)
	}
	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/layouts/product/revisions", nil)
	var revs []Revision
	gctest.DecodeData(t, rec, &revs)
	if len(revs) != 1 || !revs[0].Scheduled || revs[0].PublishedBy == "" {
		t.Fatalf("revisions = %+v, want one, scheduled, naming who scheduled it", revs)
	}
}

func TestAScheduleCanBeCancelledAndIsChecked(t *testing.T) {
	m := New(Config{})
	app := gctest.New(t, m)
	switchOn(t, app, true)
	later := time.Now().Add(2 * time.Hour)

	if rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/layouts/home/schedule", scheduleBody(0, later)); rec.Code != http.StatusConflict {
		t.Fatalf("scheduling an unsaved page = %d %s, want 409", rec.Code, rec.Body)
	}
	adminState(t, app, http.MethodPut, "/api/admin/x/layouts/home", map[string]any{"version": 0, "document": sections(hero("a", "One"))}, http.StatusOK)
	if rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/layouts/home/schedule", scheduleBody(1, time.Now().Add(-time.Minute))); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "future") {
		t.Fatalf("a time gone by = %d %s, want 400", rec.Code, rec.Body)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/layouts/home/schedule", scheduleBody(1, time.Now().AddDate(2, 0, 0))); rec.Code != http.StatusBadRequest {
		t.Fatalf("two years ahead = %d %s, want 400", rec.Code, rec.Body)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/layouts/home/schedule", scheduleBody(7, later)); rec.Code != http.StatusConflict {
		t.Fatalf("a stale version = %d %s, want 409", rec.Code, rec.Body)
	}

	adminState(t, app, http.MethodPost, "/api/admin/x/layouts/home/schedule", scheduleBody(1, later), http.StatusOK)
	// Scheduling again replaces the one there was: a page has one.
	st := adminState(t, app, http.MethodPost, "/api/admin/x/layouts/home/schedule", scheduleBody(1, later.Add(time.Hour)), http.StatusOK)
	if st.Scheduled == nil || !st.Scheduled.PublishAt.Equal(later.Add(time.Hour).Truncate(time.Second)) {
		t.Fatalf("rescheduled = %+v", st.Scheduled)
	}
	st = adminState(t, app, http.MethodDelete, "/api/admin/x/layouts/home/schedule", nil, http.StatusOK)
	if st.Scheduled != nil {
		t.Fatalf("after cancelling = %+v, want none", st.Scheduled)
	}
	dueNow(t, app)
	if n := publishDue(t, app, m); n != 0 {
		t.Fatalf("a cancelled schedule published %d", n)
	}
	publicPage(t, app, "/x/layouts/home", http.StatusNotFound)
	// Cancelling what is not there is already done.
	adminState(t, app, http.MethodDelete, "/api/admin/x/layouts/home/schedule", nil, http.StatusOK)
}
