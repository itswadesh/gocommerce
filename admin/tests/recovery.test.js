/*
 * The recovery screens' arithmetic. None of it touches an amount of money —
 * the module sends every figure summed — but a wait read as hours when it was
 * minutes is a reminder sent sixty times too early, and a week that starts on
 * the wrong day is a chart whose bars disagree with the table under it.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import {
    actorMeta,
    customerLines,
    durationWords,
    fillTrend,
    funnelRows,
    holdSentence,
    indicatorLabel,
    joinMinutes,
    periodStart,
    rangeLabel,
    rangeWindow,
    recoveryProgress,
    sequenceSummary,
    settingsError,
    splitMinutes,
    statusClass,
    timelineByDay,
    validStorefront,
    valueLabel,
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
    const past = new Date(now - 60 * 1000).toISOString();
    assert.deepEqual(recoveryProgress({ email: "" }, now), { text: "No contact", hint: "", muted: true });
    assert.deepEqual(
        recoveryProgress({ email: "a@b.c", status: "scheduled", messages_sent: 0, next_step_at: soon }, now),
        { text: "Next in 18h", hint: "", muted: false },
    );
    assert.deepEqual(
        recoveryProgress({ email: "a@b.c", status: "scheduled", messages_sent: 0, next_step_at: past }, now).text,
        "Next due now",
    );
    assert.deepEqual(
        recoveryProgress({ email: "a@b.c", status: "contacted", messages_sent: 1, steps_sent: 1, next_step_at: soon }, now),
        { text: "Email sent", hint: "Next in 18h", muted: false },
    );
    // Clicks and the next step both say something, so both are said.
    assert.equal(
        recoveryProgress({ email: "a@b.c", status: "contacted", messages_sent: 2, clicks: 1, next_step_at: soon }, now).hint,
        "Clicked · Next in 18h",
    );
    assert.equal(recoveryProgress({ email: "a@b.c", status: "contacted", messages_sent: 2, clicks: 3 }, now).text, "2 emails");
    assert.equal(recoveryProgress({ email: "a@b.c", status: "contacted", messages_sent: 2, clicks: 3 }, now).hint, "3 clicks");
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

test("statuses take kitcommerce-admin's colours", () => {
    assert.equal(statusClass("abandoned"), "warning");
    assert.equal(statusClass("scheduled"), "info");
    assert.equal(statusClass("contacted"), "info");
    assert.equal(statusClass("recovered"), "success");
    assert.equal(statusClass("suppressed"), "danger");
    assert.equal(statusClass("expired"), "");
});

test("indicators and holds are worded as kitcommerce-admin words them", () => {
    assert.equal(indicatorLabel("no_contact"), "No contact details");
    assert.equal(indicatorLabel("clicked"), "Link clicked");
    assert.equal(indicatorLabel("inventory_unavailable"), "Items unavailable");
    assert.equal(indicatorLabel("send_failed"), "Send failed");
    assert.equal(indicatorLabel("scheduled"), "Recovery scheduled");
    assert.equal(holdSentence("no_contact"), "There is no email address to write to.");
    assert.equal(holdSentence("nothing_purchasable"), "None of the items can be bought right now.");
    assert.equal(holdSentence("sequence_done"), "Every step of the recovery sequence has run.");
    assert.equal(holdSentence(""), "");
});

test("a customer reads as a name over how to reach them, or as a guest", () => {
    assert.deepEqual(customerLines({ name: "Ann Lee", email: "ann@x.io", phone: "+1 555" }), {
        top: "Ann Lee",
        hint: "ann@x.io",
        guest: false,
    });
    assert.deepEqual(customerLines({ name: "Ann Lee", phone: "+1 555" }), { top: "Ann Lee", hint: "+1 555", guest: false });
    assert.deepEqual(customerLines({ email: "ann@x.io", phone: "+1 555" }), { top: "ann@x.io", hint: "+1 555", guest: false });
    assert.deepEqual(customerLines({ email: "ann@x.io" }), { top: "ann@x.io", hint: "", guest: false });
    assert.deepEqual(customerLines({}), { top: "Guest", hint: "No contact details", guest: true });
});

test("who did it is one word, or their name", () => {
    assert.equal(actorMeta("system"), "Automatic");
    assert.equal(actorMeta("shopper"), "Customer");
    assert.equal(actorMeta("token"), "Admin token");
    assert.equal(actorMeta("ops@shop.io"), "ops@shop.io");
    assert.equal(actorMeta(""), "");
});

test("the timeline is newest first, under the day it happened", () => {
    const now = new Date(2026, 9, 9, 15, 0);
    const at = (d, h) => new Date(2026, 9, d, h, 0).toISOString();
    const days = timelineByDay(
        [
            { at: at(7, 9), kind: "cart_created" },
            { at: at(9, 14), kind: "sent" },
            { at: at(8, 10), kind: "abandoned" },
            { at: at(9, 11), kind: "scheduled" },
        ],
        now,
    );
    assert.deepEqual(
        days.map((d) => d.label),
        ["Today", "Yesterday", new Intl.DateTimeFormat(undefined, { weekday: "short", day: "numeric", month: "short", year: "numeric" }).format(new Date(2026, 9, 7))],
    );
    assert.deepEqual(
        days[0].entries.map((e) => e.kind),
        ["sent", "scheduled"],
    );
});

test("the value and date filters say what they hold", () => {
    assert.equal(valueLabel("", ""), "Any value");
    assert.equal(valueLabel("$10.00", ""), "From $10.00");
    assert.equal(valueLabel("", "$50.00"), "Up to $50.00");
    assert.equal(valueLabel("$10.00", "$50.00"), "$10.00 – $50.00");
    assert.equal(rangeLabel("all"), "Any date");
    assert.equal(rangeLabel("30d"), "Last 30 days");
    const custom = rangeLabel("custom", "2026-10-01", "2026-10-09");
    assert.ok(custom.includes("–") && custom.includes("2026"), custom);
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
