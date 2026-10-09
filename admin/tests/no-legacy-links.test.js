/*
 * No link in the panel may point at a pre-/dash address. The redirect would
 * catch it, but a link that works only by redirect is a page load spent on
 * every click, and the next rename would turn it into a 404.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { dashPath } from "../src/lib/paths.js";

function files(dir) {
    return readdirSync(dir).flatMap((name) => {
        const p = join(dir, name);
        if (statSync(p).isDirectory()) return name === "styles" ? [] : files(p);
        return /\.(svelte|js)$/.test(name) ? [p] : [];
    });
}

// {base}/x, ${base}/x, base + "/x", href: "/x", goto("/x")
const LINK = /(?:\{base\}|\$\{base\}|base \+ ["'`]|href: ["'`]|goto\(["'`])(\/[a-z0-9\-_/]*)/g;

// Links kept in data rather than markup — a lookup table, a function that
// returns a path — escape the shapes above, so any string literal that starts
// with a pre-/dash screen name counts too.
const OLD_SCREENS = [
    "orders", "products", "customers", "discounts", "notifications", "settings",
    "shipping", "carts", "locations", "cms", "inventory", "reports", "b2b",
];
const LITERAL = new RegExp(`["'\`](\\/(?:${OLD_SCREENS.join("|")})(?:[\\/?"'\`$]|$)[^"'\`]*)`, "g");

test("no string literal names a pre-/dash screen", () => {
    const stale = [];
    for (const file of files(fileURLToPath(new URL("../src", import.meta.url)))) {
        if (file.endsWith("paths.js") || /routes[\\/](portal|platform)[\\/]/.test(file)) continue;
        const src = readFileSync(file, "utf8");
        for (const m of src.matchAll(LITERAL)) stale.push(`${file}: ${m[0]}`);
    }
    assert.deepEqual(stale, []);
});

test("every panel link points at its /dash address", () => {
    const stale = [];
    for (const file of files(fileURLToPath(new URL("../src", import.meta.url)))) {
        const src = readFileSync(file, "utf8");
        for (const m of src.matchAll(LINK)) {
            const path = m[1].replace(/\/$/, "") || "/";
            if (dashPath(path) !== null) stale.push(`${file}: ${m[0]}`);
        }
    }
    assert.deepEqual(stale, []);
});
