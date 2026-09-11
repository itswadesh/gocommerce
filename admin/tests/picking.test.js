/*
 * The picking sheet's arithmetic. A warehouse reads these totals and acts on
 * them without checking, so a wrong one is units that never go in the box.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import { pickLines, countUnits } from "../src/lib/picking.js";

const order = (number, lines) => ({ number, line_items: lines });

test("the same SKU across several orders is one line and one total", () => {
    const rows = pickLines([
        order("GC-1", [{ sku: "MUG-1", title: "Mug", quantity: 2 }]),
        order("GC-2", [{ sku: "MUG-1", title: "Mug", quantity: 3 }]),
    ]);
    assert.equal(rows.length, 1);
    assert.equal(rows[0].quantity, 5);
    // Which parcels want it, so a picker who finds four knows which one is short.
    assert.deepEqual(rows[0].numbers, ["GC-1", "GC-2"]);
});

test("the same SKU on two shelves stays two lines", () => {
    // Merging them would produce a total nobody can act on: you cannot pick
    // nine from a shelf holding four.
    const rows = pickLines([
        order("GC-1", [{ sku: "MUG-1", title: "Mug", quantity: 4, location_id: 1 }]),
        order("GC-2", [{ sku: "MUG-1", title: "Mug", quantity: 5, location_id: 2 }]),
    ]);
    assert.equal(rows.length, 2);
    assert.deepEqual(
        rows.map((r) => [r.locationID, r.quantity]),
        [
            [1, 4],
            [2, 5],
        ],
    );
});

test("a line with no location is its own row, not folded into a located one", () => {
    const rows = pickLines([
        order("GC-1", [{ sku: "MUG-1", title: "Mug", quantity: 1 }]),
        order("GC-2", [{ sku: "MUG-1", title: "Mug", quantity: 1, location_id: 1 }]),
    ]);
    assert.equal(rows.length, 2);
    assert.equal(
        rows.reduce((n, r) => n + r.quantity, 0),
        2,
    );
});

test("the walk is ordered by shelf, then by SKU", () => {
    const rows = pickLines([
        order("GC-1", [
            { sku: "TEE-1", title: "Tee", quantity: 1, location_id: 2 },
            { sku: "MUG-1", title: "Mug", quantity: 1, location_id: 2 },
            { sku: "CAP-1", title: "Cap", quantity: 1, location_id: 1 },
        ]),
    ]);
    assert.deepEqual(
        rows.map((r) => r.sku),
        ["CAP-1", "MUG-1", "TEE-1"],
    );
    assert.deepEqual(
        rows.map((r) => r.locationID),
        [1, 2, 2],
    );
});

test("one order buying the same variant twice is named once and summed", () => {
    // An order edit can leave two lines for one variant; it is still one parcel.
    const rows = pickLines([
        order("GC-1", [
            { sku: "MUG-1", title: "Mug", quantity: 2 },
            { sku: "MUG-1", title: "Mug", quantity: 1 },
        ]),
    ]);
    assert.equal(rows.length, 1);
    assert.equal(rows[0].quantity, 3);
    assert.deepEqual(rows[0].numbers, ["GC-1"]);
});

test("an empty run is an empty sheet, not a crash", () => {
    assert.deepEqual(pickLines([]), []);
    assert.deepEqual(pickLines(undefined), []);
    assert.deepEqual(pickLines([order("GC-1", undefined)]), []);
    assert.equal(countUnits(undefined), 0);
});

test("the unit count is every unit in the run", () => {
    assert.equal(
        countUnits([
            order("GC-1", [{ sku: "A", quantity: 2 }, { sku: "B", quantity: 3 }]),
            order("GC-2", [{ sku: "A", quantity: 4 }]),
        ]),
        9,
    );
});
