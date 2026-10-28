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

/*
 * The four modes, in the order the screen offers them. Only a kind whose rows
 * carry a key the engine looks up takes them: a product's slug (or Handle), a
 * category's path. Orders are insert only and inventory update only by their
 * nature, and the modules' imports have one behaviour each, so those kinds
 * offer no choice at all rather than a choice the engine would refuse.
 */
const KEYED = {
    products: { one: "product", many: "products", key: "slug or Handle" },
    categories: { one: "category", many: "categories", key: "path" },
};

/**
 * The modes a kind takes, each with the sentence that says what it does to a
 * file of that kind; none for a kind that has only the one behaviour.
 *
 * @param {string} kind
 * @returns {{ value: string, label: string, help: string }[]}
 */
export function importModes(kind) {
    const k = KEYED[kind];
    if (!k) return [];
    return [
        { value: "upsert", label: "Upsert", help: `Creates new ${k.many} and updates the ones the store has.` },
        { value: "insert", label: "Insert only", help: `Creates new ${k.many}. One the store has is left exactly as it is.` },
        {
            value: "update",
            label: "Update only",
            help: `Changes the ${k.many} the store has. A row whose ${k.key} it lacks is skipped and listed.`,
        },
        { value: "delete", label: "Delete", help: `Deletes every ${k.one} the file names by its ${k.key}.` },
    ];
}

/**
 * The address a run posts to. A dry run asks the engine to validate the whole
 * file and roll back. Each switch reaches only the kind it belongs to: events
 * are an orders question, and the mode belongs to the kinds that take one.
 * Upsert is what a request with no mode does, so it is not spelled out.
 *
 * @param {string} kind
 * @param {{ dryRun?: boolean, fireEvents?: boolean, mode?: string }} [opts]
 */
export function importUrl(kind, { dryRun = false, fireEvents = false, mode = "upsert" } = {}) {
    const params = new URLSearchParams();
    if (dryRun) params.set("dry_run", "1");
    if (kind === "orders" && fireEvents) params.set("fire_events", "1");
    if (mode !== "upsert" && importModes(kind).some((m) => m.value === mode)) params.set("mode", mode);
    const path = IMPORT_PATHS[kind] ?? `/api/admin/import/${kind}`;
    const q = params.toString();
    return q ? `${path}?${q}` : path;
}

/**
 * What a run imports, in the order it imports them: every file chosen or
 * dropped, then the box if anything is typed in it. A file is imported on its
 * own rather than joined to the next, so each one's line numbers are its own.
 *
 * @param {{ name: string, text: string }[]} files
 * @param {string} csv
 */
export function importSources(files, csv) {
    return [...files, ...(csv.trim() ? [{ name: "Pasted CSV", text: csv }] : [])];
}

/**
 * The files a drop or a picker handed over that are worth reading: a CSV by
 * its name or its type. Anything else is refused by name, so a stray PDF in a
 * folder of exports is said rather than silently lost.
 *
 * @param {ArrayLike<File>} list
 */
export function csvFiles(list) {
    const ok = [];
    const refused = [];
    for (const f of Array.from(list ?? [])) {
        if (/\.csv$/i.test(f.name) || f.type === "text/csv") ok.push(f);
        else refused.push(f.name);
    }
    return { ok, refused };
}

/**
 * The totals across a run's files, for the one toast a run raises. A file the
 * engine refused outright counts as failed; a file with bad rows is still a
 * file that ran.
 *
 * @param {{ result?: object, error?: string }[]} results
 */
export function runTotals(results) {
    const t = { files: results.length, failed: 0, created: 0, updated: 0, deleted: 0, attention: 0 };
    for (const r of results) {
        if (!r.result) {
            t.failed++;
            continue;
        }
        t.created += r.result.created ?? 0;
        t.updated += r.result.updated ?? 0;
        t.deleted += r.result.deleted ?? 0;
        t.attention += r.result.errors?.length ?? 0;
    }
    return t;
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
 * @param {{ name: string, text: string }[]} sources
 */
export function importBlocker(sources) {
    return sources.length ? "" : "Nothing to import yet: drop or choose CSV files, or paste CSV into the box above.";
}

/**
 * The last run's results, if they belong under the tab now open. A products
 * dry run left showing under Orders would say "Press Import to apply the file"
 * about a file that was never an orders file. Edits to the box keep them:
 * their tables of bad rows are what the edits are fixing.
 *
 * @param {{ kind: string, results: object[] } | null} run
 * @param {string} kind
 */
export function resultFor(run, kind) {
    return run && run.kind === kind ? run.results : null;
}

/**
 * Whether the screen holds CSV that leaving would lose. A dry run writes
 * nothing, so a file that has only been rehearsed is still unsaved; it stops
 * counting once a real import has reported.
 *
 * @param {{ name: string, text: string }[]} sources
 * @param {{ dry: boolean } | null} run
 */
export function unsavedImport(sources, run) {
    return sources.length > 0 && (!run || run.dry);
}
