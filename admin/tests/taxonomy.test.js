/*
 * The taxonomy browser's rows (D146): branches open and close by depth, and a
 * switch redraws the ancestors an activation brought and the branch a
 * deactivation took.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import { within, openBranch, closeBranch, applyActivation } from "../src/lib/taxonomy.js";

const row = (id, depth, active = false) => ({ id, name: id, depth, active, category_id: null, title: "" });

test("within is the node or its descendants, not a sibling sharing a prefix", () => {
    assert.ok(within("aa-1", "aa-1"));
    assert.ok(within("aa-1-2", "aa-1"));
    assert.ok(!within("aa-10", "aa-1"));
    assert.ok(!within("aa", "aa-1"));
});

test("a branch opens under its row and closes however deep it was opened", () => {
    let rows = [row("aa", 0), row("ap", 0)];
    rows = openBranch(rows, 0, [row("aa-1", 1), row("aa-2", 1)]);
    rows = openBranch(rows, 1, [row("aa-1-1", 2)]);
    assert.deepEqual(rows.map((r) => r.id), ["aa", "aa-1", "aa-1-1", "aa-2", "ap"]);
    assert.deepEqual(closeBranch(rows, 0).map((r) => r.id), ["aa", "ap"]);
});

test("activating draws the node and its ancestors on, and nothing else", () => {
    const rows = [row("aa", 0), row("aa-1", 1), row("aa-1-1", 2), row("aa-10", 1), row("ap", 0)];
    const out = applyActivation(rows, { id: "aa-1-1" }, true, { id: 7, title: "Activewear" });
    assert.deepEqual(out.map((r) => r.active), [true, true, true, false, false]);
    assert.equal(out[2].category_id, 7);
    assert.equal(out[2].title, "Activewear");
});

test("deactivating draws the whole branch off and leaves the ancestors on", () => {
    const rows = [row("aa", 0, true), row("aa-1", 1, true), row("aa-1-1", 2, true), row("aa-2", 1, true)];
    const out = applyActivation(rows, { id: "aa-1" }, false);
    assert.deepEqual(out.map((r) => r.active), [true, false, false, true]);
    assert.equal(out[2].category_id, null);
});
