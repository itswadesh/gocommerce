package cms

import (
	"net/http"
	"strings"
	"testing"

	"github.com/misiki/gocommerce/core"
	"github.com/misiki/gocommerce/gctest"
)

func TestPageLifecycle(t *testing.T) {
	app := gctest.New(t, New(Config{}))

	// Create as a draft.
	rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/cms/pages", map[string]any{
		"slug": "about", "title": "About us", "body": "We sell things.",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body)
	}
	var created Page
	gctest.DecodeData(t, rec, &created)
	if created.Status != StatusDraft {
		t.Errorf("status = %q, want draft", created.Status)
	}
	if created.PublishedAt != nil {
		t.Error("a draft should have no publication date")
	}

	// A draft is invisible to shoppers.
	if rec := gctest.Request(t, app, http.MethodGet, "/x/cms/pages/about", nil); rec.Code != http.StatusNotFound {
		t.Errorf("public status for a draft = %d, want 404", rec.Code)
	}

	// Publish it.
	rec = gctest.AdminRequest(t, app, http.MethodPatch,
		"/api/admin/x/cms/pages/"+itoa(created.ID), map[string]any{"status": StatusPublished})
	if rec.Code != http.StatusOK {
		t.Fatalf("publish status = %d: %s", rec.Code, rec.Body)
	}
	var published Page
	gctest.DecodeData(t, rec, &published)
	if published.PublishedAt == nil {
		t.Error("publishing should record when it happened")
	}

	// Now it is public, without a token of any kind.
	rec = gctest.Request(t, app, http.MethodGet, "/x/cms/pages/about", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("public status = %d: %s", rec.Code, rec.Body)
	}
	var page Page
	gctest.DecodeData(t, rec, &page)
	if page.Title != "About us" || page.Body != "We sell things." {
		t.Errorf("page = %+v", page)
	}

	// Delete it.
	rec = gctest.AdminRequest(t, app, http.MethodDelete,
		"/api/admin/x/cms/pages/"+itoa(created.ID), nil)
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete status = %d: %s", rec.Code, rec.Body)
	}
	if rec := gctest.Request(t, app, http.MethodGet, "/x/cms/pages/about", nil); rec.Code != http.StatusNotFound {
		t.Errorf("status after delete = %d, want 404", rec.Code)
	}
}

func TestSlugIsUniquePerLanguage(t *testing.T) {
	app := gctest.New(t, New(Config{}))

	create := func(slug, lang string) int {
		body := map[string]any{"slug": slug, "title": "T", "status": StatusPublished}
		if lang != "" {
			body["language"] = lang
		}
		return gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/cms/pages", body).Code
	}

	if code := create("terms", "en"); code != http.StatusCreated {
		t.Fatalf("first create = %d", code)
	}
	// The same slug in another language is a translation, not a collision.
	if code := create("terms", "fr"); code != http.StatusCreated {
		t.Errorf("same slug in another language = %d, want 201", code)
	}
	// The same slug in the same language is a collision.
	if code := create("terms", "en"); code != http.StatusConflict {
		t.Errorf("duplicate slug and language = %d, want 409", code)
	}
}

// TestFallsBackToTheStoreLanguage: a shopper whose browser asks for a
// translation nobody has written should still get the page.
func TestFallsBackToTheStoreLanguage(t *testing.T) {
	app := gctest.NewWithConfig(t, gocommerce.Config{Languages: []string{"en", "fr"}}, New(Config{}))

	rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/cms/pages", map[string]any{
		"slug": "delivery", "title": "Delivery", "status": StatusPublished, "language": "en",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body)
	}

	// ?lang= is how a client asks for a translation explicitly.
	rec = gctest.Request(t, app, http.MethodGet, "/x/cms/pages/delivery?lang=fr", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the English page rather than a 404: %s", rec.Code, rec.Body)
	}
	var page Page
	gctest.DecodeData(t, rec, &page)
	if page.Language != "en" {
		t.Errorf("language = %q, want the en fallback", page.Language)
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}

