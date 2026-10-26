package layouts

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Field kinds a section's setting can be. The panel draws a control for each,
// and the engine checks a value against it, so adding a kind is adding both.
const (
	KindText       = "text"       // one line
	KindTextarea   = "textarea"   // paragraphs; plain text, never HTML
	KindURL        = "url"        // a path on the storefront or a web address
	KindImage      = "image"      // a picture's address, from the Files library or pasted
	KindNumber     = "number"     // a whole number between Min and Max
	KindSelect     = "select"     // one of Options
	KindBool       = "bool"       // a switch
	KindProducts   = "products"   // product ids, in the shop's order
	KindCollection = "collection" // one collection's id
	KindList       = "list"       // repeated items, each with Fields
)

// Option is one choice of a select field.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Field is one setting of a section type.
type Field struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Kind     string   `json:"kind"`
	Help     string   `json:"help,omitempty"`
	Required bool     `json:"required,omitempty"`
	Default  any      `json:"default,omitempty"`
	Options  []Option `json:"options,omitempty"`
	// MaxLen bounds a text; Min and Max a number; MaxItems a list or a
	// product pick.
	MaxLen   int `json:"max_length,omitempty"`
	Min      int `json:"min,omitempty"`
	Max      int `json:"max,omitempty"`
	MaxItems int `json:"max_items,omitempty"`
	// ShowWhen shows the field only while another setting of the section
	// has this value, so a product grid asks for a collection or for
	// products, not both. It never relaxes validation of what is sent.
	ShowWhen map[string]string `json:"show_when,omitempty"`
	// Fields are a list item's settings.
	Fields []Field `json:"fields,omitempty"`
}

// SectionType is a kind of section a page can hold, any number of times, at
// any position.
type SectionType struct {
	Type        string  `json:"type"`
	Label       string  `json:"label"`
	Description string  `json:"description"`
	Icon        string  `json:"icon"`
	Fields      []Field `json:"fields"`
}

// Theme is a look the storefront draws a page in. Phase 1 has one; the page's
// content never belongs to a theme, so changing it later loses nothing.
type Theme struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Themes are the looks a page may name.
var Themes = []Theme{{ID: "default", Label: "Default"}}

const (
	maxShort = 200
	maxLong  = 5000
)

// Sections is the registry: the panel's section library and the engine's
// validation both read it, so a type added here is offered everywhere.
var Sections = []SectionType{
	{
		Type: "announcement-bar", Label: "Announcement bar", Icon: "ri-megaphone-line",
		Description: "A line of text across the top of the page, optionally a link.",
		Fields: []Field{
			{Key: "text", Label: "Text", Kind: KindText, Required: true, MaxLen: maxShort},
			{Key: "link", Label: "Link", Kind: KindURL, Help: "Where the bar goes when tapped. Leave empty for none."},
		},
	},
	{
		Type: "hero", Label: "Hero", Icon: "ri-image-2-line",
		Description: "A large picture with a heading, a line of text and a button.",
		Fields: []Field{
			{Key: "eyebrow", Label: "Small line above the heading", Kind: KindText, MaxLen: 80},
			{Key: "heading", Label: "Heading", Kind: KindText, Required: true, MaxLen: maxShort},
			{Key: "body", Label: "Text", Kind: KindTextarea, MaxLen: 600},
			{Key: "image", Label: "Picture", Kind: KindImage, Help: "Wide, for computers."},
			{Key: "mobile_image", Label: "Picture on phones", Kind: KindImage, Help: "Taller crop. Empty uses the picture above."},
			{Key: "button_text", Label: "Button text", Kind: KindText, MaxLen: 60},
			{Key: "button_link", Label: "Button link", Kind: KindURL},
			{Key: "align", Label: "Text position", Kind: KindSelect, Default: "left",
				Options: []Option{{"left", "Left"}, {"center", "Centre"}}},
		},
	},
	{
		Type: "product-grid", Label: "Product grid", Icon: "ri-layout-grid-line",
		Description: "Products from a collection, or ones you pick, with live prices and stock.",
		Fields: []Field{
			{Key: "heading", Label: "Heading", Kind: KindText, MaxLen: maxShort},
			{Key: "source", Label: "Products from", Kind: KindSelect, Default: "collection",
				Options: []Option{{"collection", "A collection"}, {"products", "Products I pick"}}},
			{Key: "collection", Label: "Collection", Kind: KindCollection, ShowWhen: map[string]string{"source": "collection"}},
			{Key: "products", Label: "Products", Kind: KindProducts, MaxItems: 24, ShowWhen: map[string]string{"source": "products"}},
			{Key: "limit", Label: "How many to show", Kind: KindNumber, Default: 8, Min: 1, Max: 24},
			{Key: "columns", Label: "Across on a computer", Kind: KindNumber, Default: 4, Min: 2, Max: 4},
			{Key: "view_all_link", Label: "\"View all\" link", Kind: KindURL},
		},
	},
	{
		Type: "image-with-text", Label: "Image with text", Icon: "ri-layout-left-line",
		Description: "A picture beside a heading, text and a button.",
		Fields: []Field{
			{Key: "image", Label: "Picture", Kind: KindImage},
			{Key: "image_side", Label: "Picture on the", Kind: KindSelect, Default: "left",
				Options: []Option{{"left", "Left"}, {"right", "Right"}}},
			{Key: "eyebrow", Label: "Small line above the heading", Kind: KindText, MaxLen: 80},
			{Key: "heading", Label: "Heading", Kind: KindText, MaxLen: maxShort},
			{Key: "body", Label: "Text", Kind: KindTextarea, MaxLen: 2000},
			{Key: "button_text", Label: "Button text", Kind: KindText, MaxLen: 60},
			{Key: "button_link", Label: "Button link", Kind: KindURL},
		},
	},
	{
		Type: "rich-text", Label: "Text", Icon: "ri-text",
		Description: "A heading and paragraphs of text.",
		Fields: []Field{
			{Key: "heading", Label: "Heading", Kind: KindText, MaxLen: maxShort},
			{Key: "body", Label: "Text", Kind: KindTextarea, MaxLen: maxLong, Help: "A blank line starts a new paragraph."},
			{Key: "align", Label: "Alignment", Kind: KindSelect, Default: "center",
				Options: []Option{{"left", "Left"}, {"center", "Centre"}}},
		},
	},
	{
		Type: "benefits", Label: "Benefits strip", Icon: "ri-shield-check-line",
		Description: "Short promises in a row: shipping, returns, secure checkout.",
		Fields: []Field{
			{Key: "heading", Label: "Heading", Kind: KindText, MaxLen: maxShort},
			{Key: "items", Label: "Benefits", Kind: KindList, Required: true, MaxItems: 6, Fields: []Field{
				{Key: "title", Label: "Title", Kind: KindText, Required: true, MaxLen: 80},
				{Key: "body", Label: "Line under it", Kind: KindText, MaxLen: maxShort},
			}},
		},
	},
}

