/**
 * The retail assistant's screens' wording and small decisions (D192), apart from
 * the screens so they can be tested. The ledger's own figures come from the
 * engine and are never worked out here.
 */

import { translate } from "./retail-hindi.js";

/** English, for a caller with no language to hand. */
const plain = (text, vars) => translate("en", text, vars);

export const STATUS = {
    received: { label: "Received", cls: "" },
    parsing: { label: "Reading", cls: "" },
    needs_review: { label: "Needs review", cls: "warning" },
    failed: { label: "Failed", cls: "danger" },
    posted: { label: "Posted", cls: "success" },
    rejected: { label: "Rejected", cls: "" },
};

export const statusOf = (status) => STATUS[status] ?? { label: status, cls: "" };

export const DOCUMENT_TYPES = [
    { value: "purchase_invoice", label: "Supplier bill" },
    { value: "credit_note", label: "Credit note" },
    { value: "payment_proof", label: "Payment proof" },
];

export const CREDIT_KINDS = [
    { value: "return", label: "Goods returned" },
    { value: "damaged", label: "Damaged goods" },
    { value: "other", label: "Other" },
];

/** What a model may be asked to read: spreadsheets are read exactly, without one. */
export const aiReadable = (mimeType) => ["application/pdf", "image/jpeg", "image/png", "image/webp"].includes(mimeType);

export const PRICING_RULES = [
    { value: "markup", label: "Markup on what I pay" },
    { value: "margin", label: "Gross margin on the price" },
];

/** The two rules are different prices, so the screen says what the chosen one does. */
export function ruleExample(mode, percent, t = plain) {
    if (!Number.isFinite(percent) || percent < 0) return "";
    if (mode === "margin" && percent >= 100) return t("A gross margin must be under 100%.");
    const price = mode === "margin" ? 100 / (1 - percent / 100) : 100 * (1 + percent / 100);
    return t("Something you pay ₹100 for sells at ₹{price}, before tax.", { price: price.toFixed(2) });
}

/** A figure that does not add up is the one thing a reviewer may wave through. */
export const isMismatch = (message) => message.includes("add up");

/** What else is on a supplier's account, beside what is owed. */
export function moneyNote(row, format, t = plain) {
    const notes = [];
    if (row.advance?.amount_minor) notes.push(t("{advance} paid in advance", { advance: format(row.advance) }));
    if (row.unverified?.amount_minor) notes.push(t("{amount} not verified yet", { amount: format(row.unverified) }));
    return notes.join(" · ");
}

/**
 * What to prefill for a line's quantity. Stock moves in whole units, so "2.5 kg"
 * is left for the person who can see the box rather than rounded for them.
 */
export function wholeQuantity(printed) {
    const m = /^\s*(\d+)(?:\.0+)?\s*$/.exec(printed ?? "");
    return m && Number(m[1]) > 0 ? m[1] : "";
}

/** A scan: the same item again is one more of it, and the newest stays on top. */
export function basketAdd(basket, item) {
    const found = basket.find((l) => l.variant_id === item.variant_id);
    if (!found) return [{ ...item, quantity: 1 }, ...basket];
    return [{ ...found, quantity: found.quantity + 1 }, ...basket.filter((l) => l !== found)];
}

/** A quantity set by hand. Down to nothing takes the line off. */
export function basketSet(basket, variantId, quantity) {
    if (quantity <= 0) return basket.filter((l) => l.variant_id !== variantId);
    return basket.map((l) => (l.variant_id === variantId ? { ...l, quantity } : l));
}

/** A name for one press of a money button, so a retry records one thing. */
export function newKey() {
    if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID();
    return `${Date.now()}-${Math.random().toString(36).slice(2)}`;
}
