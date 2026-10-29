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

test("the theme editor's rights read as the module words them", async () => {
    // The theme editor names them when a role lacks one, before the Roles
    // screen has fetched the server's words — it used to say "layouts.read".
    const { readFileSync } = await import("node:fs");
    const go = readFileSync(new URL("../../ext/layouts/layouts.go", import.meta.url), "utf8");
    for (const [name, right] of [
        ["rightLayoutsRead", "layouts.read"],
        ["rightLayoutsWrite", "layouts.write"],
    ]) {
        const spec = go.match(new RegExp(`Right:\\s+${name},\\s+Label:\\s+"([^"]+)",\\s+Scope:\\s+"([^"]+)"`));
        assert.ok(spec, `${name}'s RegisterRight was not found in ext/layouts/layouts.go`);
        assert.equal(rightLabel(right), spec[1]);
        assert.equal(rightScope(right), spec[2]);
    }
});

test("an unknown right falls back to its identifier", () => {
    // Never to an empty string: a checkbox with no question attached to it is
    // worse than a dotted name.
    assert.equal(rightLabel("something.new"), "something.new");
    assert.equal(rightScope("something.new"), "something.new");
});

test("the account screen sums a list of rights up in one line", async () => {
    const { rightsSummary, RIGHT_ORDER } = await import("../src/lib/rights.js");
    assert.equal(rightsSummary([]), "Nothing yet");
    assert.equal(rightsSummary(RIGHT_ORDER), "Everything in the panel");
    assert.equal(rightsSummary(["orders.read", "orders.write", "reports.read"]), "Change orders; see reports");
    assert.equal(rightsSummary(["catalog.read", "apikeys.read"]), "See catalog, API keys");
});
