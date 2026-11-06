import { test } from "node:test";
import assert from "node:assert/strict";
import { COUNTRIES, countryOptions, currencyOf, timezoneOf, timezoneOptions, zoneOffset } from "../src/lib/countries.js";

test("a zone's offset reads the same way on either side of UTC", () => {
    const winter = new Date("2026-01-15T12:00:00Z");
    assert.equal(zoneOffset("Asia/Kolkata", winter), "+5:30");
    assert.equal(zoneOffset("Asia/Tokyo", winter), "+9:00");
    assert.equal(zoneOffset("America/New_York", winter), "-5:00");
    assert.equal(zoneOffset("America/New_York", new Date("2026-07-15T12:00:00Z")), "-4:00");
    assert.equal(zoneOffset("UTC", winter), "+0:00");
});

test("a country says what zone to open in, and every inhabited one has an answer", () => {
    assert.match(timezoneOf("IN"), /^Asia\/(Kolkata|Calcutta)$/);
    assert.equal(timezoneOf("GB"), "Europe/London");
    assert.equal(timezoneOf(""), "");
    const missing = COUNTRIES.filter((c) => !["AQ", "BV", "HM"].includes(c.value) && !timezoneOf(c.value)).map((c) => c.value);
    assert.deepEqual(missing, []);
});

test("a country with several zones opens on the browser's own, or its main one", () => {
    const real = Intl.DateTimeFormat;
    const on = (timeZone) => {
        globalThis.Intl.DateTimeFormat = function (locale, options) {
            return new real(locale, options?.timeZone ? options : { timeZone });
        };
    };
    try {
        on("America/Los_Angeles");
        assert.equal(timezoneOf("US"), "America/Los_Angeles");
        on("Asia/Tokyo");
        assert.equal(timezoneOf("US"), "America/New_York");
    } finally {
        globalThis.Intl.DateTimeFormat = real;
    }
});

test("a saved zone the browser does not list stays a choice", () => {
    assert.deepEqual(timezoneOptions("Mars/Olympus")[0], { value: "Mars/Olympus", label: "Mars/Olympus" });
    assert.ok(timezoneOptions("").length > 100);
});

test("India's zone can be found by the country and by the city", () => {
    const find = (word) => timezoneOptions("").filter((z) => z.label.toLowerCase().includes(word));
    assert.ok(find("india").some((z) => /^Asia\/(Kolkata|Calcutta)$/.test(z.value)));
    assert.ok(find("kolkata").some((z) => /^Asia\/(Kolkata|Calcutta)$/.test(z.value)));
});

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
