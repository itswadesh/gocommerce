/*
 * The product editor's Complete the set and You may also like cards (D133):
 * add-ons carry an optional offer price typed in the store's currency, and
 * both lists are saved whole, in the order shown.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import { addOnRows, addOnsBody, offerProblem, moved, rowFromVariant } from "../src/lib/upsells.js";

const addOn = (over = {}) => ({
    id: 1,
    variant_id: 11,
    offer_price: { amount_minor: 4900, currency: "INR" },
    price: { amount_minor: 12900, currency: "INR" },
    item: { title: "Phone grip", variant_label: "", image_url: "/m/grip.jpg", live: true },
    ...over,
});

test("saved add-ons become rows with the offer as the shop typed it", () => {
    const rows = addOnRows([addOn(), addOn({ id: 2, variant_id: 12, offer_price: null })], "INR");
    assert.equal(rows[0].offer, "49.00");
    assert.equal(rows[1].offer, "", "no offer price is an empty box, not zero");
    assert.equal(rows[0].title, "Phone grip");
});

test("a variant picked from search becomes a row with no offer yet", () => {
    const row = rowFromVariant({ id: 21, price: { amount_minor: 9900, currency: "INR" }, label: "M",
        product: { id: 5, title: "Key chain", image_url: "" } });
    assert.deepEqual([row.variant_id, row.title, row.label, row.offer], [21, "Key chain", "M", ""]);
});

test("the body sends minor units, and null where the box is empty", () => {
    const rows = addOnRows([addOn(), addOn({ id: 2, variant_id: 12, offer_price: null })], "INR");
    assert.deepEqual(addOnsBody(rows, "INR"), {
        add_ons: [
            { variant_id: 11, price_minor: 4900 },
            { variant_id: 12, price_minor: null },
        ],
    });
});

test("an unreadable or negative offer is a problem; one above the price is a warning", () => {
    const [row] = addOnRows([addOn()], "INR");
    assert.equal(offerProblem({ ...row, offer: "abc" }, "INR").error, true);
    assert.equal(offerProblem({ ...row, offer: "-1" }, "INR").error, true);
    const above = offerProblem({ ...row, offer: "150" }, "INR");
    assert.equal(above.error, false);
    assert.match(above.message, /not below/);
    assert.equal(offerProblem({ ...row, offer: "" }, "INR").message, "");
    assert.throws(() => addOnsBody([{ ...row, offer: "abc" }], "INR"), /offer price/);
});

test("rows move one place and stay put at the ends", () => {
    assert.deepEqual(moved([1, 2, 3], 0, 1), [2, 1, 3]);
    assert.deepEqual(moved([1, 2, 3], 2, 1), [1, 2, 3]);
    assert.deepEqual(moved([1, 2, 3], 0, -1), [1, 2, 3]);
});
