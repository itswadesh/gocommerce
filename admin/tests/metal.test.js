import { test } from "node:test";
import assert from "node:assert/strict";
import { rateCode, repriceMessage, scaled, unscaled } from "../src/lib/metal.js";

test("scaled reads a typed weight exactly, with no float", () => {
    assert.equal(scaled("12.345", 3), 12345);
    assert.equal(scaled("12", 3), 12000);
    assert.equal(scaled(".5", 3), 500);
    assert.equal(scaled("1,234.5", 3), 1234500);
    // 1.1 * 1000 is 1100.0000000000002 in floats, which is why this is text.
    assert.equal(scaled("1.1", 3), 1100);
    assert.equal(scaled("4.35", 2), 435);
});

test("scaled refuses what it would have to round or guess", () => {
    for (const bad of ["", ".", "abc", "-1", "1.2.3", "1.2345"]) assert.equal(scaled(bad, 3), null, bad);
});

test("unscaled shows a stored value as it was typed", () => {
    assert.equal(unscaled(12345, 3), "12.345");
    assert.equal(unscaled(12000, 3), "12");
    assert.equal(unscaled(500, 3), "0.5");
    assert.equal(unscaled(800, 2), "8");
    assert.equal(unscaled(0, 2), "0");
    for (const text of ["12.345", "0.001", "100"]) assert.equal(unscaled(scaled(text, 3), 3), text);
});

test("rateCode turns a name into a code the API accepts", () => {
    assert.equal(rateCode("Gold 22K"), "gold-22k");
    assert.equal(rateCode("  Silver (925) "), "silver-925");
    assert.equal(rateCode("सोना"), "");
});

test("repriceMessage never hides a design left at its old price", () => {
    assert.equal(repriceMessage({ repriced: 1, failed: [] }), "1 design repriced.");
    assert.equal(repriceMessage({ repriced: 3, failed: [] }), "3 designs repriced.");
    assert.equal(repriceMessage({ repriced: 0, failed: [] }), "Rate saved. No design's price changed.");
    assert.match(repriceMessage({ repriced: 2, failed: [{}] }), /1 could not be changed/);
});
