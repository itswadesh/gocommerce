// Package alltest checks what only every module mounted together can show:
// that each table any module creates is kept to its store (D83).
package alltest

import (
	"context"
	"testing"

	gocommerce "github.com/misiki-in/gocommerce/core"
	ads "github.com/misiki-in/gocommerce/ext/ads"
	aigateway "github.com/misiki-in/gocommerce/ext/ai-gateway"
	askai "github.com/misiki-in/gocommerce/ext/ask-ai"
	b2b "github.com/misiki-in/gocommerce/ext/b2b"
	banners "github.com/misiki-in/gocommerce/ext/banners"
	cartrecovery "github.com/misiki-in/gocommerce/ext/cart-recovery"
	catalogai "github.com/misiki-in/gocommerce/ext/catalog-ai"
	cms "github.com/misiki-in/gocommerce/ext/cms"
	contact "github.com/misiki-in/gocommerce/ext/contact"
	customizer "github.com/misiki-in/gocommerce/ext/customizer"
	"github.com/misiki-in/gocommerce/ext/digital"
	faq "github.com/misiki-in/gocommerce/ext/faq"
	feeds "github.com/misiki-in/gocommerce/ext/feeds"
	delhivery "github.com/misiki-in/gocommerce/ext/fulfill-delhivery"
	easyship "github.com/misiki-in/gocommerce/ext/fulfill-easyship"
	indiapost "github.com/misiki-in/gocommerce/ext/fulfill-indiapost"
	nimbuspost "github.com/misiki-in/gocommerce/ext/fulfill-nimbuspost"
	onfleet "github.com/misiki-in/gocommerce/ext/fulfill-onfleet"
	shippit "github.com/misiki-in/gocommerce/ext/fulfill-shippit"
	shippo "github.com/misiki-in/gocommerce/ext/fulfill-shippo"
	shiprocket "github.com/misiki-in/gocommerce/ext/fulfill-shiprocket"
	shipstation "github.com/misiki-in/gocommerce/ext/fulfill-shipstation"
	usps "github.com/misiki-in/gocommerce/ext/fulfill-usps"
	veeqo "github.com/misiki-in/gocommerce/ext/fulfill-veeqo"
	"github.com/misiki-in/gocommerce/ext/gokwik"
	googlereviews "github.com/misiki-in/gocommerce/ext/google-reviews"
	identity "github.com/misiki-in/gocommerce/ext/identity"
	amazon "github.com/misiki-in/gocommerce/ext/import-amazon"
	shopify "github.com/misiki-in/gocommerce/ext/import-shopify"
	indexnow "github.com/misiki-in/gocommerce/ext/indexnow"
	invoices "github.com/misiki-in/gocommerce/ext/invoices"
	klaviyo "github.com/misiki-in/gocommerce/ext/klaviyo"
	layouts "github.com/misiki-in/gocommerce/ext/layouts"
	mcp "github.com/misiki-in/gocommerce/ext/mcp"
	metacapi "github.com/misiki-in/gocommerce/ext/meta-capi"
	metalpricing "github.com/misiki-in/gocommerce/ext/metal-pricing"
	navigation "github.com/misiki-in/gocommerce/ext/navigation"
	newsletter "github.com/misiki-in/gocommerce/ext/newsletter"
	msg91 "github.com/misiki-in/gocommerce/ext/notify-msg91"
	resend "github.com/misiki-in/gocommerce/ext/notify-resend"
	sendgrid "github.com/misiki-in/gocommerce/ext/notify-sendgrid"
	twilio "github.com/misiki-in/gocommerce/ext/notify-twilio"
	whatsapp "github.com/misiki-in/gocommerce/ext/notify-whatsapp"
	adyen "github.com/misiki-in/gocommerce/ext/payments-adyen"
	cashfree "github.com/misiki-in/gocommerce/ext/payments-cashfree"
	clover "github.com/misiki-in/gocommerce/ext/payments-clover"
	creem "github.com/misiki-in/gocommerce/ext/payments-creem"
	flutterwave "github.com/misiki-in/gocommerce/ext/payments-flutterwave"
	helcim "github.com/misiki-in/gocommerce/ext/payments-helcim"
	hyperswitch "github.com/misiki-in/gocommerce/ext/payments-hyperswitch"
	lemonsqueezy "github.com/misiki-in/gocommerce/ext/payments-lemonsqueezy"
	paddle "github.com/misiki-in/gocommerce/ext/payments-paddle"
	paypal "github.com/misiki-in/gocommerce/ext/payments-paypal"
	paystack "github.com/misiki-in/gocommerce/ext/payments-paystack"
	razorpay "github.com/misiki-in/gocommerce/ext/payments-razorpay"
	revenuecat "github.com/misiki-in/gocommerce/ext/payments-revenuecat"
	stripe "github.com/misiki-in/gocommerce/ext/payments-stripe"
	photoproducts "github.com/misiki-in/gocommerce/ext/photo-products"
	"github.com/misiki-in/gocommerce/ext/retailassistant"
	reviews "github.com/misiki-in/gocommerce/ext/reviews"
	meilisearch "github.com/misiki-in/gocommerce/ext/search-meilisearch"
	shopassistant "github.com/misiki-in/gocommerce/ext/shop-assistant"
	showcases "github.com/misiki-in/gocommerce/ext/showcases"
	sitemaps "github.com/misiki-in/gocommerce/ext/sitemaps"
	telegramalerts "github.com/misiki-in/gocommerce/ext/telegram-alerts"
	translations "github.com/misiki-in/gocommerce/ext/translations"
	webhooks "github.com/misiki-in/gocommerce/ext/webhooks"
	wishlist "github.com/misiki-in/gocommerce/ext/wishlist"
	"github.com/misiki-in/gocommerce/gctest"
)

