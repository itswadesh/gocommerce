import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { hasStoreName, rememberStoreName, storeNameFrom, takeStoreName } from "../src/lib/store-name.js";

const memory = () => {
    const held = new Map();
    return { getItem: (k) => held.get(k) ?? null, setItem: (k, v) => held.set(k, v), removeItem: (k) => held.delete(k) };
};

test("the name comes from the query string, tidied", () => {
    assert.equal(storeNameFrom("?store_name=Mehta+Jewels"), "Mehta Jewels");
    assert.equal(storeNameFrom("?store_name=%20Mehta%20"), "Mehta");
    assert.equal(storeNameFrom("?other=1"), "");
    assert.equal(storeNameFrom("?store_name=" + "x".repeat(200)).length, 60);
});

test("the form gets the name once, so nobody is sent back to it", () => {
    const s = memory();
    rememberStoreName("?store_name=Mehta", s);
    assert.equal(hasStoreName(s), true);
    assert.equal(takeStoreName(s), "Mehta");
    assert.equal(hasStoreName(s), false);
    assert.equal(takeStoreName(s), "");
});

test("a page without the parameter leaves a remembered name alone", () => {
    const s = memory();
    rememberStoreName("?store_name=Mehta", s);
    rememberStoreName("?next=/dash", s);
    assert.equal(takeStoreName(s), "Mehta");
});

test("storage that refuses never breaks signing in", () => {
    const refusing = { getItem() { throw new Error("no"); }, setItem() { throw new Error("no"); }, removeItem() { throw new Error("no"); } };
    rememberStoreName("?store_name=Mehta", refusing);
    assert.equal(hasStoreName(refusing), false);
    assert.equal(takeStoreName(refusing), "");
});

test("the create form starts from the remembered name", () => {
    const page = readFileSync(new URL("../src/routes/select-store/+page.svelte", import.meta.url), "utf8");
    assert.match(page, /let name = \$state\(takeStoreName\(\)\)/);
});
