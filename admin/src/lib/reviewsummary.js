/**
 * Review summaries: the words around what the engine sends back.
 *
 * The engine answers POST /api/admin/x/reviews/products/{id}/summarize with
 * the themes, their counts and the rating, or refuses with a code saying what
 * stands in the way. Everything here is a plain function of that answer, so
 * node can test the wording without a browser.
 */
import { formatMicro } from "./ai.js";
import { pluralize, sentence } from "./format.js";

export const TOPICS = {
    fit: "Fit and size",
    quality: "Quality",
    colour: "Colour and look",
    delivery: "Delivery",
    value: "Value",
    other: "Other",
};

export const topicLabel = (topic) => TOPICS[topic] ?? TOPICS.other;

const reviews = (n) => `${n} ${pluralize(n, "review")}`;

/**
 * What the drawer shows when no summary was made. `action` is what the shop
 * can do about it: open the gateway's settings, open the AI credit, or ask
 * again.
 */
export function summaryProblem(err) {
    switch (err?.code) {
        case "ai_unavailable":
            return { title: "Summaries need the AI gateway", text: sentence(err.message), action: null };
        case "ai_not_ready":
            return { title: "Set up the AI gateway first", text: sentence(err.message), action: "settings" };
        case "ai_data_kept_in":
            return {
                title: "Review text stays in this store",
                text: "A summary sends the reviews' words to your AI provider, never names or emails. Switch on Share message bodies in the AI gateway's settings to allow it.",
                action: "settings",
            };
        case "ai_budget_exhausted":
            return {
                title: "This month's AI cap is used up",
                text: "Raise the monthly cap in the AI gateway's settings, or wait for next month.",
                action: "settings",
            };
        case "ai_credit_exhausted":
            return { title: "Out of AI credit", text: sentence(err.message), action: "credit" };
        case "ai_failed":
            return {
                title: "The summary could not be made",
                text: "The AI provider did not give a usable answer. Try again in a moment; the attempt is listed under AI usage.",
                action: "retry",
            };
    }
    return { title: "The summary could not be made", text: sentence(err?.message) || "Try again in a moment.", action: "retry" };
}

/** The rating line, from every published review. */
export function ratingLine(digest) {
    const { count = 0, average = 0 } = digest?.rating ?? {};
    if (!count) return "No published reviews";
    return `${average.toFixed(1)} out of 5 from ${reviews(count)}`;
}

/** What the summary read, or why it read nothing. "" when it read them all. */
export function readLine(digest) {
    const count = digest?.rating?.count ?? 0;
    if (digest?.state === "too_few") {
        return `A summary needs at least 3 published reviews, and this product has ${count}. Nothing was sent to the AI provider.`;
    }
    if (digest && digest.read < count) return `Read the newest ${digest.read} of ${reviews(count)}.`;
    return "";
}

/** What the run cost, or that it cost nothing. */
export function costLine(digest) {
    if (!digest || digest.outcome === "deterministic") return "";
    if (digest.outcome === "cached") return "From an earlier summary of the same reviews, at no cost.";
    return `This summary cost ${formatMicro(digest.cost_micro_minor, digest.currency)}.`;
}

/** The reviews a theme cites, in the order the engine listed them. */
export function citedReviews(theme, list) {
    const byID = new Map((list ?? []).map((r) => [r.id, r]));
    return (theme?.review_ids ?? []).map((id) => byID.get(id)).filter(Boolean);
}

export const themeCount = (theme) => reviews(theme?.count ?? 0);
