// Command gocommerce is the reference binary: a store with no modules
// installed. It is useful for migrating a database, inspecting the API
// contract, and proving the engine boots.
//
// A real store is its own tiny main() that composes the engine with the
// modules it needs — see examples/store.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/ext/b2b"
	"github.com/itswadesh/gocommerce/ext/banners"
	cartrecovery "github.com/itswadesh/gocommerce/ext/cart-recovery"
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
	"github.com/itswadesh/gocommerce/ext/invoices"
	"github.com/itswadesh/gocommerce/ext/klaviyo"
	"github.com/itswadesh/gocommerce/ext/navigation"
	"github.com/itswadesh/gocommerce/ext/newsletter"
	msg91 "github.com/itswadesh/gocommerce/ext/notify-msg91"
	resend "github.com/itswadesh/gocommerce/ext/notify-resend"
	sendgrid "github.com/itswadesh/gocommerce/ext/notify-sendgrid"
	twilio "github.com/itswadesh/gocommerce/ext/notify-twilio"
	adyen "github.com/itswadesh/gocommerce/ext/payments-adyen"
	clover "github.com/itswadesh/gocommerce/ext/payments-clover"
	creem "github.com/itswadesh/gocommerce/ext/payments-creem"
	helcim "github.com/itswadesh/gocommerce/ext/payments-helcim"
	hyperswitch "github.com/itswadesh/gocommerce/ext/payments-hyperswitch"
	lemonsqueezy "github.com/itswadesh/gocommerce/ext/payments-lemonsqueezy"
	paddle "github.com/itswadesh/gocommerce/ext/payments-paddle"
	razorpay "github.com/itswadesh/gocommerce/ext/payments-razorpay"
	revenuecat "github.com/itswadesh/gocommerce/ext/payments-revenuecat"
	stripe "github.com/itswadesh/gocommerce/ext/payments-stripe"
	"github.com/itswadesh/gocommerce/ext/reviews"
	meilisearch "github.com/itswadesh/gocommerce/ext/search-meilisearch"
	"github.com/itswadesh/gocommerce/ext/showcases"
	"github.com/itswadesh/gocommerce/ext/sitemaps"
	"github.com/itswadesh/gocommerce/ext/translations"
	webhooks "github.com/itswadesh/gocommerce/ext/webhooks"
	"github.com/itswadesh/gocommerce/ext/wishlist"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "gocommerce:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("gocommerce", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `GoCommerce — a small, composable commerce engine for Go

usage:
  gocommerce [flags] <command>

commands:
  serve      apply migrations, then serve the API (default)
  migrate    apply migrations and exit
  superuser  create or update an admin-panel operator, then exit
  store      create a store: store create <slug> [name]; or mark one a demo
             (masked personal data): store demo <slug> on|off; then exit
  doctor     run operational diagnostics and exit (-json for agents)
  secrets    secrets seal: seal every stored secret in every store under
             GOCOMMERCE_SECRETS_KEY, including what the previous key sealed;
             run it with the same module flags as serve, then exit
  spec       print the OpenAPI contract and exit
  taxonomy   import a product category tree, then exit
  attributes import the fields categories ask of a product, then exit
  version    print the engine version and exit

  superuser usage:
    gocommerce superuser create <email> <password>
    gocommerce superuser update <email> <password>
    gocommerce superuser list

  taxonomy usage:
    gocommerce taxonomy import            Shopify's standard taxonomy (embedded),
                                          and the fields that go with it
    gocommerce taxonomy import <file>     the same format, from a file or -

  attributes usage:
    gocommerce attributes import          Shopify's field definitions (embedded),
                                          for a tree already imported
    gocommerce attributes import <file>   the same format, from a file or -

flags:
`)
		fs.PrintDefaults()
		fmt.Fprint(fs.Output(), `
