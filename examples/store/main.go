// Command store is the canonical shape of a gocommerce store: a small main()
// that composes the engine with the capabilities this particular business
// needs. Everything installed is visible on one screen, and "go to definition"
// works on all of it.
//
// Run it against a local database:
//
//	createdb mystore
//	DATABASE_URL=postgres://localhost/mystore \
//	GOCOMMERCE_ADMIN_TOKEN=$(openssl rand -hex 32) \
//	go run ./examples/store
//
// Every module below is optional. With none of them the store still sells:
// cash on delivery and manual fulfillment are built in, because they need no
// third party.
package main

import (
	"log"
	"log/slog"
	"os"

	"github.com/itswadesh/gocommerce/core"

	"github.com/itswadesh/gocommerce/ext/cms"
	"github.com/itswadesh/gocommerce/ext/contact"
	"github.com/itswadesh/gocommerce/ext/faq"
	"github.com/itswadesh/gocommerce/ext/feeds"
	delhivery "github.com/itswadesh/gocommerce/ext/fulfill-delhivery"
	easyship "github.com/itswadesh/gocommerce/ext/fulfill-easyship"
	indiapost "github.com/itswadesh/gocommerce/ext/fulfill-indiapost"
	nimbuspost "github.com/itswadesh/gocommerce/ext/fulfill-nimbuspost"
	onfleet "github.com/itswadesh/gocommerce/ext/fulfill-onfleet"
	shippit "github.com/itswadesh/gocommerce/ext/fulfill-shippit"
	shippo "github.com/itswadesh/gocommerce/ext/fulfill-shippo"
	shiprocket "github.com/itswadesh/gocommerce/ext/fulfill-shiprocket"
	shipstation "github.com/itswadesh/gocommerce/ext/fulfill-shipstation"
	usps "github.com/itswadesh/gocommerce/ext/fulfill-usps"
	veeqo "github.com/itswadesh/gocommerce/ext/fulfill-veeqo"
	"github.com/itswadesh/gocommerce/ext/identity"
	amazon "github.com/itswadesh/gocommerce/ext/import-amazon"
	shopify "github.com/itswadesh/gocommerce/ext/import-shopify"
	"github.com/itswadesh/gocommerce/ext/indexnow"
	"github.com/itswadesh/gocommerce/ext/invoices"
	"github.com/itswadesh/gocommerce/ext/klaviyo"
	"github.com/itswadesh/gocommerce/ext/mcp"
	"github.com/itswadesh/gocommerce/ext/navigation"
	"github.com/itswadesh/gocommerce/ext/newsletter"
	msg91 "github.com/itswadesh/gocommerce/ext/notify-msg91"
	sendgrid "github.com/itswadesh/gocommerce/ext/notify-sendgrid"
	adyen "github.com/itswadesh/gocommerce/ext/payments-adyen"
	helcim "github.com/itswadesh/gocommerce/ext/payments-helcim"
	hyperswitch "github.com/itswadesh/gocommerce/ext/payments-hyperswitch"
	lemonsqueezy "github.com/itswadesh/gocommerce/ext/payments-lemonsqueezy"
	paddle "github.com/itswadesh/gocommerce/ext/payments-paddle"
	razorpay "github.com/itswadesh/gocommerce/ext/payments-razorpay"
	revenuecat "github.com/itswadesh/gocommerce/ext/payments-revenuecat"
	stripe "github.com/itswadesh/gocommerce/ext/payments-stripe"
	"github.com/itswadesh/gocommerce/ext/reviews"
	meilisearch "github.com/itswadesh/gocommerce/ext/search-meilisearch"
	"github.com/itswadesh/gocommerce/ext/sitemaps"
	"github.com/itswadesh/gocommerce/ext/wishlist"
)

