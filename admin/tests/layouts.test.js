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

// ------------------------------------------------------------ phase 2 (D126)

import { moveTo, dropIndex, createHistory, createAutosave, previewAddress, storefrontAddress } from "../src/lib/layouts.js";

test("moveTo moves one section to any position, and leaves the list alone for a no-op", () => {
    const list = ["a", "b", "c", "d"].map((id) => ({ id }));
    assert.deepEqual(moveTo(list, 0, 2).map((s) => s.id), ["b", "c", "a", "d"]);
    assert.deepEqual(moveTo(list, 3, 0).map((s) => s.id), ["d", "a", "b", "c"]);
    assert.equal(moveTo(list, 1, 1), list);
    assert.equal(moveTo(list, 1, 9), list);
});

test("dropIndex is the slot the pointer is over, by the rows' middles", () => {
    const middles = [20, 60, 100];
    assert.equal(dropIndex(middles, 0), 0);
    assert.equal(dropIndex(middles, 59), 1);
    assert.equal(dropIndex(middles, 61), 2);
    assert.equal(dropIndex(middles, 500), 2);
});

test("history undoes and redoes, and a new edit forgets what was undone", () => {
    const h = createHistory();
    assert.equal(h.canUndo(), false);
    h.record("v1");
    h.record("v2");
    assert.equal(h.undo("v3"), "v2");
    assert.equal(h.undo("v2"), "v1");
    assert.equal(h.undo("v1"), null);
    assert.equal(h.redo("v1"), "v2");
    h.record("v2");
    assert.equal(h.canRedo(), false);
});

test("history folds a run of typing in one field into one step", () => {
    let now = 0;
    const h = createHistory({ now: () => now, window: 1000 });
    h.record("before", "hero.heading");
    now = 400;
    h.record("typing", "hero.heading");
    now = 800;
    h.record("still typing", "hero.heading");
    assert.equal(h.undo("done"), "before");
    assert.equal(h.canUndo(), false);
    now = 5000;
    h.record("x", "hero.heading");
    now = 7000;
    h.record("y", "hero.heading");
    assert.equal(h.size(), 2, "a pause starts a new step");
});

function fakeTimers() {
    const timers = new Map();
    let next = 1;
    return {
        set: (fn) => (timers.set(next, fn), next++),
        clear: (id) => timers.delete(id),
        fire() {
            const all = [...timers.values()];
            timers.clear();
            all.forEach((fn) => fn());
        },
        pending: () => timers.size,
    };
}

test("autosave waits for a pause, saves the latest edit once, then reports saved", async () => {
    const t = fakeTimers();
    const saved = [];
    const states = [];
    const a = createAutosave({ save: async (doc) => saved.push(doc), setTimer: t.set, clearTimer: t.clear, onstate: (s) => states.push(s) });
    a.change("one");
    a.change("two");
    assert.deepEqual(saved, []);
    assert.equal(a.state(), "pending");
    t.fire();
    await a.idle();
    assert.deepEqual(saved, ["two"]);
    assert.equal(a.state(), "saved");
    assert.deepEqual(states, ["pending", "saving", "saved"]);
});

test("autosave saves one at a time: an edit during a save goes in the next", async () => {
    const t = fakeTimers();
    const saved = [];
    let release;
    const a = createAutosave({
        save: (doc) => new Promise((ok) => { saved.push(doc); release = ok; }),
        setTimer: t.set, clearTimer: t.clear,
    });
    a.change("one");
    a.flush();
    a.change("two");
    a.flush();
    assert.deepEqual(saved, ["one"], "the second waits for the first");
    release();
    await new Promise((r) => setTimeout(r, 0));
    assert.deepEqual(saved, ["one", "two"]);
    release();
    await a.idle();
    assert.equal(a.state(), "saved");
});

test("autosave keeps a failed edit to retry, and stops on a conflict", async () => {
    const t = fakeTimers();
    let fail = { status: 0 };
    const saved = [];
    const a = createAutosave({
        save: async (doc) => { if (fail) throw fail; saved.push(doc); },
        setTimer: t.set, clearTimer: t.clear,
    });
    a.change("one");
    await a.flush();
    assert.equal(a.state(), "error");
    fail = null;
    await a.retry();
    assert.deepEqual(saved, ["one"]);

    fail = { status: 409 };
    a.change("two");
    await a.flush();
    assert.equal(a.state(), "conflict");
    a.change("three");
    assert.equal(t.pending(), 0, "nothing more is sent over somebody else's save");
    await a.flush();
    assert.deepEqual(saved, ["one"]);
});