environment:
  DATABASE_URL      PostgreSQL connection string, used when -db is not given
  GOCOMMERCE_REPORTS_DATABASE_URL
                    PostgreSQL connection string for a read-only role that
                    custom reports run as, used when -reports-db is not given.
                    It must name a non-superuser role granted only SELECT (see
                    scripts/reports-role.sql). Leaving it unset disables custom
                    reports: saving and listing still work, running one does not
  GOCOMMERCE_ADMIN_TOKEN
                    admin bearer token, used when -admin-token is not given
                    (several may be given, comma-separated)
  GOCOMMERCE_ADMIN_EMAIL, GOCOMMERCE_ADMIN_PASSWORD
                    if set, "serve" creates this superuser when the database
                    has none, so an unattended deploy comes up signed-in-able
  GOCOMMERCE_PANEL_URL
                    where this store's panel is reached, e.g.
                    https://shop.example.com: the base of a password-reset
                    link, and of a recovery email's link, which counts the
                    click before sending the shopper to their basket
  GOCOMMERCE_CORS_ORIGINS
                    browser origins allowed to call the API directly,
                    comma-separated, e.g. https://shop.example.com, or * for
                    any; unset, a storefront on another origin has to proxy
  GOCOMMERCE_MEDIA_DIR
                    where uploaded media is written, used when -media-dir is
                    not given; leaving it unset disables uploads and the media
                    library records files by URL only
  GOCOMMERCE_IDENTITY_RESET_URL
                    with -identity, the storefront page a password-reset email
                    links to, with {token} where the token goes
  GOCOMMERCE_IDENTITY_VERIFY_URL
                    with -identity, the storefront page an email-confirmation
                    message links to, with {token} where the token goes
  GOCOMMERCE_B2B_INVITE_URL
                    with -b2b, the storefront page a company invitation links
                    to, with {token} where the token goes
  GOCOMMERCE_BASE_DOMAIN
                    used when -base-domain is not given: every store also
                    answers at <slug>.<base domain>
  GOCOMMERCE_SIGNUP same as -signup. Off when unset, empty, "0", "false", "no"
                    or "off"; any other value is on
  GOCOMMERCE_DEMO   same as -demo. Off when unset, empty, "0", "false", "no"
                    or "off"; any other value is on
  GOCOMMERCE_DEMO_ACCOUNT
                    on a demo only, the email of an existing operator that
                    EVERY visitor is signed in as — the form then accepts any
                    email and any password. There is no flag for this: it names
                    an account that has to exist. Leave it unset and a demo
                    still asks for real credentials
  GOCOMMERCE_SECRETS_KEY
                    32 random bytes in base64 (openssl rand -base64 32) that
                    seal the secrets the engine reads back: plugin keys, the
                    copy of an API key, webhook signing secrets. Never a flag,
                    never in the database. Unset, they are stored readable.
                    Lose it and every sealed secret has to be entered again
  GOCOMMERCE_SECRETS_KEY_PREVIOUS
                    the key before GOCOMMERCE_SECRETS_KEY, while rotating: it
                    still opens what it sealed until "secrets seal" has moved
                    everything under the new key
