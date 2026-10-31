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
	KindMenu        = "menu"        // one of the store's menus, by its handle (Content → Menus)
	KindIcon        = "icon"        // one of Options, each a picture's name: the panel shows a grid of them (D139)
	KindPath        = "path"        // a path on the storefront (/…), never another site
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
	// Requires names the plugin the setting works through, as a section
	// type's Requires does: offered only while it is on, and left out of what
	// the storefront is sent otherwise; a draft that holds it still saves.
	Requires string `json:"requires,omitempty"`
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
	// Pages are the pages the type may be on; none means every page. A
	// product's reviews mean nothing on the home page (D129).
	Pages []string `json:"pages,omitempty"`
	// Fixed marks a page's core: the part the catalogue draws, which the page
	// holds exactly once and always shows. The shop moves it, and arranges
	// the rest around it; it has no settings to change.
	Fixed bool `json:"fixed,omitempty"`
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

var (
	eyebrowField  = Field{Key: "eyebrow", Label: "Small line above the heading", Kind: KindText, MaxLen: 80}
	subtitleField = Field{Key: "subtitle", Label: "Line under the heading", Kind: KindText, MaxLen: maxShort}
	viewAllText   = Field{Key: "view_all_text", Label: "\"View all\" link text", Kind: KindText, MaxLen: 60, Help: "Empty says \"View all\"."}
	viewAllLink   = Field{Key: "view_all_link", Label: "\"View all\" link", Kind: KindURL}
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
			eyebrowField,
			{Key: "heading", Label: "Heading", Kind: KindText, Required: true, MaxLen: maxShort},
			{Key: "body", Label: "Text", Kind: KindTextarea, MaxLen: 600},
			{Key: "image", Label: "Picture", Kind: KindImage, Help: "Wide, for computers."},
			{Key: "mobile_image", Label: "Picture on phones", Kind: KindImage, Help: "Taller crop. Empty uses the picture above."},
			{Key: "image_alt", Label: "What the picture shows", Kind: KindText, MaxLen: 200, Help: "Read aloud to people who cannot see it. Empty uses the heading."},
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
			eyebrowField,
			{Key: "heading", Label: "Heading", Kind: KindText, MaxLen: maxShort},
			subtitleField,
			{Key: "source", Label: "Products from", Kind: KindSelect, Default: "collection",
				Options: []Option{{"collection", "A collection"}, {"products", "Products I pick"}, {"newest", "The newest products"}}},
			{Key: "collection", Label: "Collection", Kind: KindCollection, ShowWhen: map[string]string{"source": "collection"}},
			{Key: "products", Label: "Products", Kind: KindProducts, MaxItems: 24, ShowWhen: map[string]string{"source": "products"}},
			{Key: "limit", Label: "How many to show", Kind: KindNumber, Default: 8, Min: 1, Max: 24},
			{Key: "columns", Label: "Across on a computer", Kind: KindNumber, Default: 4, Min: 2, Max: 4},
			viewAllText,
			viewAllLink,
		},
	},
	{
		Type: "image-with-text", Label: "Image with text", Icon: "ri-layout-left-line",
		Description: "A picture beside a heading, text and a button.",
		Fields: []Field{
			{Key: "image", Label: "Picture", Kind: KindImage},
			{Key: "image_alt", Label: "What the picture shows", Kind: KindText, MaxLen: 200, Help: "Read aloud to people who cannot see it. Empty uses the heading."},
			{Key: "image_side", Label: "Picture on the", Kind: KindSelect, Default: "left",
				Options: []Option{{"left", "Left"}, {"right", "Right"}}},
			eyebrowField,
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
			eyebrowField,
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
			eyebrowField,
			{Key: "heading", Label: "Heading", Kind: KindText, MaxLen: maxShort},
			subtitleField,
			{Key: "items", Label: "Benefits", Kind: KindList, Required: true, MaxItems: 6, Fields: []Field{
				{Key: "title", Label: "Title", Kind: KindText, Required: true, MaxLen: 80},
				{Key: "body", Label: "Line under it", Kind: KindText, MaxLen: maxShort},
				{Key: "icon", Label: "Icon", Kind: KindIcon, Options: BenefitIcons, Help: "Shown beside the title. None leaves it out."},
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
			eyebrowField,
			{Key: "heading", Label: "Heading", Kind: KindText, MaxLen: maxShort},
			subtitleField,
			{Key: "collections", Label: "Collections", Kind: KindCollections, Required: true, MaxItems: 12},
			{Key: "columns", Label: "Across on a computer", Kind: KindNumber, Default: 3, Min: 2, Max: 4},
			viewAllText,
			viewAllLink,
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
	// The store's rating and reviews from Google, read live by the storefront
	// through ext/google-reviews, which has the place, the key and Google's
	// rules; the section only says where they go.
	{
		Type: "google-reviews", Label: "Google reviews", Icon: "ri-google-line", Requires: "google-reviews", Pages: []string{"home"},
		Description: "Your Google rating and latest Google reviews, with Google's attribution and a link to all of them on Google Maps. Shows nothing until Google reviews is set up under Plugins.",
		Fields: []Field{
			{Key: "heading", Label: "Heading", Kind: KindText, MaxLen: maxShort, Help: "Empty says \"Reviews on Google\"."},
		},
	},
	{
		Type: "faq", Label: "Questions and answers", Icon: "ri-question-answer-line",
		Description: "Questions a shopper opens one at a time to read the answer.",
		Fields: []Field{
			eyebrowField,
			{Key: "heading", Label: "Heading", Kind: KindText, MaxLen: maxShort},
			subtitleField,
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
			eyebrowField,
			{Key: "heading", Label: "Heading", Kind: KindText, MaxLen: maxShort},
			subtitleField,
			{Key: "items", Label: "Columns", Kind: KindList, Required: true, MaxItems: 4, Fields: []Field{
				{Key: "image", Label: "Picture", Kind: KindImage},
				{Key: "heading", Label: "Heading", Kind: KindText, MaxLen: maxShort},
				{Key: "text", Label: "Text", Kind: KindTextarea, MaxLen: 600},
				{Key: "link_text", Label: "Link text", Kind: KindText, MaxLen: 60},
				{Key: "link", Label: "Link", Kind: KindURL},
			}},
			viewAllText,
			viewAllLink,
		},
	},

	// The product page (D129). Its core is the catalogue's: the pictures, the
	// title, the price and options, the buy button and the description. The
	// two below it on the storefront's own page are sections, so the shop can
	// move them, hide them, or put its own between.
	{
		Type: "main-product", Label: "Product", Icon: "ri-shopping-bag-3-line", Pages: []string{"product"}, Fixed: true,
		Description: "The product's pictures, title, price, options, buy button and description, as the catalogue has them. Always on the page; move it up or down.",
	},
	// The products the shop chose to suggest with this one (D133). Only the
	// curated ones: the related products below already fill in from the
	// category, and drawing the same fill twice would be noise. Empty draws
	// nothing.
	{
		Type: "cross-sells", Label: "You may also like", Icon: "ri-heart-add-line", Pages: []string{"product"},
		Description: "The products chosen in the product's You may also like card, each with a quick Add to cart. Nothing shows for a product with none chosen.",
		Fields: []Field{
			{Key: "heading", Label: "Heading", Kind: KindText, MaxLen: maxShort, Help: "Empty says \"You may also like\"."},
		},
	},
	{
		Type: "product-reviews", Label: "Reviews", Icon: "ri-star-line", Pages: []string{"product"},
		Description: "The product's reviews and rating, and the form a shopper writes one in.",
	},
	{
		Type: "related-products", Label: "Related products", Icon: "ri-shapes-line", Pages: []string{"product"},
		Description: "Other products from the same category, with live prices.",
		Fields: []Field{
			{Key: "heading", Label: "Heading", Kind: KindText, MaxLen: maxShort, Help: "Empty says \"Related products\"."},
		},
	},

	// The collection page's core: its title, description and the grid of its
	// products.
	{
		Type: "main-collection", Label: "Products", Icon: "ri-layout-grid-line", Pages: []string{"collection"}, Fixed: true,
		Description: "The collection's title, description and its products, as the catalogue has them. Always on the page; move it up or down.",
	},

	// The header and the footer (D131): each the whole of its page, drawn by
	// the storefront at the top and the bottom of every page. Unlike a
	// template's core they have settings, because what they show is the
	// shop's own words and menus, not the catalogue's.
	{
		Type: "header", Label: "Header", Icon: "ri-layout-top-line", Pages: []string{"header"}, Fixed: true,
		Description: "The strip of news above the header, and the menu across it. The logo, search, account and basket stay where they are.",
		Fields: []Field{
			{Key: "show_announcement", Label: "Show the announcement strip", Kind: KindBool, Default: false},
			{Key: "announcement_text", Label: "Announcement", Kind: KindText, MaxLen: maxShort, ShowWhen: map[string]string{"show_announcement": "true"}},
			{Key: "announcement_link", Label: "Announcement link", Kind: KindURL, Help: "Where the strip goes when tapped. Leave empty for none.",
				ShowWhen: map[string]string{"show_announcement": "true"}},
			{Key: "announcement_pages", Label: "Show the strip on", Kind: KindSelect, Default: "all",
				Options: []Option{{"all", "Every page"}, {"home", "The home page only"}}, ShowWhen: map[string]string{"show_announcement": "true"}},
			// The default false is what an older header, saved before this
			// switch, reads as: the menu shown. The picker's ShowWhen needs it.
			{Key: "hide_menu", Label: "Hide the header menu", Kind: KindBool, Default: false,
				Help: "No menu across the header, and no menu button on phones. The logo, search, account and basket stay."},
			{Key: "menu", Label: "Header menu", Kind: KindMenu, Default: "header", Requires: "navigation",
				Help:     "Its links run across the header, and fill the menu on phones. Make and edit menus in Content → Menus.",
				ShowWhen: map[string]string{"hide_menu": "false"}},
		},
		Check: func(s map[string]any) error {
			if on, _ := s["show_announcement"].(bool); on && s["announcement_text"] == nil {
				return errors.New("the announcement strip is on, so it needs an announcement; switch it off to show none")
			}
			return nil
		},
	},
	{
		Type: "footer", Label: "Footer", Icon: "ri-layout-bottom-line", Pages: []string{"footer"}, Fixed: true,
		Description: "The columns of links at the foot of every page, a few words about the shop, its social links and the copyright line.",
		Fields: []Field{
			{Key: "menu", Label: "Footer menu", Kind: KindMenu, Default: "footer", Requires: "navigation",
				Help: "Each top-level link with links under it becomes a column, headed by its title; links with none under them share one column. Make and edit menus in Content → Menus."},
			{Key: "about", Label: "About the shop", Kind: KindTextarea, MaxLen: 600, Help: "A few words beside the logo. Empty uses the store's description."},
			{Key: "newsletter", Label: "Show the newsletter sign-up", Kind: KindBool, Default: true, Requires: "newsletter"},
			{Key: "social", Label: "Social links", Kind: KindList, MaxItems: 8,
				Help: "Empty keeps the social links set in the storefront's plugins.", Fields: []Field{
					{Key: "network", Label: "Network", Kind: KindSelect, Required: true, Options: SocialNetworks},
					{Key: "url", Label: "Address", Kind: KindURL, Required: true, Help: "The page's full address, https://…"},
				}},
			{Key: "copyright", Label: "Copyright line", Kind: KindText, MaxLen: maxShort, Help: "Empty says © this year, the store's name, all rights reserved."},
		},
		Check: func(s map[string]any) error {
			links, _ := s["social"].([]map[string]any)
			for i, l := range links {
				// A social link leaves the store, so a path on it is a mistake.
				if u, _ := l["url"].(string); strings.HasPrefix(u, "/") {
					return fmt.Errorf("Social links, %d: Address must be a web address (https://…)", i+1)
				}
			}
			return nil
		},
	},
}

// BenefitIcons are the pictures a benefit may carry (D139). A name, not a
// picture: the storefront draws each in its own icon style, and the panel in
// its own, so neither ships the other's artwork. A name added here needs its
// picture in the storefront's map too; until then it draws no icon there.
var BenefitIcons = []Option{
	{"truck", "Delivery"}, {"return", "Returns"}, {"exchange", "Exchange"}, {"shield", "Protection"},
	{"lock", "Secure payment"}, {"headset", "Support"}, {"phone", "Call us"}, {"cash", "Cash on delivery"},
	{"wallet", "Wallet"}, {"percent", "Offers"}, {"gift", "Gift"}, {"package", "Packaging"},
	{"clock", "Fast"}, {"star", "Quality"}, {"award", "Award"}, {"verified", "Certified"},
	{"sparkle", "Handmade"}, {"leaf", "Natural"}, {"heart", "Made with care"}, {"map-pin", "Local"},
}

// SocialNetworks are the networks a footer links to, each drawn with its own
// mark by the storefront.
var SocialNetworks = []Option{
	{"instagram", "Instagram"}, {"facebook", "Facebook"}, {"x", "X"}, {"youtube", "YouTube"},
	{"pinterest", "Pinterest"}, {"linkedin", "LinkedIn"}, {"whatsapp", "WhatsApp"}, {"telegram", "Telegram"},
}

// PageInfo is a page the shop can arrange.
type PageInfo struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Core is the page's fixed section type; empty when it has none.
	Core string `json:"core,omitempty"`
	// CoreOnly is a page that is its core and nothing more: the header and
	// the footer, which the storefront draws around every other page (D131).
	CoreOnly bool `json:"core_only,omitempty"`
}

// Pages are the pages a shop can arrange, each drawn by the storefront from
// its own document: the home page, and the templates every product and every
// collection is drawn in (D129).
var Pages = []PageInfo{
	{Key: "home", Label: "home page"},
	{Key: "product", Label: "product page", Core: "main-product"},
	{Key: "collection", Label: "collection page", Core: "main-collection"},
	{Key: "header", Label: "header", Core: "header", CoreOnly: true},
	{Key: "footer", Label: "footer", Core: "footer", CoreOnly: true},
}

func pageInfo(key string) *PageInfo {
	for i := range Pages {
		if Pages[i].Key == key {
			return &Pages[i]
		}
	}
	return nil
}

func (s SectionType) allowedOn(page string) bool {
	if p := pageInfo(page); p != nil && p.CoreOnly {
		return s.Type == p.Core
	}
	if len(s.Pages) == 0 {
		return true
	}
	for _, p := range s.Pages {
		if p == page {
			return true
		}
	}
	return false
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
// storefront's own look, so there is no default to invent: a field here has
// no Default, a switch included (D155).
var ThemeSettings = []SettingGroup{
	{Label: "Colours", Fields: []Field{
		{Key: "background", Label: "Background", Kind: KindColor},
		{Key: "text", Label: "Text", Kind: KindColor},
		{Key: "accent", Label: "Buttons and links", Kind: KindColor},
		{Key: "accent_text", Label: "Text on buttons", Kind: KindColor},
		{Key: "muted", Label: "Quiet backgrounds", Kind: KindColor, Help: "Bands behind a hero or a strip, and hover fills."},
		{Key: "sale", Label: "Savings and offers", Kind: KindColor, Help: "The \"% off\" and offer lines."},
	}},
	{Label: "Typography", Fields: []Field{
		{Key: "heading_font", Label: "Headings", Kind: KindSelect, Options: Fonts},
		{Key: "body_font", Label: "Body text", Kind: KindSelect, Options: Fonts},
		{Key: "type_scale", Label: "Text size", Kind: KindSelect,
			Options: []Option{{"compact", "Compact (14px text)"}, {"standard", "Standard (16px text)"}, {"large", "Large (18px text)"}}},
		{Key: "heading_weight", Label: "Heading weight", Kind: KindSelect,
			Options: []Option{{"medium", "Medium"}, {"semibold", "Semibold"}, {"bold", "Bold"}, {"extrabold", "Extra bold"}}},
		{Key: "eyebrow_case", Label: "Small line above a heading", Kind: KindSelect, Help: "Capitals draws it in spaced capital letters, whatever was typed.",
			Options: []Option{{"sentence", "As typed"}, {"upper", "Capitals"}}},
	}},
	{Label: "Spacing", Fields: []Field{
		{Key: "density", Label: "Density", Kind: KindSelect, Help: "The room around sections, cards and lists.",
			Options: []Option{{"compact", "Compact"}, {"comfortable", "Comfortable"}, {"spacious", "Spacious"}}},
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
	{Label: "Product cards", Fields: []Field{
		{Key: "card_badge", Label: "Badge on the picture", Kind: KindSelect, Help: "The label shown on the picture: the badge set on the product, or its first tag or highlight.",
			Options: []Option{{"none", "None"}, {"product_badge", "Product badge"}, {"first_tag", "First tag"}, {"first_highlight", "First highlight"}}},
		{Key: "card_vendor", Label: "Brand line", Kind: KindBool},
		{Key: "card_title_lines", Label: "Title", Kind: KindSelect,
			Options: []Option{{"one", "One line"}, {"two", "Two lines"}}},
		{Key: "card_rating", Label: "Star rating", Kind: KindBool, Help: "Only when the product has reviews."},
		{Key: "card_discount", Label: "% off", Kind: KindBool, Help: "Only with a compare-at price."},
		{Key: "card_wishlist", Label: "Wishlist button", Kind: KindBool},
		{Key: "card_add_to_cart", Label: "Quick add", Kind: KindBool},
		{Key: "card_second_image", Label: "Second picture on hover", Kind: KindBool},
	}},
	{Label: "Product lists", Fields: []Field{
		{Key: "list_columns_mobile", Label: "Across on a phone", Kind: KindSelect,
			Options: []Option{{"one", "One"}, {"two", "Two"}}},
		{Key: "list_columns_desktop", Label: "Across on a computer", Kind: KindSelect,
			Options: []Option{{"three", "Three"}, {"four", "Four"}, {"five", "Five"}}},
		{Key: "list_subcategories", Label: "Sub-category pictures", Kind: KindBool},
		{Key: "list_quick_filters", Label: "Quick filter chips", Kind: KindBool},
		{Key: "list_filters_desktop", Label: "Filters on a computer", Kind: KindSelect,
			Options: []Option{{"sidebar", "Beside the products"}, {"drawer", "In a drawer"}}},
		{Key: "list_mobile_bar", Label: "Sort and filter bar on phones", Kind: KindBool},
	}},
	{Label: "Product page", Fields: []Field{
		{Key: "pdp_sticky_buy", Label: "Buy button stays on screen on phones", Kind: KindBool},
		{Key: "pdp_gallery", Label: "Pictures on a computer", Kind: KindSelect,
			Options: []Option{{"carousel", "One at a time"}, {"grid", "All in a grid"}}},
		{Key: "pdp_highlights_grid", Label: "Highlights as a grid", Kind: KindBool},
		{Key: "pdp_delivery_check", Label: "Delivery check by PIN code", Kind: KindBool},
		{Key: "pdp_low_stock_at", Label: "Show \"only N left\" at or below", Kind: KindNumber, Min: 0, Max: 20, Help: "0 never shows it."},
		{Key: "pdp_size_guide", Label: "Size guide page", Kind: KindPath, MaxLen: maxShort, Help: "A page on your store, such as /size-guide."},
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
	// menuHandleRE is ext/navigation's rule for a handle.
	menuHandleRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	colorRE      = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	youTubeIDRE  = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	vimeoIDRE    = regexp.MustCompile(`^[0-9]{1,12}$`)
)

func sectionType(t string) *SectionType {
	for i := range Sections {
		if Sections[i].Type == t {
			return &Sections[i]
		}
	}
	return nil
}

// Validate checks a document for page against the registry and returns it
// cleaned: unknown settings dropped, text trimmed, defaults filled, numbers
// whole, the core shown. An error names the section and setting, for the
// editor to point at.
func Validate(page string, doc Document) (Document, error) {
	info := pageInfo(page)
	if info == nil {
		return doc, gocommerce.Validationf("there is no page %q", page)
	}
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
	// The design tokens publish with the home page (D127); another page's
	// copy would be a second set that nothing reads.
	if page == "home" {
		tokens, err := cleanFields("theme settings", themeSettingFields(), doc.ThemeSettings)
		if err != nil {
			return doc, err
		}
		if len(tokens) > 0 {
			out.ThemeSettings = tokens
		}
	}
	cores := 0
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
		if !st.allowedOn(page) {
			return doc, gocommerce.Validationf("%s: it can't go on the %s", where, info.Label)
		}
		settings, err := cleanFields(where, st.Fields, s.Settings)
		if err != nil {
			return doc, err
		}
		if st.Check != nil {
			if err := st.Check(settings); err != nil {
				return doc, gocommerce.Validationf("%s: %s", where, err.Error())
			}
		}
		enabled := s.Enabled
		if st.Fixed {
			if cores++; cores > 1 {
				return doc, gocommerce.Validationf("%s: the %s holds its %s section only once", where, info.Label, st.Label)
			}
			// Hiding the core would leave a product page without its product.
			enabled = true
		}
		out.Sections = append(out.Sections, Section{ID: s.ID, Type: s.Type, Enabled: enabled, Settings: settings})
	}
	if info.Core != "" && cores == 0 {
		return doc, gocommerce.Validationf("the %s must keep its %s section", info.Label, sectionType(info.Core).Label)
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
	case KindText, KindTextarea, KindURL, KindImage, KindVideo, KindSelect, KindIcon, KindColor, KindMenu, KindPath:
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
		case KindMenu:
			// A handle, not the menu: the storefront reads the menu's links
			// live, so a menu edited in Content → Menus shows without a publish.
			if len(s) > 64 || !menuHandleRE.MatchString(s) {
				return nil, bad("must name a menu by its handle: lowercase letters, digits and single dashes")
			}
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
		case KindPath:
			if !strings.HasPrefix(s, "/") || !safeURL(s) {
				return nil, bad("must be a page on the store, starting with /")
			}
		case KindSelect, KindIcon:
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
		// Unset and without a default is unset, as every other kind is: a
		// theme switch left alone keeps the storefront's own choice.
		if v == nil {
			return nil, nil
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
