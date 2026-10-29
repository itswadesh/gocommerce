/*
 * "Improve with AI" on the product editor (D152): the proposal beside the
 * form, and only the chosen changes copied into it.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import { applied, keyOf, refusal, rowsFor, textOf } from "../src/lib/catalogai.js";

const proposal = {
    changes: [
        { field: "title", current: "blue mug", proposed: "Blue ceramic mug", reason: "Clearer." },
        { field: "description", current: "<p>A mug.</p>", proposed: "<p>A &lt;blue&gt; mug.</p><p>Wide handle.</p>", reason: "" },
        { field: "highlights", current: [], proposed: ["Wide handle", "Glossy glaze"], reason: "Scannable." },
        { field: "tags", current: ["mug"], proposed: ["mug", "blue"], reason: "Search." },
        { field: "category", current: { id: 3, name: "Kitchen" }, proposed: { id: 7, name: "Kitchen / Drinkware" }, reason: "Closer." },
        { field: "alt", media_id: 41, current: "", proposed: "Blue mug, side view", reason: "Describes it." },
        { field: "alt", media_id: 99, current: "", proposed: "A picture since removed", reason: "" },
    ],
};

const form = {
    title: "blue mug, typed",
    description: "<p>A mug.</p>",
    highlights: [""],
    tags: ["mug"],
    category_id: 3,
    seo_title: "",
    alts: {},
};
const media = [{ id: 41, alt: "", url: "/media/mug.jpg" }];
const categories = [
    { id: 3, full_name: "Kitchen" },
    { id: 7, full_name: "Kitchen / Drinkware" },
];

test("each row puts what the form holds now beside the proposal", () => {
    const rows = rowsFor(proposal, { form, media, categories, canAlt: true });
    assert.deepEqual(
        rows.map((r) => r.key),
        ["title", "description", "highlights", "tags", "category", "alt:41"],
        "an alt text for a picture no longer on the product is left out",
    );
    const by = Object.fromEntries(rows.map((r) => [r.key, r]));
    assert.equal(by.title.now, "blue mug, typed", "now is the form, unsaved typing included");
    assert.equal(by.title.proposed, "Blue ceramic mug");
    assert.equal(by.description.proposed, "A <blue> mug.\n\nWide handle.");
    assert.deepEqual(by.highlights.now, []);
    assert.equal(by.category.now, "Kitchen");
    assert.equal(by.category.proposed, "Kitchen / Drinkware");
    assert.equal(by["alt:41"].image, "/media/mug.jpg");
    assert.equal(by["alt:41"].label, "Alt text");
});

test("alt texts need media.write, which saving them takes", () => {
    const rows = rowsFor(proposal, { form, media, categories, canAlt: false });
    assert.equal(rows.some((r) => r.field === "alt"), false);
});

test("applying copies only the chosen changes into a new form", () => {
    const chosen = new Set(["title", "highlights", "category", "alt:41"]);
    const next = applied(form, proposal.changes, chosen);
    assert.notEqual(next, form, "a new object, so the editor sees it as changed");
    assert.equal(next.title, "Blue ceramic mug");
    assert.equal(next.description, "<p>A mug.</p>", "an unchosen change is not applied");
    assert.deepEqual(next.tags, ["mug"]);
    assert.deepEqual(next.highlights, ["Wide handle", "Glossy glaze", ""], "one blank row to type into, as the editor keeps");
    assert.equal(next.category_id, 7);
    assert.deepEqual(next.alts, { 41: "Blue mug, side view" });
    assert.equal(form.title, "blue mug, typed", "the form passed in is left as it was");
    assert.deepEqual(form.alts, {});
});

test("nothing chosen is the same form", () => {
    assert.deepEqual(applied(form, proposal.changes, new Set()), form);
});

test("an alt text's key is its picture", () => {
    assert.equal(keyOf({ field: "alt", media_id: 41 }), "alt:41");
    assert.equal(keyOf({ field: "title" }), "title");
});

test("a description reads as its words", () => {
    assert.equal(textOf("<p>One<br>two</p><p>Three &amp; four</p>"), "One\ntwo\n\nThree & four");
    assert.equal(textOf(""), "");
    assert.equal(textOf(null), "");
});

test("a refusal says where it is fixed", () => {
    assert.equal(refusal({ code: "ai_not_ready", message: "switched off" }).fix, "settings");
    assert.equal(refusal({ code: "ai_policy_blocked", message: "x" }).fix, "settings");
    assert.equal(refusal({ code: "ai_budget_exhausted", message: "x" }).title, "This month's AI budget is used up");
    assert.equal(refusal({ code: "ai_provider_failed", message: "x" }).fix, "retry");
    assert.equal(refusal({ code: "ai_credit_exhausted", message: "You've used your free AI trial" }).fix, "credit");
    assert.equal(refusal({ code: "network_error", message: "Could not reach the store." }).message, "Could not reach the store.");
});
