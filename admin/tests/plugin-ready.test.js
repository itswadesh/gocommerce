/*
 * A plugin is not offered as switchable-on while a setting it needs is empty:
 * the engine would refuse, and a button that can only fail leaves a new
 * store owner lost. The note says exactly what to fill in.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { missingSettings, activateNote } from "../src/lib/plugin-ready.js";

const sitemap = {
    title: "Sitemap",
    enabled: false,
    fields: [
        { key: "storefront_url", label: "Storefront URL", kind: "url", required: true },
        { key: "product_path", label: "Product path", kind: "text" },
    ],
    settings: {},
};

test("an empty required setting is what is missing", () => {
    assert.deepEqual(missingSettings(sitemap).map((f) => f.label), ["Storefront URL"]);
    assert.deepEqual(missingSettings({ ...sitemap, settings: { storefront_url: "https://shop.example" } }), []);
    assert.deepEqual(missingSettings({ ...sitemap, settings: { storefront_url: "   " } }).length, 1);
});

test("the note says what to fill in, and how", () => {
    assert.equal(
        activateNote(sitemap),
        "Fill in Storefront URL (the full address, starting with https://) before switching Sitemap on.",
    );
    assert.equal(activateNote({ ...sitemap, settings: { storefront_url: "https://x.example" } }), "");
});

test("a plugin with no required settings is always ready", () => {
    assert.equal(activateNote({ title: "Guest checkout", fields: [], settings: {} }), "");
});
