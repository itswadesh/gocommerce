// Package googlereviews is what Google knows about a store, three ways, each
// switched on by itself under one plugin:
//
//   - The store's rating and recent reviews from its Google Business Profile,
//     read through the Places API (New) for the storefront to show with
//     Google's attribution (places.go).
//   - Google Customer Reviews: the survey Google offers a shopper after
//     checkout, which is how a store earns seller ratings (survey.go).
//   - A product ratings feed for Merchant Center, built from the store's own
//     published reviews (feed.go).
//
// Google's terms decide most of the shape. A place's rating and reviews may
// not be saved or cached: Google Maps Platform Terms 3.2.3(a)(iii) and (b)
// (cloud.google.com/maps-platform/terms), with the Service Specific Terms
// allowing only the place ID (General Service Terms 3) and, for 30 days,
// coordinates (Places API 14.3) (cloud.google.com/maps-platform/terms/maps-service-terms).
// So every read is Google's live answer, and the module has no table. The
// Business Profile API, which would list every review, needs Google's approval
// of an access request (developers.google.com/my-business/content/prereqs),
// so it is not used. The rest is cited where it applies.
//
//	fd, rv := feeds.New(feeds.Config{}), reviews.New(reviews.Config{})
//	app, err := gocommerce.New(cfg, fd, rv, googlereviews.New(googlereviews.Config{Feeds: fd, Reviews: rv}))
package googlereviews

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	feeds "github.com/itswadesh/gocommerce/ext/feeds"
	reviews "github.com/itswadesh/gocommerce/ext/reviews"
)

const pluginKey = "google-reviews"

// Config configures the module. Everything a store chooses is on the Plugins
// screen.
type Config struct {
	// Feeds and Reviews are what the product ratings feed is made of: the
	// store's reviews, naming products the way its product feed does. Either
	// nil and that feed answers that it cannot be built.
	Feeds   *feeds.Module
	Reviews *reviews.Module
	// PlacesURL replaces https://places.googleapis.com. Tests set it;
	// nothing else should.
	PlacesURL string
	// Client replaces the HTTP client. The address is Google's, not one a
	// store chose, so it needs no outbound guard (AGENTS rule 16).
	Client *http.Client
}

// Module is the routes.
type Module struct {
	cfg    Config
	app    *gocommerce.App
	client *http.Client

	mu      sync.Mutex
	lookups map[string]dayCount
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg, lookups: map[string]dayCount{}} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "google-reviews" }

// Migrations implements gocommerce.Module. None: nothing Google sends may be
// kept, and the place ID is a setting.
func (m *Module) Migrations() []gocommerce.Migration { return nil }

