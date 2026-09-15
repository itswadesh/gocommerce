/**
 * Every screen the panel can navigate to, in one list.
 *
 * It used to be a literal inside `+layout.svelte`, which was fine while the
 * sidebar was the only thing that needed it. The command palette needs the same
 * list — "go to Inventory" is the cheapest thing a palette does — and two
 * copies of it is exactly how one of them comes to be missing a screen that
 * shipped six months ago.
 *
 * The shape is Shopify's: a short list of sections in Shopify's order — Home,
 * Orders, Products, Customers, Discounts, Content, Settings — each with its
 * own screens beneath it, shown only while the section is open. Twenty-five
 * screens in one flat column had stopped reading as a menu; seven sections
 * that unfold is what an operator who has used Shopify already knows. Two
 * items are ours rather than Shopify's, and deliberately: Notifications sits
 * in the main list because the person with a shopper on the phone must find
 * "did she get it" without opening Settings, and Plugins is Shopify's "Apps"
 * under the name this store uses for them.
 *
 * Each entry names the right that makes the screen worth showing. A staff
 * operator has no business on a settings page they would be refused from, and
 * an item that leads only to a 403 is worse than no item. The engine is still
 * the one enforcing this; the nav is only telling the truth about what is
 * behind each link.
 *
 * An item may also name the module that serves it. A screen for a module this
 * binary was not built with is hidden for the same reason an item leading to a
 * 403 is: the link would lead somewhere that does not exist.
 *
 * `children` are a section's own screens. Each carries its own right and
 * module, and is filtered like a top-level item; a section whose own screen is
 * hidden still shows when any child survives, because the section is the way
 * to the child. Children have no icon of their own — the section's is theirs.
 *
 * `accent` is the item's own colour, as the B2B Leads sidebar does it: the icon
 * is always tinted and the active row takes the matching soft ground. The
 * values live in gocommerce.css, so light and dark can differ; here they are
 * only names.
 *
 * `keywords` are for the palette alone: what an operator might type that is not
 * the label. Nothing draws them.
 */

import { can } from "$lib/session.svelte.js";
import { hasModule } from "$lib/modules.svelte.js";

