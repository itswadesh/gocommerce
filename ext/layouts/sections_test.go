package layouts

import (
	"fmt"
	"strings"
	"testing"
)

// D243's sections are added first and filled in after, so each must save as it
// comes from the library and again as it comes back from the panel's blank
// "Add" in a list. A required setting among them would turn the editor's save
// state to "Couldn't save" until the shop had finished typing.
func TestTheNewSectionsSaveBeforeTheyAreFilledIn(t *testing.T) {
	for _, typ := range []string{"spotlight", "bento-grid", "testimonials", "stats", "logo-cloud", "scrolling-text", "countdown"} {
		st := sectionType(typ)
		if st == nil {
			t.Fatalf("%s is not in the registry", typ)
		}
		for _, page := range []string{"home", "product", "collection"} {
			if !st.allowedOn(page) {
				t.Errorf("%s: not allowed on the %s page", typ, page)
			}
		}
		raw := fmt.Sprintf(`{"sections":[{"id":"a","type":%q,"enabled":true,"settings":{}}]}`, typ)
		if _, err := Validate("home", doc(t, raw)); err != nil {
			t.Errorf("%s: a blank section did not save: %v", typ, err)
		}
		for _, f := range st.Fields {
			if f.Kind != KindList {
				continue
			}
			raw := fmt.Sprintf(`{"sections":[{"id":"a","type":%q,"enabled":true,"settings":{%q:[{}]}}]}`, typ, f.Key)
			if _, err := Validate("home", doc(t, raw)); err != nil {
				t.Errorf("%s: a blank %s did not save: %v", typ, f.Label, err)
			}
		}
	}
}

// The moment is what every shopper counts down to, wherever the editor sat
// when typing it.
func TestACountdownKeepsOneMoment(t *testing.T) {
	got, err := Validate("home", doc(t, `{"sections":[{"id":"a","type":"countdown","settings":{"ends_at":"2026-12-01T18:30:00+05:30"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if at := got.Sections[0].Settings["ends_at"]; at != "2026-12-01T13:00:00Z" {
		t.Fatalf("ends_at = %v, want it in UTC", at)
	}
	for _, bad := range []string{"tomorrow", "2026-12-01", "2026-12-01T18:30"} {
		raw := fmt.Sprintf(`{"sections":[{"id":"a","type":"countdown","settings":{"ends_at":%q}}]}`, bad)
		if _, err := Validate("home", doc(t, raw)); err == nil || !strings.Contains(err.Error(), "date and time") {
			t.Errorf("ends_at %q: err = %v, want it refused", bad, err)
		}
	}
}

// A picture is the one thing these sections send to a browser from a setting
// the shop typed, in a list or not, so each gets the rule every picture has.
func TestTheNewSectionsRefuseAnUnsafeAddress(t *testing.T) {
	for name, raw := range map[string]string{
		"spotlight picture": `{"id":"a","type":"spotlight","settings":{"image":"javascript:alert(1)"}}`,
		"bento tile link":   `{"id":"a","type":"bento-grid","settings":{"items":[{"link":"javascript:alert(1)"}]}}`,
		"customer photo":    `{"id":"a","type":"testimonials","settings":{"items":[{"image":"//evil.example/x.png"}]}}`,
		"logo link":         `{"id":"a","type":"logo-cloud","settings":{"items":[{"link":"data:text/html,x"}]}}`,
		"countdown button":  `{"id":"a","type":"countdown","settings":{"button_link":"javascript:alert(1)"}}`,
		"showcase picture":  `{"id":"a","type":"product-grid","settings":{"style":"showcase","image":"javascript:alert(1)"}}`,
	} {
		if _, err := Validate("home", doc(t, `{"sections":[`+raw+`]}`)); err == nil || !strings.Contains(err.Error(), "web address") {
			t.Errorf("%s: err = %v, want it refused", name, err)
		}
	}
}

// A choice and a limit hold in a section as anywhere else.
func TestTheNewSectionsKeepToTheirChoices(t *testing.T) {
	for name, c := range map[string]struct{ raw, want string }{
		"height":      {`{"id":"a","type":"spotlight","settings":{"height":"giant"}}`, "one of its choices"},
		"stars":       {`{"id":"a","type":"testimonials","settings":{"items":[{"rating":6}]}}`, "between 1 and 5"},
		"six tiles":   {`{"id":"a","type":"bento-grid","settings":{"items":[{},{},{},{},{},{},{}]}}`, "at most 6"},
		"four values": {`{"id":"a","type":"stats","settings":{"items":[{},{},{},{},{}]}}`, "at most 4"},
	} {
		if _, err := Validate("home", doc(t, `{"sections":[`+c.raw+`]}`)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to say %q", name, err, c.want)
		}
	}
}

// A grid saved before the style existed reads as what it always was.
func TestAProductGridSavedBeforeStylesStaysInRows(t *testing.T) {
	got, err := Validate("home", doc(t, `{"sections":[{"id":"a","type":"product-grid","settings":{"source":"newest"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if s := got.Sections[0].Settings["style"]; s != "grid" {
		t.Fatalf("style = %v, want grid", s)
	}
}
