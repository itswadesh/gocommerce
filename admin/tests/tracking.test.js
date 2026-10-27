import { test } from "node:test";
import assert from "node:assert/strict";
import { orderTrackingURL } from "../src/lib/tracking.js";

test("the tracking page is on the store's own domain, else its address under the base domain", () => {
    assert.equal(
        orderTrackingURL({ domain: "chillish.in", subdomain: "chillish.shops.test" }, "64"),
        "https://chillish.in/order-tracking?number=64",
    );
    assert.equal(
        orderTrackingURL({ domain: "", subdomain: "chillish.shops.test" }, "GC 64"),
        "https://chillish.shops.test/order-tracking?number=GC+64",
    );
});

test("a store with no address, or an order with no number, has no link", () => {
    assert.equal(orderTrackingURL({ domain: "", subdomain: "" }, "64"), "");
    assert.equal(orderTrackingURL(null, "64"), "");
    assert.equal(orderTrackingURL({ domain: "chillish.in" }, ""), "");
});
