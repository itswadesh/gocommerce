/*
 * Node's own test runner, so the panel gains a test suite and no dependency.
 * format.js imports nothing, so a plain ESM import works under "type": "module".
 *
 * Every assertion here pins a bug that shipped. The locale is always explicit:
 * without it these would assert against whatever CI happens to run in, which is
 * the same class of mistake the code under test was making.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import {
    parseMoney,
    isValidMoney,
    toMinor,
    fromMinor,
    minorDigits,
    orderStatusClass,
    orderStatusLabel,
    paymentLabel,
    paymentStatusClass,
} from "../src/lib/format.js";

test("toMinor refuses a value the old guards accepted", () => {
    // isNaN(parseFloat("12abc")) is false, so every guard in the panel passed
    // it, and the old toMinor turned it into 1200.
    assert.equal(isValidMoney("12abc", "en-US"), false);
    assert.equal(toMinor("12abc", "USD", "en-US"), null);
    assert.equal(toMinor("", "USD", "en-US"), null);
    assert.equal(toMinor("abc", "USD", "en-US"), null);
});

test("a decimal comma is a decimal comma", () => {
    assert.equal(parseMoney("24,99", "de-DE"), 24.99);
    // The one that matters: an en-US panel in front of somebody typing on a
    // German keyboard. parseFloat read this as 24.
    assert.equal(parseMoney("24,99", "en-US"), 24.99);

    // Three digits behind the mark is the genuinely ambiguous case, and there
    // the locale decides.
    assert.equal(parseMoney("1,000", "en-US"), 1000);
    assert.equal(parseMoney("1.000", "de-DE"), 1000);

    // fr-FR groups with a narrow no-break space (U+202F).
    assert.equal(parseMoney("1\u202f234,50", "fr-FR"), 1234.5);
    assert.equal(parseMoney("1\u00a0234,50", "fr-FR"), 1234.5);

    // Mixed marks settle themselves: the rightmost is the decimal one.
    assert.equal(parseMoney("1,234.56", "en-US"), 1234.56);
    assert.equal(parseMoney("1.234,56", "de-DE"), 1234.56);

    // Grouping that is not in threes is a typo, not a number.
    assert.equal(parseMoney("1,2,3", "en-US"), null);
});

test("minor units follow the currency, not the habit", () => {
    // The discounts bug, as an assertion: ¥500 was being spent as ¥50,000.
    assert.equal(toMinor("500", "JPY", "en-US"), 500);
    assert.equal(toMinor("1.234", "KWD", "en-US"), 1234);
    assert.equal(toMinor("24.99", "USD", "en-US"), 2499);
    assert.equal(minorDigits("JPY"), 0);
    assert.equal(minorDigits("KWD"), 3);
    assert.equal(minorDigits("USD"), 2);
});

test("money helpers refuse to guess a currency", () => {
    // The silent two-decimal fallback is what made the JPY error invisible.
    assert.throws(() => toMinor("5"), /currency code is required/);
    assert.throws(() => fromMinor(500), /currency code is required/);
    assert.throws(() => minorDigits(undefined), /currency code is required/);
    assert.throws(() => minorDigits(""), /currency code is required/);
});

test("fromMinor round-trips through toMinor", () => {
    // The display half and the entry half have to agree, or fixing one breaks
    // the other: the discounts screen was self-consistently wrong.
    for (const [currency, minor] of [
        ["USD", 2499],
        ["USD", 123456],
        ["JPY", 500],
        ["JPY", 123456],
        ["KWD", 1234],
        ["KWD", 1000000],
    ]) {
        const shown = fromMinor(minor, currency);
        assert.equal(toMinor(shown, currency, "en-US"), minor, `${currency} ${minor} → ${shown}`);
    }
});

test("a guarded path never serialises null", () => {
    /*
     * The shape of every money-entry site: guard with isValidMoney, and only
     * then call toMinor. If that holds, `price_minor: null` can never reach Go —
     * where encoding/json makes a null into a value field a no-op and into a
     * pointer field an absent one, so the product is created at zero or left
     * silently unchanged.
     */
    const inputs = ["", " ", "abc", "12abc", "-", "1,2,3", "1.2.3", "24.99", "0", "1 234,50"];
    for (const locale of ["en-US", "de-DE", "fr-FR"]) {
        for (const value of inputs) {
            const guarded = isValidMoney(value, locale);
            if (!guarded) continue;
            assert.equal(
                typeof toMinor(value, "USD", locale),
                "number",
                `${JSON.stringify(value)} in ${locale} passed the guard and produced a null`,
            );
        }
    }
});

test("a negative and a zero are read, not refused", () => {
    // A refund line and a free item are both legitimate; only garbage is not.
    assert.equal(parseMoney("0", "en-US"), 0);
    assert.equal(parseMoney("-24.99", "en-US"), -24.99);
    assert.equal(toMinor("-24,99", "USD", "de-DE"), -2499);
});

test("a partly refunded order is paid, and does not read as paid", () => {
    // The engine keeps payment_status at `paid` while the store still holds any
    // of the money, on purpose: the money did arrive. So the chip has to read
    // the refunded figure, or an order with 600 of 2000 sent back looks exactly
    // like one nobody has refunded.
    const order = { payment_status: "paid", refunded: { amount_minor: 600 }, total: { amount_minor: 2000 } };
    assert.deepEqual(paymentLabel(order), { text: "part refunded", cls: "warning" });

    // All of it back is the status, not the number.
    assert.equal(
        paymentLabel({ payment_status: "refunded", refunded: { amount_minor: 2000 }, total: { amount_minor: 2000 } }).text,
        "refunded",
    );
    // Nothing back reads exactly as it always did.
    assert.deepEqual(paymentLabel({ payment_status: "paid", total: { amount_minor: 2000 } }), {
        text: "paid",
        cls: paymentStatusClass("paid"),
    });
    // And an order that has not loaded yet says nothing rather than throwing.
    assert.equal(paymentLabel(null).text, undefined);
});

test("a partly shipped order says so in words", () => {
    // On the wire a status is one lowercase word, because it is also a URL
    // filter. "partial" next to a payment chip reads as a partial payment, so
    // the panel says the thing a person would say — and says nothing different
    // about the five statuses that were already plain.
    assert.equal(orderStatusLabel("partial"), "partly shipped");
    for (const s of ["pending", "confirmed", "shipped", "delivered", "cancelled"]) {
        assert.equal(orderStatusLabel(s), s);
    }
    // Its own colour: partial is the one status meaning the shop still owes
    // the customer something, and confirmed and shipped already share info.
    assert.equal(orderStatusClass("partial"), "warning");
    assert.notEqual(orderStatusClass("partial"), orderStatusClass("shipped"));
});
