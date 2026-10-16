import { test } from "node:test";
import assert from "node:assert/strict";
import { COUNTRIES, countryOptions } from "../src/lib/countries.js";

test("a saved code on the list keeps the list as it is", () => {
    assert.equal(countryOptions("IN"), COUNTRIES);
    assert.equal(countryOptions(""), COUNTRIES);
    assert.equal(countryOptions(undefined), COUNTRIES);
});

test("a saved value not on the list stays a choice, first, so the select does not blank it", () => {
    const opts = countryOptions("India");
    assert.deepEqual(opts[0], { value: "India", label: "India" });
    assert.equal(opts.length, COUNTRIES.length + 1);
});
