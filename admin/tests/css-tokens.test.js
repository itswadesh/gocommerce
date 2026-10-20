/*
 * Every CSS variable the panel reads is declared somewhere. A rule naming one
 * that is not falls back to inheriting, silently: forty-odd rules asked for
 * --txtPrimaryColor and --txtHintColor, which nothing declared, so hints read
 * at full strength and a colour-mix() naming one was dropped whole, taking a
 * status colour with it — in dark mode, the chosen tab looked like the others.
 * A read with a fallback, var(--x, …), is allowed: it says what happens.
 *
 * A declaration inside @media print does not count: that is how those two
 * names hid, declared for printing only. PocketBase's own stylesheets are
 * read for declarations but not checked, since this repository may not edit
 * them (CLAUDE.md).
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

function files(dir) {
    return readdirSync(dir).flatMap((name) => {
        const p = join(dir, name);
        if (statSync(p).isDirectory()) return files(p);
        return /\.(svelte|css|js|html)$/.test(name) ? [p] : [];
    });
}

const src = fileURLToPath(new URL("../src", import.meta.url));
const sources = files(src).map((f) => [f, readFileSync(f, "utf8")]);
const vendored = (file) => /[\\/]styles[\\/]/.test(file) && !/(gocommerce|fonts-inter)\.css$/.test(file);

// The text with every @media print block taken out, brace by brace.
function withoutPrint(text) {
    let out = "";
    let i = 0;
    for (const m of text.matchAll(/@media\s+print\s*\{/g)) {
        if (m.index < i) continue;
        out += text.slice(i, m.index);
        let depth = 0;
        let j = m.index + m[0].length - 1;
        for (; j < text.length; j++) {
            if (text[j] === "{") depth++;
            else if (text[j] === "}" && --depth === 0) break;
        }
        i = j + 1;
    }
    return out + text.slice(i);
}

/*
 * Size tokens read but never declared, so each of these rules has never
 * applied: the margin is 0 and the font size is inherited. Declaring them
 * would move layouts that look settled, which is its own change; listed here
 * so a new one still fails.
 */
const KNOWN = new Set(["--xsSpacing", "--xsFontSize", "--baseSpacing"]);

// Declared: `--x:` in a stylesheet or a style attribute, `style:--x=` in
// Svelte markup, or setProperty("--x", …) from a script.
const declared = new Set();
for (const [, raw] of sources) {
    // Comments out too: one saying "--txtHintColor: neither of those is
    // declared" read as a declaration of it.
    const text = withoutPrint(raw).replace(/\/\*[\s\S]*?\*\//g, "").replace(/<!--[\s\S]*?-->/g, "");
    for (const m of text.matchAll(/(?:^|[\s;{"'`(])(--[\w-]+)\s*:/g)) declared.add(m[1]);
    for (const m of text.matchAll(/style:(--[\w-]+)/g)) declared.add(m[1]);
    for (const m of text.matchAll(/setProperty\(\s*["'`](--[\w-]+)/g)) declared.add(m[1]);
}

test("every CSS variable the panel reads is declared", () => {
    const missing = [];
    for (const [file, text] of sources) {
        if (vendored(file)) continue;
        // var(--x) with no fallback: the closing bracket straight after the name.
        for (const m of text.matchAll(/var\(\s*(--[\w-]+)\s*\)/g)) {
            if (declared.has(m[1]) || KNOWN.has(m[1])) continue;
            const line = text.slice(0, m.index).split("\n").length;
            missing.push(`${file.replace(/^.*[\\/]src[\\/]/, "src/")}:${line} ${m[1]}`);
        }
    }
    assert.deepEqual(missing, []);
});
