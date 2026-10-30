import { test } from "node:test";
import assert from "node:assert/strict";

import { decodeExport, ratioText, reportCSV, rowLabel, summaryProblem } from "../src/lib/ads.js";

const m = (amount_minor, currency = "INR") => ({ amount_minor, currency });
const row = (over) => ({
    source: "google", medium: "cpc", campaign: "Diwali", orders: 2,
    revenue: m(150000), average_order: m(75000), refunded: m(0), net: m(150000), returns: 0,
    product_cost: m(60000), gross_profit: m(90000), uncosted_units: 1, new_customers: 1, returning_customers: 1,
    spend: m(30000), roas: 5, poas: 3, cost_per_order: m(15000), ...over,
});

test("the CSV has a row per campaign and a total per currency, amounts as decimals", () => {
    const csv = reportCSV({
        currencies: [
            { currency: "INR", rows: [row()], totals: row({ source: "", campaign: "" }) },
            { currency: "USD", rows: [row({ revenue: m(2000, "USD"), spend: null, roas: null })], totals: row({}) },
        ],
    });
    const lines = csv.trim().split("\r\n");
    assert.equal(lines.length, 5);
    assert.match(lines[0], /^Currency,Source,Medium,Campaign,Orders,Revenue/);
    assert.match(lines[1], /^INR,google,cpc,Diwali,2,1500\.00,750\.00/);
    assert.match(lines[2], /^INR,Total,,,/);
    assert.match(lines[3], /^USD,google,cpc,Diwali,2,20\.00,.*,1,,,3,150\.00$/);
});

test("a campaign name a spreadsheet would run is kept as text", () => {
    const csv = reportCSV({ currencies: [{ currency: "INR", rows: [row({ campaign: '=HYPERLINK("x")' })], totals: row({}) }] });
    assert.match(csv, /,"'=HYPERLINK\(""x""\)",/);
    // A negative profit is a number, not a formula.
    const loss = reportCSV({ currencies: [{ currency: "INR", rows: [row({ gross_profit: m(-2500) })], totals: row({}) }] });
    assert.match(loss, /,-25\.00,/);
});

test("names and ratios read plainly", () => {
    assert.deepEqual(rowLabel({ source: "(direct)", medium: "", campaign: "" }), { where: "(direct)", campaign: "(no campaign)" });
    assert.equal(rowLabel(row()).where, "google / cpc");
    assert.equal(ratioText(null), "—");
    assert.equal(ratioText(0.375), "0.38×");
});

test("each AI refusal says what to do", () => {
    assert.equal(summaryProblem({ code: "ai_credit_exhausted", message: "top up" }).action, "credit");
    assert.equal(summaryProblem({ code: "ai_policy_blocked" }).action, "settings");
    assert.equal(summaryProblem({ code: "ai_no_summary" }).action, "retry");
    assert.equal(summaryProblem({ code: "ai_not_ready", message: "no key" }).text, "no key");
});

test("an export saved as UTF-16 reads as text", () => {
    const utf16 = new Uint8Array([0xff, 0xfe, 0x44, 0x00, 0x61, 0x00, 0x79, 0x00]);
    assert.equal(decodeExport(utf16.buffer), "Day");
    assert.equal(decodeExport(new TextEncoder().encode("Day").buffer), "Day");
});
