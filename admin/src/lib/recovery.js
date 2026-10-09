/**
 * Abandoned-checkout recovery, in the panel's words.
 *
 * ext/cart-recovery decides everything that is a decision — whether a basket
 * can be written to, which one fact goes under a status, what each line of the
 * timeline says — and sends it as fixed words. This file only turns those
 * words into the screen's: a label for a status, a colour for its chip, a wait
 * in the unit a person would type, a window in the reader's own days. Nothing
 * here adds up money. The figures arrive summed by PostgreSQL, and formatMoney
 * is the only thing that ever touches one.
 *
 * Plain JavaScript with no runes and no `$lib` imports, so the arithmetic that
 * is easy to get subtly wrong — minutes into hours, a week that starts on a
 * Monday, a trend with gaps in it — is tested under node without a browser.
 */

import { pluralize } from "./format.js";

/* ------------------------------------------------------------ the records */

/** Every status a record can be in, in the order a basket moves through them. */
export const STATUSES = ["abandoned", "scheduled", "contacted", "recovered", "suppressed", "expired"];

const STATUS_LABELS = {
    abandoned: "Abandoned",
    scheduled: "Scheduled",
    contacted: "Contacted",
    recovered: "Recovered",
    suppressed: "Suppressed",
    expired: "Expired",
};

export function statusLabel(status) {
    return STATUS_LABELS[status] ?? status ?? "";
}

/**
 * The `.label` colour for a status, from PocketBase's four.
 *
 * Abandoned is a warning for the reason the Carts screen gives: nothing has
 * gone wrong, there is money on the table. Contacted is info because the store
 * has done its part and is waiting. Suppressed and expired are neutral — both
 * are finished, and neither is a failure anybody needs to act on.
 */
export function statusClass(status) {
    switch (status) {
        case "recovered":
            return "success";
        case "abandoned":
            return "warning";
        case "contacted":
            return "info";
        default:
            return "";
    }
}

/** Open: the sequence may still write, and an operator may still act. */
export function isOpen(status) {
    return status === "abandoned" || status === "scheduled" || status === "contacted";
}

const INDICATOR_LABELS = {
    purchased: "Purchased",
    clicked: "Link opened",
    email_sent: "Email sent",
    inventory_unavailable: "Nothing can be bought",
    send_failed: "Sending failed",
    no_email_provider: "No email provider",
    scheduled: "Reminder scheduled",
    no_contact: "No email address",
    automation_off: "Automation off",
    no_steps: "No steps to send",
    before_install: "Before recovery was set up",
    sequence_done: "Sequence finished",
};

/**
 * The one secondary fact under a status, as a phrase. An indicator this table
 * has not caught up with still says something — its own key, de-underscored —
 * because a word the module added must not vanish from the list.
 */
export function indicatorLabel(key) {
    if (!key) return "";
    return INDICATOR_LABELS[key] ?? key.replace(/_/g, " ");
}

/** Indicators that are a fault somebody can fix, drawn in the danger tone. */
export function indicatorTone(key) {
    if (key === "send_failed" || key === "no_email_provider") return "txt-danger";
    if (key === "purchased" || key === "clicked") return "txt-success";
    return "txt-hint";
}

export function kindLabel(kind) {
    return kind === "checkout" ? "Checkout" : kind === "cart" ? "Cart" : kind ?? "";
}

/**
 * The recovery rate, from basis points. An integer on the wire so no float is
 * near the money it describes; dividing it here is formatting, not arithmetic
 * on an amount.
 */
export function rateText(bp) {
    const n = Number.isFinite(bp) ? bp : 0;
    return new Intl.NumberFormat(undefined, {
        style: "percent",
        minimumFractionDigits: 1,
        maximumFractionDigits: 1,
    }).format(n / 10000);
}

/** A share of a whole as a percentage, for the funnel's step-to-step figures. */
export function shareText(part, whole) {
    if (!whole) return "—";
    return new Intl.NumberFormat(undefined, {
        style: "percent",
        maximumFractionDigits: 1,
    }).format(part / whole);
}

/* ------------------------------------------------------------ durations */

/** The units a wait is typed in. The API takes minutes and nothing else. */
export const UNITS = [
    { value: "minutes", label: "minutes", size: 1 },
    { value: "hours", label: "hours", size: 60 },
    { value: "days", label: "days", size: 1440 },
];

/**
 * splitMinutes picks the largest unit the number divides into exactly, so 60
 * comes back as `{1, hours}` and 90 stays `{90, minutes}` rather than becoming
 * a 1.5 nobody typed.
 */
