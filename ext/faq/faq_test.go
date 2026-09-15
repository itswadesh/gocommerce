package faq

import (
	"net/http"
	"strconv"
	"testing"

	gocommerce "github.com/misiki/gocommerce/core"
	"github.com/misiki/gocommerce/gctest"
)

// The module's reason to exist: a shop writes its answers, arranges them,
// and the storefront reads the published ones in that arrangement.
func TestEntriesAreWrittenOrderedAndPublished(t *testing.T) {
	app := gctest.New(t, New(Config{}))

	add := func(in EntryInput) *Entry {
		t.Helper()
		rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/faq", in)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %+v = %d %s", in, rec.Code, rec.Body)
		}
		var out Entry
		gctest.DecodeData(t, rec, &out)
		return &out
	}

	delivery := add(EntryInput{Question: "When will it arrive?", Answer: "Two to three days.", Section: "Delivery"})
	returns := add(EntryInput{Question: "Can I send it back?", Answer: "Within thirty days.", Section: "Returns"})
	cost := add(EntryInput{Question: "What does delivery cost?", Answer: "Free over fifty.", Section: "Delivery"})

	// Each lands last in its own section, not last overall.
	if delivery.Position != 0 || cost.Position != 1 || returns.Position != 0 {
		t.Fatalf("positions = %d, %d, %d; want each counted within its section", delivery.Position, cost.Position, returns.Position)
	}

	// A question with no answer helps nobody, and is refused.
	if rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/faq", EntryInput{Question: "Why?"}); rec.Code != http.StatusBadRequest {
		t.Errorf("an unanswered question = %d, want 400", rec.Code)
	}

	// A draft is invisible to shoppers and visible to the operator.
	draft := false
	hidden := add(EntryInput{Question: "Internal note", Answer: "Not for shoppers.", Section: "Delivery", Published: &draft})

	public := func() map[string]any {
		t.Helper()
		rec := gctest.Request(t, app, http.MethodGet, "/x/faq", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("public faq = %d %s", rec.Code, rec.Body)
		}
		var out map[string]any
		gctest.DecodeData(t, rec, &out)
		return out
	}

	page := public()
	if page["heading"] != "Frequently asked questions" {
		t.Errorf("heading = %v, want the plugin's default", page["heading"])
	}
	sections, _ := page["sections"].([]any)
	if len(sections) != 2 {
		t.Fatalf("sections = %d, want Delivery and Returns", len(sections))
	}
	first, _ := sections[0].(map[string]any)
	if first["section"] != "Delivery" {
		t.Errorf("first section = %v, want the one written first — the order is the shop's, not the alphabet's", first["section"])
	}
	entries, _ := first["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("Delivery entries = %d, want the two published ones without the draft", len(entries))
	}
	if q, _ := entries[0].(map[string]any); q["question"] != "When will it arrive?" {
		t.Errorf("first question = %v, want the one at position 0", q["question"])
	}

	// Reordering names every entry and moves one between sections.
	order := map[string]any{"entries": []map[string]any{
		{"id": cost.ID, "section": "Delivery"},
		{"id": delivery.ID, "section": "Delivery"},
		{"id": hidden.ID, "section": "Delivery"},
		{"id": returns.ID, "section": "Returns"},
	}}
	if rec := gctest.AdminRequest(t, app, http.MethodPut, "/api/admin/x/faq/order", order); rec.Code != http.StatusOK {
		t.Fatalf("reorder = %d %s", rec.Code, rec.Body)
	}
	page = public()
	sections, _ = page["sections"].([]any)
	first, _ = sections[0].(map[string]any)
	entries, _ = first["entries"].([]any)
	if q, _ := entries[0].(map[string]any); q["question"] != "What does delivery cost?" {
		t.Errorf("after the reorder the first question = %v, want the one moved to the top", q["question"])
	}

	// An order that leaves an entry out is refused whole: a half-sorted
	// list is worse than one that did not move.
	short := map[string]any{"entries": []map[string]any{{"id": cost.ID, "section": "Delivery"}}}
	if rec := gctest.AdminRequest(t, app, http.MethodPut, "/api/admin/x/faq/order", short); rec.Code != http.StatusBadRequest {
		t.Errorf("a partial order = %d, want 400", rec.Code)
	}

	// Publishing the draft brings it to the storefront without rewriting it.
	published := true
	if rec := gctest.AdminRequest(t, app, http.MethodPatch, "/api/admin/x/faq/"+strconv.FormatInt(hidden.ID, 10),
		EntryInput{Question: hidden.Question, Answer: hidden.Answer, Section: "Delivery", Published: &published}); rec.Code != http.StatusOK {
		t.Fatalf("publish = %d %s", rec.Code, rec.Body)
	}
	page = public()
	sections, _ = page["sections"].([]any)
	first, _ = sections[0].(map[string]any)
	if entries, _ = first["entries"].([]any); len(entries) != 3 {
		t.Errorf("Delivery entries after publishing = %d, want 3", len(entries))
	}

	// Deleted is gone; deleting it twice says so.
	if rec := gctest.AdminRequest(t, app, http.MethodDelete, "/api/admin/x/faq/"+strconv.FormatInt(hidden.ID, 10), nil); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodDelete, "/api/admin/x/faq/"+strconv.FormatInt(hidden.ID, 10), nil); rec.Code != http.StatusNotFound {
		t.Errorf("deleting it twice = %d, want 404", rec.Code)
	}
}

// Switched off, the storefront has no FAQ — and the operator still does,
// because the screen is where it is switched back on.
func TestThePluginGatesTheStorefrontOnly(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ctx := t.Context()

	off := false
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{Enabled: &off}); err != nil {
		t.Fatalf("switch off: %v", err)
	}
	if rec := gctest.Request(t, app, http.MethodGet, "/x/faq", nil); rec.Code != http.StatusNotFound {
		t.Errorf("public faq while off = %d, want 404", rec.Code)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/faq", nil); rec.Code != http.StatusOK {
		t.Errorf("admin faq while off = %d, want 200", rec.Code)
	}
}

func TestRoutesDeclareRightsAndAreDocumented(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	gctest.AssertAdminRoutesDeclareRights(t, app, "faq")
	gctest.AssertSpecCoversModuleRoutes(t, app, "faq")
}
