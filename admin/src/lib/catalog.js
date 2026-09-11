/**
 * What the catalogue says about itself.
 *
 * Vendor, product type and tag are columns on `products`, not tables, so there
 * is no list route to ask: the set of choices is only ever "what has been typed
 * before", and the products are their own index.
 *
 * The fold lives here rather than in the two screens that need it because they
 * have to agree. The product editor suggests these names while authoring, and
 * the product list offers them as filters over exactly the same column — two
 * copies of this that drifted would suggest a spelling the other screen could
 * not then find.
 */

/**
 * The distinct spellings of one free-text column across a page of the catalogue.
 *
 * Folded case-insensitively because the engine folds tags that way, and the
 * first spelling seen wins — so the suggestions read the way the catalogue
 * already reads rather than the way this function would have written them.
 *
 * `values` pulls the strings out of one row: a column is `(p) => [p.vendor]`,
 * and a list is `(p) => p.tags ?? []`.
 */
export function distinct(rows, values) {
    const seen = new Map();
    for (const row of rows ?? []) {
        for (const raw of values(row)) {
            const value = String(raw ?? "").trim();
            if (!value) continue;
            const key = value.toLowerCase();
            if (!seen.has(key)) seen.set(key, value);
        }
    }
    return [...seen.values()].sort((a, b) => a.localeCompare(b));
}
