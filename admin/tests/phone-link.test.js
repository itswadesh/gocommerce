import { test } from "node:test";
import assert from "node:assert/strict";
import { phoneFromFragment, readablePhone, takePhoneFromFragment } from "../src/lib/phone-link.js";

const at = (hash) => {
    const replaced = [];
    return {
        loc: { hash, pathname: "/admin/auth/signup", search: "" },
        hist: { state: null, replaceState: (_s, _t, url) => replaced.push(url) },
        replaced,
    };
};

test("the plus survives, where URLSearchParams would read it as a space", () => {
    assert.equal(phoneFromFragment("#phone=%2B919876543210").phone, "+919876543210");
    assert.equal(phoneFromFragment("#phone=+14155552671").phone, "+14155552671");
});

test("what is not a phone number fills nothing, and is still cleared", () => {
    for (const hash of ["#phone=hello", "#phone=%ZZ", "#phone=12345", "#phone="]) {
        const { loc, hist, replaced } = at(hash);
        assert.equal(takePhoneFromFragment(loc, hist), "", hash);
        assert.deepEqual(replaced, ["/admin/auth/signup"], hash);
    }
});

test("the number leaves the address bar and other fragments stay", () => {
    const one = at("#phone=%2B919876543210");
    assert.equal(takePhoneFromFragment(one.loc, one.hist), "+919876543210");
    assert.deepEqual(one.replaced, ["/admin/auth/signup"]);

    const two = at("#phone=%2B919876543210&email=a%40b.in");
    takePhoneFromFragment(two.loc, two.hist);
    assert.deepEqual(two.replaced, ["/admin/auth/signup#email=a%40b.in"]);
});

test("an address with no number is left alone", () => {
    for (const hash of ["", "#email=a%40b.in"]) {
        const { loc, hist, replaced } = at(hash);
        assert.equal(takePhoneFromFragment(loc, hist), "");
        assert.deepEqual(replaced, []);
    }
});

test("an Indian number reads as people say it", () => {
    assert.equal(readablePhone("+919876543210"), "+91 98765 43210");
    assert.equal(readablePhone("+14155552671"), "+14155552671");
    assert.equal(readablePhone(""), "");
});
