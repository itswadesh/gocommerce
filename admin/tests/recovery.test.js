/*
 * The recovery screens' arithmetic. None of it touches an amount of money —
 * the module sends every figure summed — but a wait read as hours when it was
 * minutes is a reminder sent sixty times too early, and a week that starts on
 * the wrong day is a chart whose bars disagree with the table under it.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import {
    durationWords,
    fillTrend,
    funnelRows,
    joinMinutes,
    periodStart,
    rangeWindow,
    recoveryProgress,
    sequenceSummary,
    settingsError,
    splitMinutes,
    statusClass,
    validStorefront,
} from "../src/lib/recovery.js";

test("a wait splits into the largest unit it divides into exactly", () => {
    assert.deepEqual(splitMinutes(60), { value: 1, unit: "hours" });
    assert.deepEqual(splitMinutes(2880), { value: 2, unit: "days" });
    assert.deepEqual(splitMinutes(1200), { value: 20, unit: "hours" });
    // 90 minutes is not an hour and a half nobody typed.
    assert.deepEqual(splitMinutes(90), { value: 90, unit: "minutes" });
    assert.deepEqual(splitMinutes(10), { value: 10, unit: "minutes" });
});

test("joining refuses what was not a whole number rather than repairing it", () => {
    assert.equal(joinMinutes(2, "hours"), 120);
    assert.equal(joinMinutes("3", "days"), 4320);
    assert.equal(joinMinutes("", "hours"), null);
    assert.equal(joinMinutes(null, "hours"), null);
    assert.equal(joinMinutes("1.5", "hours"), null);
    assert.equal(joinMinutes("-2", "hours"), null);
    assert.equal(joinMinutes("2", "weeks"), null);
    // The round trip is exact for every value the API can hold.
    for (const m of [1, 59, 60, 61, 1439, 1440, 43200]) {
        const { value, unit } = splitMinutes(m);
        assert.equal(joinMinutes(value, unit), m);
    }
});

test("durations read as a person would say them", () => {
    assert.equal(durationWords(60), "1 hour");
    assert.equal(durationWords(1260), "21 hours");
    assert.equal(durationWords(4140), "2 days 21 hours");
    assert.equal(durationWords(45), "45 minutes");
    assert.equal(durationWords(1441), "1 day 1 minute");
});

test("a sequence's summary counts each wait from the step before", () => {
    const steps = [{ wait_minutes: 60 }, { wait_minutes: 1200 }, { wait_minutes: 2880 }];
    assert.equal(sequenceSummary(steps), "3 emails over 2 days 21 hours, the first after 1 hour");
    assert.equal(sequenceSummary([]), "No steps, so nothing is sent");
});

test("the recovery column says what went out and what comes next", () => {
    const now = Date.parse("2026-10-09T10:00:00Z");
    const soon = new Date(now + 18 * 3600 * 1000).toISOString();
    assert.deepEqual(recoveryProgress({ email: "" }, now), { text: "No email", hint: "" });
    assert.deepEqual(
        recoveryProgress({ email: "a@b.c", status: "scheduled", messages_sent: 0, next_step_at: soon }, now),
        { text: "Next in 18h", hint: "" },
    );
    assert.deepEqual(
        recoveryProgress({ email: "a@b.c", status: "contacted", messages_sent: 1, steps_sent: 1, next_step_at: soon }, now),
        { text: "Email #1 sent", hint: "Next in 18h" },
    );
    assert.equal(recoveryProgress({ email: "a@b.c", status: "contacted", messages_sent: 2, steps_sent: 2 }, now).text, "2 emails");
    // A next step on a finished record is not news: it will never send.
    assert.equal(
        recoveryProgress({ email: "a@b.c", status: "recovered", messages_sent: 1, steps_sent: 1, next_step_at: soon }, now).hint,
        "",
    );
});

test("only PocketBase's four label colours come back", () => {
    for (const s of ["abandoned", "scheduled", "contacted", "recovered", "suppressed", "expired", "new"]) {
        assert.ok(["", "info", "success", "warning", "danger"].includes(statusClass(s)), s);
    }
});

test("a window is the reader's own days, with `to` the midnight after the last", () => {
    const now = new Date(2026, 9, 9, 15, 30); // 9 October 2026, local
    const week = rangeWindow("7d", "", "", now);
    assert.equal(week.fromCivil, "2026-10-03");
    assert.equal(week.toCivil, "2026-10-09");
    assert.equal(new Date(week.from).getTime(), new Date(2026, 9, 3).getTime());
    assert.equal(new Date(week.to).getTime(), new Date(2026, 9, 10).getTime());

    const custom = rangeWindow("custom", "2026-09-01", "2026-09-30", now);
    assert.equal(new Date(custom.to).getTime(), new Date(2026, 9, 1).getTime());

    assert.deepEqual(rangeWindow("all", "", "", now), { from: "", to: "", fromCivil: "", toCivil: "" });
    // A half-typed custom range is open at the missing end rather than an error.
    assert.equal(rangeWindow("custom", "2026-09-01", "", now).to, "");
});

test("weeks start on Monday, as date_trunc cuts them", () => {
    assert.equal(periodStart("2026-10-09", "week"), "2026-10-05"); // a Friday
    assert.equal(periodStart("2026-10-05", "week"), "2026-10-05"); // the Monday itself
    assert.equal(periodStart("2026-10-11", "week"), "2026-10-05"); // the Sunday after
    assert.equal(periodStart("2026-10-31", "month"), "2026-10-01");
});

test("the trend gets its quiet periods back as zeroes, never as invented figures", () => {
    const money = (n) => ({ amount_minor: n, currency: "INR" });
    const trend = [
        { period: "2026-10-01", abandoned: 2, potential: money(500), recovered: 1, recovered_revenue: money(250) },
        { period: "2026-10-04", abandoned: 1, potential: money(100), recovered: 0, recovered_revenue: money(0) },
    ];
    const filled = fillTrend(trend, "day", "INR", "2026-09-30", "2026-10-05");
    assert.deepEqual(
        filled.map((p) => p.period),
        ["2026-09-30", "2026-10-01", "2026-10-02", "2026-10-03", "2026-10-04", "2026-10-05"],
    );
    assert.equal(filled[0].abandoned, 0);
    assert.deepEqual(filled[0].potential, money(0));
    assert.equal(filled[1], trend[0]);

    const months = fillTrend(
        [{ period: "2026-08-01", abandoned: 1, potential: money(1), recovered: 0, recovered_revenue: money(0) }],
        "month",
        "INR",
        "2026-07-15",
        "2026-10-09",
    );
    assert.deepEqual(
        months.map((p) => p.period),
        ["2026-07-01", "2026-08-01", "2026-09-01", "2026-10-01"],
    );
    assert.deepEqual(fillTrend([], "day", "INR"), []);
});

test("the funnel's ratios are of the first stage and of the one before", () => {
    const rows = funnelRows([
        { key: "abandoned", count: 10 },
        { key: "recoverable", count: 5 },
        { key: "contacted", count: 4 },
        { key: "clicked", count: 0 },
        { key: "recovered", count: 0 },
    ]);
    assert.equal(rows[0].ofFirst, 1);
    assert.equal(rows[0].ofPrev, null);
    assert.equal(rows[1].ofFirst, 0.5);
    assert.equal(rows[2].ofPrev, 0.8);
    // Nothing clicked: the next stage's share of it is zero, not a division by it.
    assert.equal(rows[4].ofPrev, 0);
    assert.deepEqual(funnelRows([]), []);
});

test("a refused save is put under the box the engine named", () => {
    assert.deepEqual(settingsError("checkout step 2: wait_minutes must be between 1 and 43200"), {
        side: "checkout",
        field: "step",
        step: 1,
        message: "checkout step 2: wait_minutes must be between 1 and 43200",
    });
    assert.equal(settingsError("cart.abandon_after_minutes must be between 1 and 10080").field, "threshold");
    assert.equal(settingsError("cart: at most 5 steps").field, "steps");
    assert.equal(settingsError("storefront_url must be an absolute base URL").field, "storefront");
    assert.equal(settingsError("something else entirely").field, "form");
});

test("a storefront address is absolute http(s) or nothing", () => {
    assert.ok(validStorefront(""));
    assert.ok(validStorefront("https://shop.example.com"));
    assert.ok(!validStorefront("shop.example.com"));
    assert.ok(!validStorefront("ftp://shop.example.com"));
});
