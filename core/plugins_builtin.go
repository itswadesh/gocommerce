package gocommerce

// The plugins core ships. Every one of these is settings and nothing else:
// a storefront reads GET /api/plugins and renders the bar, the button, the
// tag. Core enforces none of them, because they describe the storefront,
// and the storefront is whatever the store built — the engine's job is to
// hold the switch and the values in one place an operator can reach.
//
// A plugin with code behind it — a search index, a marketing integration —
// is registered by its module instead, so that the list only ever shows
// what this binary can actually do.
var builtinPlugins = []PluginDef{
	{
		Key: "hello-bar", Title: "Hello bar", Category: "storefront",
		Description: "A line across the top of every storefront page — a promotion, a notice, a shipping threshold.",
		Fields: []PluginField{
			{Key: "message", Label: "Message", Kind: "text", Public: true, Required: true},
			{Key: "link", Label: "Link", Kind: "url", Public: true, Help: "Where the bar goes when clicked; leave empty for a plain notice."},
			{Key: "background", Label: "Background colour", Kind: "text", Public: true, Default: "#111111"},
		},
	},
	{
		Key: "store-close", Title: "Store closed", Category: "storefront",
		Description: "Close the storefront with a message — a holiday, a stock-take, a relaunch — without touching the catalogue.",
		Fields: []PluginField{
			{Key: "message", Label: "Message", Kind: "textarea", Public: true, Required: true, Default: "We are closed for a short while. Back soon."},
		},
	},
	{
		Key: "enquiry-mode", Title: "Enquiry mode", Category: "storefront",
		Description: "Shoppers enquire about a product instead of buying it: the storefront shows an enquiry form where the Add to cart button was.",
		Fields: []PluginField{
			{Key: "button", Label: "Button text", Kind: "text", Public: true, Default: "Enquire"},
			{Key: "email", Label: "Enquiries go to", Kind: "text", Public: false, Help: "The address the storefront sends enquiries to."},
		},
	},
	{
		Key: "guest-checkout", Title: "Guest checkout", Category: "storefront", DefaultEnabled: true,
		Description: "Let a shopper check out without an account. Off, the storefront asks them to sign in first.",
	},
	// No "wishlist" here any more. It was a switch with nothing behind it —
	// core has no table to save a product into — and ext/wishlist now
	// implements the feature under the same key, so a store that had the
	// placeholder switched on keeps its switch and gains what it promised.
	{
		Key: "recent-purchase-popup", Title: "Recent purchase popup", Category: "marketing",
		Description: "A small note that somebody just bought something, for the social proof of it.",
		Fields: []PluginField{
			{Key: "delay_seconds", Label: "Seconds between popups", Kind: "number", Public: true, Default: 8},
		},
	},
	{
		Key: "trust-badges", Title: "Trust badges", Category: "storefront",
		Description: "Badges under the Add to cart button — secure checkout, free returns, whatever this store stands behind.",
		Fields: []PluginField{
			{Key: "badges", Label: "Badges", Kind: "textarea", Public: true, Required: true, Help: "One per line."},
		},
	},
	{
		Key: "social-sharing", Title: "Social sharing buttons", Category: "marketing",
		Description: "Share buttons on the product page.",
		Fields: []PluginField{
			{Key: "networks", Label: "Networks", Kind: "text", Public: true, Default: "facebook, x, whatsapp, pinterest", Help: "Comma-separated."},
		},
	},
	{
		Key: "whatsapp-chat", Title: "WhatsApp chat button", Category: "chat",
		Description: "A WhatsApp button in the storefront's corner that opens a chat with the store.",
		Fields: []PluginField{
			{Key: "phone", Label: "Phone number", Kind: "text", Public: true, Required: true, Help: "International format, digits only: 919876543210."},
			{Key: "message", Label: "Opening message", Kind: "text", Public: true, Default: "Hi! I have a question about an order."},
		},
	},
	{
		Key: "header-scripts", Title: "Header scripts", Category: "storefront",
		Description: "HTML the storefront puts in every page's head: verification tags, a font, a pixel nothing else here covers.",
		Fields: []PluginField{
			{Key: "html", Label: "HTML", Kind: "textarea", Public: true, Required: true},
		},
	},
	{
		Key: "google-analytics", Title: "Google Analytics, Tag Manager and Ads", Category: "analytics",
		Description: "GA4 measurement, a Tag Manager container, and the Google Ads conversion the storefront fires on an order.",
		Fields: []PluginField{
			{Key: "ga4_measurement_id", Label: "GA4 measurement ID", Kind: "text", Public: true, Help: "G-XXXXXXXXXX"},
			{Key: "gtm_container_id", Label: "Tag Manager container", Kind: "text", Public: true, Help: "GTM-XXXXXXX"},
			{Key: "google_ads_id", Label: "Google Ads ID", Kind: "text", Public: true, Help: "AW-XXXXXXXXX"},
			{Key: "conversion_label", Label: "Conversion label", Kind: "text", Public: true},
		},
	},
	{
		Key: "meta-pixel", Title: "Meta pixel", Category: "analytics",
		Description: "The Facebook and Instagram pixel, for ads that know what was bought.",
		Fields: []PluginField{
			{Key: "pixel_id", Label: "Pixel ID", Kind: "text", Public: true, Required: true},
		},
	},
	{
		Key: "tidio", Title: "Tidio", Category: "chat",
		Description: "Tidio's live chat and chatbot widget.",
		Fields: []PluginField{
			{Key: "public_key", Label: "Public key", Kind: "text", Public: true, Required: true},
		},
	},
	{
		Key: "tawk-to", Title: "tawk.to", Category: "chat",
		Description: "tawk.to's free live chat widget.",
		Fields: []PluginField{
			{Key: "property_id", Label: "Property ID", Kind: "text", Public: true, Required: true},
			{Key: "widget_id", Label: "Widget ID", Kind: "text", Public: true, Required: true, Default: "default"},
		},
	},
	{
		Key: "umami", Title: "Umami", Category: "analytics",
		Description: "Umami's privacy-friendly analytics script.",
		Fields: []PluginField{
			{Key: "script_url", Label: "Script URL", Kind: "url", Public: true, Required: true, Help: "https://analytics.example.com/script.js"},
			{Key: "website_id", Label: "Website ID", Kind: "text", Public: true, Required: true},
		},
	},
	{
		Key: "plausible", Title: "Plausible", Category: "analytics",
		Description: "Plausible's privacy-friendly analytics script.",
		Fields: []PluginField{
			{Key: "domain", Label: "Domain", Kind: "text", Public: true, Required: true, Help: "shop.example.com"},
			{Key: "script_url", Label: "Script URL", Kind: "url", Public: true, Default: "https://plausible.io/js/script.js"},
		},
	},
	{
		Key: "trustpilot", Title: "Trustpilot", Category: "marketing",
		Description: "The Trustpilot TrustBox on the storefront.",
		Fields: []PluginField{
			{Key: "business_unit_id", Label: "Business unit ID", Kind: "text", Public: true, Required: true},
			{Key: "template_id", Label: "TrustBox template ID", Kind: "text", Public: true, Required: true},
		},
	},
	{
		Key: "news-ticker", Title: "News ticker", Category: "storefront",
		Description: "A scrolling line of short notices.",
		Fields: []PluginField{
			{Key: "items", Label: "Notices", Kind: "textarea", Public: true, Required: true, Help: "One per line."},
		},
	},
}
