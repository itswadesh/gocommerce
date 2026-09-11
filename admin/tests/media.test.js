/*
 * The library's filter row, translated into what the engine takes — with no
 * browser and no store.
 *
 * media.js imports nothing, so a plain ESM import works. What is pinned here is
 * the translation the two library surfaces share: a named size bucket becoming
 * a pair of byte bounds, one sort control becoming a sort and an order, and the
 * empty state of both meaning "send neither", which is what keeps the listing's
 * newest-first default reachable at all.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import {
    SIZE_BUCKETS,
    mediaLabel,
    fileType,
    fileSize,
    dimensions,
    mediaQuery,
    mediaFiltered,
} from "../src/lib/media.js";

test("no filters send nothing but the paging", () => {
    // Every value is empty rather than absent, because api.js's query() drops
    // empties on the way to the wire. What must not happen is a stray
    // `sort=` or `min_bytes=0` narrowing a listing nobody asked to narrow.
    assert.deepEqual(mediaQuery({}, { page: 1, limit: 24 }), {
        q: "",
        kind: "",
        usage: "",
        sort: "",
        order: "",
        product_id: "",
        min_bytes: "",
        max_bytes: "",
        page: 1,
        limit: 24,
    });
});

test("a size bucket becomes the byte bounds the engine takes", () => {
    const small = mediaQuery({ size: "small" });
    // 0 is not a lower bound worth sending — MediaQuery treats it as unset
    // either way — and 100 KB is the real question being asked.
    assert.equal(small.min_bytes, "");
    assert.equal(small.max_bytes, SIZE_BUCKETS.small.max);

    // "Over 5 MB" is the case with no upper bound at all, which is why the
    // bucket's max is 0 rather than a sentinel.
    const huge = mediaQuery({ size: "huge" });
    assert.equal(huge.min_bytes, SIZE_BUCKETS.huge.min);
    assert.equal(huge.max_bytes, "");

    // A bucket key from a hand-typed URL narrows nothing rather than throwing.
    assert.equal(mediaQuery({ size: "enormous" }).min_bytes, "");
});

test("one sort control becomes a sort and an order", () => {
    assert.equal(mediaQuery({ sort: "filename:asc" }).sort, "filename");
    assert.equal(mediaQuery({ sort: "filename:asc" }).order, "asc");

    // A lone order would be meaningless to the engine and is never sent.
    assert.equal(mediaQuery({ sort: "" }).sort, "");
    assert.equal(mediaQuery({ sort: "" }).order, "");

    // A field with no direction is still a field; the engine picks its own.
    assert.equal(mediaQuery({ sort: "size_bytes" }).sort, "size_bytes");
    assert.equal(mediaQuery({ sort: "size_bytes" }).order, "");
});

test("search is trimmed, because a space is not a query", () => {
    assert.equal(mediaQuery({ q: "  shirt " }).q, "shirt");
    assert.equal(mediaQuery({ q: "   " }).q, "");
});

test("an empty result knows whether anything was filtering it", () => {
    assert.equal(mediaFiltered({}), false);
    assert.equal(mediaFiltered({ q: "  " }), false);
    assert.equal(mediaFiltered({ q: "hat" }), true);
    assert.equal(mediaFiltered({ usage: "unused" }), true);
    assert.equal(mediaFiltered({ scope: "product" }), true);
});

test("a file is named by what somebody actually wrote", () => {
    assert.equal(mediaLabel({ id: 3, alt: "A red hat", filename: "hat.png" }), "A red hat");
    assert.equal(mediaLabel({ id: 3, alt: "", filename: "hat.png" }), "hat.png");
    // Linked media has neither, and an empty label reads as a broken row.
    assert.equal(mediaLabel({ id: 3, alt: "", filename: "" }), "media 3");
});

test("the type line is the extension, and the kind when there is none", () => {
    assert.equal(fileType({ kind: "image", filename: "hat.PNG" }), "PNG");
    // A URL with a query string still has an extension.
    assert.equal(fileType({ kind: "image", url: "https://x/y/hat.jpg?v=2" }), "JPG");
    // A dot in a directory name is not an extension.
    assert.equal(fileType({ kind: "image", url: "https://x/v1.2/hat" }), "IMAGE");
    assert.equal(fileType({ kind: "model", url: "https://x/chair" }), "3D model");
});

test("bytes are rounded the way a person reads them, and zero is unknown", () => {
    // Zero means linked media, whose size the engine never saw.
    assert.equal(fileSize(0), "");
    assert.equal(fileSize(undefined), "");
    assert.equal(fileSize(900), "900 B");
    assert.equal(fileSize(2048), "2 KB");
    assert.equal(fileSize(3 * 1024 * 1024), "3.0 MB");
});

test("dimensions need both, since half a pair is not a size", () => {
    assert.equal(dimensions({ width: 1200, height: 800 }), "1200 × 800");
    assert.equal(dimensions({ width: 1200 }), "");
    assert.equal(dimensions({}), "");
    assert.equal(dimensions(null), "");
});