// Limits that keep a page a page: past these a document is malformed or
// abusive rather than ambitious.
const (
	MaxSections = 50
	MaxDocBytes = 256 << 10
)

// Section is one section of a page.
type Section struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Enabled  bool           `json:"enabled"`
	Settings map[string]any `json:"settings"`
}

// Document is a page: its sections, top to bottom.
type Document struct {
	SchemaVersion int       `json:"schema_version"`
	Theme         string    `json:"theme"`
	Sections      []Section `json:"sections"`
}

const schemaVersion = 1

var sectionIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

func sectionType(t string) *SectionType {
	for i := range Sections {
		if Sections[i].Type == t {
			return &Sections[i]
		}
	}
	return nil
}

// Validate checks a document against the registry and returns it cleaned:
// unknown settings dropped, text trimmed, defaults filled, numbers whole. An
// error names the section and setting, for the editor to point at.
func Validate(doc Document) (Document, error) {
	if len(doc.Sections) > MaxSections {
		return doc, gocommerce.Validationf("a page holds at most %d sections", MaxSections)
	}
	out := Document{SchemaVersion: schemaVersion, Theme: strings.TrimSpace(doc.Theme), Sections: []Section{}}
	if out.Theme == "" {
		out.Theme = Themes[0].ID
	}
	if !knownTheme(out.Theme) {
		return doc, gocommerce.Validationf("there is no theme %q", out.Theme)
	}
	seen := map[string]bool{}
	for i, s := range doc.Sections {
		where := fmt.Sprintf("section %d", i+1)
		if !sectionIDRE.MatchString(s.ID) {
			return doc, gocommerce.Validationf("%s: its id must be 1–40 letters, digits, - or _", where)
		}
		if seen[s.ID] {
			return doc, gocommerce.Validationf("%s: the id %q is used twice", where, s.ID)
		}
		seen[s.ID] = true
		st := sectionType(s.Type)
		if st == nil {
			return doc, gocommerce.Validationf("%s: there is no section type %q", where, s.Type)
		}
		where = fmt.Sprintf("section %d (%s)", i+1, st.Label)
		settings, err := cleanFields(where, st.Fields, s.Settings)
		if err != nil {
			return doc, err
		}
		out.Sections = append(out.Sections, Section{ID: s.ID, Type: s.Type, Enabled: s.Enabled, Settings: settings})
	}
	return out, nil
}

