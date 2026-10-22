/**
 * What the Contact screen says about a message: who answered it, and what a
 * reply is called.
 *
 * Pure, so the rules are tested without a browser. The engine names a reply
 * the same way when the operator leaves the subject empty (replySubject in
 * ext/contact); the panel fills it in up front so the operator sees, and can
 * change, what the shopper will read.
 */

/** replySubject is "Re: " and the conversation's own subject, so the reply threads. */
export function replySubject(msg) {
    const about = msg?.subject || "";
    if (!about) return "Re: your message";
    return /^re:/i.test(about) ? about : "Re: " + about;
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
