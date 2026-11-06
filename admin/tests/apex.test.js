import assert from "node:assert/strict";
import test from "node:test";

import { apexDomain } from "../src/lib/apex.js";

test("a host groups under the name someone registered", () => {
    for (const [host, want] of [
        ["shopnx.in", "shopnx.in"],
        ["acme.shopnx.in", "shopnx.in"],
        ["a.b.shopnx.in", "shopnx.in"],
        ["WWW.Example.COM", "example.com"],
        ["shop.example.co.in", "example.co.in"],
        ["example.co.in", "example.co.in"],
        ["shop.example.co.uk", "example.co.uk"],
        // co is only a second level under a country code.
        ["co.example.com", "example.com"],
        ["localhost", "localhost"],
        ["", ""],
    ]) {
        assert.equal(apexDomain(host), want, host);
    }
});
