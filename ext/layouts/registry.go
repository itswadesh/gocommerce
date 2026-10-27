package layouts

import (
	"errors"
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
	KindText        = "text"        // one line
	KindTextarea    = "textarea"    // paragraphs; plain text, never HTML
	KindURL         = "url"         // a path on the storefront or a web address
	KindImage       = "image"       // a picture's address, from the Files library or pasted
	KindNumber      = "number"      // a whole number between Min and Max
	KindSelect      = "select"      // one of Options
	KindBool        = "bool"        // a switch
	KindProducts    = "products"    // product ids, in the shop's order
	KindCollection  = "collection"  // one collection's id
	KindCollections = "collections" // collection ids, in the shop's order
	KindList        = "list"        // repeated items, each with Fields
	KindColor       = "color"       // a colour as #rrggbb, nothing else
	KindVideo       = "video"       // a video file's address, from the Files library or pasted
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
	// Requires names the plugin the section works through. The editor offers
	// the section only while that plugin is on, and the storefront is not
	// sent it otherwise; a draft that holds one still saves.
	Requires string `json:"requires,omitempty"`
	// Check is a rule across fields, run on the cleaned settings.
	Check func(settings map[string]any) error `json:"-"`
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
	{
		Type: "slideshow", Label: "Slideshow", Icon: "ri-slideshow-3-line",
		Description: "Several pictures, one at a time, each with its own heading, text and button.",
		Fields: []Field{
			{Key: "slides", Label: "Slides", Kind: KindList, Required: true, MaxItems: 8, Fields: []Field{
				{Key: "image", Label: "Picture", Kind: KindImage, Required: true, Help: "Wide, for computers."},
				{Key: "mobile_image", Label: "Picture on phones", Kind: KindImage, Help: "Taller crop. Empty uses the picture above."},
				{Key: "heading", Label: "Heading", Kind: KindText, MaxLen: maxShort},
				{Key: "text", Label: "Text", Kind: KindTextarea, MaxLen: 300},
				{Key: "button_text", Label: "Button text", Kind: KindText, MaxLen: 60},
				{Key: "button_link", Label: "Button link", Kind: KindURL},
			}},
			{Key: "autoplay", Label: "Change slides by themselves", Kind: KindBool, Default: false,
				Help: "Off by default. Shoppers can still pause, and a device set to reduce motion never changes them by itself."},
			{Key: "interval", Label: "Seconds per slide", Kind: KindNumber, Default: 5, Min: 3, Max: 10, ShowWhen: map[string]string{"autoplay": "true"}},
		},
	},
	{
		Type: "collection-list", Label: "Collection list", Icon: "ri-folders-line",
		Description: "Collections you pick, each with its picture and title, linking to it.",
		Fields: []Field{
			{Key: "heading", Label: "Heading", Kind: KindText, MaxLen: maxShort},
			{Key: "collections", Label: "Collections", Kind: KindCollections, Required: true, MaxItems: 12},
			{Key: "columns", Label: "Across on a computer", Kind: KindNumber, Default: 3, Min: 2, Max: 4},
		},
	},
	{
		Type: "video", Label: "Video", Icon: "ri-movie-line",
		Description: "A YouTube or Vimeo video, or one from your Files. It plays only when pressed.",
		Fields: []Field{
			{Key: "heading", Label: "Heading", Kind: KindText, MaxLen: maxShort},
			{Key: "video_url", Label: "YouTube or Vimeo link", Kind: KindURL, Help: "Loaded only when a shopper presses play."},
			{Key: "video_file", Label: "Or a video from Files", Kind: KindVideo, Help: "An MP4. Used when there is no link above."},
			{Key: "poster", Label: "Picture before it plays", Kind: KindImage},
			{Key: "caption", Label: "Line under it", Kind: KindText, MaxLen: maxShort},
		},
		Check: func(s map[string]any) error {
			link, _ := s["video_url"].(string)
			file, _ := s["video_file"].(string)
			if link == "" && file == "" {
				return errors.New("needs a YouTube or Vimeo link, or a video from Files")
			}
			if p, _ := VideoEmbed(link); link != "" && p == "" {
				return errors.New("YouTube or Vimeo link must be the address of a YouTube or Vimeo video")
			}
			return nil
		},
	},
	{
		Type: "newsletter", Label: "Newsletter sign-up", Icon: "ri-mail-add-line", Requires: "newsletter",
		Description: "An email box that adds the shopper to your newsletter list.",
		Fields: []Field{
			{Key: "heading", Label: "Heading", Kind: KindText, MaxLen: maxShort},
			{Key: "text", Label: "Text", Kind: KindTextarea, MaxLen: 600},
			{Key: "button_text", Label: "Button text", Kind: KindText, Default: "Subscribe", MaxLen: 40},
			{Key: "success_text", Label: "After signing up", Kind: KindText, MaxLen: maxShort, Help: "Empty says the address was added."},
		},
	},
	{
		Type: "faq", Label: "Questions and answers", Icon: "ri-question-answer-line",
		Description: "Questions a shopper opens one at a time to read the answer.",
		Fields: []Field{
			{Key: "heading", Label: "Heading", Kind: KindText, MaxLen: maxShort},
			{Key: "items", Label: "Questions", Kind: KindList, Required: true, MaxItems: 30, Fields: []Field{
				{Key: "question", Label: "Question", Kind: KindText, Required: true, MaxLen: maxShort},
				{Key: "answer", Label: "Answer", Kind: KindTextarea, Required: true, MaxLen: 2000, Help: "A blank line starts a new paragraph."},
			}},
		},
	},
	{
		Type: "multi-column", Label: "Columns", Icon: "ri-layout-column-line",
		Description: "Up to four columns side by side, each a picture, heading, text and link.",
		Fields: []Field{
			{Key: "heading", Label: "Heading", Kind: KindText, MaxLen: maxShort},
			{Key: "items", Label: "Columns", Kind: KindList, Required: true, MaxItems: 4, Fields: []Field{
				{Key: "image", Label: "Picture", Kind: KindImage},
				{Key: "heading", Label: "Heading", Kind: KindText, MaxLen: maxShort},
				{Key: "text", Label: "Text", Kind: KindTextarea, MaxLen: 600},
				{Key: "link_text", Label: "Link text", Kind: KindText, MaxLen: 60},
				{Key: "link", Label: "Link", Kind: KindURL},
			}},
		},
	},
}

