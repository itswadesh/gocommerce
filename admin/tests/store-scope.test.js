/*
 * The store the panel works in (D83). One engine serves many stores, and a
 * request names its store in a header; the panel keeps the store per tab, and
 * remembers the last one so a new tab opens there.
 */
import { test } from "node:test";
import assert from "node:assert/strict";

function fakeStorage() {
    const m = new Map();
    return {
        getItem: (k) => (m.has(k) ? m.get(k) : null),
        setItem: (k, v) => m.set(k, String(v)),
        removeItem: (k) => m.delete(k),
    };
}

globalThis.sessionStorage = fakeStorage();
globalThis.localStorage = fakeStorage();
const { getStore, setStore, storeHeaders } = await import("../src/lib/store-scope.js");

test("no store yet sends no header", () => {
    assert.equal(getStore(), "");
    assert.deepEqual(storeHeaders(), {});
});

test("a chosen store is sent as x-litekart-store", () => {
    setStore("store_01JA2Z3K4M5N6P7Q8R9S0T1V2W");
    assert.deepEqual(storeHeaders(), { "x-litekart-store": "store_01JA2Z3K4M5N6P7Q8R9S0T1V2W" });
});

test("this tab's store wins over the one another tab chose last", () => {
    setStore("store_A");
    localStorage.setItem("gc_store", "store_B"); // another tab switched
    assert.equal(getStore(), "store_A");
});

test("a new tab opens in the store used last", () => {
    sessionStorage.removeItem("gc_store");
    assert.equal(getStore(), "store_B");
});

test("forgetting the store forgets it in this tab and for new ones", async () => {
    const { forgetStore } = await import("../src/lib/store-scope.js");
    setStore("store_C");
    forgetStore();
    assert.equal(getStore(), "");
    assert.deepEqual(storeHeaders(), {});
});