func knownTheme(id string) bool {
	for _, t := range Themes {
		if t.ID == id {
			return true
		}
	}
	return false
}

func cleanFields(where string, fields []Field, in map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for _, f := range fields {
		v, err := cleanField(where, f, in[f.Key])
		if err != nil {
			return nil, err
		}
		if v != nil {
			out[f.Key] = v
		}
	}
	return out, nil
}

func cleanField(where string, f Field, v any) (any, error) {
	bad := func(format string, args ...any) error {
		return gocommerce.Validationf("%s: %s %s", where, f.Label, fmt.Sprintf(format, args...))
	}
	if v == nil && f.Default != nil {
		v = f.Default
	}
	switch f.Kind {
	case KindText, KindTextarea, KindURL, KindImage, KindSelect:
		s, ok := v.(string)
		if v != nil && !ok {
			return nil, bad("must be text")
		}
		s = strings.TrimSpace(s)
		if f.Kind == KindText {
			// One line: a newline pasted into a heading is a mistake.
			s = strings.Join(strings.Fields(s), " ")
		}
		if s == "" {
			if f.Required {
				return nil, bad("is required")
			}
			return nil, nil
		}
		if f.MaxLen > 0 && len([]rune(s)) > f.MaxLen {
			return nil, bad("is longer than %d characters", f.MaxLen)
		}
		switch f.Kind {
		case KindURL, KindImage:
			if len(s) > 2048 || !safeURL(s) {
				return nil, bad("must be a web address (https://…) or a path on the store (/…)")
			}
		case KindSelect:
			if !hasOption(f.Options, s) {
				return nil, bad("must be one of its choices")
			}
		}
		return s, nil
	case KindNumber, KindCollection:
		if v == nil {
			if f.Required {
				return nil, bad("is required")
			}
			return nil, nil
		}
		n, ok := v.(float64)
		if i, isInt := v.(int); isInt {
			n, ok = float64(i), true
		}
		if !ok || n != math.Trunc(n) {
			return nil, bad("must be a whole number")
		}
		if f.Kind == KindCollection {
			if n <= 0 {
				return nil, bad("must be a collection")
			}
			return int64(n), nil
		}
		if n < float64(f.Min) || n > float64(f.Max) {
			return nil, bad("must be between %d and %d", f.Min, f.Max)
		}
		return int(n), nil
	case KindBool:
		if v == nil {
			return false, nil
		}
		b, ok := v.(bool)
		if !ok {
			return nil, bad("must be on or off")
		}
		return b, nil
	case KindProducts:
		list, ok := v.([]any)
		if v != nil && !ok {
			return nil, bad("must be a list of products")
		}
		if len(list) == 0 {
			if f.Required {
				return nil, bad("needs at least one product")
			}
			return nil, nil
		}
		if f.MaxItems > 0 && len(list) > f.MaxItems {
			return nil, bad("holds at most %d", f.MaxItems)
		}
		ids := make([]int64, 0, len(list))
		seen := map[int64]bool{}
		for _, x := range list {
			n, ok := x.(float64)
			if !ok || n <= 0 || n != math.Trunc(n) {
				return nil, bad("must be a list of products")
			}
			if !seen[int64(n)] {
				seen[int64(n)] = true
				ids = append(ids, int64(n))
			}
		}
		return ids, nil
	case KindList:
		list, ok := v.([]any)
		if v != nil && !ok {
			return nil, bad("must be a list")
		}
		if len(list) == 0 {
			if f.Required {
				return nil, bad("needs at least one item")
			}
			return nil, nil
		}
		if f.MaxItems > 0 && len(list) > f.MaxItems {
			return nil, bad("holds at most %d", f.MaxItems)
		}
		items := make([]map[string]any, 0, len(list))
		for i, x := range list {
			m, ok := x.(map[string]any)
			if !ok {
				return nil, bad("item %d is malformed", i+1)
			}
			item, err := cleanFields(fmt.Sprintf("%s, %s %d", where, f.Label, i+1), f.Fields, m)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	}
	return nil, bad("has a kind the engine does not know")
}

func hasOption(opts []Option, v string) bool {
	for _, o := range opts {
		if o.Value == v {
			return true
		}
	}
	return false
}

// safeURL accepts a web address or a path on this site, and nothing a
// browser would run: the rule banners keep (D99), so a link a section carries
// cannot be javascript:, data:, or another site dressed as a path (//host).
func safeURL(s string) bool {
	if strings.IndexFunc(s, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
		return false
	}
	if strings.HasPrefix(s, "/") {
		return !strings.HasPrefix(s, "//") && !strings.HasPrefix(s, `/\`)
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}
