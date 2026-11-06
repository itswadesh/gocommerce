/** What Ask AI's drawer decides without drawing anything (D233). */

/** Turns kept and sent: each costs tokens on every question after it. */
export const MAX_TURNS = 10;
/** What the box takes. The engine allows more because answers come back too. */
export const MAX_QUESTION = 1000;
const MAX_ANSWER = 2000;

/**
 * The conversation as the engine takes it: the last turns, the failed and the
 * pending left out. The first kept message is the person's, so the model never
 * starts on an answer to something it cannot see.
 */
export function historyFor(entries) {
    const kept = entries
        .filter((e) => e.role === "user" || e.role === "assistant")
        .map((e) => ({ role: e.role, text: e.text.slice(0, MAX_ANSWER) }))
        .filter((e) => e.text.trim());
    const recent = kept.slice(-MAX_TURNS * 2);
    while (recent.length && recent[0].role !== "user") recent.shift();
    return recent;
}

/** Older turns off the top, so the log and what is sent stay the same length. */
export function capEntries(entries) {
    return entries.slice(-MAX_TURNS * 2);
}

/** The screens the person can reach, from the nav, as the allowlist's half. */
export function screensFor(nav) {
    const seen = new Set();
    return nav
        .filter((item) => item.href && !seen.has(item.href) && seen.add(item.href))
        .map((item) => ({ href: item.href, label: item.label }));
}

/**
 * The engine has already checked links against its allowlist. This is the
 * second lock, on what becomes an href: only a path inside the panel, so a bug
 * there can never draw an address outside it.
 */
export function safeLinks(links) {
    return (links ?? []).filter(
        (l) => /^\/dash(\/[A-Za-z0-9_\-/]*)?$/.test(l?.href ?? "") && String(l?.label ?? "").trim(),
    );
}

/** A value on the card, in words; "" draws as Empty. */
export function valueText(field, value) {
    if (field === "prices_include_tax") return value === "true" ? "Yes" : "No";
    return value ?? "";
}

/** Whether a refusal is one the Support page can help with. */
export function needsSupport(err) {
    return ["ai_not_ready", "ai_credit_exhausted", "ai_budget_exhausted", "ai_policy_blocked"].includes(err?.code);
}

/**
 * A refused Apply whose store moved on carries the preview as it is now. The
 * card shows that and asks again, instead of leaving a stale card to press.
 */
export function refreshedProposal(proposal, err) {
    const d = err?.details;
    if (err?.status !== 409 || !d?.preview_hash || !d?.preview?.changes) return null;
    return { ...proposal, preview_hash: d.preview_hash, changes: d.preview.changes, warnings: d.preview.warnings ?? [] };
}
