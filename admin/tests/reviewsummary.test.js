/*
 * The Reviews screen's summary drawer: what it says for each answer the
 * engine gives, and for each refusal.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import { summaryProblem, ratingLine, readLine, costLine, citedReviews, topicLabel, themeCount } from "../src/lib/reviewsummary.js";

const digest = (over = {}) => ({
    state: "summarized",
    rating: { count: 37, average: 4.24 },
    read: 37,
    outcome: "ok",
    cost_micro_minor: 326_700,
    currency: "USD",
    ...over,
});

test("each refusal says what to do, and only a failure offers to ask again", () => {
    assert.deepEqual(
        [
            "ai_unavailable",
            "ai_not_ready",
            "ai_data_kept_in",
            "ai_budget_exhausted",
            "ai_credit_exhausted",
            "ai_failed",
            "network_error",
        ].map((code) => [code, summaryProblem({ code, message: "the AI gateway is switched off for this store" }).action]),
        [
            ["ai_unavailable", null],
            ["ai_not_ready", "settings"],
            ["ai_data_kept_in", "settings"],
            ["ai_budget_exhausted", "settings"],
            ["ai_credit_exhausted", "credit"],
            ["ai_failed", "retry"],
            ["network_error", "retry"],
        ],
    );
    assert.equal(
        summaryProblem({ code: "ai_not_ready", message: "the AI gateway is switched off for this store: switch it on under Settings › Plugins" }).text,
        "The AI gateway is switched off for this store: switch it on under Settings › Plugins.",
    );
    assert.match(summaryProblem({ code: "ai_data_kept_in" }).text, /Share message bodies/);
    assert.equal(
        summaryProblem({ code: "ai_credit_exhausted", message: "You've used your free AI trial — top up to keep using AI" }).text,
        "You've used your free AI trial — top up to keep using AI.",
    );
});

test("the rating is every published review, to one decimal", () => {
    assert.equal(ratingLine(digest()), "4.2 out of 5 from 37 reviews");
    assert.equal(ratingLine(digest({ rating: { count: 1, average: 5 } })), "5.0 out of 5 from 1 review");
    assert.equal(ratingLine(digest({ rating: { count: 0, average: 0 } })), "No published reviews");
});

test("a summary that left reviews out says how many it read", () => {
    assert.equal(readLine(digest({ read: 50, rating: { count: 130, average: 4 } })), "Read the newest 50 of 130 reviews.");
    assert.equal(readLine(digest()), "");
    assert.equal(
        readLine(digest({ state: "too_few", read: 2, rating: { count: 2, average: 4 } })),
        "A summary needs at least 3 published reviews, and this product has 2. Nothing was sent to the AI provider.",
    );
});

test("a cached answer says it cost nothing, and too few says nothing about cost", () => {
    assert.equal(costLine(digest()), "This summary cost $0.0033.");
    assert.equal(costLine(digest({ outcome: "cached", cost_micro_minor: 0 })), "From an earlier summary of the same reviews, at no cost.");
    assert.equal(costLine(digest({ outcome: "deterministic", state: "too_few" })), "");
});

test("a theme lists the reviews it cites, in its own order, and skips one it cannot find", () => {
    const list = [
        { id: 9, body: "newest" },
        { id: 4, body: "older" },
        { id: 1, body: "oldest" },
    ];
    assert.deepEqual(
        citedReviews({ review_ids: [1, 9, 77] }, list).map((r) => r.body),
        ["oldest", "newest"],
    );
    assert.equal(themeCount({ count: 1 }), "1 review");
    assert.equal(themeCount({ count: 5 }), "5 reviews");
    assert.equal(topicLabel("fit"), "Fit and size");
    assert.equal(topicLabel("something new"), "Other");
});
