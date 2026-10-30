/**
 * The ads report's pure parts (ext/ads, D168): the CSV a shop downloads, the
 * names a row is shown under, and what an AI refusal asks the shop to do.
 */

import { fromMinor } from "./format.js";

export const NOT_RECORDED = "(not recorded)";

/** A row's name: "google / cpc · Diwali", with the empty parts said plainly. */
export function rowLabel(row) {
    const where = row.medium ? `${row.source} / ${row.medium}` : row.source;
    return { where, campaign: row.campaign || "(no campaign)" };
}

/** A ratio from the engine, or a dash where no spend was entered. */
export function ratioText(r) {
    return r === null || r === undefined ? "—" : `${r.toFixed(2)}×`;
}

const COLUMNS = [
    ["Currency", (r, c) => c],
    ["Source", (r) => r.source],
    ["Medium", (r) => r.medium],
    ["Campaign", (r) => r.campaign],
    ["Orders", (r) => r.orders],
    ["Revenue", (r) => money(r.revenue)],
    ["Average order", (r) => money(r.average_order)],
    ["Refunded", (r) => money(r.refunded)],
    ["Net", (r) => money(r.net)],
    ["Orders returned", (r) => r.returns],
    ["Product cost", (r) => money(r.product_cost)],
    ["Gross profit", (r) => money(r.gross_profit)],
    ["Units without cost", (r) => r.uncosted_units],
    ["New customers", (r) => r.new_customers],
    ["Returning customers", (r) => r.returning_customers],
    ["Spend", (r) => money(r.spend)],
    ["ROAS", (r) => (r.roas ?? "")],
    ["Profit per spend", (r) => (r.poas ?? "")],
    ["Cost per order", (r) => money(r.cost_per_order)],
];

/* A decimal a spreadsheet adds up, not a formatted amount: the currency is
   its own column. */
const money = (m) => (m ? fromMinor(m.amount_minor, m.currency) : "");

/*
 * A cell a spreadsheet would run as a formula is the shopper's to write (a
 * campaign name comes from a link anybody can make), so it is opened with an
 * apostrophe, which spreadsheets read as "this is text".
 */
function cell(v) {
    let s = String(v ?? "");
    if (/^[=+\-@\t\r]/.test(s) && !/^-?\d+(\.\d+)?$/.test(s)) s = "'" + s;
    return /[",\n\r]/.test(s) ? `"${s.replaceAll('"', '""')}"` : s;
}

/** The whole report as CSV, every currency's rows and then its totals. */
export function reportCSV(report) {
    const lines = [COLUMNS.map(([h]) => h).join(",")];
    for (const block of report?.currencies ?? []) {
        for (const row of block.rows) lines.push(COLUMNS.map(([, f]) => cell(f(row, block.currency))).join(","));
        const totals = { ...block.totals, source: "Total", medium: "", campaign: "" };
        lines.push(COLUMNS.map(([, f]) => cell(f(totals, block.currency))).join(","));
    }
    return lines.join("\r\n") + "\r\n";
}

/** What an AI refusal says, and what the shop can do about it. */
export function summaryProblem(err) {
    const text = err?.message || "Try again in a moment.";
    switch (err?.code) {
        case "ai_not_ready":
            return { title: "Set up the AI gateway first", text, action: "settings" };
        case "ai_policy_blocked":
            return {
                title: "Order figures stay in this store",
                text: "The summary sends the report's figures to the AI provider, never a shopper's name or email. Switch on Share order lines in the AI gateway's settings to allow it.",
                action: "settings",
            };
        case "ai_budget_exhausted":
            return { title: "This month's AI cap is used up", text, action: "settings" };
        case "ai_credit_exhausted":
            return { title: "Out of AI credit", text, action: "credit" };
        case "ai_no_summary":
            return {
                title: "No summary kept to the figures",
                text: "The model's answer used a number that is not in the report, so it was not shown. Try again; the attempt is listed under AI usage.",
                action: "retry",
            };
    }
    return { title: "The summary could not be made", text, action: "retry" };
}

/** Reads a downloaded report as text. Google's "Excel .csv" is UTF-16. */
export function decodeExport(bytes) {
    const b = new Uint8Array(bytes);
    if (b[0] === 0xff && b[1] === 0xfe) return new TextDecoder("utf-16le").decode(b);
    if (b[0] === 0xfe && b[1] === 0xff) return new TextDecoder("utf-16be").decode(b);
    return new TextDecoder("utf-8").decode(b);
}
