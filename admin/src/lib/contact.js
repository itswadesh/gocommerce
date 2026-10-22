/**
 * What the Contact screen says about a message: its kind, the product an
 * enquiry was about, who answered it, and what a reply is called.
 *
 * Pure, so the rules are tested without a browser. The engine names a reply
 * the same way when the operator leaves the subject empty (replySubject in
 * ext/contact); the panel fills it in up front so the operator sees, and can
 * change, what the shopper will read.
 */

/** The kind filter. "all" is the screen's own word for asking for no kind. */
export const KIND_OPTIONS = [
    { value: "all", label: "All messages" },
    { value: "message", label: "Contact form" },
    { value: "enquiry", label: "Product enquiries" },
];

/** kindParam is the `kind` the engine is asked for: nothing, for all. */
export function kindParam(kind) {
    return kind === "message" || kind === "enquiry" ? kind : "";
}

/** replySubject is "Re: " and the conversation's own subject, so the reply threads. */
export function replySubject(msg) {
    const about = msg?.subject || msg?.product?.title || "";
    if (!about) return "Re: your message";
    return /^re:/i.test(about) ? about : "Re: " + about;
}

/** productFacts are the details beside an enquiry's product: which one, and how many. */
export function productFacts(product) {
    if (!product) return [];
    const facts = [];
    if (product.variant_title) facts.push(product.variant_title);
    if (product.quantity) facts.push(`${product.quantity} wanted`);
    return facts;
}

/** sentByLabel names who replied; a script or a key is said to be one. */
export function sentByLabel(sentBy) {
    if (!sentBy) return "someone";
    if (sentBy === "token") return "the admin token";
    const key = /^api key: (.+)$/.exec(sentBy);
    if (key) return `API key “${key[1]}”`;
    return sentBy;
}

/** mailtoHref opens the operator's own mail app, for a reply they would rather send from there. */
export function mailtoHref(msg) {
    return `mailto:${msg.email}?subject=${encodeURIComponent(replySubject(msg))}`;
}