export const NAV = [
    {
        href: "/",
        label: "Home",
        icon: "ri-home-5-line",
        exact: true,
        accent: "indigo",
        keywords: "dashboard overview sales reports revenue analytics",
    },
    {
        href: "/orders",
        label: "Orders",
        icon: "ri-shopping-bag-3-line",
        right: "orders.read",
        accent: "amber",
        keywords: "sales fulfilment shipments refunds returns",
        children: [
            // Shopify's word for a basket that never became an order.
            { href: "/carts", label: "Abandoned checkouts", right: "orders.read", keywords: "carts baskets" },
            { href: "/invoices", label: "Invoices", right: "orders.read", module: "invoices", keywords: "pdf tax invoice" },
        ],
    },
    {
        href: "/products",
        label: "Products",
        icon: "ri-price-tag-3-line",
        right: "catalog.read",
        accent: "sky",
        keywords: "catalog catalogue variants sku",
        children: [
            { href: "/collections", label: "Collections", right: "catalog.read", keywords: "curated lists" },
            { href: "/categories", label: "Categories", right: "catalog.read", keywords: "taxonomy tree attributes" },
            { href: "/inventory", label: "Inventory", right: "inventory.read", keywords: "stock levels ledger" },
            // A price list is a rule about what somebody pays, worked in as
            // often as a promotion is, not vocabulary configured once.
            { href: "/pricing", label: "Price lists", right: "discounts.read", keywords: "trade wholesale b2b customer groups quantity breaks tiers" },
            { href: "/reviews", label: "Reviews", right: "catalog.read", module: "reviews", keywords: "ratings moderation" },
        ],
    },
    {
        href: "/customers",
        label: "Customers",
        icon: "ri-user-3-line",
        right: "customers.read",
        accent: "teal",
        keywords: "buyers shoppers",
        children: [
            { href: "/accounts", label: "Accounts", right: "customers.read", module: "identity", keywords: "logins passwords sessions" },
            // Both are the customers talking: the form and the signup box.
            { href: "/contact", label: "Contact messages", right: "customers.read", module: "contact", keywords: "inbox enquiries" },
            { href: "/newsletter", label: "Newsletter", right: "customers.read", module: "newsletter", keywords: "subscribers signups mailing list" },
        ],
    },
    {
        href: "/discounts",
        label: "Discounts",
        icon: "ri-price-tag-2-line",
        right: "discounts.read",
        accent: "rose",
        keywords: "codes promotions coupons",
    },
    // What the store told its shoppers, and whether it arrived. In the main
    // list rather than under Settings: an operator with a shopper on the
    // phone wants the confirmation email's fate next to the orders, not
    // behind a settings page. Beneath it, where the messages come from and
    // what they say, as Litekart arranges it.
    {
        href: "/notifications",
        label: "Notifications",
        icon: "ri-notification-3-line",
        right: "orders.read",
        accent: "amber",
        keywords: "email sms confirmation sent failed resend",
        children: [
            { href: "/notifications/email", label: "Setup Email", right: "store.operate", keywords: "sendgrid provider templates" },
            { href: "/notifications/sms", label: "Setup SMS", right: "store.operate", keywords: "msg91 provider templates" },
        ],
    },
    // Shopify's Content: the words and pictures a storefront is made of that
    // are not products. The section lands on Files, which every store has;
    // Pages and Menus join when their modules are installed.
    {
        href: "/media",
        label: "Content",
        icon: "ri-layout-line",
        right: "catalog.read",
        accent: "blue",
        keywords: "media files pages menus cms",
        children: [
            { href: "/media", label: "Files", right: "catalog.read", keywords: "media images pictures uploads" },
            { href: "/cms", label: "Pages", right: "catalog.read", module: "cms", keywords: "content copy about" },
            { href: "/menus", label: "Menus", right: "catalog.read", module: "navigation", keywords: "navigation header footer links" },
        ],
    },
    // What this binary can do that is switched on from the panel: storefront
    // extras, widgets, search, marketing. Shopify's "Apps", under the name
    // this store uses. store.operate, as webhooks are — pasting an analytics
    // key is operating the store, not selling.
    {
        href: "/plugins",
        label: "Plugins",
        icon: "ri-puzzle-line",
        right: "store.operate",
        accent: "violet",
        keywords: "apps plugins integrations widgets search klaviyo meilisearch feeds sitemap",
    },
    // Settings has no right of its own: the section is a shell, and every
    // screen inside it carries its own gate. Hiding the whole section from
    // an operator who may reach one of them is a worse lie than showing a
    // section with one item in it. Its children are the store's rules —
    // where it ships, what it taxes, where its stock is — which is where
    // Shopify keeps them too.
    // `health` is a field rather than an href comparison in the template,
    // so the badge's owner is declared beside the link it rides on.
    {
        href: "/settings",
        label: "Settings",
        icon: "ri-settings-3-line",
        accent: "orange",
        health: true,
        keywords: "store team roles data diagnostics audit webhooks",
        children: [
            { href: "/shipping", label: "Shipping and delivery", right: "store.operate", keywords: "zones rates methods" },
            { href: "/shipping/providers", label: "Shipping providers", right: "store.operate", keywords: "carriers fulfilment delhivery" },
            { href: "/taxes", label: "Taxes", right: "taxes.read", keywords: "vat gst rates" },
            { href: "/locations", label: "Locations", right: "locations.read", keywords: "warehouse store pickup" },
        ],
    },
];

/**
 * The screens this operator may actually reach.
 *
 * MUST be called from a reactive context — a `$derived` or an `$effect` — in
 * both readers: `can()` and `hasModule()` read runes, and it is the read that
 * registers the dependency, wherever the function happens to be declared.
 *
 * The module clause comes first, and that ordering is load-bearing rather than
 * stylistic. With the right clause first, an operator lacking catalog.read,
 * orders.read and customers.read would short-circuit before `hasModule` was
 * ever read, no dependency would be registered, and the module answer arriving
 * would change nothing on screen.
 *
 * A section is kept when it is reachable itself or when any of its children
 * is: the section is the way to the child.
 */
export function visibleNav(items = NAV) {
    const reachable = (item) => (!item.module || hasModule(item.module)) && (!item.right || can(item.right));
    return items.filter((item) => reachable(item) || (item.children ?? []).some(reachable));
}

/**
 * Every reachable screen, sections and their children in one flat list, for
 * the palette: "go to Collections" must work whether or not Products is open.
 * A child borrows its section's icon and accent, since it has none of its own.
 */
export function flatNav(items = NAV) {
    const reachable = (item) => (!item.module || hasModule(item.module)) && (!item.right || can(item.right));
    const out = [];
    for (const item of visibleNav(items)) {
        if (reachable(item)) out.push(item);
        for (const child of item.children ?? []) {
            if (reachable(child)) out.push({ ...child, icon: item.icon, accent: item.accent });
        }
    }
    return out;
}