export function splitMinutes(minutes) {
    const n = Math.max(0, Math.round(Number(minutes) || 0));
    if (n > 0 && n % 1440 === 0) return { value: n / 1440, unit: "days" };
    if (n > 0 && n % 60 === 0) return { value: n / 60, unit: "hours" };
    return { value: n, unit: "minutes" };
}

/**
 * joinMinutes is the other direction, and refuses rather than repairs: a blank
 * box, a fraction of a minute or a negative number is null, so the screen can
 * say what is wrong instead of sending something the operator did not type.
 */
export function joinMinutes(value, unit) {
    const raw = String(value ?? "").trim();
    if (!/^\d+$/.test(raw)) return null;
    const size = UNITS.find((u) => u.value === unit)?.size;
    if (!size) return null;
    return Number(raw) * size;
}

/** "1 hour", "2 days 21 hours", "45 minutes" — the two largest parts at most. */
export function durationWords(minutes) {
    const n = Math.max(0, Math.round(Number(minutes) || 0));
    if (n === 0) return "no time";
    const days = Math.floor(n / 1440);
    const hours = Math.floor((n % 1440) / 60);
    const mins = n % 60;
    const parts = [];
    if (days) parts.push(`${days} ${pluralize(days, "day")}`);
    if (hours) parts.push(`${hours} ${pluralize(hours, "hour")}`);
    if (mins) parts.push(`${mins} ${pluralize(mins, "minute")}`);
    return parts.slice(0, 2).join(" ");
}

/** "18h", "3d", "25m" — for a list cell, where "in 18 hours" is too wide. */
export function shortDuration(ms) {
    const minutes = Math.max(1, Math.ceil(ms / 60000));
    if (minutes < 60) return `${minutes}m`;
    if (minutes < 48 * 60) return `${Math.round(minutes / 60)}h`;
    return `${Math.round(minutes / 1440)}d`;
}

/**
 * What the Recovery column says about one record: what has gone out, and what
 * happens next. "No email" when there is nobody to write to, because that is
 * the whole answer for that row.
 */
export function recoveryProgress(row, now = Date.now()) {
    if (!row?.email) return { text: "No email", hint: "" };
    const sent = row.messages_sent ?? 0;
    let text;
    if (sent === 0) text = "Nothing sent";
    else if (sent === 1) text = row.steps_sent >= 1 ? `Email #${row.steps_sent} sent` : "1 email sent";
    else text = `${sent} emails`;

    let hint = "";
    if (isOpen(row.status) && row.next_step_at) {
        const ms = new Date(row.next_step_at).getTime() - now;
        hint = ms > 0 ? `Next in ${shortDuration(ms)}` : "Next is due";
    } else if ((row.clicks ?? 0) > 0) {
        hint = `${row.clicks} ${pluralize(row.clicks, "click")}`;
    }
    // Nothing has gone out yet but something is about to: that is the news.
    if (sent === 0 && hint.startsWith("Next")) return { text: hint, hint: "" };
    return { text, hint };
}

/**
 * One line for an automation's sequence. Each step's wait runs from the one
 * before it, so the last email lands at the sum of them.
 */
export function sequenceSummary(steps) {
    const list = steps ?? [];
    if (!list.length) return "No steps, so nothing is sent";
    const first = list[0].wait_minutes;
    const total = list.reduce((sum, s) => sum + (s.wait_minutes || 0), 0);
    if (list.length === 1) return `1 email, ${durationWords(first)} after the basket is abandoned`;
    return `${list.length} emails over ${durationWords(total)}, the first after ${durationWords(first)}`;
}

/* ------------------------------------------------------------ the timeline */

const TIMELINE_ICONS = {
    cart_created: "ri-shopping-basket-2-line",
    last_activity: "ri-cursor-line",
    abandoned: "ri-time-line",
    scheduled: "ri-calendar-schedule-line",
    sent: "ri-mail-send-line",
    send_failed: "ri-mail-close-line",
    skipped: "ri-skip-forward-line",
    link_copied: "ri-link",
    clicked: "ri-external-link-line",
    resumed: "ri-arrow-go-back-line",
    recovered: "ri-checkbox-circle-line",
    suppressed: "ri-forbid-line",
    expired: "ri-delete-bin-line",
};

/** The glyph for a timeline entry. The words are the module's, never ours. */
export function timelineIcon(kind) {
    return TIMELINE_ICONS[kind] ?? "ri-information-line";
}

