/*
 * Prices typed straight into the products list: what a box means, what a row
 * shows when its variants disagree, and which products may be published.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import { commonValue, mixed, needsPrice, readMRP, readPrice, splitPublishable, withAmount } from "../src/lib/listprices.js";

const money = (amount_minor) => ({ amount_minor, currency: "INR" });
const v = (price, mrp = null) => ({ price: money(price), compare_at_price: mrp === null ? null : money(mrp) });

test("a product needs a price while any variant is at 0", () => {
    assert.equal(needsPrice({ variants: [v(49900), v(0)] }), true);
    assert.equal(needsPrice({ variants: [v(49900)] }), false);
});

test("a row shows the variants' common price, and nothing when they differ or are unpriced", () => {
    assert.equal(commonValue([v(49900), v(49900)], "price", "INR"), "499.00");
    assert.equal(commonValue([v(49900), v(59900)], "price", "INR"), "");
    assert.equal(mixed([v(49900), v(59900)], "price"), true);
    assert.equal(commonValue([v(0), v(0)], "price", "INR"), "", "0 is no price, so the box is empty");
    assert.equal(commonValue([v(100, 900)], "compare_at_price", "INR"), "9.00");
    assert.equal(commonValue([v(100)], "compare_at_price", "INR"), "");
    // Currencies without decimals are read in their own units.
    assert.equal(commonValue([{ price: { amount_minor: 500, currency: "JPY" } }], "price", "JPY"), "500");
});

test("a price box is minor units, never a silent 0", () => {
    assert.deepEqual(readPrice("499", "INR"), { minor: 49900 });
    assert.deepEqual(readPrice("1,299.50", "INR"), { minor: 129950 });
    assert.deepEqual(readPrice("  ", "INR"), { empty: true });
    assert.ok(readPrice("0", "INR").error);
    assert.ok(readPrice("abc", "INR").error);
    assert.deepEqual(readPrice("500", "JPY"), { minor: 500 });
});

test("an MRP is above the price, and an emptied box takes it off", () => {
    assert.deepEqual(readMRP("999", "INR", 49900), { minor: 99900 });
    assert.deepEqual(readMRP("", "INR", 49900), { minor: null });
    assert.ok(readMRP("499", "INR", 49900).error, "an MRP equal to the price strikes nothing through");
    assert.ok(readMRP("x", "INR", 49900).error);
});

test("publishing refuses the products that still need a price", () => {
    const products = [
        { id: 1, status: "draft", variants: [v(49900)] },
        { id: 2, status: "draft", variants: [v(49900), v(0)] },
        { id: 3, status: "active", variants: [v(100)] },
    ];
    const { ok, refused } = splitPublishable(products);
    assert.deepEqual(ok.map((p) => p.id), [1]);
    assert.deepEqual(refused.map((p) => p.id), [2]);
});

test("an optimistic row carries the new amount in minor units", () => {
    assert.deepEqual(withAmount(v(0), "price", 49900, "INR").price, money(49900));
    assert.equal(withAmount(v(100, 900), "compare_at_price", null, "INR").compare_at_price, null);
});