func main() {
	log.SetFlags(0)

	modules := []gocommerce.Module{
		// Numbered invoices whenever an order is paid. It subscribes to
		// order.paid and owns its own tables; the engine knows nothing about
		// invoicing.
		invoices.New(invoices.Config{
			SellerName:    "Example Ltd",
			SellerAddress: "1 Commerce Way, London",
			NumberFormat:  "INV-{year}-{seq:05}",
		}),

		// Content pages, served at /x/cms/pages/{slug}.
		cms.New(cms.Config{}),

		// Plugins: installed here, switched on and configured from
		// the Plugins screen. The environment is the fallback.
		meilisearch.New(meilisearch.Config{
			Host: os.Getenv("MEILI_HOST"), APIKey: os.Getenv("MEILI_API_KEY"), SearchKey: os.Getenv("MEILI_SEARCH_KEY"),
		}),
		klaviyo.New(klaviyo.Config{
			PrivateKey: os.Getenv("KLAVIYO_PRIVATE_KEY"), PublicKey: os.Getenv("KLAVIYO_PUBLIC_KEY"),
		}),
		feeds.New(feeds.Config{StorefrontURL: os.Getenv("STOREFRONT_URL")}),
		sitemaps.New(sitemaps.Config{StorefrontURL: os.Getenv("STOREFRONT_URL")}),

		// The storefront's own content: its menus, its reviews, its contact
		// form and its newsletter list, each with a screen in the panel.
		navigation.New(navigation.Config{}),
		reviews.New(reviews.Config{}),
		contact.New(contact.Config{NotifyEmail: os.Getenv("CONTACT_EMAIL")}),
		newsletter.New(newsletter.Config{}),
		faq.New(faq.Config{}),
		wishlist.New(wishlist.Config{}),

		// The store as tools for an AI agent, at /api/admin/x/mcp. The admin
		// token is the agent's credential, and every change it makes is
		// recorded in an audit table.
		mcp.New(mcp.Config{ServerName: "example-store"}),

		// Shopper accounts, at /x/identity/. Optional in the strongest
		// sense: guest checkout keeps working exactly as before, and an
		// account only adds saved addresses and a claimable order history.
		// The reset email goes through whichever notifier is installed below.
		identity.New(identity.Config{
			ResetURL: "https://shop.example.com/auth/reset-password?token={token}",
		}),

		// Products from Amazon listings, through the Chrome installed on this
		// machine. Nothing is started until an operator asks for an import,
		// so a server without Chrome boots all the same and the first job says
		// what is missing. The copy is rewritten by whichever model is
		// configured — an Anthropic, Gemini or OpenAI key, or a local server —
		// and used as scraped when none is; with IMPORT_AMAZON_HEADED set the
		// browser is visible, which is what gets a person past Amazon's robot
		// check once.
		amazon.New(amazon.Config{
			AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
			GeminiAPIKey:    os.Getenv("GEMINI_API_KEY"),
			OpenAIAPIKey:    os.Getenv("OPENAI_API_KEY"),
			LLMBaseURL:      os.Getenv("IMPORT_AMAZON_LLM_URL"),
			Model:           os.Getenv("IMPORT_AMAZON_LLM_MODEL"),
			Headed:          os.Getenv("IMPORT_AMAZON_HEADED") != "",
		}),
		// A whole Shopify catalogue over its Admin API. No configuration here:
		// the shop domain and the access token are plugin settings, so a
		// migrating merchant fills them in once on the plugins screen rather
		// than being handed an environment variable to set on a server they
		// may not have.
		shopify.New(),

		// Tell Bing, Yandex, Seznam and Naver when a page changes. Nothing to
		// configure here: the storefront URL is a plugin setting, and the key
		// file is served by the storefront rather than by this engine — see
		// the package comment for why it cannot be otherwise.
		indexnow.New(),
	}

	// Card payments, if the keys are configured. Adding Stripe changes no
	// core code: it registers a payment method, and the engine serves its
	// webhook at /api/checkout/stripe/webhook.
	if key := os.Getenv("STRIPE_SECRET_KEY"); key != "" {
		modules = append(modules, stripe.New(stripe.Config{
			SecretKey:     key,
			WebhookSecret: mustEnv("STRIPE_WEBHOOK_SECRET"),
		}))
	}

	// Real order emails and texts. With nothing in the environment both are
	// installed idle: the engine still emits every notification — to the log
	// — until the key and the sender are typed into Notifications › Setup
	// Email or Setup SMS, at which point they start going to shoppers.
	modules = append(modules,
		sendgrid.New(sendgrid.Config{
			APIKey:   os.Getenv("SENDGRID_API_KEY"),
			From:     os.Getenv("SENDGRID_FROM"),
			FromName: os.Getenv("SENDGRID_FROM_NAME"),
		}),
		msg91.New(msg91.Config{AuthKey: os.Getenv("MSG91_AUTH_KEY")}),
	)

	// Every gateway and carrier, installed idle: each is a card on Payment
	// methods or Shipping providers, switched on and given its keys from
	// there. Stripe keeps its environment fallback below, the way it always
	// had one; the rest wait for the panel.
	modules = append(modules,
		razorpay.New(razorpay.Config{}), adyen.New(adyen.Config{}), paddle.New(paddle.Config{}),
		lemonsqueezy.New(lemonsqueezy.Config{}), helcim.New(helcim.Config{}), hyperswitch.New(hyperswitch.Config{}),
		revenuecat.New(revenuecat.Config{}),
		shiprocket.New(shiprocket.Config{}), delhivery.New(delhivery.Config{}), nimbuspost.New(nimbuspost.Config{}),
		indiapost.New(indiapost.Config{}), shippo.New(shippo.Config{}), shipstation.New(shipstation.Config{}),
		easyship.New(easyship.Config{}), shippit.New(shippit.Config{}), usps.New(usps.Config{}),
		onfleet.New(onfleet.Config{}), veeqo.New(veeqo.Config{}),
	)

	app, err := gocommerce.New(gocommerce.Config{
		DBURL:  mustEnv("DATABASE_URL"),
		Addr:   ":8080",
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),

		// One settlement currency per store. Money crosses the API as integer
		// minor units plus this code, so switching to JPY or KWD — which have
		// 0 and 3 decimal places — needs no engine change.
		Currency: "USD",

		// The first language is the default; the engine negotiates the rest
		// per request from ?lang= and Accept-Language.
		Languages: []string{"en"},

		// Several tokens may be configured so one can be rotated out without
		// a window where no token works.
		AdminTokens: []string{mustEnv("GOCOMMERCE_ADMIN_TOKEN")},

		// AdminAuth is the seam where an identity module would replace bearer
		// tokens with sessions, OIDC or RBAC. Guest checkout keeps working
		// either way: shoppers never authenticate.
	}, modules...)
	if err != nil {
		log.Fatal(err)
	}

	// Returns nil after a clean SIGINT/SIGTERM shutdown.
	if err := app.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("environment variable %s is required", key)
	}
	return v
}
