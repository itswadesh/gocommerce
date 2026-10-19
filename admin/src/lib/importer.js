/**
 * The Import screen's arithmetic: which kinds a store can be given, where each
 * file goes, which dialect a header is in, and what stops a run.
 *
 * Kept out of the page so it can be tested without rendering one; the page is
 * markup over these.
 */

/*
 * Where each file goes in. The engine's own kinds live under
 * /api/admin/import/; a module's live under its own prefix, because a module
 * cannot add a verb to the engine's namespace and should not want to — the
 * door is the module's, and it closes when the module is not in the binary.
 */
const IMPORT_PATHS = {
    reviews: "/api/admin/x/reviews/import",
    menus: "/api/admin/x/navigation/import",
};

/**
 * What this store can actually be given, in the order the tabs read. A module
 * that is not installed contributes nothing rather than an option that
 * answers 404.
 *
 * @param {(name: string) => boolean} hasModule
 */
export function importKinds(hasModule) {
    return [
        { value: "products", label: "Products and variants" },
        { value: "inventory", label: "Inventory counts" },
        { value: "orders", label: "Historical orders" },
        { value: "categories", label: "Category tree" },
        ...(hasModule("reviews") ? [{ value: "reviews", label: "Product reviews" }] : []),
        ...(hasModule("navigation") ? [{ value: "menus", label: "Menus" }] : []),
    ];
}

/**
 * The address a run posts to. A dry run asks the engine to validate the whole
 * file and roll back. Each switch reaches only the kind it belongs to: events
 * are an orders question, and leaving existing rows alone is a products one.
 *
 * @param {string} kind
 * @param {{ dryRun?: boolean, fireEvents?: boolean, overwrite?: boolean }} [opts]
 */
export function importUrl(kind, { dryRun = false, fireEvents = false, overwrite = true } = {}) {
    const params = new URLSearchParams();
    if (dryRun) params.set("dry_run", "1");
    if (kind === "orders" && fireEvents) params.set("fire_events", "1");
    if (kind === "products" && !overwrite) params.set("overwrite", "0");
    const path = IMPORT_PATHS[kind] ?? `/api/admin/import/${kind}`;
    const q = params.toString();
    return q ? `${path}?${q}` : path;
}

/**
 * Which layout a file is in, read off its first line the way the engine reads
 * it — "shopify", "gocommerce", or "" for neither. Only products, inventory
 * and orders have two layouts; every other kind answers "".
 *
 * @param {string} kind
 * @param {string} csv
 */
export function detectDialect(kind, csv) {
    const header = csv.trimStart().split(/\r?\n/, 1)[0]?.toLowerCase() ?? "";
    if (!header) return "";
    const has = (name) => header.split(",").some((h) => h.replace(/^"|"$/g, "").trim() === name);
    if (kind === "products") return has("handle") ? "shopify" : has("product_slug") ? "gocommerce" : "";
    if (kind === "orders") return has("lineitem quantity") ? "shopify" : has("number") ? "gocommerce" : "";
    if (kind === "inventory") return has("on hand") || has("handle") ? "shopify" : has("sku") ? "gocommerce" : "";
    return "";
}

/**
 * Why the run buttons cannot be pressed yet, or "" when they can. The screen
 * prints it beside them: a greyed-out button with no reason is how this
 * screen came to look as if it had no way to import at all.
 *
 * @param {string} csv
 */
export function importBlocker(csv) {
    return csv.trim() ? "" : "Nothing to import yet: choose a file, or paste CSV into the box above.";
}
