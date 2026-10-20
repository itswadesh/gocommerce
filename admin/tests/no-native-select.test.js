/*
 * Every select in the panel is Select.svelte: shadcn's menu on the panel's own
 * colours. A native <select> opens a list the browser draws, which does not
 * follow those colours — the owner found it wrong in dark mode — and its
 * closed box sits differently from the selects around it.
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
        return name.endsWith(".svelte") ? [p] : [];
    });
}

// Markup only: a comment may still name the element it is explaining. Blanked
// rather than removed, so a finding keeps its real line number.
const blank = (m) => m.replace(/[^\n]/g, " ");
const stripComments = (src) =>
    src.replace(/<!--[\s\S]*?-->/g, blank).replace(/\/\*[\s\S]*?\*\//g, blank).replace(/\/\/.*$/gm, blank);

test("no screen draws a native select", () => {
    const native = [];
    for (const file of files(fileURLToPath(new URL("../src", import.meta.url)))) {
        const src = stripComments(readFileSync(file, "utf8"));
        for (const m of src.matchAll(/<select[\s>]/g)) {
            const line = src.slice(0, m.index).split("\n").length;
            native.push(`${file.replace(/^.*[\\/]src[\\/]/, "src/")}:${line}`);
        }
    }
    assert.deepEqual(native, []);
});
