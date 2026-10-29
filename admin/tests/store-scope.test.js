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

/*
 * A link names its store (?store=) and a tab remembers one; the link wins when
 * it is one of the person's stores. It used to lose: a tab opened on
 * /dash/themes/footer?store=<their store> worked in whichever store the browser
 * chose last — another store of the same name, as staff — and showed a theme
 * editor with nothing to edit.
 */
const mine = [
    { id: "store_A", slug: "corner-shop" },
    { id: "store_B", slug: "corner-shop-954" },
];

test("a link naming one of the person's stores opens it, by id or by slug", async () => {
    const { pickStore } = await import("../src/lib/store-scope.js");
    assert.equal(pickStore(mine, "store_B", "store_A")?.id, "store_B");
    assert.equal(pickStore(mine, "corner-shop-954", "store_A")?.id, "store_B");
});

test("a link naming a store that is not theirs is passed over, not refused", async () => {
    const { pickStore } = await import("../src/lib/store-scope.js");
    assert.equal(pickStore(mine, "store_Z", "store_A")?.id, "store_A");
    assert.equal(pickStore(mine, "store_Z", ""), null);
});

test("with no link, the remembered store, if it is still theirs", async () => {
    const { pickStore } = await import("../src/lib/store-scope.js");
    assert.equal(pickStore(mine, "", "store_A")?.id, "store_A");
    assert.equal(pickStore(mine, "", "corner-shop")?.id, "store_A");
    assert.equal(pickStore(mine, "", "store_Z"), null);
    assert.equal(pickStore([], "store_A", "store_A"), null);
});

test("the store a link names, also through the sign-in page it was sent to", async () => {
    const { storeNamedBy } = await import("../src/lib/store-scope.js");
    const at = (href) => storeNamedBy(new URL(href, "http://panel.test"));
    assert.equal(at("/dash/themes/footer?store=store_B"), "store_B");
    assert.equal(at("/?next=" + encodeURIComponent("/dash/themes/footer?store=store_B")), "store_B");
    assert.equal(at("/select-store?next=%2Fdash"), "");
    assert.equal(at("/dash/themes/footer"), "");
});

test("forgetting the store forgets it in this tab and for new ones", async () => {
    const { forgetStore } = await import("../src/lib/store-scope.js");
    setStore("store_C");
    forgetStore();
    assert.equal(getStore(), "");
    assert.deepEqual(storeHeaders(), {});
});
