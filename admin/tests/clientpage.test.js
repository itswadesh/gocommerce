/*
 * Client-side paging, with no browser and no engine.
 *
 * What is pinned here is the arithmetic the footer shows. `Showing X–Y of N`
 * is drawn from this meta, so an off-by-one is a sentence that lies about a
 * table the operator is looking at, and a page past the end has to land
 * somewhere real rather than on an empty table under a full count.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import { pageSlice } from "../src/lib/clientpage.js";

const rows = (n) => Array.from({ length: n }, (_, i) => ({ id: i + 1 }));

test("the meta is the engine's own shape, so the Pager cannot tell the difference", () => {
    const { rows: page, meta } = pageSlice(rows(120), { page: 2, limit: 50 });
    assert.deepEqual(meta, { total: 120, limit: 50, offset: 50, page: 2, total_pages: 3 });
    assert.equal(page.length, 50);
    assert.equal(page[0].id, 51);
    assert.equal(page.at(-1).id, 100);
});

test("the last page is the remainder, not a padded one", () => {
    const { rows: page, meta } = pageSlice(rows(120), { page: 3, limit: 50 });
    assert.equal(page.length, 20);
    assert.equal(meta.offset, 100);
    // Showing 101–120 of 120.
    assert.equal(meta.offset + page.length, meta.total);
});

test("a page past the end lands on the last page that exists", () => {
    // A bookmark from before rows were deleted, or a filter that has just cut
    // the list down. An empty table under "120 rates" is the alternative.
    const { rows: page, meta } = pageSlice(rows(12), { page: 9, limit: 50 });
    assert.equal(meta.page, 1);
    assert.equal(meta.total_pages, 1);
    assert.equal(page.length, 12);
});

test("an empty list is one empty page, not zero pages of something", () => {
    const { rows: page, meta } = pageSlice([], { page: 1, limit: 25 });
    assert.deepEqual(page, []);
    assert.deepEqual(meta, { total: 0, limit: 25, offset: 0, page: 1, total_pages: 0 });
});

test("nonsense from a typed URL degrades to the first page at a sane size", () => {
    const { meta } = pageSlice(rows(10), { page: 0, limit: 0 });
    assert.equal(meta.page, 1);
    assert.equal(meta.limit, 50);

    const missing = pageSlice(undefined, {});
    assert.deepEqual(missing.rows, []);
    assert.equal(missing.meta.total, 0);
});
