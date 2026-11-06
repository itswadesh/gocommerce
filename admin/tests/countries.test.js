import { test } from "node:test";
import assert from "node:assert/strict";
import { COUNTRIES, countryOptions, currencyOf } from "../src/lib/countries.js";

test("a country says what it spends, and every country on the list has an answer", () => {
    assert.equal(currencyOf("IN"), "INR");
    assert.equal(currencyOf("US"), "USD");
    assert.equal(currencyOf("DE"), "EUR");
    assert.equal(currencyOf(""), "");
    const missing = COUNTRIES.filter((c) => c.value !== "AQ" && !currencyOf(c.value)).map((c) => c.value);
    assert.deepEqual(missing, []);
});

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
