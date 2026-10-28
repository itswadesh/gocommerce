/*
 * The Import screen has to offer a way to import.
 *
 * It did not, in effect: the one button that ran a file was named after the
 * "Dry run" switch, which starts on, so the screen never showed a button
 * called Import — and that button sat greyed out, with no word of why, until
 * a CSV was in the box. An owner holding a file saw "Choose a file…" (which
 * only fills the box) and a dead "Dry run", and reported that there were no
 * buttons to import.
 *
 * The arithmetic of where a file goes is pinned with the module; the shape of
 * the screen is pinned by reading its source, because node:test cannot render
 * a .svelte file.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
    csvFiles,
    detectDialect,
    importBlocker,
    importKinds,
    importModes,
    importSources,
    importUrl,
    resultFor,
    runTotals,
    unsavedImport,
} from "../src/lib/importer.js";

const read = (p) => readFileSync(new URL(p, import.meta.url), "utf8");
const page = read("../src/routes/dash/data/import/+page.svelte");
const css = read("../src/lib/styles/gocommerce.css");

/* The row that runs a file, from its opening tag to the end of the Import
   button, which is the last thing in it. */
function runActions() {
    const start = page.indexOf('class="import-run ');
    assert.ok(start >= 0, "the screen has an import-run row");
    const importAt = page.search(/runImport\(\s*false\s*\)/);
    assert.ok(importAt > start, "the import-run row has a button that runs a real import");
    const end = page.indexOf("</button>", importAt);
    assert.ok(end > importAt, "the Import button closes");
    return page.slice(start, end);
}

/* The body of a function in the page's script, up to the next blank line. */
function body(name) {
    const at = page.indexOf(`function ${name}(`);
    assert.ok(at >= 0, `the page has ${name}()`);
    return page.slice(at, page.indexOf("\n\n", at));
}

test("the screen always offers a button called Import", () => {
    const row = runActions();
    assert.match(row, /onclick=\{\s*\(\)\s*=>\s*runImport\(\s*false\s*\)\s*\}/);
    assert.match(row, /<span class="txt">\s*Import\s*<\/span>/);
    // The name never depends on a switch again.
    assert.doesNotMatch(page, /dryRun \? "Dry run" : "Import"/);
});

test("a dry run is its own button beside Import, not a switch that renames it", () => {
    const row = runActions();
    assert.match(row, /onclick=\{\s*\(\)\s*=>\s*runImport\(\s*true\s*\)\s*\}/);
    assert.match(row, /<span class="txt">\s*Dry run\s*<\/span>/);
    assert.doesNotMatch(page, /id="dry-run"/);
});

test("a run button that cannot be pressed yet says why, beside it", () => {
    const row = runActions();
    assert.match(row, /aria-describedby=\{\s*blocker \? "import-blocker" : undefined\s*\}/);
    assert.match(page, /id="import-blocker"[^>]*>\s*\{blocker\}/);
});

test("a blocked run button stays in the tab order, so its reason is heard", () => {
    // `disabled` takes a button out of the tab order, and with it the
    // aria-describedby that says why; aria-disabled keeps both. Only a run
    // in flight really disables them.
    const row = runActions();
    assert.match(row, /aria-disabled=\{\s*blocker \? "true" : undefined\s*\}/);
    assert.match(row, /class:disabled=\{\s*!!blocker\s*\}/);
    assert.match(row, /disabled=\{\s*!!running\s*\}/);
    // A bare `disabled` (not class:disabled, not aria-disabled) tied to the reason.
    assert.doesNotMatch(row, /(?<![:-])disabled=\{[^}]*blocker/);
    assert.match(body("runImport"), /if \(blocker \|\| running\) return;/);
});

test("no row of buttons on the screen is barred from wrapping", () => {
    // "Field definitions only" ran off the right edge of a 360px phone,
    // because its row was a flex line that could not wrap.
    const rows = [...page.matchAll(/class="(?:[^"]* )?flex gap-\d+[^"]*"/g)].map((m) => m[0]);
    assert.ok(rows.length > 0);
    for (const row of rows) assert.match(row, /flex-wrap/, row);
});

