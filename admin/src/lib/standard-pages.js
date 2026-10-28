/**
 * The pages every shop's storefront links to, whether or not the shop has
 * written them: the footer and the contact form point at these slugs, so a
 * missing one is a dead link on every page of the site.
 *
 * The slugs are the storefront's, not ours to choose — Svelte Commerce reads
 * `/x/cms/pages/about-us`, `/x/cms/pages/refund-policy` and so on — which is
 * why they are not the sample shop's shorter `about` and `returns`.
 *
 * Each starts as a draft whose body tells the merchant what to write. It
 * states nothing about the shop: no address, no number of days, no price.
 * A page of ours left published would speak for a business we know nothing
 * about, and a draft is not shown to shoppers at all.
 */

export const STANDARD_PAGES = [
    {
        slug: "about-us",
        title: "About us",
        body: "<p>Replace this with who you are and what you sell, in your own words.</p>",
    },
    {
        slug: "contact-us",
        title: "Contact us",
        body: "<p>Replace this with how shoppers can reach you and when they can expect an answer.</p>",
    },
    {
        slug: "privacy-policy",
        title: "Privacy policy",
        body: "<p>Replace this with what personal data your shop collects, why, who it is shared with, and how a shopper can ask for it to be removed.</p>",
    },
    {
        slug: "terms-and-conditions",
        title: "Terms and conditions",
        body: "<p>Replace this with the terms a shopper agrees to when they buy from your shop.</p>",
    },
    {
        slug: "shipping-policy",
        title: "Shipping policy",
        body: "<p>Replace this with where you deliver, how orders are sent, and what delivery costs.</p>",
    },
    {
        slug: "refund-policy",
        title: "Refund and returns policy",
        body: "<p>Replace this with what can be returned, how a shopper starts a return, and how refunds are paid.</p>",
    },
];

/** The standard pages whose slug is not among `slugs`, in the order above. */
export function missingStandardPages(slugs) {
    const have = new Set(slugs);
    return STANDARD_PAGES.filter((p) => !have.has(p.slug));
}
