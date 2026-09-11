/*
 * The header cycle and the URL reading, with no browser and no engine.
 *
 * listsort.js imports nothing, so a plain ESM import works. What is pinned here
 * is the shape the panel's correctness depends on: three states rather than
 * two, a stale key degrading to the default order rather than to a 400, and a
 * lone `order` never reaching the wire.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import { NO_SORT, readSort, sortQuery, cycleSort } from "../src/lib/listsort.js";

const FIELDS = ["title", "price", "created_at"];

test("a key the screen does not offer reads as no sort", () => {
    // The engine answers an unknown sort with a 400. A bookmark from a version
    // that had one more column should degrade to the default order, not to an
    // error page.
    assert.deepEqual(readSort({ sort: "colour", order: "asc" }, FIELDS), NO_SORT);
    assert.deepEqual(readSort({ sort: "", order: "" }, FIELDS), NO_SORT);
    assert.deepEqual(readSort({}, FIELDS), NO_SORT);
    assert.deepEqual(readSort(undefined, FIELDS), NO_SORT);
});

test("a known key reads its direction, and anything but desc is ascending", () => {
    assert.deepEqual(readSort({ sort: "title", order: "asc" }, FIELDS), {
        field: "title",
        desc: false,
    });
    assert.deepEqual(readSort({ sort: "title", order: "desc" }, FIELDS), {
        field: "title",
        desc: true,
    });
    assert.deepEqual(readSort({ sort: "title", order: "sideways" }, FIELDS), {
        field: "title",
        desc: false,
    });
});

test("a hand-typed order with no sort never reaches the wire", () => {
    // `?order=desc` alone is a 400 at the engine, deliberately: the default
    // orderings carry their own direction and there is nothing to flip. Passing
    // the URL's strings through unfiltered would turn a mistyped address into an
    // error page instead of the default listing.
    const sort = readSort({ sort: "", order: "desc" }, FIELDS);
    assert.deepEqual(sortQuery(sort), { sort: "", order: "" });
});

test("sortQuery sends both parameters or neither", () => {
    assert.deepEqual(sortQuery({ field: "price", desc: true }), {
        sort: "price",
        order: "desc",
    });
    assert.deepEqual(sortQuery({ field: "price", desc: false }), {
        sort: "price",
        order: "asc",
    });
    assert.deepEqual(sortQuery(NO_SORT), { sort: "", order: "" });
});

test("a header cycles ascending, descending, then off", () => {
    let sort = NO_SORT;
    let patch = cycleSort(sort, "title");
    assert.deepEqual(patch, { sort: "title", order: "asc" });

    sort = readSort(patch, FIELDS);
    patch = cycleSort(sort, "title");
    assert.deepEqual(patch, { sort: "title", order: "desc" });

    sort = readSort(patch, FIELDS);
    patch = cycleSort(sort, "title");
    // The third state is the engine's own order, which no sort/order pair can
    // express — and it is the order an operator needs back after finding the
    // one row they were looking for.
    assert.deepEqual(patch, { sort: "", order: "" });
});

test("a money or date column starts biggest-first and still cycles to off", () => {
    let patch = cycleSort(NO_SORT, "price", true);
    assert.deepEqual(patch, { sort: "price", order: "desc" });

    patch = cycleSort(readSort(patch, FIELDS), "price", true);
    assert.deepEqual(patch, { sort: "price", order: "asc" });

    patch = cycleSort(readSort(patch, FIELDS), "price", true);
    assert.deepEqual(patch, { sort: "", order: "" });
});

test("clicking a different header starts that one over", () => {
    const onTitle = { field: "title", desc: true };
    assert.deepEqual(cycleSort(onTitle, "price", true), { sort: "price", order: "desc" });
    assert.deepEqual(cycleSort(onTitle, "created_at"), { sort: "created_at", order: "asc" });
});
