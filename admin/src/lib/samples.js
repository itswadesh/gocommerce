/*
 * The sample shop (D94, D96): what the panel says a store holds of it, and
 * who may add or erase it.
 */

/*
 * The kinds a store owner would recognise, in the order a shop is browsed.
 * Customers are left out: they are the sample orders' shoppers, not something
 * added beside them. A kind the engine reports and this list does not know is
 * left out too, rather than shown as its raw key.
 */
const SAMPLE_KINDS = [
    ["products", "product", "products"],
    ["categories", "category", "categories"],
    ["collections", "collection", "collections"],
    ["discounts", "discount", "discounts"],
    ["orders", "order", "orders"],
    ["reviews", "review", "reviews"],
    ["pages", "page", "pages"],
    ["faq", "question", "questions"],
    ["menu_links", "menu link", "menu links"],
];

/** describeSamples reads the engine's counts as a phrase: "8 products, … and 7 menu links". */
export function describeSamples(counts) {
    const parts = SAMPLE_KINDS.filter(([key]) => counts?.[key] > 0).map(
        ([key, one, many]) => `${counts[key]} ${counts[key] === 1 ? one : many}`,
    );
    if (parts.length < 2) return parts.join("");
    return parts.slice(0, -1).join(", ") + " and " + parts[parts.length - 1];
}

/*
 * What adding or erasing the sample shop needs of the engine's own areas: the
 * write right of each it touches, so nobody does through it what they could
 * not do by hand. The engine adds each installed module's right and reports
 * the whole list with the counts, which the panel prefers once it has it.
 */
export const SAMPLE_RIGHTS = [
    "catalog.write",
    "categories.write",
    "collections.write",
    "discounts.write",
    "orders.write",
    "media.write",
];
