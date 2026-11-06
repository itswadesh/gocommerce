/*
 * A form on a page saves through the save bar (D218): it appears once something
 * differs from the saved copy, and there is no Save button on the page to get
 * out of step with it. Drawers and dialogs keep their own.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

function pages(dir) {
    return readdirSync(dir).flatMap((name) => {
        const p = join(dir, name);
        if (statSync(p).isDirectory()) return pages(p);
        return name === "+page.svelte" ? [p] : [];
    });
}

// Not a form being saved: the last step of proving a new address by code.
const ALLOWED = ["Save this email"];

const onThePage = (src) =>
    src
        .replace(/<!--[\s\S]*?-->/g, "")
        .replace(/<Drawer\b[\s\S]*?<\/Drawer>/g, "");

test("no page draws its own Save button outside a drawer", () => {
    const found = [];
    for (const file of pages(fileURLToPath(new URL("../src/routes/dash", import.meta.url)))) {
        const src = onThePage(readFileSync(file, "utf8"));
        for (const [, label] of src.matchAll(/<span class="txt">([^<]*\bSave\b[^<]*)<\/span>/g)) {
            if (ALLOWED.some((a) => label.includes(a))) continue;
            found.push(`${file.replace(/^.*[\\/]dash[\\/]/, "dash/")}: ${label.trim()}`);
        }
    }
    assert.deepEqual(found, []);
});
