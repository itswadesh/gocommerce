/*
 * Create from photos: the review step's edits. A photo moved, two products
 * merged, one split off or left out — and what is sent when the shop presses
 * Create.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import { costMinor, mergeInto, movePhoto, problems, progress, saveBody, setSkip, sizesFrom } from "../src/lib/photoproducts.js";

const draft = (title, option_name, variants) => ({ title, option_name, variants, alts: {}, sizes: [] });
const proposals = () => [
    { id: 1, draft: draft("Hoops", "Finish", [{ value: "Gold", photos: [10, 11] }, { value: "Silver", photos: [12] }]) },
    { id: 2, draft: draft("Bracelet", "", [{ value: "", photos: [13] }]) },
];

test("a photo moves to another product's variant", () => {
    const out = movePhoto(proposals(), 11, [1, 0]);
    assert.deepEqual(out[0].draft.variants[0].photos, [10]);
    assert.deepEqual(out[1].draft.variants[0].photos, [13, 11]);
});

test("moving a variant's last photo away takes the variant, and one left alone loses its option", () => {
    const out = movePhoto(proposals(), 12, [0, 0]);
    assert.equal(out[0].draft.variants.length, 1);
    assert.equal(out[0].draft.option_name, "");
    assert.equal(out[0].draft.variants[0].value, "");
    assert.deepEqual(out[0].draft.variants[0].photos, [10, 11, 12]);
});

test("a photo split off becomes a product of its own, and an emptied product goes", () => {
    let out = movePhoto(proposals(), 13, "new");
    assert.equal(out.length, 2, "the bracelet had one photo, so it is gone and the new product replaces it");
    assert.equal(out[1].id, undefined);
    assert.deepEqual(out[1].draft.variants, [{ value: "", photos: [13] }]);
    out = movePhoto(proposals(), 10, "new");
    assert.equal(out.length, 3);
});

test("merging single-variant products names the variants after them", () => {
    const items = [proposals()[1], { id: 3, draft: draft("Bangle", "", [{ value: "", photos: [14] }]) }];
    const out = mergeInto(items, 1, 0);
    assert.equal(out.length, 1);
    assert.equal(out[0].draft.option_name, "Style");
    assert.deepEqual(
        out[0].draft.variants.map((v) => v.value),
        ["Bracelet", "Bangle"],
    );
});

test("the edits never change the list they were given", () => {
    const items = proposals();
    const before = JSON.stringify(items);
    movePhoto(items, 10, "new");
    mergeInto(items, 1, 0);
    setSkip(items, 0, true);
    assert.equal(JSON.stringify(items), before);
});

test("Create is held back by a missing title or two variants of one name", () => {
    const items = proposals();
    assert.deepEqual(problems(items), []);
    items[0].draft.variants[1].value = "gold ";
    items[1].draft.title = " ";
    assert.equal(problems(items).length, 2);
    // A product left out is not checked: it is not being created.
    assert.equal(problems(setSkip(items, 0, true)).length, 1);
});

test("what is saved leaves out the skipped and the already created", () => {
    let items = setSkip(proposals(), 1, true);
    items = movePhoto(items, 10, "new");
    items[0].product_id = null;
    const body = saveBody(items);
    assert.equal(body.items.length, 2);
    assert.deepEqual(Object.keys(body.items[0]), ["id", "draft"]);
    assert.deepEqual(Object.keys(body.items[1]), ["draft"]);
    items[0].product_id = 99;
    assert.equal(saveBody(items).items.length, 1);
});

test("the sizes box reads commas and lines, each size once", () => {
    assert.deepEqual(sizesFrom(" S, M ,l\nL,, XL "), ["S", "M", "l", "XL"]);
    assert.deepEqual(sizesFrom(""), []);
});

test("progress is a fraction once the count is known, and cost is whole minor units", () => {
    assert.equal(progress({ step_done: 0, step_total: 0 }), null);
    assert.equal(progress({ step_done: 3, step_total: 12 }), 0.25);
    assert.equal(costMinor({ cost_micro_minor: 2_800_000 }), 3);
    assert.equal(costMinor(null), 0);
});

test("a photo's alt text goes with it when it moves or its product is merged", () => {
    const items = proposals();
    items[0].draft.alts = { 12: "Silver hoops" };
    items[1].draft.alts = { 13: "A beaded bracelet" };
    assert.equal(movePhoto(items, 12, [1, 0])[1].draft.alts[12], "Silver hoops");
    assert.equal(mergeInto(items, 1, 0)[0].draft.alts[13], "A beaded bracelet");
});
