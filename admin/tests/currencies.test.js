import { test } from "node:test";
import assert from "node:assert/strict";
import { CURRENCIES, currencyOptions } from "../src/lib/currencies.js";

test("the list is ISO codes with their names, rupee and dollar among them", () => {
    assert.ok(CURRENCIES.length > 100);
    const inr = CURRENCIES.find((c) => c.value === "INR");
    assert.ok(inr && /rupee/i.test(inr.label) && inr.label.includes("INR"), JSON.stringify(inr));
    assert.ok(CURRENCIES.some((c) => c.value === "USD"));
    assert.ok(CURRENCIES.every((c) => /^[A-Z]{3}$/.test(c.value)));
});

test("a saved code not on the list stays a choice", () => {
    assert.equal(currencyOptions("INR"), CURRENCIES);
    assert.equal(currencyOptions(""), CURRENCIES);
    assert.deepEqual(currencyOptions("XTS")[0], { value: "XTS", label: "XTS" });
});
