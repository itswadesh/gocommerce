/*
 * The box on the sign-up, sign-in and reset screens takes an email or a phone.
 * An Indian number typed as ten digits has to reach the engine with its country
 * code, and an email, or a number from elsewhere, must not be rewritten.
 */
import { test } from "node:test";
import assert from "node:assert/strict";

import { toIdentity } from "../src/lib/identity.js";

test("an Indian mobile gets its country code however it was typed", () => {
    for (const typed of ["9876543210", "98765 43210", "09876543210", "919876543210", "+91 98765-43210"]) {
        assert.equal(toIdentity(typed), "+919876543210", typed);
    }
});

test("a number with its own country code keeps it", () => {
    assert.equal(toIdentity("+1 650 555 1234"), "+16505551234");
});

test("an email and anything unclear pass through", () => {
    assert.equal(toIdentity(" Asha@Example.com "), "Asha@Example.com");
    assert.equal(toIdentity("12345"), "12345");
    assert.equal(toIdentity(""), "");
});
