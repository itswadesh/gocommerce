/*
 * The one fold two screens have to agree on: the product editor suggests these
 * names while authoring and the product list filters on the same column, so a
 * spelling one of them invents is a filter the other can never match.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import { distinct } from "../src/lib/catalog.js";

test("the first spelling wins, and the fold is case-insensitive", () => {
    const rows = [
        { vendor: "Acme" },
        { vendor: " acme " },
        { vendor: "" },
        { vendor: null },
        { vendor: "Globex" },
    ];
    // The catalogue's own spelling, not this function's — an operator who wrote
    // "Acme" should be offered "Acme".
    assert.deepEqual(distinct(rows, (p) => [p.vendor]), ["Acme", "Globex"]);
});

test("a list column is read whole", () => {
    const rows = [{ tags: ["sale", "summer"] }, { tags: ["SALE"] }, {}];
    assert.deepEqual(distinct(rows, (p) => p.tags ?? []), ["sale", "summer"]);
});

test("no rows is an empty vocabulary, not a crash", () => {
    assert.deepEqual(distinct(null, (p) => [p.vendor]), []);
    assert.deepEqual(distinct([], (p) => [p.vendor]), []);
});
