/**
 * Words and numbers for the AI gateway's three screens.
 *
 * The gateway reports cost in millionths of a minor unit (`*_micro_minor`):
 * tokens times a price per million tokens, which is exact and never rounded on
 * the server. A single run usually costs a fraction of a cent, so formatting
 * it like a price — whole cents — would print $0.00 for nearly every row.
 * Under one major unit the figure keeps two significant digits instead, so a
 * column of runs reads $0.014 above $0.0022 rather than $0.01 above $0.0022.
 */
import { minorDigits } from "./format.js";

/** formatMicro renders a micro-minor amount in the currency. */
export function formatMicro(micro, currency = "USD") {
    if (micro === undefined || micro === null) return "—";
    const digits = minorDigits(currency);
    const major = micro / 1e6 / 10 ** digits;
    const options = { style: "currency", currency };
    if (major !== 0 && Math.abs(major) < 1) {
        options.maximumSignificantDigits = 2;
    } else {
        options.minimumFractionDigits = digits;
        options.maximumFractionDigits = digits;
    }
    try {
        return new Intl.NumberFormat(undefined, options).format(major);
    } catch {
        return `${major} ${currency}`;
    }
}

/** formatTokens is a token count, grouped: 12,480. */
export function formatTokens(n) {
    return new Intl.NumberFormat().format(n ?? 0);
}

/* A run's outcome, in words and in PocketBase's five label colours. Green is
   kept for an answer; a refusal the owner configured (the cap, a missing
   price) is a warning, not a failure, because the gateway did what it was
   told. */
const OUTCOMES = {
    ok: ["Answered", "label-success"],
    deterministic: ["Answered by code", "label-success"],
    cached: ["From the cache", "label-success"],
    invalid_output: ["Answer refused", "label-danger"],
    refused: ["Refused", "label-danger"],
    provider_error: ["Provider failed", "label-danger"],
    budget_exhausted: ["Over the cap", "label-warning"],
    no_price: ["No price set", "label-warning"],
    task_error: ["Task failed", "label-danger"],
};

export const OUTCOME_OPTIONS = [
    { value: "", label: "Every outcome" },
    ...Object.entries(OUTCOMES).map(([value, [label]]) => ({ value, label })),
];

export const outcomeLabel = (o) => OUTCOMES[o]?.[0] ?? o;
export const outcomeClass = (o) => OUTCOMES[o]?.[1] ?? "";

/* One step of a run: the code, the cache, or a model. */
const STEP_OUTCOMES = {
    hit: "Answered",
    miss: "No answer here",
    ok: "Answered",
    invalid: "Answer refused",
    low_confidence: "Not sure enough",
    refused: "Refused",
    provider_error: "Provider failed",
    budget_exhausted: "Over the cap",
    no_price: "No price set",
    error: "Failed",
};
export const stepOutcomeLabel = (o) => STEP_OUTCOMES[o] ?? o;
export const stepOk = (o) => o === "ok" || o === "hit";

const STEP_NAMES = {
    deterministic: "The task's own code",
    cache: "Earlier answers",
    small: "Small model",
    strong: "Strong model",
    task: "The task's own code",
};
export const stepName = (s) => STEP_NAMES[s] ?? s;

const JOB_STATUSES = {
    queued: ["Waiting", "label-info"],
    running: ["Running", "label-info"],
    done: ["Done", "label-success"],
    failed: ["Needs a person", "label-danger"],
    dead: ["Gave up", "label-danger"],
    cancelled: ["Cancelled", ""],
};
export const JOB_OPTIONS = [
    { value: "", label: "Every job" },
    ...Object.entries(JOB_STATUSES).map(([value, [label]]) => ({ value, label })),
];
export const jobLabel = (s) => JOB_STATUSES[s]?.[0] ?? s;
export const jobClass = (s) => JOB_STATUSES[s]?.[1] ?? "";
export const canRetry = (s) => s === "failed" || s === "dead" || s === "cancelled";
export const canCancel = (s) => s === "queued" || s === "running" || s === "failed" || s === "dead";

const APPROVAL_STATUSES = {
    pending: ["Waiting", "label-warning"],
    approved: ["Running", "label-info"],
    executed: ["Done", "label-success"],
    failed: ["Failed", "label-danger"],
    rejected: ["Rejected", ""],
    expired: ["Expired", ""],
};
export const APPROVAL_OPTIONS = [
    { value: "pending", label: "Waiting for a person" },
    { value: "executed", label: "Done" },
    { value: "failed", label: "Failed" },
    { value: "rejected", label: "Rejected" },
    { value: "expired", label: "Expired" },
    { value: "", label: "Every proposal" },
];
export const approvalLabel = (s) => APPROVAL_STATUSES[s]?.[0] ?? s;
export const approvalClass = (s) => APPROVAL_STATUSES[s]?.[1] ?? "";

/** The classes of data a store may let leave, as the settings call them. */
export const POLICY_CLASSES = [
    ["catalogue", "Catalogue"],
    ["order_lines", "Order lines"],
    ["contact_details", "Contact details"],
    ["message_bodies", "Message bodies"],
    ["documents", "Documents"],
];

/**
 * A preview or result as rows to read: an object's own keys, with nested
 * values written out as JSON. Previews are the action's own shape, so the
 * screen cannot know more about them than their keys.
 */
export function entries(value) {
    if (value === null || value === undefined) return [];
    if (typeof value !== "object" || Array.isArray(value)) return [["value", show(value)]];
    return Object.entries(value).map(([k, v]) => [k.replaceAll("_", " "), show(v)]);
}

function show(v) {
    if (v === null || v === undefined) return "—";
    if (typeof v === "object") return JSON.stringify(v);
    return String(v);
}

/** A one-line summary of a preview, for the queue's table. */
export function summary(value) {
    if (value && typeof value === "object" && !Array.isArray(value) && "from" in value && "to" in value) {
        return `${show(value.from)} → ${show(value.to)}`;
    }
    return entries(value)
        .map(([k, v]) => `${k}: ${v}`)
        .join(" · ");
}
