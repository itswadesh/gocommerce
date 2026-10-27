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
//
// Not all of them are add-ons. The storefront extras a theme draws, the
// store's own rules and cash on delivery are part of the product, so each
// names the screen it is set up on (Home) and the Plugins screen leaves it
// out (D132). The analytics, chat and marketing tags stay plugins.
const (
	homeStorefrontExtras = "/dash/themes/extras"
	homeStoreSettings    = "/dash/settings"
)

var builtinPlugins = []PluginDef{
	{
		Key: "hello-bar", Home: homeStorefrontExtras, Title: "Hello bar", Icon: "ri-megaphone-line", Category: "storefront",
		Description: "A line across the top of every storefront page — a promotion, a notice, a shipping threshold.",
		Fields: []PluginField{
			{Key: "message", Label: "Message", Kind: "text", Public: true, Required: true},
			{Key: "link", Label: "Link", Kind: "url", Public: true, Help: "Where the bar goes when clicked; leave empty for a plain notice."},
			{Key: "background", Label: "Background colour", Kind: "text", Public: true, Default: "#111111"},
		},
	},
	{
		Key: "store-close", Home: homeStoreSettings, Title: "Store closed", Icon: "ri-door-closed-line", Category: "storefront",
		Description: "Close the storefront with a message — a holiday, a stock-take, a relaunch — without touching the catalogue.",
		Fields: []PluginField{
			{Key: "message", Label: "Message", Kind: "textarea", Public: true, Required: true, Default: "We are closed for a short while. Back soon."},
		},
	},
	{
		Key: "enquiry-mode", Home: homeStoreSettings, Title: "Enquiry mode", Icon: "ri-question-answer-line", Category: "storefront",
		Description: "Shoppers enquire about a product instead of buying it: the storefront shows an enquiry form where the Add to cart button was.",
		Fields: []PluginField{
			{Key: "button", Label: "Button text", Kind: "text", Public: true, Default: "Enquire"},
			{Key: "email", Label: "Enquiries go to", Kind: "text", Public: false, Help: "The address the storefront sends enquiries to."},
		},
	},
	// Cash on delivery's switch (D102). The method itself is core's
	// codProvider; this is only whether a store takes it, so it is on until an
	// owner says otherwise, and every store that ran before the switch existed
	// keeps taking it.
	{
		Key: CODPluginKey, Home: "/dash/settings/payments", Title: "Cash on delivery", Icon: "ri-hand-coin-line", Category: "payments", DefaultEnabled: true,
		Description: "Shoppers pay in cash when the order arrives. Off, checkout offers only the gateways that are set up.",
	},
	{
		Key: "guest-checkout", Home: homeStoreSettings, Title: "Guest checkout", Icon: "ri-user-3-line", Category: "storefront", DefaultEnabled: true,
		Description: "Let a shopper check out without an account. Off, the storefront asks them to sign in first.",
	},
	// No "wishlist" here any more. It was a switch with nothing behind it —
	// core has no table to save a product into — and ext/wishlist now
	// implements the feature under the same key, so a store that had the
	// placeholder switched on keeps its switch and gains what it promised.
	{
		Key: "recent-purchase-popup", Title: "Recent purchase popup", Icon: "ri-notification-3-line", Category: "marketing",
		Description: "A small note that somebody just bought something, for the social proof of it.",
		Fields: []PluginField{
			{Key: "delay_seconds", Label: "Seconds between popups", Kind: "number", Public: true, Default: 8},
		},
	},
	{
		Key: "trust-badges", Home: homeStorefrontExtras, Title: "Trust badges", Icon: "ri-shield-check-line", Category: "storefront",
		Description: "Badges under the Add to cart button — secure checkout, free returns, whatever this store stands behind.",
		Fields: []PluginField{
			{Key: "badges", Label: "Badges", Kind: "textarea", Public: true, Required: true, Help: "One per line."},
		},
	},
	{
		Key: "social-sharing", Home: homeStorefrontExtras, Title: "Social sharing buttons", Icon: "ri-share-forward-line", Category: "marketing",
		Description: "Share buttons on the product page.",
		Fields: []PluginField{
			{Key: "networks", Label: "Networks", Kind: "text", Public: true, Default: "facebook, x, whatsapp, pinterest", Help: "Comma-separated."},
		},
	},
	{
		Key: "whatsapp-chat", Title: "WhatsApp chat button", Icon: "ri-whatsapp-line", Category: "chat",
		Description: "A WhatsApp button in the storefront's corner that opens a chat with the store.",
		Fields: []PluginField{
			{Key: "phone", Label: "Phone number", Kind: "text", Public: true, Required: true, Help: "International format, digits only: 919876543210."},
			{Key: "message", Label: "Opening message", Kind: "text", Public: true, Default: "Hi! I have a question about an order."},
		},
	},
	{
		Key: "header-scripts", Title: "Header scripts", Icon: "ri-code-s-slash-line", Category: "storefront",
		Description: "HTML the storefront puts in every page's head: verification tags, a font, a pixel nothing else here covers.",
		Fields: []PluginField{
			{Key: "html", Label: "HTML", Kind: "textarea", Public: true, Required: true},
		},
	},
	{
		Key: "google-analytics", Title: "Google Analytics, Tag Manager and Ads", Icon: "ri-google-line", Category: "analytics",
		Description: "GA4 measurement, a Tag Manager container, and the Google Ads conversion the storefront fires on an order.",
		Fields: []PluginField{
			{Key: "ga4_measurement_id", Label: "GA4 measurement ID", Kind: "text", Public: true, Help: "G-XXXXXXXXXX"},
			{Key: "gtm_container_id", Label: "Tag Manager container", Kind: "text", Public: true, Help: "GTM-XXXXXXX"},
			{Key: "google_ads_id", Label: "Google Ads ID", Kind: "text", Public: true, Help: "AW-XXXXXXXXX"},
			{Key: "conversion_label", Label: "Conversion label", Kind: "text", Public: true},
		},
	},
	{
		Key: "meta-pixel", Title: "Meta pixel", Icon: "ri-meta-line", Category: "analytics",
		Description: "The Facebook and Instagram pixel, for ads that know what was bought.",
		Fields: []PluginField{
			{Key: "pixel_id", Label: "Pixel ID", Kind: "text", Public: true, Required: true},
		},
	},
	{
		Key: "tidio", Title: "Tidio", Icon: "ri-chat-smile-3-line", Category: "chat",
		Description: "Tidio's live chat and chatbot widget.",
		Fields: []PluginField{
			{Key: "public_key", Label: "Public key", Kind: "text", Public: true, Required: true},
		},
	},
	{
		Key: "tawk-to", Title: "tawk.to", Icon: "ri-chat-3-line", Category: "chat",
		Description: "tawk.to's free live chat widget.",
		Fields: []PluginField{
			{Key: "property_id", Label: "Property ID", Kind: "text", Public: true, Required: true},
			{Key: "widget_id", Label: "Widget ID", Kind: "text", Public: true, Required: true, Default: "default"},
		},
	},
	{
		Key: "umami", Title: "Umami", Icon: "ri-bar-chart-2-line", Category: "analytics",
		Description: "Umami's privacy-friendly analytics script.",
		Fields: []PluginField{
			{Key: "script_url", Label: "Script URL", Kind: "url", Public: true, Required: true, Help: "https://analytics.example.com/script.js"},
			{Key: "website_id", Label: "Website ID", Kind: "text", Public: true, Required: true},
		},
	},
	{
		Key: "plausible", Title: "Plausible", Icon: "ri-line-chart-line", Category: "analytics",
		Description: "Plausible's privacy-friendly analytics script.",
		Fields: []PluginField{
			{Key: "domain", Label: "Domain", Kind: "text", Public: true, Required: true, Help: "shop.example.com"},
			{Key: "script_url", Label: "Script URL", Kind: "url", Public: true, Default: "https://plausible.io/js/script.js"},
		},
	},
	{
		Key: "trustpilot", Title: "Trustpilot", Icon: "ri-star-smile-line", Category: "marketing",
		Description: "The Trustpilot TrustBox on the storefront.",
		Fields: []PluginField{
			{Key: "business_unit_id", Label: "Business unit ID", Kind: "text", Public: true, Required: true},
			{Key: "template_id", Label: "TrustBox template ID", Kind: "text", Public: true, Required: true},
		},
	},
	{
		Key: "news-ticker", Home: homeStorefrontExtras, Title: "News ticker", Icon: "ri-newspaper-line", Category: "storefront",
		Description: "A scrolling line of short notices.",
		Fields: []PluginField{
			{Key: "items", Label: "Notices", Kind: "textarea", Public: true, Required: true, Help: "One per line."},
		},
	},
	// The sold count (D134). The engine applies it, so its setting is not
	// public: a storefront reads sold_at_least on the product.
	{
		Key: SoldCountPluginKey, Home: homeStorefrontExtras, Title: "Sold count", Icon: "ri-fire-line", Category: "storefront",
		Description: "Product pages say how many have sold, rounded down: \"30+ sold\", \"45k+ sold\". Counted from confirmed orders that were not cancelled or refunded, sample orders left out, and refreshed every few minutes.",
		Fields: []PluginField{
			{Key: "threshold", Label: "Show from", Kind: "number", Default: defaultSoldThreshold,
				Help: "A product shows its count once it has sold at least this many. Under it, nothing is shown."},
		},
	},
}
