/**
 * Reporting the panel's own crashes to the store's error page (D86).
 *
 * A screen that throws while an operator is using it is a bug nobody hears
 * about: the operator sees a blank card, reloads, and moves on. So every
 * uncaught error and unhandled rejection is sent to POST /api/admin/errors,
 * where the owner reads it beside the server's own failures.
 *
 * Three things keep it from being noise: what a browser extension threw is
 * not the panel's; ResizeObserver's loop warning is a browser's, harmless and
 * frequent; and a page sends the same crash once and at most ten in all, so a
 * render loop that throws on every frame does not fill the page with itself.
 * Sending can fail — offline, signed out — and that is swallowed: the reporter
 * must never be the thing that breaks the panel.
 */

const NOISE = [/ResizeObserver loop/i, /^Script error\.?$/i];
const MAX_PER_PAGE = 10;

/** reportFor is the report for an error or rejection event, or null. */
export function reportFor(event, url) {
    const error = event?.error ?? event?.reason;
    let kind = "Error";
    let message = "";
    let stack = "";
    if (error instanceof Error || (error && typeof error === "object" && "message" in error)) {
        kind = error.name || "Error";
        message = String(error.message ?? "");
        stack = String(error.stack ?? "");
    } else if (error !== undefined && error !== null) {
        kind = "UnhandledRejection";
        message = typeof error === "string" ? error : safeString(error);
    } else {
        message = String(event?.message ?? "");
    }
    if (!message.trim()) return null;
    // A request the engine refused or failed is not the panel crashing: a 500
    // is already the server's issue, and a 403 or a 404 is an answer.
    if (kind === "ApiError") return null;
    if (NOISE.some((re) => re.test(message))) return null;
    if (/(chrome|moz|safari)-extension:\/\//.test(stack)) return null;
    return { kind, message, stack, url };
}

function safeString(v) {
    try {
        return JSON.stringify(v);
    } catch {
        return String(v);
    }
}

/** createReporter sends each new crash once, at most MAX_PER_PAGE a page. */
export function createReporter(send) {
    const seen = new Set();
    return {
        handle(event, url) {
            const report = reportFor(event, url);
            if (!report || seen.size >= MAX_PER_PAGE) return;
            const key = report.kind + "\n" + report.message + "\n" + report.stack.split("\n")[1];
            if (seen.has(key)) return;
            seen.add(key);
            try {
                const sent = send(report);
                sent?.catch?.(() => {});
            } catch {
                /* the reporter must never be what breaks the panel */
            }
        },
    };
}

/**
 * installErrorReporting listens on window and returns the function that stops
 * it. `send` posts one report.
 */
export function installErrorReporting(send) {
    const reporter = createReporter(send);
    const here = () => window.location.pathname + window.location.search;
    const onError = (e) => reporter.handle(e, here());
    const onRejection = (e) => reporter.handle(e, here());
    window.addEventListener("error", onError);
    window.addEventListener("unhandledrejection", onRejection);
    return () => {
        window.removeEventListener("error", onError);
        window.removeEventListener("unhandledrejection", onRejection);
    };
}