test("cancel drops a pending edit, for a reset that throws the draft away", async () => {
    const t = fakeTimers();
    const saved = [];
    const a = createAutosave({ save: async (doc) => saved.push(doc), setTimer: t.set, clearTimer: t.clear });
    a.change("one");
    await a.cancel();
    t.fire();
    await a.idle();
    assert.deepEqual(saved, []);
    assert.equal(a.state(), "saved");
});

test("previewAddress opens the storefront's home page with the key and the panel's origin", () => {
    assert.equal(
        previewAddress("https://shop.example.com/", "k-1", "https://admin.example.com"),
        "https://shop.example.com/?preview=k-1&editor=https%3A%2F%2Fadmin.example.com",
    );
    assert.equal(previewAddress("https://shop.example.com/some/page", "k", ""), "https://shop.example.com/?preview=k");
    assert.equal(previewAddress("not an address", "k", ""), "");
    assert.equal(previewAddress("javascript:alert(1)", "k", ""), "");
});

test("a store with a domain has an address to preview without a preview key", () => {
    // Somebody who may see the theme editor but not publish gets no key, and
    // so no address from it: the store's own domain is still where it is.
    assert.equal(storefrontAddress("", "", "shop.example.com"), "https://shop.example.com");
    assert.equal(storefrontAddress("", "https://minted.example.com", "shop.example.com"), "https://minted.example.com");
    assert.equal(storefrontAddress("https://typed.example.com", "https://shop.example.com", "shop.example.com"), "https://typed.example.com");
    assert.equal(storefrontAddress("", "", ""), "");
    assert.equal(storefrontAddress("", "", undefined), "");
});

// ------------------------------------------------------------ Phase 3 (D127)

import { contrastRatio, themeWarnings, absoluteMedia, themeSettingsSummary } from "../src/lib/layouts.js";

test("a show_when on a switch matches its on and off", () => {
    const fields = [
        { key: "autoplay", kind: "bool", default: false },
        { key: "interval", kind: "number", show_when: { autoplay: "true" } },
    ];
    assert.deepEqual(visibleFields(fields, {}).map((f) => f.key), ["autoplay"]);
    assert.deepEqual(visibleFields(fields, { autoplay: true }).map((f) => f.key), ["autoplay", "interval"]);
});

test("a show_when on a switch's off counts a never-set switch as off", () => {
    // The header's menu picker: a header saved before hide_menu existed still offers it.
    const fields = [
        { key: "hide_menu", kind: "bool", default: false },
        { key: "menu", kind: "menu", show_when: { hide_menu: "false" } },
    ];
    assert.deepEqual(visibleFields(fields, {}).map((f) => f.key), ["hide_menu", "menu"]);
    assert.deepEqual(visibleFields(fields, { hide_menu: true }).map((f) => f.key), ["hide_menu"]);
});

test("a row summarises a list by its items' headings or questions", () => {
    const reg = [
        { type: "slideshow", label: "Slideshow", fields: [] },
        { type: "faq", label: "Questions and answers", fields: [] },
    ];
    assert.equal(sectionSummary({ type: "slideshow", settings: { slides: [{ heading: "One" }, { image: "/x" }, { heading: "Two" }] } }, reg).text, "One · Two");
    assert.equal(sectionSummary({ type: "faq", settings: { items: [{ question: "Why?" }] } }, reg).text, "Why?");
});

test("contrast is the WCAG ratio between two colours", () => {
    assert.equal(contrastRatio("#000000", "#ffffff").toFixed(1), "21.0");
    assert.equal(contrastRatio("#ffffff", "#ffffff").toFixed(1), "1.0");
    assert.equal(contrastRatio("#777777", "#ffffff").toFixed(2), "4.48");
    assert.equal(contrastRatio("nope", "#ffffff"), null);
});

test("a low-contrast pair is warned about, never one the storefront chose", () => {
    assert.deepEqual(themeWarnings({}), []);
    assert.deepEqual(themeWarnings({ background: "#ffffff", text: "#111111" }), []);
    const w = themeWarnings({ background: "#ffffff", text: "#999999", accent: "#ffcc00" });
    assert.deepEqual(w.map((x) => x.keys), [["text", "background"], ["accent_text", "accent"], ["text", "muted"]]);
    assert.match(w[0].message, /2\.8:1/);
    // An unset half is the storefront's own colour: white text on its own buttons.
    assert.deepEqual(themeWarnings({ accent_text: "#ffffff" }), []);
});

