package alltest

import (
	"sort"
	"testing"

	"github.com/itswadesh/gocommerce/gctest"
)

// The Plugins screen lists add-ons: an optional integration a store wires in
// from outside, or an extra it may never want. Everything else the build
// carries is part of the product, set up on its own screen (D132). A module
// that registers a plugin lands in one list or the other here, so a new one
// is a decision rather than a default.
func TestThePluginsScreenListsOnlyAddOns(t *testing.T) {
	app := gctest.New(t, everyModule()...)

	addOns := map[string]bool{
		// Analytics
		"google-analytics": true, "meta-pixel": true, "umami": true, "plausible": true,
		"microsoft-clarity": true, "hotjar": true, "tiktok-pixel": true, "pinterest-tag": true, "snapchat-pixel": true,
		// Chat
		"tidio": true, "tawk-to": true, "whatsapp-chat": true,
		// Marketing
		"trustpilot": true, "klaviyo": true, "indexnow": true, "product-feeds": true, "recent-purchase-popup": true,
		"google-reviews": true,
		// Search
		"meilisearch": true,
		// Other
		"import-shopify": true, "ai-gateway": true, "header-scripts": true,
	}
	builtIn := map[string]string{
		"banners":          "/dash/themes/banners",
		"layouts":          "/dash/themes",
		"hello-bar":        "/dash/themes/extras",
		"news-ticker":      "/dash/themes/extras",
		"sold-count":       "/dash/themes/extras",
		"cart-suggestions": "/dash/themes/extras",
		"trust-badges":     "/dash/themes/extras",
		"social-sharing":   "/dash/themes/extras",
		"telegram-alerts":  "/dash/notifications/telegram",
		"email-resend":     "/dash/notifications/email",
		"email-sendgrid":   "/dash/notifications/email",
		"sms-msg91":        "/dash/notifications/sms",
		"sms-twilio":       "/dash/notifications/sms",
		"navigation":       "/dash/menus",
		"faq":              "/dash/faq",
		"showcases":        "/dash/showcases",
		"contact-form":     "/dash/contact",
		"newsletter":       "/dash/newsletter",
		"product-reviews":  "/dash/reviews",
		"wishlist":         "/dash/wishlists",
		"sitemap":          "/dash/sitemap",
		"identity":         "/dash/settings",
		"guest-checkout":   "/dash/settings",
		"store-close":      "/dash/settings",
		"enquiry-mode":     "/dash/settings",
		"payments-cod":     "/dash/settings/payments",
	}

	var unclassified []string
	for _, def := range app.Plugins().Defs() {
		switch {
		case addOns[def.Key]:
			if def.Home != "" {
				t.Errorf("%s is an add-on and must stay on the Plugins screen, but names %s", def.Key, def.Home)
			}
		case builtIn[def.Key] != "":
			if def.Home != builtIn[def.Key] {
				t.Errorf("%s is set up at %q, want %q", def.Key, def.Home, builtIn[def.Key])
			}
		case def.Category == "payments":
			if def.Home != "/dash/settings/payments" {
				t.Errorf("gateway %s is set up at %q, want Payment methods", def.Key, def.Home)
			}
		case def.Category == "shipping":
			if def.Home != "/dash/shipping-settings/providers" {
				t.Errorf("carrier %s is set up at %q, want Shipping providers", def.Key, def.Home)
			}
		default:
			unclassified = append(unclassified, def.Key)
		}
		if def.AlwaysOn != (def.Key == "banners") {
			t.Errorf("%s always on = %v; only banners has no switch", def.Key, def.AlwaysOn)
		}
	}
	sort.Strings(unclassified)
	for _, key := range unclassified {
		t.Errorf("%s is neither an add-on nor a built-in feature here: decide which (D132)", key)
	}
}
