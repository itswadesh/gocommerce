/**
 * A list screen's filters, search and page live in the URL.
 *
 * Not as a preference: it is the only place they can live and still be right.
 * Component-local state does not survive opening a record and pressing Back, a
 * second click on a dashboard card that links to `?status=pending` changes
 * nothing while the screen is already mounted, and a filtered list with no
 * filter in its address cannot be sent to anybody.
 *
 * Writes use `replaceState`, so one screen leaves one history entry and Back
 * leaves the list rather than walking backwards through every keystroke.
 *
 * MUST be called at the top level of a component's `<script>`, never from an
 * event handler: `$derived.by` needs the component's init context and throws
 * outside it.
 *
 * The canonical usage, which every list screen copies:
 *
 *     const list = listState({ q: "", status: "", page: 1 });
 *     let draftSearch = $state(list.params.q);
 *     $effect(() => {
 *         list.params;          // the dependency: any change reloads
 *         load();
 *     });
 *
 * and `load()` assigns `rows = result.data` — a window, never
 * `[...rows, ...result.data]`. An accumulator holding pages 1-4 cannot honestly
 * write `page=4` in the URL, and restoring that URL would show a quarter of the
 * rows the operator had.
 */

import { goto } from "$app/navigation";
import { page } from "$app/state";
import { query as buildQuery } from "$lib/api.js";

/**
 * listState reads and writes one screen's parameters.
 *
 * `defaults` is every parameter the screen owns, with the value that means
 * unset — `{ q: "", status: "", page: 1 }`. The default's JS type is what the
 * URL string is parsed back to, so a screen never has to say so twice.
 */
export function listState(defaults = {}, options = {}) {
    const { replace = true } = options;
    const keys = Object.keys(defaults);

    const parse = (key, raw) => {
        const fallback = defaults[key];
        if (raw === null) return fallback;
        if (typeof fallback === "number") {
            const n = parseInt(raw, 10);
            // A page of 0, -3 or "banana" is a typed or truncated URL, not a
            // request; the honest answer is the default rather than a request
            // the engine will refuse with "pages start at 1".
            return Number.isFinite(n) && n >= 1 ? n : fallback;
        }
        if (typeof fallback === "boolean") return raw === "1" || raw === "true";
        return raw;
    };

    const params = $derived.by(() => {
        const search = page.url.searchParams;
        const out = {};
        for (const key of keys) out[key] = parse(key, search.get(key));
        return out;
    });

    /*
     * Only the keys this screen declared are touched. Anything else already on
     * the URL — a deep link a module added, a tracking parameter — is left
     * where it was rather than being quietly dropped by a filter change.
     */
    function push(next) {
        const url = new URL(page.url);
        for (const key of keys) {
            const value = next[key];
            if (value === undefined || value === null || value === "" || value === defaults[key]) {
                url.searchParams.delete(key);
            } else {
                url.searchParams.set(key, String(value));
            }
        }
        goto(url, { replaceState: replace, keepFocus: true, noScroll: true });
    }

    return {
        get params() {
            return params;
        },

        /** The current page, or 1 on a screen that does not page. */
        get page() {
            return typeof params.page === "number" ? params.page : 1;
        },

        /** True while nothing is filtered — for an empty state that says so. */
        get pristine() {
            return keys.every((key) => params[key] === defaults[key]);
        },

        /**
         * set applies a patch and returns to page 1.
         *
         * Always, and not as a convenience: page 7 of an unfiltered list is
         * page 7 of nothing once a filter cuts the result to twelve rows, and
         * the operator would be looking at an empty table they did not ask for.
         */
        set(patch) {
            const next = { ...params, ...patch };
            if ("page" in defaults) next.page = defaults.page;
            push(next);
        },

        /** setPage is the only thing that moves the page. */
        setPage(n) {
            push({ ...params, page: n });
        },

        clear() {
            push({ ...defaults });
        },

        /**
         * query builds the query string for the API call, reusing api.js's own
         * builder so empty values drop the same way they always have:
         *
         *     api.get("/api/admin/products" + list.query({ limit: PER_PAGE }))
         */
        query(extra) {
            const out = {};
            for (const key of keys) {
                if (params[key] !== defaults[key]) out[key] = params[key];
            }
            return buildQuery({ ...out, ...extra });
        },
    };
}
