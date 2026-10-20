/*
 * The marks on Payment methods and Shipping providers are keyed by provider
 * code, and a key that matches nothing fails quietly: the provider gets the
 * generic card or truck and nobody notices the logo went missing. So the keys
 * are checked against the codes the engine's modules actually register, read
 * from the Go source, and every glyph against the icon font the panel ships.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { BRAND_MARKS, CARRIER_LOGOS, METHOD_GLYPHS, KIND_GLYPHS, providerIcon } from "../src/lib/provider-icons.js";

const repo = fileURLToPath(new URL("../../", import.meta.url));

/** The code every provider in core/ and ext/* returns from Code(). */
function registeredCodes(only = () => true) {
    const dirs = only("core") ? [join(repo, "core")] : [];
    for (const name of readdirSync(join(repo, "ext"))) {
        const dir = join(repo, "ext", name);
        if (statSync(dir).isDirectory() && only(name)) dirs.push(dir);
    }
    const codes = new Set();
    for (const dir of dirs) {
        const src = readdirSync(dir)
            .filter((f) => f.endsWith(".go") && !f.endsWith("_test.go"))
            .map((f) => readFileSync(join(dir, f), "utf8"))
            .join("\n");
        for (const m of src.matchAll(/\)\s*Code\(\)\s*string\s*\{\s*return\s+("([^"]+)"|\w+)\s*\}/g)) {
            if (m[2]) {
                codes.add(m[2]);
                continue;
            }
            // A named constant: resolve it within the same package.
            const c = src.match(new RegExp(`\\b${m[1]}\\s*=\\s*"([^"]+)"`));
            if (c) codes.add(c[1]);
        }
    }
    return codes;
}

test("every brand mark belongs to a provider the engine registers", () => {
    const codes = registeredCodes();
    assert.ok(codes.has("stripe") && codes.has("delhivery"), "the Go source was read");
    assert.deepEqual(Object.keys(BRAND_MARKS).filter((c) => !codes.has(c)), []);
});

test("every built-in method's glyph belongs to a registered code", () => {
    const codes = registeredCodes();
    assert.deepEqual(Object.keys(METHOD_GLYPHS).filter((c) => !codes.has(c)), []);
});

test("a brand mark is path data, not pasted markup", () => {
    for (const [code, mark] of Object.entries(BRAND_MARKS)) {
        assert.match(mark.path, /^[Mm][0-9MmLlHhVvCcSsQqTtAaZz.,\-\s]+$/, code);
    }
});

test("each glyph is an icon the panel's font has", () => {
    const css = readFileSync(fileURLToPath(new URL("../src/lib/remixicon.css", import.meta.url)), "utf8");
    const glyphs = [...Object.values(METHOD_GLYPHS), ...Object.values(KIND_GLYPHS), "ri-plug-line"];
    for (const g of glyphs) assert.ok(css.includes(`.${g}:before`), g);
});

test("every carrier module has its own logo, and each logo its file", () => {
    // The carriers are the ext/fulfill-* modules; shipping by hand is core's,
    // and is not a company.
    const carriers = registeredCodes((name) => name.startsWith("fulfill-"));
    assert.ok(carriers.has("delhivery") && carriers.has("usps"), "the carriers' Go source was read");
    assert.deepEqual([...carriers].filter((c) => !CARRIER_LOGOS.includes(c)), []);
    assert.deepEqual(CARRIER_LOGOS.filter((c) => !carriers.has(c)), []);
    for (const code of CARRIER_LOGOS) {
        const file = fileURLToPath(new URL(`../static/images/providers/${code}.png`, import.meta.url));
        const png = readFileSync(file);
        assert.equal(png.subarray(1, 4).toString(), "PNG", code);
        // 64 by 64: twice the size it is drawn at, and no more.
        assert.deepEqual([png.readUInt32BE(16), png.readUInt32BE(20)], [64, 64], code);
    }
});

test("a provider gets its logo, then its mark, then its method's glyph, then its kind's", () => {
    assert.deepEqual(providerIcon("delhivery", "shipping"), { logo: "/images/providers/delhivery.png" });
    assert.deepEqual(providerIcon("usps", "shipping"), { logo: "/images/providers/usps.png" });
    assert.deepEqual(providerIcon("stripe", "payments"), { path: BRAND_MARKS.stripe.path, wide: false });
    assert.deepEqual(providerIcon("cod", "payments"), { glyph: "ri-hand-coin-line" });
    assert.deepEqual(providerIcon("manual", "shipping"), { glyph: "ri-box-3-line" });
    assert.deepEqual(providerIcon("helcim", "payments"), { glyph: "ri-bank-card-line" });
    assert.deepEqual(providerIcon("a-new-carrier", "shipping"), { glyph: "ri-truck-line" });
    assert.deepEqual(providerIcon("something-new", "elsewhere"), { glyph: "ri-plug-line" });
});