// Fonts the storefront can load, by key: the store names one and the
// storefront maps it to the family and its Google Fonts address. None of
// them is a default; an unset font keeps the storefront's own.
var Fonts = []Option{
	{"inter", "Inter"}, {"roboto", "Roboto"}, {"poppins", "Poppins"}, {"montserrat", "Montserrat"},
	{"lato", "Lato"}, {"open-sans", "Open Sans"}, {"nunito", "Nunito"}, {"dm-sans", "DM Sans"},
	{"work-sans", "Work Sans"}, {"playfair-display", "Playfair Display"}, {"lora", "Lora"},
	{"merriweather", "Merriweather"}, {"cormorant-garamond", "Cormorant Garamond"},
	{"libre-baskerville", "Libre Baskerville"}, {"eb-garamond", "EB Garamond"},
}

// SettingGroup is a titled set of theme settings, as the editor shows them.
type SettingGroup struct {
	Label  string  `json:"label"`
	Fields []Field `json:"fields"`
}

// ThemeSettings is the store's design tokens: one set for every page the
// storefront draws (D127). Every one is optional, and an unset one keeps the
// storefront's own look, so there is no default to invent.
var ThemeSettings = []SettingGroup{
	{Label: "Colours", Fields: []Field{
		{Key: "background", Label: "Background", Kind: KindColor},
		{Key: "text", Label: "Text", Kind: KindColor},
		{Key: "accent", Label: "Buttons and links", Kind: KindColor},
		{Key: "accent_text", Label: "Text on buttons", Kind: KindColor},
		{Key: "muted", Label: "Quiet backgrounds", Kind: KindColor, Help: "Bands behind a hero or a strip, and hover fills."},
	}},
	{Label: "Typography", Fields: []Field{
		{Key: "heading_font", Label: "Headings", Kind: KindSelect, Options: Fonts},
		{Key: "body_font", Label: "Body text", Kind: KindSelect, Options: Fonts},
	}},
	{Label: "Shape", Fields: []Field{
		{Key: "radius", Label: "Corner rounding", Kind: KindSelect,
			Options: []Option{{"none", "None"}, {"small", "Small"}, {"medium", "Medium"}, {"large", "Large"}}},
	}},
	{Label: "Product pictures", Fields: []Field{
		{Key: "product_image_ratio", Label: "Shape", Kind: KindSelect, Help: "On product cards and lists.",
			Options: []Option{{"square", "Square"}, {"portrait", "Portrait (3:4)"}, {"tall", "Tall (2:3)"}, {"landscape", "Landscape (4:3)"}}},
		{Key: "product_image_fit", Label: "Fit", Kind: KindSelect, Help: "Fill crops a picture of another shape to the frame; whole picture leaves a margin instead.",
			Options: []Option{{"fill", "Fill the frame"}, {"whole", "Show the whole picture"}}},
	}},
}

