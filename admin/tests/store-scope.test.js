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

// openStore(active, linked, remembered, recordFor): recordFor is the store the
// signed-in record was issued for, null when there is no record.
const open = async (...args) => {
    const { openStore } = await import("../src/lib/store-scope.js");
    const got = openStore(mine, ...args);
    return got && { id: got.store.id, enter: got.enter };
};

test("a link naming one of the person's stores opens it, by id or by slug, and enters it", async () => {
    assert.deepEqual(await open("store_B", "store_A", "store_A"), { id: "store_B", enter: true });
    assert.deepEqual(await open("corner-shop-954", "store_A", "store_A"), { id: "store_B", enter: true });
    // The engine reads a slug lower-cased, and an id whatever its case.
    assert.deepEqual(await open("Corner-Shop-954", "store_A", "store_A"), { id: "store_B", enter: true });
    assert.deepEqual(await open("STORE_B", "store_A", "store_A"), { id: "store_B", enter: true });
});

test("a link to the store the tab is already in enters nothing", async () => {
    assert.deepEqual(await open("store_A", "store_A", "store_A"), { id: "store_A", enter: false });
    assert.deepEqual(await open("corner-shop", "store_A", "store_A"), { id: "store_A", enter: false });
});

test("a link naming a store that is not theirs is passed over, not refused", async () => {
    assert.deepEqual(await open("store_Z", "store_A", "store_A"), { id: "store_A", enter: false });
    assert.equal(await open("store_Z", "", "store_A"), null);
});

test("with no link, the remembered store, if it is still theirs", async () => {
    assert.deepEqual(await open("", "store_A", "store_A"), { id: "store_A", enter: false });
    assert.deepEqual(await open("", "corner-shop", "store_A"), { id: "store_A", enter: false });
    assert.equal(await open("", "store_Z", "store_A"), null);
    const { openStore } = await import("../src/lib/store-scope.js");
    assert.equal(openStore([], "store_A", "store_A", "store_A"), null);
});

test("a record another store issued is replaced before anything draws", async () => {
    // Another tab switched to B as staff, and this tab, in A, is reloaded: the
    // record it finds is B's.
    assert.deepEqual(await open("", "store_A", "store_B"), { id: "store_A", enter: true });
    // A record from before records said which store they were for.
    assert.deepEqual(await open("", "store_A", ""), { id: "store_A", enter: true });
    // A static admin token has no record to be wrong.
    assert.deepEqual(await open("store_B", "store_A", null), { id: "store_B", enter: false });
});

test("the store a link names, also through the sign-in page it was sent to", async () => {
    const { storeNamedBy } = await import("../src/lib/store-scope.js");
    const at = (href) => storeNamedBy(new URL(href, "http://panel.test"));
    assert.equal(at("/dash/themes/footer?store=store_B"), "store_B");
    assert.equal(at("/?next=" + encodeURIComponent("/dash/themes/footer?store=store_B")), "store_B");
    assert.equal(at("/select-store?next=%2Fdash"), "");
    assert.equal(at("/dash/themes/footer"), "");
    assert.equal(at("/?next=" + encodeURIComponent("http://[")), "");
});

test("forgetting the store forgets it in this tab and for new ones", async () => {
    const { forgetStore } = await import("../src/lib/store-scope.js");
    setStore("store_C");
    forgetStore();
    assert.equal(getStore(), "");
    assert.deepEqual(storeHeaders(), {});
});