test("Files addresses become the panel's own, for the storefront frame to load", () => {
    const doc = {
        theme_settings: { accent: "#112233" },
        sections: [
            { id: "a", type: "hero", settings: { image: "/media/a.jpg", button_link: "/products", heading: "/media/not-a-picture" } },
            { id: "b", type: "slideshow", settings: { slides: [{ image: "/media/b.jpg", mobile_image: "https://cdn.example/m.jpg" }] } },
            { id: "c", type: "video", settings: { video_file: "/media/c.mp4", poster: "/media/c.jpg" } },
        ],
    };
    const out = absoluteMedia(doc, "https://admin.example");
    assert.equal(out.sections[0].settings.image, "https://admin.example/media/a.jpg");
    assert.equal(out.sections[0].settings.button_link, "/products");
    assert.equal(out.sections[0].settings.heading, "/media/not-a-picture");
    assert.equal(out.sections[1].settings.slides[0].image, "https://admin.example/media/b.jpg");
    assert.equal(out.sections[1].settings.slides[0].mobile_image, "https://cdn.example/m.jpg");
    assert.equal(out.sections[2].settings.video_file, "https://admin.example/media/c.mp4");
    assert.equal(out.sections[2].settings.poster, "https://admin.example/media/c.jpg");
    assert.equal(doc.sections[0].settings.image, "/media/a.jpg", "the screen's own document is left alone");
});

test("the theme settings row says what is set", () => {
    assert.equal(themeSettingsSummary({}), "The storefront's own colours and fonts");
    assert.equal(themeSettingsSummary({ accent: "#112233" }), "1 setting changed");
    assert.equal(themeSettingsSummary({ accent: "#112233", radius: "large" }), "2 settings changed");
});

// ------------------------------------------------------------ Phase 4 (D129)

import { PAGES, pageMeta, libraryFor, isFixed, previewPath, localInputValue, fromLocalInput, scheduleLine } from "../src/lib/layouts.js";

const pageRegistry = [
    { type: "hero", label: "Hero", description: "", fields: [] },
    { type: "main-product", label: "Product", description: "", fixed: true, pages: ["product"], fields: [] },
    { type: "product-reviews", label: "Reviews", description: "", pages: ["product"], fields: [] },
    { type: "main-collection", label: "Products", description: "", fixed: true, pages: ["collection"], fields: [] },
];

test("each page has its route, its title and the engine's key", () => {
    assert.deepEqual(PAGES.map((p) => p.key), ["home", "product", "collection", "header", "footer"]);
    assert.equal(pageMeta("product").route, "product");
    assert.equal(pageMeta("home").route, "homepage");
    assert.equal(pageMeta("nope"), undefined);
});

test("the library offers what a page can hold, never its fixed core", () => {
    assert.deepEqual(libraryFor(pageRegistry, "home").map((t) => t.type), ["hero"]);
    assert.deepEqual(libraryFor(pageRegistry, "product").map((t) => t.type), ["hero", "product-reviews"]);
    assert.deepEqual(libraryFor(pageRegistry, "collection").map((t) => t.type), ["hero"]);
    assert.equal(isFixed({ type: "main-product" }, pageRegistry), true);
    assert.equal(isFixed({ type: "hero" }, pageRegistry), false);
});

test("the preview opens the page being edited, on the product or collection picked", () => {
    assert.equal(previewPath("home", null), "/");
    assert.equal(previewPath("product", { slug: "blue mug" }), "/products/blue%20mug");
    assert.equal(previewPath("collection", { slug: "summer" }), "/collections/summer");
    assert.equal(previewPath("product", null), "");
    assert.equal(
        previewAddress("https://shop.example.com", "k", "https://admin.example.com", "/products/mug"),
        "https://shop.example.com/products/mug?preview=k&editor=https%3A%2F%2Fadmin.example.com",
    );
});

test("a schedule's time goes to and from the browser's local date-time box", () => {
    const at = new Date(2026, 10, 3, 9, 5);
    assert.equal(localInputValue(at), "2026-11-03T09:05");
    assert.equal(fromLocalInput("2026-11-03T09:05").getTime(), at.getTime());
    assert.equal(fromLocalInput(""), null);
    assert.equal(fromLocalInput("not a time"), null);
});

test("the schedule line says when, and whether later edits are in it", () => {
    const when = (d) => d.toISOString();
    const s = { publish_at: "2026-11-03T09:05:00Z", scheduled_by: "a@example.com", outdated: false };
    assert.equal(scheduleLine(s, when), "Publishes 2026-11-03T09:05:00.000Z, set by a@example.com.");
    assert.equal(
        scheduleLine({ ...s, outdated: true }, when),
        "Publishes 2026-11-03T09:05:00.000Z, set by a@example.com. Edits since are not in it: schedule again to include them.",
    );
    assert.equal(scheduleLine(null, when), "");
});