/** Entries that went wrong are drawn in the danger tone; the outcome in success. */
export function timelineTone(kind) {
    if (kind === "send_failed") return "is-danger";
    if (kind === "recovered") return "is-success";
    return "";
}

/** Who did it. The module writes "system", "shopper", "token" or an email. */
export function actorWords(actor) {
    switch (actor) {
        case "":
        case undefined:
        case null:
            return "";
        case "system":
            return "automatically";
        case "shopper":
            return "by the shopper";
        case "token":
            return "by an admin token";
        default:
            return `by ${actor}`;
    }
}

/* ------------------------------------------------------------ windows */

/** A local calendar date as YYYY-MM-DD, never through UTC. */
export function civilDate(date) {
    const pad = (n) => String(n).padStart(2, "0");
    return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

function localMidnight(civil) {
    const [y, m, d] = civil.split("-").map(Number);
    return new Date(y, m - 1, d);
}

/** RFC 3339 without the milliseconds, which the engine accepts either way. */
function instant(date) {
    return date.toISOString().replace(/\.\d{3}Z$/, "Z");
}

/** The ranges both list and analytics offer, by key. */
export const RANGES = [
    { value: "all", label: "All time" },
    { value: "today", label: "Today" },
    { value: "7d", label: "Last 7 days" },
    { value: "30d", label: "Last 30 days" },
    { value: "90d", label: "Last 90 days" },
    { value: "12m", label: "Last 12 months" },
    { value: "custom", label: "Custom…" },
];

/**
 * rangeWindow turns a range key (and, for "custom", two civil dates) into the
 * instants the API filters on.
 *
 * Instants rather than bare dates, because the engine reads a bare date at UTC
 * midnight and the reader's day starts at their own. `to` is sent as the
 * midnight AFTER the last day wanted: the engine's `to` is a strict upper
 * bound on an instant, and "up to and including Tuesday" means "before
 * Wednesday began here".
 *
 * Returns `{ from, to, fromCivil, toCivil }`, the civil pair being the first
 * and last day covered, for a trend to fill its axis between.
 */
export function rangeWindow(range, fromCivil = "", toCivil = "", now = new Date()) {
    const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
    const dayAfter = (d) => new Date(d.getFullYear(), d.getMonth(), d.getDate() + 1);
    const daysBack = (n) => new Date(today.getFullYear(), today.getMonth(), today.getDate() - n);

    let start = null;
    let end = null;
    switch (range) {
        case "today":
            start = today;
            end = today;
            break;
        case "7d":
            start = daysBack(6);
            end = today;
            break;
        case "30d":
            start = daysBack(29);
            end = today;
            break;
        case "90d":
            start = daysBack(89);
            end = today;
            break;
        case "12m":
            start = new Date(today.getFullYear(), today.getMonth() - 11, 1);
            end = today;
            break;
        case "custom":
            start = /^\d{4}-\d{2}-\d{2}$/.test(fromCivil) ? localMidnight(fromCivil) : null;
            end = /^\d{4}-\d{2}-\d{2}$/.test(toCivil) ? localMidnight(toCivil) : null;
            break;
        default:
            break;
    }
    return {
        from: start ? instant(start) : "",
        to: end ? instant(dayAfter(end)) : "",
        fromCivil: start ? civilDate(start) : "",
        toCivil: end ? civilDate(end) : "",
    };
}

/* ------------------------------------------------------------ the trend */

function parseCivil(civil) {
    const [y, m, d] = String(civil).split("-").map(Number);
    return new Date(Date.UTC(y, (m || 1) - 1, d || 1));
}

function formatCivil(date) {
    const pad = (n) => String(n).padStart(2, "0");
    return `${date.getUTCFullYear()}-${pad(date.getUTCMonth() + 1)}-${pad(date.getUTCDate())}`;
}

/**
 * The start of the period a civil date falls in, as PostgreSQL's date_trunc
 * cuts it: weeks start on Monday (ISO), months on the first.
 */
export function periodStart(civil, groupBy) {
    const d = parseCivil(civil);
    if (groupBy === "month") d.setUTCDate(1);
    if (groupBy === "week") {
        const back = (d.getUTCDay() + 6) % 7;
        d.setUTCDate(d.getUTCDate() - back);
    }
    return formatCivil(d);
}

function nextPeriod(civil, groupBy) {
    const d = parseCivil(civil);
    if (groupBy === "month") d.setUTCMonth(d.getUTCMonth() + 1);
    else d.setUTCDate(d.getUTCDate() + (groupBy === "week" ? 7 : 1));
    return formatCivil(d);
}

/** Beyond this many bars a chart is a texture; the gaps are left as they are. */
const MAX_FILLED = 400;

/**
 * fillTrend puts the empty periods back.
 *
 * The engine groups the records it has, so a day nothing was abandoned on has
 * no row — and a row of bars with the quiet days missing reads as consecutive
 * days that were not. The filler is a zero period, never an invented figure:
 * every amount it carries is 0 in the store's own currency.
 *
 * `from` and `to` are the window's first and last civil days when it has them;
 * an open window runs from the first period to the last one present.
 */
export function fillTrend(trend, groupBy, currency, from = "", to = "") {
    const rows = trend ?? [];
    if (!rows.length && !(from && to)) return [];
    const byPeriod = new Map(rows.map((p) => [p.period, p]));
    const first = periodStart(from || rows[0].period, groupBy);
    const last = periodStart(to || rows[rows.length - 1].period, groupBy);
    const zero = { amount_minor: 0, currency };

    const out = [];
    for (let p = first; p <= last; p = nextPeriod(p, groupBy)) {
        if (out.length >= MAX_FILLED) return rows;
        out.push(
            byPeriod.get(p) ?? {
                period: p,
                abandoned: 0,
                potential: zero,
                recovered: 0,
                recovered_revenue: zero,
                empty: true,
            },
        );
    }
    // A period the engine returned that the window did not reach — a stale
    // tab after midnight — is still a fact and still drawn.
    for (const p of rows) if (!out.some((o) => o.period === p.period)) out.push(p);
    return out.sort((a, b) => (a.period < b.period ? -1 : a.period > b.period ? 1 : 0));
}

/** A period's label, read from the civil date the engine printed. */
export function periodLabel(period, groupBy, long = false) {
    const date = parseCivil(period);
    const options =
        groupBy === "month"
            ? { year: "numeric", month: long ? "long" : "short", timeZone: "UTC" }
            : { month: "short", day: "numeric", timeZone: "UTC", ...(long ? { year: "numeric" } : {}) };
    const text = new Intl.DateTimeFormat(undefined, options).format(date);
    return groupBy === "week" ? `Week of ${text}` : text;
}

/* ------------------------------------------------------------ the funnel */

/**
 * The funnel as rows a screen can draw: each stage's share of the first, which
 * is the bar's length, and of the stage before, which is the step-to-step
 * conversion the table states. Every stage is a subset of the one before, so
 * neither ratio can pass 1 — clamped all the same, so a figure counted a beat
 * apart cannot draw a bar longer than its track.
 */
export function funnelRows(funnel) {
    const list = funnel ?? [];
    const top = list[0]?.count ?? 0;
    return list.map((step, i) => {
        const prev = i === 0 ? step.count : list[i - 1].count;
        return {
            ...step,
            ofFirst: top ? Math.min(1, step.count / top) : 0,
            ofPrev: i === 0 ? null : prev ? Math.min(1, step.count / prev) : 0,
            prevCount: i === 0 ? null : prev,
        };
    });
}

/* ------------------------------------------------------------ the settings */

/**
 * Where a refused save belongs on the screen.
 *
 * The engine names the field in its sentence — "checkout step 2: wait_minutes
 * must be between 1 and 43200" — and this reads the name back out of it so the
 * message can sit under the box it is about. A sentence this does not
 * recognise is still shown, at the top of the form.
 */
export function settingsError(message) {
    const text = String(message ?? "");
    let m = text.match(/^(checkout|cart)\.abandon_after_minutes\b/);
    if (m) return { side: m[1], field: "threshold", message: text };
    m = text.match(/^(checkout|cart) step (\d+):/);
    if (m) return { side: m[1], field: "step", step: Number(m[2]) - 1, message: text };
    m = text.match(/^(checkout|cart):/);
    if (m) return { side: m[1], field: "steps", message: text };
    if (/^storefront_url\b/.test(text)) return { field: "storefront", message: text };
    return { field: "form", message: text };
}

/** Whether a typed storefront address would be accepted, in the engine's terms. */
export function validStorefront(value) {
    const raw = String(value ?? "").trim();
    if (!raw) return true;
    try {
        const u = new URL(raw);
        return (u.protocol === "http:" || u.protocol === "https:") && !!u.host;
    } catch {
        return false;
    }
}
