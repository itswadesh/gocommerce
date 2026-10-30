package gocommerce

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func masterListApp(t *testing.T) *App {
	t.Helper()
	app := newTestApp(t)
	for _, r := range app.Routes() {
		if r.Path == "/api/admin/hsn" {
			return app
		}
	}
	app.mountMasterListRoutes()
	return app
}

// The embedded master loads, and the codes a jewellery or clothing store
// reaches for first are there by number and by word.
func TestHSNMaster(t *testing.T) {
	m := hsnData()
	if len(m.codes) < 20000 {
		t.Fatalf("master has %d codes, want the full list (~22,600)", len(m.codes))
	}
	c, ok := LookupHSN("7113")
	if !ok || !strings.HasPrefix(c.Description, "ARTICLES OF JEWELLERY") || c.Service {
		t.Fatalf("7113 = %+v, %v", c, ok)
	}
	if c, ok := LookupHSN("6109.10.00"); !ok || !strings.Contains(c.Heading, "T-SHIRTS") {
		t.Errorf("6109.10.00 = %+v, %v; want its T-shirt heading", c, ok)
	}
	if c, ok := LookupHSN("996331"); !ok || !c.Service {
		t.Errorf("996331 = %+v, %v; want a SAC code", c, ok)
	}
	if _, ok := LookupHSN("jewellery"); ok {
		t.Error("a word looked up as a code")
	}

	list, total := SearchHSN("71", 5, 0)
	if total < 5 || len(list) != 5 || list[0].Code != "71" {
		t.Errorf("prefix 71: %d matches, first %+v; want chapter 71 first", total, list)
	}
	list, _ = SearchHSN("t-shirts", 100, 0)
	if !hasCode(list, "6109") {
		t.Errorf("t-shirts did not find 6109: %+v", list)
	}
	list, _ = SearchHSN("Jewellery precious", 100, 0)
	if !hasCode(list, "7113") {
		t.Errorf("jewellery precious did not find 7113: %+v", list)
	}
	if list, total := SearchHSN("", 10, 0); len(list) != 0 || total != 0 {
		t.Errorf("empty query matched %d", total)
	}
}

func hasCode(list []HSNCode, code string) bool {
	for _, c := range list {
		if c.Code == code {
			return true
		}
	}
	return false
}

func TestMasterListRoutes(t *testing.T) {
	app := masterListApp(t)

	rec := do(t, app, http.MethodGet, "/api/admin/gst-states", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("gst-states: %d %s", rec.Code, rec.Body)
	}
	var states struct{ Data []GSTState }
	if err := json.Unmarshal(rec.Body.Bytes(), &states); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range states.Data {
		if s.Code == "23" {
			found = s.Name == "Madhya Pradesh" && s.ISO == "MP"
		}
	}
	if !found || len(states.Data) != len(gstStates) {
		t.Errorf("gst-states: 23 is not Madhya Pradesh in %+v", states.Data)
	}

	rec = do(t, app, http.MethodGet, "/api/admin/hsn?q=jewellery&limit=50", withAdmin)
	var page struct {
		Data []HSNCode
		Meta ListMeta
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("hsn search: %d %s", rec.Code, rec.Body)
	}
	if !hasCode(page.Data, "7113") || page.Meta.Total < len(page.Data) {
		t.Errorf("hsn search for jewellery: %+v", page)
	}

	rec = do(t, app, http.MethodGet, "/api/admin/hsn/6109", withAdmin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "T-SHIRTS") {
		t.Errorf("hsn 6109: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, app, http.MethodGet, "/api/admin/hsn/123456", withAdmin); rec.Code != http.StatusNotFound {
		t.Errorf("unknown code: %d, want 404", rec.Code)
	}
	if rec := do(t, app, http.MethodGet, "/api/admin/hsn?q=7113"); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous search: %d, want 401", rec.Code)
	}
}