test("the run buttons are pushed right as a pair, and take a line of their own on a phone", () => {
    // Not with a .flex-fill spacer: PocketBase gives that `flex: 1 1 auto
    // !important`, which no rule here can override, so a phone line break
    // built on it never happened.
    assert.doesNotMatch(runActions(), /flex-fill/);
    assert.match(css, /\.import-run-actions \{[^}]*margin-left: auto/);
    const phone = css.slice(css.indexOf("@media (max-width: 600px) {\n    .import-run-actions"));
    assert.match(phone, /^@media \(max-width: 600px\) \{\s*\.import-run-actions \{[^}]*flex: 1 1 100%/);
});

test("the kind tabs wrap onto a second line rather than crush their labels", () => {
    assert.match(css, /\.import-panel \.tabs-header \{[^}]*flex-wrap: wrap/);
    assert.match(css, /\.import-panel \.tabs-header \.tab-item \{[^}]*white-space: nowrap/);
});

test("a result is shown only under the kind it was run for", () => {
    // A products dry run, still on screen under the Orders tab, said "Press
    // Import to apply the file" about a file that was never an orders file.
    const run = { kind: "products", results: [{ name: "a.csv", result: { created: 1, dry_run: true } }] };
    assert.equal(resultFor(run, "products"), run.results);
    assert.equal(resultFor(run, "orders"), null);
    assert.equal(resultFor(null, "products"), null);
});

test("a new file or a cleared box drops the last result", () => {
    assert.match(body("addFiles"), /run = null;/);
    assert.match(body("clearCsv"), /run = null;/);
    assert.match(page, /onclick=\{clearCsv\}/);
});

test("a CSV that has only been dry-run is still unsaved", () => {
    // Dry run, then Import, is the path the screen asks for; leaving between
    // the two would lose a file nothing has written.
    const one = importSources([], "sku\nA");
    assert.equal(unsavedImport(importSources([], ""), null), false);
    assert.equal(unsavedImport(importSources([], "  "), null), false);
    assert.equal(unsavedImport(one, null), true);
    assert.equal(unsavedImport(one, { dry: true }), true);
    assert.equal(unsavedImport(one, { dry: false }), false);
    assert.equal(unsavedImport(importSources([{ name: "a.csv", text: "sku" }], ""), null), true);
    assert.match(page, /<DirtyGuard\s+dirty=\{unsavedImport\(sources, run\)\}/);
});

test("nothing to import is the reason, and a file or a pasted box clears it", () => {
    assert.equal(
        importBlocker(importSources([], "")),
        "Nothing to import yet: drop or choose CSV files, or paste CSV into the box above.",
    );
    assert.equal(importBlocker(importSources([], "  \n\t")), importBlocker([]));
    assert.equal(importBlocker(importSources([], "product_slug,title\nmug,Mug\n")), "");
    assert.equal(importBlocker(importSources([{ name: "a.csv", text: "x" }], "")), "");
});

test("the files run first, in the order given, and the box last", () => {
    const files = [
        { name: "a.csv", text: "1" },
        { name: "b.csv", text: "2" },
    ];
    assert.deepEqual(
        importSources(files, "3").map((s) => s.name),
        ["a.csv", "b.csv", "Pasted CSV"],
    );
    assert.deepEqual(
        importSources(files, " ").map((s) => s.name),
        ["a.csv", "b.csv"],
    );
});

test("only CSV files are taken from a drop, and the rest are named", () => {
    const { ok, refused } = csvFiles([
        { name: "a.CSV", type: "" },
        { name: "b.txt", type: "text/csv" },
        { name: "photo.jpg", type: "image/jpeg" },
    ]);
    assert.deepEqual(
        ok.map((f) => f.name),
        ["a.CSV", "b.txt"],
    );
    assert.deepEqual(refused, ["photo.jpg"]);
});

test("a run's totals count a refused file as failed and keep going", () => {
    assert.deepEqual(
        runTotals([
            { name: "a.csv", result: { created: 2, updated: 1, deleted: 0, errors: [{ line: 3 }] } },
            { name: "b.csv", error: "validation_failed" },
            { name: "c.csv", result: { created: 0, updated: 0, deleted: 4, errors: [] } },
        ]),
        { files: 3, failed: 1, created: 2, updated: 1, deleted: 4, attention: 1 },
    );
});

test("only the keyed kinds offer modes, all four, upsert first", () => {
    assert.deepEqual(
        importModes("products").map((m) => m.label),
        ["Upsert", "Insert only", "Update only", "Delete"],
    );
    assert.deepEqual(
        importModes("categories").map((m) => m.value),
        ["upsert", "insert", "update", "delete"],
    );
    assert.match(importModes("categories")[3].help, /by its path/);
    for (const kind of ["inventory", "orders", "reviews", "menus"]) assert.deepEqual(importModes(kind), []);
});

test("a delete is rehearsed and confirmed before it is sent", () => {
    // Import under Delete runs a dry run and opens the confirmation; only
    // the confirmation runs the real thing.
    const run = body("runImport");
    assert.match(run, /activeMode === "delete"/);
    assert.match(run, /await runAll\(true\)/);
    assert.match(run, /deleteOpen = true;\s*return;/);
    assert.match(body("confirmDelete"), /runAll\(false\)/);
    assert.match(page, /<Confirm\s+bind:open=\{deleteOpen\}\s+danger[\s\S]*?onconfirm=\{confirmDelete\}/);
});

test("several files can be dropped or picked, and one that fails does not stop the next", () => {
    assert.match(page, /<input\s+type="file"\s+accept="\.csv,text\/csv"\s+multiple/);
    assert.match(page, /ondrop=\{onDrop\}/);
    // The try/catch is inside the loop, so a refused file is that file's
    // result rather than the end of the run.
    assert.match(body("runAll"), /for \(const src of ran\) \{\s*try \{/);
});

test("after a real run only the files that went in leave the list", () => {
    // A refused file stays to be fixed and run again, and a file dropped
    // while the run was going was never part of it.
    const all = page.slice(page.indexOf("async function runAll("), page.indexOf("function report("));
    assert.match(all, /if \(src\.id\) applied\.add\(src\.id\);/);
    assert.match(all, /if \(!dry\) files = files\.filter\(\(f\) => !applied\.has\(f\.id\)\);/);
    assert.doesNotMatch(all, /files = \[\];/);
});

test("a dry run asks the engine to roll back; Import does not", () => {
    assert.equal(importUrl("products", { dryRun: true }), "/api/admin/import/products?dry_run=1");
    assert.equal(importUrl("products", { dryRun: false }), "/api/admin/import/products");
});

test("each switch reaches only the kind it belongs to", () => {
    assert.equal(importUrl("products", { mode: "insert" }), "/api/admin/import/products?mode=insert");
    assert.equal(
        importUrl("categories", { mode: "delete", dryRun: true }),
        "/api/admin/import/categories?dry_run=1&mode=delete",
    );
    assert.equal(importUrl("products", { mode: "upsert" }), "/api/admin/import/products");
    assert.equal(importUrl("inventory", { mode: "delete" }), "/api/admin/import/inventory");
    assert.equal(importUrl("menus", { mode: "update" }), "/api/admin/x/navigation/import");
    assert.equal(importUrl("orders", { fireEvents: true }), "/api/admin/import/orders?fire_events=1");
    assert.equal(importUrl("products", { fireEvents: true }), "/api/admin/import/products");
    assert.equal(
        importUrl("orders", { dryRun: true, fireEvents: true }),
        "/api/admin/import/orders?dry_run=1&fire_events=1",
    );
});

test("a module's file goes through the module's own door", () => {
    assert.equal(importUrl("reviews", { dryRun: true }), "/api/admin/x/reviews/import?dry_run=1");
    assert.equal(importUrl("menus"), "/api/admin/x/navigation/import");
    assert.equal(importUrl("categories"), "/api/admin/import/categories");
});

test("a module that is not installed offers no kind", () => {
    const none = importKinds(() => false).map((k) => k.value);
    assert.deepEqual(none, ["products", "inventory", "orders", "categories"]);
    const all = importKinds(() => true).map((k) => k.value);
    assert.deepEqual(all, ["products", "inventory", "orders", "categories", "reviews", "menus"]);
});

test("the dialect is read off the first line", () => {
    assert.equal(detectDialect("products", "Handle,Title\nmug,Mug"), "shopify");
    assert.equal(detectDialect("products", '"product_slug",title'), "gocommerce");
    assert.equal(detectDialect("products", "name,price"), "");
    assert.equal(detectDialect("orders", "Name,Lineitem quantity"), "shopify");
    assert.equal(detectDialect("orders", "number,email"), "gocommerce");
    assert.equal(detectDialect("inventory", "Handle,On hand"), "shopify");
    assert.equal(detectDialect("inventory", "sku,location"), "gocommerce");
    assert.equal(detectDialect("categories", "path,slug"), "");
    assert.equal(detectDialect("products", ""), "");
});
