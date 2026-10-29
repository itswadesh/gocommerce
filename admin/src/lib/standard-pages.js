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

/* The other names a storefront answers for a standard page: Svelte Commerce
   redirects /contact, /pages/privacy and the like to these slugs, and a store
   opened before D145 has its footer pointing at them. */
const SLUG_BY_NAME = new Map(
    STANDARD_PAGES.map((p) => [p.slug, p.slug]).concat([
        ["about", "about-us"],
        ["contact", "contact-us"],
        ["privacy", "privacy-policy"],
        ["terms", "terms-and-conditions"],
        ["shipping", "shipping-policy"],
        ["returns", "refund-policy"],
        ["refunds", "refund-policy"],
    ]),
);

/** The standard page a menu item links to, under any of its names. */
function standardSlugOf(item) {
    if (item.children?.length) return undefined;
    const target = item.target ?? "";
    const name = item.kind === "page" ? target : item.kind === "url" ? /^\/(?:pages\/|p\/)?([a-z-]+)\/?$/.exec(target)?.[1] : undefined;
    return name && SLUG_BY_NAME.get(name);
}

const isSearch = (item) => item.kind === "url" && /^\/search\/?$/.test(item.target ?? "") && !item.children?.length;

/**
 * The footer a new store starts with (D145), made from a store's own: the
 * six standard pages at their storefront paths, keeping the title the store
 * gave any it already linked, then the store's other links as they were.
 * The Search link the old footer had goes, since the header searches.
 */
export function standardFooter(items) {
    const titles = new Map();
    const others = [];
    for (const item of items) {
        const slug = standardSlugOf(item);
        if (slug) {
            if (!titles.has(slug)) titles.set(slug, item.title);
        } else if (!isSearch(item)) others.push(item);
    }
    return [
        ...STANDARD_PAGES.map((p) => ({ title: titles.get(p.slug) ?? p.title, kind: "url", target: "/" + p.slug, children: [] })),
        ...others,
    ];
}

/** Whether two menus link the same things, in the same order, by the same names. */
export function sameLinks(a, b) {
    const key = (list) => JSON.stringify(list.map((it) => [it.title, it.kind, it.target ?? "", it.children?.length ?? 0]));
    return key(a) === key(b);
}