func themeSettingFields() []Field {
	var out []Field
	for _, g := range ThemeSettings {
		out = append(out, g.Fields...)
	}
	return out
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
	SchemaVersion int    `json:"schema_version"`
	Theme         string `json:"theme"`
	// ThemeSettings are the store's design tokens. They ride with the home
	// page's draft so one autosave, one publish and one history cover both,
	// and every page of the storefront reads them from the published
	// revision (D127).
	ThemeSettings map[string]any `json:"theme_settings,omitempty"`
	Sections      []Section      `json:"sections"`
}

const schemaVersion = 1

var (
	sectionIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
	colorRE     = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	youTubeIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	vimeoIDRE   = regexp.MustCompile(`^[0-9]{1,12}$`)
)

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
	tokens, err := cleanFields("theme settings", themeSettingFields(), doc.ThemeSettings)
	if err != nil {
		return doc, err
	}
	if len(tokens) > 0 {
		out.ThemeSettings = tokens
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
		if st.Check != nil {
			if err := st.Check(settings); err != nil {
				return doc, gocommerce.Validationf("%s: %s", where, err.Error())
			}
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
	case KindText, KindTextarea, KindURL, KindImage, KindVideo, KindSelect, KindColor:
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
		case KindColor:
			// The value is written into a style sheet: nothing but the six
			// digits may pass.
			if !colorRE.MatchString(s) {
				return nil, bad("must be a colour written #rrggbb")
			}
			return strings.ToLower(s), nil
		case KindURL, KindImage, KindVideo:
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
	case KindProducts, KindCollections:
		what := "product"
		if f.Kind == KindCollections {
			what = "collection"
		}
		list, ok := v.([]any)
		if v != nil && !ok {
			return nil, bad("must be a list of %ss", what)
		}
		if len(list) == 0 {
			if f.Required {
				return nil, bad("needs at least one %s", what)
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
				return nil, bad("must be a list of %ss", what)
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

// VideoEmbed names the player and the video a YouTube or Vimeo address is
// for, or "" for anything else. The storefront builds the embed from these
// two alone — youtube-nocookie.com, or Vimeo with do-not-track — so the
// address a shop pastes never reaches a frame as it is.
func VideoEmbed(raw string) (provider, id string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch host {
	case "youtube.com", "m.youtube.com", "youtube-nocookie.com":
		provider = "youtube"
		switch {
		case len(parts) == 1 && parts[0] == "watch":
			id = u.Query().Get("v")
		case len(parts) == 2 && (parts[0] == "shorts" || parts[0] == "embed" || parts[0] == "live"):
			id = parts[1]
		}
	case "youtu.be":
		provider = "youtube"
		if len(parts) == 1 {
			id = parts[0]
		}
	case "vimeo.com":
		provider = "vimeo"
		if len(parts) == 1 {
			id = parts[0]
		}
	case "player.vimeo.com":
		provider = "vimeo"
		if len(parts) == 2 && parts[0] == "video" {
			id = parts[1]
		}
	default:
		return "", ""
	}
	if (provider == "youtube" && youTubeIDRE.MatchString(id)) || (provider == "vimeo" && vimeoIDRE.MatchString(id)) {
		return provider, id
	}
	return "", ""
}
