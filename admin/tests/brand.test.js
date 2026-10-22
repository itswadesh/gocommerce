/*
 * The panel shows the brand of the domain it is opened on (D111): one install
 * answers under several names, so no screen may print a product name of its
 * own. These read source, as tests/roles.test.js does, because node:test
 * cannot import a .svelte file.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const src = fileURLToPath(new URL("../src", import.meta.url));
const read = (p) => readFileSync(new URL(p, import.meta.url), "utf8");

function files(dir) {
    return readdirSync(dir).flatMap((name) => {
        const p = join(dir, name);
        if (statSync(p).isDirectory()) return files(p);
        return name.endsWith(".svelte") ? [p] : [];
    });
}

const stripComments = (s) => s.replace(/<!--[\s\S]*?-->/g, "").replace(/\/\*[\s\S]*?\*\//g, "").replace(/\/\/.*$/gm, "");

// The trade portal and a printed packing slip are the store's own pages, for
// its buyers and its parcels, and carry the store's name instead.
const storesOwn = (file) => /[\\/]routes[\\/]portal[\\/]|[\\/]print[\\/]/.test(file);

test("every page title carries the brand rather than a name of its own", () => {
    const wrong = [];
    for (const file of files(src).filter((f) => !storesOwn(f))) {
        const text = stripComments(readFileSync(file, "utf8"));
        for (const m of text.matchAll(/<title>([\s\S]*?)<\/title>/g)) {
            if (/GoCommerce/.test(m[1]) || !/titled\(|brand\.name/.test(m[1])) {
                wrong.push(`${file.replace(src, "src")}: ${m[0]}`);
            }
        }
    }
    assert.deepEqual(wrong, []);
});

test("the screens before a store and the header show the brand's name and logo", () => {
    for (const path of ["../src/lib/components/AuthShell.svelte", "../src/routes/+layout.svelte"]) {
        const markup = stripComments(read(path));
        assert.doesNotMatch(markup, />\s*GoCommerce\s*</, `${path} prints a product name`);
        assert.match(markup, /\{brand\.name\}/, `${path} does not print the brand's name`);
        assert.match(markup, /brand\.logo_url/, `${path} has no place for the brand's logo`);
    }
    assert.match(read("../src/routes/+layout.svelte"), /loadBrand\(\)/);
});

test("the brand comes from auth-state and is remembered per address", () => {
    const module = read("../src/lib/brand.svelte.js");
    assert.match(module, /auth\s*\.state\(\)/);
    assert.match(module, /localStorage/);
    assert.match(module, /export function titled/);
    // A first visit shows no name rather than another brand's.
    assert.match(module, /name: start\?\.name \?\? ""/);
    assert.match(read("../src/lib/components/Login.svelte"), /applyBrand\(s\)/);
});
