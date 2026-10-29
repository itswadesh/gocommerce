/*
 * The Pages screen offers to add the pages a storefront links to and the
 * shop has not written. Which ones it offers, and what they say, is here.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import { STANDARD_PAGES, missingStandardPages, standardFooter, sameLinks } from "../src/lib/standard-pages.js";

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

test("an old footer becomes the standard one, keeping the store's own links", () => {
    const old = [
        { title: "Search", kind: "url", target: "/search", children: [] },
        { title: "Contact us", kind: "url", target: "/contact", children: [] },
        { title: "Privacy", kind: "url", target: "/pages/privacy", children: [] },
        { title: "Instagram", kind: "url", target: "https://instagram.com/x", children: [] },
    ];
    assert.deepEqual(
        standardFooter(old).map((it) => [it.title, it.target]),
        [
            ["About us", "/about-us"],
            ["Contact us", "/contact-us"],
            ["Privacy", "/privacy-policy"],
            ["Terms and conditions", "/terms-and-conditions"],
            ["Shipping policy", "/shipping-policy"],
            ["Refund and returns policy", "/refund-policy"],
            ["Instagram", "https://instagram.com/x"],
        ],
    );
});

test("a footer that is already standard is left as it is", () => {
    const done = standardFooter([]);
    assert.ok(sameLinks(standardFooter(done), done));
    assert.ok(!sameLinks(standardFooter([{ title: "Search", kind: "url", target: "/search" }]), [{ title: "Search", kind: "url", target: "/search" }]));
});

test("a page item and a link with children are matched only when they should be", () => {
    const items = [
        { title: "Returns", kind: "page", target: "returns", children: [] },
        { title: "Help", kind: "url", target: "/contact", children: [{ title: "FAQ", kind: "page", target: "faq", children: [] }] },
    ];
    const out = standardFooter(items);
    assert.equal(out.find((it) => it.target === "/refund-policy").title, "Returns");
    assert.equal(out.at(-1).title, "Help");
});