func everyModule() []gocommerce.Module {
	// b2b needs the identity module it signs buyers in through, listed first.
	accounts := identity.New(identity.Config{})
	gateway := aigateway.New(aigateway.Config{})
	productFeeds, productReviews := feeds.New(feeds.Config{}), reviews.New(reviews.Config{})
	return []gocommerce.Module{
		gateway,
		photoproducts.New(photoproducts.Config{Gateway: gateway}),
		catalogai.New(catalogai.Config{Gateway: gateway}),
		askai.New(askai.Config{Gateway: gateway}),
		shopassistant.New(shopassistant.Config{Gateway: gateway}),
		ads.New(ads.Config{Gateway: gateway}),
		accounts,
		b2b.New(b2b.Config{Accounts: accounts}),
		cartrecovery.New(cartrecovery.Config{}),
		cms.New(cms.Config{}),
		contact.New(contact.Config{}),
		faq.New(faq.Config{}),
		showcases.New(showcases.Config{}),
		metalpricing.New(metalpricing.Config{}),
		retailassistant.New(retailassistant.Config{}),
		gokwik.New(gokwik.Config{}),
		banners.New(banners.Config{}),
		layouts.New(layouts.Config{}),
		productFeeds,
		delhivery.New(delhivery.Config{}),
		easyship.New(easyship.Config{}),
		indiapost.New(indiapost.Config{}),
		nimbuspost.New(nimbuspost.Config{}),
		onfleet.New(onfleet.Config{}),
		shippit.New(shippit.Config{}),
		shippo.New(shippo.Config{}),
		shiprocket.New(shiprocket.Config{}),
		shipstation.New(shipstation.Config{}),
		usps.New(usps.Config{}),
		veeqo.New(veeqo.Config{}),
		amazon.New(amazon.Config{}),
		shopify.New(),
		indexnow.New(indexnow.Config{}),
		invoices.New(invoices.Config{SellerName: "Test Store"}),
		klaviyo.New(klaviyo.Config{}),
		mcp.New(mcp.Config{}),
		metacapi.New(metacapi.Config{}),
		navigation.New(navigation.Config{}),
		newsletter.New(newsletter.Config{}),
		msg91.New(msg91.Config{}),
		resend.New(resend.Config{}),
		sendgrid.New(sendgrid.Config{}),
		twilio.New(twilio.Config{}),
		whatsapp.New(whatsapp.Config{}),
		adyen.New(adyen.Config{}),
		cashfree.New(cashfree.Config{}),
		clover.New(clover.Config{}),
		creem.New(creem.Config{}),
		helcim.New(helcim.Config{}),
		hyperswitch.New(hyperswitch.Config{}),
		lemonsqueezy.New(lemonsqueezy.Config{}),
		paddle.New(paddle.Config{}),
		flutterwave.New(flutterwave.Config{}),
		paypal.New(paypal.Config{}),
		paystack.New(paystack.Config{}),
		razorpay.New(razorpay.Config{}),
		revenuecat.New(revenuecat.Config{}),
		stripe.New(stripe.Config{}),
		productReviews,
		googlereviews.New(googlereviews.Config{Feeds: productFeeds, Reviews: productReviews}),
		meilisearch.New(meilisearch.Config{}),
		sitemaps.New(sitemaps.Config{}),
		telegramalerts.New(telegramalerts.Config{}),
		translations.New(),
		webhooks.New(webhooks.Config{}),
		wishlist.New(wishlist.Config{}),
		customizer.New(customizer.Config{}),
		digital.New(digital.Config{}),
	}
}

// Every table, core or module, is behind the store policy and forced to it,
// except the directory of stores and the people who sign in.
func TestEveryModuleTableIsScoped(t *testing.T) {
	app := gctest.New(t, everyModule()...)
	rows, err := app.DB().QueryContext(context.Background(), `
		SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relkind = 'r'
		  AND c.relname NOT IN ('stores', 'store_domains', 'store_members', 'store_counters',
		                        -- Which stores to look in for a phone at sign-in, before any
		                        -- store is chosen. It holds no role and no grant (D205).
		                        'store_phone_invitations',
		                        'api_key_index', 'superusers', 'superuser_sessions',
		                        'superuser_password_resets', 'superuser_login_codes', 'superuser_email_verifications', 'gocommerce_migrations',
		                        -- Which connection runs a report for which store (D84):
		                        -- the record that scopes reports, so no one store's.
		                        'report_runs',
		                        -- Which tables and columns reports may not read: the
		                        -- schema's, the same for every store (D120).
		                        'report_withheld')
		  AND NOT (c.relrowsecurity AND c.relforcerowsecurity AND EXISTS (
		      SELECT 1 FROM pg_policies p
		      WHERE p.schemaname = n.nspname AND p.tablename = c.relname AND p.policyname = 'store_scope'))
		ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		t.Errorf("%s is not kept to its store", name)
	}
}
