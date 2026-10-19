/*
 * The sample shop's banner and erase dialog name what the store holds, in a
 * store owner's words (D96): the kinds they would recognise, in the order a
 * shop is browsed, and nothing the store does not have.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import { describeSamples, SAMPLE_RIGHTS } from "../src/lib/samples.js";

test("the counts read as a sentence, in browsing order", () => {
    const counts = { orders: 12, products: 8, faq: 5, menu_links: 7, reviews: 8, pages: 3, categories: 4 };
    assert.equal(
        describeSamples(counts),
        "8 products, 4 categories, 12 orders, 8 reviews, 3 pages, 5 questions and 7 menu links",
    );
});

test("one of a kind is singular, and an empty kind is not mentioned", () => {
    assert.equal(describeSamples({ products: 1, discounts: 0, orders: 2 }), "1 product and 2 orders");
    assert.equal(describeSamples({ categories: 1 }), "1 category");
    assert.equal(describeSamples({}), "");
});

test("customers are the orders' shoppers, not a separate thing to erase", () => {
    assert.equal(describeSamples({ orders: 12, customers: 6 }), "12 orders");
});

test("a kind the panel does not know yet is not shown as a raw key", () => {
    assert.equal(describeSamples({ products: 8, widgets: 3 }), "8 products");
});

test("banners are named with the rest", () => {
    assert.equal(describeSamples({ products: 8, banners: 7 }), "8 products and 7 banners");
});

test("adding or erasing asks for every right the engine checks", () => {
    assert.deepEqual(
        [...SAMPLE_RIGHTS].sort(),
        ["catalog.write", "categories.write", "collections.write", "discounts.write", "media.write", "orders.write"],
    );
});