`)
	}

	var (
		dbURL        = fs.String("db", "", "PostgreSQL URL (default $DATABASE_URL)")
		reportsDB    = fs.String("reports-db", "", "PostgreSQL URL for a read-only role custom reports run as; leaving it unset disables custom reports (default $GOCOMMERCE_REPORTS_DATABASE_URL)")
		addr         = fs.String("addr", ":8080", "listen address")
		tokens       = fs.String("admin-token", "", "admin bearer token(s), comma-separated (default $GOCOMMERCE_ADMIN_TOKEN)")
		currency     = fs.String("currency", gocommerce.DefaultCurrency, "store settlement currency (ISO 4217)")
		langs        = fs.String("languages", gocommerce.DefaultLanguage, "served languages, comma-separated; the first is the default")
		dev          = fs.Bool("dev", false, "development mode: permits booting with no admin token")
		demo         = fs.Bool("demo", false, "public demo: mask every email address and phone number on admin responses and freeze operators, keys and roles; with $GOCOMMERCE_DEMO_ACCOUNT set, any email and password sign in as that account (default $GOCOMMERCE_DEMO)")
		signup       = fs.Bool("signup", false, "anybody may make an account on the sign-in screen and open a store with it, for an install that hosts stores for other people (default $GOCOMMERCE_SIGNUP)")
		verbose      = fs.Bool("v", false, "verbose (debug) logging")
		jsonOut      = fs.Bool("json", false, "machine-readable output (doctor)")
		mediaDir     = fs.String("media-dir", "", "directory for uploaded media (default $GOCOMMERCE_MEDIA_DIR; empty disables uploads)")
		withIdentity = fs.Bool("identity", false, "install the identity module: shopper accounts under /x/identity/ (guest checkout stays)")
		withWebhooks = fs.Bool("webhooks", false, "install the webhooks module: POST this store's events to endpoints you register")
		withSearch   = fs.Bool("meilisearch", false, "install the meilisearch module: a search index kept in step with the catalogue (MEILI_HOST, MEILI_API_KEY, MEILI_SEARCH_KEY, or the Plugins screen)")
		withKlaviyo  = fs.Bool("klaviyo", false, "install the klaviyo module: orders and abandoned carts as Klaviyo events (KLAVIYO_PRIVATE_KEY, KLAVIYO_PUBLIC_KEY, or the Plugins screen)")
		withFeeds    = fs.Bool("feeds", false, "install the feeds module: Google Merchant and Meta catalogue feeds at /x/feeds/ (STOREFRONT_URL, or the Plugins screen)")
		withSitemaps = fs.Bool("sitemaps", false, "install the sitemaps module: the storefront's sitemap at /x/sitemaps/sitemap.xml (STOREFRONT_URL, or the Plugins screen)")
		withMenus    = fs.Bool("menus", false, "install the navigation module: the storefront's menus, edited from the Menus screen")
		withReviews  = fs.Bool("reviews", false, "install the reviews module: product ratings and reviews, moderated from the Reviews screen")
		withContact  = fs.Bool("contact", false, "install the contact module: the storefront's contact form and its inbox (CONTACT_EMAIL, or the Plugins screen)")
		withNews     = fs.Bool("newsletter", false, "install the newsletter module: the storefront's signup box and its list")
		withRecovery = fs.Bool("recovery", false, "install the cart-recovery module: abandoned checkouts, a reminder sequence by email, and the recovery figures (STOREFRONT_URL, or the automation screen)")
		withResend   = fs.Bool("resend", false, "install the Resend module: the store's emails through Resend, and the one to reach for first — an API key is the only required setting (RESEND_API_KEY, RESEND_FROM, or Notifications › Setup Email)")
		withSendgrid = fs.Bool("sendgrid", false, "install the SendGrid module: the store's emails through SendGrid (SENDGRID_API_KEY, SENDGRID_FROM, or Notifications › Setup Email)")
		withTwilio   = fs.Bool("twilio", false, "install the Twilio module: the store's SMS through Twilio, with the wording from Notifications › Setup SMS (TWILIO_ACCOUNT_SID, TWILIO_AUTH_TOKEN, TWILIO_FROM)")
		withMsg91    = fs.Bool("msg91", false, "install the MSG91 module: the store's SMS through MSG91 (MSG91_AUTH_KEY, or Notifications › Setup SMS)")
		withGateways = fs.Bool("gateways", false, "install every payment gateway module idle — Stripe, Razorpay, Adyen, Paddle, Lemon Squeezy, Creem, Helcim, Clover, Hyperswitch, RevenueCat — each switched on and given its keys under Settings › Payment methods")
		withCarriers = fs.Bool("carriers", false, "install every carrier module idle — Shiprocket, Delhivery, NimbusPost, India Post, Shippo, ShipStation, Easyship, Shippit, USPS, Onfleet, Veeqo — each switched on and given its keys under Settings › Shipping providers")
		withInvoices = fs.Bool("invoices", false, "install the invoices module: a numbered invoice per paid order (INVOICES_SELLER_NAME, INVOICES_SELLER_ADDRESS, INVOICES_TAX_ID)")
		withCMS      = fs.Bool("cms", false, "install the cms module: content pages at /x/cms/pages/{slug}, edited on the Pages screen")
		withFAQ      = fs.Bool("faq", false, "install the faq module: the shop's questions and answers at /x/faq, edited on the FAQ screen")
		withShow     = fs.Bool("showcases", false, "install the showcases module: videos of the store showing its products, from YouTube or Facebook, beside those products at /x/showcases")
		withBanners  = fs.Bool("banners", false, "install the banners module: pictures on the storefront's pages, slides across the top and grids of tiles under a heading, served at /x/banners")
		withTransl   = fs.Bool("translations", false, "install the translations module: products, categories and collections in each of a store's languages, written on the Translations screen")
		withWishlist = fs.Bool("wishlist", false, "install the wishlist module: shoppers save products, and the Wishlists screen shows what is wanted most")
		withB2B      = fs.Bool("b2b", false, "install the b2b module: companies, buyer roles, orders on account against a credit limit, approvals and quotes (implies -identity)")
		// A store is found by host as well as by header and cookie: a request
		// to <slug>.<base-domain> is that store's (D83).
		baseDomain = fs.String("base-domain", "", "every store also answers at <slug>.<base-domain> (default $GOCOMMERCE_BASE_DOMAIN)")
		// The store a one-shot command works on: superuser, doctor, taxonomy,
		// attributes. Unneeded while the install has one store.
		storeFlag  = fs.String("store", "", "the store (id or slug) superuser, doctor, taxonomy and attributes act on; default: the only store")
		withAmazon = fs.Bool("import-amazon", false, "install the import-amazon module: create products from Amazon listings through a real Chrome (ANTHROPIC_API_KEY, GEMINI_API_KEY, OPENAI_API_KEY, or IMPORT_AMAZON_LLM_URL + IMPORT_AMAZON_LLM_MODEL for a local model rewrite the copy; IMPORT_AMAZON_HEADED=1 shows the browser)")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	command := "serve"
	if fs.NArg() > 0 {
		command = fs.Arg(0)
	}
	if command == "version" {
		fmt.Println("GoCommerce", gocommerce.Version)
		return nil
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if *dbURL == "" {
		*dbURL = os.Getenv("DATABASE_URL")
	}
	if *dbURL == "" {
		return errors.New("no database URL: pass -db or set DATABASE_URL")
	}
	if *reportsDB == "" {
		*reportsDB = os.Getenv("GOCOMMERCE_REPORTS_DATABASE_URL")
	}
	if *tokens == "" {
		*tokens = os.Getenv("GOCOMMERCE_ADMIN_TOKEN")
	}

	// Only `serve` exposes an HTTP surface, so only `serve` needs an admin
	// token. Requiring one for migrate/superuser/doctor/spec would mean the
	// diagnostic command refuses to run on precisely the store it exists to
	// diagnose: one whose operators are superusers and which has no static
	// token at all.
	offline := command != "serve"

	languages := splitList(*langs)
	cfg := gocommerce.Config{
		DBURL:        *dbURL,
		ReportsDBURL: *reportsDB,
		Addr:         *addr,
		Currency:     *currency,
		Languages:    languages,
		AdminTokens:  splitList(*tokens),
		Dev:          *dev || offline,
		// Not or-ed with offline the way Dev is: masking is about what the HTTP
		// surface publishes, and a store that is a demo is one whichever command
		// is running against it.
		//
		// envTrue rather than the != "" this file uses for every other variable:
		// those switch a module on, and this one lets anybody sign in. A
		// deployment that wrote GOCOMMERCE_DEMO=false meaning "off" and got a
		// panel open to the internet would have been right to expect better.
		Demo: *demo || envTrue("GOCOMMERCE_DEMO"),
		// The one account a demo visitor becomes. Env only: it names an
		// operator that has to exist, which is a deployment fact, not a flag.
		DemoAccount: os.Getenv("GOCOMMERCE_DEMO_ACCOUNT"),
		// Env only, like the demo account: where the panel is reached is a fact
		// about the deployment. It is the base of a password-reset link and of a
		// recovery email's tracked link.
		PanelURL: os.Getenv("GOCOMMERCE_PANEL_URL"),
		// Whether strangers may make an account and open a store: a choice
		// about who this install is for, so a flag as well as the environment.
		Signup: *signup || envTrue("GOCOMMERCE_SIGNUP"),
		// Env only, for the same reason: which storefronts call this engine
		// is a fact about the deployment.
		CORSOrigins: corsOrigins(os.Getenv("GOCOMMERCE_CORS_ORIGINS")),
		// Env only, and deliberately never a flag: a flag is readable by
		// anyone who can list the processes on the machine.
		SecretsKey:         os.Getenv(gocommerce.SecretsKeyEnv),
		SecretsKeyPrevious: os.Getenv(gocommerce.SecretsKeyPreviousEnv),
		Logger:             log,
	}
	if *mediaDir == "" {
		*mediaDir = os.Getenv("GOCOMMERCE_MEDIA_DIR")
	}
	cfg.MediaDir = *mediaDir
	if len(languages) > 0 {
		cfg.DefaultLanguage = languages[0]
	}

	// The reference binary installs no module by default — that is what
	// proves the engine boots on its own. Accounts are the one capability a
	// storefront asks for often enough that a flag beats a fork of main().
	//
	// A factory rather than a list: domain fills {domain} in the URL
	// settings. One engine serves every store (D83), so it is called once,
	// with no domain.
	buildModules := func(domain string) []gocommerce.Module {
		env := func(key string) string {
			v := os.Getenv(key)
			if domain != "" {
				v = strings.ReplaceAll(v, "{domain}", domain)
			}
			return v
		}
		var modules []gocommerce.Module
		// b2b's buyers are identity's accounts, so -b2b brings identity with it
		// and is handed the same instance, listed first for its tables.
		if *withIdentity || *withB2B {
			accounts := identity.New(identity.Config{
				ResetURL:  env("GOCOMMERCE_IDENTITY_RESET_URL"),
				VerifyURL: env("GOCOMMERCE_IDENTITY_VERIFY_URL"),
			})
			modules = append(modules, accounts)
			if *withB2B {
				modules = append(modules, b2b.New(b2b.Config{
					Accounts:  accounts,
					InviteURL: env("GOCOMMERCE_B2B_INVITE_URL"),
				}))
			}
		}
		if *withWebhooks {
			modules = append(modules, webhooks.New(webhooks.Config{}))
		}
		// The four below are plugins as much as modules: installed here, but
		// switched on and configured from the Plugins screen, with the
		// environment as the fallback for a store that prefers it.
		if *withSearch {
			modules = append(modules, meilisearch.New(meilisearch.Config{
				Host: os.Getenv("MEILI_HOST"), APIKey: os.Getenv("MEILI_API_KEY"), SearchKey: os.Getenv("MEILI_SEARCH_KEY"),
			}))
		}
		if *withKlaviyo {
			modules = append(modules, klaviyo.New(klaviyo.Config{
				PrivateKey: os.Getenv("KLAVIYO_PRIVATE_KEY"), PublicKey: os.Getenv("KLAVIYO_PUBLIC_KEY"),
			}))
		}
		if *withFeeds {
			modules = append(modules, feeds.New(feeds.Config{StorefrontURL: env("STOREFRONT_URL")}))
		}
		if *withSitemaps {
			modules = append(modules, sitemaps.New(sitemaps.Config{StorefrontURL: env("STOREFRONT_URL")}))
		}
		if *withMenus {
			modules = append(modules, navigation.New(navigation.Config{}))
		}
		if *withReviews {
			modules = append(modules, reviews.New(reviews.Config{}))
		}
		if *withContact {
			modules = append(modules, contact.New(contact.Config{NotifyEmail: os.Getenv("CONTACT_EMAIL")}))
		}
		if *withNews {
			modules = append(modules, newsletter.New(newsletter.Config{}))
		}
		if *withRecovery {
			modules = append(modules, cartrecovery.New(cartrecovery.Config{StorefrontURL: env("STOREFRONT_URL")}))
		}
		// The delivery backends. With nothing in the environment they are
		// installed idle and wait for the Setup Email / Setup SMS screens.
		// Resend first, because it is the one that works with one setting: a key
		// and nothing else sends, from Resend's own onboarding address, until the
		// store has a domain of its own to verify.
		if *withResend {
			modules = append(modules, resend.New(resend.Config{
				APIKey: os.Getenv("RESEND_API_KEY"), From: firstEnv("RESEND_FROM", "DEFAULT_EMAIL_FROM_ADDRESS"),
				FromName: os.Getenv("RESEND_FROM_NAME"),
			}))
		}
		if *withSendgrid {
			modules = append(modules, sendgrid.New(sendgrid.Config{
				APIKey: os.Getenv("SENDGRID_API_KEY"), From: firstEnv("SENDGRID_FROM", "DEFAULT_EMAIL_FROM_ADDRESS"),
				FromName: os.Getenv("SENDGRID_FROM_NAME"),
			}))
		}
		// Twilio before MSG91 for the reason Resend comes before SendGrid: it is
		// the one that works anywhere, because its wording is the store's own.
		// MSG91 needs every message registered with a carrier first, which is the
		// right module in India and a week of waiting everywhere else.
		if *withTwilio {
			modules = append(modules, twilio.New(twilio.Config{
				AccountSID: os.Getenv("TWILIO_ACCOUNT_SID"), AuthToken: os.Getenv("TWILIO_AUTH_TOKEN"),
				From: os.Getenv("TWILIO_FROM"), MessagingServiceSID: os.Getenv("TWILIO_MESSAGING_SERVICE_SID"),
			}))
		}
		if *withMsg91 {
			modules = append(modules, msg91.New(msg91.Config{AuthKey: firstEnv("MSG91_AUTH_KEY", "MSG91_AUTHKEY")}))
		}
		// Gateways and carriers install idle: nothing in Config, everything on
		// the Payment methods and Shipping providers screens. A store that
		// prefers the environment wires the module itself in its own main().
		if *withGateways {
			modules = append(modules,
				stripe.New(stripe.Config{}), razorpay.New(razorpay.Config{}), adyen.New(adyen.Config{}),
				paddle.New(paddle.Config{}), lemonsqueezy.New(lemonsqueezy.Config{}), creem.New(creem.Config{}),
				helcim.New(helcim.Config{}), clover.New(clover.Config{}),
				hyperswitch.New(hyperswitch.Config{}), revenuecat.New(revenuecat.Config{}),
			)
		}
		if *withCarriers {
			modules = append(modules,
				shiprocket.New(shiprocket.Config{}), delhivery.New(delhivery.Config{}), nimbuspost.New(nimbuspost.Config{}),
				indiapost.New(indiapost.Config{}), shippo.New(shippo.Config{}), shipstation.New(shipstation.Config{}),
				easyship.New(easyship.Config{}), shippit.New(shippit.Config{}), usps.New(usps.Config{}),
				onfleet.New(onfleet.Config{}), veeqo.New(veeqo.Config{}),
			)
		}
		if *withInvoices {
			seller := os.Getenv("INVOICES_SELLER_NAME")
			if seller == "" {
				seller = "This store"
			}
			modules = append(modules, invoices.New(invoices.Config{
				SellerName: seller, SellerAddress: os.Getenv("INVOICES_SELLER_ADDRESS"), TaxID: os.Getenv("INVOICES_TAX_ID"),
			}))
		}
		if *withCMS {
			modules = append(modules, cms.New(cms.Config{}))
		}
		if *withFAQ {
			modules = append(modules, faq.New(faq.Config{}))
		}
		if *withTransl {
			modules = append(modules, translations.New())
		}
		if *withShow {
			modules = append(modules, showcases.New(showcases.Config{}))
		}
		if *withBanners {
			modules = append(modules, banners.New(banners.Config{}))
		}
		if *withWishlist {
			modules = append(modules, wishlist.New(wishlist.Config{}))
		}
		if *withAmazon {
			modules = append(modules, amazon.New(amazon.Config{
				AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
				GeminiAPIKey:    os.Getenv("GEMINI_API_KEY"),
				OpenAIAPIKey:    os.Getenv("OPENAI_API_KEY"),
				LLMBaseURL:      os.Getenv("IMPORT_AMAZON_LLM_URL"),
				Model:           os.Getenv("IMPORT_AMAZON_LLM_MODEL"),
				ChromePath:      os.Getenv("CHROME_PATH"),
				Headed:          os.Getenv("IMPORT_AMAZON_HEADED") != "",
			}))
		}

		return modules
	}

	if *baseDomain == "" {
		*baseDomain = os.Getenv("GOCOMMERCE_BASE_DOMAIN")
	}
	cfg.BaseDomain = *baseDomain

	app, err := gocommerce.New(cfg, buildModules("")...)
	if err != nil {
		return err
	}

	// One-shot commands act on one store. Resolved after New, which has
	// applied the migrations that create the store table.
	inStore := func() (context.Context, error) {
		return commandStore(context.Background(), app, *storeFlag)
	}

	switch command {
	case "migrate":
		defer app.Close()
		// New already applied every pending migration; calling it again is
		// harmless and makes the command's intent explicit in the logs.
		if err := app.Migrate(context.Background()); err != nil {
			return err
		}
		log.Info("migrations up to date")
		return nil

	case "store":
		defer app.Close()
		return storeCmd(context.Background(), app, fs.Args()[1:])

	case "superuser":
		defer app.Close()
		ctx, err := inStore()
		if err != nil {
			return err
		}
		return superuserCmd(ctx, app, fs.Args()[1:])

	case "doctor":
		defer app.Close()
		ctx, err := inStore()
		if err != nil {
			return err
		}
		return doctorCmd(ctx, app, *jsonOut)

	case "secrets":
		defer app.Close()
		return secretsCmd(context.Background(), app, fs.Args()[1:])

	case "spec":
		defer app.Close()
		_, err := os.Stdout.Write(app.Spec())
		return err

	case "taxonomy":
		defer app.Close()
		ctx, err := inStore()
		if err != nil {
			return err
		}
		return taxonomyCmd(ctx, app, fs.Args()[1:], log)

	case "attributes":
		defer app.Close()
		ctx, err := inStore()
		if err != nil {
			return err
		}
		return attributesCmd(ctx, app, fs.Args()[1:], log)

	case "serve":
		// The first operator belongs to the first store: the one a fresh
		// install has, and an upgraded install's own.
		first, err := commandStore(context.Background(), app, "first")
		if err != nil {
			app.Close()
			return err
		}
		if err := bootstrapSuperuser(first, app, log); err != nil {
			app.Close()
			return err
		}
		return app.ListenAndServe()

	default:
		fs.Usage()
		return fmt.Errorf("unknown command %q", command)
	}
}

// bootstrapSuperuser creates the first operator from the environment, so a
// container can come up with a usable panel and no manual step. It is
// deliberately create-only: if a superuser already exists, a stale environment
// variable must not silently reset that operator's password.
// storeCmd opens a new store, seeded so it can stock and sell. Its first
// operator is added with `superuser create` and -store.
const storeUsage = "usage: gocommerce store create <slug> [name] | gocommerce store demo <slug> on|off"

func storeCmd(ctx context.Context, app *gocommerce.App, args []string) error {
	if len(args) == 3 && args[0] == "demo" {
		return storeDemoCmd(ctx, app, args[1], args[2])
	}
	if len(args) < 2 || args[0] != "create" {
		return errors.New(storeUsage)
	}
	name := strings.Join(args[2:], " ")
	st, err := app.Stores().Create(ctx, name, args[1])
	if err != nil {
		return err
	}
	fmt.Printf("created store %s (%s)\nadd its first operator: gocommerce -store %s superuser create <email> <password> owner\n",
		st.Slug, st.ID, st.Slug)
	return nil
}

// storeDemoCmd marks a store as a demo or a live store again. It is a command
// and not a panel switch on purpose: a demo store's visitors are signed in to
// it, and a switch they could reach would let the first one unmask it.
func storeDemoCmd(ctx context.Context, app *gocommerce.App, name, state string) error {
	var on bool
	switch strings.ToLower(state) {
	case "on":
		on = true
	case "off":
	default:
		return errors.New(storeUsage)
	}
	st, err := app.Stores().BySlug(ctx, name)
	if err != nil && gocommerce.ValidStoreID(name) {
		st, err = app.Stores().Get(ctx, name)
	}
	if err != nil {
		return fmt.Errorf("no store %q", name)
	}
	if err := app.Stores().SetDemo(ctx, st.ID, on); err != nil {
		return err
	}
	if on {
		fmt.Printf("store %s is now a demo: its admin screens mask emails and phone numbers, and its team, roles and API keys are frozen\n", st.Slug)
	} else {
		fmt.Printf("store %s is live again: its admin screens show personal data in full\n", st.Slug)
	}
	return nil
}

// commandStore binds ctx to the store a one-shot command acts on: the one
// named by id or slug, "first" for the oldest, or with none named the
// install's only store. With several stores and none named it refuses rather
// than guess which store a new operator or a taxonomy import belongs to.
func commandStore(ctx context.Context, app *gocommerce.App, name string) (context.Context, error) {
	stores, err := app.Stores().Active(ctx)
	if err != nil {
		return nil, err
	}
	if len(stores) == 0 {
		return nil, errors.New("this database has no active store")
	}
	switch {
	case name == "first":
		return gocommerce.WithStore(ctx, stores[0].ID), nil
	case name == "" && len(stores) == 1:
		return gocommerce.WithStore(ctx, stores[0].ID), nil
	case name == "":
		return nil, fmt.Errorf("this install has %d stores; name one with -store <id or slug>", len(stores))
	}
	for _, st := range stores {
		if st.ID == name || st.Slug == strings.ToLower(name) {
			return gocommerce.WithStore(ctx, st.ID), nil
		}
	}
	return nil, fmt.Errorf("no active store %q", name)
}

func bootstrapSuperuser(ctx context.Context, app *gocommerce.App, log *slog.Logger) error {
	email := os.Getenv("GOCOMMERCE_ADMIN_EMAIL")
	password := os.Getenv("GOCOMMERCE_ADMIN_PASSWORD")
	if email == "" || password == "" {
		return nil
	}
	su, created, err := app.Superusers().Bootstrap(ctx, email, password)
	if err != nil {
		return fmt.Errorf("bootstrap superuser: %w", err)
	}
	if created {
		log.Info("created the first superuser from the environment", "email", su.Email)
	}
	return nil
}

// doctorCmd renders the health report for whoever is asking.
//
// It exits non-zero when a check fails, so CI and agents can gate on it
// without parsing anything; -json is for when they want the detail.
func doctorCmd(ctx context.Context, app *gocommerce.App, asJSON bool) error {
	rep := app.Diagnose(ctx)

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return err
		}
		if !rep.OK {
			os.Exit(1)
		}
		return nil
	}

	mark := map[string]string{
		gocommerce.StatusOK:   "ok  ",
		gocommerce.StatusWarn: "warn",
		gocommerce.StatusFail: "FAIL",
	}
	fmt.Printf("GoCommerce %s — %s\n\n", rep.Version, rep.At.Format(time.RFC3339))
	for _, c := range rep.Checks {
		fmt.Printf("  %s  %-18s %s\n", mark[c.Status], c.Name, c.Detail)
		if c.Hint != "" {
			fmt.Printf("        %-18s → %s\n", "", c.Hint)
		}
		// The admin API strips this and logs it instead; a local operator at a
		// terminal is not a browser session, and the driver message is usually
		// the whole answer.
		if c.Cause != "" {
			fmt.Printf("        %-18s   %s\n", "", c.Cause)
		}
	}

	fmt.Println()
	if rep.OK {
		fmt.Println("healthy")
		return nil
	}
	fmt.Printf("%d check(s) need attention\n", len(rep.Failed()))
	os.Exit(1)
	return nil
}

// secretsCmd seals what is stored readable and moves what the previous key
// sealed, in every store. It prints counts and the fields of anything it could
// not open — never a value — and is safe to run again.
func secretsCmd(ctx context.Context, app *gocommerce.App, args []string) error {
	if len(args) != 1 || args[0] != "seal" {
		return errors.New("usage: gocommerce secrets seal")
	}
	rep, err := app.SealSecrets(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("sealed under key %s, in %d store(s):\n", rep.KeyID, rep.Stores)
	var unreadable []string
	for _, c := range rep.Counts {
		fmt.Printf("  %-24s %d sealed now, %d moved off the previous key, %d already sealed\n",
			c.Label, c.Readable, c.Previous, c.Current)
		if c.Unreadable > 0 {
			unreadable = append(unreadable, fmt.Sprintf("%d %s (%s)", c.Unreadable, c.Label, strings.Join(c.Where, ", ")))
		}
	}
	if len(unreadable) > 0 {
		fmt.Printf("left as they are, because no key here opens them: %s\n"+
			"  put the key that sealed them in %s and run this again, or enter them again\n",
			strings.Join(unreadable, "; "), gocommerce.SecretsKeyPreviousEnv)
	}
	// A module installed when the store is served but not in this run keeps
	// its secrets out of reach: say so, or retiring the previous key would
	// strand them.
	if len(rep.NotInstalled) > 0 {
		fmt.Printf("not reached: %s keep tables here but are not installed in this run; "+
			"run it again with the module flags the store is served with\n", strings.Join(rep.NotInstalled, ", "))
	}
	return nil
}

func superuserCmd(ctx context.Context, app *gocommerce.App, args []string) error {
	sus := app.Superusers()

	if len(args) > 0 && args[0] == "list" {
		list, err := sus.List(ctx)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println("no superusers yet — create one with: gocommerce superuser create <email> <password>")
			return nil
		}
		for _, su := range list {
			fmt.Printf("%d\t%s\t%s\n", su.ID, su.Email, su.CreatedAt.Format("2006-01-02 15:04"))
		}
		return nil
	}

	if len(args) < 3 || (args[0] != "create" && args[0] != "update") {
		return errors.New(
			"usage: gocommerce superuser create|update <email> <password> [role]  |  gocommerce superuser list")
	}
	action, email, password := args[0], args[1], args[2]
	// The role is optional and defaults to owner, so a script that has always
	// created an operator keeps creating the operator it always did.
	role := ""
	if len(args) > 3 {
		role = args[3]
	}

	if action == "create" {
		su, err := sus.Create(ctx, email, password, role)
		if err != nil {
			return err
		}
		fmt.Printf("created superuser %s as %s (id %d)\n", su.Email, su.Role, su.ID)
		return nil
	}

	list, err := sus.List(ctx)
	if err != nil {
		return err
	}
	for _, su := range list {
		if strings.EqualFold(su.Email, strings.TrimSpace(email)) {
			if _, err := sus.Update(ctx, su.ID, "", password); err != nil {
				return err
			}
			fmt.Printf("updated the password for %s; their other sessions were signed out\n", su.Email)
			return nil
		}
	}
	return fmt.Errorf("no superuser with email %q", email)
}

// corsOrigins trims the trailing slash an origin copied from the address bar
// carries. A browser's Origin header never has one, so "https://shop.com/"
// would match nothing and the storefront would fail with no hint why.
func corsOrigins(s string) []string {
	origins := splitList(s)
	for i, o := range origins {
		origins[i] = strings.TrimRight(o, "/")
	}
	return origins
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// taxonomyCmd imports a category tree.
//
// It is a command rather than a migration because the tree is the operator's
// data: a store that wants six categories of its own should not find fourteen
// thousand of Shopify's in it because it upgraded. Running it twice is safe —
// see Categories.ImportTaxonomy — so it is also the way to top up an existing
// tree after the published taxonomy grows.
func taxonomyCmd(ctx context.Context, app *gocommerce.App, args []string, log *slog.Logger) error {
	if len(args) == 0 || args[0] != "import" {
		return errors.New("usage: gocommerce taxonomy import [file|-]")
	}

	var reader io.Reader = strings.NewReader(gocommerce.ShopifyTaxonomy())
	source := "the embedded Shopify taxonomy"
	if len(args) > 1 {
		switch args[1] {
		case "-":
			reader, source = os.Stdin, "stdin"
		default:
			f, err := os.Open(args[1])
			if err != nil {
				return fmt.Errorf("open %s: %w", args[1], err)
			}
			defer f.Close()
			reader, source = f, args[1]
		}
	}

	log.Info("importing categories", "source", source)
	result, err := app.Categories().ImportTaxonomy(ctx, reader)
	if err != nil {
		return err
	}
	log.Info("categories imported",
		"created", result.Created, "already present", result.Matched, "skipped", result.Skipped)

	// The fields each category asks of a product follow the tree, and only for
	// the embedded source: they are matched by the taxonomy id ImportTaxonomy
	// wrote, so a tree that came from somewhere else has nothing to match. An
	// operator who wants both from their own files runs the two commands.
	if len(args) > 1 {
		return nil
	}
	attrs, err := app.Categories().ImportCategoryAttributes(
		ctx, strings.NewReader(gocommerce.ShopifyCategoryAttributes()))
	if err != nil {
		return err
	}
	log.Info("category fields imported",
		"attributes", attrs.Attributes, "categories", attrs.Categories,
		"unmatched", attrs.Unmatched, "skipped", attrs.Skipped)
	return nil
}

// attributesCmd imports only the field definitions, for a tree that is already
// in place.
func attributesCmd(ctx context.Context, app *gocommerce.App, args []string, log *slog.Logger) error {
	if len(args) == 0 || args[0] != "import" {
		return errors.New("usage: gocommerce attributes import [file|-]")
	}

	var reader io.Reader = strings.NewReader(gocommerce.ShopifyCategoryAttributes())
	source := "the embedded Shopify attributes"
	if len(args) > 1 {
		switch args[1] {
		case "-":
			reader, source = os.Stdin, "stdin"
		default:
			f, err := os.Open(args[1])
			if err != nil {
				return fmt.Errorf("open %s: %w", args[1], err)
			}
			defer f.Close()
			reader, source = f, args[1]
		}
	}

	log.Info("importing category fields", "source", source)
	result, err := app.Categories().ImportCategoryAttributes(ctx, reader)
	if err != nil {
		return err
	}
	log.Info("category fields imported",
		"attributes", result.Attributes, "categories", result.Categories,
		"unmatched", result.Unmatched, "skipped", result.Skipped)
	return nil
}

// envTrue reads a variable that must be switched on deliberately. Absent, empty,
// and the words people write when they mean no are all off; anything else is on.
//
// The rest of this file treats any non-empty value as true, which is fine for a
// variable whose only job is to carry a key. It is not fine for one that opens a
// panel, so this exists for those.
func envTrue(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

// firstEnv is the first of these variables that is set. A module's own
// variable comes first and an install-wide default or an alias after it, so
// DEFAULT_EMAIL_FROM_ADDRESS sends for any email backend not told otherwise,
// and MSG91_AUTHKEY — the name MSG91's own dashboard uses — is read too.
func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
