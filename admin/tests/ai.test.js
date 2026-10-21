/*
 * The AI screens' arithmetic and words. A run's cost arrives in millionths of
 * a minor unit and is usually a fraction of a cent, so the formatter is the
 * difference between a column of real figures and a column of $0.00.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import { formatMicro, summary, entries, canRetry, canCancel, movedKeys } from "../src/lib/ai.js";

test("a fraction of a cent keeps two significant digits", () => {
    // 326,700 micro-minor is 0.3267 of a cent.
    assert.equal(formatMicro(326_700, "USD"), "$0.0033");
    // 1.37 cents beside it reads $0.014, not $0.01: one precision per column.
    assert.equal(formatMicro(13_700_000, "USD"), "$0.14");
    assert.equal(formatMicro(1_370_000, "USD"), "$0.014");
});

test("a whole amount reads as a price", () => {
    assert.equal(formatMicro(0, "USD"), "$0.00");
    assert.equal(formatMicro(5_000 * 1e6, "USD"), "$50.00");
    assert.equal(formatMicro(123_456 * 1e6, "USD"), "$1,234.56");
});

test("a currency with no minor digits is not divided by a hundred", () => {
    // 1,500 yen is 1,500 minor units.
    assert.equal(formatMicro(1_500 * 1e6, "JPY"), "¥1,500");
});

test("a missing amount is a dash, not a zero", () => {
    assert.equal(formatMicro(null, "USD"), "—");
    assert.equal(formatMicro(undefined, "USD"), "—");
});

test("a preview with from and to reads as a change", () => {
    assert.equal(summary({ product: 2, from: "Enamel mug", to: "Steel mug" }), "Enamel mug → Steel mug");
    assert.equal(summary({ amount_minor: 2500, order: "GC-1" }), "amount minor: 2500 · order: GC-1");
    assert.deepEqual(entries({ items: [1, 2] }), [["items", "[1,2]"]]);
    assert.deepEqual(entries(null), []);
});

test("retry and cancel are offered only where the engine allows them", () => {
    assert.deepEqual(["queued", "running", "done", "failed", "dead", "cancelled"].map(canRetry), [false, false, false, true, true, true]);
    assert.deepEqual(["queued", "running", "done", "failed", "dead", "cancelled"].map(canCancel), [true, true, false, true, true, false]);
});

test("the lines that moved between what was shown and what is there now", () => {
    const shown = { product: 2, from: "Enamel mug", to: "Steel mug" };
    const now = { product: 2, from: "Renamed meanwhile", to: "Steel mug" };
    assert.deepEqual([...movedKeys(shown, now)], ["from"]);
    assert.deepEqual([...movedKeys(shown, shown)], []);
    // A line that appeared or went is a line that moved.
    assert.deepEqual([...movedKeys({ a: 1 }, { a: 1, b: 2 })].sort(), ["b"]);
    assert.deepEqual([...movedKeys(null, { a: 1 })], []);
});
