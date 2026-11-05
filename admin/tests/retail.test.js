import { test } from "node:test";
import assert from "node:assert/strict";

import { isMismatch, moneyNote, newKey, statusOf } from "../src/lib/retail.js";

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

test("each press gets its own name", () => {
    assert.notEqual(newKey(), newKey());
});
