import { test } from "node:test";
import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";

const page = readFileSync(new URL("../src/routes/dash/support/+page.svelte", import.meta.url), "utf8");

test("every setup link on the Support page is a screen the panel has", () => {
    const hrefs = [...page.matchAll(/href: "(\/dash\/[^"]+)"/g)].map((m) => m[1]);
    assert.ok(hrefs.length >= 6);
    for (const href of hrefs) {
        const file = new URL(`../src/routes${href}/+page.svelte`, import.meta.url);
        assert.ok(existsSync(file), `${href} has no page`);
    }
});

test("a feature request says it is one and carries what was typed", () => {
    assert.match(page, /Request a new feature for free/);
    assert.match(page, /Feature request\$\{store \? ` from my store \$\{store\}` : ""\}: \$\{asked\}/);
});

test("the WhatsApp section keeps its promise about messages in view", () => {
    assert.match(page, /No unwanted, promotional or spam messages\. Guaranteed\./);
});