// ------------------------------------------------------------ the header and footer (D131)

import { menuOptions, isChrome } from "../src/lib/layouts.js";

const chromeRegistry = [
    ...pageRegistry,
    { type: "header", label: "Header", description: "", fixed: true, pages: ["header"], fields: [{ key: "announcement_text", kind: "text" }] },
    { type: "footer", label: "Footer", description: "", fixed: true, pages: ["footer"], fields: [] },
];
const chromePages = [
    { key: "home" },
    { key: "product", core: "main-product" },
    { key: "header", core: "header", core_only: true },
    { key: "footer", core: "footer", core_only: true },
];

test("the header and the footer are their own section and nothing else", () => {
    assert.deepEqual(libraryFor(chromeRegistry, "header", chromePages), []);
    assert.deepEqual(libraryFor(chromeRegistry, "footer", chromePages), []);
    // A page that is not one of them still offers every type it may hold.
    assert.deepEqual(libraryFor(chromeRegistry, "home", chromePages).map((t) => t.type), ["hero"]);
    assert.equal(isChrome("header"), true);
    assert.equal(isChrome("footer"), true);
    assert.equal(isChrome("home"), false);
    assert.equal(pageMeta("header").route, "header");
    assert.equal(pageMeta("footer").noun, "footer");
});

test("the header and footer are previewed on the home page, which draws both", () => {
    assert.equal(previewPath("header", null), "/");
    assert.equal(previewPath("footer", null), "/");
    // The storefront is told which part the key is for: on the home page it
    // would otherwise try it as the home page's.
    assert.equal(
        previewAddress("https://shop.example.com", "k", "https://admin.example.com", "/", "footer"),
        "https://shop.example.com/?preview=k&editor=https%3A%2F%2Fadmin.example.com&preview_page=footer",
    );
    assert.equal(previewAddress("https://shop.example.com", "k", "", "/", ""), "https://shop.example.com/?preview=k");
});

test("a header's row says what its strip says", () => {
    assert.equal(sectionSummary({ type: "header", settings: { announcement_text: "Free returns" } }, chromeRegistry).text, "Free returns");
});

test("the menu choice lists the store's menus, and keeps one it cannot find", () => {
    const menus = [
        { handle: "main", title: "Main menu" },
        { handle: "footer", title: "Footer" },
    ];
    assert.deepEqual(menuOptions(menus, "main"), [
        { value: "main", label: "Main menu" },
        { value: "footer", label: "Footer" },
    ]);
    // A menu deleted since, or one this person cannot list, is still the
    // setting: it shows rather than silently becoming another.
    assert.deepEqual(menuOptions(menus, "gone"), [
        { value: "main", label: "Main menu" },
        { value: "footer", label: "Footer" },
        { value: "gone", label: "gone (not found in Menus)" },
    ]);
    assert.deepEqual(menuOptions(null, ""), []);
});

// ---------------------------------------------------------------- Benefit icons (D139)
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { ICON_GLYPHS, iconChoices } from "../src/lib/layouts.js";

test("every icon the engine offers has a glyph the panel's font draws", () => {
    const go = readFileSync(fileURLToPath(new URL("../../ext/layouts/registry.go", import.meta.url)), "utf8");
    const block = go.match(/var BenefitIcons = \[\]Option\{([\s\S]*?)\n\}/)[1];
    const keys = [...block.matchAll(/\{"([^"]+)", "[^"]+"\}/g)].map((m) => m[1]);
    assert.ok(keys.length >= 12, `${keys.length} keys read from the engine`);
    const css = readFileSync(fileURLToPath(new URL("../src/lib/remixicon.css", import.meta.url)), "utf8");
    for (const k of keys) {
        assert.ok(ICON_GLYPHS[k], `no glyph for ${k}`);
        assert.ok(css.includes(`.${ICON_GLYPHS[k]}:before`), ICON_GLYPHS[k]);
    }
});

test("the icon picker starts with None, then the engine's choices with their glyphs", () => {
    const choices = iconChoices([
        { value: "truck", label: "Delivery" },
        { value: "lock", label: "Secure payment" },
    ]);
    assert.deepEqual(choices, [
        { value: "", label: "None", glyph: "ri-close-line" },
        { value: "truck", label: "Delivery", glyph: "ri-truck-line" },
        { value: "lock", label: "Secure payment", glyph: "ri-lock-line" },
    ]);
});
