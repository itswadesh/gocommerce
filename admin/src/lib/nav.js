/**
 * Every screen the panel can navigate to, in one list.
 *
 * It used to be a literal inside `+layout.svelte`, which was fine while the
 * sidebar was the only thing that needed it. The command palette needs the same
 * list — "go to Inventory" is the cheapest thing a palette does — and two
 * copies of it is exactly how one of them comes to be missing a screen that
 * shipped six months ago.
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
 * `accent` is the item's own colour, as the B2B Leads sidebar does it: the icon
 * is always tinted and the active row takes the matching soft ground. The
 * values live in gocommerce.css, so light and dark can differ; here they are
 * only names. Several items deliberately share one — Invoices with Orders,
 * Accounts with Customers — because a second accent would say the two are
 * unrelated things.
 *
 * `keywords` are for the palette alone: what an operator might type that is not
 * the label. Nothing draws them.
 */

import { can } from "$lib/session.svelte.js";
import { hasModule } from "$lib/modules.svelte.js";

export const NAV = [
    {
        href: "/",
        label: "Dashboard",
        icon: "ri-dashboard-line",
        exact: true,
        accent: "indigo",
        keywords: "home overview",
    },
    // "How much did we sell" is the second thing an owner opens, and until
    // recently the panel could not answer it.
    {
        href: "/reports",
        label: "Reports",
        icon: "ri-line-chart-line",
        right: "orders.read",
        accent: "rose",
        keywords: "sales revenue analytics",
    },
    {
        href: "/products",
        label: "Products",
        icon: "ri-price-tag-3-line",
        right: "catalog.read",
        accent: "sky",
        keywords: "catalog catalogue variants sku",
    },
    // Sky, with Products: a collection is a way of arranging products, not a
    // thing of its own.
    {
        href: "/collections",
        label: "Collections",
        icon: "ri-stack-line",
        right: "catalog.read",
        accent: "sky",
        keywords: "merchandising curated",
    },
    {
        href: "/categories",
        label: "Categories",
        icon: "ri-node-tree",
        right: "catalog.read",
        accent: "blue",
        keywords: "taxonomy tree",
    },
    // A page is catalog copy that happens not to carry a price, which is
    // why it takes catalog.read and sits beside the rest of the catalog.
    {
        href: "/cms",
        label: "Pages",
        icon: "ri-pages-line",
        right: "catalog.read",
        module: "cms",
        accent: "blue",
        keywords: "cms content copy",
    },
    // Blue, with Pages: both are the catalog's content rather than the goods.
    {
        href: "/media",
        label: "Media",
        icon: "ri-image-2-line",
        right: "catalog.read",
        accent: "blue",
        keywords: "images files photos library uploads",
    },
    {
        href: "/orders",
        label: "Orders",
        icon: "ri-shopping-bag-3-line",
        right: "orders.read",
        accent: "amber",
        keywords: "sales fulfilment shipping",
    },
    {
        href: "/invoices",
        label: "Invoices",
        icon: "ri-file-list-3-line",
        right: "orders.read",
        module: "invoices",
        accent: "amber",
        keywords: "billing documents",
    },
    {
        href: "/carts",
        label: "Carts",
        icon: "ri-shopping-cart-2-line",
        right: "orders.read",
        accent: "fuchsia",
        keywords: "abandoned baskets",
    },
    {
        href: "/discounts",
        label: "Discounts",
        icon: "ri-price-tag-2-line",
        right: "discounts.read",
        accent: "rose",
        keywords: "codes promotions coupons",
    },
    {
        // Beside Discounts rather than under Settings, and behind the same
        // right: a price list is a rule about what somebody pays, worked in as
        // often as a promotion is, not vocabulary configured once.
        href: "/pricing",
        label: "Price lists",
        icon: "ri-funds-box-line",
        right: "discounts.read",
        accent: "amber",
        keywords: "trade wholesale b2b customer groups quantity breaks tiers",
    },
    {
        href: "/taxes",
        label: "Tax",
        icon: "ri-percent-line",
        right: "taxes.read",
        accent: "violet",
        keywords: "vat rates",
    },
    {
        href: "/shipping",
        label: "Shipping",
        icon: "ri-truck-line",
        // store.operate rather than a right of its own: core/rights.go is a
        // closed catalogue, and what a store charges to deliver is the same
        // kind of decision as the outbox screen behind D49.
        right: "store.operate",
        // Violet, the same as Tax, and deliberately: both are money added to an
        // order that is not the goods, and a second colour would say they are
        // unrelated.
        accent: "violet",
        keywords: "delivery rates zones postage courier",
    },
    {
        href: "/customers",
        label: "Customers",
        icon: "ri-user-3-line",
        right: "customers.read",
        accent: "teal",
        keywords: "people buyers email",
    },
    // Teal, with Customers: both are people. The two lists overlap without
    // being the same list, and both screens say so themselves.
    {
        href: "/accounts",
        label: "Accounts",
        icon: "ri-account-circle-line",
        right: "customers.read",
        module: "identity",
        accent: "teal",
        keywords: "sign-in identity logins",
    },
    {
        href: "/inventory",
        label: "Inventory",
        icon: "ri-archive-2-line",
        right: "inventory.read",
        accent: "emerald",
        keywords: "stock low levels",
    },
    {
        href: "/locations",
        label: "Locations",
        icon: "ri-map-pin-line",
        right: "locations.read",
        accent: "cyan",
        keywords: "warehouse store pickup",
    },
    // Settings has no right of its own: the section is a shell, and every
    // screen inside it carries its own gate. Hiding the whole section from
    // an operator who may reach one of them is a worse lie than showing a
    // section with one item in it.
    // `health` is a field rather than an href comparison in the template,
    // so the badge's owner is declared beside the link it rides on.
    {
        href: "/settings",
        label: "Settings",
        icon: "ri-settings-3-line",
        accent: "orange",
        health: true,
        keywords: "store team roles data diagnostics audit",
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
 */
export function visibleNav(items = NAV) {
    return items.filter(
        (item) => (!item.module || hasModule(item.module)) && (!item.right || can(item.right)),
    );
}
