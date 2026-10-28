/*
 * The Pages screen offers to add the pages a storefront links to and the
 * shop has not written. Which ones it offers, and what they say, is here.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import { STANDARD_PAGES, missingStandardPages } from "../src/lib/standard-pages.js";

test("the slugs are the ones the storefront links to", () => {
    assert.deepEqual(STANDARD_PAGES.map((p) => p.slug), [
        "about-us",
        "contact-us",
        "privacy-policy",
        "terms-and-conditions",
        "shipping-policy",
        "refund-policy",
    ]);
});

test("only the pages the shop lacks are offered, in order", () => {
    assert.equal(missingStandardPages([]).length, STANDARD_PAGES.length);
    assert.deepEqual(
        missingStandardPages(["about-us", "refund-policy", "faq"]).map((p) => p.slug),
        ["contact-us", "privacy-policy", "terms-and-conditions", "shipping-policy"],
    );
    assert.deepEqual(missingStandardPages(STANDARD_PAGES.map((p) => p.slug)), []);
});

test("a starter page states no fact about the shop", () => {
    for (const p of STANDARD_PAGES) {
        assert.ok(p.title && p.body, p.slug);
        // A number would be a promise: days to return, an hour to answer, a price.
        assert.doesNotMatch(p.body, /\d/, p.slug);
        assert.match(p.body, /^<p>Replace this with /, p.slug);
    }
});