// TestAdminPageRoutesRequireCatalogRights proves the read/write split landed on
// the right routes, and that it is enforced at all.
//
// It has to use a session: gctest.AdminToken is the static admin credential and
// carries every right by design, so a route mounted with no rights and a route
// mounted with all of them look identical through it. Staff carries pages.read
// and not pages.write by default, which is exactly the line being drawn — the
// module brings both rights itself now rather than borrowing the catalogue's,
// so a store can hand somebody the storefront's copy without its prices.
func TestAdminPageRoutesRequireTheirOwnRights(t *testing.T) {
	app := gctest.New(t, New(Config{}))

	staff := gctest.OperatorToken(t, app, "staff@example.com", gocommerce.RoleStaff)
	owner := gctest.OperatorToken(t, app, "owner@example.com", gocommerce.RoleOwner)

	if rec := gctest.SessionRequest(t, app, staff, http.MethodGet, "/api/admin/x/cms/pages", nil); rec.Code != http.StatusOK {
		t.Errorf("staff listing pages = %d, want 200: %s", rec.Code, rec.Body)
	}

	page := map[string]any{"slug": "about", "title": "About us"}
	rec := gctest.SessionRequest(t, app, staff, http.MethodPost, "/api/admin/x/cms/pages", page)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("staff creating a page = %d, want 403: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "pages.write") {
		t.Errorf("the refusal does not name the missing right: %s", rec.Body)
	}

	rec = gctest.SessionRequest(t, app, owner, http.MethodPost, "/api/admin/x/cms/pages", page)
	if rec.Code != http.StatusCreated {
		t.Fatalf("owner creating a page = %d, want 201: %s", rec.Code, rec.Body)
	}
	var created Page
	gctest.DecodeData(t, rec, &created)
	target := "/api/admin/x/cms/pages/" + itoa(created.ID)

	if rec := gctest.SessionRequest(t, app, staff, http.MethodGet, target, nil); rec.Code != http.StatusOK {
		t.Errorf("staff reading a page = %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := gctest.SessionRequest(t, app, staff, http.MethodPatch, target, map[string]any{"title": "Mine now"}); rec.Code != http.StatusForbidden {
		t.Errorf("staff editing a page = %d, want 403: %s", rec.Code, rec.Body)
	}
	if rec := gctest.SessionRequest(t, app, staff, http.MethodDelete, target, nil); rec.Code != http.StatusForbidden {
		t.Errorf("staff deleting a page = %d, want 403: %s", rec.Code, rec.Body)
	}
	if rec := gctest.SessionRequest(t, app, owner, http.MethodPatch, target, map[string]any{"title": "About"}); rec.Code != http.StatusOK {
		t.Errorf("owner editing a page = %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := gctest.SessionRequest(t, app, owner, http.MethodDelete, target, nil); rec.Code != http.StatusNoContent {
		t.Errorf("owner deleting a page = %d, want 204: %s", rec.Code, rec.Body)
	}
}

// Renaming a page onto a slug another page already holds is the same collision
// as creating one there, and used to answer 500 "internal error": the driver
// error went to RespondError unrecognised, which wrapped it and blanked the
// message. TestSlugIsUniquePerLanguage covers only the create path.
func TestRenamingASlugOntoAnotherPageIs409(t *testing.T) {
	app := gctest.New(t, New(Config{}))

	create := func(slug string) int64 {
		t.Helper()
		rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/cms/pages",
			map[string]any{"slug": slug, "title": slug})
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %s = %d: %s", slug, rec.Code, rec.Body)
		}
		var p Page
		gctest.DecodeData(t, rec, &p)
		return p.ID
	}
	create("about")
	terms := create("terms")

	rec := gctest.AdminRequest(t, app, http.MethodPatch,
		"/api/admin/x/cms/pages/"+itoa(terms), map[string]any{"slug": "about"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("renaming onto an existing slug = %d, want 409: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"conflict"`) {
		t.Errorf("the error code is not conflict: %s", rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "about") {
		t.Errorf("the message does not say which slug: %s", rec.Body)
	}
}

// The status filter goes straight into a WHERE, so an unrecognised value used
// to return an empty 200 — a contract promising an enum nothing enforces.
func TestListStatusFilterIsValidated(t *testing.T) {
	app := gctest.New(t, New(Config{}))

	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/cms/pages?status=bogus", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown status = %d, want 400: %s", rec.Code, rec.Body)
	}
	for _, status := range []string{"", "draft", "published"} {
		if rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/cms/pages?status="+status, nil); rec.Code != http.StatusOK {
			t.Errorf("status=%q = %d, want 200: %s", status, rec.Code, rec.Body)
		}
	}
}

// TestModuleContract is the pair every module should have: no admin route open
// to a role holding nothing, and the served routes and the fragment agreeing in
// both directions.
func TestModuleContract(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	gctest.AssertAdminRoutesDeclareRights(t, app, "cms")
	gctest.AssertSpecCoversModuleRoutes(t, app, "cms")
}
