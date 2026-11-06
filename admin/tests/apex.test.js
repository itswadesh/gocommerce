import assert from "node:assert/strict";
import test from "node:test";

import { apexDomain, isApexDomain } from "../src/lib/apex.js";

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

test("only a name of its own is an apex domain", () => {
    for (const host of ["example.com", "example.co.in", "www.example.com", "www.example.co.in"]) {
        assert.equal(isApexDomain(host), true, host);
    }
    for (const host of ["acme.shopnx.in", "shop.example.co.in", "www.shop.example.com", "", null]) {
        assert.equal(isApexDomain(host), false, String(host));
    }
});
