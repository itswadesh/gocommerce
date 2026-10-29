package cms

import (
	"context"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// StandardPages are the pages every storefront links to (D145): the footer
// a new store starts with points at these slugs, and so do the contact
// form's consent line and the checkout's policy links. The slugs are Svelte
// Commerce's routes, which is why they are not the sample shop's shorter
// about and returns.
//
// The Pages screen offers the same six to a store that lacks them
// (admin/src/lib/standard-pages.js); TestStandardPagesMatchThePanel keeps
// the two lists the same.
//
// Each body tells the merchant what to write and states nothing about the
// shop — no address, no number of days, no price — because a page of ours
// left published would speak for a business we know nothing about.
var StandardPages = []struct{ Slug, Title, Body string }{
	{"about-us", "About us", "<p>Replace this with who you are and what you sell, in your own words.</p>"},
	{"contact-us", "Contact us", "<p>Replace this with how shoppers can reach you and when they can expect an answer.</p>"},
	{"privacy-policy", "Privacy policy", "<p>Replace this with what personal data your shop collects, why, who it is shared with, and how a shopper can ask for it to be removed.</p>"},
	{"terms-and-conditions", "Terms and conditions", "<p>Replace this with the terms a shopper agrees to when they buy from your shop.</p>"},
	{"shipping-policy", "Shipping policy", "<p>Replace this with where you deliver, how orders are sent, and what delivery costs.</p>"},
	{"refund-policy", "Refund and returns policy", "<p>Replace this with what can be returned, how a shopper starts a return, and how refunds are paid.</p>"},
}

// SeedStore gives a newly opened store the standard pages as drafts
// (gocommerce.StoreSeeder): written for the merchant to finish and publish,
// never shown to a shopper as they are. A slug the store already has, in
// any language, is left alone.
func (m *Module) SeedStore(ctx context.Context) error {
	language := m.app.Languages(ctx)[0]
	for _, p := range StandardPages {
		if _, err := m.db.ExecContext(ctx, `
			INSERT INTO cms_pages (slug, language, title, body, status)
			SELECT $1, $2, $3, $4, 'draft'
			WHERE NOT EXISTS (SELECT 1 FROM cms_pages WHERE slug = $1)`,
			p.Slug, language, p.Title, p.Body); err != nil {
			return gocommerce.Internalf(err, "add the standard pages")
		}
	}
	return nil
}
