/**
 * The retail assistant's screens' wording and small decisions (D192), apart from
 * the screens so they can be tested. The ledger's own figures come from the
 * engine and are never worked out here.
 */

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

/** A figure that does not add up is the one thing a reviewer may wave through. */
export const isMismatch = (message) => message.includes("add up");

/** What else is on a supplier's account, beside what is owed. */
export function moneyNote(row, format) {
    const notes = [];
    if (row.advance?.amount_minor) notes.push(`${format(row.advance)} paid in advance`);
    if (row.unverified?.amount_minor) notes.push(`${format(row.unverified)} not verified yet`);
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

/** A name for one press of a money button, so a retry records one thing. */
export function newKey() {
    if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID();
    return `${Date.now()}-${Math.random().toString(36).slice(2)}`;
}
