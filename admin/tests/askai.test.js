import { test } from "node:test";
import assert from "node:assert/strict";

import { capEntries, historyFor, MAX_TURNS, needsSupport, refreshedProposal, safeLinks, screensFor, valueText } from "../src/lib/askai.js";

test("history keeps the last turns and starts on the person's words", () => {
    const entries = [];
    for (let i = 0; i < MAX_TURNS + 3; i++) {
        entries.push({ role: "user", text: `q${i}` }, { role: "assistant", text: `a${i}` });
    }
    entries.push({ role: "user", text: "now" });
    const sent = historyFor(entries);
    assert.ok(sent.length <= MAX_TURNS * 2);
    assert.equal(sent[0].role, "user");
    assert.equal(sent.at(-1).text, "now");
});

test("history trims a long answer and leaves out anything without words", () => {
    const sent = historyFor([
        { role: "user", text: "hi" },
        { role: "assistant", text: "x".repeat(5000) },
        { role: "user", text: "   " },
        { role: "user", text: "and" },
    ]);
    assert.equal(sent.length, 3);
    assert.equal(sent[1].text.length, 2000);
});

test("the log is capped to what is sent", () => {
    const entries = Array.from({ length: 50 }, (_, i) => ({ role: "user", text: `${i}` }));
    assert.equal(capEntries(entries).length, MAX_TURNS * 2);
    assert.equal(capEntries(entries).at(-1).text, "49");
});

test("screens come from the nav, once each", () => {
    assert.deepEqual(
        screensFor([
            { href: "/dash", label: "Home", icon: "x" },
            { href: "/dash/orders", label: "Orders" },
            { href: "/dash/orders", label: "Again" },
            { label: "no address" },
        ]),
        [
            { href: "/dash", label: "Home" },
            { href: "/dash/orders", label: "Orders" },
        ],
    );
});

test("only a path inside the panel becomes a link", () => {
    const good = { href: "/dash/settings", label: "Settings" };
    assert.deepEqual(
        safeLinks([
            good,
            { href: "https://evil.example", label: "a" },
            { href: "//evil.example/dash", label: "b" },
            { href: "javascript:alert(1)", label: "c" },
            { href: "/dash/../admin", label: "d" },
            { href: "/dash/x", label: " " },
            null,
        ]),
        [good],
    );
    assert.deepEqual(safeLinks(undefined), []);
});

test("a yes-or-no field reads in words and other values as they are", () => {
    assert.equal(valueText("prices_include_tax", "true"), "Yes");
    assert.equal(valueText("prices_include_tax", "false"), "No");
    assert.equal(valueText("name", "Marigold"), "Marigold");
    assert.equal(valueText("name", undefined), "");
});

test("Support is offered for what a person there can fix, not for a retry", () => {
    assert.ok(needsSupport({ code: "ai_credit_exhausted" }));
    assert.ok(needsSupport({ code: "ai_not_ready" }));
    assert.ok(!needsSupport({ code: "ai_provider_failed" }));
    assert.ok(!needsSupport(undefined));
});

test("a store that moved on redraws the card from the engine's preview", () => {
    const proposal = { id: 7, preview_hash: "old", changes: [{ current: "A" }], warnings: [] };
    const err = { status: 409, details: { preview_hash: "new", preview: { changes: [{ current: "B" }], warnings: ["w"] } } };
    assert.deepEqual(refreshedProposal(proposal, err), { id: 7, preview_hash: "new", changes: [{ current: "B" }], warnings: ["w"] });
    assert.equal(refreshedProposal(proposal, { status: 409 }), null);
    assert.equal(refreshedProposal(proposal, { status: 403, details: err.details }), null);
});
