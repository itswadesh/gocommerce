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
import { detectDialect, importBlocker, importKinds, importUrl } from "../src/lib/importer.js";

const read = (p) => readFileSync(new URL(p, import.meta.url), "utf8");
const page = read("../src/routes/dash/data/import/+page.svelte");
const css = read("../src/lib/styles/gocommerce.css");

/* The block of buttons that runs a file, from its opening tag to its close. */
function runActions() {
    const start = page.indexOf('class="import-run');
    assert.ok(start >= 0, "the screen has an import-run row");
    return page.slice(start, page.indexOf("</div>", page.indexOf("runImport(false)", start)));
}

test("the screen always offers a button called Import", () => {
    const row = runActions();
    assert.match(row, /onclick=\{\(\) => runImport\(false\)\}/);
    assert.match(row, /<span class="txt">Import<\/span>/);
    // The name never depends on a switch again.
    assert.doesNotMatch(page, /dryRun \? "Dry run" : "Import"/);
});

test("a dry run is its own button beside Import, not a switch that renames it", () => {
    const row = runActions();
    assert.match(row, /onclick=\{\(\) => runImport\(true\)\}/);
    assert.match(row, /<span class="txt">Dry run<\/span>/);
    assert.doesNotMatch(page, /id="dry-run"/);
});

test("a run button that cannot be pressed yet says why, beside it", () => {
    const row = runActions();
    assert.match(row, /aria-describedby=\{blocker \? "import-blocker" : undefined\}/);
    assert.match(page, /id="import-blocker"[^>]*>\s*\{blocker\}/);
});

test("no row of buttons on the screen is barred from wrapping", () => {
    // "Field definitions only" ran off the right edge of a 360px phone,
    // because its row was a flex line that could not wrap.
    const rows = [...page.matchAll(/class="(?:[^"]* )?flex gap-\d+[^"]*"/g)].map((m) => m[0]);
    assert.ok(rows.length > 0);
    for (const row of rows) assert.match(row, /flex-wrap/, row);
});

test("the kind tabs wrap onto a second line rather than crush their labels", () => {
    assert.match(css, /\.import-panel \.tabs-header \{[^}]*flex-wrap: wrap/);
    assert.match(css, /\.import-panel \.tabs-header \.tab-item \{[^}]*white-space: nowrap/);
});

test("nothing in the box is the reason, and anything in it clears it", () => {
    assert.equal(importBlocker(""), "Nothing to import yet: choose a file, or paste CSV into the box above.");
    assert.equal(importBlocker("  \n\t"), importBlocker(""));
    assert.equal(importBlocker("product_slug,title\nmug,Mug\n"), "");
});

test("a dry run asks the engine to roll back; Import does not", () => {
    assert.equal(importUrl("products", { dryRun: true }), "/api/admin/import/products?dry_run=1");
    assert.equal(importUrl("products", { dryRun: false }), "/api/admin/import/products");
});

test("each switch reaches only the kind it belongs to", () => {
    assert.equal(importUrl("products", { overwrite: false }), "/api/admin/import/products?overwrite=0");
    assert.equal(importUrl("inventory", { overwrite: false }), "/api/admin/import/inventory");
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
