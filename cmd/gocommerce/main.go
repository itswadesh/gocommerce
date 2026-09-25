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
	"github.com/itswadesh/gocommerce/ext/sitemaps"
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
  doctor     run operational diagnostics and exit (-json for agents)
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
  GOCOMMERCE_ADMIN_TOKEN
                    admin bearer token, used when -admin-token is not given
                    (several may be given, comma-separated)
  GOCOMMERCE_ADMIN_EMAIL, GOCOMMERCE_ADMIN_PASSWORD
                    if set, "serve" creates this superuser when the database
                    has none, so an unattended deploy comes up signed-in-able
  GOCOMMERCE_MEDIA_DIR
                    where uploaded media is written, used when -media-dir is
                    not given; leaving it unset disables uploads and the media
                    library records files by URL only
  GOCOMMERCE_IDENTITY_RESET_URL
                    with -identity, the storefront page a password-reset email
                    links to, with {token} where the token goes
`)
	}

	var (
		dbURL        = fs.String("db", "", "PostgreSQL URL (default $DATABASE_URL)")
		addr         = fs.String("addr", ":8080", "listen address")
		tokens       = fs.String("admin-token", "", "admin bearer token(s), comma-separated (default $GOCOMMERCE_ADMIN_TOKEN)")
		currency     = fs.String("currency", gocommerce.DefaultCurrency, "store settlement currency (ISO 4217)")
		langs        = fs.String("languages", gocommerce.DefaultLanguage, "served languages, comma-separated; the first is the default")
		dev          = fs.Bool("dev", false, "development mode: permits booting with no admin token")
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
		withResend   = fs.Bool("resend", false, "install the Resend module: the store's emails through Resend, and the one to reach for first — an API key is the only required setting (RESEND_API_KEY, RESEND_FROM, or Notifications › Setup Email)")
		withSendgrid = fs.Bool("sendgrid", false, "install the SendGrid module: the store's emails through SendGrid (SENDGRID_API_KEY, SENDGRID_FROM, or Notifications › Setup Email)")
		withTwilio   = fs.Bool("twilio", false, "install the Twilio module: the store's SMS through Twilio, with the wording from Notifications › Setup SMS (TWILIO_ACCOUNT_SID, TWILIO_AUTH_TOKEN, TWILIO_FROM)")
		withMsg91    = fs.Bool("msg91", false, "install the MSG91 module: the store's SMS through MSG91 (MSG91_AUTH_KEY, or Notifications › Setup SMS)")
		withGateways = fs.Bool("gateways", false, "install every payment gateway module idle — Stripe, Razorpay, Adyen, Paddle, Lemon Squeezy, Creem, Helcim, Hyperswitch, RevenueCat — each switched on and given its keys under Settings › Payment methods")
		withCarriers = fs.Bool("carriers", false, "install every carrier module idle — Shiprocket, Delhivery, NimbusPost, India Post, Shippo, ShipStation, Easyship, Shippit, USPS, Onfleet, Veeqo — each switched on and given its keys under Settings › Shipping providers")
		withInvoices = fs.Bool("invoices", false, "install the invoices module: a numbered invoice per paid order (INVOICES_SELLER_NAME, INVOICES_SELLER_ADDRESS, INVOICES_TAX_ID)")
		withCMS      = fs.Bool("cms", false, "install the cms module: content pages at /x/cms/pages/{slug}, edited on the Pages screen")
		withFAQ      = fs.Bool("faq", false, "install the faq module: the shop's questions and answers at /x/faq, edited on the FAQ screen")
		withWishlist = fs.Bool("wishlist", false, "install the wishlist module: shoppers save products, and the Wishlists screen shows what is wanted most")
		withAmazon   = fs.Bool("import-amazon", false, "install the import-amazon module: create products from Amazon listings through a real Chrome (ANTHROPIC_API_KEY, GEMINI_API_KEY, OPENAI_API_KEY, or IMPORT_AMAZON_LLM_URL + IMPORT_AMAZON_LLM_MODEL for a local model rewrite the copy; IMPORT_AMAZON_HEADED=1 shows the browser)")
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
		DBURL:       *dbURL,
		Addr:        *addr,
		Currency:    *currency,
		Languages:   languages,
		AdminTokens: splitList(*tokens),
		Dev:         *dev || offline,
		Logger:      log,
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
	var modules []gocommerce.Module
	if *withIdentity {
		modules = append(modules, identity.New(identity.Config{
			ResetURL: os.Getenv("GOCOMMERCE_IDENTITY_RESET_URL"),
		}))
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
		modules = append(modules, feeds.New(feeds.Config{StorefrontURL: os.Getenv("STOREFRONT_URL")}))
	}
	if *withSitemaps {
		modules = append(modules, sitemaps.New(sitemaps.Config{StorefrontURL: os.Getenv("STOREFRONT_URL")}))
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
	// The delivery backends. With nothing in the environment they are
	// installed idle and wait for the Setup Email / Setup SMS screens.
	// Resend first, because it is the one that works with one setting: a key
	// and nothing else sends, from Resend's own onboarding address, until the
	// store has a domain of its own to verify.
	if *withResend {
		modules = append(modules, resend.New(resend.Config{
			APIKey: os.Getenv("RESEND_API_KEY"), From: os.Getenv("RESEND_FROM"), FromName: os.Getenv("RESEND_FROM_NAME"),
		}))
	}
	if *withSendgrid {
		modules = append(modules, sendgrid.New(sendgrid.Config{
			APIKey: os.Getenv("SENDGRID_API_KEY"), From: os.Getenv("SENDGRID_FROM"), FromName: os.Getenv("SENDGRID_FROM_NAME"),
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
		modules = append(modules, msg91.New(msg91.Config{AuthKey: os.Getenv("MSG91_AUTH_KEY")}))
	}
	// Gateways and carriers install idle: nothing in Config, everything on
	// the Payment methods and Shipping providers screens. A store that
	// prefers the environment wires the module itself in its own main().
	if *withGateways {
		modules = append(modules,
			stripe.New(stripe.Config{}), razorpay.New(razorpay.Config{}), adyen.New(adyen.Config{}),
			paddle.New(paddle.Config{}), lemonsqueezy.New(lemonsqueezy.Config{}), creem.New(creem.Config{}),
			helcim.New(helcim.Config{}),
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

	app, err := gocommerce.New(cfg, modules...)
	if err != nil {
		return err
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

	case "superuser":
		defer app.Close()
		return superuserCmd(context.Background(), app, fs.Args()[1:])

	case "doctor":
		defer app.Close()
		return doctorCmd(context.Background(), app, *jsonOut)

	case "spec":
		defer app.Close()
		_, err := os.Stdout.Write(app.Spec())
		return err

	case "taxonomy":
		defer app.Close()
		return taxonomyCmd(context.Background(), app, fs.Args()[1:], log)

	case "attributes":
		defer app.Close()
		return attributesCmd(context.Background(), app, fs.Args()[1:], log)

	case "serve":
		if err := bootstrapSuperuser(context.Background(), app, log); err != nil {
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
