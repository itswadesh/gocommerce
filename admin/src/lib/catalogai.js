/*
 * "Improve with AI" on the product editor (D152): the catalog-ai module's
 * proposal laid beside the form, and the chosen changes copied into it.
 *
 * Nothing here saves. Applying writes into the form the editor already has,
 * so the shop's own Save sends it through the product's update like any edit
 * it typed. "Now" is the form, not the saved product: it is what the shop
 * sees on the screen and what applying replaces.
 */
import { highlightLines, MAX_HIGHLIGHTS } from "./highlights.js";

export const FIELD_LABELS = {
    title: "Title",
    description: "Description",
    highlights: "Highlights",
    tags: "Tags",
    product_type: "Product type",
    category: "Category",
    seo_title: "Search engine title",
    seo_description: "Search engine description",
    alt: "Alt text",
};

/** A change's key: its field, or the picture for an alt text. */
export const keyOf = (change) => (change.field === "alt" ? `alt:${change.media_id}` : change.field);

/** The words of a description's HTML, a paragraph a line, for reading beside another. */
export function textOf(html) {
    return String(html ?? "")
        .replace(/<br\s*\/?>/gi, "\n")
        .replace(/<\/(p|div|li|h[1-6])>/gi, "\n\n")
        .replace(/<[^>]*>/g, "")
        .replace(/&nbsp;/g, " ")
        .replace(/&lt;/g, "<")
        .replace(/&gt;/g, ">")
        .replace(/&quot;/g, '"')
        .replace(/&#39;/g, "'")
        .replace(/&amp;/g, "&")
        .replace(/\n{3,}/g, "\n\n")
        .trim();
}

/** What the form holds now for a change's field, shown the way the proposal is. */
function nowOf(change, form, media, categories) {
    switch (change.field) {
        case "description":
            return textOf(form.description);
        case "highlights":
            return highlightLines(form.highlights);
        case "tags":
            return [...(form.tags ?? [])];
        case "category":
            // The editor's list stops at 500 categories; the proposal names the saved one itself.
            return (
                categories.find((c) => c.id === form.category_id)?.full_name ??
                (change.current?.id === form.category_id ? change.current.name : "")
            );
        case "alt":
            return form.alts?.[change.media_id] ?? media.find((m) => m.id === change.media_id)?.alt ?? "";
        default:
            return form[change.field] ?? "";
    }
}

function shown(change) {
    if (change.field === "description") return textOf(change.proposed);
    if (change.field === "category") return change.proposed?.name ?? "";
    return change.proposed;
}

/**
 * The rows the drawer draws, one a change. An alt text is left out without
 * media.write, which its Save needs, and for a picture no longer on the
 * product.
 */
export function rowsFor(proposal, { form, media = [], categories = [], canAlt = false }) {
    return (proposal?.changes ?? [])
        .filter((c) => c.field !== "alt" || (canAlt && media.some((m) => m.id === c.media_id)))
        .map((c) => ({
            key: keyOf(c),
            field: c.field,
            label: FIELD_LABELS[c.field] ?? c.field,
            image: c.field === "alt" ? (media.find((m) => m.id === c.media_id)?.url ?? "") : "",
            now: nowOf(c, form, media, categories),
            proposed: shown(c),
            reason: c.reason ?? "",
        }));
}

/**
 * The form with the chosen changes copied in: a new object, so the editor's
 * comparison with its snapshot sees what moved. Alt texts go into
 * `form.alts`, by picture, for Save to write.
 */
export function applied(form, changes, chosen) {
    const next = JSON.parse(JSON.stringify(form));
    for (const c of changes ?? []) {
        if (!chosen.has(keyOf(c))) continue;
        switch (c.field) {
            case "highlights":
                next.highlights = c.proposed.length < MAX_HIGHLIGHTS ? [...c.proposed, ""] : [...c.proposed];
                break;
            case "tags":
                next.tags = [...c.proposed];
                break;
            case "category":
                next.category_id = c.proposed.id;
                break;
            case "alt":
                next.alts = { ...(next.alts ?? {}), [c.media_id]: c.proposed };
                break;
            default:
                next[c.field] = c.proposed;
        }
    }
    return next;
}

/**
 * What a refused proposal shows, by the module's error code: the gateway's
 * settings for what only they can fix, and a retry for what may pass.
 */
export function refusal(err) {
    const message = err?.message || "Something went wrong.";
    switch (err?.code) {
        case "ai_not_ready":
        case "ai_policy_blocked":
            return { fix: "settings", title: "The AI gateway needs setting up", message };
        case "ai_budget_exhausted":
            return { fix: "settings", title: "This month's AI budget is used up", message };
        case "ai_provider_failed":
            return { fix: "retry", title: "The model did not answer", message };
        case "ai_no_proposal":
            return { fix: "retry", title: "No usable proposal came back", message };
        default:
            return { fix: "retry", title: "Could not get a proposal", message };
    }
}
