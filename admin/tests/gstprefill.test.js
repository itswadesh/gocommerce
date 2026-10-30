/*
 * The review list "Fill from GST schedule" shows, with no browser and no
 * engine: what each row proposes, when it asks a person to check, and what
 * saving a ticked row sends.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import { reviewRows, ruleName, saveRequest } from "../src/lib/gstprefill.js";

const m = (prefix, rate_bp, extra = {}) => ({
    prefix,
    rate_bp,
    description: `entry ${prefix}`,
    conditional: false,
    source: `9/2025-Integrated Tax (Rate), Schedule X, S. No. ${prefix}`,
    ...extra,
});

const jewellery = { kind: "category", id: 1, name: "Jewellery", codes: ["7113"], matches: [m("7113", 300)] };

test("a plain match proposes its rate, named for the target, with nothing to check", () => {
    const { rows, unmatched } = reviewRows([jewellery], []);
    assert.equal(unmatched.length, 0);
    const [r] = rows;
    assert.equal(r.key, "category:1");
    assert.equal(r.choice, 0);
    assert.equal(r.options[0].rate_bp, 300);
    assert.equal(ruleName(r), "GST 3% · Jewellery");
    assert.deepEqual(r.check, []);
    assert.equal(r.existing, null);
});

test("a conditional entry, or another entry at a different rate, asks for a check", () => {
    const shoes = {
        kind: "category",
        id: 2,
        name: "Shoes",
        codes: ["6403"],
        matches: [m("6403", 1800), m("64", 500, { conditional: true })],
    };
    const tees = { kind: "category", id: 3, name: "Tees", codes: ["6109"], matches: [m("61", 500, { conditional: true }), m("61", 1800, { conditional: true })] };
    const { rows } = reviewRows([shoes, tees], []);
    assert.deepEqual(rows[0].check, ["another entry rates it differently"]);
    assert.equal(rows[0].options.length, 2, "the other rate is offered, not dropped");
    assert.ok(rows[1].check.includes("conditional"));
    // The same rate twice from two entries is one option, not two.
    const twice = reviewRows([{ ...jewellery, matches: [m("7113", 300), m("71", 300)] }], []).rows[0];
    assert.equal(twice.options.length, 1);
    assert.deepEqual(twice.check, []);
});

test("a product whose variants disagree is checked", () => {
    const p = { kind: "product", id: 9, name: "Set", codes: ["7113", "7117"], matches: [m("7113", 300), m("7117", 300)] };
    const [r] = reviewRows([p], []).rows;
    assert.ok(r.check.includes("its variants carry different codes"));
});

test("no match, or only entries under a chapter, is listed to set by hand", () => {
    const none = { kind: "category", id: 4, name: "Misc", codes: ["0000"], matches: [] };
    const chapter = { kind: "category", id: 5, name: "Gems", codes: ["71"], matches: [m("7113", 300, { narrower: true })] };
    const { rows, unmatched } = reviewRows([none, chapter], []);
    assert.equal(rows.length, 0);
    assert.deepEqual(
        unmatched.map((u) => [u.name, u.reason]),
        [
            ["Misc", "no rate found"],
            ["Gems", "the schedule rates the headings under this code, not the code itself"],
        ],
    );
});

test("an Indian rule already on the target is shown and replaced only by editing it", () => {
    const rates = [
        { id: 70, name: "Old", rate_bp: 500, country: "IN", state: "", category_id: 1 },
        { id: 71, name: "Karnataka", rate_bp: 500, country: "IN", state: "KA", category_id: 1 },
        { id: 72, name: "Elsewhere", rate_bp: 500, country: "", state: "", category_id: 1 },
    ];
    const [r] = reviewRows([jewellery], rates).rows;
    assert.equal(r.existing.id, 70);
    assert.deepEqual(saveRequest(r), {
        method: "PATCH",
        path: "/api/admin/tax-rates/70",
        body: { name: "GST 3% · Jewellery", rate_bp: 300 },
    });
});

test("a new rule is India, every state, the target, at the chosen rate", () => {
    const shoes = { kind: "product", id: 2, name: "Boot", codes: ["6403"], matches: [m("6403", 1800), m("64", 500, { conditional: true })] };
    const [r] = reviewRows([shoes], []).rows;
    r.choice = 1;
    assert.deepEqual(saveRequest(r), {
        method: "POST",
        path: "/api/admin/tax-rates",
        body: { name: "GST 5% · Boot", rate_bp: 500, country: "IN", state: "", category_id: null, product_id: 2, active: true },
    });
});
