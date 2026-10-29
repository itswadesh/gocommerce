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

export const keyOf = (change) => (change.field === "alt" ? `alt:${change.media_id}` : change.field);

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

function nowOf(change, form, media, categories) {
    switch (change.field) {
        case "description":
            return textOf(form.description);
        case "highlights":
            return highlightLines(form.highlights);
        case "tags":
            return [...(form.tags ?? [])];
        case "category":
            return (
                categories.find((c) => c.id === form.category_id)?.full_name ??
                (change.current?.id === form.category_id ? change.current.name : "")
            );
        case "alt":
            return form.unsavedAlts?.[change.media_id] ?? media.find((m) => m.id === change.media_id)?.alt ?? "";
        default:
            return form[change.field] ?? "";
    }
}

function shown(change) {
    if (change.field === "description") return textOf(change.proposed);
    if (change.field === "category") return change.proposed?.name ?? "";
    return change.proposed;
}

export function rowsFor(proposal, { form, media = [], categories = [], canWriteMedia = false }) {
    return (proposal?.changes ?? [])
        .filter((c) => c.field !== "alt" || (canWriteMedia && media.some((m) => m.id === c.media_id)))
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
                next.unsavedAlts = { ...(next.unsavedAlts ?? {}), [c.media_id]: c.proposed };
                break;
            default:
                next[c.field] = c.proposed;
        }
    }
    return next;
}

export function refusal(err) {
    const message = err?.message || "Something went wrong.";
    switch (err?.code) {
        case "ai_not_ready":
        case "ai_policy_blocked":
            return { fix: "settings", title: "The AI gateway needs setting up", message };
        case "ai_budget_exhausted":
            return { fix: "settings", title: "This month's AI budget is used up", message };
        case "ai_credit_exhausted":
            return { fix: "credit", title: "No AI credit left", message };
        case "ai_provider_failed":
            return { fix: "retry", title: "The model did not answer", message };
        case "ai_no_proposal":
            return { fix: "retry", title: "No usable proposal came back", message };
        default:
            return { fix: "retry", title: "Could not get a proposal", message };
    }
}
