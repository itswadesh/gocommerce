package layouts

import (
	"encoding/json"
	"strings"
	"testing"
)

func doc(t *testing.T, raw string) Document {
	t.Helper()
	var d Document
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// Any type, any number of times, in any order: the page is the list.
func TestASectionTypeRepeatsAnywhere(t *testing.T) {
	got, err := Validate(doc(t, `{"sections":[
		{"id":"a","type":"product-grid","enabled":true,"settings":{"source":"products","products":[3,1,3]}},
		{"id":"b","type":"hero","enabled":true,"settings":{"heading":"  New   season  "}},
		{"id":"c","type":"product-grid","enabled":false,"settings":{"collection":7}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sections) != 3 || got.Sections[0].ID != "a" || got.Sections[2].ID != "c" || got.Sections[2].Enabled {
		t.Fatalf("sections = %+v, want a, b, c in order with c hidden", got.Sections)
	}
	if ids := got.Sections[0].Settings["products"].([]int64); len(ids) != 2 || ids[0] != 3 || ids[1] != 1 {
		t.Fatalf("products = %v, want [3 1]: picked order, each once", ids)
	}
	if got.Sections[1].Settings["heading"] != "New season" || got.Sections[1].Settings["align"] != "left" {
		t.Fatalf("hero settings = %v, want the heading on one line and the default alignment", got.Sections[1].Settings)
	}
	if got.Sections[2].Settings["limit"] != 8 || got.Sections[2].Settings["collection"] != int64(7) {
		t.Fatalf("grid settings = %v, want the default limit and collection 7", got.Sections[2].Settings)
	}
	if got.Theme != "default" || got.SchemaVersion != 1 {
		t.Fatalf("theme %q schema %d, want default and 1", got.Theme, got.SchemaVersion)
	}
}

// An empty page is a page: the first section can be added to it.
func TestAnEmptyPageIsValid(t *testing.T) {
	got, err := Validate(doc(t, `{"sections":[]}`))
	if err != nil || len(got.Sections) != 0 {
		t.Fatalf("empty page = %+v, %v", got, err)
	}
}

func TestWhatAPageMayNotHold(t *testing.T) {
	for name, c := range map[string]struct{ raw, want string }{
		"unknown type":     {`{"sections":[{"id":"a","type":"marquee"}]}`, `no section type "marquee"`},
		"repeated id":      {`{"sections":[{"id":"a","type":"rich-text"},{"id":"a","type":"rich-text"}]}`, "used twice"},
		"bad id":           {`{"sections":[{"id":"a b","type":"rich-text"}]}`, "its id"},
		"script link":      {`{"sections":[{"id":"a","type":"announcement-bar","settings":{"text":"Hi","link":"javascript:alert(1)"}}]}`, "web address"},
		"protocol-less":    {`{"sections":[{"id":"a","type":"hero","settings":{"heading":"H","image":"//evil.example/x.png"}}]}`, "web address"},
		"missing required": {`{"sections":[{"id":"a","type":"hero","settings":{}}]}`, "Heading is required"},
		"not a choice":     {`{"sections":[{"id":"a","type":"hero","settings":{"heading":"H","align":"diagonal"}}]}`, "one of its choices"},
		"out of range":     {`{"sections":[{"id":"a","type":"product-grid","settings":{"limit":99}}]}`, "between 1 and 24"},
		"fraction":         {`{"sections":[{"id":"a","type":"product-grid","settings":{"columns":2.5}}]}`, "whole number"},
		"too long":         {`{"sections":[{"id":"a","type":"announcement-bar","settings":{"text":"` + strings.Repeat("x", 201) + `"}}]}`, "longer than 200"},
		"empty list item":  {`{"sections":[{"id":"a","type":"benefits","settings":{"items":[{"title":""}]}}]}`, "Title is required"},
		"unknown theme":    {`{"theme":"neon","sections":[]}`, `no theme "neon"`},
	} {
		if _, err := Validate(doc(t, c.raw)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to say %q", name, err, c.want)
		}
	}
}

// Settings the type does not have are dropped, not stored: a document holds
// only what a section can show.
func TestUnknownSettingsAreDropped(t *testing.T) {
	got, err := Validate(doc(t, `{"sections":[{"id":"a","type":"rich-text","settings":{"body":"Hi","style":"color:red"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Sections[0].Settings["style"]; ok {
		t.Fatalf("settings = %v, want style dropped", got.Sections[0].Settings)
	}
}

func TestAPageHasAMostFiftySections(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"sections":[`)
	for i := 0; i <= MaxSections; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"id":"s` + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + `","type":"rich-text"}`)
	}
	b.WriteString("]}")
	if _, err := Validate(doc(t, b.String())); err == nil || !strings.Contains(err.Error(), "at most 50") {
		t.Fatalf("51 sections: err = %v", err)
	}
}
