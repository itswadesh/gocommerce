import { readFileSync } from "node:fs";
import { test } from "node:test";
import assert from "node:assert/strict";

import { aiReadable, isMismatch, moneyNote, newKey, ruleExample, statusOf, wholeQuantity } from "../src/lib/retail.js";
import { HINDI, translate } from "../src/lib/retail-hindi.js";

test("the two pricing rules say different prices", () => {
    assert.match(ruleExample("markup", 20), /₹120\.00/);
    assert.match(ruleExample("margin", 20), /₹125\.00/);
    assert.match(ruleExample("margin", 100), /under 100/);
    assert.equal(ruleExample("markup", NaN), "");
});

test("only a PDF or a picture goes to a model", () => {
    assert.equal(aiReadable("application/pdf"), true);
    assert.equal(aiReadable("image/jpeg"), true);
    assert.equal(aiReadable("text/csv"), false);
});

test("only a whole quantity is prefilled", () => {
    assert.equal(wholeQuantity("10"), "10");
    assert.equal(wholeQuantity(" 10.00 "), "10");
    assert.equal(wholeQuantity("2.5"), "");
    assert.equal(wholeQuantity("0"), "");
    assert.equal(wholeQuantity("10 pcs"), "");
    assert.equal(wholeQuantity(null), "");
});

test("a status with no wording of its own still reads", () => {
    assert.equal(statusOf("needs_review").label, "Needs review");
    assert.equal(statusOf("new_one").label, "new_one");
});

test("only figures that do not add up may be waved through", () => {
    assert.equal(isMismatch("The lines add up to 100, which matches neither the subtotal nor the total."), true);
    assert.equal(isMismatch("The supplier is missing."), false);
});

test("the note says only what is there", () => {
    const fmt = (m) => String(m.amount_minor);
    assert.equal(moneyNote({ advance: { amount_minor: 0 }, unverified: { amount_minor: 0 } }, fmt), "");
    assert.equal(moneyNote({ advance: { amount_minor: 5 }, unverified: { amount_minor: 7 } }, fmt), "5 paid in advance · 7 not verified yet");
});

test("a screen's words are all in the Hindi list, with the same blanks", () => {
    const base = "src/routes/dash/retail-assistant/";
    const screens = ["+page.svelte", "suppliers/+page.svelte", "bank/+page.svelte", "insights/+page.svelte", "wholesale/+page.svelte"];
    const missing = [];
    for (const screen of screens) {
        const source = readFileSync(base + screen, "utf8");
        for (const [, key] of source.matchAll(/\bt\("((?:[^"\\]|\\.)*)"/g)) {
            if (!(key in HINDI)) missing.push(`${screen}: ${key}`);
        }
    }
    assert.deepEqual(missing, []);
    const blanks = (s) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort().join();
    for (const [english, hindi] of Object.entries(HINDI)) {
        assert.equal(blanks(hindi), blanks(english), english);
    }
});

test("an unknown key reads in English and the blanks are filled", () => {
    assert.equal(translate("hi", "Nothing in the list"), "Nothing in the list");
    assert.equal(translate("hi", "{n} left", { n: 3 }), "3 बचे");
    assert.equal(translate("en", "{n} left", { n: 3 }), "3 left");
});

test("each press gets its own name", () => {
    assert.notEqual(newKey(), newKey());
});
