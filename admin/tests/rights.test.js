/*
 * The JS-side twin of core/rights_test.go's panel check.
 *
 * The Go test is the one that proves the tables match the engine; this one runs
 * without a Go toolchain or a database, so a panel-only change is caught before
 * anybody runs the engine suite. It cannot know what the engine's rights are, so
 * it asserts the properties that hold whatever they turn out to be.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import {
    RIGHT_ORDER,
    RIGHT_LABELS,
    RIGHT_SCOPES,
    rightLabel,
    rightScope,
} from "../src/lib/rights.js";

test("the two label tables have the same keys, in RIGHT_ORDER", () => {
    assert.deepEqual(Object.keys(RIGHT_LABELS), RIGHT_ORDER);
    assert.deepEqual(Object.keys(RIGHT_SCOPES), RIGHT_ORDER);
    assert.equal(new Set(RIGHT_ORDER).size, RIGHT_ORDER.length, "a right is listed twice");
});

test("every right is spelled the way the engine spells it", () => {
    // area.verb, lower case. The stale copy this file replaced had
    // `settings.write`, which is well-formed and abolished — that one is the Go
    // test's to catch — but a typo like `orders.Refund` would silently render
    // as a raw identifier forever, and is catchable here.
    for (const right of RIGHT_ORDER) {
        assert.match(right, /^[a-z]+\.[a-z]+$/, `${right} is not an engine right identifier`);
    }
});

test("no label is the bare identifier", () => {
    // The fallback exists so an unknown right still renders something; a table
    // entry that *is* the fallback means somebody added a row and no words.
    for (const right of RIGHT_ORDER) {
        assert.notEqual(rightLabel(right), right, `${right} has no label`);
        assert.notEqual(rightScope(right), right, `${right} has no scope sentence`);
        assert.ok(rightLabel(right).length > 3);
        assert.ok(rightScope(right).length > 3);
    }
});

test("an unknown right falls back to its identifier", () => {
    // Never to an empty string: a checkbox with no question attached to it is
    // worse than a dotted name.
    assert.equal(rightLabel("something.new"), "something.new");
    assert.equal(rightScope("something.new"), "something.new");
});
