// Package alltest checks what only every module mounted together can show:
// that each table any module creates is kept to its store (D83).
package alltest

import (
	"context"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	b2b "github.com/itswadesh/gocommerce/ext/b2b"
	cartrecovery "github.com/itswadesh/gocommerce/ext/cart-recovery"
	cms "github.com/itswadesh/gocommerce/ext/cms"
	contact "github.com/itswadesh/gocommerce/ext/contact"
	faq "github.com/itswadesh/gocommerce/ext/faq"
	feeds "github.com/itswadesh/gocommerce/ext/feeds"
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
	identity "github.com/itswadesh/gocommerce/ext/identity"
	amazon "github.com/itswadesh/gocommerce/ext/import-amazon"
	shopify "github.com/itswadesh/gocommerce/ext/import-shopify"
	indexnow "github.com/itswadesh/gocommerce/ext/indexnow"
	invoices "github.com/itswadesh/gocommerce/ext/invoices"
	klaviyo "github.com/itswadesh/gocommerce/ext/klaviyo"
	mcp "github.com/itswadesh/gocommerce/ext/mcp"
	navigation "github.com/itswadesh/gocommerce/ext/navigation"
	newsletter "github.com/itswadesh/gocommerce/ext/newsletter"
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
	reviews "github.com/itswadesh/gocommerce/ext/reviews"
	meilisearch "github.com/itswadesh/gocommerce/ext/search-meilisearch"
	showcases "github.com/itswadesh/gocommerce/ext/showcases"
	sitemaps "github.com/itswadesh/gocommerce/ext/sitemaps"
	translations "github.com/itswadesh/gocommerce/ext/translations"
	webhooks "github.com/itswadesh/gocommerce/ext/webhooks"
	wishlist "github.com/itswadesh/gocommerce/ext/wishlist"
	"github.com/itswadesh/gocommerce/gctest"
)

func everyModule() []gocommerce.Module {
	// b2b needs the identity module it signs buyers in through, listed first.
	accounts := identity.New(identity.Config{})
	return []gocommerce.Module{
		accounts,
		b2b.New(b2b.Config{Accounts: accounts}),
		cartrecovery.New(cartrecovery.Config{}),
		cms.New(cms.Config{}),
		contact.New(contact.Config{}),
		faq.New(faq.Config{}),
		showcases.New(showcases.Config{}),
		feeds.New(feeds.Config{}),
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
		navigation.New(navigation.Config{}),
		newsletter.New(newsletter.Config{}),
		msg91.New(msg91.Config{}),
		resend.New(resend.Config{}),
		sendgrid.New(sendgrid.Config{}),
		twilio.New(twilio.Config{}),
		adyen.New(adyen.Config{}),
		creem.New(creem.Config{}),
		helcim.New(helcim.Config{}),
		hyperswitch.New(hyperswitch.Config{}),
		lemonsqueezy.New(lemonsqueezy.Config{}),
		paddle.New(paddle.Config{}),
		razorpay.New(razorpay.Config{}),
		revenuecat.New(revenuecat.Config{}),
		stripe.New(stripe.Config{}),
		reviews.New(reviews.Config{}),
		meilisearch.New(meilisearch.Config{}),
		sitemaps.New(sitemaps.Config{}),
		translations.New(),
		webhooks.New(webhooks.Config{}),
		wishlist.New(wishlist.Config{}),
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
		                        'api_key_index', 'superusers', 'superuser_sessions',
		                        'superuser_password_resets', 'gocommerce_migrations',
		                        -- Which connection runs a report for which store (D84):
		                        -- the record that scopes reports, so no one store's.
		                        'report_runs')
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
