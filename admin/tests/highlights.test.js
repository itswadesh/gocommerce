/*
 * The product editor's Highlights card (D134): up to eight short lines and a
 * free gift note, held to the limits the engine enforces.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import {
    MAX_BADGE,
    MAX_HIGHLIGHTS,
    badgeOf,
    freeGiftOf,
    lineLength,
    highlightLines,
    highlightRows,
    lineProblem,
    removedRow,
    typedRows,
} from "../src/lib/highlights.js";

test("saved highlights become rows with one blank to type into", () => {
    assert.deepEqual(highlightRows({ highlights: ["Shockproof", "Slim"] }), ["Shockproof", "Slim", ""]);
    assert.deepEqual(highlightRows({}), [""]);
    assert.deepEqual(highlightRows(null), [""]);
    const full = Array.from({ length: MAX_HIGHLIGHTS }, (_, i) => `Line ${i}`);
    assert.equal(highlightRows({ highlights: full }).length, MAX_HIGHLIGHTS, "no blank row past the limit");
});

test("what is saved is trimmed and leaves the blank rows out", () => {
    assert.deepEqual(highlightLines(["  Shockproof ", "", "  ", "Slim"]), ["Shockproof", "Slim"]);
});

test("typing in the last row opens another, up to the limit", () => {
    assert.deepEqual(typedRows(["A", ""], 1, "B"), ["A", "B", ""]);
    assert.deepEqual(typedRows(["A", ""], 0, "AA"), ["AA", ""]);
    const seven = Array.from({ length: 7 }, (_, i) => `L${i}`);
    assert.equal(typedRows([...seven, ""], 7, "L7").length, MAX_HIGHLIGHTS);
});

test("emptying a row again leaves one blank row, so the form is not left looking changed", () => {
    const typed = typedRows(["A", ""], 1, "B");
    assert.deepEqual(typedRows(typed, 1, ""), ["A", ""]);
});

test("removing a row keeps a blank one to type into", () => {
    assert.deepEqual(removedRow(["A", ""], 0), [""]);
    assert.deepEqual(removedRow(["A", "B", ""], 1), ["A", ""]);
});

test("a line over 80 characters or on two lines is refused before saving", () => {
    assert.equal(lineProblem("Shockproof corners"), "");
    assert.match(lineProblem("x".repeat(81)), /81 of 80/);
    assert.equal(lineProblem("one\ntwo"), "One line only.");
});

test("the free gift note reads as text or not at all", () => {
    assert.equal(freeGiftOf({ free_gift: "A screen guard" }), "A screen guard");
    assert.equal(freeGiftOf({ free_gift: 3 }), "");
    assert.equal(freeGiftOf(undefined), "");
});

test("a badge is held to 24 characters, counted as the engine counts them", () => {
    assert.equal(MAX_BADGE, 24);
    assert.equal(badgeOf({ badge: "Oversized fit" }), "Oversized fit");
    assert.equal(badgeOf({ badge: ["New"] }), "");
    assert.equal(badgeOf(null), "");
    assert.equal(lineLength("  Hand block print "), 16);
    assert.equal(lineLength("क".repeat(24)), 24, "a Devanagari letter is one character");
    assert.equal(lineProblem("क".repeat(24), MAX_BADGE), "");
    assert.equal(lineProblem("Handwoven Banarasi silk!!", MAX_BADGE), "25 of 24 characters: shorten it.");
    assert.equal(lineProblem("New\tIn", MAX_BADGE), "One line only.");
    assert.equal(lineProblem("x".repeat(25)), "", "the 80-character limit still holds for other lines");
});
