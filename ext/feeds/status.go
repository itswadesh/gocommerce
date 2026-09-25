package feeds

import (
	"net/http"
	"strings"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// What is in the feed right now.
//
// Both feeds are built on request, which is the right design for a store this
// size — a platform that fetches daily never sees a price more than a day old,
// and there is no build to schedule, watch or retry. It has one cost: the
// screen has nothing to show. A page about a file that is not stored anywhere
// can say when it was last built, which is "never, and also always", or it can
// go and look.
//
// So it goes and looks, over the same walk the feed itself uses. That is the
// whole reason this lives here rather than being assembled in the panel out of
// the product list: a count derived from anywhere else is a second answer to
// "what is in the feed", and two answers to one question drift.
//
// The three missing-field counts are not decoration. Missing GTIN or brand,
// and images the platform cannot use, are what Merchant Center and Commerce
// Manager actually reject items over, and an operator finds out days later in
// somebody else's dashboard. Counting them here turns the advice on the screen
// from a leaflet about feeds in general into a list of this store's own work.

// feedStatus is what the Feeds screen draws.
type feedStatus struct {
	// Enabled and Configured say whether the addresses answer at all. The
	// screen renders either way, so this route does not 404 when the plugin is
	// off — a status that refuses to load is the same blank page it replaced.
	Enabled    bool   `json:"enabled"`
	Configured bool   `json:"configured"`
	Storefront string `json:"storefront"`
	Currency   string `json:"currency"`

	Items      int `json:"items"`
	Products   int `json:"products"`
	InStock    int `json:"in_stock"`
	OutOfStock int `json:"out_of_stock"`

	MissingImage      int `json:"missing_image"`
	MissingBrand      int `json:"missing_brand"`
	MissingIdentifier int `json:"missing_identifier"`
}

func (m *Module) handleStatus(w http.ResponseWriter, r *http.Request) {
	s, err := m.settings(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	out := feedStatus{
		Enabled:    s.enabled,
		Configured: s.storefront != "",
		Storefront: s.storefront,
		Currency:   strings.ToUpper(m.app.Settings().Currency),
	}
	// Nothing to count until there is an address to build links from: items()
	// would produce rows pointing at nowhere, and a count of those would
	// overstate what the feed can serve.
	if !out.Configured {
		gocommerce.Respond(w, http.StatusOK, out)
		return
	}

	items, err := m.items(r.Context(), s, requestBase(r))
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	groups := make(map[string]struct{}, len(items))
	for _, it := range items {
		out.Items++
		groups[it.GroupID] = struct{}{}
		// The feed's own word for it, not the stock number: a variant that
		// does not track inventory is in stock with no quantity at all, and
		// counting quantities would call it sold out.
		if it.Availability == "in stock" {
			out.InStock++
		} else {
			out.OutOfStock++
		}
		if it.Image == "" {
			out.MissingImage++
		}
		if it.Brand == "" {
			out.MissingBrand++
		}
		// Google takes a GTIN, or a brand and an MPN together — not an MPN on
		// its own. Every item here has an MPN, because the feed sends the SKU
		// as one, so the item at risk is the one with no barcode AND no
		// vendor. Counting "no GTIN and no MPN" instead, which is the rule as
		// people remember it, would report zero on every store forever.
		if it.GTIN == "" && (it.Brand == "" || it.MPN == "") {
			out.MissingIdentifier++
		}
	}
	out.Products = len(groups)
	gocommerce.Respond(w, http.StatusOK, out)
}
