import { test } from "node:test";
import assert from "node:assert/strict";
import {
    blankSection,
    insertAt,
    moveSection,
    duplicateSection,
    removeSection,
    toggleSection,
    sectionSummary,
    visibleFields,
} from "../src/lib/layouts.js";

const registry = [
    {
        type: "hero",
        label: "Hero",
        fields: [
            { key: "heading", label: "Heading", kind: "text", required: true },
            { key: "align", label: "Text position", kind: "select", default: "left" },
        ],
    },
    {
        type: "product-grid",
        label: "Product grid",
        fields: [
            { key: "heading", kind: "text" },
            { key: "source", kind: "select", default: "collection" },
            { key: "collection", kind: "collection", show_when: { source: "collection" } },
            { key: "products", kind: "products", show_when: { source: "products" } },
            { key: "limit", kind: "number", default: 8 },
        ],
    },
    {
        type: "benefits",
        label: "Benefits strip",
        fields: [{ key: "items", kind: "list", fields: [{ key: "title", kind: "text" }] }],
    },
];

const ids = (list) => list.map((s) => s.id).join(",");
const page = () => [
    { id: "a", type: "hero", enabled: true, settings: { heading: "One" } },
    { id: "b", type: "hero", enabled: true, settings: { heading: "Two" } },
];

test("a new section takes its type's defaults and an id the page does not have", () => {
    const s = blankSection("product-grid", registry, ["a", "b"]);
    assert.equal(s.type, "product-grid");
    assert.equal(s.enabled, true);
    assert.deepEqual(s.settings, { source: "collection", limit: 8 });
    assert.ok(s.id && !["a", "b"].includes(s.id));
    assert.match(s.id, /^[A-Za-z0-9_-]{1,40}$/);
});

test("a section goes in at the very place asked: before the first, between, after the last", () => {
    const n = { id: "n", type: "hero", enabled: true, settings: {} };
    assert.equal(ids(insertAt(page(), 0, n)), "n,a,b");
    assert.equal(ids(insertAt(page(), 1, n)), "a,n,b");
    assert.equal(ids(insertAt(page(), 2, n)), "a,b,n");
    assert.equal(ids(insertAt([], 0, n)), "n", "an empty page takes its first section");
});

test("moving keeps each section's own settings and stops at the ends", () => {
    const moved = moveSection(page(), 0, 1);
    assert.equal(ids(moved), "b,a");
    assert.equal(moved[1].settings.heading, "One");
    assert.equal(ids(moveSection(page(), 0, -1)), "a,b");
    assert.equal(ids(moveSection(page(), 1, 1)), "a,b");
});

test("a duplicate is a deep copy with a new id, right below the original", () => {
    const list = [
        { id: "a", type: "benefits", enabled: true, settings: { items: [{ title: "Free" }] } },
        { id: "b", type: "hero", enabled: true, settings: {} },
    ];
    const out = duplicateSection(list, 0);
    assert.equal(out.length, 3);
    assert.equal(out[0].id, "a");
    assert.notEqual(out[1].id, "a");
    assert.equal(out[2].id, "b");
    out[1].settings.items[0].title = "Changed";
    assert.equal(list[0].settings.items[0].title, "Free", "editing the copy leaves the original alone");
});

test("removing and hiding change only the one section", () => {
    assert.equal(ids(removeSection(page(), 0)), "b");
    const hidden = toggleSection(page(), 1);
    assert.equal(hidden[1].enabled, false);
    assert.equal(hidden[1].settings.heading, "Two", "hiding keeps the settings");
    assert.equal(hidden[0].enabled, true);
});

test("a row says what the section is and what it says", () => {
    assert.deepEqual(sectionSummary(page()[0], registry), { label: "Hero", text: "One" });
    assert.deepEqual(sectionSummary({ id: "x", type: "marquee", settings: {} }, registry), {
        label: "Unknown section (marquee)",
        text: "",
    });
});

test("a product grid asks for a collection or for products, not both", () => {
    const grid = registry[1].fields;
    const keys = (s) => visibleFields(grid, s).map((f) => f.key).join(",");
    assert.equal(keys({ source: "collection" }), "heading,source,collection,limit");
    assert.equal(keys({ source: "products" }), "heading,source,products,limit");
    assert.equal(keys({}), "heading,source,collection,limit", "the default source counts");
});

test("a duplicate copies a proxied section, as the screen's state is", () => {
    const target = { id: "a", type: "hero", enabled: true, settings: { heading: "One" } };
    const proxied = new Proxy(target, {});
    const out = duplicateSection([proxied], 0);
    assert.equal(out.length, 2);
    assert.equal(out[1].settings.heading, "One");
});
