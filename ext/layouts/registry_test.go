package layouts

import (
	"encoding/json"
	"fmt"
	"reflect"
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
	got, err := Validate("home", doc(t, `{"sections":[
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
	got, err := Validate("home", doc(t, `{"sections":[]}`))
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
		if _, err := Validate("home", doc(t, c.raw)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to say %q", name, err, c.want)
		}
	}
}

// Settings the type does not have are dropped, not stored: a document holds
// only what a section can show.
func TestUnknownSettingsAreDropped(t *testing.T) {
	got, err := Validate("home", doc(t, `{"sections":[{"id":"a","type":"rich-text","settings":{"body":"Hi","style":"color:red"}}]}`))
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
	if _, err := Validate("home", doc(t, b.String())); err == nil || !strings.Contains(err.Error(), "at most 50") {
		t.Fatalf("51 sections: err = %v", err)
	}
}

// ---------------------------------------------------------------- Phase 3 (D127)

// Theme settings are the store's design tokens: colours as #rrggbb only,
// fonts from the storefront's list, rounding from its steps. Unset ones are
// left out, so the storefront keeps its own look for them.
func TestThemeSettingsAreCleaned(t *testing.T) {
	got, err := Validate("home", doc(t, `{"theme_settings":{"background":"#FFFFFF","accent":" #1a2b3c ","heading_font":"playfair-display","radius":"large","text":"","shadow":"0 0 9px red"},"sections":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	ts := got.ThemeSettings
	if ts["background"] != "#ffffff" || ts["accent"] != "#1a2b3c" || ts["heading_font"] != "playfair-display" || ts["radius"] != "large" {
		t.Fatalf("theme settings = %v, want lower-case colours and the font and rounding kept", ts)
	}
	if _, ok := ts["text"]; ok {
		t.Fatalf("theme settings = %v, want an empty colour left out", ts)
	}
	if _, ok := ts["shadow"]; ok {
		t.Fatalf("theme settings = %v, want a setting the theme does not have dropped", ts)
	}
	pics, err := Validate("home", doc(t, `{"theme_settings":{"product_image_ratio":"square","product_image_fit":"fill"},"sections":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if pics.ThemeSettings["product_image_ratio"] != "square" || pics.ThemeSettings["product_image_fit"] != "fill" {
		t.Fatalf("theme settings = %v, want the picture shape and fit kept", pics.ThemeSettings)
	}
	if got, _ := Validate("home", doc(t, `{"sections":[]}`)); got.ThemeSettings != nil {
		t.Fatalf("no theme settings = %v, want none stored", got.ThemeSettings)
	}
}

func TestWhatThemeSettingsMayNotHold(t *testing.T) {
	for name, c := range map[string]struct{ raw, want string }{
		"named colour":  {`{"theme_settings":{"background":"red"}}`, "#rrggbb"},
		"short hex":     {`{"theme_settings":{"text":"#fff"}}`, "#rrggbb"},
		"css injection": {`{"theme_settings":{"accent":"#000000;}body{display:none"}}`, "#rrggbb"},
		"unknown font":  {`{"theme_settings":{"body_font":"comic-sans"}}`, "one of its choices"},
		"font family":   {`{"theme_settings":{"body_font":"Inter, sans-serif"}}`, "one of its choices"},
		"bad rounding":  {`{"theme_settings":{"radius":"huge"}}`, "one of its choices"},
		"ratio as css":  {`{"theme_settings":{"product_image_ratio":"1 / 1"}}`, "one of its choices"},
		"fit as css":    {`{"theme_settings":{"product_image_fit":"cover"}}`, "one of its choices"},
		"sale named":    {`{"theme_settings":{"sale":"green"}}`, "Savings and offers must be a colour written #rrggbb"},
		"scale as css":  {`{"theme_settings":{"type_scale":"15px"}}`, "Text size must be one of its choices"},
		"weight number": {`{"theme_settings":{"heading_weight":"800"}}`, "Heading weight must be one of its choices"},
		"lines number":  {`{"theme_settings":{"card_title_lines":2}}`, "Title must be text"},
		"switch word":   {`{"theme_settings":{"card_vendor":"yes"}}`, "Brand line must be on or off"},
		"stock over":    {`{"theme_settings":{"pdp_low_stock_at":21}}`, "must be between 0 and 20"},
		"stock under":   {`{"theme_settings":{"pdp_low_stock_at":-1}}`, "must be between 0 and 20"},
		"stock part":    {`{"theme_settings":{"pdp_low_stock_at":2.5}}`, "must be a whole number"},
		"guide address": {`{"theme_settings":{"pdp_size_guide":"https://example.com/size-guide"}}`, "Size guide page must be a page on the store, starting with /"},
		"guide bare":    {`{"theme_settings":{"pdp_size_guide":"size-guide"}}`, "starting with /"},
		"guide offsite": {`{"theme_settings":{"pdp_size_guide":"//evil.example/size-guide"}}`, "starting with /"},
		"guide script":  {`{"theme_settings":{"pdp_size_guide":"javascript:alert(1)"}}`, "starting with /"},
		"guide spaced":  {`{"theme_settings":{"pdp_size_guide":"/size guide"}}`, "starting with /"},
	} {
		if _, err := Validate("home", doc(t, c.raw)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to say %q", name, err, c.want)
		}
	}
}

// Every theme setting refuses a value of the wrong shape for its kind, naming
// itself: none of them reaches the storefront unchecked.
func TestEveryThemeSettingRefusesAWrongValue(t *testing.T) {
	bad := map[string]any{KindColor: "#12345", KindSelect: "not-a-choice", KindBool: "on", KindNumber: 999, KindPath: "https://example.com/"}
	for _, f := range themeSettingFields() {
		v, ok := bad[f.Kind]
		if !ok {
			t.Fatalf("%s: no wrong value for kind %q", f.Key, f.Kind)
		}
		_, err := Validate("home", Document{ThemeSettings: map[string]any{f.Key: v}})
		if err == nil || !strings.Contains(err.Error(), f.Label) {
			t.Errorf("%s = %v: err = %v, want a refusal naming %q", f.Key, v, err, f.Label)
		}
	}
}

// The look beyond colours (D155) is kept as sent, a switch off and a count of
// 0 included, and a key the shop did not send stays unset, so the storefront
// keeps its own value for it.
func TestTheLookSettingsAreKeptAsSent(t *testing.T) {
	got, err := Validate("home", doc(t, `{"theme_settings":{"sale":"#198837","type_scale":"large","heading_weight":"semibold",
		"density":"spacious","card_badge":"first_highlight","card_vendor":false,"card_title_lines":"two","card_rating":false,
		"card_discount":true,"card_wishlist":false,"card_add_to_cart":true,"card_second_image":false,"list_columns_mobile":"one",
		"list_columns_desktop":"three","list_subcategories":false,"list_quick_filters":true,"list_filters_desktop":"drawer",
		"list_mobile_bar":false,"pdp_sticky_buy":false,"pdp_gallery":"grid","pdp_highlights_grid":false,"pdp_delivery_check":true,
		"pdp_low_stock_at":0,"pdp_size_guide":" /size-guide "},"sections":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"sale": "#198837", "type_scale": "large", "heading_weight": "semibold",
		"density": "spacious", "card_badge": "first_highlight", "card_vendor": false, "card_title_lines": "two", "card_rating": false,
		"card_discount": true, "card_wishlist": false, "card_add_to_cart": true, "card_second_image": false, "list_columns_mobile": "one",
		"list_columns_desktop": "three", "list_subcategories": false, "list_quick_filters": true, "list_filters_desktop": "drawer",
		"list_mobile_bar": false, "pdp_sticky_buy": false, "pdp_gallery": "grid", "pdp_highlights_grid": false, "pdp_delivery_check": true,
		"pdp_low_stock_at": 0, "pdp_size_guide": "/size-guide"}
	if !reflect.DeepEqual(got.ThemeSettings, want) {
		t.Fatalf("theme settings = %v\nwant %v", got.ThemeSettings, want)
	}

	for _, v := range []string{"none", "product_badge", "first_tag", "first_highlight"} {
		got, err := Validate("home", Document{ThemeSettings: map[string]any{"card_badge": v}})
		if err != nil || got.ThemeSettings["card_badge"] != v {
			t.Errorf("card_badge %q: kept %v, err %v", v, got.ThemeSettings, err)
		}
	}

	one, err := Validate("home", doc(t, `{"theme_settings":{"card_vendor":true},"sections":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(one.ThemeSettings, map[string]any{"card_vendor": true}) {
		t.Fatalf("theme settings = %v, want only the brand line: no other switch filled in", one.ThemeSettings)
	}

	// A section's switch still reads as its default when it is not sent.
	h, err := Validate("header", doc(t, `{"sections":[{"id":"header","type":"header","settings":{}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if s := h.Sections[0].Settings; s["show_announcement"] != false || s["hide_menu"] != false {
		t.Fatalf("header settings = %v, want both switches off", s)
	}
}

// Every new section type takes what it should and refuses what it should not.
func TestTheNewSectionTypes(t *testing.T) {
	got, err := Validate("home", doc(t, `{"sections":[
		{"id":"s","type":"slideshow","enabled":true,"settings":{"slides":[{"image":"/media/a.jpg","heading":"One","button_link":"/products"},{"image":"https://cdn.example/b.jpg"}]}},
		{"id":"c","type":"collection-list","enabled":true,"settings":{"collections":[4,2,4]}},
		{"id":"v","type":"video","enabled":true,"settings":{"video_url":"https://youtu.be/dQw4w9WgXcQ"}},
		{"id":"f","type":"video","enabled":true,"settings":{"video_file":"/media/clip.mp4","poster":"/media/clip.jpg"}},
		{"id":"n","type":"newsletter","enabled":true,"settings":{"heading":"Letters"}},
		{"id":"q","type":"faq","enabled":true,"settings":{"items":[{"question":"Do you ship abroad?","answer":"Yes.\n\nTo most countries."}]}},
		{"id":"m","type":"multi-column","enabled":true,"settings":{"items":[{"heading":"Made here"},{"heading":"Kept long","link":"/about"}]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	s := got.Sections
	if s[0].Settings["autoplay"] != false || s[0].Settings["interval"] != 5 {
		t.Errorf("slideshow = %v, want autoplay off and five seconds by default", s[0].Settings)
	}
	if ids := s[1].Settings["collections"].([]int64); len(ids) != 2 || ids[0] != 4 || ids[1] != 2 {
		t.Errorf("collections = %v, want [4 2]: picked order, each once", ids)
	}
	if s[4].Settings["button_text"] != "Subscribe" {
		t.Errorf("newsletter = %v, want the button's default label", s[4].Settings)
	}
	if s[5].Settings["items"].([]map[string]any)[0]["answer"] != "Yes.\n\nTo most countries." {
		t.Errorf("faq = %v, want the answer's paragraphs kept", s[5].Settings)
	}

	for name, c := range map[string]struct{ raw, want string }{
		"slide without picture": {`{"sections":[{"id":"a","type":"slideshow","settings":{"slides":[{"heading":"H"}]}}]}`, "Picture is required"},
		"no slides":             {`{"sections":[{"id":"a","type":"slideshow","settings":{}}]}`, "needs at least one item"},
		"too many slides":       {`{"sections":[{"id":"a","type":"slideshow","settings":{"slides":[` + strings.TrimSuffix(strings.Repeat(`{"image":"/m.jpg"},`, 9), ",") + `]}}]}`, "at most 8"},
		"slide script link":     {`{"sections":[{"id":"a","type":"slideshow","settings":{"slides":[{"image":"/m.jpg","button_link":"javascript:x"}]}}]}`, "web address"},
		"slow slides":           {`{"sections":[{"id":"a","type":"slideshow","settings":{"interval":60,"slides":[{"image":"/m.jpg"}]}}]}`, "between 3 and 10"},
		"no collections":        {`{"sections":[{"id":"a","type":"collection-list","settings":{}}]}`, "needs at least one collection"},
		"not a collection":      {`{"sections":[{"id":"a","type":"collection-list","settings":{"collections":["x"]}}]}`, "list of collections"},
		"no video":              {`{"sections":[{"id":"a","type":"video","settings":{"heading":"H"}}]}`, "needs a YouTube or Vimeo link, or a video from Files"},
		"other site":            {`{"sections":[{"id":"a","type":"video","settings":{"video_url":"https://example.com/v/1"}}]}`, "YouTube or Vimeo"},
		"youtube without id":    {`{"sections":[{"id":"a","type":"video","settings":{"video_url":"https://www.youtube.com/"}}]}`, "YouTube or Vimeo"},
		"script video file":     {`{"sections":[{"id":"a","type":"video","settings":{"video_file":"javascript:x"}}]}`, "web address"},
		"faq without answer":    {`{"sections":[{"id":"a","type":"faq","settings":{"items":[{"question":"Q"}]}}]}`, "Answer is required"},
		"five columns":          {`{"sections":[{"id":"a","type":"multi-column","settings":{"items":[{"heading":"1"},{"heading":"2"},{"heading":"3"},{"heading":"4"},{"heading":"5"}]}}]}`, "at most 4"},
		"column script link":    {`{"sections":[{"id":"a","type":"multi-column","settings":{"items":[{"heading":"1","link":"javascript:x"}]}}]}`, "web address"},
	} {
		if _, err := Validate("home", doc(t, c.raw)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to say %q", name, err, c.want)
		}
	}
}

func TestVideoLinksBecomeEmbeds(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=10": "youtube:dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ":                     "youtube:dQw4w9WgXcQ",
		"https://m.youtube.com/shorts/dQw4w9WgXcQ":         "youtube:dQw4w9WgXcQ",
		"https://www.youtube.com/embed/dQw4w9WgXcQ":        "youtube:dQw4w9WgXcQ",
		"https://vimeo.com/76979871":                       "vimeo:76979871",
		"https://player.vimeo.com/video/76979871":          "vimeo:76979871",
		"https://www.youtube.com/watch?v=bad id":           "",
		"https://evil.example/youtube.com/watch?v=x":       "",
		"http://vimeo.com/abc":                             "",
	} {
		p, id := VideoEmbed(in)
		got := ""
		if p != "" {
			got = p + ":" + id
		}
		if got != want {
			t.Errorf("VideoEmbed(%q) = %q, want %q", in, got, want)
		}
	}
}

// A benefit carries a picture from a fixed set, by a name the storefront
// draws its own way (D139). A saved strip from before icons has none and
// still saves; a name outside the set is refused, not drawn as nothing.
func TestABenefitPicksItsIconFromTheSet(t *testing.T) {
	got, err := Validate("home", doc(t, `{"sections":[{"id":"a","type":"benefits","settings":{"items":[
		{"title":"Fast delivery","icon":"truck"},{"title":"No icon"},{"title":"Blank icon","icon":""}]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	items := got.Sections[0].Settings["items"].([]map[string]any)
	if items[0]["icon"] != "truck" {
		t.Fatalf("item 1 = %v, want the truck kept", items[0])
	}
	for _, it := range items[1:] {
		if _, ok := it["icon"]; ok {
			t.Fatalf("item %v, want no icon stored", it)
		}
	}
	_, err = Validate("home", doc(t, `{"sections":[{"id":"a","type":"benefits","settings":{"items":[{"title":"T","icon":"ri-truck-line"}]}}]}`))
	if err == nil || !strings.Contains(err.Error(), "Icon must be one of its choices") {
		t.Fatalf("unknown icon: err = %v, want it refused", err)
	}
}

// The starter strip has a picture on each promise, and every icon in the set
// has a name the panel can show.
func TestTheStarterBenefitsHaveIcons(t *testing.T) {
	d, err := Validate("home", Starter("home"))
	if err != nil {
		t.Fatal(err)
	}
	var icons []any
	for _, s := range d.Sections {
		if s.Type == "benefits" {
			for _, it := range s.Settings["items"].([]map[string]any) {
				icons = append(icons, it["icon"])
			}
		}
	}
	if want := []any{"truck", "return", "shield", "headset"}; fmt.Sprint(icons) != fmt.Sprint(want) {
		t.Fatalf("starter icons = %v, want %v", icons, want)
	}
	if len(BenefitIcons) < 12 {
		t.Fatalf("%d icons, want a useful set", len(BenefitIcons))
	}
	for _, o := range BenefitIcons {
		if o.Value == "" || o.Label == "" || strings.ContainsAny(o.Value, " _") {
			t.Fatalf("icon %+v, want a lowercase-dashed name and a label", o)
		}
	}
}