// The opt-in's positions, as the panel names them, and as Google does.
var optInStyles = map[string]string{
	"Centre": "CENTER_DIALOG", "Bottom right": "BOTTOM_RIGHT_DIALOG", "Bottom left": "BOTTOM_LEFT_DIALOG",
	"Top right": "TOP_RIGHT_DIALOG", "Top left": "TOP_LEFT_DIALOG", "Bottom tray": "BOTTOM_TRAY",
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	if m.cfg.PlacesURL == "" {
		m.cfg.PlacesURL = "https://places.googleapis.com"
	}
	m.cfg.PlacesURL = strings.TrimRight(m.cfg.PlacesURL, "/")
	m.client = m.cfg.Client
	if m.client == nil {
		m.client = &http.Client{Timeout: 10 * time.Second}
	}
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: pluginKey, Title: "Google reviews", Icon: "ri-google-line", Category: "marketing",
		Description: "Three things, each switched on below. Your Google rating and latest reviews on the storefront, " +
			"read live from Google Maps with Google's attribution. Google Customer Reviews: Google's survey offered after checkout, " +
			"and its store widget. A product ratings feed of your own reviews at /x/google-reviews/product-reviews.xml, for Merchant Center.",
		Docs: "https://developers.google.com/maps/documentation/places/web-service/place-id",
		Fields: []gocommerce.PluginField{
			{Key: "show_place", Label: "Show your Google rating and reviews", Kind: "bool", Public: true,
				Help: "The Google reviews section on the home page shows them. Google sends its five most relevant reviews at most, and the section links to the rest on Google Maps."},
			{Key: "api_key", Label: "Google Maps Platform API key", Kind: "secret",
				Help: "From Google Cloud: enable Places API (New) and restrict the key to it. Each time a shopper scrolls to the section is one Place Details call, which Google bills."},
			{Key: "place_id", Label: "Place ID", Kind: "text",
				Help: "Your Business Profile's ID, starting ChIJ. Search your shop's name in Google's Place ID Finder (the Docs link) and paste what it shows."},
			{Key: "daily_lookups", Label: "Most Google lookups a day", Kind: "number", Default: 30,
				Help: "After this many the section hides until tomorrow, so nobody can run up your Google bill. Google gives 1,000 a month free; 30 a day stays inside that."},
			{Key: "customer_reviews", Label: "Offer the Google Customer Reviews survey", Kind: "bool", Public: true,
				Help: "After checkout, Google asks the shopper whether to email them a survey after delivery. Join Google Customer Reviews in Merchant Center first, and say in your privacy policy that Google receives order details."},
			{Key: "merchant_id", Label: "Merchant Center ID", Kind: "text", Help: "The number at the top right of Merchant Center."},
			{Key: "delivery_days", Label: "Delivery estimate in days", Kind: "number",
				Help: "Days from the order to delivery. Google sends its survey after this date. With no estimate the survey is not offered."},
			{Key: "opt_in_style", Label: "Where the survey offer appears", Kind: "select",
				Options: []string{"Centre", "Bottom right", "Bottom left", "Top right", "Top left", "Bottom tray"},
				Help:    "Empty is Google's default, the centre of the screen."},
			{Key: "store_widget", Label: "Google Customer Reviews store widget", Kind: "select", Public: true,
				Options: []string{"Bottom right", "Bottom left"},
				Help:    "Google's badge with your store rating, on every page. Empty shows none. It needs the survey switched on."},
			{Key: "product_ratings", Label: "Serve the product ratings feed", Kind: "bool",
				Help: "Your verified, published product reviews, for Merchant Center's Product Ratings. It needs Product reviews and Product feeds on. Google asks for every review, low ratings too, so moderate only spam."},
		},
	})
	app.HandleFunc("GET /x/google-reviews/place", m.handlePlace)
	app.HandleFunc("GET /x/google-reviews/survey/{number}", m.handleSurvey)
	app.HandleFunc("GET /x/google-reviews/product-reviews.xml", m.handleFeed)
	return nil
}

// settings is the plugin's values, with "off" folded in: a switch reads off
// while the plugin is.
type settings struct {
	showPlace, customerReviews, productRatings bool
	apiKey, placeID, merchantID, optInStyle    string
	dailyLookups, deliveryDays                 int
}

func (m *Module) settings(ctx context.Context) (settings, error) {
	on, err := m.app.Plugins().Enabled(ctx, pluginKey)
	if err != nil || !on {
		return settings{}, err
	}
	values, err := m.app.Plugins().Settings(ctx, pluginKey)
	if err != nil {
		return settings{}, err
	}
	str := func(key string) string { s, _ := values[key].(string); return strings.TrimSpace(s) }
	flag := func(key string) bool { b, _ := values[key].(bool); return b }
	// A stored number is a float64; an unset one reads as its Default, an int.
	num := func(key string) int {
		switch n := values[key].(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
		return 0
	}
	return settings{
		showPlace: flag("show_place"), customerReviews: flag("customer_reviews"), productRatings: flag("product_ratings"),
		apiKey: str("api_key"), placeID: str("place_id"), merchantID: str("merchant_id"),
		optInStyle: optInStyles[str("opt_in_style")], dailyLookups: num("daily_lookups"), deliveryDays: num("delivery_days"),
	}, nil
}
