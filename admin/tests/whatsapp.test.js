/*
 * The order screen's WhatsApp button reads a customer's phone the way core
 * reads a shop's (whatsappDigits in core/whatsapp_order.go): the same cases.
 */
import { test } from "node:test";
import assert from "node:assert/strict";

import { whatsappDigits, whatsappLink } from "../src/lib/whatsapp.js";

test("a number reads the way core reads it", () => {
    for (const [phone, country, want] of [
        ["+91 98765 43210", "", "919876543210"],
        ["919876543210", "IN", "919876543210"],
        ["98765 43210", "IN", "919876543210"],
        ["098765-43210", "in", "919876543210"],
        ["0044 20 7946 0958", "GB", "442079460958"],
        ["+44 (20) 7946 0958", "IN", "442079460958"],
        ["12345", "IN", ""],
        ["", "IN", ""],
        [null, undefined, ""],
    ]) {
        assert.equal(whatsappDigits(phone, country), want, `${phone} in ${country}`);
    }
});

test("the link carries the text encoded, spaces as %20", () => {
    assert.equal(
        whatsappLink("98765 43210", "IN", "About your order GC-000123: "),
        "https://wa.me/919876543210?text=About%20your%20order%20GC-000123%3A%20",
    );
    assert.equal(whatsappLink("98765 43210", "IN"), "https://wa.me/919876543210");
    assert.equal(whatsappLink("12", "IN", "hi"), "");
});
